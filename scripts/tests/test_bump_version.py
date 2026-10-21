"""Unit tests for scripts/bump_version.py (D127) on temporary copies of the real mirror files.

The `npm run recording` step is replaced by a recorder, so no test runs npm or touches the
repository; one test checks the committed tree with a dry run, as the DX-RELEASE gate does.
"""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import re
import shutil
import stat
import sys
import unittest
from pathlib import Path
from typing import Dict, List, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import bump_version as bv  # noqa: E402
import check_version as cv  # noqa: E402
from test_check_version import ROOT, make_root  # noqa: E402

TODAY = "2026-11-05"


class Recorder:
    def __init__(self, code: int = 0) -> None:
        self.code = code
        self.calls: List[Tuple[List[str], Path]] = []

    def __call__(self, cmd: Sequence[str], cwd: Path) -> int:
        self.calls.append((list(cmd), cwd))
        return self.code


def digest(root: Path) -> Dict[str, str]:
    return {
        str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
        for p in sorted(root.rglob("*"))
        if p.is_file()
    }


class BumpTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root("1.4.2")
        self.rec = Recorder()

    def tearDown(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)

    def bump(self, *argv: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = bv.main(list(argv), root=self.root, runner=self.rec, today=TODAY)
        return code, out.getvalue(), err.getvalue()

    def read(self, rel: str) -> str:
        return (self.root / rel).read_text(encoding="utf-8")

    def test_dry_run_plans_every_mirror_and_writes_nothing(self) -> None:
        before = digest(self.root)
        code, out, err = self.bump("--dry-run", "1.5.0")
        self.assertEqual(code, 0, err)
        self.assertEqual(digest(self.root), before)
        self.assertEqual(self.rec.calls, [])
        for mirror in cv.MIRRORS:
            self.assertIn(f"{mirror.path}: {mirror.what} 1.4.2 -> 1.5.0", out)
        self.assertIn("then: npm run recording (in frontend-react)", out)
        self.assertIn("dry run, nothing written", out)

    def test_a_bump_rewrites_every_mirror_and_regenerates_the_recording(self) -> None:
        code, out, err = self.bump("1.5.0")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.read("VERSION"), "1.5.0\n")
        report = cv.check(self.root)
        self.assertEqual(report.errors, [])
        for mirror in cv.MIRRORS:
            self.assertEqual(set(cv.mirror_versions(self.root, mirror)), {"1.5.0"}, mirror.path)
        self.assertEqual(
            self.rec.calls, [(["npm", "run", "recording"], self.root / "frontend-react")]
        )
        self.assertIn("bump_version: ok: version 1.5.0", out)

    def test_only_the_version_lines_change(self) -> None:
        files = [m.path for m in cv.MIRRORS if m.path != "VERSION"]
        before = {rel: self.read(rel).splitlines() for rel in files}
        self.assertEqual(self.bump("--no-recording", "1.5.0")[0], 0)
        compose_lines = sum(
            "${TRUSTAGENT_VERSION:-" in line for line in before["docker-compose.yml"]
        )
        expected = {"frontend-react/package-lock.json": 2, "docker-compose.yml": compose_lines}
        for rel in files:
            after = self.read(rel).splitlines()
            self.assertEqual(len(after), len(before[rel]), rel)
            changed = [(a, b) for a, b in zip(before[rel], after) if a != b]
            self.assertEqual(len(changed), expected.get(rel, 1), rel)
            for old, new in changed:
                self.assertEqual(old.replace("1.4.2", "1.5.0"), new, rel)

    def test_the_changelog_heading_is_released(self) -> None:
        (self.root / cv.CHANGELOG).write_text(
            '# Changelog\n\n## Unreleased — 1.5.0 "Jev gateway" (in progress)\n\n- new\n\n'
            "## v1.4.2 — 2026-01-02\n",
            encoding="utf-8",
        )
        code, out, _ = self.bump("--no-recording", "1.5.0")
        self.assertEqual(code, 0)
        text = self.read(cv.CHANGELOG)
        self.assertIn(f'\n## v1.5.0 — {TODAY} "Jev gateway"\n\n- new\n', text)
        self.assertNotIn("Unreleased — 1.5.0", text)
        self.assertNotIn("(in progress)", text)
        self.assertEqual(cv.check(self.root, "v1.5.0").errors, [])
        self.assertIn(f"heading -> ## v1.5.0 — {TODAY}", out)

    def test_an_explicit_date_and_a_plain_heading(self) -> None:
        (self.root / cv.CHANGELOG).write_text("## Unreleased — 1.5.0\n\n- new\n", encoding="utf-8")
        self.assertEqual(self.bump("--no-recording", "--date", "2027-01-31", "1.5.0")[0], 0)
        self.assertEqual(self.read(cv.CHANGELOG), "## v1.5.0 — 2027-01-31\n\n- new\n")

    def test_without_an_unreleased_heading_the_changelog_is_kept_with_a_warning(self) -> None:
        before = self.read(cv.CHANGELOG)
        code, out, _ = self.bump("--no-recording", "1.4.3")
        self.assertEqual(code, 0)
        self.assertEqual(self.read(cv.CHANGELOG), before)
        self.assertIn("warning: CHANGELOG.md has no '## Unreleased — 1.4.3' heading", out)
        self.assertIn(
            "has no released heading '## v1.4.3'", cv.check(self.root, "v1.4.3").errors[0]
        )

    def test_refusals_write_nothing(self) -> None:
        cases = {
            "1.4.2": "VERSION is already 1.4.2",
            "1.4.1": "1.4.1 is older than VERSION 1.4.2: a downgrade is refused",
            "1.4.2-rc.1": "older than VERSION 1.4.2",
            "1.5": "'1.5' is not a semantic version",
            "1.5.0+build.7": "build metadata '+…' cannot be an image tag",
            "latest": "'latest' is not a semantic version",
        }
        before = digest(self.root)
        for version, message in cases.items():
            with self.subTest(version=version):
                code, _, err = self.bump(version)
                self.assertEqual(code, 1)
                self.assertIn("refused", err)
                self.assertIn(message, err)
        self.assertEqual(digest(self.root), before)
        self.assertEqual(self.rec.calls, [])

    def test_mirrors_that_already_disagree_are_refused(self) -> None:
        init = "backend-python/src/truthtable/__init__.py"
        (self.root / init).write_text(
            self.read(init).replace('"1.4.2"', '"1.4.0"'), encoding="utf-8"
        )
        before = digest(self.root)
        code, _, err = self.bump("1.5.0")
        self.assertEqual(code, 1)
        self.assertIn("the mirrors disagree with VERSION", err)
        self.assertIn(init, err)
        self.assertEqual(digest(self.root), before)

    def test_a_pre_release_then_its_release(self) -> None:
        self.assertEqual(self.bump("--no-recording", "2.0.0-rc.1")[0], 0)
        self.assertEqual(self.read("VERSION"), "2.0.0-rc.1\n")
        self.assertEqual(self.bump("--no-recording", "v2.0.0")[0], 0)
        self.assertEqual(self.read("VERSION"), "2.0.0\n")
        self.assertEqual(cv.check(self.root).errors, [])

    def test_overlays_on_the_old_default_follow_and_others_are_left(self) -> None:
        old = "image: x:${TRUSTAGENT_VERSION:-1.4.2}\n"
        (self.root / "docker-compose.same.yml").write_text(old, encoding="utf-8")
        (self.root / "docker-compose.edge.yml").write_text(
            "image: x:${TRUSTAGENT_VERSION:-edge}\n", encoding="utf-8"
        )
        code, out, _ = self.bump("--no-recording", "1.5.0")
        self.assertEqual(code, 0)
        self.assertEqual(
            self.read("docker-compose.same.yml"), "image: x:${TRUSTAGENT_VERSION:-1.5.0}\n"
        )
        self.assertEqual(
            self.read("docker-compose.edge.yml"), "image: x:${TRUSTAGENT_VERSION:-edge}\n"
        )
        self.assertIn("docker-compose.same.yml: default image tag 1.4.2 -> 1.5.0", out)

    def test_a_failed_recording_is_reported(self) -> None:
        self.rec.code = 1
        code, _, err = self.bump("1.5.0")
        self.assertEqual(code, 1)
        self.assertIn("the files are bumped, but npm run recording (in frontend-react) failed", err)
        self.assertIn("make recording", err)
        self.assertEqual(self.read("VERSION"), "1.5.0\n")

    def test_no_recording_skips_npm_and_says_so(self) -> None:
        code, out, _ = self.bump("--no-recording", "1.5.0")
        self.assertEqual(code, 0)
        self.assertEqual(self.rec.calls, [])
        self.assertIn("run `make recording` before committing", out)

    def test_a_bad_date_is_a_usage_error(self) -> None:
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit) as ctx:
                bv.main(["--date", "5 Nov", "1.5.0"], root=self.root, runner=self.rec)
        self.assertEqual(ctx.exception.code, 2)

    def test_rewritten_files_keep_their_mode(self) -> None:
        path = self.root / "VERSION"
        os.chmod(path, 0o600)
        self.assertEqual(self.bump("--no-recording", "1.5.0")[0], 0)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(sorted(p.name for p in self.root.glob(".VERSION.*")), [])


