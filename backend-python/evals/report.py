"""
Benchmark reports (SPEC section 2.13 benchmark-run.v1; P6 bench-lite and bench-publish).

A run is labelled **measured** only when every result came from a real decision
model and a real judge **on a vendor host** (D203: common.v1 VendorHost);
anything that touched the mock backend or the mock judge, or called a stand-in
such as mock-upstream posing as OpenRouter, is **simulated** (honest numbers:
never present mock output as measured). The run file names the reasons
(`simulated_reasons`: mock, non_vendor_host).

- `write_local_run` writes the run to evals/results/<run_id>/run.json
  (gitignored): the label, the benchmark-run document, its schema errors and
  the per-example records. A simulated run's document says `measured: false,
  mock: true`, which benchmark-run.v1 forbids, so it can never pass as one.
- `publish` (make bench-publish) refuses anything but a measured, schema-valid
  run that passes every publish rule (D203, D204; `publish_problems`, the
  same rules as contracts/validate.mjs): mock output, a non-vendor host, a tree
  that was dirty when the run started, more than 5% failures or fewer than 95%
  scored, more than one decision-model snapshot, an unknown cost source,
  dataset text in per_example, a missing P6 block. Every refusal is listed at
  once (PublishRefused), and nothing is written. It writes
  docs/benchmarks/results/<run_id>.json, rebuilds docs/benchmarks/index.json
  and derives contracts/claims/measured.json from the published runs; the
  landing page and the dashboard read only those files. The proxy overhead
  comes from the newest clean gateway load run in docs/benchmarks/load
  (load-run.v1, scripts/load/report.py, D118): its scenario c (flag, the fast
  check at 0 ms) gives proxy_overhead_ms_p50/p95 with system proxy and the
  load run's id. A load run alone publishes nothing: bench-publish still needs
  a benchmark run.
- The derived measured.json also passes measured.v1's rules (`measured_problems`):
  the snapshot every value comes from (D207), every bench run since Step 0 and
  the refused ones (`sources.attempts`, D204), each cost's source (reported, or
  computed = measured tokens x vendor price), plain accuracy with its Wilson
  interval and balanced accuracy with a seeded bootstrap (2,000 resamples, seed
  13; D212), the full audit's cost, and the held-out ECE before and after
  calibration from `calibrate --holdout` (D206), never an in-sample one.
- `publish(check_only=True)` runs every check and writes the would-be files
  into a scratch directory only (Gate B).

System summaries carry what the section 8.3 Pareto chart needs: cost per
1,000 answers, balanced accuracy with its 95% CI, latency p50/p95, and the
calibration view (ECE, Brier, reliability bins).
"""

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Set, Tuple

from jsonschema import Draft202012Validator
from referencing import Registry, Resource

from truthtable.config import repo_root
from truthtable.contracts import contracts_dir
from truthtable.providers.pricing import SIMULATED, UNKNOWN

from .examples import DatasetManifest
from .judges import (
    CASCADE_RESPONSE,
    FAILED,
    GATE,
    LLM_FULL_AUDIT,
    LLM_SAME_CHECKS,
    OK,
    OVER_BUDGET,
    JudgeRecord,
    SystemDescription,
    aggregate_cost_source,
)
from .metrics import (
    BOOTSTRAP_RESAMPLES,
    METRIC_NAMES,
    balanced_accuracy_only,
    binary_metrics,
    bootstrap_ci,
    percentile,
    reliability_bins,
    wilson_interval,
)

# Gateway load runs (D118): the schema, and the scenario whose overhead is the headline figure.
LOAD_RUN_SCHEMA = "load-run.v1.schema.json"
LOAD_RUN_ID = "trustagent.load-run/v1"
PROXY_SCENARIO = "c"

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
# measured.json cost values and the system each one measures (sources.<key>.cost_source).
COST_KEYS = {
    "fast_check_usd_per_1k": "gate",
    "llm_same_checks_usd_per_1k": "llm_same_checks",
    "llm_full_audit_usd_per_1k": "llm_full_audit",
}
ACCURACY_KEY = "ragtruth_qa_accuracy"
AGREEMENT_KEY = "ragtruth_qa_agreement"
ECE_BEFORE_KEY = "groundedness_ece_before"
ECE_AFTER_KEY = "groundedness_ece_after"
LATENCY_KEYS = ("gate_latency_ms_p50", "gate_latency_ms_p95")
# D212: the published balanced accuracy's interval.
PUBLISH_BOOTSTRAP_RESAMPLES = 2000
PUBLISH_BOOTSTRAP_SEED = 13
# D204: sources.attempts counts the bench runs from P6 Step 0 on.
ATTEMPTS_SINCE = "2026-10-10T00:00:00Z"
# D204 publish thresholds.
MAX_FAILURE_RATE = 0.05
MIN_SCORED_FRACTION = 0.95
# D229: a cost's input tokens per answer are published when at least this share of the examples
# that made a call have the vendor's count.
TOKENS_REPORTED_SHARE = 0.95

# Simulated reasons (run.json `simulated_reasons`).
REASON_MOCK = "mock"
REASON_NON_VENDOR_HOST = "non_vendor_host"

HOLDOUT_SCHEMA = "trustagent.calibration-holdout/v1"
HOLDOUT_EVALUATOR = "groundedness@1"
# What make calib-holdout writes next to the run for HOLDOUT= (evals/calibset.py run_holdout).
HOLDOUT_REPORT_NAME = "holdout-groundedness.json"
CALIBRATION_FILE = re.compile(r"^config/calibration/[A-Za-z0-9._-]+\.json$")

# benchmark-run.v1 run_id; "." and ".." are refused too (the id names files).
_RUN_ID = re.compile(r"^[A-Za-z0-9._-]{1,128}$")


def check_run_id(run_id: str) -> str:
    """
    `run_id` if it is safe as a file name and schema-valid.

    Raises:
        ValueError: Otherwise.
    """
    if not _RUN_ID.fullmatch(run_id) or run_id in (".", ".."):
        raise ValueError(
            f"run id {run_id!r} must be 1-128 letters, digits, '.', '_' or '-' (not . or ..)"
        )
    return run_id


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


def vendor_hosts() -> Tuple[str, ...]:
    """common.v1 VendorHost: the hosts a measured run may name (D203)."""
    with open(contracts_dir() / "schema" / "common.v1.schema.json", encoding="utf-8") as f:
        common = json.load(f)
    return tuple(common["$defs"]["VendorHost"]["enum"])


def record_hosts(records_by_system: Mapping[str, Sequence[JudgeRecord]]) -> Set[str]:
    """Every host the records say their calls went to (gate endpoint_host, judge host)."""
    hosts: Set[str] = set()
    for records in records_by_system.values():
        for record in records:
            for name in ("endpoint_host", "host"):
                host = record.details.get(name)
                if isinstance(host, str) and host:
                    hosts.add(host)
    return hosts


# The detail that names where a system's calls went (D203): gate-eval's endpoint_host, the
# judge provider's host. cascade_response is computed offline from the other two.
HOST_DETAIL = {GATE: "endpoint_host", LLM_SAME_CHECKS: "host", LLM_FULL_AUDIT: "host"}


