"""
Ollama Provider Implementation

Implements the LLMProvider interface for Ollama - a local LLM runtime.
Ollama allows running models like Llama, Mistral, etc. on your own hardware.

v2 notes: structured outputs use `format: <schema>` (Ollama >= 0.5); an answer
cut off by `num_predict` reports `done_reason: "length"` (response.truncated);
only connection errors and 5xx are retried (retry.OLLAMA_RETRY_POLICY); local
calls are priced `free_local`.
"""

import logging
from typing import Any, Dict, List, Optional

from .base import CompletionRequest, CompletionResponse, normalized_usage
from .errors import ProviderConnectionError, ProviderError
from .http_provider import HTTPProvider
from .retry import NO_RETRY, OLLAMA_RETRY_POLICY, RetryPolicy
from .structured import ollama_format
from .transport import JsonObject, first_text, int_field, object_field

logger = logging.getLogger(__name__)

OLLAMA_BASE_URL = "http://localhost:11434"


class OllamaProvider(HTTPProvider):
    """
    Provider for Ollama local LLM service.

    Ollama runs models locally and exposes an OpenAI-compatible API.
    This is perfect for development and cost-free experimentation.

    Args:
        model: Model name (e.g., "llama3.2", "mistral")
        base_url: Ollama server URL (default: http://localhost:11434)
        timeout: Request timeout in seconds
        **kwargs: HTTPProvider options (retry_policy, transport, price_table, ...)

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

    provider_name = "ollama"

    def __init__(
        # Generous default: a cold model must be loaded into memory before the
        # first token, and CPU inference on a 3B judge can exceed 60s.
        self,
        model: str,
        base_url: str = OLLAMA_BASE_URL,
        timeout: float = 180.0,
        retry_policy: RetryPolicy = OLLAMA_RETRY_POLICY,
        **kwargs: Any,
    ):
        """Initialize Ollama provider."""
        super().__init__(
            model=model, base_url=base_url, timeout=timeout, retry_policy=retry_policy, **kwargs
        )

    @property
    def supports_structured_outputs(self) -> bool:
        return True

    def _payload(self, request: CompletionRequest) -> JsonObject:
        payload: Dict[str, Any] = {
            "model": request.model or self.model,
            "messages": [msg.to_dict() for msg in request.messages],
            "stream": False,  # We want the full response, not streaming
            "options": {
                "temperature": request.temperature,
                "num_predict": request.max_tokens,
            },
        }
        if request.response_schema is not None:
            payload["format"] = ollama_format(request.response_schema)
        return payload

    async def _complete(self, request: CompletionRequest) -> CompletionResponse:
        """
        Generate text completion using Ollama.

        Raises:
            ProviderConnectionError: If the Ollama server is unreachable (also a
                ConnectionError).
            ProviderError: If generation fails.
        """
        payload = self._payload(request)
        logger.debug(f"Sending request to Ollama: model={payload['model']}")
        try:
            data = await self._request("POST", "/api/chat", payload, timeout_s=request.timeout_s)
        except ProviderConnectionError as exc:
            logger.error(f"Failed to connect to Ollama at {self.base_url}: {exc}")
            raise ProviderConnectionError(
                f"Cannot connect to Ollama at {self.base_url}. "
                f"Is the server running? Try: docker compose --profile local-llm up ollama",
                kind=exc.kind,
                provider=self.provider_name,
                retryable=exc.retryable,
            ) from exc
        except ProviderError as exc:
            logger.error(f"Ollama request failed: {exc}")
            raise

        message = object_field(data, "message")
        prompt_tokens = int_field(data, "prompt_eval_count")
        eval_tokens = int_field(data, "eval_count")
        response = CompletionResponse(
            content=first_text(message.get("content")),
            model=str(data.get("model") or request.model or self.model),
            finish_reason=str(data.get("done_reason") or "stop"),
            usage=normalized_usage(prompt_tokens, eval_tokens),
            provider=self.provider_name,
            structured=request.response_schema is not None,
        )
        return self.priced(response)

    async def _models(self) -> Optional[List[str]]:
        """Model names Ollama has pulled, or None when it is unreachable."""
        try:
            data = await self._request("GET", "/api/tags", timeout_s=5.0, retry_policy=NO_RETRY)
        except ProviderError as exc:
            logger.error(f"Ollama health check failed: {exc}")
            return None
        models = data.get("models")
        if not isinstance(models, list):
            return []
        return [
            str(m["name"]) for m in models if isinstance(m, dict) and isinstance(m.get("name"), str)
        ]

    async def reachable(self) -> bool:
        """Whether the Ollama server answers (whatever models it has)."""
        return await self._models() is not None

    async def health_check(self) -> bool:
        """
        Check if Ollama server is reachable and has the configured model.

        Returns:
            True if server is responding, False otherwise
        """
        models = await self._models()
        if models is None:
            return False

        # Match exact name or with a tag suffix (e.g. :latest)
        model_found = any(
            m == self.model or m == f"{self.model}:latest" or m.startswith(f"{self.model}:")
            for m in models
        )
        if not model_found:
            logger.warning(
                f"Model '{self.model}' not found in Ollama. "
                f"Available models: {', '.join(models)}"
            )
            return False

        logger.info(f"Ollama health check passed. Model '{self.model}' is available.")
        return True
