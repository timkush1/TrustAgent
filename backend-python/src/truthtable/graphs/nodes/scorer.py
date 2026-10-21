"""
Score Calculator Node

This node turns the claim verifications into the audit's result: scores,
verdict, status and a human-readable reasoning trace. The arithmetic is
scoring v2 (scoring/aggregate.py; SPEC section 3.2): each decided claim is a
distribution over the four verdicts, CONTRADICTED counts as wrong and
UNSUPPORTED as unverified or baseless depending on the unsupported policy,
UNKNOWN and NOT_CHECKED claims are left out, and a score that cannot be
computed is absent rather than invented.

The status follows SPEC section 2.2: FAILED (judge_unavailable, no scores)
when UNKNOWN claims reach half of the checked claims; DEGRADED when some claim
is UNKNOWN or an earlier step recorded a degraded reason (decomposer_fallback,
context_trimmed, judge_retry_exhausted); COMPLETED otherwise.

The legacy `faithfulness_score` carries the trust score (0.0 when absent), and
`hallucination_detected` is whether the verdict is HALLUCINATED.
"""

import logging
import time
from typing import List, Mapping, Optional, Sequence

from ...scoring.aggregate import (
    DEFAULT_SCORING,
    SCORING_VERSION,
    AuditSummary,
    ClaimScoreInput,
    ScoringConfig,
    aggregate,
    resolve_unsupported_policy,
)
from ..state import (
    AuditState,
    AuditVerdict,
    ClaimVerification,
    NotAssessedReason,
    UnsupportedPolicy,
    VerificationStatus,
    coerce_status,
)

logger = logging.getLogger(__name__)

NO_CLAIMS_TRACE = "No verifiable claims were found in the response."

# Claims listed under "Problematic claims" in the reasoning trace.
MAX_TRACE_CLAIMS = 5

_STATUS_LINES = (
    (VerificationStatus.SUPPORTED, "✓ Supported"),
    (VerificationStatus.PARTIALLY_SUPPORTED, "⚠ Partially supported"),
    (VerificationStatus.CONTRADICTED, "✕ Contradicted"),
    (VerificationStatus.UNSUPPORTED, "✗ Unsupported"),
    (VerificationStatus.UNKNOWN, "? Unknown"),
    (VerificationStatus.NOT_CHECKED, "- Not checked"),
)


def effective_policy(state: Mapping[str, object]) -> UnsupportedPolicy:
    """
    The unsupported policy to score with: the resolved request policy
    (preflight resolves "" and auto; a state that skipped preflight is resolved
    here from its evidence sources), switched to `unverifiable` when the
    verifier capped the evidence (context_trimmed), because the support of an
    UNSUPPORTED claim may be in the part the judge did not see.
    """
    requested = state.get("unsupported_policy")
    sources_raw = state.get("context_sources")
    sources: Sequence[str] = (
        [str(source) for source in sources_raw] if isinstance(sources_raw, list) else []
    )
    documents = state.get("context_docs")
    count = len(documents) if isinstance(documents, list) else 0
    padded = [sources[i] if i < len(sources) else "" for i in range(count)]
    policy = resolve_unsupported_policy(str(requested or ""), padded)
    if state.get("context_trimmed"):
        return UnsupportedPolicy.UNVERIFIABLE
    return policy


def _fmt(value: Optional[float]) -> str:
    return "n/a" if value is None else f"{value:.2f}"


