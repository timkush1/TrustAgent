#!/usr/bin/env python3
"""Diagnose a running TrustAgent stack (standard library only).

    python3 scripts/doctor.py                                  # auto-detect
    python3 scripts/doctor.py --target docker --no-probe --expect demo
    python3 scripts/doctor.py --json

Every check reports ``{id, status, summary, detail, fix}`` with status PASS,
WARN, FAIL or SKIP; ``fix`` is a command to paste. Exit code 0 means no FAIL,
1 means at least one FAIL, 2 is a usage error.

Checks in this version (the S0 skeleton; SPEC §3.4, §4.2):

  compose.services  every container of the compose project is running and
                    healthy (docker target)
  proxy.live        GET /health on the proxy
  engine.health     `python -m truthtable.healthcheck` inside the engine
                    container, cross-checked with /health audit_engine
  dashboard.live    the dashboard's own /healthz, /config.json and its
                    same-origin /health proxy (docker target)
  demo.mode         --expect demo: demo mode on, mock decision model and judge,
                    no vendor key in the proxy or engine environment
  traces.recent     GET /api/traces returns traces (waits for them with
                    --expect demo)
  audits.completed  --expect demo: at least one trace has a completed deep audit

No check makes a billed call; --no-probe is accepted for the documented
interface and keeps it that way when live probes are added.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import resolve  # noqa: E402

DEFAULT_PROJECT = "truthtable"
DEMO_PROJECT = "truthtable-demo"
CORE_SERVICES = ("proxy", "engine", "postgres", "dashboard")
DEMO_SERVICES = CORE_SERVICES + ("trafficgen",)
# One-shot services: exiting with code 0 is their success state.
ONE_SHOT_SERVICES = frozenset({"ollama-init"})
# Credentials that must not reach the demo stack (docker-compose.demo.yml).
VENDOR_KEYS = (
    "OPENROUTER_API_KEY",
    "TYPESAFE_API_KEY",
    "TRUTHTABLE_JEV_API_KEY",
    "TRUTHTABLE_UPSTREAM_API_KEY",
    "TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY",
    "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY",
    "ANTHROPIC_API_KEY",
    "OPENAI_API_KEY",
    "LANGSMITH_API_KEY",
)
STATUSES = ("PASS", "WARN", "FAIL", "SKIP")
HTTP_TIMEOUT_S = 5.0
POLL_INTERVAL_S = 3.0


@dataclass
class Check:
    id: str
    status: str
    summary: str
    detail: str = ""
    fix: str = ""


@dataclass
class HttpResult:
    status: int  # 0 when no response arrived
    body: bytes = b""
    error: str = ""

    def json(self) -> Any:
        return json.loads(self.body.decode("utf-8"))


@dataclass
class Traces:
    items: Optional[List[Dict[str, Any]]]  # None when the request failed
    error: str
    status: int


@dataclass
class Container:
    name: str
    service: str
    state: str  # running | exited | restarting | created | paused | dead
    health: Optional[str]  # healthy | unhealthy | starting | None (no healthcheck)
    exit_code: int
    env: Dict[str, str] = field(default_factory=dict)


Runner = Callable[[Sequence[str], float], Tuple[int, str, str]]
Fetcher = Callable[[str, Mapping[str, str]], HttpResult]


def run_command(argv: Sequence[str], timeout: float) -> Tuple[int, str, str]:
    """Run a command; (returncode, stdout, stderr). 127 when it cannot be started."""
    try:
        done = subprocess.run(
            list(argv), capture_output=True, text=True, timeout=timeout, check=False
        )
    except FileNotFoundError:
        return 127, "", f"{argv[0]}: command not found"
    except subprocess.TimeoutExpired:
        return 124, "", f"timed out after {timeout:.0f}s"
    except OSError as exc:
        return 126, "", str(exc)
    return done.returncode, done.stdout, done.stderr


def http_get(url: str, headers: Mapping[str, str]) -> HttpResult:
    """GET ``url``; HTTP errors are results, not exceptions."""
    request = urllib.request.Request(url, headers=dict(headers), method="GET")
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT_S) as response:  # nosec B310
            return HttpResult(status=response.status, body=response.read())
    except urllib.error.HTTPError as exc:
        return HttpResult(status=exc.code, body=exc.read() or b"", error=str(exc))
    except (urllib.error.URLError, OSError, ValueError) as exc:
        reason = getattr(exc, "reason", exc)
        return HttpResult(status=0, error=str(reason))


def _host_for(bind_addr: str) -> str:
    if bind_addr in ("", "0.0.0.0", "::", "[::]"):  # nosec B104 - a bind address, not a bind
        return "127.0.0.1"
    return bind_addr


@dataclass
class Doctor:
    target: str
    project: str
    proxy_url: str
    dashboard_url: str
    expect: Optional[str] = None
    api_key: Optional[str] = None
    wait_s: float = 0.0
    run: Runner = run_command
    fetch: Fetcher = http_get
    sleep: Callable[[float], None] = time.sleep
    clock: Callable[[], float] = time.monotonic
    # The project `make up` / `make logs` address (COMPOSE_PROJECT_NAME or truthtable).
    default_project: str = DEFAULT_PROJECT
    containers: Optional[List[Container]] = None
    health: Optional[Dict[str, Any]] = None

    # ---------- docker ----------

    def load_containers(self) -> Optional[str]:
        """Read the project's containers; an error message when docker cannot be asked."""
        code, out, err = self.run(
            [
                "docker",
                "ps",
                "--all",
                "--quiet",
                "--filter",
                f"label=com.docker.compose.project={self.project}",
            ],
            20,
        )
        if code != 0:
            return (err or out).strip() or f"docker ps exited {code}"
        ids = out.split()
        if not ids:
            self.containers = []
            return None
        code, out, err = self.run(["docker", "inspect", *ids], 20)
        if code != 0:
            return (err or out).strip() or f"docker inspect exited {code}"
        self.containers = [c for c in (parse_container(item) for item in json.loads(out)) if c]
        return None

    def logs_hint(self, service: str) -> str:
        """The command that shows a service's logs for this stack."""
        if self.target != "docker":
            return f"the {service} terminal (make dev-*)"
        if self.project == DEMO_PROJECT:
            return f"make demo-logs SERVICE={service}"
        if self.project == self.default_project:
            return f"make logs SERVICE={service}"
        return f"docker compose -p {self.project} logs {service}"

    def ps_hint(self) -> str:
        return f"docker ps --all --filter label=com.docker.compose.project={self.project}"

    def service(self, name: str) -> Optional[Container]:
        if self.containers is None and self.load_containers() is not None:
            return None
        return next((c for c in self.containers or [] if c.service == name), None)

    # ---------- checks ----------

    def check_services(self) -> Check:
        error = self.load_containers()
        if error is not None:
            return Check(
                "compose.services",
                "FAIL",
                "cannot list the project's containers",
                detail=error,
                fix="start Docker (Docker Desktop: enable WSL integration for this distro)",
            )
        start = "make demo" if self.expect == "demo" else "make up"
        required = DEMO_SERVICES if self.expect == "demo" else CORE_SERVICES
        self.containers = self.containers or []
        if not self.containers:
            return Check(
                "compose.services",
                "FAIL",
                f"no containers in compose project {self.project}",
                fix=start,
            )
        problems: List[str] = []
        waiting: List[str] = []
        for c in sorted(self.containers, key=lambda c: c.service):
            state = describe_state(c)
            if state == "ok":
                continue
            if state == "starting":
                waiting.append(c.service)
            else:
                problems.append(f"{c.service} {state}")
        present = {c.service for c in self.containers}
        missing = [s for s in required if s not in present]
        problems += [f"{s} missing" for s in missing]
        services = ", ".join(sorted(present))
        if problems:
            worst = next((p.split()[0] for p in problems if not p.endswith("missing")), None)
            fix = self.logs_hint(worst) if worst else start
            return Check(
                "compose.services",
                "FAIL",
                "; ".join(problems),
                detail=f"containers: {services}",
                fix=fix,
            )
        if waiting:
            return Check(
                "compose.services",
                "WARN",
                "still starting: " + ", ".join(waiting),
                detail=f"containers: {services}",
                fix=self.ps_hint(),
            )
        return Check("compose.services", "PASS", f"all healthy: {services}")

    def check_proxy(self) -> Check:
        result = self.fetch(f"{self.proxy_url}/health", {})
        fix = self.logs_hint("proxy") if self.target == "docker" else "make dev-go"
        if result.status != 200:
            what = f"HTTP {result.status}" if result.status else result.error
            return Check("proxy.live", "FAIL", f"{self.proxy_url}/health: {what}", fix=fix)
        try:
            body = result.json()
        except ValueError:
            return Check("proxy.live", "FAIL", "/health did not return JSON", fix=fix)
        if not isinstance(body, dict):
            return Check("proxy.live", "FAIL", "/health did not return a JSON object", fix=fix)
        self.health = body
        detail = ", ".join(
            f"{key}={body[key]}" for key in ("version", "demo", "audit_engine") if key in body
        )
        if body.get("status") != "ok":
            return Check(
                "proxy.live",
                "WARN",
                f"/health answered with status {body.get('status')!r}, expected 'ok'",
                detail=detail,
            )
        return Check("proxy.live", "PASS", f"{self.proxy_url}/health ok", detail=detail)

    def check_engine(self) -> Check:
        linked = None if self.health is None else self.health.get("audit_engine")
        if self.target != "docker":
            if linked is True:
                return Check("engine.health", "PASS", "the proxy reaches the engine")
            if linked is False:
                return Check(
                    "engine.health",
                    "FAIL",
                    "the proxy cannot reach the engine (/health audit_engine=false)",
                    fix="make dev-python",
                )
            return Check("engine.health", "SKIP", "no /health answer to read audit_engine from")
        engine = self.service("engine")
        if engine is None:
            return Check("engine.health", "FAIL", "no engine container", fix="make up")
        code, out, err = self.run(
            ["docker", "exec", engine.name, "python", "-m", "truthtable.healthcheck"], 30
        )
        if code != 0:
            output = (err or out).strip().splitlines()
            return Check(
                "engine.health",
                "FAIL",
                f"truthtable.healthcheck exited {code}",
                detail=output[-1] if output else "",
                fix=self.logs_hint("engine"),
            )
        if linked is False:
            return Check(
                "engine.health",
                "WARN",
                "the engine is healthy but the proxy cannot reach it (/health audit_engine=false)",
                fix=self.logs_hint("proxy"),
            )
        return Check("engine.health", "PASS", "engine answers its gRPC health check")

    def check_dashboard(self) -> Check:
        if self.target != "docker":
            return Check(
                "dashboard.live", "SKIP", "host target: the dashboard runs with make dev-react"
            )
        fix = self.logs_hint("dashboard")
        live = self.fetch(f"{self.dashboard_url}/healthz", {})
        if live.status != 200:
            what = f"HTTP {live.status}" if live.status else live.error
            return Check("dashboard.live", "FAIL", f"{self.dashboard_url}/healthz: {what}", fix=fix)
        config = self.fetch(f"{self.dashboard_url}/config.json", {})
        try:
            parsed = config.json() if config.status == 200 else None
        except ValueError:
            parsed = None
        if not isinstance(parsed, dict):
            return Check(
                "dashboard.live",
                "FAIL",
                f"/config.json is not a JSON object (HTTP {config.status})",
                fix=fix,
            )
        proxied = self.fetch(f"{self.dashboard_url}/health", {})
        if proxied.status != 200:
            what = f"HTTP {proxied.status}" if proxied.status else proxied.error
            return Check(
                "dashboard.live",
                "FAIL",
                f"the dashboard cannot reach the proxy: /health {what}",
                fix=fix,
            )
        return Check(
            "dashboard.live", "PASS", f"{self.dashboard_url} serves the app and proxies /health"
        )

    def check_demo_mode(self) -> Check:
        problems: List[str] = []
        if self.health is not None and self.health.get("demo") is not True:
            problems.append("/health reports demo=false")
        if self.target == "docker":
            proxy, engine = self.service("proxy"), self.service("engine")
            if proxy is not None:
                if proxy.env.get("TRUTHTABLE_DEMO_MODE", "").lower() != "true":
                    problems.append("proxy: TRUTHTABLE_DEMO_MODE is not true")
                if proxy.env.get("TRUTHTABLE_JEV_BACKEND") != "mock":
                    problems.append("proxy: TRUTHTABLE_JEV_BACKEND is not mock")
            if engine is not None and engine.env.get("LLM_PROVIDER") != "mock":
                problems.append("engine: LLM_PROVIDER is not mock")
            for c in (proxy, engine):
                if c is None:
                    continue
                leaked = [key for key in VENDOR_KEYS if c.env.get(key)]
                if leaked:
                    problems.append(f"{c.service}: vendor keys set ({', '.join(leaked)})")
        if problems:
            return Check(
                "demo.mode",
                "FAIL",
                "; ".join(problems),
                fix="make demo-down && make demo (docker-compose.demo.yml sets the mocks)",
            )
        return Check("demo.mode", "PASS", "mock decision model and judge; no vendor keys ($0)")

    def fetch_traces(self) -> Traces:
        headers = {"X-TrustAgent-Key": self.api_key} if self.api_key else {}
        result = self.fetch(f"{self.proxy_url}/api/traces?limit=50", headers)
        if result.status != 200:
            what = f"HTTP {result.status}" if result.status else result.error
            return Traces(None, f"/api/traces: {what}", result.status)
        try:
            body = result.json()
        except ValueError:
            return Traces(None, "/api/traces did not return JSON", result.status)
        items = body.get("items") if isinstance(body, dict) else None
        if not isinstance(items, list):
            return Traces(None, "/api/traces returned no items list", result.status)
        return Traces([item for item in items if isinstance(item, dict)], "", result.status)

    def wait_for(self, predicate: Callable[[List[Dict[str, Any]]], bool]) -> Traces:
        """Poll /api/traces until ``predicate`` holds or ``wait_s`` runs out.

        A client error (401, 403, 404) is final: waiting cannot fix it.
        """
        deadline = self.clock() + self.wait_s
        while True:
            traces = self.fetch_traces()
            if traces.items is not None and predicate(traces.items):
                return traces
            if 400 <= traces.status < 500 or self.clock() >= deadline:
                return traces
            self.sleep(POLL_INTERVAL_S)

    def check_traces(self) -> Check:
        traces = self.wait_for(lambda items: bool(items))
        items = traces.items
        demo = self.expect == "demo"
        if items is None:
            fix = "check TRAFFICGEN_API_KEY and TRUTHTABLE_API_KEYS in .env (make configure)"
            if traces.status == 404:
                fix = "this proxy build has no /api/traces; rebuild it (make demo)"
            elif traces.status == 0:
                fix = self.logs_hint("proxy")
            return Check("traces.recent", "FAIL" if demo else "WARN", traces.error, fix=fix)
        if not items:
            waited = f" after {self.wait_s:.0f}s" if self.wait_s else ""
            fix = self.logs_hint("trafficgen") if demo else "send a request to /v1"
            return Check(
                "traces.recent", "FAIL" if demo else "WARN", f"no traces yet{waited}", fix=fix
            )
        latest = items[0]
        detail = ", ".join(
            f"{key}={latest.get(key)}"
            for key in ("route", "action", "gate_status")
            if key in latest
        )
        return Check(
            "traces.recent", "PASS", f"{len(items)} recent trace(s)", detail=f"latest: {detail}"
        )

    def check_audits(self) -> Check:
        traces = self.wait_for(lambda items: any(audit_status(i) == "completed" for i in items))
        items = traces.items
        if items is None:
            return Check("audits.completed", "FAIL", traces.error)
        completed = [i for i in items if audit_status(i) == "completed"]
        if not completed:
            waited = f" after {self.wait_s:.0f}s" if self.wait_s else ""
            states = sorted({audit_status(i) or "none" for i in items})
            return Check(
                "audits.completed",
                "FAIL",
                f"no completed deep audit yet{waited}",
                detail="audit states seen: " + ", ".join(states),
                fix=self.logs_hint("engine"),
            )
        verdicts = sorted({str(i["audit"].get("verdict")) for i in completed})
        return Check(
            "audits.completed",
            "PASS",
            f"{len(completed)} completed deep audit(s)",
            detail="verdicts: " + ", ".join(verdicts),
        )

    def checks(self) -> List[Tuple[str, Callable[[], Check]]]:
        plan: List[Tuple[str, Callable[[], Check]]] = []
        if self.target == "docker":
            plan.append(("compose.services", self.check_services))
        plan += [
            ("proxy.live", self.check_proxy),
            ("engine.health", self.check_engine),
            ("dashboard.live", self.check_dashboard),
        ]
        if self.expect == "demo":
            plan.append(("demo.mode", self.check_demo_mode))
        plan.append(("traces.recent", self.check_traces))
        if self.expect == "demo":
            plan.append(("audits.completed", self.check_audits))
        return plan


