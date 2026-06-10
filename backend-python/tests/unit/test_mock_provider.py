"""Tests for the fixture-replay MockLLMProvider."""

import json

import pytest

from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.mock import FixtureNotFoundError, MockLLMProvider
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
