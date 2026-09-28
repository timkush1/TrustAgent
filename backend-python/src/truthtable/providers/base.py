"""
LLM Provider Base Interface

This module defines the abstract interface that all LLM providers must implement.
This allows us to easily swap between different LLM backends (Ollama, OpenAI, Anthropic,
OpenRouter) without changing any of the business logic code.

v2 additions (SPEC section 3.2):
- CompletionRequest.response_schema / schema_name ask for structured output
  (structured.py maps them onto each provider's API); timeout_s bounds each attempt.
- CompletionResponse carries normalized token usage, the call's cost and its
  cost_source (pricing.py), and a `truncated` flag for answers cut off by max_tokens.
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import Enum
from typing import Any, ClassVar, Dict, List, Optional

from .pricing import UNKNOWN, PriceTable, price_call

# finish_reason values that mean the answer was cut off by max_tokens
# (Anthropic "max_tokens"; OpenAI, OpenRouter and Ollama "length").
TRUNCATED_FINISH_REASONS = frozenset({"max_tokens", "length"})


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
        return {"role": self.role.value, "content": self.content}


@dataclass
class CompletionRequest:
    """
    Request for LLM text generation.

    Attributes:
        messages: Conversation history (system prompt + user messages)
        model: Which model to use (e.g., "llama3.2", "gpt-4")
        temperature: Randomness (0.0 = deterministic, 1.0 = creative)
        max_tokens: Maximum length of response
        response_schema: JSON Schema the answer must match (structured outputs);
            None asks for free text
        schema_name: Name of the schema, for APIs that require one (OpenAI)
        timeout_s: Per-attempt timeout in seconds; None uses the provider default
    """

    messages: List[Message]
    model: str
    temperature: float = 0.0  # Default to deterministic for fact-checking
    max_tokens: int = 2048
    response_schema: Optional[Dict[str, Any]] = None
    schema_name: str = "output"
    timeout_s: Optional[float] = None

    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary format for API calls."""
        return {
            "model": self.model,
            "messages": [msg.to_dict() for msg in self.messages],
            "temperature": self.temperature,
            "max_tokens": self.max_tokens,
        }


def normalized_usage(input_tokens: int, output_tokens: int) -> Dict[str, int]:
    """
    Token usage under both naming conventions: input_tokens/output_tokens (v2)
    and prompt_tokens/completion_tokens/total_tokens (v1 callers).
    """
    input_tokens, output_tokens = max(0, int(input_tokens)), max(0, int(output_tokens))
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "prompt_tokens": input_tokens,
        "completion_tokens": output_tokens,
        "total_tokens": input_tokens + output_tokens,
    }


@dataclass
class CompletionResponse:
    """
    Response from LLM text generation.

    Attributes:
        content: The generated text
        model: Which model was actually used
        finish_reason: Why generation stopped ("stop", "length", "max_tokens", ...)
        usage: Token counts (if available); see normalized_usage
        provider: Provider that answered ("anthropic", "openrouter", ...)
        cost_usd: Cost of the call; None when unknown
        cost_source: reported|computed|free_local|simulated|unknown (pricing.py)
        latency_ms: Wall time of the call, retries included
        structured: Whether the provider constrained the answer to the request's
            response_schema (native structured outputs)
    """

    content: str
    model: str
    finish_reason: str
    usage: Optional[Dict[str, int]] = None
    provider: str = ""
    cost_usd: Optional[float] = None
    cost_source: str = UNKNOWN
    latency_ms: int = 0
    structured: bool = False

    @property
    def truncated(self) -> bool:
        """The answer was cut off by max_tokens (it is incomplete)."""
        return self.finish_reason in TRUNCATED_FINISH_REASONS

    @property
    def input_tokens(self) -> int:
        usage = self.usage or {}
        return int(usage.get("input_tokens", usage.get("prompt_tokens", 0)))

    @property
    def output_tokens(self) -> int:
        usage = self.usage or {}
        return int(usage.get("output_tokens", usage.get("completion_tokens", 0)))

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

    #: Provider name used for pricing and reporting ("anthropic", "ollama", ...).
    provider_name: ClassVar[str] = ""

    def __init__(self, model: str, *, price_table: Optional[PriceTable] = None, **kwargs: Any):
        """
        Initialize the provider.

        Args:
            model: Default model name to use
            price_table: Prices for cost_usd; None uses the active table (pricing.py)
            **kwargs: Provider-specific configuration
        """
        self.model = model
        self.config = kwargs
        self.price_table = price_table

    @abstractmethod
    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        """
        Generate text completion from the LLM.

        Args:
            request: The completion request with messages and parameters

        Returns:
            CompletionResponse with generated text

        Raises:
            ProviderError: For provider failures (a RuntimeError; see errors.py).
                ProviderConnectionError (also a ConnectionError) when the
                provider is unreachable.
            ValueError: If request is invalid
        """

    @abstractmethod
    async def health_check(self) -> bool:
        """
        Check if the provider is reachable and ready.

        Returns:
            True if healthy, False otherwise
        """

    @property
    def supports_structured_outputs(self) -> bool:
        """Whether a request's response_schema is enforced by the provider's API."""
        return False

    async def close(self) -> None:
        """Release network resources (HTTP clients). Safe to call more than once."""

    def create_messages(self, system_prompt: str, user_message: str) -> List[Message]:
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

    def priced(
        self,
        response: CompletionResponse,
        *,
        reported_cost_usd: Optional[float] = None,
    ) -> CompletionResponse:
        """Fill response.provider, cost_usd and cost_source from the price table."""
        provider = response.provider or self.provider_name
        cost = price_call(
            provider,
            response.model or self.model,
            response.input_tokens,
            response.output_tokens,
            reported_cost_usd,
            self.price_table,
        )
        response.provider = provider
        response.cost_usd = cost.cost_usd
        response.cost_source = cost.cost_source
        return response
