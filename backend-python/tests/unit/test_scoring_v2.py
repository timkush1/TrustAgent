"""
Scoring v2 (scoring/aggregate.py; SPEC sections 2.2 and 3.2).

Table-driven over claim-verdict mixes: the verdict, status, degraded reasons
and every score, including the cases the v1 scorer got wrong (D1 zero claims,
D3 CONTRADICTED vs UNSUPPORTED, D4 judge outage, D5 one contradiction among
supported claims).
"""

from typing import Any, Dict, Optional, Sequence, Tuple

import pytest

from truthtable.config import Settings
from truthtable.graphs.state import (
    AuditOutcome,
    AuditVerdict,
    UnsupportedPolicy,
    VerificationStatus,
)
from truthtable.scoring import (
    DEFAULT_SCORING,
    AuditScores,
    ClaimScoreInput,
    ScoringConfig,
    aggregate,
    grade_for,
    llm_distribution,
    resolve_unsupported_policy,
)
from truthtable.scoring.aggregate import (
    PROBABILITY_KEYS,
    claim_components,
    normalized_distribution,
    ordered_reasons,
)

S = VerificationStatus.SUPPORTED
P = VerificationStatus.PARTIALLY_SUPPORTED
C = VerificationStatus.CONTRADICTED
U = VerificationStatus.UNSUPPORTED
K = VerificationStatus.UNKNOWN
N = VerificationStatus.NOT_CHECKED

HALLUCINATION = UnsupportedPolicy.HALLUCINATION
UNVERIFIABLE = UnsupportedPolicy.UNVERIFIABLE

GROUNDED = AuditVerdict.GROUNDED
HALLUCINATED = AuditVerdict.HALLUCINATED
UNVERIFIABLE_VERDICT = AuditVerdict.UNVERIFIABLE
NO_CLAIMS = AuditVerdict.NO_CLAIMS
NOT_ASSESSED = AuditVerdict.NOT_ASSESSED

COMPLETED, DEGRADED, FAILED = AuditOutcome.COMPLETED, AuditOutcome.DEGRADED, AuditOutcome.FAILED

Claim = Tuple[VerificationStatus, float]


def claims(*items: Claim) -> Sequence[ClaimScoreInput]:
    return [ClaimScoreInput(status, confidence) for status, confidence in items]


