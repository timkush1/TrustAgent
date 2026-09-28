"""
Score Calculator Node

This node aggregates claim verification results into a final faithfulness score.

The faithfulness score indicates how much of the LLM's response is supported
by the provided context. A score of 1.0 means fully faithful, 0.0 means
completely unsupported (hallucination).

It also classifies the audit for the v2 wire result (SPEC section 2.2): the
terminal status (COMPLETED, DEGRADED or FAILED) and the verdict (GROUNDED,
HALLUCINATED, UNVERIFIABLE or NO_CLAIMS). The scores themselves are still the
v1 formula; scoring v2 (SPEC section 3.2, scoring/aggregate.py) replaces them.
"""

import logging
from dataclasses import dataclass
from typing import List, Optional, Sequence, Tuple

from ..state import (
    DECIDED_STATUSES,
    AuditOutcome,
    AuditState,
    AuditVerdict,
    ClaimVerification,
    NotAssessedReason,
    UnsupportedPolicy,
    VerificationStatus,
    coerce_status,
)

logger = logging.getLogger(__name__)

# An audit whose UNKNOWN share of the checked claims reaches this fraction FAILS
# with error_kind judge_unavailable instead of reporting a score built on judge
# failures (SPEC section 2.2; the TRUTHTABLE_SCORING_MAX_UNKNOWN_FRACTION default).
MAX_UNKNOWN_FRACTION = 0.5

NO_CLAIMS_TRACE = "No verifiable claims were found in the response."


def calculate_faithfulness_score(verifications: List[ClaimVerification]) -> float:
    """
    Calculate faithfulness score from claim verifications.

    Scoring logic:
    - SUPPORTED claim: 1.0 points
    - PARTIALLY_SUPPORTED: 0.5 points
    - CONTRADICTED: 0.0 points (context disproves the claim)
    - UNSUPPORTED: 0.0 points (no evidence either way)
    - UNKNOWN: 0.3 points (conservative estimate)

    Final score is the weighted average, taking into account confidence levels.

    Args:
        verifications: List of claim verifications

    Returns:
        Faithfulness score between 0.0 and 1.0
    """
    if not verifications:
        logger.warning("No verifications to score")
        return 0.0

    # Assign base scores
    score_map = {
        VerificationStatus.SUPPORTED: 1.0,
        VerificationStatus.PARTIALLY_SUPPORTED: 0.5,
        VerificationStatus.CONTRADICTED: 0.0,
        VerificationStatus.UNSUPPORTED: 0.0,
        VerificationStatus.UNKNOWN: 0.3,
    }

    # Calculate weighted score
    total_weight = 0.0
    weighted_sum = 0.0

    for verification in verifications:
        status = verification["status"]
        confidence = verification["confidence"]

        # Weight by confidence (claims we're more confident about count more)
        weight = max(confidence, 0.1)  # Minimum weight of 0.1
        base_score = score_map.get(status, 0.0)

        weighted_sum += base_score * weight
        total_weight += weight

    # Compute average
    if total_weight == 0:
        return 0.0

    final_score = weighted_sum / total_weight

    logger.debug(
        f"Calculated faithfulness score: {final_score:.3f} " f"from {len(verifications)} claims"
    )

    return final_score


def detect_hallucination(verifications: List[ClaimVerification]) -> bool:
    """
    Determine if hallucination is detected.

    We consider it a hallucination if:
    - Any claim is CONTRADICTED or UNSUPPORTED with high confidence (>0.7), OR
    - More than 30% of claims are CONTRADICTED, UNSUPPORTED or PARTIALLY_SUPPORTED

    Args:
        verifications: List of claim verifications

    Returns:
        True if hallucination detected
    """
    if not verifications:
        return False

    # A confident CONTRADICTED or UNSUPPORTED claim is a hallucination. (A
    # CONTRADICTED claim is the strongest signal: the context disproves it.)
    for verification in verifications:
        if (
            verification["status"]
            in (VerificationStatus.CONTRADICTED, VerificationStatus.UNSUPPORTED)
            and verification["confidence"] > 0.7
        ):
            logger.info(
                f"Hallucination detected: high-confidence {verification['status'].value} claim: "
                f"{verification['claim'][:50]}..."
            )
            return True

    # Check percentage of problematic claims
    problematic = sum(
        1
        for v in verifications
        if v["status"]
        in [
            VerificationStatus.CONTRADICTED,
            VerificationStatus.UNSUPPORTED,
            VerificationStatus.PARTIALLY_SUPPORTED,
        ]
    )

    problematic_ratio = problematic / len(verifications)

    if problematic_ratio > 0.3:
        logger.info(
            f"Hallucination detected: {problematic_ratio:.1%} of claims "
            f"are unsupported or partially supported"
        )
        return True

    return False


