"""
The benchmark CLI behind `make bench-lite`, `bench-smoke`, `bench-publish`,
`calib-holdout` and `eval-live` (SPEC sections 2.13, 3.2 and P6; P6 plan section 3).

    # Prepare a dataset first (datasets/ragtruth.py, datasets/aggrefact.py), then:
    python -m evals.bench run --dataset evals/datasets/ragtruth/bench-lite.jsonl \\
        --tier lite --max-cost-usd 5 --full-audit-n 50 --resume-key lite-p6 \\
        --gate-eval-bin ../backend-go/bin/gate-eval --judge-provider openrouter
    python -m evals.bench run ... --dry-run           # the plan only: nothing is spent
    python -m evals.bench export-labels --run <run.json> --out labels.jsonl
    python -m evals.bench publish --run <run.json> [--calibration-holdout <report>] [--check-only]

`run` checks everything that could fail before the first paid call (the
gate-eval binary and a dry gate-eval start that resolves its route, evaluators,
backend and key; the judge key; a price for the judge model; the key's
remaining limit), then judges the examples system by system under one hard
cost cap, resumable: rerunning the same command reuses the cached results and
the spend ledger (evals/.cache/<resume key>.*), so the cap covers every
session. It writes evals/results/<run_id>/run.json, labelled measured or
simulated (report.py), and ends with one `TRUSTAGENT_P6_RESULT {json}` line.

P6 (D201, D203-D207):

- **Dry run** (`--dry-run`; the make targets use it unless CONFIRM=1): the
  plan (n, systems, the price-table estimate, the effective cap, the resume
  key and its ledger, the hosts, the snapshot) and exit 0, nothing spent.
- **Key limit** (D201): with an OpenRouter key, the free `GET /api/v1/key`
  lowers the cap to what the ledger has spent plus the key's remaining limit
  minus $0.10; below `--min-cost-usd` of room the run is refused.
- **Pinned inputs** (D204): the gate-eval binary's sha256 is recorded, and a
  resume key started with another binary is refused (its cache would miss and
  the run would pay twice). `--policy builtin` (the default) runs gate-eval on
  the built-in policy with TRUTHTABLE_POLICY_FILE removed from its environment.
  Whether tracked files were dirty is taken when the run starts.
- **Latency** (D205): the gate system runs first and alone, `--gate-concurrency`
  examples at a time (default 1), each gate-eval process warm (`--warm` makes
  OpenRouter's free key check on the same connection first, so latency_ms
  leaves out the TCP and TLS setup); the LLM systems then run at
  `--concurrency`. The machine's conditions go into `inputs.host_env`.
- **Stop rule** (D204): after `--stop-after-failures` (default 5) consecutive
  failures of one system, nothing more starts: the failure kind and its fix
  are printed and the exit status is 3. The examples it never reached are
  not cached; the same command with `--retry-failed` continues, under the same
  ledger.
- **Hosts** (D203): gate-eval names the host its decision calls went to and
  the judge records its provider's; a host outside common.v1 VendorHost (for
  example mock-upstream behind TRUTHTABLE_OPENROUTER_BASE_URL) makes the run
  simulated (non_vendor_host), and publish refuses it.

Keys come from the environment or the repository's .env, like the engine
(one OPENROUTER_API_KEY covers Jev and the judge). gate-eval reads only its
process environment, so the harness passes it the .env values it needs
(OPENROUTER_API_KEY, TYPESAFE_API_KEY, TRUTHTABLE_*) that the environment does
not already set. Nothing here prints a key.

The cache key covers the judge model, prompts, evaluators and the gate-eval
binary and arguments, but not gate-eval's own configuration (backend,
calibration): after changing that, start a new run with `--resume-key` (or
clear the cache).
"""

import argparse
import asyncio
import json
import logging
import os
import re
import shutil
import subprocess  # nosec B404
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

from truthtable.config import env_files, load_settings, repo_root
from truthtable.judge import build_provider, judge_model
from truthtable.providers.base import LLMProvider
from truthtable.providers.mock import MockLLMProvider

from . import plan as planning
from . import vendor_key
from .budget import CostBudget, ledger_total
from .datasets.sampling import stratified_sample
from .examples import BenchExample, DatasetManifest, load_prepared, manifest_path
from .judges import (
    CASCADE_RESPONSE,
    DEFAULT_EVALUATORS,
    GATE,
    LLM_FULL_AUDIT,
    LLM_SAME_CHECKS,
    OK,
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
    PublishRefused,
    build_benchmark_run,
    check_publish,
    check_run_id,
    publish,
    simulated_reasons,
    summarize_system,
    utc_now,
    write_local_run,
)
from .runner import (
    DEFAULT_CONCURRENCY,
    DEFAULT_STOP_AFTER_FAILURES,
    ResultCache,
    Stop,
    SystemRun,
    run_judge,
)

