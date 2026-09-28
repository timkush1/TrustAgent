"""
Benchmark reports (SPEC section 2.13 benchmark-run.v1; P6 bench-lite and bench-publish).

A run is labelled **measured** only when every result came from a real decision
model and a real judge; anything that touched the mock backend or the mock
judge is **simulated** (honest numbers: never present mock output as measured).

- `write_local_run` writes the run to evals/results/<run_id>/run.json
  (gitignored): the label, the benchmark-run document, its schema errors and
  the per-example records. A simulated run's document says `measured: false,
  mock: true`, which benchmark-run.v1 forbids, so it can never pass as one.
- `publish` (make bench-publish) refuses anything but a measured, schema-valid
  run (MockOutputRefused, BenchmarkSchemaError). It writes
  docs/benchmarks/results/<run_id>.json, rebuilds docs/benchmarks/index.json
  and derives contracts/claims/measured.json from the published runs; the
  landing page and the dashboard read only those files.

System summaries carry what the section 8.3 Pareto chart needs: cost per
1,000 answers, balanced accuracy with its 95% CI, latency p50/p95, and the
calibration view (ECE, Brier, reliability bins).
"""

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple

from jsonschema import Draft202012Validator
from referencing import Registry, Resource

from truthtable.config import repo_root
from truthtable.contracts import contracts_dir
from truthtable.providers.pricing import SIMULATED, UNKNOWN

from .examples import DatasetManifest
from .judges import (
    CASCADE_RESPONSE,
    FAILED,
    OK,
    OVER_BUDGET,
    JudgeRecord,
    SystemDescription,
    aggregate_cost_source,
)
from .metrics import (
    BOOTSTRAP_RESAMPLES,
    METRIC_NAMES,
    binary_metrics,
    bootstrap_ci,
    percentile,
    reliability_bins,
)

LABEL_MEASURED = "measured"
LABEL_SIMULATED = "simulated"

BENCHMARK_SCHEMA = "benchmark-run.v1.schema.json"
MEASURED_SCHEMA = "measured.v1.schema.json"
MEASURED_CLAIMS = Path("claims") / "measured.json"
INDEX_SCHEMA = "trustagent.benchmark-index/v1"
LOCAL_RUN_SCHEMA = "trustagent.local-benchmark-run/v1"
GENERATED_BY = "make bench-publish"

METRIC_DECIMALS = 6
COST_DECIMALS = 8
LATENCY_DECIMALS = 1

# measured.json values derived from runs: key -> (system, how).
_RATIO_KEY = "same_checks_cost_ratio"


class MockOutputRefused(RuntimeError):
    """A simulated (mock) run was about to be published or labelled measured."""


class BenchmarkSchemaError(ValueError):
    """A document does not match its contracts schema."""

    def __init__(self, what: str, errors: Sequence[str]) -> None:
        super().__init__(f"{what} is not schema-valid: " + "; ".join(errors[:10]))
        self.errors = list(errors)


def utc_now() -> str:
    """RFC 3339 UTC with a trailing Z, second precision (common ISODateTime)."""
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _round(value: Optional[float], digits: int) -> Optional[float]:
    return None if value is None else round(float(value), digits)


# ---------------------------------------------------------------------------
# Schemas
# ---------------------------------------------------------------------------


def _registry(schema_dir: Path) -> Registry:
    resources = []
    for path in sorted(schema_dir.glob("*.json")):
        with open(path, encoding="utf-8") as f:
            document = json.load(f)
        resource = Resource.from_contents(document)
        resources.append((str(document.get("$id", path.name)), resource))
        # Relative $refs ("common.v1.schema.json#/...") resolve against the $id base.
    return Registry().with_resources(resources)


def schema_errors(document: Any, schema_name: str, *, root: Optional[Path] = None) -> List[str]:
    """Validation errors of `document` against contracts/schema/<schema_name>."""
    schema_dir = (root or contracts_dir()) / "schema"
    with open(schema_dir / schema_name, encoding="utf-8") as f:
        schema = json.load(f)
    validator = Draft202012Validator(schema, registry=_registry(schema_dir))
    errors = sorted(validator.iter_errors(document), key=lambda error: list(error.absolute_path))
    return [
        f"{'/'.join(str(part) for part in error.absolute_path) or '(root)'}: {error.message}"
        for error in errors
    ]


# ---------------------------------------------------------------------------
# Summaries
# ---------------------------------------------------------------------------


def is_simulated(records: Iterable[JudgeRecord]) -> bool:
    """Whether any record came from a mock decision model or judge."""
    return any(record.mock or record.cost_source == SIMULATED for record in records)


def run_label(records_by_system: Mapping[str, Sequence[JudgeRecord]]) -> str:
    """measured, or simulated when any result is mock output."""
    simulated = any(is_simulated(records) for records in records_by_system.values())
    return LABEL_SIMULATED if simulated else LABEL_MEASURED


