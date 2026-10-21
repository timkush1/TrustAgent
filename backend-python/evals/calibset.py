"""
The calibration set and pass (P6 M-6, M-7; D206, D207).

Fit on RAGTruth-QA **train**, measure on the bench's **test** sample:

    python -m evals.calibset prepare --split train --n 800 --revision <sha> \\
        --exclude-sources evals/datasets/ragtruth/bench-lite.jsonl --out calib.jsonl
    python -m evals.calibset run --gate-eval-bin ../backend-go/bin/gate-eval --n 800 \\
        --revision <sha> --exclude-sources ... --max-cost-usd 0.30 [--dry-run]
    python -m evals.calibset result --gate evals/calibration/<run_id>/gate.jsonl
    python -m evals.calibset holdout --run evals/results/<run_id>/run.json

`prepare` draws a **natural** (unstratified, seeded) sample of the QA train
split and writes it as gate-eval input, each example labelled for both
evaluators (`label_source: public_dataset`):

- `groundedness@1` fires on any hallucinated span (`label_hallucinated`);
- `contradiction@1` fires only on a conflict span: a baseless-only answer is
  unsupported, not contradicted, so it is 0; a clean answer is 0.

It refuses a sample that shares a RAGTruth `source_id` with the bench's test
sample (about six answers share a source, so a shared one would leak the
held-out passages into the fit). The splits share none (Step 0), so this is a
guard; a bench sample that is not prepared yet is prepared first (free).

`run` (make calib-pass, a dry run unless CONFIRM=1) prepares the sample, lowers
its cap to the key's remaining limit minus $0.10 (D201), and sends it through
**one** warm gate-eval process (`--policy builtin --warm --concurrency 1`,
TRUTHTABLE_POLICY_FILE removed) into evals/calibration/<run_id>/gate.jsonl
(gitignored). It prints the positives per evaluator, the commands that fit
the calibration, and one `TRUSTAGENT_P6_RESULT` line.

`holdout` (make calib-holdout RUN=..., free) exports the run's gate risks as
labels (`bench export-labels`) and runs `calibrate --holdout` for each
evaluator against config/calibration/<snapshot>.json, writing
holdout-<evaluator>.json next to the run for `bench publish
--calibration-holdout`.
"""

import argparse
import json
import os
import random
import shutil
import subprocess  # nosec B404
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Set

from truthtable.config import repo_root

from . import plan as planning
from . import vendor_key
from .bench import dataset_tag, export_labels, gate_eval_env
from .examples import (
    CONTRADICTION,
    GROUNDEDNESS,
    BenchExample,
    DatasetFormatError,
    evaluator_labels,
    read_jsonl,
)
from .datasets import ragtruth
from .datasets.fetch import sha256_file
from .report import run_snapshots, utc_now, vendor_hosts

EVALS_DIR = Path(__file__).parent
CALIBRATION_DIR = EVALS_DIR / "calibration"
EVALUATORS = (GROUNDEDNESS, CONTRADICTION)
LABEL_SOURCE = "public_dataset"
SAMPLING = "natural"
RESULT_SCHEMA = "trustagent.calib-pass/v1"
POLICY_FILE_ENV = "TRUTHTABLE_POLICY_FILE"

EXIT_OK = 0
EXIT_FAILED = 1
EXIT_PRECONDITION = 2
EXIT_OVER_BUDGET = 3

# The gate-eval input members (backend-go/cmd/gate-eval example.go); anything else is an error.
GATE_EVAL_MEMBERS = frozenset(
    {
        "id",
        "query",
        "user_request",
        "response",
        "answer",
        "system_instructions",
        "context",
        "evidence",
        "rules",
        "max_marker",
        "scenario",
        "labels",
        "label_hallucinated",
        "label_source",
        "domain",
        "meta",
    }
)


class CalibsetError(RuntimeError):
    """The calibration set cannot be made as asked; nothing was spent."""