def generate_reasoning_trace(
    verifications: Sequence[ClaimVerification], summary: Optional[AuditSummary] = None
) -> str:
    """
    Explain the audit: verdict and scores, claim counts, and the problematic
    claims (contradicted first). Safe without claims (D1).
    """
    total = len(verifications)
    if total == 0:
        return NO_CLAIMS_TRACE

    counts = {status: 0 for status in VerificationStatus}
    for verification in verifications:
        counts[coerce_status(verification["status"])] += 1

    lines: List[str] = []
    if summary is not None:
        if summary.verdict is None:
            lines.append(
                f"Audit failed ({summary.error_kind}): the judge could not decide "
                f"{counts[VerificationStatus.UNKNOWN]} of {total} claims."
            )
        else:
            lines.append(f"Verdict: {summary.verdict.value} ({summary.policy.value} policy)")
        scores = summary.scores
        if scores is not None and scores.faithfulness is not None:
            lines.append(
                f"Trust score: {_fmt(scores.trust_score)}  "
                f"(faithfulness {_fmt(scores.faithfulness)}, "
                f"contradiction {_fmt(scores.contradiction_rate)}, "
                f"coverage {_fmt(scores.coverage)})"
            )
        if summary.degraded_reasons:
            lines.append(f"Degraded: {', '.join(summary.degraded_reasons)}")
        lines.append("")

    lines.append(f"Total Claims Analyzed: {total}")
    for status, label in _STATUS_LINES:
        count = counts[status]
        if count or status is not VerificationStatus.NOT_CHECKED:
            lines.append(f"  {label}: {count} ({count / total * 100:.1f}%)")
    lines.append("")

    # Contradicted claims first: the strongest signal.
    problems = [
        v for v in verifications if coerce_status(v["status"]) is VerificationStatus.CONTRADICTED
    ] + [v for v in verifications if coerce_status(v["status"]) is VerificationStatus.UNSUPPORTED]
    if problems:
        lines.append("Problematic Claims:")
        for i, verification in enumerate(problems[:MAX_TRACE_CLAIMS], 1):
            label = coerce_status(verification["status"]).value
            lines.append(f"  {i}. [{label}] {verification['claim'][:100]}")
        if len(problems) > MAX_TRACE_CLAIMS:
            lines.append(f"  ... and {len(problems) - MAX_TRACE_CLAIMS} more")
        lines.append("")

    return "\n".join(lines).rstrip()


def score_audit(
    verifications: Sequence[ClaimVerification],
    policy: UnsupportedPolicy,
    config: ScoringConfig = DEFAULT_SCORING,
    degraded_reasons: Sequence[str] = (),
) -> AuditSummary:
    """Scoring v2 over claim verification records (scoring.aggregate)."""
    return aggregate(
        [ClaimScoreInput.from_verification(verification) for verification in verifications],
        policy,
        config,
        degraded_reasons,
    )


class ScorerNode:
    """
    LangGraph node for score calculation.

    This node:
    1. Takes claim_verifications from state
    2. Scores them (scoring v2)
    3. Sets the verdict, status and degraded reasons
    4. Generates the reasoning trace

    Usage in graph:
        node = ScorerNode()
        graph.add_node("score", node.run)
    """

    def __init__(self, config: ScoringConfig = DEFAULT_SCORING):
        """
        Args:
            config: Scoring thresholds (ScoringConfig.from_settings for the
                TRUTHTABLE_SCORING_* keys; the registry defaults otherwise)
        """
        self.config = config

    async def run(self, state: AuditState) -> AuditState:
        """
        Execute score calculation.

        Args:
            state: Current graph state

        Returns:
            Updated state with final scores and reasoning
        """
        start = time.time()
        logger.info(f"Calculating scores for request {state['request_id']}")

        verifications = state.get("claim_verifications") or []
        policy = effective_policy(state)
        summary = score_audit(
            verifications, policy, self.config, state.get("degraded_reasons") or []
        )

        state["unsupported_policy"] = summary.policy.value
        state["scores"] = summary.scores.to_dict() if summary.scores is not None else None
        state["grade"] = summary.grade
        state["scoring_version"] = SCORING_VERSION
        state["faithfulness_score"] = summary.legacy_score
        state["hallucination_detected"] = summary.hallucination_detected
        state["reasoning_trace"] = generate_reasoning_trace(verifications, summary)
        state["audit_status"] = summary.status.value
        state["verdict"] = summary.verdict.value if summary.verdict is not None else None
        state["degraded_reasons"] = list(summary.degraded_reasons)
        state["error_kind"] = summary.error_kind
        if summary.verdict is AuditVerdict.NOT_ASSESSED:
            state["not_assessed_reason"] = NotAssessedReason.NO_EVIDENCE.value

        elapsed_ms = int((time.time() - start) * 1000)
        state.setdefault("step_timings", {})
        timings = state["step_timings"]
        if timings is not None:
            timings["score_ms"] = elapsed_ms

        logger.info(
            f"Audit scored: verdict={state['verdict']}, status={summary.status.value}, "
            f"trust={_fmt(summary.trust_score)}, policy={summary.policy.value} ({elapsed_ms}ms)"
        )

        return state
