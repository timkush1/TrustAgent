"""
Per-call LLM cost records of an audit (SPEC sections 2.2 `CostRecord` and 7.4).

The gateway's cost ledger (`cost_events`) holds one row per LLM call, built
from the `CostRecord`s an AuditResult carries; Go reconciles the audit's LLM
cap reservation with them. The engine opens a `cost_ledger()` around each
audit, and the decomposer and verifier make every judge call through
`complete_recorded(provider, request, component)`, which appends one
`CallCost` to that ledger (a call that raised too, with status `error`).
Outside a ledger (the eval harness) recording is a no-op. A ledger opened with
a `component` books every call inside it under that component: knowledge-base
ingestion reuses the decomposer and verifier, and its calls are `kb_ingest`.

The cost of a call is what the provider priced it at (pricing.py), with its
label: `reported` (OpenRouter `usage.cost`), `computed` (tokens x the price
table), `free_local` (Ollama), `simulated` (the mock judge) or `unknown`. A
call that raised has no usage to price: cost 0 and `unknown`.

Tasks the audit spawns (concurrent windows and claims) copy the context, so
they all record into the audit's ledger.
"""

import time
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, replace
from typing import Iterable, Iterator, List, Optional, Tuple

from .base import CompletionRequest, CompletionResponse, LLMProvider
from .limiter import unwrap
from .pricing import COMPUTED, FREE_LOCAL, REPORTED, SIMULATED, UNKNOWN

# CostRecord.component values of a deep audit.
DEEP_AUDIT_DECOMPOSE = "deep_audit_decompose"
DEEP_AUDIT_VERIFY = "deep_audit_verify"
# CostRecord.component of a Studio "Draft with AI" call and of KB ingestion.
STUDIO_DRAFT = "studio_draft"
KB_INGEST = "kb_ingest"

# CostRecord.kind of a judge call.
KIND_LLM = "llm"

# CostRecord.status values.
STATUS_OK = "ok"
STATUS_ERROR = "error"


@dataclass(frozen=True)
class CallCost:
    """One judge call, as proto CostRecord reports it."""

    component: str
    provider: str
    model: str
    input_tokens: int
    output_tokens: int
    cost_usd: Optional[float]
    cost_source: str
    latency_ms: int
    status: str
    kind: str = KIND_LLM


class CostLedger:
    """
    The judge calls of one audit (or one other operation), in the order they
    finished. With `component`, every call is booked under it.
    """

    def __init__(self, component: Optional[str] = None) -> None:
        self.component = component
        self._calls: List[CallCost] = []

    def add(self, call: CallCost) -> None:
        if self.component and call.component != self.component:
            call = replace(call, component=self.component)
        self._calls.append(call)

    @property
    def calls(self) -> Tuple[CallCost, ...]:
        return tuple(self._calls)

    @property
    def total_usd(self) -> float:
        """The known costs summed (unknown ones count as 0)."""
        total = 0.0
        for call in self._calls:
            total += call.cost_usd or 0.0
        return total


_LEDGER: ContextVar[Optional[CostLedger]] = ContextVar("truthtable_cost_ledger", default=None)


@contextmanager
def cost_ledger(component: Optional[str] = None) -> Iterator[CostLedger]:
    """Record the judge calls made inside the block (one audit; see CostLedger)."""
    ledger = CostLedger(component)
    token = _LEDGER.set(ledger)
    try:
        yield ledger
    finally:
        _LEDGER.reset(token)


def current_ledger() -> Optional[CostLedger]:
    """The ledger of the audit in progress, if any."""
    return _LEDGER.get()


def _elapsed_ms(started: float) -> int:
    return int((time.monotonic() - started) * 1000)


async def complete_recorded(
    provider: LLMProvider, request: CompletionRequest, component: str
) -> CompletionResponse:
    """
    `provider.complete(request)`, recorded as one `component` call in the
    current audit's ledger (if any).

    Raises:
        Whatever the provider raised (after recording the failed call).
    """
    ledger = _LEDGER.get()
    if ledger is None:
        return await provider.complete(request)
    name = unwrap(provider).provider_name
    started = time.monotonic()
    try:
        response = await provider.complete(request)
    except Exception:
        ledger.add(
            CallCost(
                component=component,
                provider=name,
                model=request.model or provider.model,
                input_tokens=0,
                output_tokens=0,
                cost_usd=None,
                cost_source=UNKNOWN,
                latency_ms=_elapsed_ms(started),
                status=STATUS_ERROR,
            )
        )
        raise
    ledger.add(
        CallCost(
            component=component,
            provider=response.provider or name,
            model=response.model or request.model or provider.model,
            input_tokens=response.input_tokens,
            output_tokens=response.output_tokens,
            cost_usd=response.cost_usd,
            cost_source=response.cost_source,
            latency_ms=response.latency_ms or _elapsed_ms(started),
            status=STATUS_OK,
        )
    )
    return response


# How far a cost can be trusted, least first: a sum is labelled by its weakest part.
_SOURCE_RANK = {UNKNOWN: 0, COMPUTED: 1, REPORTED: 2, FREE_LOCAL: 3, SIMULATED: 4}


def combine_calls(calls: Iterable[CallCost], component: str) -> Optional[CallCost]:
    """
    Several calls of one operation as a single `component` record (None when
    there are none): tokens, latency and known costs summed; the cost is None
    and `unknown` when no call's cost is known, otherwise labelled by the least
    trustworthy source among the calls; the status is `error` when any call
    failed.
    """
    calls = list(calls)
    if not calls:
        return None
    known = [call.cost_usd for call in calls if call.cost_usd is not None]
    sources = {call.cost_source for call in calls}
    source = min(sources, key=lambda name: _SOURCE_RANK.get(name, 0))
    last = calls[-1]
    return CallCost(
        component=component,
        provider=last.provider,
        model=last.model,
        input_tokens=sum(call.input_tokens for call in calls),
        output_tokens=sum(call.output_tokens for call in calls),
        cost_usd=sum(known) if known else None,
        cost_source=source if known else UNKNOWN,
        latency_ms=sum(call.latency_ms for call in calls),
        status=STATUS_ERROR if any(call.status == STATUS_ERROR for call in calls) else STATUS_OK,
        kind=last.kind,
    )
