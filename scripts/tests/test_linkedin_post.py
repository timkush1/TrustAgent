"""Unit tests for scripts/linkedin_post.py and scripts/clip.py (D213, S-9).

Every test runs on a temporary copy of the post template and the claims contract with canned
measured values and a canned recorded tape; nothing reads the repository's measured.json or tape,
and the clipboard is a fake clip.exe that records the bytes it receives.
"""

from __future__ import annotations

import copy
import io
import json
import shutil
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, Optional, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import clip  # noqa: E402
import linkedin_post as lp  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
TRACE_ID = "01a0e73e-1649-7a0c-8ad3-2df6e8a4fa5f"
REFUND_CLAIM = "you can return it for a full refund within 60 days"
REFUND_POLICY = "Unopened items can be returned within 30 days of delivery."

MEASURED: Dict[str, Any] = {
    "schema": "trustagent.measured/v1",
    "status": "measured",
    "tier": "lite",
    "mock": False,
    "generated_by": "bench-publish",
    "generated_at": "2026-10-24T18:02:11Z",
    "runs": ["20261024T171500Z-abc1234"],
    "snapshot": "typesafe/jev-20260901",
    "values": {
        "fast_check_usd_per_1k": {"value": 0.0507, "unit": "usd_per_1k"},
        "same_checks_cost_ratio": {"value": 26.84, "unit": "multiplier"},
        "gate_latency_ms_p50": {"value": 241, "unit": "ms"},
        "gate_latency_ms_p95": {"value": 512, "unit": "ms"},
        "ragtruth_qa_agreement": {"value": 0.781, "unit": "fraction"},
        "ragtruth_qa_accuracy": {"value": 0.804, "unit": "fraction"},
    },
    "intervals": {
        "ragtruth_qa_accuracy": {
            "lo": 0.752,
            "hi": 0.848,
            "method": "wilson",
            "n_pos": 120,
            "n_neg": 180,
        },
        "ragtruth_qa_agreement": {
            "lo": 0.733,
            "hi": 0.826,
            "method": "bootstrap",
            "resamples": 2000,
            "seed": 13,
            "n_pos": 120,
            "n_neg": 180,
        },
    },
}

TRACE: Dict[str, Any] = {
    "trace_id": TRACE_ID,
    "audit_detail": {
        "claims": [
            {
                "index": 0,
                "claim": "The order ships today.",
                "status": "SUPPORTED",
                "span": {"start": 0, "end": 10},
                "quote": "It ships today",
                "evidence_spans": [{"text": "Orders ship the same day."}],
            },
            {
                "index": 1,
                "claim": "It can be refunded within 60 days.",
                "status": "CONTRADICTED",
                "quote": "the refund",
                "evidence": ["ignored without a span"],
            },
            {
                "index": 2,
                "claim": "The customer can get a full refund within 60 days.",
                "status": "CONTRADICTED",
                "span": {"start": 20, "end": 70},
                "quote": REFUND_CLAIM,
                "evidence": ["A fallback quote."],
                "evidence_spans": [{"doc_id": "returns-policy.md", "text": REFUND_POLICY}],
            },
        ]
    },
}


def make_root(
    measured: Optional[Dict[str, Any]] = None,
    manifest: Optional[Dict[str, Any]] = None,
    trace: Optional[Dict[str, Any]] = None,
    tape: bool = True,
) -> Path:
    root = Path(tempfile.mkdtemp(prefix="linkedin-post-"))
    for rel in (lp.TEMPLATE_PATH, lp.CLAIMS_PATH):
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(ROOT / rel, root / rel)
    (root / lp.MEASURED_PATH).write_text(json.dumps(measured or MEASURED), encoding="utf-8")
    if tape:
        rec = root / lp.RECORDED_DIR
        (rec / "traces").mkdir(parents=True)
        man = (
            manifest
            if manifest is not None
            else {
                "kind": "recorded",
                "featured": {"trace_id": TRACE_ID, "rule": "contradicted_with_span"},
            }
        )
        (rec / "manifest.json").write_text(json.dumps(man), encoding="utf-8")
        if trace is not False:  # type: ignore[comparison-overlap]
            (rec / "traces" / f"{TRACE_ID}.json").write_text(
                json.dumps(trace if trace is not None else TRACE), encoding="utf-8"
            )
    return root


def run(root: Path, *argv: str, which: Any = None) -> Tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    code = lp.main(
        list(argv),
        root=root,
        stdout=out,
        stderr=err,
        which=which if which is not None else (lambda _name: None),
    )
    return code, out.getvalue(), err.getvalue()


