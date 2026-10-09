"""Static checks of the compose files against SPEC §3.4 and the key registry.

Standard library only: the compose files are read as text (their layout is
regular enough), so these run without Docker. `docker compose config -q`
validates the YAML itself (make compose-check, CI).
"""

from __future__ import annotations

import json
import re
import sys
import unittest
from pathlib import Path
from typing import Any, Dict, List, Set

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import doctor  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
DEMO = (ROOT / "docker-compose.demo.yml").read_text(encoding="utf-8")
E2E = (ROOT / "docker-compose.e2e.yml").read_text(encoding="utf-8")
ONBOARDING = (ROOT / "docker-compose.onboarding.yml").read_text(encoding="utf-8")
RECORD = (ROOT / "docker-compose.record.yml").read_text(encoding="utf-8")
KNOWLEDGE = (ROOT / "docker-compose.knowledge.yml").read_text(encoding="utf-8")
DX_MK = (ROOT / "mk" / "dx.mk").read_text(encoding="utf-8")
REGISTRY = json.loads((ROOT / "contracts" / "config" / "keys.v1.json").read_text(encoding="utf-8"))
KEYS: Dict[str, Dict[str, Any]] = {entry["name"]: entry for entry in REGISTRY["keys"]}
ALIASES: Dict[str, str] = {
    alias: entry["name"] for entry in REGISTRY["keys"] for alias in entry.get("aliases", [])
}


# Container wiring: set explicitly per service in docker-compose.yml, so a
# host-oriented .env cannot point a container at localhost.
WIRED = {
    "proxy": {
        "TRUTHTABLE_PORT",
        "TRUTHTABLE_METRICS_PORT",
        "TRUTHTABLE_DATABASE_URL",
        "TRUTHTABLE_GRPC_ADDRESS",
        "OLLAMA_BASE_URL",
        "TRUTHTABLE_TRUSTED_PROXIES",
        "TRUTHTABLE_PUBLIC_URL",
        "TRUTHTABLE_ALLOWED_ORIGINS",
    },
    "engine": {"GRPC_HOST", "GRPC_PORT", "METRICS_PORT", "OLLAMA_BASE_URL", "QDRANT_URL"},
}


def anchor_keys(text: str, anchor: str) -> List[str]:
    """Keys of a top-level `x-...: &anchor` mapping whose values are all empty."""
    lines = text.splitlines()
    start = next(i for i, line in enumerate(lines) if line.endswith(f"&{anchor}"))
    keys: List[str] = []
    for line in lines[start + 1 :]:
        if line and not line.startswith(" "):
            break
        match = re.match(r"^  ([A-Z][A-Z0-9_]*):\s*$", line)
        if match:
            keys.append(match.group(1))
    return keys


def expected_passthrough(service: str) -> Set[str]:
    names: Set[str] = set()
    for entry in REGISTRY["keys"]:
        if service not in entry["services"] or entry["kind"] == "path":
            continue
        if entry["name"] in WIRED[service]:
            continue
        names.add(entry["name"])
        names.update(entry.get("aliases", []))
    return names


