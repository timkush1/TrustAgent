"""Release scratch paths are ignored by git; the paths a published run writes are not.

The paid measurement and recording steps write ledgers, gate-eval output, rehearsal tapes and video files. A stray
`git add -A` must not pick any of them up, while the few files a published run writes must stay
committable. `git check-ignore` decides, so the test proves the real ignore rules.
"""

from __future__ import annotations

import subprocess  # nosec B404 - runs git with fixed arguments
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# Written by rehearsals and paid runs; never committed.
SCRATCH = (
    "tmp/record-demo/ledger.jsonl",
    "tmp/record-demo/attempt-1/manifest.json",
    "tmp/launch/video-4x5.mp4",
    "tmp/launch/video-16x9.mp4",
    "backend-python/evals/calibration/x/gate.jsonl",
    "backend-python/evals/results/x/run.json",
    "backend-python/evals/.cache/ledger.jsonl",
    "backend-python/evals/datasets/ragtruth/bench-lite.jsonl",
)

# Written only by a reviewed, published step; must stay committable.
PUBLISHED = (
    "docs/benchmarks/x.json",
    "contracts/claims/measured.json",
    "config/calibration/x.json",
    "frontend-react/demo-data/recorded/manifest.json",
    "docs/launch/terms-check.md",
)


def ignored(path: str) -> bool:
    proc = subprocess.run(  # nosec B603 B607 - fixed git arguments, no shell
        ["git", "-C", str(ROOT), "check-ignore", "-q", "--no-index", path],
        capture_output=True,
        check=False,
    )
    if proc.returncode not in (0, 1):
        raise AssertionError(f"git check-ignore {path}: {proc.stderr.decode().strip()}")
    return proc.returncode == 0


class GitignoreScratchTests(unittest.TestCase):
    def test_lists_are_not_empty(self) -> None:
        self.assertGreaterEqual(len(SCRATCH), 4)
        self.assertGreaterEqual(len(PUBLISHED), 4)

    def test_scratch_paths_are_ignored(self) -> None:
        for path in SCRATCH:
            with self.subTest(path=path):
                self.assertTrue(ignored(path), f"{path} is not ignored")

    def test_published_paths_are_not_ignored(self) -> None:
        for path in PUBLISHED:
            with self.subTest(path=path):
                self.assertFalse(ignored(path), f"{path} is ignored")


if __name__ == "__main__":
    unittest.main()