class RunRecordingTest(unittest.TestCase):
    """The real runner (the other tests inject a Recorder): exit codes and a missing program."""

    def test_the_exit_code_and_the_working_directory_reach_the_caller(self) -> None:
        cwd = make_root()
        try:
            probe = "import os, sys; sys.exit(3 if os.path.isfile('VERSION') else 4)"
            self.assertEqual(bv.run_recording([sys.executable, "-c", probe], cwd), 3)
        finally:
            shutil.rmtree(cwd, ignore_errors=True)

    def test_a_program_that_is_not_installed_is_127(self) -> None:
        self.assertEqual(bv.run_recording(["trustagent-no-such-program-x9"], ROOT), 127)


RECORDED = "frontend-react/demo-data/recorded"


class RecordedTapeTest(unittest.TestCase):
    """The bump stamps the simulated tape only: a recorded tape keeps the version it was recorded
    with (it is evidence of that run), so no step of the bump may write under demo-data/recorded."""

    def setUp(self) -> None:
        self.root = make_root("1.9.0")
        self.addCleanup(shutil.rmtree, self.root, True)
        tape = self.root / RECORDED
        (tape / "traces").mkdir(parents=True)
        # Every string a bump rewrites elsewhere: the bare version, a compose default and a
        # CHANGELOG-style heading.
        (tape / "manifest.json").write_text(
            '{"kind": "recorded", "trustagent_version": "1.9.0",\n'
            ' "note": "${TRUSTAGENT_VERSION:-1.9.0}"}\n',
            encoding="utf-8",
        )
        (tape / "traces" / "t1.json").write_text(
            '{"version": "1.9.0", "text": "## Unreleased — 2.0.0"}\n', encoding="utf-8"
        )
        # A compose overlay inside the tape directory is not an overlay of the project.
        (tape / "docker-compose.tape.yml").write_text(
            "image: x:${TRUSTAGENT_VERSION:-1.9.0}\n", encoding="utf-8"
        )

    def tape(self) -> Dict[str, str]:
        return digest(self.root / RECORDED)

    def test_a_bump_leaves_the_recorded_tape_alone(self) -> None:
        before = self.tape()
        rec = Recorder()
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = bv.main(["2.0.0"], root=self.root, runner=rec, today=TODAY)
        self.assertEqual(code, 0, err.getvalue())
        self.assertEqual((self.root / "VERSION").read_text(encoding="utf-8"), "2.0.0\n")
        self.assertEqual(self.tape(), before)
        self.assertNotIn("recorded", out.getvalue())
        # The one command it runs regenerates the simulated tape, in frontend-react.
        self.assertEqual(rec.calls, [(bv.RECORDING_CMD, self.root / bv.RECORDING_DIR)])

    def test_the_plan_names_no_recorded_path(self) -> None:
        _, changes, notes = bv.plan_bump(self.root, "2.0.0", TODAY)
        self.assertTrue(changes)
        for path in changes:
            self.assertFalse(path.replace("\\", "/").startswith(RECORDED), path)
            self.assertNotIn("demo-data", path)
        self.assertFalse([n for n in notes if "recorded" in n])

    def test_the_recording_command_writes_the_simulated_tape(self) -> None:
        # `npm run recording` is the bump's only other writer: its script must not name the
        # recorded tape, and its default output directory is demo-data/simulated.
        pkg = json.loads((ROOT / bv.RECORDING_DIR / "package.json").read_text(encoding="utf-8"))
        script = pkg["scripts"][bv.RECORDING_CMD[-1]]
        self.assertNotIn("recorded", script)
        self.assertNotIn("--out", script)
        source = script.split()[-1]
        builder = (ROOT / bv.RECORDING_DIR / source).read_text(encoding="utf-8")
        default_out = re.search(r"out:\s*\{[^}]*default:\s*join\(web,\s*'([^']+)'\)", builder)
        self.assertIsNotNone(default_out, "the recording builder's --out default moved")
        self.assertEqual(default_out.group(1), "demo-data/simulated")


