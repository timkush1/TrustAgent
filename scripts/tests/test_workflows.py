"""Static checks of the GitHub workflows (D124, D140), text-based like test_compose.py.

Standard library only: the workflow files are read as text (their layout is regular: two-space
indented jobs, `- ` steps), so these run without actionlint or a YAML parser. `make
lint-workflows` lints the YAML itself.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path
from typing import Dict, List

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"
RELEASE_MK = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")

# A third-party action pinned to a full commit SHA, with the release it is in a comment.
PINNED_USES = re.compile(r"^\s*(?:- )?uses: [\w.-]+/[\w./-]+@[0-9a-f]{40} # v\d+\.\d+\.\d+$")


def read(name: str) -> str:
    return (WORKFLOWS / name).read_text(encoding="utf-8")


def code(text: str) -> str:
    """The workflow without its comment lines (comments may name what the code must not do)."""
    return "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))


def jobs(text: str) -> Dict[str, str]:
    """Job id -> the job's text (up to the next two-space-indented key)."""
    body = text.split("\njobs:\n", 1)[1]
    parts = re.split(r"(?m)^  ([A-Za-z0-9_-]+):\n", body)
    return {parts[i]: parts[i + 1] for i in range(1, len(parts) - 1, 2)}


def steps(job: str) -> List[str]:
    """The job's steps, each as its text (split on the six-space `- ` items)."""
    body = job.split("\n    steps:\n", 1)[1]
    return [s for s in re.split(r"(?m)^      - ", body)[1:]]


def run_lines(text: str) -> List[str]:
    """Every line of shell a workflow runs: `run: x` lines and the lines of `run: |` blocks."""
    lines = text.splitlines()
    out: List[str] = []
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*)(?:- )?run: ?(.*)$", line)
        if not m:
            continue
        if m.group(2) not in ("|", ">", "|-", ">-"):
            out.append(m.group(2))
            continue
        indent = len(m.group(1))
        for block in lines[i + 1 :]:
            if block.strip() and len(block) - len(block.lstrip()) <= indent:
                break
            out.append(block)
    return out


