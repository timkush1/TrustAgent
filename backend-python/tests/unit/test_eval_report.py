"""
Benchmark reports (SPEC section 2.13 benchmark-run.v1; P6 bench-publish):
system summaries against hand-computed values, schema validity of measured
runs, the measured/simulated label, and publishing (results, index and
contracts/claims/measured.json) that refuses mock output.
"""

import copy
import hashlib
import json
import shutil
from pathlib import Path
from typing import Any, Dict, List, Sequence

import pytest

from evals.examples import DatasetManifest
from evals.judges import (
    CASCADE_RESPONSE,
    FAILED,
    GATE,
    LLM_FULL_AUDIT,
    LLM_SAME_CHECKS,
    OK,
    OVER_BUDGET,
    JudgeRecord,
    SystemDescription,
)
from evals.metrics import wilson_interval
from evals.report import (
    LABEL_MEASURED,
    LABEL_SIMULATED,
    BenchmarkSchemaError,
    MockOutputRefused,
    PublishRefused,
    accuracy_intervals,
    build_benchmark_run,
    build_index,
    check_publish,
    derive_measured,
    document_problems,
    gate_conditions_text,
    gate_latency_source,
    measured_problems,
    publish,
    publish_problems,
    schema_errors,
    simulated_reasons,
    summarize_system,
    write_local_run,
)
from truthtable.contracts import contract_path, contracts_dir

from bench_runs import MANIFEST as BENCH_MANIFEST
from bench_runs import SNAPSHOT, p6_run, write_p6_run

GATE_MODEL = SystemDescription(
    decision_model=("openrouter-decisions", "typesafe/jev-1.13-20260917")
)
JUDGE = SystemDescription(judge=("openrouter", "anthropic/claude-haiku-4.5"))
CASCADE = SystemDescription(decision_model=GATE_MODEL.decision_model, judge=JUDGE.judge)
DESCRIPTIONS = {
    GATE: GATE_MODEL,
    LLM_SAME_CHECKS: JUDGE,
    LLM_FULL_AUDIT: JUDGE,
    CASCADE_RESPONSE: CASCADE,
}

MANIFEST = DatasetManifest(
    name="RAGTruth",
    subset="qa",
    split="test",
    n=6,
    revision="main@0123456789ab",
    license_note="RAGTruth, only metrics published [A9].",
    seed=13,
    sampling="stratified:label,model",
    excluded_over_budget=2,
)


def _records(system: str, *, cost: float = 0.001, mock: bool = False) -> List[JudgeRecord]:
    """Four scored examples (TP, FN, TN, FP), one failure, one exclusion."""
    rows = [
        ("a", True, True, 0.9, 100.0),
        ("b", True, False, 0.4, 200.0),
        ("c", False, False, 0.2, 300.0),
        ("d", False, True, 0.6, 400.0),
    ]
    records = [
        JudgeRecord(
            example_id=example_id,
            system=system,
            status=OK,
            label=label,
            predicted=predicted,
            score=score,
            latency_ms=latency,
            cost_usd=cost,
            cost_source="simulated" if mock else "computed",
            calls=1,
            mock=mock,
            # Where the calls went (D203): the vendor, unless the output is mock.
            details={}
            if mock
            else {"endpoint_host" if system == GATE else "host": "openrouter.ai"},
        )
        for example_id, label, predicted, score, latency in rows
    ]
    records.append(
        JudgeRecord("e", system, FAILED, True, cost_usd=2 * cost, cost_source="computed", calls=1)
    )
    records.append(JudgeRecord("f", system, OVER_BUDGET, False, error_kind="cost_cap"))
    return records


