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
  scenario's `judge.claims[]` as `{"claims": [{"claim", "quote"}]}`, in order
  (decomposer v2; the quote locates the claim in the answer);
- a verification prompt (`<claim>` block) is answered with that claim's
  `verdict`, `confidence`, `evidence` and `reasoning` (an UNKNOWN verdict is
  replayed as an unparseable judge answer, i.e. a judge failure);
- anything else is a strict miss: FixtureNotFoundError is raised and recorded
  on the scope, so the audit FAILS with error_kind mock_fixture_missing rather
  than reporting a score built on a missing fixture. A scenario whose `judge`
  is null expects no judge call at all.

Knowledge fixtures (the knowledge stack, LLM_PROVIDER=mock): the same packs'
`knowledge[]` documents, which the knowledge base must hold. The pack says
RetrieveContext returns each document as it is, so outside any audit (no
`mock_judge_scope`: the IngestDocuments path) the mock ingests each document as
ONE claim, its own text:

- a decomposition prompt whose audited text is a knowledge document (compared
  whitespace-normalised) is answered with that one claim, quoting the whole
  text;
- the Gate-1 verification of that claim against a context that holds the
  document is SUPPORTED with confidence 1.0, so the claim is accepted;
- the ingest-time contradiction check of two knowledge documents' claims is
  CONSISTENT: a pack's documents are seeded together as one knowledge base.

Any other ingestion prompt is a miss (FixtureNotFoundError): an uploaded
document the packs do not list fails its ingest instead of being invented.

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
fixtures answer every prompt outside a scenario scope that no knowledge fixture
answers.

