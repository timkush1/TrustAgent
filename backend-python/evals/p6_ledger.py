"""
`make p6-ledger` (P6 M-10): a read-only sum of the P6 spend, reconciled
against the key's usage and the D201 caps.

It reads what the paid steps left behind and calls nothing but the free
`GET /api/v1/key` (only when there is a key):

- the bench ledgers, evals/.cache/<resume key>.ledger.jsonl (bench-smoke:
  smoke-p6; bench-lite: lite-p6), every session of a run;
- the calib-pass runs, evals/calibration/<run_id>/result.json (else the
  cost_usd of its gate.jsonl lines);
- the record ledger, tmp/record-demo/ledger.jsonl (VEND, D208), read by
  scripts/record_demo.py's own Ledger: each attempt counts once, at its final
  reading of the key, or at an upper bound when that reading failed;
- the result line of jev-capture from the journal tmp/p6/results.log, where
  its make target appends it. jev-check and doctor --probe print no result
  line (D232: one probe call, below the ledger's precision), so they have no
  row; the key's usage still counts them, as unattributed.

Each step is compared with its D201 cap (capture $0.01, smoke $0.30,
calib-pass $0.30 a pass, two at most, bench-lite $5, record-demo $4 in
total); the total with the $9.61 the caps add up to and the key's $8 limit.
The key's usage is the source of truth: what no file explains is shown as
unattributed. Exit 1 when a step went over its cap, else 0.
"""

import argparse
import importlib.util
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Sequence

from truthtable.config import repo_root

from . import plan as planning
from . import vendor_key

EVALS_DIR = Path(__file__).parent
CACHE_DIR = EVALS_DIR / ".cache"
CALIBRATION_DIR = EVALS_DIR / "calibration"

# D201: the hard caps of the paid steps, and the key's limit.
CAPS = {
    "jev-capture": 0.01,
    "bench-smoke": 0.30,
    "calib-pass": 0.30,
    "bench-lite": 5.00,
    "record-demo": 4.00,
}
CALIB_PASSES_ALLOWED = 2  # One re-run on a new card (D201)
CAPS_TOTAL = 9.61
KEY_LIMIT = 8.00
# Ledger names of the bench steps (fixed resume keys, D204).
BENCH_LEDGERS = {"bench-smoke": "smoke-p6", "bench-lite": "lite-p6"}
# Steps whose spend comes only from their result line (jev-check prints none, D232).
JOURNAL_STEPS = ("jev-capture",)
TOLERANCE_USD = 0.01  # Rounding between the vendor's usage and the files


@dataclass
class Step:
    """One step's spend and where it was read."""

    name: str
    spent_usd: float = 0.0
    cap_usd: Optional[float] = None
    sources: List[str] = field(default_factory=list)
    entries: int = 0

    @property
    def over_cap(self) -> bool:
        return self.cap_usd is not None and self.spent_usd > self.cap_usd + 1e-9


def _amount(row: Dict[str, Any]) -> float:
    for name in ("usd", "cost_usd", "spent_usd", "usage_delta_usd"):
        value = row.get(name)
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            return float(value)
    return 0.0


def _jsonl(path: Path) -> Iterable[Dict[str, Any]]:
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(row, dict):
                yield row


def bench_steps(cache_dir: Path) -> List[Step]:
    steps = []
    for name, key in BENCH_LEDGERS.items():
        step = Step(name, cap_usd=CAPS[name])
        path = cache_dir / f"{key}.ledger.jsonl"
        if path.is_file():
            rows = list(_jsonl(path))
            step.spent_usd = sum(_amount(row) for row in rows)
            step.entries = len(rows)
            step.sources.append(f"{path.name} ({len(rows)} charges)")
        steps.append(step)
    return steps


