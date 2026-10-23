"""Unit tests for scripts/release_notes.py : the CHANGELOG section, the image digests from
canned inspect output, the verify commands, the measured labels and every refusal.

No test calls docker or the network: the canned output is scripts/tests/fixtures/inspect.json, and
the live path is checked with a stubbed subprocess.
"""

from __future__ import annotations

import contextlib
import copy
import io
import json
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, List, Tuple
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import release_notes as rn  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = Path(__file__).resolve().parent / "fixtures" / "inspect.json"
SNAPSHOT = "jev-1.13-20260917"

CHANGELOG = """# Changelog

## v2.0.0 — 2026-11-05 "Jev gateway"

The gateway release. See [the spec](docs/v2/SPEC.md) and [a site](https://example.com/x).

### Highlights
- One fast check per answer.

### Build log by phase

#### Added (P3)
- A long log nobody reads on the release page.

## v1.0.0 — 2026-06-11

- The first release.
"""

UNRELEASED = CHANGELOG.replace(
    '## v2.0.0 — 2026-11-05 "Jev gateway"', '## Unreleased — 2.0.0 "Jev gateway" (in progress)'
)

MEASURED: Dict[str, Any] = {
    "schema": "trustagent.measured/v1",
    "status": "measured",
    "tier": "lite",
    "mock": False,
    "generated_by": "bench-publish",
    "generated_at": "2026-11-02T10:00:00Z",
    "runs": ["20261102T100000Z-abc1234"],
    "snapshot": SNAPSHOT,
    "values": {
        "fast_check_usd_per_1k": {
            "value": 0.2134,
            "unit": "usd_per_1k",
            "description": "d",
            "run_id": "20261102T100000Z-abc1234",
            "system": "gate",
        },
        "ragtruth_qa_agreement": {
            "value": 0.8125,
            "unit": "fraction",
            "description": "d",
            "run_id": "20261102T100000Z-abc1234",
            "system": "gate",
        },
        "gate_latency_ms_p50": {
            "value": None,
            "unit": "ms",
            "description": "d",
            "run_id": None,
            "system": "gate",
        },
    },
    "intervals": {
        "ragtruth_qa_agreement": {
            "lo": 0.75,
            "hi": 0.86,
            "method": "bootstrap",
            "resamples": 2000,
            "seed": 13,
            "n_pos": 40,
            "n_neg": 160,
        },
    },
}

LIKE_FOR_LIKE = {
    "id": "arize_ragtruth_accuracy_at_default_threshold",
    "text": "Arize RAGTruth test: Jev accuracy at a 0.5 threshold",
    "unit": "fraction",
    "display": "76% at 0.5",
    "label": "third_party",
    "caveat": "Thresholds need calibrating.",
}

RECORDED_TAPE: Dict[str, Any] = {
    "kind": "recorded",
    "recorded_at": "2026-11-03T12:00:00Z",
    "trustagent_version": "2.0.0",
    "decision_model": {
        "backend": "openrouter-decisions",
        "snapshot": SNAPSHOT,
        "host": "openrouter.ai",
    },
    "judge": {
        "provider": "openrouter",
        "model": "anthropic/claude-haiku",
        "fallback_active": False,
        "host": "openrouter.ai",
    },
    "answer_sources": {"scripted": 10, "generated": 2, "generated_host": "openrouter.ai"},
}

CLAIMS = {
    "version": "1",
    "claims": [
        {
            "id": "fast_check_cost_per_1k_measured",
            "text": "Our measured fast-check cost",
            "unit": "usd_per_1k",
            "display": "${value} / 1,000 answers",
            "label": "measured",
            "measured_ref": "fast_check_usd_per_1k",
        },
        {
            "id": "ragtruth_qa_agreement_measured",
            "text": "Our RAGTruth-QA agreement",
            "unit": "fraction",
            "display": "{value}",
            "label": "measured",
            "measured_ref": "ragtruth_qa_agreement",
        },
        {
            "id": "gate_latency_p50_measured",
            "text": "Our p50 fast-check latency",
            "unit": "ms",
            "display": "{value} ms p50",
            "label": "measured",
            "measured_ref": "gate_latency_ms_p50",
        },
        {
            "id": "fast_check_cost_per_1k",
            "text": "Fast check",
            "unit": "usd_per_1k",
            "display": "~$0.21 / 1,000 answers",
            "label": "estimate",
        },
        LIKE_FOR_LIKE,
    ],
}


