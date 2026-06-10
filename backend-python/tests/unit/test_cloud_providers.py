"""Unit tests for the OpenAI and Anthropic providers (mocked HTTP)."""

import pytest
from unittest.mock import AsyncMock, MagicMock, patch

import httpx

from truthtable.providers.anthropic import AnthropicProvider
from truthtable.providers.base import CompletionRequest, Message, MessageRole
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


def mock_client_returning(payload: dict) -> AsyncMock:
    client = AsyncMock()
    response = MagicMock()
    response.json.return_value = payload
    response.raise_for_status = MagicMock()
    client.post.return_value = response
    return client


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
        provider = OpenAIProvider(model="gpt-4o-mini", api_key="sk-test")
        payload = {
            "model": "gpt-4o-mini",
            "choices": [{"message": {"content": "Yes."}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15},
        }

        with patch.object(provider, "_get_client") as get_client:
            client = mock_client_returning(payload)
            get_client.return_value = client

            response = await provider.complete(make_request("gpt-4o-mini"))

        assert response.content == "Yes."
        assert response.finish_reason == "stop"
        assert response.usage["total_tokens"] == 15

        # System prompt stays in the messages array for OpenAI.
        sent = client.post.call_args.kwargs["json"]
        assert sent["messages"][0]["role"] == "system"

    async def test_complete_http_error(self):
        provider = OpenAIProvider(model="gpt-4o-mini", api_key="sk-test")

        with patch.object(provider, "_get_client") as get_client:
            client = AsyncMock()
            response = MagicMock()
            response.status_code = 429
            response.text = "rate limited"
            response.raise_for_status.side_effect = httpx.HTTPStatusError(
                "429", request=MagicMock(), response=response
            )
            client.post.return_value = response
            get_client.return_value = client

            with pytest.raises(RuntimeError, match="429"):
                await provider.complete(make_request("gpt-4o-mini"))


class TestAnthropicProvider:
    def test_requires_api_key(self, monkeypatch):
        monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
        with pytest.raises(ValueError, match="ANTHROPIC_API_KEY"):
            AnthropicProvider(model="claude-haiku-4-5-20251001")

    async def test_complete_success_extracts_text_blocks(self):
        provider = AnthropicProvider(model="claude-haiku-4-5-20251001", api_key="sk-ant-test")
        payload = {
            "model": "claude-haiku-4-5-20251001",
            "content": [{"type": "text", "text": "Yes, the sky is blue."}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 20, "output_tokens": 7},
        }

        with patch.object(provider, "_get_client") as get_client:
            client = mock_client_returning(payload)
            get_client.return_value = client

            response = await provider.complete(make_request("claude-haiku-4-5-20251001"))

        assert response.content == "Yes, the sky is blue."
        assert response.finish_reason == "end_turn"
        assert response.usage["total_tokens"] == 27

    async def test_system_prompt_is_top_level(self):
        provider = AnthropicProvider(model="claude-haiku-4-5-20251001", api_key="sk-ant-test")
        payload = {
            "content": [{"type": "text", "text": "ok"}],
            "stop_reason": "end_turn",
            "usage": {},
        }

        with patch.object(provider, "_get_client") as get_client:
            client = mock_client_returning(payload)
            get_client.return_value = client

            await provider.complete(make_request("claude-haiku-4-5-20251001"))

        sent = client.post.call_args.kwargs["json"]
        assert sent["system"] == "You are a fact checker."
        assert all(m["role"] != "system" for m in sent["messages"])


def test_providers_registered():
    with pytest.MonkeyPatch.context() as mp:
        mp.setenv("OPENAI_API_KEY", "sk-test")
        mp.setenv("ANTHROPIC_API_KEY", "sk-ant-test")
        assert isinstance(get_provider("openai", model="gpt-4o-mini"), OpenAIProvider)
        assert isinstance(
            get_provider("anthropic", model="claude-haiku-4-5-20251001"), AnthropicProvider
        )
