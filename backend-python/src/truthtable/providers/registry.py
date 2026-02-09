"""
Provider Registry

Manages registration and retrieval of LLM providers.
This implements the Registry pattern for dynamic provider selection.
"""

from typing import Dict, Type, Optional
from .base import LLMProvider


class ProviderRegistry:
    """
    Registry for LLM providers.
    
    Allows registering providers by name and retrieving them later.
    This makes it easy to switch providers via configuration.
    
    Usage:
        # Register a provider class
        registry.register("ollama", OllamaProvider)
        
        # Get an instance
        provider = registry.get("ollama", model="llama3.2", base_url="...")
    """
    
    def __init__(self):
        self._providers: Dict[str, Type[LLMProvider]] = {}
    
    def register(self, name: str, provider_class: Type[LLMProvider]) -> None:
        """
        Register a provider class.
        
        Args:
            name: Identifier for this provider (e.g., "ollama", "openai")
            provider_class: The provider class (not an instance)
        """
        self._providers[name] = provider_class
    
    def get(self, name: str, **kwargs) -> LLMProvider:
        """
        Get a provider instance.
        
        Args:
            name: Provider identifier
            **kwargs: Arguments passed to provider constructor
            
        Returns:
            Initialized provider instance
            
        Raises:
            ValueError: If provider not registered
        """
        if name not in self._providers:
            available = ", ".join(self._providers.keys())
            raise ValueError(
                f"Provider '{name}' not registered. "
                f"Available providers: {available}"
            )
        
        provider_class = self._providers[name]
        return provider_class(**kwargs)
    
    def list_providers(self) -> list[str]:
        """Get list of registered provider names."""
        return list(self._providers.keys())


# Global registry instance
_registry = ProviderRegistry()


def register_provider(name: str, provider_class: Type[LLMProvider]) -> None:
    """Register a provider in the global registry."""
    _registry.register(name, provider_class)


def get_provider(name: str, **kwargs) -> LLMProvider:
    """Get a provider from the global registry."""
    return _registry.get(name, **kwargs)


def list_providers() -> list[str]:
    """List all registered providers."""
    return _registry.list_providers()
