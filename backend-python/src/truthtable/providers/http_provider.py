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
from typing import Any, Callable, Dict, FrozenSet, Iterable, Optional

import httpx

from .base import CompletionRequest, CompletionResponse, LLMProvider
from .errors import INVALID, ProviderError
from .origins import canonical_origin
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
        allowed_origins: Canonical origins the provider's credential may be
            sent to (D78). When given, base_url must be one of them and
            every request to another origin is refused before it is sent.
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
        allowed_origins: Optional[Iterable[str]] = None,
        **kwargs: Any,
    ):
        super().__init__(model=model, **kwargs)
        self.base_url = base_url.rstrip("/")
        self.allowed_origins: Optional[FrozenSet[str]] = None
        if allowed_origins is not None:
            self.allowed_origins = frozenset(allowed_origins)
            if canonical_origin(self.base_url) not in self.allowed_origins:
                raise ValueError(
                    f"{self.provider_name or 'provider'} base URL origin "
                    f"{canonical_origin(self.base_url)} is not an allowed origin of its credential"
                )
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
            hooks: Dict[str, Any] = {}
            if self.allowed_origins is not None:
                hooks["request"] = [self._check_origin]
            self._client = httpx.AsyncClient(
                base_url=self.base_url,
                timeout=self.timeout,
                headers=self._headers(),
                transport=self._transport,
                event_hooks=hooks,
            )
        return self._client

    async def _check_origin(self, request: httpx.Request) -> None:
        """Refuse a request whose origin is not an allowed origin of the credential."""
        allowed = self.allowed_origins
        if allowed is None:
            return
        try:
            origin = canonical_origin(str(request.url))
        except ValueError:
            origin = ""
        if origin not in allowed:
            raise ProviderError(
                f"refusing to send the {self.provider_name} credential to {origin or 'that URL'}: "
                "not one of its allowed origins",
                kind=INVALID,
                provider=self.provider_name,
            )

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
        # Detach first: a call made while the old client closes gets a new one.
        client, self._client = self._client, None
        if client is not None:
            await client.aclose()

    async def __aenter__(self) -> "HTTPProvider":
        return self

    async def __aexit__(self, *exc_info: object) -> None:
        await self.close()