# (id, claims, policy, degraded in, status, verdict, reasons, trust_score, grade)
CASES = [
    # D1: an answer without claims is NO_CLAIMS, with no score.
    ("empty", [], HALLUCINATION, (), COMPLETED, NO_CLAIMS, (), None, None),
    # D4: a judge outage is FAILED (no verdict, no score), never ~0.3.
    ("unknown-only", [(K, 0.0), (K, 0.0)], HALLUCINATION, (), FAILED, None, ("unknown_claims",), None, None),
    ("outage-at-threshold", [(S, 0.9), (K, 0.0)], HALLUCINATION, (), FAILED, None, ("unknown_claims",), None, None),
    # NOT_CHECKED claims are left out of the unknown fraction: 1 of 2 checked.
    ("outage-with-not-checked", [(S, 0.9), (K, 0.0), (N, 0.0)], HALLUCINATION, (), FAILED, None, ("unknown_claims",), None, None),
    # Below the threshold: DEGRADED, scored over the decided claims only.
    ("outage-partial", [(S, 0.9), (S, 0.9), (K, 0.0)], HALLUCINATION, (), DEGRADED, GROUNDED, ("unknown_claims",), 0.883333, "B"),
    ("not-checked-only", [(N, 0.0), (N, 0.0)], HALLUCINATION, (), COMPLETED, NOT_ASSESSED, (), None, None),
    ("supported", [(S, 0.9)], HALLUCINATION, (), COMPLETED, GROUNDED, (), 0.883333, "B"),
    ("supported-confident", [(S, 0.99), (S, 0.97)], HALLUCINATION, (), COMPLETED, GROUNDED, (), 0.976667, "A"),
    # D5: one contradiction at 0.6 among three supported claims is flagged.
    ("d5-one-contradiction", [(C, 0.6), (S, 0.9), (S, 0.9), (S, 0.9)], HALLUCINATION, (), COMPLETED, HALLUCINATED, (), 0.5625, "C"),
    # D3: under the unverifiable policy, UNSUPPORTED is unchecked, CONTRADICTED is wrong.
    ("d3-unsupported-unverifiable", [(U, 0.9)], UNVERIFIABLE, (), COMPLETED, UNVERIFIABLE_VERDICT, (), 0.6, "C"),
    ("d3-contradicted-unverifiable", [(C, 0.9)], UNVERIFIABLE, (), COMPLETED, HALLUCINATED, (), 0.052632, "F"),
    # Under the hallucination policy an unsupported claim is baseless.
    ("baseless-claim", [(S, 0.9), (U, 0.9)], HALLUCINATION, (), COMPLETED, HALLUCINATED, (), 0.45, "D"),
    ("baseless-unverifiable", [(S, 0.9), (U, 0.9)], UNVERIFIABLE, (), COMPLETED, GROUNDED, (), 0.935484, "A"),
    # Partial claims: V = 0.5 flags under hallucination, coverage 0.5 is enough under unverifiable.
    ("partials-hallucination", [(P, 1.0), (P, 1.0)], HALLUCINATION, (), COMPLETED, HALLUCINATED, (), 0.5, "C"),
    ("partials-unverifiable", [(P, 1.0), (P, 1.0)], UNVERIFIABLE, (), COMPLETED, GROUNDED, (), 1.0, "A"),
    # Earlier steps' degraded reasons make an audit DEGRADED, in reporting order.
    ("degraded-reasons", [(S, 0.9)], HALLUCINATION, ("context_trimmed", "decomposer_fallback"), DEGRADED, GROUNDED, ("decomposer_fallback", "context_trimmed"), 0.883333, "B"),
    ("no-claims-degraded", [], HALLUCINATION, ("decomposer_fallback",), DEGRADED, NO_CLAIMS, ("decomposer_fallback",), None, None),
]  # fmt: skip


@pytest.mark.parametrize(
    "items, policy, degraded, status, verdict, reasons, trust, grade",
    [case[1:] for case in CASES],
    ids=[case[0] for case in CASES],
)
def test_aggregate_table(
    items: Sequence[Claim],
    policy: UnsupportedPolicy,
    degraded: Tuple[str, ...],
    status: AuditOutcome,
    verdict: Optional[AuditVerdict],
    reasons: Tuple[str, ...],
    trust: Optional[float],
    grade: Optional[str],
) -> None:
    summary = aggregate(claims(*items), policy, DEFAULT_SCORING, degraded)

    assert summary.status is status
    assert summary.verdict is verdict
    assert summary.degraded_reasons == reasons
    assert summary.policy is policy
    if trust is None:
        assert summary.trust_score is None
    else:
        assert summary.trust_score == pytest.approx(trust, abs=1e-6)
    assert summary.grade == grade
    assert summary.hallucination_detected is (verdict is HALLUCINATED)
    assert summary.legacy_score == pytest.approx(trust or 0.0, abs=1e-6)
    if status is FAILED:
        assert summary.error_kind == "judge_unavailable"
    else:
        assert summary.error_kind is None


def test_failed_and_not_assessed_audits_have_no_scores() -> None:
    assert aggregate(claims((K, 0.0))).scores is None
    assert aggregate(claims((N, 0.0))).scores is None


def test_no_claims_scores_only_count() -> None:
    scores = aggregate([]).scores

    assert scores == AuditScores(claims_total=0, claims_decided=0, claims_unknown=0)
    assert scores is not None and scores.trust_score is None and scores.faithfulness is None


