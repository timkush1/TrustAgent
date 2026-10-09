"""
OpenRouter judge provider (SPEC section 3.2): OpenAI-compatible chat at
/api/v1/chat/completions with `usage: {include: true}`, the reported cost,
402 -> BudgetError, the JSON Content-Type check and the attribution headers.
All HTTP goes through httpx.MockTransport; nothing reaches openrouter.ai.
"""

import json

import httpx
import pytest

from provider_fakes import ScriptedTransport, SleepRecorder, json_reply, text_reply
from truthtable.providers import OpenRouterProvider
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.errors import BudgetError, ProviderError
from truthtable.providers.openrouter import (
    DEFAULT_OPENROUTER_JUDGE_MODEL,
    normalize_base_url,
)
from truthtable.providers.pricing import PriceEntry, PriceTable
from truthtable.providers.registry import get_provider

CHAT_OK = {
    "id": "gen-1",
    "model": "anthropic/claude-haiku-4.5",
    "choices": [
        {
            "message": {"role": "assistant", "content": '{"status": "SUPPORTED"}'},
            "finish_reason": "stop",
        }
    ],
    "usage": {"prompt_tokens": 1200, "completion_tokens": 40, "cost": 0.00142},
}

PRICES = PriceTable(
    version="test",
    entries=(
        PriceEntry(
            match="anthropic/claude-haiku-4.5",
            provider="openrouter",
            input_per_mtok=1.0,
            output_per_mtok=5.0,
        ),
    ),
)


def request(**overrides) -> CompletionRequest:
    return CompletionRequest(
        messages=[
            Message(MessageRole.SYSTEM, "You are a fact checker."),
            Message(MessageRole.USER, "Is Paris in France?"),
        ],
        model="",
        **overrides,
    )


def provider(transport: ScriptedTransport, **kwargs) -> OpenRouterProvider:
    kwargs.setdefault("api_key", "sk-or-v1-test")
    kwargs.setdefault("sleep", SleepRecorder())
    kwargs.setdefault("price_table", PRICES)
    return OpenRouterProvider(transport=transport, **kwargs)


async def test_chat_request_shape_and_attribution_headers() -> None:
    transport = ScriptedTransport(json_reply(CHAT_OK))
    judge = provider(
        transport, app_url="https://github.com/timkush1/TrustAgent", app_title="TrustAgent"
    )

    await judge.complete(request())

    sent = transport.requests[0]
    assert sent.method == "POST"
    assert str(sent.url) == "https://openrouter.ai/api/v1/chat/completions"
    assert sent.headers["authorization"] == "Bearer sk-or-v1-test"
    assert sent.headers["http-referer"] == "https://github.com/timkush1/TrustAgent"
    assert sent.headers["x-openrouter-title"] == "TrustAgent"
    assert sent.headers["content-type"] == "application/json"

    body = transport.body()
    assert body["model"] == DEFAULT_OPENROUTER_JUDGE_MODEL == "anthropic/claude-haiku-4.5"
    assert body["usage"] == {"include": True}
    assert body["messages"][0] == {"role": "system", "content": "You are a fact checker."}
    assert "response_format" not in body


async def test_reported_cost_wins_over_the_price_table() -> None:
    transport = ScriptedTransport(json_reply(CHAT_OK))

    response = await provider(transport).complete(request())

    assert response.content == '{"status": "SUPPORTED"}'
    assert response.provider == "openrouter"
    assert (response.input_tokens, response.output_tokens) == (1200, 40)
    assert response.cost_usd == pytest.approx(0.00142)
    assert response.cost_source == "reported"


async def test_cost_is_computed_when_usage_has_no_cost() -> None:
    answer = dict(CHAT_OK, usage={"prompt_tokens": 1000, "completion_tokens": 200})
    transport = ScriptedTransport(json_reply(answer))

    response = await provider(transport).complete(request())

    assert response.cost_source == "computed"
    assert response.cost_usd == pytest.approx((1000 * 1.0 + 200 * 5.0) / 1_000_000)


def raw_json_reply(usage: str) -> httpx.Response:
    """CHAT_OK with `usage` as raw JSON text (NaN, Infinity and huge integers included)."""
    answer = dict(CHAT_OK, usage="USAGE")
    text = json.dumps(answer).replace('"USAGE"', usage)
    return httpx.Response(200, content=text.encode(), headers={"content-type": "application/json"})


@pytest.mark.parametrize(
    "usage",
    [
        '{"prompt_tokens": 1000, "completion_tokens": 200, "cost": 1' + "0" * 400 + "}",
        '{"prompt_tokens": 1000, "completion_tokens": 200, "cost": Infinity}',
        '{"prompt_tokens": 1000, "completion_tokens": 200, "cost": NaN}',
    ],
    ids=["cost-beyond-float", "cost-infinity", "cost-nan"],
)
async def test_a_reported_cost_no_float_holds_is_priced_from_the_table(usage: str) -> None:
    response = await provider(ScriptedTransport(raw_json_reply(usage))).complete(request())

    assert response.cost_source == "computed"
    assert response.cost_usd == pytest.approx((1000 * 1.0 + 200 * 5.0) / 1_000_000)


