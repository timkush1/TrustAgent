#!/usr/bin/env python3
"""Create or complete the repository's ``.env`` (standard library only).

``--ensure`` is what ``make up`` and ``make demo`` run first. It creates ``.env``
from ``.env.example`` when missing and fills in the values the compose stack
needs but nobody should have to invent (SPEC §3.4):

- ``POSTGRES_PASSWORD``: only when ``.env`` is new or no postgres volume exists
  yet. A postgres volume keeps the password it was created with, so changing it
  later would lock the proxy out; a new ``.env`` next to an existing volume
  (``.env`` deleted, volume kept) gets a password and a warning saying so.
- ``GRAFANA_ADMIN_PASSWORD`` (observability profile).
- ``TRAFFICGEN_API_KEY`` and a matching ``trafficgen=<key>`` entry in
  ``TRUTHTABLE_API_KEYS``: the ingest key of the demo traffic generator.

It never overwrites a non-empty value and never prints a secret. The file is
written atomically with mode 0600.

``--with-secrets`` (``make configure WITH_SECRETS=1``) does the same and
then asks for the vendor keys a real stack needs, at hidden prompts (getpass):
the OpenRouter key that runs the fast check and the deep-audit judge, and the
optional TypeSafe, Anthropic and OpenAI keys. Enter keeps a key that is already
set (or skips one that is not), ``-`` removes it. A value is never echoed,
printed or logged; only the names of the keys that changed are reported. It
needs a terminal: with stdin redirected it refuses instead of reading a key from
a pipe. Keys in ``.env`` win over keys saved in the dashboard, which shows them
read-only.

Exit codes: 0 done (warnings go to stderr), 2 usage error or unreadable file,
130 cancelled at a prompt (nothing written).
"""

from __future__ import annotations

import argparse
import datetime as _dt
import getpass
import os
import re
import secrets
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, List, Mapping, Optional, Sequence, TextIO, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import DEFAULT_ENV_FILE, REPO_ROOT, EnvFile  # noqa: E402

DEFAULT_EXAMPLE = REPO_ROOT / ".env.example"
DEFAULT_PROJECT = "truthtable"
DEMO_PROJECT = "truthtable-demo"
TRAFFICGEN_LABEL = "trafficgen"

# Returns True/False when it can tell whether any of the named volumes exists,
# None when it cannot (docker missing or not running).
VolumeProbe = Callable[[Sequence[str]], Optional[bool]]
# Reads one secret at a hidden prompt (getpass.getpass in production).
SecretReader = Callable[[str], str]


@dataclass(frozen=True)
class SecretPrompt:
    """One vendor key ``--with-secrets`` asks for (a ``secret`` key of the registry)."""

    key: str
    label: str
    hint: str
    # The prefix keys of this vendor start with; another prefix earns a warning, not a refusal.
    prefix: str = ""


# In the order of docs/JEV-ACCESS.md: one OpenRouter key runs both the fast check and the judge;
# the others are alternatives. TRUTHTABLE_JEV_API_KEY is left out on purpose: it only applies to
# an explicit or compatible backend, which a headless setup configures by hand.
SECRET_PROMPTS: Tuple[SecretPrompt, ...] = (
    SecretPrompt(
        "OPENROUTER_API_KEY",
        "OpenRouter API key",
        "runs the fast check (Jev) and the deep-audit judge; create one at "
        "openrouter.ai/settings/keys and give it a credit limit",
        "sk-or-",
    ),
    SecretPrompt(
        "TYPESAFE_API_KEY",
        "TypeSafe API key",
        "optional: the fast check straight from console.typesafe.ai instead of OpenRouter",
    ),
    SecretPrompt(
        "ANTHROPIC_API_KEY",
        "Anthropic API key",
        "optional: the deep-audit judge on Anthropic directly",
        "sk-ant-",
    ),
    SecretPrompt(
        "OPENAI_API_KEY",
        "OpenAI API key",
        "optional: the deep-audit judge on OpenAI",
        "sk-",
    ),
)
# What a key may hold: what vendor keys use (letters, digits, - _ . and base64's + / =). Anything
# else (spaces, quotes, $, a second pasted line) would be mangled by .env parsing or compose
# interpolation, so it is refused rather than written.
_SECRET_VALUE = re.compile(r"[A-Za-z0-9._~+/=:-]+")
MIN_SECRET_LENGTH = 8
MAX_SECRET_LENGTH = 512
SECRET_ATTEMPTS = 3
REMOVE_MARK = "-"


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
        exists = volume_exists(volumes)
        if result.created:
            # A new .env always gets one (SPEC §3.4); an old volume cannot use it.
            fill("POSTGRES_PASSWORD", generate_secret())
            if exists is True:
                result.warnings.append(
                    "generated a new POSTGRES_PASSWORD, but a postgres volume from an "
                    "earlier .env already exists (one of: "
                    + ", ".join(volumes)
                    + ") and keeps the password it was created with. Put that password in "
                    ".env as POSTGRES_PASSWORD, or delete the volume (make clean-data / "
                    "make demo-clean) before starting the stack."
                )
        else:
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


def secret_problem(value: str) -> Optional[str]:
    """Why ``value`` cannot be a key in ``.env`` (never quoting it), or None when it can."""
    if len(value) < MIN_SECRET_LENGTH:
        return f"it is shorter than {MIN_SECRET_LENGTH} characters, too short for an API key"
    if len(value) > MAX_SECRET_LENGTH:
        return f"it is longer than {MAX_SECRET_LENGTH} characters"
    if not _SECRET_VALUE.fullmatch(value):
        return (
            "it holds characters API keys do not (a space, a quote, $ or a line break); "
            "paste the key alone"
        )
    return None