logger = logging.getLogger(__name__)

EVALS_DIR = Path(__file__).parent
CACHE_DIR = EVALS_DIR / ".cache"
RESULTS_DIR = EVALS_DIR / "results"
TIERS = ("lite", "full")
DEFAULT_REGION = "local"
DEFAULT_SEED = 13
POLICIES = ("builtin", "file")
POLICY_FILE_ENV = "TRUTHTABLE_POLICY_FILE"

EXIT_OK = 0
EXIT_FAILED = 1
EXIT_PRECONDITION = 2
EXIT_STOPPED = 3

_RUN_ID_UNSAFE = re.compile(r"[^A-Za-z0-9._-]+")

# The stop rule's fixes (docs/JEV-ACCESS.md "Errors and fixes"), by failure kind.
FIXES: Tuple[Tuple[str, str], ...] = (
    (
        "credits",
        "402: the account has no credit or the key's limit is reached. Add credit at "
        "openrouter.ai/settings/credits or raise the key's limit (docs/JEV-ACCESS.md)",
    ),
    (
        "data_policy",
        "404 data_policy: the privacy settings exclude the only provider that serves the model. "
        "Check openrouter.ai/settings/privacy, or unset TRUTHTABLE_JEV_ZDR (docs/JEV-ACCESS.md)",
    ),
    (
        "auth",
        "401: the key was refused. Paste a working key with make configure WITH_SECRETS=1 "
        "(docs/JEV-ACCESS.md)",
    ),
    (
        "not_found",
        "404: the model or the endpoint was not found. Check TRUTHTABLE_JEV_MODEL and the base "
        "URLs (docs/JEV-ACCESS.md)",
    ),
    (
        "timeout",
        "timeouts: the endpoint did not answer in time. Wait, then continue",
    ),
    (
        "rate_limited",
        "429: rate limited. Wait a minute, then continue",
    ),
    (
        "overload",
        "the provider is busy (429, 529 or 5xx). Wait, then continue",
    ),
    (
        "bad_response",
        "parse errors: the answers could not be read. Check the model id, then continue",
    ),
    (
        "invalid_output",
        "parse errors: the judge's answers could not be read. Check the judge model",
    ),
    (
        "gate_eval",
        "gate-eval failed to run. Rebuild it (make gate-eval) and check the stderr in the "
        "run's diagnostics",
    ),
)


class PreconditionError(RuntimeError):
    """Something a run needs is missing; nothing has been spent."""


def failure_fix(kind: str) -> str:
    """The fix for a failure kind, e.g. "gate_error:credits" (docs/JEV-ACCESS.md)."""
    lowered = kind.lower()
    for needle, fix in FIXES:
        if needle in lowered:
            return fix
    return "see the run's diagnostics (evals/.cache, the cache's notes) and docs/JEV-ACCESS.md"


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


def _positive_int(text: str) -> int:
    """An argparse type: an integer of at least 1 (benchmark-run.v1 concurrency)."""
    try:
        value = int(text)
    except ValueError:
        raise argparse.ArgumentTypeError(f"not an integer: {text!r}") from None
    if value < 1:
        raise argparse.ArgumentTypeError(f"must be at least 1: {value}")
    return value


def _non_negative_int(text: str) -> int:
    try:
        value = int(text)
    except ValueError:
        raise argparse.ArgumentTypeError(f"not an integer: {text!r}") from None
    if value < 0:
        raise argparse.ArgumentTypeError(f"must not be negative: {value}")
    return value


def parse_systems(text: str) -> Tuple[str, ...]:
    systems = tuple(part.strip() for part in text.split(",") if part.strip())
    unknown = [system for system in systems if system not in SYSTEMS]
    if unknown or not systems:
        raise PreconditionError(f"unknown systems {unknown}; choose from {', '.join(SYSTEMS)}")
    if CASCADE_RESPONSE in systems and not {GATE, LLM_FULL_AUDIT} <= set(systems):
        raise PreconditionError("cascade_response is computed from gate and llm_full_audit")
    return systems


def resolved_judge_model(args: argparse.Namespace) -> str:
    """The judge model the LLM systems use."""
    if args.judge_provider == "mock":
        return "mock"
    return str(args.judge_model or judge_model(load_settings().settings, args.judge_provider))


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


