#!/usr/bin/env python3
"""The GitHub release notes for a tag (standard library only).

    python3 scripts/release_notes.py --tag v2.0.0                      # inspects the published images
    python3 scripts/release_notes.py --tag v2.0.0 --inspect canned.json  # canned inspect output
    make release-notes TAG=v2.0.0 | make clip                          # the same, onto the clipboard

The notes are Markdown on stdout, in this order:

1. The CHANGELOG.md section of the tag's version, without its heading (the release title says it)
   and without its "Build log by phase" (a link to the tagged CHANGELOG replaces it). Relative
   links become links into the tagged tree, so they work on the release page.
2. "Numbers in this release": every measured figure with its [measured] label, the run and the
   decision-model snapshot, from contracts/claims/measured.json and value-claims.v1.json; only a
   measured run with mock:false counts. The demo tape's kind (recorded or simulated) is named too.
3. "Images": each image's digest and platforms, read anonymously with
   ``docker buildx imagetools inspect --format '{{json .Manifest}}'`` (DOCKER_CONFIG points at an
   empty directory, so no saved login is used: what a stranger can pull is what is listed), then
   the commands that verify them (``gh attestation verify`` per digest).

It refuses (exit 1) when an image has no digest or lacks linux/amd64 or linux/arm64, when the tag
does not name VERSION, when CHANGELOG.md has no released heading for it, or when the licence still
holds the owner placeholder. A rehearsal tag (a pre-release that says "rehearsal", such as
v0.0.0-rehearsal) takes the upcoming "Unreleased" section (or VERSION's, after the bump) and says
it is a rehearsal at the top.

``--inspect FILE`` reads canned output instead of calling docker: a JSON object whose keys are the
service names (proxy, engine, dashboard) or the image references, and whose values are what the
inspect command prints (the manifest list). Tests and rehearsals use it.

Exit status: 0 notes printed, 1 refused, 2 usage error or an unreadable file.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, List, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_version as cv  # noqa: E402

ROOT = cv.ROOT
REPO = "timkush1/TrustAgent"
REPO_URL = f"https://github.com/{REPO}"
REGISTRY = "ghcr.io/timkush1"
SERVICES = ("proxy", "engine", "dashboard")
PLATFORMS = ("linux/amd64", "linux/arm64")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
BUILD_LOG_HEADING = re.compile(r"^### Build log by phase\s*$", re.MULTILINE)
# A Markdown link or image target that is relative to the repository (not a URL, not an anchor).
RELATIVE_LINK = re.compile(r"(\]\()(?!https?://|mailto:|#)([^)\s]+)(\))")
OWNER_PLACEHOLDER = "{{OWNER_LEGAL_NAME}}"
MEASURED_PATH = "contracts/claims/measured.json"
CLAIMS_PATH = "contracts/claims/value-claims.v1.json"
RECORDED_MANIFEST = "frontend-react/demo-data/recorded/manifest.json"

Inspector = Callable[[str, str], Any]


class Refused(Exception):
    """The notes cannot be made; the message says why."""


class UsageError(Exception):
    """A bad argument or an unreadable file."""


@dataclass
class Image:
    service: str
    ref: str
    digest: str
    platforms: List[str] = field(default_factory=list)


# ------------------------------------------------------------------------------ tag and changelog


def parse_tag(tag: str) -> Tuple[str, bool]:
    """(version, rehearsal) for a tag vX.Y.Z[-pre]; UsageError otherwise."""
    if not tag.startswith("v") or not cv.SEMVER.match(tag[1:]):
        raise UsageError(f"tag {tag!r} is not a release tag vX.Y.Z")
    version = tag[1:]
    _, _, _, pre = cv.parse_semver(version)
    return version, any("rehearsal" in ident for ident in pre)


def changelog_section(text: str, version: str, released_only: bool) -> Tuple[str, str]:
    """(heading line, body) of ``version``'s section: the released ``## vX.Y.Z`` heading, or with
    ``released_only=False`` also its ``## Unreleased — X.Y.Z`` heading. Refused when absent."""
    names = [rf"v{re.escape(version)}"]
    if not released_only:
        names.append(rf"Unreleased — {re.escape(version)}")
    m = re.search(rf"(?m)^## (?:{'|'.join(names)})(?:\s.*)?$", text)
    if not m:
        raise Refused(
            f"CHANGELOG.md has no heading '## v{version}' (make version-bump V={version} adds it)"
        )
    rest = text[m.end() :]
    nxt = re.search(r"(?m)^## ", rest)
    body = rest[: nxt.start()] if nxt else rest
    return m.group(0).strip(), body


def release_body(body: str, tag: str) -> str:
    """The section's body for a release page: the build log replaced by a link, links absolute."""
    log = BUILD_LOG_HEADING.search(body)
    changelog_url = f"{REPO_URL}/blob/{tag}/CHANGELOG.md"
    if log:
        body = body[: log.start()].rstrip() + (
            f"\n\nThe build log by phase is in [CHANGELOG.md]({changelog_url})."
        )
    base = f"{REPO_URL}/blob/{tag}/"
    body = RELATIVE_LINK.sub(lambda m: f"{m.group(1)}{base}{m.group(2)}{m.group(3)}", body)
    return body.strip()


