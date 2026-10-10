#!/usr/bin/env python3
"""Set a new project version in VERSION and every mirror at once (D127; standard library only).

    python3 scripts/bump_version.py 2.0.0              # rewrite, then regenerate the recording
    python3 scripts/bump_version.py --dry-run 2.0.0    # print the plan, change nothing
    make version-bump V=2.0.0                          # the same (DRY_RUN=1 for the plan)

Steps, in this order:

1. Refuse a version that is not semver (X.Y.Z or X.Y.Z-pre; no "+build", which a Docker tag
   cannot hold) or that is not newer than VERSION (a downgrade, or the same version).
2. Refuse when the mirrors already disagree with VERSION (scripts/check_version.py names them):
   the bump would hide which copy was wrong.
3. Rewrite VERSION and its mirrors (the list in check_version.py: pyproject.toml, __init__.py,
   package.json, package-lock.json, version.go and the docker-compose.yml default image tag),
   plus any compose overlay whose default image tag still names the old version. Every new file
   content is computed before the first write, so a refused bump leaves every file as it was.
4. Rename the CHANGELOG.md heading ``## Unreleased — X.Y.Z …`` to ``## vX.Y.Z — <date> …`` (the
   "(in progress)" note is dropped). Without that heading the CHANGELOG is left alone with a
   warning: ``check_version.py --tag`` refuses the release until it has a ``## vX.Y.Z`` heading.
5. Re-run ``npm run recording`` in frontend-react: the simulated demo recording carries the
   version (``trustagent_version``), and ``make recording-check`` fails while it is stale.
   ``--no-recording`` skips it and says so.

It never commits, tags or pushes; the release steps after it are in docs/v2/SPEC.md §4.2 (P6).

Exit status: 0 bumped (or planned, with --dry-run), 1 refused or a step failed, 2 usage error.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Callable, Dict, List, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_version as cv  # noqa: E402

ROOT = cv.ROOT
RECORDING_CMD = ["npm", "run", "recording"]
RECORDING_DIR = "frontend-react"


class Refused(Exception):
    """The bump cannot go ahead; nothing was written."""


def unreleased_heading(new: str) -> "re.Pattern[str]":
    return re.compile(rf"(?m)^## Unreleased — {re.escape(new)}(?P<rest>(?:[ \t].*)?)$")


def changelog_edit(text: str, new: str, date: str) -> Tuple[Optional[str], Optional[str]]:
    """(new text, the heading it now has), or (None, None) when no Unreleased heading names it."""
    m = unreleased_heading(new).search(text)
    if not m:
        return None, None
    rest = re.sub(r"\s*\(in progress\)\s*$", "", m.group("rest")).rstrip()
    heading = f"## v{new} — {date}{rest}"
    return text[: m.start()] + heading + text[m.end() :], heading


def plan_bump(root: Path, new: str, date: str) -> Tuple[str, Dict[str, str], List[str]]:
    """(old version, {path: new content}, notes). Raises Refused; writes nothing."""
    try:
        cv.parse_semver(new)
    except ValueError:
        detail = " (build metadata '+…' cannot be an image tag)" if "+" in new else ""
        raise Refused(f"{new!r} is not a semantic version X.Y.Z or X.Y.Z-pre{detail}") from None
    try:
        old = cv.read_version(root)
    except cv.VersionError as exc:
        raise Refused(str(exc)) from None
    order = cv.compare_semver(new, old)
    if order == 0:
        raise Refused(f"VERSION is already {old}")
    if order < 0:
        raise Refused(f"{new} is older than VERSION {old}: a downgrade is refused")

    report = cv.check(root)
    if report.errors:
        raise Refused(
            "the mirrors disagree with VERSION; fix them first (check_version.py):\n  "
            + "\n  ".join(report.errors)
        )

    changes: Dict[str, str] = {}
    notes: List[str] = []
    try:
        for mirror in cv.MIRRORS:
            text = cv.read_text(root, mirror.path)
            changes[mirror.path] = mirror.write(text, old, new)
            notes.append(f"{mirror.path}: {mirror.what} {old} -> {new}")
    except cv.VersionError as exc:
        raise Refused(str(exc)) from None

    old_default = "${TRUSTAGENT_VERSION:-" + old + "}"
    for path in cv.overlay_files(root):
        text = path.read_text(encoding="utf-8")
        if old_default in text:
            changes[path.name] = text.replace(old_default, "${TRUSTAGENT_VERSION:-" + new + "}")
            notes.append(f"{path.name}: default image tag {old} -> {new}")

    changelog = root / cv.CHANGELOG
    if changelog.is_file():
        edited, heading = changelog_edit(changelog.read_text(encoding="utf-8"), new, date)
        if edited is not None:
            changes[cv.CHANGELOG] = edited
            notes.append(f"{cv.CHANGELOG}: heading -> {heading}")
        else:
            notes.append(
                f"warning: {cv.CHANGELOG} has no '## Unreleased — {new}' heading; left as it is "
                f"(check_version.py --tag v{new} needs a '## v{new}' heading before the release)"
            )
    else:
        notes.append(f"warning: no {cv.CHANGELOG}; nothing to rename")
    return old, changes, notes


def write_atomic(path: Path, text: str) -> None:
    """Replace ``path`` with ``text``, keeping its mode; a crash leaves the old file or the new."""
    mode = path.stat().st_mode & 0o7777 if path.exists() else 0o644
    fd, tmp = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="") as fh:
            fh.write(text)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise


Runner = Callable[[Sequence[str], Path], int]


def run_recording(cmd: Sequence[str], cwd: Path) -> int:
    # Resolve the program on PATH first: on Windows npm is npm.cmd, which a bare "npm" without a
    # shell does not find.
    argv = [shutil.which(cmd[0]) or cmd[0], *cmd[1:]]
    try:
        # A fixed argument list (no shell): `npm run recording` in frontend-react.
        return subprocess.run(argv, cwd=str(cwd), check=False).returncode  # nosec B603
    except FileNotFoundError:
        return 127


def main(
    argv: Optional[Sequence[str]] = None,
    root: Path = ROOT,
    runner: Runner = run_recording,
    today: Optional[str] = None,
) -> int:
    parser = argparse.ArgumentParser(
        prog="bump_version.py",
        description="Set a new version in VERSION and every mirror, rename the CHANGELOG "
        "heading and regenerate the demo recording (D127).",
    )
    parser.add_argument("version", help="the new version, X.Y.Z or X.Y.Z-pre (no leading v)")
    parser.add_argument(
        "--dry-run", action="store_true", help="print what would change; write and run nothing"
    )
    parser.add_argument(
        "--no-recording",
        action="store_true",
        help="do not re-run `npm run recording` (make recording later, or recording-check fails)",
    )
    parser.add_argument(
        "--date",
        help="the release date for the CHANGELOG heading, YYYY-MM-DD (default: today, UTC)",
    )
    args = parser.parse_args(argv)

    new = args.version[1:] if args.version.startswith("v") else args.version
    date = args.date or today or _dt.datetime.now(_dt.timezone.utc).date().isoformat()
    try:
        _dt.date.fromisoformat(date)
    except ValueError:
        parser.error(f"--date {date!r} is not YYYY-MM-DD")

    try:
        old, changes, notes = plan_bump(root, new, date)
    except Refused as exc:
        print(f"bump_version: refused: {exc}", file=sys.stderr)
        return 1

    prefix = "would change" if args.dry_run else "changing"
    print(f"bump_version: {old} -> {new} ({prefix} {len(changes)} files)")
    for note in notes:
        print(f"  {note}")
    rec = f"{' '.join(RECORDING_CMD)} (in {RECORDING_DIR})"
    if args.dry_run:
        print(
            f"  then: {rec}" if not args.no_recording else "  then: no recording (--no-recording)"
        )
        print("bump_version: dry run, nothing written")
        return 0

    for rel, text in changes.items():
        write_atomic(root / rel, text)

    if args.no_recording:
        print(f"bump_version: skipped {rec}; run `make recording` before committing")
    else:
        print(f"bump_version: {rec}")
        code = runner(RECORDING_CMD, root / RECORDING_DIR)
        if code != 0:
            print(
                f"bump_version: the files are bumped, but {rec} failed (exit {code}). Fix it "
                "(npm ci in frontend-react?) and run `make recording`; make recording-check "
                "fails until then.",
                file=sys.stderr,
            )
            return 1

    report = cv.check(root)
    if report.errors:  # pragma: no cover - plan_bump rewrote every mirror
        for error in report.errors:
            print(f"bump_version: {error}", file=sys.stderr)
        return 1
    print(
        f"✅ Version {new}. Review `git diff`, then commit; the release steps are in SPEC §4.2 P6."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
