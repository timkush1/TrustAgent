"""
Structured outputs per provider and the `truncated` flag (SPEC section 3.2).

Anthropic's parameter was verified against the public docs (structured.py):
`output_config: {format: {type: "json_schema", schema}}`, no beta header.
"""

from typing import Any, Dict

import pytest

from provider_fakes import ScriptedTransport, json_reply
from truthtable.providers.anthropic import AnthropicProvider
from truthtable.providers.base import (
    CompletionRequest,
    CompletionResponse,
    Message,
    MessageRole,
)
from truthtable.providers.errors import ProviderError
from truthtable.providers.mock import MockLLMProvider
from truthtable.providers.ollama import OllamaProvider
from truthtable.providers.openai import OpenAIProvider
from truthtable.providers.structured import (
    StructuredOutputsMode,
    anthropic_schema,
    openai_response_format,
    openai_schema_name,
)

VERDICT_SCHEMA: Dict[str, Any] = {
    "type": "object",
    "properties": {
        "status": {
            "type": "string",
            "enum": ["SUPPORTED", "PARTIALLY_SUPPORTED", "CONTRADICTED", "UNSUPPORTED"],
        },
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
        "evidence": {"type": "array", "items": {"type": "string", "maxLength": 300}, "maxItems": 3},
        "reasoning": {"type": "string", "minLength": 1},
    },
    "required": ["status", "confidence", "evidence", "reasoning"],
}

ANTHROPIC_OK = {
    "model": "claude-haiku-4-5",
    "content": [{"type": "text", "text": '{"status": "SUPPORTED"}'}],
    "stop_reason": "end_turn",
    "usage": {"input_tokens": 10, "output_tokens": 5},
}

OUTPUT_CONFIG_REJECTED = {
    "type": "error",
    "error": {
        "type": "invalid_request_error",
        "message": "output_config: Extra inputs are not permitted",
    },
}


def request(schema: Any = VERDICT_SCHEMA) -> CompletionRequest:
    return CompletionRequest(
        messages=[Message(MessageRole.SYSTEM, "Judge."), Message(MessageRole.USER, "claim")],
        model="",
        response_schema=schema,
        schema_name="verdict",
    )


# ---------------------------------------------------------------------------
# Anthropic
# ---------------------------------------------------------------------------


def test_anthropic_schema_drops_unsupported_keywords_and_closes_objects() -> None:
    schema = {
        "type": "object",
        "properties": {
            "maximum": {"type": "integer", "minimum": 0},  # a property named like a keyword
            "items": {"type": "array", "items": {"type": "object", "properties": {}}},
            "choice": {"anyOf": [{"type": "string", "maxLength": 3}, {"type": "null"}]},
            "tags": {"type": "array", "minItems": 2, "items": {"type": "string"}},
            "one": {"type": "array", "minItems": 1, "items": {"type": "string"}},
        },
        "$defs": {"inner": {"type": "object", "properties": {"x": {"multipleOf": 2}}}},
    }

    reduced = anthropic_schema(schema)

    assert reduced["additionalProperties"] is False
    props = reduced["properties"]
    assert props["maximum"] == {"type": "integer"}
    assert props["items"]["items"] == {
        "type": "object",
        "properties": {},
        "additionalProperties": False,
    }
    assert props["choice"]["anyOf"] == [{"type": "string"}, {"type": "null"}]
    assert "minItems" not in props["tags"]
    assert props["one"]["minItems"] == 1
    assert reduced["$defs"]["inner"] == {
        "type": "object",
        "properties": {"x": {}},
        "additionalProperties": False,
    }
    # The caller's schema is not modified.
    assert schema["properties"]["maximum"]["minimum"] == 0


async def test_anthropic_sends_output_config_without_a_beta_header() -> None:
    transport = ScriptedTransport(json_reply(ANTHROPIC_OK))
    judge = AnthropicProvider(api_key="k", transport=transport)

    response = await judge.complete(request())

    body = transport.body()
    assert body["output_config"] == {
        "format": {"type": "json_schema", "schema": anthropic_schema(VERDICT_SCHEMA)}
    }
    confidence = body["output_config"]["format"]["schema"]["properties"]["confidence"]
    assert confidence == {"type": "number"}  # the range is checked in code
    assert "output_format" not in body
    assert "anthropic-beta" not in transport.requests[0].headers
    assert response.structured is True
    assert judge.supports_structured_outputs is True


async def test_anthropic_free_text_request_has_no_output_config() -> None:
    transport = ScriptedTransport(json_reply(ANTHROPIC_OK))

    response = await AnthropicProvider(api_key="k", transport=transport).complete(request(None))

    assert "output_config" not in transport.body()
    assert response.structured is False


async def test_anthropic_auto_mode_drops_a_rejected_output_config_once() -> None:
    transport = ScriptedTransport(json_reply(OUTPUT_CONFIG_REJECTED, 400), json_reply(ANTHROPIC_OK))
    judge = AnthropicProvider(api_key="k", transport=transport)

    first = await judge.complete(request())

    assert transport.calls == 2
    assert "output_config" in transport.body(0)
    assert "output_config" not in transport.body(1)
    assert first.structured is False
    assert judge.supports_structured_outputs is False

    await judge.complete(request())

    assert transport.calls == 3
    assert "output_config" not in transport.body(2)  # not tried again


