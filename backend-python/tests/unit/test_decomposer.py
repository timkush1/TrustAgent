"""
Decomposer v2 (SPEC section 3.2; D2, D14): the question block, the
{claim, quote} output, claim length rules, the truncation and repair retries,
the whole-response fallback, windows for long answers, and UTF-16 spans.
"""

import json
from typing import Any, Callable, List, Optional, Sequence, Union

import pytest

from truthtable.graphs.nodes.decomposer import (
    DECOMPOSER_MAX_TOKENS,
    DECOMPOSER_RETRY_MAX_TOKENS,
    DECOMPOSER_SCHEMA,
    DECOMPOSER_SYSTEM_PROMPT,
    DecomposerNode,
    create_decomposer_prompt,
    decompose,
    decompose_claims,
    validate_extracted_claims,
)
from truthtable.graphs.state import AuditState
from truthtable.providers.base import (
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
    MessageRole,
)
from truthtable.providers.errors import ProviderError

Answer = Union[str, Exception, CompletionResponse]


def claims_json(*pairs: Any) -> str:
    return json.dumps({"claims": [{"claim": c, "quote": q} for c, q in pairs]})


class QueueProvider(LLMProvider):
    """Answers each call with the next scripted answer (or `answer_for(prompt)`)."""

    def __init__(
        self,
        answers: Sequence[Answer] = (),
        answer_for: Optional[Callable[[str], Answer]] = None,
    ):
        super().__init__(model="scripted")
        self.answers: List[Answer] = list(answers)
        self.answer_for = answer_for
        self.requests: List[CompletionRequest] = []

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.requests.append(request)
        if self.answer_for is not None:
            answer = self.answer_for(request.messages[1].content)
        else:
            answer = self.answers.pop(0)
        if isinstance(answer, Exception):
            raise answer
        if isinstance(answer, CompletionResponse):
            return answer
        return CompletionResponse(content=answer, model=self.model, finish_reason="stop")

    async def health_check(self) -> bool:
        return True


def truncated(content: str = '{"claims": [{"claim": "Par') -> CompletionResponse:
    return CompletionResponse(content=content, model="scripted", finish_reason="max_tokens")


# ---------------------------------------------------------------------------
# Prompt and output schema
# ---------------------------------------------------------------------------


def test_prompt_carries_the_question_and_the_text_as_data() -> None:
    prompt = create_decomposer_prompt("Delhi.", "What is the capital of India?")

    assert "Extract all factual claims" in prompt  # recorded fixtures match on it
    assert "<question>\nWhat is the capital of India?\n</question>" in prompt
    assert "<text>\nDelhi.\n</text>" in prompt
    assert prompt.index("<question>") < prompt.index("<text>")
    assert "JSON object" in prompt


def test_prompt_without_a_question_has_no_question_block() -> None:
    assert "<question>" not in create_decomposer_prompt("Paris is in France.")
    assert "<question>" not in create_decomposer_prompt("Paris is in France.", "   ")


def test_question_is_sanitized_and_capped() -> None:
    prompt = create_decomposer_prompt("x", "Wh​at? " + "q" * 5000)

    question = prompt.split("<question>\n", 1)[1].split("\n</question>", 1)[0]
    assert "​" not in question
    assert len(question) <= 2000 + len(" [truncated]")


def test_system_prompt_rules() -> None:
    for rule in ("<question>", "quote", "Greetings, refusals and questions", "UNTRUSTED DATA"):
        assert rule in DECOMPOSER_SYSTEM_PROMPT
    assert "Never extract claims from the question" in " ".join(DECOMPOSER_SYSTEM_PROMPT.split())


def test_requests_ask_for_structured_output() -> None:
    assert DECOMPOSER_SCHEMA["required"] == ["claims"]
    item = DECOMPOSER_SCHEMA["properties"]["claims"]["items"]
    assert item["required"] == ["claim", "quote"]


