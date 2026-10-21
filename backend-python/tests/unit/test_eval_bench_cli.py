"""
The benchmark CLI end to end (make bench-lite / bench-publish, SPEC P6): a
prepared dataset, a fake gate-eval executable and a scripted or mock judge.
Preconditions fail before anything is spent; a mock run is labelled simulated
and publishing refuses it; a measured run publishes schema-valid files.
"""

import json
import shutil
import subprocess  # nosec B404
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple

import pytest

from evals import bench
from evals import plan as planning
from evals.budget import ledger_total
from evals.examples import DatasetManifest, write_prepared
from evals.report import schema_errors
from truthtable.config import repo_root
from truthtable.contracts import contract_path
from truthtable.providers.base import CompletionRequest, LLMProvider

from bench_fakes import FakeGateEval, ScriptedJudge, example, gate_line


@pytest.fixture(autouse=True)
def clean_tree(monkeypatch: pytest.MonkeyPatch) -> None:
    """The runs here start on a clean tree (the dirty flag is tested on its own)."""
    monkeypatch.setattr(planning, "tree_dirty", lambda root=None: False)


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
    assert "SIMULATED (mock)" in capsys.readouterr().out
    assert bench.main(_publish_args(tmp_path, run_file)) == 1
    err = capsys.readouterr().err
    assert "refuses run lite-test-run" in err and "- mock:" in err
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
    assert measured["snapshot"] == "typesafe/jev-1.13-20260917"
    assert measured["values"]["llm_full_audit_usd_per_1k"]["value"] is not None
    # Both runs in the results directory are disclosed (D204).
    assert measured["sources"]["attempts"] == {"bench_runs": 2, "refused_run_ids": []}
    assert document["vendor"] == {"decision_host": "openrouter.ai", "judge_host": "openrouter.ai"}
    assert document["latency"] == {"connection": "warm", "gate_concurrency": 1}
    assert document["inputs"]["policy_source"] == "builtin" and document["dirty"] is False


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