def gate_eval_extra_args(args: argparse.Namespace) -> Tuple[str, ...]:
    """The P6 gate-eval arguments (--policy builtin, --warm), then the operator's own."""
    extra: List[str] = []
    if args.policy == "builtin":
        extra += ["--policy", "builtin"]
    if args.warm:
        extra.append("--warm")
    return tuple(extra) + tuple(args.gate_eval_arg or ())


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
                "`make gate-eval` and pass --gate-eval-bin"
            )
        config = GateEvalConfig(
            binary=binary,
            evaluators=tuple(args.evaluators),
            extra_args=gate_eval_extra_args(args),
            env=gate_eval_env(),
            unset_env=(POLICY_FILE_ENV,) if args.policy == "builtin" else (),
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


# What gate-eval reads from its environment: the decision-model keys and the
# gateway's TRUTHTABLE_* settings (backend, model, price, policy file, budget).
GATE_EVAL_ENV_KEYS = frozenset({"OPENROUTER_API_KEY", "TYPESAFE_API_KEY"})
GATE_EVAL_ENV_PREFIX = "TRUTHTABLE_"


def gate_eval_env(environ: Optional[Mapping[str, str]] = None) -> Dict[str, str]:
    """
    The dotenv values gate-eval needs but would not see: it reads only its own
    process environment, while the judge (like the engine) also reads the
    repository's .env. The same files and precedence apply (a later file
    wins; the process environment wins over every file).
    """
    from dotenv import dotenv_values  # a pydantic-settings dependency

    env = os.environ if environ is None else environ
    values: Dict[str, str] = {}
    for path in env_files(env):
        if not path.is_file():
            continue
        for name, value in dotenv_values(path).items():
            wanted = name in GATE_EVAL_ENV_KEYS or name.startswith(GATE_EVAL_ENV_PREFIX)
            if value is not None and wanted:
                values[name] = value
    return {name: value for name, value in values.items() if name not in env}


async def preflight(plan: Plan) -> None:
    """Check what can be checked for free before the first paid call."""
    for judge, _ in plan.judges:
        if isinstance(judge, GateJudge):
            problem = await judge.preflight()
            if problem is not None:
                raise PreconditionError(
                    f"{problem} (gate-eval reads OPENROUTER_API_KEY or TYPESAFE_API_KEY and "
                    "TRUTHTABLE_JEV_* from the environment or .env; see docs/JEV-ACCESS.md)"
                )


def _progress(system: str, done: int, total: int) -> None:
    if done == total or done % 10 == 0:
        print(f"  {system}: {done}/{total}", flush=True)


# ---------------------------------------------------------------------------
# Pinned inputs (D204)
# ---------------------------------------------------------------------------


def meta_path(cache_dir: Path, resume_key: str) -> Path:
    return cache_dir / f"{resume_key}.meta.json"


def check_resume_binary(path: Path, gate_eval_sha256: Optional[str]) -> None:
    """
    Record the gate-eval binary a resume key started with, and refuse another
    one: the cache key includes the binary, so a resume with a new build
    would judge (and pay for) every example again.
    """
    if gate_eval_sha256 is None:
        return
    if path.is_file():
        try:
            recorded = json.loads(path.read_text(encoding="utf-8")).get("gate_eval_sha256")
        except (OSError, json.JSONDecodeError, AttributeError):
            recorded = None
        if recorded and recorded != gate_eval_sha256:
            raise PreconditionError(
                f"resume key {path.name[: -len('.meta.json')]} was started with gate-eval sha256 "
                f"{recorded[:12]}…, this binary is {gate_eval_sha256[:12]}…: a resume would pay "
                "twice (the cache is keyed by the binary). Use the binary the run started with, "
                "or start a new resume key"
            )
        if recorded:
            return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps({"gate_eval_sha256": gate_eval_sha256, "created_at": utc_now()}) + "\n",
        encoding="utf-8",
    )


def dataset_revision(manifest: DatasetManifest) -> str:
    """The source revision of a prepared dataset (its git ref, without the content digest)."""
    return manifest.revision.split("@", 1)[0] or manifest.revision


def _key_fetch() -> Optional[vendor_key.Fetch]:
    """The fetch the key reads use (tests replace it); None = urllib."""
    return None


def read_key(args: argparse.Namespace) -> planning.KeyContext:
    """The key's amounts and the snapshot, only when there is an OpenRouter key."""
    return planning.key_context(model=args.jev_model, fetch=_key_fetch())


