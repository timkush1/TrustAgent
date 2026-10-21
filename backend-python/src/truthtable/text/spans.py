"""
Claim spans: where in the audited answer each claim is asserted.

The decomposer asks the judge for a `quote` per claim (the shortest exact
substring of the answer that asserts it). This module locates that quote in
the original answer and reports the span the dashboard highlights.

Offsets (SPEC section 2.1, a contract with Go and the dashboard) are UTF-16
code units, half-open `[start, end)`, because that is what JavaScript
`String.prototype.slice` uses; `-1` means unaligned. A character outside the
Basic Multilingual Plane (most emoji) is two code units; CJK characters and
combining marks are one each.

Alignment tries, in order (the first hit wins):

1. **exact**: the quote (then the claim) as a substring of the sanitized
   answer, searching from the end of the previous match first so that a
   repeated phrase aligns in claim order, then from the start.
2. **normalized**: the same search after folding both sides with a character
   map: compatibility decomposition (NFKD, the per-character form of NFKC that
   keeps an exact index map), case folding, curly quotes and dashes to ASCII,
   and runs of whitespace collapsed to one space.
3. **fuzzy**: the answer is split into sentences (`[.!?]` followed by
   whitespace, or newlines); the sentence with the best token F1 over content
   words against the quote or the claim wins when its F1 is at least 0.5, and
   the span is that whole sentence.
4. **none**: `(-1, -1)`.

The judge saw the answer after `security.sanitize_text` removed hidden and
control characters, so its quotes are matched against that sanitized text and
mapped back to offsets in the original answer.
"""

import bisect
import re
import unicodedata
from collections import Counter
from dataclasses import dataclass
from typing import Iterable, List, Optional, Sequence, Tuple

# The characters sanitize_text strips before any text reaches a prompt (one
# definition, so the offsets here always match what the judge was shown).
from ..security import _HIDDEN_CHARS as HIDDEN_CHARS

UNALIGNED = -1

# span_method values (proto ClaimVerification.span_method).
EXACT = "exact"
NORMALIZED = "normalized"
FUZZY = "fuzzy"
NONE = "none"

# Minimum token F1 for a fuzzy (sentence-level) alignment.
FUZZY_MIN_F1 = 0.5

# Sentence boundaries for fuzzy alignment: whitespace after . ! or ?, or newlines.
_SENTENCE_BREAK = re.compile(r"(?<=[.!?])\s+|\n+")

_WORD = re.compile(r"\w+")

# Function words ignored when comparing a quote with a sentence.
_STOPWORDS = frozenset(
    """
    a an and are as at be been being but by did do does for from had has have he her
    his i if in into is it its of on or our she so than that the their them then there
    these they this those to was we were what when where which who will with would you
    your
    """.split()
)

# Typographic quotes and dashes, folded to their ASCII forms.
_PUNCTUATION_FOLD = str.maketrans(
    {
        "‘": "'",
        "’": "'",
        "‚": "'",
        "‛": "'",
        "′": "'",
        "“": '"',
        "”": '"',
        "„": '"',
        "‟": '"',
        "″": '"',
        "«": '"',
        "»": '"',
        "‐": "-",
        "‑": "-",
        "‒": "-",
        "–": "-",
        "—": "-",
        "―": "-",
        "−": "-",
    }
)


@dataclass(frozen=True)
class Span:
    """A claim's location in the answer: UTF-16 code units, half-open; -1 when unaligned."""

    start: int
    end: int
    method: str

    @property
    def aligned(self) -> bool:
        return self.start >= 0

    def shifted(self, offset: int) -> "Span":
        """The span moved right by `offset` code units (unaligned spans stay unaligned)."""
        if not self.aligned:
            return self
        return Span(self.start + offset, self.end + offset, self.method)


UNALIGNED_SPAN = Span(UNALIGNED, UNALIGNED, NONE)


