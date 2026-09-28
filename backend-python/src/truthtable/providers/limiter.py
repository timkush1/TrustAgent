"""
Process-wide cap on in-flight judge calls (SPEC section 3.2, TRUTHTABLE_JUDGE_MAX_INFLIGHT).

Each audit already verifies at most VERIFIER_CONCURRENCY claims at once, but N
concurrent audits would still send N times that many requests to the judge.
`LimitedProvider` wraps the judge so every `complete()` call in the process
takes a slot of one shared `JudgeLimiter` (default 16 slots); health checks
are not limited.
"""

import asyncio
import threading
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Optional

from .base import CompletionRequest, CompletionResponse, LLMProvider

DEFAULT_JUDGE_MAX_INFLIGHT = 16


class JudgeLimiter:
    """An asyncio semaphore with observable occupancy."""

    def __init__(self, max_inflight: int = DEFAULT_JUDGE_MAX_INFLIGHT):
        if max_inflight < 1:
            raise ValueError(f"max_inflight must be at least 1, not {max_inflight}")
        self.max_inflight = max_inflight
        self._semaphore = asyncio.Semaphore(max_inflight)
        self._inflight = 0
        self._waiting = 0

    @property
    def inflight(self) -> int:
        """Calls currently holding a slot."""
        return self._inflight

    @property
    def waiting(self) -> int:
        """Calls queued for a slot."""
        return self._waiting

    @asynccontextmanager
    async def slot(self) -> AsyncIterator[None]:
        """Hold one slot for the duration of the block."""
        self._waiting += 1
        try:
            await self._semaphore.acquire()
        finally:
            self._waiting -= 1
        self._inflight += 1
        try:
            yield
        finally:
            self._inflight -= 1
            self._semaphore.release()


_process_lock = threading.Lock()
_process_limiter: Optional[JudgeLimiter] = None


def configure_process_limiter(max_inflight: int) -> JudgeLimiter:
    """(Re)create the process-wide limiter with `max_inflight` slots (engine startup)."""
    global _process_limiter
    with _process_lock:
        _process_limiter = JudgeLimiter(max_inflight)
        return _process_limiter


def process_limiter() -> JudgeLimiter:
    """The process-wide limiter (created with the default size on first use)."""
    global _process_limiter
    with _process_lock:
        if _process_limiter is None:
            _process_limiter = JudgeLimiter(DEFAULT_JUDGE_MAX_INFLIGHT)
        return _process_limiter


class LimitedProvider(LLMProvider):
    """
    A judge whose `complete()` calls share the process-wide in-flight cap.

    The model, health_check, close and provider-specific attributes are those of
    the wrapped provider (`provider_name` stays the class default: report the
    wrapped provider, see `unwrap`).
    """

    def __init__(self, inner: LLMProvider, limiter: Optional[JudgeLimiter] = None):
        self.inner = inner
        self.limiter = limiter if limiter is not None else process_limiter()
        super().__init__(model=inner.model, price_table=inner.price_table)

    @property
    def model(self) -> str:
        return self.inner.model

    @model.setter
    def model(self, value: str) -> None:
        self.inner.model = value

    @property
    def supports_structured_outputs(self) -> bool:
        return self.inner.supports_structured_outputs

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        async with self.limiter.slot():
            return await self.inner.complete(request)

    async def health_check(self) -> bool:
        return await self.inner.health_check()

    async def close(self) -> None:
        await self.inner.close()

    def __getattr__(self, name: str) -> Any:
        # Only called for attributes not found on the wrapper itself.
        if name == "inner":
            raise AttributeError(name)
        return getattr(self.inner, name)


def unwrap(provider: LLMProvider) -> LLMProvider:
    """The provider inside any LimitedProvider wrappers."""
    while isinstance(provider, LimitedProvider):
        provider = provider.inner
    return provider
