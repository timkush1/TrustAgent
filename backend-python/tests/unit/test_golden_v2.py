"""
The golden regression gate (ADR-001; SPEC section 3.2): golden_v2 through the
real audit graph with the recorded judge matches the committed baseline
exactly, the committed dataset and fixtures are what build_golden produces,
and golden_v1 (frozen) regenerates byte for byte.
"""

import argparse
import json
from pathlib import Path
from typing import Any, Dict, List

import pytest

from evals import build_golden, run_eval
from evals.recorded_judge import RecordedJudge, split_fixtures
from truthtable.providers.base import CompletionRequest, Message, MessageRole
from truthtable.providers.errors import ProviderError


def _jsonl(path: Path) -> List[Dict[str, Any]]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def test_committed_golden_v2_matches_the_builder() -> None:
    examples, fixtures = build_golden.build_v2()

    assert _jsonl(run_eval.GOLDEN_V2.dataset) == examples
    assert json.loads(run_eval.GOLDEN_V2.fixtures.read_text(encoding="utf-8")) == fixtures


def test_golden_v1_is_frozen(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(build_golden, "DATASET_PATH", tmp_path / "golden_v1.jsonl")
    monkeypatch.setattr(build_golden, "FIXTURES_PATH", tmp_path / "golden_v1_fixtures.json")

    build_golden.build()

    assert (tmp_path / "golden_v1.jsonl").read_bytes() == run_eval.GOLDEN_V1.dataset.read_bytes()
    assert (tmp_path / "golden_v1_fixtures.json").read_bytes() == (
        run_eval.GOLDEN_V1.fixtures.read_bytes()
    )


def test_golden_v2_covers_the_v2_cases() -> None:
    examples = _jsonl(run_eval.GOLDEN_V2.dataset)
    labels = {claim["label"] for example in examples for claim in example["claims"]}
    outcomes = {(e["expected"]["status"], e["expected"]["verdict"]) for e in examples}

    assert {"SUPPORTED", "CONTRADICTED", "UNSUPPORTED", "PARTIALLY_SUPPORTED"} <= labels
    assert {
        ("completed", "GROUNDED"),
        ("completed", "HALLUCINATED"),
        ("completed", "UNVERIFIABLE"),
        ("completed", "NO_CLAIMS"),
        ("degraded", "GROUNDED"),
        ("degraded", "UNVERIFIABLE"),
        ("failed", None),
    } <= outcomes
    # F-L7: every injected document says where it came from.
    assert {e["context_source"] for e in examples} == {"request_context", "knowledge"}
    _, faults = split_fixtures(json.loads(run_eval.GOLDEN_V2.fixtures.read_text()))
    assert len(faults) >= 4  # judge outages: whole audit, one claim, decomposer


async def test_golden_v2_gate_matches_the_committed_baseline() -> None:
    args = argparse.Namespace(
        dataset="golden_v2", provider="mock", fixtures=None, limit=0, model="mock"
    )

    report = await run_eval.evaluate(args)

    assert run_eval.check_baseline(report, run_eval.GOLDEN_V2.baseline) == 0
    assert report["scoring_version"] == "v2"
    by_id = {result["id"]: result for result in report["per_example"]}
    outage = by_id["special-outage-all-claims"]
    assert (outage["status"], outage["verdict"], outage["trust_score"]) == (
        "failed",
        None,
        None,
    )
    assert by_id["special-outage-decomposer"]["error_kind"] == "decomposer_failed"
    assert by_id["special-greeting-no-claims"]["verdict"] == "NO_CLAIMS"
    assert by_id["special-kb-unverifiable"]["verdict"] == "UNVERIFIABLE"
    assert by_id["special-unicode-spans"]["claims"][0]["span"] == [3, 31, "exact"]


async def test_recorded_judge_raises_fault_fixtures() -> None:
    judge = RecordedJudge(
        [
            {"match": ["<claim>\nbad\n</claim>"], "error": {"kind": "rate_limited", "status": 429}},
            {"match": ["<claim>\nbad\n</claim>"], "error": {"kind": "auth", "retryable": False}},
            {"match": ["hello"], "response": "world"},
        ]
    )

    def request(text: str) -> CompletionRequest:
        return CompletionRequest(messages=[Message(MessageRole.USER, text)], model="mock")

    assert (await judge.complete(request("hello there"))).content == "world"
    with pytest.raises(ProviderError) as raised:
        await judge.complete(request("<claim>\nbad\n</claim>"))
    assert (raised.value.kind, raised.value.status, raised.value.retryable) == (
        "rate_limited",
        429,
        True,
    )
    assert judge.calls == 2


def test_context_sources_default_to_request_context() -> None:
    assert run_eval.context_sources({"context": ["a", "b"]}) == ["request_context"] * 2
    assert run_eval.context_sources({"context": ["a"], "context_source": "knowledge"}) == [
        "knowledge"
    ]
    assert run_eval.context_sources(
        {"context": ["a", "b"], "context_sources": ["knowledge", "request_system"]}
    ) == ["knowledge", "request_system"]