def unhosted_results(records_by_system: Mapping[str, Sequence[JudgeRecord]]) -> int:
    """
    How many scored, non-mock results name no host (D203 fails closed): a
    result whose host is unknown (a gate-eval build without endpoint_host, a
    cache from before P6, a host too long to keep) cannot show it came from a
    vendor, so it counts as a non-vendor host.
    """
    count = 0
    for system, name in HOST_DETAIL.items():
        for record in records_by_system.get(system) or []:
            if record.mock or not _is_scored(record):
                continue
            host = record.details.get(name)
            if not (isinstance(host, str) and host):
                count += 1
    return count


def simulated_reasons(
    records_by_system: Mapping[str, Sequence[JudgeRecord]],
    vendor: Optional[Mapping[str, Optional[str]]] = None,
) -> List[str]:
    """
    Why a run is simulated (empty when it is measured): mock output, or a call
    to a host that is not a vendor's (D203), e.g. mock-upstream behind
    TRUTHTABLE_OPENROUTER_BASE_URL, or a scored result that names no host.
    """
    reasons: List[str] = []
    if any(is_simulated(records) for records in records_by_system.values()):
        reasons.append(REASON_MOCK)
    allowed = set(vendor_hosts())
    hosts = record_hosts(records_by_system)
    if vendor is not None:
        hosts |= {host for host in vendor.values() if host}
    if hosts - allowed or unhosted_results(records_by_system):
        reasons.append(REASON_NON_VENDOR_HOST)
    return reasons


def run_label(
    records_by_system: Mapping[str, Sequence[JudgeRecord]],
    vendor: Optional[Mapping[str, Optional[str]]] = None,
) -> str:
    """measured, or simulated when any result is mock output or came from a non-vendor host."""
    return LABEL_SIMULATED if simulated_reasons(records_by_system, vendor) else LABEL_MEASURED


def _is_scored(record: JudgeRecord) -> bool:
    return record.status == OK and record.predicted is not None and record.score is not None


def quality_counts(
    records_by_system: Mapping[str, Sequence[JudgeRecord]],
) -> Dict[str, Any]:
    """
    benchmark-run.v1 `quality` (D204): examples attempted, scored, failed and
    excluded over the measured systems (the derived cascade is not counted),
    and per system.
    """
    by_system: Dict[str, Dict[str, int]] = {}
    for system, records in records_by_system.items():
        if system == CASCADE_RESPONSE:
            continue
        by_system[system] = {
            "n": len(records),
            "scored": sum(1 for record in records if _is_scored(record)),
            "failed": sum(1 for record in records if record.status == FAILED),
            "excluded": sum(1 for record in records if record.status == OVER_BUDGET),
        }
    totals = {
        name: sum(counts[name] for counts in by_system.values())
        for name in ("n", "scored", "failed", "excluded")
    }
    rate = totals["failed"] / totals["n"] if totals["n"] else 0.0
    return {**totals, "failure_rate": round(rate, METRIC_DECIMALS), "by_system": by_system}


def input_tokens_per_answer(records: Sequence[JudgeRecord]) -> Optional[int]:
    """
    The mean input tokens of the examples that made a call, as the vendor counted them: what a
    cost per answer paid for (it grows with the documents' length). None when fewer than
    TOKENS_REPORTED_SHARE of those examples have a count (a mean of a few is no description).
    """
    called = [record for record in records if record.calls > 0]
    counted = [record.input_tokens for record in called if record.input_tokens]
    if not counted or len(counted) < TOKENS_REPORTED_SHARE * len(called):
        return None
    return round(sum(counted) / len(counted))


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
        "input_tokens_per_answer": input_tokens_per_answer(records),
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
    vendor: Optional[Mapping[str, Optional[str]]] = None,
    inputs: Optional[Mapping[str, Any]] = None,
    dirty: Optional[bool] = None,
    latency: Optional[Mapping[str, Any]] = None,
) -> Tuple[Dict[str, Any], str]:
    """
    The benchmark-run.v1 document of a run, and its label. The P6 blocks
    (vendor, inputs, dirty, latency; D203-D205) are written when given; quality
    always is. A run without them is never publishable (missing_fields).
    """
    label = run_label(records_by_system, vendor)
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
        "quality": quality_counts(records_by_system),
    }
    if vendor is not None:
        document["vendor"] = {
            "decision_host": vendor.get("decision_host"),
            "judge_host": vendor.get("judge_host"),
        }
    if inputs is not None:
        document["inputs"] = dict(inputs)
    if dirty is not None:
        document["dirty"] = dirty
    if latency is not None:
        document["latency"] = dict(latency)
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
    reasons = simulated_reasons(records_by_system, document.get("vendor"))
    if label == LABEL_MEASURED and (reasons or document.get("mock") is not False):
        raise MockOutputRefused(
            "refusing to label mock output (or output from a non-vendor host) measured"
        )
    errors = schema_errors(document, BENCHMARK_SCHEMA) if label == LABEL_MEASURED else []
    path = out_dir / check_run_id(str(document["run_id"])) / "run.json"
    _write_json(
        path,
        {
            "schema": LOCAL_RUN_SCHEMA,
            "label": label,
            "simulated_reasons": reasons,
            "benchmark_run": document,
            "schema_errors": errors,
            "dataset_manifest": manifest.to_json(),
            "notes": list(notes),
            # Ids, labels and scores only (D204): free-text notes go to diagnostics.jsonl.
            "per_example": {
                system: [record.to_json(public=True) for record in records]
                for system, records in records_by_system.items()
            },
        },
    )
    notes_path = path.with_name("diagnostics.jsonl")
    with open(notes_path, "w", encoding="utf-8", newline="\n") as f:
        for system, records in records_by_system.items():
            for record in records:
                if record.note:
                    entry = {
                        "system": system,
                        "example_id": record.example_id,
                        "error_kind": record.error_kind,
                        "note": record.note,
                    }
                    f.write(json.dumps(entry, ensure_ascii=False) + "\n")
    return path


# ---------------------------------------------------------------------------
# Publish rules (D203, D204): the same as contracts/validate.mjs publishProblems
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Problem:
    """One reason bench publish refuses: a code (a fixture name) and a sentence."""

    code: str
    message: str

    def __str__(self) -> str:
        return f"{self.code}: {self.message}"


class PublishRefused(RuntimeError):
    """bench publish refuses a run; every problem is listed, and nothing was written."""

    def __init__(self, run_id: Any, problems: Sequence[Problem]) -> None:
        self.run_id = run_id
        self.problems = list(problems)
        super().__init__(f"run {run_id}: " + "; ".join(str(problem) for problem in self.problems))


# What a published run's per_example may hold (D204: ids, labels and scores only; the
# RAGTruth-QA passages are MS MARCO, research-only).
_RECORD_KEYS = frozenset(
    {
        "example_id",
        "system",
        "status",
        "label",
        "predicted",
        "score",
        "latency_ms",
        "cost_usd",
        "cost_source",
        "calls",
        "input_tokens",
        "error_kind",
        "mock",
        "details",
    }
)
_DETAIL_KEYS = frozenset(
    {
        "action",
        "answers",
        "audit_status",
        "backend",
        "claim_status",
        "claims",
        "connection",
        "contradiction",
        "degraded_reasons",
        "endpoint_host",
        "escalate",
        "escalated",
        "gate_status",
        "host",
        "labels",
        "models",
        "probabilities",
        "risk_raw",
        "snapshot",
        "verdict",
    }
)
# An identifier, never prose: no spaces and short.
_TOKEN = re.compile(r"^[A-Za-z0-9_.:/@+-]{0,64}$")