# ---------------------------------------------------------------------------
# prepare
# ---------------------------------------------------------------------------


def natural_sample(examples: Sequence[BenchExample], n: int, seed: int) -> List[BenchExample]:
    """A seeded simple random sample (no strata): the label mix is the split's own."""
    ordered = sorted(examples, key=lambda example: example.id)
    if n >= len(ordered):
        return ordered
    # A reproducible draw for a public dataset sample, not for anything secret.
    rng = random.Random(seed)  # nosec B311
    return sorted(rng.sample(ordered, n), key=lambda example: example.id)


def gate_eval_line(example: BenchExample) -> Dict[str, Any]:
    """An example as gate-eval input, labelled for both evaluators (D206)."""
    return {
        "id": example.id,
        "query": example.query,
        "response": example.response,
        "evidence": [
            {"id": f"doc{index}", "source": example.context_source, "text": text}
            for index, text in enumerate(example.context, 1)
        ],
        "labels": evaluator_labels(example),
        "label_hallucinated": example.label_hallucinated,
        "label_source": LABEL_SOURCE,
        "meta": dict(example.meta),
    }


def source_ids(examples: Sequence[BenchExample]) -> Set[str]:
    return {example.meta.get("source_id", "") for example in examples} - {""}


def bench_source_ids(path: Path) -> Set[str]:
    """The source ids of a prepared bench sample (its examples' meta.source_id)."""
    rows = read_jsonl(path)
    ids = {str((row.get("meta") or {}).get("source_id") or "") for row in rows} - {""}
    if rows and not ids:
        raise CalibsetError(
            f"{path} has no source ids (prepared before P6): delete it and prepare it again"
        )
    return ids


@dataclass
class Prepared:
    """A prepared calibration set."""

    path: Path
    manifest: Dict[str, Any]
    examples: List[BenchExample] = field(default_factory=list)


