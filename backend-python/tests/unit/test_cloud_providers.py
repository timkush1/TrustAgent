"""Unit tests for the OpenAI and Anthropic providers (scripted HTTP, no network)."""

import pytest

from provider_fakes import ScriptedTransport, SleepRecorder, json_reply
from truthtable.providers.anthropic import ANTHROPIC_VERSION, AnthropicProvider
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.errors import ProviderError
from truthtable.providers.openai import OpenAIProvider
from truthtable.providers.registry import get_provider


def make_request(model: str) -> CompletionRequest:
    return CompletionRequest(
        messages=[
            Message(role=MessageRole.SYSTEM, content="You are a fact checker."),
            Message(role=MessageRole.USER, content="Is the sky blue?"),
        ],
        model=model,
    )


class TestOpenAIProvider:
    def test_requires_api_key(self, monkeypatch):
        monkeypatch.delenv("OPENAI_API_KEY", raising=False)
        with pytest.raises(ValueError, match="OPENAI_API_KEY"):
            OpenAIProvider(model="gpt-4o-mini")

    def test_reads_key_from_env(self, monkeypatch):
        monkeypatch.setenv("OPENAI_API_KEY", "sk-test")
        provider = OpenAIProvider(model="gpt-4o-mini")
        assert provider.api_key == "sk-test"

    async def test_complete_success(self):
        transport = ScriptedTransport(
            json_reply(
                {
                    "model": "gpt-4o-mini",
                    "choices": [{"message": {"content": "Yes."}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
                }
            )
        )
        provider = OpenAIProvider(model="gpt-4o-mini", api_key="sk-test", transport=transport)

        response = await provider.complete(make_request("gpt-4o-mini"))

        assert response.content == "Yes."
        assert response.finish_reason == "stop"
        assert response.usage["total_tokens"] == 15
        assert (response.input_tokens, response.output_tokens) == (12, 3)
        assert response.provider == "openai"
        assert not response.truncated

        request = transport.requests[0]
        assert request.url.path == "/v1/chat/completions"
        assert request.headers["authorization"] == "Bearer sk-test"
        # System prompt stays in the messages array for OpenAI.
        assert transport.body()["messages"][0]["role"] == "system"
        assert "response_format" not in transport.body()

    async def test_complete_http_error(self):
        sleep = SleepRecorder()
        transport = ScriptedTransport(json_reply({"error": {"message": "rate limited"}}, 429))
        provider = OpenAIProvider(
            model="gpt-4o-mini", api_key="sk-test", transport=transport, sleep=sleep
        )

        with pytest.raises(RuntimeError, match="429") as exc_info:
            await provider.complete(make_request("gpt-4o-mini"))

        assert isinstance(exc_info.value, ProviderError)
        assert exc_info.value.kind == "rate_limited"
        assert "rate limited" in str(exc_info.value)
        assert transport.calls == 4  # the first attempt and 3 retries
        assert len(sleep.delays) == 3

    async def test_null_content_is_empty_text(self):
        transport = ScriptedTransport(
            json_reply({"choices": [{"message": {"content": None}, "finish_reason": "stop"}]})
        )
        provider = OpenAIProvider(model="gpt-4o-mini", api_key="sk-test", transport=transport)

        response = await provider.complete(make_request("gpt-4o-mini"))

        assert response.content == ""
        assert response.model == "gpt-4o-mini"

    async def test_answer_without_choices_is_a_bad_response(self):
        transport = ScriptedTransport(json_reply({"object": "chat.completion"}))
        provider = OpenAIProvider(model="gpt-4o-mini", api_key="sk-test", transport=transport)

        with pytest.raises(ProviderError) as exc_info:
            await provider.complete(make_request("gpt-4o-mini"))

        assert exc_info.value.kind == "bad_response"
        assert transport.calls == 1


class TestAnthropicProvider:
    def test_requires_api_key(self, monkeypatch):
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
        with pytest.raises(ValueError, match="ANTHROPIC_API_KEY"):
            AnthropicProvider(model="claude-haiku-4-5")

    async def test_complete_success_extracts_text_blocks(self):
        transport = ScriptedTransport(
            json_reply(
                {
                    "model": "claude-haiku-4-5-20251001",
                    "content": [
                        {"type": "text", "text": "Yes, the sky "},
                        {"type": "text", "text": "is blue."},
                    ],
                    "stop_reason": "end_turn",
                    "usage": {"input_tokens": 20, "output_tokens": 7},
                }
            )
        )
        provider = AnthropicProvider(
            model="claude-haiku-4-5", api_key="sk-ant-test", transport=transport
        )

        response = await provider.complete(make_request("claude-haiku-4-5"))

        assert response.content == "Yes, the sky is blue."
        assert response.finish_reason == "end_turn"
        assert response.usage["total_tokens"] == 27
        assert response.model == "claude-haiku-4-5-20251001"
        assert not response.truncated

        request = transport.requests[0]
        assert request.url.path == "/v1/messages"
        assert request.headers["x-api-key"] == "sk-ant-test"
        assert request.headers["anthropic-version"] == ANTHROPIC_VERSION

    async def test_system_prompt_is_top_level(self):
        transport = ScriptedTransport(
            json_reply({"content": [{"type": "text", "text": "ok"}], "stop_reason": "end_turn"})
        )
        provider = AnthropicProvider(
            model="claude-haiku-4-5", api_key="sk-ant-test", transport=transport
        )

        await provider.complete(make_request("claude-haiku-4-5"))

        sent = transport.body()
        assert sent["system"] == "You are a fact checker."
        assert all(m["role"] != "system" for m in sent["messages"])

    async def test_health_check_lists_models(self):
        transport = ScriptedTransport(json_reply({"data": []}))
        provider = AnthropicProvider(
            model="claude-haiku-4-5", api_key="sk-ant-test", transport=transport
        )

        assert await provider.health_check() is True
        assert transport.path() == "/v1/models"

    async def test_health_check_fails_on_auth_error(self):
        transport = ScriptedTransport(json_reply({"error": {"message": "invalid x-api-key"}}, 401))
        provider = AnthropicProvider(
            model="claude-haiku-4-5", api_key="sk-ant-test", transport=transport
        )

        assert await provider.health_check() is False
        assert transport.calls == 1


def test_providers_registered():
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv("OPENAI_API_KEY", "sk-test")
        mp.setenv("ANTHROPIC_API_KEY", "sk-ant-test")
        assert isinstance(get_provider("openai", model="gpt-4o-mini"), OpenAIProvider)
        assert isinstance(get_provider("anthropic", model="claude-haiku-4-5"), AnthropicProvider)
