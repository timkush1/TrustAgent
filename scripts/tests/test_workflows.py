"""Static checks of the GitHub workflows (D124, D125, D140), text-based like test_compose.py.

Standard library only: the workflow files are read as text (their layout is regular: two-space
indented jobs, `- ` steps), so these run without actionlint or a YAML parser. `make
lint-workflows` lints the YAML itself (actionlint, zizmor).

The hardening rules hold for every workflow; each workflow with a job of its own shape (Pages,
release images, eval-live, the Docker builds) has its own class below.
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path
from typing import Dict, List

ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"
RELEASE_MK = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")
ALL = sorted(path.name for path in WORKFLOWS.glob("*.yml"))

# Branch protection matches these check names (CLAUDE.md): file -> job id -> name.
REQUIRED_JOBS = {
    "ci-go.yml": {"go": "Go (proxy)"},
    "ci-python.yml": {"python": "Python (audit engine)"},
    "ci-web.yml": {"frontend": "Frontend (dashboard)"},
    "ci-docker.yml": {"docker": "Docker builds"},
    "ci-contracts.yml": {"contracts": "Contracts"},
    "ci-e2e.yml": {"scripts": "DX scripts", "e2e": "E2E (compose stack)"},
    "security.yml": {
        "gosec": "gosec (Go SAST)",
        "bandit": "bandit (Python SAST)",
        "gitleaks": "gitleaks (secrets)",
        "npm-audit": "npm audit (frontend deps)",
        "trivy": "Trivy (filesystem scan)",
    },
}
# The workflows that publish or spend: a tag build, the Pages artifact, a paid run (D140).
PRIVILEGED = ("pages.yml", "release-images.yml", "eval-live.yml", "ci-knowledge.yml")

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


def on_block(text: str) -> str:
    """The workflow's `on:` block."""
    return text.split("\non:\n", 1)[1].split("\n\n", 1)[0]


def job_name(job: str) -> str:
    m = re.search(r"(?m)^    name: (.+)$", job)
    return m.group(1) if m else ""


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