@dataclass
class Outcome:
    """What a run did, for the exit status and the result line."""

    exit_code: int
    label: Optional[str] = None
    path: Optional[Path] = None
    run_id: Optional[str] = None
    records: Dict[str, List[JudgeRecord]] = field(default_factory=dict)
    document: Dict[str, Any] = field(default_factory=dict)
    stopped: Optional[Stop] = None
    cap_usd: Optional[float] = None  # The effective cap (D201)


def _most_common(values: Sequence[Optional[str]]) -> Optional[str]:
    counts: Dict[str, int] = {}
    for value in values:
        if value:
            counts[value] = counts.get(value, 0) + 1
    return max(sorted(counts), key=lambda value: counts[value]) if counts else None


def vendor_block(
    records: Mapping[str, Sequence[JudgeRecord]], provider: Optional[MeteredProvider]
) -> Dict[str, Optional[str]]:
    """benchmark-run.v1 vendor (D203): the hosts the run's calls actually went to."""
    gate = records.get(GATE) or []
    decision_host = _most_common([record.details.get("endpoint_host") for record in gate])
    judge_host = None
    if provider is not None and (LLM_SAME_CHECKS in records or LLM_FULL_AUDIT in records):
        judge_host = provider.host
    return {"decision_host": decision_host, "judge_host": judge_host}


def latency_block(
    records: Mapping[str, Sequence[JudgeRecord]], gate_concurrency: int
) -> Dict[str, Any]:
    """benchmark-run.v1 latency (D205): warm only if every scored gate call was warm."""
    gate_ok = [record for record in records.get(GATE) or [] if record.status == OK]
    warm = bool(gate_ok) and all(record.details.get("connection") == "warm" for record in gate_ok)
    return {"connection": "warm" if warm else "cold", "gate_concurrency": gate_concurrency}


def _stop_message(stop: Stop) -> str:
    return (
        f"bench: stopped: {stop.failures} consecutive {stop.system} failures (last kinds: "
        f"{', '.join(stop.error_kinds[-3:])}). Nothing more was started, and the examples it "
        "never reached are not cached.\n"
        f"  fix: {failure_fix(stop.kind)}\n"
        '  then run the same command with --retry-failed (make: BENCH_ARGS="--retry-failed"): '
        "it continues under the same ledger and cap."
    )


async def execute(args: argparse.Namespace) -> Outcome:
    """Run the benchmark (see the module docstring)."""
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
    dirty = planning.tree_dirty()
    budget = CostBudget.open(args.max_cost_usd, cache_dir / f"{resume_key}.ledger.jsonl")
    cache = ResultCache(cache_dir / f"{resume_key}.cache.jsonl")

    key = read_key(args)
    if key.has_key and key.status is None:
        raise PreconditionError(
            f"the key's limit could not be read ({key.problem}); nothing was spent (D201)"
        )
    cap = vendor_key.effective_cap(
        args.max_cost_usd, key.status, spent_usd=budget.spent_usd, minimum_usd=args.min_cost_usd
    )
    if cap.refused:
        raise PreconditionError(f"refused: {cap.refused} (D201)")
    budget.cap_usd = cap.cap_usd

    gate_sha = None
    if GATE in systems and args.gate_eval_bin:
        gate_sha = planning.sha256_file(Path(args.gate_eval_bin))
    plan = plan_run(args, systems, examples, budget)
    check_resume_binary(meta_path(cache_dir, resume_key), gate_sha)

    print(
        f"Benchmark {manifest.name} {manifest.subset or ''} {manifest.split}: {len(examples)} "
        f"examples, systems {', '.join(systems)}; {cap.describe()} "
        f"(${budget.spent_usd:.4f} spent in earlier sessions, resume key {resume_key})"
        + ("; the tree has uncommitted changes, so this run cannot be published" if dirty else ""),
        flush=True,
    )
    inputs = {
        "dataset_revision": dataset_revision(manifest),
        "policy_source": args.policy,
        "gate_eval_sha256": gate_sha,
        "host_env": planning.host_env(args.region),
    }
    runs: Dict[str, SystemRun] = {}
    descriptions: Dict[str, SystemDescription] = {}
    stopped: Optional[Stop] = None
    try:
        await preflight(plan)
        for judge, subset in plan.judges:
            run = await run_judge(
                judge,
                subset,
                budget=budget,
                cache=cache,
                concurrency=args.gate_concurrency if judge.system == GATE else args.concurrency,
                retry_failed=args.retry_failed,
                progress=_progress,
                stop_after_failures=args.stop_after_failures,
            )
            runs[judge.system] = run
            print(
                f"  {judge.system}: {run.ran} judged, {run.reused} from the cache; "
                f"spent ${budget.spent_usd:.4f} of ${budget.cap_usd:.2f}",
                flush=True,
            )
            if run.stopped is not None:
                stopped = run.stopped
                break
            descriptions[judge.system] = judge.describe(run.records)
    finally:
        if plan.provider is not None:
            await plan.provider.close()

    records: Dict[str, List[JudgeRecord]] = {system: run.records for system, run in runs.items()}
    if stopped is not None:
        print(_stop_message(stopped), file=sys.stderr, flush=True)
        return Outcome(
            exit_code=EXIT_STOPPED,
            run_id=run_id,
            records=records,
            stopped=stopped,
            cap_usd=budget.cap_usd,
        )
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
        vendor=vendor_block(records, plan.provider),
        inputs=inputs,
        dirty=dirty,
        latency=latency_block(records, args.gate_concurrency),
    )
    if LLM_FULL_AUDIT in runs:
        notes.append(
            f"llm_full_audit and cascade_response ran on a stratified subset of "
            f"{len(runs[LLM_FULL_AUDIT].records)} examples (seed {args.seed})"
        )
    path = write_local_run(Path(args.out_dir), document, label, records, manifest, notes=notes)
    _print_summary(document, label, path, simulated_reasons(records, document.get("vendor")))
    return Outcome(
        exit_code=EXIT_OK,
        label=label,
        path=path,
        run_id=str(document["run_id"]),
        records=records,
        document=document,
        cap_usd=budget.cap_usd,
    )


