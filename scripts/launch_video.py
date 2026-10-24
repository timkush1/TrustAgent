#!/usr/bin/env python3
"""launch_video.py - the 60-second launch video (D210; SPEC §8.2, erratum H-6).

    python3 scripts/launch_video.py srt                    # the captions, from the demo script
    python3 scripts/launch_video.py stack --attempt <id>   # the stack beats (record-demo ACCEPT=1 VIDEO=1)
    python3 scripts/launch_video.py static                 # the static beats, from the Pages build
    python3 scripts/launch_video.py assemble [--pages-only] # clips + burned-in captions -> tmp/launch/
    make launch-video                                      # static, then assemble
    make launch-video-stack ATTEMPT=<id>                   # stack (ACCEPT=1 VIDEO=1 runs it)

The take follows the table in docs/launch/demo-script.md, one beat per row: the landing hero, the
replay's Traffic, the featured trace (D209), the Studio, the Policies fallback and Savings. Two
recorders make the clips (Playwright recordVideo, 1920x1080, light theme):

* the stack beats (Studio draft, test on samples, test on past traffic, the payments-faq fallback,
  Savings) run on the record stack of a reviewed attempt before ACCEPT removes it: the dashboard is
  published on the attempt's 127.0.0.1 port (tmp/record-demo/<attempt>/video.yml), the spec signs
  in with the attempt's admin key, changes nothing (the policy's id and content are compared before
  and after, P5C-27), and its paid calls sit inside the record ledger;
* the static beats (hero, Try the demo, Traffic, the featured trace) record the recorded Pages
  build (`npm run build:launch`, served by vite preview).

``assemble`` joins the clips with ffmpeg: each is scaled into the frame, cut to its row's time
(the clip's own lead, the moment its page was ready, is skipped), the last row ends on a card with
the repository's address, and the captions (an .srt made from the table's Caption column) are
burned in. It writes tmp/launch/video-16x9.mp4 (1920x1080, the release) and
tmp/launch/video-4x5.mp4 (1080x1350, the LinkedIn feed) and tmp/launch/video.srt; MP4s are never
committed. ``--pages-only`` is the H-6 fallback: a 45-second take from the Pages build alone (its
Savings tab instead of the stack beats).

Honest numbers: the video shows the product's output, so every recorder and the assembly refuse a
tape that is not ``recorded``. VIDEO_ALLOW_MOCK=1 makes a rehearsal of the simulated tape (or the
zero-key demo stack) instead: its clips and MP4s go only to tmp/launch/rehearsal/, and every frame
carries "Simulated rehearsal (mock tape): not model output".

Exit status: 0 done; 1 a recorder or ffmpeg failed; 2 refused or a usage error (a tape that is not
recorded, missing clips, a missing tool, a beat table that does not add up). Standard library only.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess  # nosec B404 - fixed argument lists (ffmpeg, npx, docker), never a shell
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
import record_demo  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
WEB = ROOT / "frontend-react"
SCRIPT = ROOT / "docs" / "launch" / "demo-script.md"
OUT_DIR = ROOT / "tmp" / "launch"
REHEARSAL_DIR = OUT_DIR / "rehearsal"
SPEC = "video.spec.ts"
SITE_MANIFEST = WEB / "dist" / "demo" / "manifest.json"

ALLOW_MOCK_ENV = "VIDEO_ALLOW_MOCK"
CLIPS_SCHEMA = "trustagent.video-clips/v1"
REPO_URL = "github.com/timkush1/TrustAgent"
REHEARSAL_STAMP = "Simulated rehearsal (mock tape): not model output"
SIZES: Dict[str, Tuple[int, int]] = {"16x9": (1920, 1080), "4x5": (1080, 1350)}
FPS = 30
BACKGROUND = "0x0b0b0c"
# libass style of the burned-in captions: white on an opaque dark box, at the bottom.
CAPTION_STYLE = (
    "FontName=DejaVu Sans,FontSize=13,PrimaryColour=&H00FFFFFF,OutlineColour=&H00101010,"
    "BackColour=&H00101010,BorderStyle=3,Outline=6,Shadow=0,MarginV=22"
)
CAPTION_WIDTH = 46  # characters a caption line holds before it wraps

# The table's rows, in order, and the beats each row is made of. A beat's clip comes from the
# static build, the stack, or (the end card) ffmpeg itself.
ROW_BEATS: Tuple[Tuple[str, ...], ...] = (
    ("landing",),
    ("traffic",),
    ("trace",),
    ("studio",),
    ("policies",),
    ("savings", "end"),
)
STATIC_BEATS = ("landing", "traffic", "trace", "savings_static")
STACK_BEATS = ("studio", "policies", "savings")
# The last row splits between the Savings strip and the end card.
END_CARD_S = 5.0
# H-6: the Pages-only take, 45 s: the rows it keeps and how long each beat lasts.
PAGES_ONLY: Tuple[Tuple[str, int, float], ...] = (
    ("landing", 0, 5.0),
    ("traffic", 1, 10.0),
    ("trace", 2, 15.0),
    ("savings_static", 5, 7.0),
    ("end", 5, 8.0),
)


class Refused(Exception):
    """Refused before anything is made (exit 2)."""


class Failed(Exception):
    """A recorder or ffmpeg failed (exit 1)."""


def rel(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(ROOT))
    except ValueError:
        return str(path)


# ------------------------------------------------------------------------------ the beat table


@dataclass(frozen=True)
class Row:
    """One row of the demo script's table: its time span and its caption."""

    start: float
    end: float
    screen: str
    caption: str