def calib_step(calibration_dir: Path) -> Step:
    step = Step("calib-pass", cap_usd=CAPS["calib-pass"])
    passes = 0
    if calibration_dir.is_dir():
        for run_dir in sorted(path for path in calibration_dir.iterdir() if path.is_dir()):
            result = run_dir / "result.json"
            gate = run_dir / "gate.jsonl"
            cost: Optional[float] = None
            if result.is_file():
                try:
                    value = json.loads(result.read_text(encoding="utf-8")).get("cost_usd")
                    cost = float(value) if isinstance(value, (int, float)) else None
                except (OSError, json.JSONDecodeError, AttributeError):
                    cost = None
            if cost is None and gate.is_file():
                cost = sum(_amount(row) for row in _jsonl(gate))
            if cost is None:
                continue
            passes += 1
            step.spent_usd += cost
            step.sources.append(f"{run_dir.name} ${cost:.4f}")
    step.entries = passes
    if passes > 1:
        step.cap_usd = CAPS["calib-pass"] * min(passes, CALIB_PASSES_ALLOWED)
    return step


class LedgerUnreadable(Exception):
    """A spend file p6-ledger cannot read: no total is printed rather than one that is too low."""


def _record_demo_module(root: Path) -> Any:
    """scripts/record_demo.py, the record ledger's writer: its event log (start, measure, end per
    attempt) is read by its own Ledger, so the two never count an attempt differently."""
    script = root / "scripts" / "record_demo.py"
    if not script.is_file():
        raise LedgerUnreadable(f"{script} is missing: the record ledger cannot be read")
    name = "_trustagent_record_demo"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, script)
    if spec is None or spec.loader is None:
        raise LedgerUnreadable(f"{script} cannot be loaded")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module  # dataclasses resolve their module while it executes
    path = list(sys.path)  # it puts scripts/ first for its envfile import; nothing else sees it
    try:
        spec.loader.exec_module(module)
    except BaseException:
        del sys.modules[name]
        raise
    finally:
        sys.path[:] = path
    return module


def record_step(
    path: Path, *, current_usage: Optional[float] = None, root: Optional[Path] = None
) -> Step:
    """record-demo: every attempt once (D208). An attempt whose final key reading failed counts at
    the key's usage since its start, or at its caps, as record_demo itself counts it."""
    step = Step("record-demo", cap_usd=CAPS["record-demo"])
    if not path.is_file():
        return step
    module = _record_demo_module(root or repo_root() or Path.cwd())
    try:
        accounts = module.Ledger(path).accounts(current_usage)
    except module.Refused as exc:
        raise LedgerUnreadable(str(exc)) from exc
    step.spent_usd = sum(float(a.counted) for a in accounts)
    step.entries = len(accounts)
    bounded = sum(1 for a in accounts if a.how != "reading")
    note = f", {bounded} at an upper bound" if bounded else ""
    step.sources.append(f"{path.name} ({len(accounts)} attempts{note})")
    return step


def journal_steps(journal: Path) -> List[Step]:
    """The steps only a result line tells about (each line's cost_usd)."""
    steps = {name: Step(name, cap_usd=CAPS.get(name)) for name in JOURNAL_STEPS}
    if journal.is_file():
        with open(journal, encoding="utf-8") as f:
            for line in f:
                if not line.startswith(vendor_key.RESULT_PREFIX + " "):
                    continue
                try:
                    result = json.loads(line.split(" ", 1)[1])
                except json.JSONDecodeError:
                    continue
                name = str(result.get("step") or "")
                if name in steps:
                    steps[name].spent_usd += _amount(result)
                    steps[name].entries += 1
        for step in steps.values():
            if step.entries:
                step.sources.append(f"{journal.name} ({step.entries} result lines)")
    return list(steps.values())


@dataclass
class Ledger:
    steps: List[Step]
    key: Optional[vendor_key.KeyStatus]
    key_problem: Optional[str] = None

    @property
    def total(self) -> float:
        return sum(step.spent_usd for step in self.steps)

    @property
    def unattributed(self) -> Optional[float]:
        if self.key is None or self.key.usage_usd is None:
            return None
        return self.key.usage_usd - self.total


def collect(
    *,
    cache_dir: Path,
    calibration_dir: Path,
    record_ledger: Path,
    journal: Path,
    key: Optional[planning.KeyContext] = None,
) -> Ledger:
    status = key.status if key is not None else None
    problem = key.problem if key is not None else None
    capture, *checks = journal_steps(journal)
    usage = status.usage_usd if status is not None else None
    steps = (
        [capture]
        + bench_steps(cache_dir)
        + [calib_step(calibration_dir), record_step(record_ledger, current_usage=usage)]
        + checks
    )
    return Ledger(steps=steps, key=status, key_problem=problem)


