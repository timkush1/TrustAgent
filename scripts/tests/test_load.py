"""The load test: the compose overrides, the load policy, the k6 script and the make target agree
with each other and with the key registry (docs/GATEWAY.md §13); the k6 script's helpers run under
node; scripts/load/report.py turns canned k6 output into a run file that validates against
load-run.v1, refuses every run D118 forbids, and writes the GATEWAY.md block.

Standard library, plus node for the k6 helpers and jsonschema for the run file (CI installs it);
the load policy's schema check needs PyYAML and is skipped without it.
"""

from __future__ import annotations

import base64
import io
import json
import os
import re
import shutil
import subprocess  # nosec B404 - node with a fixed argv, in tests
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))

import test_compose  # noqa: E402

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "load"))
import report  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
LOAD = (ROOT / "docker-compose.load.yml").read_text(encoding="utf-8")
SLOW = (ROOT / "docker-compose.load-slow-jev.yml").read_text(encoding="utf-8")
POLICY_PATH = ROOT / "scripts" / "load" / "policies.yaml"
POLICY = POLICY_PATH.read_text(encoding="utf-8")
K6 = (ROOT / "scripts" / "load" / "gateway-overhead.js").read_text(encoding="utf-8")
K6_LIB = ROOT / "scripts" / "load" / "overhead-lib.js"
LOAD_MK = (ROOT / "mk" / "load.mk").read_text(encoding="utf-8")
GATEWAY_DOC = (ROOT / "docs" / "GATEWAY.md").read_text(encoding="utf-8")

SCENARIOS = "abcdefg"


def k6_scenarios() -> Dict[str, Dict[str, str]]:
    """The SCENARIOS table of the k6 script: {id: {route, stream, mode, what, threshold}}."""
    found: Dict[str, Dict[str, str]] = {}
    for match in re.finditer(
        r"^\s+([a-g]): \{ route: '([a-z-]+)', stream: (true|false), mode: '([a-z]+)', what: '([^']*)', "
        r"threshold: (null|'[^']*')",
        K6,
        re.MULTILINE,
    ):
        found[match.group(1)] = {
            "route": match.group(2),
            "stream": match.group(3),
            "mode": match.group(4),
            "what": match.group(5),
            "threshold": match.group(6).strip("'"),
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
            "load-g": "block",
        }
        self.assertEqual(routes, expected)
        for scenario, spec in k6_scenarios().items():
            self.assertIn(spec["route"], routes, scenario)
            # The run file's mode: the route's, except (a), whose gate has nothing to ask.
            want = "disabled" if scenario == "a" else routes[spec["route"]]
            self.assertEqual(spec["mode"], want, scenario)

    def test_scenario_g_blocks_json_answers(self) -> None:
        block_g = re.search(r"(?ms)^  - name: load-g\n(.*?)(?=^  - name:|\Z)", POLICY)
        assert block_g is not None
        self.assertIn("mode: block", block_g.group(1))
        self.assertNotIn("streaming:", block_g.group(1), "g sends JSON answers; nothing to buffer")
        self.assertEqual(k6_scenarios()["g"]["stream"], "false")

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
                "g": "p(95)<10",
            },
        )
        # report.py describes each scenario as the k6 script does.
        self.assertEqual({k: v["what"] for k, v in scenarios.items()}, report.SCENARIO_WHAT)
        self.assertEqual(scenarios["e"]["stream"], "true")
        # docs/GATEWAY.md §13 states the same targets.
        for row in ("< 3 ms", "< 5 ms", "< 10 ms", "< 175 ms", "reported only", "0% 5xx"):
            self.assertIn(row, GATEWAY_DOC)

    def test_the_overhead_comes_from_the_gateway_header(self) -> None:
        self.assertIn("res.headers['X-Trustagent-Overhead-Ms']", K6)
        self.assertIn("new Trend('gateway_overhead_ms', true)", K6)
        self.assertIn("'fault:hang@1'", K6)

    def test_the_make_target_runs_every_scenario(self) -> None:
        # One k6 run per scenario, so the counters read around a run are that scenario's.
        pass1 = re.search(r"(?m)^LOAD_PASS1\s+\?= (.+)$", LOAD_MK)
        pass2 = re.search(r"(?m)^LOAD_PASS2\s+\?= (.+)$", LOAD_MK)
        assert pass1 and pass2
        ran = pass1.group(1).split() + pass2.group(1).split()
        self.assertEqual(sorted(ran), list(SCENARIOS))
        self.assertEqual(pass2.group(1).split(), ["d"], "d runs on the slow fast check")
        self.assertIn("for s in $(LOAD_PASS1); do", LOAD_MK)
        self.assertIn("$(call k6_run,$$s,http://mock-upstream:8090)", LOAD_MK)
        self.assertIn("$(call k6_run,$$s,http://mock-jev-slow:8090)", LOAD_MK)
        for n in ("1", "2"):
            self.assertIn(f"| tee -a $(LOAD_OUT)/pass{n}.log", LOAD_MK)
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

    def test_the_k6_image_is_pinned_and_the_script_mounted(self) -> None:
        image = re.search(r"(?m)^K6_IMAGE\s+\?= (\S+)$", LOAD_MK)
        assert image is not None
        self.assertRegex(image.group(1), r"^grafana/k6:\d+\.\d+\.\d+@sha256:[0-9a-f]{64}$")
        # The script imports its helpers, so k6 reads it from the mounted directory, not stdin.
        k6_run = LOAD_MK.split("k6_run =", 1)[1].split("\n\n", 1)[0]
        self.assertIn('-v "$(CURDIR)/scripts/load:/scripts/load:ro"', k6_run)
        self.assertIn("run /scripts/load/gateway-overhead.js", k6_run)
        # (K6ScriptRunTest runs the script itself, so a helper it uses but does not import fails.)
        self.assertRegex(K6, r"(?m)^import \{[^}]*\} from './overhead-lib\.js';$")


