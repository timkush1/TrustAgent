"""Unit tests for scripts/launch_video.py (D210): the captions from the demo script's table, the
refusal of a tape that is not recorded, and the ffmpeg arguments for both sizes, run through a
canned ffmpeg. Nothing records, nothing is encoded, nothing reaches the network."""

from __future__ import annotations

import io
import json
import os
import shutil
import stat
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import launch_video  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

TABLE = textwrap.dedent(
    """\
    # A script

    | Time | Screen | What the take shows | Caption (burned in) |
    |---|---|---|---|
    | 0–5 s | Landing | Wait | "One." |
    | 5–15 s | Traffic | Wait | "Two." |
    | 15–27 s | Trace | Point | "Three." |
    | 27–40 s | Studio | Type | "Four `code`." |
    | 40–50 s | Policies | Show | "Five." |
    | 50–60 s | Savings | Hold | "Six. `make demo` runs it." |

    ## After
    """
)

# A canned ffmpeg: records its argument list and writes the output file it was given.
CANNED_FFMPEG = """#!/usr/bin/env python3
import json, os, sys
log = os.environ["CANNED_FFMPEG_LOG"]
with open(log, "a", encoding="utf-8") as f:
    f.write(json.dumps(sys.argv[1:]) + "\\n")
with open(sys.argv[-1], "wb") as f:
    f.write(b"mp4")
"""


def cue_times(srt: str) -> List[str]:
    return [line for line in srt.splitlines() if " --> " in line]


class CaptionTest(unittest.TestCase):
    def test_the_srt_timings_come_from_the_table(self) -> None:
        rows = launch_video.parse_table(TABLE)
        self.assertEqual(
            [(r.start, r.end) for r in rows],
            [(0, 5), (5, 15), (15, 27), (27, 40), (40, 50), (50, 60)],
        )
        self.assertEqual(rows[3].caption, "Four code.")
        srt = launch_video.srt(rows)
        self.assertEqual(
            cue_times(srt),
            [
                "00:00:00,000 --> 00:00:05,000",
                "00:00:05,000 --> 00:00:15,000",
                "00:00:15,000 --> 00:00:27,000",
                "00:00:27,000 --> 00:00:40,000",
                "00:00:40,000 --> 00:00:50,000",
                "00:00:50,000 --> 00:01:00,000",
                # The repository's address on the end card, the last 5 s.
                "00:00:55,000 --> 00:01:00,000",
            ],
        )
        self.assertIn("Six. make demo runs it.", srt)
        self.assertIn("{\\an5}github.com/timkush1/TrustAgent", srt)
        self.assertNotIn("Simulated", srt)
        self.assertTrue(srt.startswith("1\n00:00:00,000"))

    def test_the_committed_demo_script_parses(self) -> None:
        rows = launch_video.parse_table(
            (ROOT / "docs" / "launch" / "demo-script.md").read_text("utf-8")
        )
        self.assertEqual(len(rows), 6)
        self.assertEqual(rows[-1].end, 60)
        self.assertEqual(rows[0].caption, "Catch hallucinations before your users do.")
        # The featured trace may have been flagged by an instant check, or have drifted (D209):
        # the caption credits TrustAgent, never one tier by name.
        self.assertTrue(rows[2].caption.startswith("TrustAgent flags it"), rows[2].caption)
        srt = launch_video.srt(rows)
        self.assertIn("make demo runs it with zero keys", srt)
        self.assertNotIn("`", srt)
        for block in srt.split("\n\n"):
            lines = block.strip().splitlines()[2:]
            self.assertLessEqual(len(lines), 2, block)

    def test_the_pages_only_take_is_45_seconds(self) -> None:
        rows = launch_video.parse_table(TABLE)
        plan = launch_video.segments(rows, pages_only=True)
        self.assertEqual(
            [s.beat for s in plan], ["landing", "traffic", "trace", "savings_static", "end"]
        )
        self.assertEqual(plan[-1].start + plan[-1].duration, 45)
        times = cue_times(launch_video.srt(rows, pages_only=True))
        self.assertIn("00:00:15,000 --> 00:00:30,000", times)  # row 3 over its longer trace beat
        self.assertIn("00:00:30,000 --> 00:00:45,000", times)  # row 6 over Savings and the card

    def test_a_rehearsal_says_simulated_over_the_whole_take(self) -> None:
        srt = launch_video.srt(launch_video.parse_table(TABLE), rehearsal=True)
        self.assertIn("00:00:00,000 --> 00:01:00,000\n{\\an8}" + launch_video.REHEARSAL_STAMP, srt)

    def test_a_table_that_does_not_add_up_is_refused(self) -> None:
        for change in (
            lambda t: t.replace("| 5–15 s |", "| 6–15 s |"),
            lambda t: t.replace('| "Two." |', "| |"),
            lambda t: t.replace("| 50–60 s |", "| 50–54 s |"),
            lambda t: t.replace('| 40–50 s | Policies | Show | "Five." |\n', ""),
            lambda t: t.replace("| 0–5 s |", "| zero |"),
        ):
            with self.assertRaises(launch_video.Refused):
                launch_video.segments(launch_video.parse_table(change(TABLE)))
        with self.assertRaises(launch_video.Refused):
            launch_video.parse_table("# no table\n")

    def test_long_captions_wrap_into_two_balanced_lines(self) -> None:
        text = "A fast check flags it in a fraction of a second. The deep audit explains why."
        lines = launch_video.wrap(text).split("\n")
        self.assertEqual(len(lines), 2)
        self.assertLess(abs(len(lines[0]) - len(lines[1])), 10)
        self.assertEqual(launch_video.wrap("Short."), "Short.")