def _usd(value: float) -> str:
    return f"${value:.4f}"


def render(ledger: Ledger) -> str:
    lines = [
        "P6 spend (read-only: nothing is called but the free key check)",
        f"  {'step':<13} {'spent':>9} {'cap':>8}  source",
    ]
    for step in ledger.steps:
        cap = f"${step.cap_usd:.2f}" if step.cap_usd is not None else "-"
        flag = "  OVER CAP" if step.over_cap else ""
        source = "; ".join(step.sources) or "nothing recorded"
        lines.append(f"  {step.name:<13} {_usd(step.spent_usd):>9} {cap:>8}  {source}{flag}")
    lines.append(
        f"  {'total':<13} {_usd(ledger.total):>9} {'$' + format(CAPS_TOTAL, '.2f'):>8}  "
        "(the D201 caps add up to this)"
    )
    if ledger.key is not None:
        lines.append(f"  key           {ledger.key.host}: {ledger.key.describe()}")
        gap = ledger.unattributed
        if gap is not None:
            if gap < -TOLERANCE_USD:
                lines.append(
                    f"  reconcile     the files count ${-gap:.4f} more than the key used: a ledger "
                    "holds another key's spend, or an estimate charged in full"
                )
            else:
                lines.append(
                    f"  reconcile     ${max(gap, 0.0):.4f} of the key's usage is in no file here "
                    "(free checks, earlier use of the key, rounding)"
                )
        if ledger.key.limit_usd is not None and abs(ledger.key.limit_usd - KEY_LIMIT) > 1e-9:
            lines.append(
                f"  note          the key's limit is ${ledger.key.limit_usd:.2f}, not the planned ${KEY_LIMIT:.2f}"
            )
    elif ledger.key_problem:
        lines.append(f"  key           could not be read ({ledger.key_problem})")
    else:
        lines.append(f"  key           {planning.NO_KEY}: no reconciliation")
    return "\n".join(lines)


def main(argv: Optional[Sequence[str]] = None) -> int:
    root = repo_root() or Path.cwd()
    parser = argparse.ArgumentParser(description="Read-only sum of the P6 spend (D201)")
    parser.add_argument("--cache-dir", default=str(CACHE_DIR))
    parser.add_argument("--calibration-dir", default=str(CALIBRATION_DIR))
    parser.add_argument(
        "--record-ledger", default=str(root / "tmp" / "record-demo" / "ledger.jsonl")
    )
    parser.add_argument("--journal", default=str(root / "tmp" / "p6" / "results.log"))
    parser.add_argument("--json", action="store_true", help="Print one JSON object")
    args = parser.parse_args(argv)
    try:
        ledger = collect(
            cache_dir=Path(args.cache_dir),
            calibration_dir=Path(args.calibration_dir),
            record_ledger=Path(args.record_ledger),
            journal=Path(args.journal),
            key=planning.key_context(model=None, fetch=_key_fetch()),
        )
    except LedgerUnreadable as exc:
        print(f"p6-ledger: {exc}; no total is printed until it is fixed", file=sys.stderr)
        return 1
    if args.json:
        print(
            json.dumps(
                {
                    "steps": [
                        {
                            "step": step.name,
                            "spent_usd": round(step.spent_usd, 6),
                            "cap_usd": step.cap_usd,
                            "over_cap": step.over_cap,
                        }
                        for step in ledger.steps
                    ],
                    "total_usd": round(ledger.total, 6),
                    "key": ledger.key.to_json() if ledger.key is not None else None,
                    "unattributed_usd": (
                        round(ledger.unattributed, 6) if ledger.unattributed is not None else None
                    ),
                }
            )
        )
    else:
        print(render(ledger))
    return 1 if any(step.over_cap for step in ledger.steps) else 0


def _key_fetch() -> Optional[vendor_key.Fetch]:
    """The fetch the key read uses (tests replace it); None = urllib."""
    return None


if __name__ == "__main__":
    sys.exit(main())