class EveryWorkflowTest(unittest.TestCase):
    """D140's rules, held by every workflow (the privileged ones and the CI checks alike)."""

    def test_the_workflow_set(self) -> None:
        for name in list(REQUIRED_JOBS) + ["pages.yml", "release-images.yml", "eval-live.yml"]:
            self.assertIn(name, ALL)
        self.assertIn("eval.yml", ALL)

    def test_every_action_is_pinned_to_a_commit_with_its_release(self) -> None:
        pins: Dict[str, set] = {}
        for name in ALL:
            for line in read(name).splitlines():
                if not re.match(r"^\s*(- )?uses:", line):
                    continue
                self.assertRegex(line, PINNED_USES, f"{name}: {line.strip()}")
                m = re.search(r"uses: ([\w.-]+/[\w./-]+)@([0-9a-f]{40}) # (v[\d.]+)$", line)
                if m:
                    pins.setdefault(m.group(1), set()).add((m.group(2), m.group(3)))
        self.assertGreaterEqual(len(pins), 10)
        # One pin per action across the workflows, so Dependabot moves them together.
        for action, seen in pins.items():
            self.assertEqual(len(seen), 1, f"{action} is pinned differently: {sorted(seen)}")

    def test_no_checkout_persists_credentials(self) -> None:
        for name in ALL:
            text = read(name)
            for job_id, job in jobs(text).items():
                for step in steps(job) if "\n    steps:\n" in job else []:
                    if "uses: actions/checkout@" in step:
                        self.assertIn(
                            "persist-credentials: false", step, f"{name}: {job_id}: checkout"
                        )

    def test_permissions_are_held_by_jobs(self) -> None:
        for name in ALL:
            text = read(name)
            self.assertRegex(text, r"(?m)^permissions: \{\}$", name)
            self.assertNotRegex(text, r"(?m)^permissions:\s*$", f"{name}: top-level grants")
            for job_id, job in jobs(text).items():
                self.assertRegex(job, r"(?m)^    permissions:", f"{name}: {job_id}")
                self.assertNotIn("write-all", job, f"{name}: {job_id}")
                self.assertNotIn("read-all", job, f"{name}: {job_id}")

    def test_no_privileged_trigger_and_no_expression_in_a_shell(self) -> None:
        for name in ALL:
            text = code(read(name))
            self.assertNotIn("pull_request_target", text, name)
            self.assertNotIn("workflow_run", text, name)
            for line in run_lines(read(name)):
                self.assertNotIn("${{", line, f"{name}: an expression inside run: {line.strip()}")

    def test_no_secret_reaches_a_build_arg(self) -> None:
        for name in ALL:
            text = read(name)
            blocks = re.findall(r"(?ms)^( +)build-args: \|\n(.*?)(?=^\1\S|\Z)", text)
            if "build-push-action@" in text:
                self.assertTrue(blocks, f"{name}: build-args not found")
            for _, block in blocks:
                self.assertIn("GIT_SHA=", block, name)
                self.assertNotIn("secrets.", block, f"{name}: a secret in build-args")

    def test_required_check_names_are_unchanged(self) -> None:
        for name, expected in REQUIRED_JOBS.items():
            found = jobs(read(name))
            for job_id, job_title in expected.items():
                self.assertIn(job_id, found, f"{name}: job {job_id}")
                self.assertEqual(job_name(found[job_id]), job_title, f"{name}: {job_id}")
                self.assertNotIn("    if:", found[job_id].split("\n    steps:\n", 1)[0], job_id)

    def test_required_checks_report_on_every_pull_request(self) -> None:
        # A required check with a path filter never reports on other pull requests and blocks
        # their merge.
        for name in REQUIRED_JOBS:
            on = on_block(read(name))
            self.assertRegex(on, r"(?m)^  pull_request:$", name)
            self.assertNotIn("paths", on, name)
            self.assertNotIn("branches-ignore", on, name)

    def test_privileged_workflows_cache_nothing(self) -> None:
        # A run that publishes or reads a paid key must not restore a cache another run wrote
        # (zizmor cache-poisoning). release-images.yml caches only off a tag (its own class).
        for name in PRIVILEGED:
            if name not in ALL or name == "release-images.yml":
                continue
            text = read(name)
            self.assertNotRegex(text, r"(?m)^\s+cache: (npm|pip|true)", name)
            if "actions/setup-node@" in text:
                self.assertIn("package-manager-cache: false", text, name)
            if "actions/setup-go@" in text:
                self.assertIn("cache: false", text, name)

    def test_lint_workflows_runs_both_pinned_linters(self) -> None:
        recipe = RELEASE_MK.split("\nlint-workflows:", 1)[1].split("\n\n", 1)[0]
        self.assertRegex(RELEASE_MK, r"(?m)^ACTIONLINT_VERSION\s+:= \d+\.\d+\.\d+$")
        self.assertRegex(RELEASE_MK, r"(?m)^ZIZMOR_VERSION\s+:= \d+\.\d+\.\d+$")
        self.assertRegex(RELEASE_MK, r"(?m)^SHELLCHECK_PY_VERSION\s+:= [\d.]+$")
        self.assertIn(
            "go run github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION)", recipe
        )
        self.assertIn("-shellcheck=$(WORKFLOW_LINT_BIN)/shellcheck", recipe)
        self.assertIn("zizmor --offline --min-severity high", recipe)
        install = RELEASE_MK.split("\nworkflow-lint-install:", 1)[1].split("\n\n", 1)[0]
        self.assertIn('"zizmor==$(ZIZMOR_VERSION)"', install)
        self.assertIn('"shellcheck-py==$(SHELLCHECK_PY_VERSION)"', install)
        # The DX scripts job proves the record stack's configuration and lints every workflow
        # (the lead's I-2 turn-on line).
        scripts = jobs(read("ci-e2e.yml"))["scripts"]
        self.assertIn("run: make record-check-config", scripts)
        self.assertIn("run: make lint-workflows", scripts)
        # Every accepted zizmor finding says why, next to it.
        for name in ALL:
            for line in read(name).splitlines():
                if "zizmor: ignore[" in line:
                    self.assertRegex(line, r"zizmor: ignore\[[a-z-]+\] \S.{10,}", name)

    def test_dependabot_waits_before_proposing_a_release(self) -> None:
        bot = (ROOT / ".github" / "dependabot.yml").read_text(encoding="utf-8")
        ecosystems = re.findall(r"(?m)^  - package-ecosystem: (\S+)$", bot)
        self.assertIn("github-actions", ecosystems)
        self.assertIn("docker", ecosystems)
        self.assertEqual(bot.count("    cooldown:\n      default-days: 7\n"), len(ecosystems))


