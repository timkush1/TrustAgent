"""
Per-call cost records of an audit (SPEC sections 2.2 CostRecord and 7.4):
one CallCost per judge call inside a cost ledger, a failed call included, and
nothing recorded outside one.
"""

import asyncio
from typing import List, Optional

import pytest

from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider, Message
from truthtable.providers.base import MessageRole, normalized_usage
from truthtable.providers.costs import (
    DEEP_AUDIT_DECOMPOSE,
    DEEP_AUDIT_VERIFY,
    STATUS_ERROR,
    STATUS_OK,
    complete_recorded,
    cost_ledger,
    current_ledger,
)
from truthtable.providers.errors import ProviderError
from truthtable.providers.limiter import JudgeLimiter, LimitedProvider


class PricedJudge(LLMProvider):
    """Answers with a priced response, or raises `error`."""

    provider_name = "anthropic"

    def __init__(self, error: Optional[Exception] = None) -> None:
        super().__init__(model="claude-haiku-4-5")
        self.error = error

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        if self.error is not None:
            raise self.error
        return CompletionResponse(
            content="{}",
            model="claude-haiku-4-5-20251001",
            finish_reason="stop",
            usage=normalized_usage(1200, 80),
            provider=self.provider_name,
            cost_usd=0.0016,
            cost_source="computed",
            latency_ms=420,
        )

    async def health_check(self) -> bool:
        return True


def _request() -> CompletionRequest:
    return CompletionRequest(
        messages=[Message(role=MessageRole.USER, content="hi")], model="claude-haiku-4-5"
    )


async def test_no_ledger_records_nothing() -> None:
    assert current_ledger() is None
    response = await complete_recorded(PricedJudge(), _request(), DEEP_AUDIT_VERIFY)
    assert response.cost_usd == pytest.approx(0.0016)


async def test_a_call_is_recorded_with_its_price_and_label() -> None:
    with cost_ledger() as ledger:
        await complete_recorded(PricedJudge(), _request(), DEEP_AUDIT_DECOMPOSE)

    (call,) = ledger.calls
    assert call.component == DEEP_AUDIT_DECOMPOSE and call.kind == "llm"
    assert (call.provider, call.model) == ("anthropic", "claude-haiku-4-5-20251001")
    assert (call.input_tokens, call.output_tokens) == (1200, 80)
    assert call.cost_usd == pytest.approx(0.0016) and call.cost_source == "computed"
    assert (call.latency_ms, call.status) == (420, STATUS_OK)
    assert ledger.total_usd == pytest.approx(0.0016)


async def test_a_failed_call_is_recorded_as_an_error_with_unknown_cost() -> None:
    judge = LimitedProvider(
        PricedJudge(error=ProviderError("down", kind="unavailable", provider="anthropic")),
        JudgeLimiter(2),
    )

    with cost_ledger() as ledger:
        with pytest.raises(ProviderError):
            await complete_recorded(judge, _request(), DEEP_AUDIT_VERIFY)

    (call,) = ledger.calls
    # The wrapped provider's name, not the limiter's.
    assert (call.provider, call.model, call.status) == (
        "anthropic",
        "claude-haiku-4-5",
        STATUS_ERROR,
    )
    assert call.cost_usd is None and call.cost_source == "unknown"
    assert (call.input_tokens, call.output_tokens) == (0, 0)


async def test_concurrent_tasks_record_into_the_audit_ledger() -> None:
    judge = PricedJudge()

    with cost_ledger() as ledger:
        await asyncio.gather(
            *(complete_recorded(judge, _request(), DEEP_AUDIT_VERIFY) for _ in range(5))
        )

    components: List[str] = [call.component for call in ledger.calls]
    assert components == [DEEP_AUDIT_VERIFY] * 5
    assert current_ledger() is None
