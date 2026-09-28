"""
OpenAI Provider Implementation

Implements the LLMProvider interface for the OpenAI Chat Completions API.
The API key is read from the OPENAI_API_KEY environment variable unless
passed explicitly; it is never logged.

`OpenAICompatibleProvider` holds the Chat Completions request and answer
handling shared with OpenRouter (openrouter.py): `response_format` for
structured outputs, `finish_reason: "length"` as truncation, and an error
envelope in a 200 answer treated as a failure.
"""

import logging
import os
from typing import Any, Dict, Optional

from .base import CompletionRequest, CompletionResponse, normalized_usage
from .errors import UNAVAILABLE, ProviderError
from .http_provider import HTTPProvider
from .structured import openai_response_format
from .transport import (
    JsonObject,
    bad_response,
    error_from_status,
    first_text,
    int_field,
    object_field,
)

logger = logging.getLogger(__name__)

OPENAI_BASE_URL = "https://api.openai.com"
DEFAULT_OPENAI_MODEL = "gpt-4o-mini"


class OpenAICompatibleProvider(HTTPProvider):
    """Chat Completions request building and answer parsing (OpenAI dialect)."""

    #: Path of the Chat Completions endpoint under base_url.
    chat_path = "/v1/chat/completions"

    @property
    def supports_structured_outputs(self) -> bool:
        return True

    def _payload(self, request: CompletionRequest) -> JsonObject:
        payload: Dict[str, Any] = {
            "model": request.model or self.model,
            "messages": [msg.to_dict() for msg in request.messages],
            "temperature": request.temperature,
            "max_tokens": request.max_tokens,
        }
        if request.response_schema is not None:
            payload["response_format"] = openai_response_format(
                request.response_schema, request.schema_name
            )
        return payload

    def _reported_cost_usd(self, usage: JsonObject) -> Optional[float]:
        """The call cost the provider reports in `usage`, if any."""
        return None

    async def _complete(self, request: CompletionRequest) -> CompletionResponse:
        data = await self._request(
            "POST",
            self.chat_path,
            self._payload(request),
            timeout_s=request.timeout_s,
            check=self._check_answer,
        )
        return self._parse(data, request)

    def _check_answer(self, data: JsonObject) -> None:
        """Raise for an error reported inside a 200 answer (retried like a 5xx)."""
        if "error" in data and not data.get("choices"):
            # OpenAI-compatible gateways may put an error envelope in a 200 answer.
            code = object_field(data, "error").get("code")
            status = code if isinstance(code, int) and 400 <= code <= 599 else 502
            raise error_from_status(self.provider_name, status, data)
        choices = data.get("choices")
        if not isinstance(choices, list) or not choices or not isinstance(choices[0], dict):
            raise bad_response(self.provider_name, "choices[0]")
        if choices[0].get("finish_reason") == "error":
            raise ProviderError(
                f"{self.provider_name} generation failed mid-answer",
                kind=UNAVAILABLE,
                provider=self.provider_name,
                status=502,
                retryable=True,
            )

    def _parse(self, data: JsonObject, request: CompletionRequest) -> CompletionResponse:
        choice: JsonObject = data["choices"][0]
        finish_reason = str(choice.get("finish_reason") or "stop")
        message = object_field(choice, "message")
        usage = object_field(data, "usage")
        response = CompletionResponse(
            content=first_text(message.get("content")),
            model=str(data.get("model") or request.model or self.model),
            finish_reason=finish_reason,
            usage=normalized_usage(
                int_field(usage, "prompt_tokens"), int_field(usage, "completion_tokens")
            ),
            provider=self.provider_name,
            structured=request.response_schema is not None,
        )
        return self.priced(response, reported_cost_usd=self._reported_cost_usd(usage))


class OpenAIProvider(OpenAICompatibleProvider):
    """
    Provider for the OpenAI Chat Completions API.

    Args:
        model: Model name (e.g., "gpt-4o-mini")
        api_key: API key; defaults to the OPENAI_API_KEY environment variable
        base_url: API root (override for Azure/compatible gateways)
        timeout: Request timeout in seconds (per attempt)
        **kwargs: HTTPProvider options (retry_policy, transport, price_table, ...)
    """

    provider_name = "openai"

    def __init__(
        self,
        model: str = DEFAULT_OPENAI_MODEL,
        api_key: Optional[str] = None,
        base_url: str = OPENAI_BASE_URL,
        timeout: float = 60.0,
        **kwargs: Any,
    ):
        super().__init__(model=model, base_url=base_url, timeout=timeout, **kwargs)
        self.api_key = api_key or os.environ.get("OPENAI_API_KEY", "")
        if not self.api_key:
            raise ValueError("OpenAI API key missing: set OPENAI_API_KEY or pass api_key")

    def _headers(self) -> Dict[str, str]:
        return {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
        }

    async def health_check(self) -> bool:
        healthy = await self._probe("/v1/models")
        if not healthy:
            logger.error("OpenAI health check failed")
        return healthy