def _print_summary(
    document: Dict[str, object], label: str, path: Path, reasons: Sequence[str] = ()
) -> None:
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
    print(f"  errors: {document.get('errors')}; quality: {_quality_line(document)}")
    if label == LABEL_SIMULATED:
        why = f" ({', '.join(reasons)})" if reasons else ""
        print(
            f"  SIMULATED{why}: mock output or a stand-in host. These numbers are not "
            "measurements and cannot be published."
        )
    print(f"Wrote {path}")


def _quality_line(document: Mapping[str, Any]) -> str:
    quality = document.get("quality") or {}
    return (
        f"{quality.get('scored', 0)} of {quality.get('n', 0)} scored, "
        f"{quality.get('failed', 0)} failed, {quality.get('excluded', 0)} excluded"
    )


def _counts(records: Mapping[str, Sequence[JudgeRecord]]) -> Dict[str, int]:
    measured = [
        record
        for system, items in records.items()
        if system != CASCADE_RESPONSE
        for record in items
        if record.status != "not_run"
    ]
    return {
        "n": len(measured),
        "scored": sum(1 for record in measured if record.status == OK and record.score is not None),
        "failed": sum(1 for record in measured if record.status == "failed"),
    }


def _print_result(
    args: argparse.Namespace,
    outcome: Outcome,
    *,
    cost_usd: Optional[float],
    cap_usd: Optional[float],
) -> None:
    key = read_key(args) if outcome.exit_code in (EXIT_OK, EXIT_STOPPED) else None
    gate = outcome.records.get(GATE) or []
    vendor = outcome.document.get("vendor") or {}
    vendor_key.emit_result(
        vendor_key.result_line(
            args.step or f"bench-{args.tier}",
            run_id=outcome.run_id,
            exit_code=outcome.exit_code,
            cost_usd=cost_usd,
            cap_usd=cap_usd,
            key=key.status if key is not None else None,
            label=outcome.label,
            snapshot=_most_common([record.details.get("snapshot") for record in gate]),
            hosts={
                "decision": vendor.get("decision_host")
                or _most_common([record.details.get("endpoint_host") for record in gate]),
                "judge": vendor.get("judge_host"),
                "upstream": None,
            },
            counts=_counts(outcome.records),
        ),
        journal=Path(args.journal) if args.journal else None,
    )


# ---------------------------------------------------------------------------
# The dry run (D204)
# ---------------------------------------------------------------------------


