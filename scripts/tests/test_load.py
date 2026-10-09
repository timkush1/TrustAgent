"""Static checks of the load test: the compose overrides, the load policy, the k6 script and the
make target agree with each other and with the key registry (docs/GATEWAY.md §13).

Standard library only, except the policy's schema check, which needs PyYAML and jsonschema and is
skipped without them (the other checks read the files as text).
"""

from __future__ import annotations

import re
import sys
import unittest
from pathlib import Path
from typing import Any, Dict, List

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import test_compose  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
LOAD = (ROOT / "docker-compose.load.yml").read_text(encoding="utf-8")
SLOW = (ROOT / "docker-compose.load-slow-jev.yml").read_text(encoding="utf-8")
POLICY_PATH = ROOT / "scripts" / "load" / "policies.yaml"
POLICY = POLICY_PATH.read_text(encoding="utf-8")
K6 = (ROOT / "scripts" / "load" / "gateway-overhead.js").read_text(encoding="utf-8")
LOAD_MK = (ROOT / "mk" / "load.mk").read_text(encoding="utf-8")
GATEWAY_DOC = (ROOT / "docs" / "GATEWAY.md").read_text(encoding="utf-8")

SCENARIOS = "abcdef"


def k6_scenarios() -> Dict[str, Dict[str, str]]:
    """The SCENARIOS table of the k6 script: {id: {route, threshold}}."""
    found: Dict[str, Dict[str, str]] = {}
    for match in re.finditer(
        r"^\s+([a-f]): \{ route: '([a-z-]+)', stream: (true|false), what: '[^']*', "
        r"threshold: (null|'[^']*')",
        K6,
        re.MULTILINE,
    ):
        found[match.group(1)] = {
            "route": match.group(2),
            "stream": match.group(3),
            "threshold": match.group(4).strip("'"),
        }
    return found


def policy_routes() -> Dict[str, str]:
    """Route name -> mode, read from the load policy as text."""
    routes: Dict[str, str] = {}
    current = None
    for line in POLICY.splitlines():
        name = re.match(r"^  - name: ([a-z0-9-]+)\s*$", line)
        if name:
            current = name.group(1)
            continue
        mode = re.match(r"^    mode: ([a-z]+)\s*$", line)
        if mode and current:
            routes[current] = mode.group(1)
    return routes


class LoadComposeTest(unittest.TestCase):
    def test_project_name(self) -> None:
        self.assertRegex(LOAD, r"(?m)^name: truthtable-load$")

    def test_every_variable_and_key_is_in_the_registry(self) -> None:
        for name, text in (("load", LOAD), ("load-slow-jev", SLOW)):
            for var in set(re.findall(r"(?<!\$)\$\{([A-Za-z_][A-Za-z0-9_]*)", text)):
                self.assertTrue(
                    var in test_compose.KEYS or var in test_compose.ALIASES, f"{name}: ${{{var}}}"
                )
            for key in set(re.findall(r"^      ([A-Z][A-Z0-9_]*):", text, re.MULTILINE)):
                self.assertTrue(
                    key in test_compose.KEYS or key in test_compose.ALIASES, f"{name}: {key}"
                )

    def test_no_host_port_is_published(self) -> None:
        # A load run must not clash with a demo or `make up` stack on 8080, 3000 or 5432.
        for service in ("proxy", "postgres", "dashboard"):
            block = re.search(rf"(?ms)^  {service}:\n(.*?)(?=^  [a-z]|\Z)", LOAD)
            assert block is not None, service
            self.assertIn("ports: !reset []", block.group(1), service)
        self.assertNotRegex(LOAD, r"(?m)^\s+- \"?\$\{BIND_ADDR")

    def test_no_vendor_key_reaches_the_stack(self) -> None:
        for key in (
            "OPENROUTER_API_KEY",
            "TYPESAFE_API_KEY",
            "TRUTHTABLE_UPSTREAM_API_KEY",
            "TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY",
            "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY",
            "ANTHROPIC_API_KEY",
            "OPENAI_API_KEY",
        ):
            self.assertIn(f"{key}: !reset null", LOAD, key)
        for url_key in (
            "TRUTHTABLE_JEV_BASE_URL",
            "TRUTHTABLE_UPSTREAM_URL",
            "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL",
            "TRUTHTABLE_FALLBACK_UPSTREAM_URL",
            "TRUTHTABLE_OPENROUTER_BASE_URL",
        ):
            self.assertRegex(LOAD, rf"(?m)^      {url_key}: http://mock-upstream:8090$")

    def test_the_proxy_reads_the_load_policy(self) -> None:
        self.assertIn("./scripts/load/policies.yaml:/etc/trustagent-load/policies.yaml:ro", LOAD)
        self.assertIn("TRUTHTABLE_POLICY_FILE: /etc/trustagent-load/policies.yaml", LOAD)
        self.assertIn('TRUTHTABLE_TEST_MODE: "true"', LOAD, "scenario f needs test mode")

    def test_the_slow_pass_points_the_fast_check_at_mock_jev_slow(self) -> None:
        self.assertIn('"-jev-latency", "150ms"', LOAD)
        self.assertRegex(SLOW, r"(?m)^      TRUTHTABLE_JEV_BASE_URL: http://mock-jev-slow:8090$")


