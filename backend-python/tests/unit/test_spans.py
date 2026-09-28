"""
Claim spans (text/spans.py): UTF-16 code units, half-open, -1 when unaligned
(SPEC section 2.1), and the exact -> normalized -> fuzzy -> none ladder.

The oracle for every offset is the text encoded as UTF-16 (what JavaScript's
String.prototype.slice indexes), not the code under test.
"""

from typing import Tuple

import pytest

from truthtable.text.spans import (
    EXACT,
    FUZZY,
    NONE,
    NORMALIZED,
    UNALIGNED_SPAN,
    QuoteAligner,
    Span,
    align_quote,
    content_tokens,
    normalize,
    sanitize_with_map,
    sentence_spans,
    token_f1,
    utf16_length,
    utf16_offsets,
)
from truthtable.text.windows import DEFAULT_WINDOW_CHARS, Window, split_windows


def js_slice(text: str, start: int, end: int) -> str:
    """text.slice(start, end) in JavaScript: UTF-16 code-unit indexing."""
    units = text.encode("utf-16-le")
    return units[start * 2 : end * 2].decode("utf-16-le")


def utf16_index(text: str, needle: str) -> Tuple[int, int]:
    """Where `needle` is in `text`, in UTF-16 code units (the oracle)."""
    cp = text.index(needle)
    start = len(text[:cp].encode("utf-16-le")) // 2
    return start, start + len(needle.encode("utf-16-le")) // 2


@pytest.mark.parametrize(
    "text, quote",
    [
        ("Plain ASCII text. The quote is here.", "The quote is here"),
        # Astral-plane emoji are two UTF-16 code units each.
        ("\U0001F5FB\U0001F389 Mount Fuji is in Japan.", "Mount Fuji is in Japan"),
        ("Rocket \U0001F680 then the claim: water boils at 100 C.", "water boils at 100 C"),
        # CJK characters are one code unit each.
        ("富士山は日本にある。 Fuji is in Japan.", "Fuji is in Japan"),
        ("东京是日本的首都。", "是日本的首都"),
        # Combining marks are separate code points (and code units).
        ("Café au lait costs 3 euros.", "au lait costs 3 euros"),
        ("The néé claim is here.", "néé claim"),
        # Mixed: emoji inside the quote itself.
        ("Launch day \U0001F680 went well.", "day \U0001F680 went"),
    ],
)
def test_exact_spans_are_utf16_code_units(text: str, quote: str) -> None:
    span = align_quote(text, quote)

    assert span.method == EXACT
    assert (span.start, span.end) == utf16_index(text, quote)
    assert js_slice(text, span.start, span.end) == quote


def test_utf16_helpers() -> None:
    text = "a\U0001F600bé"
    assert utf16_length(text) == 5
    assert utf16_offsets(text) == [0, 1, 3, 4, 5]
    assert utf16_length("") == 0 and utf16_offsets("") == [0]
    # A lone surrogate (from a JSON "\ud83d") counts as one code unit.
    assert utf16_length("x\ud83dy") == 3


def test_hidden_characters_the_judge_never_saw_map_back_to_the_original() -> None:
    text = "Zero​width: The sky‍ is blue."
    clean, index_map = sanitize_with_map(text)

    assert clean == "Zerowidth: The sky is blue."
    assert index_map[-1] == len(text)
    assert all(clean[i] == text[index_map[i]] for i in range(len(clean)))

    span = align_quote(text, "The sky is blue")
    assert span.method == EXACT
    assert js_slice(text, span.start, span.end) == "The sky‍ is blue"


def test_normalized_alignment_folds_quotes_dashes_case_and_whitespace() -> None:
    text = "The Louvre’s pyramid — finished in 1989 — is  made of GLASS."

    span = align_quote(text, "the louvre's pyramid - finished in 1989 - is made of glass")

    assert span.method == NORMALIZED
    assert js_slice(text, span.start, span.end) == (
        "The Louvre’s pyramid — finished in 1989 — is  made of GLASS"
    )


def test_normalized_alignment_matches_composed_and_decomposed_accents() -> None:
    decomposed = "Où est le café? Le café est fermé."

    span = align_quote(decomposed, "Le café est fermé")

    assert span.method == NORMALIZED
    assert js_slice(decomposed, span.start, span.end) == "Le café est fermé"


def test_normalized_alignment_after_an_emoji() -> None:
    text = "\U0001F600 “Quoted” words here."

    span = align_quote(text, '"quoted" words')

    assert span.method == NORMALIZED
    assert (span.start, span.end) == (3, 17)  # the emoji and its space are 3 code units
    assert js_slice(text, span.start, span.end) == "“Quoted” words"