def test_summarize_system_by_hand() -> None:
    summary = summarize_system(
        LLM_SAME_CHECKS, _records(LLM_SAME_CHECKS), JUDGE, bootstrap_resamples=200
    )

    assert summary is not None
    metrics = summary["metrics"]
    assert (metrics["precision"], metrics["recall"], metrics["f1"]) == (0.5, 0.5, 0.5)
    assert metrics["accuracy"] == 0.5 and metrics["balanced_accuracy"] == 0.5
    assert metrics["auroc"] == 0.75  # 0.9 beats 0.2 and 0.6; 0.4 beats 0.2
    assert metrics["brier"] == pytest.approx((0.01 + 0.36 + 0.04 + 0.36) / 4)
    for lo, hi in metrics["ci95"].values():
        assert 0.0 <= lo <= hi <= 1.0
    assert summary["latency_ms"] == {"p50": 200.0, "p95": 400.0}
    # Every example's cost counts; per 1k attempted (4 ok + 1 failed).
    assert summary["cost"] == {"usd_total": 0.006, "usd_per_1k": 1.2, "cost_source": "computed"}
    assert [(b["lo"], b["n"], b["frac_pos"]) for b in summary["reliability"]] == [
        (0.2, 1, 0.0),
        (0.4, 1, 1.0),
        (0.6, 1, 0.0),
        (0.9, 1, 1.0),
    ]
    assert summary["judge"] == {"provider": "openrouter", "model": "anthropic/claude-haiku-4.5"}
    assert summary["decision_model"] is None


def test_summary_without_scored_examples_is_none() -> None:
    records = [JudgeRecord("x", GATE, FAILED, True), JudgeRecord("y", GATE, OVER_BUDGET, False)]

    assert summarize_system(GATE, records, GATE_MODEL) is None


def test_unknown_costs_make_the_source_unknown() -> None:
    records = _records(GATE)
    records[0].cost_usd = None

    summary = summarize_system(GATE, records, GATE_MODEL, bootstrap_resamples=10)

    assert summary is not None and summary["cost"]["cost_source"] == "unknown"


def _run(
    mock: bool = False, created_at: str = "2026-09-28T12:00:00Z", run_id: str = "lite-ragtruth-qa-1"
) -> Any:
    records = {system: _records(system, mock=mock) for system in DESCRIPTIONS}
    systems = [
        summarize_system(system, records[system], DESCRIPTIONS[system], bootstrap_resamples=50)
        for system in DESCRIPTIONS
    ]
    document, label = build_benchmark_run(
        run_id=run_id,
        created_at=created_at,
        git_sha="289a99a",
        tier="lite",
        manifest=MANIFEST,
        systems=[system for system in systems if system is not None],
        records_by_system=records,
        region="local",
        concurrency=4,
    )
    return document, label, records


def test_a_measured_run_is_schema_valid() -> None:
    document, label, _ = _run()

    assert label == LABEL_MEASURED
    assert document["measured"] is True and document["mock"] is False
    # One failure in six is more than benchmark-run.v1 allows a published run (D204).
    assert schema_errors(document, "benchmark-run.v1.schema.json") == [
        "quality/failure_rate: 0.166667 is greater than the maximum of 0.05"
    ]
    p6_document, _, _ = p6_run()
    assert schema_errors(p6_document, "benchmark-run.v1.schema.json") == []
    assert [system["id"] for system in document["systems"]] == [
        GATE,
        LLM_SAME_CHECKS,
        LLM_FULL_AUDIT,
        CASCADE_RESPONSE,
    ]
    assert document["dataset"]["revision"] == "main@0123456789ab/stratified:label,model/seed13"
    # Failures and exclusions of the measured systems (not the derived cascade),
    # plus the examples excluded as too long when the dataset was prepared.
    assert document["errors"] == {"failed": 3, "excluded_over_budget": 3 + 2}


def test_mock_output_is_labelled_simulated_and_cannot_pass_the_schema(tmp_path: Path) -> None:
    document, label, records = _run(mock=True)

    assert label == LABEL_SIMULATED
    assert document["measured"] is False and document["mock"] is True
    assert schema_errors(document, "benchmark-run.v1.schema.json") != []
    with pytest.raises(MockOutputRefused):
        write_local_run(tmp_path, document, LABEL_MEASURED, records, MANIFEST)
    measured_doc, _, _ = _run()
    with pytest.raises(MockOutputRefused):
        write_local_run(tmp_path, measured_doc, LABEL_MEASURED, records, MANIFEST)

    path = write_local_run(tmp_path, document, label, records, MANIFEST, notes=["dry run"])
    written = json.loads(path.read_text())
    assert written["label"] == LABEL_SIMULATED and written["notes"] == ["dry run"]
    assert written["schema_errors"] == [] and written["dataset_manifest"]["seed"] == 13
    assert len(written["per_example"][GATE]) == 6


@pytest.fixture
def bench_paths(tmp_path: Path) -> Dict[str, Path]:
    measured = tmp_path / "contracts" / "measured.json"
    measured.parent.mkdir()
    shutil.copy(contract_path("claims/measured.json"), measured)
    return {
        "results_dir": tmp_path / "docs" / "benchmarks" / "results",
        "index_path": tmp_path / "docs" / "benchmarks" / "index.json",
        "measured_path": measured,
    }