def utf16_length(text: str) -> int:
    """Length of `text` in UTF-16 code units (a lone surrogate counts as one)."""
    return sum(2 if ord(char) > 0xFFFF else 1 for char in text)


def utf16_offsets(text: str) -> List[int]:
    """
    Prefix table: `offsets[i]` is the UTF-16 offset of code point `i`
    (`len(text) + 1` entries; the last is the UTF-16 length of `text`).
    """
    offsets = [0] * (len(text) + 1)
    total = 0
    for index, char in enumerate(text):
        offsets[index] = total
        total += 2 if ord(char) > 0xFFFF else 1
    offsets[len(text)] = total
    return offsets


def sanitize_with_map(text: str) -> Tuple[str, List[int]]:
    """
    The text sanitize_text shows the judge (hidden and control characters
    removed, no length cap) and an index map back to `text`.

    Returns:
        (clean, index_map): `clean[i] == text[index_map[i]]`, and
        `index_map[len(clean)] == len(text)`.
    """
    kept: List[int] = []
    position = 0
    for match in HIDDEN_CHARS.finditer(text):
        kept.extend(range(position, match.start()))
        position = match.end()
    kept.extend(range(position, len(text)))
    clean = "".join(text[index] for index in kept)
    kept.append(len(text))
    return clean, kept


def _fold(char: str) -> str:
    return unicodedata.normalize("NFKD", char).casefold().translate(_PUNCTUATION_FOLD)


def normalize_with_map(text: str) -> Tuple[str, List[int]]:
    """
    `text` folded for normalized matching, with the source index of every
    folded character (`sources[j]` is the index in `text` that produced
    folded character `j`). Leading whitespace is dropped and every run of
    whitespace becomes one space.
    """
    folded: List[str] = []
    sources: List[int] = []
    after_space = True
    for index, char in enumerate(text):
        for piece in _fold(char):
            if piece.isspace():
                if after_space:
                    continue
                piece, after_space = " ", True
            else:
                after_space = False
            folded.append(piece)
            sources.append(index)
    return "".join(folded), sources


def normalize(text: str) -> str:
    """`text` folded the way normalized matching compares it (see normalize_with_map)."""
    return normalize_with_map(text)[0].strip()


def content_tokens(text: str) -> List[str]:
    """Content words of `text` for fuzzy matching (folded, stopwords and 1-letter words dropped)."""
    return [
        token
        for token in _WORD.findall(normalize(text))
        if token not in _STOPWORDS and (len(token) > 1 or token.isdigit())
    ]


def token_f1(reference: Sequence[str], candidate: Sequence[str]) -> float:
    """Token-multiset F1 of `candidate` against `reference` (0.0 when either is empty)."""
    if not reference or not candidate:
        return 0.0
    overlap = sum((Counter(reference) & Counter(candidate)).values())
    if overlap == 0:
        return 0.0
    precision = overlap / len(candidate)
    recall = overlap / len(reference)
    return 2 * precision * recall / (precision + recall)


def sentence_spans(text: str) -> List[Tuple[int, int]]:
    """Code-point spans of the sentences of `text`, whitespace trimmed, empty ones dropped."""
    spans: List[Tuple[int, int]] = []

    def add(start: int, end: int) -> None:
        while start < end and text[start].isspace():
            start += 1
        while end > start and text[end - 1].isspace():
            end -= 1
        if start < end:
            spans.append((start, end))

    position = 0
    for match in _SENTENCE_BREAK.finditer(text):
        add(position, match.start())
        position = match.end()
    add(position, len(text))
    return spans


@dataclass(frozen=True)
class Alignment:
    """A located quote, in code points of the aligned text."""

    start: int
    end: int
    method: str