def _text_places(per_example: Mapping[str, Any], limit: int = 5) -> List[str]:
    """Where per_example carries something other than ids, labels and scores."""
    places: List[str] = []

    def walk(value: Any, where: str) -> None:
        if len(places) >= limit:
            return
        if isinstance(value, str):
            if not _TOKEN.fullmatch(value):
                places.append(where)
        elif isinstance(value, Mapping):
            for key, item in value.items():
                if not isinstance(key, str) or not _TOKEN.fullmatch(key):
                    places.append(f"{where} (a key)")
                    return
                walk(item, f"{where}.{key}")
        elif isinstance(value, (list, tuple)):
            for index, item in enumerate(value):
                walk(item, f"{where}[{index}]")

    for system, records in per_example.items():
        for index, record in enumerate(records if isinstance(records, list) else []):
            where = f"{system}[{index}]"
            if not isinstance(record, Mapping):
                places.append(where)
                continue
            for key in sorted(set(record) - _RECORD_KEYS):
                places.append(f"{where}.{key}")
            details = record.get("details") or {}
            if isinstance(details, Mapping):
                for key in sorted(set(details) - _DETAIL_KEYS):
                    places.append(f"{where}.details.{key}")
            for key in _RECORD_KEYS:
                if key in record:
                    walk(record[key], f"{where}.{key}")
            if len(places) >= limit:
                return places[:limit]
    return places[:limit]


def _records_of(run: Mapping[str, Any]) -> Dict[str, List[JudgeRecord]]:
    per_example = run.get("per_example") or {}
    return {
        str(system): [JudgeRecord.from_json(record) for record in records]
        for system, records in per_example.items()
        if isinstance(records, list)
    }


def run_snapshots(run: Mapping[str, Any]) -> Set[str]:
    """
    Every decision-model snapshot a run's results came from: each system's
    decision_model, and each gate result (a system's own snapshot is only the
    most common one, so a run that straddled a roll would hide it; D204).
    """
    document = run.get("benchmark_run") if "benchmark_run" in run else run
    snapshots: Set[str] = set()
    for system in (document or {}).get("systems", []) or []:
        model = system.get("decision_model") if isinstance(system, Mapping) else None
        if isinstance(model, Mapping) and model.get("snapshot"):
            snapshots.add(str(model["snapshot"]))
    for records in (run.get("per_example") or {}).values():
        for record in records if isinstance(records, list) else []:
            details = record.get("details") if isinstance(record, Mapping) else None
            if isinstance(details, Mapping) and details.get("snapshot"):
                snapshots.add(str(details["snapshot"]))
    return snapshots


# Schema error paths a rule already explains (reported once, under the rule's code).
_EXPLAINED = re.compile(
    r"^(measured|mock|dirty|quality/failure_rate|vendor/(decision|judge)_host"
    r"|systems/\d+/cost/cost_source)(:|/|$)"
)


def document_problems(document: Mapping[str, Any]) -> List[Problem]:
    """
    The publish rules on a benchmark-run document alone: contracts/validate.mjs
    publishProblems, which checks every published run and the fixtures
    contracts/fixtures/bench/run.refused.<code>.json.
    """
    problems: List[Problem] = []
    systems = [s for s in document.get("systems", []) or [] if isinstance(s, Mapping)]
    if document.get("mock") is not False or document.get("measured") is not True:
        problems.append(Problem("mock", "a mock run is simulated, never measured"))
    missing = [
        key for key in ("vendor", "inputs", "quality", "dirty", "latency") if key not in document
    ]
    if missing:
        problems.append(
            Problem(
                "missing_fields",
                f"a published run records {', '.join(missing)} (run it again with this harness)",
            )
        )
    allowed = vendor_hosts()
    vendor = document.get("vendor")
    if isinstance(vendor, Mapping):
        needs = {
            "decision_host": any(system.get("decision_model") for system in systems),
            "judge_host": any(system.get("judge") for system in systems),
        }
        for name, needed in needs.items():
            host = vendor.get(name)
            if (host is None and needed) or (host is not None and host not in allowed):
                problems.append(
                    Problem(
                        "non_vendor_host",
                        f"vendor.{name} {host} is not a vendor host ({', '.join(allowed)}): "
                        "the run is simulated",
                    )
                )
    if "dirty" in document and document.get("dirty") is not False:
        problems.append(
            Problem(
                "dirty",
                "the run started on a tree with uncommitted tracked changes: commit, then run again",
            )
        )
    quality = document.get("quality")
    if isinstance(quality, Mapping):
        n = int(quality.get("n") or 0)
        failed = int(quality.get("failed") or 0)
        scored = int(quality.get("scored") or 0)
        rate = float(quality.get("failure_rate") or 0.0)
        if rate > MAX_FAILURE_RATE or (n > 0 and failed / n > MAX_FAILURE_RATE):
            problems.append(Problem("failure_rate", f"{failed} of {n} failed (more than 5%)"))
        if scored < MIN_SCORED_FRACTION * n:
            problems.append(Problem("scored", f"{scored} of {n} scored (fewer than 95%)"))
    snapshots = sorted(
        {
            str((system.get("decision_model") or {}).get("snapshot"))
            for system in systems
            if (system.get("decision_model") or {}).get("snapshot")
        }
    )
    if len(snapshots) > 1:
        problems.append(_mixed(snapshots))
    for system in systems:
        if (system.get("cost") or {}).get("cost_source") == UNKNOWN:
            problems.append(
                Problem(
                    "cost_source_unknown",
                    f"system {system.get('id')} has cost_source unknown (a call's cost was not "
                    "known)",
                )
            )
    return problems


def _mixed(snapshots: Sequence[str]) -> Problem:
    return Problem(
        "mixed_snapshot",
        f"the run used {len(snapshots)} decision-model snapshots ({', '.join(snapshots)})",
    )


def publish_problems(run: Mapping[str, Any]) -> List[Problem]:
    """
    Every reason bench publish refuses a local run (run.json), empty when it
    may be published: the document's rules (`document_problems`), then what
    only the run file shows. Its label and per-example records (mock output,
    the hosts and snapshots of each result, dataset text) and the rest of the
    schema. The codes are contracts/fixtures/bench/run.refused.<code>.
    """
    document = run.get("benchmark_run")
    if not isinstance(document, Mapping):
        return [Problem("schema", "benchmark_run is missing")]
    try:
        by_system = _records_of(run)
    except (KeyError, TypeError, ValueError) as exc:
        return [Problem("schema", f"per_example is unreadable: {exc}")]
    records = [record for records in by_system.values() for record in records]
    systems = [s for s in document.get("systems", []) or [] if isinstance(s, Mapping)]

    problems = [problem for problem in document_problems(document) if problem.code != "mock"]
    mock: Optional[str] = None
    if not records:
        mock = "no per-example records: nothing shows the run is not mock output"
    elif (
        run.get("label") != LABEL_MEASURED
        or document.get("measured") is not True
        or document.get("mock") is not False
    ):
        reasons = run.get("simulated_reasons") or []
        why = f" ({', '.join(str(reason) for reason in reasons)})" if reasons else ""
        mock = (
            f"the run is labelled {run.get('label')!r}{why}: simulated output is never "
            "published as measured"
        )
    elif is_simulated(records) or any(
        (system.get("cost") or {}).get("cost_source") == SIMULATED for system in systems
    ):
        mock = "some results came from the mock decision model or judge"
    if mock is not None:
        problems.insert(0, Problem("mock", mock))

    stray = sorted(record_hosts(by_system) - set(vendor_hosts()))
    if stray and not any(problem.code == "non_vendor_host" for problem in problems):
        problems.append(
            Problem("non_vendor_host", f"results came from {', '.join(stray)}, not a vendor host")
        )
    unhosted = unhosted_results(by_system)
    if unhosted and not any(problem.code == "non_vendor_host" for problem in problems):
        problems.append(
            Problem(
                "non_vendor_host",
                f"{unhosted} scored results name no host, so nothing shows they came from a "
                "vendor (run it again with this harness and a fresh gate-eval build)",
            )
        )
    snapshots = sorted(run_snapshots(run))
    if len(snapshots) > 1 and not any(problem.code == "mixed_snapshot" for problem in problems):
        problems.append(_mixed(snapshots))
    places = _text_places(run.get("per_example") or {})
    if places:
        problems.append(
            Problem(
                "text",
                "per_example must hold ids, labels and scores only (the passages are MS MARCO, "
                f"research-only); found more at {', '.join(places)}",
            )
        )
    try:
        check_run_id(str(document.get("run_id")))
    except ValueError as exc:
        problems.append(Problem("schema", str(exc)))
    unexplained = [
        error for error in schema_errors(document, BENCHMARK_SCHEMA) if not _EXPLAINED.match(error)
    ]
    if unexplained:
        problems.append(Problem("schema", "benchmark-run.v1: " + "; ".join(unexplained[:5])))
    return problems


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


