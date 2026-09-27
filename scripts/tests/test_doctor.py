"""Unit tests for scripts/doctor.py with fake docker and HTTP (no Docker, no network)."""

from __future__ import annotations

import contextlib
import io
import json
import sys
import unittest
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import doctor  # noqa: E402
from doctor import Container, Doctor, HttpResult, describe_state, selected  # noqa: E402

PROXY = "http://127.0.0.1:8080"
DASH = "http://127.0.0.1:3000"
DEMO_PROXY_ENV = ["TRUTHTABLE_DEMO_MODE=true", "TRUTHTABLE_JEV_BACKEND=mock", "PATH=/bin"]
DEMO_ENGINE_ENV = ["LLM_PROVIDER=mock", "TRUTHTABLE_DEMO_MODE=true"]


def container(
    service: str,
    status: str = "running",
    health: Optional[str] = "healthy",
    exit_code: int = 0,
    env: Optional[List[str]] = None,
    oneoff: bool = False,
) -> Dict[str, Any]:
    state: Dict[str, Any] = {"Status": status, "ExitCode": exit_code}
    if health is not None:
        state["Health"] = {"Status": health}
    labels = {"com.docker.compose.service": service, "com.docker.compose.project": "p"}
    if oneoff:
        labels["com.docker.compose.oneoff"] = "True"
    return {
        "Name": f"/p-{service}-1",
        "State": state,
        "Config": {"Labels": labels, "Env": env or []},
    }


def demo_containers() -> List[Dict[str, Any]]:
    return [
        container("proxy", env=DEMO_PROXY_ENV),
        container("engine", env=DEMO_ENGINE_ENV),
        container("postgres"),
        container("dashboard"),
        container("trafficgen", health=None),
    ]


class FakeDocker:
    def __init__(
        self, inspect: List[Dict[str, Any]], healthcheck: Tuple[int, str, str] = (0, "", "")
    ):
        self.inspect = inspect
        self.healthcheck = healthcheck
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        if argv[:2] == ["docker", "ps"]:
            return 0, "\n".join(f"id{i}" for i in range(len(self.inspect))), ""
        if argv[:2] == ["docker", "inspect"]:
            return 0, json.dumps(self.inspect), ""
        if argv[:2] == ["docker", "exec"]:
            return self.healthcheck
        return 127, "", "unexpected command"


class FakeHttp:
    def __init__(self, routes: Mapping[str, Any]):
        # url -> HttpResult, or a list of HttpResults served in order (last one repeats)
        self.routes = dict(routes)
        self.calls: List[Tuple[str, Dict[str, str]]] = []

    def __call__(self, url: str, headers: Mapping[str, str]) -> HttpResult:
        self.calls.append((url, dict(headers)))
        route = self.routes.get(url)
        if route is None:
            return HttpResult(status=0, error="connection refused")
        if isinstance(route, list):
            route = route.pop(0) if len(route) > 1 else route[0]
        assert isinstance(route, HttpResult)
        return route


def ok_json(body: Any) -> HttpResult:
    return HttpResult(status=200, body=json.dumps(body).encode())


def traces(*audit_states: str) -> HttpResult:
    items = [
        {
            "trace_id": f"t{i}",
            "route": "support-bot",
            "action": "pass",
            "gate_status": "ok",
            "audit": {"status": state, "verdict": "GROUNDED" if state == "completed" else None},
        }
        for i, state in enumerate(audit_states)
    ]
    return ok_json({"items": items, "next_cursor": None})


def demo_routes(trace_result: Any) -> Dict[str, Any]:
    return {
        f"{PROXY}/health": ok_json(
            {"status": "ok", "version": "2.0.0", "demo": True, "audit_engine": True}
        ),
        f"{DASH}/healthz": HttpResult(status=200, body=b"ok\n"),
        f"{DASH}/config.json": ok_json({"apiBase": "", "wsUrl": ""}),
        f"{DASH}/health": ok_json({"status": "ok"}),
        f"{PROXY}/api/traces?limit=50": trace_result,
    }


class FakeClock:
    def __init__(self) -> None:
        self.now = 0.0
        self.sleeps: List[float] = []

    def clock(self) -> float:
        return self.now

    def sleep(self, seconds: float) -> None:
        self.sleeps.append(seconds)
        self.now += seconds


def make_doctor(docker: FakeDocker, http: FakeHttp, clock: FakeClock, **kwargs: Any) -> Doctor:
    options: Dict[str, Any] = dict(
        target="docker",
        project="truthtable-demo",
        proxy_url=PROXY,
        dashboard_url=DASH,
        expect="demo",
        api_key="tg-key",
        wait_s=30.0,
    )
    options.update(kwargs)
    return Doctor(run=docker, fetch=http, sleep=clock.sleep, clock=clock.clock, **options)


def run_all(d: Doctor) -> Dict[str, doctor.Check]:
    return {check_id: check() for check_id, check in d.checks()}


