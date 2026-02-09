"""
Ollama Provider Implementation

Implements the LLMProvider interface for Ollama - a local LLM runtime.
Ollama allows running models like Llama, Mistral, etc. on your own hardware.
"""

import httpx
from typing import Optional, Dict, Any
import logging

from .base import LLMProvider, CompletionRequest, CompletionResponse

logger = logging.getLogger(__name__)


class OllamaProvider(LLMProvider):
    """
    Provider for Ollama local LLM service.
    
    Ollama runs models locally and exposes an OpenAI-compatible API.
    This is perfect for development and cost-free experimentation.
    
    Args:
        model: Model name (e.g., "llama3.2", "mistral")
        base_url: Ollama server URL (default: http://localhost:11434)
        timeout: Request timeout in seconds
    
    Example:
        provider = OllamaProvider(
            model="llama3.2",
            base_url="http://localhost:11434"
        )
        
        request = CompletionRequest(
            messages=provider.create_messages(
                system_prompt="You are a fact checker",
                user_message="Is the sky blue?"
            ),
            model="llama3.2"
        )
        
        response = await provider.complete(request)
        print(response.content)
    """
    
    def __init__(
        self,
        model: str,
        base_url: str = "http://localhost:11434",
        timeout: float = 60.0,
        **kwargs
    ):
        """Initialize Ollama provider."""
        super().__init__(model=model, **kwargs)
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        
        # Create async HTTP client (reuse connections)
        self._client: Optional[httpx.AsyncClient] = None
    
    async def _get_client(self) -> httpx.AsyncClient:
        """Get or create the HTTP client (lazy initialization)."""
        if self._client is None:
            self._client = httpx.AsyncClient(
                base_url=self.base_url,
                timeout=self.timeout,
                headers={"Content-Type": "application/json"}
            )
        return self._client
    
    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        """
        Generate text completion using Ollama.
        
        Args:
            request: Completion request with messages and parameters
            
        Returns:
            CompletionResponse with generated text
            
        Raises:
            ConnectionError: If Ollama server is unreachable
            ValueError: If request is invalid
            RuntimeError: If generation fails
        """
        client = await self._get_client()
        
        # Prepare request payload (Ollama format)
        payload = {
            "model": request.model or self.model,
            "messages": [msg.to_dict() for msg in request.messages],
            "stream": False,  # We want the full response, not streaming
            "options": {
                "temperature": request.temperature,
                "num_predict": request.max_tokens,
            }
        }
        
        try:
            logger.debug(f"Sending request to Ollama: model={payload['model']}")
            
            # Make API call
            response = await client.post("/api/chat", json=payload)
            response.raise_for_status()
            
            # Parse response
            data = response.json()
            
            # Extract the assistant's message
            message = data.get("message", {})
            content = message.get("content", "")
            
            # Build response object
            return CompletionResponse(
                content=content,
                model=data.get("model", request.model),
                finish_reason=data.get("done_reason", "stop"),
                usage={
                    "prompt_tokens": data.get("prompt_eval_count", 0),
                    "completion_tokens": data.get("eval_count", 0),
                    "total_tokens": data.get("prompt_eval_count", 0) + data.get("eval_count", 0),
                }
            )
            
        except httpx.ConnectError as e:
            logger.error(f"Failed to connect to Ollama at {self.base_url}: {e}")
            raise ConnectionError(
                f"Cannot connect to Ollama at {self.base_url}. "
                f"Is the server running? Try: docker-compose up ollama"
            ) from e
        
        except httpx.HTTPStatusError as e:
            logger.error(f"Ollama returned error {e.response.status_code}: {e.response.text}")
            raise RuntimeError(
                f"Ollama request failed: {e.response.status_code} - {e.response.text}"
            ) from e
        
        except Exception as e:
            logger.error(f"Unexpected error calling Ollama: {e}")
            raise RuntimeError(f"Failed to get completion from Ollama: {e}") from e
    
    async def health_check(self) -> bool:
        """
        Check if Ollama server is reachable and healthy.
        
        Returns:
            True if server is responding, False otherwise
        """
        client = await self._get_client()
        
        try:
            # Ollama has a /api/tags endpoint that lists models
            response = await client.get("/api/tags", timeout=5.0)
            response.raise_for_status()
            
            # Check if our model is available
            data = response.json()
            models = [m["name"] for m in data.get("models", [])]
            
            # Check if model matches (handle :latest suffix)
            model_found = False
            for m in models:
                # Match exact name or with :latest suffix
                if m == self.model or m == f"{self.model}:latest" or m.startswith(f"{self.model}:"):
                    model_found = True
                    break
            
            if not model_found:
                logger.warning(
                    f"Model '{self.model}' not found in Ollama. "
                    f"Available models: {', '.join(models)}"
                )
                return False
            
            logger.info(f"Ollama health check passed. Model '{self.model}' is available.")
            return True
            
        except Exception as e:
            logger.error(f"Ollama health check failed: {e}")
            return False
    
    async def close(self):
        """Close the HTTP client connection."""
        if self._client is not None:
            await self._client.aclose()
            self._client = None
    
    async def __aenter__(self):
        """Context manager entry."""
        return self
    
    async def __aexit__(self, exc_type, exc_val, exc_tb):
        """Context manager exit - close connections."""
        await self.close()