@pytest.mark.parametrize(
    "parsed, expected",
    [
        ({"claims": [{"claim": "Paris is in France.", "quote": "Paris"}]}, [("Paris is in France.", "Paris")]),
        (["Paris is in France."], [("Paris is in France.", "Paris is in France.")]),
        ({"claims": ["A legacy item."]}, [("A legacy item.", "A legacy item.")]),
        ({"claims": []}, []),
        ([], []),
        # 3-500 characters: "Ox" is dropped, "Delhi" (5 chars, dropped by v1) is kept.
        (["Ox", "Delhi", "", "  "], [("Delhi", "Delhi")]),
        ([" " + "y" * 600 + " "], [("y" * 500, "y" * 500)]),
        ({"claims": [{"claim": "A claim.", "quote": "q" * 900, "extra": 1}]}, [("A claim.", "q" * 500)]),
    ],
)  # fmt: skip
def test_validate_extracted_claims(parsed: Any, expected: List[Any]) -> None:
    assert validate_extracted_claims(parsed) == expected


@pytest.mark.parametrize(
    "parsed",
    [
        {"claims": "Paris"},
        {"statements": []},
        "Paris is in France.",
        42,
        [{"claim": "No quote given."}],
        [{"claim": 1, "quote": "x"}],
        [["nested"]],
    ],
)
def test_validate_extracted_claims_rejects_other_shapes(parsed: Any) -> None:
    with pytest.raises(ValueError):
        validate_extracted_claims(parsed)


# ---------------------------------------------------------------------------
# Retries and fallback
# ---------------------------------------------------------------------------


async def test_object_answer_with_quotes_and_spans() -> None:
    response = "Paris is the capital of France. The Eiffel Tower is in Paris."
    provider = QueueProvider(
        [
            claims_json(
                ("Paris is the capital of France.", "Paris is the capital of France"),
                ("The Eiffel Tower is in Paris.", "The Eiffel Tower is in Paris"),
            )
        ]
    )

    result = await decompose(response, provider, question="Tell me about Paris.")

    assert result.texts == ["Paris is the capital of France.", "The Eiffel Tower is in Paris."]
    assert [(c.span.start, c.span.end, c.span.method) for c in result.claims] == [
        (0, 30, "exact"),
        (32, 60, "exact"),
    ]
    assert not result.fallback and result.llm_calls == 1 and result.windows == 1
    request = provider.requests[0]
    assert request.response_schema == DECOMPOSER_SCHEMA
    assert request.max_tokens == DECOMPOSER_MAX_TOKENS
    assert "<question>\nTell me about Paris.\n</question>" in request.messages[1].content


async def test_short_answer_becomes_a_self_contained_claim() -> None:
    # D2: "Delhi." with the question yields a claim; its quote is the word itself.
    provider = QueueProvider([claims_json(("The capital of India is Delhi.", "Delhi"))])

    result = await decompose("Delhi.", provider, question="What is the capital of India?")

    assert result.texts == ["The capital of India is Delhi."]
    assert (result.claims[0].span.start, result.claims[0].span.end) == (0, 5)


async def test_empty_claim_list_is_legitimate() -> None:
    provider = QueueProvider([claims_json()])

    result = await decompose("Hello! How can I help you today?", provider)

    assert result.claims == () and not result.fallback and result.llm_calls == 1


async def test_truncated_answer_is_retried_once_with_a_larger_budget() -> None:
    provider = QueueProvider([truncated(), claims_json(("Paris is in France.", "Paris"))])

    result = await decompose("Paris is in France.", provider)

    assert result.texts == ["Paris is in France."]
    assert result.truncation_retries == 1 and result.repair_retries == 0
    assert [r.max_tokens for r in provider.requests] == [
        DECOMPOSER_MAX_TOKENS,
        DECOMPOSER_RETRY_MAX_TOKENS,
    ]