class LoadPolicyTest(unittest.TestCase):
    def test_one_route_per_scenario(self) -> None:
        routes = policy_routes()
        expected = {
            "load-a": "flag",
            "load-b": "observe",
            "load-c": "flag",
            "load-d": "flag",
            "load-e": "block",
            "load-f": "flag",
        }
        self.assertEqual(routes, expected)
        for scenario, spec in k6_scenarios().items():
            self.assertIn(spec["route"], routes, scenario)

    def test_block_route_buffers_streams_and_f_hedges_inside_its_deadline(self) -> None:
        block_e = re.search(r"(?ms)^  - name: load-e\n(.*?)(?=^  - name:|\Z)", POLICY)
        assert block_e is not None
        self.assertIn("streaming: buffer", block_e.group(1))
        block_f = re.search(r"(?ms)^  - name: load-f\n(.*?)(?=^  - name:|\Z)", POLICY)
        assert block_f is not None
        deadline = int(re.search(r"deadline_ms: (\d+)", block_f.group(1)).group(1))  # type: ignore[union-attr]
        hedge = int(re.search(r"hedge_after_ms: (\d+)", block_f.group(1)).group(1))  # type: ignore[union-attr]
        self.assertEqual((deadline, hedge), (1500, 400))
        self.assertIn("p(99)<=1500", K6, "scenario f's target is the route deadline")

    def test_the_policy_is_valid(self) -> None:
        try:
            import yaml  # type: ignore[import-untyped]

            import check_recording  # noqa: F401  (needs jsonschema)
            import jsonschema  # noqa: F401
        except ImportError as exc:  # pragma: no cover - depends on the environment
            self.skipTest(f"needs PyYAML and jsonschema ({exc})")
        doc: Any = yaml.safe_load(POLICY)
        errors: List[str] = list(check_recording.Schemas().errors(doc, "policy.v1.schema.json"))
        self.assertEqual(errors, [])
        self.assertFalse(doc["defaults"]["deep_audit"]["enabled"], "the load test skips the engine")


class LoadK6Test(unittest.TestCase):
    def test_every_scenario_and_its_target(self) -> None:
        scenarios = k6_scenarios()
        self.assertEqual(sorted(scenarios), list(SCENARIOS))
        targets = {name: spec["threshold"] for name, spec in scenarios.items()}
        self.assertEqual(
            targets,
            {
                "a": "p(95)<3",
                "b": "p(95)<5",
                "c": "p(95)<10",
                "d": "p(95)<175",
                "e": "null",
                "f": "p(99)<=1500",
            },
        )
        self.assertEqual(scenarios["e"]["stream"], "true")
        # docs/GATEWAY.md §13 states the same targets.
        for row in ("< 3 ms", "< 5 ms", "< 10 ms", "< 175 ms", "reported only", "0% 5xx"):
            self.assertIn(row, GATEWAY_DOC)

    def test_the_overhead_comes_from_the_gateway_header(self) -> None:
        self.assertIn("res.headers['X-Trustagent-Overhead-Ms']", K6)
        self.assertIn("new Trend('gateway_overhead_ms', true)", K6)
        self.assertIn("'fault:hang@1'", K6)

    def test_the_make_target_runs_every_scenario(self) -> None:
        passes = re.findall(r"\$\(call k6_run,([a-z$()]+)\)", LOAD_MK)
        ran = "".join(p.replace("$(comma)", "") for p in passes)
        self.assertEqual(sorted(ran), list(SCENARIOS))
        self.assertRegex(LOAD_MK, r"(?m)^load-test-gate: load-up ##")
        recipe = LOAD_MK.split("load-test-gate: load-up", 1)[1].split("\nload-down:", 1)[0]
        self.assertNotIn("$(MAKE)", recipe, "make -n would run such a recipe line for real")
        self.assertIn("down --volumes", recipe)

    def test_the_proxy_key_never_reaches_a_command_line(self) -> None:
        # `docker run -e API_KEY=<key>` would show the key to every local user (ps); docker
        # takes it from its own environment when -e names the variable alone.
        k6_run = LOAD_MK.split("k6_run =", 1)[1].split("\n\n", 1)[0]
        self.assertRegex(k6_run, r"-e API_KEY(\s|\\)")
        self.assertNotRegex(k6_run, r"-e API_KEY=")
        self.assertRegex(
            k6_run, r'^ API_KEY="\$\$\(\$\(PYTHON\) scripts/envfile\.py get TRAFFICGEN_API_KEY\)"'
        )


if __name__ == "__main__":
    unittest.main()
