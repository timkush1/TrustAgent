"""
Anthropic Provider Implementation

Implements the LLMProvider interface for the Anthropic Messages API.
The API key is read from the ANTHROPIC_API_KEY environment variable unless
passed explicitly; it is never logged.

API shape notes (vs. OpenAI):
- The system prompt is a top-level "system" parameter, not a message role.
- Responses carry content as a list of blocks; we use the first text block.
"""

import logging
import os
from typing import Optional

import httpx

from .base import CompletionRequest, CompletionResponse, LLMProvider, MessageRole

logger = logging.getLogger(__name__)

ANTHROPIC_VERSION = "2023-06-01"


class AnthropicProvider(LLMProvider):
    """
    Provider for the Anthropic Messages API.

    Args:
        model: Model name (e.g., "claude-haiku-4-5-20251001")
        api_key: API key; defaults to the ANTHROPIC_API_KEY environment variable
        base_url: API root
        timeout: Request timeout in seconds
    """

    def __init__(
        self,
        model: str,
        api_key: Optional[str] = None,
        base_url: str = "https://api.anthropic.com",
        timeout: float = 60.0,
        **kwargs,
    ):
        super().__init__(model=model, **kwargs)
        self.api_key = api_key or os.environ.get("ANTHROPIC_API_KEY", "")
        if not self.api_key:
            raise ValueError("Anthropic API key missing: set ANTHROPIC_API_KEY or pass api_key")
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._client: Optional[httpx.AsyncClient] = None

    async def _get_client(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(
                base_url=self.base_url,
                timeout=self.timeout,
                headers={
                    "Content-Type": "application/json",
                    "x-api-key": self.api_key,
                    "anthropic-version": ANTHROPIC_VERSION,
                },
            )
        return self._client

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        client = await self._get_client()

        # Anthropic takes the system prompt as a top-level parameter.
        system_parts = [m.content for m in request.messages if m.role == MessageRole.SYSTEM]
        chat_messages = [m.to_dict() for m in request.messages if m.role != MessageRole.SYSTEM]

        payload = {
            "model": request.model or self.model,
            "messages": chat_messages,
            "max_tokens": request.max_tokens,
            "temperature": request.temperature,
        }
        if system_parts:
            payload["system"] = "\n\n".join(system_parts)

        try:
            response = await client.post("/v1/messages", json=payload)
            response.raise_for_status()
            data = response.json()

            text_blocks = [
                b.get("text", "") for b in data.get("content", []) if b.get("type") == "text"
            ]
            usage = data.get("usage", {})
            prompt_tokens = usage.get("input_tokens", 0)
            completion_tokens = usage.get("output_tokens", 0)

            return CompletionResponse(
                content="".join(text_blocks),
                model=data.get("model", request.model),
                finish_reason=data.get("stop_reason", "stop"),
                usage={
                    "prompt_tokens": prompt_tokens,
                    "completion_tokens": completion_tokens,
                    "total_tokens": prompt_tokens + completion_tokens,
                },
            )

        except httpx.ConnectError as e:
            logger.error(f"Failed to connect to Anthropic at {self.base_url}: {e}")
            raise ConnectionError(f"Cannot connect to Anthropic at {self.base_url}") from e

        except httpx.HTTPStatusError as e:
            logger.error(f"Anthropic returned error {e.response.status_code}")
            raise RuntimeError(
                f"Anthropic request failed: {e.response.status_code} - {e.response.text}"
            ) from e

    async def health_check(self) -> bool:
        client = await self._get_client()
        try:
            response = await client.get("/v1/models", timeout=10.0)
            response.raise_for_status()
            return True
        except Exception as e:
            logger.error(f"Anthropic health check failed: {e}")
            return False

    async def close(self):
        if self._client is not None:
            await self._client.aclose()
            self._client = None
