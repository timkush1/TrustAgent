"""
Per-claim context (SPEC section 3.2 "Graph and nodes"; D13, C-M26).

Each claim is judged against its BM25 documents when the evidence is large; a
claim without a BM25 hit falls back to all documents (never "UNSUPPORTED by
rule"), and a claim without any document is NOT_CHECKED without a judge call.
"""

import json
import math
import re
from typing import Any, Dict, List

import pytest

from truthtable.graphs.nodes.verifier import (
    CONTEXT_ALL,
    CONTEXT_BM25,
    CONTEXT_FALLBACK_ALL,
    CONTEXT_NONE,
    PER_CLAIM_TOP_K,
    SMALL_EVIDENCE_TOKENS,
    VerifierNode,
    check_all_claims,
    estimate_tokens,
    select_claim_contexts,
)
from truthtable.graphs.state import AuditState, VerificationStatus
from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider
from truthtable.text.bm25 import BM25, STOPWORDS, tokenize

_DOC_MARKER = re.compile(r"\[Document \d+\]\n")


def _filler(topic: str, words: int = 1000) -> str:
    """A long off-topic document (about `words` words, well over 3,000 tokens in total)."""
    return " ".join(f"{topic}{i % 50}" for i in range(words))


REFUNDS = "Refunds are issued within 30 days of purchase to the original card. " + _filler("r")
SHIPPING = "Shipping is free on every order above fifty dollars. " + _filler("s")
WARRANTY = "The warranty covers manufacturing defects for two years. " + _filler("w")
SUPPORT = "Support is available by chat from nine to five on weekdays. " + _filler("h")
LARGE = [REFUNDS, SHIPPING, WARRANTY, SUPPORT]


class RecordingJudge(LLMProvider):
    """Answers SUPPORTED and records which documents each claim was shown."""

    def __init__(self) -> None:
        super().__init__(model="recording")
        self.seen: Dict[str, List[str]] = {}

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        prompt = request.messages[-1].content
        claim = prompt.split("<claim>\n", 1)[1].split("\n</claim>", 1)[0]
        context = prompt.split("<context>\n", 1)[1].split("\n</context>", 1)[0]
        self.seen[claim] = [doc.strip() for doc in _DOC_MARKER.split(context) if doc.strip()]
        answer = {"status": "SUPPORTED", "confidence": 0.9, "evidence": [], "reasoning": "ok"}
        return CompletionResponse(
            content=json.dumps(answer), model=self.model, finish_reason="stop"
        )

    async def health_check(self) -> bool:
        return True


def _state(claims: List[str], documents: List[str]) -> AuditState:
    state: Dict[str, Any] = {
        "request_id": "r-1",
        "user_query": "q",
        "llm_response": " ".join(claims),
        "context_docs": documents,
        "context_sources": [],
        "claims": claims,
        "claim_verifications": None,
        "degraded_reasons": [],
        "step_timings": {},
    }
    return state  # type: ignore[return-value]


# ---------------------------------------------------------------------------
# BM25
# ---------------------------------------------------------------------------


def test_tokenize_folds_case_and_drops_stop_words() -> None:
    assert tokenize("The Refund-window is 30 DAYS; café_au_lait") == [
        "refund",
        "window",
        "30",
        "days",
        "café",
        "au",
        "lait",
    ]
    assert "the" in tokenize("The end", drop_stopwords=False)
    assert "the" in STOPWORDS


def test_bm25_scores_match_the_okapi_formula() -> None:
    index = BM25(["apple banana", "apple apple cherry", "durian"], k1=1.5, b=0.75)

    scores = index.scores("apple")

    n_docs, doc_freq = 3, 2
    idf = math.log(1 + (n_docs - doc_freq + 0.5) / (doc_freq + 0.5))
    avg_length = (2 + 3 + 1) / 3

    def okapi(term_freq: int, length: int) -> float:
        norm = 1 - 0.75 + 0.75 * length / avg_length
        return idf * term_freq * 2.5 / (term_freq + 1.5 * norm)

    assert scores == pytest.approx([okapi(1, 2), okapi(2, 3), 0.0])
    top = index.top("apple", 5)
    assert [position for position, _ in top] == [1, 0]
    assert [score for _, score in top] == pytest.approx([okapi(2, 3), okapi(1, 2)])
    assert index.top("apple", 0) == []
    assert index.top("the of and", 5) == []  # stop words only: no hit
    assert len(index) == 3


def test_bm25_ties_keep_document_order() -> None:
    index = BM25(["kiwi", "kiwi", "kiwi"])

    assert [position for position, _ in index.top("kiwi", 2)] == [0, 1]