async def test_invalid_answer_gets_one_repair_turn() -> None:
    provider = QueueProvider(["Sure! Here are the claims: Paris", claims_json(("Paris.", "Paris"))])

    result = await decompose("Paris.", provider)

    assert result.texts == ["Paris."]
    assert not result.fallback and result.repair_retries == 1
    repair = provider.requests[1].messages
    assert [m.role for m in repair] == [
        MessageRole.SYSTEM,
        MessageRole.USER,
        MessageRole.ASSISTANT,
        MessageRole.USER,
    ]
    assert repair[2].content == "Sure! Here are the claims: Paris"
    assert "could not be used: it was not valid JSON" in repair[3].content
    # The repair turn carries no data block of its own (recorded fixtures key on them).
    assert "<text>" not in repair[3].content


async def test_truncation_then_invalid_then_fallback() -> None:
    # One truncation retry, one repair turn, then the whole response is one claim.
    provider = QueueProvider([truncated(), truncated(), "still not json"])
    response = "The store opens at 9am on weekdays."

    result = await decompose(response, provider)

    assert result.fallback
    assert result.llm_calls == 3
    assert (result.truncation_retries, result.repair_retries) == (1, 1)
    assert result.texts == [response]
    assert (result.claims[0].span.start, result.claims[0].span.end) == (0, len(response))
    assert "cut off by the token limit" in provider.requests[2].messages[3].content


async def test_fallback_claim_is_capped_and_sanitized() -> None:
    response = "Hidden​ text. " + "word " * 1000
    provider = QueueProvider(["nope", "nope"])

    result = await decompose(response, provider)

    (claim,) = result.claims
    assert result.fallback
    assert "​" not in claim.claim and len(claim.claim) <= 2000
    assert claim.span.start == 0 and claim.span.method == "exact"


async def test_fallback_of_a_tiny_response_has_no_claim() -> None:
    result = await decompose("Ok", QueueProvider(["?", "?"]))

    assert result.claims == () and result.fallback


async def test_provider_exception_propagates() -> None:
    provider = QueueProvider([ProviderError("down", kind="unavailable", provider="x")])

    with pytest.raises(ProviderError):
        await decompose("Paris is in France.", provider)


async def test_duplicates_are_dropped_and_claims_capped() -> None:
    pairs = [(f"Claim number {i}.", f"Claim number {i}") for i in range(30)]
    provider = QueueProvider([claims_json(pairs[0], pairs[0], *pairs[1:])])

    result = await decompose("text", provider, max_claims=25)

    assert len(result.claims) == 25
    assert result.texts[:2] == ["Claim number 0.", "Claim number 1."]
    assert result.dropped == 6  # one duplicate and five beyond the cap
    assert all(not c.span.aligned for c in result.claims)  # not in the text


# ---------------------------------------------------------------------------
# Windows (D14)
# ---------------------------------------------------------------------------


async def test_long_answers_are_decomposed_in_windows_with_shifted_spans() -> None:
    first = "\U0001F600 Alpha fact is true. " + "Filler sentence here. " * 30
    second = "Beta fact is also true. " + "More filler text. " * 42
    third = "Alpha fact is true. Gamma fact closes it."
    response = f"{first}\n\n{second}\n\n{third}"

    def answer_for(prompt: str) -> str:
        text = prompt.split("<text>\n", 1)[1].rsplit("\n</text>", 1)[0]
        pairs = []
        for fact in ("Alpha fact is true", "Beta fact is also true", "Gamma fact closes it"):
            if fact in text:
                pairs.append((fact + ".", fact))
        return claims_json(*pairs)

    provider = QueueProvider(answer_for=answer_for)

    result = await decompose(response, provider, window_chars=800)

    assert result.windows == 3 and result.llm_calls == 3
    # The repeated "Alpha" claim of the last window is an exact duplicate: dropped.
    assert result.texts == [
        "Alpha fact is true.",
        "Beta fact is also true.",
        "Gamma fact closes it.",
    ]
    assert result.dropped == 1
    units = response.encode("utf-16-le")
    for claim in result.claims:
        start, end = claim.span.start, claim.span.end
        assert units[start * 2 : end * 2].decode("utf-16-le") == claim.quote
    # Spans are UTF-16 over the whole answer: the emoji shifts every later span by one.
    alpha = result.claims[0].span
    assert (alpha.start, alpha.end) == (3, 3 + len("Alpha fact is true"))
    assert result.claims[1].span.start == len(response[: response.index("Beta")]) + 1


