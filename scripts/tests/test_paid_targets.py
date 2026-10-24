"""Every make target that spends money is a dry run unless CONFIRM=1 (D204, D232).

A target whose help line names a cost (``USD`` or ``$<digits>``) must branch on CONFIRM before
its first command, or hand CONFIRM to a script that does, or run only on an attempt that a
confirmed run created. The exceptions are listed with their reason (D232 records them). ``make
-n`` then proves the dry run of ``eval-compare`` sends nothing to a paid judge, and that
``calibrate`` writes only with ``WRITE=1``.

Standard library only; ``make -n`` prints the recipe without running it.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess  # nosec B404 - runs make with fixed arguments
import unittest
from pathlib import Path
from typing import Dict, List

ROOT = Path(__file__).resolve().parents[2]
MAKEFILES = [ROOT / "Makefile", *sorted((ROOT / "mk").glob("*.mk"))]

TARGET_RE = re.compile(r"^([A-Za-z0-9][A-Za-z0-9_.-]*):[^=]*##\s*(.*)$")
COST_RE = re.compile(r"USD|\$\d")
GATE_RE = re.compile(r"^if(n?eq)\s*\(\$\(CONFIRM\),\s*1\)\s*$")

# Targets whose help names a cost but which need no CONFIRM=1, and why.
NOT_GATED: Dict[str, str] = {
    "jev-check": "one probe call billed about USD 0.00001, the check an owner runs first",
    "doctor": "free; only an explicit --probe sends one test call of about USD 0.00001",
}
# Targets that hand CONFIRM to a script which prints its plan and spends nothing without it.
DELEGATED: Dict[str, str] = {
    "record-demo": "scripts/record_demo.py",
}
# Targets that run only on what a confirmed run created, and spend inside its caps: the script
# refuses an attempt that is not under review (scripts/tests/test_launch_video.py proves it), and
# an attempt exists only after make record-demo CONFIRM=1, whose ledger counts these calls.
ON_A_CONFIRMED_ATTEMPT: Dict[str, str] = {
    "launch-video-stack": 'scripts/launch_video.py" stack --attempt',
}


def paid_targets() -> Dict[str, List[str]]:
    """target -> its recipe lines (up to the next target), for help lines that name a cost."""
    out: Dict[str, List[str]] = {}
    for path in MAKEFILES:
        lines = path.read_text(encoding="utf-8").splitlines()
        for i, line in enumerate(lines):
            m = TARGET_RE.match(line)
            if not m or not COST_RE.search(m.group(2)):
                continue
            body: List[str] = []
            for nxt in lines[i + 1 :]:
                if TARGET_RE.match(nxt) or (nxt and not nxt[0].isspace() and nxt.endswith(":")):
                    break
                body.append(nxt)
            out[m.group(1)] = body
    return out


class PaidTargetsAreDryRuns(unittest.TestCase):
    def test_the_scan_finds_the_known_paid_targets(self) -> None:
        found = set(paid_targets())
        for target in (
            "bench-lite",
            "bench-smoke",
            "calib-pass",
            "eval-live",
            "jev-capture",
            "eval-compare",
            "record-demo",
            "launch-video-stack",
        ):
            self.assertIn(target, found)

    def test_every_paid_target_branches_on_confirm_first(self) -> None:
        for target, body in paid_targets().items():
            if target in NOT_GATED:
                continue
            with self.subTest(target=target):
                first = next(
                    (ln for ln in body if ln.strip() and not ln.lstrip().startswith("#")), ""
                )
                if target in DELEGATED:
                    self.assertIn('CONFIRM="$(CONFIRM)"', first)
                    self.assertIn(DELEGATED[target], first)
                    continue
                if target in ON_A_CONFIRMED_ATTEMPT:
                    recipe = "\n".join(body)
                    self.assertIn(ON_A_CONFIRMED_ATTEMPT[target], recipe)
                    # It never starts a paid run itself: no record_demo.py, no sub-make.
                    self.assertNotIn("record_demo.py", recipe, target)
                    self.assertNotIn("$(MAKE)", recipe, target)
                    continue
                self.assertRegex(first, GATE_RE, f"{target} spends before checking CONFIRM=1")


@unittest.skipUnless(shutil.which("make"), "make is not installed")
class EvalCompareDryRun(unittest.TestCase):
    def make_n(self, *args: str) -> str:
        env = {k: v for k, v in os.environ.items() if k not in ("CONFIRM", "MAKEFLAGS", "MFLAGS")}
        r = subprocess.run(  # nosec B603 B607 - fixed arguments
            ["make", "-n", "--no-print-directory", "eval-compare", *args],
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    def test_without_confirm_it_prints_the_plan_and_calls_no_judge(self) -> None:
        out = self.make_n()
        self.assertIn("a dry run, nothing is spent", out)
        self.assertIn("make eval-compare CONFIRM=1", out)
        self.assertIn("[estimate]", out)
        self.assertNotIn("evals.run_eval", out)
        self.assertNotIn("evals.datasets.download", out)

    def test_with_confirm_it_runs_each_judge_on_the_bounded_set(self) -> None:
        out = self.make_n("CONFIRM=1", "EVAL_COMPARE_N=6")
        for provider in ("ollama", "openai", "anthropic"):
            self.assertRegex(out, rf"evals\.run_eval .*--limit 6 --provider {provider}\b")
        self.assertIn("halueval --samples 6", out)
        self.assertNotIn("nothing is spent", out)


@unittest.skipUnless(shutil.which("make"), "make is not installed")
class CalibrateWritesOnlyWhenAsked(unittest.TestCase):
    def recipe(self, *args: str) -> str:
        env = {k: v for k, v in os.environ.items() if k not in ("WRITE", "MAKEFLAGS", "MFLAGS")}
        r = subprocess.run(  # nosec B603 B607 - fixed arguments
            ["make", "-n", "--no-print-directory", "calibrate", "EVALUATOR=groundedness@1", "IN=-"]
            + list(args),
            cwd=ROOT,
            env=env,
            capture_output=True,
            text=True,
            timeout=60,
            check=False,
        )
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout

    def test_only_write_1_writes_config_calibration(self) -> None:
        self.assertIn("--write", self.recipe("WRITE=1"))
        for args in ((), ("WRITE=0",), ("WRITE=no",)):
            with self.subTest(args=args):
                self.assertNotIn("--write", self.recipe(*args))


if __name__ == "__main__":
    unittest.main()