class ReleaseImagesWorkflowTest(unittest.TestCase):
    """release-images.yml (D125): both platforms, pushed only from main or a release tag."""

    def setUp(self) -> None:
        self.text = read("release-images.yml")
        self.jobs = jobs(self.text)
        self.plan = self.jobs["plan"]
        self.images = self.jobs["images"]

    def test_triggers(self) -> None:
        on = on_block(self.text)
        self.assertIn("  push:\n    branches: [main]\n    tags: ['v*.*.*']\n", on)
        prs = on.split("\n  pull_request:\n", 1)[1].split("\n  workflow_dispatch:", 1)[0] + "\n"
        for path in (
            "'**/Dockerfile'",
            "'**/Dockerfile.dockerignore'",
            ".github/workflows/release-images.yml",
            "backend-go/go.sum",
            "backend-python/poetry.lock",
            "frontend-react/package-lock.json",
        ):
            self.assertIn(f"      - {path}\n", prs)
        dispatch = on.split("\n  workflow_dispatch:\n", 1)[1]
        self.assertIn("      push:\n", dispatch)
        self.assertIn("        type: boolean\n        default: false", dispatch)

    def test_push_is_guarded_by_repository_event_and_ref(self) -> None:
        push = self.plan.split("          PUSH: >-\n", 1)[1].split("\n        run:", 1)[0]
        self.assertEqual(
            " ".join(push.split()),
            "${{ github.repository == 'timkush1/TrustAgent' "
            "&& (github.event_name == 'push' || (github.event_name == 'workflow_dispatch' "
            "&& inputs.push)) "
            "&& (github.ref == 'refs/heads/main' || startsWith(github.ref, 'refs/tags/v')) }}",
        )
        # Every push, login, attestation and later job keys off that one decision.
        self.assertIn("push: ${{ needs.plan.outputs.push == 'true' }}", self.images)
        for step in steps(self.images):
            if "docker/login-action@" in step or "attest-build-provenance@" in step:
                self.assertIn("if: needs.plan.outputs.push == 'true'", step)
        for job_id in ("release-notes", "first-run", "anonymous-pull"):
            cond = re.search(r"(?m)^    if: (.+)$", self.jobs[job_id])
            self.assertIsNotNone(cond, job_id)
            self.assertIn("needs.plan.outputs.push == 'true'", cond.group(1) if cond else "")

    def test_a_release_tag_is_checked_against_version(self) -> None:
        check = [s for s in steps(self.plan) if "check_version.py" in s]
        self.assertEqual(len(check), 1)
        self.assertIn("if: startsWith(github.ref, 'refs/tags/v')", check[0])
        self.assertIn("RELEASE_TAG: ${{ github.ref_name }}", check[0])
        self.assertIn('run: python3 scripts/check_version.py --tag "$RELEASE_TAG"', check[0])
        self.assertIn('sha_tag="sha-${GITHUB_SHA::7}"', self.plan)

    def test_one_job_per_service_for_both_platforms(self) -> None:
        matrix = re.findall(r"- service: (\w+)\n\s+dockerfile: (\S+)", self.images)
        self.assertEqual(
            matrix,
            [
                ("proxy", "backend-go/Dockerfile"),
                ("engine", "backend-python/Dockerfile"),
                ("dashboard", "frontend-react/Dockerfile"),
            ],
        )
        self.assertIn("IMAGE: ghcr.io/timkush1/truthtable-${{ matrix.service }}", self.images)
        self.assertIn("platforms: linux/amd64,linux/arm64", self.images)
        self.assertIn("uses: docker/setup-qemu-action@", self.images)
        self.assertIn(
            "timeout-minutes: ${{ startsWith(github.ref, 'refs/tags/v') && 90 || 60 }}",
            self.images,
        )

    def test_tags(self) -> None:
        meta = [s for s in steps(self.images) if "docker/metadata-action@" in s][0]
        for tag in (
            "type=edge,branch=main",
            "type=semver,pattern={{version}}",
            "type=semver,pattern={{major}}.{{minor}}",
            "type=sha,prefix=sha-,format=short",
        ):
            self.assertIn(tag, meta)

    def test_attestations_and_clean_tag_builds(self) -> None:
        texts = steps(self.images)
        build = [i for i, s in enumerate(texts) if "docker/build-push-action@" in s]
        attest = [i for i, s in enumerate(texts) if "actions/attest-build-provenance@" in s]
        self.assertEqual(len(build), 1)
        self.assertEqual(len(attest), 1)
        self.assertLess(build[0], attest[0], "the attestation follows the push")
        b, a = texts[build[0]], texts[attest[0]]
        self.assertIn("provenance: mode=max", b)
        self.assertIn("sbom: true", b)
        self.assertIn("no-cache: ${{ startsWith(github.ref, 'refs/tags/v') }}", b)
        self.assertIn("pull: ${{ startsWith(github.ref, 'refs/tags/v') }}", b)
        for key in ("cache-from", "cache-to"):
            line = re.search(rf"(?m)^\s+{key}: (.+)$", b)
            self.assertIsNotNone(line, key)
            self.assertTrue(
                (line.group(1) if line else "").startswith(
                    "${{ !startsWith(github.ref, 'refs/tags/v') && "
                ),
                f"{key}: no cache on a tag",
            )
        self.assertIn("subject-digest: ${{ steps.build.outputs.digest }}", a)
        self.assertIn("push-to-registry: true", a)
        self.assertIn("GIT_SHA=${{ needs.plan.outputs.git_sha }}", b)

    def test_permissions_per_job(self) -> None:
        self.assertIn(
            "    permissions:\n      contents: read\n      packages: write\n"
            "      attestations: write\n      id-token: write\n",
            self.images,
        )
        for job_id, job in self.jobs.items():
            if job_id != "images":
                self.assertNotIn("attestations: write", job, job_id)
                self.assertNotIn("id-token: write", job, job_id)
                self.assertNotIn("packages: write", job, job_id)
        self.assertIn(
            "    permissions:\n      contents: read\n      packages: read\n", self.jobs["first-run"]
        )
        self.assertIn("    permissions: {}\n", self.jobs["anonymous-pull"])

    def test_release_notes_list_digests_and_the_verify_command(self) -> None:
        text = self.text
        self.assertIn("gh attestation verify oci://$IMAGE:$tag -R timkush1/TrustAgent", text)
        self.assertIn("DIGEST: ${{ steps.build.outputs.digest }}", self.images)
        notes = self.jobs["release-notes"]
        self.assertIn("startsWith(github.ref, 'refs/tags/v')", notes)
        self.assertIn('for svc in proxy engine dashboard; do cat "release-notes/$svc.md"', notes)

    def test_first_run_pulls_the_commit_tag(self) -> None:
        job = self.jobs["first-run"]
        self.assertIn("    needs: [plan, images]\n", job)
        self.assertIn("FIRST_RUN_TAG: ${{ needs.plan.outputs.sha_tag }}", job)
        self.assertIn("uses: docker/login-action@", job)
        self.assertIn("run: make first-run\n", job)
        # The timings land in tmp/ (ignored, D143), where make first-run writes them by default.
        self.assertIn("            tmp/first-run.json\n", job)
        self.assertRegex(RELEASE_MK, r"(?m)^FIRST_RUN_OUT\s+\?= tmp/first-run\.json$")
        self.assertIn("FIRST_RUN_KEEP: '1'", job)
        self.assertIn("make -s first-run-logs > first-run-artifacts/compose.log", job)
        self.assertIn("run: make first-run-down", job)
        texts = steps(job)
        order = [
            next(i for i, s in enumerate(texts) if marker in s)
            for marker in (
                "run: make first-run\n",
                "first-run-logs",
                "upload-artifact@",
                "first-run-down",
            )
        ]
        self.assertEqual(order, sorted(order))
        # make first-run takes FIRST_RUN_TAG from the environment and pulls it.
        self.assertRegex(RELEASE_MK, r"(?m)^FIRST_RUN_TAG\s+\?=\s*$")

    def test_anonymous_pull_logs_out_first(self) -> None:
        job = self.jobs["anonymous-pull"]
        self.assertIn("vars.GHCR_PUBLIC == 'true'", job)
        body = "\n".join(run_lines(job))
        self.assertLess(body.index("docker logout ghcr.io"), body.index("docker pull"))
        self.assertIn("for svc in proxy engine dashboard", body)
        self.assertIn("for platform in linux/amd64 linux/arm64", body)


