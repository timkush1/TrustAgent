"""Unit tests for scripts/check_version.py (D127): the mirrors, the overlays and --tag.

Each test works on a temporary copy of the real mirror files, so the line patterns are proved
against the formats the repository actually uses.
"""

from __future__ import annotations

import contextlib
import io
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from typing import List, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import check_version as cv  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]


def make_root(version: str = "1.4.2") -> Path:
    """A temporary repository with every mirror copied from this one and set to ``version``."""
    tmp = Path(tempfile.mkdtemp(prefix="version-"))
    current = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
    for mirror in cv.MIRRORS:
        dest = tmp / mirror.path
        dest.parent.mkdir(parents=True, exist_ok=True)
        text = (ROOT / mirror.path).read_text(encoding="utf-8")
        dest.write_text(mirror.write(text, current, version), encoding="utf-8")
    (tmp / cv.CHANGELOG).write_text(
        "# Changelog\n\n"
        f'## Unreleased — 9.0.0 "Next" (in progress)\n\n- work\n\n## v{version} — 2026-01-02\n\n'
        "- shipped\n",
        encoding="utf-8",
    )
    return tmp


def run_main(root: Path, *argv: str) -> Tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = cv.main(list(argv), root=root)
    return code, out.getvalue(), err.getvalue()


class SemverTest(unittest.TestCase):
    def test_precedence_follows_semver_2(self) -> None:
        # The ordering example of semver.org §11, plus the core-number cases.
        ordered = [
            "0.9.9",
            "1.0.0-alpha",
            "1.0.0-alpha.1",
            "1.0.0-alpha.beta",
            "1.0.0-beta",
            "1.0.0-beta.2",
            "1.0.0-beta.11",
            "1.0.0-rc.1",
            "1.0.0",
            "1.0.1",
            "1.1.0",
            "1.10.0",
            "2.0.0",
        ]
        for i, low in enumerate(ordered):
            self.assertEqual(cv.compare_semver(low, low), 0, low)
            for high in ordered[i + 1 :]:
                self.assertEqual(cv.compare_semver(low, high), -1, f"{low} < {high}")
                self.assertEqual(cv.compare_semver(high, low), 1, f"{high} > {low}")

    def test_refuses_what_is_not_a_semver_image_tag(self) -> None:
        for bad in ("1.0", "01.0.0", "1.0.0+build.5", "v1.0.0", "1.0.0-", "1.0.0-01", "edge", ""):
            with self.assertRaises(ValueError, msg=bad):
                cv.parse_semver(bad)
        self.assertEqual(cv.parse_semver("2.0.0-rc.1"), (2, 0, 0, ("rc", "1")))


class CheckTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root()

    def tearDown(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)

    def edit(self, rel: str, old: str, new: str, count: int = 1) -> None:
        path = self.root / rel
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text, rel)
        path.write_text(text.replace(old, new, count), encoding="utf-8")

    def test_the_repository_itself_is_consistent(self) -> None:
        report = cv.check(ROOT)
        self.assertEqual(report.errors, [])

    def test_a_consistent_copy_passes(self) -> None:
        report = cv.check(self.root)
        self.assertEqual((report.errors, report.warnings), ([], []))
        code, out, err = run_main(self.root)
        self.assertEqual(code, 0, err)
        self.assertIn("check_version: ok: version 1.4.2", out)

    def test_every_mirror_out_of_step_is_named(self) -> None:
        for mirror in cv.MIRRORS[1:]:
            with self.subTest(mirror=mirror.path):
                root = make_root()
                try:
                    path = root / mirror.path
                    text = path.read_text(encoding="utf-8")
                    path.write_text(mirror.write(text, "1.4.2", "1.4.3"), encoding="utf-8")
                    errors = cv.check(root).errors
                    self.assertEqual(len(errors), 1, errors)
                    self.assertIn(mirror.path, errors[0])
                    self.assertIn("1.4.3", errors[0])
                    self.assertIn("VERSION is 1.4.2", errors[0])
                finally:
                    shutil.rmtree(root, ignore_errors=True)

    def test_the_lockfile_root_package_is_checked_on_its_own(self) -> None:
        lock = "frontend-react/package-lock.json"
        text = (self.root / lock).read_text(encoding="utf-8")
        # Only packages[""].version (six-space indent) changes; the top-level one stays.
        head, sep, tail = text.partition('    "": {')
        (self.root / lock).write_text(
            head + sep + tail.replace('"version": "1.4.2"', '"version": "1.4.0"', 1),
            encoding="utf-8",
        )
        errors = cv.check(self.root).errors
        self.assertEqual(len(errors), 1, errors)
        self.assertIn(lock, errors[0])

    def test_one_stray_compose_default_is_caught(self) -> None:
        self.edit(
            "docker-compose.yml",
            "truthtable-dashboard:${TRUSTAGENT_VERSION:-1.4.2}",
            "truthtable-dashboard:${TRUSTAGENT_VERSION:-edge}",
        )
        errors = cv.check(self.root).errors
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("docker-compose.yml: default image tag is edge", errors[0])

    def test_a_missing_field_is_an_error(self) -> None:
        self.edit("backend-go/internal/version/version.go", 'var Version = "', 'var version = "')
        errors = cv.check(self.root).errors
        self.assertEqual(errors, ["backend-go/internal/version/version.go: no var Version found"])

    def test_an_overlay_with_another_default_tag_only_warns(self) -> None:
        (self.root / "docker-compose.extra.yml").write_text(
            "services:\n  x:\n    image: ghcr.io/timkush1/truthtable-proxy:"
            "${TRUSTAGENT_VERSION:-edge}\n",
            encoding="utf-8",
        )
        (self.root / "docker-compose.fine.yml").write_text(
            "services:\n  y:\n    image: ghcr.io/timkush1/truthtable-proxy:"
            "${TRUSTAGENT_VERSION:-1.4.2}\n",
            encoding="utf-8",
        )
        report = cv.check(self.root)
        self.assertEqual(report.errors, [])
        self.assertEqual(len(report.warnings), 1, report.warnings)
        self.assertIn("docker-compose.extra.yml: default image tag edge", report.warnings[0])
        code, _, err = run_main(self.root)
        self.assertEqual(code, 0)
        self.assertIn("warning: docker-compose.extra.yml", err)

    def test_version_that_is_not_semver_fails(self) -> None:
        (self.root / "VERSION").write_text("1.4\n", encoding="utf-8")
        code, _, err = run_main(self.root)
        self.assertEqual(code, 1)
        self.assertIn("not a semantic version", err)

    def test_an_unreadable_mirror_exits_2(self) -> None:
        (self.root / "backend-python/pyproject.toml").unlink()
        code, _, err = run_main(self.root)
        self.assertEqual(code, 2)
        self.assertIn("backend-python/pyproject.toml: cannot be read", err)
        self.assertIn("bump_version.py", err)


class TagTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root()

    def tearDown(self) -> None:
        shutil.rmtree(self.root, ignore_errors=True)

    def errors(self, tag: str) -> List[str]:
        return cv.check(self.root, tag).errors

    def test_a_released_tag_passes(self) -> None:
        self.assertEqual(self.errors("v1.4.2"), [])
        code, out, _ = run_main(self.root, "--tag", "v1.4.2")
        self.assertEqual(code, 0)
        self.assertIn("tag v1.4.2", out)

    def test_a_tag_naming_another_version_fails(self) -> None:
        self.assertEqual(self.errors("v1.4.3"), ["tag v1.4.3 does not match VERSION 1.4.2"])

    def test_a_tag_that_is_not_a_release_tag_fails(self) -> None:
        for tag in ("1.4.2", "v1.4", "release-1.4.2", "v1.4.2+x"):
            with self.subTest(tag=tag):
                self.assertEqual(self.errors(tag), [f"tag {tag!r} is not a release tag vX.Y.Z"])

    def test_the_changelog_must_have_the_released_heading(self) -> None:
        (self.root / cv.CHANGELOG).write_text(
            "# Changelog\n\n## Unreleased — 1.4.2 (in progress)\n\n- work\n", encoding="utf-8"
        )
        errors = self.errors("v1.4.2")
        self.assertEqual(len(errors), 2, errors)
        self.assertIn("has no released heading '## v1.4.2'", errors[0])
        self.assertIn("still has the heading '## Unreleased — 1.4.2'", errors[1])

    def test_a_heading_for_a_longer_version_does_not_count(self) -> None:
        (self.root / cv.CHANGELOG).write_text(
            "# Changelog\n\n## v1.4.21 — 2026-01-02\n", encoding="utf-8"
        )
        self.assertIn("has no released heading", self.errors("v1.4.2")[0])

    def test_a_pre_release_tag_works_the_same_way(self) -> None:
        root = make_root("2.0.0-rc.1")
        try:
            (root / cv.CHANGELOG).write_text("## v2.0.0-rc.1 — 2026-03-04\n", encoding="utf-8")
            self.assertEqual(cv.check(root, "v2.0.0-rc.1").errors, [])
        finally:
            shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
