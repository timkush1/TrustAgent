"""
Retry policy for judge-provider calls (SPEC section 3.2, D7).

A failed attempt is retried when the policy says its error is transient:

- HTTP 408, 429 and every 5xx (including Anthropic's 529 "overloaded"),
  connection errors and client-side timeouts (the default policy);
- only connection errors and 5xx for Ollama (a local model that timed out
  would only time out again, and Ollama has no rate limits);
- a 402 only when the provider marked it transient (OpenRouter's in-flight
  budget, see BudgetError).

Backoff is exponential with equal jitter: attempt k (0-based) waits
d/2 + U(0, d/2) with d = min(max_delay, base_delay * 2**k), i.e. about 0.5 s,
1 s, 2 s ... capped at 8 s. A Retry-After the server sends is honoured when it
is at most `retry_after_cap_s` (20 s); a longer one ends the retries, so one
audit never waits minutes for a rate-limit window. The last error is raised
when the attempts run out.
"""

import asyncio
import logging
import random
from dataclasses import dataclass
from typing import AbstractSet, Awaitable, Callable, Optional, TypeVar

from .errors import CONNECTION, TIMEOUT, BudgetError, ProviderError

logger = logging.getLogger(__name__)

T = TypeVar("T")

# Statuses retried besides 5xx.
DEFAULT_RETRY_STATUSES: AbstractSet[int] = frozenset({408, 429})


@dataclass(frozen=True)
class RetryPolicy:
    """
    When and how often to retry a failed provider call.

    Attributes:
        max_retries: Retries after the first attempt (0 disables retrying).
        base_delay_s: Backoff of the first retry, before jitter.
        max_delay_s: Backoff cap, before jitter.
        retry_after_cap_s: Longest Retry-After that is honoured; a longer one
            stops retrying.
        retry_statuses: HTTP statuses retried in addition to 5xx.
        retry_server_errors: Whether HTTP 5xx is retried.
        retry_timeouts: Whether client-side timeouts are retried.
        retry_connection_errors: Whether connection failures are retried.
    """

    max_retries: int = 3
    base_delay_s: float = 0.5
    max_delay_s: float = 8.0
    retry_after_cap_s: float = 20.0
    retry_statuses: AbstractSet[int] = DEFAULT_RETRY_STATUSES
    retry_server_errors: bool = True
    retry_timeouts: bool = True
    retry_connection_errors: bool = True

    def is_retryable(self, error: ProviderError) -> bool:
        """Whether this policy retries `error` (attempts permitting)."""
        if not error.retryable:
            return False
        if error.status is None:
            if error.kind == CONNECTION:
                return self.retry_connection_errors
            if error.kind == TIMEOUT:
                return self.retry_timeouts
            return False
        if isinstance(error, BudgetError):
            return True  # a transient 402 (in-flight budget), marked by the provider
        if 500 <= error.status <= 599:
            return self.retry_server_errors
        return error.status in self.retry_statuses

    def backoff_s(self, attempt: int, rng: Callable[[], float] = random.random) -> float:
        """Equal-jitter exponential backoff before retry number `attempt` + 1."""
        ceiling = min(self.max_delay_s, self.base_delay_s * (2.0**attempt))
        return ceiling / 2 + rng() * ceiling / 2

    def delay_s(
        self, error: ProviderError, attempt: int, rng: Callable[[], float] = random.random
    ) -> Optional[float]:
        """
        Seconds to wait before retrying after `error` on 0-based `attempt`, or
        None when the call must not be retried.
        """
        if attempt >= self.max_retries or not self.is_retryable(error):
            return None
        if error.retry_after_s is not None:
            if error.retry_after_s > self.retry_after_cap_s:
                return None
            return max(0.0, error.retry_after_s)
        return self.backoff_s(attempt, rng)


DEFAULT_RETRY_POLICY = RetryPolicy()

# Ollama: connection errors and 5xx only (design-engine section 7).
OLLAMA_RETRY_POLICY = RetryPolicy(retry_statuses=frozenset(), retry_timeouts=False)

NO_RETRY = RetryPolicy(max_retries=0)

Sleep = Callable[[float], Awaitable[None]]


async def call_with_retry(
    call: Callable[[], Awaitable[T]],
    policy: RetryPolicy = DEFAULT_RETRY_POLICY,
    *,
    sleep: Sleep = asyncio.sleep,
    rng: Callable[[], float] = random.random,
) -> T:
    """
    Run `call`, retrying ProviderErrors the policy considers transient.

    Raises:
        ProviderError: The last error, when it is not retryable or the retries
            (or an over-long Retry-After) run out.
    """
    attempt = 0
    while True:
        try:
            return await call()
        except ProviderError as error:
            delay = policy.delay_s(error, attempt, rng)
            if delay is None:
                raise
            attempt += 1
            logger.warning(
                f"{error.provider} judge call failed ({error.kind}"
                + (f", HTTP {error.status}" if error.status is not None else "")
                + f"); retry {attempt}/{policy.max_retries} in {delay:.2f}s"
            )
            await sleep(delay)
