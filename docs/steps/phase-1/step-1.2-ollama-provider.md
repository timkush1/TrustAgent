# Step 1.2: Ollama Provider Implementation

## 🎯 Goal

Implement `OllamaProvider` - a concrete implementation of our `LLMProvider` abstract class that talks to Ollama, a local LLM server. This gives us **free, local AI inference** for development.

---

## 📚 Prerequisites

- Completed Step 1.1 (LLM Provider Interface)
- Ollama running locally (from docker-compose)
- A model pulled: `docker-compose exec ollama ollama pull llama3.2`

Verify Ollama is running:
```bash
curl http://localhost:11434/
# Should output: Ollama is running
```

---

## 🧠 Concepts Explained

### What is Ollama?

Ollama is a local LLM server that lets you run open-source models (Llama, Mistral, etc.) on your own machine. Think of it as "Docker for LLMs."

| Cloud LLM (OpenAI) | Local LLM (Ollama) |
|--------------------|-------------------|
| Requires API key | No key needed |
| Costs money per token | Free |
| Data sent to cloud | Data stays local |
| Always latest models | Need to pull models |
| High availability | Depends on your hardware |

### Ollama API

Ollama exposes a REST API at `http://localhost:11434`:

```bash
# Generate completion (non-streaming)
curl http://localhost:11434/api/generate -d '{
  "model": "llama3.2",
  "prompt": "Hello!",
  "stream": false
}'

# Chat completion (conversation format)
curl http://localhost:11434/api/chat -d '{
  "model": "llama3.2",
  "messages": [{"role": "user", "content": "Hello!"}],
  "stream": false
}'
```

### HTTPX - Async HTTP Client

We use `httpx` instead of `requests` because it supports async:

```python
# Sync (blocks)
import requests
response = requests.post(url, json=data)

# Async (non-blocking)
import httpx
async with httpx.AsyncClient() as client:
    response = await client.post(url, json=data)
```

---

## 💻 Implementation

### Step 1: Create the Ollama Provider

Create `src/truthtable/providers/ollama.py`:

