"""Register all available providers."""

from .registry import register_provider
from .ollama import OllamaProvider
from .mock import MockLLMProvider

# Register providers
register_provider("ollama", OllamaProvider)
register_provider("mock", MockLLMProvider)

# Future providers can be registered here:
# register_provider("openai", OpenAIProvider)
# register_provider("anthropic", AnthropicProvider)

__all__ = [
    "OllamaProvider",
    "MockLLMProvider",
    "register_provider",
]
