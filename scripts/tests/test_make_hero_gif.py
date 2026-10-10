"""Unit tests for scripts/make_hero_gif.py (D115): the hero GIF and MP4 from the hero recording.

ffmpeg is replaced by a small fake (a Python script written per test) that records every call and
writes outputs with the sizes a test asks for, so the two-pass palette, the size limit and its
retries, the width check and the honest-numbers refusals are all exercised without a real encoder.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Dict, List, Optional
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import make_hero_gif as mhg  # noqa: E402

REPO = Path(__file__).resolve().parents[2]
# The module's own targets, read before any test points them into a temporary tree.
REAL_DEFAULT_GIF = mhg.DEFAULT_GIF
REAL_DEFAULT_MP4 = mhg.DEFAULT_MP4
REAL_SCRATCH_DIR = mhg.SCRATCH_DIR

FAKE_FFMPEG = """#!{python}
import json, os, re, sys
args = sys.argv[1:]
out = args[-1]
log = os.environ.get("FAKE_FFMPEG_LOG")
if log:
    with open(log, "a", encoding="utf-8") as f:
        f.write(json.dumps(args) + "\\n")
fail = os.environ.get("FAKE_FFMPEG_FAIL", "")
if "-vf" in args and "palettegen" in args[args.index("-vf") + 1]:
    if fail == "palette":
        sys.stderr.write("Error: palettegen exploded\\n")
        sys.exit(1)
    colours = re.search(r"max_colors=(\\d+)", args[args.index("-vf") + 1]).group(1)
    with open(out, "wb") as f:
        f.write(b"\\x89PNG\\r\\n\\x1a\\n" + ("colours=" + colours).encode())
elif "-lavfi" in args:
    if fail == "gif":
        sys.stderr.write("Error: paletteuse exploded\\n")
        sys.exit(1)
    palette = args[args.index("-i", args.index("-i") + 1) + 1]
    colours = open(palette, "rb").read().split(b"colours=")[1].decode()
    width = int(os.environ.get("FAKE_GIF_WIDTH") or re.search(r"scale=(\\d+)", args[args.index("-lavfi") + 1]).group(1))
    size = int(os.environ.get("FAKE_GIF_BYTES_" + colours, "4000"))
    header = b"GIF89a" + width.to_bytes(2, "little") + (675).to_bytes(2, "little")
    with open(out, "wb") as f:
        f.write(header + b"\\0" * max(0, size - len(header)))
elif "libx264" in args:
    if fail == "mp4":
        sys.stderr.write("Error: no libx264\\n")
        sys.exit(1)
    with open(out, "wb") as f:
        f.write(b"\\0\\0\\0\\x18ftypisom" + b"\\0" * 500)
else:
    sys.stderr.write("fake ffmpeg: unexpected arguments\\n")
    sys.exit(3)
