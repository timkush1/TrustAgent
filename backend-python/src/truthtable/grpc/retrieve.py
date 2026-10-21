"""
RetrieveContext: knowledge-base evidence for the gate (SPEC sections 2.2, 7.1).

Go calls `RetrieveContext` concurrently with the upstream request, using the
query only, under a short deadline (TRUTHTABLE_KNOWLEDGE_DEADLINE, 150 ms by
default). The documents come back as evidence source `knowledge`.

C-M24: the query must not wait behind knowledge-base ingestion, which encodes
whole documents on the embedding service's own single-worker thread. So:

- retrieval runs on a dedicated executor owned by `KnowledgeRetriever` (it
  calls the blocking `embed` and the Qdrant search itself, never the shared
  `aembed` thread);
- query vectors are kept in an LRU cache (embeddings are deterministic), and
  whole results in a short-lived LRU cache (the knowledge base changes on
  ingest; `invalidate()` drops them at once);
- identical concurrent queries share one search (single flight), and a search
  that outlives its caller's deadline still fills the cache for the next one.

Retrieval is dense (cosine similarity against accepted knowledge-base claims
and legacy chunks; quarantined claims are excluded), so `min_score` means the
same thing as TRUTHTABLE_RETRIEVER_SCORE_THRESHOLD. `max_chars` is the budget
of the whole response: documents are added best first, the one that crosses
the budget is cut (and marked truncated), and the rest are dropped.

This module must not import the knowledge packages (test_lazy_imports): the
embedding service and the store are typed by the small protocols below.
"""

import asyncio
import logging
import math
import re
import threading
import time
from collections import OrderedDict
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import (
    Any,
    Callable,
    Dict,
    Generic,
    Hashable,
    List,
    Mapping,
    Optional,
    Protocol,
    Tuple,
    TypeVar,
)

logger = logging.getLogger(__name__)

# Request defaults for zero fields (proto3 cannot tell "unset" from 0). They
# match the policy default `evidence.knowledge` {top_k: 5, min_score: 0.3}.
DEFAULT_TOP_K = 5
DEFAULT_MIN_SCORE = 0.3
# AuditRequest.context carries at most 20 documents of at most 20,000 characters.
MAX_TOP_K = 20
MAX_DOC_CHARS = 20_000
DEFAULT_MAX_CHARS = 20_000
# A document cut to fit the budget keeps at least this much, else it is dropped.
MIN_TRUNCATED_CHARS = 200

# Sentences of the response added to the query when the caller sends one.
RESPONSE_SENTENCES = 3
# The embedded text is capped (MiniLM reads 256 word pieces anyway), which
# also bounds the cache keys.
MAX_QUERY_CHARS = 2_000

# Cache sizes and lifetimes.
VECTOR_CACHE_SIZE = 512
RESULT_CACHE_SIZE = 256
RESULT_CACHE_TTL_S = 60.0
COUNT_CACHE_TTL_S = 5.0

# RetrieveContextResponse.index_state values.
INDEX_READY = "ready"
INDEX_EMPTY = "empty"
INDEX_DISABLED = "disabled"
INDEX_ERROR = "error"

# FAILED_PRECONDITION details when the knowledge add-on is off.
KNOWLEDGE_DISABLED = "knowledge_disabled"
# ContextDocument.source of every returned document.
KNOWLEDGE_SOURCE = "knowledge"

# Payload fields copied into ContextDocument.metadata (string values only).
_METADATA_KEYS = ("source_doc_id", "doc_id", "kind", "kb_status", "title", "source")
_MAX_METADATA_VALUE_CHARS = 256
# Payload fields tried, in order, for ContextDocument.label.
_LABEL_KEYS = ("title", "source_doc_id", "doc_id")

_SENTENCE_END = re.compile(r"(?<=[.!?])\s+")

K = TypeVar("K", bound=Hashable)
V = TypeVar("V")


class Embedder(Protocol):
    """The part of vectorstore.embeddings.EmbeddingService used here."""

    def embed(self, texts: List[str]) -> List[List[float]]:
        ...


