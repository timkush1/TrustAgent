"""
Decomposition windows for long answers (D14).

The decomposer used to cut every answer at 8,000 characters, silently dropping
the claims of the rest. An answer longer than `max_chars` is now decomposed in
windows instead:

- split at paragraph boundaries (blank lines) and packed greedily into
  consecutive windows of at most `max_chars` code points;
- a paragraph longer than that is split at sentence ends, and a sentence
  longer than that is cut hard.

Windows are contiguous slices of the answer (`answer[start:start + len(text)]`),
so a span found inside a window becomes a span of the answer by adding the
window's offset. Whitespace-only windows are dropped.
"""

import re
from dataclasses import dataclass
from typing import List

# Most code points per decomposition window (SPEC section 3.2, decomposer v2).
DEFAULT_WINDOW_CHARS = 6000

_PARAGRAPH_BREAK = re.compile(r"\n[ \t\r\f\v]*\n\s*")
_SENTENCE_BREAK = re.compile(r"(?<=[.!?])\s+")


@dataclass(frozen=True)
class Window:
    """A slice of the answer: `text == answer[start:end]` (code points)."""

    start: int
    text: str

    @property
    def end(self) -> int:
        return self.start + len(self.text)


def _cut_points(text: str, pattern: "re.Pattern[str]", start: int, end: int) -> List[int]:
    """Positions in (start, end) just after each match of `pattern` in text[start:end]."""
    return [
        match.end() for match in pattern.finditer(text, start, end) if start < match.end() < end
    ]


def _units(text: str, max_chars: int) -> List[int]:
    """
    Boundaries (ascending, starting at 0 and ending at len(text)) of pieces no
    longer than `max_chars`: paragraphs, else sentences, else hard cuts.
    """
    bounds = [0, *_cut_points(text, _PARAGRAPH_BREAK, 0, len(text)), len(text)]
    result = [0]
    for start, end in zip(bounds, bounds[1:]):
        if end - start <= max_chars:
            result.append(end)
            continue
        sentence_bounds = [start, *_cut_points(text, _SENTENCE_BREAK, start, end), end]
        for sentence_start, sentence_end in zip(sentence_bounds, sentence_bounds[1:]):
            position = sentence_start
            while sentence_end - position > max_chars:
                position += max_chars
                result.append(position)
            result.append(sentence_end)
    return result


def split_windows(text: str, max_chars: int = DEFAULT_WINDOW_CHARS) -> List[Window]:
    """
    Split `text` into decomposition windows of at most `max_chars` code points.

    Text that fits is one window; whitespace-only text has none.
    """
    if max_chars < 1:
        raise ValueError("max_chars must be at least 1")
    if not text.strip():
        return []
    if len(text) <= max_chars:
        return [Window(0, text)]

    bounds = _units(text, max_chars)
    windows: List[Window] = []
    window_start = 0
    previous = 0
    for bound in bounds[1:]:
        if bound - window_start > max_chars and previous > window_start:
            windows.append(Window(window_start, text[window_start:previous]))
            window_start = previous
        previous = bound
    windows.append(Window(window_start, text[window_start:]))
    return [window for window in windows if window.text.strip()]
