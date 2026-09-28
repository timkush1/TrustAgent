"""
Audit-graph preflight tests (SPEC section 3.2, test_preflight).

With no evidence documents and knowledge off, an audit is NOT_ASSESSED
(no_evidence) and makes zero LLM calls: it no longer verifies every claim
against empty context and reports it UNSUPPORTED. Retrieval runs only when the
caller enables knowledge; when it finds nothing the audit is NOT_ASSESSED
(knowledge_no_hits) and its claims come back NOT_CHECKED.
"""

import json
import re
from typing import Any, Dict, List, Optional, Sequence

import pytest

from truthtable.graphs.audit_graph import build_audit_graph, resolve_unsupported_policy, run_audit
from truthtable.graphs.state import UnsupportedPolicy, VerificationStatus
from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider

CLAIM = "Paris is the capital of France."
_CLAIM_BLOCK = re.compile(r"<claim>\n(.*?)\n</claim>", re.DOTALL)


class SpyJudge(LLMProvider):
    """Decomposes into `claims` (one claim by default) and supports each, recording every call."""

    def __init__(self, claims: Sequence[str] = (CLAIM,)) -> None:
        super().__init__(model="spy")
        self.claims = list(claims)
        self.prompts: List[str] = []

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        prompt = request.messages[-1].content
        self.prompts.append(prompt)
        if _CLAIM_BLOCK.search(prompt):
            answer: Any = {"status": "SUPPORTED", "confidence": 0.9, "evidence": [CLAIM]}
        else:
            answer = self.claims
        return CompletionResponse(
            content=json.dumps(answer), model=self.model, finish_reason="stop"
        )

    async def health_check(self) -> bool:
        return True


class FakeEmbeddings:
    dimension = 3

    def embed(self, texts: Sequence[str]) -> List[List[float]]:
        return [[0.1, 0.2, 0.3] for _ in texts]


class FakeKnowledgeBase:
    """Stands in for QdrantStore.search: returns `hits` for every query."""

    def __init__(self, hits: Sequence[str]):
        self.hits = list(hits)
        self.searches = 0

    def search(
        self, query_vector: List[float], top_k: int, score_threshold: float
    ) -> List[Dict[str, Any]]:
        self.searches += 1
        return [{"text": text, "score": 0.9} for text in self.hits]


async def _audit(
    graph: Any,
    context_docs: Optional[List[str]] = None,
    **kwargs: Any,
) -> Dict[str, Any]:
    state = await run_audit(
        graph=graph,
        request_id="req-preflight",
        user_query="What is the capital of France?",
        llm_response=CLAIM,
        context_docs=context_docs or [],
        **kwargs,
    )
    return dict(state)


def _assert_not_assessed(state: Dict[str, Any], reason: str) -> None:
    assert state["verdict"] == "NOT_ASSESSED"
    assert state["not_assessed_reason"] == reason
    assert state["audit_status"] == "completed"
    assert state["error_kind"] is None
    # The v1 fields keep their types for legacy callers.
    assert state["faithfulness_score"] == 0.0
    assert state["hallucination_detected"] is False
    assert state["reasoning_trace"].startswith("Not assessed")


@pytest.mark.parametrize("knowledge_enabled", [False, True])
async def test_no_evidence_is_not_assessed_with_zero_llm_calls(knowledge_enabled: bool) -> None:
    # Without a knowledge base, asking for knowledge changes nothing.
    judge = SpyJudge()

    state = await _audit(build_audit_graph(provider=judge), knowledge_enabled=knowledge_enabled)

    assert judge.prompts == []
    _assert_not_assessed(state, "no_evidence")
    assert state["claims"] is None
    assert state["claim_verifications"] == []
    assert state["step_timings"] == {}


