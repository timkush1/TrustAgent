"""Tests for the fixture-replay MockLLMProvider (match fixtures and scenario fixtures)."""

import copy
import json
from typing import Any, Dict

import pytest

from truthtable.contracts import DEMO_SCENARIOS, contract_path, load_contract
from truthtable.graphs.nodes.decomposer import (
    DECOMPOSER_SYSTEM_PROMPT,
    create_decomposer_prompt,
)
from truthtable.graphs.nodes.verifier import VERIFIER_SYSTEM_PROMPT, create_verifier_prompt
from truthtable.kb.contradiction import CONTRADICTION_SYSTEM_PROMPT, create_contradiction_prompt
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.mock import (
    UNPARSEABLE_JUDGE_ANSWER,
    FixtureNotFoundError,
    JudgeClaim,
    KnowledgeDocument,
    MockLLMProvider,
    load_scenario_pack,
    mock_judge_scope,
    parse_scenario_pack,
)
from truthtable.providers.registry import get_provider


def make_request(system: str, user: str) -> CompletionRequest:
    return CompletionRequest(
        messages=[
            Message(role=MessageRole.SYSTEM, content=system),
            Message(role=MessageRole.USER, content=user),
        ],
        model="mock",
    )


async def test_replays_matching_fixture():
    provider = MockLLMProvider(
        fixtures=[{"match": "capital of France", "response": '["Paris is the capital"]'}]
    )

    response = await provider.complete(make_request("extract claims", "capital of France?"))

    assert json.loads(response.content) == ["Paris is the capital"]
    assert response.finish_reason == "stop"


async def test_all_match_terms_must_be_present():
    provider = MockLLMProvider(
        fixtures=[
            {"match": ["<claim>", "Paris"], "response": "verifier-verdict"},
            {"match": ["Paris"], "response": "decomposer-claims"},
        ]
    )

    # Only the second fixture matches a prompt without "<claim>".
    response = await provider.complete(make_request("extract", "Paris facts"))
    assert response.content == "decomposer-claims"

    # Both terms present -> first fixture wins.
    response = await provider.complete(make_request("verify", "<claim>\nParis\n</claim>"))
    assert response.content == "verifier-verdict"


async def test_unmatched_prompt_raises():
    provider = MockLLMProvider(fixtures=[{"match": "nope", "response": "x"}])

    with pytest.raises(FixtureNotFoundError):
        await provider.complete(make_request("system", "completely different prompt"))


async def test_loads_fixtures_from_file(tmp_path):
    fixtures_file = tmp_path / "fixtures.json"
    fixtures_file.write_text(json.dumps([{"match": "hello", "response": "world"}]))

    provider = MockLLMProvider(fixtures_path=fixtures_file)
    response = await provider.complete(make_request("greet", "hello there"))

    assert response.content == "world"


async def test_registered_in_registry():
    provider = get_provider("mock", fixtures=[{"match": "x", "response": "y"}])

    assert isinstance(provider, MockLLMProvider)
    assert await provider.health_check() is True


# ---------------------------------------------------------------------------
# Scenario fixtures (AuditOptions.mock_scenario)
# ---------------------------------------------------------------------------

RESPONSE = "Refunds take 60 days. Monthly plans are non-refundable."
CLAIMS = ["Refunds take 60 days.", "Monthly plans are non-refundable."]


def _pack(**judge_overrides: Any) -> Dict[str, Any]:
    judge: Any = {
        "claims": [
            {
                "claim": CLAIMS[0],
                "quote": "Refunds take 60 days",
                "verdict": "CONTRADICTED",
                "confidence": 0.96,
                "evidence": ["Refunds take 30 days."],
                "reasoning": "The policy says 30 days.",
            },
            {"claim": CLAIMS[1], "verdict": "SUPPORTED", "confidence": 0.9},
        ]
    }
    judge.update(judge_overrides)
    return {
        "schema": "trustagent.scenarios/v1",
        "scenarios": [
            {"id": "refund", "response": RESPONSE, "judge": judge},
            {"id": "silent", "response": "Nothing to see.", "judge": None},
        ],
    }


def _decompose_request(response: str = RESPONSE) -> CompletionRequest:
    return make_request(DECOMPOSER_SYSTEM_PROMPT, create_decomposer_prompt(response))


def _verify_request(claim: str) -> CompletionRequest:
    return make_request(
        VERIFIER_SYSTEM_PROMPT, create_verifier_prompt(claim, ["Refunds: 30 days."])
    )


