"""
Okapi BM25 over a handful of evidence documents (per-claim context, D13).

The verifier ranks an audit's evidence documents for each claim (SPEC section
3.2, "per-claim context comes from BM25 over the documents"). The corpus is one
audit's evidence (at most 20 documents), so the index is built per audit and
held in memory.

This is deliberately separate from `truthtable.kb.hybrid.BM25Index`: the
knowledge package belongs to the optional add-on and the engine must import
without it (test_lazy_imports), and the verifier needs the scores (a claim
with no positive score has no hit and falls back to all documents), not only
the ranked ids.

Tokens are runs of Unicode letters and digits, case-folded; a small English
stop-word list is dropped from queries and documents so that a claim sharing
only "the" or "is" with a document is not a hit.
"""

import math
import re
from collections import Counter
from typing import Dict, FrozenSet, List, Sequence, Tuple

# Standard Okapi BM25 parameters.
DEFAULT_K1 = 1.5
DEFAULT_B = 0.75

# Letters and digits (any script); underscores and punctuation separate tokens.
_TOKEN = re.compile(r"[^\W_]+")

STOPWORDS: FrozenSet[str] = frozenset(
    """
    a about above after again against all am an and any are as at be because been before
    being below between both but by can could did do does doing down during each few for
    from further had has have having he her here hers herself him himself his how i if in
    into is it its itself just me more most my myself no nor not now of off on once only
    or other our ours ourselves out over own same she should so some such than that the
    their theirs them themselves then there these they this those through to too under
    until up very was we were what when where which while who whom why will with would
    you your yours yourself yourselves
    """.split()
)


def tokenize(text: str, *, drop_stopwords: bool = True) -> List[str]:
    """Case-folded letter/digit runs of `text`, without stop words by default."""
    tokens = [token.casefold() for token in _TOKEN.findall(text)]
    if drop_stopwords:
        return [token for token in tokens if token not in STOPWORDS]
    return tokens


class BM25:
    """
    A BM25 index over `documents` (positions are the document ids).

    Args:
        documents: The corpus, in order.
        k1: Term-frequency saturation.
        b: Length normalisation (0 = none, 1 = full).
    """

    def __init__(
        self, documents: Sequence[str], *, k1: float = DEFAULT_K1, b: float = DEFAULT_B
    ) -> None:
        self.k1 = k1
        self.b = b
        self._counts: List[Counter[str]] = [Counter(tokenize(doc)) for doc in documents]
        self._lengths: List[int] = [sum(counts.values()) for counts in self._counts]
        self._avg_length = sum(self._lengths) / len(self._lengths) if self._lengths else 0.0
        self._doc_freq: Dict[str, int] = {}
        for counts in self._counts:
            for token in counts:
                self._doc_freq[token] = self._doc_freq.get(token, 0) + 1

    def __len__(self) -> int:
        return len(self._counts)

    def _idf(self, token: str) -> float:
        doc_freq = self._doc_freq.get(token, 0)
        n_docs = len(self._counts)
        # The "+1" form keeps every idf positive, even for a token in every document.
        return math.log(1.0 + (n_docs - doc_freq + 0.5) / (doc_freq + 0.5))

    def scores(self, query: str) -> List[float]:
        """The BM25 score of every document for `query` (0 when nothing matches)."""
        scores = [0.0] * len(self._counts)
        query_tokens = set(tokenize(query))
        for token in sorted(query_tokens):  # sorted: a deterministic summation order
            if token not in self._doc_freq:
                continue
            idf = self._idf(token)
            for index, counts in enumerate(self._counts):
                term_freq = counts.get(token, 0)
                if not term_freq:
                    continue
                norm = 1.0 - self.b + self.b * self._lengths[index] / (self._avg_length or 1.0)
                scores[index] += idf * term_freq * (self.k1 + 1.0) / (term_freq + self.k1 * norm)
        return scores

    def top(self, query: str, k: int) -> List[Tuple[int, float]]:
        """
        The (index, score) of the best `k` documents with a positive score,
        best first; ties keep document order.
        """
        if k < 1:
            return []
        ranked = sorted(
            ((index, score) for index, score in enumerate(self.scores(query)) if score > 0.0),
            key=lambda item: (-item[1], item[0]),
        )
        return ranked[:k]
