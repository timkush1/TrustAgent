#!/usr/bin/env python3
"""Mint a one-time dashboard setup URL on a running stack (standard library only).

    python3 scripts/setup_url.py                       # print "SETUP: open <url>"
    python3 scripts/setup_url.py --url-only --project truthtable-demo
    python3 scripts/setup_url.py --open --dashboard-url http://localhost:3000

Runs ``/proxy -new-setup-token`` in the proxy container of the first listed
compose project that has one running (default: COMPOSE_PROJECT_NAME or
truthtable, then truthtable-demo, then truthtable-e2e), and reads the
``SETUP: open <url>`` line it prints (authz.SetupLinePrefix). Claiming the URL
on the dashboard creates the admin key; each run replaces the previous token.

Modes:
- default: print the SETUP line (``make setup-url``);
- ``--url-only``: print the URL alone on stdout, notes on stderr, so
  ``export TRUSTAGENT_SETUP_URL="$(make -s demo-setup-url)"`` works;
- ``--open``: print the URL with a label and open it in the browser
  (``make up`` / ``make demo``; ``--print-only`` or CI only prints). When an
  admin already exists, or no token can be minted, it opens ``--dashboard-url``
  instead and still exits 0: opening a page is a convenience.

Exit codes: 0 printed (or, with --open, opened something); 1 the proxy minted
no token (an admin exists: sign in with its key, or pass --force for a
recovery token); 2 usage error or no running proxy.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, List, Mapping, Optional, Sequence, TextIO, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
import open_url  # noqa: E402
from envfile import resolve  # noqa: E402

# authz.SetupLinePrefix (backend-go/internal/authz/setup.go).
SETUP_PREFIX = "SETUP: open "
DEFAULT_PROJECT = "truthtable"
DEMO_PROJECT = "truthtable-demo"
E2E_PROJECT = "truthtable-e2e"
# What `/proxy -new-setup-token` says on stderr when setup is not required.
ADMIN_EXISTS_HINT = "admin credential already exists"
EXEC_TIMEOUT_S = 60.0

Runner = Callable[[Sequence[str], float], Tuple[int, str, str]]
Opener = Callable[[str], bool]


def run_command(argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
    """Run a command; (returncode, stdout, stderr). 127 when it cannot be started."""
    try:
        done = subprocess.run(
            list(argv), capture_output=True, text=True, timeout=timeout, check=False
        )
    except FileNotFoundError:
        return 127, "", f"{argv[0]}: command not found"
    except subprocess.TimeoutExpired:
        return 124, "", f"timed out after {timeout:.0f}s"
    except OSError as exc:
        return 126, "", str(exc)
    return done.returncode, done.stdout, done.stderr


@dataclass
class Minted:
    """The outcome of one -new-setup-token run."""

    project: Optional[str]  # None: no project had a running proxy
    url: Optional[str]  # the setup URL, None when none was minted
    admin_exists: bool = False
    message: str = ""  # the proxy's own notes (validity, or why it minted nothing)


def running_proxy(project: str, run: Runner) -> Optional[str]:
    """The id of the project's running proxy container, None when there is none."""
    code, out, _ = run(
        [
            "docker",
            "ps",
            "--quiet",
            "--filter",
            f"label=com.docker.compose.project={project}",
            "--filter",
            "label=com.docker.compose.service=proxy",
            "--filter",
            "status=running",
        ],
        20,
    )
    ids = out.split() if code == 0 else []
    return ids[0] if ids else None


def setup_line_url(stdout: str) -> Optional[str]:
    """The URL of the first ``SETUP: open <url>`` line, None without one."""
    for line in stdout.splitlines():
        line = line.strip()
        if line.startswith(SETUP_PREFIX):
            url = line[len(SETUP_PREFIX) :].strip()
            if url:
                return url
    return None