```python
"""
Ollama LLM Provider implementation.

Ollama is a local LLM server that runs open-source models.
Documentation: https://ollama.ai/docs/api
"""

import json
from typing import AsyncIterator, Any

import httpx

from .base import (
    LLMProvider,
    CompletionRequest,
    CompletionResponse,
    ProviderConnectionError,
    ProviderTimeoutError,
    ProviderError,
)


class OllamaProvider(LLMProvider):
    """
    LLM Provider for Ollama local inference.
    
    Example usage:
        provider = OllamaProvider(
            base_url="http://localhost:11434",
            model="llama3.2"
        )
        
        response = await provider.complete(CompletionRequest(
            messages=[Message(role="user", content="Hello!")]
        ))
        print(response.content)
    """
    
    def __init__(
        self,
        base_url: str = "http://localhost:11434",
        model: str = "llama3.2",
        timeout: float = 120.0,
        **kwargs,  # Accept extra kwargs for flexibility
    ):
        """
        Initialize Ollama provider.
        
        Args:
            base_url: Ollama server URL (default: localhost:11434)
            model: Model to use (e.g., "llama3.2", "mistral", "codellama")
            timeout: Request timeout in seconds (default: 120s)
        """
        self.base_url = base_url.rstrip("/")
        self.model = model
        self.timeout = timeout
        
        # Create HTTP client with configured timeout
        self._client = httpx.AsyncClient(
            base_url=self.base_url,
            timeout=httpx.Timeout(timeout),
        )
    
    @property
    def name(self) -> str:
        return "ollama"
    
    async def complete(
        self,
        request: CompletionRequest,
    ) -> CompletionResponse:
        """
        Generate a completion using Ollama's chat API.
        
        Uses /api/chat endpoint which handles conversation format.
        """
        # Build the request payload
        payload = self._build_chat_payload(request, stream=False)
        
        try:
            # Make the request
            response = await self._client.post(
                "/api/chat",
                json=payload,
            )
            response.raise_for_status()
            
            # Parse response
            data = response.json()
            
            return CompletionResponse(
                content=data["message"]["content"],
                model=data.get("model", self.model),
                usage={
                    "prompt_tokens": data.get("prompt_eval_count", 0),
                    "completion_tokens": data.get("eval_count", 0),
                    "total_duration_ns": data.get("total_duration", 0),
                },
            )
            
        except httpx.ConnectError as e:
            raise ProviderConnectionError(
                message=f"Cannot connect to Ollama at {self.base_url}. Is it running?",
                provider=self.name,
                cause=e,
            )
        except httpx.TimeoutException as e:
            raise ProviderTimeoutError(
                message=f"Request timed out after {self.timeout}s",
                provider=self.name,
                cause=e,
            )
        except httpx.HTTPStatusError as e:
            raise ProviderError(
                message=f"HTTP {e.response.status_code}: {e.response.text}",
                provider=self.name,
                cause=e,
            )
    
    async def stream(
        self,
        request: CompletionRequest,
    ) -> AsyncIterator[str]:
        """
        Stream a completion chunk by chunk.
        
        Ollama streams JSON lines, each containing a chunk of the response.
        """
        payload = self._build_chat_payload(request, stream=True)
        
        try:
            async with self._client.stream(
                "POST",
                "/api/chat",
                json=payload,
            ) as response:
                response.raise_for_status()
                
                async for line in response.aiter_lines():
                    if not line:
                        continue
                    
                    try:
                        data = json.loads(line)
                        
                        # Extract content from message
                        if "message" in data and "content" in data["message"]:
                            chunk = data["message"]["content"]
                            if chunk:
                                yield chunk
                        
                        # Check if stream is done
                        if data.get("done", False):
                            break
                            
                    except json.JSONDecodeError:
                        # Skip malformed lines
                        continue
                        
        except httpx.ConnectError as e:
            raise ProviderConnectionError(
                message=f"Cannot connect to Ollama at {self.base_url}",
                provider=self.name,
                cause=e,
            )
        except httpx.TimeoutException as e:
            raise ProviderTimeoutError(
                message=f"Stream timed out after {self.timeout}s",
                provider=self.name,
                cause=e,
            )
    
    async def health_check(self) -> bool:
        """Check if Ollama is running and the model is available."""
        try:
            # Check Ollama is running
            response = await self._client.get("/")
            if response.status_code != 200:
                return False
            
            # Check model is available
            response = await self._client.get("/api/tags")
            if response.status_code != 200:
                return False
            
            models = response.json().get("models", [])
            model_names = [m["name"] for m in models]
            
            # Check if our model (or a variant) is available
            return any(
                self.model in name or name.startswith(self.model)
                for name in model_names
            )
            
        except Exception:
            return False
    
    async def list_models(self) -> list[str]:
        """List all available models in Ollama."""
        try:
            response = await self._client.get("/api/tags")
            response.raise_for_status()
            
            models = response.json().get("models", [])
            return [m["name"] for m in models]
            
        except Exception:
            return []
    
    def _build_chat_payload(
        self,
        request: CompletionRequest,
        stream: bool = False,
    ) -> dict[str, Any]:
        """
        Build the payload for Ollama's /api/chat endpoint.
        
        Converts our CompletionRequest format to Ollama's expected format.
        """
        # Convert messages to Ollama format
        messages = [
            {"role": msg.role, "content": msg.content}
            for msg in request.messages
        ]
        
        # Use request model if specified, otherwise use default
        model = request.model or self.model
        
        payload = {
            "model": model,
            "messages": messages,
            "stream": stream,
            "options": {
                "temperature": request.temperature,
            },
        }
        
        # Add max_tokens if specified
        if request.max_tokens:
            payload["options"]["num_predict"] = request.max_tokens
        
        return payload
    
    async def close(self) -> None:
        """Close the HTTP client."""
        await self._client.aclose()
    
    async def __aenter__(self) -> "OllamaProvider":
        """Async context manager entry."""
        return self
    
    async def __aexit__(self, *args) -> None:
        """Async context manager exit."""
        await self.close()
```

