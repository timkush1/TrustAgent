"""Unit tests for scripts/check_recording.py over copies of the committed demo recording.

Needs the jsonschema package (the check validates against contracts/schema); without it the
tests are skipped, like the check itself refuses to run.
"""

from __future__ import annotations

import contextlib
import io
import json
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Callable, Dict

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import check_recording  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
# The committed simulated tape (D110: the datasets live in frontend-react/demo-data/). It is always
# committed, so a missing tape fails setUp instead of skipping the class.
RECORDING = ROOT / "frontend-react" / "demo-data" / "simulated"
RECORDED_LABEL = (
    "Recorded 28 Sep 2026 · typesafe/jev-1.13 + anthropic/claude-haiku-4.5 · "
    "scripted answers · replayed, not live"
)

try:
    import jsonschema  # noqa: F401

    HAVE_JSONSCHEMA = True
except ImportError:  # pragma: no cover - depends on the environment
    HAVE_JSONSCHEMA = False


@unittest.skipUnless(HAVE_JSONSCHEMA, "jsonschema is not installed (pip install jsonschema)")
class CheckRecordingTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="recording-"))
        # A neutral name: the dataset directories (simulated/, recorded/) add rules of their own,
        # tested in the directory tests below.
        self.dir = self.tmp / "tape"
        shutil.copytree(RECORDING, self.dir)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    # -------------------------------------------------------------------------- helpers

    def manifest(self) -> Dict[str, Any]:
        return json.loads((self.dir / "manifest.json").read_text(encoding="utf-8"))

    def edit_manifest(self, change: Callable[[Dict[str, Any]], None]) -> None:
        doc = self.manifest()
        change(doc)
        (self.dir / "manifest.json").write_text(json.dumps(doc), encoding="utf-8")

    def first_trace_path(self) -> Path:
        return self.dir / "traces" / f"{self.manifest()['traces'][0]['trace_id']}.json"

    def edit_trace(self, change: Callable[[Dict[str, Any]], None]) -> None:
        path = self.first_trace_path()
        doc = json.loads(path.read_text(encoding="utf-8"))
        change(doc)
        path.write_text(json.dumps(doc), encoding="utf-8")

    def problems(self, require_recorded: bool = False) -> list[str]:
        return check_recording.check_dir(self.dir, require_recorded).items

    def assert_problem(self, needle: str, require_recorded: bool = False) -> None:
        found = self.problems(require_recorded)
        self.assertTrue(any(needle in p for p in found), f"no problem mentions {needle!r}: {found}")

    def as_recorded(self, doc: Dict[str, Any]) -> None:
        doc["kind"] = "recorded"
        doc["label"] = RECORDED_LABEL
        doc["decision_model"] = {"backend": "openrouter-systemone", "snapshot": "typesafe/jev-1.13"}
        doc["judge"] = {
            "provider": "anthropic",
            "model": "claude-haiku-4.5",
            "fallback_active": False,
        }
        doc["roi"]["simulated"] = False

    # -------------------------------------------------------------------------- tests

    def test_the_committed_recording_passes(self) -> None:
        self.assertEqual(self.problems(), [])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(check_recording.main([str(self.dir)]), 0)
        self.assertIn("kind mock", out.getvalue())

    def test_the_launch_gate_refuses_a_mock_recording(self) -> None:
        self.assert_problem("kind mock: this is the simulated tape", require_recorded=True)
        self.assert_problem("make record-demo CONFIRM=1", require_recorded=True)
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(check_recording.main([str(self.dir), "--require-recorded"]), 1)
        self.assertIn("accepts only a real recording (kind recorded)", out.getvalue())

    def test_the_launch_gate_explains_a_missing_recording(self) -> None:
        missing = self.tmp / "recorded"
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            self.assertEqual(check_recording.main([str(missing), "--require-recorded"]), 1)
        self.assertIn("no recording in", err.getvalue())
        self.assertIn("make record-demo CONFIRM=1", err.getvalue())
        self.assertIn("frontend-react/demo-data/recorded/", err.getvalue())
        # Without the gate, a missing manifest is a usage error.
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            self.assertEqual(check_recording.main([str(missing)]), 2)
        self.assertIn("manifest.json not found", err.getvalue())

    # -------------------------------------------------------------------------- directory rules

    def as_dataset(self, name: str) -> None:
        """Move the copy to a dataset directory name (D110)."""
        target = self.tmp / name
        self.dir.rename(target)
        self.dir = target

    def test_the_simulated_directory_holds_the_mock_tape(self) -> None:
        self.as_dataset("simulated")
        self.assertEqual(self.problems(), [])
        self.edit_manifest(self.as_recorded)
        self.assert_problem("demo-data/simulated holds the simulated tape (kind mock)")

    def test_the_recorded_directory_holds_only_a_real_recording(self) -> None:
        self.as_dataset("recorded")
        self.assert_problem("demo-data/recorded holds only a real recording (kind recorded)")
        self.edit_manifest(self.as_recorded)
        self.assertEqual(self.problems(), [])
        self.assertEqual(self.problems(require_recorded=True), [])

    def test_the_directory_holds_a_recording_and_nothing_else(self) -> None:
        (self.dir / ".gitkeep").write_text("", encoding="utf-8")
        self.assertEqual(self.problems(), [])
        (self.dir / "notes.txt").write_text("draft", encoding="utf-8")
        (self.dir / "traces" / "README.md").write_text("x", encoding="utf-8")
        (self.dir / "extra").mkdir()
        found = self.problems()
        self.assertTrue(any(p.startswith("notes.txt: is not part of a recording") for p in found))
        self.assertTrue(any(p.startswith("extra: is not part of a recording") for p in found))
        self.assertTrue(any(p.startswith("traces/README.md: is not a trace file") for p in found))
        self.assertTrue(all("published" in p for p in found), found)

    def test_a_mock_recording_keeps_the_simulated_label(self) -> None:
        self.edit_manifest(lambda d: d.update(label="Recorded yesterday · replayed, not live"))
        self.assert_problem("a mock recording must be labelled")

    def test_mock_data_never_claims_to_be_measured(self) -> None:
        def measured(doc: Dict[str, Any]) -> None:
            doc["decisions"][0]["decision_model"]["cost_source"] = "reported"

        self.edit_trace(measured)
        self.assert_problem("mock costs are simulated")

    def test_mock_roi_must_be_simulated(self) -> None:
        self.edit_manifest(lambda d: d["roi"].update(simulated=False))
        self.assert_problem("roi.simulated true")

    def test_a_real_recording_passes(self) -> None:
        self.edit_manifest(self.as_recorded)
        self.assertEqual(self.problems(), [])
        self.assertEqual(self.problems(require_recorded=True), [])

    def test_recorded_needs_a_real_decision_backend(self) -> None:
        def mock_backend(doc: Dict[str, Any]) -> None:
            self.as_recorded(doc)
            doc["decision_model"]["backend"] = "mock"

        self.edit_manifest(mock_backend)
        self.assert_problem("kind recorded needs a real decision backend")

    def test_recorded_refuses_a_fallback_or_mock_judge(self) -> None:
        def fallback(doc: Dict[str, Any]) -> None:
            self.as_recorded(doc)
            doc["judge"]["fallback_active"] = True

        self.edit_manifest(fallback)
        self.assert_problem("a judge that is not on its fallback")

        def mock_judge(doc: Dict[str, Any]) -> None:
            self.as_recorded(doc)
            doc["judge"]["provider"] = "mock"

        self.edit_manifest(mock_judge)
        self.assert_problem("cannot have the mock judge")

    def test_recorded_needs_an_answer_source_on_every_trace(self) -> None:
        def unsourced(doc: Dict[str, Any]) -> None:
            self.as_recorded(doc)
            doc["traces"][0]["answer_source"] = None
            doc["answer_sources"]["scripted"] -= 1

        self.edit_manifest(unsourced)
        self.assert_problem("answer_source on every trace")

    def test_recorded_needs_the_replayed_label(self) -> None:
        def unlabelled(doc: Dict[str, Any]) -> None:
            self.as_recorded(doc)
            doc["label"] = "Live traffic"

        self.edit_manifest(unlabelled)
        self.assert_problem("replayed, not live")

    def test_trace_files_match_the_manifest(self) -> None:
        extra = self.dir / "traces" / "01a0e700-0000-7000-8000-000000000000.json"
        shutil.copy(self.first_trace_path(), extra)
        self.assert_problem("is not listed in the manifest")
        extra.unlink()
        self.first_trace_path().unlink()
        self.assert_problem("is missing")

    def test_totals_add_up(self) -> None:
        self.edit_manifest(lambda d: d["totals"].update(traces=d["totals"]["traces"] + 1))
        self.assert_problem("totals.traces")

    def test_events_are_in_time_order(self) -> None:
        self.edit_manifest(lambda d: d["events"].reverse())
        self.assert_problem("out of time order")

    def test_no_credentials_or_email_addresses(self) -> None:
        def leak(doc: Dict[str, Any]) -> None:
            doc["body"][
                "response_text"
            ] = "Write to jane.doe@acme-mail.io with sk-live1234567890abc"

        self.edit_trace(leak)
        self.assert_problem("an e-mail address")
        self.assert_problem("an sk- API key")

    def test_addresses_at_reserved_example_domains_pass(self) -> None:
        # RFC 2606 and RFC 6761 names are never real mailboxes (pii-leak-flag answers with one).
        def reserved(doc: Dict[str, Any]) -> None:
            doc["body"]["response_text"] = (
                "Write to jordan.miller@example.net, ops@mail.example.com, a@b.example, "
                "c@d.test, e@f.invalid or g@h.localhost."
            )

        self.edit_trace(reserved)
        self.assertEqual([p for p in self.problems() if "e-mail" in p], [])

        # A real domain next to them is still refused.
        def mixed(doc: Dict[str, Any]) -> None:
            doc["body"]["response_text"] += " Or jane@acme-mail.io."

        self.edit_trace(mixed)
        self.assert_problem("an e-mail address")


class ReservedHostTest(unittest.TestCase):
    def test_mirrors_the_go_sanitizer(self) -> None:
        for host in (
            "localhost",
            "example.com",
            "EXAMPLE.NET.",
            "a.example.org",
            "x.example",
            "y.test",
            "z.invalid",
            "w.localhost",
        ):
            self.assertTrue(check_recording.reserved_host(host), host)
        for host in (
            "example.co",
            "notexample.com",
            "example.com.evil.io",
            "acme.io",
            "testing.dev",
        ):
            self.assertFalse(check_recording.reserved_host(host), host)


if __name__ == "__main__":
    unittest.main()