def parse_container(item: Mapping[str, Any]) -> Optional[Container]:
    config = item.get("Config") or {}
    labels = config.get("Labels") or {}
    service = labels.get("com.docker.compose.service")
    if not service or labels.get("com.docker.compose.oneoff") == "True":
        return None
    state = item.get("State") or {}
    health = (state.get("Health") or {}).get("Status")
    env: Dict[str, str] = {}
    for entry in config.get("Env") or []:
        key, _, value = str(entry).partition("=")
        env[key] = value
    return Container(
        name=str(item.get("Name", "")).lstrip("/"),
        service=service,
        state=str(state.get("Status", "unknown")),
        health=health,
        exit_code=int(state.get("ExitCode", 0) or 0),
        env=env,
    )


def describe_state(c: Container) -> str:
    """'ok', 'starting', or a short description of what is wrong."""
    if c.service in ONE_SHOT_SERVICES and c.state == "exited":
        return "ok" if c.exit_code == 0 else f"exited {c.exit_code}"
    if c.state != "running":
        return f"{c.state} (exit {c.exit_code})" if c.state == "exited" else c.state
    if c.health is None or c.health == "healthy":
        return "ok"
    return c.health


def audit_status(item: Mapping[str, Any]) -> Optional[str]:
    audit = item.get("audit")
    return audit.get("status") if isinstance(audit, dict) else None