def prepare(
    *,
    n: int,
    seed: int,
    revision: str,
    exclude_sources: Path,
    out: Path,
    split: str = "train",
    bench_n: int = 200,
    bench_seed: int = 13,
    source_dir: Optional[Path] = None,
    fetch: Any = None,
) -> Prepared:
    """
    Draw and write the calibration set (see the module docstring).

    Raises:
        CalibsetError: The sample shares a source with the bench sample, or
            the bench sample has no source ids.
    """
    if not exclude_sources.is_file():
        ragtruth.prepare(
            subset="qa",
            split="test",
            n=bench_n,
            seed=bench_seed,
            revision=revision,
            source_dir=source_dir,
            out=exclude_sources,
            fetch=fetch,
        )
    excluded = bench_source_ids(exclude_sources)

    if source_dir is None:
        hashes = ragtruth.fetch_sources(revision, ragtruth.RAW_DIR, fetch=fetch)
        files = ragtruth.RAW_DIR / revision.replace("/", "_")
    else:
        files = source_dir
        hashes = {
            name: sha256_file(source_dir / name)
            for name in (ragtruth.RESPONSE_FILE, ragtruth.SOURCE_FILE)
        }
    examples, counts = ragtruth.convert(
        read_jsonl(files / ragtruth.RESPONSE_FILE),
        read_jsonl(files / ragtruth.SOURCE_FILE),
        subset="qa",
        split=split,
    )
    if not examples:
        raise DatasetFormatError(f"no RAGTruth qa/{split} examples ({counts})")
    picked = natural_sample(examples, n, seed)
    shared = source_ids(picked) & excluded
    if shared:
        raise CalibsetError(
            f"refused: the calibration sample shares {len(shared)} RAGTruth source(s) with the "
            f"bench sample {exclude_sources.name} (for example {sorted(shared)[0]}): a held-out "
            "ECE would then be in-sample (D206)"
        )
    positives = {
        ref: sum(1 for example in picked if evaluator_labels(example).get(ref))
        for ref in EVALUATORS
    }
    tag = dataset_tag("RAGTruth", "qa", split, revision)
    manifest = {
        "dataset": tag,
        "name": ragtruth.NAME,
        "subset": "qa",
        "split": split,
        "n": len(picked),
        "revision": revision,
        "source_sha256": hashes,
        "seed": seed,
        "sampling": SAMPLING,
        "label_source": LABEL_SOURCE,
        "positives": positives,
        "sources": len(source_ids(picked)),
        "bench_sample": exclude_sources.name,
        "bench_sources": len(excluded),
        "shared_sources": 0,
        "eligible": counts.get("eligible", len(examples)),
    }
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8", newline="\n") as f:
        for example in picked:
            f.write(json.dumps(gate_eval_line(example), ensure_ascii=False, sort_keys=True) + "\n")
    with open(out.with_name(out.stem + ".manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")
    return Prepared(path=out, manifest=manifest, examples=picked)


# ---------------------------------------------------------------------------
# result: what one gate-eval pass gave
# ---------------------------------------------------------------------------


@dataclass
class PassSummary:
    """A calib-pass gate.jsonl, counted."""

    lines: int = 0
    evaluated: int = 0
    errors: int = 0
    over_budget: int = 0
    cost_usd: float = 0.0
    snapshots: Set[str] = field(default_factory=set)
    hosts: Set[str] = field(default_factory=set)
    backends: Set[str] = field(default_factory=set)
    connections: Set[str] = field(default_factory=set)
    # Per evaluator: assessed lines with a label, and how many of them should fire.
    assessed: Dict[str, int] = field(default_factory=dict)
    positives: Dict[str, int] = field(default_factory=dict)

    @property
    def snapshot(self) -> Optional[str]:
        return next(iter(self.snapshots)) if len(self.snapshots) == 1 else None

    @property
    def simulated(self) -> bool:
        """Mock, or a stand-in host (D203): such risks never make a measured calibration."""
        return "mock" in self.backends or bool(self.hosts - set(vendor_hosts()))


def summarize_pass(path: Path, evaluators: Sequence[str] = EVALUATORS) -> PassSummary:
    summary = PassSummary(
        assessed={ref: 0 for ref in evaluators}, positives={ref: 0 for ref in evaluators}
    )
    for line in read_jsonl(path):
        summary.lines += 1
        summary.cost_usd += float(line.get("cost_usd") or 0.0)
        if line.get("skipped"):
            summary.over_budget += 1
            continue
        decision = line.get("decision")
        if line.get("error") or not isinstance(decision, Mapping):
            summary.errors += 1
            continue
        summary.evaluated += 1
        usage = line.get("decision_model") or {}
        if usage.get("snapshot"):
            summary.snapshots.add(str(usage["snapshot"]))
        if usage.get("backend"):
            summary.backends.add(str(usage["backend"]))
        if line.get("endpoint_host"):
            summary.hosts.add(str(line["endpoint_host"]))
        if line.get("connection"):
            summary.connections.add(str(line["connection"]))
        evaluators_out = decision.get("evaluators") or {}
        labels = line.get("labels") or {}
        for ref in evaluators:
            result = evaluators_out.get(ref) or {}
            label = labels.get(ref)
            if result.get("status") == "assessed" and isinstance(label, bool):
                summary.assessed[ref] += 1
                summary.positives[ref] += int(label)
    return summary


def describe_pass(summary: PassSummary) -> str:
    parts = ", ".join(
        f"{ref} {summary.positives[ref]} of {summary.assessed[ref]} should fire"
        for ref in summary.assessed
    )
    where = ", ".join(sorted(summary.hosts)) or "no host"
    connection = "/".join(sorted(summary.connections)) or "unknown"
    return (
        f"calib-pass: {summary.evaluated} of {summary.lines} evaluated, {summary.errors} errors, "
        f"{summary.over_budget} over budget; {parts}; snapshot "
        f"{', '.join(sorted(summary.snapshots)) or 'none'} at {where} ({connection} connection); "
        f"cost ${summary.cost_usd:.6f}"
    )


def quality_hint(summary: PassSummary) -> List[str]:
    """What the counts mean for calibration quality (decision/calibrate_file.go: n >= 200, 30 a class)."""
    hints = []
    for ref, n in summary.assessed.items():
        positive = summary.positives[ref]
        negative = n - positive
        if summary.simulated:
            hints.append(
                f"{ref}: simulated risks (mock or a stand-in host): never a measured calibration"
            )
        elif n >= 200 and positive >= 30 and negative >= 30:
            hints.append(f"{ref}: enough labels for a measured calibration ({positive}/{negative})")
        else:
            hints.append(
                f"{ref}: {positive} should fire, {negative} should not: below measured "
                "(n >= 200, 30 a class), so it will be provisional (D206)"
            )
    return hints


# ---------------------------------------------------------------------------
# run (make calib-pass)
# ---------------------------------------------------------------------------


def _gate_eval_args(binary: Path, cap_usd: float) -> List[str]:
    return [
        str(binary),
        "--policy",
        "builtin",
        "--warm",
        "--concurrency",
        "1",
        "--evaluators",
        ",".join(EVALUATORS),
        "--max-cost-usd",
        f"{cap_usd:.6f}",
    ]


def _key_fetch() -> Optional[vendor_key.Fetch]:
    """The fetch the key reads use (tests replace it); None = urllib."""
    return None


def run_pass(args: argparse.Namespace) -> int:
    """make calib-pass (see the module docstring)."""
    binary = Path(args.gate_eval_bin)
    key = planning.key_context(model=args.jev_model, fetch=_key_fetch())
    exclude = Path(args.exclude_sources)
    if args.dry_run:
        return _dry_run(args, key, binary)

    def result(code: int, **fields: Any) -> int:
        vendor_key.emit_result(
            vendor_key.result_line(
                "calib-pass",
                run_id=fields.get("run_id"),
                exit_code=code,
                cost_usd=fields.get("cost_usd"),
                cap_usd=fields.get("cap_usd"),
                key=key.status,
                label=fields.get("label"),
                snapshot=fields.get("snapshot"),
                hosts={"decision": fields.get("host")},
                counts=fields.get("counts") or {},
            ),
            journal=Path(args.journal) if args.journal else None,
        )
        return code

    if not binary.is_file():
        print(f"calib-pass: no gate-eval binary at {binary}: make gate-eval", file=sys.stderr)
        return result(EXIT_PRECONDITION)
    if key.has_key and key.status is None:
        print(f"calib-pass: the key's limit could not be read ({key.problem})", file=sys.stderr)
        return result(EXIT_PRECONDITION)
    cap = vendor_key.effective_cap(args.max_cost_usd, key.status, minimum_usd=args.min_cost_usd)
    if cap.refused:
        print(f"calib-pass: refused: {cap.refused} (D201)", file=sys.stderr)
        return result(EXIT_PRECONDITION, cap_usd=cap.cap_usd)

    created_at = utc_now()
    run_id = args.run_id or "calib-" + created_at.replace("-", "").replace(":", "").lower()
    run_dir = Path(args.out_root) / run_id
    try:
        prepared = prepare(
            n=args.n,
            seed=args.seed,
            revision=args.revision,
            exclude_sources=exclude,
            out=run_dir / "calib.jsonl",
            bench_n=args.bench_n,
            bench_seed=args.bench_seed,
            source_dir=Path(args.source_dir) if args.source_dir else None,
        )
    except (CalibsetError, OSError, ValueError) as exc:
        print(f"calib-pass: {exc}", file=sys.stderr)
        return result(EXIT_PRECONDITION, run_id=run_id, cap_usd=cap.cap_usd)
    print(
        f"calib-pass {run_id}: {prepared.manifest['n']} answers from {prepared.manifest['dataset']} "
        f"(natural sample, seed {args.seed}; {prepared.manifest['positives']} should fire; "
        f"no source shared with {exclude.name}); {cap.describe()}",
        flush=True,
    )

    gate_out = run_dir / "gate.jsonl"
    env = {
        name: value
        for name, value in {**os.environ, **gate_eval_env()}.items()
        if name != POLICY_FILE_ENV
    }
    with open(prepared.path, "rb") as stdin, open(gate_out, "wb") as stdout:
        # The gate-eval binary the operator built, with fixed arguments and no shell.
        process = subprocess.run(  # nosec B603
            _gate_eval_args(binary, cap.cap_usd),
            stdin=stdin,
            stdout=stdout,
            stderr=subprocess.PIPE,
            env=env,
            check=False,
        )
    stderr = process.stderr.decode("utf-8", "replace").strip()
    if stderr:
        print(stderr[-2000:], file=sys.stderr, flush=True)
    summary = summarize_pass(gate_out)
    code = {0: EXIT_OK, 3: EXIT_OVER_BUDGET}.get(process.returncode, EXIT_FAILED)
    label = "simulated" if summary.simulated else "measured"
    document = {
        "schema": RESULT_SCHEMA,
        "run_id": run_id,
        "created_at": created_at,
        "exit": code,
        "dataset": prepared.manifest["dataset"],
        "n": prepared.manifest["n"],
        "label": label,
        "label_source": LABEL_SOURCE,
        "snapshot": summary.snapshot,
        "snapshots": sorted(summary.snapshots),
        "endpoint_hosts": sorted(summary.hosts),
        "connection": sorted(summary.connections),
        "evaluated": summary.evaluated,
        "errors": summary.errors,
        "over_budget": summary.over_budget,
        "assessed": summary.assessed,
        "positives": summary.positives,
        "cost_usd": round(summary.cost_usd, 6),
        "cap_usd": cap.cap_usd,
        "gate_eval_sha256": planning.sha256_file(binary),
    }
    (run_dir / "result.json").write_text(json.dumps(document, indent=2) + "\n", encoding="utf-8")
    print(describe_pass(summary), flush=True)
    for hint in quality_hint(summary):
        print(f"  {hint}", flush=True)
    if len(summary.snapshots) > 1:
        print("  more than one snapshot: a calibration covers one (D207)", flush=True)
    if code == EXIT_OK and summary.snapshot:
        gate_abs = gate_out.resolve()
        for ref in EVALUATORS:
            print(
                f"  next (free): make calibrate EVALUATOR={ref} IN={gate_abs} "
                f"LABEL_SOURCE={LABEL_SOURCE} DATASET={prepared.manifest['dataset']} "
                "(then the same with WRITE=1)",
                flush=True,
            )
    return result(
        code,
        run_id=run_id,
        cost_usd=summary.cost_usd,
        cap_usd=cap.cap_usd,
        label=label,
        snapshot=summary.snapshot,
        host=",".join(sorted(summary.hosts)) or None,
        counts={
            "n": summary.lines,
            "scored": summary.assessed.get(GROUNDEDNESS, 0),
            "failed": summary.errors,
        },
    )


def _dry_run(args: argparse.Namespace, key: planning.KeyContext, binary: Path) -> int:
    cap = vendor_key.effective_cap(args.max_cost_usd, key.status, minimum_usd=args.min_cost_usd)
    estimate = planning.estimate(
        ["gate"],
        planning.typical_sizes(args.n),
        full_audit_n=0,
        judge_provider="mock",
        judge_model="",
        jev_model=args.jev_model,
    )
    print(
        planning.render_plan(
            planning.PlanInputs(
                step="calib-pass",
                what=(
                    f"a natural sample of {args.n} RAGTruth-QA train answers (seed {args.seed}, "
                    f"revision {args.revision}); no source shared with "
                    f"{Path(args.exclude_sources).name}"
                ),
                systems=[
                    "gate: one warm gate-eval process, one answer at a time, "
                    "groundedness@1 and contradiction@1, built-in policy"
                ],
                estimate=estimate,
                cap=cap,
                resume_key=None,
                ledger_path=None,
                hosts={"decision": planning.decision_host()},
                key=key,
                gate_eval=binary,
                notes=[f"output: {args.out_root}/<run_id>/gate.jsonl (gitignored)"],
            )
        ),
        flush=True,
    )
    return EXIT_OK


# ---------------------------------------------------------------------------
# holdout (make calib-holdout)
# ---------------------------------------------------------------------------


def calibration_file_name(snapshot: str) -> str:
    """config/calibration/<name>: decision.CalibrationFileName (a "/" becomes "__")."""
    return snapshot.replace("/", "__") + ".json"


def _calibrate_command(args: argparse.Namespace) -> List[str]:
    if args.calibrate_bin:
        return [str(args.calibrate_bin)]
    go = shutil.which("go")
    root = repo_root()
    if go is None or root is None:
        raise CalibsetError("go is not on PATH: build cmd/calibrate and pass --calibrate-bin")
    return [go, "-C", str(root / "backend-go"), "run", "./cmd/calibrate"]


def run_holdout(args: argparse.Namespace) -> int:
    run_file = Path(args.run).resolve()
    try:
        run = json.loads(run_file.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"calib-holdout: cannot read {run_file}: {exc}", file=sys.stderr)
        return EXIT_PRECONDITION
    snapshots = sorted(run_snapshots(run))
    if len(snapshots) != 1:
        print(
            f"calib-holdout: the run used {len(snapshots)} snapshots ({', '.join(snapshots)}): "
            "one calibration covers one (D207)",
            file=sys.stderr,
        )
        return EXIT_PRECONDITION
    root = repo_root() or Path.cwd()
    calibration = (
        Path(args.calibration)
        if args.calibration
        else root / "config" / "calibration" / calibration_file_name(snapshots[0])
    ).resolve()
    if not calibration.is_file():
        print(
            f"calib-holdout: no calibration for {snapshots[0]} at {calibration}: run make "
            "calib-pass and make calibrate ... WRITE=1 first (D206)",
            file=sys.stderr,
        )
        return EXIT_PRECONDITION
    lines, counts = export_labels(run, list(EVALUATORS))
    if not lines:
        print("calib-holdout: the run has no exportable gate risks", file=sys.stderr)
        return EXIT_FAILED
    labels = run_file.parent / "holdout-labels.jsonl"
    with open(labels, "w", encoding="utf-8", newline="\n") as f:
        for line in lines:
            f.write(json.dumps(line, sort_keys=True) + "\n")
    print(f"calib-holdout: {len(lines)} labelled risks from {run_file.parent.name}: {counts}")
    try:
        command = _calibrate_command(args)
    except CalibsetError as exc:
        print(f"calib-holdout: {exc}", file=sys.stderr)
        return EXIT_PRECONDITION
    code = EXIT_OK
    for ref in EVALUATORS:
        out = run_file.parent / f"holdout-{ref.split('@')[0]}.json"
        # calibrate with fixed arguments and no shell.
        process = subprocess.run(  # nosec B603
            command
            + [
                "--evaluator",
                ref,
                "--holdout",
                str(labels),
                "--calibration",
                str(calibration),
                "--holdout-out",
                str(out),
            ],
            capture_output=True,
            text=True,
            check=False,
        )
        print(process.stdout.rstrip(), flush=True)
        if process.returncode != 0:
            message = process.stderr.strip()[-500:]
            print(
                f"calib-holdout: {ref}: calibrate exited {process.returncode}: {message}",
                file=sys.stderr,
            )
            if ref == GROUNDEDNESS:
                code = EXIT_FAILED
    if code == EXIT_OK:
        report = run_file.parent / "holdout-groundedness.json"
        print(
            f"  next (free): make bench-publish RUN={run_file} HOLDOUT={report} CHECK_ONLY=1",
            flush=True,
        )
    return code


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def _add_sample_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--n", type=int, required=True, help="Answers in the sample (CALIB_N)")
    parser.add_argument("--seed", type=int, default=13)
    parser.add_argument("--revision", required=True, help="RAGTruth commit (pinned)")
    parser.add_argument(
        "--exclude-sources",
        required=True,
        help="The bench's prepared test sample: no source may be shared (prepared if missing)",
    )
    parser.add_argument("--bench-n", type=int, default=200, help="The bench sample's n")
    parser.add_argument("--bench-seed", type=int, default=13, help="The bench sample's seed")
    parser.add_argument("--source-dir", help="Read the two RAGTruth files from here (tests)")


def cmd_prepare(args: argparse.Namespace) -> int:
    try:
        prepared = prepare(
            n=args.n,
            seed=args.seed,
            revision=args.revision,
            exclude_sources=Path(args.exclude_sources),
            out=Path(args.out),
            split=args.split,
            bench_n=args.bench_n,
            bench_seed=args.bench_seed,
            source_dir=Path(args.source_dir) if args.source_dir else None,
        )
    except CalibsetError as exc:
        print(f"calibset: {exc}", file=sys.stderr)
        return EXIT_PRECONDITION
    except (OSError, ValueError) as exc:
        print(f"calibset: {exc}", file=sys.stderr)
        return EXIT_FAILED
    manifest = prepared.manifest
    print(
        f"Wrote {manifest['n']} answers ({manifest['dataset']}, natural sample, seed "
        f"{manifest['seed']}; should fire: {manifest['positives']}; {manifest['sources']} sources, "
        f"none shared with {manifest['bench_sample']}) -> {prepared.path}"
    )
    return EXIT_OK


def cmd_result(args: argparse.Namespace) -> int:
    summary = summarize_pass(Path(args.gate))
    print(describe_pass(summary))
    for hint in quality_hint(summary):
        print(f"  {hint}")
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="RAGTruth-QA calibration set and pass (D206)")
    sub = parser.add_subparsers(dest="command", required=True)

    prep = sub.add_parser("prepare", help="Draw and write the calibration set (free)")
    _add_sample_args(prep)
    prep.add_argument("--split", default="train", choices=ragtruth.SPLITS)
    prep.add_argument("--out", required=True)
    prep.set_defaults(func=cmd_prepare)

    run = sub.add_parser("run", help="make calib-pass: prepare, then one warm gate-eval pass")
    _add_sample_args(run)
    run.add_argument("--gate-eval-bin", required=True)
    run.add_argument("--max-cost-usd", type=float, default=0.30)
    run.add_argument("--min-cost-usd", type=float, default=0.0)
    run.add_argument("--jev-model", default=planning.DEFAULT_JEV_MODEL)
    run.add_argument("--out-root", default=str(CALIBRATION_DIR))
    run.add_argument("--run-id")
    run.add_argument(
        "--journal", help="Also append the result line here (make: tmp/p6/results.log)"
    )
    run.add_argument("--dry-run", action="store_true", help="Print the plan; spend nothing")
    run.set_defaults(func=run_pass)

    result = sub.add_parser("result", help="Count a calib-pass gate.jsonl")
    result.add_argument("--gate", required=True)
    result.set_defaults(func=cmd_result)

    holdout = sub.add_parser("holdout", help="make calib-holdout: held-out ECE of a bench run")
    holdout.add_argument("--run", required=True)
    holdout.add_argument("--calibration", help="Default: config/calibration/<the run's snapshot>")
    holdout.add_argument("--calibrate-bin", help="A built cmd/calibrate (default: go run)")
    holdout.set_defaults(func=run_holdout)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    code: int = args.func(args)
    return code


if __name__ == "__main__":
    sys.exit(main())
