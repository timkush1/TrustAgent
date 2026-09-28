"""
Verifier v2 (SPEC section 3.2; D7): structured verdicts with a required
confidence, one repair turn then UNKNOWN, no judge call without context
(NOT_CHECKED), provider exceptions as UNKNOWN with error=true, capped evidence
(context_trimmed), and the per-claim record the node builds.
"""

import json
from typing import Any, Dict, List, Optional, Sequence, Union

import pytest

from truthtable.graphs.nodes.verifier import (
    MAX_CONTEXT_DOC_CHARS,
    MAX_CONTEXT_DOCS,
    VERIFIER_MAX_TOKENS,
    VERIFIER_RETRY_MAX_TOKENS,
    VERIFIER_SCHEMA,
    VERIFIER_SYSTEM_PROMPT,
    VerifierNode,
    check_claim,
    create_verifier_prompt,
    prepare_context,
    validate_verdict_v2,
    verify_all_claims,
    verify_claim,
)
from truthtable.graphs.state import AuditState, VerificationStatus
from truthtable.providers.base import (
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
    MessageRole,
)
from truthtable.providers.errors import BudgetError, ProviderError

Answer = Union[str, Exception, CompletionResponse, Dict[str, Any]]


def verdict(status: str = "SUPPORTED", confidence: Optional[float] = 0.9, **extra: Any) -> str:
    body: Dict[str, Any] = {"status": status, "evidence": ["a quote"], "reasoning": "Because."}
    if confidence is not None:
        body["confidence"] = confidence
    body.update(extra)
    return json.dumps(body)


class QueueProvider(LLMProvider):
    """Answers with the scripted answers in order; the last one repeats."""

    def __init__(self, *answers: Answer):
        super().__init__(model="scripted")
        self.answers: List[Answer] = list(answers)
        self.requests: List[CompletionRequest] = []

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.requests.append(request)
        answer = self.answers[min(len(self.requests), len(self.answers)) - 1]
        if isinstance(answer, Exception):
            raise answer
        if isinstance(answer, CompletionResponse):
            return answer
        if isinstance(answer, dict):
            answer = json.dumps(answer)
        return CompletionResponse(content=answer, model=self.model, finish_reason="stop")

    async def health_check(self) -> bool:
        return True


# ---------------------------------------------------------------------------
# Prompt and schema
# ---------------------------------------------------------------------------


def test_prompt_carries_the_contradicted_vs_partially_supported_rule() -> None:
    prompt = " ".join(VERIFIER_SYSTEM_PROMPT.split())

    assert "CONTRADICTED vs PARTIALLY_SUPPORTED" in prompt
    assert "if any part of the claim is stated to be false by a relevant document" in prompt
    assert "the claim is CONTRADICTED, even when other parts are supported" in prompt
    assert "UNTRUSTED DATA" in prompt


def test_schema_requires_every_field() -> None:
    assert VERIFIER_SCHEMA["required"] == ["status", "confidence", "evidence", "reasoning"]
    assert VERIFIER_SCHEMA["properties"]["status"]["enum"] == [
        "SUPPORTED",
        "PARTIALLY_SUPPORTED",
        "CONTRADICTED",
        "UNSUPPORTED",
    ]


def test_prompt_renders_claim_and_documents_as_data() -> None:
    prompt = create_verifier_prompt("A cl​aim.", ["Doc one.", "x" * (MAX_CONTEXT_DOC_CHARS + 5)])

    assert "<claim>\nA claim.\n</claim>" in prompt  # the mock judges key on this block
    assert "[Document 1]\nDoc one." in prompt
    assert "[Document 2]\n" + "x" * MAX_CONTEXT_DOC_CHARS + " [truncated]" in prompt


# ---------------------------------------------------------------------------
# Answer validation (D7)
# ---------------------------------------------------------------------------


def test_validate_verdict_reads_every_field() -> None:
    result = validate_verdict_v2(
        {
            "status": " partially_supported ",
            "confidence": "0.7",
            "evidence": ["one", "", "two", "three", "four"],
            "reasoning": "  " + "r" * 2000,
        }
    )

    assert result.status is VerificationStatus.PARTIALLY_SUPPORTED
    assert result.confidence == 0.7
    assert result.evidence == ["one", "two"]  # at most 3 items, blanks dropped
    assert result.reasoning == "r" * 1000