def test_a_gate_eval_that_cannot_start_stops_the_run_before_any_spend(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset = _dataset(tmp_path)
    fake = _gate(tmp_path)
    fake.set(exit=1, stderr="gate-eval: no decision model: OPENROUTER_API_KEY is not set")

    code = bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate"))

    assert code == 2
    err = capsys.readouterr().err
    assert "gate-eval exited 1 before any example" in err and "OPENROUTER_API_KEY" in err
    assert len(fake.argv()) == 1 and fake.inputs() == []  # only the dry start ran
    ledger = tmp_path / "cache" / "lite-set.ledger.jsonl"
    assert not ledger.exists()


def test_gate_eval_gets_the_dotenv_values_it_needs(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    dotenv = tmp_path / "test.env"
    dotenv.write_text(
        "OPENROUTER_API_KEY=sk-or-from-dotenv\n"
        "TRUTHTABLE_JEV_BACKEND=openrouter-decisions\n"
        "TRUTHTABLE_JEV_MODEL=typesafe/jev-1.13\n"
        "ANTHROPIC_API_KEY=not-for-gate-eval\n",
        encoding="utf-8",
    )
    environ = {"TRUTHTABLE_ENV_FILE": str(dotenv), "TRUTHTABLE_JEV_MODEL": "from-the-process"}

    env = bench.gate_eval_env(environ)

    # The process environment wins; only what gate-eval reads is passed on.
    assert env == {
        "OPENROUTER_API_KEY": "sk-or-from-dotenv",
        "TRUTHTABLE_JEV_BACKEND": "openrouter-decisions",
    }
    assert bench.gate_eval_env({"TRUTHTABLE_ENV_FILE": ""}) == {}


def test_concurrency_must_be_positive(tmp_path: Path) -> None:
    # benchmark-run.v1 requires environment.concurrency >= 1; a run that could
    # never be published is refused before it starts.
    with pytest.raises(SystemExit) as exc_info:
        bench.main(
            ["run", "--dataset", str(tmp_path / "d.jsonl"), "--max-cost-usd", "1"]
            + ["--concurrency", "0"]
        )
    assert exc_info.value.code == 2


def test_publish_needs_a_named_run(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    # D204: never "the latest by time".
    assert bench.main(["publish"]) == 2
    assert "usage: bench publish --run" in capsys.readouterr().err
    assert bench.main(["publish", "--run", str(tmp_path / "missing.json")]) == 2
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


# ---------------------------------------------------------------------------
# P6 (D201, D203-D205): the dry run, the stop rule, pinned inputs, hosts, the key's limit
# ---------------------------------------------------------------------------

FAKE_KEY = "sk-or-v1-e2e-p6benchkey0000"


def _result(out: str) -> Dict[str, Any]:
    lines = [line for line in out.splitlines() if line.startswith("TRUSTAGENT_P6_RESULT ")]
    assert len(lines) == 1, out
    parsed: Dict[str, Any] = json.loads(lines[0].split(" ", 1)[1])
    return parsed


def _answer_all(fake: FakeGateEval, **overrides: Any) -> None:
    for i, (response, (_, hallucinated)) in enumerate(list(RESPONSES.items()) * 3):
        fake.answer(
            f"ex{i}",
            **{
                **gate_line(
                    groundedness=0.92 if hallucinated else 0.08,
                    verdict="unsupported" if hallucinated else "supported",
                ),
                **overrides,
            },
        )


def _fake_fetch(
    calls: List[str],
    *,
    usage: float = 0.5,
    limit: Optional[float] = 8.0,
    remaining: Optional[float] = 7.5,
) -> Any:
    def fetch(url: str, headers: Mapping[str, str], timeout: float) -> Tuple[int, bytes]:
        calls.append(url)
        if url.endswith("/api/v1/key"):
            assert headers["Authorization"] == f"Bearer {FAKE_KEY}"
            data = {
                "label": "sk-or-v1-e2e…0000",
                "usage": usage,
                "limit": limit,
                "limit_remaining": remaining,
                "is_free_tier": False,
            }
            return 200, json.dumps({"data": data}).encode()
        if "/api/v1/models" in url:
            models = [{"id": "typesafe/jev-1.13", "canonical_slug": "typesafe/jev-1.13-20260917"}]
            return 200, json.dumps({"data": models}).encode()
        return 404, b""

    return fetch


def test_a_dry_run_spends_nothing_and_prints_the_plan(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)

    code = bench.main(_run_args(tmp_path, dataset, fake, "--dry-run", "--step", "bench-lite"))

    out = capsys.readouterr().out
    assert code == 0
    assert "make bench-lite CONFIRM=1" in out and "nothing is spent" in out
    assert "6 RAGTruth qa test answers (prepared" in out
    assert "[estimate: price table x example tokens]" in out
    assert "key         no key: plan only" in out and "snapshot    unknown" in out
    assert "resume key  lite-set: $0.0000 spent" in out and "gate-eval   sha256 " in out
    assert fake.argv() == [] and not (tmp_path / "cache").exists()

    # Not prepared yet: the plan still shows n and the estimate.
    missing = tmp_path / "data" / "not-yet.jsonl"
    args = _run_args(tmp_path, missing, fake, "--dry-run", "--plan-n", "200", "--systems", "gate")
    assert bench.main(args) == 0
    assert "200 answers, not prepared yet" in capsys.readouterr().out


def test_the_dry_run_reads_the_key_and_the_snapshot_for_free(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    calls: List[str] = []
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)
    monkeypatch.setattr(bench, "_key_fetch", lambda: _fake_fetch(calls, remaining=0.6))

    assert bench.main(_run_args(tmp_path, dataset, fake, "--dry-run", "--systems", "gate")) == 0

    out = capsys.readouterr().out
    assert "openrouter.ai: used $0.5000 of the $8.00 limit; $0.6000 left" in out
    assert "snapshot    typesafe/jev-1.13-20260917" in out
    assert "cap $0.50 (lowered from $1.00" in out
    assert FAKE_KEY not in out and "e2e…0000" not in out  # neither the key nor its label
    assert sorted(calls) == [
        "https://openrouter.ai/api/v1/key",
        "https://openrouter.ai/api/v1/models?output_modalities=all",
    ]
    assert fake.argv() == []


def test_the_cap_is_lowered_to_the_keys_remaining_limit(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    calls: List[str] = []
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)
    monkeypatch.setattr(bench, "_key_fetch", lambda: _fake_fetch(calls, remaining=0.4))

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0

    out = capsys.readouterr().out
    assert "cap $0.30 (lowered from $1.00: the key's remaining limit minus $0.10)" in out
    result = _result(out)
    assert result["cap_usd"] == 0.3 and result["key_limit_remaining_usd"] == 0.4
    assert result["exit"] == 0 and result["label"] == "measured"
    assert result["hosts"] == {"decision": "openrouter.ai", "judge": None, "upstream": None}
    assert result["counts"] == {"n": 6, "scored": 6, "failed": 0}
    assert FAKE_KEY not in out


def test_a_key_with_too_little_left_is_refused_before_anything_is_spent(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)
    monkeypatch.setattr(bench, "_key_fetch", lambda: _fake_fetch([], remaining=0.12))

    code = bench.main(
        _run_args(tmp_path, dataset, fake, "--systems", "gate", "--min-cost-usd", "0.05")
    )

    captured = capsys.readouterr()
    assert code == 2 and "below this step's minimum $0.05" in captured.err
    assert fake.argv() == [] and _result(captured.out)["exit"] == 2


def test_the_stop_rule_then_a_resume_with_retry_failed(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    no_credits = gate_line(
        groundedness=None,
        contradiction=None,
        verdict=None,
        status="error",
        error_reason="error:credits",
    )
    for i in range(6):
        fake.answer(f"ex{i}", **no_credits)
    args = _run_args(tmp_path, dataset, fake, "--systems", "gate")

    assert bench.main(args) == 3

    captured = capsys.readouterr()
    assert "stopped: 5 consecutive gate failures" in captured.err
    assert "gate_error:credits" in captured.err and "402" in captured.err
    assert 'BENCH_ARGS="--retry-failed"' in captured.err
    assert len(fake.inputs()) == 5  # the sixth was never started
    assert not (tmp_path / "results").exists()
    result = _result(captured.out)
    assert result["exit"] == 3 and result["counts"] == {"n": 5, "scored": 0, "failed": 5}
    ledger = tmp_path / "cache" / "lite-set.ledger.jsonl"
    assert len(ledger.read_text().splitlines()) == 5

    # Fixed: the same command with --retry-failed continues under the same ledger.
    _answer_all(fake)
    assert bench.main(args + ["--retry-failed"]) == 0
    assert len(fake.inputs()) == 5 + 6
    assert len(ledger.read_text().splitlines()) == 11  # both sessions
    assert ledger_total(ledger) == pytest.approx(11 * 0.00004)
    run = json.loads((tmp_path / "results" / "lite-test-run" / "run.json").read_text())
    assert run["benchmark_run"]["quality"]["failed"] == 0


def test_cached_failures_do_not_trip_the_stop_rule(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    broken = gate_line(groundedness=None, contradiction=None, verdict=None, status="error")
    for i in range(4):
        fake.answer(f"ex{i}", **broken)
    args = _run_args(tmp_path, dataset, fake, "--systems", "gate", "--stop-after-failures", "5")
    assert bench.main(args) == 0  # four in a row, then two that work

    # Resumed without --retry-failed, the cached failures cost nothing again and stop nothing.
    assert bench.main(args + ["--stop-after-failures", "2"]) == 0
    assert len(fake.inputs()) == 6


def test_policy_builtin_removes_the_policy_file_from_gate_eval(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    monkeypatch.setenv("TRUTHTABLE_POLICY_FILE", "/somewhere/policies.yaml")

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0

    assert all(argv[:3] == ["--max-cost-usd", argv[1], "--evaluators"] for argv in fake.argv())
    assert all(
        "--policy" in argv and "builtin" in argv and "--warm" in argv for argv in fake.argv()
    )
    assert {env["TRUTHTABLE_POLICY_FILE"] for env in fake.envs()} == {None}
    run = json.loads((tmp_path / "results" / "lite-test-run" / "run.json").read_text())
    assert run["benchmark_run"]["inputs"]["policy_source"] == "builtin"

    # --policy file keeps the operator's policy (and says so).
    args = _run_args(tmp_path, dataset, fake, "--systems", "gate", "--policy", "file")
    assert bench.main(args + ["--resume-key", "policy-file", "--run-id", "with-file"]) == 0
    assert fake.envs()[-1]["TRUTHTABLE_POLICY_FILE"] == "/somewhere/policies.yaml"
    assert "--policy" not in fake.argv()[-1]


def test_a_resume_with_another_gate_eval_binary_is_refused(
    tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    args = _run_args(tmp_path, dataset, fake, "--systems", "gate")
    assert bench.main(args) == 0
    capsys.readouterr()
    run = json.loads((tmp_path / "results" / "lite-test-run" / "run.json").read_text())
    sha = run["benchmark_run"]["inputs"]["gate_eval_sha256"]
    assert sha == planning.sha256_file(fake.binary)

    fake.binary.write_text(fake.binary.read_text() + "\n# rebuilt\n")
    calls = len(fake.argv())

    assert bench.main(args) == 2
    assert "would pay twice" in capsys.readouterr().err
    assert len(fake.argv()) == calls  # not even the dry start ran


@pytest.mark.parametrize(
    "extra, connection, concurrency",
    [
        ((), "warm", 1),
        (("--gate-concurrency", "2"), "warm", 2),
        (("--no-warm",), "cold", 1),
    ],
)
def test_warm_or_cold_and_the_gate_concurrency_are_recorded(
    tmp_path: Path, extra: Tuple[str, ...], connection: str, concurrency: int
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    if connection == "cold":
        _answer_all(fake, connection="cold")

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate", *extra)) == 0

    document = json.loads((tmp_path / "results" / "lite-test-run" / "run.json").read_text())[
        "benchmark_run"
    ]
    assert document["latency"] == {"connection": connection, "gate_concurrency": concurrency}
    assert all(("--warm" in argv) == (connection == "warm") for argv in fake.argv())
    host_env = document["inputs"]["host_env"]
    assert len(host_env["loadavg"]) == 3 and host_env["region"] == "local"


def test_a_run_through_a_stand_in_host_is_simulated_and_refused(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    # D203: mock-upstream posing as OpenRouter (TRUTHTABLE_OPENROUTER_BASE_URL=http://127.0.0.1:...).
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    _answer_all(fake, endpoint_host="127.0.0.1")

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0

    out = capsys.readouterr().out
    assert "SIMULATED (non_vendor_host)" in out and _result(out)["label"] == "simulated"
    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    run = json.loads(run_file.read_text())
    assert run["simulated_reasons"] == ["non_vendor_host"]
    assert run["benchmark_run"]["vendor"]["decision_host"] == "127.0.0.1"
    assert bench.main(_publish_args(tmp_path, run_file)) == 1
    assert "non_vendor_host" in capsys.readouterr().err


def test_a_run_started_on_a_dirty_tree_cannot_be_published(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(planning, "tree_dirty", lambda root=None: True)
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0
    assert "uncommitted changes" in capsys.readouterr().out

    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    assert bench.main(_publish_args(tmp_path, run_file)) == 1
    assert "- dirty:" in capsys.readouterr().err


def test_publish_check_only_lists_the_refusals_into_scratch(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(planning, "tree_dirty", lambda root=None: True)
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0
    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    scratch = tmp_path / "scratch"
    capsys.readouterr()

    args = _publish_args(tmp_path, run_file) + ["--check-only", "--scratch-dir", str(scratch)]
    assert bench.main(args) == 1

    out = capsys.readouterr().out
    assert "refused: dirty:" in out and "1 refusal(s)" in out
    assert json.loads((scratch / "problems.json").read_text())["problems"][0]["code"] == "dirty"
    assert not (tmp_path / "published").exists() and not (tmp_path / "index.json").exists()


def test_export_labels_for_the_held_out_check(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == 0
    run_file = tmp_path / "results" / "lite-test-run" / "run.json"
    out = tmp_path / "labels.jsonl"

    assert bench.main(["export-labels", "--run", str(run_file), "--out", str(out)]) == 0

    lines = [json.loads(line) for line in out.read_text().splitlines()]
    # The synthetic examples carry no span kinds, so only groundedness@1 is labelled.
    assert {line["target"] for line in lines} == {"groundedness@1"} and len(lines) == 6
    assert lines[0] == {
        "id": lines[0]["id"],
        "target": "groundedness@1",
        "risk_raw": lines[0]["risk_raw"],
        "label": lines[0]["label"],
        "label_source": "public_dataset",
        "dataset": "ragtruth:qa-test@main",
        "snapshot": "typesafe/jev-1.13-20260917",
        "backend": "openrouter-decisions",
        "endpoint_host": "openrouter.ai",
    }
    assert sum(line["label"] for line in lines) == 3


def _make(*args: str) -> "subprocess.CompletedProcess[str]":
    root = repo_root()
    make = shutil.which("make")
    assert root is not None and make is not None, "the make tests need GNU make and the repository"
    # GNU make on the repository's own Makefile, with fixed arguments and no shell.
    return subprocess.run(  # nosec B603
        [make, "-s", "-C", str(root), *args],
        capture_output=True,
        text=True,
        timeout=60,
        check=False,
    )


def test_bench_args_come_last_in_the_make_recipes() -> None:
    lite = _make("-n", "bench-lite", "CONFIRM=1", "BENCH_ARGS=--retry-failed --systems gate")
    assert lite.returncode == 0, lite.stderr
    run_line = [line for line in lite.stdout.splitlines() if "evals.bench run" in line][-1]
    assert run_line.rstrip().endswith("--retry-failed --systems gate")
    assert "--resume-key lite-p6" in run_line and "--max-cost-usd 5" in run_line
    assert "--revision c103204b9ce28d6bbad859304bf30de72b8ed8fe" in lite.stdout

    smoke = _make("-n", "bench-smoke", "CONFIRM=1")
    assert smoke.returncode == 0, smoke.stderr
    run_line = [line for line in smoke.stdout.splitlines() if "evals.bench run" in line][-1]
    for part in (
        "--max-cost-usd 0.30",
        "--full-audit-n 3",
        "--resume-key smoke-p6",
        "--out-dir evals/results/smoke",
    ):
        assert part in run_line, part
    assert "--n 20 " in smoke.stdout

    dry = _make("-n", "bench-lite")
    assert dry.returncode == 0 and "--dry-run" in dry.stdout


def test_make_bench_publish_needs_run() -> None:
    refused = _make("bench-publish")
    assert refused.returncode == 2
    assert "RUN=" in refused.stdout + refused.stderr

    named = _make("-n", "bench-publish", "RUN=/abs/run.json", "CHECK_ONLY=1")
    assert named.returncode == 0, named.stderr
    assert '--run "/abs/run.json"' in named.stdout and "--check-only" in named.stdout


@pytest.mark.parametrize("status, code", [(401, 2), (402, 0), (500, 0)])
def test_a_refused_key_stops_the_run_and_an_unreadable_limit_warns(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    status: int,
    code: int,
) -> None:
    dataset, fake = _dataset(tmp_path), _gate(tmp_path)
    monkeypatch.setenv("OPENROUTER_API_KEY", FAKE_KEY)

    def fetch(url: str, headers: Mapping[str, str], timeout: float) -> Tuple[int, bytes]:
        return status, b"{}"

    monkeypatch.setattr(bench, "_key_fetch", lambda: fetch)

    assert bench.main(_run_args(tmp_path, dataset, fake, "--systems", "gate")) == code

    err = capsys.readouterr().err
    if code == 2:
        assert "the key was refused" in err and fake.argv() == []
    else:
        # The hard cap holds, and the stop rule ends a run the key's own limit stops.
        assert "the key's limit could not be read" in err and len(fake.argv()) == 7
