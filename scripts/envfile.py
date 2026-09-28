#!/usr/bin/env python3
"""Read and update the repository's ``.env`` file (standard library only).

Shared by ``scripts/configure.py`` and ``scripts/doctor.py``. The Makefile also
uses the small CLI to read one setting with the same precedence the services
use (process environment first, then ``.env``, then a default)::

    python3 scripts/envfile.py get DASHBOARD_HOST_PORT 3000

The supported syntax is the subset docker compose and the services agree on:
``KEY=VALUE`` lines, ``#`` comments, an optional ``export`` prefix, and single-
or double-quoted values. Updates keep every other line (comments, order,
unknown keys) untouched.
"""

from __future__ import annotations

import os
import re
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, Iterator, List, Mapping, Optional, Tuple

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_ENV_FILE = REPO_ROOT / ".env"

_ASSIGNMENT = re.compile(r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$")


def parse_value(raw: str) -> str:
    """Return the value of a ``KEY=raw`` assignment, unquoted and without an inline comment."""
    value = raw.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        inner = value[1:-1]
        if value[0] == '"':
            inner = inner.replace('\\"', '"').replace("\\n", "\n").replace("\\\\", "\\")
        return inner
    # Unquoted: an inline comment needs whitespace before the '#'.
    match = re.search(r"\s#", value)
    if match:
        value = value[: match.start()]
    return value.strip()


def parse_line(line: str) -> Optional[Tuple[str, str]]:
    """Return ``(key, value)`` for an assignment line, ``None`` for comments and blanks."""
    if line.lstrip().startswith("#"):
        return None
    match = _ASSIGNMENT.match(line)
    if not match:
        return None
    return match.group(1), parse_value(match.group(2))


@dataclass
class EnvFile:
    """A ``.env`` file as a list of lines, edited in place."""

    path: Path
    lines: List[str] = field(default_factory=list)
    existed: bool = False

    @classmethod
    def load(cls, path: Path) -> "EnvFile":
        """Read ``path``; a missing file loads as empty with ``existed`` false."""
        try:
            text = path.read_text(encoding="utf-8")
        except FileNotFoundError:
            return cls(path=path)
        return cls(path=path, lines=text.splitlines(), existed=True)

    def items(self) -> Iterator[Tuple[int, str, str]]:
        """Yield ``(line_index, key, value)`` for every assignment."""
        for index, line in enumerate(self.lines):
            parsed = parse_line(line)
            if parsed is not None:
                yield index, parsed[0], parsed[1]

    def values(self) -> Dict[str, str]:
        """All assignments; a key assigned twice keeps its last value (as compose does)."""
        return {key: value for _, key, value in self.items()}

    def get(self, key: str) -> Optional[str]:
        """The value of ``key``, or ``None`` when it is not assigned."""
        return self.values().get(key)

    def set(self, key: str, value: str) -> None:
        """Assign ``key``: rewrite its last assignment in place, or append a new line."""
        last = None
        for index, found, _ in self.items():
            if found == key:
                last = index
        line = f"{key}={value}"
        if last is None:
            self.lines.append(line)
            return
        prefix = "export " if self.lines[last].lstrip().startswith("export ") else ""
        self.lines[last] = prefix + line

    def render(self) -> str:
        return "\n".join(self.lines) + "\n" if self.lines else ""

    def write(self) -> None:
        """Write atomically (temp file + rename) with mode 0600: the file holds secrets."""
        self.path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(prefix=".env.", dir=str(self.path.parent))
        try:
            with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
                handle.write(self.render())
            os.chmod(tmp, 0o600)
            os.replace(tmp, self.path)
        except BaseException:
            if os.path.exists(tmp):
                os.unlink(tmp)
            raise
        self.existed = True


def resolve(
    key: str,
    default: Optional[str] = None,
    env_file: Optional[Path] = None,
    environ: Optional[Mapping[str, str]] = None,
) -> Optional[str]:
    """Look ``key`` up like the services do: a non-empty process variable, then ``.env``."""
    env = os.environ if environ is None else environ
    value = env.get(key)
    if value:
        return value
    value = EnvFile.load(env_file or DEFAULT_ENV_FILE).get(key)
    if value:
        return value
    return default


def main(argv: List[str]) -> int:
    if len(argv) not in (2, 3) or argv[0] != "get":
        print("usage: envfile.py get KEY [DEFAULT]", file=sys.stderr)
        return 2
    value = resolve(argv[1], argv[2] if len(argv) == 3 else None)
    if value is None:
        return 1
    print(value)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
