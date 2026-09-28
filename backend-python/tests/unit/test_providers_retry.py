"""
Retry policy and error mapping of the judge providers (SPEC section 3.2, D7):
408, 429, 5xx and 529 are retried with equal-jitter backoff, Retry-After is
honoured up to 20 s, Ollama retries only connection errors and 5xx, and every
failure surfaces as a typed ProviderError.
"""

import email.utils
from typing import List, Optional

import httpx
import pytest

from provider_fakes import ScriptedTransport, SleepRecorder, connect_error, json_reply
from truthtable.providers.anthropic import AnthropicProvider
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.errors import (
    BudgetError,
    ProviderConnectionError,
    ProviderError,
)
from truthtable.providers.ollama import OllamaProvider
from truthtable.providers.retry import (
    DEFAULT_RETRY_POLICY,
    NO_RETRY,
    OLLAMA_RETRY_POLICY,
    RetryPolicy,
    call_with_retry,
)
from truthtable.providers.transport import (
    error_from_status,
    parse_retry_after,
    status_kind,
)

ANTHROPIC_OK = {
    "model": "claude-haiku-4-5",
    "content": [{"type": "text", "text": "ok"}],
    "stop_reason": "end_turn",
    "usage": {"input_tokens": 3, "output_tokens": 1},
}


def request() -> CompletionRequest:
    return CompletionRequest(messages=[Message(MessageRole.USER, "hi")], model="")


def http_error(status: int, retry_after: Optional[float] = None) -> ProviderError:
    headers = {"retry-after": str(retry_after)} if retry_after is not None else {}
    return error_from_status("test", status, {"error": {"message": "boom"}}, headers=headers)


# ---------------------------------------------------------------------------
# Classification
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "status, kind",
    [
        (400, "invalid"),
        (401, "auth"),
        (402, "credits"),
        (403, "auth"),
        (404, "not_found"),
        (408, "timeout"),
        (413, "too_large"),
        (422, "invalid"),
        (429, "rate_limited"),
        (500, "unavailable"),
        (502, "unavailable"),
        (503, "unavailable"),
        (504, "unavailable"),
        (524, "unavailable"),
        (529, "overloaded"),
        (418, "http_error"),
    ],
)
def test_status_kinds(status: int, kind: str) -> None:
    assert status_kind(status) == kind
    assert http_error(status).kind == kind