@pytest.mark.parametrize("blank", ["", "  \n\t "])
async def test_blank_documents_are_not_evidence(blank: str) -> None:
    judge = SpyJudge()

    state = await _audit(
        build_audit_graph(provider=judge),
        context_docs=[blank, blank],
        context_sources=["request_system", "request_system"],
    )

    assert judge.prompts == []
    _assert_not_assessed(state, "no_evidence")
    assert state["context_docs"] == []


async def test_blank_documents_are_dropped_before_verification() -> None:
    judge = SpyJudge()

    state = await _audit(
        build_audit_graph(provider=judge),
        context_docs=["", "Paris is in France.", " "],
        context_sources=["request_system", "knowledge", "request_context"],
    )

    assert state["verdict"] == "GROUNDED"
    assert state["context_docs"] == ["Paris is in France."]
    # Sources stay aligned with the documents they describe: only knowledge is
    # left, so auto resolves to unverifiable.
    assert state["context_sources"] == ["knowledge"]
    assert state["unsupported_policy"] == "unverifiable"
    verify_prompt = judge.prompts[-1]
    assert "[Document 1]\nParis is in France." in verify_prompt
    assert "[Document 2]" not in verify_prompt


async def test_evidence_runs_the_full_audit() -> None:
    judge = SpyJudge()

    state = await _audit(build_audit_graph(provider=judge), context_docs=["Paris is in France."])

    assert len(judge.prompts) == 2  # decompose + verify one claim
    assert state["verdict"] == "GROUNDED"
    assert state["not_assessed_reason"] is None
    assert state["claim_verifications"][0]["status"] == VerificationStatus.SUPPORTED
    assert {"decompose_ms", "verify_ms", "score_ms"} <= set(state["step_timings"])


async def test_knowledge_off_skips_retrieval_even_with_a_knowledge_base() -> None:
    judge, knowledge = SpyJudge(), FakeKnowledgeBase(hits=["Paris is in France."])
    graph = build_audit_graph(
        provider=judge, embedding_service=FakeEmbeddings(), qdrant_store=knowledge
    )

    state = await _audit(graph, knowledge_enabled=False)

    assert judge.prompts == []
    assert knowledge.searches == 0
    _assert_not_assessed(state, "no_evidence")


async def test_knowledge_hits_are_verified_as_unverifiable_policy() -> None:
    judge, knowledge = SpyJudge(), FakeKnowledgeBase(hits=["Paris is the capital of France."])
    graph = build_audit_graph(
        provider=judge, embedding_service=FakeEmbeddings(), qdrant_store=knowledge
    )

    state = await _audit(graph, knowledge_enabled=True)

    assert knowledge.searches > 0
    assert state["context_docs"] == ["Paris is the capital of France."]
    # The retrieved documents are tagged as knowledge evidence.
    assert state["context_sources"] == ["knowledge"]
    assert state["verdict"] == "GROUNDED"
    # auto: knowledge-base evidence only, so UNSUPPORTED would mean unverifiable.
    assert state["unsupported_policy"] == "unverifiable"
    assert "retrieve_ms" in state["step_timings"]


async def test_knowledge_without_hits_is_not_assessed_and_claims_not_checked() -> None:
    judge, knowledge = SpyJudge(), FakeKnowledgeBase(hits=[])
    graph = build_audit_graph(
        provider=judge, embedding_service=FakeEmbeddings(), qdrant_store=knowledge
    )

    state = await _audit(graph, knowledge_enabled=True)

    assert len(judge.prompts) == 1  # the decomposition feeds retrieval; no verification
    _assert_not_assessed(state, "knowledge_no_hits")
    assert [cv["status"] for cv in state["claim_verifications"]] == [VerificationStatus.NOT_CHECKED]
    # The unchecked claim keeps where the decomposer found it in the answer.
    (unchecked,) = state["claim_verifications"]
    assert unchecked["decided_by"] == "none" and unchecked["index"] == 0
    assert (unchecked["span_start"], unchecked["span_end"]) == (0, len(CLAIM))
    assert unchecked["span_method"] == "exact"
    assert "verify_ms" not in state["step_timings"]