# ------------------------------------------------------------------------------ images


def image_ref(service: str, version: str) -> str:
    return f"{REGISTRY}/truthtable-{service}:{version}"


def docker_inspect(ref: str, docker_config: str) -> Any:
    """``docker buildx imagetools inspect`` as a stranger: no saved login (DOCKER_CONFIG empty)."""
    env = dict(os.environ, DOCKER_CONFIG=docker_config)
    proc = subprocess.run(  # noqa: S603 - fixed argv, no shell
        ["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", ref],
        capture_output=True,
        text=True,
        env=env,
        timeout=120,
        check=False,
    )
    if proc.returncode != 0:
        raise Refused(
            f"{ref}: docker buildx imagetools inspect failed: {proc.stderr.strip()[:300]}"
        )
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise Refused(f"{ref}: the inspect output is not JSON ({exc})") from None


def canned_inspector(path: Path) -> Inspector:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise UsageError(f"{path}: {exc}") from None
    if not isinstance(data, dict):
        raise UsageError(f"{path}: expected an object of service (or image) -> inspect output")

    def inspect(service: str, ref: str) -> Any:
        for key in (ref, service):
            if key in data:
                return data[key]
        raise Refused(f"{ref}: not in {path.name}")

    return inspect


def read_image(service: str, ref: str, manifest: Any) -> Image:
    """The digest and platforms of one inspect output; Refused when either falls short."""
    if not isinstance(manifest, dict):
        raise Refused(f"{ref}: the inspect output is not a manifest")
    digest = manifest.get("digest")
    if not isinstance(digest, str) or not DIGEST.match(digest):
        raise Refused(f"{ref}: no image digest (the release notes list one per image)")
    platforms = []
    for entry in manifest.get("manifests") or []:
        plat = entry.get("platform") if isinstance(entry, dict) else None
        if not isinstance(plat, dict) or plat.get("os") in (None, "unknown"):
            continue  # attestation manifests (unknown/unknown)
        name = f"{plat.get('os')}/{plat.get('architecture')}"
        if plat.get("variant"):
            name += f"/{plat['variant']}"
        if name not in platforms:
            platforms.append(name)
    missing = [p for p in PLATFORMS if p not in platforms]
    if missing:
        raise Refused(
            f"{ref}: no {' or '.join(missing)} image (found {', '.join(platforms) or 'none'})"
        )
    return Image(service=service, ref=ref, digest=digest, platforms=platforms)


def images(version: str, inspect: Inspector) -> List[Image]:
    """Every service's image, or Refused naming every one that falls short."""
    found, problems = [], []
    for service in SERVICES:
        ref = image_ref(service, version)
        try:
            found.append(read_image(service, ref, inspect(service, ref)))
        except Refused as exc:
            problems.append(str(exc))
    if problems:
        raise Refused("; ".join(problems))
    return found


def images_section(found: List[Image], tag: str) -> List[str]:
    lines = [
        "## Images",
        "",
        "Each image is built for linux/amd64 and linux/arm64, with an SBOM and a build provenance "
        "attestation.",
        "",
        "| Service | Image | Digest | Platforms |",
        "|---|---|---|---|",
    ]
    for img in found:
        lines.append(
            f"| {img.service} | `{img.ref}` | `{img.digest}` | {', '.join(img.platforms)} |"
        )
    repo_image = lambda img: img.ref.rsplit(":", 1)[0]  # noqa: E731
    lines += [
        "",
        "Verify what you pull (the GitHub CLI checks each build attestation):",
        "",
        "```bash",
    ]
    for img in found:
        lines.append(f"gh attestation verify oci://{repo_image(img)}@{img.digest} -R {REPO}")
    for img in found:
        lines.append(f"docker buildx imagetools inspect {img.ref}")
    lines += [
        "```",
        "",
        f"Run this release: check out `{tag}` and run `make up` (or `make demo`, which needs no key); "
        "a release checkout pulls these images instead of building them.",
    ]
    return lines


# ------------------------------------------------------------------------------ numbers


def _round_half_up(x: float) -> float:
    return float(math.floor(x + 0.5))  # JavaScript's Math.round


def format_value(value: float, unit: str) -> str:
    """Same formatting as scripts/check-claims.mjs and the landing page."""
    if unit == "fraction":
        return f"{_round_half_up(value * 1000) / 10:g}%"
    if unit == "usd_per_1k":
        return f"{value:.2f}" if value >= 1 else f"{value:.3f}"
    return f"{_round_half_up(value * 100) / 100:g}"


def _read_json(root: Path, rel: str) -> Optional[Any]:
    path = root / rel
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise UsageError(f"{rel}: {exc}") from None


