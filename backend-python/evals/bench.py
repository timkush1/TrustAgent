"""
The benchmark CLI behind `make bench-lite`, `make bench-publish` and `make eval-live`
(SPEC sections 2.13, 3.2 and P6).

    # Prepare a dataset first (datasets/ragtruth.py, datasets/aggrefact.py), then:
    python -m evals.bench run --dataset evals/datasets/ragtruth/ragtruth_qa_test_n200_seed13.jsonl \\
        --tier lite --max-cost-usd 5 --full-audit-n 50 \\
        --gate-eval-bin ../backend-go/bin/gate-eval --judge-provider openrouter

    python -m evals.bench publish --latest          # refuses simulated (mock) runs

`run` checks everything that could fail before the first paid call (the
gate-eval binary, the judge key, a price for the judge model), then judges the
examples system by system under one hard cost cap, resumable: rerunning the
same command reuses the cached results and the spend ledger
(evals/.cache/<resume key>.*), so the cap covers every session. It writes
evals/results/<run_id>/run.json, labelled measured or simulated (report.py).

Keys come from the environment or the repository's .env, like the engine
(OPENROUTER_API_KEY covers the judge; gate-eval reads its own configuration).

The cache key covers the judge model, prompts, evaluators and the gate-eval
binary, but not gate-eval's own configuration (backend, calibration, policy):
after changing that, start a new run with `--resume-key` (or clear the cache).
"""

import argparse
import asyncio
import logging
import re
import shutil
import subprocess  # nosec B404
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, List, Optional, Sequence, Tuple

from truthtable.config import load_settings, repo_root
from truthtable.judge import build_provider, judge_model
from truthtable.providers.base import LLMProvider
from truthtable.providers.mock import MockLLMProvider

from .budget import CostBudget
from .datasets.sampling import stratified_sample
from .examples import BenchExample, DatasetManifest, load_prepared
from .judges import (
    CASCADE_RESPONSE,
    DEFAULT_EVALUATORS,
    GATE,
    LLM_FULL_AUDIT,
    LLM_SAME_CHECKS,
    SYSTEMS,
    GateEvalConfig,
    GateJudge,
    Judge,
    JudgeRecord,
    LLMFullAuditJudge,
    LLMSameChecksJudge,
    MeteredProvider,
    SystemDescription,
    UnpricedModelError,
    cascade_records,
    describe_cascade,
    load_evaluators,
)
from .report import (
    LABEL_SIMULATED,
    BenchmarkSchemaError,
    MockOutputRefused,
    build_benchmark_run,
    check_run_id,
    publish,
    summarize_system,
    utc_now,
    write_local_run,
)
from .runner import DEFAULT_CONCURRENCY, ResultCache, SystemRun, run_judge

logger = logging.getLogger(__name__)

EVALS_DIR = Path(__file__).parent
CACHE_DIR = EVALS_DIR / ".cache"
RESULTS_DIR = EVALS_DIR / "results"
TIERS = ("lite", "full")
DEFAULT_REGION = "local"
DEFAULT_SEED = 13

EXIT_OK = 0
EXIT_FAILED = 1
EXIT_PRECONDITION = 2

_RUN_ID_UNSAFE = re.compile(r"[^A-Za-z0-9._-]+")


class PreconditionError(RuntimeError):
    """Something a run needs is missing; nothing has been spent."""


