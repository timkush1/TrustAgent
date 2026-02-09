# Step 1.1: LLM Provider Interface

## 🎯 Goal

Create an abstract base class (`LLMProvider`) that defines how TruthTable talks to any LLM. This is the **adapter pattern** - we define a common interface, then create specific implementations for different LLMs (Ollama, OpenAI, Anthropic).

**Why this matters:** Without this abstraction, switching from Ollama (free, local) to OpenAI (paid, production) would require rewriting all your code. With it, you just swap one line of configuration.

---

## 📚 Prerequisites

- Completed Phase 0 (project structure, proto file, docker-compose)
- Python 3.11+ installed
- Poetry installed (`curl -sSL https://install.python-poetry.org | python3 -`)

---

## 🧠 Concepts Explained

### The Adapter Pattern

Imagine you have different phone chargers (USB-C, Lightning, Micro-USB). An adapter pattern is like a universal charging station - your phone doesn't care which charger it uses, as long as it gets power.

```
Without Adapter:                 With Adapter:
┌──────────┐                    ┌──────────┐
│  Code    │───► Ollama         │  Code    │───► LLMProvider ───┬──► Ollama
│          │                    │          │                    ├──► OpenAI
│          │  (tightly coupled) │          │  (loosely coupled) └──► Anthropic
└──────────┘                    └──────────┘
```

### Abstract Base Class (ABC)