def detect_target(project: str, run: Runner) -> str:
    """docker when the project has a running proxy container, host otherwise."""
    if shutil.which("docker") is None:
        return "host"
    code, out, _ = run(
        [
            "docker",
            "ps",
            "--quiet",
            "--filter",
            f"label=com.docker.compose.project={project}",
            "--filter",
            "label=com.docker.compose.service=proxy",
        ],
        20,
    )
    return "docker" if code == 0 and out.strip() else "host"


def selected(check_id: str, only: Sequence[str], skip: Sequence[str]) -> bool:
    def matches(patterns: Sequence[str]) -> bool:
        return any(check_id == p or check_id.startswith(p.rstrip(".*") + ".") for p in patterns)

    return (not only or matches(only)) and not matches(skip)


def render_text(target: str, project: str, results: Sequence[Check], color: bool) -> str:
    palette = {"PASS": "32", "WARN": "33", "FAIL": "31", "SKIP": "90"}
    lines = [f"TrustAgent doctor: target {target}, project {project}"]
    width = max((len(c.id) for c in results), default=0)
    for c in results:
        status = f"\033[{palette[c.status]}m{c.status}\033[0m" if color else c.status
        lines.append(f"  {status}  {c.id.ljust(width)}  {c.summary}")
        if c.detail:
            lines.append(f"        {' ' * width}  {c.detail}")
        if c.fix and c.status in ("WARN", "FAIL"):
            lines.append(f"        {' ' * width}  fix: {c.fix}")
    counts = {s: sum(1 for c in results if c.status == s) for s in STATUSES}
    lines.append(
        f"{counts['FAIL']} failed, {counts['WARN']} warning(s), {counts['PASS']} passed, "
        f"{counts['SKIP']} skipped"
    )
    return "\n".join(lines)