Inside `mock_answer_scope(answer)` the mock answers every prompt with
`answer`: the Studio drafter's simulated draft (the drafter prompt's first
example), so "Draft with AI" works without a key.
"""

import json
import logging
import re
from contextlib import contextmanager
from contextvars import ContextVar
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Iterator, List, Mapping, Optional, Tuple, Union

from .base import (
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
    MessageRole,
    normalized_usage,
)

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

# The verifier renders the evidence as "<context>\n[Document 1]\n...\n</context>".
_CONTEXT_BLOCK = re.compile(r"<context>\n(.*)\n</context>", re.DOTALL)

# The ingest-time contradiction check renders its two claims as
# "<claim_a>\n{a}\n</claim_a>" and "<claim_b>\n{b}\n</claim_b>".
_CLAIM_A_BLOCK = re.compile(r"<claim_a>\n(.*?)\n</claim_a>", re.DOTALL)
_CLAIM_B_BLOCK = re.compile(r"<claim_b>\n(.*?)\n</claim_b>", re.DOTALL)

# Gate-1 confidence of a knowledge claim: the claim is its source's own text.
KNOWLEDGE_ENTAILMENT_CONFIDENCE = 1.0

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
class KnowledgeDocument:
    """A knowledge-base document a scenario relies on (scenarios.v1 knowledge[])."""

    id: str
    text: str


@dataclass(frozen=True)
class JudgeScenario:
    """The mock-judge part of one scenario."""

    id: str
    response: str
    claims: Optional[Tuple[JudgeClaim, ...]]  # None: the judge must not be called
    knowledge: Tuple[KnowledgeDocument, ...] = ()


def _normalize(text: str) -> str:
    """Collapse whitespace so fixtures match regardless of prompt formatting."""
    return " ".join(text.split())


def _user_text(request: CompletionRequest) -> str:
    """The user messages of a request: where the prompts put the audited data."""
    return "\n".join(
        message.content for message in request.messages if message.role == MessageRole.USER
    )


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


def _parse_knowledge(scenario_id: str, raw: Any) -> Tuple[KnowledgeDocument, ...]:
    if raw is None:
        return ()
    if not isinstance(raw, list):
        raise ValueError(f"scenario {scenario_id!r}: knowledge must be a list")
    documents = []
    for index, item in enumerate(raw):
        where = f"scenario {scenario_id!r} knowledge[{index}]"
        if not isinstance(item, dict):
            raise ValueError(f"{where} must be an object")
        doc_id, text = item.get("id"), item.get("text")
        if not isinstance(doc_id, str) or not doc_id:
            raise ValueError(f"{where}.id must be a non-empty string")
        if not isinstance(text, str) or not text.strip():
            raise ValueError(f"{where}.text must be a non-empty string")
        documents.append(KnowledgeDocument(id=doc_id, text=text))
    return tuple(documents)


def parse_scenario_pack(pack: Mapping[str, Any]) -> Dict[str, JudgeScenario]:
    """
    Extract the judge fixtures of a trustagent.scenarios/v1 pack, by scenario id,
    with each scenario's knowledge documents.

    Only the fields the mock judge uses are checked here; make contracts-check
    validates the whole pack against its schema. A knowledge document id the
    pack gives two different texts is an error (the knowledge base holds one,
    and trafficgen --seed-knowledge refuses it too).

    Raises:
        ValueError: If the pack is not a scenario pack or a judge fixture is malformed.
    """
    if not isinstance(pack, Mapping) or pack.get("schema") != SCENARIO_PACK_SCHEMA:
        raise ValueError(f"not a scenario pack: expected schema {SCENARIO_PACK_SCHEMA!r}")
    raw_scenarios = pack.get("scenarios")
    if not isinstance(raw_scenarios, list):
        raise ValueError("scenario pack: scenarios must be a list")

    scenarios: Dict[str, JudgeScenario] = {}
    knowledge_texts: Dict[str, str] = {}
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
        knowledge = _parse_knowledge(scenario_id, raw.get("knowledge"))
        for document in knowledge:
            if knowledge_texts.setdefault(document.id, document.text) != document.text:
                raise ValueError(
                    f"scenario pack: knowledge document {document.id!r} has two different texts"
                )
        scenarios[scenario_id] = JudgeScenario(
            id=scenario_id, response=response, claims=claims, knowledge=knowledge
        )
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


_ANSWER: ContextVar[Optional[str]] = ContextVar("truthtable_mock_answer", default=None)


@contextmanager
def mock_answer_scope(answer: str) -> Iterator[None]:
    """
    Make the mock judge answer every prompt inside the block with `answer`
    (the Studio drafter's simulated draft). Other providers ignore it.
    """
    token = _ANSWER.set(answer)
    try:
        yield
    finally:
        _ANSWER.reset(token)


class MockLLMProvider(LLMProvider):
    """Deterministic fixture-replay provider for evaluation, testing and the demo."""

    provider_name = "mock"

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
            scenarios: Scenario judge fixtures by scenario id (parse_scenario_pack);
                       their knowledge documents are the knowledge fixtures.
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

        # Knowledge fixtures, by whitespace-normalised text.
        self.knowledge: Dict[str, KnowledgeDocument] = {}
        for scenario in (self.scenarios or {}).values():
            for document in scenario.knowledge:
                self.knowledge.setdefault(_normalize(document.text), document)

        logger.info(
            f"MockLLMProvider initialized with {len(self.fixtures)} fixtures, "
            f"{len(self.scenarios or {})} scenario fixtures and "
            f"{len(self.knowledge)} knowledge documents"
        )

    @staticmethod
    def _match_terms(fixture: Dict[str, Any]) -> List[str]:
        match = fixture["match"]
        return [match] if isinstance(match, str) else list(match)

    def _answer_from_fixtures(self, prompt: str, knowledge_hint: bool = False) -> str:
        for fixture in self.fixtures:
            if all(term in prompt for term in self._match_terms(fixture)):
                return str(fixture["response"])
        hint = (
            "; outside an audit the mock judge answers only the knowledge-base ingestion "
            "of the scenario packs' knowledge documents"
            if knowledge_hint
            else ""
        )
        raise FixtureNotFoundError(
            f"No fixture matched prompt (first 200 chars): {prompt[:200]!r}{hint}"
        )

    def _answer_from_knowledge(self, user_text: str) -> Optional[str]:
        """
        The knowledge-base ingestion of a pack's knowledge document (see the
        module docstring), or None when the prompt is not one.
        """
        if not self.knowledge:
            return None

        # Gate-1: the claim, verified against its own source document.
        claim_block = _CLAIM_BLOCK.search(user_text)
        if claim_block is not None:
            document = self.knowledge.get(_normalize(claim_block.group(1)))
            context = _CONTEXT_BLOCK.search(user_text)
            if (
                document is None
                or context is None
                or _normalize(document.text) not in _normalize(context.group(1))
            ):
                return None
            return json.dumps(
                {
                    "status": "SUPPORTED",
                    "confidence": KNOWLEDGE_ENTAILMENT_CONFIDENCE,
                    "evidence": [document.text.strip()],
                    "reasoning": f"Knowledge document {document.id} states the claim verbatim.",
                }
            )

        # The ingest-time contradiction check of two knowledge claims.
        claim_a, claim_b = _CLAIM_A_BLOCK.search(user_text), _CLAIM_B_BLOCK.search(user_text)
        if claim_a is not None or claim_b is not None:
            if (
                claim_a is None
                or claim_b is None
                or _normalize(claim_a.group(1)) not in self.knowledge
                or _normalize(claim_b.group(1)) not in self.knowledge
            ):
                return None
            return json.dumps({"relation": "CONSISTENT", "confidence": 1.0})

        # The decomposition: the document is one claim, quoting all of it.
        text_block = _TEXT_BLOCK.search(user_text)
        if text_block is not None:
            document = self.knowledge.get(_normalize(text_block.group(1)))
            if document is not None:
                text = document.text.strip()
                return json.dumps({"claims": [{"claim": text, "quote": text}]})
        return None

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
            # Decomposer v2 shape: each claim with the scenario's quote, so the
            # demo's claims are located in the answer like a real judge's.
            return json.dumps(
                {
                    "claims": [
                        {"claim": judge_claim.claim, "quote": judge_claim.quote}
                        for judge_claim in scenario.claims
                    ]
                }
            )

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
        answer = _ANSWER.get()
        try:
            if answer is not None:
                content = answer
            elif scope is not None and scope.scenario and self.scenarios is not None:
                content = self._answer_from_scenario(scope.scenario, _user_text(request))
            else:
                # Knowledge fixtures answer outside any audit only: ingestion
                # opens no mock_judge_scope, every Evaluate does.
                outside_audit = scope is None
                knowledge = (
                    self._answer_from_knowledge(_user_text(request)) if outside_audit else None
                )
                if knowledge is not None:
                    content = knowledge
                else:
                    prompt = "\n".join(message.content for message in request.messages)
                    content = self._answer_from_fixtures(
                        prompt, knowledge_hint=outside_audit and bool(self.knowledge)
                    )
        except FixtureNotFoundError as exc:
            if scope is not None:
                scope.misses.append(str(exc))
            logger.error(f"Mock judge fixture miss: {exc}")
            raise

        # The schema of a structured request is ignored; costs are simulated.
        return self.priced(
            CompletionResponse(
                content=content,
                model=self.model,
                finish_reason="stop",
                usage=normalized_usage(0, 0),
                provider=self.provider_name,
            )
        )

    async def health_check(self) -> bool:
        return True
