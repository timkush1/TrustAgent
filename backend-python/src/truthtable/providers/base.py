"""
LLM Provider Base Interface

This module defines the abstract interface that all LLM providers must implement.
This allows us to easily swap between different LLM backends (Ollama, OpenAI, Anthropic)
without changing any of the business logic code.
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import Enum
from typing import List, Optional, Dict, Any


class MessageRole(str, Enum):
    """Roles for chat messages following the standard chat format."""
    
    SYSTEM = "system"
    USER = "user"
    ASSISTANT = "assistant"


@dataclass
class Message:
    """
    A single message in a chat conversation.
    
    Attributes:
        role: Who sent this message (system/user/assistant)
        content: The actual text content
    """
    
    role: MessageRole
    content: str
    
    def to_dict(self) -> Dict[str, str]:
        """Convert to dictionary format for API calls."""
        return {
            "role": self.role.value,
            "content": self.content
        }


@dataclass
class CompletionRequest:
    """
    Request for LLM text generation.
    
    This is intentionally simple - we only include what we actually need
    for the audit engine. You can extend this later if needed.
    
    Attributes:
        messages: Conversation history (system prompt + user messages)
        model: Which model to use (e.g., "llama3.2", "gpt-4")
        temperature: Randomness (0.0 = deterministic, 1.0 = creative)
        max_tokens: Maximum length of response
    """
    
    messages: List[Message]
    model: str
    temperature: float = 0.0  # Default to deterministic for fact-checking
    max_tokens: int = 2048
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary format for API calls."""
        return {
            "model": self.model,
            "messages": [msg.to_dict() for msg in self.messages],
            "temperature": self.temperature,
            "max_tokens": self.max_tokens,
        }


@dataclass
class CompletionResponse:
    """
    Response from LLM text generation.
    
    Attributes:
        content: The generated text
        model: Which model was actually used
        finish_reason: Why generation stopped ("stop", "length", "error")
        usage: Token counts (if available)
    """
    
    content: str
    model: str
    finish_reason: str
    usage: Optional[Dict[str, int]] = None
    
    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "CompletionResponse":
        """Create from API response dictionary."""
        return cls(
            content=data["content"],
            model=data["model"],
            finish_reason=data.get("finish_reason", "unknown"),
            usage=data.get("usage"),
        )


class LLMProvider(ABC):
    """
    Abstract base class for all LLM providers.
    
    Any new provider (OpenAI, Anthropic, Cohere, etc.) must implement
    these methods. This ensures consistency across different backends.
    
    Usage:
        provider = OllamaProvider(base_url="http://localhost:11434")
        response = await provider.complete(request)
    """
    
    def __init__(self, model: str, **kwargs):
        """
        Initialize the provider.
        
        Args:
            model: Default model name to use
            **kwargs: Provider-specific configuration
        """
        self.model = model
        self.config = kwargs
    
    @abstractmethod
    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        """
        Generate text completion from the LLM.
        
        Args:
            request: The completion request with messages and parameters
            
        Returns:
            CompletionResponse with generated text
            
        Raises:
            ConnectionError: If provider is unreachable
            ValueError: If request is invalid
            RuntimeError: If generation fails
        """
        pass
    
    @abstractmethod
    async def health_check(self) -> bool:
        """
        Check if the provider is reachable and ready.
        
        Returns:
            True if healthy, False otherwise
        """
        pass
    
    def create_messages(
        self, 
        system_prompt: str, 
        user_message: str
    ) -> List[Message]:
        """
        Convenience method to create a standard message list.
        
        Args:
            system_prompt: Instructions for the model
            user_message: The actual user query
            
        Returns:
            List of Message objects ready for completion
        """
        return [
            Message(role=MessageRole.SYSTEM, content=system_prompt),
            Message(role=MessageRole.USER, content=user_message),
        ]
