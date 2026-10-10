#!/usr/bin/env python3
"""make_hero_gif.py - turn the hero recording into the README GIF and the release MP4 (D115).

`npm run hero` (frontend-react/e2e/launch/hero.spec.ts) records the first-visit story of the static
site as a WebM video and writes a sidecar, frontend-react/test-results/launch/hero/hero.json, with
the tape it played (`kind`: recorded or mock, and its label). This script reads that sidecar and
makes, with ffmpeg:

  * the GIF, two-pass with a generated palette: 1200 px wide, 12 frames a second, at most 8 MB.
    When the first try is too large it retries with a smaller palette (256, 128, then 64 colours)
    and fails, leaving nothing behind, when even that is too large;
  * the MP4 (H.264, yuv420p, faststart), the same width, for the GitHub release.

Honest numbers: the hero shows the product's output, so it is made only from a recorded tape
(the real fast check and judge). A simulated tape (`kind: mock`) is refused unless
HERO_ALLOW_MOCK=1, and then both files go only to the scratch directory
(frontend-react/test-results/launch/), never to the README's images.

Usage
    python3 scripts/make_hero_gif.py                 # recorded: docs/images/v2/hero.gif, tmp/launch/hero.mp4
    HERO_ALLOW_MOCK=1 python3 scripts/make_hero_gif.py  # simulated: scratch copies only
    options: --meta PATH, --gif PATH, --mp4 PATH, --ffmpeg BIN (else $FFMPEG, else ffmpeg on PATH),
             --max-bytes N (8 MB), --width N (1200), --fps N (12)

Exit codes: 0 done; 1 the output does not meet its limits (too large, wrong size, ffmpeg failed);
2 refused or a usage error (a simulated tape, a missing recording or ffmpeg, a target outside the
scratch directory for a simulated tape).

Standard library only.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess  # nosec B404 - ffmpeg with a fixed argument list, never a shell
import sys
import tempfile
from pathlib import Path
from typing import List, Optional, Sequence

ROOT = Path(__file__).resolve().parents[1]
SCRATCH_DIR = ROOT / "frontend-react" / "test-results" / "launch"
DEFAULT_META = SCRATCH_DIR / "hero" / "hero.json"
DEFAULT_GIF = ROOT / "docs" / "images" / "v2" / "hero.gif"
# The MP4 is a release asset (uploaded to the GitHub release), not a committed file: tmp/ is the
# repository's ignored scratch space.
DEFAULT_MP4 = ROOT / "tmp" / "launch" / "hero.mp4"
SCRATCH_GIF = SCRATCH_DIR / "hero.gif"
SCRATCH_MP4 = SCRATCH_DIR / "hero.mp4"

MAX_BYTES = 8 * 1024 * 1024
WIDTH = 1200
FPS = 12
PALETTE_COLOURS = (256, 128, 64)
SIDECAR_SCHEMA = "trustagent.hero-capture/v1"
ALLOW_MOCK_ENV = "HERO_ALLOW_MOCK"


class Refused(Exception):
    """The run is refused before anything is made (exit 2)."""


class Failed(Exception):
    """Something was made but does not meet its limits, or ffmpeg failed (exit 1)."""


def rel(path: Path) -> str:
    """A path relative to the repository when it is inside it, for messages."""
    try:
        return str(path.resolve().relative_to(ROOT))
    except ValueError:
        return str(path)


def in_scratch(path: Path) -> bool:
    resolved = path.resolve()
    scratch = SCRATCH_DIR.resolve()
    return resolved == scratch or scratch in resolved.parents


def read_sidecar(meta: Path) -> dict:
    """The sidecar hero.spec.ts wrote, checked; raises Refused when it is missing or malformed."""
    if not meta.is_file():
        raise Refused(
            f"no hero recording at {rel(meta)}: run `cd frontend-react && npm run hero` first "
            "(make hero-gif runs both)"
        )
    try:
        doc = json.loads(meta.read_text(encoding="utf-8"))
    except (OSError, ValueError) as err:
        raise Refused(f"{rel(meta)} is not readable JSON ({err})") from err
    if not isinstance(doc, dict) or doc.get("schema") != SIDECAR_SCHEMA:
        raise Refused(f"{rel(meta)} is not a {SIDECAR_SCHEMA} sidecar")
    if doc.get("kind") not in ("recorded", "mock"):
        raise Refused(f"{rel(meta)} names an unknown tape kind: {doc.get('kind')!r}")
    if not isinstance(doc.get("label"), str) or not doc["label"].strip():
        raise Refused(f"{rel(meta)} has no tape label")
    video = doc.get("video")
    if not isinstance(video, str) or not video:
        raise Refused(f"{rel(meta)} names no video")
    path = (meta.parent / video).resolve()
    if not path.is_file():
        raise Refused(f"the hero video {rel(path)} is missing: run `npm run hero` again")
    doc["video_path"] = path
    return doc


def plan_outputs(
    kind: str, gif: Optional[Path], mp4: Optional[Path], allow_mock: bool
) -> tuple[Path, Path]:
    """Where the GIF and the MP4 go. A simulated tape needs HERO_ALLOW_MOCK=1 and stays in scratch."""
    if kind == "recorded":
        return (gif or DEFAULT_GIF), (mp4 or DEFAULT_MP4)
    if not allow_mock:
        raise Refused(
            "the hero recording replays the simulated tape (kind: mock), which is not model output. "
            "Record the demo first (make record-demo CONFIRM=1), build with it and run "
            f"`npm run hero` again, or set {ALLOW_MOCK_ENV}=1 for a scratch copy in {rel(SCRATCH_DIR)}/"
        )
    gif, mp4 = (gif or SCRATCH_GIF), (mp4 or SCRATCH_MP4)
    for target in (gif, mp4):
        if not in_scratch(target):
            raise Refused(
                f"a simulated hero goes only to {rel(SCRATCH_DIR)}/, not {rel(target)} "
                f"({ALLOW_MOCK_ENV}=1 is for scratch copies)"
            )
    return gif, mp4


def find_ffmpeg(explicit: Optional[str], env: Optional[dict] = None) -> str:
    env = os.environ if env is None else env
    candidate = explicit or env.get("FFMPEG") or shutil.which("ffmpeg")
    if not candidate:
        raise Refused(
            "ffmpeg is not installed: install it (sudo apt install ffmpeg, brew install ffmpeg) "
            "or pass --ffmpeg /path/to/ffmpeg"
        )
    if os.sep in candidate and not os.access(candidate, os.X_OK):
        raise Refused(f"{candidate} is not an executable ffmpeg")
    return candidate


def scale_filter(width: int, fps: int) -> str:
    return f"fps={fps},scale={width}:-1:flags=lanczos"


def palette_args(
    ffmpeg: str, video: Path, palette: Path, width: int, fps: int, colours: int
) -> List[str]:
    """Pass 1: one palette for the whole clip, from the pixels that change (stats_mode=diff)."""
    vf = f"{scale_filter(width, fps)},palettegen=max_colors={colours}:stats_mode=diff"
    return [ffmpeg, "-y", "-v", "error", "-i", str(video), "-vf", vf, str(palette)]


def gif_args(ffmpeg: str, video: Path, palette: Path, out: Path, width: int, fps: int) -> List[str]:
    """Pass 2: the GIF through that palette, redrawing only the rectangle that changed."""
    lavfi = (
        f"{scale_filter(width, fps)} [x]; "
        "[x][1:v] paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle"
    )
    return [
        ffmpeg,
        "-y",
        "-v",
        "error",
        "-i",
        str(video),
        "-i",
        str(palette),
        "-lavfi",
        lavfi,
        "-loop",
        "0",
        "-f",
        "gif",
        str(out),
    ]


def mp4_args(ffmpeg: str, video: Path, out: Path, width: int) -> List[str]:
    vf = f"scale={width}:-2:flags=lanczos,format=yuv420p"
    return [
        ffmpeg,
        "-y",
        "-v",
        "error",
        "-i",
        str(video),
        "-vf",
        vf,
        "-c:v",
        "libx264",
        "-preset",
        "slow",
        "-crf",
        "23",
        "-movflags",
        "+faststart",
        "-an",
        "-f",
        "mp4",
        str(out),
    ]


def run(argv: Sequence[str]) -> None:
    try:
        proc = subprocess.run(  # nosec B603 - an argument list; argv[0] is the ffmpeg chosen above
            list(argv), capture_output=True, text=True, check=False
        )
    except OSError as err:
        raise Failed(f"could not run {argv[0]}: {err}") from err
    if proc.returncode != 0:
        tail = (proc.stderr or proc.stdout or "").strip().splitlines()[-5:]
        raise Failed(f"ffmpeg failed (exit {proc.returncode}): " + " | ".join(tail))


def gif_size(data: bytes) -> tuple[int, int]:
    """Width and height from a GIF's logical screen descriptor; raises Failed when it is no GIF."""
    if len(data) < 10 or data[:6] not in (b"GIF87a", b"GIF89a"):
        raise Failed("the output is not a GIF")
    return int.from_bytes(data[6:8], "little"), int.from_bytes(data[8:10], "little")