def numbers_section(root: Path) -> List[str]:
    """Every measured figure with its label; a simulated or missing run is said as such."""
    measured = _read_json(root, MEASURED_PATH) or {}
    claims = (_read_json(root, CLAIMS_PATH) or {}).get("claims") or []
    lines = ["## Numbers in this release", ""]
    is_run = measured.get("status") == "measured" and measured.get("mock") is False
    values = measured.get("values") or {}
    shown = []
    if is_run:
        for claim in claims:
            if claim.get("label") != "measured":
                continue
            entry = values.get(claim.get("measured_ref")) or {}
            value = entry.get("value")
            if not isinstance(value, (int, float)):
                continue
            display = claim["display"].replace(
                "{value}", format_value(value, claim.get("unit", ""))
            )
            run = f" (run {entry['run_id']})" if entry.get("run_id") else ""
            shown.append(f"- {claim['text']}: {display} [measured]{run}")
    if shown:
        snap = measured.get("snapshot")
        snap_text = " and ".join(snap) if isinstance(snap, list) else snap
        lines += shown
        lines += [
            "",
            f"Measured runs: {', '.join(measured.get('runs') or [])}"
            + (f", decision-model snapshot {snap_text}" if snap_text else "")
            + ". Every other figure in the README and on the site carries its own label: "
            "estimate, vendor, third-party or simulated.",
        ]
    else:
        lines.append(
            "No measured benchmark run is published in this release: every figure in the README "
            "and on the site is labelled as an estimate, a vendor price, a third-party result or "
            "simulated."
        )
    tape = _read_json(root, RECORDED_MANIFEST)
    if isinstance(tape, dict) and tape.get("kind") == "recorded":
        model = tape.get("decision_model") or {}
        lines += [
            "",
            f"The live demo replays a recorded tape: real fast-check and judge answers, recorded "
            f"{str(tape.get('recorded_at', ''))[:10]} on snapshot {model.get('snapshot')} with "
            f"TrustAgent {tape.get('trustagent_version')}.",
        ]
    else:
        lines += [
            "",
            "The live demo replays a simulated tape (mock checks and a mock judge): every page "
            "says Simulated.",
        ]
    return lines


# ------------------------------------------------------------------------------ notes


def build_notes(root: Path, tag: str, inspect: Inspector) -> Tuple[str, List[str]]:
    """(the Markdown notes, warnings). Raises Refused or UsageError."""
    version, rehearsal = parse_tag(tag)
    warnings: List[str] = []
    current = cv.read_version(root)
    try:
        changelog = (root / "CHANGELOG.md").read_text(encoding="utf-8")
    except OSError as exc:
        raise UsageError(f"CHANGELOG.md: {exc}") from None
    if rehearsal:
        # Before the bump the release being rehearsed is the upcoming "## Unreleased — X.Y.Z"
        # section; after it, VERSION's own.
        upcoming = re.search(r"(?m)^## Unreleased — (\S+)", changelog)
        section_version = upcoming.group(1) if upcoming else current
        warnings.append(f"{tag} is a rehearsal: the notes use the {section_version} section")
    else:
        if version != current:
            raise Refused(f"tag {tag} does not match VERSION {current}")
        section_version = version
    _, body = changelog_section(changelog, section_version, released_only=not rehearsal)
    licence = (root / "LICENSE").read_text(encoding="utf-8") if (root / "LICENSE").is_file() else ""
    if OWNER_PLACEHOLDER in licence:
        if not rehearsal:
            raise Refused(
                f"LICENSE still says {OWNER_PLACEHOLDER}: write the owner's legal name first"
            )
        warnings.append(f"LICENSE still says {OWNER_PLACEHOLDER}")
    found = images(version, inspect)
    out: List[str] = []
    if rehearsal:
        out += [f"> **Rehearsal ({tag}): canned or test data, not for publishing.**", ""]
    out += [release_body(body, tag), ""]
    out += numbers_section(root) + [""]
    out += images_section(found, tag)
    return "\n".join(out).rstrip() + "\n", warnings


def main(argv: Optional[Sequence[str]] = None, root: Path = ROOT) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("--tag", required=True, help="the release tag, e.g. v2.0.0")
    parser.add_argument(
        "--inspect", type=Path, help="canned inspect output (JSON) instead of docker"
    )
    parser.add_argument("--out", type=Path, help="write the notes here instead of stdout")
    parser.add_argument("--root", type=Path, default=root, help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    try:
        if args.inspect:
            notes, warnings = build_notes(args.root, args.tag, canned_inspector(args.inspect))
        else:
            with tempfile.TemporaryDirectory(prefix="release-notes-docker-") as empty:
                notes, warnings = build_notes(
                    args.root, args.tag, lambda _svc, ref: docker_inspect(ref, empty)
                )
    except UsageError as exc:
        print(f"release-notes: {exc}", file=sys.stderr)
        return 2
    except (Refused, cv.VersionError) as exc:
        print(f"release-notes: refused: {exc}", file=sys.stderr)
        return 1
    for warning in warnings:
        print(f"release-notes: warning: {warning}", file=sys.stderr)
    if args.out:
        args.out.write_text(notes, encoding="utf-8")
        print(f"release-notes: wrote {args.out}", file=sys.stderr)
    else:
        sys.stdout.write(notes)
    return 0


if __name__ == "__main__":
    sys.exit(main())