def generate_reasoning_trace(verifications: List[ClaimVerification], score: float) -> str:
    """
    Generate human-readable explanation of the audit results.

    Args:
        verifications: List of claim verifications
        score: Calculated faithfulness score

    Returns:
        Reasoning trace as formatted string
    """
    # Count by status (every VerificationStatus must be a key here, or the
    # increment below raises KeyError for that status).
    total = len(verifications)
    if total == 0:
        # A response without claims has nothing to break down (and nothing to
        # divide by: this used to raise ZeroDivisionError, D1).
        return NO_CLAIMS_TRACE

    status_counts = {status: 0 for status in VerificationStatus}
    for v in verifications:
        status_counts[coerce_status(v["status"])] += 1

    # Build trace
    trace_lines = [
        f"Faithfulness Score: {score:.2f}/1.00",
        "",
        f"Total Claims Analyzed: {total}",
        f"  ✓ Supported: {status_counts[VerificationStatus.SUPPORTED]} ({status_counts[VerificationStatus.SUPPORTED]/total*100:.1f}%)",
        f"  ⚠ Partially Supported: {status_counts[VerificationStatus.PARTIALLY_SUPPORTED]} ({status_counts[VerificationStatus.PARTIALLY_SUPPORTED]/total*100:.1f}%)",
        f"  ✕ Contradicted: {status_counts[VerificationStatus.CONTRADICTED]} ({status_counts[VerificationStatus.CONTRADICTED]/total*100:.1f}%)",
        f"  ✗ Unsupported: {status_counts[VerificationStatus.UNSUPPORTED]} ({status_counts[VerificationStatus.UNSUPPORTED]/total*100:.1f}%)",
        f"  ? Unknown: {status_counts[VerificationStatus.UNKNOWN]} ({status_counts[VerificationStatus.UNKNOWN]/total*100:.1f}%)",
    ]
    not_checked = status_counts[VerificationStatus.NOT_CHECKED]
    if not_checked:
        trace_lines.append(f"  - Not checked: {not_checked} ({not_checked/total*100:.1f}%)")
    trace_lines.append("")

    # Add details for problematic claims (contradicted first — strongest signal)
    problem_claims = [
        v
        for v in verifications
        if v["status"] in (VerificationStatus.CONTRADICTED, VerificationStatus.UNSUPPORTED)
    ]
    if problem_claims:
        trace_lines.append("Problematic Claims:")
        for i, v in enumerate(problem_claims[:5], 1):  # Show max 5
            label = (
                "contradicted" if v["status"] == VerificationStatus.CONTRADICTED else "unsupported"
            )
            trace_lines.append(f"  {i}. [{label}] {v['claim'][:100]}...")

        if len(problem_claims) > 5:
            trace_lines.append(f"  ... and {len(problem_claims) - 5} more")
        trace_lines.append("")

    return "\n".join(trace_lines)


@dataclass(frozen=True)
class AuditAssessment:
    """How an audit ends: its terminal status and, unless it failed, its verdict."""

    status: AuditOutcome
    verdict: Optional[AuditVerdict]
    degraded_reasons: Tuple[str, ...] = ()
    error_kind: Optional[str] = None


