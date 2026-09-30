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


GATEWAY_METRICS = """# HELP trustagent_gateway_ws_clients Connected WebSocket clients.
# TYPE trustagent_gateway_ws_clients gauge
trustagent_gateway_ws_clients 1
# HELP trustagent_gateway_audit_roundtrip_seconds Time from queueing a deep audit to its result.
# TYPE trustagent_gateway_audit_roundtrip_seconds histogram
trustagent_gateway_audit_roundtrip_seconds_bucket{le="0.1"} 0
trustagent_gateway_requests_total{route="support-bot",format="openai.chat",action="pass"} 3
# HELP go_goroutines Number of goroutines that currently exist.
# TYPE go_goroutines gauge
go_goroutines 42
"""
ENGINE_METRICS = """# HELP python_info Python platform information
python_info{implementation="CPython",major="3",minor="11"} 1.0
# HELP trustagent_engine_audits_total Deep audits by outcome
# TYPE trustagent_engine_audits_total counter
trustagent_engine_audits_total{status="success"} 4.0
trustagent_engine_audits_total_created{status="success"} 1.7e+09
# HELP trustagent_engine_active_audits Audits running now
# TYPE trustagent_engine_active_audits gauge
trustagent_engine_active_audits 0.0
"""


# A sample under the engine's pre-rename (v1) name.
LEGACY_SAMPLE = 'trustagent_claims_total{status="supported"} 2.0'  # check-forbidden: ignore


class FakeDocker:
    """docker ps/inspect from ``inspect``; docker exec answers the engine healthcheck and the
    in-container metrics fetches (wget in the proxy, python -c in the engine)."""

    def __init__(
        self,
        inspect: List[Dict[str, Any]],
        healthcheck: Tuple[int, str, str] = (0, "", ""),
        gateway_metrics: Tuple[int, str, str] = (0, GATEWAY_METRICS, ""),
        engine_metrics: Tuple[int, str, str] = (0, ENGINE_METRICS, ""),
    ):
        self.inspect = inspect
        self.healthcheck = healthcheck
        self.gateway_metrics = gateway_metrics
        self.engine_metrics = engine_metrics
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        if argv[:2] == ["docker", "ps"]:
            return 0, "\n".join(f"id{i}" for i in range(len(self.inspect))), ""
        if argv[:2] == ["docker", "inspect"]:
            return 0, json.dumps(self.inspect), ""
        if argv[:2] == ["docker", "exec"]:
            if argv[3:5] == ["wget", "-q"]:
                return self.gateway_metrics
            if argv[3:5] == ["python", "-c"]:
                return self.engine_metrics
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


def decision_model(**overrides: Any) -> Dict[str, Any]:
    dm: Dict[str, Any] = {
        "provider": "jev",
        "backend": "mock",
        "backend_reason": "demo mode",
        "endpoint_host": None,
        "model_requested": "mock/jev-heuristic-v1",
        "snapshot": "mock/jev-heuristic-v1",
        "credential": {"source": "none", "name": None, "masked": None},
        "state": "mock",
        "breaker": "closed",
        "last_error": None,
        "calibration_state": "mock",
        "calibrated_targets": 3,
        "p50_ms": 1.0,
        "p95_ms": 2.4,
        "calls_24h": 26,
        "cost_24h_usd": 0.0,
        "rpm_limit": 600,
        "rpm_used": 4,
    }
    dm.update(overrides)
    return dm


def system_status(dm: Optional[Dict[str, Any]] = None, **policy: Any) -> HttpResult:
    pol: Dict[str, Any] = {
        "version": 3,
        "source": "file",
        "locked": False,
        "persisted": True,
        "editable": True,
        "file_status": "ok",
        "file_error": None,
    }
    pol.update(policy)
    return ok_json(
        {"version": "2.0.0", "demo": True, "decision_model": dm or decision_model(), "policy": pol}
    )


def policies(*downgraded: Tuple[str, str, str, str]) -> HttpResult:
    routes = [
        {"name": "support-bot", "mode": "flag", "effective_mode": "flag", "downgrades": []},
    ]
    for name, mode, effective, code in downgraded:
        routes.append(
            {
                "name": name,
                "mode": mode,
                "effective_mode": effective,
                "downgrades": [{"code": code, "message": "m", "fix": None}],
            }
        )
    return ok_json({"id": 3, "effective_routes": routes})