class K6ResultLineTest(unittest.TestCase):
    """What the k6 script reads around a run and prints at its end."""

    def test_the_result_line_is_the_one_report_py_reads(self) -> None:
        self.assertIn(f"export const RESULT_PREFIX = '{report.RESULT_PREFIX}';", K6)
        self.assertIn("lines.push(RESULT_PREFIX + JSON.stringify(result), '');", K6)
        for field in ("rate", "duration", "metrics_read", "scenarios", "jev", "detectors"):
            self.assertRegex(K6, rf"const result = \{{[^}}]*\b{field}\b")

    def test_the_metrics_read(self) -> None:
        # The proxy's metrics port (TRUTHTABLE_METRICS_PORT) inside its network namespace, and the
        # gateway metrics the teardown reads, which internal/metrics defines.
        self.assertIn("'http://127.0.0.1:8002/metrics'", K6)
        keys = json.loads(
            (ROOT / "contracts" / "config" / "keys.v1.json").read_text(encoding="utf-8")
        )
        port = next(k for k in keys["keys"] if k["name"] == "TRUTHTABLE_METRICS_PORT")
        self.assertEqual(port["default"], "8002")
        metrics_go = "".join(
            p.read_text(encoding="utf-8")
            for p in (ROOT / "backend-go" / "internal" / "metrics").glob("*.go")
        )
        for name, go_name in (
            ("trustagent_gateway_overhead_seconds", "overhead_seconds"),
            ("trustagent_gateway_gate_seconds", "gate_seconds"),
            ("trustagent_gateway_jev_requests_total", "jev_requests_total"),
        ):
            self.assertIn(f"'{name}'", K6)
            self.assertIn(f'Name: "{go_name}"', metrics_go)
        self.assertIn("{ stage: 'detectors' }", K6)
        self.assertIn("{ outcome: 'hedge_lost' }", K6)
        self.assertIn("/_mock/requests", K6)

    def test_scenario_f_hangs_the_first_attempt(self) -> None:
        self.assertIn("headers['X-TrustAgent-Scenario'] = 'fault:hang@1';", K6)
        self.assertIn("Math.random() < HANG_RATE", K6)


def node_eval(expr: str) -> Any:
    """Evaluates expr with the k6 helpers in scope (as `lib`) under node, and returns its JSON."""
    node = shutil.which("node")
    if node is None:
        raise AssertionError("the k6 helper tests need node (Node 22 in development and CI)")
    src = base64.b64encode(K6_LIB.read_bytes()).decode()
    program = (
        f"const lib = await import('data:text/javascript;base64,{src}');"
        f"process.stdout.write(JSON.stringify({expr}));"
    )
    out = subprocess.run(  # nosec B603 - node with a fixed argv
        [node, "--input-type=module", "-e", program],
        check=True,
        capture_output=True,
        text=True,
        timeout=60,
    )
    return json.loads(out.stdout)


