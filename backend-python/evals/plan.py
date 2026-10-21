"""
The plan a paid P6 step prints before it spends anything (D204), and what the
steps share: the hosts they will call, the key's room (D201), the gate-eval
binary's sha256 and the machine's conditions (D205).

`make bench-lite`, `bench-smoke`, `calib-pass` and `eval-live` print a plan
and exit 0 unless `CONFIRM=1`:

- n and the systems;
- the price-table estimate [estimate] and the effective cap (the key's
  remaining limit minus $0.10, when that is lower);
- the resume key and what its ledger has spent in earlier sessions;
- the hosts from the configuration and the snapshot from OpenRouter's free
  models list; with no key there are no network reads at all: "no key: plan
  only".

The estimate multiplies each example's token count by the price table; for a
dataset not prepared yet it uses the typical RAGTruth-QA example (515 tokens,
measured at P6 Step 0 from the free download). It is a guide, never a cap: the
cap is the hard limit.
"""

import hashlib
import math
import os
import platform
import shutil
import subprocess  # nosec B404
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, Dict, List, Mapping, Optional, Sequence, Tuple

from truthtable.config import repo_root
from truthtable.providers.pricing import PriceTable, active_price_table

from . import vendor_key
from .examples import BenchExample, estimate_tokens
from .judges import GATE, LLM_FULL_AUDIT, LLM_SAME_CHECKS

# The typical RAGTruth-QA example (query, answer and passages), P6 Step 0: mean 515 tokens.
TYPICAL_EXAMPLE_TOKENS = 515
TYPICAL_RESPONSE_TOKENS = 110
# What compile adds around an example for two evaluators (about 275 overhead and 150 per
# question; contracts/pricing/assumptions.v1.json uses 600 for four questions).
GATE_OVERHEAD_TOKENS = 575
# The judge's prompts around an example are measured from the prompts themselves
# (`prompt_overheads`); these are the fallbacks. Output sizes and claims per answer are the
# contracts/pricing/assumptions.v1.json inputs.
SAME_CHECKS_OVERHEAD_TOKENS = 975
SAME_CHECKS_OUTPUT_TOKENS = 150
CLAIMS_PER_ANSWER = 8
DECOMPOSE_OVERHEAD_TOKENS = 400
DECOMPOSE_OUTPUT_TOKENS = 300
VERIFY_OVERHEAD_TOKENS = 675  # The verifier's prompt and the claim
VERIFY_OUTPUT_TOKENS = 120

DEFAULT_JEV_MODEL = "typesafe/jev-1.13"
JUDGE_HOSTS = {
    "anthropic": "api.anthropic.com",
    "openai": "api.openai.com",
}
NO_KEY = "no key: plan only"


# ---------------------------------------------------------------------------
# Hosts (D203), from the configuration only
# ---------------------------------------------------------------------------


def _merged_env(environ: Optional[Mapping[str, str]] = None) -> Dict[str, str]:
    env = os.environ if environ is None else environ
    return {**vendor_key._dotenv(env), **env}


def decision_host(environ: Optional[Mapping[str, str]] = None) -> Optional[str]:
    """
    The host gate-eval's decision calls will go to, as the gateway resolves it:
    TRUTHTABLE_JEV_BASE_URL, else api.typesafe.ai for the typesafe backend,
    else the OpenRouter origin (TRUTHTABLE_OPENROUTER_BASE_URL). None for mock.
    """
    env = _merged_env(environ)
    backend = (env.get("TRUTHTABLE_JEV_BACKEND") or "auto").strip()
    if backend == "mock":
        return None
    base = (env.get("TRUTHTABLE_JEV_BASE_URL") or "").strip()
    if base:
        return vendor_key.host_of(base) or None
    if backend == "typesafe" or (
        backend == "auto" and not env.get("OPENROUTER_API_KEY") and env.get("TYPESAFE_API_KEY")
    ):
        return "api.typesafe.ai"
    return vendor_key.host_of(vendor_key.normalize_origin(env.get(vendor_key.BASE_URL_ENV)))


