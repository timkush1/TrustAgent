"""Unit tests for scripts/envfile.py (standard library only; no Docker, no network)."""

from __future__ import annotations

import contextlib
import io
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import envfile  # noqa: E402
from envfile import EnvFile, parse_line, parse_value, resolve  # noqa: E402


class ParseTest(unittest.TestCase):
    def test_parse_value_forms(self) -> None:
        self.assertEqual(parse_value("plain"), "plain")
        self.assertEqual(parse_value("  spaced  "), "spaced")
        self.assertEqual(
            parse_value('"double quoted # not a comment"'), "double quoted # not a comment"
        )
        self.assertEqual(parse_value("'single $literal'"), "single $literal")
        self.assertEqual(parse_value("value # trailing comment"), "value")
        self.assertEqual(parse_value("a#b"), "a#b")
        self.assertEqual(parse_value('"say \\"hi\\""'), 'say "hi"')
        self.assertEqual(parse_value(""), "")

    def test_parse_line(self) -> None:
        self.assertEqual(parse_line("KEY=value"), ("KEY", "value"))
        self.assertEqual(parse_line("export KEY=value"), ("KEY", "value"))
        self.assertEqual(parse_line("  KEY = value"), ("KEY", "value"))
        self.assertEqual(parse_line("KEY="), ("KEY", ""))
        self.assertIsNone(parse_line("# KEY=value"))
        self.assertIsNone(parse_line(""))
        self.assertIsNone(parse_line("not an assignment"))
        self.assertIsNone(parse_line("1KEY=value"))


class EnvFileTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_missing_file_loads_empty(self) -> None:
        env = EnvFile.load(self.dir / ".env")
        self.assertFalse(env.existed)
        self.assertEqual(env.values(), {})
        self.assertEqual(env.render(), "")

    def test_last_assignment_wins_and_set_rewrites_it_in_place(self) -> None:
        path = self.dir / ".env"
        path.write_text("# comment\nA=1\nB=2\nexport A=3\n", encoding="utf-8")
        env = EnvFile.load(path)
        self.assertEqual(env.get("A"), "3")
        env.set("A", "4")
        env.set("C", "5")
        self.assertEqual(env.lines, ["# comment", "A=1", "B=2", "export A=4", "C=5"])

    def test_write_is_atomic_and_private(self) -> None:
        path = self.dir / "sub" / ".env"
        env = EnvFile.load(path)
        env.set("SECRET", "x")
        env.write()
        self.assertEqual(path.read_text(encoding="utf-8"), "SECRET=x\n")
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual([p.name for p in path.parent.iterdir()], [".env"])
        self.assertTrue(env.existed)

    def test_resolve_prefers_non_empty_process_env(self) -> None:
        path = self.dir / ".env"
        path.write_text("PORT=3100\nEMPTY=\n", encoding="utf-8")
        self.assertEqual(resolve("PORT", "3000", path, environ={}), "3100")
        self.assertEqual(resolve("PORT", "3000", path, environ={"PORT": "3200"}), "3200")
        self.assertEqual(resolve("PORT", "3000", path, environ={"PORT": ""}), "3100")
        self.assertEqual(resolve("EMPTY", "d", path, environ={}), "d")
        self.assertIsNone(resolve("MISSING", None, path, environ={}))

    def test_cli_get(self) -> None:
        path = self.dir / ".env"
        path.write_text("DASHBOARD_HOST_PORT=3100\n", encoding="utf-8")
        original = envfile.DEFAULT_ENV_FILE
        envfile.DEFAULT_ENV_FILE = path
        out, err = io.StringIO(), io.StringIO()
        try:
            os.environ.pop("DASHBOARD_HOST_PORT", None)
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                self.assertEqual(envfile.main(["get", "DASHBOARD_HOST_PORT", "3000"]), 0)
                self.assertEqual(envfile.main(["get", "NOPE_NOT_SET_ANYWHERE"]), 1)
                self.assertEqual(envfile.main(["bogus"]), 2)
            self.assertEqual(out.getvalue(), "3100\n")
            self.assertIn("usage", err.getvalue())
        finally:
            envfile.DEFAULT_ENV_FILE = original


if __name__ == "__main__":
    unittest.main()