def load_load_runs(load_dir: Path) -> List[Dict[str, Any]]:
    """The published gateway load runs in load_dir, each checked against load-run.v1."""
    out: List[Dict[str, Any]] = []
    if not load_dir.is_dir():
        return out
    for path in sorted(load_dir.glob("*.json")):
        with open(path, encoding="utf-8") as f:
            run = json.load(f)
        errors = schema_errors(run, LOAD_RUN_SCHEMA)
        if errors:
            raise BenchmarkSchemaError(f"load run {path.name}", errors)
        out.append(run)
    return out


def is_clean_load_run(run: Mapping[str, Any]) -> bool:
    """
    A clean load run: published from a clean tree, every target met, and no
    targeted scenario published report-only (scripts/load/report.py is_clean).
    """
    if run.get("schema") != LOAD_RUN_ID or run.get("dirty") is not False:
        return False
    return all(
        scenario.get("ok") and (scenario.get("target") is None or not scenario.get("report_only"))
        for scenario in run.get("scenarios", [])
    )


def _proxy_scenario(run: Mapping[str, Any]) -> Optional[Mapping[str, Any]]:
    for scenario in run.get("scenarios", []):
        if isinstance(scenario, dict) and scenario.get("id") == PROXY_SCENARIO:
            found: Mapping[str, Any] = scenario
            return found
    return None


def _latest_load(load_runs: Sequence[Mapping[str, Any]]) -> Optional[Mapping[str, Any]]:
    candidates = [
        run for run in load_runs if is_clean_load_run(run) and _proxy_scenario(run) is not None
    ]
    if not candidates:
        return None
    return max(candidates, key=lambda run: (run["created_at"], run["run_id"]))


def _gate_scored(per_example: Mapping[str, Any]) -> Tuple[List[bool], List[bool], List[float]]:
    """(labels, predictions, scores) of a run's scored gate examples."""
    labels: List[bool] = []
    predictions: List[bool] = []
    scores: List[float] = []
    for raw in per_example.get(GATE) or []:
        record = JudgeRecord.from_json(raw)
        if _is_scored(record):
            labels.append(record.label)
            predictions.append(bool(record.predicted))
            scores.append(float(record.score or 0.0))
    return labels, predictions, scores


def accuracy_intervals(
    per_example: Mapping[str, Any], accuracy: Optional[float], agreement: Optional[float]
) -> Dict[str, Dict[str, Any]]:
    """
    D212: plain accuracy with its Wilson interval, balanced accuracy with a
    seeded percentile bootstrap (2,000 resamples, seed 13), each with n per
    class, from a run's scored gate examples. A bootstrap interval that would
    not contain its own point estimate (possible at the edges) is widened to it.
    """
    labels, predictions, scores = _gate_scored(per_example)
    n_pos = sum(labels)
    counts = {"n_pos": n_pos, "n_neg": len(labels) - n_pos}
    out: Dict[str, Dict[str, Any]] = {}
    correct = sum(1 for label, pred in zip(labels, predictions) if label == pred)
    wilson = wilson_interval(correct, len(labels))
    if wilson is not None and accuracy is not None:
        out[ACCURACY_KEY] = {
            "lo": round(wilson[0], METRIC_DECIMALS),
            "hi": round(wilson[1], METRIC_DECIMALS),
            "method": "wilson",
            **counts,
        }
    if agreement is not None:
        ci = bootstrap_ci(
            labels,
            predictions,
            scores,
            n_resamples=PUBLISH_BOOTSTRAP_RESAMPLES,
            seed=PUBLISH_BOOTSTRAP_SEED,
            metrics=balanced_accuracy_only,
        ).get("balanced_accuracy")
        if ci is not None:
            out[AGREEMENT_KEY] = {
                "lo": min(round(ci[0], METRIC_DECIMALS), agreement),
                "hi": max(round(ci[1], METRIC_DECIMALS), agreement),
                "method": "bootstrap",
                "resamples": PUBLISH_BOOTSTRAP_RESAMPLES,
                "seed": PUBLISH_BOOTSTRAP_SEED,
                **counts,
            }
    return out


def gate_conditions_text(
    latency: Mapping[str, Any], host_env: Mapping[str, Any], n: int, snapshot: Optional[str]
) -> str:
    """
    The words written after a measured gate latency (D205, D225): the connection, how many gate
    calls ran at once, where the run really ran (inputs.host_env: platform and region, never an
    assumed machine), the snapshot and n. Every surface shows this sentence as written here
    (measured.json sources.gate_latency.conditions, the latency claims' {gate_conditions}).
    """
    connection = (
        "warm connection"
        if latency.get("connection") == "warm"
        else "cold connection (each call set up its own)"
    )
    at_once = int(latency.get("gate_concurrency") or 1)
    concurrency = "one request at a time" if at_once == 1 else f"{at_once} requests at a time"
    where = f"run on {host_env.get('platform')}, region {host_env.get('region')}"
    parts = [connection, concurrency, where]
    if snapshot:
        parts.append(f"snapshot {snapshot}")
    parts.append(f"n={n}")
    return ", ".join(parts)


def gate_latency_source(run: Mapping[str, Any]) -> Optional[Dict[str, Any]]:
    """
    measured.json sources.gate_latency (D205) from the bench run that gave the gate latency, or
    None when the run does not record its latency block, host_env or the gate's scored count.
    """
    latency = run.get("latency")
    host_env = (run.get("inputs") or {}).get("host_env")
    gate_counts = ((run.get("quality") or {}).get("by_system") or {}).get(GATE) or {}
    n = gate_counts.get("scored")
    if not isinstance(latency, Mapping) or not isinstance(host_env, Mapping):
        return None
    if not isinstance(n, int) or isinstance(n, bool) or n < 1:
        return None
    if latency.get("connection") not in ("warm", "cold") or not host_env.get("platform"):
        return None
    snapshot = ((_system(run, GATE) or {}).get("decision_model") or {}).get("snapshot")
    return {
        "connection": latency["connection"],
        "gate_concurrency": int(latency.get("gate_concurrency") or 1),
        "platform": str(host_env["platform"]),
        "region": str(host_env.get("region") or "local"),
        "n": n,
        "conditions": gate_conditions_text(latency, host_env, n, snapshot),
    }