def setup_status(
    required: bool = False, ingest: str = "keys", persistence: bool = True
) -> HttpResult:
    return ok_json(
        {
            "setup_required": required,
            "ingest_auth": ingest,
            "demo": True,
            "readonly": False,
            "persistence": persistence,
            "public_url": "http://localhost:3000",
            "recovery_command": "docker compose exec proxy /proxy -new-setup-token -force",
        }
    )


def demo_routes(trace_result: Any) -> Dict[str, Any]:
    return {
        f"{PROXY}/health": ok_json(
            {"status": "ok", "version": "2.0.0", "demo": True, "audit_engine": True}
        ),
        f"{DASH}/healthz": HttpResult(status=200, body=b"ok\n"),
        f"{DASH}/config.json": ok_json({"apiBase": "", "wsUrl": ""}),
        f"{DASH}/health": ok_json({"status": "ok"}),
        f"{PROXY}/api/setup/status": setup_status(),
        f"{PROXY}/api/status": system_status(),
        f"{PROXY}/api/policies": policies(),
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
                "setup.status",
                "decision_model.state",
                "calibration.targets",
                "policy.state",
                "metrics.gateway",
                "metrics.engine",
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


class StatusChecksTest(unittest.TestCase):
    """setup.status, decision_model.state, calibration.targets and policy.state."""

    def doctor_with(self, routes: Dict[str, Any], **kwargs: Any) -> Doctor:
        return make_doctor(FakeDocker(demo_containers()), FakeHttp(routes), FakeClock(), **kwargs)

    def real_stack(self, **routes: Any) -> Doctor:
        all_routes = demo_routes(traces("completed"))
        all_routes.update({f"{PROXY}{path}": result for path, result in routes.items()})
        return self.doctor_with(all_routes, project="truthtable", expect=None)

    def test_decision_model_reports_backend_reason_snapshot_latency_and_cost(self) -> None:
        check = self.doctor_with(demo_routes(traces())).check_decision_model()
        self.assertEqual(check.status, "PASS")
        self.assertIn("backend mock, state mock", check.summary)
        self.assertIn("simulated", check.summary)
        for part in (
            "why: demo mode",
            "snapshot mock/jev-heuristic-v1",
            "p50 1 ms, p95 2 ms",
            "26 call(s), $0.0000 (simulated)",
        ):
            self.assertIn(part, check.detail)

    def test_demo_needs_the_mock_backend(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/api/status"] = system_status(
            decision_model(backend="openrouter-decisions", state="ready")
        )
        check = self.doctor_with(routes).check_decision_model()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("mock backend", check.summary)

    def test_decision_model_states_on_a_real_stack(self) -> None:
        error = {"kind": "no_credits", "message": "HTTP 402", "hint": "Add credits", "at": "x"}
        cases = {
            "ready": ("PASS", ""),
            "off": ("WARN", "OPENROUTER_API_KEY"),
            "mock": ("WARN", "TRUTHTABLE_JEV_BACKEND=auto"),
            "breaker_open": ("WARN", "Add credits"),
            "no_credits": ("FAIL", "Add credits"),
            "unauthorized": ("FAIL", "Add credits"),
        }
        for state, (want, fix) in cases.items():
            with self.subTest(state=state):
                dm = decision_model(
                    backend="openrouter-decisions",
                    state=state,
                    last_error=error if state not in ("ready", "off", "mock") else None,
                    cost_24h_usd=0.0123,
                )
                check = self.real_stack(**{"/api/status": system_status(dm)}).check_decision_model()
                self.assertEqual(check.status, want, check.summary)
                self.assertIn(fix, check.fix)
                self.assertIn("$0.0123 (estimate)", check.detail)
        dm = decision_model(backend="typesafe", state="misconfigured", last_error=None)
        check = self.real_stack(**{"/api/status": system_status(dm)}).check_decision_model()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("docs/JEV-ACCESS.md", check.fix)

    def test_a_status_failure_is_reported_once(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/api/status"] = HttpResult(status=401, error="unauthorized")
        d = self.doctor_with(routes)
        first = d.check_decision_model()
        self.assertEqual(first.status, "FAIL")
        self.assertIn("/api/status: HTTP 401", first.summary)
        self.assertIn("TRAFFICGEN_API_KEY", first.fix)
        self.assertEqual(d.check_calibration().status, "SKIP")
        self.assertEqual(d.check_policy().status, "SKIP")
        # Run alone (--only calibration), the check reports the failure itself.
        alone = self.doctor_with(routes).check_calibration()
        self.assertEqual((alone.id, alone.status), ("calibration.targets", "FAIL"))

    def test_an_old_proxy_without_status_is_told_to_rebuild(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/api/status"] = HttpResult(status=404)
        check = self.doctor_with(routes).check_decision_model()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("rebuild it (make demo)", check.fix)

    def test_demo_calibration_needs_two_targets(self) -> None:
        for targets, want in ((0, "FAIL"), (1, "FAIL"), (2, "PASS"), (5, "PASS")):
            with self.subTest(targets=targets):
                routes = demo_routes(traces())
                routes[f"{PROXY}/api/status"] = system_status(
                    decision_model(calibrated_targets=targets)
                )
                check = self.doctor_with(routes).check_calibration()
                self.assertEqual(check.status, want, check.summary)
                self.assertIn(f"{targets} calibrated target(s)", check.summary)
                if want == "FAIL":
                    self.assertEqual(check.fix, "make demo-logs SERVICE=proxy")

    def test_calibration_is_optional_on_a_real_stack(self) -> None:
        dm = decision_model(calibration_state="none", calibrated_targets=0)
        check = self.real_stack(**{"/api/status": system_status(dm)}).check_calibration()
        self.assertEqual(check.status, "PASS")
        self.assertIn("make calibrate", check.summary)

    def test_policy_reports_version_source_and_file(self) -> None:
        check = self.doctor_with(demo_routes(traces())).check_policy()
        self.assertEqual(check.status, "PASS")
        self.assertEqual(check.summary, "version 3 from file, policy file ok")

    def test_policy_file_states(self) -> None:
        cases = {
            "invalid": ("FAIL", "config/policies.yaml"),
            "missing": ("WARN", "TRUTHTABLE_POLICY_FILE"),
            "diverged": ("WARN", "adopt-file"),
        }
        for file_status, (want, fix) in cases.items():
            with self.subTest(file_status=file_status):
                error = "line 4: unknown key" if file_status == "invalid" else None
                status = system_status(file_status=file_status, file_error=error)
                check = self.real_stack(**{"/api/status": status}).check_policy()
                self.assertEqual(check.status, want)
                self.assertIn(fix, check.fix)
                if error:
                    self.assertIn("file error: line 4: unknown key", check.detail)

    def test_policy_lists_route_downgrades(self) -> None:
        d = self.real_stack(
            **{
                "/api/policies": policies(
                    ("handbook", "block", "flag", "uncalibrated"),
                    ("chat", "flag", "flag", "stream_not_buffered"),
                )
            }
        )
        check = d.check_policy()
        self.assertEqual(check.status, "WARN")
        self.assertIn("2 route(s) run below their configured mode", check.summary)
        self.assertIn("handbook runs as flag (configured block): uncalibrated", check.detail)
        self.assertIn("chat: stream_not_buffered", check.detail)

    def test_policy_without_the_policies_endpoint_warns(self) -> None:
        check = self.real_stack(**{"/api/policies": HttpResult(status=500)}).check_policy()
        self.assertEqual(check.status, "WARN")
        self.assertIn("route downgrades unknown (/api/policies: HTTP 500)", check.detail)

    def test_setup_passes_with_an_admin_and_ingest_keys(self) -> None:
        d = self.doctor_with(demo_routes(traces()))
        check = d.check_setup()
        self.assertEqual(check.status, "PASS")
        self.assertIn("ingest_auth=keys", check.detail)
        # /api/setup/status is public: no key is sent.
        sent = [h for url, h in d.fetch.calls if url.endswith("/api/setup/status")]  # type: ignore[attr-defined]
        self.assertEqual(sent, [{}])

    def test_setup_points_at_the_setup_link_of_its_stack(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/api/setup/status"] = setup_status(required=True)
        demo = self.doctor_with(routes).check_setup()
        self.assertEqual(demo.status, "WARN")
        self.assertIn("no admin yet", demo.summary)
        self.assertIn("make demo-setup-url", demo.fix)
        real = self.real_stack(**{"/api/setup/status": setup_status(required=True)}).check_setup()
        self.assertTrue(real.fix.startswith("make setup-url"))
        no_db = self.real_stack(
            **{"/api/setup/status": setup_status(required=True, persistence=False)}
        ).check_setup()
        self.assertIn("TRUTHTABLE_ADMIN_KEYS", no_db.fix)

    def test_open_ingest_warns(self) -> None:
        check = self.real_stack(**{"/api/setup/status": setup_status(ingest="open")}).check_setup()
        self.assertEqual(check.status, "WARN")
        self.assertIn("ingest is open", check.summary)
        self.assertIn("TRUTHTABLE_API_KEYS", check.fix)

    def test_setup_endpoint_missing(self) -> None:
        routes = demo_routes(traces())
        routes[f"{PROXY}/api/setup/status"] = HttpResult(status=404)
        check = self.doctor_with(routes).check_setup()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("no /api/setup/status", check.fix)


class MetricsChecksTest(unittest.TestCase):
    def check(
        self, docker: FakeDocker, target: str = "docker"
    ) -> Tuple[doctor.Check, doctor.Check]:
        d = make_doctor(docker, FakeHttp({}), FakeClock(), target=target)
        return d.check_gateway_metrics(), d.check_engine_metrics()

    def test_both_prefixes_pass(self) -> None:
        docker = FakeDocker(demo_containers())
        gateway, engine = self.check(docker)
        self.assertEqual((gateway.status, engine.status), ("PASS", "PASS"))
        self.assertIn("proxy :8002/metrics exposes 3 trustagent_gateway_* series", gateway.summary)
        self.assertIn("engine :8001/metrics exposes 3 trustagent_engine_* series", engine.summary)
        execs = [c for c in docker.calls if c[:2] == ["docker", "exec"]]
        self.assertIn(
            [
                "docker",
                "exec",
                "p-proxy-1",
                "wget",
                "-q",
                "-O",
                "-",
                "http://127.0.0.1:8002/metrics",
            ],
            execs,
        )
        engine_call = next(c for c in execs if c[3:5] == ["python", "-c"])
        # The URL is an argument of the fetch, not part of its code.
        self.assertEqual(engine_call[-1], "http://127.0.0.1:8001/metrics")
        self.assertNotIn("8001", engine_call[-2])

    def test_legacy_names_fail(self) -> None:
        legacy = ENGINE_METRICS + LEGACY_SAMPLE + "\n"
        gateway, engine = self.check(FakeDocker(demo_containers(), engine_metrics=(0, legacy, "")))
        self.assertEqual((gateway.status, engine.status), ("PASS", "FAIL"))
        self.assertIn("1 name(s) outside trustagent_engine_*", engine.summary)
        self.assertIn("claims_total", engine.detail)
        self.assertIn("make demo", engine.fix)

    def test_the_other_services_prefix_fails_too(self) -> None:
        mixed = GATEWAY_METRICS + "trustagent_engine_active_audits 0\n"
        gateway, _ = self.check(FakeDocker(demo_containers(), gateway_metrics=(0, mixed, "")))
        self.assertEqual(gateway.status, "FAIL")
        self.assertIn("trustagent_engine_active_audits", gateway.detail)

    def test_no_prefixed_metric_fails(self) -> None:
        bare = "# TYPE go_goroutines gauge\ngo_goroutines 3\n"
        gateway, _ = self.check(FakeDocker(demo_containers(), gateway_metrics=(0, bare, "")))
        self.assertEqual(gateway.status, "FAIL")
        self.assertIn("exposes no trustagent_gateway_* metric", gateway.summary)

    def test_an_unreachable_endpoint_fails_with_a_logs_hint(self) -> None:
        down = (1, "", "wget: can't connect to remote host (127.0.0.1): Connection refused\n")
        gateway, _ = self.check(FakeDocker(demo_containers(), gateway_metrics=down))
        self.assertEqual(gateway.status, "FAIL")
        self.assertIn("Connection refused", gateway.detail)
        self.assertEqual(gateway.fix, "make demo-logs SERVICE=proxy")

    def test_ports_come_from_the_container_environment(self) -> None:
        inspect = demo_containers()
        inspect[0] = container("proxy", env=DEMO_PROXY_ENV + ["TRUTHTABLE_METRICS_PORT=9102"])
        inspect[1] = container("engine", env=DEMO_ENGINE_ENV + ["METRICS_PORT=9101; rm -rf /"])
        docker = FakeDocker(inspect)
        gateway, engine = self.check(docker)
        self.assertIn(":9102/metrics", gateway.summary)
        self.assertIn(":8001/metrics", engine.summary, "a non-numeric port falls back")

    def test_host_target_skips(self) -> None:
        gateway, engine = self.check(FakeDocker([]), target="host")
        self.assertEqual((gateway.status, engine.status), ("SKIP", "SKIP"))

    def test_metric_names(self) -> None:
        names = doctor.metric_names(GATEWAY_METRICS)
        self.assertEqual(
            names,
            {
                "trustagent_gateway_ws_clients",
                "trustagent_gateway_audit_roundtrip_seconds",
                "trustagent_gateway_audit_roundtrip_seconds_bucket",
                "trustagent_gateway_requests_total",
                "go_goroutines",
            },
        )


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