class K6HelpersTest(unittest.TestCase):
    """scripts/load/overhead-lib.js under node."""

    METRICS = "\n".join(
        [
            "# HELP trustagent_gateway_jev_requests_total Decision-model attempts.",
            'trustagent_gateway_jev_requests_total{backend="compatible",outcome="ok"} 600',
            'trustagent_gateway_jev_requests_total{backend="compatible",outcome="hedge_lost"} 90',
            'trustagent_gateway_overhead_seconds_bucket{mode="observe",route="load-b",le="0.001"} 300',
            'trustagent_gateway_overhead_seconds_bucket{mode="observe",route="load-b",le="0.0025"} 540',
            'trustagent_gateway_overhead_seconds_bucket{mode="observe",route="load-b",le="0.005"} 600',
            'trustagent_gateway_overhead_seconds_bucket{mode="observe",route="load-b",le="+Inf"} 600',
        ]
    )

    def test_the_parser_and_sums(self) -> None:
        got = node_eval(
            f"(() => {{ const s = lib.parseMetrics({json.dumps(self.METRICS)}); return ["
            "s.length, lib.sumOf(s, 'trustagent_gateway_jev_requests_total'),"
            "lib.sumOf(s, 'trustagent_gateway_jev_requests_total', {outcome: 'hedge_lost'}),"
            "lib.buckets(s, 'trustagent_gateway_overhead_seconds', {route: 'load-b'})]; })()"
        )
        self.assertEqual(got[:3], [6, 690, 90])
        self.assertEqual(got[3], {"0.001": 300, "0.0025": 540, "0.005": 600, "Infinity": 600})

    def test_histogram_percentiles_between_two_readings(self) -> None:
        # 600 observations between the readings: 300 under 1 ms, 240 in (1, 2.5], 60 in (2.5, 5].
        got = node_eval(
            "lib.histogramStats({0.001: 100, 0.0025: 100, 0.005: 100, Infinity: 100},"
            "{0.001: 400, 0.0025: 640, 0.005: 700, Infinity: 700})"
        )
        self.assertEqual(got["n"], 600)
        self.assertAlmostEqual(got["p50"], 1.0)  # the 300th of 600 ends the first bucket
        self.assertAlmostEqual(got["p95"], 2.5 + 2.5 * (570 - 540) / 60)
        self.assertAlmostEqual(got["max"], 5.0)
        self.assertEqual(node_eval("lib.histogramStats({0.001: 5}, {0.001: 5})")["n"], 0)
        # Everything past the last finite bucket: the percentile is that bucket's bound, max unknown.
        tail = node_eval("lib.histogramStats({0.001: 0, Infinity: 0}, {0.001: 0, Infinity: 10})")
        self.assertEqual((tail["p95"], tail["max"]), (1.0, None))

    def test_targets_and_decision_requests(self) -> None:
        self.assertEqual(
            node_eval("lib.target('p(95)<10')"),
            {"stat": "p95_ms", "max_ms": 10, "inclusive": False},
        )
        self.assertEqual(
            node_eval("lib.target('p(99)<=1500')"),
            {"stat": "p99_ms", "max_ms": 1500, "inclusive": True},
        )
        self.assertIsNone(node_eval("lib.target(null)"))
        totals = {
            "POST /v1/systemone": 5,
            "POST /api/v1/systemone": 2,
            "POST /v1/chat/completions": 9,
            "GET /api/v1/key": 1,
        }
        self.assertEqual(node_eval(f"lib.decisionRequests({json.dumps(totals)})"), 7)


# The k6 modules the script imports, as node stubs: http answers from the test's canned readings
# and records the posts, metrics record what is added, execution names the running scenario.
K6_STUBS = {
    "k6-http.mjs": """
export default {
  get(url) { return globalThis.__k6.get(url); },
  post(url, body, params) {
    globalThis.__k6.posts.push({ url, body: JSON.parse(body), headers: params.headers });
    return globalThis.__k6.post();
  },
};
""",
    "k6.mjs": "export function check(res, checks) { for (const f of Object.values(checks)) f(res); return true; }\n",
    "k6-execution.mjs": "export default { get scenario() { return { name: globalThis.__k6.scenario }; } };\n",
    "k6-metrics.mjs": """
class Metric {
  constructor(name) { this.name = name; globalThis.__k6.metrics[name] = globalThis.__k6.metrics[name] || []; }
  add(value) { globalThis.__k6.metrics[this.name].push(value); }
}
export class Counter extends Metric {}
export class Gauge extends Metric {}
export class Rate extends Metric {}
export class Trend extends Metric {}
""",
}

