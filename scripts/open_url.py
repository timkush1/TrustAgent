#!/usr/bin/env python3
"""Print a URL and open it in the host's browser (standard library only).

Used by ``make up``, ``make demo``, ``make metrics`` and ``make dashboards``. The
URL is always printed first, so a headless shell still gets something to click.
Opening is skipped in CI (``CI`` set) and with ``--print-only``.

Launchers, in order:
- WSL: ``wslview`` (wslu), then ``cmd.exe /c start``, then ``explorer.exe``;
- macOS: ``open``; Windows: ``os.startfile``;
- Linux desktop: ``xdg-open`` (only with a display);
- otherwise Python's ``webbrowser`` module.

Exit code 0 even when no launcher works: opening a browser is a convenience.
"""

from __future__ import annotations

import argparse
import os
import platform
import shutil
import subprocess
import sys
import webbrowser
from pathlib import Path
from typing import Callable, List, Mapping, Optional, Sequence


def is_wsl(proc_version: Path = Path("/proc/version")) -> bool:
    """True inside the Windows Subsystem for Linux."""
    try:
        return "microsoft" in proc_version.read_text(encoding="utf-8", errors="replace").lower()
    except OSError:
        return False


def launchers(
    url: str,
    system: str,
    wsl: bool,
    environ: Mapping[str, str],
    which: Callable[[str], Optional[str]] = shutil.which,
) -> List[List[str]]:
    """Candidate commands that open ``url``, best first."""
    commands: List[List[str]] = []
    if wsl:
        if which("wslview"):
            commands.append(["wslview", url])
        if which("cmd.exe"):
            # `start` treats the first quoted argument as a window title.
            commands.append(["cmd.exe", "/c", "start", '""', url.replace("&", "^&")])
        if which("explorer.exe"):
            commands.append(["explorer.exe", url])
    elif system == "Darwin":
        if which("open"):
            commands.append(["open", url])
    elif system == "Linux":
        has_display = bool(environ.get("DISPLAY") or environ.get("WAYLAND_DISPLAY"))
        if has_display and which("xdg-open"):
            commands.append(["xdg-open", url])
    return commands


def open_url(url: str, environ: Optional[Mapping[str, str]] = None) -> bool:
    """Try every launcher for this host; True when one reported success."""
    env = os.environ if environ is None else environ
    system = platform.system()
    if system == "Windows":
        try:
            os.startfile(url)  # type: ignore[attr-defined]  # Windows only
            return True
        except OSError:
            return False
    for command in launchers(url, system, is_wsl(), env):
        try:
            result = subprocess.run(
                command,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=15,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            continue
        # explorer.exe exits 1 even when it opened the URL.
        if result.returncode == 0 or command[0] == "explorer.exe":
            return True
    if system == "Linux" and not (env.get("DISPLAY") or env.get("WAYLAND_DISPLAY")):
        return False
    try:
        return webbrowser.open(url)
    except webbrowser.Error:
        return False


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="open_url.py", description="Print a URL and open it in the host browser."
    )
    parser.add_argument("url")
    parser.add_argument("--label", default="Opening", help="text printed before the URL")
    parser.add_argument(
        "--print-only", action="store_true", help="print the URL without opening a browser"
    )
    args = parser.parse_args(argv)

    print(f"{args.label}: {args.url}", flush=True)
    if args.print_only or os.environ.get("CI"):
        return 0
    if not open_url(args.url):
        print("No browser launcher worked; open the URL above manually.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