async def test_anthropic_on_mode_surfaces_the_rejection() -> None:
    transport = ScriptedTransport(json_reply(OUTPUT_CONFIG_REJECTED, 400))
    judge = AnthropicProvider(api_key="k", transport=transport, structured_outputs="on")

    with pytest.raises(ProviderError) as exc_info:
        await judge.complete(request())

    assert exc_info.value.kind == "invalid"
    assert transport.calls == 1
    assert judge.supports_structured_outputs is True


async def test_anthropic_auto_mode_keeps_structured_outputs_for_other_400s() -> None:
    transport = ScriptedTransport(
        json_reply({"error": {"message": "messages: at least one message is required"}}, 400)
    )
    judge = AnthropicProvider(api_key="k", transport=transport)

    with pytest.raises(ProviderError):
        await judge.complete(request())

    assert transport.calls == 1
    assert judge.supports_structured_outputs is True


async def test_anthropic_off_mode_never_sends_output_config() -> None:
    transport = ScriptedTransport(json_reply(ANTHROPIC_OK))
    judge = AnthropicProvider(api_key="k", transport=transport, structured_outputs="off")

    response = await judge.complete(request())

    assert "output_config" not in transport.body()
    assert response.structured is False
    assert judge.supports_structured_outputs is False


def test_structured_outputs_mode_parsing() -> None:
    assert StructuredOutputsMode.parse("AUTO") is StructuredOutputsMode.AUTO
    assert StructuredOutputsMode.parse(StructuredOutputsMode.OFF) is StructuredOutputsMode.OFF
    with pytest.raises(ValueError, match="auto, on or off"):
        StructuredOutputsMode.parse("maybe")


# ---------------------------------------------------------------------------
# OpenAI, Ollama, mock
# ---------------------------------------------------------------------------


async def test_openai_sends_a_strict_json_schema_response_format() -> None:
    transport = ScriptedTransport(
        json_reply({"choices": [{"message": {"content": "{}"}, "finish_reason": "stop"}]})
    )

    response = await OpenAIProvider(api_key="k", transport=transport).complete(request())

    assert transport.body()["response_format"] == openai_response_format(VERDICT_SCHEMA, "verdict")
    assert transport.body()["response_format"]["json_schema"]["strict"] is True
    assert response.structured is True


def test_openai_schema_names_are_sanitized() -> None:
    assert openai_schema_name("claim verdict/v2") == "claim_verdict_v2"
    assert openai_schema_name("") == "output"
    assert len(openai_schema_name("x" * 100)) == 64


async def test_ollama_sends_the_schema_as_format() -> None:
    transport = ScriptedTransport(
        json_reply({"message": {"content": "{}"}, "done_reason": "stop", "model": "llama3.2:1b"})
    )

    response = await OllamaProvider(model="llama3.2:1b", transport=transport).complete(request())

    assert transport.body()["format"] == VERDICT_SCHEMA
    assert response.structured is True


async def test_mock_ignores_the_schema_and_simulates_the_cost() -> None:
    judge = MockLLMProvider(fixtures=[{"match": "claim", "response": "{}"}])

    response = await judge.complete(request())

    assert response.content == "{}"
    assert response.structured is False
    assert judge.supports_structured_outputs is False
    assert (response.cost_usd, response.cost_source, response.provider) == (
        0.0,
        "simulated",
        "mock",
    )


# ---------------------------------------------------------------------------
# truncated
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "finish_reason, truncated",
    [("max_tokens", True), ("length", True), ("end_turn", False), ("stop", False), ("", False)],
)
def test_truncated_flag(finish_reason: str, truncated: bool) -> None:
    response = CompletionResponse(content="x", model="m", finish_reason=finish_reason)
    assert response.truncated is truncated


async def test_anthropic_max_tokens_is_truncated() -> None:
    answer = dict(ANTHROPIC_OK, stop_reason="max_tokens")
    transport = ScriptedTransport(json_reply(answer))

    response = await AnthropicProvider(api_key="k", transport=transport).complete(request())

    assert response.truncated is True


async def test_ollama_length_is_truncated() -> None:
    transport = ScriptedTransport(
        json_reply({"message": {"content": "{"}, "done_reason": "length", "model": "m"})
    )

    response = await OllamaProvider(model="m", transport=transport).complete(request())

    assert response.truncated is True


async def test_anthropic_drops_a_rejected_temperature_per_model() -> None:
    transport = ScriptedTransport(
        json_reply(
            {"error": {"type": "invalid_request_error", "message": "temperature: not supported"}},
            400,
        ),
        json_reply(ANTHROPIC_OK),
    )
    judge = AnthropicProvider(api_key="k", model="claude-sonnet-5", transport=transport)

    await judge.complete(request())
    await judge.complete(request())

    assert "temperature" in transport.body(0)
    assert "temperature" not in transport.body(1)
    assert "temperature" not in transport.body(2)
    assert "output_config" in transport.body(2)  # structured outputs unaffected
    assert transport.calls == 3
