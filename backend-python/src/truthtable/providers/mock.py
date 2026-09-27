"""
Mock LLM Provider

A deterministic provider for evaluation, tests and the zero-key demo. It
answers from one of two fixture kinds.

Scenario fixtures (LLM_PROVIDER=mock in the demo and e2e stacks): the judge
fixtures of a scenario pack (contracts/demo/scenarios.v1.json, schema
trustagent.scenarios/v1), keyed by the scenario id that the proxy sends as
AuditOptions.mock_scenario. The engine opens a `mock_judge_scope(scenario)`
around each audit; within it:

- a decomposition prompt whose audited text is the scenario's `response`
  (compared whitespace-normalised, never as a substring) is answered with the
  scenario's `judge.claims[].claim`, in order;
- a verification prompt (`<claim>` block) is answered with that claim's
  `verdict`, `confidence`, `evidence` and `reasoning` (an UNKNOWN verdict is
  replayed as an unparseable judge answer, i.e. a judge failure);
- anything else is a strict miss: FixtureNotFoundError is raised and recorded
  on the scope, so the audit FAILS with error_kind mock_fixture_missing rather
  than reporting a score built on a missing fixture. A scenario whose `judge`
  is null expects no judge call at all.

Match fixtures (the evaluation harness, the Tier-1 regression gate): recorded
responses matched on prompt content rather than exact prompt hashes, so prompt
template changes don't invalidate recorded fixtures:

    [
      {"match": ["Extract all factual claims", "Paris is the capital"],
       "response": "[\"Paris is the capital of France\"]"},
      {"match": ["<claim>", "Paris is the capital of France"],
       "response": "{\"status\": \"SUPPORTED\", ...}"}
    ]

Every string in `match` must appear in the rendered prompt (system + user
messages joined). The first fixture whose substrings all match wins. Match
fixtures answer every prompt outside a scenario scope.
"""

import json
import logging
import re
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Iterator, List, Mapping, Optional, Tuple, Union

from .base import CompletionRequest, CompletionResponse, LLMProvider, MessageRole

logger = logging.getLogger(__name__)

SCENARIO_PACK_SCHEMA = "trustagent.scenarios/v1"

# Claim verdicts a scenario judge fixture may carry (contracts common ClaimVerdict).
_JUDGED_VERDICTS = {"SUPPORTED", "PARTIALLY_SUPPORTED", "CONTRADICTED", "UNSUPPORTED"}
_UNKNOWN_VERDICT = "UNKNOWN"
_CLAIM_VERDICTS = _JUDGED_VERDICTS | {_UNKNOWN_VERDICT, "NOT_CHECKED"}

# The verifier renders the claim as "<claim>\n{claim}\n</claim>". Requiring the
# newlines skips the literal "<claim></claim>" in the verifier system prompt.
_CLAIM_BLOCK = re.compile(r"<claim>\n(.*?)\n</claim>", re.DOTALL)

# The decomposer renders the audited answer as "<text>\n{response}\n</text>".
# Greedy, so an answer that itself contains "</text>" is still read whole.
_TEXT_BLOCK = re.compile(r"<text>\n(.*)\n</text>", re.DOTALL)

# What the mock "judge" says when a fixture replays a judge failure (UNKNOWN):
# deliberately not JSON, so the verifier's strict parser rejects it.
UNPARSEABLE_JUDGE_ANSWER = "mock judge: no verdict"


class FixtureNotFoundError(LookupError):
    """No fixture matched the prompt — fail loudly so evals never run on garbage."""


@dataclass(frozen=True)
class JudgeClaim:
    """One claim of a scenario judge fixture: decomposer and verifier output."""

    claim: str
    verdict: str
    confidence: float
    quote: str = ""
    evidence: Tuple[str, ...] = ()
    reasoning: str = ""


@dataclass(frozen=True)
class JudgeScenario:
    """The mock-judge part of one scenario."""

    id: str
    response: str
    claims: Optional[Tuple[JudgeClaim, ...]]  # None: the judge must not be called


def _normalize(text: str) -> str:
    """Collapse whitespace so fixtures match regardless of prompt formatting."""
    return " ".join(text.split())