def dry_run(args: argparse.Namespace) -> int:
    """Print the plan; nothing is spent and no paid call is made."""
    systems = parse_systems(args.systems)
    dataset = Path(args.dataset)
    if dataset.is_file() and manifest_path(dataset).is_file():
        manifest, examples = load_prepared(dataset)
        sizes = planning.example_sizes(examples)
        what = (
            f"{len(examples)} {manifest.name} {manifest.subset or ''} {manifest.split} answers "
            f"(prepared; revision {dataset_revision(manifest)}, seed {manifest.seed})"
        )
    else:
        n = args.plan_n
        sizes = planning.typical_sizes(n)
        what = (
            f"{n} answers, not prepared yet (CONFIRM=1 prepares them from the pinned revision, "
            f"free); the estimate uses the typical {planning.TYPICAL_EXAMPLE_TOKENS}-token example"
        )
    resume_key = args.resume_key or f"{args.tier}-{dataset.stem}"
    ledger = Path(args.cache_dir) / f"{resume_key}.ledger.jsonl"
    key = read_key(args)
    cap = vendor_key.effective_cap(
        args.max_cost_usd, key.status, spent_usd=ledger_total(ledger), minimum_usd=args.min_cost_usd
    )
    judge_model_name = (
        resolved_judge_model(args) if set(systems) & {LLM_SAME_CHECKS, LLM_FULL_AUDIT} else ""
    )
    estimate = planning.estimate(
        systems,
        sizes,
        full_audit_n=args.full_audit_n,
        judge_provider=args.judge_provider,
        judge_model=judge_model_name,
        jev_model=args.jev_model,
    )
    shown = [
        f"{GATE} (alone, {args.gate_concurrency} at a time, {'warm' if args.warm else 'cold'})"
        if system == GATE
        else (
            f"{LLM_FULL_AUDIT} ({args.full_audit_n or 'all'})"
            if system == LLM_FULL_AUDIT
            else system
        )
        for system in systems
    ]
    hosts: Dict[str, Optional[str]] = {}
    if GATE in systems:
        hosts["decision"] = planning.decision_host()
    if set(systems) & {LLM_SAME_CHECKS, LLM_FULL_AUDIT}:
        hosts["judge"] = planning.judge_host(args.judge_provider)
    notes = [
        f"policy {args.policy}"
        + (" (TRUTHTABLE_POLICY_FILE is ignored)" if args.policy == "builtin" else "")
    ]
    if planning.tree_dirty():
        notes.append("the tree has uncommitted changes: a run started now cannot be published")
    print(
        planning.render_plan(
            planning.PlanInputs(
                step=args.step or f"bench-{args.tier}",
                what=what,
                systems=shown,
                estimate=estimate,
                cap=cap,
                resume_key=resume_key,
                ledger_path=ledger,
                hosts=hosts,
                key=key,
                gate_eval=Path(args.gate_eval_bin)
                if GATE in systems and args.gate_eval_bin
                else None,
                notes=notes,
            )
        ),
        flush=True,
    )
    return EXIT_OK


def cmd_run(args: argparse.Namespace) -> int:
    if args.dry_run:
        try:
            return dry_run(args)
        except PreconditionError as exc:
            print(f"bench: {exc}", file=sys.stderr)
            return EXIT_PRECONDITION
    resume_key = args.resume_key or f"{args.tier}-{Path(args.dataset).stem}"
    ledger = Path(args.cache_dir) / f"{resume_key}.ledger.jsonl"
    try:
        outcome = asyncio.run(execute(args))
    except PreconditionError as exc:
        print(f"bench: {exc}", file=sys.stderr)
        outcome = Outcome(exit_code=EXIT_PRECONDITION)
    except (OSError, ValueError) as exc:
        print(f"bench: {exc}", file=sys.stderr)
        outcome = Outcome(exit_code=EXIT_FAILED)
    cap = outcome.cap_usd if outcome.cap_usd is not None else args.max_cost_usd
    _print_result(args, outcome, cost_usd=ledger_total(ledger), cap_usd=cap)
    return outcome.exit_code


# ---------------------------------------------------------------------------
# export-labels (D206): a run's gate risks as held-out calibration labels
# ---------------------------------------------------------------------------


def dataset_tag(name: str, subset: Optional[str], split: str, revision: str) -> str:
    """A dataset name for calibration files, e.g. "ragtruth:qa-test@c103204"."""
    part = f"{subset}-{split}" if subset else split
    return f"{name.lower()}:{part}@{revision[:7]}"


