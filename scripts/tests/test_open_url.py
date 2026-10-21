"""Unit tests for scripts/open_url.py (no browser is ever launched)."""

from __future__ import annotations

import contextlib
import io
import os
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Dict, Optional, Tuple
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import open_url  # noqa: E402

URL = "http://localhost:3000/?a=1&b=2"


def which_all(name: str) -> Optional[str]:
    return f"/usr/bin/{name}"


def which_none(name: str) -> Optional[str]:
    return None


class LaunchersTest(unittest.TestCase):
    def test_wsl_prefers_wslview_then_windows_shells(self) -> None:
        commands = open_url.launchers(URL, "Linux", True, {}, which_all)
        self.assertEqual([c[0] for c in commands], ["wslview", "cmd.exe", "explorer.exe"])
        self.assertEqual(commands[1][-1], "http://localhost:3000/?a=1^&b=2")

    def test_macos_uses_open(self) -> None:
        self.assertEqual(open_url.launchers(URL, "Darwin", False, {}, which_all), [["open", URL]])

    def test_linux_needs_a_display(self) -> None:
        self.assertEqual(open_url.launchers(URL, "Linux", False, {}, which_all), [])
        self.assertEqual(
            open_url.launchers(URL, "Linux", False, {"DISPLAY": ":0"}, which_all),
            [["xdg-open", URL]],
        )
        self.assertEqual(open_url.launchers(URL, "Linux", False, {"DISPLAY": ":0"}, which_none), [])

    def test_is_wsl_reads_proc_version(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            version = Path(tmp) / "version"
            version.write_text("Linux version 5.15.167.4-microsoft-standard-WSL2", encoding="utf-8")
            self.assertTrue(open_url.is_wsl(version))
            version.write_text("Linux version 6.8.0-generic", encoding="utf-8")
            self.assertFalse(open_url.is_wsl(version))
            self.assertFalse(open_url.is_wsl(Path(tmp) / "missing"))


class MainTest(unittest.TestCase):
    def run_main(
        self, *args: str, env: Optional[Dict[str, str]] = None
    ) -> Tuple[int, str, mock.MagicMock]:
        out = io.StringIO()
        with mock.patch.object(open_url, "open_url", return_value=True) as opener, mock.patch.dict(
            os.environ, env or {}, clear=True
        ), contextlib.redirect_stdout(out):
            code = open_url.main([*args, URL])
        return code, out.getvalue(), opener

    def test_prints_then_opens(self) -> None:
        code, out, opener = self.run_main("--label", "Dashboard")
        self.assertEqual((code, out), (0, f"Dashboard: {URL}\n"))
        opener.assert_called_once_with(URL)

    def test_print_only_and_ci_never_open(self) -> None:
        for args, env in ((("--print-only",), {}), ((), {"CI": "true"})):
            code, out, opener = self.run_main(*args, env=env)
            self.assertEqual(code, 0)
            self.assertIn(URL, out)
            opener.assert_not_called()

    def test_failure_to_open_is_not_an_error(self) -> None:
        out = io.StringIO()
        with mock.patch.object(open_url, "open_url", return_value=False), mock.patch.dict(
            os.environ, {}, clear=True
        ), contextlib.redirect_stdout(out):
            self.assertEqual(open_url.main([URL]), 0)
        self.assertIn("open the URL above manually", out.getvalue())


if __name__ == "__main__":
    unittest.main()