class EvalLiveWorkflowTest(unittest.TestCase):
    """eval-live.yml: the paid bench-lite run, by hand, behind the eval-live environment."""

    def setUp(self) -> None:
        self.text = read("eval-live.yml")
        self.job = jobs(self.text)["bench-lite"]

    def test_dispatch_only_in_its_environment(self) -> None:
        self.assertEqual(on_block(self.text).strip(), "workflow_dispatch:")
        self.assertIn("    environment: eval-live\n", self.job)
        self.assertIn("    if: github.repository == 'timkush1/TrustAgent'\n", self.job)

    def test_gate_eval_then_bench_lite_and_never_publish(self) -> None:
        runs = run_lines(self.text)
        order = [r.strip() for r in runs if r.strip().startswith("make ")]
        self.assertEqual(order, ["make gate-eval", "make bench-lite"])
        self.assertNotIn("bench-publish", code(self.text))
        self.assertNotIn("eval-live", " ".join(order))
        upload = [s for s in steps(self.job) if "actions/upload-artifact@" in s]
        self.assertEqual(len(upload), 1)
        self.assertIn("path: backend-python/evals/results/", upload[0])
        self.assertIn("if: always()", upload[0])

    def test_the_key_reaches_only_the_bench_step(self) -> None:
        self.assertEqual(code(self.text).count("secrets."), 1)
        bench = [s for s in steps(self.job) if "run: make bench-lite" in s][0]
        self.assertIn("OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}", bench)

    def test_the_bench_step_confirms_the_spend(self) -> None:
        # make bench-lite is a dry run unless CONFIRM=1 (D204): without it the paid run spends nothing.
        bench = [s for s in steps(self.job) if "run: make bench-lite" in s][0]
        self.assertIn('CONFIRM: "1"', bench)

    def test_the_free_tier_stays_in_eval_yml(self) -> None:
        free = read("eval.yml")
        self.assertIn("--provider ollama", free)
        self.assertNotIn("secrets.", code(free))
        self.assertNotIn("bench-lite", free)