def git_sha() -> str:
    """HEAD of this repository ("0000000" outside a git checkout)."""
    git = shutil.which("git")
    root = repo_root()
    if git is None or root is None:
        return "0000000"
    try:
        # `git rev-parse HEAD` with fixed arguments and no shell.
        result = subprocess.run(  # nosec B603
            [git, "-C", str(root), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return "0000000"
    sha = result.stdout.strip()
    return sha if re.fullmatch(r"[0-9a-f]{7,40}", sha) else "0000000"


def make_run_id(tier: str, manifest: DatasetManifest, created_at: str) -> str:
    stamp = created_at.replace("-", "").replace(":", "")
    raw = f"{tier}-{manifest.name}-{manifest.subset or manifest.split}-{stamp}".lower()
    return _RUN_ID_UNSAFE.sub("-", raw)[:128]


def parse_systems(text: str) -> Tuple[str, ...]:
    systems = tuple(part.strip() for part in text.split(",") if part.strip())
    unknown = [system for system in systems if system not in SYSTEMS]
    if unknown or not systems:
        raise PreconditionError(f"unknown systems {unknown}; choose from {', '.join(SYSTEMS)}")
    if CASCADE_RESPONSE in systems and not {GATE, LLM_FULL_AUDIT} <= set(systems):
        raise PreconditionError("cascade_response is computed from gate and llm_full_audit")
    return systems


def build_judge_provider(args: argparse.Namespace) -> LLMProvider:
    """The judge the LLM systems use (mock only with explicit fixtures)."""
    if args.judge_provider == "mock":
        if args.mock_fixtures is None:
            raise PreconditionError("--judge-provider mock needs --mock-fixtures")
        return MockLLMProvider(model="mock", fixtures_path=args.mock_fixtures)
    settings = load_settings().settings
    model = args.judge_model or judge_model(settings, args.judge_provider)
    try:
        return build_provider(settings, args.judge_provider, model)
    except ValueError as exc:
        raise PreconditionError(
            f"the LLM judge is not configured: {exc} (set it in the environment or .env; one "
            "OpenRouter key covers Jev and the judge, see docs/JEV-ACCESS.md)"
        ) from exc


def full_audit_subset(examples: Sequence[BenchExample], n: int, seed: int) -> List[BenchExample]:
    """The stratified subset the full audit (and so the cascade) runs on."""
    if n <= 0 or n >= len(examples):
        return list(examples)
    return stratified_sample(
        examples, lambda example: example.stratum, n, seed, key=lambda example: example.id
    )


@dataclass
class Plan:
    """The judges of a run and the examples each one sees."""

    judges: List[Tuple[Judge, List[BenchExample]]]
    provider: Optional[MeteredProvider]


def plan_run(
    args: argparse.Namespace,
    systems: Sequence[str],
    examples: Sequence[BenchExample],
    budget: CostBudget,
) -> Plan:
    """Build the judges; raises PreconditionError before anything is spent."""
    judges: List[Tuple[Judge, List[BenchExample]]] = []
    if GATE in systems:
        binary = Path(args.gate_eval_bin) if args.gate_eval_bin else None
        if binary is None or not binary.is_file():
            raise PreconditionError(
                f"the gate-eval binary was not found at {binary}; build it with "
                "`go -C backend-go build -o bin/gate-eval ./cmd/gate-eval` and pass --gate-eval-bin"
            )
        config = GateEvalConfig(
            binary=binary,
            evaluators=tuple(args.evaluators),
            extra_args=tuple(args.gate_eval_arg or ()),
        )
        judges.append((GateJudge(config), list(examples)))

    provider: Optional[MeteredProvider] = None
    if LLM_SAME_CHECKS in systems or LLM_FULL_AUDIT in systems:
        try:
            provider = MeteredProvider(build_judge_provider(args), budget, system="llm")
        except UnpricedModelError as exc:
            raise PreconditionError(str(exc)) from exc
    if LLM_SAME_CHECKS in systems and provider is not None:
        try:
            questions = load_evaluators(args.evaluators)
        except (OSError, ValueError, KeyError) as exc:
            raise PreconditionError(f"evaluators {args.evaluators}: {exc}") from exc
        judges.append((LLMSameChecksJudge(provider, questions), list(examples)))
    if LLM_FULL_AUDIT in systems and provider is not None:
        subset = full_audit_subset(examples, args.full_audit_n, args.seed)
        judges.append((LLMFullAuditJudge(provider), subset))
    return Plan(judges=judges, provider=provider)


def _progress(system: str, done: int, total: int) -> None:
    if done == total or done % 10 == 0:
        print(f"  {system}: {done}/{total}", flush=True)


async def execute(
    args: argparse.Namespace,
) -> Tuple[Path, str, Dict[str, List[JudgeRecord]]]:
    """Run the benchmark; returns (run file, label, records by system)."""
    systems = parse_systems(args.systems)
    run_id: Optional[str] = None
    if args.run_id:
        try:
            run_id = check_run_id(args.run_id)
        except ValueError as exc:
            raise PreconditionError(str(exc)) from exc
    manifest, examples = load_prepared(Path(args.dataset))
    resume_key = args.resume_key or f"{args.tier}-{Path(args.dataset).stem}"
    cache_dir = Path(args.cache_dir)
    budget = CostBudget.open(args.max_cost_usd, cache_dir / f"{resume_key}.ledger.jsonl")
    cache = ResultCache(cache_dir / f"{resume_key}.cache.jsonl")
    plan = plan_run(args, systems, examples, budget)

    print(
        f"Benchmark {manifest.name} {manifest.subset or ''} {manifest.split}: {len(examples)} "
        f"examples, systems {', '.join(systems)}; hard cap ${budget.cap_usd:.2f} "
        f"(${budget.spent_usd:.4f} spent in earlier sessions)",
        flush=True,
    )
    runs: Dict[str, SystemRun] = {}
    descriptions: Dict[str, SystemDescription] = {}
    try:
        for judge, subset in plan.judges:
            run = await run_judge(
                judge,
                subset,
                budget=budget,
                cache=cache,
                concurrency=args.concurrency,
                retry_failed=args.retry_failed,
                progress=_progress,
            )
            runs[judge.system] = run
            descriptions[judge.system] = judge.describe(run.records)
            print(
                f"  {judge.system}: {run.ran} judged, {run.reused} from the cache; "
                f"spent ${budget.spent_usd:.4f} of ${budget.cap_usd:.2f}",
                flush=True,
            )
    finally:
        if plan.provider is not None:
            await plan.provider.close()

    records: Dict[str, List[JudgeRecord]] = {system: run.records for system, run in runs.items()}
    if CASCADE_RESPONSE in systems and GATE in runs and LLM_FULL_AUDIT in runs:
        records[CASCADE_RESPONSE] = cascade_records(
            [example.id for example in examples], runs[GATE].by_id(), runs[LLM_FULL_AUDIT].by_id()
        )
        descriptions[CASCADE_RESPONSE] = describe_cascade(
            descriptions[GATE], descriptions[LLM_FULL_AUDIT]
        )

    summaries = []
    notes = []
    for system in SYSTEMS:
        if system not in records:
            continue
        summary = summarize_system(system, records[system], descriptions[system], seed=args.seed)
        if summary is None:
            notes.append(f"{system}: no scored example (every one failed or was over budget)")
            continue
        summaries.append(summary)
    if not summaries:
        raise PreconditionError("no system produced a scored example; nothing to report")

    created_at = utc_now()
    document, label = build_benchmark_run(
        run_id=run_id or make_run_id(args.tier, manifest, created_at),
        created_at=created_at,
        git_sha=git_sha(),
        tier=args.tier,
        manifest=manifest,
        systems=summaries,
        records_by_system=records,
        region=args.region,
        concurrency=args.concurrency,
    )
    if LLM_FULL_AUDIT in runs:
        notes.append(
            f"llm_full_audit and cascade_response ran on a stratified subset of "
            f"{len(runs[LLM_FULL_AUDIT].records)} examples (seed {args.seed})"
        )
    path = write_local_run(Path(args.out_dir), document, label, records, manifest, notes=notes)
    _print_summary(document, label, path)
    return path, label, records


def _print_summary(document: Dict[str, object], label: str, path: Path) -> None:
    print(f"\nRun {document['run_id']} ({label.upper()})")
    systems = document.get("systems")
    if isinstance(systems, list):
        print(f"  {'system':<18} {'BAcc':>6} {'AUROC':>6} {'ECE':>6} {'p50 ms':>8} {'$/1k':>9}")
        for system in systems:
            metrics, cost, latency = system["metrics"], system["cost"], system["latency_ms"]

            def fmt(value: object, spec: str) -> str:
                return format(value, spec) if isinstance(value, (int, float)) else "n/a"

            print(
                f"  {system['id']:<18} {fmt(metrics['balanced_accuracy'], '6.3f')} "
                f"{fmt(metrics['auroc'], '6.3f')} {fmt(metrics['ece'], '6.3f')} "
                f"{fmt(latency['p50'], '8.0f')} {fmt(cost['usd_per_1k'], '9.4f')}"
            )
    print(f"  errors: {document.get('errors')}")
    if label == LABEL_SIMULATED:
        print(
            "  SIMULATED (mock output): these numbers are not measurements and cannot be published."
        )
    print(f"Wrote {path}")


def latest_run_file(results_dir: Path) -> Optional[Path]:
    """The most recently written local run file."""
    runs = sorted(results_dir.glob("*/run.json"), key=lambda path: path.stat().st_mtime)
    return runs[-1] if runs else None


def cmd_run(args: argparse.Namespace) -> int:
    try:
        asyncio.run(execute(args))
    except PreconditionError as exc:
        print(f"bench: {exc}", file=sys.stderr)
        return EXIT_PRECONDITION
    except (OSError, ValueError) as exc:
        print(f"bench: {exc}", file=sys.stderr)
        return EXIT_FAILED
    return EXIT_OK


def cmd_publish(args: argparse.Namespace) -> int:
    run_file = Path(args.run) if args.run else latest_run_file(Path(args.out_dir))
    if run_file is None or not run_file.is_file():
        print(
            "bench publish: no run file (pass --run, or run make bench-lite first)", file=sys.stderr
        )
        return EXIT_PRECONDITION
    try:
        written = publish(
            run_file,
            results_dir=Path(args.results_dir) if args.results_dir else None,
            index_path=Path(args.index) if args.index else None,
            measured_path=Path(args.measured) if args.measured else None,
        )
    except MockOutputRefused as exc:
        print(f"bench publish: {exc}", file=sys.stderr)
        return EXIT_FAILED
    except BenchmarkSchemaError as exc:
        print(f"bench publish: {exc}", file=sys.stderr)
        return EXIT_FAILED
    for what, path in written.items():
        print(f"Wrote {what}: {path}")
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="TrustAgent benchmark (bench-lite, eval-live)")
    sub = parser.add_subparsers(dest="command", required=True)

    run = sub.add_parser("run", help="Run a benchmark under a hard cost cap")
    run.add_argument("--dataset", required=True, help="Prepared .jsonl (datasets/*.py)")
    run.add_argument("--tier", choices=TIERS, default="lite")
    run.add_argument("--systems", default=",".join(SYSTEMS))
    run.add_argument("--max-cost-usd", type=float, required=True, help="Hard cap (all sessions)")
    run.add_argument("--full-audit-n", type=int, default=50, help="0 = every example")
    run.add_argument("--evaluators", nargs="+", default=list(DEFAULT_EVALUATORS))
    run.add_argument("--gate-eval-bin", help="Path to the Go gate-eval binary")
    run.add_argument(
        "--gate-eval-arg", action="append", help="Extra gate-eval argument (repeatable)"
    )
    run.add_argument(
        "--judge-provider",
        default="openrouter",
        choices=["openrouter", "anthropic", "openai", "ollama", "mock"],
    )
    run.add_argument("--judge-model", help="Default: the engine's model for the provider")
    run.add_argument("--mock-fixtures", type=Path, help="Match fixtures for --judge-provider mock")
    run.add_argument("--concurrency", type=int, default=DEFAULT_CONCURRENCY)
    run.add_argument("--seed", type=int, default=DEFAULT_SEED, help="Subset and bootstrap seed")
    run.add_argument("--region", default=DEFAULT_REGION, help="Where the run executed")
    run.add_argument("--run-id")
    run.add_argument("--resume-key", help="Cache and ledger name (default: tier + dataset)")
    run.add_argument("--retry-failed", action="store_true")
    run.add_argument("--cache-dir", default=str(CACHE_DIR))
    run.add_argument("--out-dir", default=str(RESULTS_DIR))
    run.set_defaults(func=cmd_run)

    pub = sub.add_parser("publish", help="Publish a measured run (refuses mock output)")
    pub.add_argument("--run", help="evals/results/<run_id>/run.json (default: the latest)")
    pub.add_argument("--latest", action="store_true", help="Publish the latest local run")
    pub.add_argument("--out-dir", default=str(RESULTS_DIR))
    pub.add_argument("--results-dir")
    pub.add_argument("--index")
    pub.add_argument("--measured")
    pub.set_defaults(func=cmd_publish)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    logging.basicConfig(level=logging.WARNING)
    args = build_parser().parse_args(argv)
    code: int = args.func(args)
    return code


if __name__ == "__main__":
    sys.exit(main())