@pytest.mark.parametrize("confidence, expected", [(1.7, 1.0), (-2, 0.0), (0, 0.0), (1, 1.0)])
def test_validate_verdict_clamps_confidence(confidence: float, expected: float) -> None:
    assert validate_verdict_v2({"status": "SUPPORTED", "confidence": confidence}).confidence == (
        expected
    )


def test_validate_verdict_tolerates_missing_evidence_and_reasoning() -> None:
    result = validate_verdict_v2({"status": "SUPPORTED", "confidence": 0.8})

    assert (result.evidence, result.reasoning) == ([], "")


@pytest.mark.parametrize(
    "parsed, problem",
    [
        ({"status": "SUPPORTED"}, "confidence is missing"),
        ({"status": "SUPPORTED", "confidence": None}, "confidence is missing"),
        ({"status": "SUPPORTED", "confidence": "high"}, "confidence must be a number"),
        ({"status": "SUPPORTED", "confidence": True}, "confidence must be a number"),
        ({"status": "SUPPORTED", "confidence": float("nan")}, "confidence must be a number"),
        ({"status": "UNKNOWN", "confidence": 0.9}, "status must be one of"),
        ({"status": "TRUSTED", "confidence": 0.9}, "status must be one of"),
        ({"status": "SUPPORTED", "confidence": 0.9, "evidence": "q"}, "evidence must be a list"),
        (["SUPPORTED"], "expected a JSON object"),
    ],
)
def test_validate_verdict_rejects(parsed: Any, problem: str) -> None:
    with pytest.raises(ValueError, match=problem):
        validate_verdict_v2(parsed)


# ---------------------------------------------------------------------------
# One claim
# ---------------------------------------------------------------------------


async def test_supported_claim_record() -> None:
    provider = QueueProvider(verdict("SUPPORTED", 0.9))

    check = await check_claim("Paris is in France.", ["Paris is the capital of France."], provider)
    record = check.verification

    assert record["status"] is VerificationStatus.SUPPORTED
    assert (record["confidence"], record["evidence"], record["reasoning"]) == (
        0.9,
        ["a quote"],
        "Because.",
    )
    assert record["decided_by"] == "llm"
    assert record["probability_source"] == "llm_confidence"
    assert record["verdict_probabilities"]["supported"] == pytest.approx(0.9)
    assert sum(record["verdict_probabilities"].values()) == pytest.approx(1.0)
    assert check.calls == 1 and not check.retry_exhausted
    request = provider.requests[0]
    assert request.response_schema == VERIFIER_SCHEMA
    assert request.max_tokens == VERIFIER_MAX_TOKENS


async def test_missing_confidence_gets_one_repair_turn() -> None:
    provider = QueueProvider(verdict(confidence=None), verdict("CONTRADICTED", 0.8))

    check = await check_claim("Refunds last 60 days.", ["Refunds last 30 days."], provider)

    assert check.verification["status"] is VerificationStatus.CONTRADICTED
    assert check.verification["confidence"] == 0.8
    assert check.calls == 2
    repair = provider.requests[1].messages
    assert [m.role for m in repair] == [
        MessageRole.SYSTEM,
        MessageRole.USER,
        MessageRole.ASSISTANT,
        MessageRole.USER,
    ]
    assert "could not be used: confidence is missing" in repair[3].content
    assert "<claim>" not in repair[3].content
    assert provider.requests[1].max_tokens == VERIFIER_RETRY_MAX_TOKENS


async def test_missing_confidence_twice_is_unknown_never_a_default() -> None:
    # D7: v1 silently used 0.5.
    provider = QueueProvider(verdict(confidence=None))

    record = await verify_claim("A claim.", ["Some context."], provider)

    assert record["status"] is VerificationStatus.UNKNOWN
    assert record["confidence"] == 0.0
    assert record["error"] is False and record["error_kind"] == "invalid_output"
    assert record["decided_by"] == "none"
    assert len(provider.requests) == 2


