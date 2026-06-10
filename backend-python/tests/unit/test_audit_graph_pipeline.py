"""
Full-pipeline test: decompose -> verify -> score through the compiled
LangGraph, using the MockLLMProvider so no model or Qdrant is needed.
"""

import json

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
    assert state["faithfulness_score"] == 1.0
    assert state["hallucination_detected"] is False
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

    assert state["faithfulness_score"] == 0.0
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
