"""Register all available providers."""

from .registry import register_provider
from .ollama import OllamaProvider
from .openai import OpenAIProvider
from .anthropic import AnthropicProvider
from .mock import MockLLMProvider

# Register providers
register_provider("ollama", OllamaProvider)
register_provider("openai", OpenAIProvider)
register_provider("anthropic", AnthropicProvider)
register_provider("mock", MockLLMProvider)

__all__ = [
    "OllamaProvider",
    "OpenAIProvider",
    "AnthropicProvider",
    "MockLLMProvider",
    "register_provider",
]