def judge_host(provider: str, environ: Optional[Mapping[str, str]] = None) -> Optional[str]:
    """The host the LLM judge's calls will go to (None for mock and unknown providers)."""
    env = _merged_env(environ)
    if provider == "openrouter":
        return vendor_key.host_of(vendor_key.normalize_origin(env.get(vendor_key.BASE_URL_ENV)))
    if provider == "ollama":
        return vendor_key.host_of(env.get("OLLAMA_BASE_URL") or "http://localhost:11434") or None
    return JUDGE_HOSTS.get(provider)


# ---------------------------------------------------------------------------
# The key's room (D201) and the snapshot (D207): free reads, only with a key
# ---------------------------------------------------------------------------


@dataclass
class KeyContext:
    """What the free reads say: the key's amounts and the model's snapshot."""

    status: Optional[vendor_key.KeyStatus] = None
    snapshot: Optional[str] = None
    problem: Optional[str] = None  # Why the key could not be read
    has_key: bool = False
    problem_kind: Optional[str] = None  # vendor_key's error kind (auth: the key was refused)

    @property
    def refused(self) -> bool:
        """The vendor refused the key (401/403): every paid call would fail."""
        return self.problem_kind == vendor_key.AUTH


def key_context(
    *,
    model: Optional[str] = DEFAULT_JEV_MODEL,
    environ: Optional[Mapping[str, str]] = None,
    fetch: Optional[vendor_key.Fetch] = None,
) -> KeyContext:
    """Read the key and the snapshot when there is a key; nothing at all without one."""
    env = os.environ if environ is None else environ
    key, origin = vendor_key.resolve(env)
    if key is None:
        return KeyContext()
    context = KeyContext(has_key=True)
    try:
        context.status = vendor_key.read_key_status(key, origin, fetch=fetch)
    except vendor_key.KeyCheckError as exc:
        context.problem = f"{exc.kind}: {exc}"
        context.problem_kind = exc.kind
    if model:
        context.snapshot = vendor_key.model_snapshot(model, origin, fetch=fetch)
    return context


# ---------------------------------------------------------------------------
# Inputs (D204, D205)
# ---------------------------------------------------------------------------