def fake_clip(directory: Path) -> Tuple[Path, Path]:
    """A clip.exe that writes the bytes it reads to a capture file."""
    capture = directory / "captured.bin"
    exe = directory / "clip.exe"
    exe.write_text(f"#!/bin/sh\ncat > '{capture}'\n", encoding="utf-8")
    exe.chmod(exe.stat().st_mode | stat.S_IXUSR)
    return exe, capture


class PostTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root()
        self.addCleanup(shutil.rmtree, self.root, True)

    def test_the_post_is_filled_from_the_run_and_the_tape(self) -> None:
        code, out, err = run(self.root)
        self.assertEqual(code, 0, err)
        first = out.split("\n\n", 1)[0]
        self.assertEqual(
            first,
            f"My support bot promised “{REFUND_CLAIM}”. The policy said “{REFUND_POLICY[:-1]}”. "
            "TrustAgent caught it in 241 ms for $0.051/1,000 answers, 26.84× cheaper than an LLM "
            "judge doing the same checks [measured].",
        )
        self.assertIn("measured on 2026-10-24 with typesafe/jev-20260901", out)
        self.assertNotRegex(out, r"\{[a-z][a-z0-9_.:]*\}", "every placeholder is filled")

    def test_the_post_is_plain_text(self) -> None:
        _, out, _ = run(self.root)
        for line in out.splitlines():
            self.assertFalse(line.startswith(("> ", "#  ", "```", "|", "- ")), line)
        self.assertNotIn("**", out)
        self.assertNotRegex(out, r"\]\(", "no Markdown links")
        self.assertIn("live mode uses your own OpenRouter key", out)
        self.assertNotIn("no sign-up", out.lower())

    def test_the_agreement_stands_beside_the_like_for_like_figure(self) -> None:
        _, out, _ = run(self.root)
        para = [p for p in out.split("\n\n") if "RAGTruth-QA" in p]
        self.assertEqual(len(para), 1)
        text = para[0]
        self.assertIn("at the default thresholds", text)
        self.assertIn(
            "80.4% of answers (95% interval 75.2% to 84.8%, 120 hallucinated and 180 faithful answers) [measured]",
            text,
        )
        self.assertIn(
            "78.1% (95% interval 73.3% to 82.6%, 120 hallucinated and 180 faithful answers) [measured]",
            text,
        )
        self.assertIn("76% at 0.5 [third-party]", text)
        self.assertNotIn("87%", out, "never beside the tuned-threshold figure (D212)")

    def test_an_incomplete_agreement_is_left_out_with_a_note(self) -> None:
        measured = copy.deepcopy(MEASURED)
        del measured["intervals"]["ragtruth_qa_agreement"]
        root = make_root(measured=measured)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root)
        self.assertEqual(code, 0, err)
        self.assertNotIn("RAGTruth", out)
        self.assertIn("the agreement paragraph is left out", err)
        self.assertIn("the interval of ragtruth_qa_agreement", err)

    def test_the_roles_line_only_when_asked(self) -> None:
        _, without, _ = run(self.root)
        _, with_roles, _ = run(self.root, "--roles")
        self.assertNotIn("open to", without)
        self.assertIn("I'm open to backend and AI-platform engineering roles", with_roles)
        self.assertIn("Built with Go", without)

    def test_the_featured_claim_needs_a_span_and_falls_back_to_the_evidence_list(self) -> None:
        trace = copy.deepcopy(TRACE)
        del trace["audit_detail"]["claims"][2]["evidence_spans"]
        root = make_root(trace=trace)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root)
        self.assertEqual(code, 0, err)
        self.assertIn(f"promised “{REFUND_CLAIM}”. The policy said “A fallback quote”.", out)


