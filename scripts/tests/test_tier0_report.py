"""Unit tests for scripts/tier0_report.py (D120, D167): parsing a `go test -bench` run, the newest
run, the GATEWAY.md block and --check."""

from __future__ import annotations

import io
import shutil
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import tier0_report as tr  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]

HEADER = """commit: {commit}
date: {date}
go: go1.25.3
goos: linux
goarch: amd64
pkg: github.com/truthtable/backend-go/internal/gate
cpu: 12th Gen Intel(R) Core(TM) i5-12450H
"""

BENCH = """BenchmarkDetect/evidence=8k/answer=1k-10         \t    1872\t    {a} ns/op\t   6.23 MB/s\t  381913 B/op\t    2721 allocs/op
BenchmarkDetect/evidence=32k/answer=1k-10        \t     600\t   1154928 ns/op\t   2.42 MB/s\t 1374558 B/op\t   10409 allocs/op
BenchmarkDetect/evidence=1MiB/answer=1k-10       \t     100\t  11932893 ns/op\t   0.34 MB/s\t10785195 B/op\t   83661 allocs/op
BenchmarkDetect/dense-10                         \t     150\t   7861122 ns/op\t   0.49 MB/s\t 2306460 B/op\t   15326 allocs/op
BenchmarkGatePreJev-10                           \t     900\t   1312345 ns/op\t  512000 B/op\t    4000 allocs/op
"""


def write_run(directory: Path, commit: str, date: str, counts=(652555, 650000, 640000, 660000, 655000)) -> Path:
    text = HEADER.format(commit=commit, date=date)
    text += "".join(BENCH.format(a=a) for a in counts)
    text += "PASS\nok  \tgithub.com/truthtable/backend-go/internal/gate\t40.1s\n"
    path = directory / f"{commit}.txt"
    path.write_text(text, encoding="utf-8")
    return path


class Tier0ReportTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(tempfile.mkdtemp(prefix="tier0-"))
        self.addCleanup(shutil.rmtree, self.root)
        self.runs = self.root / "docs" / "benchmarks" / "tier0"
        self.runs.mkdir(parents=True)
        self.doc = self.root / "GATEWAY.md"
        self.doc.write_text(
            f"# Gateway\n\nBefore.\n\n{tr.BLOCK_START}\nold\n{tr.BLOCK_END}\n\nAfter.\n", encoding="utf-8"
        )

    def main(self, *args: str) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        code = tr.main(
            ["--gateway-md", "--runs-dir", str(self.runs), "--doc", str(self.doc), "--root", str(self.root), *args],
            out=out,
            err=err,
        )
        return code, out.getvalue(), err.getvalue()

    def test_parses_the_median_of_the_counts(self) -> None:
        run = tr.parse_run(write_run(self.runs, "abc1234", "2026-10-10T14:00:00Z"))
        self.assertEqual(run.config["cpu"], "12th Gen Intel(R) Core(TM) i5-12450H")
        self.assertEqual(len(run.ns_per_op["BenchmarkDetect/evidence=8k/answer=1k"]), 5)
        self.assertAlmostEqual(run.median_ms("BenchmarkDetect/evidence=8k/answer=1k"), 0.652555)
        self.assertAlmostEqual(run.median_ms("BenchmarkGatePreJev"), 1.312345)

    def test_writes_the_block_from_the_newest_run(self) -> None:
        write_run(self.runs, "0ld0000", "2026-10-01T09:00:00Z", counts=(9000000,) * 5)
        write_run(self.runs, "abc1234", "2026-10-10T14:00:00Z")
        code, _, err = self.main()
        self.assertEqual(code, 0, err)
        text = self.doc.read_text(encoding="utf-8")
        block = text[text.index(tr.BLOCK_START) : text.index(tr.BLOCK_END)]
        self.assertIn("from docs/benchmarks/tier0/abc1234.txt; do not edit", block)
        self.assertIn(
            "**Measured** (Go benchmark, 12th Gen Intel(R) Core(TM) i5-12450H, linux/amd64, go1.25.3) at `abc1234` on 2026-10-10",
            block,
        )
        self.assertIn("| Instant checks | 8k tokens | 1k tokens | 0.65 ms | yes |", block)
        self.assertIn("| Instant checks | 32k tokens | 1k tokens | 1.2 ms | yes |", block)
        self.assertIn("| Instant checks | 1 MiB | 1k tokens | 11.9 ms | reported only |", block)
        self.assertNotIn("9.0 ms", block)
        self.assertTrue(text.startswith("# Gateway\n\nBefore.") and text.endswith("\n\nAfter.\n"))
        code, out, _ = self.main("--check")
        self.assertEqual(code, 0)
        self.assertIn("up to date", out)

    def test_check_fails_on_a_stale_block(self) -> None:
        write_run(self.runs, "abc1234", "2026-10-10T14:00:00Z")
        code, _, err = self.main("--check")
        self.assertEqual(code, 1)
        self.assertIn("out of date", err)
        self.assertIn("old", self.doc.read_text(encoding="utf-8"))

    def test_placeholder_without_a_run(self) -> None:
        code, _, _ = self.main()
        self.assertEqual(code, 0)
        self.assertIn("No Tier-0 benchmark figures are published yet", self.doc.read_text(encoding="utf-8"))

    def test_refuses_a_run_without_its_commit_or_a_row(self) -> None:
        path = write_run(self.runs, "abc1234", "2026-10-10T14:00:00Z")
        path.write_text(path.read_text(encoding="utf-8").replace("commit: abc1234\n", ""), encoding="utf-8")
        code, _, err = self.main()
        self.assertEqual(code, 2)
        self.assertIn("no `commit:` line", err)
        path = write_run(self.runs, "abc1234", "2026-10-10T14:00:00Z")
        path.write_text(
            "\n".join(line for line in path.read_text(encoding="utf-8").splitlines() if "dense" not in line),
            encoding="utf-8",
        )
        code, _, err = self.main()
        self.assertEqual(code, 2)
        self.assertIn("BenchmarkDetect/dense", err)

    def test_refuses_a_document_without_markers(self) -> None:
        self.doc.write_text("# Gateway\n", encoding="utf-8")
        code, _, err = self.main()
        self.assertEqual(code, 2)
        self.assertIn("markers", err)

    def test_the_repository_block_is_current(self) -> None:
        self.assertEqual(tr.main(["--gateway-md", "--check"], out=io.StringIO(), err=io.StringIO()), 0)


if __name__ == "__main__":
    unittest.main()