def split_ids(value: str) -> List[str]:
    return [part.strip() for part in value.split(",") if part.strip()]


def main(
    argv: Optional[Sequence[str]] = None,
    run: Runner = run_command,
    fetch: Fetcher = http_get,
    environ: Optional[Mapping[str, str]] = None,
) -> int:
    env = os.environ if environ is None else environ
    parser = argparse.ArgumentParser(
        prog="doctor.py",
        description="Diagnose a running TrustAgent stack. Exit 0: no FAIL; 1: a check failed.",
    )
    parser.add_argument(
        "--target",
        choices=("auto", "docker", "host"),
        default="auto",
        help="docker: the compose stack; host: services run with make dev-*; "
        "auto: docker when the project's proxy container is running (default)",
    )
    parser.add_argument(
        "--expect",
        choices=("demo",),
        help="assert the zero-key demo: demo mode, mocks only, traces and a completed audit",
    )
    parser.add_argument(
        "--no-probe",
        action="store_true",
        help="make no billed call (no check in this version makes one)",
    )
    parser.add_argument("--json", action="store_true", help="print the results as JSON")
    parser.add_argument(
        "--project",
        help="compose project (default: truthtable-demo with --expect demo, else "
        "COMPOSE_PROJECT_NAME or truthtable)",
    )
    parser.add_argument(
        "--proxy-url", help="proxy base URL (default: from BIND_ADDR/PROXY_HOST_PORT)"
    )
    parser.add_argument(
        "--dashboard-url", help="dashboard base URL (default: from BIND_ADDR/DASHBOARD_HOST_PORT)"
    )
    parser.add_argument(
        "--wait",
        type=float,
        default=None,
        metavar="SECONDS",
        help="how long to wait for traces and a completed audit (default: 120 with "
        "--expect demo, else 0)",
    )
    parser.add_argument(
        "--only", type=split_ids, default=[], help="comma-separated check ids to run"
    )
    parser.add_argument(
        "--skip", type=split_ids, default=[], help="comma-separated check ids to skip"
    )
    args = parser.parse_args(argv)

    default_project = resolve("COMPOSE_PROJECT_NAME", environ=env) or DEFAULT_PROJECT
    project: str = args.project or (DEMO_PROJECT if args.expect == "demo" else default_project)
    host = _host_for(resolve("BIND_ADDR", "127.0.0.1", environ=env) or "127.0.0.1")
    proxy_port = resolve("PROXY_HOST_PORT", "8080", environ=env)
    dashboard_port = resolve("DASHBOARD_HOST_PORT", "3000", environ=env)
    target = args.target if args.target != "auto" else detect_target(project, run)
    doctor = Doctor(
        target=target,
        project=project,
        default_project=default_project,
        proxy_url=(args.proxy_url or f"http://{host}:{proxy_port}").rstrip("/"),
        dashboard_url=(args.dashboard_url or f"http://{host}:{dashboard_port}").rstrip("/"),
        expect=args.expect,
        api_key=resolve("TRAFFICGEN_API_KEY", environ=env),
        wait_s=args.wait if args.wait is not None else (120.0 if args.expect == "demo" else 0.0),
        run=run,
        fetch=fetch,
    )

    results: List[Check] = []
    for check_id, check in doctor.checks():
        if selected(check_id, args.only, args.skip):
            results.append(check())
    failed = any(c.status == "FAIL" for c in results)

    if args.json:
        report = {
            "target": target,
            "project": doctor.project,
            "expect": args.expect,
            "ok": not failed,
            "checks": [asdict(c) for c in results],
        }
        print(json.dumps(report, indent=2))
    else:
        color = sys.stdout.isatty() and not env.get("NO_COLOR")
        print(render_text(target, doctor.project, results, color))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