@dataclass(frozen=True)
class Segment:
    """One piece of the video: a beat's clip (or the end card) for `duration` seconds."""

    beat: str
    row: int
    start: float
    duration: float


TIME_CELL = re.compile(r"^(\d+(?:\.\d+)?)\s*[–-]\s*(\d+(?:\.\d+)?)\s*s$")


def parse_table(markdown: str) -> List[Row]:
    """The rows of the demo script's take table (Time | Screen | What the take shows | Caption)."""
    rows: List[Row] = []
    header: Optional[List[str]] = None
    for line in markdown.splitlines():
        line = line.strip()
        if not line.startswith("|"):
            if header is not None and rows:
                break
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if header is None:
            if cells and cells[0].lower() == "time":
                header = [c.lower() for c in cells]
            continue
        if all(set(c) <= set("-: ") for c in cells):
            continue
        if len(cells) != len(header):
            raise Refused(f"the demo script's table row has {len(cells)} cells: {line[:60]}")
        m = TIME_CELL.match(cells[0])
        if not m:
            raise Refused(f"the demo script's time cell {cells[0]!r} is not like '5–15 s'")
        caption_cell = cells[header.index(next(h for h in header if h.startswith("caption")))]
        rows.append(
            Row(
                start=float(m.group(1)),
                end=float(m.group(2)),
                screen=cells[header.index("screen")] if "screen" in header else "",
                caption=caption_text(caption_cell),
            )
        )
    if header is None or not rows:
        raise Refused("the demo script has no take table (| Time | Screen | ... | Caption |)")
    return rows


def caption_text(cell: str) -> str:
    """A caption cell without its quotes and Markdown code marks."""
    text = cell.strip().strip('"“”').strip()
    return re.sub(r"`([^`]*)`", r"\1", text)


def check_rows(rows: Sequence[Row]) -> None:
    """Six rows, back to back from 0 s, each at least as long as what it holds."""
    if len(rows) != len(ROW_BEATS):
        raise Refused(
            f"the demo script's table has {len(rows)} rows; the take has {len(ROW_BEATS)}"
        )
    at = 0.0
    for i, row in enumerate(rows):
        if row.start != at or row.end <= row.start:
            raise Refused(
                f"the demo script's row {i + 1} runs {row.start:g}–{row.end:g} s, not from {at:g} s"
            )
        if not row.caption:
            raise Refused(f"the demo script's row {i + 1} has no caption")
        at = row.end
    if rows[-1].end - rows[-1].start <= END_CARD_S:
        raise Refused(f"the last row must be longer than the {END_CARD_S:g} s end card")


