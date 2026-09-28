"""
Scorer node tests: the node delegates to scoring v2 (scoring/aggregate.py,
see test_scoring_v2.py for the arithmetic) and writes the audit's result into
the graph state: verdict, status, scores, grade, policy, legacy fields and
the reasoning trace.
"""

from typing import Any, Dict, List

import pytest

from truthtable.graphs.nodes.scorer import (
    NO_CLAIMS_TRACE,
    ScorerNode,
    effective_policy,
    generate_reasoning_trace,
    score_audit,
)
from truthtable.graphs.state import UnsupportedPolicy, VerificationStatus
from truthtable.scoring import ScoringConfig

S = VerificationStatus.SUPPORTED
C = VerificationStatus.CONTRADICTED
U = VerificationStatus.UNSUPPORTED
P = VerificationStatus.PARTIALLY_SUPPORTED
K = VerificationStatus.UNKNOWN
N = VerificationStatus.NOT_CHECKED


def _v(status: Any, confidence: float = 0.9, claim: str = "a claim") -> Dict[str, Any]:
    return {"claim": claim, "status": status, "confidence": confidence, "evidence": []}


def _state(verifications: List[Dict[str, Any]], **extra: Any) -> Dict[str, Any]:
    state: Dict[str, Any] = {
        "request_id": "r",
        "claim_verifications": verifications,
        "unsupported_policy": "hallucination",
        "context_docs": ["doc"],
        "context_sources": ["request_context"],
        "degraded_reasons": [],
        "step_timings": {},
    }
    state.update(extra)
    return state


async def test_zero_claims_is_no_claims_without_a_score() -> None:
    state = await ScorerNode().run(_state([]))

    assert state["verdict"] == "NO_CLAIMS"
    assert state["audit_status"] == "completed"
    assert state["faithfulness_score"] == 0.0  # legacy: trust_score or 0
    assert state["scores"]["trust_score"] is None
    assert state["scores"]["claims_total"] == 0
    assert state["grade"] is None
    assert state["hallucination_detected"] is False
    assert state["reasoning_trace"] == NO_CLAIMS_TRACE
    assert state["degraded_reasons"] == [] and state["error_kind"] is None
    assert state["scoring_version"] == "v2"
    assert "score_ms" in state["step_timings"]


async def test_contradicted_claim_is_hallucinated() -> None:
    state = await ScorerNode().run(_state([_v(C, 0.95, claim="Refunds last 60 days")]))

    assert state["verdict"] == "HALLUCINATED"
    assert state["audit_status"] == "completed"
    assert state["hallucination_detected"] is True
    assert state["faithfulness_score"] == 0.0  # clamp(F - K) with K = 0.95
    assert state["scores"]["max_contradiction"] == pytest.approx(0.95)
    assert state["grade"] == "F"


async def test_supported_claims_are_grounded_with_a_grade() -> None:
    state = await ScorerNode().run(_state([_v(S, 0.95), _v(S, 0.9)]))

    assert state["verdict"] == "GROUNDED"
    assert state["faithfulness_score"] == pytest.approx(state["scores"]["trust_score"])
    assert state["scores"]["trust_score"] == pytest.approx(0.9125)
    assert state["grade"] == "A"


async def test_unknown_claims_degrade_and_earlier_reasons_are_kept() -> None:
    state = await ScorerNode().run(
        _state([_v(S), _v(S), _v(K, 0.0)], degraded_reasons=["decomposer_fallback"])
    )

    assert state["audit_status"] == "degraded"
    assert state["verdict"] == "GROUNDED"
    assert state["degraded_reasons"] == ["decomposer_fallback", "unknown_claims"]
    assert state["scores"]["claims_unknown"] == 1


async def test_judge_outage_fails_without_a_score() -> None:
    state = await ScorerNode().run(_state([_v(K, 0.0), _v(K, 0.0)]))

    assert state["audit_status"] == "failed"
    assert state["verdict"] is None
    assert state["error_kind"] == "judge_unavailable"
    assert state["scores"] is None and state["grade"] is None
    assert state["faithfulness_score"] == 0.0
    assert state["hallucination_detected"] is False
    assert "Audit failed (judge_unavailable)" in state["reasoning_trace"]


