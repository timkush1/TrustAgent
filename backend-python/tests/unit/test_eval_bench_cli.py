"""
The benchmark CLI end to end (make bench-lite / bench-publish, SPEC P6): a
prepared dataset, a fake gate-eval executable and a scripted or mock judge.
Preconditions fail before anything is spent; a mock run is labelled simulated
and publishing refuses it; a measured run publishes schema-valid files.
"""

import json
import shutil
from pathlib import Path
from typing import Any, Dict, List

import pytest

from evals import bench
from evals.examples import DatasetManifest, write_prepared
from evals.report import schema_errors
from truthtable.contracts import contract_path
from truthtable.providers.base import CompletionRequest, LLMProvider

from bench_fakes import FakeGateEval, ScriptedJudge, example, gate_line

RESPONSES = {
    "Lyon is the capital of France.": ("CONTRADICTED", True),
    "Paris is the capital of France.": ("SUPPORTED", False),
}


def _dataset(tmp_path: Path) -> Path:
    examples = [
        example(
            f"ex{i}",
            hallucinated=hallucinated,
            response=response,
            stratum=f"{int(hallucinated)}|m",
        )
        for i, (response, (_, hallucinated)) in enumerate(list(RESPONSES.items()) * 3)
    ]
    manifest = DatasetManifest(
        name="RAGTruth",
        subset="qa",
        split="test",
        n=len(examples),
        revision="main@0123456789ab",
        license_note="Synthetic stand-in for RAGTruth [A9].",
        seed=13,
        sampling="stratified:label,model",
    )
    path = tmp_path / "data" / "set.jsonl"
    write_prepared(path, manifest, examples)
    return path


def _answer(request: CompletionRequest) -> str:
    """Same checks, decomposition and verification, keyed on the audited text."""
    prompt = request.messages[-1].content
    if "<questions>" in prompt:
        hallucinated = "Lyon" in prompt
        p = 0.9 if hallucinated else 0.1
        return json.dumps(
            {
                "answers": {
                    "groundedness__v1": {"answer": hallucinated, "probability": p},
                    "contradiction__v1": {"answer": hallucinated, "probability": p},
                }
            }
        )
    for response, (status, _) in RESPONSES.items():
        if f"<text>\n{response}\n</text>" in prompt:
            return json.dumps({"claims": [{"claim": response, "quote": response.rstrip(".")}]})
        if f"<claim>\n{response}\n</claim>" in prompt:
            return json.dumps(
                {"status": status, "confidence": 0.9, "evidence": ["Paris"], "reasoning": "r"}
            )
    raise AssertionError(f"unexpected prompt: {prompt[:200]}")


def _mock_fixtures(tmp_path: Path) -> Path:
    fixtures: List[Dict[str, Any]] = []
    for response, (status, hallucinated) in RESPONSES.items():
        p = 0.9 if hallucinated else 0.1
        answers = {
            "answers": {
                "groundedness__v1": {"answer": hallucinated, "probability": p},
                "contradiction__v1": {"answer": hallucinated, "probability": p},
            }
        }
        state_text = json.dumps(response)
        fixtures.append({"match": ["<questions>", state_text], "response": json.dumps(answers)})
        fixtures.append(
            {
                "match": ["Extract all factual claims", f"<text>\n{response}\n</text>"],
                "response": json.dumps({"claims": [{"claim": response, "quote": response}]}),
            }
        )
        fixtures.append(
            {
                "match": [f"<claim>\n{response}\n</claim>"],
                "response": json.dumps(
                    {"status": status, "confidence": 0.9, "evidence": [], "reasoning": "r"}
                ),
            }
        )
    path = tmp_path / "fixtures.json"
    path.write_text(json.dumps(fixtures))
    return path


def _gate(tmp_path: Path, backend: str = "openrouter-decisions") -> FakeGateEval:
    gate_dir = tmp_path / "gate"
    gate_dir.mkdir()
    fake = FakeGateEval(gate_dir)
    for i, (response, (_, hallucinated)) in enumerate(list(RESPONSES.items()) * 3):
        fake.answer(
            f"ex{i}",
            **gate_line(
                groundedness=0.92 if hallucinated else 0.08,
                verdict="unsupported" if hallucinated else "supported",
                deep_audit="high" if i % 3 == 0 else "sample",
                backend=backend,
                cost_usd=0.0 if backend == "mock" else 0.00004,
                cost_source="simulated" if backend == "mock" else "reported",
            ),
        )
    return fake


def _run_args(tmp_path: Path, dataset: Path, fake: FakeGateEval, *extra: str) -> List[str]:
    return [
        "run",
        "--dataset",
        str(dataset),
        "--max-cost-usd",
        "1",
        "--gate-eval-bin",
        str(fake.binary),
        f"--gate-eval-arg={fake.arg}",
        "--full-audit-n",
        "4",
        "--run-id",
        "lite-test-run",
        "--cache-dir",
        str(tmp_path / "cache"),
        "--out-dir",
        str(tmp_path / "results"),
        *extra,
    ]


def _publish_args(tmp_path: Path, run_file: Path) -> List[str]:
    measured = tmp_path / "measured.json"
    if not measured.exists():
        shutil.copy(contract_path("claims/measured.json"), measured)
    return [
        "publish",
        "--run",
        str(run_file),
        "--results-dir",
        str(tmp_path / "published"),
        "--index",
        str(tmp_path / "index.json"),
        "--measured",
        str(measured),
    ]