def test_error_message_carries_the_upstream_message_not_the_body() -> None:
    error = error_from_status(
        "anthropic",
        529,
        {"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}},
    )
    assert str(error) == "anthropic returned HTTP 529: Overloaded"
    assert error.status == 529 and error.provider == "anthropic"


def test_402_is_a_budget_error() -> None:
    error = http_error(402)
    assert isinstance(error, BudgetError)
    assert not error.retryable


# ---------------------------------------------------------------------------
# Policy
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("status", [408, 429, 500, 502, 503, 504, 524, 529])
def test_default_policy_retries_transient_statuses(status: int) -> None:
    assert DEFAULT_RETRY_POLICY.is_retryable(http_error(status))


@pytest.mark.parametrize("status", [400, 401, 402, 403, 404, 413, 422])
def test_default_policy_never_retries_client_errors(status: int) -> None:
    assert not DEFAULT_RETRY_POLICY.is_retryable(http_error(status))


def test_default_policy_retries_connection_errors_and_timeouts() -> None:
    connection = ProviderConnectionError("x", kind="connection", provider="t", retryable=True)
    timeout = ProviderError("x", kind="timeout", provider="t", retryable=True)
    assert DEFAULT_RETRY_POLICY.is_retryable(connection)
    assert DEFAULT_RETRY_POLICY.is_retryable(timeout)


def test_ollama_policy_retries_only_connection_errors_and_5xx() -> None:
    connection = ProviderConnectionError("x", kind="connection", provider="t", retryable=True)
    timeout = ProviderError("x", kind="timeout", provider="t", retryable=True)
    assert OLLAMA_RETRY_POLICY.is_retryable(connection)
    assert OLLAMA_RETRY_POLICY.is_retryable(http_error(503))
    assert not OLLAMA_RETRY_POLICY.is_retryable(timeout)
    assert not OLLAMA_RETRY_POLICY.is_retryable(http_error(429))
    assert not OLLAMA_RETRY_POLICY.is_retryable(http_error(408))


def test_backoff_is_exponential_with_equal_jitter_and_capped() -> None:
    policy = RetryPolicy()
    # Equal jitter: [d/2, d] with d = min(8, 0.5 * 2**k).
    assert policy.backoff_s(0, rng=lambda: 0.0) == pytest.approx(0.25)
    assert policy.backoff_s(0, rng=lambda: 1.0) == pytest.approx(0.5)
    assert policy.backoff_s(2, rng=lambda: 0.0) == pytest.approx(1.0)
    assert policy.backoff_s(2, rng=lambda: 1.0) == pytest.approx(2.0)
    assert policy.backoff_s(10, rng=lambda: 0.0) == pytest.approx(4.0)
    assert policy.backoff_s(10, rng=lambda: 1.0) == pytest.approx(8.0)


def test_retry_after_is_honoured_up_to_the_cap() -> None:
    policy = RetryPolicy()
    assert policy.delay_s(http_error(429, retry_after=3), 0) == pytest.approx(3.0)
    assert policy.delay_s(http_error(429, retry_after=20), 0) == pytest.approx(20.0)
    # A longer wait ends the retries instead of stalling the audit.
    assert policy.delay_s(http_error(429, retry_after=21), 0) is None


def test_no_delay_once_the_retries_run_out() -> None:
    policy = RetryPolicy(max_retries=2)
    assert policy.delay_s(http_error(503), 1) is not None
    assert policy.delay_s(http_error(503), 2) is None
    assert NO_RETRY.delay_s(http_error(503), 0) is None


@pytest.mark.parametrize(
    "headers, expected",
    [
        ({"retry-after": "7"}, 7.0),
        ({"retry-after": "1.5"}, 1.5),
        ({"retry-after-ms": "250"}, 0.25),
        ({"retry-after-ms": "250", "retry-after": "9"}, 0.25),
        ({"retry-after": "soon"}, None),
        ({"retry-after": "-3"}, None),
        ({}, None),
    ],
)
def test_parse_retry_after(headers: dict, expected: float) -> None:
    assert parse_retry_after(headers) == expected


def test_parse_retry_after_http_date() -> None:
    now = 1_700_000_000.0
    header = email.utils.formatdate(now + 12, usegmt=True)
    assert parse_retry_after({"retry-after": header}, now=now) == pytest.approx(12.0)


# ---------------------------------------------------------------------------
# call_with_retry
# ---------------------------------------------------------------------------


async def test_call_with_retry_recovers_from_transient_errors() -> None:
    sleep = SleepRecorder()
    outcomes: List[object] = [http_error(503), http_error(429, retry_after=2), "done"]

    async def call() -> str:
        outcome = outcomes.pop(0)
        if isinstance(outcome, Exception):
            raise outcome
        return str(outcome)

    assert await call_with_retry(call, sleep=sleep, rng=lambda: 0.0) == "done"
    assert sleep.delays == [pytest.approx(0.25), pytest.approx(2.0)]


async def test_call_with_retry_raises_the_last_error() -> None:
    sleep = SleepRecorder()
    attempts = 0

    async def call() -> str:
        nonlocal attempts
        attempts += 1
        raise http_error(500)

    with pytest.raises(ProviderError) as exc_info:
        await call_with_retry(call, RetryPolicy(max_retries=3), sleep=sleep)

    assert exc_info.value.status == 500
    assert attempts == 4
    assert len(sleep.delays) == 3


async def test_call_with_retry_does_not_retry_permanent_errors() -> None:
    sleep = SleepRecorder()
    attempts = 0

    async def call() -> str:
        nonlocal attempts
        attempts += 1
        raise http_error(401)

    with pytest.raises(ProviderError):
        await call_with_retry(call, sleep=sleep)

    assert attempts == 1
    assert sleep.delays == []


# ---------------------------------------------------------------------------
# Providers
# ---------------------------------------------------------------------------


async def test_anthropic_retries_an_overloaded_answer() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply(
            {"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}},
            529,
            headers={"retry-after": "1"},
        ),
        json_reply(ANTHROPIC_OK),
    )
    provider = AnthropicProvider(api_key="k", transport=transport, sleep=sleep)

    response = await provider.complete(request())

    assert response.content == "ok"
    assert transport.calls == 2
    assert sleep.delays == [pytest.approx(1.0)]


async def test_anthropic_gives_up_on_a_long_retry_after() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply({"error": {"message": "slow down"}}, 429, headers={"retry-after": "60"})
    )
    provider = AnthropicProvider(api_key="k", transport=transport, sleep=sleep)

    with pytest.raises(ProviderError) as exc_info:
        await provider.complete(request())

    assert exc_info.value.kind == "rate_limited"
    assert exc_info.value.retry_after_s == 60
    assert transport.calls == 1
    assert sleep.delays == []


async def test_timeouts_are_retried_then_raised() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(httpx.ReadTimeout("read timed out"), json_reply(ANTHROPIC_OK))
    provider = AnthropicProvider(api_key="k", transport=transport, sleep=sleep)

    assert (await provider.complete(request())).content == "ok"
    assert transport.calls == 2


async def test_ollama_does_not_retry_a_timeout() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(httpx.ReadTimeout("read timed out"))
    provider = OllamaProvider(model="llama3.2:1b", transport=transport, sleep=sleep)

    with pytest.raises(ProviderError) as exc_info:
        await provider.complete(request())

    assert exc_info.value.kind == "timeout"
    assert transport.calls == 1


async def test_ollama_retries_a_server_error() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply({"error": "model is loading"}, 503),
        json_reply({"model": "llama3.2:1b", "message": {"content": "ok"}, "done_reason": "stop"}),
    )
    provider = OllamaProvider(model="llama3.2:1b", transport=transport, sleep=sleep)

    assert (await provider.complete(request())).content == "ok"
    assert transport.calls == 2


async def test_connection_errors_are_typed_and_keep_the_base_url() -> None:
    transport = ScriptedTransport(connect_error())
    provider = AnthropicProvider(
        api_key="k", transport=transport, retry_policy=NO_RETRY, base_url="http://judge.test"
    )

    with pytest.raises(ProviderConnectionError) as exc_info:
        await provider.complete(request())

    assert exc_info.value.kind == "connection"
    assert "http://judge.test" in str(exc_info.value)


async def test_error_messages_never_contain_the_api_key() -> None:
    transport = ScriptedTransport(json_reply({"error": {"message": "invalid x-api-key"}}, 401))
    provider = AnthropicProvider(api_key="sk-ant-secret-value", transport=transport)

    with pytest.raises(ProviderError) as exc_info:
        await provider.complete(request())

    assert exc_info.value.kind == "auth"
    assert "sk-ant-secret-value" not in str(exc_info.value)
    assert "sk-ant-secret-value" not in repr(exc_info.value)