class FakeRun:
    """Stands in for npx, npm and docker; ffmpeg runs for real (the canned one)."""

    def __init__(self) -> None:
        self.calls: List[List[str]] = []
        self.envs: List[Optional[Mapping[str, str]]] = []
        self.code = 0

    def __call__(
        self, argv: Sequence[str], env: Optional[Mapping[str, str]], cwd: Optional[Path]
    ) -> int:
        argv = list(argv)
        if Path(argv[0]).name == "ffmpeg":
            return launch_video.run_tool(argv, env, cwd)
        self.calls.append(argv)
        self.envs.append(env)
        return self.code


class VideoTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="launch-video-"))
        self.script = self.tmp / "demo-script.md"
        self.script.write_text(TABLE, encoding="utf-8")
        self.out_dir = self.tmp / "launch"
        self.rehearsal = self.out_dir / "rehearsal"
        self.work = self.tmp / "record-demo"
        self.site = self.tmp / "dist" / "demo" / "manifest.json"
        self.site.parent.mkdir(parents=True)
        self.ffmpeg = self.tmp / "bin" / "ffmpeg"
        self.ffmpeg.parent.mkdir()
        self.ffmpeg.write_text(CANNED_FFMPEG, encoding="utf-8")
        self.ffmpeg.chmod(self.ffmpeg.stat().st_mode | stat.S_IXUSR)
        self.log = self.tmp / "ffmpeg.log"
        self.run = FakeRun()
        self.out = io.StringIO()

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def video(self, env: Optional[Dict[str, str]] = None) -> launch_video.Video:
        base = {
            "CANNED_FFMPEG_LOG": str(self.log),
            "PATH": os.environ.get("PATH", ""),
            "NPX": "npx",
            "NPM": "npm",
        }
        return launch_video.Video(
            run=self.run,
            env={**base, **(env or {})},
            out=self.out,
            script=self.script,
            out_dir=self.out_dir,
            rehearsal_dir=self.rehearsal,
            site_manifest=self.site,
            work=self.work,
            ffmpeg=str(self.ffmpeg),
        )

    def main(self, argv: Sequence[str], env: Optional[Dict[str, str]] = None) -> int:
        os.environ["CANNED_FFMPEG_LOG"] = str(self.log)
        return launch_video.main(list(argv), self.video(env))

    def write_clips(
        self, directory: Path, source: str, kind: str, beats: Sequence[str], parts: bool = False
    ) -> None:
        directory.mkdir(parents=True, exist_ok=True)
        clips: List[Dict[str, Any]] = []
        for i, beat in enumerate(beats):
            (directory / f"{beat}.webm").write_bytes(b"webm")
            clip: Dict[str, Any] = {"beat": beat, "file": f"{beat}.webm", "lead_ms": 1000 + i}
            if parts and beat == "studio":
                clip["parts"] = [[2000, 6000], [9000, 12500]]
            clips.append(clip)
        doc = {
            "schema": launch_video.CLIPS_SCHEMA,
            "source": source,
            "kind": kind,
            "label": "x",
            "clips": clips,
        }
        (directory / "clips.json").write_text(json.dumps(doc), encoding="utf-8")

    def ffmpeg_runs(self) -> List[List[str]]:
        if not self.log.is_file():
            return []
        return [json.loads(line) for line in self.log.read_text("utf-8").splitlines()]

    # -------------------------------------------------------------------------- assembly

    def test_ffmpeg_arguments_for_both_sizes(self) -> None:
        self.write_clips(
            self.out_dir / "clips" / "static", "static", "recorded", launch_video.STATIC_BEATS
        )
        self.write_clips(
            self.out_dir / "clips" / "stack",
            "stack",
            "recorded",
            launch_video.STACK_BEATS,
            parts=True,
        )
        self.assertEqual(self.main(["assemble"]), 0, self.out.getvalue())
        runs = self.ffmpeg_runs()
        self.assertEqual(len(runs), 2)
        outputs = [Path(r[-1]) for r in runs]
        self.assertEqual([p.name for p in outputs], ["video-16x9.mp4", "video-4x5.mp4"])
        self.assertTrue(all(p.parent == self.out_dir and p.is_file() for p in outputs))
        for run, (w, h) in zip(runs, launch_video.SIZES.values()):
            graph = run[run.index("-filter_complex") + 1]
            self.assertIn(f"scale={w}:{h}:force_original_aspect_ratio=decrease", graph)
            self.assertIn(f"pad={w}:{h}:(ow-iw)/2:(oh-ih)/2", graph)
            self.assertIn(f"color=c={launch_video.BACKGROUND}:s={w}x{h}:r=30", run)
            self.assertIn(
                f"subtitles=filename={self.out_dir / 'video.srt'}:original_size={w}x{h}", graph
            )
            # Seven segments (six rows, the last split into Savings and the end card).
            self.assertIn("concat=n=7:v=1:a=0[cat]", graph)
            for flag, value in (
                ("-c:v", "libx264"),
                ("-pix_fmt", "yuv420p"),
                ("-movflags", "+faststart"),
                ("-r", "30"),
            ):
                self.assertEqual(run[run.index(flag) + 1], value)
            self.assertIn("-an", run)
            # Each clip starts at its lead; the Studio's two parts are two inputs joined at real speed.
            inputs = [run[i + 1] for i, a in enumerate(run) if a == "-i"]
            self.assertEqual(sum(1 for x in inputs if x.endswith("studio.webm")), 2)
            starts = [run[i + 1] for i, a in enumerate(run) if a == "-ss"]
            self.assertIn("2.000", starts)
            self.assertIn("9.000", starts)
            self.assertIn("1.000", starts)  # the landing's lead
            self.assertIn("tpad=stop_mode=clone:stop_duration=13.000,trim=duration=13.000", graph)
            self.assertNotIn("setpts=PTS/", graph)  # nothing is sped up
        srt = (self.out_dir / "video.srt").read_text(encoding="utf-8")
        self.assertNotIn("Simulated", srt)
        self.assertIn("never committed", self.out.getvalue())

    def test_the_pages_only_take_needs_only_the_static_clips(self) -> None:
        self.write_clips(
            self.out_dir / "clips" / "static", "static", "recorded", launch_video.STATIC_BEATS
        )
        self.assertEqual(self.main(["assemble"]), 2)  # the full take needs the stack clips
        self.assertEqual(self.main(["assemble", "--pages-only"]), 0)
        runs = self.ffmpeg_runs()
        self.assertEqual(
            [Path(r[-1]).name for r in runs], ["video-16x9-pages.mp4", "video-4x5-pages.mp4"]
        )
        self.assertIn("concat=n=5:v=1:a=0[cat]", runs[0][runs[0].index("-filter_complex") + 1])

    def test_assembly_refuses_clips_that_are_not_recorded(self) -> None:
        self.write_clips(
            self.out_dir / "clips" / "static", "static", "mock", launch_video.STATIC_BEATS
        )
        self.write_clips(
            self.out_dir / "clips" / "stack", "stack", "recorded", launch_video.STACK_BEATS
        )
        self.assertEqual(self.main(["assemble"]), 2)
        self.assertEqual(self.ffmpeg_runs(), [])

    def test_a_rehearsal_stays_in_the_rehearsal_directory(self) -> None:
        self.write_clips(
            self.rehearsal / "clips" / "static", "static", "mock", launch_video.STATIC_BEATS
        )
        self.write_clips(
            self.rehearsal / "clips" / "stack", "stack", "mock", launch_video.STACK_BEATS
        )
        self.assertEqual(self.main(["assemble"]), 2)  # no VIDEO_ALLOW_MOCK: no real clips to use
        self.assertEqual(self.main(["assemble"], {launch_video.ALLOW_MOCK_ENV: "1"}), 0)
        runs = self.ffmpeg_runs()
        outs = [Path(r[-1]) for r in runs]
        self.assertTrue(
            all(p.parent == self.rehearsal and p.name.endswith("-SIMULATED.mp4") for p in outs),
            outs,
        )
        self.assertFalse((self.out_dir / "video-16x9.mp4").exists())
        self.assertIn(
            launch_video.REHEARSAL_STAMP, (self.rehearsal / "video.srt").read_text("utf-8")
        )

    def test_a_failed_ffmpeg_leaves_no_video(self) -> None:
        self.ffmpeg.write_text("#!/bin/sh\nexit 3\n", encoding="utf-8")
        self.write_clips(
            self.out_dir / "clips" / "static", "static", "recorded", launch_video.STATIC_BEATS
        )
        self.assertEqual(self.main(["assemble", "--pages-only"]), 1)
        self.assertFalse((self.out_dir / "video-16x9-pages.mp4").exists())

    def test_a_clip_outside_its_directory_or_missing_is_refused(self) -> None:
        d = self.out_dir / "clips" / "static"
        self.write_clips(d, "static", "recorded", launch_video.STATIC_BEATS)
        doc = json.loads((d / "clips.json").read_text("utf-8"))
        doc["clips"][0]["file"] = "../../../../etc/passwd"
        (d / "clips.json").write_text(json.dumps(doc), encoding="utf-8")
        self.assertEqual(self.main(["assemble", "--pages-only"]), 2)

    # -------------------------------------------------------------------------- recorders

    def test_static_refuses_a_site_that_ships_the_simulated_tape(self) -> None:
        self.site.write_text(json.dumps({"kind": "mock"}), encoding="utf-8")
        self.assertEqual(self.main(["static", "--no-build"]), 2)
        self.assertEqual(self.run.calls, [])
        # A rehearsal records into the rehearsal directory, with the flag the spec reads.
        self.assertEqual(self.main(["static", "--no-build"], {launch_video.ALLOW_MOCK_ENV: "1"}), 0)
        argv, env = self.run.calls[-1], self.run.envs[-1] or {}
        self.assertEqual(
            argv[1:6], ["playwright", "test", "--project=launch", launch_video.SPEC, "-g"]
        )
        self.assertEqual(argv[-1], "static beats")
        self.assertEqual(env["VIDEO_CLIPS_DIR"], str(self.rehearsal / "clips" / "static"))
        self.assertEqual(env[launch_video.ALLOW_MOCK_ENV], "1")
        self.assertEqual(env["CI"], "1")

    def test_static_records_a_recorded_site(self) -> None:
        self.site.write_text(json.dumps({"kind": "recorded"}), encoding="utf-8")
        self.assertEqual(self.main(["static"]), 0)
        self.assertEqual(self.run.calls[0][1:], ["run", "build:launch"])
        env = self.run.envs[-1] or {}
        self.assertEqual(env["VIDEO_CLIPS_DIR"], str(self.out_dir / "clips" / "static"))
        self.assertEqual(env[launch_video.ALLOW_MOCK_ENV], "")

    def attempt(self, state: str = "review", kind: str = "recorded") -> str:
        attempt = "20261021T093000Z"
        folder = self.work / attempt
        (folder / "tape").mkdir(parents=True)
        (folder / "attempt.json").write_text(
            json.dumps({"state": state, "video_port": 43210}), "utf-8"
        )
        (folder / "tape" / "manifest.json").write_text(
            json.dumps({"kind": kind, "label": "Recorded x"}), "utf-8"
        )
        (folder / "admin.key").write_text("ab" * 24 + "\n", encoding="utf-8")
        (folder / "stack.yml").write_text("services: {}\n", encoding="utf-8")
        return attempt

    def test_stack_records_on_the_attempts_dashboard_port(self) -> None:
        attempt = self.attempt()
        self.assertEqual(self.main(["stack", "--attempt", attempt]), 0, self.out.getvalue())
        folder = self.work / attempt
        override = (folder / "video.yml").read_text(encoding="utf-8")
        self.assertIn('- "127.0.0.1:43210:3000"', override)
        build, up, playwright, stop = self.run.calls
        version = launch_video.record_demo.Recorder().repo_version()
        self.assertEqual(build[-4:], ["build", "--build-arg", f"VERSION={version}", "dashboard"])
        self.assertIn(str(folder / "stack.yml"), up)
        self.assertIn(str(folder / "video.yml"), up)
        self.assertEqual(up[-6:], ["up", "-d", "--wait", "--wait-timeout", "300", "dashboard"])
        self.assertEqual(self.run.envs[1], {"TRUSTAGENT_VERSION": "p6rec"})
        env = self.run.envs[2] or {}
        self.assertEqual(playwright[-1], "stack beats")
        self.assertEqual(env["VIDEO_STACK_URL"], "http://localhost:43210")
        self.assertEqual(env["VIDEO_ADMIN_KEY_FILE"], str(folder / "admin.key"))
        self.assertEqual(env["VIDEO_TAPE_KIND"], "recorded")
        self.assertEqual(env["PLAYWRIGHT_STATIC_PREVIEW"], "0")
        self.assertEqual(stop[-2:], ["stop", "dashboard"])
        self.assertNotIn("ab" * 24, self.out.getvalue())

    def test_stack_refuses_an_attempt_that_is_not_recorded_or_not_under_review(self) -> None:
        attempt = self.attempt(kind="mock")
        self.assertEqual(self.main(["stack", "--attempt", attempt]), 2)
        shutil.rmtree(self.work)
        attempt = self.attempt(state="accepted")
        self.assertEqual(self.main(["stack", "--attempt", attempt]), 2)
        self.assertEqual(self.main(["stack", "--attempt", "latest"]), 2)
        self.assertEqual(self.run.calls, [])

    def test_a_failed_recorder_still_stops_the_dashboard(self) -> None:
        attempt = self.attempt()
        self.run.code = 0

        def fail_playwright(
            argv: Sequence[str], env: Optional[Mapping[str, str]], cwd: Optional[Path]
        ) -> int:
            self.run.calls.append(list(argv))
            return 1 if "playwright" in argv else 0

        v = self.video()
        v.run = fail_playwright
        self.assertEqual(launch_video.main(["stack", "--attempt", attempt], v), 1)
        self.assertEqual(self.run.calls[-1][-2:], ["stop", "dashboard"])


class LaunchTargetsTest(unittest.TestCase):
    def test_the_make_targets_run_the_script(self) -> None:
        mk = (ROOT / "mk" / "launch.mk").read_text(encoding="utf-8")
        video = mk.split("\nlaunch-video:", 1)[1].split("\n\n", 1)[0]
        self.assertIn('scripts/launch_video.py" static', video)
        self.assertIn(
            'scripts/launch_video.py" assemble $(if $(filter 1,$(PAGES_ONLY)),--pages-only)', video
        )
        stack = mk.split("\nlaunch-video-stack:", 1)[1].split("\n\n", 1)[0]
        self.assertIn('scripts/launch_video.py" stack --attempt "$(ATTEMPT)"', stack)
        self.assertNotIn("not implemented", video + stack)

    def test_the_outputs_are_ignored(self) -> None:
        gitignore = (ROOT / ".gitignore").read_text(encoding="utf-8")
        self.assertRegex(gitignore, r"(?m)^/tmp/$")
        self.assertEqual(launch_video.OUT_DIR, ROOT / "tmp" / "launch")


if __name__ == "__main__":
    unittest.main()