@pytest.fixture
def scenario_judge() -> MockLLMProvider:
    return MockLLMProvider(scenarios=parse_scenario_pack(_pack()))


async def test_scenario_answers_the_decomposition(scenario_judge: MockLLMProvider):
    with mock_judge_scope("refund") as scope:
        response = await scenario_judge.complete(_decompose_request())

    # Decomposer v2 shape: each claim with the scenario's quote ("" when it has none).
    assert json.loads(response.content) == {
        "claims": [
            {"claim": CLAIMS[0], "quote": "Refunds take 60 days"},
            {"claim": CLAIMS[1], "quote": ""},
        ]
    }
    assert scope.misses == []


async def test_scenario_answers_each_verification(scenario_judge: MockLLMProvider):
    with mock_judge_scope("refund"):
        contradicted = await scenario_judge.complete(_verify_request(CLAIMS[0]))
        supported = await scenario_judge.complete(_verify_request(CLAIMS[1]))

    assert json.loads(contradicted.content) == {
        "status": "CONTRADICTED",
        "confidence": 0.96,
        "evidence": ["Refunds take 30 days."],
        "reasoning": "The policy says 30 days.",
    }
    assert json.loads(supported.content) == {
        "status": "SUPPORTED",
        "confidence": 0.9,
        "evidence": [],
        "reasoning": "",
    }


async def test_scenario_matching_ignores_whitespace(scenario_judge: MockLLMProvider):
    with mock_judge_scope("refund"):
        response = await scenario_judge.complete(_decompose_request(f"  {RESPONSE}\n"))

    assert [item["claim"] for item in json.loads(response.content)["claims"]] == CLAIMS


async def test_scenario_unknown_verdict_replays_a_judge_failure():
    pack = _pack(claims=[{"claim": CLAIMS[0], "verdict": "UNKNOWN", "confidence": 0.0}])
    judge = MockLLMProvider(scenarios=parse_scenario_pack(pack))

    with mock_judge_scope("refund") as scope:
        response = await judge.complete(_verify_request(CLAIMS[0]))

    assert response.content == UNPARSEABLE_JUDGE_ANSWER
    assert scope.misses == []  # a replayed failure is not a fixture miss


@pytest.mark.parametrize(
    "scenario, request_factory, message",
    [
        ("no-such-scenario", _decompose_request, "not in the scenario pack"),
        ("silent", lambda: _decompose_request("Nothing to see."), "judge is null"),
        ("refund", lambda: _decompose_request("A different answer."), "neither"),
        # The scenario's response inside a longer answer is a different answer.
        ("refund", lambda: _decompose_request(f"{RESPONSE} Refunds are instant."), "neither"),
        ("refund", lambda: _decompose_request(f"Hello. {RESPONSE}"), "neither"),
        ("refund", lambda: _verify_request("An unknown claim."), "no judge verdict"),
    ],
    ids=[
        "unknown-scenario",
        "judge-null",
        "other-response",
        "extra-suffix",
        "extra-prefix",
        "other-claim",
    ],
)
async def test_scenario_strict_miss_raises_and_is_recorded(
    scenario_judge: MockLLMProvider, scenario: str, request_factory: Any, message: str
):
    with mock_judge_scope(scenario) as scope:
        with pytest.raises(FixtureNotFoundError, match=message):
            await scenario_judge.complete(request_factory())

    assert len(scope.misses) == 1
    assert message in scope.misses[0]


async def test_not_checked_claim_is_never_judged():
    pack = _pack(claims=[{"claim": CLAIMS[0], "verdict": "NOT_CHECKED", "confidence": 0.0}])
    judge = MockLLMProvider(scenarios=parse_scenario_pack(pack))

    with mock_judge_scope("refund"):
        with pytest.raises(FixtureNotFoundError):
            await judge.complete(_verify_request(CLAIMS[0]))


async def test_match_fixtures_answer_outside_a_scenario(scenario_judge: MockLLMProvider):
    scenario_judge.fixtures.append({"match": "Extract all factual claims", "response": "[]"})

    # No scope, and a scope without a scenario, both use the match fixtures.
    assert (await scenario_judge.complete(_decompose_request())).content == "[]"
    with mock_judge_scope("") as scope:
        assert (await scenario_judge.complete(_decompose_request())).content == "[]"
    assert scope.misses == []