class DockerBuildsWorkflowTest(unittest.TestCase):
    """ci-docker.yml: the required amd64 build, plus a non-required arm64 smoke build."""

    def setUp(self) -> None:
        self.text = read("ci-docker.yml")
        self.jobs = jobs(self.text)

    def test_never_pushes(self) -> None:
        self.assertNotIn("push: true", self.text)
        self.assertNotIn("docker/login-action", self.text)
        self.assertEqual(self.text.count("push: false"), 5)

    def test_arm64_smoke_builds_the_cross_compiled_images(self) -> None:
        arm = self.jobs["arm64"]
        self.assertEqual(job_name(arm), "Docker arm64 smoke (proxy, dashboard)")
        self.assertIn("uses: docker/setup-qemu-action@", arm)
        files = re.findall(r"file: (\S+)", arm)
        self.assertEqual(files, ["backend-go/Dockerfile", "frontend-react/Dockerfile"])
        self.assertEqual(arm.count("platforms: linux/arm64\n"), 2)
        self.assertIn("release-images.yml", self.text.split("\non:\n", 1)[0])


class FirstRunTargetsTest(unittest.TestCase):
    """make first-run, golden-path and first-run-onboarding (D130, D165): fresh stacks on their own ports."""

    def recipe(self, target: str) -> str:
        return RELEASE_MK.split(f"\n{target}:", 1)[1].split("\n\n", 1)[0]

    def test_modes_and_images(self) -> None:
        self.assertIn(
            "FIRST_RUN_MODE = $(if $(FIRST_RUN_TAG),$(if $(filter 1,$(FIRST_RUN_LOCAL)),,pull),"
            "$(if $(filter 1,$(FIRST_RUN_LOCAL)),local,))",
            RELEASE_MK,
        )
        self.assertRegex(RELEASE_MK, r"(?m)^FIRST_RUN_LOCAL_TAG\s+\?= p5local$")
        up = RELEASE_MK.split("\ndefine FIRST_RUN_UP\n", 1)[1].split("\nendef\n", 1)[0]
        # Local mode never rebuilds unless asked, and fails on a missing image; pull mode pulls.
        self.assertIn('if [ "$(FIRST_RUN_BUILD)" = 1 ]; then $(FIRST_RUN_COMPOSE) build; fi', up)
        self.assertIn("docker image inspect", up)
        self.assertIn("$(FIRST_RUN_COMPOSE) pull --quiet", up)
        self.assertIn("up -d --no-build --pull never --wait", up)
        # T0 is taken after the clean slate and before the pull and up.
        self.assertLess(up.index("down -v --remove-orphans"), up.index("t0=$$($(NOW_MS))"))
        self.assertLess(up.index("t0=$$($(NOW_MS))"), up.index("pull --quiet"))
        self.assertIn("trap '$(FIRST_RUN_COMPOSE) down -v --remove-orphans", up)
        self.assertIn('[ "$(FIRST_RUN_KEEP)" != 1 ]', up)

    def test_ports_keep_clear_of_the_owner_stacks(self) -> None:
        ports = {
            name: int(value)
            for name, value in re.findall(r"(?m)^FIRST_RUN_(\w+)_PORT\s+\?= (\d+)$", RELEASE_MK)
        }
        self.assertEqual(set(ports), {"PROXY", "DASHBOARD", "MOCK"})
        self.assertFalse({8080, 3000, 5432, 18090} & set(ports.values()))
        self.assertIn("-f docker-compose.onboarding.yml --profile e2e", RELEASE_MK)

    def test_first_run_passes_what_the_spec_reads(self) -> None:
        recipe = self.recipe("first-run")
        self.assertIn("doctor.py --target docker --expect fresh", recipe)
        self.assertIn("--no-probe", recipe)
        self.assertIn("-g first-run", recipe)
        spec = (ROOT / "frontend-react" / "e2e" / "stack" / "first-run.spec.ts").read_text(
            encoding="utf-8"
        )
        read = set(re.findall(r"process\.env\.(FIRST_RUN_\w+)|'(FIRST_RUN_\w+)'", spec))
        names = {a or b for a, b in read} | set(re.findall(r"(?m)^  (FIRST_RUN_\w+):", spec))
        passed = set(re.findall(r"(FIRST_RUN_\w+)=", recipe))
        self.assertTrue(names, "the spec reads FIRST_RUN_* variables")
        self.assertEqual(names - passed, set(), "every variable the spec reads is passed")
        self.assertIn("measured locally, images prebuilt", spec)
        self.assertIn("measured in CI, images pulled", spec)

    def test_first_run_waits_for_the_proxy_to_reach_the_engine(self) -> None:
        # Ready is doctor passing on a linked stack, not three start-up WARNs (I-7).
        self.assertIn("--wait $(FIRST_RUN_DOCTOR_WAIT)", self.recipe("first-run"))
        self.assertRegex(RELEASE_MK, r"(?m)^FIRST_RUN_DOCTOR_WAIT\s+\?= 25$")

    def test_the_specs_run_in_a_subshell_so_the_removal_finds_the_compose_files(self) -> None:
        for target in ("first-run", "golden-path", "first-run-onboarding"):
            recipe = self.recipe(target)
            self.assertIn("(cd frontend-react && ", recipe, target)
            self.assertNotRegex(recipe, r"(?m)^\tcd frontend-react", target)

    def test_golden_path_runs_its_spec_on_a_fresh_stack(self) -> None:
        recipe = self.recipe("golden-path")
        self.assertIn("$(FIRST_RUN_UP)", recipe)
        self.assertIn("-g golden-path", recipe)
        self.assertNotIn("doctor", recipe)

    def test_onboarding_runs_its_spec_on_a_fresh_stack(self) -> None:
        recipe = self.recipe("first-run-onboarding")
        self.assertIn("$(FIRST_RUN_UP)", recipe)
        self.assertIn("-g onboarding", recipe)
        self.assertIn("$(FIRST_RUN_PLAYWRIGHT_ENV)", recipe)

    def test_the_proof_runs_each_spec_on_its_own_fresh_stack_in_order(self) -> None:
        # Each spec spends its stack's setup link, so they never share one (D165).
        recipe = self.recipe("first-run-proof")
        steps = re.findall(r"(?m)^\t@\$\(MAKE\) --no-print-directory (\S+)$", recipe)
        self.assertEqual(steps, ["first-run", "golden-path", "first-run-onboarding"])