def _publish(run_file: Path, paths: Dict[str, Path], **options: Any) -> Dict[str, Path]:
    options.setdefault("generated_at", "2026-10-12T13:00:00Z")
    return publish(
        run_file,
        results_dir=paths["results_dir"],
        index_path=paths["index_path"],
        measured_path=paths["measured_path"],
        **options,
    )


def _codes(exc: PublishRefused) -> List[str]:
    return [problem.code for problem in exc.problems]


def test_publish_writes_results_index_and_measured_values(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    run_file = write_p6_run(tmp_path / "local")

    written = _publish(run_file, bench_paths)

    result = json.loads(written["result"].read_text())
    run = json.loads(run_file.read_text())
    assert result == run["benchmark_run"] and written["result"].name == "lite-p6-test.json"
    assert run["simulated_reasons"] == []
    index = json.loads(bench_paths["index_path"].read_text())
    assert index["schema"] == "trustagent.benchmark-index/v1"
    assert index["runs"] == [
        {
            "run_id": "lite-p6-test",
            "created_at": "2026-10-12T10:00:00Z",
            "tier": "lite",
            "dataset": {"name": "RAGTruth", "subset": "qa", "split": "test", "n": 40},
            "systems": [GATE, LLM_SAME_CHECKS, LLM_FULL_AUDIT],
            "file": "results/lite-p6-test.json",
        }
    ]
    measured = json.loads(bench_paths["measured_path"].read_text())
    assert schema_errors(measured, "measured.v1.schema.json") == []
    assert measured_problems(measured) == []
    assert measured["status"] == "measured" and measured["tier"] == "lite"
    assert measured["mock"] is False and measured["generated_by"] == "make bench-publish"
    assert measured["runs"] == ["lite-p6-test"] and measured["snapshot"] == SNAPSHOT
    values = {key: entry["value"] for key, entry in measured["values"].items()}
    gate, same, audit = result["systems"]
    assert values["fast_check_usd_per_1k"] == gate["cost"]["usd_per_1k"] == 0.04
    assert values["llm_same_checks_usd_per_1k"] == same["cost"]["usd_per_1k"] == 3.0
    # M-8: the full audit's measured cost, which the Savings row reads (roi_api.go).
    assert values["llm_full_audit_usd_per_1k"] == audit["cost"]["usd_per_1k"] == 30.0
    assert values["same_checks_cost_ratio"] == 75.0
    assert values["ragtruth_qa_agreement"] == gate["metrics"]["balanced_accuracy"]
    assert values["ragtruth_qa_accuracy"] == gate["metrics"]["accuracy"] == 0.85
    assert values["groundedness_ece_before"] == gate["metrics"]["ece"]
    # Not measured by a benchmark run: still "not yet measured".
    assert values["proxy_overhead_ms_p50"] is None and values["groundedness_ece_after"] is None
    sources = measured["sources"]
    assert sources["fast_check_usd_per_1k"] == {"cost_source": "reported"}
    assert sources["llm_full_audit_usd_per_1k"] == {"cost_source": "reported"}
    assert sources["attempts"] == {"bench_runs": 1, "refused_run_ids": []}
    accuracy = measured["intervals"]["ragtruth_qa_accuracy"]
    assert accuracy["method"] == "wilson" and (accuracy["n_pos"], accuracy["n_neg"]) == (10, 30)
    assert accuracy["lo"] < 0.85 < accuracy["hi"]
    agreement = measured["intervals"]["ragtruth_qa_agreement"]
    assert agreement["method"] == "bootstrap"
    assert (agreement["resamples"], agreement["seed"]) == (2000, 13)
    assert agreement["lo"] <= values["ragtruth_qa_agreement"] <= agreement["hi"]


def test_a_computed_cost_is_named_as_such(tmp_path: Path, bench_paths: Dict[str, Path]) -> None:
    run_file = write_p6_run(tmp_path / "local", cost_source="computed")

    _publish(run_file, bench_paths)

    measured = json.loads(bench_paths["measured_path"].read_text())
    assert measured["sources"]["fast_check_usd_per_1k"] == {"cost_source": "computed"}


@pytest.mark.parametrize(
    "knobs, code",
    [
        ({"mock": True}, "mock"),
        ({"gate_host": "127.0.0.1"}, "non_vendor_host"),
        ({"judge_host": "mock-upstream"}, "non_vendor_host"),
        ({"dirty": True}, "dirty"),
        ({"failed": 7}, "failure_rate"),
        ({"excluded": 7}, "scored"),
        ({"other_snapshot": "typesafe/jev-1.14-20261020"}, "mixed_snapshot"),
        ({"cost_source": "unknown"}, "cost_source_unknown"),
    ],
)
def test_each_refusal_has_its_own_message(
    tmp_path: Path, bench_paths: Dict[str, Path], knobs: Dict[str, Any], code: str
) -> None:
    run_file = write_p6_run(tmp_path / "local", **knobs)
    before = bench_paths["measured_path"].read_text()

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    assert code in _codes(refused.value), refused.value
    assert not bench_paths["results_dir"].exists() and not bench_paths["index_path"].exists()
    assert bench_paths["measured_path"].read_text() == before


def test_a_non_vendor_run_is_simulated(tmp_path: Path) -> None:
    # D203: mock-upstream posing as OpenRouter (TRUTHTABLE_OPENROUTER_BASE_URL) is not measured.
    document, label, records = p6_run(gate_host="127.0.0.1")

    assert label == LABEL_SIMULATED and document["measured"] is False
    assert simulated_reasons(records, document["vendor"]) == ["non_vendor_host"]
    with pytest.raises(MockOutputRefused):
        write_local_run(tmp_path, document, LABEL_MEASURED, records, BENCH_MANIFEST)


@pytest.mark.parametrize("knobs", [{"gate_host": None}, {"judge_host": None}])
def test_a_scored_result_without_a_host_is_simulated(tmp_path: Path, knobs: Dict[str, Any]) -> None:
    # D203 fails closed: a result that names no host (an old gate-eval build, a pre-P6 cache)
    # cannot show it came from a vendor, so the run is simulated, never measured.
    document, label, records = p6_run(**knobs)

    assert label == LABEL_SIMULATED and document["measured"] is False
    assert simulated_reasons(records, document["vendor"]) == ["non_vendor_host"]
    with pytest.raises(MockOutputRefused):
        write_local_run(tmp_path, document, LABEL_MEASURED, records, BENCH_MANIFEST)


def test_publish_refuses_results_without_a_host_even_under_a_vendor_block(tmp_path: Path) -> None:
    # A run.json edited to drop the gate's hosts while its vendor block still names the vendor.
    run_file = write_p6_run(tmp_path / "local")
    run = json.loads(run_file.read_text())
    for record in run["per_example"][GATE]:
        record["details"].pop("endpoint_host")
    assert run["benchmark_run"]["vendor"]["decision_host"] == "openrouter.ai"

    problems = publish_problems(run)

    assert [problem.code for problem in problems] == ["non_vendor_host"], problems
    assert "name no host" in problems[0].message


def test_every_refusal_is_listed_together(tmp_path: Path, bench_paths: Dict[str, Path]) -> None:
    run_file = write_p6_run(tmp_path / "local", dirty=True, failed=7, gate_host="127.0.0.1")

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    codes = _codes(refused.value)
    assert {"mock", "non_vendor_host", "dirty", "failure_rate", "scored"} <= set(codes)
    assert len(codes) == len(set(codes)), "each refusal once"


def test_dataset_text_in_per_example_is_refused(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    run_file = write_p6_run(tmp_path / "local")
    run = json.loads(run_file.read_text())
    run["per_example"][GATE][0]["details"]["claim"] = "Lyon is the capital"
    run["per_example"][GATE][1]["details"]["verdict"] = "Paris is the capital of France."
    run_file.write_text(json.dumps(run))

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    message = [problem.message for problem in refused.value.problems if problem.code == "text"]
    assert (
        message
        and "gate[0].details.claim" in message[0]
        and "gate[1].details.verdict" in message[0]
    )


def test_free_text_notes_go_to_diagnostics_not_per_example(tmp_path: Path) -> None:
    document, label, records = p6_run()
    records[GATE][0].note = "gate-eval: invalid example: Paris is the capital"
    run_file = write_local_run(tmp_path, document, label, records, BENCH_MANIFEST)

    run = json.loads(run_file.read_text())
    assert "note" not in run["per_example"][GATE][0]
    assert publish_problems(run) == []
    diagnostics = (run_file.parent / "diagnostics.jsonl").read_text().splitlines()
    assert [json.loads(line)["note"] for line in diagnostics] == [records[GATE][0].note]


def test_publish_refuses_a_run_without_the_p6_blocks(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    document, label, records = _run()  # built without vendor, inputs, dirty or latency
    run_file = write_local_run(tmp_path / "local", document, label, records, MANIFEST)

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    assert "missing_fields" in _codes(refused.value)


def test_publish_refuses_mock_output_even_relabelled(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    run_file = write_p6_run(tmp_path / "local", mock=True)
    tampered = json.loads(run_file.read_text())
    tampered["label"] = LABEL_MEASURED
    tampered["benchmark_run"].update(measured=True, mock=False)
    run_file.write_text(json.dumps(tampered))

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    assert "mock" in _codes(refused.value)
    assert not bench_paths["results_dir"].exists()


def test_publish_refuses_a_schema_invalid_run(tmp_path: Path, bench_paths: Dict[str, Path]) -> None:
    run_file = write_p6_run(tmp_path / "local")
    broken = json.loads(run_file.read_text())
    del broken["benchmark_run"]["environment"]
    run_file.write_text(json.dumps(broken))

    with pytest.raises(PublishRefused, match="environment"):
        _publish(run_file, bench_paths)


def test_publish_refuses_a_run_without_per_example_records(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    run_file = write_p6_run(tmp_path / "local")
    stripped = json.loads(run_file.read_text())
    stripped["per_example"] = {}
    run_file.write_text(json.dumps(stripped))

    with pytest.raises(PublishRefused, match="no per-example records"):
        _publish(run_file, bench_paths)
    assert not bench_paths["results_dir"].exists()


def test_publish_writes_nothing_when_a_published_run_is_broken(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    # A broken run already in docs/benchmarks/results stops the publish before
    # the new run, the index or measured.json is written.
    results = bench_paths["results_dir"]
    results.mkdir(parents=True)
    (results / "broken.json").write_text(json.dumps({"run_id": "broken"}))
    run_file = write_p6_run(tmp_path / "local")
    before = bench_paths["measured_path"].read_text()

    with pytest.raises(BenchmarkSchemaError, match="broken.json"):
        _publish(run_file, bench_paths)

    assert sorted(path.name for path in results.iterdir()) == ["broken.json"]
    assert not bench_paths["index_path"].exists()
    assert bench_paths["measured_path"].read_text() == before


def test_republishing_a_run_replaces_it(tmp_path: Path, bench_paths: Dict[str, Path]) -> None:
    run_file = write_p6_run(tmp_path / "local")

    _publish(run_file, bench_paths)
    _publish(run_file, bench_paths)

    index = json.loads(bench_paths["index_path"].read_text())
    assert [entry["run_id"] for entry in index["runs"]] == ["lite-p6-test"]


def test_attempts_disclose_every_run_and_the_refused_ones(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    results = tmp_path / "local"
    write_p6_run(results, run_id="lite-p6-a", created_at="2026-10-12T08:00:00Z", dirty=True)
    write_p6_run(results, run_id="lite-p6-old", created_at="2026-10-01T08:00:00Z")  # before Step 0
    run_file = write_p6_run(results, run_id="lite-p6-b", created_at="2026-10-12T09:00:00Z")

    _publish(run_file, bench_paths)

    measured = json.loads(bench_paths["measured_path"].read_text())
    assert measured["sources"]["attempts"] == {"bench_runs": 2, "refused_run_ids": ["lite-p6-a"]}


def test_check_only_writes_nothing_outside_scratch(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    run_file = write_p6_run(tmp_path / "local", dirty=True)
    before = bench_paths["measured_path"].read_text()
    scratch = tmp_path / "scratch"

    problems, written = check_publish(
        run_file,
        scratch,
        results_dir=bench_paths["results_dir"],
        measured_path=bench_paths["measured_path"],
    )

    assert [problem.code for problem in problems] == ["dirty"]
    assert all(scratch in path.parents for path in written.values())
    report = json.loads(written["problems"].read_text())
    assert report["publishable"] is False and report["problems"][0]["code"] == "dirty"
    assert json.loads(written["measured"].read_text())["status"] == "measured"
    assert bench_paths["measured_path"].read_text() == before
    assert not bench_paths["results_dir"].exists() and not bench_paths["index_path"].exists()


@pytest.mark.parametrize("knobs", [{"mock": True}, {"gate_host": "127.0.0.1"}])
def test_check_only_writes_no_would_be_files_for_a_simulated_run(
    tmp_path: Path, bench_paths: Dict[str, Path], knobs: Dict[str, Any]
) -> None:
    # A simulated run's would-be measured.json would say "measured" over mock figures (D203).
    run_file = write_p6_run(tmp_path / "local", **knobs)
    scratch = tmp_path / "scratch"

    problems, written = check_publish(
        run_file,
        scratch,
        results_dir=bench_paths["results_dir"],
        measured_path=bench_paths["measured_path"],
    )

    assert {"mock", "non_vendor_host"} & {problem.code for problem in problems}
    assert set(written) == {"problems"}
    assert sorted(path.name for path in scratch.rglob("*") if path.is_file()) == ["problems.json"]
    assert json.loads(written["problems"].read_text())["publishable"] is False


# ---------------------------------------------------------------------------
# The held-out calibration (D206)
# ---------------------------------------------------------------------------


def _holdout(root: Path, run_file: Path, **changes: Any) -> Path:
    calibration = root / "config" / "calibration" / "typesafe__jev-1.13-20260917.json"
    calibration.parent.mkdir(parents=True, exist_ok=True)
    calibration.write_text('{"schema": "trustagent.calibration/v1"}\n')
    report: Dict[str, Any] = {
        "schema": "trustagent.calibration-holdout/v1",
        "evaluator": "groundedness@1",
        "target_key": "groundedness@1",
        "snapshot": SNAPSHOT,
        "backend": "openrouter-decisions",
        "calibration_file": "config/calibration/typesafe__jev-1.13-20260917.json",
        "calibration_sha256": hashlib.sha256(calibration.read_bytes()).hexdigest(),
        "calibration_method": "isotonic",
        "calibration_quality": "measured",
        "calibration_datasets": ["ragtruth:qa-train@c103204"],
        "calibration_n": 800,
        "holdout": {"file": "labels.jsonl", "n": 40, "n_pos": 10, "n_neg": 30},
        "before": {"ece": 0.21, "brier": 0.12},
        "after": {"ece": 0.04, "brier": 0.09},
        "auroc": 0.9,
        "non_vendor_hosts": [],
        "warnings": [],
    }
    report.update(changes)
    path = run_file.parent / "holdout.groundedness@1.json"
    path.write_text(json.dumps(report))
    return path


def test_the_held_out_ece_is_folded(tmp_path: Path, bench_paths: Dict[str, Path]) -> None:
    run_file = write_p6_run(tmp_path / "local")
    report = _holdout(tmp_path, run_file)

    _publish(run_file, bench_paths, calibration_holdout=report, root=tmp_path)

    measured = json.loads(bench_paths["measured_path"].read_text())
    values = measured["values"]
    assert values["groundedness_ece_before"]["value"] == 0.21
    assert values["groundedness_ece_after"] == {
        **values["groundedness_ece_after"],
        "value": 0.04,
        "run_id": "lite-p6-test",
    }
    assert measured["sources"]["groundedness_ece"] == {
        "method": "holdout",
        "calibration_file": "config/calibration/typesafe__jev-1.13-20260917.json",
        "calibration_sha256": json.loads(report.read_text())["calibration_sha256"],
        "dataset": "ragtruth:qa-train@c103204",
    }
    assert measured_problems(measured) == []


@pytest.mark.parametrize(
    "changes, needle",
    [
        ({"snapshot": "typesafe/jev-1.14-20261020"}, "one snapshot"),
        ({"calibration_quality": "provisional"}, "not measured"),
        ({"non_vendor_hosts": ["127.0.0.1"]}, "not a vendor host"),
        ({"calibration_sha256": "0" * 64}, "changed since"),
        ({"holdout": {"n": 12}}, "not made from this run"),
        ({"evaluator": "contradiction@1"}, "not groundedness@1"),
    ],
)
def test_a_holdout_that_does_not_fit_is_refused(
    tmp_path: Path, bench_paths: Dict[str, Path], changes: Dict[str, Any], needle: str
) -> None:
    run_file = write_p6_run(tmp_path / "local")
    report = _holdout(tmp_path, run_file, **changes)

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths, calibration_holdout=report, root=tmp_path)

    holdout = [problem.message for problem in refused.value.problems if problem.code == "holdout"]
    assert any(needle in message for message in holdout), holdout


# ---------------------------------------------------------------------------
# The same rules as contracts/validate.mjs, on the lead's fixtures
# ---------------------------------------------------------------------------


def _fixtures(kind: str) -> List[Path]:
    paths = sorted((contracts_dir() / "fixtures" / "bench").glob(f"{kind}.*.json"))
    assert paths, f"no {kind} fixtures"  # a glob must not match an empty set
    return paths


@pytest.mark.parametrize("path", _fixtures("run"), ids=lambda path: path.name)
def test_run_fixtures_are_refused_for_their_reason(path: Path) -> None:
    document = json.loads(path.read_text())
    codes = [problem.code for problem in document_problems(document)]
    if path.name == "run.publishable.json":
        assert codes == []
    else:
        assert path.name.split(".")[2] in codes, codes


@pytest.mark.parametrize("path", _fixtures("measured"), ids=lambda path: path.name)
def test_measured_fixtures_are_refused_for_their_reason(path: Path) -> None:
    measured = json.loads(path.read_text())
    codes = [problem.code for problem in measured_problems(measured)]
    if path.name == "measured.publishable.json":
        assert codes == []
    else:
        assert path.name.split(".")[2] in codes, codes


def test_the_committed_measured_json_keeps_the_rules() -> None:
    assert measured_problems(json.loads(contract_path("claims/measured.json").read_text())) == []


# ---------------------------------------------------------------------------
# Intervals (D212): fixed vectors
# ---------------------------------------------------------------------------


def test_wilson_interval_fixed_vectors() -> None:
    # Wilson's 95% interval for 8 of 10 (Newcombe 1998, method 3): 0.4902 to 0.9433.
    lo, hi = wilson_interval(8, 10) or (0.0, 0.0)
    assert (round(lo, 4), round(hi, 4)) == (0.4902, 0.9433)
    lo, hi = wilson_interval(0, 10) or (1.0, 1.0)
    assert lo == 0.0 and round(hi, 4) == 0.2775
    assert wilson_interval(0, 0) is None


def test_accuracy_intervals_from_the_runs_examples() -> None:
    document, label, records = p6_run()
    per_example = {
        system: [r.to_json(public=True) for r in items] for system, items in records.items()
    }
    gate = document["systems"][0]["metrics"]

    first = accuracy_intervals(per_example, gate["accuracy"], gate["balanced_accuracy"])
    again = accuracy_intervals(per_example, gate["accuracy"], gate["balanced_accuracy"])

    assert first == again  # seeded
    wilson = wilson_interval(34, 40) or (0.0, 0.0)
    assert first["ragtruth_qa_accuracy"] == {
        "lo": round(wilson[0], 6),
        "hi": round(wilson[1], 6),
        "method": "wilson",
        "n_pos": 10,
        "n_neg": 30,
    }
    agreement = first["ragtruth_qa_agreement"]
    assert agreement["lo"] <= gate["balanced_accuracy"] <= agreement["hi"]
    assert 0.6 < agreement["lo"] < agreement["hi"] <= 1.0


@pytest.mark.parametrize("run_id", ["..", ".", "a/b", "../escape", ""])
def test_unsafe_run_ids_are_refused(tmp_path: Path, run_id: str) -> None:
    document, label, records = _run()
    document["run_id"] = run_id

    with pytest.raises(ValueError, match="run id"):
        write_local_run(tmp_path / "local", document, label, records, MANIFEST)
    assert not (tmp_path / "local").exists()


def _published(
    run_id: str, created_at: str, tier: str, name: str, subset: Any, systems: Sequence[str]
) -> Dict[str, Any]:
    built, _, _ = _run(created_at=created_at, run_id=run_id)
    document: Dict[str, Any] = copy.deepcopy(built)
    document["tier"] = tier
    document["dataset"]["name"], document["dataset"]["subset"] = name, subset
    document["systems"] = [system for system in document["systems"] if system["id"] in systems]
    return document


def test_measured_values_come_from_the_newest_run_that_measures_them() -> None:
    previous = json.loads(contract_path("claims/measured.json").read_text())
    old = _published(
        "old", "2026-09-01T00:00:00Z", "lite", "RAGTruth", "qa", [GATE, LLM_SAME_CHECKS]
    )
    old["systems"][0]["cost"]["usd_per_1k"] = 0.05
    new = _published("new", "2026-09-20T00:00:00Z", "full", "LLM-AggreFact", None, [GATE])
    new["systems"][0]["cost"]["usd_per_1k"] = 0.04

    derived = derive_measured([old, new], previous, "2026-09-28T00:00:00Z")

    values = derived["values"]
    assert values["fast_check_usd_per_1k"] == {
        **values["fast_check_usd_per_1k"],
        "value": 0.04,
        "run_id": "new",
    }
    assert values["ragtruth_qa_agreement"]["run_id"] == "old"  # only the RAGTruth-QA run
    assert values["same_checks_cost_ratio"]["run_id"] == "old"
    assert derived["runs"] == ["new", "old"] and derived["tier"] == "lite"
    assert schema_errors(derived, "measured.v1.schema.json") == []

    nothing = derive_measured([], previous, "2026-09-28T00:00:00Z")
    assert nothing["status"] == "not_measured" and nothing["runs"] == [] and nothing["tier"] is None
    assert schema_errors(nothing, "measured.v1.schema.json") == []


def test_a_measured_gate_latency_carries_its_conditions() -> None:
    """D205, D225: the connection, requests at a time, where it ran, snapshot and n."""
    previous = json.loads(contract_path("claims/measured.json").read_text())
    document, _, _ = p6_run(run_id="lite-cond")

    derived = derive_measured([document], previous, "2026-10-12T10:00:00Z")

    gate_n = document["quality"]["by_system"][GATE]["scored"]
    assert derived["values"]["gate_latency_ms_p50"]["run_id"] == "lite-cond"
    assert derived["sources"]["gate_latency"] == {
        "connection": "warm",
        "gate_concurrency": 1,
        "platform": "WSL2",
        "region": "il",
        "n": gate_n,
        "conditions": "warm connection, one request at a time, run on WSL2, region il, "
        f"snapshot {SNAPSHOT}, n={gate_n}",
    }
    assert schema_errors(derived, "measured.v1.schema.json") == []
    assert "latency_conditions" not in [p.code for p in measured_problems(derived)]

    # A run without its latency block or host_env gives no conditions, and the rule refuses it.
    bare = copy.deepcopy(document)
    del bare["latency"]
    derived = derive_measured([bare], previous, "2026-10-12T10:00:00Z")
    assert "gate_latency" not in derived.get("sources", {})
    assert "latency_conditions" in [p.code for p in measured_problems(derived)]


def test_the_publishable_fixtures_agree_on_the_latency_conditions() -> None:
    """contracts/fixtures/bench: measured.publishable's conditions are what publish derives."""
    bench = contracts_dir() / "fixtures" / "bench"
    run = json.loads((bench / "run.publishable.json").read_text())
    measured = json.loads((bench / "measured.publishable.json").read_text())
    assert measured["sources"]["gate_latency"] == gate_latency_source(run)


def test_the_conditions_name_where_the_run_really_ran() -> None:
    cold = gate_conditions_text(
        {"connection": "cold", "gate_concurrency": 4},
        {"platform": "Linux", "region": "cloud us-east"},
        180,
        None,
    )
    assert cold == (
        "cold connection (each call set up its own), 4 requests at a time, "
        "run on Linux, region cloud us-east, n=180"
    )
    assert "laptop" not in cold


def test_index_is_newest_first() -> None:
    old = _published("old", "2026-09-01T00:00:00Z", "lite", "RAGTruth", "qa", [GATE])
    new = _published("new", "2026-09-20T00:00:00Z", "lite", "RAGTruth", "qa", [GATE])

    index = build_index([old, new], "2026-09-28T00:00:00Z")

    assert [entry["run_id"] for entry in index["runs"]] == ["new", "old"]


def test_a_simulated_run_is_refused_without_echoes(
    tmp_path: Path, bench_paths: Dict[str, Path]
) -> None:
    # The derived measured.json of a mock run would only repeat the refusal ("simulated" costs).
    run_file = write_p6_run(tmp_path / "local", mock=True)

    with pytest.raises(PublishRefused) as refused:
        _publish(run_file, bench_paths)

    codes = _codes(refused.value)
    assert "mock" in codes
    assert not {"measured_schema", "cost_source_unknown"} & set(codes), codes
