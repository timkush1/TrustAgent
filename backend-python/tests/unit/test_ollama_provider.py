"""Unit tests for Ollama provider."""

import pytest
from unittest.mock import AsyncMock, patch, MagicMock
import httpx

from truthtable.providers.ollama import OllamaProvider
from truthtable.providers.base import CompletionRequest, Message, MessageRole


class TestOllamaProvider:
    """Tests for OllamaProvider."""
    
    @pytest.mark.asyncio
    async def test_initialization(self):
        """Test provider initialization."""
        provider = OllamaProvider(
            model="llama3.2",
            base_url="http://localhost:11434"
        )
        
        assert provider.model == "llama3.2"
        assert provider.base_url == "http://localhost:11434"
        assert provider.timeout == 60.0
    
    @pytest.mark.asyncio
    async def test_complete_success(self):
        """Test successful completion."""
        provider = OllamaProvider(model="llama3.2")
        
        # Mock the HTTP client
        mock_response = {
            "model": "llama3.2",
            "message": {"content": "The sky is blue."},
            "done_reason": "stop",
            "prompt_eval_count": 10,
            "eval_count": 5
        }
        
        with patch.object(provider, '_get_client') as mock_get_client:
            mock_client = AsyncMock()
            mock_http_response = MagicMock()
            mock_http_response.json.return_value = mock_response
            mock_http_response.raise_for_status = MagicMock()
            mock_client.post.return_value = mock_http_response
            mock_get_client.return_value = mock_client
            
            request = CompletionRequest(
                messages=[
                    Message(role=MessageRole.USER, content="Is the sky blue?")
                ],
                model="llama3.2"
            )
            
            response = await provider.complete(request)
            
            assert response.content == "The sky is blue."
            assert response.model == "llama3.2"
            assert response.finish_reason == "stop"
            assert response.usage["prompt_tokens"] == 10
            assert response.usage["completion_tokens"] == 5
            assert response.usage["total_tokens"] == 15
    
    @pytest.mark.asyncio
    async def test_complete_connection_error(self):
        """Test handling of connection errors."""
        provider = OllamaProvider(model="llama3.2")
        
        with patch.object(provider, '_get_client') as mock_get_client:
            mock_client = AsyncMock()
            mock_client.post.side_effect = httpx.ConnectError("Connection refused")
            mock_get_client.return_value = mock_client
            
            request = CompletionRequest(
                messages=[Message(role=MessageRole.USER, content="Test")],
                model="llama3.2"
            )
            
            with pytest.raises(ConnectionError) as exc_info:
                await provider.complete(request)
            
            assert "Cannot connect to Ollama" in str(exc_info.value)
    
    @pytest.mark.asyncio
    async def test_health_check_success(self):
        """Test successful health check."""
        provider = OllamaProvider(model="llama3.2")
        
        mock_response = {
            "models": [
                {"name": "llama3.2"},
                {"name": "mistral"}
            ]
        }
        
        with patch.object(provider, '_get_client') as mock_get_client:
            mock_client = AsyncMock()
            mock_http_response = MagicMock()
            mock_http_response.json.return_value = mock_response
            mock_http_response.raise_for_status = MagicMock()
            mock_client.get.return_value = mock_http_response
            mock_get_client.return_value = mock_client
            
            result = await provider.health_check()
            
            assert result is True
    
    @pytest.mark.asyncio
    async def test_health_check_model_not_found(self):
        """Test health check when model is not available."""
        provider = OllamaProvider(model="nonexistent-model")
        
        mock_response = {
            "models": [
                {"name": "llama3.2"}
            ]
        }
        
        with patch.object(provider, '_get_client') as mock_get_client:
            mock_client = AsyncMock()
            mock_http_response = MagicMock()
            mock_http_response.json.return_value = mock_response
            mock_http_response.raise_for_status = MagicMock()
            mock_client.get.return_value = mock_http_response
            mock_get_client.return_value = mock_client
            
            result = await provider.health_check()
            
            assert result is False
    
    @pytest.mark.asyncio
    async def test_context_manager(self):
        """Test using provider as context manager."""
        async with OllamaProvider(model="llama3.2") as provider:
            assert provider is not None
            assert provider._client is None  # Client is lazy-initialized
        
        # After exit, client should be closed
        # (We can't easily test this without actually creating a client)