def dataset_text(dataset: Mapping[str, Any]) -> str:
    """A run's dataset as the cost conditions name it: "RAGTruth-QA test split"."""
    name = str(dataset.get("name") or "an unnamed dataset")
    subset = str(dataset.get("subset") or "")
    if subset:
        name = f"{name}-{subset.upper()}" if len(subset) <= 3 else f"{name} {subset}"
    split = str(dataset.get("split") or "")
    return f"{name} {split} split" if split else name


def cost_conditions_text(
    dataset: str, n: int, tokens: Optional[int], judge_model: Optional[str]
) -> str:
    """
    The words written after a measured cost per 1,000 answers (D229): the dataset its answers
    came from, how many, the mean input tokens each paid for (a cost per answer grows with the
    documents' length) and, for an LLM system, the judge model. Every surface shows this sentence
    as written here (measured.json sources.<key>.conditions, the cost claims' {cost_conditions}).
    """
    parts = [f"{dataset}, n={n}"]
    if tokens:
        parts.append(f"about {tokens:,} input tokens per answer")
    if judge_model:
        parts.append(f"judge {judge_model}")
    return ", ".join(parts)


def cost_source_entry(run: Mapping[str, Any], system: str) -> Dict[str, Any]:
    """
    measured.json sources.<key> of a cost value (D204, D229) from the bench run that measured it:
    its cost source, and the conditions a reader needs to weigh the figure (the dataset, the
    examples it was divided over, the input tokens per answer and the judge model).
    """
    entry = _system(run, system) or {}
    cost = entry.get("cost") or {}
    out: Dict[str, Any] = {"cost_source": cost.get("cost_source")}
    counts = ((run.get("quality") or {}).get("by_system") or {}).get(system) or {}
    n = counts.get("n")
    if isinstance(n, int) and not isinstance(n, bool):
        excluded = counts.get("excluded")
        n -= excluded if isinstance(excluded, int) and not isinstance(excluded, bool) else 0
    else:
        n = (run.get("dataset") or {}).get("n")
    if not isinstance(n, int) or isinstance(n, bool) or n < 1:
        return out
    tokens = cost.get("input_tokens_per_answer")
    tokens = (
        tokens if isinstance(tokens, int) and not isinstance(tokens, bool) and tokens > 0 else None
    )
    judge = (entry.get("judge") or {}).get("model") or None
    dataset = dataset_text(run.get("dataset") or {})
    out.update({"dataset": dataset, "n": n})
    if tokens is not None:
        out["input_tokens_per_answer"] = tokens
    if judge:
        out["model"] = str(judge)
    out["conditions"] = cost_conditions_text(dataset, n, tokens, str(judge) if judge else None)
    return out


def derive_measured(
    runs: Sequence[Mapping[str, Any]],
    previous: Mapping[str, Any],
    generated_at: str,
    load_runs: Sequence[Mapping[str, Any]] = (),
    *,
    current: Optional[Mapping[str, Any]] = None,
    holdout: Optional[Mapping[str, Any]] = None,
    attempts: Optional[Mapping[str, Any]] = None,
    snapshot_split: bool = False,
) -> Dict[str, Any]:
    """
    contracts/claims/measured.json from the published runs: each value from
    the newest run that measures it, and the proxy overhead from the newest
    clean load run (its scenario c). Values no run provides keep their
    previous value, run id, interval and source. The tier is the benchmark
    runs' (a load run has none).

    `current` is the local run being published (run.json, with per_example):
    the accuracy intervals are computed from its examples when it provides
    the accuracy values. `holdout` is a checked calibrate --holdout report of
    that run (D206): it gives the ECE before and after calibration, held out.
    `attempts` is sources.attempts (D204). The snapshot is every snapshot the
    values come from (D207): two only with `snapshot_split`.
    """
    previous_values = previous.get("values") or {}
    values: Dict[str, Dict[str, Any]] = {key: dict(entry) for key, entry in previous_values.items()}
    used: Dict[str, Mapping[str, Any]] = {}
    from_run: Dict[str, Mapping[str, Any]] = {}  # key -> the benchmark run its value came from

    def put(key: str, run: Optional[Mapping[str, Any]], value: Optional[float]) -> None:
        if key not in values or run is None or value is None:
            return
        values[key]["value"] = value
        values[key]["run_id"] = run["run_id"]
        used[str(run["run_id"])] = run
        from_run[key] = run

    def metric(run: Optional[Mapping[str, Any]], system: str, *path: str) -> Optional[float]:
        node: Any = _system(run, system) if run is not None else None
        for part in path:
            node = node.get(part) if isinstance(node, Mapping) else None
        return float(node) if isinstance(node, (int, float)) else None

    gate_run = _latest(runs, needs=["gate"])
    put("fast_check_usd_per_1k", gate_run, metric(gate_run, "gate", "cost", "usd_per_1k"))
    put("gate_latency_ms_p50", gate_run, metric(gate_run, "gate", "latency_ms", "p50"))
    put("gate_latency_ms_p95", gate_run, metric(gate_run, "gate", "latency_ms", "p95"))
    if holdout is None:
        put(ECE_BEFORE_KEY, gate_run, metric(gate_run, "gate", "metrics", "ece"))
    llm_run = _latest(runs, needs=["llm_same_checks"])
    put(
        "llm_same_checks_usd_per_1k",
        llm_run,
        metric(llm_run, "llm_same_checks", "cost", "usd_per_1k"),
    )
    audit_run = _latest(runs, needs=["llm_full_audit"])
    put(
        "llm_full_audit_usd_per_1k",
        audit_run,
        metric(audit_run, "llm_full_audit", "cost", "usd_per_1k"),
    )
    pair = _latest(runs, needs=["gate", "llm_same_checks"])
    fast = metric(pair, "gate", "cost", "usd_per_1k")
    same = metric(pair, "llm_same_checks", "cost", "usd_per_1k")
    put(
        _RATIO_KEY, pair, round(same / fast, METRIC_DECIMALS) if fast and same is not None else None
    )
    ragtruth = _latest(runs, needs=["gate"], dataset=("RAGTruth", "qa"))
    put(AGREEMENT_KEY, ragtruth, metric(ragtruth, "gate", "metrics", "balanced_accuracy"))
    put(ACCURACY_KEY, ragtruth, metric(ragtruth, "gate", "metrics", "accuracy"))

    current_doc = (current or {}).get("benchmark_run") or {}
    if holdout is not None and current_doc:
        put(ECE_BEFORE_KEY, current_doc, _number(holdout, "before", "ece"))
        put(ECE_AFTER_KEY, current_doc, _number(holdout, "after", "ece"))

    load_run = _latest_load(load_runs)
    if load_run is not None:
        proxy = _proxy_scenario(load_run) or {}
        for key, stat in (("proxy_overhead_ms_p50", "p50_ms"), ("proxy_overhead_ms_p95", "p95_ms")):
            value = proxy.get(stat)
            if key in values and isinstance(value, (int, float)):
                values[key]["value"] = float(value)
                values[key]["run_id"] = load_run["run_id"]

    # Intervals: kept while their value is unchanged, computed for the run being published.
    intervals: Dict[str, Dict[str, Any]] = {
        key: dict(entry)
        for key, entry in (previous.get("intervals") or {}).items()
        if key in values
        and key not in from_run
        and values[key].get("run_id") == (previous_values.get(key) or {}).get("run_id")
    }
    current_id = current_doc.get("run_id")
    if current is not None and current_id is not None:
        fresh = accuracy_intervals(
            current.get("per_example") or {},
            values.get(ACCURACY_KEY, {}).get("value"),
            values.get(AGREEMENT_KEY, {}).get("value"),
        )
        for key, interval in fresh.items():
            if values.get(key, {}).get("run_id") == current_id:
                intervals[key] = interval

    sources: Dict[str, Any] = dict(previous.get("sources") or {})
    for key, system in COST_KEYS.items():
        run = from_run.get(key)
        if run is not None:
            sources[key] = cost_source_entry(run, system)
        elif values.get(key, {}).get("value") is None:
            sources.pop(key, None)
    latency_run = next((from_run[key] for key in LATENCY_KEYS if key in from_run), None)
    if latency_run is not None:
        conditions = gate_latency_source(latency_run)
        if conditions is not None:
            sources["gate_latency"] = conditions
        else:
            sources.pop("gate_latency", None)
    elif all(values.get(key, {}).get("value") is None for key in LATENCY_KEYS):
        sources.pop("gate_latency", None)
    if holdout is not None:
        sources["groundedness_ece"] = {
            "method": "holdout",
            "calibration_file": holdout.get("calibration_file"),
            "calibration_sha256": holdout.get("calibration_sha256"),
            "dataset": ", ".join(holdout.get("calibration_datasets") or []) or "unnamed",
        }
    if attempts is not None:
        sources["attempts"] = dict(attempts)
    if snapshot_split:
        sources["snapshot_split"] = True
    else:
        sources.pop("snapshot_split", None)

    run_ids = sorted(
        {
            str(entry["run_id"])
            for entry in values.values()
            if entry.get("value") is not None and entry.get("run_id")
        }
    )
    # Measured means a benchmark run measured something: a load run alone (system proxy) is no
    # benchmark, and has no tier.
    measured = any(
        entry.get("value") is not None and entry.get("run_id") and entry.get("system") != "proxy"
        for entry in values.values()
    )
    snapshots: Set[str] = set()
    for run in used.values():
        model = (_system(run, GATE) or {}).get("decision_model") or {}
        if model.get("snapshot"):
            snapshots.add(str(model["snapshot"]))
    if holdout is not None and holdout.get("snapshot"):
        snapshots.add(str(holdout["snapshot"]))
    previous_snapshot = previous.get("snapshot")
    if not snapshots and previous_snapshot:
        snapshots = set(
            previous_snapshot if isinstance(previous_snapshot, list) else [previous_snapshot]
        )
    snapshot: Any = None
    if snapshots:
        ordered = sorted(snapshots)
        snapshot = ordered[0] if len(ordered) == 1 else ordered

    tiers = {str(run["tier"]) for run in used.values()}
    tier = previous.get("tier") if not tiers else ("lite" if "lite" in tiers else "full")
    derived: Dict[str, Any] = {
        "schema": "trustagent.measured/v1",
        "status": "measured" if measured else "not_measured",
        "tier": tier if measured else None,
        "mock": False,
        "generated_by": GENERATED_BY if measured else None,
        "generated_at": generated_at if measured else None,
        "runs": run_ids,
        "values": values,
    }
    if measured:
        derived["snapshot"] = snapshot
        if intervals:
            derived["intervals"] = intervals
        if sources:
            derived["sources"] = sources
    return derived