def make_root(
    version: str = "2.0.0",
    changelog: str = CHANGELOG,
    licence: str = "Copyright (c) 2026 Jane Doe\n",
    measured: Dict[str, Any] = MEASURED,
    tape: Dict[str, Any] | None = None,
    claims_doc: Dict[str, Any] = CLAIMS,
) -> Path:
    tmp = Path(tempfile.mkdtemp(prefix="release-notes-"))
    (tmp / "VERSION").write_text(version + "\n", encoding="utf-8")
    (tmp / "CHANGELOG.md").write_text(changelog, encoding="utf-8")
    (tmp / "LICENSE").write_text("MIT License\n\n" + licence, encoding="utf-8")
    claims = tmp / rn.CLAIMS_PATH
    claims.parent.mkdir(parents=True)
    claims.write_text(json.dumps(claims_doc), encoding="utf-8")
    # The contract's own vendor host list (D203), as the release checkout has it.
    schema = tmp / rn.COMMON_SCHEMA
    schema.parent.mkdir(parents=True)
    shutil.copyfile(ROOT / rn.COMMON_SCHEMA, schema)
    (tmp / rn.MEASURED_PATH).write_text(json.dumps(measured), encoding="utf-8")
    if tape is not None:
        path = tmp / rn.RECORDED_MANIFEST
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps(tape), encoding="utf-8")
    return tmp


def run(root: Path, *argv: str) -> Tuple[int, str, str]:
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = rn.main(list(argv), root=root)
    return code, out.getvalue(), err.getvalue()


def canned(**changes: Any) -> Path:
    """A copy of the fixture with some services' manifests replaced (None drops the service)."""
    data = json.loads(FIXTURE.read_text(encoding="utf-8"))
    for service, manifest in changes.items():
        if manifest is None:
            data.pop(service, None)
        else:
            data[service] = manifest
    path = Path(tempfile.mkdtemp(prefix="inspect-")) / "inspect.json"
    path.write_text(json.dumps(data), encoding="utf-8")
    return path


class ReleaseNotesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.root = make_root()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))

    def test_notes_from_canned_inspect_output(self) -> None:
        code, out, err = run(self.root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        # Canned digests never pass for the registry's: the notes say so first, and stderr too.
        banner = (
            "> **Canned image data (inspect.json): these digests were not read from the registry. "
            "Not for publishing.**\n\n"
        )
        self.assertTrue(out.startswith(banner), out[:200])
        self.assertIn("warning: the image digests come from inspect.json, not the registry", err)
        out = out[len(banner) :]
        # The CHANGELOG section without its heading and without the build log.
        self.assertTrue(out.startswith("The gateway release."), out[:200])
        self.assertNotIn("## v2.0.0", out)
        self.assertIn("### Highlights\n- One fast check per answer.", out)
        self.assertNotIn("A long log nobody reads", out)
        self.assertIn(
            "The build log by phase is in [CHANGELOG.md](https://github.com/timkush1/TrustAgent/blob/v2.0.0/CHANGELOG.md).",
            out,
        )
        self.assertNotIn("The first release", out)
        # Relative links point into the tagged tree; absolute ones stay.
        self.assertIn(
            "[the spec](https://github.com/timkush1/TrustAgent/blob/v2.0.0/docs/v2/SPEC.md)", out
        )
        self.assertIn("[a site](https://example.com/x)", out)
        # One row and one verify command per image, by digest.
        for service in rn.SERVICES:
            digest = self.fixture[service]["digest"]
            ref = f"ghcr.io/timkush1/truthtable-{service}"
            self.assertIn(
                f"| {service} | `{ref}:2.0.0` | `{digest}` | linux/amd64, linux/arm64 |", out
            )
            self.assertIn(f"gh attestation verify oci://{ref}@{digest} -R timkush1/TrustAgent", out)
            self.assertIn(f"docker buildx imagetools inspect {ref}:2.0.0", out)
        self.assertIn("check out `v2.0.0` and run `make up`", out)

    def test_measured_figures_carry_their_label_run_and_snapshot(self) -> None:
        _, out, _ = run(self.root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertIn(
            "- Our measured fast-check cost: $0.213 / 1,000 answers [measured] (run 20261102T100000Z-abc1234)",
            out,
        )
        # D212: the agreement with its interval and n per class, then the like-for-like figure.
        self.assertIn(
            "- Our RAGTruth-QA agreement: 81.3% [measured] (run 20261102T100000Z-abc1234). "
            "95% interval 75% to 86% (bootstrap, 2000 resamples); 40 answers with a "
            "hallucination and 160 without.",
            out,
        )
        self.assertIn(
            "Like for like (default thresholds): Arize RAGTruth test, Jev accuracy at a 0.5 "
            "threshold: 76% at 0.5 [third-party]. Thresholds need calibrating.",
            out,
        )
        self.assertNotIn("p50 fast-check latency", out)  # no value in this run: not listed
        self.assertNotIn("~$0.21", out)  # an estimate is not a measured figure
        self.assertIn(f"decision-model snapshot {SNAPSHOT}", out)
        self.assertIn("simulated tape", out)

    def test_a_measured_latency_names_how_it_was_taken(self) -> None:
        """D205, D225: connection, requests at a time, where it ran, snapshot and n."""
        conditions = (
            "warm connection, one request at a time, run on Linux, region cloud us-east, "
            f"snapshot {SNAPSHOT}, n=199"
        )
        values = dict(MEASURED["values"])
        values["gate_latency_ms_p50"] = dict(
            values["gate_latency_ms_p50"], value=231.0, run_id="20261102T100000Z-abc1234"
        )
        claims = dict(CLAIMS, claims=[dict(c) for c in CLAIMS["claims"]])
        for c in claims["claims"]:
            if c["id"] == "gate_latency_p50_measured":
                c["caveat"] = "The fast check's own time per answer: {gate_conditions}."
        with_conditions = dict(
            MEASURED, values=values, sources={"gate_latency": {"conditions": conditions}}
        )
        root = make_root(measured=with_conditions, claims_doc=claims)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        self.assertIn(
            "- Our p50 fast-check latency: 231 ms p50 [measured] (run 20261102T100000Z-abc1234). "
            f"The fast check's own time per answer: {conditions}.",
            out,
        )
        self.assertNotIn("{gate_conditions}", out)
        self.assertNotIn("laptop", out)
        # Without the conditions the latency is refused, never printed bare.
        root = make_root(measured=dict(MEASURED, values=values), claims_doc=claims)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 1)
        self.assertEqual(out, "")
        self.assertIn("never published without how it was taken (D205)", err)

    def test_a_mock_or_unmeasured_run_lists_no_measured_figure(self) -> None:
        for measured in (dict(MEASURED, mock=True), dict(MEASURED, status="not_measured")):
            root = make_root(measured=measured)
            self.addCleanup(shutil.rmtree, root, True)
            code, out, _ = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
            self.assertEqual(code, 0)
            self.assertNotIn("[measured]", out)
            self.assertIn("No measured benchmark run is published in this release", out)

    def test_an_agreement_is_never_published_without_the_like_for_like_figure(self) -> None:
        claims = dict(CLAIMS, claims=[c for c in CLAIMS["claims"] if c is not LIKE_FOR_LIKE])
        root = make_root(claims_doc=claims)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 1)
        self.assertEqual(out, "")
        self.assertIn("an agreement is never published alone (D212)", err)
        # Without a measured agreement the like-for-like figure is not needed.
        values = {k: v for k, v in MEASURED["values"].items() if k != "ragtruth_qa_agreement"}
        root = make_root(claims_doc=claims, measured=dict(MEASURED, values=values))
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        self.assertNotIn("Like for like", out)

    def test_a_recorded_tape_is_named(self) -> None:
        root = make_root(tape=RECORDED_TAPE)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        self.assertIn(f"recorded 2026-11-03 on snapshot {SNAPSHOT} with TrustAgent 2.0.0", out)

    def test_a_tape_that_says_recorded_from_a_non_vendor_host_is_refused(self) -> None:
        # D203: the hosts are the evidence, not the kind the recorder wrote.
        for tape, named in (
            (
                dict(
                    RECORDED_TAPE,
                    decision_model=dict(RECORDED_TAPE["decision_model"], host="mock-upstream"),
                ),
                "fast-check host mock-upstream",
            ),
            (
                dict(
                    RECORDED_TAPE,
                    judge={k: v for k, v in RECORDED_TAPE["judge"].items() if k != "host"},
                ),
                "judge host none",
            ),
            (
                dict(
                    RECORDED_TAPE,
                    answer_sources={
                        "scripted": 1,
                        "generated": 3,
                        "generated_host": "mock-upstream",
                    },
                ),
                "generated-answer host mock-upstream",
            ),
        ):
            root = make_root(tape=tape)
            self.addCleanup(shutil.rmtree, root, True)
            code, out, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
            self.assertEqual(code, 1, named)
            self.assertEqual(out, "")
            self.assertIn(named, err)
            self.assertIn("not a vendor host", err)
        # Scripted answers only: no generated host is needed.
        tape = dict(RECORDED_TAPE, answer_sources={"scripted": 12, "generated": 0})
        root = make_root(tape=tape)
        self.addCleanup(shutil.rmtree, root, True)
        self.assertEqual(run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))[0], 0)

    def test_refuses_a_missing_digest(self) -> None:
        engine = copy.deepcopy(self.fixture["engine"])
        del engine["digest"]
        code, out, err = run(self.root, "--tag", "v2.0.0", "--inspect", str(canned(engine=engine)))
        self.assertEqual(code, 1)
        self.assertEqual(out, "")
        self.assertIn("ghcr.io/timkush1/truthtable-engine:2.0.0: no image digest", err)

    def test_refuses_a_malformed_digest_and_a_missing_image(self) -> None:
        proxy = dict(self.fixture["proxy"], digest="sha256:short")
        code, _, err = run(
            self.root, "--tag", "v2.0.0", "--inspect", str(canned(proxy=proxy, dashboard=None))
        )
        self.assertEqual(code, 1)
        self.assertIn("truthtable-proxy:2.0.0: no image digest", err)
        self.assertIn("truthtable-dashboard:2.0.0: not in inspect.json", err)

    def test_refuses_an_image_without_both_platforms(self) -> None:
        engine = copy.deepcopy(self.fixture["engine"])
        engine["manifests"] = [
            m for m in engine["manifests"] if m["platform"]["architecture"] != "arm64"
        ]
        code, _, err = run(self.root, "--tag", "v2.0.0", "--inspect", str(canned(engine=engine)))
        self.assertEqual(code, 1)
        self.assertIn("truthtable-engine:2.0.0: no linux/arm64 image (found linux/amd64)", err)

    def test_attestation_manifests_are_not_platforms(self) -> None:
        img = rn.read_image("proxy", "r", self.fixture["proxy"])
        self.assertEqual(img.platforms, ["linux/amd64", "linux/arm64"])

    def test_refuses_a_tag_that_is_not_version(self) -> None:
        code, _, err = run(self.root, "--tag", "v2.0.1", "--inspect", str(FIXTURE))
        self.assertEqual(code, 1)
        self.assertIn("tag v2.0.1 does not match VERSION 2.0.0", err)

    def test_refuses_an_unreleased_heading(self) -> None:
        root = make_root(changelog=UNRELEASED)
        self.addCleanup(shutil.rmtree, root, True)
        code, _, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 1)
        self.assertIn("CHANGELOG.md has no heading '## v2.0.0'", err)

    def test_refuses_the_owner_placeholder_in_the_licence(self) -> None:
        root = make_root(licence=f"Copyright (c) 2026 {rn.OWNER_PLACEHOLDER}\n")
        self.addCleanup(shutil.rmtree, root, True)
        code, _, err = run(root, "--tag", "v2.0.0", "--inspect", str(FIXTURE))
        self.assertEqual(code, 1)
        self.assertIn("LICENSE still says {{OWNER_LEGAL_NAME}}", err)

    def test_a_rehearsal_takes_the_unreleased_section_and_says_so(self) -> None:
        root = make_root(
            changelog=UNRELEASED, licence=f"Copyright (c) 2026 {rn.OWNER_PLACEHOLDER}\n"
        )
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v0.0.0-rehearsal", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        self.assertTrue(
            out.startswith(
                "> **Rehearsal (v0.0.0-rehearsal): canned or test data, not for publishing.**"
            )
        )
        self.assertNotIn("Canned image data", out)  # the rehearsal banner already says so
        self.assertIn("### Highlights", out)
        self.assertIn("truthtable-proxy:0.0.0-rehearsal", out)
        self.assertIn(
            "warning: v0.0.0-rehearsal is a rehearsal: the notes use the 2.0.0 section", err
        )
        self.assertIn("warning: LICENSE still says", err)

    def test_a_rehearsal_before_the_bump_takes_the_upcoming_section(self) -> None:
        # VERSION still names the last release; the Unreleased section is what will ship.
        changelog = UNRELEASED + "\n## v1.0.0 — 2026-06-11\n\nThe old release.\n"
        root = make_root(version="1.0.0", changelog=changelog)
        self.addCleanup(shutil.rmtree, root, True)
        code, out, err = run(root, "--tag", "v0.0.0-rehearsal", "--inspect", str(FIXTURE))
        self.assertEqual(code, 0, err)
        self.assertIn("### Highlights", out)
        self.assertNotIn("The old release.", out)
        self.assertIn("the notes use the 2.0.0 section", err)

    def test_usage_errors(self) -> None:
        self.assertEqual(run(self.root, "--tag", "2.0.0", "--inspect", str(FIXTURE))[0], 2)
        missing = self.root / "nope.json"
        self.assertEqual(run(self.root, "--tag", "v2.0.0", "--inspect", str(missing))[0], 2)

    def test_out_writes_a_file(self) -> None:
        dest = self.root / "notes.md"
        code, out, err = run(
            self.root, "--tag", "v2.0.0", "--inspect", str(FIXTURE), "--out", str(dest)
        )
        self.assertEqual(code, 0, err)
        self.assertEqual(out, "")
        self.assertIn("## Images", dest.read_text(encoding="utf-8"))