class MissingValueTest(unittest.TestCase):
    def assert_missing(self, root: Path, *expected: str) -> None:
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root)
        self.assertEqual(code, 1, err)
        self.assertEqual(out, "", "nothing is printed with a gap")
        self.assertIn("not printed, values are missing", err)
        for item in expected:
            self.assertIn(item, err)

    def test_no_measured_run(self) -> None:
        measured = copy.deepcopy(MEASURED)
        measured["status"] = "not_measured"
        self.assert_missing(
            make_root(measured=measured), "is not a measured run", "measured:gate_latency_ms_p50"
        )

    def test_a_mock_run_is_not_measured(self) -> None:
        measured = copy.deepcopy(MEASURED)
        measured["mock"] = True
        self.assert_missing(
            make_root(measured=measured), "mock True", "measured:fast_check_usd_per_1k"
        )

    def test_one_null_value(self) -> None:
        measured = copy.deepcopy(MEASURED)
        measured["values"]["same_checks_cost_ratio"]["value"] = None
        root = make_root(measured=measured)
        self.addCleanup(shutil.rmtree, root, True)
        code, _, err = run(root)
        self.assertEqual(code, 1)
        self.assertIn("measured:same_checks_cost_ratio", err)
        self.assertNotIn("measured:gate_latency_ms_p50", err)

    def test_no_recorded_tape(self) -> None:
        self.assert_missing(
            make_root(tape=False), "no recorded tape", "featured.claim", "featured.evidence"
        )

    def test_a_simulated_tape_does_not_count(self) -> None:
        manifest = {"kind": "mock", "featured": {"trace_id": TRACE_ID, "rule": "scripted"}}
        self.assert_missing(
            make_root(manifest=manifest), "kind 'mock', not recorded", "featured.claim"
        )

    def test_no_featured_trace(self) -> None:
        self.assert_missing(make_root(manifest={"kind": "recorded"}), "names no featured trace")

    def test_the_featured_trace_file_is_missing(self) -> None:
        self.assert_missing(make_root(trace=False), f"the featured trace {TRACE_ID} is not in")  # type: ignore[arg-type]

    def test_no_contradicted_claim(self) -> None:
        trace = copy.deepcopy(TRACE)
        trace["audit_detail"]["claims"] = trace["audit_detail"]["claims"][:2]
        self.assert_missing(make_root(trace=trace), "no CONTRADICTED claim with a span")

    def test_no_run_date(self) -> None:
        measured = copy.deepcopy(MEASURED)
        measured["generated_at"] = None
        self.assert_missing(make_root(measured=measured), "run.date")


