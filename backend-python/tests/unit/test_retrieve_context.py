"""
RetrieveContext (docs/v2/SPEC.md sections 2.2 and 7.1; C-M24).

The RPC cases run the real AuditServicer on an in-process grpc.aio server; the
retriever cases drive KnowledgeRetriever directly. The embedding service and
the vector store are small fakes, so no model, Qdrant or network is needed.
"""

import asyncio
import threading
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from types import SimpleNamespace
from typing import Any, AsyncIterator, Dict, List, Optional

import grpc
import pytest
from grpc import aio

from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.retrieve import (
    DEFAULT_MAX_CHARS,
    DEFAULT_TOP_K,
    INDEX_EMPTY,
    INDEX_ERROR,
    INDEX_READY,
    MAX_TOP_K,
    MIN_TRUNCATED_CHARS,
    Hit,
    KnowledgeRetriever,
    LruCache,
    first_sentences,
    knowledge_retriever_for,
    pack_documents,
    query_text,
)
from truthtable.grpc.server import AuditServicer

RPC_TIMEOUT_S = 5


class FakeEmbedder:
    """Records each embed call and the thread it ran on."""

    def __init__(self) -> None:
        self.calls: List[List[str]] = []
        self.threads: List[str] = []
        # The embedding service's own single-worker thread, which ingestion uses.
        self.ingest_executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="embed")

    def embed(self, texts: List[str]) -> List[List[float]]:
        self.calls.append(list(texts))
        self.threads.append(threading.current_thread().name)
        return [[float(len(text)), 1.0] for text in texts]


class FakeStore:
    """A vector store over fixed points; `score` is what a search returns."""

    def __init__(self, points: Optional[List[Dict[str, Any]]] = None, total: int = -1) -> None:
        self.points = list(points or [])
        self.total = len(self.points) if total < 0 else total
        self.searches: List[Dict[str, Any]] = []
        self.count_calls = 0
        self.error: Optional[Exception] = None
        self.gate: Optional[threading.Event] = None

    def search_filtered(
        self,
        query_vector: List[float],
        top_k: int = 5,
        score_threshold: float = 0.3,
        must: Optional[Dict[str, Any]] = None,
        must_not: Optional[Dict[str, Any]] = None,
    ) -> List[Dict[str, Any]]:
        self.searches.append(
            {
                "vector": query_vector,
                "top_k": top_k,
                "score_threshold": score_threshold,
                "must": must,
                "must_not": must_not,
            }
        )
        if self.gate is not None:
            self.gate.wait(timeout=5)
        if self.error is not None:
            raise self.error
        excluded = must_not or {}
        hits = [
            dict(point)
            for point in self.points
            if point["score"] >= score_threshold
            and not any(point.get(key) == value for key, value in excluded.items())
        ]
        return hits[:top_k]

    def count(self) -> int:
        self.count_calls += 1
        return self.total


class FakeClock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


POINTS = [
    {
        "id": "c1",
        "text": "Refunds are issued within 30 days of purchase.",
        "score": 0.91,
        "kind": "claim",
        "kb_status": "accepted",
        "source_doc_id": "refund-policy.md",
        "ingested_at_ms": 17,
    },
    {
        "id": "c2",
        "text": "Shipping is free on orders over $50.",
        "score": 0.52,
        "kind": "claim",
        "kb_status": "accepted",
        "title": "Shipping FAQ",
    },
    {
        "id": "c3",
        "text": "Refunds take 90 days.",
        "score": 0.95,
        "kind": "claim",
        "kb_status": "quarantined",
    },
    {"id": "c4", "text": "Weakly related text.", "score": 0.1, "kind": "claim"},
]


@asynccontextmanager
async def serving(servicer: AuditServicer) -> AsyncIterator[pb_grpc.AuditServiceStub]:
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield pb_grpc.AuditServiceStub(channel)
    finally:
        await channel.close()
        await server.stop(grace=None)


# ---------------------------------------------------------------------------
# The RPC
# ---------------------------------------------------------------------------


async def test_knowledge_disabled_is_failed_precondition() -> None:
    servicer = AuditServicer(audit_graph=None)
    assert servicer.knowledge_retriever is None

    async with serving(servicer) as stub:
        with pytest.raises(aio.AioRpcError) as exc_info:
            await stub.RetrieveContext(
                pb.RetrieveContextRequest(query="refund window?"), timeout=RPC_TIMEOUT_S
            )

    assert exc_info.value.code() == grpc.StatusCode.FAILED_PRECONDITION
    assert exc_info.value.details() == "knowledge_disabled"