class KnowledgeWorkflowTest(unittest.TestCase):
    """ci-knowledge.yml (D131): the knowledge stack in Docker, not a required check."""

    def setUp(self) -> None:
        self.text = read("ci-knowledge.yml")
        self.job = jobs(self.text)["knowledge"]

    def test_triggers_dispatch_weekly_and_the_knowledge_path(self) -> None:
        on = on_block(self.text)
        self.assertIn("  workflow_dispatch:\n", on)
        self.assertRegex(on, r"(?m)^  schedule:\n    - cron: '\d+ \d+ \* \* \d'")
        push = on.split("\n  push:\n", 1)[1].split("\n  pull_request:\n", 1)[0]
        pr = on.split("\n  pull_request:\n", 1)[1]
        push_paths = re.findall(r"(?m)^      - (\S+)$", push)
        self.assertEqual(push_paths, re.findall(r"(?m)^      - (\S+)$", pr))
        for path in (
            "docker-compose.knowledge.yml",
            "config/policies.knowledge.yaml",
            "backend-python/src/truthtable/kb/**",
            ".github/workflows/ci-knowledge.yml",
        ):
            self.assertIn(path, push_paths)

    def test_not_a_required_check(self) -> None:
        names = {name for checks in REQUIRED_JOBS.values() for name in checks.values()}
        self.assertNotIn(job_name(self.job), names)
        self.assertNotIn("ci-knowledge.yml", REQUIRED_JOBS)

    def test_up_seed_check_and_always_down(self) -> None:
        texts = steps(self.job)
        markers = [
            "run: make knowledge-up",
            "run: make knowledge-seed",
            "run: make knowledge-scenarios",
            "run: make knowledge-down",
        ]
        found = [next(i for i, s in enumerate(texts) if m in s) for m in markers]
        self.assertEqual(found, sorted(found))
        self.assertIn("if: always()", texts[found[-1]])
        self.assertNotIn("secrets.", code(self.text))


if __name__ == "__main__":
    unittest.main()
