#!/usr/bin/env python3
"""Check that every copy of the project version agrees with VERSION (D127; standard library only).

    python3 scripts/check_version.py                 # the mirrors agree with VERSION
    python3 scripts/check_version.py --tag v2.0.0    # also: the tag names VERSION and is released

The root ``VERSION`` file is the one place the version is decided. These files repeat it, and
``scripts/bump_version.py`` rewrites them together:

- ``backend-python/pyproject.toml`` (``[tool.poetry] version``);
- ``backend-python/src/truthtable/__init__.py`` (``__version__``);
- ``frontend-react/package.json`` (``version``);
- ``frontend-react/package-lock.json`` (``version`` and ``packages[""].version``);
- ``backend-go/internal/version/version.go`` (``var Version``);
- ``docker-compose.yml``: the default image tag, every ``${TRUSTAGENT_VERSION:-X}`` (D126), so a
  plain ``docker compose up`` without make runs the images of this version.

An overlay ``docker-compose.*.yml`` that names an app image with a different default tag is
reported as a warning: it is not a D127 mirror, but a different default there runs a different
image than the base file when TRUSTAGENT_VERSION is unset.

``--tag vX.Y.Z`` (the release workflows run it on a tag push) also requires that the tag is
``v`` + VERSION and that CHANGELOG.md has the released heading ``## vX.Y.Z`` and no
``## Unreleased — X.Y.Z`` heading left.

The simulated demo recording also carries the version (``trustagent_version``); ``make
recording-check`` fails when it is stale, and bump_version.py regenerates it.

Exit status: 0 consistent (warnings go to stderr), 1 a check failed, 2 usage error or a file
that cannot be read.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, List, Optional, Sequence, Tuple

ROOT = Path(__file__).resolve().parent.parent

# Semantic versioning 2.0.0 without build metadata: a Docker tag cannot hold "+", and the version
# becomes the image tag (docker-compose.yml, release-images.yml).
SEMVER = re.compile(
    r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?$"
)

COMPOSE_FILE = "docker-compose.yml"
COMPOSE_DEFAULT = re.compile(r"\$\{TRUSTAGENT_VERSION:-([^}]*)\}")
CHANGELOG = "CHANGELOG.md"


class VersionError(Exception):
    """A mirror file is missing or does not hold the field it should."""


# ------------------------------------------------------------------------------ semver


def parse_semver(text: str) -> Tuple[int, int, int, Tuple[str, ...]]:
    """(major, minor, patch, pre-release identifiers); ValueError when ``text`` is not semver."""
    m = SEMVER.match(text)
    if not m:
        raise ValueError(text)
    pre = tuple(m.group(4).split(".")) if m.group(4) else ()
    return int(m.group(1)), int(m.group(2)), int(m.group(3)), pre


def _pre_key(ident: str) -> Tuple[int, int, str]:
    # Numeric identifiers sort numerically and before alphanumeric ones (semver §11.4).
    return (0, int(ident), "") if ident.isdigit() else (1, 0, ident)


def compare_semver(a: str, b: str) -> int:
    """-1, 0 or 1 as ``a`` has lower, equal or higher precedence than ``b`` (semver §11)."""
    ma, na, pa, prea = parse_semver(a)
    mb, nb, pb, preb = parse_semver(b)
    if (ma, na, pa) != (mb, nb, pb):
        return -1 if (ma, na, pa) < (mb, nb, pb) else 1
    if prea == preb:
        return 0
    # A release has higher precedence than any of its pre-releases.
    if not prea:
        return 1
    if not preb:
        return -1
    ka = [_pre_key(i) for i in prea]
    kb = [_pre_key(i) for i in preb]
    return -1 if ka < kb else 1


# ------------------------------------------------------------------------------ mirrors


@dataclass(frozen=True)
class Mirror:
    """One file that repeats the version: how to read its copies and how to rewrite them."""

    path: str
    what: str
    read: Callable[[str], List[str]]
    write: Callable[[str, str, str], str]


def _sub_exact(pattern: str, text: str, old: str, new: str, count: int, path: str) -> str:
    """Replace the version in the first ``count`` matches of ``pattern`` (group 2 = version)."""
    rx = re.compile(pattern, re.MULTILINE)
    found = list(rx.finditer(text))[:count]
    if len(found) < count:
        raise VersionError(f"{path}: the version field is missing")
    out, last = [], 0
    for m in found:
        if m.group(2) != old:
            raise VersionError(f"{path}: holds {m.group(2)}, expected {old}")
        out.append(text[last : m.start(2)])
        out.append(new)
        last = m.end(2)
    out.append(text[last:])
    return "".join(out)


def _first(pattern: str) -> Callable[[str], List[str]]:
    rx = re.compile(pattern, re.MULTILINE)

    def read(text: str) -> List[str]:
        m = rx.search(text)
        return [m.group(2)] if m else []

    return read


def _rewrite_first(pattern: str, path: str) -> Callable[[str, str, str], str]:
    def write(text: str, old: str, new: str) -> str:
        return _sub_exact(pattern, text, old, new, 1, path)

    return write


def _version_file_read(text: str) -> List[str]:
    return [text.strip()] if text.strip() else []


def _version_file_write(text: str, old: str, new: str) -> str:
    if text.strip() != old:
        raise VersionError(f"VERSION: holds {text.strip()}, expected {old}")
    return new + "\n"


def _package_json_read(text: str) -> List[str]:
    data = json.loads(text)
    version = data.get("version") if isinstance(data, dict) else None
    return [version] if isinstance(version, str) else []


def _lock_read(text: str) -> List[str]:
    data = json.loads(text)
    if not isinstance(data, dict):
        return []
    root = (data.get("packages") or {}).get("") or {}
    found = [data.get("version"), root.get("version")]
    return [v for v in found if isinstance(v, str)] if all(found) else []


# The top-level "version" of a package.json or lockfile is the first one at two-space indent; the
# lockfile's packages[""] entry is the first "version" at six-space indent after `    "": {`.
PKG_VERSION = r'^(  "version": ")([^"]*)(",?)$'
LOCK_ROOT_VERSION = r'(^    "": \{\n(?:      .*\n)*?      "version": ")([^"]*)(")'


def _package_json_write(path: str) -> Callable[[str, str, str], str]:
    def write(text: str, old: str, new: str) -> str:
        out = _sub_exact(PKG_VERSION, text, old, new, 1, path)
        before, after = json.loads(text), json.loads(out)
        before["version"] = new
        if before != after:  # pragma: no cover - guards the line-based edit
            raise VersionError(f"{path}: rewriting the version changed other fields")
        return out

    return write


def _lock_write(text: str, old: str, new: str) -> str:
    path = "frontend-react/package-lock.json"
    out = _sub_exact(PKG_VERSION, text, old, new, 1, path)
    out = _sub_exact(LOCK_ROOT_VERSION, out, old, new, 1, path)
    before, after = json.loads(text), json.loads(out)
    before["version"] = new
    before["packages"][""]["version"] = new
    if before != after:  # pragma: no cover - guards the line-based edit
        raise VersionError(f"{path}: rewriting the version changed other fields")
    return out


def _compose_read(text: str) -> List[str]:
    return COMPOSE_DEFAULT.findall(text)


def _compose_write(text: str, old: str, new: str) -> str:
    found = COMPOSE_DEFAULT.findall(text)
    if not found:
        raise VersionError(f"{COMPOSE_FILE}: no ${{TRUSTAGENT_VERSION:-…}} default tag")
    stray = sorted(set(found) - {old})
    if stray:
        raise VersionError(f"{COMPOSE_FILE}: default tag {', '.join(stray)}, expected {old}")
    return COMPOSE_DEFAULT.sub("${TRUSTAGENT_VERSION:-" + new + "}", text)


PYPROJECT_VERSION = r'^(version = ")([^"]*)(")$'
INIT_VERSION = r'^(__version__ = ")([^"]*)(")$'
GO_VERSION = r'^(var Version = ")([^"]*)(")$'

MIRRORS: Tuple[Mirror, ...] = (
    Mirror("VERSION", "the version", _version_file_read, _version_file_write),
    Mirror(
        "backend-python/pyproject.toml",
        "[tool.poetry] version",
        _first(PYPROJECT_VERSION),
        _rewrite_first(PYPROJECT_VERSION, "backend-python/pyproject.toml"),
    ),
    Mirror(
        "backend-python/src/truthtable/__init__.py",
        "__version__",
        _first(INIT_VERSION),
        _rewrite_first(INIT_VERSION, "backend-python/src/truthtable/__init__.py"),
    ),
    Mirror(
        "frontend-react/package.json",
        "version",
        _package_json_read,
        _package_json_write("frontend-react/package.json"),
    ),
    Mirror(
        "frontend-react/package-lock.json",
        'version and packages[""].version',
        _lock_read,
        _lock_write,
    ),
    Mirror(
        "backend-go/internal/version/version.go",
        "var Version",
        _first(GO_VERSION),
        _rewrite_first(GO_VERSION, "backend-go/internal/version/version.go"),
    ),
    Mirror(COMPOSE_FILE, "default image tag", _compose_read, _compose_write),
)


def read_text(root: Path, rel: str) -> str:
    try:
        return (root / rel).read_text(encoding="utf-8")
    except OSError as exc:
        raise VersionError(f"{rel}: cannot be read ({exc.strerror or exc})") from exc


def read_version(root: Path) -> str:
    """The contents of VERSION; VersionError when it is missing or not semver."""
    version = read_text(root, "VERSION").strip()
    try:
        parse_semver(version)
    except ValueError:
        raise VersionError(f"VERSION: {version!r} is not a semantic version (X.Y.Z)") from None
    return version


def mirror_versions(root: Path, mirror: Mirror) -> List[str]:
    text = read_text(root, mirror.path)
    try:
        found = mirror.read(text)
    except (ValueError, AttributeError) as exc:
        raise VersionError(f"{mirror.path}: cannot be parsed ({exc})") from exc
    if not found:
        raise VersionError(f"{mirror.path}: no {mirror.what} found")
    return found


def overlay_files(root: Path) -> List[Path]:
    """The compose overlays (every docker-compose.*.yml next to docker-compose.yml)."""
    return sorted(p for p in root.glob("docker-compose.*.yml") if p.name != COMPOSE_FILE)


# ------------------------------------------------------------------------------ changelog


def changelog_problems(text: str, version: str) -> List[str]:
    """What keeps CHANGELOG.md from describing release ``version``."""
    problems = []
    released = re.compile(rf"(?m)^## v{re.escape(version)}(?:\s|$)")
    unreleased = re.compile(rf"(?m)^## Unreleased — {re.escape(version)}(?:\s|$)")
    if not released.search(text):
        problems.append(
            f"has no released heading '## v{version}' (scripts/bump_version.py adds it)"
        )
    if unreleased.search(text):
        problems.append(f"still has the heading '## Unreleased — {version}'")
    return problems


# ------------------------------------------------------------------------------ check


@dataclass
class Report:
    errors: List[str]
    warnings: List[str]


def check(root: Path, tag: Optional[str] = None) -> Report:
    report = Report(errors=[], warnings=[])
    try:
        version = read_version(root)
    except VersionError as exc:
        report.errors.append(str(exc))
        return report

    for mirror in MIRRORS[1:]:
        try:
            found = mirror_versions(root, mirror)
        except VersionError as exc:
            report.errors.append(str(exc))
            continue
        wrong = sorted(set(found) - {version})
        if wrong:
            report.errors.append(
                f"{mirror.path}: {mirror.what} is {', '.join(wrong)}, VERSION is {version}"
            )

    for path in overlay_files(root):
        found = COMPOSE_DEFAULT.findall(path.read_text(encoding="utf-8"))
        wrong = sorted(set(found) - {version})
        if wrong:
            report.warnings.append(
                f"{path.name}: default image tag {', '.join(wrong)} differs from VERSION "
                f"{version} (it runs another image than {COMPOSE_FILE} when "
                "TRUSTAGENT_VERSION is unset)"
            )

    if tag is not None:
        if not tag.startswith("v") or not SEMVER.match(tag[1:]):
            report.errors.append(f"tag {tag!r} is not a release tag vX.Y.Z")
        elif tag[1:] != version:
            report.errors.append(f"tag {tag} does not match VERSION {version}")
        else:
            try:
                text = read_text(root, CHANGELOG)
            except VersionError as exc:
                report.errors.append(str(exc))
            else:
                report.errors.extend(f"{CHANGELOG}: {p}" for p in changelog_problems(text, version))
    return report


def main(argv: Optional[Sequence[str]] = None, root: Path = ROOT) -> int:
    parser = argparse.ArgumentParser(
        prog="check_version.py",
        description="Check that every version mirror agrees with VERSION (D127).",
    )
    parser.add_argument(
        "--tag",
        metavar="vX.Y.Z",
        help="also require that this release tag names VERSION and that CHANGELOG.md has its "
        "released heading (the release workflows pass the pushed tag)",
    )
    args = parser.parse_args(argv)

    report = check(root, args.tag)
    for warning in report.warnings:
        print(f"check_version: warning: {warning}", file=sys.stderr)
    if report.errors:
        for error in report.errors:
            print(f"check_version: {error}", file=sys.stderr)
        print(
            "check_version: FAILED. Set the version with scripts/bump_version.py X.Y.Z "
            "(make version-bump V=X.Y.Z), never by hand.",
            file=sys.stderr,
        )
        return 2 if any("cannot be read" in e for e in report.errors) else 1
    version = read_version(root)
    suffix = f", tag {args.tag}" if args.tag else ""
    print(f"✅ Version {version}: VERSION and its {len(MIRRORS) - 1} mirrors agree{suffix}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