### Step 2: Register the Provider

Update `src/truthtable/providers/__init__.py`:

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
from .ollama import OllamaProvider


# ===== Auto-register providers on import =====
register_provider("ollama", OllamaProvider)


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
    # Implementations
    "OllamaProvider",
]
```

### Step 3: Create a Factory Function

Add to `src/truthtable/providers/registry.py`:

```python
# Add this function at the end of the file

def create_provider_from_settings() -> LLMProvider:
    """
    Create a provider based on application settings.
    
    Reads from config which provider to use and creates it
    with the appropriate settings.
    """
    from ..config import get_settings
    
    settings = get_settings()
    backend = settings.llm_backend
    
    if backend == "ollama":
        return get_provider(
            "ollama",
            base_url=settings.ollama_base_url,
            model=settings.ollama_model,
        )
    elif backend == "openai":
        # We'll implement this in a future step
        raise NotImplementedError("OpenAI provider not yet implemented")
    else:
        raise ValueError(f"Unknown LLM backend: {backend}")
```

Update the `__init__.py` exports:

```python
# Add to imports
from .registry import (
    register_provider,
    get_provider,
    list_providers,
    create_provider_from_settings,  # Add this
)

# Add to __all__
__all__ = [
    # ... existing exports ...
    "create_provider_from_settings",
]
```

---

## ✅ Testing

### Test 1: Quick Smoke Test

Make sure Ollama is running with a model:

```bash
# Start Ollama (if not already running)
docker-compose up -d ollama

# Pull a model
docker-compose exec ollama ollama pull llama3.2

# Wait for it to be ready
sleep 5
```

Now test the provider:

```bash
cd backend-python
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider, CompletionRequest, Message

async def test():
    provider = OllamaProvider()
    
    # Test health check
    healthy = await provider.health_check()
    print(f'Health check: {\"✓\" if healthy else \"✗\"}')
    
    # List models
    models = await provider.list_models()
    print(f'Available models: {models}')
    
    # Test completion
    request = CompletionRequest(
        messages=[Message(role='user', content='Say hello in exactly 3 words')]
    )
    response = await provider.complete(request)
    print(f'Response: {response.content}')
    
    await provider.close()

asyncio.run(test())
"
```

Expected output:
```
Health check: ✓
Available models: ['llama3.2:latest']
Response: Hello to you!
```

### Test 2: Test Streaming

```bash
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider, CompletionRequest, Message

async def test_stream():
    provider = OllamaProvider()
    
    request = CompletionRequest(
        messages=[Message(role='user', content='Count from 1 to 5')]
    )
    
    print('Streaming response: ', end='', flush=True)
    async for chunk in provider.stream(request):
        print(chunk, end='', flush=True)
    print()  # Newline at end
    
    await provider.close()

asyncio.run(test_stream())
"
```

You should see the response appear word by word.

### Test 3: Test Error Handling

```bash
poetry run python -c "
import asyncio
from truthtable.providers import OllamaProvider, CompletionRequest, Message, ProviderConnectionError

async def test_connection_error():
    # Use wrong port to simulate connection error
    provider = OllamaProvider(base_url='http://localhost:99999')
    
    request = CompletionRequest(
        messages=[Message(role='user', content='Hello')]
    )
    
    try:
        await provider.complete(request)
        print('✗ Should have raised error')
    except ProviderConnectionError as e:
        print(f'✓ Correctly caught connection error: {e.message}')
    finally:
        await provider.close()

asyncio.run(test_connection_error())
"
```

### Test 4: Unit Tests

Create `tests/unit/test_ollama_provider.py`:

```python
"""Tests for Ollama provider."""

import pytest
from unittest.mock import AsyncMock, patch, MagicMock
import httpx

