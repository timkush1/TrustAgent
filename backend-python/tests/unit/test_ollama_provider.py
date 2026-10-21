"""Unit tests for Ollama provider (scripted HTTP, no network)."""

import pytest

from provider_fakes import ScriptedTransport, SleepRecorder, connect_error, json_reply
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.errors import ProviderConnectionError
from truthtable.providers.ollama import OllamaProvider


def make_request() -> CompletionRequest:
    return CompletionRequest(
        messages=[Message(role=MessageRole.USER, content="Is the sky blue?")], model="llama3.2"
    )


class TestOllamaProvider:
    """Tests for OllamaProvider."""

    async def test_initialization(self):
        """Test provider initialization."""
        provider = OllamaProvider(model="llama3.2", base_url="http://localhost:11434")

        assert provider.model == "llama3.2"
        assert provider.base_url == "http://localhost:11434"
        assert provider.timeout == 180.0

    async def test_complete_success(self):
        """Test successful completion."""
        transport = ScriptedTransport(
            json_reply(
                {
                    "model": "llama3.2",
                    "message": {"content": "The sky is blue."},
                    "done_reason": "stop",
                    "prompt_eval_count": 10,
                    "eval_count": 5,
                }
            )
        )
        provider = OllamaProvider(model="llama3.2", transport=transport)

        response = await provider.complete(make_request())

        assert response.content == "The sky is blue."
        assert response.model == "llama3.2"
        assert response.finish_reason == "stop"
        assert response.usage["prompt_tokens"] == 10
        assert response.usage["completion_tokens"] == 5
        assert response.usage["total_tokens"] == 15
        assert (response.cost_usd, response.cost_source) == (0.0, "free_local")

        sent = transport.body()
        assert transport.path() == "/api/chat"
        assert sent["stream"] is False
        assert sent["options"] == {"temperature": 0.0, "num_predict": 2048}

    async def test_complete_connection_error(self):
        """Connection errors are retried, then raised as a ConnectionError."""
        sleep = SleepRecorder()
        transport = ScriptedTransport(connect_error())
        provider = OllamaProvider(model="llama3.2", transport=transport, sleep=sleep)

        with pytest.raises(ConnectionError) as exc_info:
            await provider.complete(make_request())

        assert isinstance(exc_info.value, ProviderConnectionError)
        assert "Cannot connect to Ollama" in str(exc_info.value)
        assert transport.calls == 4
        assert len(sleep.delays) == 3

    async def test_health_check_success(self):
        """Test successful health check."""
        transport = ScriptedTransport(
            json_reply({"models": [{"name": "llama3.2:latest"}, {"name": "mistral"}]})
        )
        provider = OllamaProvider(model="llama3.2", transport=transport)

        assert await provider.health_check() is True
        assert transport.path() == "/api/tags"

    async def test_health_check_model_not_found(self):
        """Test health check when model is not available."""
        transport = ScriptedTransport(json_reply({"models": [{"name": "llama3.2"}]}))
        provider = OllamaProvider(model="nonexistent-model", transport=transport)

        assert await provider.health_check() is False

    async def test_reachable_ignores_the_model(self):
        transport = ScriptedTransport(json_reply({"models": []}))
        provider = OllamaProvider(model="nonexistent-model", transport=transport)

        assert await provider.reachable() is True
        assert await provider.health_check() is False

    async def test_unreachable_server(self):
        transport = ScriptedTransport(connect_error())
        provider = OllamaProvider(model="llama3.2", transport=transport)

        assert await provider.reachable() is False
        assert await provider.health_check() is False
        assert transport.calls == 2  # probes are never retried

    async def test_context_manager(self):
        """Test using provider as context manager."""
        async with OllamaProvider(model="llama3.2") as provider:
            assert provider is not None
            assert provider._client is None  # Client is lazy-initialized

        assert provider._client is None