@pytest.mark.parametrize("bad", ["NaN", "Infinity", "-Infinity"])
async def test_token_counts_that_are_not_finite_count_as_zero(bad: str) -> None:
    usage = f'{{"prompt_tokens": {bad}, "completion_tokens": 200}}'

    response = await provider(ScriptedTransport(raw_json_reply(usage))).complete(request())

    assert (response.input_tokens, response.output_tokens) == (0, 200)
    assert response.cost_source == "computed"
    assert response.cost_usd == pytest.approx(200 * 5.0 / 1_000_000)


async def test_402_raises_budget_error_without_retrying() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply(
            {"error": {"code": 402, "message": "Insufficient credits. Add more credits."}}, 402
        )
    )

    with pytest.raises(BudgetError) as exc_info:
        await provider(transport, sleep=sleep).complete(request())

    error = exc_info.value
    assert error.kind == "credits" and error.status == 402 and not error.retryable
    assert "Insufficient credits" in str(error)
    assert transport.calls == 1
    assert sleep.delays == []


async def test_in_flight_budget_402_is_retried_after_retry_after() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply(
            {
                "error": {
                    "code": 402,
                    "message": "Request would exceed the in-flight budget",
                    "metadata": {"limit_source": "openrouter_in_flight_budget"},
                }
            },
            402,
            headers={"retry-after": "2"},
        ),
        json_reply(CHAT_OK),
    )

    response = await provider(transport, sleep=sleep).complete(request())

    assert response.cost_source == "reported"
    assert transport.calls == 2
    assert sleep.delays == [pytest.approx(2.0)]


async def test_html_answer_is_a_content_type_error() -> None:
    # A wrong base path gets the site's catch-all page: 200 text/html.
    transport = ScriptedTransport(text_reply("<!doctype html><html>OpenRouter</html>"))

    with pytest.raises(ProviderError) as exc_info:
        await provider(transport).complete(request())

    assert exc_info.value.kind == "bad_content_type"
    assert "text/html" in str(exc_info.value)
    assert transport.calls == 1  # never retried


async def test_json_with_a_charset_is_accepted() -> None:
    transport = ScriptedTransport(
        text_reply(
            '{"choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}]}',
            content_type="application/json; charset=utf-8",
        )
    )

    response = await provider(transport).complete(request())

    assert response.content == "ok"


async def test_error_inside_a_200_answer_is_retried_then_raised() -> None:
    sleep = SleepRecorder()
    transport = ScriptedTransport(
        json_reply({"error": {"code": 502, "message": "Upstream provider error"}})
    )

    with pytest.raises(ProviderError) as exc_info:
        await provider(transport, sleep=sleep).complete(request())

    assert exc_info.value.status == 502
    assert "Upstream provider error" in str(exc_info.value)
    assert transport.calls == 4


async def test_structured_output_uses_response_format() -> None:
    schema = {
        "type": "object",
        "properties": {"status": {"type": "string"}},
        "required": ["status"],
        "additionalProperties": False,
    }
    transport = ScriptedTransport(json_reply(CHAT_OK))

    response = await provider(transport).complete(
        request(response_schema=schema, schema_name="claim verdict")
    )

    assert transport.body()["response_format"] == {
        "type": "json_schema",
        "json_schema": {"name": "claim_verdict", "schema": schema, "strict": True},
    }
    assert response.structured is True


async def test_length_finish_reason_is_truncated() -> None:
    answer = dict(
        CHAT_OK, choices=[{"message": {"content": '{"status": "SUPP'}, "finish_reason": "length"}]
    )
    transport = ScriptedTransport(json_reply(answer))

    response = await provider(transport).complete(request())

    assert response.truncated is True


async def test_health_check_reads_the_key_endpoint() -> None:
    transport = ScriptedTransport(json_reply({"data": {"label": "sk-or-v1-...", "limit": None}}))

    assert await provider(transport).health_check() is True
    assert transport.requests[0].method == "GET"
    assert transport.path() == "/api/v1/key"


async def test_health_check_fails_on_an_invalid_key() -> None:
    transport = ScriptedTransport(json_reply({"error": {"code": 401, "message": "No auth"}}, 401))

    assert await provider(transport).health_check() is False


@pytest.mark.parametrize(
    "base_url, expected",
    [
        ("https://openrouter.ai", "https://openrouter.ai"),
        ("https://openrouter.ai/", "https://openrouter.ai"),
        ("https://openrouter.ai/api", "https://openrouter.ai"),
        ("https://openrouter.ai/api/v1/", "https://openrouter.ai"),
        ("http://mock-upstream:8081", "http://mock-upstream:8081"),
        ("", "https://openrouter.ai"),
    ],
)
def test_base_url_normalization(base_url: str, expected: str) -> None:
    assert normalize_base_url(base_url) == expected


async def test_custom_base_url_is_used_for_every_path() -> None:
    transport = ScriptedTransport(json_reply(CHAT_OK))

    await provider(transport, base_url="http://mock-upstream:8081/api/v1").complete(request())

    assert str(transport.requests[0].url) == "http://mock-upstream:8081/api/v1/chat/completions"


def test_requires_api_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("OPENROUTER_API_KEY", raising=False)
    with pytest.raises(ValueError, match="OPENROUTER_API_KEY"):
        OpenRouterProvider()


def test_registered_and_reads_key_from_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("OPENROUTER_API_KEY", "sk-or-v1-env")
    judge = get_provider("openrouter", model="anthropic/claude-haiku-4.5")
    assert isinstance(judge, OpenRouterProvider)
    assert judge.api_key == "sk-or-v1-env"
    assert judge.supports_structured_outputs is True