from truthtable.providers import (
    OllamaProvider,
    CompletionRequest,
    Message,
    ProviderConnectionError,
    ProviderTimeoutError,
)


@pytest.fixture
def provider():
    """Create a provider instance."""
    return OllamaProvider(
        base_url="http://localhost:11434",
        model="llama3.2",
    )


@pytest.fixture
def sample_request():
    """Create a sample completion request."""
    return CompletionRequest(
        messages=[
            Message(role="system", content="Be helpful"),
            Message(role="user", content="Hello!"),
        ],
        temperature=0.5,
    )


class TestOllamaProviderInit:
    """Tests for OllamaProvider initialization."""
    
    def test_default_values(self, provider):
        assert provider.base_url == "http://localhost:11434"
        assert provider.model == "llama3.2"
        assert provider.name == "ollama"
    
    def test_custom_values(self):
        provider = OllamaProvider(
            base_url="http://custom:8080",
            model="mistral",
            timeout=60.0,
        )
        assert provider.base_url == "http://custom:8080"
        assert provider.model == "mistral"


class TestBuildPayload:
    """Tests for payload building."""
    
    def test_basic_payload(self, provider, sample_request):
        payload = provider._build_chat_payload(sample_request, stream=False)
        
        assert payload["model"] == "llama3.2"
        assert payload["stream"] is False
        assert len(payload["messages"]) == 2
        assert payload["options"]["temperature"] == 0.5
    
    def test_with_max_tokens(self, provider):
        request = CompletionRequest(
            messages=[Message(role="user", content="Hi")],
            max_tokens=100,
        )
        payload = provider._build_chat_payload(request, stream=False)
        
        assert payload["options"]["num_predict"] == 100
    
    def test_custom_model_in_request(self, provider):
        request = CompletionRequest(
            messages=[Message(role="user", content="Hi")],
            model="codellama",
        )
        payload = provider._build_chat_payload(request, stream=False)
        
        assert payload["model"] == "codellama"


class TestComplete:
    """Tests for complete method."""
    
    @pytest.mark.asyncio
    async def test_successful_completion(self, provider, sample_request):
        # Mock the HTTP response
        mock_response = MagicMock()
        mock_response.json.return_value = {
            "message": {"content": "Hello there!"},
            "model": "llama3.2",
            "prompt_eval_count": 10,
            "eval_count": 5,
        }
        mock_response.raise_for_status = MagicMock()
        
        with patch.object(
            provider._client, 
            "post", 
            new_callable=AsyncMock,
            return_value=mock_response
        ):
            response = await provider.complete(sample_request)
        
        assert response.content == "Hello there!"
        assert response.model == "llama3.2"
        assert response.usage["prompt_tokens"] == 10
    
    @pytest.mark.asyncio
    async def test_connection_error(self, provider, sample_request):
        with patch.object(
            provider._client,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.ConnectError("Connection refused"),
        ):
            with pytest.raises(ProviderConnectionError) as exc_info:
                await provider.complete(sample_request)
        
        assert "Cannot connect to Ollama" in exc_info.value.message
    
    @pytest.mark.asyncio
    async def test_timeout_error(self, provider, sample_request):
        with patch.object(
            provider._client,
            "post",
            new_callable=AsyncMock,
            side_effect=httpx.TimeoutException("Request timed out"),
        ):
            with pytest.raises(ProviderTimeoutError) as exc_info:
                await provider.complete(sample_request)
        
        assert "timed out" in exc_info.value.message


class TestHealthCheck:
    """Tests for health check."""
    
    @pytest.mark.asyncio
    async def test_healthy(self, provider):
        # Mock successful responses
        async def mock_get(url):
            response = MagicMock()
            response.status_code = 200
            if "tags" in url:
                response.json.return_value = {
                    "models": [{"name": "llama3.2:latest"}]
                }
            return response
        
        with patch.object(
            provider._client,
            "get",
            new_callable=AsyncMock,
            side_effect=mock_get,
        ):
            healthy = await provider.health_check()
        
        assert healthy is True
    
    @pytest.mark.asyncio
    async def test_unhealthy_no_connection(self, provider):
        with patch.object(
            provider._client,
            "get",
            new_callable=AsyncMock,
            side_effect=httpx.ConnectError("No connection"),
        ):
            healthy = await provider.health_check()
        
        assert healthy is False