class TemplateTest(unittest.TestCase):
    def test_the_real_templates_use_known_placeholders_only(self) -> None:
        md = (ROOT / lp.TEMPLATE_PATH).read_text(encoding="utf-8")
        claims = json.loads((ROOT / lp.CLAIMS_PATH).read_text(encoding="utf-8"))["claims"]
        measured_keys = set(
            json.loads((ROOT / lp.MEASURED_PATH).read_text(encoding="utf-8"))["values"]
        )
        claim_ids = {c["id"] for c in claims}
        for heading in (lp.POST_HEADING, lp.RESTRICTED_HEADING):
            tpl = lp.template(md, heading)
            for name in lp.PLACEHOLDER.findall(tpl):
                if name.startswith("measured:"):
                    self.assertIn(name.split(":", 1)[1], measured_keys, name)
                elif name.startswith("claim:"):
                    self.assertIn(name.split(":", 1)[1], claim_ids, name)
                else:
                    self.assertIn(
                        name,
                        {
                            "featured.claim",
                            "featured.evidence",
                            "run.date",
                            "run.ids",
                            "snapshot",
                            "agreement",
                            "roles",
                        },
                    )

    def test_the_mode_r_post_carries_no_fast_check_figure(self) -> None:
        md = (ROOT / lp.TEMPLATE_PATH).read_text(encoding="utf-8")
        tpl = lp.template(md, lp.RESTRICTED_HEADING)
        self.assertEqual(
            [n for n in lp.PLACEHOLDER.findall(tpl) if n not in ("roles",)],
            [],
            "mode R names no measured value, claim or recorded answer",
        )
        self.assertNotIn("Jev", tpl)
        self.assertNotRegex(tpl, r"\d\s?(%|ms\b|×)|\$\d")

    def test_the_mode_r_post_prints_without_a_run_or_a_tape(self) -> None:
        measured = copy.deepcopy(MEASURED)
        measured["status"] = "not_measured"
        root = make_root(measured=measured, tape=False)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--restricted")
        self.assertEqual(code, 0, err)
        self.assertTrue(out.startswith("Support bots promise things"))
        self.assertIn("the demo replays simulated traffic", out)

    def test_an_unknown_placeholder_is_a_usage_error(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        path = root / lp.TEMPLATE_PATH
        path.write_text(
            path.read_text(encoding="utf-8").replace("{run.date}", "{run.when}", 1),
            encoding="utf-8",
        )
        code, _, err = run(root)
        self.assertEqual(code, 2)
        self.assertIn("unknown placeholder {run.when}", err)

    def test_a_measured_claim_is_not_a_claim_placeholder(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        path = root / lp.TEMPLATE_PATH
        text = path.read_text(encoding="utf-8").replace(
            "{run.date}", "{claim:gate_latency_p50_measured}", 1
        )
        path.write_text(text, encoding="utf-8")
        code, _, err = run(root)
        self.assertEqual(code, 2)
        self.assertIn("goes in as {measured:KEY}", err)

    def test_the_make_targets_run_the_scripts(self) -> None:
        launch = (ROOT / "mk" / "launch.mk").read_text(encoding="utf-8")
        post = launch.split("\nlinkedin-post:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("scripts/linkedin_post.py", post)
        for flag in (
            "$(if $(CLIP),--clip)",
            "$(if $(ROLES),--roles)",
            "$(if $(RESTRICTED),--restricted)",
        ):
            self.assertIn(flag, post)
        target = launch.split("\nclip:", 1)[1].split("\n\n", 1)[0]
        self.assertIn('scripts/clip.py" "$(FILE)"', target)
        self.assertNotIn("not implemented", post + target)


class ClipTest(unittest.TestCase):
    TEXT = "27× cheaper — “quoted” and naïve ✓\n"

    def setUp(self) -> None:
        self.dir = Path(tempfile.mkdtemp(prefix="clip-"))
        self.addCleanup(shutil.rmtree, self.dir, True)
        self.exe, self.capture = fake_clip(self.dir)

    def which(self, iconv: bool = True) -> Any:
        def find(name: str) -> Optional[str]:
            if name == "clip.exe":
                return str(self.exe)
            if name == "iconv":
                return shutil.which("iconv") if iconv else None
            return None

        return find

    def test_the_utf16le_pipe_through_iconv(self) -> None:
        out = io.BytesIO()
        where = clip.copy(self.TEXT.encode("utf-8"), out=out, which=self.which())
        self.assertEqual(where, "clipboard")
        self.assertEqual(out.getvalue(), b"", "nothing goes to stdout")
        self.assertEqual(self.capture.read_bytes(), self.TEXT.encode("utf-16-le"))

    def test_without_iconv_the_same_bytes(self) -> None:
        where = clip.copy(
            self.TEXT.encode("utf-8"), out=io.BytesIO(), which=self.which(iconv=False)
        )
        self.assertEqual(where, "clipboard")
        self.assertEqual(self.capture.read_bytes(), self.TEXT.encode("utf-16-le"))

    def test_stdout_without_clip_exe(self) -> None:
        out = io.BytesIO()
        where = clip.copy(self.TEXT.encode("utf-8"), out=out, which=lambda _n: None)
        self.assertEqual(where, "stdout")
        self.assertEqual(out.getvalue(), self.TEXT.encode("utf-8"))
        self.assertFalse(self.capture.exists())

    def test_a_failing_clip_exe_is_reported(self) -> None:
        self.exe.write_text("#!/bin/sh\ncat >/dev/null\nexit 3\n", encoding="utf-8")
        with self.assertRaisesRegex(clip.ClipError, "clip.exe failed"):
            clip.copy(b"x", out=io.BytesIO(), which=self.which(iconv=False))

    def test_the_command_copies_a_file(self) -> None:
        src = self.dir / "notes.md"
        src.write_text(self.TEXT, encoding="utf-8")
        err = io.StringIO()
        code = clip.main([str(src)], stdout=io.BytesIO(), stderr=err, which=self.which(iconv=False))
        self.assertEqual(code, 0, err.getvalue())
        self.assertEqual(self.capture.read_bytes(), self.TEXT.encode("utf-16-le"))
        self.assertIn(f"copied {len(self.TEXT)} characters", err.getvalue())

    def test_the_command_refuses_a_missing_file_and_non_utf8(self) -> None:
        err = io.StringIO()
        self.assertEqual(clip.main([str(self.dir / "nope")], stderr=err, which=self.which()), 2)
        bad = self.dir / "latin1.txt"
        bad.write_bytes("naïve".encode("latin-1"))
        err = io.StringIO()
        self.assertEqual(
            clip.main([str(bad)], stdout=io.BytesIO(), stderr=err, which=self.which()), 1
        )
        self.assertIn("not UTF-8", err.getvalue())

    def test_linkedin_post_clip_copies_the_post(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--clip", which=self.which(iconv=False))
        self.assertEqual(code, 0, err)
        self.assertEqual(out, "")
        self.assertIn("copied the post", err)
        copied = self.capture.read_bytes().decode("utf-16-le")
        self.assertTrue(copied.startswith("My support bot promised"))
        self.assertIn("26.84×", copied)

    def test_linkedin_post_clip_without_clip_exe_prints(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--clip")
        self.assertEqual(code, 0, err)
        self.assertTrue(out.startswith("My support bot promised"))
        self.assertIn("no clip.exe here", err)


if __name__ == "__main__":
    unittest.main()