def sha256_file(path: Path) -> Optional[str]:
    """The SHA-256 of a file, None when it does not exist."""
    if not path.is_file():
        return None
    with open(path, "rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def tree_dirty(root: Optional[Path] = None) -> bool:
    """
    Whether tracked files have uncommitted changes (`git status --porcelain
    --untracked-files=no`), taken when a run starts (D204). Outside a git
    checkout, or when git cannot run, the tree counts as dirty: a run whose
    source cannot be named is not publishable.
    """
    git = shutil.which("git")
    root = root or repo_root()
    if git is None or root is None:
        return True
    try:
        # Fixed arguments and no shell.
        result = subprocess.run(  # nosec B603
            [git, "-C", str(root), "status", "--porcelain", "--untracked-files=no"],
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return True
    return result.returncode != 0 or bool(result.stdout.strip())


def _mem_available_mb() -> int:
    try:
        with open("/proc/meminfo", encoding="utf-8") as f:
            for line in f:
                if line.startswith("MemAvailable:"):
                    return int(line.split()[1]) // 1024
    except (OSError, ValueError, IndexError):
        pass
    return 0


def _platform() -> str:
    try:
        with open("/proc/version", encoding="utf-8") as f:
            if "microsoft" in f.read().lower():
                return "WSL2"
    except OSError:
        pass
    return (platform.system() or "unknown")[:64]


def host_env(region: str) -> Dict[str, object]:
    """benchmark-run.v1 inputs.host_env: the machine's conditions at run start (D205)."""
    try:
        load = [round(max(0.0, value), 2) for value in os.getloadavg()]
    except OSError:
        load = [0.0, 0.0, 0.0]
    return {
        "mem_available_mb": _mem_available_mb(),
        "loadavg": load,
        "platform": _platform(),
        "region": (region or "local")[:64],
    }


# ---------------------------------------------------------------------------
# The estimate
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ExampleSize:
    """Token counts of one example: everything it sends, and the answer alone."""

    total: int
    response: int


def example_sizes(examples: Sequence[BenchExample]) -> List[ExampleSize]:
    return [
        ExampleSize(
            total=sum(
                estimate_tokens(text)
                for text in (example.query, example.response, *example.context)
            ),
            response=estimate_tokens(example.response),
        )
        for example in examples
    ]


def typical_sizes(n: int) -> List[ExampleSize]:
    return [ExampleSize(TYPICAL_EXAMPLE_TOKENS, TYPICAL_RESPONSE_TOKENS)] * max(0, n)


def prompt_overheads(
    evaluators: Sequence[str] = ("groundedness@1", "contradiction@1")
) -> Dict[str, int]:
    """
    Tokens the LLM judge's prompts add around an example, measured from the
    prompts the bench sends: the same-checks system prompt, question block
    and answer schema; the decomposer's and the verifier's system prompts.
    The fixed fallbacks stand in when the prompts cannot be loaded.
    """
    try:
        import json

        from truthtable.graphs.nodes.decomposer import DECOMPOSER_SYSTEM_PROMPT
        from truthtable.graphs.nodes.verifier import VERIFIER_SYSTEM_PROMPT

        from .judges import (
            SAME_CHECKS_SYSTEM_PROMPT,
            load_evaluators,
            same_checks_prompt,
            same_checks_schema,
        )

        questions = load_evaluators(list(evaluators))
        empty = BenchExample(
            id="plan",
            dataset="plan",
            level="response",
            query="",
            response="",
            context=(),
            label_hallucinated=False,
        )
        return {
            "same_checks": estimate_tokens(SAME_CHECKS_SYSTEM_PROMPT)
            + estimate_tokens(same_checks_prompt(empty, questions))
            + estimate_tokens(json.dumps(same_checks_schema(questions))),
            "decompose": estimate_tokens(DECOMPOSER_SYSTEM_PROMPT),
            "verify": estimate_tokens(VERIFIER_SYSTEM_PROMPT) + 75,
        }
    except (OSError, ValueError, KeyError, ImportError):
        return {
            "same_checks": SAME_CHECKS_OVERHEAD_TOKENS,
            "decompose": DECOMPOSE_OVERHEAD_TOKENS,
            "verify": VERIFY_OVERHEAD_TOKENS,
        }


def _price(table: PriceTable, provider: str, model: str) -> Tuple[float, float]:
    entry = table.lookup(provider, model)
    if entry is None:
        return (math.nan, math.nan)
    return entry.input_per_mtok, entry.output_per_mtok


@dataclass
class Estimate:
    """The price-table estimate of a step, per system (USD)."""

    by_system: Dict[str, float] = field(default_factory=dict)

    @property
    def total(self) -> float:
        return sum(value for value in self.by_system.values() if math.isfinite(value))

    @property
    def unpriced(self) -> List[str]:
        return [system for system, value in self.by_system.items() if not math.isfinite(value)]


def estimate(
    systems: Sequence[str],
    sizes: Sequence[ExampleSize],
    *,
    full_audit_n: int,
    judge_provider: str,
    judge_model: str,
    jev_model: str = DEFAULT_JEV_MODEL,
    table: Optional[PriceTable] = None,
) -> Estimate:
    """What each system is expected to cost on these examples [estimate]."""
    table = table or active_price_table()
    overheads = prompt_overheads()
    out = Estimate()
    jev_in, _ = _price(table, "openrouter", jev_model)
    judge_in, judge_out = (
        (0.0, 0.0) if judge_provider == "mock" else _price(table, judge_provider, judge_model)
    )
    if GATE in systems:
        # Jev is priced on input only; its output (a few decisions) is free.
        out.by_system[GATE] = (
            sum((size.total + GATE_OVERHEAD_TOKENS) * jev_in for size in sizes) / 1e6
        )
    if LLM_SAME_CHECKS in systems:
        out.by_system[LLM_SAME_CHECKS] = (
            sum(
                (size.total + overheads["same_checks"]) * judge_in
                + SAME_CHECKS_OUTPUT_TOKENS * judge_out
                for size in sizes
            )
            / 1e6
        )
    if LLM_FULL_AUDIT in systems:
        audited = sizes if full_audit_n <= 0 else sizes[: min(full_audit_n, len(sizes))]
        out.by_system[LLM_FULL_AUDIT] = (
            sum(
                (overheads["decompose"] + size.response) * judge_in
                + DECOMPOSE_OUTPUT_TOKENS * judge_out
                + CLAIMS_PER_ANSWER
                * (
                    (size.total - size.response + overheads["verify"]) * judge_in
                    + VERIFY_OUTPUT_TOKENS * judge_out
                )
                for size in audited
            )
            / 1e6
        )
    return out


# ---------------------------------------------------------------------------
# The printed plan
# ---------------------------------------------------------------------------


@dataclass
class PlanInputs:
    """Everything a plan shows."""

    step: str
    what: str  # e.g. "200 RAGTruth-QA test answers (prepared, seed 13)"
    systems: Sequence[str]
    estimate: Estimate
    cap: vendor_key.CapDecision
    resume_key: Optional[str]
    ledger_path: Optional[Path]
    hosts: Mapping[str, Optional[str]]
    key: KeyContext
    gate_eval: Optional[Path] = None
    notes: Sequence[str] = ()


def _usd(value: float) -> str:
    return f"${value:.4f}" if value < 1 else f"${value:.2f}"


def render_plan(plan: PlanInputs, *, sha256: Callable[[Path], Optional[str]] = sha256_file) -> str:
    """The plan as plain lines (the first one says how to run it for real)."""
    lines = [
        f"{plan.step}: dry run, nothing is spent. It spends money on your OpenRouter key; "
        f"to run it: make {plan.step} CONFIRM=1",
        f"  examples    {plan.what}",
        f"  systems     {', '.join(plan.systems)}",
    ]
    parts = ", ".join(
        f"{system} {_usd(value)}" if math.isfinite(value) else f"{system} unpriced"
        for system, value in plan.estimate.by_system.items()
    )
    lines.append(
        f"  estimate    {_usd(plan.estimate.total)} [estimate: price table x example tokens]"
        + (f" ({parts})" if parts else "")
    )
    lines.append(f"  cap         {plan.cap.describe()}")
    if plan.cap.refused:
        lines.append(f"  REFUSED     {plan.cap.refused}")
    if plan.resume_key:
        where = f" ({plan.ledger_path.name})" if plan.ledger_path else ""
        lines.append(
            f"  resume key  {plan.resume_key}: {_usd(plan.cap.spent_usd)} spent in earlier "
            f"sessions{where}"
        )
    hosts = ", ".join(f"{name} {host or 'none'}" for name, host in plan.hosts.items())
    lines.append(f"  hosts       {hosts} (from the configuration)")
    if not plan.key.has_key:
        lines.append(f"  key         {NO_KEY}")
        lines.append(f"  snapshot    unknown ({NO_KEY})")
    else:
        lines.append(
            "  key         "
            + (
                f"{plan.key.status.host}: {plan.key.status.describe()}"
                if plan.key.status is not None
                else f"could not be read ({plan.key.problem})"
            )
        )
        lines.append(
            f"  snapshot    {plan.key.snapshot or 'not listed'} (OpenRouter models list, free)"
        )
    if plan.gate_eval is not None:
        digest = sha256(plan.gate_eval)
        lines.append(
            f"  gate-eval   sha256 {digest}"
            if digest
            else "  gate-eval   not built: make gate-eval"
        )
    for note in plan.notes:
        lines.append(f"  note        {note}")
    return "\n".join(lines)