def _cost(records: Sequence[JudgeRecord]) -> Dict[str, Any]:
    paid = [record for record in records if record.calls > 0 or record.cost_usd]
    usd_total = sum(record.cost_usd or 0.0 for record in records)
    attempted = sum(1 for record in records if record.status in (OK, FAILED))
    source = aggregate_cost_source(record.cost_source for record in paid)
    if any(record.cost_usd is None for record in paid):
        source = UNKNOWN if source != SIMULATED else source
    return {
        "usd_total": round(usd_total, COST_DECIMALS),
        "usd_per_1k": round(usd_total / attempted * 1000, COST_DECIMALS) if attempted else 0.0,
        "cost_source": source,
    }


def summarize_system(
    system: str,
    records: Sequence[JudgeRecord],
    description: SystemDescription,
    *,
    bootstrap_resamples: int = BOOTSTRAP_RESAMPLES,
    seed: int = 0,
) -> Optional[Dict[str, Any]]:
    """
    The benchmark-run.v1 entry of one system; None when it has no scored
    example. Metrics cover the examples judged ok; failures and over-budget
    exclusions are counted in the run's `errors`, and every example's cost
    counts in `cost` (usd_per_1k is per attempted example).
    """
    scored = [
        record
        for record in records
        if record.status == OK and record.predicted is not None and record.score is not None
    ]
    if not scored:
        return None
    labels = [record.label for record in scored]
    predictions = [bool(record.predicted) for record in scored]
    scores = [float(record.score or 0.0) for record in scored]

    values = binary_metrics(labels, predictions, scores)
    metrics: Dict[str, Any] = {name: _round(values[name], METRIC_DECIMALS) for name in METRIC_NAMES}
    intervals = bootstrap_ci(
        labels, predictions, scores, n_resamples=bootstrap_resamples, seed=seed
    )
    metrics["ci95"] = {
        name: [
            round(intervals[name][0], METRIC_DECIMALS),
            round(intervals[name][1], METRIC_DECIMALS),
        ]
        for name in METRIC_NAMES
        if name in intervals
    }
    latencies = [float(record.latency_ms) for record in scored if record.latency_ms is not None]
    decision_model = None
    if description.decision_model is not None:
        backend, snapshot = description.decision_model
        decision_model = {"backend": backend, "snapshot": snapshot}
    judge = None
    if description.judge is not None:
        provider, model = description.judge
        judge = {"provider": provider, "model": model}
    return {
        "id": system,
        "decision_model": decision_model,
        "judge": judge,
        "metrics": metrics,
        "latency_ms": {
            "p50": _round(percentile(latencies, 50), LATENCY_DECIMALS),
            "p95": _round(percentile(latencies, 95), LATENCY_DECIMALS),
        },
        "cost": _cost(records),
        "reliability": [
            {
                "lo": round(item.lo, METRIC_DECIMALS),
                "hi": round(item.hi, METRIC_DECIMALS),
                "n": item.n,
                "mean_pred": round(item.mean_pred, METRIC_DECIMALS),
                "frac_pos": round(item.frac_pos, METRIC_DECIMALS),
            }
            for item in reliability_bins(labels, scores)
        ],
    }


def error_counts(
    records_by_system: Mapping[str, Sequence[JudgeRecord]], manifest: DatasetManifest
) -> Dict[str, int]:
    """errors{failed, excluded_over_budget}: the measured systems (not the derived cascade)."""
    failed = excluded = 0
    for system, records in records_by_system.items():
        if system == CASCADE_RESPONSE:
            continue
        failed += sum(1 for record in records if record.status == FAILED)
        excluded += sum(1 for record in records if record.status == OVER_BUDGET)
    return {"failed": failed, "excluded_over_budget": excluded + manifest.excluded_over_budget}


def build_benchmark_run(
    *,
    run_id: str,
    created_at: str,
    git_sha: str,
    tier: str,
    manifest: DatasetManifest,
    systems: Sequence[Dict[str, Any]],
    records_by_system: Mapping[str, Sequence[JudgeRecord]],
    region: str,
    concurrency: int,
) -> Tuple[Dict[str, Any], str]:
    """The benchmark-run.v1 document of a run, and its label."""
    label = run_label(records_by_system)
    measured = label == LABEL_MEASURED
    document = {
        "run_id": run_id,
        "created_at": created_at,
        "git_sha": git_sha,
        "tier": tier,
        "measured": measured,
        "mock": not measured,
        "dataset": {
            "name": manifest.name,
            "subset": manifest.subset,
            "split": manifest.split,
            "n": manifest.n,
            "revision": manifest.run_revision,
            "license_note": manifest.license_note,
        },
        "systems": list(systems),
        "errors": error_counts(records_by_system, manifest),
        "environment": {"region": region, "concurrency": concurrency},
    }
    return document, label