class ComposeTest(unittest.TestCase):
    def test_passthrough_covers_every_registry_key_of_the_service(self) -> None:
        for service in ("proxy", "engine"):
            keys = anchor_keys(COMPOSE, f"{service}-env")
            self.assertEqual(len(keys), len(set(keys)), f"{service}: duplicate keys")
            self.assertEqual(set(keys), expected_passthrough(service), service)

    def test_every_interpolated_variable_is_a_registry_key(self) -> None:
        for name, text in (
            ("docker-compose.yml", COMPOSE),
            ("docker-compose.demo.yml", DEMO),
            ("docker-compose.e2e.yml", E2E),
            ("docker-compose.onboarding.yml", ONBOARDING),
            ("docker-compose.record.yml", RECORD),
            ("docker-compose.knowledge.yml", KNOWLEDGE),
        ):
            # `$${X}` is an escaped `$` for the container's shell, not interpolation.
            for var in set(re.findall(r"(?<!\$)\$\{([A-Za-z_][A-Za-z0-9_]*)", text)):
                self.assertTrue(
                    var in KEYS or var in ALIASES, f"{name}: ${{{var}}} is not in the registry"
                )

    def test_explicit_environment_keys_are_registry_keys(self) -> None:
        # `      KEY: value` lines (six spaces: inside a service's environment map).
        pattern = re.compile(r"^      ([A-Z][A-Z0-9_]*):", re.MULTILINE)
        allowed_foreign = {
            "POSTGRES_USER",
            "POSTGRES_DB",
            "OLLAMA_HOST",
            "QDRANT__SERVICE__GRPC_PORT",
            "GF_SECURITY_ADMIN_PASSWORD",
            "GF_USERS_ALLOW_SIGN_UP",
        }
        for name, text in (
            ("docker-compose.yml", COMPOSE),
            ("docker-compose.demo.yml", DEMO),
            ("docker-compose.e2e.yml", E2E),
            ("docker-compose.onboarding.yml", ONBOARDING),
            ("docker-compose.record.yml", RECORD),
            ("docker-compose.knowledge.yml", KNOWLEDGE),
        ):
            for key in set(pattern.findall(text)):
                if key in allowed_foreign:
                    continue
                self.assertTrue(
                    key in KEYS or key in ALIASES, f"{name}: {key} is not in the registry"
                )

    def test_project_names(self) -> None:
        # Fixed container names are caught repo-wide by scripts/check-forbidden.mjs.
        self.assertRegex(COMPOSE, r"(?m)^name: truthtable$")
        self.assertRegex(DEMO, r"(?m)^name: truthtable-demo$")
        self.assertRegex(E2E, r"(?m)^name: truthtable-e2e$")
        self.assertRegex(ONBOARDING, r"(?m)^name: truthtable-onboarding$")
        self.assertRegex(RECORD, r"(?m)^name: truthtable-record$")

    def test_record_and_knowledge_overlays_are_checked(self) -> None:
        # P5-PLAN D123 and D131: both overlays exist from Step 0 (stubs until DX-RELEASE fills
        # them) and compose-check validates them with the files they are layered on.
        for text in (RECORD, KNOWLEDGE):
            self.assertRegex(text, r"(?m)^services:")
        check = DX_MK.split("\ncompose-check:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(RECORD_COMPOSE) config -q", check)
        self.assertIn("$(KNOWLEDGE_COMPOSE) config -q", check)
        record = re.search(r"(?m)^RECORD_COMPOSE\s*=(.*)$", DX_MK)
        self.assertIsNotNone(record)
        self.assertEqual(
            re.findall(r"-f (\S+)", record.group(1) if record else ""),
            ["docker-compose.yml", "docker-compose.record.yml"],
        )
        self.assertRegex(DX_MK, r"(?m)^RECORD_PROJECT\s+\?= truthtable-record$")
        knowledge = re.search(r"(?m)^KNOWLEDGE_COMPOSE\s*=(.*)$", DX_MK)
        self.assertIsNotNone(knowledge)
        self.assertIn("docker-compose.knowledge.yml", knowledge.group(1) if knowledge else "")

    def test_every_published_port_binds_bind_addr(self) -> None:
        for name, text in (
            ("docker-compose.yml", COMPOSE),
            ("docker-compose.onboarding.yml", ONBOARDING),
        ):
            ports = re.findall(r'^\s+- "([^"]*:\d+)"', text, re.MULTILINE)
            self.assertTrue(ports, name)
            for port in ports:
                self.assertTrue(port.startswith("${BIND_ADDR:-127.0.0.1}:"), f"{name}: {port}")

    def test_app_images_and_pull_policy(self) -> None:
        for service in ("proxy", "engine", "dashboard"):
            self.assertIn(
                f"image: ghcr.io/timkush1/truthtable-{service}:${{TRUSTAGENT_VERSION:-edge}}",
                COMPOSE,
            )
            self.assertIn(
                f"dockerfile: {'backend-go' if service == 'proxy' else 'backend-python' if service == 'engine' else 'frontend-react'}/Dockerfile",
                COMPOSE,
            )
        self.assertEqual(COMPOSE.count("pull_policy: missing"), 3)

    def test_dashboard_settings_keep_the_image_defaults(self) -> None:
        # A bare `KEY:` that is unset reaches docker as "KEY", which clears the
        # image's ENV default (frontend-react/Dockerfile sets all four).
        dockerfile = (ROOT / "frontend-react" / "Dockerfile").read_text(encoding="utf-8")
        for key in (
            "DASHBOARD_API_BASE",
            "DASHBOARD_WS_URL",
            "DASHBOARD_PROXY_UPSTREAM",
            "DASHBOARD_CONNECT_SRC",
        ):
            default = KEYS[key]["default"]
            self.assertIn(f"      {key}: ${{{key}:-{default}}}", COMPOSE)
            self.assertIn(f'{key}="{default}"', dockerfile)

    def test_engine_gets_qdrant_only_with_knowledge(self) -> None:
        # A set QDRANT_URL starts the engine's retrieval stack (model download,
        # Qdrant connection); qdrant only runs in the knowledge profile.
        self.assertIn(
            "      QDRANT_URL: ${TRUTHTABLE_KNOWLEDGE_ENABLED:+http://qdrant:6333}\n", COMPOSE
        )
        self.assertEqual(COMPOSE.count("QDRANT_URL:"), 1)

    def test_trafficgen_overrides_the_proxy_image_healthcheck(self) -> None:
        # trafficgen reuses the proxy image, whose HEALTHCHECK probes
        # 127.0.0.1:8080; `up --wait` (make demo) fails on an unhealthy or a
        # disabled check, so the service checks the proxy it sends to instead.
        service = COMPOSE.split("\n  trafficgen:\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn(
            '      test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://proxy:8080/health"]',
            service,
        )
        self.assertNotIn("disable: true", service)

    def test_demo_removes_every_vendor_key_doctor_checks(self) -> None:
        for key in doctor.VENDOR_KEYS:
            self.assertIn(key, KEYS)
            self.assertTrue(KEYS[key]["secret"], key)
            self.assertRegex(DEMO, rf"(?m)^      {key}: !reset null$", key)

    def test_mock_upstream_serves_the_e2e_and_load_profiles(self) -> None:
        # Same image as the proxy (built by the proxy service, never pulled on its
        # own); its healthcheck probes its own port, not the proxy's.
        service = COMPOSE.split("\n  mock-upstream:\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn(
            "image: ghcr.io/timkush1/truthtable-proxy:${TRUSTAGENT_VERSION:-edge}", service
        )
        self.assertIn("pull_policy: never", service)
        self.assertIn("profiles: [e2e, load]", service)
        self.assertIn('command: ["/mock-upstream"]', service)
        self.assertIn('MOCK_UPSTREAM_ADDR: ":8090"', service)
        self.assertIn("http://127.0.0.1:8090/healthz", service)
        self.assertNotIn("ports:", service)
        dockerfile = (ROOT / "backend-go" / "Dockerfile").read_text(encoding="utf-8")
        self.assertRegex(dockerfile, r'(?m)^ARG CMDS="[^"]*\bmock-upstream\b[^"]*"$')

    def test_e2e_points_the_gateway_at_mock_upstream(self) -> None:
        for line in (
            'TRUTHTABLE_TEST_MODE: "true"',
            'TRUTHTABLE_STRICT_CONFIG: "true"',
            "TRUTHTABLE_TRACE_FLUSH: sync",
            "TRUTHTABLE_JEV_BACKEND: compatible",
            "TRUTHTABLE_JEV_BASE_URL: http://mock-upstream:8090",
            "TRUTHTABLE_JEV_PATH: /v1/systemone",
            "TRUTHTABLE_UPSTREAM_URL: http://mock-upstream:8090",
            "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL: http://mock-upstream:8090",
            "LLM_PROVIDER: mock",
        ):
            self.assertIn(line, E2E)
        # The proxy depends on mock-upstream, so the override turns the service on
        # itself: `config` and `up` work with or without --profile e2e.
        self.assertIn("\n  mock-upstream:\n    profiles: !reset []\n", E2E)
        # Every vendor credential is removed; the decision key is a placeholder
        # that only mock-upstream accepts.
        for key in doctor.VENDOR_KEYS:
            if key == "TRUTHTABLE_JEV_API_KEY":
                self.assertIn(f"      {key}: e2e-mock-upstream-key\n", E2E)
                continue
            self.assertRegex(E2E, rf"(?m)^      {key}: !reset null$", key)

    def test_demo_turns_on_the_mocks(self) -> None:
        for line in (
            'TRUTHTABLE_DEMO_MODE: "true"',
            'TRUTHTABLE_TEST_MODE: "true"',
            'TRUTHTABLE_STRICT_CONFIG: "true"',
            "TRUTHTABLE_JEV_BACKEND: mock",
            "LLM_PROVIDER: mock",
        ):
            self.assertIn(line, DEMO)

    def test_onboarding_starts_a_fresh_install_on_mock_upstream(self) -> None:
        # No decision-model settings and no env admin key (whatever .env holds), so the
        # dashboard opens on the setup wizard; sign-in required; debug logs for the leak check.
        proxy = ONBOARDING.split("\n  proxy:\n", 1)[1].split("\n\n", 1)[0] + "\n"
        for key in (
            "TRUTHTABLE_JEV_BACKEND",
            "TRUTHTABLE_JEV_API_KEY",
            "TRUTHTABLE_JEV_BASE_URL",
            "TRUTHTABLE_JEV_PATH",
            "TRUTHTABLE_JEV_MODEL",
            "TRUTHTABLE_ADMIN_KEYS",
            "TRUTHTABLE_ADMIN_KEY",
        ):
            self.assertIn(f'      {key}: ""\n', proxy, key)
            # Each one is set in the e2e stack or passed through from .env: the override must
            # reach it, so it has to be a key (or an alias of one) the proxy reads.
            self.assertIn("proxy", KEYS[ALIASES.get(key, key)]["services"], key)
        self.assertIn('      TRUTHTABLE_PUBLIC_READ: "false"\n', proxy)
        self.assertIn("      TRUTHTABLE_LOG_LEVEL: debug\n", proxy)
        # Connect OpenRouter and the judge stay on the mocks the e2e stack configures.
        self.assertIn("      TRUTHTABLE_OPENROUTER_BASE_URL: http://mock-upstream:8090\n", proxy)
        self.assertIn("TRUTHTABLE_OPENROUTER_BASE_URL: http://mock-upstream:8090", E2E)
        engine = ONBOARDING.split("\n  engine:\n", 1)[1].split("\n\n", 1)[0] + "\n"
        self.assertIn("      LLM_PROVIDER: mock\n", engine)
        self.assertIn("      TRUTHTABLE_LOG_LEVEL: debug\n", engine)
        # LOG_LEVEL wins in the engine (truthtable.config): set, not left to .env.
        self.assertIn("      LOG_LEVEL: DEBUG\n", engine)
        # The browser reaches mock-upstream's sign-in page on MOCK_UPSTREAM_HOST_PORT.
        mock = ONBOARDING.split("\n  mock-upstream:\n", 1)[1]
        self.assertIn(
            '      - "${BIND_ADDR:-127.0.0.1}:${MOCK_UPSTREAM_HOST_PORT:-18090}:8090"', mock
        )
        key = KEYS["MOCK_UPSTREAM_HOST_PORT"]
        self.assertEqual((key["services"], key["default"]), (["compose"], "18090"))

    def test_onboarding_targets_layer_the_e2e_stack_and_delete_volumes(self) -> None:
        compose = re.search(r"(?m)^ONBOARDING_COMPOSE\s*=((?:.*\\\n)*.*)$", DX_MK)
        self.assertIsNotNone(compose)
        files = re.findall(r"-f (\S+)", compose.group(1) if compose else "")
        self.assertEqual(
            files, ["docker-compose.yml", "docker-compose.e2e.yml", "docker-compose.onboarding.yml"]
        )
        self.assertIn("--profile e2e", compose.group(1) if compose else "")
        self.assertRegex(DX_MK, r"(?m)^ONBOARDING_PROJECT \?= truthtable-onboarding$")
        self.assertRegex(DX_MK, r"(?m)^ONBOARDING_BUILD\s+\?= --build$")
        for target in (
            "onboarding-up",
            "onboarding-down",
            "onboarding-logs",
            "onboarding-setup-url",
        ):
            self.assertRegex(DX_MK, rf"(?m)^{target}: ## ", target)
        down = DX_MK.split("\nonboarding-down:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(ONBOARDING_COMPOSE) down -v", down)
        url = DX_MK.split("\nonboarding-setup-url:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("--url-only --project $(ONBOARDING_PROJECT)", url)
        check = DX_MK.split("\ncompose-check:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(ONBOARDING_COMPOSE) config -q", check)

    def test_images_are_built_with_their_git_revision(self) -> None:
        # GET /api/export/replay names the revision in a recording; the build context has no .git,
        # so make passes `git rev-parse --short HEAD` and the Dockerfiles stamp it.
        self.assertEqual(COMPOSE.count("        GIT_SHA: ${GIT_SHA:-}\n"), 3)
        self.assertRegex(
            DX_MK, r"(?m)^GIT_SHA := \$\(shell git rev-parse --short HEAD 2>/dev/null\)$"
        )
        self.assertRegex(DX_MK, r"(?m)^export GIT_SHA$")
        for dockerfile in ("backend-go", "backend-python", "frontend-react"):
            text = (ROOT / dockerfile / "Dockerfile").read_text(encoding="utf-8")
            self.assertIn('ARG GIT_SHA=""', text, dockerfile)
            self.assertIn('org.opencontainers.image.revision="${GIT_SHA}"', text, dockerfile)
            # In the runtime stage the revision is declared and labelled last, after every RUN
            # and COPY: a new commit must not rebuild (or re-download) the layers above it.
            final = text.rsplit("\nFROM ", 1)[1]
            instructions = re.findall(r"(?m)^([A-Z]+)\s+(.*)$", final)
            self.assertEqual(
                instructions[-2:],
                [
                    ("ARG", 'GIT_SHA=""'),
                    ("LABEL", 'org.opencontainers.image.revision="${GIT_SHA}"'),
                ],
                dockerfile,
            )
            self.assertEqual(final.count("GIT_SHA"), 2, dockerfile)
        go = (ROOT / "backend-go" / "Dockerfile").read_text(encoding="utf-8")
        self.assertIn("-X github.com/truthtable/backend-go/internal/version.GitSHA=${GIT_SHA}", go)


if __name__ == "__main__":
    unittest.main()