def make_gif(
    ffmpeg: str, video: Path, out: Path, width: int, fps: int, max_bytes: int
) -> tuple[int, int]:
    """Builds the GIF in a temporary file next to `out` and moves it in place only when it fits.

    Returns (bytes, colours). A GIF that never fits, or has the wrong width, leaves `out` as it was.
    """
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".hero-", dir=out.parent) as work:
        palette = Path(work) / "palette.png"
        candidate = Path(work) / "hero.gif"
        sizes = []
        for colours in PALETTE_COLOURS:
            run(palette_args(ffmpeg, video, palette, width, fps, colours))
            run(gif_args(ffmpeg, video, palette, candidate, width, fps))
            data = candidate.read_bytes()
            got_width, _ = gif_size(data)
            if got_width != width:
                raise Failed(f"the GIF is {got_width} px wide, not {width}")
            sizes.append(f"{colours} colours: {len(data):,} bytes")
            if len(data) <= max_bytes:
                os.replace(candidate, out)
                return len(data), colours
        raise Failed(
            f"the GIF is over {max_bytes:,} bytes at every palette size ({'; '.join(sizes)}): "
            "record a shorter story (hero.spec.ts BEAT) and run again"
        )


def make_mp4(ffmpeg: str, video: Path, out: Path, width: int) -> int:
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".hero-", dir=out.parent) as work:
        candidate = Path(work) / "hero.mp4"
        run(mp4_args(ffmpeg, video, candidate, width))
        data = candidate.read_bytes()
        if len(data) < 12 or data[4:8] != b"ftyp":
            raise Failed("the MP4 output is not an MP4 file")
        os.replace(candidate, out)
        return len(data)


