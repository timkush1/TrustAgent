"""
A judge that can be replaced while the engine runs (SPEC section 3.2 `runtime/`, D34).

The engine wraps its judge as `LimitedProvider(SwappableProvider(judge))`, and
the audit graph, the knowledge-base ingestor and the Studio drafter all hold
that one object. When a runtime-config push changes the judge, `swap(new)`
sends every later call to `new` at once, while calls already running finish on
the old judge; the old judge is closed once the last of them returns (drain,
then close). A judge that is swapped back in before that is kept open.

Without a judge (`None`) a call raises ProviderError; the servicer checks for
a judge before it starts work, so that is a race-only path.
"""

import asyncio
import dataclasses
import logging
from typing import Any, Dict, Optional, Set

from .base import CompletionRequest, CompletionResponse, LLMProvider
from .errors import UNAVAILABLE, ProviderError

logger = logging.getLogger(__name__)


class _Usage:
    """The calls running on one judge."""

    def __init__(self) -> None:
        self.inflight = 0
        self.idle = asyncio.Event()


class SwappableProvider(LLMProvider):
    """
    Delegates to the judge currently installed (see the module docstring).

    `provider_name` stays the class default: report the installed judge (see
    `limiter.unwrap`).
    """

    def __init__(self, initial: Optional[LLMProvider] = None) -> None:
        self._current = initial
        self._usage: Dict[LLMProvider, _Usage] = {}
        # Models of judges swapped out: a request built from the old judge's
        # model just before a swap is sent with the new judge's model instead.
        self._retired_models: Set[str] = set()
        self._closing: Set["asyncio.Task[None]"] = set()
        super().__init__(model=initial.model if initial is not None else "", price_table=None)

    @property
    def current(self) -> Optional[LLMProvider]:
        """The judge new calls go to (None: no judge)."""
        return self._current

    @property
    def model(self) -> str:
        return self._current.model if self._current is not None else ""

    @model.setter
    def model(self, value: str) -> None:
        if self._current is not None:
            self._current.model = value

    @property
    def supports_structured_outputs(self) -> bool:
        return self._current is not None and self._current.supports_structured_outputs

    def inflight(self, provider: Optional[LLMProvider] = None) -> int:
        """Calls running on `provider` (default: the current judge)."""
        target = provider if provider is not None else self._current
        usage = self._usage.get(target) if target is not None else None
        return usage.inflight if usage is not None else 0

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        provider = self._current
        if provider is None:
            raise ProviderError(
                "no judge is configured", kind=UNAVAILABLE, provider="none", retryable=False
            )
        if request.model != provider.model and request.model in self._retired_models:
            request = dataclasses.replace(request, model=provider.model)
        usage = self._usage.get(provider)
        if usage is None:
            usage = self._usage[provider] = _Usage()
        usage.inflight += 1
        try:
            return await provider.complete(request)
        finally:
            usage.inflight -= 1
            if usage.inflight == 0:
                usage.idle.set()
                if self._usage.get(provider) is usage:
                    del self._usage[provider]

    async def health_check(self) -> bool:
        provider = self._current
        return provider is not None and await provider.health_check()

    def swap(self, new: Optional[LLMProvider]) -> Optional["asyncio.Task[None]"]:
        """
        Install `new` (None: no judge) for every later call. The old judge is
        closed in the background once its running calls finish; the returned
        task completes then (None when nothing is to be closed).
        """
        old = self._current
        if old is new:
            return None
        self._current = new
        if new is not None:
            self._retired_models.discard(new.model)
        if old is None:
            return None
        if old.model and (new is None or old.model != new.model):
            self._retired_models.add(old.model)
        task = asyncio.get_running_loop().create_task(self._drain_and_close(old))
        self._closing.add(task)
        task.add_done_callback(self._closing.discard)
        return task

    async def _drain_and_close(self, provider: LLMProvider) -> None:
        while True:
            usage = self._usage.get(provider)
            if usage is None:
                break
            await usage.idle.wait()
        if self._current is provider:
            return  # swapped back in meanwhile: it stays open
        try:
            await provider.close()
        except Exception as exc:  # nothing depends on it; a later client is a new one
            logger.warning(f"Closing the replaced {provider.provider_name} judge failed: {exc}")

    async def drained(self) -> None:
        """Wait until every replaced judge has been closed."""
        while self._closing:
            tasks = list(self._closing)
            # gather completes without yielding when every task is done, so the
            # done callbacks may not have removed them yet: remove them here.
            await asyncio.gather(*tasks, return_exceptions=True)
            self._closing.difference_update(tasks)

    async def close(self) -> None:
        if self._current is not None:
            await self._current.close()
        await self.drained()

    def __getattr__(self, name: str) -> Any:
        # Only called for attributes not found on the wrapper itself.
        if name.startswith("_"):
            raise AttributeError(name)
        current = self.__dict__.get("_current")
        if current is None:
            raise AttributeError(name)
        return getattr(current, name)