def test_scores_of_a_mixed_audit() -> None:
    # S 0.9 -> s .916667 c .033333 u .05;  C 0.6 -> s .2 c .6 u .2;  K excluded.
    summary = aggregate(claims((S, 0.9), (C, 0.6), (K, 0.0), (N, 0.0), (N, 0.0)))
    scores = summary.scores
    assert scores is not None

    assert summary.status is DEGRADED  # 1 unknown of 3 checked
    assert summary.verdict is HALLUCINATED and summary.contradiction_flag
    assert (scores.claims_total, scores.claims_decided, scores.claims_unknown) == (5, 2, 1)
    assert summary.claims_not_checked == 2
    assert scores.faithfulness == pytest.approx((0.916667 + 0.2) / 2, abs=1e-6)
    assert scores.contradiction_rate == pytest.approx((0.033333 + 0.6) / 2, abs=1e-6)
    assert scores.unverified_rate == pytest.approx((0.05 + 0.2) / 2, abs=1e-6)
    assert scores.coverage == pytest.approx(1 - 0.125, abs=1e-6)
    assert scores.accuracy_addressed == pytest.approx(1.116667 / (1.116667 + 0.633333), abs=1e-6)
    assert scores.max_contradiction == pytest.approx(0.6)
    assert scores.p_hallucinated == pytest.approx(0.8)  # max(c + u) under hallucination
    assert scores.trust_score == pytest.approx(0.558333 - 0.316667, abs=1e-6)


def test_p_hallucinated_is_max_contradiction_under_unverifiable() -> None:
    scores = aggregate(claims((S, 0.9), (U, 0.9)), UNVERIFIABLE).scores

    assert scores is not None
    assert scores.p_hallucinated == pytest.approx(scores.max_contradiction)


def test_accuracy_addressed_is_absent_without_support_or_contradiction() -> None:
    # A recorded distribution that is all "unsupported": nothing was addressed.
    only_unsupported = ClaimScoreInput(U, 1.0, {"unsupported": 1.0})

    summary = aggregate([only_unsupported], UNVERIFIABLE)

    assert summary.scores is not None
    assert summary.scores.accuracy_addressed is None
    assert summary.trust_score is None and summary.grade is None
    assert summary.verdict is UNVERIFIABLE_VERDICT
    assert summary.legacy_score == 0.0


def test_recorded_probabilities_are_used_and_normalized() -> None:
    # Weights (not yet normalized): 70% supported, 30% contradicted.
    claim = ClaimScoreInput(S, 0.99, {"supported": 1.4, "contradicted": 0.6})

    summary = aggregate([claim])

    assert summary.scores is not None
    assert summary.scores.max_contradiction == pytest.approx(0.3)
    assert summary.verdict is GROUNDED


def test_thresholds_compare_with_tolerance() -> None:
    at_flag = ClaimScoreInput(S, 0.9, {"supported": 0.65, "contradicted": 0.35})
    below_flag = ClaimScoreInput(S, 0.9, {"supported": 0.66, "contradicted": 0.34})

    assert aggregate([at_flag]).verdict is HALLUCINATED
    assert aggregate([below_flag]).verdict is GROUNDED


def test_thresholds_come_from_the_config() -> None:
    lenient = ScoringConfig(contradiction_flag=0.7)

    assert aggregate(claims((C, 0.6), (S, 0.9)), config=lenient).verdict is GROUNDED
    assert aggregate(claims((C, 0.6), (S, 0.9))).verdict is HALLUCINATED


def test_partial_weight_moves_partial_mass() -> None:
    component = claim_components({"partially_supported": 1.0}, partial_weight=0.8)

    assert (component.support, component.contradiction, component.unverified) == pytest.approx(
        (0.8, 0.0, 0.2)
    )


@pytest.mark.parametrize("status", [S, P, C, U])
@pytest.mark.parametrize("confidence", [0.0, 0.25, 0.6, 1.0])
def test_llm_distribution_sums_to_one(status: VerificationStatus, confidence: float) -> None:
    distribution = llm_distribution(status, confidence)

    assert tuple(distribution) == PROBABILITY_KEYS
    assert sum(distribution.values()) == pytest.approx(1.0)
    key = {S: "supported", P: "partially_supported", C: "contradicted", U: "unsupported"}[status]
    assert distribution[key] == pytest.approx(confidence)