# The script's run harness: setup() reads the "before" readings, one answer goes through gate(),
# teardown() reads the "after" readings, and handleSummary() gets k6's summary of the answers
# (canned, per scenario) plus the gauges teardown set.
K6_DRIVER = """
const cfg = JSON.parse(process.env.K6_TEST_CONFIG);
let phase = 'before';
globalThis.__ENV = cfg.env;
globalThis.__k6 = {
  metrics: {}, posts: [], scenario: cfg.env.SCENARIOS,
  get(url) {
    if (url.endsWith('/metrics')) return { status: 200, body: cfg.metrics[phase] };
    if (url.endsWith('/_mock/requests')) return { status: 200, body: JSON.stringify({ requests: [], totals: cfg.mock[phase] }) };
    return { status: 404, body: '' };
  },
  post() { return { status: 200, headers: cfg.response_headers, timings: { duration: 7.5 } }; },
};
const k6 = await import('./gateway-overhead.mjs');
const before = k6.setup();
k6.gate();
phase = 'after';
k6.teardown(before);
const metrics = { ...cfg.summary };
for (const [name, values] of Object.entries(globalThis.__k6.metrics)) {
  if (name.startsWith('load_') && values.length) metrics[name] = { values: { value: values[values.length - 1] } };
}
const out = k6.handleSummary({ metrics });
process.stdout.write(JSON.stringify({ stdout: out.stdout, posts: globalThis.__k6.posts }));
"""


def run_k6_script(config: Dict[str, Any]) -> Dict[str, Any]:
    """Runs scripts/load/gateway-overhead.js's lifecycle under node with the k6 stubs."""
    node = shutil.which("node")
    if node is None:
        raise AssertionError("the k6 script test needs node (Node 22 in development and CI)")
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        script = K6
        for module, stub in (
            ("k6/http", "k6-http.mjs"),
            ("k6/execution", "k6-execution.mjs"),
            ("k6/metrics", "k6-metrics.mjs"),
            ("k6", "k6.mjs"),
        ):
            script = script.replace(f"from '{module}';", f"from './{stub}';")
        (d / "gateway-overhead.mjs").write_text(script, encoding="utf-8")
        shutil.copy(K6_LIB, d / "overhead-lib.js")
        (d / "package.json").write_text('{"type": "module"}', encoding="utf-8")
        for name, text in K6_STUBS.items():
            (d / name).write_text(text, encoding="utf-8")
        (d / "driver.mjs").write_text(K6_DRIVER, encoding="utf-8")
        out = subprocess.run(  # nosec B603 - node with a fixed argv, in tests
            [node, str(d / "driver.mjs")],
            check=False,
            capture_output=True,
            text=True,
            timeout=60,
            cwd=d,
            env={**os.environ, "K6_TEST_CONFIG": json.dumps(config)},
        )
    if out.returncode != 0:
        raise AssertionError(f"the k6 script failed under node:\n{out.stderr}")
    result: Dict[str, Any] = json.loads(out.stdout)
    return result


def metrics_text(overhead_b: Sequence[int], jev_ok: int, hedge_lost: int, detectors: int) -> str:
    """A /metrics reading: load-b's overhead buckets (le 1, 2.5, 5 ms, +Inf), the fast-check
    attempts and Tier 0's histogram."""
    les = ("0.001", "0.0025", "0.005", "+Inf")
    lines = [
        "# HELP trustagent_gateway_overhead_seconds Gateway overhead.",
        f'trustagent_gateway_jev_requests_total{{backend="compatible",outcome="ok"}} {jev_ok}',
        f'trustagent_gateway_jev_requests_total{{backend="compatible",outcome="hedge_lost"}} {hedge_lost}',
    ]
    lines += [
        f'trustagent_gateway_overhead_seconds_bucket{{mode="observe",route="load-b",le="{le}"}} {n}'
        for le, n in zip(les, overhead_b)
    ]
    lines += [
        f'trustagent_gateway_gate_seconds_bucket{{stage="detectors",le="{le}"}} {detectors}'
        for le in ("0.0005", "0.001", "+Inf")
    ]
    return "\n".join(lines) + "\n"