def export_labels(
    run: Mapping[str, Any], evaluators: Sequence[str]
) -> Tuple[List[Dict[str, Any]], Dict[str, Dict[str, int]]]:
    """
    The run's scored gate results as calibrate's labelled risks, one line per
    evaluator with a raw risk and a label: {id, target, risk_raw, label,
    label_source, snapshot, backend, dataset, endpoint_host}. Returns the
    lines and, per evaluator, n and the positives.
    """
    document = run.get("benchmark_run") or {}
    data = document.get("dataset") or {}
    inputs = document.get("inputs") or {}
    revision = str(inputs.get("dataset_revision") or str(data.get("revision") or "").split("@")[0])
    tag = dataset_tag(
        str(data.get("name") or "dataset"),
        data.get("subset"),
        str(data.get("split") or "test"),
        revision,
    )
    lines: List[Dict[str, Any]] = []
    counts: Dict[str, Dict[str, int]] = {ref: {"n": 0, "positive": 0} for ref in evaluators}
    for raw in (run.get("per_example") or {}).get(GATE) or []:
        if raw.get("status") != OK:
            continue
        details = raw.get("details") or {}
        for ref in evaluators:
            risk = (details.get("risk_raw") or {}).get(ref)
            label = (details.get("labels") or {}).get(ref)
            if (
                not isinstance(risk, (int, float))
                or isinstance(risk, bool)
                or not isinstance(label, bool)
            ):
                continue
            line: Dict[str, Any] = {
                "id": str(raw.get("example_id")),
                "target": ref,
                "risk_raw": float(risk),
                "label": label,
                "label_source": "public_dataset",
                "dataset": tag,
            }
            for name, value in (
                ("snapshot", details.get("snapshot")),
                ("backend", details.get("backend")),
                ("endpoint_host", details.get("endpoint_host")),
            ):
                if isinstance(value, str) and value:
                    line[name] = value
            lines.append(line)
            counts[ref]["n"] += 1
            counts[ref]["positive"] += int(label)
    return lines, counts


def cmd_export_labels(args: argparse.Namespace) -> int:
    try:
        with open(args.run, encoding="utf-8") as f:
            run = json.load(f)
    except (OSError, json.JSONDecodeError) as exc:
        print(f"bench export-labels: cannot read {args.run}: {exc}", file=sys.stderr)
        return EXIT_PRECONDITION
    lines, counts = export_labels(run, args.evaluators)
    if not lines:
        print(
            "bench export-labels: the run has no scored gate result with a raw risk and a label "
            "(it predates P6, or the gate did not run)",
            file=sys.stderr,
        )
        return EXIT_FAILED
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    with open(out, "w", encoding="utf-8", newline="\n") as f:
        for line in lines:
            f.write(json.dumps(line, sort_keys=True) + "\n")
    summary = ", ".join(
        f"{ref} {c['n']} ({c['positive']} should fire)" for ref, c in counts.items()
    )
    print(f"Wrote {len(lines)} labelled risks to {out}: {summary}; dataset {lines[0]['dataset']}")
    return EXIT_OK


# ---------------------------------------------------------------------------
# publish
# ---------------------------------------------------------------------------


def _scratch_dir(args: argparse.Namespace, run_file: Path) -> Path:
    if args.scratch_dir:
        return Path(args.scratch_dir)
    root = repo_root() or Path.cwd()
    return root / "tmp" / "bench-check" / run_file.resolve().parent.name


