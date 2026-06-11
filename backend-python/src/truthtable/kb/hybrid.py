"""
Hybrid retrieval: BM25 lexical search fused with dense vector search via
Reciprocal Rank Fusion (RRF).

Dense embeddings excel at paraphrase ("car" ~ "automobile") but miss exact
terms (model numbers, names, dates); BM25 is the opposite. RRF combines both
rankings without score normalization:

    rrf_score(d) = sum over rankings r of  1 / (k + rank_r(d))

The BM25 index is tiny (claim texts only) and rebuilt lazily from the vector
store whenever ingestion marks it dirty.
"""

import logging
import math
import re
from collections import Counter
from typing import Any, Dict, List, Optional, Sequence

logger = logging.getLogger(__name__)

_TOKEN = re.compile(r"[a-z0-9]+")

# Standard RRF damping constant (Cormack et al., 2009).
RRF_K = 60


def tokenize(text: str) -> List[str]:
    return _TOKEN.findall(text.lower())


def rrf_fuse(rankings: Sequence[Sequence[str]], k: int = RRF_K) -> List[str]:
    """
    Fuse ranked id lists with Reciprocal Rank Fusion.

    Args:
        rankings: One ordered id list per retrieval system (best first).
        k: Damping constant; higher = less top-rank dominance.

    Returns:
        Ids ordered by fused score (best first).
    """
    scores: Dict[str, float] = {}
    for ranking in rankings:
        for rank, doc_id in enumerate(ranking):
            scores[doc_id] = scores.get(doc_id, 0.0) + 1.0 / (k + rank + 1)
    return sorted(scores, key=lambda doc_id: scores[doc_id], reverse=True)


class BM25Index:
    """
    Minimal BM25 (Okapi) index over the KB claim texts.

    Implemented directly (~40 lines) rather than pulling in rank-bm25: the
    corpus is small (claims, not documents), and the implementation is the
    standard formula with k1/b defaults.
    """

    def __init__(self, k1: float = 1.5, b: float = 0.75):
        self.k1 = k1
        self.b = b
        self._ids: List[str] = []
        self._token_counts: List[Counter] = []
        self._doc_lengths: List[int] = []
        self._doc_freq: Counter = Counter()
        self._avg_length = 0.0

    def build(self, corpus: Dict[str, str]) -> None:
        """Index {doc_id: text}. Replaces any previous index."""
        self._ids = list(corpus.keys())
        self._token_counts = []
        self._doc_lengths = []
        self._doc_freq = Counter()

        for doc_id in self._ids:
            tokens = tokenize(corpus[doc_id])
            counts = Counter(tokens)
            self._token_counts.append(counts)
            self._doc_lengths.append(len(tokens))
            for token in counts:
                self._doc_freq[token] += 1

        self._avg_length = (
            sum(self._doc_lengths) / len(self._doc_lengths) if self._doc_lengths else 0.0
        )
        logger.debug(f"BM25 index built over {len(self._ids)} claims")

    def __len__(self) -> int:
        return len(self._ids)

    def search(self, query: str, top_k: int = 10) -> List[str]:
        """Return doc ids ranked by BM25 score (best first, zero-score dropped)."""
        if not self._ids:
            return []

        query_tokens = tokenize(query)
        n_docs = len(self._ids)
        scores = [0.0] * n_docs

        for token in query_tokens:
            doc_freq = self._doc_freq.get(token)
            if not doc_freq:
                continue
            idf = math.log(1 + (n_docs - doc_freq + 0.5) / (doc_freq + 0.5))
            for i in range(n_docs):
                term_freq = self._token_counts[i].get(token, 0)
                if term_freq == 0:
                    continue
                length_norm = 1 - self.b + self.b * self._doc_lengths[i] / (self._avg_length or 1)
                scores[i] += idf * (term_freq * (self.k1 + 1)) / (term_freq + self.k1 * length_norm)

        ranked = sorted(range(n_docs), key=lambda i: scores[i], reverse=True)
        return [self._ids[i] for i in ranked[:top_k] if scores[i] > 0]


class HybridClaimRetriever:
    """
    Dense + BM25 retrieval over accepted KB claims, fused with RRF.

    The BM25 side indexes claim texts pulled from the vector store; ingestion
    calls `mark_dirty()` and the index is rebuilt lazily on next use.
    Quarantined claims are excluded from both retrieval paths.
    """

    def __init__(self, embedding_service: Any, qdrant_store: Any, top_k: int = 10):
        self.embedding_service = embedding_service
        self.qdrant_store = qdrant_store
        self.top_k = top_k
        self._bm25 = BM25Index()
        self._texts: Dict[str, str] = {}
        self._dirty = True

    def mark_dirty(self) -> None:
        self._dirty = True

    def _ensure_index(self) -> None:
        if not self._dirty:
            return
        points = self.qdrant_store.scroll_points(must={"kind": "claim", "kb_status": "accepted"})
        self._texts = {p["id"]: p.get("text", "") for p in points if p.get("text")}
        self._bm25.build(self._texts)
        self._dirty = False
        logger.info(f"Hybrid retriever index rebuilt ({len(self._texts)} accepted claims)")

    def retrieve(self, query: str, top_k: Optional[int] = None) -> List[str]:
        """Return fused claim texts for the query (best first)."""
        self._ensure_index()
        limit = top_k or self.top_k

        vector = self.embedding_service.embed([query])[0]
        dense = self.qdrant_store.search_filtered(
            query_vector=list(vector),
            top_k=limit,
            score_threshold=0.3,
            must={"kind": "claim"},
            must_not={"kb_status": "quarantined"},
        )
        dense_ids = [d["id"] for d in dense]
        dense_texts = {d["id"]: d.get("text", "") for d in dense}

        sparse_ids = self._bm25.search(query, top_k=limit)

        fused = rrf_fuse([dense_ids, sparse_ids])
        results = []
        for doc_id in fused[:limit]:
            text = dense_texts.get(doc_id) or self._texts.get(doc_id, "")
            if text:
                results.append(text)
        return results
