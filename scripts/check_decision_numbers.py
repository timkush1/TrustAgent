#!/usr/bin/env python3
"""List every lead decision number (D<n>) the v2 plans define and check that none is defined twice.

P5 defined one number twice (CO-1). P6 therefore took a block of its own (D200-D219) instead of
the next free number, and Step 0 of the P6 plan (docs/v2/P6-PLAN.md section 2.1) runs this check:

* every ``D<n>`` defined in the P3-P6 plans (a table row whose first cell is the number);
* every ``D<n>`` defined in SPEC Appendices E-H (a ``- **D<n> (...)**`` bullet or a table row);
* every ``D<n>`` defined in the bodies of the P5 integration merges (a ``- D<n> (...)`` line).

It fails when a number is defined in two documents, defined twice in one document, defined by
two different merges, when a P5 number reaches the P6 block, or when a P6 number falls outside
it. A number a merge defined but SPEC Appendix G does not record is reported as "cited by
commit" (the P6 plan then cites it by commit).

Standard library only. Usage::

    python3 scripts/check_decision_numbers.py [--merges A..B] [--list]
"""

from __future__ import annotations

import argparse
import re
import subprocess  # nosec B404 - runs git with fixed arguments
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Sequence, Tuple

ROOT = Path(__file__).resolve().parents[1]
SPEC = "docs/v2/SPEC.md"
PLANS: Tuple[Tuple[str, str], ...] = (
    ("P3", "docs/v2/P3-PLAN.md"),
    ("P4", "docs/v2/P4-PLAN.md"),
    ("P5", "docs/v2/P5-PLAN.md"),
    ("P6", "docs/v2/P6-PLAN.md"),
)
# SPEC appendix letter -> the phase whose errata it holds.
APPENDICES: Dict[str, str] = {"E": "P3", "F": "P4", "G": "P5", "H": "P6"}
# The P5 integration merges: after the P4 merge into feat/v2-jev-gateway, up to the P5 merge.
DEFAULT_MERGES = "a03ecb3..b39e61c"
P6_BLOCK = (200, 219)

NUM = r"D(\d+)([a-z]?)"
ROW_RE = re.compile(r"^\|\s*\**\s*" + NUM + r"\s*\**\s*\|")
BULLET_RE = re.compile(r"^\s*[-*]\s+\*\*((?:D\d+[a-z]?\s*(?:,|and)\s*)*D\d+[a-z]?)\b[^*]*\*\*")
COMMIT_RE = re.compile(r"^\s*-\s+((?:D\d+[a-z]?\s*(?:,|and)\s*)*D\d+[a-z]?)\s*\(")
ID_RE = re.compile(NUM)
AMEND_RE = re.compile(r"\((?:refined|amended|continued)\b", re.IGNORECASE)
APPENDIX_RE = re.compile(r"^## Appendix ([A-Z])\b")


@dataclass(frozen=True)
class Definition:
    ident: str  # e.g. "D76b"
    number: int  # e.g. 76
    phase: str  # "P3".."P6"
    where: str  # "docs/v2/P5-PLAN.md:207" or "commit 3e3eea4"
    kind: str  # "plan", "spec", "commit"


@dataclass
class Report:
    definitions: List[Definition] = field(default_factory=list)
    errors: List[str] = field(default_factory=list)
    cited_by_commit: List[str] = field(default_factory=list)


def _ids(text: str) -> List[Tuple[str, int]]:
    return [(f"D{m.group(1)}{m.group(2)}", int(m.group(1))) for m in ID_RE.finditer(text)]


def plan_definitions(text: str, phase: str, path: str) -> List[Definition]:
    """Table rows whose first cell is exactly one decision number."""
    out = []
    for lineno, line in enumerate(text.splitlines(), 1):
        m = ROW_RE.match(line)
        if m:
            ident, number = f"D{m.group(1)}{m.group(2)}", int(m.group(1))
            out.append(Definition(ident, number, phase, f"{path}:{lineno}", "plan"))
    return out


def spec_definitions(text: str, path: str = SPEC) -> List[Definition]:
    """Decision bullets and decision rows inside SPEC Appendices E-H."""
    out = []
    phase: Optional[str] = None
    for lineno, line in enumerate(text.splitlines(), 1):
        a = APPENDIX_RE.match(line)
        if a:
            phase = APPENDICES.get(a.group(1))
            continue
        if line.startswith("## ") and not line.startswith("## Appendix"):
            phase = None
        if phase is None:
            continue
        where = f"{path}:{lineno}"
        b = BULLET_RE.match(line)
        if b and AMEND_RE.search(b.group(0)):
            continue  # "- **D92 (refined):**" amends the earlier bullet; it defines nothing new
        if b:
            out += [Definition(i, n, phase, where, "spec") for i, n in _ids(b.group(1))]
            continue
        r = ROW_RE.match(line)
        if r:
            ident, number = f"D{r.group(1)}{r.group(2)}", int(r.group(1))
            out.append(Definition(ident, number, phase, where, "spec"))
    return out


def commit_definitions(bodies: Iterable[Tuple[str, str]], phase: str = "P5") -> List[Definition]:
    """``- D<n> (...)`` lines in merge-commit bodies, given as (short sha, body) pairs."""
    out = []
    for sha, body in bodies:
        seen = set()
        for line in body.splitlines():
            m = COMMIT_RE.match(line)
            if not m:
                continue
            for ident, number in _ids(m.group(1)):
                if ident not in seen:
                    seen.add(ident)
                    out.append(Definition(ident, number, phase, f"commit {sha}", "commit"))
    return out