class PagesWorkflowTest(unittest.TestCase):
    """pages.yml: builds on every trigger, deploys only for the owner (D124), hardened (D140)."""

    def setUp(self) -> None:
        self.text = read("pages.yml")
        self.jobs = jobs(self.text)
        self.build = self.jobs["build"]
        self.deploy = self.jobs["deploy"]

    def test_triggers(self) -> None:
        on = self.text.split("\non:\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn("  push:\n    branches: [main]\n    tags: ['v*']\n", on)
        self.assertIn("\n  pull_request:\n", on)
        self.assertIn("\n  workflow_dispatch:\n    inputs:\n      deploy:\n", on)
        self.assertIn("        type: boolean\n        default: false", on)
        self.assertNotIn("pull_request_target", code(self.text))
        self.assertNotIn("workflow_run", code(self.text))

    def test_permissions_are_held_by_jobs(self) -> None:
        self.assertRegex(self.text, r"(?m)^permissions: \{\}$")
        self.assertIn("    permissions:\n      contents: read\n    env:", self.build)
        self.assertIn(
            "    permissions:\n      pages: write\n      id-token: write\n    concurrency:",
            self.deploy,
        )
        self.assertEqual(self.text.count("permissions:"), 3)

    def test_the_deploy_job_is_guarded_on_every_event(self) -> None:
        cond = self.deploy.split("    if: >-\n", 1)[1].split("\n    runs-on:", 1)[0]
        cond = " ".join(cond.split())
        self.assertEqual(
            cond,
            "vars.PAGES_ENABLED == 'true' && github.event_name != 'pull_request' && "
            "(github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/v')) && "
            "(github.event_name != 'workflow_dispatch' || inputs.deploy)",
        )
        self.assertIn("    needs: build\n", self.deploy)
        self.assertIn(
            "    concurrency:\n      group: pages\n      cancel-in-progress: false\n", self.deploy
        )
        self.assertIn("      name: github-pages\n", self.deploy)
        # The build job never deploys and never gets a deploy permission.
        self.assertNotIn("deploy-pages", self.build)
        self.assertNotIn("pages: write", self.build)

    def test_the_build_steps_run_in_the_d124_order(self) -> None:
        texts = steps(self.build)
        markers = [
            "uses: actions/checkout@",
            "pip install jsonschema==4.26.0",
            "run: make pages-recording-check",
            "uses: actions/setup-node@",
            "run: npm ci",
            "run: make pages-static",
            "run: make pages-size",
            "run: make pages-files",
            "run: make pages-gitleaks",
            "uses: actions/upload-pages-artifact@",
        ]
        found = []
        for marker in markers:
            idx = [i for i, s in enumerate(texts) if marker in s]
            self.assertEqual(len(idx), 1, marker)
            found.append(idx[0])
        self.assertEqual(found, sorted(found))
        self.assertIn("path: frontend-react/dist", texts[found[-1]])

    def test_dataset_and_base_path(self) -> None:
        # D110: recorded on a tag (and check_recording --require-recorded), auto elsewhere.
        self.assertIn(
            "      PAGES_DATASET: ${{ startsWith(github.ref, 'refs/tags/v') && 'recorded' || 'auto' }}\n",
            self.build,
        )
        self.assertIn("      PAGES_BASE_PATH: /TrustAgent/\n", self.build)
        static = RELEASE_MK.split("\npages-static:", 1)[1].split("\n\n", 1)[0]
        self.assertIn(
            "VITE_APP_MODE=static VITE_BASE_PATH=$(PAGES_BASE_PATH) "
            "VITE_DEMO_DATASET=$(PAGES_DATASET) npm run build:static",
            static,
        )
        check = RELEASE_MK.split("\npages-recording-check:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(if $(filter recorded,$(PAGES_DATASET)), --require-recorded)", check)

    def test_hardening(self) -> None:
        uses = [line for line in self.text.splitlines() if re.match(r"^\s*(- )?uses:", line)]
        self.assertGreaterEqual(len(uses), 5)
        for line in uses:
            self.assertRegex(line, PINNED_USES)
        checkouts = self.text.count("uses: actions/checkout@")
        self.assertEqual(self.text.count("persist-credentials: false"), checkouts)
        self.assertNotIn("secrets.", code(self.build))
        for line in run_lines(self.text):
            self.assertNotIn("${{", line, f"an expression inside run: {line.strip()}")
        # No dependency cache in a workflow that publishes (zizmor cache-poisoning).
        self.assertIn("package-manager-cache: false", self.build)
        self.assertNotRegex(self.build, r"(?m)^\s+cache:")

    def test_make_pages_build_runs_the_workflow_steps_in_order(self) -> None:
        recipe = RELEASE_MK.split("\npages-build:", 1)[1].split("\n\n", 1)[0]
        local = re.findall(r"(pages-[a-z-]+)|(npm ci)", recipe.split("\n", 1)[1])
        local_order = [a or b for a, b in local]
        ci_order = re.findall(r"run: (?:make )?(pages-[a-z-]+|npm ci)", self.build)
        self.assertEqual(local_order, ci_order)

    def test_gitleaks_and_jsonschema_pins_match_the_other_workflows(self) -> None:
        security = read("security.yml")
        version = re.search(r"GITLEAKS_VERSION: ([\d.]+)", security)
        digest = re.search(r"GITLEAKS_SHA256: ([0-9a-f]{64})", security)
        self.assertIsNotNone(version)
        self.assertIsNotNone(digest)
        self.assertRegex(
            RELEASE_MK,
            rf"(?m)^GITLEAKS_VERSION := {re.escape(version.group(1) if version else '')}$",
        )
        self.assertRegex(
            RELEASE_MK,
            rf"(?m)^GITLEAKS_SHA256_linux_x64\s+:= {digest.group(1) if digest else ''}$",
        )
        self.assertIn("gitleaks_$(GITLEAKS_VERSION)_$${os}_$${arch}.tar.gz", RELEASE_MK)
        self.assertIn("checksum mismatch", RELEASE_MK)
        pages_scan = RELEASE_MK.split("\npages-gitleaks:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("dir --config .gitleaks.toml --redact", pages_scan)
        self.assertIn("--exit-code 1 $(PAGES_DIST)", pages_scan)
        self.assertIn("jsonschema==4.26.0", read("ci-web.yml"))


if __name__ == "__main__":
    unittest.main()