# ---------------------------------------------------------------------------
# Selection
# ---------------------------------------------------------------------------


def test_small_evidence_goes_whole_to_every_claim() -> None:
    documents = ["Paris is the capital of France.", "The Louvre is in Paris."]

    contexts = select_claim_contexts(["Lyon is the capital.", "Rome is old."], documents)

    assert [c.method for c in contexts] == [CONTEXT_ALL, CONTEXT_ALL]
    assert all(c.documents == tuple(documents) and c.doc_indices == (0, 1) for c in contexts)


def test_large_evidence_is_ranked_per_claim_with_bm25() -> None:
    assert sum(estimate_tokens(doc) for doc in LARGE) > SMALL_EVIDENCE_TOKENS

    contexts = select_claim_contexts(
        ["Refunds are issued within 60 days.", "The warranty lasts two years."], LARGE
    )

    refund, warranty = contexts
    assert refund.method == CONTEXT_BM25 and refund.doc_indices == (0,)
    assert refund.documents == (REFUNDS,)
    assert warranty.method == CONTEXT_BM25 and warranty.doc_indices == (2,)


def test_bm25_keeps_at_most_top_k_documents_in_evidence_order() -> None:
    documents = [f"Topic{i} policy text. " + _filler(f"x{i}") for i in range(6)]

    (context,) = select_claim_contexts(["policy text for Topic5 Topic1 Topic3 Topic0"], documents)

    assert context.method == CONTEXT_BM25
    assert len(context.doc_indices) == PER_CLAIM_TOP_K
    assert list(context.doc_indices) == sorted(context.doc_indices)
    assert set(context.doc_indices) <= {0, 1, 3, 5}


def test_a_claim_without_a_bm25_hit_falls_back_to_all_documents() -> None:
    (context,) = select_claim_contexts(["Jupiter hosts many moons."], LARGE)

    assert context.method == CONTEXT_FALLBACK_ALL
    assert context.documents == tuple(LARGE)
    assert context.doc_indices == (0, 1, 2, 3)


def test_no_documents_means_no_context() -> None:
    contexts = select_claim_contexts(["A claim.", "Another."], [])

    assert [c.method for c in contexts] == [CONTEXT_NONE, CONTEXT_NONE]
    assert all(c.documents == () for c in contexts)


def test_scope_all_sends_every_document() -> None:
    contexts = select_claim_contexts(["Refunds take 60 days."], LARGE, scope="all")

    assert contexts[0].method == CONTEXT_ALL and contexts[0].documents == tuple(LARGE)


def test_unknown_scope_is_rejected() -> None:
    with pytest.raises(ValueError, match="context scope"):
        select_claim_contexts(["x"], ["y"], scope="nearest")
    with pytest.raises(ValueError, match="context_scope"):
        VerifierNode(RecordingJudge(), context_scope="nearest")


# ---------------------------------------------------------------------------
# The node
# ---------------------------------------------------------------------------


async def test_node_shows_each_claim_its_own_documents() -> None:
    judge = RecordingJudge()
    claims = ["Refunds are issued within 60 days.", "Jupiter hosts many moons."]

    state = await VerifierNode(judge).run(_state(claims, LARGE))

    assert judge.seen[claims[0]] == [REFUNDS]
    assert judge.seen[claims[1]] == LARGE  # no hit: every document, never a rule verdict
    statuses = [v["status"] for v in state["claim_verifications"] or []]
    assert statuses == [VerificationStatus.SUPPORTED, VerificationStatus.SUPPORTED]
    assert state["degraded_reasons"] == []


async def test_node_with_scope_all_shows_every_document() -> None:
    judge = RecordingJudge()

    await VerifierNode(judge, context_scope="all").run(
        _state(["Refunds are issued within 60 days."], LARGE)
    )

    assert judge.seen["Refunds are issued within 60 days."] == LARGE


async def test_node_without_documents_is_not_checked_without_a_judge_call() -> None:
    judge = RecordingJudge()

    state = await VerifierNode(judge).run(_state(["Refunds take 60 days."], []))

    (verification,) = state["claim_verifications"] or []
    assert verification["status"] is VerificationStatus.NOT_CHECKED
    assert verification["decided_by"] == "none"
    assert judge.seen == {}


async def test_check_all_claims_rejects_mismatched_contexts() -> None:
    with pytest.raises(ValueError, match="one entry per claim"):
        await check_all_claims(["a", "b"], ["doc"], RecordingJudge(), contexts=[["doc"]])