async def test_match_fixture_miss_is_recorded_on_the_scope():
    judge = MockLLMProvider(fixtures=[])

    with mock_judge_scope() as scope:
        with pytest.raises(FixtureNotFoundError):
            await judge.complete(make_request("system", "anything"))

    assert len(scope.misses) == 1


async def test_scope_is_reset_on_exit(scenario_judge: MockLLMProvider):
    with mock_judge_scope("refund"):
        pass

    # Outside the scope the scenario no longer applies (and there are no match fixtures).
    with pytest.raises(FixtureNotFoundError, match="No fixture matched"):
        await scenario_judge.complete(_decompose_request())


def test_contract_scenario_pack_loads():
    scenarios = load_scenario_pack(contract_path(DEMO_SCENARIOS))
    pack = load_contract(DEMO_SCENARIOS)

    assert set(scenarios) == {s["id"] for s in pack["scenarios"]}
    for raw in pack["scenarios"]:
        scenario = scenarios[raw["id"]]
        assert scenario.response == raw.get("response", "")
        if raw.get("judge") is None:
            assert scenario.claims is None
        else:
            assert [c.claim for c in scenario.claims] == [
                c["claim"] for c in raw["judge"]["claims"]
            ]
            assert all(isinstance(c, JudgeClaim) for c in scenario.claims)
        assert scenario.knowledge == tuple(
            KnowledgeDocument(id=k["id"], text=k["text"]) for k in raw.get("knowledge", [])
        )
    # The knowledge stack needs these documents (make knowledge-seed).
    assert any(scenario.knowledge for scenario in scenarios.values())


async def test_registry_builds_a_scenario_judge():
    judge = get_provider("mock", model="mock", scenarios_path=contract_path(DEMO_SCENARIOS))

    assert isinstance(judge, MockLLMProvider)
    assert judge.scenarios and "grounded-pass" in judge.scenarios


# ---------------------------------------------------------------------------
# Knowledge fixtures (the knowledge stack's ingestion, outside any audit)
# ---------------------------------------------------------------------------

HANDBOOK = "New employees receive 20 days of paid vacation per year."
REMOTE = "Employees may work remotely two days a week."


def _knowledge_pack() -> Dict[str, Any]:
    pack = _pack()
    pack["scenarios"][0]["knowledge"] = [{"id": "handbook", "text": HANDBOOK}]
    pack["scenarios"][1]["knowledge"] = [
        {"id": "handbook", "text": HANDBOOK},
        {"id": "remote", "text": REMOTE},
    ]
    return pack


@pytest.fixture
def knowledge_judge() -> MockLLMProvider:
    return MockLLMProvider(scenarios=parse_scenario_pack(_knowledge_pack()))


def _contradiction_request(claim_a: str, claim_b: str) -> CompletionRequest:
    return make_request(CONTRADICTION_SYSTEM_PROMPT, create_contradiction_prompt(claim_a, claim_b))


def test_knowledge_documents_are_indexed_once(knowledge_judge: MockLLMProvider):
    assert sorted(document.id for document in knowledge_judge.knowledge.values()) == [
        "handbook",
        "remote",
    ]


async def test_knowledge_document_decomposes_into_itself(knowledge_judge: MockLLMProvider):
    response = await knowledge_judge.complete(_decompose_request(f"\n  {HANDBOOK} \n"))

    # One claim, the document's own text, quoting all of it.
    assert json.loads(response.content) == {"claims": [{"claim": HANDBOOK, "quote": HANDBOOK}]}


async def test_knowledge_claim_is_entailed_by_its_own_source(knowledge_judge: MockLLMProvider):
    request = make_request(VERIFIER_SYSTEM_PROMPT, create_verifier_prompt(HANDBOOK, [HANDBOOK]))

    answer = json.loads((await knowledge_judge.complete(request)).content)

    assert answer["status"] == "SUPPORTED"
    assert answer["confidence"] == 1.0
    assert answer["evidence"] == [HANDBOOK]


async def test_knowledge_claims_are_consistent(knowledge_judge: MockLLMProvider):
    same = await knowledge_judge.complete(_contradiction_request(HANDBOOK, HANDBOOK))
    other = await knowledge_judge.complete(_contradiction_request(HANDBOOK, REMOTE))

    assert json.loads(same.content) == {"relation": "CONSISTENT", "confidence": 1.0}
    assert json.loads(other.content) == {"relation": "CONSISTENT", "confidence": 1.0}