async def test_only_one_of_the_knowledge_services_is_still_disabled() -> None:
    assert AuditServicer(audit_graph=None, qdrant_store=FakeStore()).knowledge_retriever is None
    assert (
        AuditServicer(audit_graph=None, embedding_service=FakeEmbedder()).knowledge_retriever
        is None
    )


async def test_enabled_returns_knowledge_documents() -> None:
    store = FakeStore(POINTS)
    servicer = AuditServicer(audit_graph=None, embedding_service=FakeEmbedder(), qdrant_store=store)

    async with serving(servicer) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="How long is the refund window?", top_k=3),
            timeout=RPC_TIMEOUT_S,
        )

    assert response.index_state == INDEX_READY
    assert response.latency_ms >= 0
    # Quarantined claims are excluded; the low-score point is under min_score.
    assert [doc.id for doc in response.documents] == ["c1", "c2"]
    first, second = response.documents
    assert first.source == "knowledge"
    assert first.content == POINTS[0]["text"]
    assert first.relevance_score == pytest.approx(0.91)
    assert first.label == "refund-policy.md"
    assert dict(first.metadata) == {
        "source_doc_id": "refund-policy.md",
        "kind": "claim",
        "kb_status": "accepted",
    }
    assert not first.truncated
    assert second.label == "Shipping FAQ"
    assert store.searches[0]["top_k"] == 3
    assert store.searches[0]["score_threshold"] == pytest.approx(0.3)
    assert store.searches[0]["must_not"] == {"kb_status": "quarantined"}


async def test_request_limits_are_passed_to_the_search() -> None:
    store = FakeStore(POINTS)
    servicer = AuditServicer(audit_graph=None, embedding_service=FakeEmbedder(), qdrant_store=store)

    async with serving(servicer) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="refunds", top_k=1, min_score=0.6),
            timeout=RPC_TIMEOUT_S,
        )

    assert [doc.id for doc in response.documents] == ["c1"]
    assert store.searches[0]["top_k"] == 1
    assert store.searches[0]["score_threshold"] == pytest.approx(0.6)


async def test_empty_index_and_no_hits_are_told_apart() -> None:
    empty = AuditServicer(
        audit_graph=None, embedding_service=FakeEmbedder(), qdrant_store=FakeStore([])
    )
    no_hits = AuditServicer(
        audit_graph=None,
        embedding_service=FakeEmbedder(),
        qdrant_store=FakeStore([POINTS[3]]),
    )

    async with serving(empty) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="refunds"), timeout=RPC_TIMEOUT_S
        )
    assert response.index_state == INDEX_EMPTY
    assert list(response.documents) == []

    async with serving(no_hits) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="refunds"), timeout=RPC_TIMEOUT_S
        )
    assert response.index_state == INDEX_READY
    assert list(response.documents) == []


async def test_a_request_without_text_is_invalid() -> None:
    servicer = AuditServicer(
        audit_graph=None, embedding_service=FakeEmbedder(), qdrant_store=FakeStore(POINTS)
    )

    async with serving(servicer) as stub:
        with pytest.raises(aio.AioRpcError) as exc_info:
            await stub.RetrieveContext(
                pb.RetrieveContextRequest(query="   ", response=""), timeout=RPC_TIMEOUT_S
            )

    assert exc_info.value.code() == grpc.StatusCode.INVALID_ARGUMENT
    assert exc_info.value.details() == "query_required"


async def test_a_search_error_is_reported_as_index_state_error() -> None:
    store = FakeStore(POINTS)
    store.error = ConnectionError("qdrant down")
    servicer = AuditServicer(audit_graph=None, embedding_service=FakeEmbedder(), qdrant_store=store)

    async with serving(servicer) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="refunds"), timeout=RPC_TIMEOUT_S
        )

    assert response.index_state == INDEX_ERROR
    assert list(response.documents) == []


async def test_an_injected_retriever_is_used() -> None:
    retriever = KnowledgeRetriever(FakeEmbedder(), FakeStore(POINTS), default_top_k=1)
    servicer = AuditServicer(audit_graph=None, knowledge_retriever=retriever)

    async with serving(servicer) as stub:
        response = await stub.RetrieveContext(
            pb.RetrieveContextRequest(query="refunds"), timeout=RPC_TIMEOUT_S
        )

    assert [doc.id for doc in response.documents] == ["c1"]


# ---------------------------------------------------------------------------
# Request handling
# ---------------------------------------------------------------------------


def test_query_text_adds_the_first_three_response_sentences() -> None:
    text = query_text(
        "  What is the  refund window? ",
        "It is 30 days. Shipping is free! Returns need a receipt? Nothing else. More.",
    )

    assert text == (
        "What is the refund window?\n" "It is 30 days. Shipping is free! Returns need a receipt?"
    )
    assert first_sentences("") == ""
    assert first_sentences("One. Two.", count=0) == ""