class DemoDoctorTest(unittest.TestCase):
    def test_healthy_demo_passes_every_check(self) -> None:
        http = FakeHttp(demo_routes(traces("queued", "completed", "not_assessed")))
        results = run_all(make_doctor(FakeDocker(demo_containers()), http, FakeClock()))
        self.assertEqual(
            list(results),
            [
                "compose.services",
                "proxy.live",
                "engine.health",
                "dashboard.live",
                "demo.mode",
                "traces.recent",
                "audits.completed",
            ],
        )
        for check_id, check in results.items():
            self.assertEqual(check.status, "PASS", f"{check_id}: {check.summary}")
        self.assertIn("GROUNDED", results["audits.completed"].detail)
        # The trafficgen key is sent as the proxy key header.
        self.assertIn(
            ("X-TrustAgent-Key", "tg-key"),
            [item for _, headers in http.calls for item in headers.items()],
        )

    def test_unhealthy_and_missing_services_fail_with_a_logs_hint(self) -> None:
        inspect = [
            c
            for c in demo_containers()
            if c["Config"]["Labels"]["com.docker.compose.service"] != "trafficgen"
        ]
        inspect[1] = container("engine", health="unhealthy", env=DEMO_ENGINE_ENV)
        d = make_doctor(
            FakeDocker(inspect), FakeHttp(demo_routes(traces("completed"))), FakeClock()
        )
        check = d.check_services()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("engine unhealthy", check.summary)
        self.assertIn("trafficgen missing", check.summary)
        self.assertEqual(check.fix, "make demo-logs SERVICE=engine")

    def test_no_containers_points_at_make_demo(self) -> None:
        d = make_doctor(FakeDocker([]), FakeHttp({}), FakeClock())
        check = d.check_services()
        self.assertEqual((check.status, check.fix), ("FAIL", "make demo"))

    def test_vendor_keys_fail_demo_mode_without_revealing_them(self) -> None:
        inspect = demo_containers()
        inspect[1] = container(
            "engine", env=DEMO_ENGINE_ENV + ["ANTHROPIC_API_KEY=sk-ant-secret", "OPENAI_API_KEY="]
        )
        d = make_doctor(
            FakeDocker(inspect), FakeHttp(demo_routes(traces("completed"))), FakeClock()
        )
        d.check_proxy()
        check = d.check_demo_mode()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("ANTHROPIC_API_KEY", check.summary)
        self.assertNotIn("OPENAI_API_KEY", check.summary, "empty values are not keys")
        self.assertNotIn("sk-ant-secret", check.summary + check.detail)

    def test_non_mock_backend_fails_demo_mode(self) -> None:
        inspect = demo_containers()
        inspect[0] = container(
            "proxy", env=["TRUTHTABLE_DEMO_MODE=true", "TRUTHTABLE_JEV_BACKEND=auto"]
        )
        routes = demo_routes(traces("completed"))
        routes[f"{PROXY}/health"] = ok_json({"status": "ok", "demo": False, "audit_engine": True})
        d = make_doctor(FakeDocker(inspect), FakeHttp(routes), FakeClock())
        d.check_proxy()
        summary = d.check_demo_mode().summary
        self.assertIn("demo=false", summary)
        self.assertIn("TRUTHTABLE_JEV_BACKEND is not mock", summary)

    def test_waits_for_traffic_then_passes(self) -> None:
        clock = FakeClock()
        http = FakeHttp(demo_routes([traces(), traces(), traces("queued")]))
        check = make_doctor(FakeDocker(demo_containers()), http, clock).check_traces()
        self.assertEqual(check.status, "PASS")
        self.assertEqual(clock.sleeps, [doctor.POLL_INTERVAL_S] * 2)

    def test_gives_up_after_the_wait(self) -> None:
        clock = FakeClock()
        d = make_doctor(
            FakeDocker(demo_containers()),
            FakeHttp(demo_routes(traces("queued"))),
            clock,
            wait_s=9.0,
        )
        check = d.check_audits()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("after 9s", check.summary)
        self.assertIn("queued", check.detail)
        self.assertGreaterEqual(clock.now, 9.0)

    def test_client_errors_are_not_retried(self) -> None:
        clock = FakeClock()
        routes = demo_routes(HttpResult(status=401, error="unauthorized"))
        check = make_doctor(FakeDocker(demo_containers()), FakeHttp(routes), clock).check_traces()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("HTTP 401", check.summary)
        self.assertIn("TRAFFICGEN_API_KEY", check.fix)
        self.assertEqual(clock.sleeps, [])

    def test_engine_healthcheck_failure(self) -> None:
        docker = FakeDocker(
            demo_containers(), healthcheck=(1, "", "No module named truthtable.healthcheck\n")
        )
        d = make_doctor(docker, FakeHttp(demo_routes(traces())), FakeClock())
        d.check_proxy()
        check = d.check_engine()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("No module named", check.detail)
        self.assertIn(
            ["docker", "exec", "p-engine-1", "python", "-m", "truthtable.healthcheck"], docker.calls
        )

    def test_engine_unreachable_from_proxy_warns(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/health"] = ok_json({"status": "ok", "demo": True, "audit_engine": False})
        d = make_doctor(FakeDocker(demo_containers()), FakeHttp(routes), FakeClock())
        d.check_proxy()
        self.assertEqual(d.check_engine().status, "WARN")

    def test_dashboard_that_cannot_reach_the_proxy_fails(self) -> None:
        routes = demo_routes(traces())
        routes[f"{DASH}/health"] = HttpResult(status=502)
        check = make_doctor(
            FakeDocker(demo_containers()), FakeHttp(routes), FakeClock()
        ).check_dashboard()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("HTTP 502", check.summary)


class HostAndCliTest(unittest.TestCase):
    def test_host_target_without_a_proxy(self) -> None:
        d = make_doctor(
            FakeDocker([]), FakeHttp({}), FakeClock(), target="host", expect=None, wait_s=0.0
        )
        results = run_all(d)
        self.assertNotIn("compose.services", results)
        self.assertEqual(results["proxy.live"].status, "FAIL")
        self.assertEqual(results["proxy.live"].fix, "make dev-go")
        self.assertEqual(results["engine.health"].status, "SKIP")
        self.assertEqual(results["dashboard.live"].status, "SKIP")
        self.assertEqual(results["traces.recent"].status, "WARN", "no traffic is not a failure")

    def test_main_json_report_and_exit_code(self) -> None:
        docker = FakeDocker(demo_containers())
        http = FakeHttp(demo_routes(traces("completed")))
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = doctor.main(
                ["--target", "docker", "--expect", "demo", "--no-probe", "--json", "--wait", "0"],
                run=docker,
                fetch=http,
                environ={
                    "PROXY_HOST_PORT": "8080",
                    "DASHBOARD_HOST_PORT": "3000",
                    "BIND_ADDR": "127.0.0.1",
                    "TRAFFICGEN_API_KEY": "k",
                },
            )
        report = json.loads(out.getvalue())
        self.assertEqual(code, 0)
        self.assertEqual(
            (report["target"], report["project"], report["ok"]), ("docker", "truthtable-demo", True)
        )
        self.assertEqual({c["status"] for c in report["checks"]}, {"PASS"})
        self.assertEqual(set(report["checks"][0]), {"id", "status", "summary", "detail", "fix"})

    def test_main_exits_1_on_failure_and_honours_skip(self) -> None:
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = doctor.main(
                [
                    "--target",
                    "host",
                    "--skip",
                    "traces,engine",
                    "--proxy-url",
                    "http://127.0.0.1:1",
                ],
                run=FakeDocker([]),
                fetch=FakeHttp({}),
                environ={"NO_COLOR": "1"},
            )
        text = out.getvalue()
        self.assertEqual(code, 1)
        self.assertIn("FAIL  proxy.live", text)
        self.assertNotIn("traces.recent", text)
        self.assertNotIn("engine.health", text)
        self.assertIn("fix: make dev-go", text)

    def test_logs_hint_follows_the_project(self) -> None:
        clock, http, docker = FakeClock(), FakeHttp({}), FakeDocker([])
        self.assertEqual(
            make_doctor(docker, http, clock).logs_hint("engine"), "make demo-logs SERVICE=engine"
        )
        default = make_doctor(docker, http, clock, project="truthtable", expect=None)
        self.assertEqual(default.logs_hint("proxy"), "make logs SERVICE=proxy")
        other = make_doctor(docker, http, clock, project="truthtable-go", expect=None)
        self.assertEqual(other.logs_hint("proxy"), "docker compose -p truthtable-go logs proxy")

    def test_selected(self) -> None:
        self.assertTrue(selected("proxy.live", [], []))
        self.assertTrue(selected("proxy.live", ["proxy"], []))
        self.assertTrue(selected("proxy.live", ["proxy.*"], []))
        self.assertFalse(selected("proxy.live", ["engine"], []))
        self.assertFalse(selected("proxy.live", [], ["proxy.live"]))

    def test_describe_state(self) -> None:
        def c(service: str, state: str, health: Optional[str], code: int = 0) -> Container:
            return Container(name="n", service=service, state=state, health=health, exit_code=code)

        self.assertEqual(describe_state(c("proxy", "running", "healthy")), "ok")
        self.assertEqual(describe_state(c("trafficgen", "running", None)), "ok")
        self.assertEqual(describe_state(c("proxy", "running", "starting")), "starting")
        self.assertEqual(describe_state(c("engine", "running", "unhealthy")), "unhealthy")
        self.assertEqual(describe_state(c("engine", "exited", None, 2)), "exited (exit 2)")
        self.assertEqual(describe_state(c("proxy", "restarting", None)), "restarting")
        self.assertEqual(describe_state(c("ollama-init", "exited", None, 0)), "ok")
        self.assertEqual(describe_state(c("ollama-init", "exited", None, 1)), "exited 1")

    def test_oneoff_containers_are_ignored(self) -> None:
        self.assertIsNone(doctor.parse_container(container("proxy", oneoff=True)))
        parsed = doctor.parse_container(container("proxy", env=["A=b=c"]))
        assert parsed is not None
        self.assertEqual((parsed.name, parsed.env), ("p-proxy-1", {"A": "b=c"}))


if __name__ == "__main__":
    unittest.main()