def mint(projects: Sequence[str], force: bool, run: Runner) -> Minted:
    """Mint a setup token in the first project that has a running proxy."""
    for project in projects:
        container = running_proxy(project, run)
        if container is None:
            continue
        argv = ["docker", "exec", container, "/proxy", "-new-setup-token"]
        if force:
            argv.append("-force")
        code, out, err = run(argv, EXEC_TIMEOUT_S)
        url = setup_line_url(out) if code == 0 else None
        message = (err or ("" if url else out)).strip()
        return Minted(
            project=project,
            url=url,
            admin_exists=url is None and ADMIN_EXISTS_HINT in message.lower(),
            message=message or f"/proxy -new-setup-token exited {code}",
        )
    return Minted(project=None, url=None)


def default_projects(environ: Mapping[str, str], env_file: Optional[Path] = None) -> List[str]:
    """The stacks `make setup-url` tries, the `make up` project first (COMPOSE_PROJECT_NAME from
    ``environ``, then ``env_file``, the repository's .env by default)."""
    first = resolve("COMPOSE_PROJECT_NAME", env_file=env_file, environ=environ) or DEFAULT_PROJECT
    ordered = [first, DEFAULT_PROJECT, DEMO_PROJECT, E2E_PROJECT]
    return list(dict.fromkeys(ordered))


def main(
    argv: Optional[Sequence[str]] = None,
    run: Runner = run_command,
    opener: Opener = open_url.open_url,
    environ: Optional[Mapping[str, str]] = None,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    env_file: Optional[Path] = None,
) -> int:
    env = os.environ if environ is None else environ
    parser = argparse.ArgumentParser(
        prog="setup_url.py",
        description="Mint a one-time dashboard setup URL on the running stack.",
    )
    parser.add_argument(
        "--project",
        action="append",
        default=[],
        help="compose project to use (repeatable, first running one wins; default: "
        "COMPOSE_PROJECT_NAME or truthtable, then truthtable-demo, then truthtable-e2e)",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        help="mint a recovery token although an admin credential exists",
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--url-only", action="store_true", help="print only the URL on stdout")
    mode.add_argument(
        "--open",
        action="store_true",
        help="print the URL and open it; open --dashboard-url when no token is minted",
    )
    parser.add_argument(
        "--print-only", action="store_true", help="with --open: print without opening a browser"
    )
    parser.add_argument("--dashboard-url", help="with --open: the page to open without a token")
    args = parser.parse_args(argv)

    projects = args.project or default_projects(env, env_file)
    minted = mint(projects, args.force, run)

    def note(text: str) -> None:
        print(text, file=stderr if args.url_only else stdout, flush=True)

    def show(label: str, url: str) -> None:
        print(f"{label}: {url}", file=stdout, flush=True)
        if args.print_only or env.get("CI"):
            return
        if not opener(url):
            print("No browser launcher worked; open the URL above manually.", file=stdout)

    if minted.url is not None:
        if args.url_only:
            print(minted.url, file=stdout, flush=True)
            print(minted.message, file=stderr, flush=True)
        elif args.open:
            show("Setup (one-time link: it creates your admin key)", minted.url)
            note(f"   {minted.message}")
        else:
            print(f"{SETUP_PREFIX}{minted.url}", file=stdout, flush=True)
            note(minted.message)
        return 0

    if minted.project is None:
        reason = (
            "no running TrustAgent proxy in compose project(s) "
            + ", ".join(projects)
            + ": start one with make up or make demo"
        )
    elif minted.admin_exists:
        reason = (
            f"{minted.project}: an admin already exists, so no setup link is needed. "
            "Sign in at /app/setup with an admin key (TRUTHTABLE_ADMIN_KEYS or the key "
            "setup showed), or pass --force (make setup-url SETUP_FORCE=1) for a recovery link."
        )
    else:
        reason = f"{minted.project}: no setup link was minted: {minted.message}"

    if args.open and args.dashboard_url:
        print(f"Note: {reason}", file=stdout, flush=True)
        show("Dashboard", args.dashboard_url)
        return 0
    print(reason, file=stderr, flush=True)
    return 2 if minted.project is None else 1


if __name__ == "__main__":
    sys.exit(main())
