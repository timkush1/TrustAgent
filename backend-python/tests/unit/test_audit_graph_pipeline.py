"""
Full-pipeline test: decompose -> verify -> score through the compiled
LangGraph, using the MockLLMProvider so no model or Qdrant is needed.

Scores are scoring v2 (SPEC section 3.2): the legacy faithfulness_score is the
trust score, and each claim is located in the answer (UTF-16 spans).
"""

import json

import pytest

from truthtable.graphs.audit_graph import build_audit_graph, run_audit
from truthtable.providers.mock import MockLLMProvider


def make_fixtures(response_text: str, verdicts: dict[str, dict]) -> list[dict]:
    """Build decomposer + verifier fixtures for one example."""
    fixtures = [
        {
            "match": ["Extract all factual claims", response_text],
            "response": json.dumps(list(verdicts.keys())),
        }
    ]
    for claim, verdict in verdicts.items():
        fixtures.append(
            {
                "match": [f"<claim>\n{claim}\n</claim>"],
                "response": json.dumps(verdict),
            }
        )
    return fixtures


async def test_faithful_response_passes():
    response_text = "Paris is the capital of France."
    provider = MockLLMProvider(
        fixtures=make_fixtures(
            response_text,
            {
                "Paris is the capital of France.": {
                    "status": "SUPPORTED",
                    "confidence": 0.95,
                    "evidence": ["Paris is the capital of France."],
                }
            },
        )
    )
    graph = build_audit_graph(provider=provider)

    state = await run_audit(
        graph=graph,
        request_id="test-faithful",
        user_query="What is the capital of France?",
        llm_response=response_text,
        context_docs=["Paris is the capital and largest city of France."],
    )

    assert state["claims"] == ["Paris is the capital of France."]
    # SUPPORTED at 0.95: s = 0.95 + 0.5 * 0.05/3, c = 0.05/3, trust = s - c.
    assert state["faithfulness_score"] == pytest.approx(0.941667, abs=1e-6)
    assert state["verdict"] == "GROUNDED" and state["audit_status"] == "completed"
    assert state["grade"] == "A" and state["scoring_version"] == "v2"
    assert state["hallucination_detected"] is False
    (claim,) = state["claim_verifications"]
    assert (claim["span_start"], claim["span_end"], claim["span_method"]) == (0, 31, "exact")
    assert claim["decided_by"] == "llm" and claim["index"] == 0
    assert "decompose_ms" in state["step_timings"]
    assert "verify_ms" in state["step_timings"]
    assert "score_ms" in state["step_timings"]


async def test_hallucinated_response_is_flagged():
    response_text = "Lyon is the capital of France."
    provider = MockLLMProvider(
        fixtures=make_fixtures(
            response_text,
            {
                "Lyon is the capital of France.": {
                    "status": "UNSUPPORTED",
                    "confidence": 0.9,
                    "evidence": [],
                }
            },
        )
    )
    graph = build_audit_graph(provider=provider)

    state = await run_audit(
        graph=graph,
        request_id="test-hallucinated",
        user_query="What is the capital of France?",
        llm_response=response_text,
        context_docs=["Paris is the capital and largest city of France."],
    )

    # UNSUPPORTED at 0.9 against request evidence: baseless, and trust near 0.
    assert state["faithfulness_score"] == pytest.approx(0.016667, abs=1e-6)
    assert state["verdict"] == "HALLUCINATED"
    assert state["unsupported_policy"] == "hallucination"
    assert state["hallucination_detected"] is True


async def test_mixed_response_scores_between():
    response_text = "Paris is the capital of France. France borders twelve countries."
    provider = MockLLMProvider(
        fixtures=make_fixtures(
            response_text,
            {
                "Paris is the capital of France.": {
                    "status": "SUPPORTED",
                    "confidence": 0.9,
                    "evidence": ["Paris is the capital of France."],
                },
                "France borders twelve countries.": {
                    "status": "UNSUPPORTED",
                    "confidence": 0.9,
                    "evidence": [],
                },
            },
        )
    )
    graph = build_audit_graph(provider=provider)

    state = await run_audit(
        graph=graph,
        request_id="test-mixed",
        user_query="Tell me about France.",
        llm_response=response_text,
        context_docs=["Paris is the capital of France. France borders eight countries."],
    )

    assert 0.0 < state["faithfulness_score"] < 1.0
    assert state["hallucination_detected"] is True  # high-confidence unsupported claim
    spans = [(c["span_start"], c["span_end"]) for c in state["claim_verifications"]]
    assert spans == [(0, 31), (32, 64)]


async def test_contradicted_and_unverifiable_differ_end_to_end():
    response_text = "The museum opens at 10am. It has a rooftop cafe."
    provider = MockLLMProvider(
        fixtures=make_fixtures(
            response_text,
            {
                "The museum opens at 10am.": {
                    "status": "CONTRADICTED",
                    "confidence": 0.9,
                    "evidence": ["The museum opens at 9am."],
                    "reasoning": "9am, not 10am.",
                },
                "It has a rooftop cafe.": {
                    "status": "UNSUPPORTED",
                    "confidence": 0.9,
                    "evidence": [],
                    "reasoning": "Not mentioned.",
                },
            },
        )
    )
    graph = build_audit_graph(provider=provider)

    state = await run_audit(
        graph=graph,
        request_id="test-kb",
        user_query="When does the museum open?",
        llm_response=response_text,
        context_docs=["The museum opens at 9am."],
        context_sources=["knowledge"],
    )

    # Knowledge evidence: the unsupported claim is unverifiable, the
    # contradicted one still makes the answer HALLUCINATED.
    assert state["unsupported_policy"] == "unverifiable"
    assert state["verdict"] == "HALLUCINATED"
    assert [c["status"].name for c in state["claim_verifications"]] == [
        "CONTRADICTED",
        "UNSUPPORTED",
    ]
    assert state["claim_verifications"][0]["reasoning"] == "9am, not 10am."
