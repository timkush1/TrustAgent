"""
The recorded ("mock") judge of the golden evals, with judge outages.

Golden fixture files are lists of match fixtures replayed by MockLLMProvider
(`{"match": [...], "response": "..."}`; the first fixture whose terms all
appear in the prompt answers it). golden_v2 adds fault fixtures, which make
the judge fail the way a real provider does after its retries:

    {"match": ["<claim>\\nRefunds take 5 days.\\n</claim>"],
     "error": {"kind": "unavailable", "status": 503}}

A fault fixture raises `ProviderError(kind, status, retryable)` for every
prompt it matches (retryable defaults to the kind being transient: connection,
timeout, rate_limited, overloaded or unavailable). Fault fixtures are checked
before the recorded answers.
"""

import json
from pathlib import Path
from typing import Any, Dict, List, Sequence, Tuple, Union

from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider
from truthtable.providers.errors import (
    CONNECTION,
    OVERLOADED,
    RATE_LIMITED,
    TIMEOUT,
    UNAVAILABLE,
    ProviderError,
)
from truthtable.providers.mock import MockLLMProvider

TRANSIENT_KINDS = frozenset({CONNECTION, TIMEOUT, RATE_LIMITED, OVERLOADED, UNAVAILABLE})


def _terms(fixture: Dict[str, Any]) -> List[str]:
    match = fixture["match"]
    return [match] if isinstance(match, str) else list(match)


def split_fixtures(
    fixtures: Sequence[Dict[str, Any]],
) -> Tuple[List[Dict[str, Any]], List[Dict[str, Any]]]:
    """(recorded answers, fault fixtures)."""
    answers = [fixture for fixture in fixtures if "error" not in fixture]
    faults = [fixture for fixture in fixtures if "error" in fixture]
    return answers, faults


class RecordedJudge(LLMProvider):
    """MockLLMProvider over the recorded answers, raising the fault fixtures' errors."""

    provider_name = "mock"

    def __init__(self, fixtures: Sequence[Dict[str, Any]], model: str = "mock"):
        super().__init__(model=model)
        answers, self.faults = split_fixtures(fixtures)
        self.inner = MockLLMProvider(model=model, fixtures=answers)
        self.calls = 0

    @classmethod
    def from_file(cls, path: Union[str, Path]) -> "RecordedJudge":
        with open(path, encoding="utf-8") as f:
            return cls(json.load(f))

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.calls += 1
        prompt = "\n".join(message.content for message in request.messages)
        for fault in self.faults:
            if all(term in prompt for term in _terms(fault)):
                error = fault["error"]
                kind = str(error.get("kind", UNAVAILABLE))
                raise ProviderError(
                    f"recorded judge outage: {kind}",
                    kind=kind,
                    provider=self.provider_name,
                    status=error.get("status"),
                    retryable=bool(error.get("retryable", kind in TRANSIENT_KINDS)),
                )
        return await self.inner.complete(request)

    async def health_check(self) -> bool:
        return True