def merge_bodies(rev_range: str, root: Path = ROOT) -> List[Tuple[str, str]]:
    """(short sha, body) of every merge in ``rev_range``; raises when git cannot read it."""
    proc = subprocess.run(  # nosec B603 B607 - fixed git arguments, no shell
        ["git", "-C", str(root), "log", "--merges", "--format=%h%x00%B%x01", rev_range],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"git log {rev_range}: {proc.stderr.strip()}")
    out = []
    for chunk in proc.stdout.split("\x01"):
        chunk = chunk.strip("\n")
        if "\x00" in chunk:
            sha, body = chunk.split("\x00", 1)
            out.append((sha.strip(), body))
    return out


def check(defs: Sequence[Definition]) -> Report:
    report = Report(definitions=list(defs))
    by_id: Dict[str, List[Definition]] = {}
    for d in defs:
        by_id.setdefault(d.ident, []).append(d)

    for ident, items in sorted(by_id.items(), key=lambda kv: (kv[1][0].number, kv[0])):
        docs = [d for d in items if d.kind != "commit"]
        commits = [d for d in items if d.kind == "commit"]
        if len(docs) > 1:
            report.errors.append(
                f"{ident} is defined {len(docs)} times: " + ", ".join(d.where for d in docs)
            )
        if len({d.where for d in commits}) > 1:
            report.errors.append(
                f"{ident} is defined by {len(commits)} merges: "
                + ", ".join(d.where for d in commits)
            )
        if commits and not any(d.phase == commits[0].phase and d.kind == "spec" for d in docs):
            report.cited_by_commit.append(f"{ident} ({commits[0].where})")
        phases = {d.phase for d in docs}
        if len(phases) > 1:
            report.errors.append(f"{ident} belongs to more than one phase: {sorted(phases)}")

    lo, hi = P6_BLOCK
    for d in defs:
        if d.phase in ("P3", "P4", "P5") and d.number >= lo:
            report.errors.append(
                f"{d.ident} ({d.phase}, {d.where}) reaches the P6 block D{lo}-D{hi}"
            )
        if d.phase == "P6" and not lo <= d.number <= hi:
            report.errors.append(f"{d.ident} ({d.where}) is a P6 number outside D{lo}-D{hi}")
    return report


def ranges(numbers: Iterable[int], suffixed: Iterable[str] = ()) -> str:
    nums = sorted(set(numbers))
    if not nums:
        return "none"
    parts, start, prev = [], nums[0], nums[0]
    for n in nums[1:]:
        if n != prev + 1:
            parts.append(f"D{start}" if start == prev else f"D{start}-D{prev}")
            start = n
        prev = n
    parts.append(f"D{start}" if start == prev else f"D{start}-D{prev}")
    return ", ".join(parts + sorted(set(suffixed)))


def collect(root: Path = ROOT, merges: Optional[str] = DEFAULT_MERGES) -> List[Definition]:
    defs: List[Definition] = []
    for phase, rel in PLANS:
        path = root / rel
        if path.exists():
            defs += plan_definitions(path.read_text(encoding="utf-8"), phase, rel)
    defs += spec_definitions((root / SPEC).read_text(encoding="utf-8"))
    if merges:
        defs += commit_definitions(merge_bodies(merges, root))
    return defs


def main(argv: Optional[Sequence[str]] = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument(
        "--merges", default=DEFAULT_MERGES, help="git range of the P5 integration merges ('' skips)"
    )
    ap.add_argument("--list", action="store_true", help="print every definition")
    args = ap.parse_args(argv)

    try:
        defs = collect(ROOT, args.merges or None)
    except RuntimeError as exc:
        print(
            f"check_decision_numbers: {exc} (pass --merges '' on a shallow clone)", file=sys.stderr
        )
        return 2
    report = check(defs)

    for phase in ("P3", "P4", "P5", "P6"):
        for kind, label in (
            ("plan", "plan"),
            ("spec", "SPEC appendix"),
            ("commit", "merge bodies"),
        ):
            these = [d for d in defs if d.phase == phase and d.kind == kind]
            if these:
                plain = [d.number for d in these if d.ident == f"D{d.number}"]
                print(
                    f"{phase} {label}: {ranges(plain, [d.ident for d in these if d.ident != f'D{d.number}'])}"
                )
    if args.list:
        for d in sorted(defs, key=lambda d: (d.number, d.ident, d.where)):
            print(f"  {d.ident:6} {d.phase} {d.kind:6} {d.where}")
    p5 = [d.number for d in defs if d.phase in ("P3", "P4", "P5")]
    print(
        f"highest number before P6: D{max(p5) if p5 else 0}; P6 block: D{P6_BLOCK[0]}-D{P6_BLOCK[1]}"
    )
    if report.cited_by_commit:
        print("cited by commit (no SPEC appendix copy): " + ", ".join(report.cited_by_commit))
    for err in report.errors:
        print(f"ERROR: {err}", file=sys.stderr)
    if report.errors:
        return 1
    print("decision numbers: OK (no number defined twice; every P5 number is below the P6 block)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
