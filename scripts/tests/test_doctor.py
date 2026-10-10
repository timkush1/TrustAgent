"""Unit tests for scripts/doctor.py with fake docker and HTTP (no Docker, no network)."""

from __future__ import annotations

import contextlib
import io
import json
import re
import sys
import tempfile
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


# The image each service runs: trafficgen runs the proxy's.
IMAGE_OF = {"trafficgen": "proxy", "mock-upstream": "proxy"}
REVISION = "abc1234def56"


def container(
    service: str,
    status: str = "running",
    health: Optional[str] = "healthy",
    exit_code: int = 0,
    env: Optional[List[str]] = None,
    oneoff: bool = False,
    ports: Sequence[int] = (),
) -> Dict[str, Any]:
    state: Dict[str, Any] = {"Status": status, "ExitCode": exit_code}
    if health is not None:
        state["Health"] = {"Status": health}
    labels = {"com.docker.compose.service": service, "com.docker.compose.project": "p"}
    if oneoff:
        labels["com.docker.compose.oneoff"] = "True"
    image = IMAGE_OF.get(service, service)
    return {
        "Name": f"/p-{service}-1",
        "Image": f"sha256:{image}",
        "State": state,
        "Config": {
            "Labels": labels,
            "Env": env or [],
            "Image": f"ghcr.io/timkush1/truthtable-{image}:1.0.0",
        },
        "NetworkSettings": {
            "Ports": {f"{p}/tcp": [{"HostIp": "127.0.0.1", "HostPort": str(p)}] for p in ports}
        },
    }


def demo_containers() -> List[Dict[str, Any]]:
    return [
        container("proxy", env=DEMO_PROXY_ENV, ports=[8080]),
        container("engine", env=DEMO_ENGINE_ENV),
        container("postgres"),
        container("dashboard", ports=[3000]),
        container("trafficgen", health=None),
    ]


def image(
    service: str, version: str = "1.0.0", revision: str = REVISION, pulled: bool = False
) -> Dict[str, Any]:
    labels = {"org.opencontainers.image.version": version}
    if revision:
        labels["org.opencontainers.image.revision"] = revision
    ref = f"ghcr.io/timkush1/truthtable-{service}"
    return {
        "Id": f"sha256:{service}",
        "RepoTags": [f"{ref}:{version}"],
        "RepoDigests": [f"{ref}@sha256:{'0' * 64}"] if pulled else [],
        "Config": {"Labels": labels},
    }


def app_images(**overrides: Dict[str, Any]) -> Dict[str, Dict[str, Any]]:
    images = {f"sha256:{s}": image(s) for s in ("proxy", "engine", "dashboard")}
    for service, img in overrides.items():
        images[f"sha256:{service}"] = img
    return images


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
        docker_version: Tuple[int, str, str] = (0, "28.1.1\n", ""),
        compose_version: Tuple[int, str, str] = (0, "v2.39.2-desktop.1\n", ""),
        mem_total: Tuple[int, str, str] = (0, f"{16 * 1024**3}\n", ""),
        images: Optional[Dict[str, Dict[str, Any]]] = None,
    ):
        self.inspect = inspect
        self.healthcheck = healthcheck
        self.gateway_metrics = gateway_metrics
        self.engine_metrics = engine_metrics
        self.docker_version = docker_version
        self.compose_version = compose_version
        self.mem_total = mem_total
        self.images = app_images() if images is None else images
        self.calls: List[List[str]] = []

    def __call__(self, argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        if argv[:2] == ["docker", "version"]:
            return self.docker_version
        if argv[:4] == ["docker", "compose", "version", "--short"]:
            return self.compose_version
        if argv[:2] == ["docker", "info"]:
            return self.mem_total
        if argv[:3] == ["docker", "image", "inspect"]:
            found = [self.images[i] for i in argv[3:] if i in self.images]
            if not found:
                return 1, "", "Error: No such image"
            return 0, json.dumps(found), ""
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


def judge(**overrides: Any) -> Dict[str, Any]:
    j: Dict[str, Any] = {
        "state": "up",
        "provider": "mock",
        "model": "mock-judge",
        "credential_source": "none",
        "fallback_active": False,
        "fallback_reason": None,
        "warnings": [],
    }
    j.update(overrides)
    return j


def budgets(**overrides: Any) -> Dict[str, Any]:
    b: Dict[str, Any] = {
        "decision_usd_day": 2.0,
        "decision_spent_today_usd": 0.0,
        "llm_usd_day": 5.0,
        "llm_spent_today_usd": 0.0,
        "decision_exhausted": False,
        "decision_reserve_only": False,
        "llm_exhausted": False,
    }
    b.update(overrides)
    return b


def status_body(dm: Optional[Dict[str, Any]] = None, **policy: Any) -> Dict[str, Any]:
    """A /api/status body as a demo stack serves it (runtime settings never saved: version 0,
    which the proxy leaves out of engine.runtime_config_version)."""
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
    return {
        "version": "2.0.0",
        "build_sha": REVISION[:7],
        "demo": True,
        "decision_model": dm or decision_model(),
        "policy": pol,
        "engine": {"state": "up", "version": "2.0.0"},
        "judge": judge(),
        "budgets": budgets(),
        "credentials": {"store": "ready", "undecryptable": []},
        "warnings": [],
    }


def system_status(dm: Optional[Dict[str, Any]] = None, **policy: Any) -> HttpResult:
    return ok_json(status_body(dm, **policy))


def settings_view(version: int = 0) -> HttpResult:
    return ok_json({"version": version, "etag": f'"s{version}"', "env_managed": []})


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
        f"{PROXY}/api/settings": settings_view(),
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


class FakePost:
    """The one POST doctor may make (--probe); records every call."""

    def __init__(self, result: Optional[HttpResult] = None) -> None:
        self.result = result or HttpResult(status=500, error="no probe answer configured")
        self.calls: List[Tuple[str, Dict[str, str], bytes]] = []

    def __call__(self, url: str, headers: Mapping[str, str], body: bytes) -> HttpResult:
        self.calls.append((url, dict(headers), body))
        return self.result


# No .env: environment.env_file skips (its own tests use real files).
NO_ENV_FILE = Path(__file__).resolve().parent / "no-such-dir" / ".env"


def make_doctor(docker: FakeDocker, http: FakeHttp, clock: FakeClock, **kwargs: Any) -> Doctor:
    options: Dict[str, Any] = dict(
        target="docker",
        project="truthtable-demo",
        proxy_url=PROXY,
        dashboard_url=DASH,
        expect="demo",
        api_key="tg-key",
        wait_s=30.0,
        environ={},
        env_file=NO_ENV_FILE,
        host_ports={"proxy": 8080, "dashboard": 3000, "postgres": 5432},
        port_probe=lambda host, port: True,
        post=FakePost(),
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
                "environment.docker",
                "environment.memory",
                "environment.ports",
                "environment.env_file",
                "proxy.live",
                "engine.health",
                "dashboard.live",
                "demo.mode",
                "setup.status",
                "decision_model.state",
                "decision_model.rpm",
                "decision_model.probe",
                "calibration.targets",
                "policy.state",
                "credentials.store",
                "runtime_config.version",
                "judge.credential_source",
                "budgets.today",
                "config.price_table",
                "profiles.addons",
                "images.version",
                "metrics.gateway",
                "metrics.engine",
                "traces.recent",
                "audits.completed",
            ],
        )
        # The demo stays green: a skip means "does not apply here" (no .env file in this test, a
        # mock fast check has no rate limit, and doctor sends no probe by default).
        skips = {"environment.env_file", "decision_model.rpm", "decision_model.probe"}
        for check_id, check in results.items():
            expected = "SKIP" if check_id in skips else "PASS"
            self.assertEqual(check.status, expected, f"{check_id}: {check.summary}")
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
        # chat keeps its flag mode: only handbook runs below the mode it was configured with.
        self.assertIn(
            "2 route(s) with downgrades (1 running below the configured mode)", check.summary
        )
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