def cmd_publish(args: argparse.Namespace) -> int:
    if not args.run:
        print(
            "usage: bench publish --run <abs path to evals/results/<run_id>/run.json> "
            "[--calibration-holdout <report>] [--check-only] (make bench-publish RUN=...): one "
            "named run, never the latest by time (D204)",
            file=sys.stderr,
        )
        return EXIT_PRECONDITION
    run_file = Path(args.run)
    if not run_file.is_file():
        print(f"bench publish: no run file at {run_file}", file=sys.stderr)
        return EXIT_PRECONDITION
    common: Dict[str, Any] = {
        "results_dir": Path(args.results_dir) if args.results_dir else None,
        "measured_path": Path(args.measured) if args.measured else None,
        "calibration_holdout": Path(args.calibration_holdout) if args.calibration_holdout else None,
        "snapshot_split": args.snapshot_split,
        "attempts_dir": Path(args.attempts_dir) if args.attempts_dir else None,
    }
    try:
        if args.check_only:
            problems, written = check_publish(run_file, _scratch_dir(args, run_file), **common)
            for problem in problems:
                print(f"  refused: {problem}")
            print(
                (
                    "bench publish --check-only: publishable"
                    if not problems
                    else f"bench publish --check-only: {len(problems)} refusal(s)"
                )
                + f"; the would-be files are in {written['problems'].parent} (nothing else was written)"
            )
            return EXIT_OK if not problems else EXIT_FAILED
        written = publish(run_file, index_path=Path(args.index) if args.index else None, **common)
    except PublishRefused as exc:
        print(f"bench publish refuses run {exc.run_id} (nothing was written):", file=sys.stderr)
        for problem in exc.problems:
            print(f"  - {problem}", file=sys.stderr)
        return EXIT_FAILED
    except (MockOutputRefused, BenchmarkSchemaError) as exc:
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
    run.add_argument(
        "--min-cost-usd",
        type=float,
        default=0.0,
        help="Refuse when the key's remaining limit leaves less room than this (D201)",
    )
    run.add_argument("--full-audit-n", type=int, default=50, help="0 = every example")
    run.add_argument("--evaluators", nargs="+", default=list(DEFAULT_EVALUATORS))
    run.add_argument("--gate-eval-bin", help="Path to the Go gate-eval binary")
    run.add_argument(
        "--gate-eval-arg", action="append", help="Extra gate-eval argument (repeatable)"
    )
    run.add_argument(
        "--policy",
        choices=POLICIES,
        default="builtin",
        help="builtin: gate-eval's built-in policy, TRUTHTABLE_POLICY_FILE removed (D204)",
    )
    run.add_argument(
        "--warm",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="gate-eval --warm: the free key check first, so latency leaves out TLS setup (D205)",
    )
    run.add_argument(
        "--gate-concurrency",
        type=_positive_int,
        default=1,
        help="Gate examples at once; the gate runs alone, before the LLM systems (D205)",
    )
    run.add_argument(
        "--stop-after-failures",
        type=_non_negative_int,
        default=DEFAULT_STOP_AFTER_FAILURES,
        help="Stop after this many consecutive failures of one system, exit 3 (0 = never; D204)",
    )
    run.add_argument(
        "--judge-provider",
        default="openrouter",
        choices=["openrouter", "anthropic", "openai", "ollama", "mock"],
    )
    run.add_argument("--judge-model", help="Default: the engine's model for the provider")
    run.add_argument(
        "--jev-model", default=planning.DEFAULT_JEV_MODEL, help="For the plan and the snapshot"
    )
    run.add_argument("--mock-fixtures", type=Path, help="Match fixtures for --judge-provider mock")
    run.add_argument(
        "--concurrency", type=_positive_int, default=DEFAULT_CONCURRENCY, help="Examples at once"
    )
    run.add_argument("--seed", type=int, default=DEFAULT_SEED, help="Subset and bootstrap seed")
    run.add_argument("--region", default=DEFAULT_REGION, help="Where the run executed")
    run.add_argument("--run-id")
    run.add_argument("--resume-key", help="Cache and ledger name (default: tier + dataset)")
    run.add_argument("--retry-failed", action="store_true")
    run.add_argument("--cache-dir", default=str(CACHE_DIR))
    run.add_argument("--out-dir", default=str(RESULTS_DIR))
    run.add_argument("--step", help="The step name in the plan and the result line")
    run.add_argument(
        "--journal", help="Also append the result line here (make: tmp/p6/results.log)"
    )
    run.add_argument("--dry-run", action="store_true", help="Print the plan; spend nothing")
    run.add_argument(
        "--plan-n", type=int, default=200, help="--dry-run: n when the dataset is not prepared yet"
    )
    run.set_defaults(func=cmd_run)

    export = sub.add_parser(
        "export-labels", help="A run's gate risks as held-out calibration labels (D206)"
    )
    export.add_argument("--run", required=True, help="evals/results/<run_id>/run.json")
    export.add_argument("--out", required=True, help="Labelled-risk JSONL for calibrate --holdout")
    export.add_argument("--evaluators", nargs="+", default=list(DEFAULT_EVALUATORS))
    export.set_defaults(func=cmd_export_labels)

    pub = sub.add_parser("publish", help="Publish a measured run (refuses mock output)")
    pub.add_argument("--run", help="evals/results/<run_id>/run.json (required: D204)")
    pub.add_argument("--results-dir")
    pub.add_argument("--index")
    pub.add_argument("--measured")
    pub.add_argument(
        "--calibration-holdout",
        help="calibrate --holdout report of this run (make calib-holdout): the held-out ECE (D206)",
    )
    pub.add_argument(
        "--snapshot-split",
        action="store_true",
        help="The owner chose to keep two snapshots (D207 option b): both are named",
    )
    pub.add_argument(
        "--attempts-dir", help="Where the bench runs are counted (default: the run's results dir)"
    )
    pub.add_argument(
        "--check-only",
        action="store_true",
        help="Every check; the would-be files go to the scratch dir only (Gate B)",
    )
    pub.add_argument(
        "--scratch-dir", help="--check-only output (default: tmp/bench-check/<run_id>)"
    )
    pub.set_defaults(func=cmd_publish)
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    logging.basicConfig(level=logging.WARNING)
    args = build_parser().parse_args(argv)
    code: int = args.func(args)
    return code


if __name__ == "__main__":
    sys.exit(main())