def _parse_judge_claim(scenario_id: str, index: int, raw: Any) -> JudgeClaim:
    where = f"scenario {scenario_id!r} judge.claims[{index}]"
    if not isinstance(raw, dict):
        raise ValueError(f"{where} must be an object")
    claim, verdict, confidence = raw.get("claim"), raw.get("verdict"), raw.get("confidence")
    if not isinstance(claim, str) or not claim.strip():
        raise ValueError(f"{where}.claim must be a non-empty string")
    if verdict not in _CLAIM_VERDICTS:
        raise ValueError(f"{where}.verdict must be one of {sorted(_CLAIM_VERDICTS)}")
    if (
        isinstance(confidence, bool)
        or not isinstance(confidence, (int, float))
        or not 0.0 <= confidence <= 1.0
    ):
        raise ValueError(f"{where}.confidence must be a number in [0, 1]")
    evidence = raw.get("evidence", [])
    if not isinstance(evidence, list) or not all(isinstance(e, str) for e in evidence):
        raise ValueError(f"{where}.evidence must be a list of strings")
    quote, reasoning = raw.get("quote", ""), raw.get("reasoning", "")
    if not isinstance(quote, str) or not isinstance(reasoning, str):
        raise ValueError(f"{where}.quote and .reasoning must be strings")
    return JudgeClaim(
        claim=claim,
        verdict=verdict,
        confidence=float(confidence),
        quote=quote,
        evidence=tuple(evidence),
        reasoning=reasoning,
    )


def parse_scenario_pack(pack: Mapping[str, Any]) -> Dict[str, JudgeScenario]:
    """
    Extract the judge fixtures of a trustagent.scenarios/v1 pack, by scenario id.

    Only the fields the mock judge uses are checked here; make contracts-check
    validates the whole pack against its schema.

    Raises:
        ValueError: If the pack is not a scenario pack or a judge fixture is malformed.
    """
    if not isinstance(pack, Mapping) or pack.get("schema") != SCENARIO_PACK_SCHEMA:
        raise ValueError(f"not a scenario pack: expected schema {SCENARIO_PACK_SCHEMA!r}")
    raw_scenarios = pack.get("scenarios")
    if not isinstance(raw_scenarios, list):
        raise ValueError("scenario pack: scenarios must be a list")

    scenarios: Dict[str, JudgeScenario] = {}
    for position, raw in enumerate(raw_scenarios):
        scenario_id = raw.get("id") if isinstance(raw, dict) else None
        if not isinstance(scenario_id, str) or not scenario_id:
            raise ValueError(f"scenario pack: scenarios[{position}].id must be a non-empty string")
        if scenario_id in scenarios:
            raise ValueError(f"scenario pack: duplicate scenario id {scenario_id!r}")
        response = raw.get("response", "")
        if not isinstance(response, str):
            raise ValueError(f"scenario {scenario_id!r}: response must be a string")

        judge = raw.get("judge")
        claims: Optional[Tuple[JudgeClaim, ...]] = None
        if judge is not None:
            raw_claims = judge.get("claims") if isinstance(judge, dict) else None
            if not isinstance(raw_claims, list):
                raise ValueError(f"scenario {scenario_id!r}: judge.claims must be a list")
            claims = tuple(
                _parse_judge_claim(scenario_id, index, item)
                for index, item in enumerate(raw_claims)
            )
        scenarios[scenario_id] = JudgeScenario(id=scenario_id, response=response, claims=claims)
    return scenarios


def load_scenario_pack(path: Union[str, Path]) -> Dict[str, JudgeScenario]:
    """Read a scenario pack file and extract its judge fixtures (see parse_scenario_pack)."""
    with open(path, encoding="utf-8") as f:
        return parse_scenario_pack(json.load(f))


@dataclass
class MockJudgeScope:
    """One audit's view of the mock judge: its scenario and any fixture misses."""

    scenario: str = ""
    misses: List[str] = field(default_factory=list)


_SCOPE: ContextVar[Optional[MockJudgeScope]] = ContextVar("truthtable_mock_judge", default=None)


@contextmanager
def mock_judge_scope(scenario: str = "") -> Iterator[MockJudgeScope]:
    """
    Scope the mock judge to one audit.

    Mock calls made inside (including from tasks the audit spawns, which copy
    the context) answer from `scenario`'s fixtures when it is set, and every
    fixture miss is recorded on the yielded scope. Other providers ignore it,
    so AuditOptions.mock_scenario is honoured only by LLM_PROVIDER=mock.
    """
    scope = MockJudgeScope(scenario=scenario)
    token = _SCOPE.set(scope)
    try:
        yield scope
    finally:
        _SCOPE.reset(token)


