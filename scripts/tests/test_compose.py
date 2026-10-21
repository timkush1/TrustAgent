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
from typing import Any, Dict, List, Sequence, Set

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import doctor  # noqa: E402
import public_demo  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
COMPOSE = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
DEMO = (ROOT / "docker-compose.demo.yml").read_text(encoding="utf-8")
E2E = (ROOT / "docker-compose.e2e.yml").read_text(encoding="utf-8")
ONBOARDING = (ROOT / "docker-compose.onboarding.yml").read_text(encoding="utf-8")
RECORD = (ROOT / "docker-compose.record.yml").read_text(encoding="utf-8")
KNOWLEDGE = (ROOT / "docker-compose.knowledge.yml").read_text(encoding="utf-8")
PUBLIC = (ROOT / "docker-compose.public.yml").read_text(encoding="utf-8")
PUBLIC_LOCAL = (ROOT / "deploy" / "public-demo" / "local.yml").read_text(encoding="utf-8")
CADDYFILE = (ROOT / "deploy" / "public-demo" / "Caddyfile").read_text(encoding="utf-8")
DX_MK = (ROOT / "mk" / "dx.mk").read_text(encoding="utf-8")
RELEASE_MK = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")
VERSION = (ROOT / "VERSION").read_text(encoding="utf-8").strip()
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
            ("docker-compose.public.yml", PUBLIC),
            ("deploy/public-demo/local.yml", PUBLIC_LOCAL),
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
            ("docker-compose.public.yml", PUBLIC),
            ("deploy/public-demo/local.yml", PUBLIC_LOCAL),
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
        # The public demo is the demo stack on a VM: same project, so its volumes are the demo's.
        self.assertRegex(PUBLIC, r"(?m)^name: truthtable-demo$")

    def test_record_and_knowledge_overlays_are_checked(self) -> None:
        # D123 and D131: compose-check validates both overlays with the files they are layered
        # on; the record stack (mk/release.mk) always runs as truthtable-record.
        for text in (RECORD, KNOWLEDGE):
            self.assertRegex(text, r"(?m)^services:")
        check = DX_MK.split("\ncompose-check:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(RECORD_COMPOSE) config -q", check)
        self.assertIn("$(KNOWLEDGE_COMPOSE) config -q", check)
        record = re.search(r"(?m)^RECORD_COMPOSE\s*=(.*)$", RELEASE_MK)
        self.assertIsNotNone(record)
        self.assertEqual(
            re.findall(r"-f (\S+)", record.group(1) if record else ""),
            ["docker-compose.yml", "docker-compose.record.yml"],
        )
        self.assertRegex(RELEASE_MK, r"(?m)^RECORD_PROJECT\s+:= truthtable-record$")
        self.assertRegex(RECORD, r"(?m)^name: truthtable-record$")
        knowledge = re.search(r"(?m)^KNOWLEDGE_COMPOSE\s*=(.*)$", DX_MK)
        self.assertIsNotNone(knowledge)
        self.assertIn("docker-compose.knowledge.yml", knowledge.group(1) if knowledge else "")

    def test_the_record_overlay_holds_the_d123_settings(self) -> None:
        proxy = RECORD.split("\n  proxy:\n", 1)[1].split("\n\n", 1)[0]
        for line in (
            "      TRUTHTABLE_TRACE_FLUSH: sync\n",
            '      TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY: "0.50"\n',
            '      TRUTHTABLE_SPEND_CAP_LLM_USD_DAY: "3"\n',
            "      TRAFFICGEN_API_KEY:\n",
            '    ports: !override\n      - "127.0.0.1::8080"',
        ):
            self.assertIn(line, proxy)
        for line in (
            '  TRUTHTABLE_DEMO_MODE: "false"\n',
            '  TRUTHTABLE_TEST_MODE: "true"\n',
            '  TRUTHTABLE_STRICT_CONFIG: "true"\n',
            '  TRUTHTABLE_PUBLIC_READ: "false"\n',
        ):
            self.assertIn(
                line, RECORD.split("x-record-env: &record-env\n", 1)[1].split("\n\n")[0] + "\n"
            )
        self.assertIn("      LLM_FALLBACK: none\n", RECORD)
        for service in ("postgres", "dashboard"):
            block = RECORD.split(f"\n  {service}:\n", 1)[1]
            self.assertTrue(block.startswith("    ports: !reset []"), service)
        # No mock-upstream, no profile: the record stack talks to the real vendors only.
        self.assertNotIn("mock-upstream:", RECORD)
        self.assertNotIn("profiles", RECORD)

    def test_the_knowledge_overlay_holds_the_d131_settings(self) -> None:
        self.assertRegex(KNOWLEDGE, r"(?m)^name: truthtable-knowledge$")
        env = KNOWLEDGE.split("x-knowledge-env: &knowledge-env\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn('TRUTHTABLE_KNOWLEDGE_ENABLED: "true"', env)
        self.assertIn('TRUTHTABLE_TEST_MODE: "true"', env)
        proxy = KNOWLEDGE.split("\n  proxy:\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn("TRUTHTABLE_POLICY_FILE: /etc/trustagent/policies.knowledge.yaml", proxy)
        self.assertIn("TRUTHTABLE_JEV_BACKEND: compatible", proxy)
        self.assertIn("TRUTHTABLE_JEV_BASE_URL: http://mock-upstream:8090", proxy)
        engine = KNOWLEDGE.split("\n  engine:\n", 1)[1].split("\n\n", 1)[0]
        for line in ("QDRANT_URL: http://qdrant:6333", "LLM_PROVIDER: mock", "LLM_FALLBACK: none"):
            self.assertIn(line, engine)
        for key in ("OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"):
            self.assertIn(f"{key}: !reset null", engine)
        self.assertIn("OPENROUTER_API_KEY: !reset null", proxy)
        self.assertIn("  mock-upstream:\n    profiles: !reset []", KNOWLEDGE)
        # The variables compose reads while it resolves the files go on its command line.
        knowledge_env = re.search(r"(?m)^KNOWLEDGE_ENV\s*=(.*)$", DX_MK)
        self.assertIsNotNone(knowledge_env)
        self.assertEqual(
            (knowledge_env.group(1) if knowledge_env else "").split(),
            [
                "COMPOSE_PROFILES=knowledge",
                "ENGINE_EXTRAS=knowledge",
                "TRUTHTABLE_KNOWLEDGE_ENABLED=true",
            ],
        )
        for target in ("knowledge-up", "knowledge-seed", "knowledge-scenarios", "knowledge-down"):
            self.assertRegex(DX_MK, rf"(?m)^{target}: ## ", target)
        seed = DX_MK.split("\nknowledge-seed:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("--seed-knowledge --admin-key-file /tmp/knowledge-admin.key", seed)
        self.assertIn("umask 077", seed)
        self.assertIn("rm -f /tmp/knowledge-admin.key", seed)
        scenarios = DX_MK.split("\nknowledge-scenarios:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(TRAFFICGEN_CHECK) --scenarios knowledge", scenarios)

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
        # D126: the default tag is the version (a mirror of VERSION, kept by bump_version.py),
        # and only the engine tag carries the ENGINE_EXTRAS suffix.
        tag = "${TRUSTAGENT_VERSION:-" + VERSION + "}"
        extras = "${ENGINE_EXTRAS:+-${ENGINE_EXTRAS:-}}"
        for service in ("proxy", "engine", "dashboard"):
            suffix = extras if service == "engine" else ""
            self.assertIn(
                f"    image: ghcr.io/timkush1/truthtable-{service}:{tag}{suffix}\n",
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
            "image: ghcr.io/timkush1/truthtable-proxy:${TRUSTAGENT_VERSION:-" + VERSION + "}",
            service,
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


def service_section(text: str, service: str) -> str:
    """A service's lines: from `  <service>:` to the next line indented two spaces or less."""
    rest = text.split(f"\n  {service}:\n", 1)[1]
    end = re.search(r"(?m)^ {0,2}\S", rest)
    return rest[: end.start()] if end else rest


def make_value(name: str) -> str:
    """A make variable's value, continuation lines joined."""
    found = re.search(rf"(?m)^{name}\s*[?:]?=((?:.*\\\n)*.*)$", DX_MK)
    if found is None:
        raise AssertionError(f"{name} is not defined in mk/dx.mk")
    return " ".join(found.group(1).replace("\\\n", " ").split())


def recipe(target: str) -> str:
    return DX_MK.split(f"\n{target}:", 1)[1].split("\n\n", 1)[0]


def caddy_routes() -> Dict[str, List[str]]:
    """The Caddyfile's named matchers: name -> path patterns."""
    code = "\n".join(line.split("#", 1)[0] for line in CADDYFILE.splitlines())
    return {
        name: patterns.split()
        for name, patterns in re.findall(r"(?m)^\s*@(\w+)\s+path\s+(.+?)\s*$", code)
    }


def caddy_covers(patterns: Sequence[str], path: str) -> bool:
    """Whether a Caddy `path` matcher (exact, or a trailing `*` prefix) matches ``path``."""
    return any(path == p or (p.endswith("*") and path.startswith(p[:-1])) for p in patterns)


class PublicDemoTest(unittest.TestCase):
    """D218 (P6 CL-1, CL-2): the always-on public demo's overlay, its edge and its targets."""

    DEMO_STACK = ("proxy", "engine", "postgres", "dashboard", "trafficgen")

    def test_only_caddy_publishes_host_ports(self) -> None:
        caddy = service_section(PUBLIC, "caddy")
        ports = caddy.split("    ports:\n", 1)[1].split("    environment:\n", 1)[0]
        self.assertEqual(
            re.findall(r'(?m)^      - "([^"]*)"$', ports), ["80:80", "443:443", "443:443/udp"]
        )
        # Every port the demo stack publishes in docker-compose.yml (bound to BIND_ADDR) is
        # unpublished by the demo overlay or this one, so BIND_ADDR stays 127.0.0.1 and Caddy is
        # the only way in.
        published = [s for s in self.DEMO_STACK if "    ports:" in service_section(COMPOSE, s)]
        self.assertEqual(published, ["proxy", "postgres", "dashboard"])
        for service in published:
            resets = [
                text
                for text in (DEMO, PUBLIC)
                if f"\n  {service}:\n" in text
                and "    ports: !reset []" in service_section(text, service)
            ]
            self.assertTrue(resets, f"{service} still publishes a host port")
        for service in self.DEMO_STACK:
            if f"\n  {service}:\n" in PUBLIC:
                self.assertNotRegex(service_section(PUBLIC, service), r'- "\d', service)

    def test_the_local_rehearsal_binds_loopback_only(self) -> None:
        caddy = service_section(PUBLIC_LOCAL, "caddy")
        self.assertIn("    ports: !override\n", caddy)
        self.assertEqual(re.findall(r'(?m)^      - "([^"]*)"$', caddy), ["127.0.0.1:28443:28443"])
        self.assertEqual(public_demo.LOCAL_URL, "http://localhost:28443")
        self.assertIn(public_demo.LOCAL_URL, CADDYFILE)
        self.assertIn(public_demo.LOCAL_URL, PUBLIC_LOCAL)
        self.assertNotIn("\n  proxy:", PUBLIC_LOCAL)

    def test_read_only_retention_origins_and_memory(self) -> None:
        env = PUBLIC.split("x-public-env: &public-env\n", 1)[1].split("\n\n", 1)[0]
        self.assertEqual(env.strip(), 'TRUTHTABLE_READONLY: "true"')
        self.assertEqual(KEYS["TRUTHTABLE_READONLY"]["services"], ["proxy", "engine"])
        proxy = service_section(PUBLIC, "proxy")
        for line in (
            "      <<: *public-env\n",
            '      TRUTHTABLE_RETENTION_DAYS: "7"\n',
            # No localhost origin on a public host; TRUTHTABLE_PUBLIC_URL is always allowed.
            "      TRUTHTABLE_ALLOWED_ORIGINS: ${TRUTHTABLE_PUBLIC_URL:-}\n",
        ):
            self.assertIn(line, proxy)
        # Retention is a proxy key (only the proxy stores traces), so the engine does not get it.
        self.assertEqual(KEYS["TRUTHTABLE_RETENTION_DAYS"]["services"], ["proxy"])
        engine = service_section(PUBLIC, "engine")
        self.assertIn("      <<: *public-env\n", engine)
        self.assertIn("          memory: 1g\n", engine)
        self.assertIn("          memory: 4g\n", service_section(COMPOSE, "engine"))
        # verify (scripts/public_demo.py) relies on the proxy trusting the compose network.
        self.assertIn(
            "      TRUTHTABLE_TRUSTED_PROXIES: ${TRUTHTABLE_TRUSTED_PROXIES:-172.16.0.0/12}\n",
            service_section(COMPOSE, "proxy"),
        )

    def test_every_service_rotates_its_log(self) -> None:
        services = re.findall(r"(?m)^  ([a-z][a-z0-9-]*):$", PUBLIC.split("\nservices:\n", 1)[1])
        services = [s for s in services if s not in ("caddy-data", "caddy-config")]
        self.assertEqual(sorted(services), sorted([*self.DEMO_STACK, "caddy"]))
        for service in services:
            self.assertIn("    logging: *public-logging\n", service_section(PUBLIC, service))
        logging = PUBLIC.split("x-public-logging: &public-logging\n", 1)[1].split("\n\n", 1)[0]
        self.assertIn('max-size: "10m"', logging)
        self.assertIn('max-file: "3"', logging)

    def test_caddy_is_pinned_small_and_keeps_its_certificates(self) -> None:
        caddy = service_section(PUBLIC, "caddy")
        self.assertRegex(caddy, r"(?m)^    image: caddy:2-alpine@sha256:[0-9a-f]{64}$")
        comments = " ".join(" ".join(re.findall(r"(?m)^\s*# ?(.*)$", PUBLIC)).split())
        self.assertRegex(comments, r"Pinned 20\d\d-\d\d-\d\d: caddy:2-alpine;")
        self.assertIn("the index digest after it is the pin", comments)
        for line in (
            "    mem_limit: 128m\n",
            "    restart: unless-stopped\n",
            "      - ./deploy/public-demo/Caddyfile:/etc/caddy/Caddyfile:ro\n",
            "      - caddy-data:/data\n",
            "      - caddy-config:/config\n",
            "      dashboard:\n        condition: service_healthy\n",
            "      TRUTHTABLE_PUBLIC_URL: ${TRUTHTABLE_PUBLIC_URL:-}\n",
            "    cap_drop: [ALL]\n",
            "    cap_add: [NET_BIND_SERVICE]\n",
        ):
            self.assertIn(line, caddy)
        volumes = PUBLIC.split("\nvolumes:\n", 1)[1]
        self.assertEqual(re.findall(r"(?m)^  ([a-z-]+):$", volumes), ["caddy-data", "caddy-config"])
        # The app images come from docker-compose.yml (TRUSTAGENT_VERSION) and are pulled.
        self.assertEqual(APP_IMAGE.findall(PUBLIC), [])
        self.assertNotIn("build:", PUBLIC)

    def test_the_edge_routes(self) -> None:
        code = "\n".join(line.split("#", 1)[0].rstrip() for line in CADDYFILE.splitlines())
        sites = re.findall(r"(?m)^(\S+) \{$", code)
        self.assertEqual(sites, ["{$TRUTHTABLE_PUBLIC_URL}"])
        self.assertIn("TRUTHTABLE_PUBLIC_URL", KEYS)
        routes = caddy_routes()
        self.assertEqual(routes["gateway"], ["/v1", "/v1/*"])
        self.assertEqual(routes["trustagent"], ["/api/*", "/ws", "/health", "/health/*"])
        gateway = code.index("handle @gateway {")
        proxy = code.index("handle @trustagent {")
        rest = code.index("\thandle {")
        self.assertLess(gateway, proxy)
        self.assertLess(proxy, rest)
        block = code[gateway:proxy]
        self.assertRegex(block, r'respond "[^"]*' + public_demo.EDGE_404_TEXT + r'[^"]*" 404')
        self.assertIn("reverse_proxy proxy:8080", code[proxy:rest])
        self.assertIn("reverse_proxy dashboard:3000", code[rest:])
        self.assertEqual(code.count("reverse_proxy"), 2)
        # Caddy writes the connecting address into X-Forwarded-For: no client-supplied address is
        # trusted or forwarded, and no access log keeps visitors' addresses.
        for forbidden in ("trusted_proxies", "header_up", "client_ip_headers"):
            self.assertNotIn(forbidden, code)
        self.assertRegex(code, r"(?m)^\trequest_header -X-Real-IP$")
        self.assertNotRegex(code, r"(?m)^\s*log\b")

    def test_the_edge_sends_every_proxied_nginx_path_to_the_proxy(self) -> None:
        # What the dashboard's nginx forwards to the proxy, Caddy forwards itself (keeping each
        # visitor's address), except the gateway API, which the edge closes.
        nginx = (
            ROOT / "frontend-react" / "nginx" / "templates" / "default.conf.template"
        ).read_text(encoding="utf-8")
        proxied = [
            path
            for path, body in re.findall(r"location\s+(?:=\s+)?(\S+)\s*\{([^}]*)\}", nginx)
            if "proxy_pass $trustagent_upstream" in body
        ]
        self.assertIn("/api/", proxied)
        self.assertIn("/v1/", proxied)
        routes = caddy_routes()
        for path in proxied:
            name = "gateway" if path.startswith("/v1") else "trustagent"
            self.assertTrue(caddy_covers(routes[name], path), path)
        for path in ("/", "/config.json", "/assets/app.js", "/connect/callback", "/healthz"):
            self.assertFalse(caddy_covers(routes["trustagent"], path), path)
            self.assertFalse(caddy_covers(routes["gateway"], path), path)

    def test_the_targets_pull_and_never_build(self) -> None:
        self.assertRegex(DX_MK, r"(?m)^DEMO_PROJECT \?= truthtable-demo$")
        self.assertRegex(DX_MK, r"(?m)^PUBLIC_DEMO_PROJECT \?= \$\(DEMO_PROJECT\)$")
        files = ["docker-compose.yml", "docker-compose.demo.yml", "docker-compose.public.yml"]
        compose = make_value("PUBLIC_DEMO_COMPOSE")
        self.assertEqual(re.findall(r"-f (\S+)", compose), files)
        local = make_value("PUBLIC_DEMO_LOCAL_COMPOSE")
        self.assertEqual(re.findall(r"-f (\S+)", local), [*files, "deploy/public-demo/local.yml"])
        for value in (compose, local):
            self.assertTrue(value.startswith("$(COMPOSE) -p $(PUBLIC_DEMO_PROJECT) "), value)
            self.assertTrue(value.endswith(" --profile demo"), value)
        up = recipe("public-demo-up")
        steps = [
            "$(PYTHON) scripts/configure.py --ensure",
            "$(PYTHON) scripts/public_demo.py url",
            "$$compose pull $$pull",
            "$$compose up -d --no-build --wait --wait-timeout $(WAIT_TIMEOUT)",
            "$(PYTHON) scripts/public_demo.py verify --project $(PUBLIC_DEMO_PROJECT)",
        ]
        self.assertEqual(sorted(steps, key=up.index), steps)
        self.assertNotRegex(up, r"(?<!no)-build")
        self.assertIn('compose="$(PUBLIC_DEMO_LOCAL_COMPOSE)"; pull="--policy missing"', up)
        self.assertIn('compose="$(PUBLIC_DEMO_COMPOSE)"; pull=""', up)
        self.assertIn("$(PUBLIC_DEMO_COMPOSE) down\n", recipe("public-demo-down") + "\n")
        self.assertIn("$(PUBLIC_DEMO_COMPOSE) down --volumes", recipe("public-demo-clean"))
        self.assertIn(
            "scripts/setup_url.py --project $(PUBLIC_DEMO_PROJECT)", recipe("public-demo-setup-url")
        )
        for target in (
            "public-demo-up",
            "public-demo-check",
            "public-demo-down",
            "public-demo-clean",
            "public-demo-logs",
            "public-demo-setup-url",
        ):
            self.assertRegex(DX_MK, rf"(?m)^{target}: ## ", target)
            self.assertNotIn("not implemented", recipe(target), target)


DOCKERFILES = ("backend-go", "backend-python", "frontend-react")
# The overlays DX-RELEASE owns (§15). docker-compose.load*.yml belongs to the GO stream;
# scripts/check_version.py warns when an overlay's default tag differs from VERSION.
OWNED_OVERLAYS = {
    "docker-compose.demo.yml": DEMO,
    "docker-compose.e2e.yml": E2E,
    "docker-compose.onboarding.yml": ONBOARDING,
    "docker-compose.record.yml": RECORD,
    "docker-compose.knowledge.yml": KNOWLEDGE,
}
APP_IMAGE = re.compile(r"image: ghcr\.io/timkush1/truthtable-(\w+):(\S+)")


class ImagesTest(unittest.TestCase):
    """DR-2: the release image set, the D126 tags and the D125 base-image pins."""

    def test_every_app_image_uses_the_version_tag_and_only_the_engine_has_extras(self) -> None:
        tag = "${TRUSTAGENT_VERSION:-" + VERSION + "}"
        for name, text in {"docker-compose.yml": COMPOSE, **OWNED_OVERLAYS}.items():
            for service, ref in APP_IMAGE.findall(text):
                expected = tag + (
                    "${ENGINE_EXTRAS:+-${ENGINE_EXTRAS:-}}" if service == "engine" else ""
                )
                self.assertEqual(ref, expected, f"{name}: truthtable-{service}")
        # ENGINE_EXTRAS reaches the engine's tag and build only (comments aside).
        engine = COMPOSE.split("\n  engine:\n", 1)[1].split("\n\n", 1)[0]
        code = "\n".join(line for line in COMPOSE.splitlines() if not line.lstrip().startswith("#"))
        self.assertEqual(code.count("ENGINE_EXTRAS"), engine.count("ENGINE_EXTRAS"))
        self.assertEqual(engine.count("ENGINE_EXTRAS"), 3)
        self.assertIn("        EXTRAS: ${ENGINE_EXTRAS:-}\n", engine)
        for name, text in OWNED_OVERLAYS.items():
            self.assertNotIn("ENGINE_EXTRAS", text.replace("ENGINE_EXTRAS=knowledge", ""), name)

    def test_every_overlay_is_validated_by_compose_check(self) -> None:
        check = DX_MK.split("\ncompose-check:", 1)[1].split("\n\n", 1)[0]
        load_mk = (ROOT / "mk" / "load.mk").read_text(encoding="utf-8")
        files = set(re.findall(r"-f (docker-compose[\w.-]*\.yml)", DX_MK + RELEASE_MK + load_mk))
        files |= set(re.findall(r"(docker-compose[\w.-]*\.yml)", load_mk))
        for path in sorted(ROOT.glob("docker-compose.*.yml")):
            self.assertIn(path.name, files, f"{path.name} is in no compose stack definition")
        for var in (
            "DEMO",
            "E2E",
            "ONBOARDING",
            "LOAD",
            "RECORD",
            "KNOWLEDGE",
            "PUBLIC_DEMO",
            "PUBLIC_DEMO_LOCAL",
        ):
            self.assertIn(f"$({var}_COMPOSE) config -q", check, var)

    def test_base_images_are_pinned_by_digest(self) -> None:
        # D125: every FROM names a tag and its multi-arch index digest; Dependabot's docker
        # ecosystem reads the tag and moves both. No ARG-built image references (Dependabot
        # cannot follow them) and no stage built from an unpinned image.
        for dockerfile in DOCKERFILES:
            text = (ROOT / dockerfile / "Dockerfile").read_text(encoding="utf-8")
            froms = re.findall(r"(?m)^FROM\s+(?:--platform=\S+\s+)?(\S+)", text)
            self.assertGreaterEqual(len(froms), 2, dockerfile)
            for image in froms:
                self.assertRegex(
                    image, r"^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$", dockerfile
                )
            # D144: the header says when the pins were taken and that the digest is the pin; it
            # names no patch release, which a Dependabot digest refresh would change silently.
            self.assertRegex(text, r"Pinned 20\d\d-\d\d-\d\d:", dockerfile)
            self.assertIn(
                "the index digest after it is the pin", " ".join(text.split()), dockerfile
            )
            header = text.split("\nFROM", 1)[0]
            self.assertNotIn("Resolved", header, dockerfile)
            self.assertNotRegex(header, r"\b\d+\.\d+\.\d+\b", f"{dockerfile}: a patch release")

    def test_dependabot_follows_the_pins_and_holds_the_engine_python(self) -> None:
        # The docker ecosystem watches every Dockerfile directory, so tag and digest move together.
        # Python 3.11 -> 3.12+ is a semver minor to Dependabot; the engine's ML wheels lag a new
        # Python, so both minor and major bumps are held back (digest refreshes still arrive).
        bot = (ROOT / ".github" / "dependabot.yml").read_text(encoding="utf-8")
        docker = bot.split("  - package-ecosystem: docker\n", 1)[1].split("\n\n", 1)[0]
        for dockerfile in DOCKERFILES:
            self.assertIn(f"      - /{dockerfile}\n", docker)
        python = docker.split("- dependency-name: 'python'\n", 1)[1].split("\n", 1)[0]
        self.assertIn("'version-update:semver-major'", python)
        self.assertIn("'version-update:semver-minor'", python)
        self.assertNotIn("semver-patch", python)

    def test_the_proxy_image_ships_every_command_the_targets_run(self) -> None:
        go = (ROOT / "backend-go" / "Dockerfile").read_text(encoding="utf-8")
        cmds = re.search(r'(?m)^ARG CMDS="([^"]*)"$', go)
        self.assertIsNotNone(cmds)
        self.assertEqual(
            (cmds.group(1) if cmds else "").split(),
            ["proxy", "trafficgen", "mock-upstream", "jevcheck", "export-replay"],
        )
        for cmd in (cmds.group(1) if cmds else "").split():
            self.assertTrue((ROOT / "backend-go" / "cmd" / cmd / "main.go").is_file(), cmd)

    def test_up_and_demo_build_off_a_release_tag_and_pull_on_one(self) -> None:
        # D126: RELEASE_CHECKOUT is 1 only when HEAD is exactly the tag v<VERSION>.
        self.assertIn(
            'git describe --exact-match --tags --match "v$$(cat VERSION 2>/dev/null)" HEAD', DX_MK
        )
        self.assertRegex(
            DX_MK, r"(?m)^APP_BUILD = \$\(if \$\(filter 1,\$\(RELEASE_CHECKOUT\)\),,--build\)$"
        )
        self.assertRegex(DX_MK, r"(?m)^DEMO_BUILD\s+\?= \$\(APP_BUILD\)$")
        self.assertRegex(DX_MK, r"(?m)^UP_BUILD\s+\?= \$\(APP_BUILD\)$")
        up = DX_MK.split("\nup:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(COMPOSE) up -d $(UP_BUILD) --wait", up)
        demo = DX_MK.split("\ndemo:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("$(DEMO_COMPOSE) up -d $(DEMO_BUILD) --wait", demo)


if __name__ == "__main__":
    unittest.main()