@dataclass
class SecretsResult:
    saved: List[str] = field(default_factory=list)
    removed: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)

    @property
    def changed(self) -> bool:
        return bool(self.saved or self.removed)


def assign_documented(env: EnvFile, key: str, value: str) -> bool:
    """Assign ``key``: in place when assigned, else on its commented ``# KEY=`` template line.

    Returns False (and assigns nothing) when neither exists, so the caller can append it.
    """
    if env.get(key) is not None:
        env.set(key, value)
        return True
    template = re.compile(rf"^\s*#\s*{re.escape(key)}=\s*$")
    for index, line in enumerate(env.lines):
        if template.match(line):
            env.lines[index] = f"{key}={value}"
            return True
    return False


def ask_secrets(
    env: EnvFile,
    read_secret: SecretReader,
    out: TextIO,
    prompts: Sequence[SecretPrompt] = SECRET_PROMPTS,
) -> SecretsResult:
    """Ask for each vendor key at a hidden prompt and assign the answers in ``env`` (in memory).

    Enter keeps a key that is set and skips one that is not; ``-`` removes a set key. A refused
    value is asked for again, up to SECRET_ATTEMPTS times, then left unchanged. Nothing here
    prints, logs or returns a value: messages name the key and the reason only.
    """
    result = SecretsResult()
    additions: List[Tuple[str, str]] = []
    print(
        "Vendor keys: typing is hidden. Enter keeps a key that is set or skips one that is not; "
        f"{REMOVE_MARK} removes a key.",
        file=out,
    )
    for item in prompts:
        current = env.get(item.key) or ""
        state = f"set; Enter keeps it, {REMOVE_MARK} removes it" if current else "Enter skips"
        print(f"\n{item.label} ({item.key}): {item.hint}.", file=out)
        for attempt in range(1, SECRET_ATTEMPTS + 1):
            value = read_secret(f"{item.label} [{state}]: ").strip()
            if not value:
                break
            if value == REMOVE_MARK:
                if current:
                    env.set(item.key, "")
                    result.removed.append(item.key)
                break
            problem = secret_problem(value)
            if problem is None:
                if value != current:
                    if not assign_documented(env, item.key, value):
                        additions.append((item.key, value))
                    result.saved.append(item.key)
                if item.prefix and not value.startswith(item.prefix):
                    result.warnings.append(
                        f"{item.key} does not start with {item.prefix}, as {item.label}s do; "
                        "saved anyway. Check it with `make doctor` once the stack runs."
                    )
                break
            left = SECRET_ATTEMPTS - attempt
            tail = f"try again ({left} left)" if left else f"{item.key} left unchanged"
            print(f"  Not saved: {problem}; {tail}.", file=out)
    if additions:
        if env.lines and env.lines[-1].strip():
            env.lines.append("")
        env.lines.append(
            f"# --- added by scripts/configure.py --with-secrets ({_dt.date.today().isoformat()}) ---"
        )
        env.lines.extend(f"{key}={value}" for key, value in additions)
    return result


def stdin_is_interactive() -> bool:
    """Whether stdin is a terminal, where getpass can hide what is typed."""
    try:
        return sys.stdin is not None and sys.stdin.isatty()
    except (AttributeError, ValueError):
        return False


def read_secret(prompt: str) -> str:
    """One hidden answer (no echo); looked up at call time so tests can replace it."""
    return getpass.getpass(prompt)


def _display(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(Path.cwd().resolve()))
    except ValueError:
        return str(path)


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="configure.py",
        description=(
            "Create or complete .env for the compose stack: --ensure fills in the generated "
            "values; --with-secrets also asks for the vendor keys at hidden prompts."
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
        "--with-secrets",
        action="store_true",
        help="do what --ensure does, then ask for the OpenRouter, TypeSafe, Anthropic and "
        "OpenAI keys at hidden prompts (needs a terminal; nothing typed is shown)",
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
    if not (args.ensure or args.with_secrets):
        parser.print_usage(sys.stderr)
        print(
            "configure.py: nothing to do; run with --ensure, or --with-secrets to also "
            "enter vendor keys",
            file=sys.stderr,
        )
        return 2
    if args.with_secrets and not stdin_is_interactive():
        print(
            "configure.py: --with-secrets asks for keys at a hidden prompt and needs a "
            "terminal (stdin is not one). Run it in a terminal, or put the keys in .env "
            "with an editor.",
            file=sys.stderr,
        )
        return 2

    try:
        env = EnvFile.load(args.env_file)
    except (OSError, UnicodeDecodeError) as exc:
        print(f"configure.py: cannot read {args.env_file}: {exc}", file=sys.stderr)
        return 2

    result = ensure(env, args.example, volume_exists=docker_volume_exists)
    name = _display(args.env_file)
    for warning in result.warnings:
        print(f"configure.py: warning: {warning}", file=sys.stderr)

    keys = SecretsResult()
    if args.with_secrets:
        try:
            keys = ask_secrets(env, read_secret, sys.stdout)
        except (KeyboardInterrupt, EOFError):
            print(f"\nconfigure.py: cancelled; {name} is unchanged", file=sys.stderr)
            return 130
        print()
        for warning in keys.warnings:
            print(f"configure.py: warning: {warning}", file=sys.stderr)

    if not (result.changed or keys.changed):
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
    if keys.saved:
        print(f"{tag}saved {', '.join(keys.saved)} (values not shown)")
    if keys.removed:
        print(f"{tag}removed {', '.join(keys.removed)}")
    if keys.changed and not args.dry_run:
        print(
            "configure.py: next, `make up` (or `docker compose up -d`) starts the stack with "
            "these keys, and `make doctor` checks them. A key in .env wins over one saved in "
            "the dashboard, which shows it as read-only."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
