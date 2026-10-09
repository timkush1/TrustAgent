#!/usr/bin/env python3
"""Run a command on the host with the environment docker compose gives one service.

    docker compose -f docker-compose.yml ... config --format json \\
        | python3 scripts/service_env.py proxy --expect TRUTHTABLE_STRICT_CONFIG=true \\
            --expect-default TRUTHTABLE_JEV_BACKEND -- go -C backend-go run ./cmd/proxy -check-config

``make onboarding-check-config`` uses it to prove, without starting a container,
that the gateway accepts the onboarding stack's configuration under
TRUTHTABLE_STRICT_CONFIG (backend auto, no key anywhere): the stack would
otherwise refuse to start and `up --wait` would only report an unhealthy proxy.

Before the command runs, each expectation is checked against the service's
environment, so the proof cannot pass for the wrong reason (a key that slipped
into the stack, strict mode turned off):
- ``--expect NAME=VALUE``: compose sets NAME to exactly VALUE;
- ``--expect-default NAME``: compose leaves NAME unset or empty, or sets it to
  the registry default, so the program sees its default.

The command sees what the container would see, not the developer's shell:
- every name in contracts/config/keys.v1.json (and its aliases), every
  ``TRUTHTABLE_*`` name and every name the service's environment declares is
  removed from the inherited environment, so a key exported in the shell or
  read from .env cannot reach the command unless compose passes it;
- the service's values are set; a name compose leaves unset (``null``) stays
  unset, as in the container;
- ``TRUTHTABLE_ENV_FILE`` is empty, so the gateway reads no .env file (the
  container has none: compose passes the settings as the environment);
- everything else (PATH, HOME, the Go caches) is inherited, so the toolchain
  works.

No value is ever printed: the messages name the service, the variables and how
many variables compose gave it.

Exit codes: the command's own; 1 when an expectation fails (the command does
not run); 2 for a usage error, compose output that is not JSON, or a service
the output does not have.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Dict, FrozenSet, List, Mapping, NamedTuple, Optional, Sequence, TextIO

ROOT = Path(__file__).resolve().parents[1]
REGISTRY = ROOT / "contracts" / "config" / "keys.v1.json"
ENV_FILE_VAR = "TRUTHTABLE_ENV_FILE"
USAGE = (
    "usage: service_env.py SERVICE [--expect NAME=VALUE]... [--expect-default NAME]... "
    "-- COMMAND [ARGS...]  (compose config --format json on stdin)"
)


class UsageError(Exception):
    """A usage or input error: exit 2 with this message."""


def registry_defaults(path: Path = REGISTRY) -> Dict[str, str]:
    """Each registry key and alias with its key's default value (as the registry writes it)."""
    data = json.loads(path.read_text(encoding="utf-8"))
    defaults: Dict[str, str] = {}
    for entry in data["keys"]:
        default = entry.get("default")
        value = "" if default is None else str(default)
        for name in [entry["name"], *entry.get("aliases", [])]:
            defaults[name] = value
    return defaults


class Invocation(NamedTuple):
    """The parsed command line."""

    service: str
    expect: Dict[str, str]
    expect_default: List[str]
    command: List[str]


def parse_args(argv: Sequence[str]) -> Invocation:
    """SERVICE [--expect NAME=VALUE]... [--expect-default NAME]... -- COMMAND [ARGS...]"""
    args = list(argv)
    if "--" not in args:
        raise UsageError(USAGE)
    split = args.index("--")
    head, command = args[:split], args[split + 1 :]
    if not head or not head[0] or head[0].startswith("-") or not command:
        raise UsageError(USAGE)
    expect: Dict[str, str] = {}
    expect_default: List[str] = []
    rest = head[1:]
    while rest:
        flag = rest.pop(0)
        if flag not in ("--expect", "--expect-default") or not rest:
            raise UsageError(USAGE)
        operand = rest.pop(0)
        if flag == "--expect":
            name, sep, value = operand.partition("=")
            if not sep or not name:
                raise UsageError(f"service_env: --expect needs NAME=VALUE, got {operand!r}")
            expect[name] = value
        else:
            if not operand or "=" in operand:
                raise UsageError(f"service_env: --expect-default needs a NAME, got {operand!r}")
            expect_default.append(operand)
    return Invocation(head[0], expect, expect_default, command)


