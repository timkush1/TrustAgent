"""Scorer node tests, focused on CONTRADICTED handling and faithfulness scoring.

These are deterministic (no LLM): they lock in that a CONTRADICTED verdict
scores 0.0, is treated as a hallucination, and is surfaced in the trace — the
end-to-end wiring that a local model can't be relied on to exercise.
"""

import pytest

from truthtable.graphs.nodes.scorer import (
    NO_CLAIMS_TRACE,
    AuditAssessment,
    ScorerNode,
    assess_audit,
    calculate_faithfulness_score,
    detect_hallucination,
    generate_reasoning_trace,
)
from truthtable.graphs.state import (
    AuditOutcome,
    AuditVerdict,
    UnsupportedPolicy,
    VerificationStatus,
)

S = VerificationStatus.SUPPORTED
C = VerificationStatus.CONTRADICTED
U = VerificationStatus.UNSUPPORTED
P = VerificationStatus.PARTIALLY_SUPPORTED
K = VerificationStatus.UNKNOWN
N = VerificationStatus.NOT_CHECKED


def _v(status: VerificationStatus, confidence: float = 0.9, claim: str = "a claim"):
    return {"claim": claim, "status": status, "confidence": confidence, "evidence": []}


def test_supported_scores_one():
    assert calculate_faithfulness_score([_v(VerificationStatus.SUPPORTED)]) == 1.0


def test_contradicted_scores_zero():
    assert calculate_faithfulness_score([_v(VerificationStatus.CONTRADICTED)]) == 0.0


def test_unsupported_scores_zero():
    assert calculate_faithfulness_score([_v(VerificationStatus.UNSUPPORTED)]) == 0.0


def test_high_confidence_contradicted_is_hallucination():
    assert detect_hallucination([_v(VerificationStatus.CONTRADICTED, 0.95)]) is True


def test_any_contradicted_claim_flags_hallucination_via_ratio():
    # A single contradicted claim is 100% problematic (> 30%) even at low confidence.
    assert detect_hallucination([_v(VerificationStatus.CONTRADICTED, 0.4)]) is True


def test_all_supported_is_not_hallucination():
    verifications = [_v(VerificationStatus.SUPPORTED), _v(VerificationStatus.SUPPORTED)]
    assert detect_hallucination(verifications) is False


def test_reasoning_trace_counts_and_labels_contradicted():
    trace = generate_reasoning_trace(
        [
            _v(VerificationStatus.CONTRADICTED, claim="X was invented in 1850"),
            _v(VerificationStatus.SUPPORTED),
        ],
        0.5,
    )
    assert "Contradicted: 1" in trace
    assert "[contradicted]" in trace  # problematic-claims section uses this label


def test_mixed_batch_weighted_score():
    # SUPPORTED(1.0) + CONTRADICTED(0.0) at equal confidence -> 0.5
    score = calculate_faithfulness_score(
        [_v(VerificationStatus.SUPPORTED, 0.9), _v(VerificationStatus.CONTRADICTED, 0.9)]
    )
    assert abs(score - 0.5) < 1e-6


# ---------------------------------------------------------------------------
# Zero claims (D1) and the audit classification (SPEC section 2.2)
# ---------------------------------------------------------------------------


def test_reasoning_trace_without_claims_does_not_divide_by_zero():
    # D1: generate_reasoning_trace([]) used to raise ZeroDivisionError.
    assert generate_reasoning_trace([], 0.0) == NO_CLAIMS_TRACE


def test_reasoning_trace_counts_not_checked_claims():
    trace = generate_reasoning_trace([_v(S), _v(N)], 0.5)
    assert "Not checked: 1" in trace


async def test_scorer_node_scores_zero_claims_as_no_claims():
    state = {"request_id": "r", "claim_verifications": [], "step_timings": {}}

    state = await ScorerNode().run(state)

    assert state["verdict"] == "NO_CLAIMS"
    assert state["audit_status"] == "completed"
    assert state["faithfulness_score"] == 0.0
    assert state["hallucination_detected"] is False
    assert state["reasoning_trace"] == NO_CLAIMS_TRACE
    assert state["degraded_reasons"] == []
    assert state["error_kind"] is None


async def test_scorer_node_classifies_verified_claims():
    state = {
        "request_id": "r",
        "claim_verifications": [_v(C, 0.95, claim="Refunds last 60 days")],
        "unsupported_policy": "hallucination",
        "step_timings": {},
    }

    state = await ScorerNode().run(state)

    assert state["verdict"] == "HALLUCINATED"
    assert state["audit_status"] == "completed"
    assert state["faithfulness_score"] == 0.0


@pytest.mark.parametrize(
    "statuses, policy, expected",
    [
        ([], "hallucination", AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.NO_CLAIMS)),
        (
            [N, N],
            "hallucination",
            AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.NOT_ASSESSED),
        ),
        ([S, S], "hallucination", AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.GROUNDED)),
        (
            [S, C],
            "hallucination",
            AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.HALLUCINATED),
        ),
        (
            [S, U],
            "hallucination",
            AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.HALLUCINATED),
        ),
        # Under the unverifiable policy an unsupported claim is unchecked, not wrong...
        (
            [S, U],
            "unverifiable",
            AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.UNVERIFIABLE),
        ),
        # ...but a contradicted one is wrong under either policy.
        (
            [U, C],
            "unverifiable",
            AuditAssessment(AuditOutcome.COMPLETED, AuditVerdict.HALLUCINATED),
        ),
        # UNKNOWN below the threshold: DEGRADED, verdict from the decided claims only.
        (
            [S, S, K],
            "hallucination",
            AuditAssessment(AuditOutcome.DEGRADED, AuditVerdict.GROUNDED, ("unknown_claims",)),
        ),
        # UNKNOWN at the threshold (1 of 2): FAILED, judge_unavailable, no verdict.
        (
            [S, K],
            "hallucination",
            AuditAssessment(AuditOutcome.FAILED, None, error_kind="judge_unavailable"),
        ),
        # NOT_CHECKED claims are left out of the fraction: 1 of 2 checked is UNKNOWN.
        (
            [S, K, N],
            "hallucination",
            AuditAssessment(AuditOutcome.FAILED, None, error_kind="judge_unavailable"),
        ),
        (
            [S, S, S, P, K, N],
            "hallucination",
            AuditAssessment(AuditOutcome.DEGRADED, AuditVerdict.GROUNDED, ("unknown_claims",)),
        ),
    ],
)
def test_assess_audit(statuses, policy, expected):
    verifications = [_v(status) for status in statuses]

    assert assess_audit(verifications, UnsupportedPolicy(policy)) == expected


def test_assess_audit_treats_an_unrecognised_status_as_unknown():
    verifications = [_v(S), {"claim": "x", "status": "bogus", "confidence": 0.5, "evidence": []}]

    assessment = assess_audit(verifications)

    assert assessment.status is AuditOutcome.FAILED
    assert assessment.error_kind == "judge_unavailable"


def test_unknown_claims_do_not_dilute_the_verdict():
    # The v1 ratio rule runs over the decided claims only: one low-confidence
    # contradiction among three decided claims (33%) is a hallucination, even
    # though it is only 20% of all five claims.
    verifications = [_v(C, 0.5), _v(S), _v(S), _v(K), _v(K)]

    assert detect_hallucination(verifications) is False  # the v1 view, over all claims
    assessment = assess_audit(verifications)

    assert assessment.verdict is AuditVerdict.HALLUCINATED
    assert assessment.status is AuditOutcome.DEGRADED  # 2 of 5 UNKNOWN, below the 0.5 cutoff