def test_llm_distribution_edge_cases() -> None:
    assert llm_distribution(K, 0.9) == {}
    assert llm_distribution(N, 0.9) == {}
    assert llm_distribution(S, 7.0)["supported"] == 1.0  # clamped
    assert llm_distribution(S, float("nan"))["supported"] == 0.0


@pytest.mark.parametrize(
    "probabilities, expected",
    [
        ({}, None),
        ({"supported": 0.0}, None),
        ({"supported": -1.0, "contradicted": 2.0}, {"contradicted": 1.0}),
        ({"supported": "x", "unsupported": True, "contradicted": 1}, {"contradicted": 1.0}),
        ({"supported": float("inf"), "unsupported": 3.0}, {"unsupported": 1.0}),
        ({"supported": 1.0, "other": 5.0}, {"supported": 1.0}),
    ],
)
def test_normalized_distribution(
    probabilities: Dict[str, Any], expected: Optional[Dict[str, float]]
) -> None:
    result = normalized_distribution(probabilities)
    if expected is None:
        assert result is None
    else:
        assert result is not None
        assert {k: v for k, v in result.items() if v} == expected


def test_claim_input_from_a_verification_record() -> None:
    record = {
        "claim": "x",
        "status": "contradicted",
        "confidence": "0.8",
        "evidence": [],
        "verdict_probabilities": {"contradicted": 1.0},
    }

    claim = ClaimScoreInput.from_verification(record)

    assert claim.status is C and claim.confidence == 0.8
    assert claim.distribution() == {
        "contradicted": 1.0,
        "partially_supported": 0.0,
        "supported": 0.0,
        "unsupported": 0.0,
    }
    bogus = ClaimScoreInput.from_verification({"status": "bogus", "confidence": None})
    assert bogus.status is K and bogus.distribution() == {}


@pytest.mark.parametrize(
    "requested, sources, expected",
    [
        ("hallucination", ["knowledge"], HALLUCINATION),
        ("UNVERIFIABLE", ["request_system"], UNVERIFIABLE),
        ("", ["request_context"], HALLUCINATION),
        ("auto", ["knowledge", "tool_results"], HALLUCINATION),
        ("auto", [""], HALLUCINATION),  # an untagged caller document is request evidence
        ("auto", ["knowledge", "upload"], UNVERIFIABLE),
        ("", [], UNVERIFIABLE),
        ("sometimes", ["extension"], HALLUCINATION),
    ],
)
def test_resolve_unsupported_policy(
    requested: str, sources: Sequence[str], expected: UnsupportedPolicy
) -> None:
    assert resolve_unsupported_policy(requested, sources) is expected


@pytest.mark.parametrize(
    "score, grade",
    [(None, None), (1.0, "A"), (0.9, "A"), (0.8999, "B"), (0.7, "B"), (0.5, "C"), (0.3, "D"), (0.29, "F")],
)  # fmt: skip
def test_grade_cutoffs(score: Optional[float], grade: Optional[str]) -> None:
    assert grade_for(score, DEFAULT_SCORING.grade_cutoffs) == grade


def test_ordered_reasons() -> None:
    reasons = ordered_reasons(
        ["custom", "judge_retry_exhausted", "unknown_claims", "custom", "", "decomposer_fallback"]
    )

    assert reasons == ("decomposer_fallback", "unknown_claims", "judge_retry_exhausted", "custom")


def test_config_validation_and_settings() -> None:
    with pytest.raises(ValueError):
        ScoringConfig(contradiction_flag=1.5)
    with pytest.raises(ValueError):
        ScoringConfig(grade_cutoffs=(0.5, 0.7, 0.3, 0.1))
    with pytest.raises(ValueError):
        ScoringConfig(grade_cutoffs=(0.9, 0.7))

    config = ScoringConfig.from_settings(
        Settings(scoring_contradiction_flag=0.4, scoring_grade_cutoffs="0.95,0.8,0.6,0.4")
    )

    assert config.contradiction_flag == 0.4
    assert config.grade_cutoffs == (0.95, 0.8, 0.6, 0.4)
    assert ScoringConfig.from_settings(Settings()) == DEFAULT_SCORING