def assess_audit(
    verifications: Sequence[ClaimVerification],
    unsupported_policy: UnsupportedPolicy = UnsupportedPolicy.HALLUCINATION,
    max_unknown_fraction: float = MAX_UNKNOWN_FRACTION,
) -> AuditAssessment:
    """
    Classify a verified audit (SPEC section 2.2 status rules).

    - No claims: COMPLETED, NO_CLAIMS.
    - Only NOT_CHECKED claims: COMPLETED, NOT_ASSESSED.
    - UNKNOWN claims reach ``max_unknown_fraction`` of the checked claims:
      FAILED with error_kind judge_unavailable and no verdict.
    - Otherwise the verdict comes from the decided claims only (UNKNOWN and
      NOT_CHECKED are excluded): GROUNDED unless the v1 hallucination rule fires.
      When it fires, HALLUCINATED, except UNVERIFIABLE under the ``unverifiable``
      policy when no claim is CONTRADICTED (the answer is unchecked, not wrong).
      Any UNKNOWN claim makes the audit DEGRADED (unknown_claims).
    """
    if not verifications:
        return AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.NO_CLAIMS)

    statuses = [coerce_status(v["status"]) for v in verifications]
    checked = [s for s in statuses if s is not VerificationStatus.NOT_CHECKED]
    if not checked:
        return AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.NOT_ASSESSED)

    unknown = sum(1 for s in checked if s not in DECIDED_STATUSES)
    if unknown / len(checked) >= max_unknown_fraction:
        return AuditAssessment(AuditOutcome.FAILED, None, error_kind="judge_unavailable")

    decided = [v for v, s in zip(verifications, statuses) if s in DECIDED_STATUSES]
    if not detect_hallucination(decided):
        verdict = AuditVerdict.GROUNDED
    elif unsupported_policy is UnsupportedPolicy.UNVERIFIABLE and not any(
        s is VerificationStatus.CONTRADICTED for s in statuses
    ):
        verdict = AuditVerdict.UNVERIFIABLE
    else:
        verdict = AuditVerdict.HALLUCINATED

    if unknown:
        return AuditAssessment(AuditOutcome.DEGRADED, verdict, degraded_reasons=("unknown_claims",))
    return AuditAssessment(AuditOutcome.COMPLETED, verdict)


class ScorerNode:
    """
    LangGraph node for score calculation.

    This node:
    1. Takes claim_verifications from state
    2. Calculates faithfulness score
    3. Detects hallucinations
    4. Generates reasoning trace
    5. Updates state with final results

    Usage in graph:
        node = ScorerNode()
        graph.add_node("score", node.run)
    """

    async def run(self, state: AuditState) -> AuditState:
        """
        Execute score calculation.

        Args:
            state: Current graph state

        Returns:
            Updated state with final scores and reasoning
        """
        import time

        start = time.time()
        logger.info(f"Calculating scores for request {state['request_id']}")

        verifications = state.get("claim_verifications") or []

        # Calculate faithfulness score (0.0 when there are no claims)
        score = calculate_faithfulness_score(verifications)

        # Detect hallucinations
        hallucination = detect_hallucination(verifications)

        # Generate reasoning trace
        reasoning = generate_reasoning_trace(verifications, score)

        # Classify the audit for the v2 result
        policy = UnsupportedPolicy(
            state.get("unsupported_policy") or UnsupportedPolicy.HALLUCINATION.value
        )
        assessment = assess_audit(verifications, policy)

        # Update state
        state["faithfulness_score"] = score
        state["hallucination_detected"] = hallucination
        state["reasoning_trace"] = reasoning
        state["audit_status"] = assessment.status.value
        state["verdict"] = assessment.verdict.value if assessment.verdict else None
        state["degraded_reasons"] = list(assessment.degraded_reasons)
        state["error_kind"] = assessment.error_kind
        if assessment.verdict is AuditVerdict.NOT_ASSESSED:
            state["not_assessed_reason"] = NotAssessedReason.NO_EVIDENCE.value

        elapsed_ms = int((time.time() - start) * 1000)
        state.setdefault("step_timings", {})
        state["step_timings"]["score_ms"] = elapsed_ms

        logger.info(
            f"Audit complete: score={score:.3f}, hallucination={hallucination}, "
            f"status={assessment.status.value}, verdict={state['verdict']} ({elapsed_ms}ms)"
        )

        return state