class K6ScriptRunTest(unittest.TestCase):
    """The k6 script's own code path (setup, gate, teardown, handleSummary) under node, with the k6
    modules stubbed: every name it uses resolves, and its result line is one report.py accepts."""

    def config(
        self, scenario: str, headers: Dict[str, str], summary: Dict[str, Any]
    ) -> Dict[str, Any]:
        return {
            "env": {
                "SCENARIOS": scenario,
                "API_KEY": "sk-test-load",
                "RATE": "20",
                "DURATION": "30s",
            },
            "metrics": {
                "before": metrics_text((10, 10, 10, 10), jev_ok=100, hedge_lost=5, detectors=10),
                "after": metrics_text(
                    (310, 550, 610, 610), jev_ok=700, hedge_lost=5, detectors=610
                ),
            },
            "mock": {
                "before": {"POST /v1/systemone": 100, "POST /v1/chat/completions": 100},
                "after": {"POST /v1/systemone": 700, "POST /v1/chat/completions": 700},
            },
            "response_headers": headers,
            "summary": summary,
        }

    def test_observe_reads_the_gateway_histogram(self) -> None:
        summary = {
            "gateway_overhead_ms{scenario:b}": {
                "values": {"count": 600, "med": 9.0, "p(95)": 12.0, "p(99)": 14.0, "max": 20.0}
            },
            "gateway_overhead_without_header{scenario:b}": {"values": {"count": 600}},
            "gateway_requests{scenario:b}": {"values": {"count": 600}},
            "gateway_non_200{scenario:b}": {"values": {"count": 0}},
            "gateway_5xx{scenario:b}": {"values": {"rate": 0}},
        }
        got = run_k6_script(self.config("b", {"X-Trustagent-Route": "load-b@1"}, summary))
        post = got["posts"][0]
        self.assertTrue(post["url"].endswith("/v1/chat/completions"))
        self.assertEqual(post["headers"]["X-TrustAgent-Route"], "load-b")
        self.assertEqual(post["headers"]["X-TrustAgent-Key"], "sk-test-load")
        self.assertIs(post["body"]["stream"], False)

        results = report.parse_result_lines(got["stdout"])
        self.assertEqual(len(results), 1)
        line = results[0]
        self.assertTrue(line["metrics_read"])
        # Deltas between the two readings: 600 attempts, no hedge, 600 decision requests.
        self.assertEqual(line["jev"], {"attempts": 600, "hedged": 0, "mock_requests": 600})
        self.assertEqual(line["detectors"]["n"], 600)
        self.assertAlmostEqual(line["detectors"]["p50_ms"], 0.25)
        b = line["scenarios"][0]
        # Observe answers carry no overhead header: the figures are the gateway's histogram of the
        # route (n 600; p50 1 ms, p95 3.75 ms, p99 4.75 ms interpolated; max the 5 ms bucket).
        self.assertEqual((b["id"], b["source"], b["n"]), ("b", "metrics", 600))
        self.assertAlmostEqual(b["p50_ms"], 1.0)
        self.assertAlmostEqual(b["p95_ms"], 3.75)
        self.assertAlmostEqual(b["p99_ms"], 4.75)
        self.assertAlmostEqual(b["max_ms"], 5.0)
        self.assertTrue(b["ok"])
        scenarios, _ = report.merge_results(results, report_only=False)
        self.assertEqual(scenarios[0]["jev"], {"attempts": 600, "hedged": 0, "mock_requests": 600})

    def test_a_flag_answer_reports_its_overhead_header(self) -> None:
        summary = {
            "gateway_overhead_ms{scenario:c}": {
                "values": {"count": 600, "med": 1.0, "p(95)": 2.0, "p(99)": 3.0, "max": 6.0}
            },
            "gateway_overhead_without_header{scenario:c}": {"values": {"count": 0}},
            "gateway_requests{scenario:c}": {"values": {"count": 600}},
        }
        headers = {"X-Trustagent-Route": "load-c@1", "X-Trustagent-Overhead-Ms": "2"}
        got = run_k6_script(self.config("c", headers, summary))
        c = report.parse_result_lines(got["stdout"])[0]["scenarios"][0]
        self.assertEqual((c["source"], c["n"], c["p95_ms"]), ("header", 600, 2.0))
        self.assertEqual(c["target"], {"stat": "p95_ms", "max_ms": 10})
        self.assertTrue(c["ok"])
        self.assertIn("(c) flag, mock Jev 0 ms", got["stdout"])


# ------------------------------------------------------------------------------------- report.py


def scenario_result(sid: str, **over: Any) -> Dict[str, Any]:
    """One scenario as the k6 result line states it."""
    spec = {
        "a": ("load-a", "disabled", False, {"stat": "p95_ms", "max_ms": 3}),
        "b": ("load-b", "observe", False, {"stat": "p95_ms", "max_ms": 5}),
        "c": ("load-c", "flag", False, {"stat": "p95_ms", "max_ms": 10}),
        "d": ("load-d", "flag", False, {"stat": "p95_ms", "max_ms": 175}),
        "e": ("load-e", "block", True, None),
        "f": ("load-f", "flag", False, {"stat": "p99_ms", "max_ms": 1500}),
        "g": ("load-g", "block", False, {"stat": "p95_ms", "max_ms": 10}),
    }[sid]
    out = {
        "id": sid, "route": spec[0], "mode": spec[1], "streaming": spec[2], "n": 600,
        "p50_ms": 1.2, "p95_ms": 2.4, "p99_ms": 3.9, "max_ms": 12.5,
        "source": "metrics" if sid == "b" else "header", "target": spec[3], "ok": True,
        "report_only": spec[3] is None, "without_header": 600 if sid == "b" else 0,
        "requests": 600, "non_200": 0, "rate_5xx": 0,
    }  # fmt: skip
    out.update(over)
    return out