class RepositoryDryRunTest(unittest.TestCase):
    def test_the_committed_tree_plans_the_release_bump(self) -> None:
        # The release bump of the 2.0 line plans every mirror, the CHANGELOG heading of the
        # consolidated section, and nothing under the recorded tape.
        version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
        if cv.compare_semver("2.0.0", version) <= 0:
            # After the release bump the consolidated section carries the released heading.
            changelog = (ROOT / cv.CHANGELOG).read_text(encoding="utf-8")
            self.assertRegex(changelog, r'(?m)^## v2\.0\.0 — \d{4}-\d{2}-\d{2} "Jev gateway"$')
            return
        before = digest(ROOT / "backend-go" / "internal" / "version")
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            code = bv.main(["--dry-run", "2.0.0"], runner=Recorder())
        self.assertEqual(code, 0, out.getvalue())
        self.assertIn("CHANGELOG.md: heading -> ## v2.0.0 — ", out.getvalue())
        self.assertIn('"Jev gateway"', out.getvalue())
        self.assertNotIn("recorded", out.getvalue())
        self.assertEqual(digest(ROOT / "backend-go" / "internal" / "version"), before)

    def test_the_committed_tree_plans_a_patch_bump(self) -> None:
        version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
        major, minor, patch, _ = cv.parse_semver(version)
        before = digest(ROOT / "backend-go" / "internal" / "version")
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            code = bv.main(["--dry-run", f"{major}.{minor}.{patch + 1}"], runner=Recorder())
        self.assertEqual(code, 0, out.getvalue())
        self.assertIn("dry run, nothing written", out.getvalue())
        self.assertEqual(digest(ROOT / "backend-go" / "internal" / "version"), before)


if __name__ == "__main__":
    unittest.main()