def test_build_query_applies_defaults_and_caps() -> None:
    retriever = knowledge_retriever_for(FakeEmbedder(), FakeStore(), default_min_score=0.4)
    assert retriever is not None

    defaults = retriever.build_query("refunds")
    assert (defaults.top_k, defaults.max_chars) == (DEFAULT_TOP_K, DEFAULT_MAX_CHARS)
    assert defaults.min_score == pytest.approx(0.4)
    capped = retriever.build_query("refunds", top_k=500, max_chars=100, min_score=7.0)
    assert (capped.top_k, capped.max_chars, capped.min_score) == (MAX_TOP_K, 100, 1.0)
    assert retriever.build_query("", response="Only the answer.").text == "Only the answer."
    with pytest.raises(ValueError, match="query_required"):
        retriever.build_query(" ", response="  ")
    assert knowledge_retriever_for(None, FakeStore()) is None


def test_pack_documents_fits_the_character_budget() -> None:
    alpha = " ".join(["Alpha"] * 10)
    long_text = "x" * 500
    hits = (
        Hit("a", alpha, 0.9, {}),
        Hit("dup", f"  {alpha}\n", 0.8, {}),
        Hit("b", long_text, 0.7, {"doc_id": "b.md"}),
        Hit("c", "Gamma", 0.6, {}),
    )

    whole = pack_documents(hits, 10_000)
    assert [doc.id for doc in whole] == ["a", "b", "c"]  # the duplicate text is dropped
    assert not any(doc.truncated for doc in whole)

    cut = pack_documents(hits, len(alpha) + MIN_TRUNCATED_CHARS + 50)
    assert [doc.id for doc in cut] == ["a", "b"]
    assert cut[1].truncated and len(cut[1].content) == MIN_TRUNCATED_CHARS + 50
    assert cut[1].label == "b.md"

    # Too little budget left for a useful cut: the rest is dropped.
    assert [doc.id for doc in pack_documents(hits, len(alpha) + 10)] == ["a"]


# ---------------------------------------------------------------------------
# Executor and caches (C-M24)
# ---------------------------------------------------------------------------


async def test_identical_queries_hit_the_result_cache() -> None:
    embedder, store = FakeEmbedder(), FakeStore(POINTS)
    retriever = KnowledgeRetriever(embedder, store)
    query = retriever.build_query("refund window")

    first = await retriever.retrieve(query)
    second = await retriever.retrieve(query)

    assert not first.cached and second.cached
    assert [doc.id for doc in second.documents] == [doc.id for doc in first.documents]
    assert len(store.searches) == 1 and len(embedder.calls) == 1


async def test_query_vectors_are_cached_across_limits() -> None:
    embedder, store = FakeEmbedder(), FakeStore(POINTS)
    retriever = KnowledgeRetriever(embedder, store)

    await retriever.retrieve(retriever.build_query("refund window", top_k=1))
    await retriever.retrieve(retriever.build_query("refund window", top_k=2))

    assert len(store.searches) == 2  # a different result key
    assert len(embedder.calls) == 1  # the same query vector


async def test_results_expire_and_invalidate_drops_them() -> None:
    clock = FakeClock()
    store = FakeStore(POINTS)
    retriever = KnowledgeRetriever(FakeEmbedder(), store, result_ttl_s=60.0, clock=clock)
    query = retriever.build_query("refund window")

    await retriever.retrieve(query)
    clock.now += 30
    assert (await retriever.retrieve(query)).cached
    clock.now += 31
    assert not (await retriever.retrieve(query)).cached
    retriever.invalidate()
    assert not (await retriever.retrieve(query)).cached
    assert len(store.searches) == 3


class IngestingStore(FakeStore):
    """A FakeStore that IngestDocuments can write to."""

    async def aensure_collection(self) -> None:
        return None


class FakeIngestor:
    """Adds each ingested document to the store as an accepted claim."""

    def __init__(self, store: FakeStore) -> None:
        self.store = store

    async def ingest_documents(self, documents: List[Dict[str, Any]]) -> Any:
        for number, document in enumerate(documents):
            self.store.points.append(
                {
                    "id": f"new{number}",
                    "text": document["content"],
                    "score": 0.99,
                    "kind": "claim",
                    "kb_status": "accepted",
                }
            )
        return SimpleNamespace(
            documents=len(documents),
            accepted=len(documents),
            quarantined=0,
            conflicts_detected=0,
            claim_results=[],
        )