async def test_truncated_answer_is_retried_with_a_larger_budget() -> None:
    cut = CompletionResponse(content='{"status": "SUPP', model="m", finish_reason="length")
    provider = QueueProvider(cut, verdict("SUPPORTED", 0.8))

    record = await verify_claim("A claim.", ["Some context."], provider)

    assert record["status"] is VerificationStatus.SUPPORTED
    assert [r.max_tokens for r in provider.requests] == [
        VERIFIER_MAX_TOKENS,
        VERIFIER_RETRY_MAX_TOKENS,
    ]


async def test_truncated_twice_then_repair() -> None:
    cut = CompletionResponse(content='{"status": "SUPP', model="m", finish_reason="max_tokens")
    provider = QueueProvider(cut, cut, verdict("UNSUPPORTED", 0.7))

    check = await check_claim("A claim.", ["Some context."], provider)

    assert check.verification["status"] is VerificationStatus.UNSUPPORTED
    assert check.calls == 3
    assert "cut off by the token limit" in provider.requests[2].messages[3].content


@pytest.mark.parametrize("context", [[], [""], ["  \n "], ["\u200b\u200d "]])
async def test_no_context_is_not_checked_without_a_judge_call(context: List[str]) -> None:
    provider = QueueProvider(verdict())

    check = await check_claim("A claim.", context, provider)

    assert provider.requests == []
    assert check.verification["status"] is VerificationStatus.NOT_CHECKED
    assert check.verification["decided_by"] == "none"
    assert check.calls == 0


async def test_provider_exception_is_unknown_with_error() -> None:
    provider = QueueProvider(ConnectionError("judge down"))

    check = await check_claim("A claim.", ["Some context."], provider)

    assert check.verification["status"] is VerificationStatus.UNKNOWN
    assert check.verification["error"] is True
    assert check.verification["error_kind"] == "ConnectionError"
    assert not check.retry_exhausted


async def test_exhausted_provider_retries_are_reported() -> None:
    error = ProviderError("overloaded", kind="overloaded", provider="anthropic", retryable=True)
    check = await check_claim("A claim.", ["ctx"], QueueProvider(error))

    assert check.verification["error_kind"] == "overloaded"
    assert check.retry_exhausted

    budget = BudgetError("no credits", kind="credits", provider="openrouter", status=402)
    check = await check_claim("A claim.", ["ctx"], QueueProvider(budget))
    assert check.verification["error_kind"] == "credits"
    assert not check.retry_exhausted


async def test_exception_during_repair_is_unknown_with_error() -> None:
    provider = QueueProvider("not json", ConnectionError("gone"))

    check = await check_claim("A claim.", ["ctx"], provider)

    assert check.verification["status"] is VerificationStatus.UNKNOWN
    assert check.verification["error"] is True and check.calls == 2


async def test_verify_all_claims_keeps_order_and_isolates_failures() -> None:
    class ByClaim(QueueProvider):
        async def complete(self, request: CompletionRequest) -> CompletionResponse:
            self.requests.append(request)
            if "<claim>\nbad\n</claim>" in request.messages[1].content:
                raise ConnectionError("down")
            return CompletionResponse(content=verdict(), model="m", finish_reason="stop")

    records = await verify_all_claims(["one", "bad", "three"], ["ctx"], ByClaim(), concurrency=2)

    assert [r["claim"] for r in records] == ["one", "bad", "three"]
    assert [r["status"] for r in records] == [
        VerificationStatus.SUPPORTED,
        VerificationStatus.UNKNOWN,
        VerificationStatus.SUPPORTED,
    ]


# ---------------------------------------------------------------------------
# Evidence caps
# ---------------------------------------------------------------------------


def test_prepare_context_keeps_twenty_non_blank_documents() -> None:
    documents = ["", " "] + [f"doc {i}" for i in range(25)]

    context = prepare_context(documents)

    assert context.documents == tuple(f"doc {i}" for i in range(MAX_CONTEXT_DOCS))
    assert context.trimmed and context.dropped == 5