def _number(document: Mapping[str, Any], *path: str) -> Optional[float]:
    node: Any = document
    for part in path:
        node = node.get(part) if isinstance(node, Mapping) else None
    return float(node) if isinstance(node, (int, float)) and not isinstance(node, bool) else None


# ---------------------------------------------------------------------------
# measured.json rules (D204, D206, D207, D212): contracts/validate.mjs measuredProblems
# ---------------------------------------------------------------------------


def measured_problems(measured: Mapping[str, Any]) -> List[Problem]:
    """
    The rules measured.json keeps beyond its schema. The codes are
    contracts/fixtures/bench/measured.refused.<code>.
    """
    out: List[Problem] = []
    values: Mapping[str, Any] = measured.get("values") or {}
    runs = list(measured.get("runs") or [])
    sources: Mapping[str, Any] = measured.get("sources") or {}
    if measured.get("status") == "not_measured":
        if (
            any(v.get("value") is not None or v.get("run_id") is not None for v in values.values())
            or runs
        ):
            out.append(
                Problem(
                    "not_measured",
                    "status not_measured requires every value and run_id to be null and runs empty",
                )
            )
        if measured.get("snapshot") is not None:
            out.append(Problem("snapshot", "status not_measured has no snapshot"))
        return out
    for key, entry in values.items():
        if (
            entry.get("value") is not None
            and entry.get("run_id") is not None
            and entry["run_id"] not in runs
        ):
            out.append(Problem("run_id", f"values.{key}.run_id {entry['run_id']} is not in runs"))
    snapshot = measured.get("snapshot")
    if snapshot is None:
        out.append(
            Problem(
                "snapshot", "a measured file names the decision-model snapshot of its values (D207)"
            )
        )
    split = sources.get("snapshot_split") is True
    if isinstance(snapshot, list) != split:
        out.append(
            Problem(
                "snapshot_split",
                "two snapshots only with sources.snapshot_split true, and then both are named (D207)"
                + (
                    f": the values come from {', '.join(snapshot)}"
                    if isinstance(snapshot, list)
                    else ""
                ),
            )
        )
    if not sources.get("attempts"):
        out.append(
            Problem(
                "attempts", "sources.attempts discloses every bench run and the refused ones (D204)"
            )
        )
    for key, entry in values.items():
        if (
            entry.get("value") is None
            or entry.get("unit") != "usd_per_1k"
            or entry.get("system") == "proxy"
        ):
            continue
        source = (sources.get(key) or {}).get("cost_source")
        if source is None:
            out.append(
                Problem(
                    "cost_source_missing",
                    f"values.{key} needs sources.{key}.cost_source (reported or computed)",
                )
            )
            continue
        if source not in ("reported", "computed"):
            out.append(
                Problem(
                    "cost_source_unknown", f"sources.{key}.cost_source {source} is refused (D204)"
                )
            )
        if not sources[key].get("conditions"):
            out.append(
                Problem(
                    "cost_conditions",
                    f"values.{key} needs sources.{key}.conditions: the dataset, n and the input "
                    "tokens per answer (D229)",
                )
            )
    if (values.get(ECE_AFTER_KEY) or {}).get("value") is not None and (
        (sources.get("groundedness_ece") or {}).get("method") != "holdout"
    ):
        out.append(
            Problem(
                "ece_source",
                "groundedness_ece_after needs sources.groundedness_ece, held out (D206)",
            )
        )
    published_latency = [
        key for key in LATENCY_KEYS if (values.get(key) or {}).get("value") is not None
    ]
    if published_latency and not (sources.get("gate_latency") or {}).get("conditions"):
        out.append(
            Problem(
                "latency_conditions",
                f"{', '.join(published_latency)} needs sources.gate_latency: the connection, "
                "the requests at a time, where it ran, the snapshot and n (D205)",
            )
        )
    for key, interval in (measured.get("intervals") or {}).items():
        entry = values.get(key)
        if not entry or entry.get("unit") != "fraction":
            out.append(Problem("intervals", f"intervals.{key} is not a fraction in values"))
        elif entry.get("value") is not None and not (
            interval["lo"] <= entry["value"] <= interval["hi"]
        ):
            out.append(Problem("intervals", f"intervals.{key} does not contain its value"))
    return out


