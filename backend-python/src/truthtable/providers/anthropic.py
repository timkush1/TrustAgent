"""
Anthropic Provider Implementation

Implements the LLMProvider interface for the Anthropic Messages API.
The API key is read from the ANTHROPIC_API_KEY environment variable unless
passed explicitly; it is never logged.

API shape notes (vs. OpenAI):
- The system prompt is a top-level "system" parameter, not a message role.
- Responses carry content as a list of blocks; the text blocks are joined.
- `stop_reason: "max_tokens"` means the answer was cut off (response.truncated).
- Structured outputs use `output_config.format` (structured.py documents the
  verified parameter and the TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS modes).
- 429 and 529 ("overloaded_error") and other 5xx are retried (retry.py),
  honouring `retry-after`.
"""

import logging
import os
from typing import Any, Dict, Optional, Union

from .base import CompletionRequest, CompletionResponse, MessageRole, normalized_usage
from .errors import ProviderError
from .http_provider import HTTPProvider
from .structured import (
    ANTHROPIC_OUTPUT_CONFIG,
    StructuredOutputsMode,
    anthropic_output_config,
    rejects_parameter,
)
from .transport import JsonObject, bad_response, int_field, object_field

logger = logging.getLogger(__name__)

ANTHROPIC_VERSION = "2023-06-01"
ANTHROPIC_BASE_URL = "https://api.anthropic.com"
DEFAULT_ANTHROPIC_MODEL = "claude-haiku-4-5"


class AnthropicProvider(HTTPProvider):
    """
    Provider for the Anthropic Messages API.

    Args:
        model: Model name (e.g., "claude-haiku-4-5")
        api_key: API key; defaults to the ANTHROPIC_API_KEY environment variable
        base_url: API root
        timeout: Request timeout in seconds (per attempt)
        structured_outputs: auto | on | off (TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS)
        **kwargs: HTTPProvider options (retry_policy, transport, price_table, ...)
    """

    provider_name = "anthropic"

    def __init__(
        self,
        model: str = DEFAULT_ANTHROPIC_MODEL,
        api_key: Optional[str] = None,
        base_url: str = ANTHROPIC_BASE_URL,
        timeout: float = 60.0,
        structured_outputs: Union[str, StructuredOutputsMode] = StructuredOutputsMode.AUTO,
        **kwargs: Any,
    ):
        super().__init__(model=model, base_url=base_url, timeout=timeout, **kwargs)
        self.api_key = api_key or os.environ.get("ANTHROPIC_API_KEY", "")
        if not self.api_key:
            raise ValueError("Anthropic API key missing: set ANTHROPIC_API_KEY or pass api_key")
        self.structured_outputs_mode = StructuredOutputsMode.parse(structured_outputs)
        # Set once in `auto` mode when the API rejects output_config.
        self._structured_outputs_rejected = False

    def _headers(self) -> Dict[str, str]:
        return {
            "Content-Type": "application/json",
            "x-api-key": self.api_key,
            "anthropic-version": ANTHROPIC_VERSION,
        }

    @property
    def supports_structured_outputs(self) -> bool:
        return (
            self.structured_outputs_mode is not StructuredOutputsMode.OFF
            and not self._structured_outputs_rejected
        )

    def _payload(self, request: CompletionRequest, structured: bool) -> JsonObject:
        # Anthropic takes the system prompt as a top-level parameter.
        system_parts = [m.content for m in request.messages if m.role == MessageRole.SYSTEM]
        chat_messages = [m.to_dict() for m in request.messages if m.role != MessageRole.SYSTEM]

        payload: Dict[str, Any] = {
            "model": request.model or self.model,
            "messages": chat_messages,
            "max_tokens": request.max_tokens,
            "temperature": request.temperature,
        }
        if system_parts:
            payload["system"] = "\n\n".join(system_parts)
        if structured and request.response_schema is not None:
            payload[ANTHROPIC_OUTPUT_CONFIG] = anthropic_output_config(request.response_schema)
        return payload

    async def _complete(self, request: CompletionRequest) -> CompletionResponse:
        structured = request.response_schema is not None and self.supports_structured_outputs
        try:
            data = await self._request(
                "POST",
                "/v1/messages",
                self._payload(request, structured),
                timeout_s=request.timeout_s,
            )
        except ProviderError as exc:
            if not (
                structured
                and self.structured_outputs_mode is StructuredOutputsMode.AUTO
                and rejects_parameter(exc, ANTHROPIC_OUTPUT_CONFIG)
            ):
                raise
            self._structured_outputs_rejected = True
            logger.warning(
                f"Anthropic rejected structured outputs for model {request.model or self.model} "
                f"({exc}); continuing without them (TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS=auto)"
            )
            structured = False
            data = await self._request(
                "POST", "/v1/messages", self._payload(request, False), timeout_s=request.timeout_s
            )
        return self._parse(data, request, structured)

    def _parse(
        self, data: JsonObject, request: CompletionRequest, structured: bool
    ) -> CompletionResponse:
        blocks = data.get("content")
        if not isinstance(blocks, list):
            raise bad_response(self.provider_name, "content blocks")
        text_blocks = [
            str(b.get("text", ""))
            for b in blocks
            if isinstance(b, dict) and b.get("type") == "text"
        ]
        usage = object_field(data, "usage")
        response = CompletionResponse(
            content="".join(text_blocks),
            model=str(data.get("model") or request.model or self.model),
            finish_reason=str(data.get("stop_reason") or "stop"),
            usage=normalized_usage(
                int_field(usage, "input_tokens"), int_field(usage, "output_tokens")
            ),
            provider=self.provider_name,
            structured=structured,
        )
        return self.priced(response)

    async def health_check(self) -> bool:
        healthy = await self._probe("/v1/models")
        if not healthy:
            logger.error("Anthropic health check failed")
        return healthy