async def test_every_window_is_asked_with_the_question() -> None:
    response = "\n\n".join(["Paragraph with a fact. " * 20] * 4)
    provider = QueueProvider(answer_for=lambda prompt: claims_json())

    await decompose(response, provider, question="Why?", window_chars=1000)

    assert len(provider.requests) > 1
    assert all("<question>\nWhy?\n</question>" in r.messages[1].content for r in provider.requests)


# ---------------------------------------------------------------------------
# Legacy API and the node
# ---------------------------------------------------------------------------


async def test_decompose_claims_returns_texts_and_wraps_failures() -> None:
    assert await decompose_claims("Paris.", QueueProvider([json.dumps(["Paris."])])) == ["Paris."]
    with pytest.raises(RuntimeError, match="Failed to decompose claims"):
        await decompose_claims("Paris.", QueueProvider([ConnectionError("down")]))


def _state(response: str, query: str = "Test query?") -> AuditState:
    return {
        "request_id": "test-123",
        "user_query": query,
        "llm_response": response,
        "context_docs": ["doc1"],
        "claims": None,
        "claim_verifications": None,
        "faithfulness_score": None,
        "hallucination_detected": None,
        "reasoning_trace": None,
        "degraded_reasons": [],
        "step_timings": {},
    }  # type: ignore[typeddict-item]


async def test_node_records_claims_quotes_and_spans() -> None:
    provider = QueueProvider(
        [claims_json(("Claim one is here.", "Claim one"), ("Claim two is here.", "missing"))]
    )

    state = await DecomposerNode(provider=provider).run(
        _state("Claim one is here. Claim two is here.")
    )

    assert state["claims"] == ["Claim one is here.", "Claim two is here."]
    assert state["extracted_claims"] == [
        {
            "claim": "Claim one is here.",
            "quote": "Claim one",
            "span_start": 0,
            "span_end": 9,
            "span_method": "exact",
        },
        {
            # The quote is not in the answer, but the claim is.
            "claim": "Claim two is here.",
            "quote": "missing",
            "span_start": 19,
            "span_end": 37,
            "span_method": "exact",
        },
    ]
    assert state["degraded_reasons"] == []
    assert "decompose_ms" in state["step_timings"]
    assert "Test query?" in provider.requests[0].messages[1].content


async def test_node_marks_the_fallback_degraded() -> None:
    state = await DecomposerNode(provider=QueueProvider(["x", "y"])).run(
        _state("The whole answer is one claim.")
    )

    assert state["claims"] == ["The whole answer is one claim."]
    assert state["degraded_reasons"] == ["decomposer_fallback"]


async def test_node_honours_max_claims() -> None:
    pairs = [(f"Claim {i} text.", f"Claim {i}") for i in range(5)]
    node = DecomposerNode(provider=QueueProvider([claims_json(*pairs)]), max_claims=2)

    state = await node.run(_state("Claim 0 text. Claim 1 text."))

    assert state["claims"] == ["Claim 0 text.", "Claim 1 text."]


async def test_node_wraps_provider_failures() -> None:
    node = DecomposerNode(provider=QueueProvider([ConnectionError("judge down")]))

    with pytest.raises(RuntimeError, match="judge down"):
        await node.run(_state("Paris."))


def test_prompt_is_unchanged_for_the_mock_scenario_matcher() -> None:
    # providers/mock.py reads the answer back from "<text>\n...\n</text>".
    prompt = create_decomposer_prompt("line one\nline two", "q")
    assert "<text>\nline one\nline two\n</text>" in prompt