# ---------------------------------------------------------------------------
# The held-out calibration check (D206) and the attempts (D204)
# ---------------------------------------------------------------------------


def exportable_labels(run: Mapping[str, Any], evaluator: str) -> int:
    """How many of a run's gate results carry a raw risk and a label for `evaluator`."""
    count = 0
    for raw in (run.get("per_example") or {}).get(GATE) or []:
        details = raw.get("details") or {}
        risk = (details.get("risk_raw") or {}).get(evaluator)
        label = (details.get("labels") or {}).get(evaluator)
        if raw.get("status") == OK and isinstance(risk, (int, float)) and isinstance(label, bool):
            count += 1
    return count


def read_holdout(
    path: Path, run: Mapping[str, Any], *, root: Optional[Path] = None
) -> Tuple[Optional[Dict[str, Any]], List[Problem]]:
    """
    A calibrate --holdout report of this run (make calib-holdout), checked
    before it is folded: the groundedness evaluator, this run's snapshot and
    labels, vendor hosts only, a measured calibration, and the calibration
    file unchanged since (its sha256). Returns (report or None, problems).
    """
    try:
        with open(path, encoding="utf-8") as f:
            report = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        return None, [Problem("holdout", f"cannot read {path}: {exc}")]
    if not isinstance(report, dict) or report.get("schema") != HOLDOUT_SCHEMA:
        return None, [Problem("holdout", f"{path} is not a {HOLDOUT_SCHEMA} report")]
    problems: List[Problem] = []
    if report.get("evaluator") != HOLDOUT_EVALUATOR:
        problems.append(
            Problem(
                "holdout", f"the report checks {report.get('evaluator')}, not {HOLDOUT_EVALUATOR}"
            )
        )
    snapshots = sorted(run_snapshots(run))
    if snapshots != [report.get("snapshot")]:
        problems.append(
            Problem(
                "holdout",
                f"the calibration is for {report.get('snapshot')}, the run used "
                f"{', '.join(snapshots) or 'no snapshot'} (D207: one snapshot)",
            )
        )
    if report.get("non_vendor_hosts"):
        problems.append(
            Problem(
                "holdout",
                f"the held-out risks came from {', '.join(report['non_vendor_hosts'])}, not a vendor host",
            )
        )
    if report.get("calibration_quality") != "measured":
        problems.append(
            Problem(
                "holdout",
                f"the calibration's quality is {report.get('calibration_quality')}, not measured (D206: "
                "re-run calib-pass with a larger CALIB_N, or publish without --calibration-holdout)",
            )
        )
    for side in ("before", "after"):
        if _number(report, side, "ece") is None:
            problems.append(Problem("holdout", f"the report has no {side}.ece"))
    expected = exportable_labels(run, HOLDOUT_EVALUATOR)
    if (report.get("holdout") or {}).get("n") != expected:
        problems.append(
            Problem(
                "holdout",
                f"the report holds {(report.get('holdout') or {}).get('n')} labels, this run "
                f"exports {expected}: it was not made from this run (make calib-holdout RUN=...)",
            )
        )
    calibration_file = str(report.get("calibration_file") or "")
    if not CALIBRATION_FILE.fullmatch(calibration_file):
        problems.append(
            Problem(
                "holdout",
                f"calibration_file {calibration_file!r} is not config/calibration/<snapshot>.json",
            )
        )
    else:
        base = root or repo_root()
        target = (base / calibration_file) if base is not None else None
        if target is None or not target.is_file():
            problems.append(Problem("holdout", f"{calibration_file} does not exist"))
        else:
            digest = hashlib.sha256(target.read_bytes()).hexdigest()
            if digest != report.get("calibration_sha256"):
                problems.append(
                    Problem(
                        "holdout",
                        f"{calibration_file} changed since the holdout check: run make calib-holdout again",
                    )
                )
    return report, problems


def unpassed_holdout(
    run_file: Path, run: Mapping[str, Any], *, root: Optional[Path] = None
) -> List[Problem]:
    """
    D206: the held-out check make calib-holdout wrote next to the run, when publish would accept
    it but it was not passed. Publishing without it would leave the ECE before and after
    unpublished with nothing to notice, so it is refused. A report publish would refuse (a
    calibration that is not measured, another snapshot) is no reason to stop: publishing without
    the held-out ECE is D206's own path then.
    """
    candidate = run_file.parent / HOLDOUT_REPORT_NAME
    if not candidate.is_file():
        return []
    report, problems = read_holdout(candidate, run, root=root)
    if report is None or problems:
        return []
    return [
        Problem(
            "holdout_not_passed",
            f"make calib-holdout checked this run ({candidate}) but it was not passed: add "
            f"HOLDOUT={candidate} (bench publish --calibration-holdout), or the held-out ECE "
            "before and after calibration goes unpublished (D206)",
        )
    ]


def count_attempts(
    results_dir: Path, tier: str, current: Mapping[str, Any], previous: Mapping[str, Any]
) -> Dict[str, Any]:
    """
    sources.attempts (D204): every local run of `tier` since P6 Step 0 in the
    directory the run was written to (the current one included), and the ones
    publish refuses; merged with what measured.json already disclosed.
    """
    seen: Dict[str, bool] = {}  # run_id -> refused
    candidates: List[Mapping[str, Any]] = []
    if results_dir.is_dir():
        for path in sorted(results_dir.glob("*/run.json")):
            try:
                with open(path, encoding="utf-8") as f:
                    candidates.append(json.load(f))
            except (OSError, json.JSONDecodeError):
                continue
    candidates.append(current)
    for run in candidates:
        document = run.get("benchmark_run") if isinstance(run, Mapping) else None
        if not isinstance(document, Mapping) or document.get("tier") != tier:
            continue
        if str(document.get("created_at") or "") < ATTEMPTS_SINCE:
            continue
        run_id = str(document.get("run_id"))
        if not _RUN_ID.fullmatch(run_id) or run_id in seen:
            continue
        seen[run_id] = bool(publish_problems(run))
    earlier = (previous.get("sources") or {}).get("attempts") or {}
    refused = {run_id for run_id, was_refused in seen.items() if was_refused}
    refused |= {str(run_id) for run_id in earlier.get("refused_run_ids") or []}
    return {
        "bench_runs": max(len(seen), int(earlier.get("bench_runs") or 0)),
        "refused_run_ids": sorted(refused),
    }


def benchmark_paths() -> Tuple[Path, Path]:
    """(docs/benchmarks/results, docs/benchmarks/index.json) of this repository."""
    root = repo_root()
    if root is None:
        raise FileNotFoundError("repository root not found (no VERSION file above evals/)")
    return root / "docs" / "benchmarks" / "results", root / "docs" / "benchmarks" / "index.json"


# Run refusals after which the derived measured.json is not checked (it would only echo them).
_FATAL = frozenset({"mock", "non_vendor_host", "schema", "missing_fields"})
# Refusals that make a run simulated (D203): --check-only writes none of its would-be files.
_SIMULATED_CODES = frozenset({"mock", "non_vendor_host"})