In Python, an ABC is a class that:
1. **Cannot be instantiated directly** (you can't do `provider = LLMProvider()`)
2. **Defines methods that subclasses MUST implement**
3. **Provides a contract** - any class inheriting from it guarantees certain methods exist

```python
from abc import ABC, abstractmethod

class LLMProvider(ABC):
    @abstractmethod
    def complete(self, prompt: str) -> str:
        """Subclasses MUST implement this"""
        pass

# This will ERROR:
provider = LLMProvider()  # TypeError: Can't instantiate abstract class

# This works:
class OllamaProvider(LLMProvider):
    def complete(self, prompt: str) -> str:
        return "response from Ollama"

provider = OllamaProvider()  # ✓ Works!
```

### Async/Await in Python

LLM calls are **I/O bound** - we spend most time waiting for network responses. `async/await` lets Python do other work while waiting:

```python
# Synchronous (blocking) - waits and does nothing
response = call_llm(prompt)  # 2 seconds waiting...

# Asynchronous (non-blocking) - can do other things while waiting
response = await call_llm(prompt)  # Python can handle other requests
```

---

## 💻 Implementation

### Step 1: Set Up Python Project

```bash
cd backend-python

# Initialize with Poetry (if not already done)
poetry init --name truthtable --python "^3.11" -n

# Add core dependencies
poetry add grpcio grpcio-tools protobuf pydantic pydantic-settings httpx

# Add LangChain/LangGraph for orchestration
poetry add langchain langgraph langchain-core

# Add dev dependencies
poetry add --group dev pytest pytest-asyncio pytest-cov black ruff mypy

# Create virtual environment and install
poetry install
```

### Step 2: Create Configuration

Create `src/truthtable/config.py`:

```python
"""
Configuration management using Pydantic Settings.

Pydantic Settings automatically loads values from:
1. Environment variables
2. .env file
3. Default values (defined in class)

Priority: Environment variables > .env file > defaults
"""

from functools import lru_cache
from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """Application settings with validation."""
    
    # ===== gRPC Server =====
    grpc_port: int = 50051
    grpc_max_workers: int = 10
    
    # ===== Redis =====
    redis_url: str = "redis://localhost:6379"
    audit_queue_key: str = "truthtable:audit:queue"
    audit_events_key: str = "truthtable:audit:events"
    
    # ===== Qdrant =====
    qdrant_host: str = "localhost"
    qdrant_port: int = 6333
    qdrant_collection: str = "truthtable_context"
    
    # ===== LLM Provider =====
    # Which backend to use: "ollama", "openai", "anthropic"
    llm_backend: str = "ollama"
    
    # Ollama settings
    ollama_base_url: str = "http://localhost:11434"
    ollama_model: str = "llama3.2"
    
    # OpenAI settings (optional)
    openai_api_key: str = ""
    openai_model: str = "gpt-4o"
    
    # ===== Observability =====
    log_level: str = "INFO"
    
    class Config:
        # Load from .env file if present
        env_file = ".env"
        env_file_encoding = "utf-8"
        # Prefix for environment variables
        env_prefix = "TRUTHTABLE_"


@lru_cache
def get_settings() -> Settings:
    """
    Get cached settings instance.
    
    @lru_cache ensures we only parse settings once,
    not on every function call.
    """
    return Settings()
```

### Step 3: Create the Abstract Base Class

Create `src/truthtable/providers/base.py`:

```python
"""
Abstract base class for LLM providers.

This defines the contract that ALL LLM providers must follow.
By programming to this interface (not concrete implementations),
we can easily swap providers without changing application code.
"""

from abc import ABC, abstractmethod
from typing import AsyncIterator
from pydantic import BaseModel, Field


# ===== Data Models =====
# These define the structure of requests and responses

class Message(BaseModel):
    """A single message in a conversation."""
    role: str = Field(..., description="Role: 'system', 'user', or 'assistant'")
    content: str = Field(..., description="The message content")


class CompletionRequest(BaseModel):
    """Request to generate a completion."""
    messages: list[Message] = Field(..., description="Conversation history")
    model: str | None = Field(None, description="Model to use (provider-specific)")
    temperature: float = Field(0.7, ge=0.0, le=2.0, description="Randomness (0=deterministic)")
    max_tokens: int | None = Field(None, description="Maximum tokens to generate")
    
    def get_prompt(self) -> str:
        """Extract the last user message as a simple prompt."""
        for msg in reversed(self.messages):
            if msg.role == "user":
                return msg.content
        return ""
    
    def get_system_prompt(self) -> str | None:
        """Extract the system message if present."""
        for msg in self.messages:
            if msg.role == "system":
                return msg.content
        return None


class CompletionResponse(BaseModel):
    """Response from an LLM completion."""
    content: str = Field(..., description="Generated text")
    model: str = Field(..., description="Model that generated this")
    usage: dict = Field(default_factory=dict, description="Token usage stats")
    
    @property
    def text(self) -> str:
        """Alias for content."""
        return self.content


# ===== Abstract Base Class =====

class LLMProvider(ABC):
    """
    Abstract base class for LLM providers.
    
    Any class that inherits from this MUST implement:
    - name (property): Returns provider identifier
    - complete(): Generates a completion
    - stream(): Streams a completion chunk by chunk
    
    Optional to override:
    - health_check(): Verifies provider is available
    """
    
    @property
    @abstractmethod
    def name(self) -> str:
        """
        Return the provider's identifier.
        
        Examples: "ollama", "openai", "anthropic"
        """
        pass
    
    @abstractmethod
    async def complete(
        self,
        request: CompletionRequest,
    ) -> CompletionResponse:
        """
        Generate a completion for the given request.
        
        Args:
            request: The completion request with messages and parameters
            
        Returns:
            CompletionResponse with generated text
            
        Raises:
            ProviderError: If the provider fails
        """
        pass
    
    @abstractmethod
    async def stream(
        self,
        request: CompletionRequest,
    ) -> AsyncIterator[str]:
        """
        Stream a completion chunk by chunk.
        
        This is an async generator - use with `async for`:
        
        ```python
        async for chunk in provider.stream(request):
            print(chunk, end="")
        ```
        
        Args:
            request: The completion request
            
        Yields:
            String chunks of the response
        """
        pass
    
    async def health_check(self) -> bool:
        """
        Check if the provider is available.
        
        Default implementation tries a minimal completion.
        Override for provider-specific health checks.
        
        Returns:
            True if healthy, False otherwise
        """
        try:
            test_request = CompletionRequest(
                messages=[Message(role="user", content="hi")],
                max_tokens=1,
            )
            await self.complete(test_request)
            return True
        except Exception:
            return False
    
    def __repr__(self) -> str:
        return f"<{self.__class__.__name__} name={self.name}>"


# ===== Custom Exceptions =====

class ProviderError(Exception):
    """Base exception for provider errors."""
    
    def __init__(self, message: str, provider: str, cause: Exception | None = None):
        self.message = message
        self.provider = provider
        self.cause = cause
        super().__init__(f"[{provider}] {message}")


class ProviderConnectionError(ProviderError):
    """Provider is unreachable."""
    pass


class ProviderTimeoutError(ProviderError):
    """Provider request timed out."""
    pass


class ProviderRateLimitError(ProviderError):
    """Provider rate limit exceeded."""
    pass
```

### Step 4: Create the Provider Registry

Create `src/truthtable/providers/registry.py`:

```python
"""
Provider registry for dynamic provider selection.

This implements the Factory pattern - instead of creating providers directly,
you ask the registry to give you the right one based on configuration.
"""

from typing import Type
from .base import LLMProvider


class ProviderRegistry:
    """
    Registry for LLM providers.
    
    Usage:
        registry = ProviderRegistry()
        registry.register("ollama", OllamaProvider)
        
        provider = registry.get("ollama", **config)
    """
    
    def __init__(self):
        self._providers: dict[str, Type[LLMProvider]] = {}
        self._instances: dict[str, LLMProvider] = {}
    
    def register(self, name: str, provider_class: Type[LLMProvider]) -> None:
        """
        Register a provider class.
        
        Args:
            name: Identifier for this provider (e.g., "ollama")
            provider_class: The class (not instance) to register
        """
        self._providers[name] = provider_class
    
    def get(self, name: str, **kwargs) -> LLMProvider:
        """
        Get or create a provider instance.
        
        Args:
            name: Provider identifier
            **kwargs: Arguments to pass to provider constructor
            
        Returns:
            LLMProvider instance
            
        Raises:
            ValueError: If provider is not registered
        """
        if name not in self._providers:
            available = ", ".join(self._providers.keys())
            raise ValueError(
                f"Unknown provider: {name}. Available: {available}"
            )
        
        # Create new instance with provided kwargs
        provider_class = self._providers[name]
        return provider_class(**kwargs)
    
    def get_singleton(self, name: str, **kwargs) -> LLMProvider:
        """
        Get a singleton instance (reuse existing if available).
        
        Use this when you want to reuse connection pools, etc.
        """
        if name not in self._instances:
            self._instances[name] = self.get(name, **kwargs)
        return self._instances[name]
    
    def list_providers(self) -> list[str]:
        """List all registered provider names."""
        return list(self._providers.keys())
    
    def is_registered(self, name: str) -> bool:
        """Check if a provider is registered."""
        return name in self._providers


# Global registry instance
_registry = ProviderRegistry()


def register_provider(name: str, provider_class: Type[LLMProvider]) -> None:
    """Register a provider globally."""
    _registry.register(name, provider_class)


def get_provider(name: str, **kwargs) -> LLMProvider:
    """Get a provider from the global registry."""
    return _registry.get(name, **kwargs)


def list_providers() -> list[str]:
    """List all registered providers."""
    return _registry.list_providers()
```

### Step 5: Create Package Init Files

Create `src/truthtable/providers/__init__.py`:

```python
"""
LLM Provider abstractions.

This package provides a unified interface for different LLM backends.
"""

from .base import (
    LLMProvider,
    CompletionRequest,
    CompletionResponse,
    Message,
    ProviderError,
    ProviderConnectionError,
    ProviderTimeoutError,
    ProviderRateLimitError,
)
from .registry import (
    register_provider,
    get_provider,
    list_providers,
)

__all__ = [
    # Base class
    "LLMProvider",
    # Data models
    "CompletionRequest",
    "CompletionResponse", 
    "Message",
    # Exceptions
    "ProviderError",
    "ProviderConnectionError",
    "ProviderTimeoutError",
    "ProviderRateLimitError",
    # Registry
    "register_provider",
    "get_provider",
    "list_providers",
]
```

Update `src/truthtable/__init__.py`:

```python
"""
TruthTable - AI Hallucination Detection Engine.

This package provides tools for detecting hallucinations in LLM outputs
by verifying claims against source context.
"""

__version__ = "0.1.0"
```

---

## ✅ Testing

### Test 1: Verify Module Imports

```bash
cd backend-python
poetry run python -c "
from truthtable.providers import (
    LLMProvider,
    CompletionRequest,
    CompletionResponse,
    Message,
    get_provider,
    list_providers,
)
print('✓ All imports successful!')
print(f'Available providers: {list_providers()}')
"
```

Expected output:
```
✓ All imports successful!
Available providers: []
```

(Empty list is expected - we haven't registered any providers yet!)

### Test 2: Verify Abstract Class Cannot Be Instantiated

```bash
poetry run python -c "
from truthtable.providers import LLMProvider

try:
    provider = LLMProvider()
    print('✗ Should have failed!')
except TypeError as e:
    print(f'✓ Correctly prevented instantiation: {e}')
"
```

Expected output:
```
✓ Correctly prevented instantiation: Can't instantiate abstract class LLMProvider...
```

### Test 3: Create Unit Tests

Create `tests/unit/test_providers_base.py`:

```python
"""Tests for the LLM provider base class."""

import pytest
from truthtable.providers import (
    LLMProvider,
    CompletionRequest,
    CompletionResponse,
    Message,
    register_provider,
    get_provider,
    list_providers,
)


class TestMessage:
    """Tests for Message model."""
    
    def test_create_message(self):
        msg = Message(role="user", content="Hello!")
        assert msg.role == "user"
        assert msg.content == "Hello!"
    
    def test_message_validation(self):
        # Role and content are required
        with pytest.raises(ValueError):
            Message(role="user")  # Missing content


class TestCompletionRequest:
    """Tests for CompletionRequest model."""
    
    def test_create_request(self):
        request = CompletionRequest(
            messages=[Message(role="user", content="Hello")]
        )
        assert len(request.messages) == 1
        assert request.temperature == 0.7  # default
    
    def test_get_prompt(self):
        request = CompletionRequest(
            messages=[
                Message(role="system", content="You are helpful"),
                Message(role="user", content="Hello"),
                Message(role="assistant", content="Hi there!"),
                Message(role="user", content="How are you?"),
            ]
        )
        # Should return last user message
        assert request.get_prompt() == "How are you?"
    
    def test_get_system_prompt(self):
        request = CompletionRequest(
            messages=[
                Message(role="system", content="Be concise"),
                Message(role="user", content="Hello"),
            ]
        )
        assert request.get_system_prompt() == "Be concise"
    
    def test_temperature_validation(self):
        # Temperature must be 0-2
        with pytest.raises(ValueError):
            CompletionRequest(
                messages=[Message(role="user", content="Hi")],
                temperature=3.0,  # Invalid!
            )


class TestLLMProviderAbstract:
    """Tests for LLMProvider ABC."""
    
    def test_cannot_instantiate_directly(self):
        with pytest.raises(TypeError):
            LLMProvider()
    
    def test_subclass_must_implement_methods(self):
        # Incomplete implementation should fail
        class IncompleteProvider(LLMProvider):
            @property
            def name(self):
                return "incomplete"
            # Missing complete() and stream()
        
        with pytest.raises(TypeError):
            IncompleteProvider()


class TestProviderRegistry:
    """Tests for provider registry."""
    
    def test_register_and_get(self):
        # Create a mock provider
        class MockProvider(LLMProvider):
            def __init__(self, **kwargs):
                self.kwargs = kwargs
            
            @property
            def name(self):
                return "mock"
            
            async def complete(self, request):
                return CompletionResponse(content="mock", model="mock")
            
            async def stream(self, request):
                yield "mock"
        
        register_provider("mock", MockProvider)
        
        provider = get_provider("mock", foo="bar")
        assert provider.name == "mock"
        assert provider.kwargs["foo"] == "bar"
    
    def test_unknown_provider_raises(self):
        with pytest.raises(ValueError) as exc_info:
            get_provider("nonexistent")
        
        assert "Unknown provider" in str(exc_info.value)
```

Run the tests:

```bash
cd backend-python
poetry run pytest tests/unit/test_providers_base.py -v
```

Expected output:
```
tests/unit/test_providers_base.py::TestMessage::test_create_message PASSED
tests/unit/test_providers_base.py::TestMessage::test_message_validation PASSED
tests/unit/test_providers_base.py::TestCompletionRequest::test_create_request PASSED
...
```

---

## 🐛 Common Issues

### Issue: `ModuleNotFoundError: No module named 'truthtable'`

**Solution:** Install in development mode:
```bash
poetry install
```

Or make sure you're running from the `backend-python` directory.

### Issue: `pydantic_settings not found`

**Solution:** Install it:
```bash
poetry add pydantic-settings
```

### Issue: Tests fail with import errors

**Solution:** Ensure `__init__.py` files exist in all directories:
```bash
find src -type d -exec touch {}/__init__.py \;
```

---

## 📖 Further Reading

- [Python ABC Documentation](https://docs.python.org/3/library/abc.html)
- [Pydantic Models](https://docs.pydantic.dev/latest/concepts/models/)
- [Async/Await in Python](https://realpython.com/async-io-python/)
- [Adapter Pattern](https://refactoring.guru/design-patterns/adapter)

---

## ⏭️ Next Step

Continue to [Step 1.2: Ollama Provider Implementation](step-1.2-ollama-provider.md) to create our first concrete provider.