def main(argv: Optional[Sequence[str]] = None, env: Optional[dict] = None) -> int:
    env = os.environ if env is None else env
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--meta", type=Path, default=DEFAULT_META, help="the hero.json sidecar")
    parser.add_argument("--gif", type=Path, help="the GIF to write")
    parser.add_argument("--mp4", type=Path, help="the MP4 to write")
    parser.add_argument("--ffmpeg", help="the ffmpeg binary")
    parser.add_argument("--max-bytes", type=int, default=MAX_BYTES)
    parser.add_argument("--width", type=int, default=WIDTH)
    parser.add_argument("--fps", type=int, default=FPS)
    args = parser.parse_args(argv)
    if args.max_bytes <= 0 or args.width <= 0 or args.fps <= 0:
        parser.error("--max-bytes, --width and --fps must be positive")

    try:
        sidecar = read_sidecar(args.meta)
        allow_mock = env.get(ALLOW_MOCK_ENV) == "1"
        gif, mp4 = plan_outputs(sidecar["kind"], args.gif, args.mp4, allow_mock)
        ffmpeg = find_ffmpeg(args.ffmpeg, env)
    except Refused as err:
        print(f"make_hero_gif: refused: {err}", file=sys.stderr)
        return 2

    video = sidecar["video_path"]
    try:
        size, colours = make_gif(ffmpeg, video, gif, args.width, args.fps, args.max_bytes)
        mp4_size = make_mp4(ffmpeg, video, mp4, args.width)
    except Failed as err:
        print(f"make_hero_gif: failed: {err}", file=sys.stderr)
        return 1

    what = "Simulated, scratch only" if sidecar["kind"] == "mock" else "recorded tape"
    print(
        f"make_hero_gif: {rel(gif)}: {size:,} bytes, {args.width} px, {args.fps} fps, {colours} colours"
    )
    print(f"make_hero_gif: {rel(mp4)}: {mp4_size:,} bytes (attach it to the GitHub release)")
    print(f"make_hero_gif: {what}: {sidecar['label']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