def test_a_mock_run_is_simulated_and_cannot_be_published(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path, backend="mock")
    args = _run_args(
        tmp_path,
        dataset,
        fake,
        "--judge-provider",
        "mock",
        "--mock-fixtures",
        str(_mock_fixtures(tmp_path)),
    )

    assert bench.main(args) == 0

    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    run = json.loads(run_file.read_text())
    assert run["label"] == "simulated" and run["benchmark_run"]["mock"] is True
    assert [system["id"] for system in run["benchmark_run"]["systems"]] == [
        "gate",
        "llm_same_checks",
        "llm_full_audit",
        "cascade_response",
    ]
    assert all(
        record["status"] == "ok" for records in run["per_example"].values() for record in records
    )
    assert "SIMULATED" in capsys.readouterr().out
    assert bench.main(_publish_args(tmp_path, run_file)) == 1
    assert "refuses mock output" in capsys.readouterr().err
    assert not (tmp_path / "published").exists()


def test_a_measured_run_publishes(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    judge = ScriptedJudge(_answer)

    def scripted(args: Any) -> LLMProvider:
        return judge

    monkeypatch.setattr(bench, "build_judge_provider", scripted)

    assert bench.main(_run_args(tmp_path, dataset, fake)) == 0

    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    run = json.loads(run_file.read_text())
    assert run["label"] == "measured" and run["schema_errors"] == []
    document = run["benchmark_run"]
    systems = {system["id"]: system for system in document["systems"]}
    assert list(systems) == ["gate", "llm_same_checks", "llm_full_audit", "cascade_response"]
    assert systems["gate"]["metrics"]["balanced_accuracy"] == 1.0
    assert systems["gate"]["decision_model"] == {
        "backend": "openrouter-decisions",
        "snapshot": "typesafe/jev-1.13-20260917",
    }
    assert systems["llm_same_checks"]["judge"] == {
        "provider": "openrouter",
        "model": "anthropic/claude-haiku-4.5",
    }
    assert systems["llm_same_checks"]["cost"]["cost_source"] == "computed"
    assert len(run["per_example"]["llm_full_audit"]) == 4
    assert len(run["per_example"]["cascade_response"]) == 4
    assert document["errors"] == {"failed": 0, "excluded_over_budget": 0}
    assert re_git_sha(document["git_sha"])
    assert "subset of 4 examples" in " ".join(run["notes"])

    # Resuming is free: every result comes from the cache.
    calls = len(judge.requests)
    assert bench.main(_run_args(tmp_path, dataset, fake, "--run-id", "lite-test-run-2")) == 0
    assert len(judge.requests) == calls and "0 judged" in capsys.readouterr().out

    assert bench.main(_publish_args(tmp_path, run_file)) == 0
    published = json.loads((tmp_path / "published" / "lite-test-run.json").read_text())
    assert schema_errors(published, "benchmark-run.v1.schema.json") == []
    measured = json.loads((tmp_path / "measured.json").read_text())
    assert measured["status"] == "measured" and measured["runs"] == ["lite-test-run"]
    assert schema_errors(measured, "measured.v1.schema.json") == []


def re_git_sha(value: str) -> bool:
    return len(value) >= 7 and all(char in "0123456789abcdef" for char in value)


def test_preconditions_fail_before_anything_is_spent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset = _dataset(tmp_path)
    common = [
        "--max-cost-usd",
        "1",
        "--cache-dir",
        str(tmp_path / "cache"),
        "--out-dir",
        str(tmp_path / "out"),
    ]

    no_binary = [
        "run",
        "--dataset",
        str(dataset),
        "--systems",
        "gate",
        "--gate-eval-bin",
        str(tmp_path / "missing"),
        *common,
    ]
    assert bench.main(no_binary) == 2
    assert "gate-eval binary was not found" in capsys.readouterr().err

    monkeypatch.setenv("OPENROUTER_API_KEY", "")
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    no_key = ["run", "--dataset", str(dataset), "--systems", "llm_same_checks", *common]
    assert bench.main(no_key) == 2
    assert "OPENROUTER_API_KEY" in capsys.readouterr().err

    cascade_alone = [
        "run",
        "--dataset",
        str(dataset),
        "--systems",
        "gate,cascade_response",
        *common,
    ]
    assert bench.main(cascade_alone) == 2
    assert "cascade_response is computed from gate and llm_full_audit" in capsys.readouterr().err

    mock_without_fixtures = [
        "run",
        "--dataset",
        str(dataset),
        "--systems",
        "llm_same_checks",
        "--judge-provider",
        "mock",
        *common,
    ]
    assert bench.main(mock_without_fixtures) == 2

    unsafe_run_id = ["run", "--dataset", str(dataset), "--run-id", "../../escape", *common]
    assert bench.main(unsafe_run_id) == 2
    assert "run id" in capsys.readouterr().err
    assert not (tmp_path / "cache").exists() or not any((tmp_path / "cache").glob("*.ledger.jsonl"))


def test_concurrency_must_be_positive(tmp_path: Path) -> None:
    # benchmark-run.v1 requires environment.concurrency >= 1; a run that could
    # never be published is refused before it starts.
    with pytest.raises(SystemExit) as exc_info:
        bench.main(
            ["run", "--dataset", str(tmp_path / "d.jsonl"), "--max-cost-usd", "1"]
            + ["--concurrency", "0"]
        )
    assert exc_info.value.code == 2


def test_publish_without_a_run_file(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    assert bench.main(["publish", "--out-dir", str(tmp_path / "empty")]) == 2
    assert "no run file" in capsys.readouterr().err


def test_run_ids_are_schema_safe() -> None:
    manifest = DatasetManifest(
        name="LLM-AggreFact",
        subset=None,
        split="test",
        n=1,
        revision="r",
        license_note="l",
        seed=1,
        sampling="s",
    )

    assert bench.make_run_id("full", manifest, "2026-09-28T12:00:00Z") == (
        "full-llm-aggrefact-test-20260928t120000z"
    )