def unmet_expectations(
    service_env: Mapping[str, Optional[str]],
    expect: Mapping[str, str],
    expect_default: Sequence[str],
    defaults: Mapping[str, str],
) -> List[str]:
    """One line per expectation the service's environment does not meet; no value is quoted.

    A name the registry does not list has no default to compare against: --expect-default then
    accepts only an unset or empty value.
    """
    problems: List[str] = []
    for name, want in expect.items():
        got = service_env.get(name)
        if got is None:
            problems.append(f"{name} is not set (expected the value given with --expect)")
        elif got != want:
            problems.append(f"{name} differs from the value given with --expect")
    for name in expect_default:
        got = service_env.get(name)
        if got is None or got == "" or got == defaults.get(name, ""):
            continue
        problems.append(f"{name} is set to a value other than its default")
    return problems


def service_environment(config: Mapping[str, Any], service: str) -> Dict[str, Optional[str]]:
    """The environment `docker compose config --format json` resolved for one service.

    The value is None for a name compose declares but leaves unset. Both the mapping form compose
    prints and the list form ("K=V", "K") are accepted.
    """
    services = config.get("services")
    if not isinstance(services, dict) or service not in services:
        raise UsageError(f"service_env: the compose configuration has no service {service!r}")
    raw = services[service].get("environment") or {}
    env: Dict[str, Optional[str]] = {}
    if isinstance(raw, dict):
        for name, value in raw.items():
            env[str(name)] = None if value is None else str(value)
    elif isinstance(raw, list):
        for item in raw:
            name, sep, value = str(item).partition("=")
            env[name] = value if sep else None
    else:
        raise UsageError(f"service_env: service {service!r} has an unreadable environment")
    return env


def command_environment(
    inherited: Mapping[str, str],
    service_env: Mapping[str, Optional[str]],
    registered: FrozenSet[str],
) -> Dict[str, str]:
    """The environment the command runs with (see the module docstring)."""
    env = {
        name: value
        for name, value in inherited.items()
        if name not in registered and name not in service_env and not name.startswith("TRUTHTABLE_")
    }
    for name, value in service_env.items():
        if value is not None:
            env[name] = value
    env[ENV_FILE_VAR] = ""
    return env


def main(
    argv: Sequence[str],
    stdin: TextIO = sys.stdin,
    stderr: TextIO = sys.stderr,
    inherited: Optional[Mapping[str, str]] = None,
) -> int:
    try:
        inv = parse_args(argv)
        try:
            config = json.loads(stdin.read())
        except json.JSONDecodeError:
            raise UsageError(
                "service_env: stdin is not JSON (did `docker compose config --format json` fail?)"
            ) from None
        if not isinstance(config, dict):
            raise UsageError("service_env: stdin is not a compose configuration")
        service_env = service_environment(config, inv.service)
    except UsageError as err:
        print(err, file=stderr)
        return 2
    defaults = registry_defaults()
    problems = unmet_expectations(service_env, inv.expect, inv.expect_default, defaults)
    if problems:
        for line in problems:
            print(f"service_env: {inv.service}: {line}", file=stderr)
        print("service_env: the command did not run", file=stderr)
        return 1
    env = command_environment(
        os.environ if inherited is None else inherited, service_env, frozenset(defaults)
    )
    passed = sum(1 for value in service_env.values() if value is not None)
    print(
        f"service_env: running with the {inv.service} service's environment from compose "
        f"({passed} variables, values not shown)",
        file=stderr,
    )
    stderr.flush()
    try:
        return subprocess.run(inv.command, env=env, check=False).returncode  # nosec B603
    except FileNotFoundError:
        print(f"service_env: command not found: {inv.command[0]}", file=stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
