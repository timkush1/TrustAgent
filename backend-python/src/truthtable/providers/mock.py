"""
Mock LLM Provider

A deterministic provider that replays recorded responses from a fixture file.
Used by the evaluation harness (Tier-1 regression gate) so the full audit
pipeline — graph wiring, JSON parsing, scoring math, thresholds — can run in
CI without a live model.

Fixtures match on prompt content rather than exact prompt hashes, so prompt
template changes don't invalidate recorded fixtures:

    [
      {"match": ["Extract all factual claims", "Paris is the capital"],
       "response": "[\"Paris is the capital of France\"]"},
      {"match": ["<claim>", "Paris is the capital of France"],
       "response": "{\"status\": \"SUPPORTED\", ...}"}
    ]

Every string in `match` must appear in the rendered prompt (system + user
messages joined). The first fixture whose substrings all match wins.
"""

import json
import logging
from pathlib import Path
from typing import Any, Dict, List, Optional, Union

from .base import CompletionRequest, CompletionResponse, LLMProvider

logger = logging.getLogger(__name__)


class FixtureNotFoundError(LookupError):
    """No fixture matched the prompt — fail loudly so evals never run on garbage."""


class MockLLMProvider(LLMProvider):
    """Deterministic fixture-replay provider for evaluation and testing."""

    def __init__(
        self,
        model: str = "mock",
        fixtures: Optional[List[Dict[str, Any]]] = None,
        fixtures_path: Optional[Union[str, Path]] = None,
        **kwargs,
    ):
        """
        Args:
            model: Reported model name (cosmetic).
            fixtures: List of {"match": str | [str], "response": str} entries.
            fixtures_path: JSON file containing such a list; merged after
                           `fixtures` if both are given.
        """
        super().__init__(model=model, **kwargs)
        self.fixtures: List[Dict[str, Any]] = list(fixtures or [])
        if fixtures_path:
            with open(fixtures_path, encoding="utf-8") as f:
                self.fixtures.extend(json.load(f))
        logger.info(f"MockLLMProvider initialized with {len(self.fixtures)} fixtures")

    @staticmethod
    def _match_terms(fixture: Dict[str, Any]) -> List[str]:
        match = fixture["match"]
        return [match] if isinstance(match, str) else list(match)

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        prompt = "\n".join(message.content for message in request.messages)

        for fixture in self.fixtures:
            if all(term in prompt for term in self._match_terms(fixture)):
                return CompletionResponse(
                    content=fixture["response"],
                    model=self.model,
                    finish_reason="stop",
                )

        raise FixtureNotFoundError(f"No fixture matched prompt (first 200 chars): {prompt[:200]!r}")

    async def health_check(self) -> bool:
        return True