class RuntimeChecksTest(unittest.TestCase):
    """credentials.store, runtime_config.version, judge.credential_source and budgets.today."""

    def stack(self, status: Dict[str, Any], expect: Optional[str] = None, **routes: Any) -> Doctor:
        all_routes = demo_routes(traces("completed"))
        all_routes[f"{PROXY}/api/status"] = ok_json(status)
        all_routes.update({f"{PROXY}{path}": result for path, result in routes.items()})
        return make_doctor(
            FakeDocker(demo_containers()),
            FakeHttp(all_routes),
            FakeClock(),
            project="truthtable",
            expect=expect,
        )

    def with_status(self, **fields: Any) -> Dict[str, Any]:
        body = status_body()
        body.update(fields)
        return body

    # ---------- credentials.store ----------

    def test_credentials_ready(self) -> None:
        check = self.stack(status_body()).check_credentials()
        self.assertEqual(check.status, "PASS")
        self.assertIn("every saved key decrypts", check.summary)

    def test_undecryptable_keys_fail_by_name_with_the_way_back(self) -> None:
        body = self.with_status(
            credentials={"store": "ready", "undecryptable": ["openrouter", "judge-anthropic"]}
        )
        check = self.stack(body).check_credentials()
        self.assertEqual(check.status, "FAIL")
        self.assertIn(
            "2 saved key(s) cannot be decrypted (openrouter, judge-anthropic)", check.summary
        )
        self.assertIn("master key changed", check.summary)
        self.assertIn("trustagent-secrets", check.fix)
        self.assertIn("Settings > Connections", check.fix)

    def test_a_master_key_warning_names_the_keys_too(self) -> None:
        body = self.with_status(
            warning_details=[
                {"code": "master_key_changed", "message": "m", "details": {"names": ["typesafe"]}}
            ]
        )
        check = self.stack(body).check_credentials()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("(typesafe)", check.summary)

    def test_store_states(self) -> None:
        for store, expected in (("unavailable", "FAIL"), ("no_database", "WARN")):
            with self.subTest(store=store):
                body = self.with_status(credentials={"store": store, "undecryptable": []})
                self.assertEqual(self.stack(body).check_credentials().status, expected)
        body = status_body()
        del body["credentials"]
        self.assertEqual(self.stack(body).check_credentials().status, "SKIP")

    # ---------- runtime_config.version ----------

    def test_engine_applied_the_gateway_settings(self) -> None:
        check = self.stack(status_body()).check_runtime_config()
        self.assertEqual(check.status, "PASS", check.summary)
        self.assertIn("none saved yet", check.summary)
        body = self.with_status(
            engine={
                "state": "up",
                "version": "2.0.0",
                "runtime_config_version": 7,
                "runtime_config_epoch": "0190a8c4-epoch",
            }
        )
        check = self.stack(body, **{"/api/settings": settings_view(7)}).check_runtime_config()
        self.assertEqual(check.status, "PASS")
        self.assertIn("settings version 7", check.summary)
        self.assertIn("epoch 0190a8c4-epoch", check.detail)

    # The push warnings as the gateway words them (internal/runtimecfg/push.go).
    NOT_APPLIED = (
        "The audit engine has not applied the settings from the dashboard yet "
        "(it answered Unavailable): connection refused"
    )
    JUDGE_REFUSED = (
        "The audit engine could not use the judge settings from the dashboard, so deep audits "
        "keep the engine's own judge."
    )

    def test_an_engine_behind_the_gateway_warns(self) -> None:
        body = self.with_status(
            engine={"state": "up", "runtime_config_version": 6},
            warnings=[self.NOT_APPLIED, "Evaluators: unrelated"],
        )
        check = self.stack(body, **{"/api/settings": settings_view(7)}).check_runtime_config()
        self.assertEqual(check.status, "WARN")
        self.assertIn("applied settings version 6, the gateway holds 7", check.summary)
        self.assertIn("has not applied the settings", check.detail)
        self.assertNotIn("unrelated", check.detail)
        self.assertIn("make logs SERVICE=engine", check.fix)

    def test_a_push_warning_warns_even_when_the_versions_match(self) -> None:
        body = self.with_status(
            engine={"state": "up", "runtime_config_version": 7}, warnings=[self.JUDGE_REFUSED]
        )
        check = self.stack(body, **{"/api/settings": settings_view(7)}).check_runtime_config()
        self.assertEqual(check.status, "WARN")
        self.assertIn("runs settings version 7, but: The audit engine could not use", check.summary)

    def test_an_engine_that_reports_no_version_warns_once_settings_exist(self) -> None:
        check = self.stack(
            status_body(), **{"/api/settings": settings_view(3)}
        ).check_runtime_config()
        self.assertEqual(check.status, "WARN")
        self.assertIn("reports no applied settings", check.summary)
        self.assertIn("UP_ARGS=--build", check.fix)

    def test_doctor_reads_every_push_warning_the_gateway_sets(self) -> None:
        # doctor tells the push warnings apart from the other SystemStatus.warnings by their
        # wording ("the audit engine"); a rewording in the gateway would hide a failed push.
        push = Path(__file__).resolve().parents[2] / "backend-go/internal/runtimecfg/push.go"
        texts = re.findall(r'state\.Warning = "([^"]+)"', push.read_text(encoding="utf-8"))
        self.assertGreaterEqual(len(texts), 3, "the pattern still finds the pusher's warnings")
        for text in texts:
            body = self.with_status(
                engine={"state": "up", "runtime_config_version": 7}, warnings=[text]
            )
            check = self.stack(body, **{"/api/settings": settings_view(7)}).check_runtime_config()
            self.assertEqual(check.status, "WARN", text)

    def test_settings_endpoint_missing_or_refused(self) -> None:
        old = self.stack(status_body(), **{"/api/settings": HttpResult(status=404)})
        self.assertEqual(old.check_runtime_config().status, "SKIP")
        refused = self.stack(status_body(), **{"/api/settings": HttpResult(status=401)})
        check = refused.check_runtime_config()
        self.assertEqual(check.status, "WARN")
        self.assertIn("TRAFFICGEN_API_KEY", check.fix)

    # ---------- judge.credential_source ----------

    def test_demo_judge_is_the_mock(self) -> None:
        check = self.stack(status_body(), expect="demo").check_judge()
        self.assertEqual(check.status, "PASS")
        self.assertIn("simulated", check.summary)
        body = self.with_status(judge=judge(provider="openrouter", credential_source="env"))
        self.assertEqual(self.stack(body, expect="demo").check_judge().status, "FAIL")

    def test_judge_key_sources_on_a_real_stack(self) -> None:
        cases = [
            (judge(provider="openrouter", credential_source="env"), "PASS", "engine's .env"),
            (judge(provider="anthropic", credential_source="dashboard"), "PASS", "dashboard"),
            (judge(provider="ollama", model="llama3.2:1b"), "PASS", "needs no key"),
            (judge(provider="openai", credential_source="none"), "FAIL", "has no key"),
            # LLM_PROVIDER=none: the engine reports provider "none" and the proxy state disabled.
            (judge(provider="none", state="disabled"), "WARN", "no deep-audit judge"),
            (judge(provider="mock"), "WARN", "simulated"),
            (
                judge(
                    provider="openrouter",
                    credential_source="env",
                    fallback_active=True,
                    fallback_reason="primary unreachable",
                ),
                "WARN",
                "fallback",
            ),
            (judge(provider="openrouter", credential_source="env", state="down"), "WARN", "down"),
        ]
        for j, expected, words in cases:
            with self.subTest(judge=j):
                check = self.stack(self.with_status(judge=j)).check_judge()
                self.assertEqual(check.status, expected, check.summary)
                self.assertIn(words, check.summary + check.detail)
        check = self.stack(
            self.with_status(judge=judge(provider="openai", credential_source="none"))
        ).check_judge()
        self.assertIn("OPENAI_API_KEY", check.fix)

    def test_an_engine_that_has_not_reported_is_not_blamed_on_a_key(self) -> None:
        # No engine health yet: the proxy reports provider null and state unknown.
        unknown = judge(provider=None, model=None, state="unknown")
        for expect in (None, "demo"):
            with self.subTest(expect=expect):
                check = self.stack(self.with_status(judge=unknown), expect=expect).check_judge()
                self.assertEqual(check.status, "WARN", check.summary)
                self.assertIn("has not reported its judge", check.summary)
                self.assertIn("engine", check.fix)
                self.assertNotIn("OPENROUTER_API_KEY", check.fix)

    def test_the_mock_judge_fix_covers_env_and_dashboard(self) -> None:
        check = self.stack(self.with_status(judge=judge(provider="mock"))).check_judge()
        self.assertIn("LLM_PROVIDER=mock", check.fix)
        self.assertIn("Settings > Connections", check.fix)
        self.assertIn("turns simulated checks off", check.fix)

    def test_a_judge_without_credential_source_is_skipped(self) -> None:
        j = judge()
        del j["credential_source"]
        self.assertEqual(self.stack(self.with_status(judge=j)).check_judge().status, "SKIP")

    # ---------- budgets.today ----------

    def test_budgets_within_limits_label_real_spend(self) -> None:
        body = self.with_status(
            budgets=budgets(decision_spent_today_usd=0.0123, llm_spent_today_usd=0.35)
        )
        check = self.stack(body).check_budgets()
        self.assertEqual(check.status, "PASS")
        self.assertIn("fast checks $0.0123 of $2.00", check.detail)
        self.assertIn("deep audits $0.3500 of $5.00", check.detail)
        # Costs the vendor does not report are priced from the table: the total is an estimate.
        self.assertIn("(estimate: the vendor's reported cost where given", check.detail)
        self.assertNotIn("measured", check.summary + check.detail)
        demo = self.stack(status_body(), expect="demo").check_budgets()
        self.assertEqual(demo.status, "PASS")
        self.assertIn("simulated checks cost $0", demo.detail)
        unlimited = self.stack(self.with_status(budgets=budgets(llm_usd_day=0))).check_budgets()
        self.assertIn("deep audits $0.0000 of no budget set", unlimited.detail)

    def test_exhausted_budgets_warn_in_plain_words(self) -> None:
        cases = [
            (
                budgets(llm_exhausted=True),
                "Deep audits paused: today's deep-audit budget is used up",
            ),
            (budgets(decision_exhausted=True), "pass through unchecked"),
            (budgets(decision_reserve_only=True), "only block and fallback routes"),
            (budgets(decision_exhausted=True, llm_exhausted=True), "both daily budgets"),
        ]
        for b, words in cases:
            with self.subTest(budgets=b):
                check = self.stack(self.with_status(budgets=b)).check_budgets()
                self.assertEqual(check.status, "WARN")
                self.assertIn(words, check.summary)
                self.assertIn("Settings > Budgets", check.fix)
        body = status_body()
        del body["budgets"]
        self.assertEqual(self.stack(body).check_budgets().status, "SKIP")

    def test_a_failed_status_read_is_reported_once(self) -> None:
        d = self.stack(status_body(), **{"/api/status": HttpResult(status=500)})
        first, rest = d.check_credentials(), [
            d.check_runtime_config(),
            d.check_judge(),
            d.check_budgets(),
        ]
        self.assertEqual(first.status, "FAIL")
        self.assertEqual([c.status for c in rest], ["SKIP"] * 3)


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
                post=FakePost(),
                probe_port=lambda host, port: True,
                env_file=NO_ENV_FILE,
            )
        report = json.loads(out.getvalue())
        self.assertEqual(code, 0)
        self.assertEqual(
            (report["target"], report["project"], report["ok"]), ("docker", "truthtable-demo", True)
        )
        self.assertEqual({c["status"] for c in report["checks"]}, {"PASS", "SKIP"})
        self.assertEqual(
            set(report["checks"][0]), {"id", "status", "summary", "detail", "fix", "data"}
        )

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
                post=FakePost(),
                env_file=NO_ENV_FILE,
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