async def test_not_checked_claims_are_not_assessed() -> None:
    state = await ScorerNode().run(_state([_v(N, 0.0), _v("not_checked", 0.0)]))

    assert state["verdict"] == "NOT_ASSESSED"
    assert state["not_assessed_reason"] == "no_evidence"
    assert state["audit_status"] == "completed"
    assert state["scores"] is None


async def test_capped_evidence_scores_unsupported_as_unverifiable() -> None:
    verifications = [_v(U, 0.9)]

    plain = await ScorerNode().run(_state(verifications))
    trimmed = await ScorerNode().run(
        _state(verifications, context_trimmed=True, degraded_reasons=["context_trimmed"])
    )

    assert (plain["verdict"], plain["unsupported_policy"]) == ("HALLUCINATED", "hallucination")
    assert (trimmed["verdict"], trimmed["unsupported_policy"]) == ("UNVERIFIABLE", "unverifiable")
    assert trimmed["audit_status"] == "degraded"
    assert trimmed["degraded_reasons"] == ["context_trimmed"]


@pytest.mark.parametrize(
    "requested, sources, expected",
    [
        ("hallucination", ["knowledge"], UnsupportedPolicy.HALLUCINATION),
        ("", ["knowledge"], UnsupportedPolicy.UNVERIFIABLE),
        ("auto", ["request_system"], UnsupportedPolicy.HALLUCINATION),
        ("auto", [], UnsupportedPolicy.HALLUCINATION),  # an untagged document is request evidence
    ],
)
def test_effective_policy_resolves_requests_that_skipped_preflight(
    requested: str, sources: List[str], expected: UnsupportedPolicy
) -> None:
    state = _state([], unsupported_policy=requested, context_sources=sources)

    assert effective_policy(state) is expected


def test_effective_policy_without_documents_is_unverifiable() -> None:
    assert effective_policy({"unsupported_policy": "", "context_docs": []}) is (
        UnsupportedPolicy.UNVERIFIABLE
    )


async def test_scorer_uses_its_scoring_config() -> None:
    verifications = [_v(C, 0.6), _v(S), _v(S)]

    default = await ScorerNode().run(_state(verifications))
    lenient = await ScorerNode(ScoringConfig(contradiction_flag=0.7)).run(_state(verifications))

    assert default["verdict"] == "HALLUCINATED"
    assert lenient["verdict"] == "GROUNDED"


def test_reasoning_trace_lists_counts_and_problem_claims() -> None:
    verifications = [
        _v(U, claim="Refunds are instant"),
        _v(C, claim="X was invented in 1850"),
        _v(S),
    ]
    summary = score_audit(verifications, UnsupportedPolicy.HALLUCINATION)

    trace = generate_reasoning_trace(verifications, summary)

    assert trace.startswith("Verdict: HALLUCINATED (hallucination policy)")
    assert "Trust score:" in trace
    assert "Contradicted: 1" in trace and "Unsupported: 1" in trace
    assert "Not checked" not in trace  # listed only when present
    assert trace.index("[contradicted] X was invented") < trace.index("[unsupported] Refunds")


def test_reasoning_trace_counts_not_checked_and_is_safe_without_claims() -> None:
    assert generate_reasoning_trace([]) == NO_CLAIMS_TRACE  # D1: no ZeroDivisionError
    trace = generate_reasoning_trace([_v(S), _v(N)])
    assert "Not checked: 1" in trace


def test_reasoning_trace_lists_degraded_reasons() -> None:
    verifications = [_v(S), _v(S), _v(K, 0.0)]
    summary = score_audit(verifications, UnsupportedPolicy.HALLUCINATION, degraded_reasons=[])

    assert "Degraded: unknown_claims" in generate_reasoning_trace(verifications, summary)