def segments(rows: Sequence[Row], pages_only: bool = False) -> List[Segment]:
    """The video's pieces in order. The full take follows the table's times; the Pages-only take
    (H-6) keeps rows 1-3 and 6 at its own lengths."""
    check_rows(rows)
    out: List[Segment] = []
    if pages_only:
        at = 0.0
        for beat, row, duration in PAGES_ONLY:
            out.append(Segment(beat=beat, row=row, start=at, duration=duration))
            at += duration
        return out
    for i, (row, beats) in enumerate(zip(rows, ROW_BEATS)):
        if beats == ("savings", "end"):
            first = row.end - row.start - END_CARD_S
            out.append(Segment(beat="savings", row=i, start=row.start, duration=first))
            out.append(Segment(beat="end", row=i, start=row.start + first, duration=END_CARD_S))
        else:
            out.append(Segment(beat=beats[0], row=i, start=row.start, duration=row.end - row.start))
    return out


# ------------------------------------------------------------------------------ captions


def timestamp(seconds: float) -> str:
    ms = int(round(seconds * 1000))
    h, ms = divmod(ms, 3_600_000)
    m, ms = divmod(ms, 60_000)
    s, ms = divmod(ms, 1000)
    return f"{h:02d}:{m:02d}:{s:02d},{ms:03d}"


def wrap(text: str, width: int = CAPTION_WIDTH) -> str:
    """At most two balanced lines (a caption must stay readable on a phone)."""
    if len(text) <= width:
        return text
    words = text.split()
    best = text
    best_gap = len(text)
    for i in range(1, len(words)):
        a, b = " ".join(words[:i]), " ".join(words[i:])
        gap = abs(len(a) - len(b))
        if max(len(a), len(b)) <= width + 8 and gap < best_gap:
            best, best_gap = f"{a}\n{b}", gap
    return best


def srt(rows: Sequence[Row], pages_only: bool = False, rehearsal: bool = False) -> str:
    """The captions: one cue per row over that row's segments, the repository's address on the end
    card, and on a rehearsal a stamp over the whole take."""
    segs = segments(rows, pages_only)
    cues: List[Tuple[float, float, str]] = []
    for i, row in enumerate(rows):
        mine = [s for s in segs if s.row == i]
        if not mine:
            continue
        start = mine[0].start
        end = mine[-1].start + mine[-1].duration
        cues.append((start, end, wrap(row.caption)))
    end_card = next(s for s in segs if s.beat == "end")
    # {\an5}: the middle of the frame (an SRT override tag that libass reads).
    cues.append((end_card.start, end_card.start + end_card.duration, "{\\an5}" + REPO_URL))
    total = segs[-1].start + segs[-1].duration
    if rehearsal:
        cues.append((0.0, total, "{\\an8}" + REHEARSAL_STAMP))
    cues.sort(key=lambda c: (c[0], c[1]))
    blocks = [
        f"{n}\n{timestamp(a)} --> {timestamp(b)}\n{text}\n"
        for n, (a, b, text) in enumerate(cues, 1)
    ]
    return "\n".join(blocks)


# ------------------------------------------------------------------------------ clips


@dataclass
class Clip:
    """A beat's recording: where its page was ready, and the parts to keep (jump cuts at real
    speed, in seconds from the clip's start); no parts means everything from the lead on."""

    beat: str
    path: Path
    lead_s: float
    parts: Tuple[Tuple[float, float], ...] = ()

    def spans(self, duration: float) -> List[Tuple[float, float]]:
        if self.parts:
            return list(self.parts)
        return [(self.lead_s, self.lead_s + duration)]


@dataclass
class ClipSet:
    source: str
    kind: str
    label: str
    clips: Dict[str, Clip]