ADMIN_KEY = "sk-test-doctoradminkey"
LEAK_KEY = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"


def real_status(**dm: Any) -> HttpResult:
    """A /api/status of a stack with a real fast check (not the demo)."""
    body = status_body(
        decision_model(
            backend="openrouter-decisions",
            backend_reason="OPENROUTER_API_KEY is set",
            state="ready",
            calibration_state="none",
            calibrated_targets=0,
            **dm,
        )
    )
    body["demo"] = False
    return ok_json(body)


class EnvironmentChecksTest(unittest.TestCase):
    """environment.* (D129): Docker and Compose versions, memory, host ports, .env mode."""

    def doctor(self, docker: FakeDocker, **kwargs: Any) -> Doctor:
        return make_doctor(
            docker, FakeHttp(demo_routes(traces("completed"))), FakeClock(), **kwargs
        )

    def test_docker_and_compose_versions(self) -> None:
        check = self.doctor(FakeDocker(demo_containers())).check_docker()
        self.assertEqual((check.status, check.summary), ("PASS", "Docker 28.1, Compose 2.39"))
        old = FakeDocker(
            demo_containers(),
            docker_version=(0, "24.0.7\n", ""),
            compose_version=(0, "2.20.2\n", ""),
        )
        check = self.doctor(old).check_docker()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("Docker Engine 24.0 is older than 25", check.summary)
        self.assertIn("Compose 2.20 is older than 2.24", check.summary)
        down = FakeDocker([], docker_version=(1, "", "Cannot connect to the Docker daemon"))
        check = self.doctor(down).check_docker()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("Cannot connect", check.detail)

    def test_memory_below_4_gb_warns(self) -> None:
        low = FakeDocker(demo_containers(), mem_total=(0, str(3 * 1024**3), ""))
        check = self.doctor(low).check_memory()
        self.assertEqual(check.status, "WARN")
        self.assertIn("3.0 GB", check.summary)
        self.assertIn(".wslconfig", check.fix)
        self.assertEqual(self.doctor(FakeDocker(demo_containers())).check_memory().status, "PASS")
        unknown = FakeDocker(demo_containers(), mem_total=(1, "", "error"))
        self.assertEqual(self.doctor(unknown).check_memory().status, "SKIP")

    def test_busy_ports(self) -> None:
        # A running service is judged by what it publishes; a missing one needs its port free.
        no_dashboard = [c for c in demo_containers() if "dashboard" not in c["Name"]]
        d = self.doctor(
            FakeDocker(no_dashboard),
            host_ports={"proxy": 8080, "dashboard": 13000, "postgres": 5432},
            port_probe=lambda host, port: port != 13000,
        )
        check = d.check_ports()
        self.assertEqual(check.status, "WARN")
        self.assertIn("in use by another program: 13000 (dashboard)", check.summary)
        self.assertIn("make up cannot publish there", check.summary)
        self.assertEqual(
            check.fix, "stop that program, or set DASHBOARD_HOST_PORT in .env to free ports"
        )
        self.assertIn("8080 proxy", check.detail)
        # The demo publishes no database port: a busy 5432 is not its problem.
        d = self.doctor(FakeDocker(demo_containers()), port_probe=lambda host, port: False)
        check = d.check_ports()
        self.assertEqual(check.status, "PASS")
        self.assertEqual(check.summary, "host ports: 8080 proxy; 3000 dashboard; postgres: no port")
        # Nothing running: every configured port must be free.
        d = self.doctor(FakeDocker([]), port_probe=lambda host, port: port != 8080)
        check = d.check_ports()
        self.assertEqual(check.status, "WARN")
        self.assertIn("8080 (proxy)", check.summary)
        self.assertIn("PROXY_HOST_PORT", check.fix)
        self.assertIn("5432 free", check.detail)

    def test_env_file_mode(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            env = Path(tmp) / ".env"
            env.write_text(f"OPENROUTER_API_KEY={LEAK_KEY}\n", encoding="utf-8")
            env.chmod(0o644)
            check = self.doctor(FakeDocker([]), env_file=env, platform="posix").check_env_file()
            self.assertEqual(check.status, "WARN")
            self.assertIn("mode 0644", check.summary)
            self.assertEqual(check.fix, f"chmod 600 {env}")
            self.assertNotIn(LEAK_KEY, check.summary + check.detail + check.fix)
            env.chmod(0o600)
            check = self.doctor(FakeDocker([]), env_file=env, platform="posix").check_env_file()
            self.assertEqual((check.status, check.summary), ("PASS", ".env is private (mode 0600)"))
            check = self.doctor(FakeDocker([]), env_file=env, platform="nt").check_env_file()
            self.assertEqual(check.status, "SKIP")
        self.assertEqual(self.doctor(FakeDocker([])).check_env_file().status, "SKIP")


class ImagesCheckTest(unittest.TestCase):
    """images.version (D129): one version, matching revision labels, pulled or built."""

    def check(
        self, images: Dict[str, Dict[str, Any]], build_sha: str = REVISION[:7]
    ) -> doctor.Check:
        d = make_doctor(
            FakeDocker(demo_containers(), images=images),
            FakeHttp(demo_routes(traces())),
            FakeClock(),
        )
        d.status = {"build_sha": build_sha}
        return d.check_images()

    def test_one_version_and_revision_built_locally(self) -> None:
        check = self.check(app_images())
        self.assertEqual(check.status, "PASS")
        self.assertEqual(
            check.summary, f"one version 1.0.0, one revision {REVISION}; built locally"
        )
        self.assertIn("engine 1.0.0 (abc1234def56, built locally)", check.detail)

    def test_pulled_images_say_so(self) -> None:
        pulled = {s: image(s, pulled=True) for s in ("proxy", "engine", "dashboard")}
        check = self.check(app_images(**pulled))
        self.assertEqual(check.status, "PASS")
        self.assertTrue(check.summary.endswith("; pulled"), check.summary)

    def test_a_revision_mismatch_warns(self) -> None:
        check = self.check(app_images(engine=image("engine", revision="0123456789ab")))
        self.assertEqual(check.status, "WARN")
        self.assertIn("different commits", check.summary)
        self.assertIn("engine 0123456789ab", check.summary)
        self.assertIn("make demo", check.fix)
        # The proxy image against the running proxy's build.
        check = self.check(app_images(), build_sha="fedcba9")
        self.assertEqual(check.status, "WARN")
        self.assertIn("reports build fedcba9", check.summary)

    def test_mixed_versions_fail(self) -> None:
        check = self.check(app_images(dashboard=image("dashboard", version="0.9.0")))
        self.assertEqual(check.status, "FAIL")
        self.assertIn("dashboard 0.9.0", check.summary)

    def test_no_revision_labels_pass_with_a_note(self) -> None:
        bare = {s: image(s, revision="") for s in ("proxy", "engine", "dashboard")}
        check = self.check(app_images(**bare))
        self.assertEqual(check.status, "PASS")
        self.assertIn("no revision labels", check.summary)

    def test_an_image_that_cannot_be_inspected_warns(self) -> None:
        self.assertEqual(self.check({}).status, "WARN")


class FastCheckRateAndProbeTest(unittest.TestCase):
    """decision_model.rpm and decision_model.probe (--probe only, D129)."""

    def doctor(self, status: HttpResult, post: Optional[FakePost] = None, **kwargs: Any) -> Doctor:
        routes = demo_routes(traces("completed"))
        routes[f"{PROXY}/api/status"] = status
        options: Dict[str, Any] = dict(project="truthtable", expect=None)
        options.update(kwargs)
        return make_doctor(
            FakeDocker(demo_containers()),
            FakeHttp(routes),
            FakeClock(),
            post=post or FakePost(),
            **options,
        )

    def test_rpm_headroom(self) -> None:
        self.assertEqual(self.doctor(system_status()).check_rpm().status, "SKIP")
        check = self.doctor(real_status(rpm_limit=600, rpm_used=90)).check_rpm()
        self.assertEqual(
            (check.status, check.summary), ("PASS", "90 of 600 fast-check calls a minute (15%)")
        )
        check = self.doctor(real_status(rpm_limit=600, rpm_used=540)).check_rpm()
        self.assertEqual(check.status, "WARN")
        self.assertIn("rate_limited", check.summary)
        self.assertIn("TRUTHTABLE_GATE_JEV_RPM", check.fix)
        self.assertEqual(self.doctor(real_status(rpm_limit=0)).check_rpm().status, "SKIP")

    def test_no_probe_never_posts(self) -> None:
        post = FakePost(ok_json({"ok": True}))
        d = self.doctor(real_status(), post)
        results = run_all(d)
        self.assertEqual(results["decision_model.probe"].status, "SKIP")
        self.assertIn("--probe --admin-key-file", results["decision_model.probe"].summary)
        self.assertEqual(post.calls, [])
        # Through main too, with and without --no-probe.
        for argv in (["--no-probe"], []):
            post = FakePost(ok_json({"ok": True}))
            with contextlib.redirect_stdout(io.StringIO()):
                doctor.main(
                    ["--target", "docker", "--json", *argv],
                    run=FakeDocker(demo_containers()),
                    fetch=FakeHttp(demo_routes(traces("completed"))),
                    environ={"TRAFFICGEN_API_KEY": "k"},
                    post=post,
                    probe_port=lambda host, port: True,
                    env_file=NO_ENV_FILE,
                )
            self.assertEqual(post.calls, [], argv)

    def test_a_probe_that_passes(self) -> None:
        result = {
            "ok": True,
            "backend": "openrouter-decisions",
            "snapshot": "typesafe/jev-1.13",
            "latency_ms": 231,
            "cost_usd": 0.0000084,
            "cost_source": "reported",
            "error": None,
        }
        post = FakePost(ok_json(result))
        d = self.doctor(real_status(), post, probe=True, admin_key=ADMIN_KEY)
        check = d.check_probe()
        self.assertEqual(check.status, "PASS")
        self.assertEqual(
            check.summary,
            "probe ok: openrouter-decisions, snapshot typesafe/jev-1.13, 231 ms, $0.000008 (vendor)",
        )
        url, headers, body = post.calls[0]
        self.assertEqual(url, f"{PROXY}/api/decision-model/test")
        self.assertEqual(headers["X-TrustAgent-Key"], ADMIN_KEY)
        self.assertEqual(body, b"{}")

    def test_probe_errors_follow_the_spec_errors_table(self) -> None:
        cases = {
            "auth": ("FAIL", "key rejected (401/403)", "reconnect OpenRouter"),
            "credits": ("FAIL", "no credits (402)", "openrouter.ai/settings/credits"),
            "data_policy": ("FAIL", "privacy settings", "openrouter.ai/settings/privacy"),
            "bad_content_type": ("FAIL", "got a web page", "TRUTHTABLE_JEV_BASE_URL"),
            "retryable": ("WARN", "provider busy", "nothing to do"),
            "timeout": ("WARN", "timed out", "fails open"),
            "something_new": ("FAIL", "the probe failed (something_new)", "a hint"),
        }
        for kind, (status, meaning, fix) in cases.items():
            with self.subTest(kind=kind):
                body = {
                    "ok": False,
                    "backend": "openrouter-decisions",
                    "error": {"kind": kind, "status": 402, "message": "m", "hint": "a hint"},
                }
                d = self.doctor(
                    real_status(), FakePost(ok_json(body)), probe=True, admin_key=ADMIN_KEY
                )
                check = d.check_probe()
                self.assertEqual(check.status, status)
                self.assertIn(meaning, check.summary)
                self.assertIn(fix, check.fix)
                self.assertEqual(check.data["error_kind"], kind)

    def test_a_refused_admin_key(self) -> None:
        post = FakePost(HttpResult(status=403, error="forbidden"))
        check = self.doctor(real_status(), post, probe=True, admin_key=ADMIN_KEY).check_probe()
        self.assertEqual(check.status, "FAIL")
        self.assertIn("no admin key", check.fix)

    def test_the_admin_key_is_never_printed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            key_file = Path(tmp) / "admin.key"
            key_file.write_text(ADMIN_KEY + "\n", encoding="utf-8")
            # A component that echoes the key back in its error message.
            body = {
                "ok": False,
                "backend": "openrouter-decisions",
                "error": {"kind": "auth", "message": f"key {ADMIN_KEY} rejected", "hint": ""},
            }
            for fmt in (["--json"], []):
                out = io.StringIO()
                with contextlib.redirect_stdout(out):
                    code = doctor.main(
                        ["--target", "docker", "--probe", "--admin-key-file", str(key_file), *fmt],
                        run=FakeDocker(demo_containers()),
                        fetch=FakeHttp(
                            {
                                **demo_routes(traces("completed")),
                                f"{PROXY}/api/status": real_status(),
                            }
                        ),
                        environ={"NO_COLOR": "1"},
                        post=FakePost(ok_json(body)),
                        probe_port=lambda host, port: True,
                        env_file=NO_ENV_FILE,
                    )
                self.assertEqual(code, 1, "a rejected key fails the probe")
                self.assertNotIn(ADMIN_KEY, out.getvalue())
                self.assertIn("[redacted]", out.getvalue())

    def test_probe_usage(self) -> None:
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.assertEqual(doctor.main(["--probe"], env_file=NO_ENV_FILE), 2)
        self.assertIn("--probe needs --admin-key-file", err.getvalue())
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            doctor.main(["--probe", "--no-probe"])
        with tempfile.TemporaryDirectory() as tmp:
            empty = Path(tmp) / "empty.key"
            empty.write_text("\n", encoding="utf-8")
            err = io.StringIO()
            with contextlib.redirect_stderr(err):
                code = doctor.main(
                    ["--admin-key-file", str(empty), "--probe"], env_file=NO_ENV_FILE
                )
            self.assertEqual(code, 2)
            self.assertIn("is empty", err.getvalue())

    def test_an_api_key_file_replaces_the_trafficgen_key(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            key_file = Path(tmp) / "read.key"
            key_file.write_text(LEAK_KEY, encoding="utf-8")
            http = FakeHttp(demo_routes(traces("completed")))
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                doctor.main(
                    [
                        "--target",
                        "host",
                        "--api-key-file",
                        str(key_file),
                        "--only",
                        "decision_model",
                    ],
                    fetch=http,
                    environ={"TRAFFICGEN_API_KEY": "tg-key"},
                    env_file=NO_ENV_FILE,
                )
            sent = {h.get("X-TrustAgent-Key") for _, h in http.calls if h}
            self.assertEqual(sent, {LEAK_KEY})
            self.assertNotIn(LEAK_KEY, out.getvalue())


class ConfigAndAddonChecksTest(unittest.TestCase):
    """config.price_table (CO-12) and profiles.addons (D129)."""

    def doctor(
        self, status: Dict[str, Any], inspect: List[Dict[str, Any]], **kwargs: Any
    ) -> Doctor:
        routes = demo_routes(traces("completed"))
        routes[f"{PROXY}/api/status"] = ok_json(status)
        options: Dict[str, Any] = dict(project="truthtable", expect=None)
        options.update(kwargs)
        return make_doctor(FakeDocker(inspect), FakeHttp(routes), FakeClock(), **options)

    def test_an_unusable_price_table_file_warns(self) -> None:
        status = status_body()
        status["warnings"] = [
            "TRUTHTABLE_PRICE_TABLE_FILE: /etc/trustagent/prices.json: no such file; costs are "
            "priced from the built-in table until the file is fixed or the key is unset"
        ]
        check = self.doctor(status, demo_containers()).check_price_table()
        self.assertEqual(check.status, "WARN")
        self.assertIn("built-in", check.summary)
        self.assertIn("prices.json", check.detail)
        self.assertIn("unset TRUTHTABLE_PRICE_TABLE_FILE", check.fix)
        check = self.doctor(status_body(), demo_containers()).check_price_table()
        self.assertEqual((check.status, check.summary), ("PASS", "the built-in price table"))
        inspect = demo_containers()
        inspect[0] = container("proxy", env=["TRUTHTABLE_PRICE_TABLE_FILE=/etc/trustagent/p.json"])
        check = self.doctor(status_body(), inspect).check_price_table()
        self.assertEqual(check.summary, "the price table file loads")

    def test_addons_run_and_are_used(self) -> None:
        status = status_body()
        status["knowledge"] = {"state": "up", "enabled": True}
        status["redis"] = {"state": "up", "configured": True}
        inspect = demo_containers() + [container("qdrant"), container("redis")]
        env = {"COMPOSE_PROFILES": "knowledge,redis"}
        check = self.doctor(status, inspect, environ=env).check_addons()
        self.assertEqual(check.status, "PASS")
        self.assertEqual(check.summary, "add-ons: knowledge (qdrant ok); redis (redis ok)")

    def test_an_enabled_profile_that_does_not_run_warns(self) -> None:
        env = {"COMPOSE_PROFILES": "observability"}
        check = self.doctor(status_body(), demo_containers(), environ=env).check_addons()
        self.assertEqual(check.status, "WARN")
        self.assertIn("prometheus, grafana is not running", check.summary)
        status = status_body()
        status["knowledge"] = {"state": "disabled", "enabled": False}
        inspect = demo_containers() + [container("qdrant")]
        check = self.doctor(
            status, inspect, environ={"COMPOSE_PROFILES": "knowledge"}
        ).check_addons()
        self.assertEqual(check.status, "WARN")
        self.assertIn("TRUTHTABLE_KNOWLEDGE_ENABLED is not true", check.summary)

    def test_no_addons(self) -> None:
        check = self.doctor(status_body(), demo_containers()).check_addons()
        self.assertEqual(check.status, "PASS")
        self.assertIn("no add-on profile", check.summary)


def fresh_routes() -> Dict[str, Any]:
    routes = demo_routes(traces())
    body = status_body(
        decision_model(
            backend="off",
            backend_reason="no decision-model key",
            state="off",
            calibration_state="none",
            calibrated_targets=0,
            rpm_used=0,
        )
    )
    body["demo"] = False
    body["judge"] = judge(provider=None, model=None, state="down", credential_source="none")
    body["judge"]["provider"] = "none"
    routes[f"{PROXY}/api/status"] = ok_json(body)
    routes[f"{PROXY}/api/setup/status"] = setup_status(required=True)
    routes[f"{PROXY}/health"] = ok_json({"status": "ok", "demo": False, "audit_engine": True})
    return routes


class FreshAndPagesTest(unittest.TestCase):
    """--expect fresh (a new install before setup) and --target pages (the static site)."""

    def fresh(self, routes: Dict[str, Any]) -> Dict[str, doctor.Check]:
        inspect = [
            container("proxy", ports=[8080]),
            container("engine"),
            container("postgres"),
            container("dashboard", ports=[3000]),
        ]
        d = make_doctor(
            FakeDocker(inspect),
            FakeHttp(routes),
            FakeClock(),
            project="truthtable",
            expect="fresh",
            wait_s=0.0,
        )
        return run_all(d)

    def test_a_fresh_install_passes(self) -> None:
        results = self.fresh(fresh_routes())
        self.assertNotIn("demo.mode", results)
        self.assertNotIn("audits.completed", results)
        bad = {k: c.summary for k, c in results.items() if c.status in ("WARN", "FAIL")}
        self.assertEqual(bad, {})
        self.assertIn("a fresh install", results["setup.status"].summary)
        self.assertIn("no key yet", results["decision_model.state"].summary)
        self.assertIn("no traces yet", results["traces.recent"].summary)

    def test_a_used_install_is_not_fresh(self) -> None:
        routes = fresh_routes()
        routes[f"{PROXY}/api/setup/status"] = setup_status(required=False)
        routes[f"{PROXY}/api/traces?limit=50"] = traces("completed")
        results = self.fresh(routes)
        self.assertEqual(results["setup.status"].status, "FAIL")
        self.assertIn("an admin already exists", results["setup.status"].summary)
        self.assertEqual(results["traces.recent"].status, "WARN")

    def site(self, **overrides: HttpResult) -> Dict[str, HttpResult]:
        base = "https://example.test/TrustAgent/"
        csp = '<meta http-equiv="Content-Security-Policy" content="default-src \'self\'">'
        app = f'<html><head>{csp}<script src="/TrustAgent/assets/app.js"></script></head></html>'
        landing = f'<html><head>{csp}<link href="/TrustAgent/assets/landing.css"></head></html>'
        manifest = {
            "schema": "trustagent.recording/v1",
            "kind": "mock",
            "label": "Simulated (mock checks + mock judge): not model output",
            "traces": [{"trace_id": "t1"}],
        }
        routes = {
            base: HttpResult(status=200, body=landing.encode()),
            base + "app/": HttpResult(status=200, body=app.encode()),
            base + "404.html": HttpResult(status=200, body=app.encode()),
            base + "demo/manifest.json": ok_json(manifest),
            base + "demo/traces/t1.json": ok_json({"trace_id": "t1"}),
        }
        routes.update(
            {base + k.replace("_", "/") if k != "root" else base: v for k, v in overrides.items()}
        )
        return routes

    def pages(
        self, routes: Dict[str, HttpResult], argv: Sequence[str] = ()
    ) -> Tuple[int, Dict[str, Any]]:
        out = io.StringIO()
        docker = FakeDocker([])
        with contextlib.redirect_stdout(out):
            code = doctor.main(
                [
                    "--target",
                    "pages",
                    "--pages-url",
                    "https://example.test/TrustAgent",
                    "--json",
                    *argv,
                ],
                run=docker,
                fetch=FakeHttp(routes),
                environ={},
                post=FakePost(),
                env_file=NO_ENV_FILE,
            )
        self.assertEqual(docker.calls, [], "the pages target never asks docker")
        return code, json.loads(out.getvalue())

    def test_the_static_site_passes(self) -> None:
        code, report = self.pages(self.site())
        self.assertEqual(code, 0)
        self.assertEqual(report["target"], "pages")
        ids = [c["id"] for c in report["checks"]]
        self.assertEqual(ids, ["pages.landing", "pages.app", "pages.fallback", "pages.dataset"])
        self.assertEqual({c["status"] for c in report["checks"]}, {"PASS"})
        dataset = report["checks"][3]
        self.assertIn("simulated tape, 1 traces", dataset["summary"])
        self.assertEqual(dataset["data"]["kind"], "mock")

    def test_a_broken_site_fails(self) -> None:
        no_csp = HttpResult(status=200, body=b'<html><script src="/assets/app.js"></script></html>')
        code, report = self.pages(self.site(root=no_csp))
        self.assertEqual(code, 1)
        landing = report["checks"][0]
        self.assertEqual(landing["status"], "FAIL")
        self.assertIn("no Content-Security-Policy meta", landing["summary"])
        self.assertIn("loads no asset under /TrustAgent/", landing["summary"])

    def test_a_mislabelled_or_incomplete_dataset_fails(self) -> None:
        bad = ok_json({"kind": "mock", "label": "Recorded", "traces": []})
        code, report = self.pages(self.site(**{"demo/manifest.json": bad}))
        self.assertEqual(report["checks"][3]["status"], "FAIL")
        self.assertIn("not labelled simulated", report["checks"][3]["summary"])
        routes = self.site()
        del routes["https://example.test/TrustAgent/demo/traces/t1.json"]
        code, report = self.pages(routes)
        self.assertEqual(code, 1)
        self.assertIn("does not serve", report["checks"][3]["summary"])

    def test_a_404_that_is_not_the_app_warns(self) -> None:
        other = HttpResult(status=200, body=b"<html>Not found</html>")
        code, report = self.pages(self.site(**{"404.html": other}))
        self.assertEqual(code, 0)
        self.assertEqual(report["checks"][2]["status"], "WARN")

    def test_pages_usage(self) -> None:
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            code = doctor.main(["--target", "pages", "--expect", "demo"], env_file=NO_ENV_FILE)
        self.assertEqual(code, 2)
        with contextlib.redirect_stderr(err):
            code = doctor.main(
                ["--target", "pages", "--pages-url", "ftp://x"], env_file=NO_ENV_FILE
            )
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
