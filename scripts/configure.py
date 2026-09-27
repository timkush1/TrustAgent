#!/usr/bin/env python3
"""Create or complete the repository's ``.env`` (standard library only).

``--ensure`` is what ``make up`` and ``make demo`` run first. It creates ``.env``
from ``.env.example`` when missing and fills in the values the compose stack
needs but nobody should have to invent (SPEC §3.4):

- ``POSTGRES_PASSWORD``: only when ``.env`` is new or no postgres volume exists
  yet. A postgres volume keeps the password it was created with, so changing it
  later would lock the proxy out.
- ``GRAFANA_ADMIN_PASSWORD`` (observability profile).
- ``TRAFFICGEN_API_KEY`` and a matching ``trafficgen=<key>`` entry in
  ``TRUTHTABLE_API_KEYS``: the ingest key of the demo traffic generator.

It never overwrites a non-empty value and never prints a secret. The file is
written atomically with mode 0600.

Exit codes: 0 done (warnings go to stderr), 2 usage error or unreadable file.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import os
import secrets
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import DEFAULT_ENV_FILE, REPO_ROOT, EnvFile  # noqa: E402

DEFAULT_EXAMPLE = REPO_ROOT / ".env.example"
DEFAULT_PROJECT = "truthtable"
DEMO_PROJECT = "truthtable-demo"
TRAFFICGEN_LABEL = "trafficgen"

# Returns True/False when it can tell whether any of the named volumes exists,
# None when it cannot (docker missing or not running).
VolumeProbe = Callable[[Sequence[str]], Optional[bool]]


def generate_secret(nbytes: int = 24) -> str:
    """A random hex secret: safe in URLs (the database URL) and in .env without quoting."""
    return secrets.token_hex(nbytes)


def parse_key_list(value: str) -> List[Tuple[Optional[str], str]]:
    """Split ``TRUTHTABLE_API_KEYS`` (``label=key,...``) into ``(label, key)`` pairs.

    An entry without ``=`` is an unlabelled legacy key and yields ``(None, key)``.
    """
    entries: List[Tuple[Optional[str], str]] = []
    for raw in value.split(","):
        item = raw.strip()
        if not item:
            continue
        label, sep, key = item.partition("=")
        if sep:
            entries.append((label.strip(), key.strip()))
        else:
            entries.append((None, item))
    return entries


def docker_volume_exists(names: Sequence[str]) -> Optional[bool]:
    """Whether any of the named docker volumes exists (None if docker is unavailable)."""
    docker = shutil.which("docker")
    if docker is None:
        return None
    try:
        result = subprocess.run(
            [docker, "volume", "ls", "--quiet"],
            capture_output=True,
            text=True,
            timeout=20,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    existing = set(result.stdout.split())
    return any(name in existing for name in names)


def postgres_volumes(project: str) -> List[str]:
    """Volume names the default and demo stacks would use for postgres data."""
    projects = [project] if project == DEMO_PROJECT else [project, DEMO_PROJECT]
    return [f"{name}_postgres-data" for name in projects]


@dataclass
class EnsureResult:
    created: bool = False
    generated: List[str] = field(default_factory=list)
    notes: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)

    @property
    def changed(self) -> bool:
        return self.created or bool(self.generated) or bool(self.notes)


def ensure(
    env: EnvFile,
    example: Optional[Path],
    volume_exists: VolumeProbe = docker_volume_exists,
    environ: Optional[Mapping[str, str]] = None,
) -> EnsureResult:
    """Fill in the generated values of ``env`` in memory; the caller writes it."""
    result = EnsureResult()
    environment = os.environ if environ is None else environ

    if not env.existed:
        result.created = True
        if example is not None and example.is_file():
            env.lines = example.read_text(encoding="utf-8").splitlines()

    additions: List[Tuple[str, str]] = []

    def fill(key: str, value: str) -> None:
        # A key assigned but empty is filled in place; a missing one is appended
        # under a dated header after all checks.
        if env.get(key) is None:
            additions.append((key, value))
        else:
            env.set(key, value)
        result.generated.append(key)

    # POSTGRES_PASSWORD: never replace one a volume may already use.
    if not env.get("POSTGRES_PASSWORD"):
        project = (
            environment.get("COMPOSE_PROJECT_NAME")
            or env.get("COMPOSE_PROJECT_NAME")
            or DEFAULT_PROJECT
        )
        volumes = postgres_volumes(project)
        if result.created:
            fill("POSTGRES_PASSWORD", generate_secret())
        else:
            exists = volume_exists(volumes)
            if exists is False:
                fill("POSTGRES_PASSWORD", generate_secret())
            elif exists is True:
                result.warnings.append(
                    "POSTGRES_PASSWORD is empty but a postgres volume already exists ("
                    + ", ".join(volumes)
                    + "). Set POSTGRES_PASSWORD in .env to the password that volume was "
                    "created with, or delete the volume (make clean-data / make demo-clean) "
                    "and run this again."
                )
            else:
                result.warnings.append(
                    "POSTGRES_PASSWORD is empty and docker could not be asked whether a "
                    "postgres volume exists; not generating one. Start docker and run "
                    "`python3 scripts/configure.py --ensure` again."
                )

    if not env.get("GRAFANA_ADMIN_PASSWORD"):
        fill("GRAFANA_ADMIN_PASSWORD", generate_secret(16))

    # The trafficgen ingest key lives in two places that must agree.
    api_keys_value = env.get("TRUTHTABLE_API_KEYS") or ""
    entries = parse_key_list(api_keys_value)
    listed = next((key for label, key in entries if label == TRAFFICGEN_LABEL), None)
    tg_key = env.get("TRAFFICGEN_API_KEY") or None
    if tg_key and listed and tg_key != listed:
        result.warnings.append(
            "TRAFFICGEN_API_KEY differs from the trafficgen entry in TRUTHTABLE_API_KEYS; "
            "trafficgen will be rejected until they match. Left unchanged."
        )
    elif tg_key is None and listed:
        fill("TRAFFICGEN_API_KEY", listed)
    else:
        if tg_key is None:
            tg_key = generate_secret(32)
            fill("TRAFFICGEN_API_KEY", tg_key)
        if listed is None:
            entry = f"{TRAFFICGEN_LABEL}={tg_key}"
            new_value = f"{api_keys_value.strip().rstrip(',')},{entry}" if entries else entry
            if env.get("TRUTHTABLE_API_KEYS") is None:
                additions.append(("TRUTHTABLE_API_KEYS", new_value))
            else:
                env.set("TRUTHTABLE_API_KEYS", new_value)
            result.notes.append("added the trafficgen entry to TRUTHTABLE_API_KEYS")
    unlabelled = sum(1 for label, _ in entries if label is None)
    if unlabelled:
        result.warnings.append(
            f"TRUTHTABLE_API_KEYS has {unlabelled} entr{'y' if unlabelled == 1 else 'ies'} "
            "without a label; v2 expects label=key (for example ci=<key>)."
        )

    if additions:
        stamp = _dt.date.today().isoformat()
        if env.lines and env.lines[-1].strip():
            env.lines.append("")
        env.lines.append(f"# --- added by scripts/configure.py --ensure ({stamp}) ---")
        env.lines.extend(f"{key}={value}" for key, value in additions)
    return result


def _display(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(Path.cwd().resolve()))
    except ValueError:
        return str(path)


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="configure.py",
        description=(
            "Create or complete .env for the compose stack. This version implements "
            "--ensure only (generated values, no questions); the interactive setup "
            "(non-secrets by default, --with-secrets) follows in a later phase."
        ),
    )
    parser.add_argument(
        "--ensure",
        action="store_true",
        help="create .env from .env.example if missing and fill in generated values "
        "(POSTGRES_PASSWORD, GRAFANA_ADMIN_PASSWORD, TRAFFICGEN_API_KEY and its "
        "TRUTHTABLE_API_KEYS entry); never overwrites a non-empty value",
    )
    parser.add_argument(
        "--env-file",
        type=Path,
        default=DEFAULT_ENV_FILE,
        help="the .env file to create or complete (default: %(default)s)",
    )
    parser.add_argument(
        "--example",
        type=Path,
        default=DEFAULT_EXAMPLE,
        help="template for a new .env (default: %(default)s)",
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="report what would change; write nothing"
    )
    args = parser.parse_args(argv)
    if not args.ensure:
        parser.print_usage(sys.stderr)
        print(
            "configure.py: nothing to do; run with --ensure (interactive setup is not "
            "available yet)",
            file=sys.stderr,
        )
        return 2

    try:
        env = EnvFile.load(args.env_file)
    except (OSError, UnicodeDecodeError) as exc:
        print(f"configure.py: cannot read {args.env_file}: {exc}", file=sys.stderr)
        return 2

    result = ensure(env, args.example)
    name = _display(args.env_file)
    for warning in result.warnings:
        print(f"configure.py: warning: {warning}", file=sys.stderr)
    if not result.changed:
        print(f"configure.py: {name} is up to date")
        return 0

    if not args.dry_run:
        try:
            env.write()
        except OSError as exc:
            print(f"configure.py: cannot write {name}: {exc}", file=sys.stderr)
            return 2
    tag = "configure.py: " + ("dry run, nothing written: " if args.dry_run else "")
    if result.created:
        source = _display(args.example) if args.example.is_file() else "scratch"
        print(f"{tag}created {name} from {source}")
    if result.generated:
        print(f"{tag}generated {', '.join(result.generated)} (values not shown)")
    for note in result.notes:
        print(f"{tag}{note}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
