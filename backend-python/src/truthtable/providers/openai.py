"""
OpenAI Provider Implementation

Implements the LLMProvider interface for the OpenAI Chat Completions API.
The API key is read from the OPENAI_API_KEY environment variable unless
passed explicitly; it is never logged.
"""

import logging
import os
from typing import Optional

import httpx

from .base import CompletionRequest, CompletionResponse, LLMProvider

logger = logging.getLogger(__name__)


class OpenAIProvider(LLMProvider):
    """
    Provider for the OpenAI Chat Completions API.

    Args:
        model: Model name (e.g., "gpt-4o-mini")
        api_key: API key; defaults to the OPENAI_API_KEY environment variable
        base_url: API root (override for Azure/compatible gateways)
        timeout: Request timeout in seconds
    """

    def __init__(
        self,
        model: str,
        api_key: Optional[str] = None,
        base_url: str = "https://api.openai.com",
        timeout: float = 60.0,
        **kwargs,
    ):
        super().__init__(model=model, **kwargs)
        self.api_key = api_key or os.environ.get("OPENAI_API_KEY", "")
        if not self.api_key:
            raise ValueError("OpenAI API key missing: set OPENAI_API_KEY or pass api_key")
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
                    "Authorization": f"Bearer {self.api_key}",
                },
            )
        return self._client

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        client = await self._get_client()

        payload = {
            "model": request.model or self.model,
            "messages": [msg.to_dict() for msg in request.messages],
            "temperature": request.temperature,
            "max_tokens": request.max_tokens,
        }

        try:
            response = await client.post("/v1/chat/completions", json=payload)
            response.raise_for_status()
            data = response.json()

            choice = data["choices"][0]
            usage = data.get("usage", {})

            return CompletionResponse(
                content=choice["message"]["content"],
                model=data.get("model", request.model),
                finish_reason=choice.get("finish_reason", "stop"),
                usage={
                    "prompt_tokens": usage.get("prompt_tokens", 0),
                    "completion_tokens": usage.get("completion_tokens", 0),
                    "total_tokens": usage.get("total_tokens", 0),
                },
            )

        except httpx.ConnectError as e:
            logger.error(f"Failed to connect to OpenAI at {self.base_url}: {e}")
            raise ConnectionError(f"Cannot connect to OpenAI at {self.base_url}") from e

        except httpx.HTTPStatusError as e:
            logger.error(f"OpenAI returned error {e.response.status_code}")
            raise RuntimeError(
                f"OpenAI request failed: {e.response.status_code} - {e.response.text}"
            ) from e

        except (KeyError, IndexError) as e:
            logger.error(f"Unexpected OpenAI response shape: {e}")
            raise RuntimeError(f"Unexpected OpenAI response shape: {e}") from e

    async def health_check(self) -> bool:
        client = await self._get_client()
        try:
            response = await client.get("/v1/models", timeout=10.0)
            response.raise_for_status()
            return True
        except Exception as e:
            logger.error(f"OpenAI health check failed: {e}")
            return False

    async def close(self):
        if self._client is not None:
            await self._client.aclose()
            self._client = None