# ---------------------------------------------------------------------------
# Files
# ---------------------------------------------------------------------------


def _write_json(path: Path, document: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp")
    with open(tmp, "w", encoding="utf-8", newline="\n") as f:
        json.dump(document, f, indent=2, ensure_ascii=False)
        f.write("\n")
    tmp.replace(path)


def write_local_run(
    out_dir: Path,
    document: Dict[str, Any],
    label: str,
    records_by_system: Mapping[str, Sequence[JudgeRecord]],
    manifest: DatasetManifest,
    *,
    notes: Sequence[str] = (),
) -> Path:
    """
    Write evals/results/<run_id>/run.json (see the module docstring).

    Raises:
        MockOutputRefused: If a simulated run is labelled measured.
    """
    simulated = any(is_simulated(records) for records in records_by_system.values())
    if label == LABEL_MEASURED and (simulated or document.get("mock") is not False):
        raise MockOutputRefused("refusing to label mock output measured")
    errors = schema_errors(document, BENCHMARK_SCHEMA) if label == LABEL_MEASURED else []
    path = out_dir / str(document["run_id"]) / "run.json"
    _write_json(
        path,
        {
            "schema": LOCAL_RUN_SCHEMA,
            "label": label,
            "benchmark_run": document,
            "schema_errors": errors,
            "dataset_manifest": manifest.to_json(),
            "notes": list(notes),
            "per_example": {
                system: [record.to_json() for record in records]
                for system, records in records_by_system.items()
            },
        },
    )
    return path


def _check_publishable(run: Mapping[str, Any]) -> Dict[str, Any]:
    document = run.get("benchmark_run")
    if not isinstance(document, dict):
        raise BenchmarkSchemaError("run file", ["benchmark_run is missing"])
    records = [
        JudgeRecord.from_json(record)
        for records in (run.get("per_example") or {}).values()
        for record in records
    ]
    if (
        run.get("label") != LABEL_MEASURED
        or document.get("measured") is not True
        or document.get("mock") is not False
        or is_simulated(records)
        or any(
            system.get("cost", {}).get("cost_source") == SIMULATED
            for system in document.get("systems", [])
        )
    ):
        raise MockOutputRefused(
            f"run {document.get('run_id')} is labelled {run.get('label')!r}: bench-publish "
            "refuses mock output (only measured runs are published)"
        )
    errors = schema_errors(document, BENCHMARK_SCHEMA)
    if errors:
        raise BenchmarkSchemaError(f"run {document.get('run_id')}", errors)
    return document


def load_results(results_dir: Path) -> List[Dict[str, Any]]:
    """The published benchmark runs, each checked against benchmark-run.v1."""
    runs: List[Dict[str, Any]] = []
    for path in sorted(results_dir.glob("*.json")):
        with open(path, encoding="utf-8") as f:
            document = json.load(f)
        errors = schema_errors(document, BENCHMARK_SCHEMA)
        if errors:
            raise BenchmarkSchemaError(str(path), errors)
        runs.append(document)
    return runs


def build_index(runs: Sequence[Mapping[str, Any]], generated_at: str) -> Dict[str, Any]:
    """docs/benchmarks/index.json: the published runs, newest first."""
    entries = [
        {
            "run_id": run["run_id"],
            "created_at": run["created_at"],
            "tier": run["tier"],
            "dataset": {key: run["dataset"][key] for key in ("name", "subset", "split", "n")},
            "systems": [system["id"] for system in run["systems"]],
            "file": f"results/{run['run_id']}.json",
        }
        for run in runs
    ]
    entries.sort(key=lambda entry: (entry["created_at"], entry["run_id"]), reverse=True)
    return {"schema": INDEX_SCHEMA, "generated_at": generated_at, "runs": entries}


def _system(run: Mapping[str, Any], system_id: str) -> Optional[Mapping[str, Any]]:
    for system in run.get("systems", []):
        if isinstance(system, dict) and system.get("id") == system_id:
            found: Mapping[str, Any] = system
            return found
    return None


def _latest(
    runs: Sequence[Mapping[str, Any]],
    *,
    needs: Sequence[str],
    dataset: Optional[Tuple[str, str]] = None,
) -> Optional[Mapping[str, Any]]:
    candidates = [
        run
        for run in runs
        if all(_system(run, system) is not None for system in needs)
        and (dataset is None or (run["dataset"]["name"], run["dataset"].get("subset")) == dataset)
    ]
    if not candidates:
        return None
    return max(candidates, key=lambda run: (run["created_at"], run["run_id"]))


def derive_measured(
    runs: Sequence[Mapping[str, Any]], previous: Mapping[str, Any], generated_at: str
) -> Dict[str, Any]:
    """
    contracts/claims/measured.json from the published runs: each value from
    the newest run that measures it. Values no run provides (k6 overhead,
    post-calibration ECE) keep their previous value and run id.
    """
    values: Dict[str, Dict[str, Any]] = {
        key: dict(entry) for key, entry in (previous.get("values") or {}).items()
    }
    used: Dict[str, Mapping[str, Any]] = {}

    def put(key: str, run: Optional[Mapping[str, Any]], value: Optional[float]) -> None:
        if key not in values or run is None or value is None:
            return
        values[key]["value"] = value
        values[key]["run_id"] = run["run_id"]
        used[str(run["run_id"])] = run

    def metric(run: Optional[Mapping[str, Any]], system: str, *path: str) -> Optional[float]:
        node: Any = _system(run, system) if run is not None else None
        for part in path:
            node = node.get(part) if isinstance(node, Mapping) else None
        return float(node) if isinstance(node, (int, float)) else None

    gate_run = _latest(runs, needs=["gate"])
    put("fast_check_usd_per_1k", gate_run, metric(gate_run, "gate", "cost", "usd_per_1k"))
    put("gate_latency_ms_p50", gate_run, metric(gate_run, "gate", "latency_ms", "p50"))
    put("gate_latency_ms_p95", gate_run, metric(gate_run, "gate", "latency_ms", "p95"))
    put("groundedness_ece_before", gate_run, metric(gate_run, "gate", "metrics", "ece"))
    llm_run = _latest(runs, needs=["llm_same_checks"])
    put(
        "llm_same_checks_usd_per_1k",
        llm_run,
        metric(llm_run, "llm_same_checks", "cost", "usd_per_1k"),
    )
    pair = _latest(runs, needs=["gate", "llm_same_checks"])
    fast = metric(pair, "gate", "cost", "usd_per_1k")
    same = metric(pair, "llm_same_checks", "cost", "usd_per_1k")
    put(
        _RATIO_KEY, pair, round(same / fast, METRIC_DECIMALS) if fast and same is not None else None
    )
    ragtruth = _latest(runs, needs=["gate"], dataset=("RAGTruth", "qa"))
    put(
        "ragtruth_qa_agreement",
        ragtruth,
        metric(ragtruth, "gate", "metrics", "balanced_accuracy"),
    )

    run_ids = sorted(
        {
            str(entry["run_id"])
            for entry in values.values()
            if entry.get("value") is not None and entry.get("run_id")
        }
    )
    measured = bool(run_ids)
    tiers = {str(run["tier"]) for run in used.values()}
    tier = previous.get("tier") if not tiers else ("lite" if "lite" in tiers else "full")
    return {
        "schema": "trustagent.measured/v1",
        "status": "measured" if measured else "not_measured",
        "tier": tier if measured else None,
        "mock": False,
        "generated_by": GENERATED_BY if measured else None,
        "generated_at": generated_at if measured else None,
        "runs": run_ids,
        "values": values,
    }


def benchmark_paths() -> Tuple[Path, Path]:
    """(docs/benchmarks/results, docs/benchmarks/index.json) of this repository."""
    root = repo_root()
    if root is None:
        raise FileNotFoundError("repository root not found (no VERSION file above evals/)")
    return root / "docs" / "benchmarks" / "results", root / "docs" / "benchmarks" / "index.json"


def publish(
    run_file: Path,
    *,
    results_dir: Optional[Path] = None,
    index_path: Optional[Path] = None,
    measured_path: Optional[Path] = None,
    generated_at: Optional[str] = None,
) -> Dict[str, Path]:
    """
    Publish a local measured run (make bench-publish; see the module docstring).

    Raises:
        MockOutputRefused: The run is simulated.
        BenchmarkSchemaError: The run, a published run, or the derived
            measured.json is not schema-valid.
    """
    if results_dir is None or index_path is None:
        default_results, default_index = benchmark_paths()
        results_dir = results_dir or default_results
        index_path = index_path or default_index
    results, index = results_dir, index_path
    claims = measured_path or contracts_dir() / MEASURED_CLAIMS
    now = generated_at or utc_now()

    with open(run_file, encoding="utf-8") as f:
        run = json.load(f)
    document = _check_publishable(run)
    result_path = results / f"{document['run_id']}.json"
    _write_json(result_path, document)

    runs = load_results(results)
    _write_json(index, build_index(runs, now))
    with open(claims, encoding="utf-8") as f:
        previous = json.load(f)
    derived = derive_measured(runs, previous, now)
    errors = schema_errors(derived, MEASURED_SCHEMA)
    if errors:
        raise BenchmarkSchemaError("derived measured.json", errors)
    _write_json(claims, derived)
    return {"result": result_path, "index": index, "measured": claims}