async def test_an_ingest_drops_the_cached_results() -> None:
    # A knowledge-base change through IngestDocuments must be visible to the
    # next RetrieveContext at once, not after the 60 s result cache expires.
    store = IngestingStore(POINTS)
    servicer = AuditServicer(
        audit_graph=None,
        embedding_service=FakeEmbedder(),
        qdrant_store=store,
        ingestor=FakeIngestor(store),
    )
    request = pb.RetrieveContextRequest(query="How long is the refund window?")

    async with serving(servicer) as stub:
        before = await stub.RetrieveContext(request, timeout=RPC_TIMEOUT_S)
        ingested = await stub.IngestDocuments(
            pb.IngestRequest(documents=[pb.ContextDocument(id="d9", content="Refunds: 45 days.")]),
            timeout=RPC_TIMEOUT_S,
        )
        after = await stub.RetrieveContext(request, timeout=RPC_TIMEOUT_S)

    assert ingested.status == "success"
    assert [doc.id for doc in before.documents] == ["c1", "c2"]
    assert [doc.id for doc in after.documents] == ["c1", "c2", "new0"]
    assert len(store.searches) == 2


async def test_errors_are_not_cached() -> None:
    store = FakeStore(POINTS)
    store.error = RuntimeError("boom")
    retriever = KnowledgeRetriever(FakeEmbedder(), store)
    query = retriever.build_query("refund window")

    assert (await retriever.retrieve(query)).index_state == INDEX_ERROR
    store.error = None
    outcome = await retriever.retrieve(query)

    assert outcome.index_state == INDEX_READY and not outcome.cached
    assert len(store.searches) == 2


async def test_retrieval_does_not_wait_for_the_ingest_embedding_thread() -> None:
    embedder = FakeEmbedder()
    retriever = KnowledgeRetriever(embedder, FakeStore(POINTS))
    ingest_running = threading.Event()
    release_ingest = threading.Event()

    def long_ingest() -> None:
        ingest_running.set()
        release_ingest.wait(timeout=5)

    ingest = embedder.ingest_executor.submit(long_ingest)
    assert ingest_running.wait(timeout=5)
    try:
        outcome = await asyncio.wait_for(
            retriever.retrieve(retriever.build_query("refund window")), timeout=2
        )
    finally:
        release_ingest.set()
        ingest.result(timeout=5)
        embedder.ingest_executor.shutdown()
        retriever.close()

    assert outcome.index_state == INDEX_READY
    assert embedder.threads and embedder.threads[0].startswith("kb-retrieve")


async def test_concurrent_identical_queries_share_one_search() -> None:
    store = FakeStore(POINTS)
    store.gate = threading.Event()
    retriever = KnowledgeRetriever(FakeEmbedder(), store)
    query = retriever.build_query("refund window")

    tasks = [asyncio.create_task(retriever.retrieve(query)) for _ in range(3)]
    await asyncio.sleep(0.05)
    store.gate.set()
    outcomes = await asyncio.gather(*tasks)

    assert len(store.searches) == 1
    assert all([doc.id for doc in outcome.documents] == ["c1", "c2"] for outcome in outcomes)


async def test_a_search_past_the_deadline_still_fills_the_cache() -> None:
    store = FakeStore(POINTS)
    store.gate = threading.Event()
    retriever = KnowledgeRetriever(FakeEmbedder(), store)
    query = retriever.build_query("refund window")

    with pytest.raises(TimeoutError):
        await retriever.retrieve(query, timeout_s=0.01)
    store.gate.set()
    for _ in range(100):  # let the executor finish and the callback run
        await asyncio.sleep(0.01)
        if len(retriever._results):
            break

    outcome = await retriever.retrieve(query)
    assert outcome.cached and [doc.id for doc in outcome.documents] == ["c1", "c2"]
    assert len(store.searches) == 1


async def test_invalidate_during_a_search_keeps_its_result_out_of_the_cache() -> None:
    store = FakeStore(POINTS)
    store.gate = threading.Event()
    retriever = KnowledgeRetriever(FakeEmbedder(), store)
    query = retriever.build_query("refund window")

    task = asyncio.create_task(retriever.retrieve(query))
    await asyncio.sleep(0.05)
    retriever.invalidate()
    store.gate.set()
    await task

    assert not (await retriever.retrieve(query)).cached


def test_lru_cache_evicts_least_recently_used_and_expires() -> None:
    clock = FakeClock()
    cache: LruCache[str, int] = LruCache(2, ttl_s=10.0, clock=clock)
    cache.put("a", 1)
    cache.put("b", 2)
    assert cache.get("a") == 1  # a is now the most recently used
    cache.put("c", 3)
    assert cache.get("b") is None and cache.get("a") == 1 and cache.get("c") == 3
    clock.now += 11
    assert cache.get("a") is None and len(cache) == 1
    cache.clear()
    assert len(cache) == 0
    with pytest.raises(ValueError):
        LruCache(0)