def result_line(
    *scenarios: Dict[str, Any], jev: Optional[Dict[str, Any]] = None, rate: int = 20
) -> str:
    obj = {
        "rate": rate, "duration": "30s", "hang_rate": 0.15, "metrics_read": True, "scenarios": list(scenarios),
        "jev": jev if jev is not None else {"attempts": 600, "hedged": 0, "mock_requests": 600},
        "detectors": {"n": 600, "p50_ms": 0.1, "p95_ms": 0.3},
    }  # fmt: skip
    return report.RESULT_PREFIX + json.dumps(obj)


def k6_log(*lines: str) -> str:
    """A pass log as the make recipe tees it: k6's banner and table around each result line."""
    noise = [
        "          /\\      Grafana   /‾‾/  ",
        "     execution: local",
        "  (c) flag, mock Jev 0 ms                       med 1.2 ms  p95 2.4 ms  p99 3.9 ms  [header, n 600]  ok (p(95)<10)",
        'time="2026-10-10T12:00:00Z" level=info msg="teardown done"',
    ]
    return "\n".join(noise + [ln for line in lines for ln in (line, noise[2])]) + "\n"


COMPOSE_CONFIG = {
    "services": {
        "proxy": {
            "deploy": {"resources": {"limits": {"cpus": 1, "memory": "536870912"}}},
            "environment": {"TRUTHTABLE_TEST_MODE": "true"},
        }
    }
}


class FakeRunner:
    """git, docker and compose answers for report.py, without running them."""

    def __init__(
        self, dirty: str = "", docker: str = "28.0.1\n", config: Optional[Dict[str, Any]] = None
    ) -> None:
        self.dirty, self.docker = dirty, docker
        self.config = COMPOSE_CONFIG if config is None else config
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str]) -> str:
        self.calls.append(list(argv))
        if argv[:2] == ["git", "rev-parse"]:
            return "abc1234\n"
        if argv[:2] == ["git", "status"]:
            return self.dirty
        if argv[:2] == ["docker", "version"]:
            return self.docker
        if list(argv[-3:]) == ["config", "--format", "json"]:
            return json.dumps(self.config)
        raise AssertionError(f"unexpected command {argv}")


HOST_FILES = {
    "/proc/cpuinfo": "processor\t: 0\nmodel name\t: Example CPU @ 3.00GHz\n",
    "/proc/meminfo": "MemTotal:        8048376 kB\n",
    "/etc/os-release": 'NAME="Ubuntu"\nPRETTY_NAME="Ubuntu 24.04.1 LTS"\n',
}

COMPOSE = "docker compose -p truthtable-load -f docker-compose.yml -f docker-compose.load.yml --profile load"
K6_IMAGE = "grafana/k6:2.3.0@sha256:" + "9c" * 32
FIXED_NOW = __import__("datetime").datetime(
    2026, 10, 10, 12, 0, 0, tzinfo=__import__("datetime").timezone.utc
)


def build(logs: Sequence[str], runner: Optional[FakeRunner] = None, **kw: Any) -> Dict[str, Any]:
    return report.build_run(
        logs, run=runner or FakeRunner(), compose=kw.pop("compose", COMPOSE), k6_image=kw.pop("k6_image", K6_IMAGE),
        now=FIXED_NOW, files=HOST_FILES.get, **kw,
    )  # fmt: skip


def full_logs() -> List[str]:
    pass1 = k6_log(*(result_line(scenario_result(s)) for s in "abcefg"))
    pass2 = k6_log(
        result_line(scenario_result("d", p50_ms=151.0, p95_ms=158.0, p99_ms=163.0, max_ms=181.0))
    )
    return [pass1, pass2]