class QuoteAligner:
    """
    Locates the quotes of one text's claims, in claim order.

    `align` returns code-point offsets in the original text (for the caller to
    shift, e.g. by a decomposition window's offset); `span` converts them to
    the UTF-16 span of the wire format. The aligner remembers where the last
    exact or normalized match ended, so a phrase that occurs twice aligns to
    its first occurrence for the first claim and its second for the next.
    """

    def __init__(self, text: str):
        self.text = text
        self._clean, self._clean_map = sanitize_with_map(text)
        self._cursor = 0  # in clean-text code points
        self._folded: Optional[Tuple[str, List[int]]] = None
        self._sentences: Optional[List[Tuple[int, int, List[str]]]] = None
        self._utf16: Optional[List[int]] = None

    def align(self, quote: str, claim: str = "") -> Optional[Alignment]:
        """Locate `quote` (falling back to `claim`) in the text; None when it cannot."""
        candidates = _unique(term.strip() for term in (quote, claim))
        if not candidates or not self._clean:
            return None
        for find, method in ((self._find_exact, EXACT), (self._find_normalized, NORMALIZED)):
            for term in candidates:
                found = find(term)
                if found is not None:
                    start, end = found
                    self._cursor = end
                    return self._original(start, end, method)
        found = self._find_fuzzy(candidates)
        if found is not None:
            return self._original(found[0], found[1], FUZZY)
        return None

    def span(self, quote: str, claim: str = "") -> Span:
        """The UTF-16 span of `quote` (or `claim`) in the text; UNALIGNED_SPAN when not found."""
        alignment = self.align(quote, claim)
        if alignment is None:
            return UNALIGNED_SPAN
        return self.to_span(alignment)

    def to_span(self, alignment: Alignment, offset: int = 0) -> Span:
        """
        The UTF-16 span of an alignment of this text, when the text itself
        starts `offset` UTF-16 code units into a longer answer.
        """
        if self._utf16 is None:
            self._utf16 = utf16_offsets(self.text)
        return Span(
            self._utf16[alignment.start] + offset,
            self._utf16[alignment.end] + offset,
            alignment.method,
        )

    # -- matching over the clean text -------------------------------------------------

    def _find_exact(self, term: str) -> Optional[Tuple[int, int]]:
        index = self._clean.find(term, self._cursor)
        if index < 0:
            index = self._clean.find(term)
        if index < 0:
            return None
        return index, index + len(term)

    def _find_normalized(self, term: str) -> Optional[Tuple[int, int]]:
        target = normalize(term)
        if not target:
            return None
        if self._folded is None:
            self._folded = normalize_with_map(self._clean)
        folded, sources = self._folded
        start_from = bisect.bisect_left(sources, self._cursor)
        index = folded.find(target, start_from)
        if index < 0:
            index = folded.find(target)
        if index < 0:
            return None
        return sources[index], sources[index + len(target) - 1] + 1

    def _find_fuzzy(self, candidates: Sequence[str]) -> Optional[Tuple[int, int]]:
        if self._sentences is None:
            self._sentences = [
                (start, end, content_tokens(self._clean[start:end]))
                for start, end in sentence_spans(self._clean)
            ]
        references = [tokens for tokens in (content_tokens(term) for term in candidates) if tokens]
        best: Optional[Tuple[int, int]] = None
        best_score = 0.0
        for start, end, tokens in self._sentences:
            score = max((token_f1(reference, tokens) for reference in references), default=0.0)
            if score > best_score:
                best, best_score = (start, end), score
        if best is None or best_score < FUZZY_MIN_F1:
            return None
        return best

    def _original(self, start: int, end: int, method: str) -> Alignment:
        """Map a clean-text match back to code points of the original text."""
        original_start = self._clean_map[start]
        original_end = self._clean_map[end - 1] + 1 if end > start else original_start
        return Alignment(original_start, original_end, method)


def align_quote(response: str, quote: str, claim: str = "") -> Span:
    """The UTF-16 span of one claim's `quote` (or `claim`) in `response`."""
    return QuoteAligner(response).span(quote, claim)


def _unique(terms: Iterable[str]) -> List[str]:
    seen: List[str] = []
    for term in terms:
        if term and term not in seen:
            seen.append(term)
    return seen