@dataclass
class Prepared:
    """A publish, checked and derived but not written."""

    run_id: str
    document: Dict[str, Any]
    runs: List[Mapping[str, Any]]
    derived: Dict[str, Any]
    problems: List[Problem]
    generated_at: str


def prepare_publish(
    run_file: Path,
    *,
    results_dir: Path,
    measured_path: Path,
    generated_at: Optional[str] = None,
    load_dir: Optional[Path] = None,
    calibration_holdout: Optional[Path] = None,
    snapshot_split: bool = False,
    attempts_dir: Optional[Path] = None,
    root: Optional[Path] = None,
) -> Prepared:
    """
    Check a local run and derive what publishing it would write; nothing is
    written. Every problem is collected (the run's publish rules, the
    held-out report's, and the derived measured.json's schema and rules).

    Raises:
        BenchmarkSchemaError: A published run or load run is not schema-valid
            (the published files themselves are broken).
    """
    now = generated_at or utc_now()
    with open(run_file, encoding="utf-8") as f:
        run = json.load(f)
    problems = publish_problems(run)
    document = run.get("benchmark_run") if isinstance(run.get("benchmark_run"), dict) else {}
    run_id = str(document.get("run_id"))

    holdout: Optional[Dict[str, Any]] = None
    if calibration_holdout is not None:
        report, holdout_problems = read_holdout(calibration_holdout, run, root=root)
        problems += holdout_problems
        holdout = report if not holdout_problems else None
    else:
        problems += unpassed_holdout(run_file, run, root=root)

    published = load_results(results_dir) if results_dir.is_dir() else []
    runs: List[Mapping[str, Any]] = [
        existing for existing in published if existing.get("run_id") != run_id
    ]
    with open(measured_path, encoding="utf-8") as f:
        previous = json.load(f)
    attempts = count_attempts(
        attempts_dir if attempts_dir is not None else run_file.resolve().parent.parent,
        str(document.get("tier")),
        run,
        previous,
    )
    derived: Dict[str, Any] = {}
    try:
        runs.append(document)
        load_runs = load_load_runs(load_dir or results_dir.parent / "load")
        derived = derive_measured(
            runs,
            previous,
            now,
            load_runs,
            current=run,
            holdout=holdout,
            attempts=attempts,
            snapshot_split=snapshot_split,
        )
    except (KeyError, TypeError, ValueError, AttributeError) as exc:
        if isinstance(exc, BenchmarkSchemaError):
            raise
        problems.append(Problem("schema", f"measured.json cannot be derived from this run: {exc}"))
    # A run refused as simulated or unreadable folds nothing worth checking: its measured.json
    # problems would only repeat the refusal.
    if derived and not any(problem.code in _FATAL for problem in problems):
        errors = schema_errors(derived, MEASURED_SCHEMA)
        if errors:
            problems.append(Problem("measured_schema", "measured.v1: " + "; ".join(errors[:5])))
        problems += measured_problems(derived)
    return Prepared(
        run_id=run_id,
        document=dict(document),
        runs=runs,
        derived=derived,
        problems=_unique(problems),
        generated_at=now,
    )


def _unique(problems: Sequence[Problem]) -> List[Problem]:
    seen: Set[Tuple[str, str]] = set()
    out: List[Problem] = []
    for problem in problems:
        if (problem.code, problem.message) not in seen:
            seen.add((problem.code, problem.message))
            out.append(problem)
    return out


def _default_paths(
    results_dir: Optional[Path], index_path: Optional[Path], measured_path: Optional[Path]
) -> Tuple[Path, Path, Path]:
    if results_dir is None or index_path is None:
        default_results, default_index = benchmark_paths()
        results_dir = results_dir or default_results
        index_path = index_path or default_index
    return results_dir, index_path, measured_path or contracts_dir() / MEASURED_CLAIMS


def publish(
    run_file: Path,
    *,
    results_dir: Optional[Path] = None,
    index_path: Optional[Path] = None,
    measured_path: Optional[Path] = None,
    generated_at: Optional[str] = None,
    load_dir: Optional[Path] = None,
    calibration_holdout: Optional[Path] = None,
    snapshot_split: bool = False,
    attempts_dir: Optional[Path] = None,
    root: Optional[Path] = None,
) -> Dict[str, Path]:
    """
    Publish a local measured run (make bench-publish; see the module docstring).

    Everything is checked before anything is written: a refused run leaves
    docs/benchmarks and measured.json as they were. The load runs are read
    from load_dir, by default docs/benchmarks/load beside the results.

    Raises:
        PublishRefused: Every reason the run is refused (D203, D204, D206, D207).
        BenchmarkSchemaError: A published run or load run is not schema-valid.
    """
    results, index, claims = _default_paths(results_dir, index_path, measured_path)
    prepared = prepare_publish(
        run_file,
        results_dir=results,
        measured_path=claims,
        generated_at=generated_at,
        load_dir=load_dir,
        calibration_holdout=calibration_holdout,
        snapshot_split=snapshot_split,
        attempts_dir=attempts_dir,
        root=root,
    )
    if prepared.problems:
        raise PublishRefused(prepared.run_id, prepared.problems)
    result_path = results / f"{prepared.run_id}.json"
    _write_json(result_path, prepared.document)
    _write_json(index, build_index(prepared.runs, prepared.generated_at))
    _write_json(claims, prepared.derived)
    return {"result": result_path, "index": index, "measured": claims}


def check_publish(
    run_file: Path,
    scratch_dir: Path,
    *,
    results_dir: Optional[Path] = None,
    measured_path: Optional[Path] = None,
    generated_at: Optional[str] = None,
    load_dir: Optional[Path] = None,
    calibration_holdout: Optional[Path] = None,
    snapshot_split: bool = False,
    attempts_dir: Optional[Path] = None,
    root: Optional[Path] = None,
) -> Tuple[List[Problem], Dict[str, Path]]:
    """
    bench publish --check-only (Gate B): every check, and the files a publish
    would write, written into `scratch_dir` only (with problems.json). The
    published results, index and measured.json are only read. A simulated run
    (mock output, a non-vendor host) gets problems.json alone: it can never be
    published, and a would-be measured.json of it would label simulated
    figures measured, even in scratch.
    """
    results, _, claims = _default_paths(results_dir, None, measured_path)
    prepared = prepare_publish(
        run_file,
        results_dir=results,
        measured_path=claims,
        generated_at=generated_at,
        load_dir=load_dir,
        calibration_holdout=calibration_holdout,
        snapshot_split=snapshot_split,
        attempts_dir=attempts_dir,
        root=root,
    )
    written = {
        "problems": scratch_dir / "problems.json",
        "result": scratch_dir / "results" / f"{prepared.run_id}.json",
        "index": scratch_dir / "index.json",
        "measured": scratch_dir / "measured.json",
    }
    _write_json(
        written["problems"],
        {
            "run_id": prepared.run_id,
            "publishable": not prepared.problems,
            "problems": [
                {"code": problem.code, "message": problem.message} for problem in prepared.problems
            ],
        },
    )
    simulated = any(problem.code in _SIMULATED_CODES for problem in prepared.problems)
    if prepared.document.get("run_id") is not None and not simulated:
        _write_json(written["result"], prepared.document)
        _write_json(written["index"], build_index(prepared.runs, prepared.generated_at))
    else:
        del written["result"], written["index"]
    if prepared.derived and not simulated:
        _write_json(written["measured"], prepared.derived)
    else:
        del written["measured"]
    return prepared.problems, written