class ReportPublishTest(unittest.TestCase):
    def test_the_parser_on_canned_k6_output(self) -> None:
        results = report.parse_result_lines(full_logs()[0])
        self.assertEqual([r["scenarios"][0]["id"] for r in results], list("abcefg"))
        # A prefixed line (k6's own log format) still parses.
        prefixed = 'time="x" level=info msg="" ' + result_line(scenario_result("c"))
        self.assertEqual(len(report.parse_result_lines(prefixed)), 1)
        with self.assertRaisesRegex(report.Refusal, "not JSON"):
            report.parse_result_lines(report.RESULT_PREFIX + "{broken")

    def test_a_run_file_validates_against_load_run_v1(self) -> None:
        import check_recording

        doc = build(full_logs())
        errors = list(check_recording.Schemas().errors(doc, "load-run.v1.schema.json"))
        self.assertEqual(errors, [])
        self.assertEqual([s["id"] for s in doc["scenarios"]], list(SCENARIOS))
        self.assertEqual(doc["run_id"], "20261010T120000Z-abc1234")
        self.assertEqual(
            (doc["git_sha"], doc["dirty"], doc["vendors"]),
            ("abc1234", False, "simulated (mock-upstream)"),
        )
        env = doc["environment"]
        self.assertEqual(env["cpu_model"], "Example CPU @ 3.00GHz")
        self.assertEqual((env["mem_gb"], env["docker"]), (7.7, "28.0.1"))
        self.assertTrue(env["os"].startswith("Ubuntu 24.04.1 LTS"), env["os"])
        self.assertEqual(env["proxy_limits"], {"cpus": 1.0, "mem": "512m"})
        self.assertEqual(
            (env["trace_flush"], env["k6_image"], env["rate"], env["duration"]),
            ("async", K6_IMAGE, 20, "30s"),
        )
        self.assertEqual(doc["errors"], {"rate_5xx": 0.0, "non_200": 0})
        b = next(s for s in doc["scenarios"] if s["id"] == "b")
        self.assertEqual((b["source"], b["mode"]), ("metrics", "observe"))
        e = next(s for s in doc["scenarios"] if s["id"] == "e")
        self.assertEqual((e["target"], e["report_only"], e["ok"]), (None, True, True))
        # The example the contracts ship validates too (kept in step with the schema).
        example = json.loads(
            (ROOT / "contracts" / "fixtures" / "load" / "load-run.example.json").read_text(
                encoding="utf-8"
            )
        )
        self.assertEqual(
            list(check_recording.Schemas().errors(example, "load-run.v1.schema.json")), []
        )

    def test_every_refusal(self) -> None:
        logs = full_logs()
        pass1 = lambda *s: [k6_log(*(result_line(x) for x in s)), logs[1]]  # noqa: E731
        cases = [
            ("uncommitted tracked changes", lambda: build(logs, FakeRunner(dirty=" M VERSION\n"))),
            ("fewer than 500", lambda: build(pass1(scenario_result("c", n=499)))),
            ("environment is incomplete: docker", lambda: build(logs, FakeRunner(docker=""))),
            ("environment is incomplete: k6_image", lambda: build(logs, k6_image=None)),
            ("compose command", lambda: build(logs, compose=None)),
            ("no proxy service", lambda: build(logs, FakeRunner(config={"services": {}}))),
            ("missed its target", lambda: build(pass1(scenario_result("c", ok=False, p95_ms=12.0)))),
            ("without X-TrustAgent-Overhead-Ms", lambda: build(pass1(scenario_result("c", without_header=3, source="total")))),
            ("several scenarios", lambda: build([k6_log(result_line(scenario_result("a"), scenario_result("c")))])),
            ("appears in two result lines", lambda: build([k6_log(result_line(scenario_result("c")), result_line(scenario_result("c")))])),
            ("jev counters missing", lambda: build([report.RESULT_PREFIX + json.dumps({"rate": 20, "duration": "30s", "scenarios": [scenario_result("c")], "jev": None})])),
            # /metrics was read but the mock's totals were not (k6 reports the count as null).
            ("counters mock_requests could not be read", lambda: build([k6_log(result_line(scenario_result("c"), jev={"attempts": 600, "hedged": 0, "mock_requests": None}))])),
            ("different rates", lambda: build([k6_log(result_line(scenario_result("a")), result_line(scenario_result("c"), rate=50))])),
            ("no TRUSTAGENT_LOAD_RESULT", lambda: build(["k6 crashed\n"])),
            ("p95_ms is missing", lambda: build(pass1(scenario_result("c", p95_ms=None)))),
        ]  # fmt: skip
        for want, call in cases:
            with self.subTest(want=want):
                with self.assertRaisesRegex(report.Refusal, re.escape(want)):
                    call()

    def test_report_only_keeps_a_missed_target_marked(self) -> None:
        logs = [k6_log(result_line(scenario_result("c", ok=False, p95_ms=12.0)))]
        doc = build(logs, report_only=True)
        c = doc["scenarios"][0]
        self.assertEqual((c["ok"], c["report_only"]), (False, True))
        self.assertFalse(report.is_clean(doc))

    def test_the_cli_writes_the_run_and_refuses_without_writing(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out_dir = Path(tmp) / "runs"
            logs = []
            for i, text in enumerate(full_logs()):
                path = Path(tmp) / f"pass{i + 1}.log"
                path.write_text(text, encoding="utf-8")
                logs.append(str(path))
            argv = [
                "--out-dir",
                str(out_dir),
                "--k6-image",
                K6_IMAGE,
                "--compose",
                COMPOSE,
                "--run-id",
                "test-run",
                *logs,
            ]
            out, err = io.StringIO(), io.StringIO()
            orig = report.read_text
            report.read_text = HOST_FILES.get  # type: ignore[assignment]
            try:
                code = report.main(argv, run=FakeRunner(), out=out, err=err)
                self.assertEqual(code, 0, err.getvalue())
                self.assertTrue((out_dir / "test-run.json").is_file())
                # The same run id again is refused, and a dirty tree writes nothing.
                self.assertEqual(report.main(argv, run=FakeRunner(), out=out, err=err), 2)
                argv[argv.index("test-run")] = "dirty-run"
                self.assertEqual(
                    report.main(argv, run=FakeRunner(dirty=" M x\n"), out=out, err=err), 2
                )
                self.assertFalse((out_dir / "dirty-run.json").exists())
                self.assertIn("refused: the tree has uncommitted", err.getvalue())
            finally:
                report.read_text = orig  # type: ignore[assignment]


class ReportGatewayMdTest(unittest.TestCase):
    DOC = "# Gateway\n\n## 13. Load test\n\n<!-- load:start -->\nold\n<!-- load:end -->\n\nMore.\n"

    def write_runs(self, d: Path) -> None:
        older = build(full_logs(), run_id="older")
        older["created_at"] = "2026-10-01T00:00:00Z"
        newer = build(full_logs(), run_id="newer")
        newer["created_at"] = "2026-10-05T00:00:00Z"
        report_only = build(
            [k6_log(result_line(scenario_result("c", ok=False, p95_ms=12.0)))],
            report_only=True,
            run_id="missed",
        )
        report_only["created_at"] = "2026-10-09T00:00:00Z"  # newest, but not clean
        for doc in (older, newer, report_only):
            (d / f"{doc['run_id']}.json").write_text(json.dumps(doc), encoding="utf-8")

    def test_the_newest_clean_run_fills_the_block(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            self.write_runs(d)
            run = report.newest_clean_run(d)
            assert run is not None
            self.assertEqual(run["run_id"], "newer")
            block = report.gateway_block(run)
            self.assertIn("docs/benchmarks/load/newer.json", block)
            self.assertIn("**Measured** (k6, simulated vendors", block)
            self.assertIn(
                "| (c) flag, mock Jev 0 ms | overhead header | 600 | 1.2 ms | 2.4 ms | 3.9 ms | p95 < 10.0 ms | met |",
                block,
            )
            self.assertIn("| (b) observe: Tier 0 + async mock Jev | gateway histogram |", block)
            self.assertIn(
                "| (e) block, buffered streams (report only) | overhead header | 600 |", block
            )
            self.assertIn("reported only |", block)

            doc = d / "GATEWAY.md"
            doc.write_text(self.DOC, encoding="utf-8")
            out, err = io.StringIO(), io.StringIO()
            args = ["--gateway-md", "--runs-dir", str(d), "--doc", str(doc)]
            self.assertEqual(report.main([*args, "--check"], out=out, err=err), 1)
            self.assertEqual(report.main(args, out=out, err=err), 0)
            text = doc.read_text(encoding="utf-8")
            self.assertTrue(
                text.startswith(
                    "# Gateway\n\n## 13. Load test\n\n<!-- load:start -->\n<!-- generated"
                )
            )
            self.assertTrue(text.endswith("<!-- load:end -->\n\nMore.\n"))
            self.assertEqual(report.main([*args, "--check"], out=out, err=err), 0)

    def test_without_a_run_or_markers(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp)
            self.assertIsNone(report.newest_clean_run(d / "missing"))
            self.assertIn("No load run is published yet", report.gateway_block(None))
            doc = d / "GATEWAY.md"
            doc.write_text("# Gateway without markers\n", encoding="utf-8")
            err = io.StringIO()
            self.assertEqual(
                report.main(["--gateway-md", "--runs-dir", str(d), "--doc", str(doc)], err=err), 2
            )
            self.assertIn("no <!-- load:start -->", err.getvalue())


if __name__ == "__main__":
    unittest.main()
