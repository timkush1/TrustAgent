"""
The proxy overhead in contracts/claims/measured.json (D118, GO-10): derive_measured
folds in the newest clean gateway load run (docs/benchmarks/load, load-run.v1),
its scenario c, with system proxy and the load run's id. The benchmark keys and
the tier stay the benchmark runs'; a load run alone measures nothing.
"""

import copy
import json
from pathlib import Path
from typing import Any, Dict, Optional

import pytest
from bench_runs import write_p6_run  # a publishable P6 run
from test_eval_report import _run  # the benchmark-run fixtures

from evals.report import (
    BenchmarkSchemaError,
    derive_measured,
    is_clean_load_run,
    load_load_runs,
    publish,
    schema_errors,
)
from truthtable.contracts import contract_path

GENERATED_AT = "2026-10-10T12:00:00Z"
PROXY_KEYS = ("proxy_overhead_ms_p50", "proxy_overhead_ms_p95")


def load_run(
    run_id: str,
    created_at: str,
    *,
    c_p50: float = 1.6,
    c_p95: float = 4.2,
    missed: Optional[str] = None,
    report_only: bool = False,
) -> Dict[str, Any]:
    """A load-run.v1 document from the contracts example, with scenario c's figures set."""
    run: Dict[str, Any] = json.loads(
        contract_path("fixtures/load/load-run.example.json").read_text()
    )
    run = copy.deepcopy(run)
    run["run_id"], run["created_at"], run["git_sha"] = run_id, created_at, "abc1234"
    for scenario in run["scenarios"]:
        if scenario["id"] == "c":
            scenario["p50_ms"], scenario["p95_ms"] = c_p50, c_p95
        if scenario["id"] == missed:
            scenario["ok"] = False
        if report_only:
            scenario["report_only"] = True
    assert schema_errors(run, "load-run.v1.schema.json") == []
    return run


def previous() -> Dict[str, Any]:
    loaded: Dict[str, Any] = json.loads(contract_path("claims/measured.json").read_text())
    return loaded


def test_the_proxy_overhead_comes_from_the_newest_clean_load_run() -> None:
    bench, _, _ = _run()
    runs = [
        load_run("older", "2026-10-01T00:00:00Z", c_p50=2.0, c_p95=5.0),
        load_run("newer", "2026-10-05T00:00:00Z", c_p50=1.6, c_p95=4.2),
        # Newer still, but not clean: a missed target, or published report-only.
        load_run("missed", "2026-10-08T00:00:00Z", c_p50=9.0, c_p95=19.0, missed="g"),
        load_run("report-only", "2026-10-09T00:00:00Z", c_p50=8.0, c_p95=18.0, report_only=True),
    ]
    assert [is_clean_load_run(r) for r in runs] == [True, True, False, False]

    derived = derive_measured([bench], previous(), GENERATED_AT, runs)

    values = derived["values"]
    assert values["proxy_overhead_ms_p50"]["value"] == 1.6
    assert values["proxy_overhead_ms_p95"]["value"] == 4.2
    for key in PROXY_KEYS:
        assert values[key]["run_id"] == "newer" and values[key]["system"] == "proxy"
        assert values[key]["unit"] == "ms"
    assert derived["runs"] == sorted([bench["run_id"], "newer"])
    assert derived["status"] == "measured" and derived["tier"] == "lite"
    assert schema_errors(derived, "measured.v1.schema.json") == []


def test_the_benchmark_keys_and_the_tier_are_untouched() -> None:
    bench, _, _ = _run()
    bench["tier"] = "full"
    without = derive_measured([bench], previous(), GENERATED_AT)
    with_load = derive_measured(
        [bench], previous(), GENERATED_AT, [load_run("load-1", "2026-10-05T00:00:00Z")]
    )
    for key, entry in without["values"].items():
        if key not in PROXY_KEYS:
            assert with_load["values"][key] == entry, key
    assert with_load["tier"] == without["tier"] == "full"
    assert without["values"]["proxy_overhead_ms_p50"]["value"] is None


def test_a_load_run_alone_measures_nothing() -> None:
    derived = derive_measured(
        [], previous(), GENERATED_AT, [load_run("load-1", "2026-10-05T00:00:00Z")]
    )

    # The figure is kept with its run, but without a benchmark run nothing is published as measured.
    assert derived["values"]["proxy_overhead_ms_p95"]["run_id"] == "load-1"
    assert derived["status"] == "not_measured" and derived["tier"] is None
    assert derived["generated_by"] is None
    assert schema_errors(derived, "measured.v1.schema.json") == []

    # No clean run: the proxy values keep what they were.
    unclean = derive_measured(
        [], previous(), GENERATED_AT, [load_run("x", "2026-10-05T00:00:00Z", missed="c")]
    )
    assert unclean["values"]["proxy_overhead_ms_p50"]["value"] is None


@pytest.fixture
def paths(tmp_path: Path) -> Dict[str, Path]:
    measured = tmp_path / "contracts" / "measured.json"
    measured.parent.mkdir()
    measured.write_text(contract_path("claims/measured.json").read_text())
    return {
        "results_dir": tmp_path / "docs" / "benchmarks" / "results",
        "index_path": tmp_path / "docs" / "benchmarks" / "index.json",
        "measured_path": measured,
        "load_dir": tmp_path / "docs" / "benchmarks" / "load",
    }


def test_publish_reads_the_load_runs_beside_the_results(
    tmp_path: Path, paths: Dict[str, Path]
) -> None:
    paths["load_dir"].mkdir(parents=True)
    run = load_run("20261005T000000Z-abc1234", "2026-10-05T00:00:00Z")
    (paths["load_dir"] / f"{run['run_id']}.json").write_text(json.dumps(run))
    run_file = write_p6_run(tmp_path / "local")

    # load_dir defaults to docs/benchmarks/load beside the results directory.
    publish(
        run_file,
        results_dir=paths["results_dir"],
        index_path=paths["index_path"],
        measured_path=paths["measured_path"],
        generated_at=GENERATED_AT,
    )

    measured = json.loads(paths["measured_path"].read_text())
    assert measured["values"]["proxy_overhead_ms_p95"] == {
        **measured["values"]["proxy_overhead_ms_p95"],
        "value": 4.2,
        "run_id": "20261005T000000Z-abc1234",
    }
    assert "20261005T000000Z-abc1234" in measured["runs"]
    assert schema_errors(measured, "measured.v1.schema.json") == []


def test_a_load_run_that_is_not_load_run_v1_is_refused(
    tmp_path: Path, paths: Dict[str, Path]
) -> None:
    paths["load_dir"].mkdir(parents=True)
    bad = load_run("bad", "2026-10-05T00:00:00Z")
    bad["scenarios"][0]["n"] = 10  # report.py never publishes fewer than 500
    (paths["load_dir"] / "bad.json").write_text(json.dumps(bad))
    with pytest.raises(BenchmarkSchemaError, match="load run bad.json"):
        load_load_runs(paths["load_dir"])

    run_file = write_p6_run(tmp_path / "local")
    before = paths["measured_path"].read_text()
    with pytest.raises(BenchmarkSchemaError):
        publish(run_file, **paths)
    assert paths["measured_path"].read_text() == before
    assert not paths["results_dir"].exists()
    assert load_load_runs(tmp_path / "nowhere") == []