class MockLLMProvider(LLMProvider):
    """Deterministic fixture-replay provider for evaluation, testing and the demo."""

    def __init__(
        self,
        model: str = "mock",
        fixtures: Optional[List[Dict[str, Any]]] = None,
        fixtures_path: Optional[Union[str, Path]] = None,
        scenarios: Optional[Mapping[str, JudgeScenario]] = None,
        scenarios_path: Optional[Union[str, Path]] = None,
        **kwargs: Any,
    ):
        """
        Args:
            model: Reported model name (cosmetic).
            fixtures: List of {"match": str | [str], "response": str} entries.
            fixtures_path: JSON file containing such a list; merged after
                           `fixtures` if both are given.
            scenarios: Scenario judge fixtures by scenario id (parse_scenario_pack).
            scenarios_path: A trustagent.scenarios/v1 pack file; merged after
                           `scenarios` if both are given.
        """
        super().__init__(model=model, **kwargs)
        self.fixtures: List[Dict[str, Any]] = list(fixtures or [])
        if fixtures_path:
            with open(fixtures_path, encoding="utf-8") as f:
                self.fixtures.extend(json.load(f))

        self.scenarios: Optional[Dict[str, JudgeScenario]] = None
        if scenarios is not None or scenarios_path:
            self.scenarios = dict(scenarios or {})
            if scenarios_path:
                self.scenarios.update(load_scenario_pack(scenarios_path))

        logger.info(
            f"MockLLMProvider initialized with {len(self.fixtures)} fixtures and "
            f"{len(self.scenarios or {})} scenario fixtures"
        )

    @staticmethod
    def _match_terms(fixture: Dict[str, Any]) -> List[str]:
        match = fixture["match"]
        return [match] if isinstance(match, str) else list(match)

    def _answer_from_fixtures(self, prompt: str) -> str:
        for fixture in self.fixtures:
            if all(term in prompt for term in self._match_terms(fixture)):
                return str(fixture["response"])
        raise FixtureNotFoundError(f"No fixture matched prompt (first 200 chars): {prompt[:200]!r}")

    def _answer_from_scenario(self, scenario_id: str, user_text: str) -> str:
        scenario = (self.scenarios or {}).get(scenario_id)
        if scenario is None:
            raise FixtureNotFoundError(f"mock scenario {scenario_id!r} is not in the scenario pack")
        if scenario.claims is None:
            raise FixtureNotFoundError(
                f"mock scenario {scenario_id!r} expects no judge call (judge is null)"
            )

        claim_block = _CLAIM_BLOCK.search(user_text)
        if claim_block is not None:
            return self._verdict_for(scenario, _normalize(claim_block.group(1)))

        # Exact (whitespace-normalised) text, not containment: an answer with
        # extra sentences is a different answer, and a strict miss.
        text_block = _TEXT_BLOCK.search(user_text)
        if (
            text_block is not None
            and scenario.response.strip()
            and _normalize(text_block.group(1)) == _normalize(scenario.response)
        ):
            return json.dumps([judge_claim.claim for judge_claim in scenario.claims])

        raise FixtureNotFoundError(
            f"mock scenario {scenario_id!r}: the prompt is neither a verification of one of "
            "its claims nor a decomposition of its response"
        )

    @staticmethod
    def _verdict_for(scenario: JudgeScenario, claim: str) -> str:
        for judge_claim in scenario.claims or ():
            if _normalize(judge_claim.claim) != claim:
                continue
            if judge_claim.verdict == _UNKNOWN_VERDICT:
                return UNPARSEABLE_JUDGE_ANSWER
            if judge_claim.verdict not in _JUDGED_VERDICTS:
                break  # NOT_CHECKED: the judge is never asked about an unchecked claim
            return json.dumps(
                {
                    "status": judge_claim.verdict,
                    "confidence": judge_claim.confidence,
                    "evidence": list(judge_claim.evidence),
                    "reasoning": judge_claim.reasoning,
                }
            )
        raise FixtureNotFoundError(
            f"mock scenario {scenario.id!r} has no judge verdict for claim {claim[:80]!r}"
        )

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        scope = _SCOPE.get()
        try:
            if scope is not None and scope.scenario and self.scenarios is not None:
                user_text = "\n".join(
                    message.content
                    for message in request.messages
                    if message.role == MessageRole.USER
                )
                content = self._answer_from_scenario(scope.scenario, user_text)
            else:
                prompt = "\n".join(message.content for message in request.messages)
                content = self._answer_from_fixtures(prompt)
        except FixtureNotFoundError as exc:
            if scope is not None:
                scope.misses.append(str(exc))
            logger.error(f"Mock judge fixture miss: {exc}")
            raise

        return CompletionResponse(content=content, model=self.model, finish_reason="stop")

    async def health_check(self) -> bool:
        return True
