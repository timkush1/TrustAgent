"""Register all available providers."""

from .registry import register_provider
from .ollama import OllamaProvider

# Register Ollama provider
register_provider("ollama", OllamaProvider)

# Future providers can be registered here:
# register_provider("openai", OpenAIProvider)
# register_provider("anthropic", AnthropicProvider)

__all__ = [
    "OllamaProvider",
    "register_provider",
]