class VectorStore(Protocol):
    """The part of vectorstore.qdrant_store.QdrantStore used here."""

    def search_filtered(
        self,
        query_vector: List[float],
        top_k: int = 5,
        score_threshold: float = 0.3,
        must: Optional[Dict[str, Any]] = None,
        must_not: Optional[Dict[str, Any]] = None,
    ) -> List[Dict[str, Any]]:
        ...

    def count(self) -> int:
        ...


class LruCache(Generic[K, V]):
    """
    A bounded, thread-safe LRU map with an optional time to live.

    Args:
        max_entries: Entries kept; the least recently used goes first.
        ttl_s: Seconds an entry stays valid; None keeps it until evicted.
        clock: Monotonic time source (tests pass a fake).
    """

    def __init__(
        self,
        max_entries: int,
        ttl_s: Optional[float] = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        if max_entries < 1:
            raise ValueError("max_entries must be at least 1")
        self.max_entries = max_entries
        self.ttl_s = ttl_s
        self._clock = clock
        self._entries: "OrderedDict[K, Tuple[float, V]]" = OrderedDict()
        self._lock = threading.Lock()

    def get(self, key: K) -> Optional[V]:
        with self._lock:
            entry = self._entries.get(key)
            if entry is None:
                return None
            stored_at, value = entry
            if self.ttl_s is not None and self._clock() - stored_at > self.ttl_s:
                del self._entries[key]
                return None
            self._entries.move_to_end(key)
            return value

    def put(self, key: K, value: V) -> None:
        with self._lock:
            self._entries[key] = (self._clock(), value)
            self._entries.move_to_end(key)
            while len(self._entries) > self.max_entries:
                self._entries.popitem(last=False)

    def clear(self) -> None:
        with self._lock:
            self._entries.clear()

    def __len__(self) -> int:
        with self._lock:
            return len(self._entries)


@dataclass(frozen=True)
class RetrieveQuery:
    """A validated RetrieveContext request."""

    text: str  # What is embedded: the query, plus the response's first sentences
    top_k: int
    max_chars: int
    min_score: float

    @property
    def cache_key(self) -> Tuple[str, int, float]:
        return (self.text, self.top_k, round(self.min_score, 6))


@dataclass(frozen=True)
class Hit:
    """One search result."""

    id: str
    text: str
    score: float
    payload: Mapping[str, Any]


# (hits, index_state) of one search, as cached.
_SearchResult = Tuple[Tuple[Hit, ...], str]


@dataclass(frozen=True)
class RetrievedDocument:
    """One returned evidence document (a ContextDocument with source `knowledge`)."""

    id: str
    content: str
    score: float
    label: str
    truncated: bool
    metadata: Mapping[str, str]


@dataclass(frozen=True)
class RetrieveOutcome:
    """What a retrieval produced."""

    documents: Tuple[RetrievedDocument, ...]
    index_state: str  # ready|empty|error
    cached: bool = False


def first_sentences(text: str, count: int = RESPONSE_SENTENCES) -> str:
    """The first `count` sentences of `text` (whitespace-collapsed)."""
    collapsed = " ".join(text.split())
    if not collapsed or count < 1:
        return ""
    return " ".join(_SENTENCE_END.split(collapsed)[:count])


def query_text(query: str, response: str = "") -> str:
    """The text to embed: the query, then the response's first sentences."""
    parts = [" ".join(query.split()), first_sentences(response)]
    return "\n".join(part for part in parts if part)[:MAX_QUERY_CHARS]


def _payload_metadata(payload: Mapping[str, Any]) -> Dict[str, str]:
    metadata: Dict[str, str] = {}
    for key in _METADATA_KEYS:
        value = payload.get(key)
        if isinstance(value, bool) or value is None:
            continue
        if isinstance(value, (str, int, float)):
            text = str(value)
            if text:
                metadata[key] = text[:_MAX_METADATA_VALUE_CHARS]
    return metadata


def _label(payload: Mapping[str, Any]) -> str:
    for key in _LABEL_KEYS:
        value = payload.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()[:_MAX_METADATA_VALUE_CHARS]
    return ""


def pack_documents(hits: Tuple[Hit, ...], max_chars: int) -> Tuple[RetrievedDocument, ...]:
    """
    The documents that fit `max_chars`, best first: whole documents while they
    fit (each capped at MAX_DOC_CHARS), then the next one cut to the remaining
    budget when at least MIN_TRUNCATED_CHARS remain. Duplicate texts are dropped.
    """
    documents: List[RetrievedDocument] = []
    seen: set[str] = set()
    remaining = max_chars
    for hit in hits:
        text = hit.text.strip()
        if not text or text in seen:
            continue
        if remaining <= 0:
            break
        truncated = len(text) > MAX_DOC_CHARS
        content = text[:MAX_DOC_CHARS]
        if len(content) > remaining:
            if remaining < MIN_TRUNCATED_CHARS:
                break
            content, truncated = content[:remaining], True
        seen.add(text)
        remaining -= len(content)
        documents.append(
            RetrievedDocument(
                id=hit.id,
                content=content,
                score=hit.score,
                label=_label(hit.payload),
                truncated=truncated,
                metadata=_payload_metadata(hit.payload),
            )
        )
    return tuple(documents)


def _finite_score(value: Any) -> float:
    try:
        score = float(value)
    except (TypeError, ValueError):
        return 0.0
    return score if math.isfinite(score) else 0.0


def _hits(results: List[Dict[str, Any]]) -> Tuple[Hit, ...]:
    hits: List[Hit] = []
    for result in results:
        text = result.get("text")
        if not isinstance(text, str) or not text.strip():
            continue
        payload = {key: value for key, value in result.items() if key not in ("text", "score")}
        hits.append(
            Hit(
                id=str(result.get("id", "")),
                text=text,
                score=_finite_score(result.get("score")),
                payload=payload,
            )
        )
    return tuple(hits)


class KnowledgeRetriever:
    """
    Knowledge-base retrieval for RetrieveContext (see the module docstring).

    Args:
        embedder: The embedding service (its blocking `embed` is called on this
            retriever's own executor).
        store: The vector store.
        default_top_k: top_k for a request that sends 0.
        default_min_score: min_score for a request that sends 0
            (TRUTHTABLE_RETRIEVER_SCORE_THRESHOLD).
        max_workers: Threads of the dedicated executor.
        vector_cache_size, result_cache_size, result_ttl_s, count_ttl_s: Caches.
        clock: Monotonic time source for the caches.
    """

    def __init__(
        self,
        embedder: Embedder,
        store: VectorStore,
        *,
        default_top_k: int = DEFAULT_TOP_K,
        default_min_score: float = DEFAULT_MIN_SCORE,
        max_workers: int = 1,
        vector_cache_size: int = VECTOR_CACHE_SIZE,
        result_cache_size: int = RESULT_CACHE_SIZE,
        result_ttl_s: float = RESULT_CACHE_TTL_S,
        count_ttl_s: float = COUNT_CACHE_TTL_S,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.embedder = embedder
        self.store = store
        self.default_top_k = min(max(1, default_top_k), MAX_TOP_K)
        self.default_min_score = min(max(0.0, default_min_score), 1.0)
        self._executor = ThreadPoolExecutor(
            max_workers=max(1, max_workers), thread_name_prefix="kb-retrieve"
        )
        self._vectors: LruCache[str, Tuple[float, ...]] = LruCache(vector_cache_size, clock=clock)
        self._results: LruCache[Tuple[str, int, float], _SearchResult] = LruCache(
            result_cache_size, ttl_s=result_ttl_s, clock=clock
        )
        self._counts: LruCache[str, int] = LruCache(1, ttl_s=count_ttl_s, clock=clock)
        self._inflight: Dict[Tuple[str, int, float], "asyncio.Future[_SearchResult]"] = {}
        # Bumped by invalidate(): a search started before it must not refill the cache.
        self._generation = 0

    def build_query(
        self,
        query: str,
        response: str = "",
        top_k: int = 0,
        max_chars: int = 0,
        min_score: float = 0,
    ) -> RetrieveQuery:
        """
        Validate a request (zero fields take the defaults).

        Raises:
            ValueError: If there is nothing to search for.
        """
        text = query_text(query, response)
        if not text:
            raise ValueError("query_required")
        score = float(min_score) if math.isfinite(min_score) else 0.0
        return RetrieveQuery(
            text=text,
            top_k=min(top_k, MAX_TOP_K) if top_k > 0 else self.default_top_k,
            max_chars=max_chars if max_chars > 0 else DEFAULT_MAX_CHARS,
            min_score=min(score, 1.0) if score > 0 else self.default_min_score,
        )

    def invalidate(self) -> None:
        """Forget cached results (call after the knowledge base changed)."""
        self._generation += 1
        self._results.clear()
        self._counts.clear()

    def close(self) -> None:
        """Stop the executor (a running search finishes in the background)."""
        self._executor.shutdown(wait=False, cancel_futures=True)

    def _vector(self, text: str) -> List[float]:
        cached = self._vectors.get(text)
        if cached is not None:
            return list(cached)
        vector = self.embedder.embed([text])[0]
        self._vectors.put(text, tuple(float(value) for value in vector))
        return list(vector)

    def _count(self) -> int:
        cached = self._counts.get("points")
        if cached is not None:
            return cached
        points = int(self.store.count())
        self._counts.put("points", points)
        return points

    def _search(self, query: RetrieveQuery) -> _SearchResult:
        """Blocking: embed (cached), search, and tell an empty index from no hits."""
        results = self.store.search_filtered(
            query_vector=self._vector(query.text),
            top_k=query.top_k,
            score_threshold=query.min_score,
            must_not={"kb_status": "quarantined"},
        )
        hits = _hits(results)
        if hits:
            return hits, INDEX_READY
        return hits, INDEX_EMPTY if self._count() == 0 else INDEX_READY

    def _start(self, query: RetrieveQuery) -> "asyncio.Future[_SearchResult]":
        """The search of `query`: the one in flight, or a new one on the executor."""
        key = query.cache_key
        running = self._inflight.get(key)
        if running is not None:
            return running
        loop = asyncio.get_running_loop()
        future = loop.run_in_executor(self._executor, self._search, query)
        generation = self._generation

        def _done(done: "asyncio.Future[_SearchResult]") -> None:
            self._inflight.pop(key, None)
            if done.cancelled() or done.exception() is not None:
                return
            if generation == self._generation:
                self._results.put(key, done.result())

        future.add_done_callback(_done)
        self._inflight[key] = future
        return future

    async def retrieve(
        self, query: RetrieveQuery, timeout_s: Optional[float] = None
    ) -> RetrieveOutcome:
        """
        Retrieve documents for `query`.

        A search error is reported as index_state `error` with no documents
        (Go records knowledge `error` and carries on without it).

        Raises:
            TimeoutError: If `timeout_s` passes first; the search keeps running
                and fills the cache.
        """
        cached = self._results.get(query.cache_key)
        if cached is not None:
            hits, state = cached
            return RetrieveOutcome(pack_documents(hits, query.max_chars), state, cached=True)
        future = self._start(query)
        try:
            if timeout_s is None:
                hits, state = await asyncio.shield(future)
            else:
                hits, state = await asyncio.wait_for(asyncio.shield(future), max(0.0, timeout_s))
        except (TimeoutError, asyncio.CancelledError):
            raise
        except Exception as exc:
            logger.error(f"Knowledge retrieval failed: {type(exc).__name__}: {exc}")
            return RetrieveOutcome((), INDEX_ERROR)
        return RetrieveOutcome(pack_documents(hits, query.max_chars), state)


def knowledge_retriever_for(
    embedding_service: Optional[Embedder],
    qdrant_store: Optional[VectorStore],
    *,
    default_min_score: float = DEFAULT_MIN_SCORE,
) -> Optional[KnowledgeRetriever]:
    """A retriever over the engine's knowledge add-on; None when it is off."""
    if embedding_service is None or qdrant_store is None:
        return None
    return KnowledgeRetriever(embedding_service, qdrant_store, default_min_score=default_min_score)