@pytest.mark.parametrize(
    "request_factory",
    [
        # A document the packs do not list.
        lambda: _decompose_request("The office closes at 5 pm."),
        # A knowledge document inside a longer upload is a different document.
        lambda: _decompose_request(f"{HANDBOOK} The office closes at 5 pm."),
        # A knowledge claim against a source that does not hold it.
        lambda: make_request(
            VERIFIER_SYSTEM_PROMPT, create_verifier_prompt(HANDBOOK, ["Something else."])
        ),
        # A claim that is no knowledge document, against a source that holds one.
        lambda: make_request(
            VERIFIER_SYSTEM_PROMPT, create_verifier_prompt("Vacation is unlimited.", [HANDBOOK])
        ),
        # A contradiction check with a claim the packs do not hold.
        lambda: _contradiction_request(HANDBOOK, "Vacation is unlimited."),
    ],
    ids=[
        "unknown-document",
        "longer-document",
        "claim-without-its-source",
        "unknown-claim",
        "unknown-contradiction-pair",
    ],
)
async def test_knowledge_miss_fails_loudly(knowledge_judge: MockLLMProvider, request_factory: Any):
    with pytest.raises(FixtureNotFoundError, match="No fixture matched.*knowledge documents"):
        await knowledge_judge.complete(request_factory())


async def test_knowledge_fixtures_never_answer_inside_an_audit(knowledge_judge: MockLLMProvider):
    # Every Evaluate opens a scope; only the ingestion path runs outside one.
    for scenario in ("", "refund"):
        with mock_judge_scope(scenario) as scope:
            with pytest.raises(FixtureNotFoundError):
                await knowledge_judge.complete(_decompose_request(HANDBOOK))
        assert len(scope.misses) == 1
        assert "knowledge documents" not in scope.misses[0]


async def test_match_fixtures_have_no_knowledge():
    judge = MockLLMProvider(fixtures=[])

    assert judge.knowledge == {}
    with pytest.raises(FixtureNotFoundError, match="No fixture matched") as miss:
        await judge.complete(_decompose_request(HANDBOOK))
    assert "knowledge documents" not in str(miss.value)


def _broken(mutate: Any) -> Dict[str, Any]:
    pack = copy.deepcopy(_pack())
    mutate(pack)
    return pack


@pytest.mark.parametrize(
    "pack, message",
    [
        ({"schema": "something/else", "scenarios": []}, "not a scenario pack"),
        ({"schema": "trustagent.scenarios/v1", "scenarios": {}}, "must be a list"),
        (_broken(lambda p: p["scenarios"].append({"id": "refund"})), "duplicate"),
        (_broken(lambda p: p["scenarios"][0].pop("id")), "id must be"),
        (_broken(lambda p: p["scenarios"][0].update(judge={})), "judge.claims"),
        (
            _broken(lambda p: p["scenarios"][0]["judge"]["claims"][0].update(verdict="WRONG")),
            "verdict",
        ),
        (
            _broken(lambda p: p["scenarios"][0]["judge"]["claims"][0].update(confidence=1.5)),
            "confidence",
        ),
        (
            _broken(lambda p: p["scenarios"][0]["judge"]["claims"][0].update(confidence=True)),
            "confidence",
        ),
        (_broken(lambda p: p["scenarios"][0]["judge"]["claims"][0].update(claim=" ")), "claim"),
        (
            _broken(lambda p: p["scenarios"][0]["judge"]["claims"][0].update(evidence="x")),
            "evidence",
        ),
        (_broken(lambda p: p["scenarios"][0].update(knowledge={})), "knowledge must be a list"),
        (_broken(lambda p: p["scenarios"][0].update(knowledge=["x"])), "must be an object"),
        (
            _broken(lambda p: p["scenarios"][0].update(knowledge=[{"id": "", "text": "x"}])),
            r"knowledge\[0\]\.id",
        ),
        (
            _broken(lambda p: p["scenarios"][0].update(knowledge=[{"id": "k", "text": " "}])),
            r"knowledge\[0\]\.text",
        ),
        (
            _broken(
                lambda p: (
                    p["scenarios"][0].update(knowledge=[{"id": "k", "text": "One."}]),
                    p["scenarios"][1].update(knowledge=[{"id": "k", "text": "Two."}]),
                )
            ),
            "two different texts",
        ),
    ],
)
def test_malformed_scenario_pack_is_rejected(pack: Dict[str, Any], message: str):
    with pytest.raises(ValueError, match=message):
        parse_scenario_pack(pack)