async def test_knowledge_is_not_searched_for_an_answer_without_claims() -> None:
    judge, knowledge = SpyJudge(claims=[]), FakeKnowledgeBase(hits=[])
    graph = build_audit_graph(
        provider=judge, embedding_service=FakeEmbeddings(), qdrant_store=knowledge
    )

    state = await _audit(graph, knowledge_enabled=True)

    assert len(judge.prompts) == 1  # the decomposition only
    assert knowledge.searches == 0
    # Nothing to check is NO_CLAIMS, not NOT_ASSESSED (knowledge_no_hits).
    assert state["verdict"] == "NO_CLAIMS"
    assert state["not_assessed_reason"] is None
    assert state["claim_verifications"] == []


@pytest.mark.parametrize(
    "requested, sources, documents, expected",
    [
        ("hallucination", ["knowledge"], 1, UnsupportedPolicy.HALLUCINATION),
        ("unverifiable", ["request_system"], 1, UnsupportedPolicy.UNVERIFIABLE),
        ("UNVERIFIABLE", [], 1, UnsupportedPolicy.UNVERIFIABLE),
        ("", ["request_system"], 1, UnsupportedPolicy.HALLUCINATION),
        ("auto", ["knowledge", "tool_results"], 2, UnsupportedPolicy.HALLUCINATION),
        ("auto", ["knowledge", "upload"], 2, UnsupportedPolicy.UNVERIFIABLE),
        # Caller documents without a source are request evidence (legacy callers).
        ("", [], 1, UnsupportedPolicy.HALLUCINATION),
        # No caller documents: only knowledge can supply evidence.
        ("", [], 0, UnsupportedPolicy.UNVERIFIABLE),
        ("bogus", ["request_context"], 1, UnsupportedPolicy.HALLUCINATION),
    ],
)
def test_resolve_unsupported_policy(
    requested: str, sources: List[str], documents: int, expected: UnsupportedPolicy
) -> None:
    assert resolve_unsupported_policy(requested, sources, documents) is expected


@pytest.mark.parametrize("engine_default", [False, True])
async def test_extract_claims_without_evidence_decomposes_and_never_verifies(
    engine_default: bool,
) -> None:
    # AuditOptions.extract_claims_without_evidence (or the engine's
    # TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE): the claims come back
    # NOT_CHECKED, located in the answer, and the judge is asked nothing else.
    judge = SpyJudge()
    graph = build_audit_graph(provider=judge, extract_claims_without_evidence=engine_default)

    state = await _audit(graph, extract_claims_without_evidence=not engine_default)

    assert len(judge.prompts) == 1  # the decomposition only
    assert state["verdict"] == "NOT_ASSESSED"
    assert state["not_assessed_reason"] == "no_evidence"
    assert state["audit_status"] == "completed"
    assert state["scores"] is None and state["grade"] is None
    (claim,) = state["claim_verifications"]
    assert claim["status"] == VerificationStatus.NOT_CHECKED
    assert (claim["span_start"], claim["span_end"], claim["span_method"]) == (
        0,
        len(CLAIM),
        "exact",
    )


@pytest.mark.parametrize(
    "engine_policy, requested, expected",
    [
        # The engine's TRUTHTABLE_UNSUPPORTED_POLICY applies when the request names none.
        ("unverifiable", "", "unverifiable"),
        ("hallucination", "", "hallucination"),
        # A request's own policy (auto included) wins over the engine default.
        ("unverifiable", "hallucination", "hallucination"),
        ("unverifiable", "auto", "hallucination"),
    ],
)
async def test_engine_unsupported_policy_is_the_default(
    engine_policy: str, requested: str, expected: str
) -> None:
    judge = SpyJudge()
    graph = build_audit_graph(provider=judge, unsupported_policy=engine_policy)

    state = await _audit(
        graph,
        context_docs=["Paris is in France."],
        context_sources=["request_system"],
        unsupported_policy=requested,
    )

    assert state["unsupported_policy"] == expected