@pytest.mark.parametrize(
    "documents, upstream, trimmed",
    [
        (["short"], False, False),
        (["x" * MAX_CONTEXT_DOC_CHARS], False, False),
        (["x" * (MAX_CONTEXT_DOC_CHARS + 1)], False, True),
        # Hidden characters are stripped before measuring, like the prompt does.
        (["x" * MAX_CONTEXT_DOC_CHARS + "​" * 10], False, False),
        (["short"], True, True),
        ([], True, False),  # nothing was sent, so nothing was trimmed
    ],
)
def test_prepare_context_trimming(documents: Sequence[str], upstream: bool, trimmed: bool) -> None:
    assert prepare_context(documents, upstream_truncated=upstream).trimmed is trimmed


# ---------------------------------------------------------------------------
# The node
# ---------------------------------------------------------------------------


def _state(claims: List[str], documents: List[str], **extra: Any) -> AuditState:
    state: Dict[str, Any] = {
        "request_id": "r-1",
        "user_query": "q",
        "llm_response": "Paris is the capital of France. Lyon is big.",
        "context_docs": documents,
        "context_sources": [],
        "claims": claims,
        "claim_verifications": None,
        "degraded_reasons": [],
        "step_timings": {},
    }
    state.update(extra)
    return state  # type: ignore[return-value]


async def test_node_builds_the_v2_claim_records() -> None:
    extracted = [
        {
            "claim": "Paris is the capital of France.",
            "quote": "Paris is the capital of France",
            "span_start": 0,
            "span_end": 30,
            "span_method": "exact",
        }
    ]
    state = _state(
        ["Paris is the capital of France.", "Lyon is big."],
        ["Paris is the capital and largest city of France."],
        extracted_claims=extracted,
    )

    state = await VerifierNode(QueueProvider(verdict())).run(state)
    first, second = state["claim_verifications"]

    assert (first["index"], first["quote"], first["span_start"], first["span_end"]) == (
        0,
        "Paris is the capital of France",
        0,
        30,
    )
    assert first["span_method"] == "exact"
    # A claim the decomposer did not record is unaligned.
    assert (second["index"], second["quote"], second["span_start"], second["span_end"]) == (
        1,
        "",
        -1,
        -1,
    )
    assert second["span_method"] == "none"
    assert state["degraded_reasons"] == []
    assert "context_trimmed" not in state
    assert "verify_ms" in state["step_timings"]


async def test_node_marks_capped_evidence() -> None:
    provider = QueueProvider(verdict())
    documents = [f"Notice {i}." for i in range(MAX_CONTEXT_DOCS + 1)]

    state = await VerifierNode(provider).run(_state(["A claim."], documents))

    assert state["context_trimmed"] is True
    assert state["degraded_reasons"] == ["context_trimmed"]
    prompt = provider.requests[0].messages[1].content
    assert f"[Document {MAX_CONTEXT_DOCS}]" in prompt
    assert f"[Document {MAX_CONTEXT_DOCS + 1}]" not in prompt


async def test_node_honours_upstream_truncation() -> None:
    state = await VerifierNode(QueueProvider(verdict())).run(
        _state(["A claim."], ["doc"], context_truncated=True)
    )

    assert state["context_trimmed"] is True


async def test_node_without_claims_records_nothing() -> None:
    provider = QueueProvider(verdict())

    state = await VerifierNode(provider).run(_state([], [f"d{i}" for i in range(30)]))

    assert state["claim_verifications"] == []
    assert state["degraded_reasons"] == [] and provider.requests == []


async def test_node_reports_exhausted_judge_retries() -> None:
    error = ProviderError("rate limited", kind="rate_limited", provider="x", retryable=True)

    state = await VerifierNode(QueueProvider(error)).run(_state(["A claim."], ["doc"]))

    assert state["claim_verifications"][0]["status"] is VerificationStatus.UNKNOWN
    assert state["degraded_reasons"] == ["judge_retry_exhausted"]


async def test_node_without_context_marks_claims_not_checked() -> None:
    provider = QueueProvider(verdict())

    state = await VerifierNode(provider).run(_state(["A claim.", "Another."], ["", "  "]))

    assert [v["status"] for v in state["claim_verifications"]] == [
        VerificationStatus.NOT_CHECKED
    ] * 2
    assert provider.requests == []