```

Run the tests:

```bash
poetry run pytest tests/unit/test_ollama_provider.py -v
```

### Test 5: Integration Test (Requires Running Ollama)

Create `tests/integration/test_ollama_integration.py`:

```python
"""Integration tests for Ollama provider (requires running Ollama)."""

import pytest
import httpx

from truthtable.providers import (
    OllamaProvider,
    CompletionRequest,
    Message,
)


def is_ollama_running() -> bool:
    """Check if Ollama is available."""
    try:
        response = httpx.get("http://localhost:11434/", timeout=2)
        return response.status_code == 200
    except Exception:
        return False


# Skip all tests in this file if Ollama is not running
pytestmark = pytest.mark.skipif(
    not is_ollama_running(),
    reason="Ollama is not running"
)


@pytest.fixture
async def provider():
    """Create and cleanup provider."""
    p = OllamaProvider()
    yield p
    await p.close()


class TestOllamaIntegration:
    """Integration tests against real Ollama."""
    
    @pytest.mark.asyncio
    async def test_health_check(self, provider):
        healthy = await provider.health_check()
        assert healthy is True
    
    @pytest.mark.asyncio
    async def test_list_models(self, provider):
        models = await provider.list_models()
        assert isinstance(models, list)
        # There should be at least one model
        assert len(models) > 0
    
    @pytest.mark.asyncio
    async def test_complete(self, provider):
        request = CompletionRequest(
            messages=[
                Message(role="user", content="What is 2+2? Answer with just the number.")
            ],
            temperature=0,  # Deterministic
            max_tokens=10,
        )
        
        response = await provider.complete(request)
        
        assert "4" in response.content
        assert response.model is not None
    
    @pytest.mark.asyncio
    async def test_stream(self, provider):
        request = CompletionRequest(
            messages=[
                Message(role="user", content="Count: 1, 2, 3")
            ],
            max_tokens=20,
        )
        
        chunks = []
        async for chunk in provider.stream(request):
            chunks.append(chunk)
        
        # Should have received multiple chunks
        assert len(chunks) > 0
        
        # Joined content should make sense
        full_response = "".join(chunks)
        assert len(full_response) > 0
```

Run integration tests:

```bash
# Make sure Ollama is running
docker-compose up -d ollama

# Run integration tests
poetry run pytest tests/integration/test_ollama_integration.py -v
```

---

## 🐛 Common Issues

### Issue: `Connection refused` when testing

**Solution:** Ollama might not be ready yet:
```bash
# Check if Ollama is running
curl http://localhost:11434/

# If not, start it
docker-compose up -d ollama

# Wait a few seconds for it to start
sleep 10
```

### Issue: `Model not found`

**Solution:** Pull the model first:
```bash
docker-compose exec ollama ollama pull llama3.2
```

### Issue: Slow responses

**Solution:** 
- First request is slow (model loading). Subsequent requests are faster.
- Consider using a smaller model: `ollama pull llama3.2:1b`
- If you have a GPU, enable it in docker-compose.yml

### Issue: `httpx.ReadTimeout`

**Solution:** Increase the timeout:
```python
provider = OllamaProvider(timeout=300.0)  # 5 minutes
```

---

## 📖 Further Reading

- [Ollama API Documentation](https://github.com/ollama/ollama/blob/main/docs/api.md)
- [HTTPX Documentation](https://www.python-httpx.org/)
- [Async Generators in Python](https://peps.python.org/pep-0525/)

---

## ⏭️ Next Step

Continue to [Step 1.3: Claim Decomposer Node](step-1.3-claim-decomposer.md) to build the first LangGraph node.
