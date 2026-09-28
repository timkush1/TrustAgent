"""Register all available providers."""

from .anthropic import AnthropicProvider
from .errors import BudgetError, ProviderConnectionError, ProviderError
from .limiter import JudgeLimiter, LimitedProvider
from .mock import MockLLMProvider
from .ollama import OllamaProvider
from .openai import OpenAIProvider
from .openrouter import OpenRouterProvider
from .registry import register_provider

# Register providers
register_provider("ollama", OllamaProvider)
register_provider("openai", OpenAIProvider)
register_provider("anthropic", AnthropicProvider)
register_provider("openrouter", OpenRouterProvider)
register_provider("mock", MockLLMProvider)

__all__ = [
    "AnthropicProvider",
    "BudgetError",
    "JudgeLimiter",
    "LimitedProvider",
    "MockLLMProvider",
    "OllamaProvider",
    "OpenAIProvider",
    "OpenRouterProvider",
    "ProviderConnectionError",
    "ProviderError",
    "register_provider",
]