def test_fuzzy_alignment_is_the_best_sentence() -> None:
    text = "The glass pyramid was completed in 1989. It was designed by the architect Pei."

    span = align_quote(text, "the pyramid was designed by Pei", "Pei designed the pyramid.")

    assert span.method == FUZZY
    assert js_slice(text, span.start, span.end) == "It was designed by the architect Pei."


def test_fuzzy_alignment_needs_half_the_words() -> None:
    text = "Paris is lovely in spring. Rome has great food."

    assert align_quote(text, "Berlin has a large zoo") == UNALIGNED_SPAN
    assert align_quote(text, "", "") == UNALIGNED_SPAN
    assert align_quote("", "anything", "anything") == UNALIGNED_SPAN


def test_the_claim_is_used_when_the_quote_is_not_in_the_text() -> None:
    text = "Delhi is the capital of India."

    span = align_quote(text, "New Delhi", "Delhi is the capital of India")

    assert span.method == EXACT
    assert js_slice(text, span.start, span.end) == "Delhi is the capital of India"


def test_repeated_phrases_align_in_claim_order() -> None:
    text = "It is open. It is open. It is closed."
    aligner = QuoteAligner(text)

    first = aligner.span("It is open")
    second = aligner.span("It is open")
    third = aligner.span("It is open")  # no later occurrence: wraps to the first

    assert (first.start, second.start, third.start) == (0, 12, 0)


def test_alignment_offsets_are_code_points_and_spans_shift() -> None:
    text = "\U0001F600 abc"
    aligner = QuoteAligner(text)

    alignment = aligner.align("abc")

    assert alignment is not None and (alignment.start, alignment.end) == (2, 5)
    assert aligner.to_span(alignment) == Span(3, 6, EXACT)
    assert aligner.to_span(alignment, offset=10) == Span(13, 16, EXACT)
    assert Span(3, 6, EXACT).shifted(4) == Span(7, 10, EXACT)
    assert UNALIGNED_SPAN.shifted(4) == UNALIGNED_SPAN
    assert not UNALIGNED_SPAN.aligned and UNALIGNED_SPAN.method == NONE


def test_text_helpers() -> None:
    assert normalize("  A ‘B’  – C ") == "a 'b' - c"
    assert content_tokens("The Moon has 1 moon, and it is 3.5 km wide.") == [
        "moon",
        "1",
        "moon",
        "3",
        "5",
        "km",
        "wide",
    ]
    assert token_f1(["a", "b"], ["a", "b"]) == 1.0
    assert token_f1(["a", "b"], ["c"]) == 0.0
    assert token_f1([], ["a"]) == 0.0
    assert sentence_spans("One. Two!\n\nThree?  ") == [(0, 4), (5, 9), (11, 17)]


# ---------------------------------------------------------------------------
# Decomposition windows (text/windows.py)
# ---------------------------------------------------------------------------


def test_short_text_is_one_window() -> None:
    assert split_windows("Short answer.") == [Window(0, "Short answer.")]
    assert split_windows("   \n  ") == []
    assert DEFAULT_WINDOW_CHARS == 6000


def test_windows_split_at_paragraphs_and_cover_the_text() -> None:
    paragraph = "Sentence number one is here. " * 10  # 290 chars
    text = "\n\n".join([paragraph] * 12)

    windows = split_windows(text, max_chars=1000)

    assert len(windows) > 1
    assert all(len(window.text) <= 1000 for window in windows)
    assert "".join(window.text for window in windows) == text
    assert all(text[window.start : window.end] == window.text for window in windows)
    # Windows break after a blank line, never inside a paragraph.
    assert all(window.text.endswith("\n\n") for window in windows[:-1])


def test_long_paragraphs_split_at_sentences_then_hard() -> None:
    sentence_text = "A fairly long sentence goes here. " * 100  # one paragraph, 3,400 chars
    windows = split_windows(sentence_text, max_chars=500)
    assert all(len(window.text) <= 500 for window in windows)
    assert "".join(window.text for window in windows) == sentence_text
    assert all(window.text.endswith(". ") for window in windows[:-1])

    unbroken = "x" * 1234
    windows = split_windows(unbroken, max_chars=500)
    assert [len(window.text) for window in windows] == [500, 500, 234]

    with pytest.raises(ValueError):
        split_windows("text", max_chars=0)