def read_clips(directory: Path, source: str) -> ClipSet:
    """The sidecar a recorder wrote (clips.json) and the clips it names, checked."""
    meta = directory / "clips.json"
    if not meta.is_file():
        raise Refused(
            f"no {source} clips in {rel(directory)}: run "
            + (
                "make launch-video-stack ATTEMPT=<id> (make record-demo ACCEPT=1 VIDEO=1 runs it)"
                if source == "stack"
                else "python3 scripts/launch_video.py static"
            )
            + ", or assemble --pages-only (H-6)"
        )
    try:
        doc = json.loads(meta.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise Refused(f"{rel(meta)} is not readable JSON ({exc})") from exc
    if (
        not isinstance(doc, dict)
        or doc.get("schema") != CLIPS_SCHEMA
        or doc.get("source") != source
    ):
        raise Refused(f"{rel(meta)} is not a {CLIPS_SCHEMA} sidecar of the {source} beats")
    if doc.get("kind") not in ("recorded", "mock"):
        raise Refused(f"{rel(meta)} names an unknown tape kind: {doc.get('kind')!r}")
    clips: Dict[str, Clip] = {}
    for c in doc.get("clips") or []:
        if not isinstance(c, dict):
            continue
        path = (directory / str(c.get("file") or "")).resolve()
        if directory.resolve() not in path.parents or not path.is_file():
            raise Refused(f"{rel(meta)}: the clip {c.get('file')!r} is missing")
        lead = c.get("lead_ms")
        if isinstance(lead, bool) or not isinstance(lead, (int, float)) or lead < 0:
            raise Refused(f"{rel(meta)}: the clip {c.get('file')!r} has no lead_ms")
        parts: List[Tuple[float, float]] = []
        for part in c.get("parts") or []:
            ok = (
                isinstance(part, list)
                and len(part) == 2
                and all(isinstance(x, (int, float)) and not isinstance(x, bool) for x in part)
                and 0 <= part[0] < part[1]
            )
            if not ok:
                raise Refused(f"{rel(meta)}: the clip {c.get('file')!r} has a bad part {part!r}")
            parts.append((part[0] / 1000, part[1] / 1000))
        clips[str(c.get("beat"))] = Clip(
            beat=str(c.get("beat")), path=path, lead_s=lead / 1000, parts=tuple(parts)
        )
    return ClipSet(
        source=source, kind=str(doc["kind"]), label=str(doc.get("label") or ""), clips=clips
    )


def require_recorded(kind: str, what: str, allow_mock: bool) -> bool:
    """True for a rehearsal (a mock tape with VIDEO_ALLOW_MOCK=1); refuses a mock tape otherwise."""
    if kind == "recorded":
        return False
    if allow_mock:
        return True
    raise Refused(
        f"{what} is the simulated tape (kind: {kind}), not model output: the launch video is made "
        "only from a recorded tape (make record-demo CONFIRM=1, then ACCEPT=1). For a rehearsal "
        f"set {ALLOW_MOCK_ENV}=1: it goes only to {rel(REHEARSAL_DIR)}/ and says Simulated on every frame"
    )


# ------------------------------------------------------------------------------ ffmpeg


def find_tool(name: str, explicit: Optional[str], env: Mapping[str, str]) -> str:
    candidate = explicit or env.get(name.upper()) or shutil.which(name)
    if not candidate:
        raise Refused(
            f"{name} is not installed: install it (sudo apt install ffmpeg, brew install ffmpeg) "
            f"or pass --{name} /path/to/{name}"
        )
    return candidate


def filter_path(path: Path) -> str:
    """A path inside an ffmpeg filter argument; the characters the filter syntax gives a meaning
    are refused rather than escaped."""
    text = str(path)
    if any(ch in text for ch in ":'\\,;[]="):
        raise Refused(f"{text} holds a character ffmpeg's filter syntax reserves; use another path")
    return text


def ffmpeg_args(
    ffmpeg: str,
    plan: Sequence[Segment],
    clips: Mapping[str, Clip],
    captions: Path,
    size: Tuple[int, int],
    out: Path,
) -> List[str]:
    """One ffmpeg run: every segment scaled into the frame, cut to its length, joined, captioned."""
    w, h = size
    args = [ffmpeg, "-hide_banner", "-loglevel", "error", "-y"]
    chains: List[str] = []
    n = 0  # inputs so far
    for i, seg in enumerate(plan):
        d = f"{seg.duration:.3f}"
        if seg.beat == "end":
            args += ["-f", "lavfi", "-t", d, "-i", f"color=c={BACKGROUND}:s={w}x{h}:r={FPS}"]
            spans = 1
        else:
            clip = clips[seg.beat]
            spans = 0
            for start, end in clip.spans(seg.duration):
                args += ["-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-i", str(clip.path)]
                spans += 1
        parts = []
        for k in range(n, n + spans):
            chains.append(
                f"[{k}:v]scale={w}:{h}:force_original_aspect_ratio=decrease,"
                f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2:color={BACKGROUND},setsar=1,fps={FPS},"
                f"setpts=PTS-STARTPTS[p{k}]"
            )
            parts.append(f"[p{k}]")
        n += spans
        # The parts at real speed, then held on their last frame or cut to the row's length.
        chains.append(
            f"{''.join(parts)}concat=n={len(parts)}:v=1:a=0,"
            f"tpad=stop_mode=clone:stop_duration={d},trim=duration={d},setpts=PTS-STARTPTS[v{i}]"
        )
    joined = "".join(f"[v{i}]" for i in range(len(plan)))
    chains.append(f"{joined}concat=n={len(plan)}:v=1:a=0[cat]")
    chains.append(
        f"[cat]subtitles=filename={filter_path(captions)}:original_size={w}x{h}:"
        f"force_style='{CAPTION_STYLE}'[out]"
    )
    args += [
        "-filter_complex",
        ";".join(chains),
        "-map",
        "[out]",
        "-an",
        "-c:v",
        "libx264",
        "-preset",
        "medium",
        "-crf",
        "20",
        "-pix_fmt",
        "yuv420p",
        "-r",
        str(FPS),
        "-movflags",
        "+faststart",
        str(out),
    ]
    return args


Runner = Callable[[Sequence[str], Optional[Mapping[str, str]], Optional[Path]], int]


def run_tool(
    argv: Sequence[str], env: Optional[Mapping[str, str]] = None, cwd: Optional[Path] = None
) -> int:
    try:
        # nosec B603 - an argument list whose program is ffmpeg, npx or docker, chosen above
        return subprocess.run(  # nosec B603
            list(argv),
            check=False,
            env={**os.environ, **env} if env else None,
            cwd=str(cwd) if cwd else None,
        ).returncode
    except FileNotFoundError:
        return 127


# ------------------------------------------------------------------------------ the commands


@dataclass
class Video:
    run: Runner = run_tool
    env: Mapping[str, str] = field(default_factory=lambda: dict(os.environ))
    out: Any = sys.stdout
    script: Path = SCRIPT
    out_dir: Path = OUT_DIR
    rehearsal_dir: Path = REHEARSAL_DIR
    site_manifest: Path = SITE_MANIFEST
    work: Path = record_demo.WORK
    ffmpeg: Optional[str] = None

    def say(self, message: str) -> None:
        print(message, file=self.out, flush=True)

    @property
    def allow_mock(self) -> bool:
        return self.env.get(ALLOW_MOCK_ENV) == "1"

    def clips_dir(self, source: str, rehearsal: bool) -> Path:
        return (self.rehearsal_dir if rehearsal else self.out_dir) / "clips" / source

    def rows(self) -> List[Row]:
        try:
            return parse_table(self.script.read_text(encoding="utf-8"))
        except OSError as exc:
            raise Refused(f"cannot read {rel(self.script)} ({exc})") from exc

    # ---------- srt ----------

    def write_srt(self, target: Path, pages_only: bool, rehearsal: bool) -> Path:
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(srt(self.rows(), pages_only, rehearsal), encoding="utf-8")
        return target

    # ---------- recorders ----------

    def playwright(self, grep: str, env: Mapping[str, str]) -> None:
        npx = find_tool("npx", None, self.env)
        argv = [npx, "playwright", "test", "--project=launch", SPEC, "-g", grep]
        code = self.run(argv, {**env, "CI": "1"}, WEB)
        if code != 0:
            raise Failed(f"the {grep} recorder failed (exit {code}; Playwright's report is above)")

    def static(self, build: bool) -> int:
        """The static beats from the launch build of the Pages site (the shipped tape)."""
        if build:
            npm = find_tool("npm", None, self.env)
            if self.run([npm, "run", "build:launch"], None, WEB) != 0:
                raise Failed("npm run build:launch failed")
        try:
            manifest = json.loads(self.site_manifest.read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise Refused(
                f"no built site at {rel(self.site_manifest)} ({exc}): build it first "
                "(cd frontend-react && npm run build:launch), or drop --no-build"
            ) from exc
        rehearsal = require_recorded(
            str(manifest.get("kind")), "the built site's tape", self.allow_mock
        )
        target = self.clips_dir("static", rehearsal)
        self.playwright(
            "static beats",
            {
                "VIDEO_BEATS": "static",
                "VIDEO_CLIPS_DIR": str(target),
                ALLOW_MOCK_ENV: "1" if rehearsal else "",
            },
        )
        self.say(f"launch video: the static beats are in {rel(target)}")
        return 0

    def stack(self, attempt: str) -> int:
        """The stack beats on a reviewed attempt's record stack, before ACCEPT removes it."""
        if not record_demo.ATTEMPT_ID.match(attempt):
            raise Refused(f"--attempt {attempt!r} is not an attempt id (like 20261021T093000Z)")
        folder = self.work / attempt
        try:
            doc = json.loads((folder / "attempt.json").read_text(encoding="utf-8"))
            manifest = json.loads((folder / "tape" / "manifest.json").read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise Refused(f"no reviewed attempt {attempt} in {rel(folder)} ({exc})") from exc
        if doc.get("state") != record_demo.REVIEW:
            raise Refused(
                f"attempt {attempt} is {doc.get('state')}: the stack beats need its stack, which is "
                "up while the attempt is under review"
            )
        rehearsal = require_recorded(
            str(manifest.get("kind")), f"attempt {attempt}'s tape", self.allow_mock
        )
        port = doc.get("video_port")
        key_file = folder / "admin.key"
        if not isinstance(port, int) or not key_file.is_file():
            raise Refused(f"attempt {attempt} has no video port or admin key (record it again)")
        override = folder / "video.yml"
        override.write_text(
            "# Written by scripts/launch_video.py (D210): the dashboard for the video's stack\n"
            "# beats, on 127.0.0.1 only, while ACCEPT=1 VIDEO=1 records them.\n"
            "services:\n  dashboard:\n    ports: !override\n"
            f'      - "127.0.0.1:{port}:3000"\n',
            encoding="utf-8",
        )
        recorder = record_demo.Recorder(work=self.work)
        compose = recorder.compose(attempt) + ["-f", str(override)]
        env = recorder.compose_env
        version = recorder.repo_version() or recorder.release_version
        build = compose + ["build", "--build-arg", f"VERSION={version}", "dashboard"]
        if self.run(build, env, ROOT) != 0:
            raise Failed("the record stack's dashboard did not build (docker compose build)")
        up = compose + ["up", "-d", "--wait", "--wait-timeout", "300", "dashboard"]
        if self.run(up, env, ROOT) != 0:
            raise Failed("the record stack's dashboard did not start (docker compose up dashboard)")
        target = self.clips_dir("stack", rehearsal)
        try:
            self.playwright(
                "stack beats",
                {
                    "VIDEO_BEATS": "stack",
                    "VIDEO_CLIPS_DIR": str(target),
                    "VIDEO_STACK_URL": f"http://localhost:{port}",
                    "VIDEO_ADMIN_KEY_FILE": str(key_file),
                    "VIDEO_TAPE_KIND": str(manifest.get("kind")),
                    "VIDEO_TAPE_LABEL": str(manifest.get("label") or ""),
                    "PLAYWRIGHT_STATIC_PREVIEW": "0",
                    ALLOW_MOCK_ENV: "1" if rehearsal else "",
                },
            )
        finally:
            self.run(compose + ["stop", "dashboard"], env, ROOT)
        self.say(f"launch video: the stack beats are in {rel(target)}")
        return 0

    # ---------- assembly ----------

    def assemble(self, pages_only: bool) -> int:
        rows = self.rows()
        plan = segments(rows, pages_only)
        sources = ["static"] if pages_only else ["static", "stack"]
        # A real take uses the recorded clips in tmp/launch/clips/; a rehearsal (VIDEO_ALLOW_MOCK=1
        # and no real clips) uses tmp/launch/rehearsal/clips/ only. The two never mix.
        real = all((self.clips_dir(s, False) / "clips.json").is_file() for s in sources)
        rehearsal = not real and self.allow_mock
        sets = [read_clips(self.clips_dir(s, rehearsal), s) for s in sources]
        for cs in sets:
            if not rehearsal:
                require_recorded(cs.kind, f"the {cs.source} clips' tape", False)
        clips: Dict[str, Clip] = {}
        for cs in sets:
            clips.update(cs.clips)
        missing = [s.beat for s in plan if s.beat != "end" and s.beat not in clips]
        if missing:
            raise Refused("missing clips for " + ", ".join(missing) + " (record them first)")
        ffmpeg = find_tool("ffmpeg", self.ffmpeg, self.env)
        target_dir = self.rehearsal_dir if rehearsal else self.out_dir
        captions = self.write_srt(target_dir / "video.srt", pages_only, rehearsal)
        made: List[Path] = []
        for name, size in SIZES.items():
            suffix = "-pages" if pages_only else ""
            out = target_dir / f"video-{name}{suffix}{'-SIMULATED' if rehearsal else ''}.mp4"
            out.parent.mkdir(parents=True, exist_ok=True)
            code = self.run(ffmpeg_args(ffmpeg, plan, clips, captions, size, out), None, ROOT)
            if code != 0 or not out.is_file() or out.stat().st_size == 0:
                out.unlink(missing_ok=True)
                raise Failed(f"ffmpeg failed for the {name} video (exit {code})")
            made.append(out)
        total = plan[-1].start + plan[-1].duration
        for out in made:
            self.say(
                f"launch video: {rel(out)} ({total:g} s{', a rehearsal: Simulated' if rehearsal else ''})"
            )
        self.say(
            f"launch video: captions in {rel(captions)}; MP4s are release assets, never committed"
        )
        return 0


def main(argv: Optional[Sequence[str]] = None, video: Optional[Video] = None) -> int:
    parser = argparse.ArgumentParser(prog="launch_video.py", description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    p_srt = sub.add_parser("srt", help="write the captions from the demo script's table")
    p_srt.add_argument("--out", type=Path, default=OUT_DIR / "video.srt")
    p_srt.add_argument("--pages-only", action="store_true", help="the H-6 45-second take")
    p_stack = sub.add_parser("stack", help="record the stack beats on a reviewed attempt's stack")
    p_stack.add_argument("--attempt", required=True)
    p_static = sub.add_parser("static", help="record the static beats from the Pages build")
    p_static.add_argument("--no-build", action="store_true", help="use the existing launch build")
    p_asm = sub.add_parser("assemble", help="join the clips with burned-in captions (ffmpeg)")
    p_asm.add_argument("--pages-only", action="store_true", help="the H-6 45-second take")
    p_asm.add_argument("--ffmpeg", help="the ffmpeg binary (else $FFMPEG, else ffmpeg on PATH)")
    args = parser.parse_args(argv)
    v = video or Video()
    try:
        if args.command == "srt":
            path = v.write_srt(args.out, args.pages_only, v.allow_mock)
            v.say(f"launch video: captions in {rel(path)}")
            return 0
        if args.command == "stack":
            return v.stack(args.attempt)
        if args.command == "static":
            return v.static(not args.no_build)
        v.ffmpeg = args.ffmpeg or v.ffmpeg
        return v.assemble(args.pages_only)
    except Refused as exc:
        print(f"launch_video: refused: {exc}", file=sys.stderr)
        return 2
    except Failed as exc:
        print(f"launch_video: failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
