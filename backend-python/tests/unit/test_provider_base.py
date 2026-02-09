"""Unit tests for LLM Provider base interface."""

import pytest
from truthtable.providers.base import (
    Message,
    MessageRole,
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
)


class TestMessage:
    """Tests for Message dataclass."""
    
    def test_message_creation(self):
        """Test creating a message."""
        msg = Message(role=MessageRole.USER, content="Hello")
        assert msg.role == MessageRole.USER
        assert msg.content == "Hello"
    
    def test_message_to_dict(self):
        """Test converting message to dictionary."""
        msg = Message(role=MessageRole.SYSTEM, content="You are helpful")
        result = msg.to_dict()
        
        assert result == {
            "role": "system",
            "content": "You are helpful"
        }


class TestCompletionRequest:
    """Tests for CompletionRequest dataclass."""
    
    def test_request_creation(self):
        """Test creating a completion request."""
        messages = [
            Message(role=MessageRole.SYSTEM, content="Be helpful"),
            Message(role=MessageRole.USER, content="Hello"),
        ]
        
        request = CompletionRequest(
            messages=messages,
            model="llama3.2",
            temperature=0.7,
            max_tokens=100
        )
        
        assert request.model == "llama3.2"
        assert request.temperature == 0.7
        assert request.max_tokens == 100
        assert len(request.messages) == 2
    
    def test_request_defaults(self):
        """Test default values."""
        messages = [Message(role=MessageRole.USER, content="Hi")]
        request = CompletionRequest(messages=messages, model="test")
        
        assert request.temperature == 0.0  # Default for fact-checking
        assert request.max_tokens == 2048
    
    def test_request_to_dict(self):
        """Test converting request to dictionary."""
        messages = [Message(role=MessageRole.USER, content="Test")]
        request = CompletionRequest(messages=messages, model="gpt-4")
        
        result = request.to_dict()
        
        assert result["model"] == "gpt-4"
        assert result["temperature"] == 0.0
        assert result["max_tokens"] == 2048
        assert len(result["messages"]) == 1


class TestCompletionResponse:
    """Tests for CompletionResponse dataclass."""
    
    def test_response_creation(self):
        """Test creating a response."""
        response = CompletionResponse(
            content="Hello!",
            model="llama3.2",
            finish_reason="stop",
            usage={"prompt_tokens": 10, "completion_tokens": 5}
        )
        
        assert response.content == "Hello!"
        assert response.model == "llama3.2"
        assert response.finish_reason == "stop"
        assert response.usage["prompt_tokens"] == 10
    
    def test_response_from_dict(self):
        """Test creating response from dictionary."""
        data = {
            "content": "Response text",
            "model": "gpt-4",
            "finish_reason": "stop",
            "usage": {"total_tokens": 20}
        }
        
        response = CompletionResponse.from_dict(data)
        
        assert response.content == "Response text"
        assert response.model == "gpt-4"
        assert response.finish_reason == "stop"
        assert response.usage == {"total_tokens": 20}
    
    def test_response_from_dict_minimal(self):
        """Test creating response with minimal data."""
        data = {
            "content": "Text",
            "model": "test"
        }
        
        response = CompletionResponse.from_dict(data)
        
        assert response.content == "Text"
        assert response.finish_reason == "unknown"
        assert response.usage is None


class TestLLMProvider:
    """Tests for LLMProvider abstract base class."""
    
    def test_create_messages(self):
        """Test the convenience method for creating messages."""
        
        # Create a concrete implementation for testing
        class MockProvider(LLMProvider):
            async def complete(self, request):
                return CompletionResponse(
                    content="test",
                    model=self.model,
                    finish_reason="stop"
                )
            
            async def health_check(self):
                return True
        
        provider = MockProvider(model="test-model")
        messages = provider.create_messages(
            system_prompt="You are helpful",
            user_message="Hello"
        )
        
        assert len(messages) == 2
        assert messages[0].role == MessageRole.SYSTEM
        assert messages[0].content == "You are helpful"
        assert messages[1].role == MessageRole.USER
        assert messages[1].content == "Hello"
    
    @pytest.mark.asyncio
    async def test_provider_must_implement_methods(self):
        """Test that abstract methods must be implemented."""
        
        # This should fail because we don't implement the abstract methods
        with pytest.raises(TypeError):
            class IncompleteProvider(LLMProvider):
                pass
            
            IncompleteProvider(model="test")