class LiveInspectTest(unittest.TestCase):
    """Without --inspect: docker buildx imagetools inspect, anonymously (an empty DOCKER_CONFIG)."""

    def test_inspects_each_image_without_a_saved_login(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
        calls: List[Tuple[List[str], str]] = []

        def fake_run(argv: List[str], **kwargs: Any) -> subprocess.CompletedProcess:
            calls.append((argv, kwargs["env"]["DOCKER_CONFIG"]))
            service = argv[-1].split("truthtable-", 1)[1].split(":", 1)[0]
            return subprocess.CompletedProcess(argv, 0, json.dumps(fixture[service]), "")

        with mock.patch.object(rn.subprocess, "run", side_effect=fake_run):
            code, out, err = run(root, "--tag", "v2.0.0")
        self.assertEqual(code, 0, err)
        self.assertEqual(
            [c[0] for c in calls],
            [
                [
                    "docker",
                    "buildx",
                    "imagetools",
                    "inspect",
                    "--format",
                    "{{json .Manifest}}",
                    f"ghcr.io/timkush1/truthtable-{s}:2.0.0",
                ]
                for s in rn.SERVICES
            ],
        )
        config = calls[0][1]
        self.assertTrue(config and all(c[1] == config for c in calls))
        self.assertFalse(Path(config).exists(), "the empty DOCKER_CONFIG is removed afterwards")
        self.assertIn(fixture["proxy"]["digest"], out)
        self.assertNotIn("Canned image data", out)
        self.assertEqual(err, "")

    def test_a_failed_inspect_is_refused(self) -> None:
        root = make_root()
        self.addCleanup(shutil.rmtree, root, True)
        failed = subprocess.CompletedProcess([], 1, "", "ERROR: not found")
        with mock.patch.object(rn.subprocess, "run", return_value=failed):
            code, _, err = run(root, "--tag", "v2.0.0")
        self.assertEqual(code, 1)
        self.assertIn("docker buildx imagetools inspect failed: ERROR: not found", err)


class RepositoryTest(unittest.TestCase):
    def test_make_target_runs_the_script(self) -> None:
        release_mk = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")
        target = release_mk.split("\nrelease-notes:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("scripts/release_notes.py --tag $(TAG)", target)
        self.assertIn("$(if $(RELEASE_NOTES_INSPECT),--inspect $(RELEASE_NOTES_INSPECT))", target)
        self.assertNotIn("not implemented", target)

    def test_the_fixture_has_every_service(self) -> None:
        data = json.loads(FIXTURE.read_text(encoding="utf-8"))
        for service in rn.SERVICES:
            rn.read_image(service, service, data[service])

    def test_the_release_pr_body_links_resolve(self) -> None:
        # A PR body has no relative links, so docs/launch/release-pr.md links absolutely into the
        # branch; each must name a file (and heading) that exists in this tree.
        import check_links

        body = (ROOT / "docs" / "launch" / "release-pr.md").read_text(encoding="utf-8")
        base = "https://github.com/timkush1/TrustAgent/blob/feat/v2-jev-gateway/"
        targets = re.findall(r"\]\((https://github\.com/timkush1/TrustAgent/blob/[^)\s]+)\)", body)
        self.assertGreater(len(targets), 5)
        for url in targets:
            self.assertTrue(url.startswith(base), url)
            path, _, anchor = url[len(base) :].partition("#")
            self.assertTrue((ROOT / path).is_file(), path)
            if anchor:
                headings = check_links.anchors((ROOT / path).read_text(encoding="utf-8"))
                self.assertIn(anchor, headings, url)
        self.assertNotRegex(body, r"\]\((?!https?://)", "no relative link in a PR body")

    def test_the_licence_and_the_package_metadata_agree(self) -> None:
        # One licence (MIT) and one holder across LICENSE, package.json and pyproject.toml: the
        # placeholder until the owner names it (S1), then that one name everywhere (D216).
        licence = (ROOT / "LICENSE").read_text(encoding="utf-8")
        self.assertTrue(licence.startswith("MIT License\n"))
        holder = re.search(r"(?m)^Copyright \(c\) \d{4} (.+)$", licence)
        self.assertIsNotNone(holder)
        name = holder.group(1).strip()
        pkg = json.loads((ROOT / "frontend-react" / "package.json").read_text(encoding="utf-8"))
        lock = json.loads(
            (ROOT / "frontend-react" / "package-lock.json").read_text(encoding="utf-8")
        )
        self.assertEqual(pkg.get("license"), "MIT")
        self.assertEqual(lock["packages"][""].get("license"), "MIT")
        self.assertEqual(pkg.get("author"), name)
        pyproject = (ROOT / "backend-python" / "pyproject.toml").read_text(encoding="utf-8")
        self.assertIn('license = "MIT"', pyproject)
        authors = re.search(r'(?m)^authors = \["([^"<]+?)(?: <[^>]+>)?"\]$', pyproject)
        self.assertIsNotNone(authors)
        self.assertEqual(authors.group(1), name)
        self.assertNotIn(
            "@", pyproject.split("[tool.poetry.dependencies]")[0], "no email in the metadata"
        )

    def test_the_real_changelog_has_a_section_for_version(self) -> None:
        version = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
        text = (ROOT / "CHANGELOG.md").read_text(encoding="utf-8")
        heading, body = rn.changelog_section(
            text, "2.0.0" if version == "1.0.0" else version, released_only=False
        )
        self.assertIn("Jev gateway", heading)
        notes = rn.release_body(body, "v2.0.0")
        self.assertIn("### Highlights", notes)
        self.assertNotIn("#### ", notes, "the build log by phase stays in CHANGELOG.md")
        self.assertLess(
            len(notes), 60_000, "a GitHub release body holds at most 125,000 characters"
        )


if __name__ == "__main__":
    unittest.main()