"""


class HeroGifTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="hero-"))
        self.addCleanup(self._cleanup)
        self.scratch = self.tmp / "frontend-react" / "test-results" / "launch"
        self.hero = self.scratch / "hero"
        self.hero.mkdir(parents=True)
        # The module's paths, pointed into the temporary tree.
        for name, value in {
            "ROOT": self.tmp,
            "SCRATCH_DIR": self.scratch,
            "DEFAULT_META": self.hero / "hero.json",
            "DEFAULT_GIF": self.tmp / "docs" / "images" / "v2" / "hero.gif",
            "DEFAULT_MP4": self.tmp / "tmp" / "launch" / "hero.mp4",
            "SCRATCH_GIF": self.scratch / "hero.gif",
            "SCRATCH_MP4": self.scratch / "hero.mp4",
        }.items():
            patcher = mock.patch.object(mhg, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.ffmpeg = self.tmp / "ffmpeg"
        self.ffmpeg.write_text(FAKE_FFMPEG.replace("{python}", sys.executable), encoding="utf-8")
        self.ffmpeg.chmod(self.ffmpeg.stat().st_mode | stat.S_IXUSR)
        self.log = self.tmp / "ffmpeg.log"
        self.environ: Dict[str, str] = {"FAKE_FFMPEG_LOG": str(self.log)}

    def _cleanup(self) -> None:
        import shutil

        shutil.rmtree(self.tmp, ignore_errors=True)

    # helpers

    def sidecar(self, tape: str = "recorded", **overrides: object) -> Path:
        (self.hero / "hero.webm").write_bytes(b"\x1aE\xdf\xa3 fake webm")
        doc = {
            "schema": mhg.SIDECAR_SCHEMA,
            "kind": tape,
            "label": (
                "Recorded 2026-10-12 · typesafe/jev-1.13 + claude-haiku-4.5"
                if tape == "recorded"
                else "Simulated (mock checks + mock judge): not model output"
            ),
            "traces": 72,
            "video": "hero.webm",
            "width": 1200,
            "height": 675,
            "captured_at": "2026-10-12T10:00:00.000Z",
        }
        doc.update(overrides)
        meta = self.hero / "hero.json"
        meta.write_text(json.dumps(doc), encoding="utf-8")
        return meta

    def run_main(self, args: List[str], env: Optional[Dict[str, str]] = None) -> tuple:
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, self.environ), contextlib.redirect_stdout(
            out
        ), contextlib.redirect_stderr(err):
            code = mhg.main(["--ffmpeg", str(self.ffmpeg), *args], env=env or {})
        return code, out.getvalue(), err.getvalue()

    def calls(self) -> List[List[str]]:
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text(encoding="utf-8").splitlines()]

    def leftovers(self, directory: Path) -> List[str]:
        return sorted(p.name for p in directory.iterdir() if p.name.startswith(".hero-"))

    # the recorded tape

    def test_recorded_tape_makes_the_gif_and_the_mp4_in_two_passes(self) -> None:
        self.sidecar("recorded")
        code, out, err = self.run_main([])
        self.assertEqual(code, 0, err)
        gif = mhg.DEFAULT_GIF.read_bytes()
        self.assertEqual(mhg.gif_size(gif)[0], 1200)
        self.assertLessEqual(len(gif), mhg.MAX_BYTES)
        self.assertEqual(mhg.DEFAULT_MP4.read_bytes()[4:8], b"ftyp")

        palette, gif_pass, mp4_pass = self.calls()
        self.assertIn(
            "fps=12,scale=1200:-1:flags=lanczos,palettegen=max_colors=256:stats_mode=diff", palette
        )
        lavfi = gif_pass[gif_pass.index("-lavfi") + 1]
        self.assertIn("fps=12,scale=1200:-1:flags=lanczos", lavfi)
        self.assertIn("paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle", lavfi)
        self.assertEqual(gif_pass[gif_pass.index("-loop") + 1], "0")
        for flag in ("libx264", "+faststart", "-an"):
            self.assertIn(flag, mp4_pass)
        self.assertIn("scale=1200:-2:flags=lanczos,format=yuv420p", mp4_pass)

        self.assertIn("docs/images/v2/hero.gif", out)
        self.assertIn("tmp/launch/hero.mp4", out)
        self.assertIn("recorded tape: Recorded 2026-10-12", out)
        self.assertEqual(self.leftovers(mhg.DEFAULT_GIF.parent), [])

    def test_the_default_targets_are_the_readme_image_and_the_release_asset(self) -> None:
        gif, mp4 = mhg.plan_outputs("recorded", None, None, allow_mock=False)
        self.assertEqual((gif, mp4), (mhg.DEFAULT_GIF, mhg.DEFAULT_MP4))
        # The real targets: the README's image, and an MP4 for the release that git ignores (tmp/).
        self.assertEqual(REAL_DEFAULT_GIF, REPO / "docs" / "images" / "v2" / "hero.gif")
        self.assertEqual(REAL_DEFAULT_MP4, REPO / "tmp" / "launch" / "hero.mp4")
        lines = (REPO / ".gitignore").read_text(encoding="utf-8").splitlines()
        anchored = {
            line.strip("/") for line in lines if line.startswith("/") and line.endswith("/")
        }
        self.assertIn(
            "tmp", anchored, "the root .gitignore ignores tmp/: the MP4 is never committed"
        )
        # The scratch directory is Playwright's output directory, which git ignores too.
        self.assertEqual(REAL_SCRATCH_DIR, REPO / "frontend-react" / "test-results" / "launch")

    def test_explicit_targets_are_used_for_a_recorded_tape(self) -> None:
        self.sidecar("recorded")
        gif, mp4 = self.tmp / "out" / "a.gif", self.tmp / "out" / "a.mp4"
        code, _, err = self.run_main(["--gif", str(gif), "--mp4", str(mp4)])
        self.assertEqual(code, 0, err)
        self.assertTrue(gif.is_file() and mp4.is_file())
        self.assertFalse(mhg.DEFAULT_GIF.exists())

    # honest numbers: the simulated tape

    def test_a_simulated_tape_is_refused_without_hero_allow_mock(self) -> None:
        self.sidecar("mock")
        code, out, err = self.run_main([])
        self.assertEqual(code, 2)
        self.assertIn("refused", err)
        self.assertIn("not model output", err)
        self.assertIn("HERO_ALLOW_MOCK=1", err)
        self.assertEqual(self.calls(), [], "ffmpeg never runs")
        self.assertFalse(mhg.DEFAULT_GIF.exists())
        self.assertFalse(mhg.SCRATCH_GIF.exists())

    def test_hero_allow_mock_writes_labelled_scratch_copies_only(self) -> None:
        self.sidecar("mock")
        code, out, err = self.run_main([], env={"HERO_ALLOW_MOCK": "1"})
        self.assertEqual(code, 0, err)
        self.assertTrue(mhg.SCRATCH_GIF.is_file())
        self.assertTrue(mhg.SCRATCH_MP4.is_file())
        self.assertFalse(mhg.DEFAULT_GIF.exists(), "never the README's image")
        self.assertFalse(mhg.DEFAULT_MP4.exists())
        self.assertIn("Simulated, scratch only", out)

    def test_a_simulated_tape_never_goes_outside_the_scratch_directory(self) -> None:
        self.sidecar("mock")
        for flag, target in (("--gif", mhg.DEFAULT_GIF), ("--mp4", self.tmp / "docs" / "hero.mp4")):
            with self.subTest(flag=flag):
                code, _, err = self.run_main([flag, str(target)], env={"HERO_ALLOW_MOCK": "1"})
                self.assertEqual(code, 2)
                self.assertIn("goes only to", err)
                self.assertFalse(target.exists())
        self.assertEqual(self.calls(), [])

    def test_hero_allow_mock_must_be_exactly_one(self) -> None:
        self.sidecar("mock")
        code, _, _ = self.run_main([], env={"HERO_ALLOW_MOCK": "true"})
        self.assertEqual(code, 2)

    # the size limit

    def test_a_gif_over_the_limit_is_retried_with_a_smaller_palette(self) -> None:
        self.sidecar("recorded")
        self.environ.update({"FAKE_GIF_BYTES_256": "9000", "FAKE_GIF_BYTES_128": "4000"})
        code, out, err = self.run_main(["--max-bytes", "5000"])
        self.assertEqual(code, 0, err)
        self.assertEqual(len(mhg.DEFAULT_GIF.read_bytes()), 4000)
        palettes = [
            c[c.index("-vf") + 1]
            for c in self.calls()
            if "-vf" in c and "palettegen" in c[c.index("-vf") + 1]
        ]
        self.assertEqual(len(palettes), 2)
        self.assertIn("max_colors=128", palettes[1])
        self.assertIn("128 colours", out)

    def test_a_gif_that_never_fits_fails_and_leaves_the_old_one(self) -> None:
        self.sidecar("recorded")
        mhg.DEFAULT_GIF.parent.mkdir(parents=True)
        mhg.DEFAULT_GIF.write_bytes(b"the previous hero")
        self.environ.update({f"FAKE_GIF_BYTES_{n}": "9000" for n in mhg.PALETTE_COLOURS})
        code, _, err = self.run_main(["--max-bytes", "5000"])
        self.assertEqual(code, 1)
        self.assertIn("over 5,000 bytes at every palette size", err)
        self.assertIn("64 colours: 9,000 bytes", err)
        self.assertEqual(mhg.DEFAULT_GIF.read_bytes(), b"the previous hero")
        self.assertFalse(mhg.DEFAULT_MP4.exists(), "no MP4 without a GIF")
        self.assertEqual(self.leftovers(mhg.DEFAULT_GIF.parent), [])

    def test_a_gif_of_the_wrong_width_fails(self) -> None:
        self.sidecar("recorded")
        self.environ["FAKE_GIF_WIDTH"] = "800"
        code, _, err = self.run_main([])
        self.assertEqual(code, 1)
        self.assertIn("800 px wide, not 1200", err)
        self.assertFalse(mhg.DEFAULT_GIF.exists())

    # ffmpeg

    def test_an_ffmpeg_failure_exits_1_with_its_message(self) -> None:
        self.sidecar("recorded")
        for step, words in (
            ("palette", "palettegen exploded"),
            ("gif", "paletteuse exploded"),
            ("mp4", "no libx264"),
        ):
            with self.subTest(step=step):
                self.environ["FAKE_FFMPEG_FAIL"] = step
                code, _, err = self.run_main([])
                self.assertEqual(code, 1)
                self.assertIn(words, err)

    def test_a_missing_ffmpeg_is_refused_with_how_to_install_it(self) -> None:
        with mock.patch.object(mhg.shutil, "which", return_value=None):
            with self.assertRaises(mhg.Refused) as caught:
                mhg.find_ffmpeg(None, env={})
        self.assertIn("ffmpeg is not installed", str(caught.exception))
        self.assertEqual(mhg.find_ffmpeg(None, env={"FFMPEG": str(self.ffmpeg)}), str(self.ffmpeg))
        with self.assertRaises(mhg.Refused):
            mhg.find_ffmpeg(str(self.tmp / "no-such-ffmpeg"), env={})

    # the recording

    def test_a_missing_or_broken_recording_is_refused(self) -> None:
        code, _, err = self.run_main([])
        self.assertEqual(code, 2)
        self.assertIn("npm run hero", err)

        cases = {
            "an unknown kind": {"kind": "replayed"},
            "another schema": {"schema": "something/v1"},
            "no label": {"label": " "},
            "no video": {"video": ""},
        }
        for name, overrides in cases.items():
            with self.subTest(name):
                self.sidecar("recorded", **overrides)
                code, _, _ = self.run_main([])
                self.assertEqual(code, 2)

        self.sidecar("recorded")
        (self.hero / "hero.webm").unlink()
        code, _, err = self.run_main([])
        self.assertEqual(code, 2)
        self.assertIn("is missing", err)
        self.assertEqual(self.calls(), [])

    def test_options_must_be_positive(self) -> None:
        self.sidecar("recorded")
        for flag in ("--fps", "--width", "--max-bytes"):
            with self.subTest(flag=flag), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as caught:
                    mhg.main([flag, "0"], env={})
                self.assertEqual(caught.exception.code, 2)

    # helpers of the module

    def test_gif_size_reads_the_logical_screen(self) -> None:
        self.assertEqual(
            mhg.gif_size(b"GIF89a" + (1200).to_bytes(2, "little") + (675).to_bytes(2, "little")),
            (1200, 675),
        )
        self.assertEqual(mhg.gif_size(b"GIF87a\x10\x00\x20\x00rest"), (16, 32))
        for data in (b"", b"\x89PNG\r\n\x1a\n0000", b"GIF89a\x01"):
            with self.assertRaises(mhg.Failed):
                mhg.gif_size(data)

    def test_in_scratch_accepts_only_paths_inside_it(self) -> None:
        self.assertTrue(mhg.in_scratch(self.scratch / "hero.gif"))
        self.assertTrue(mhg.in_scratch(self.scratch / "a" / ".." / "hero.gif"))
        self.assertFalse(mhg.in_scratch(self.scratch / ".." / "hero.gif"))
        self.assertFalse(
            mhg.in_scratch(self.tmp / "frontend-react" / "test-results" / "launch-other" / "x.gif")
        )


if __name__ == "__main__":
    unittest.main()
