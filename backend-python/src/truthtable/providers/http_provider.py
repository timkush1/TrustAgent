"""
Common base of the HTTP judge providers (Anthropic, OpenAI, OpenRouter, Ollama).

It owns the lazily created httpx client (one per provider, closed by
`close()`), sends every call through the retry policy (retry.py) and the typed
error mapping (transport.py), and measures latency. Subclasses build the
request payload and parse the answer.
"""

import asyncio
import random
import time
from abc import abstractmethod
from typing import Any, Callable, Dict, Optional

import httpx

from .base import CompletionRequest, CompletionResponse, LLMProvider
from .errors import ProviderError
from .retry import DEFAULT_RETRY_POLICY, NO_RETRY, RetryPolicy, Sleep, call_with_retry
from .transport import JsonObject, request_json


class HTTPProvider(LLMProvider):
    """
    An LLM provider reached over HTTP with JSON bodies.

    Args:
        model: Default model name.
        base_url: API root (no trailing slash needed).
        timeout: Default per-attempt timeout in seconds.
        retry_policy: When to retry a failed attempt.
        transport: httpx transport override (tests use httpx.MockTransport).
        sleep: Awaitable sleep used between retries (tests pass a no-op).
        rng: Jitter source for the backoff.
    """

    def __init__(
        self,
        model: str,
        *,
        base_url: str,
        timeout: float,
        retry_policy: RetryPolicy = DEFAULT_RETRY_POLICY,
        transport: Optional[httpx.AsyncBaseTransport] = None,
        sleep: Sleep = asyncio.sleep,
        rng: Callable[[], float] = random.random,
        **kwargs: Any,
    ):
        super().__init__(model=model, **kwargs)
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.retry_policy = retry_policy
        self._transport = transport
        self._sleep = sleep
        self._rng = rng
        self._client: Optional[httpx.AsyncClient] = None

    def _headers(self) -> Dict[str, str]:
        """Headers sent with every request (credentials included)."""
        return {"Content-Type": "application/json"}

    async def _get_client(self) -> httpx.AsyncClient:
        """Get or create the HTTP client (lazy initialization)."""
        if self._client is None:
            self._client = httpx.AsyncClient(
                base_url=self.base_url,
                timeout=self.timeout,
                headers=self._headers(),
                transport=self._transport,
            )
        return self._client

    async def _request(
        self,
        method: str,
        path: str,
        payload: Optional[JsonObject] = None,
        *,
        timeout_s: Optional[float] = None,
        retry_policy: Optional[RetryPolicy] = None,
        check: Optional[Callable[[JsonObject], None]] = None,
    ) -> JsonObject:
        """
        One JSON call with retries. `check` inspects each 2xx answer inside the
        retried attempt and may raise a (retryable) ProviderError for an error
        the provider reports in a 200 body.

        Raises:
            ProviderError: When the call fails and is not (or no longer) retried.
        """
        client = await self._get_client()

        async def attempt() -> JsonObject:
            data = await request_json(
                client,
                method,
                path,
                provider=self.provider_name,
                json_body=payload,
                timeout_s=timeout_s,
            )
            if check is not None:
                check(data)
            return data

        return await call_with_retry(
            attempt, retry_policy or self.retry_policy, sleep=self._sleep, rng=self._rng
        )

    async def _probe(self, path: str, timeout_s: float = 10.0) -> bool:
        """Whether GET `path` answers 2xx JSON (a single attempt; never raises)."""
        try:
            await self._request("GET", path, timeout_s=timeout_s, retry_policy=NO_RETRY)
        except ProviderError:
            return False
        return True

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        started = time.monotonic()
        response = await self._complete(request)
        response.latency_ms = int((time.monotonic() - started) * 1000)
        return response

    @abstractmethod
    async def _complete(self, request: CompletionRequest) -> CompletionResponse:
        """Provider-specific completion (payload, call, parse, pricing)."""

    async def close(self) -> None:
        """Close the HTTP client connection."""
        if self._client is not None:
            await self._client.aclose()
            self._client = None

    async def __aenter__(self) -> "HTTPProvider":
        return self

    async def __aexit__(self, *exc_info: object) -> None:
        await self.close()
