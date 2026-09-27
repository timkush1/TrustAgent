"""Scorer node tests, focused on CONTRADICTED handling and faithfulness scoring.

These are deterministic (no LLM): they lock in that a CONTRADICTED verdict
scores 0.0, is treated as a hallucination, and is surfaced in the trace — the
end-to-end wiring that a local model can't be relied on to exercise.
"""

from truthtable.graphs.nodes.scorer import (
    calculate_faithfulness_score,
    detect_hallucination,
    generate_reasoning_trace,
)
from truthtable.graphs.state import VerificationStatus


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
