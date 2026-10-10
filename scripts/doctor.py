#!/usr/bin/env python3
"""Diagnose a running TrustAgent stack, or the static site (standard library only).

    python3 scripts/doctor.py                                  # auto-detect
    python3 scripts/doctor.py --target docker --expect demo    # the zero-key demo
    python3 scripts/doctor.py --expect fresh                   # a new install, before setup
    python3 scripts/doctor.py --json --only decision_model,policy
    python3 scripts/doctor.py --probe --admin-key-file admin.key   # one test call, ~$0.00001
    python3 scripts/doctor.py --target pages --pages-url http://127.0.0.1:4173/TrustAgent/

Every check reports ``{id, status, summary, detail, fix, data}`` with status PASS, WARN, FAIL or
SKIP; ``fix`` is a command to paste and ``data`` holds the facts a script may read (--json). Exit
code 0 means no FAIL, 1 means at least one FAIL, 2 is a usage error.

Checks of a stack (SPEC §3.4, §6 "Verify: make doctor", D129), in the order they run:

  compose.services      every container of the compose project is running and
                        healthy (docker target)
  environment.docker    Docker Engine 25 or later, Compose 2.24 or later (docker)
  environment.memory    Docker has at least 4 GB of memory, else WARN (docker)
  environment.ports     the proxy, dashboard and database host ports are this
                        stack's, or free; another program on one warns (docker)
  environment.env_file  .env is mode 0600: it holds keys (not on Windows)
  proxy.live            GET /health on the proxy
  engine.health         `python -m truthtable.healthcheck` inside the engine
                        container, cross-checked with /health audit_engine
  dashboard.live        the dashboard's own /healthz, /config.json and its
                        same-origin /health proxy (docker target)
  demo.mode             --expect demo: demo mode on, mock decision model and
                        judge, no vendor key in the proxy or engine environment
  setup.status          GET /api/setup/status: whether an admin exists yet
                        (else the setup link to claim) and the ingest auth mode;
                        --expect fresh needs no admin yet
  decision_model.state  GET /api/status: the fast-check backend, why it was
                        chosen, its snapshot, state, latency and 24 h calls/cost
  decision_model.rpm    the fast check's calls this minute against
                        TRUTHTABLE_GATE_JEV_RPM; 80% or more warns
  decision_model.probe  --probe only: POST /api/decision-model/test with the
                        admin key (one call, about $0.00001), its error mapped
                        to the SPEC §6 errors table; SKIP by default
  calibration.targets   GET /api/status: calibrated targets (--expect demo
                        needs at least 2: the demo's mock calibration)
  policy.state          GET /api/status and /api/policies: the version in
                        effect, its source, the policy file, route downgrades
  credentials.store     GET /api/status: the encrypted credential store is
                        ready, and every saved key decrypts (a changed master
                        key leaves rows that need re-entering)
  runtime_config.version
                        GET /api/settings and /api/status: the engine applied
                        the settings version the gateway holds (and its epoch)
  judge.credential_source
                        GET /api/status: the deep-audit judge, and whether its
                        key comes from .env, the dashboard or nowhere
  budgets.today         GET /api/status: today's real spend against the daily
                        fast-check and deep-audit budgets
  config.price_table    TRUTHTABLE_PRICE_TABLE_FILE loads (a missing or invalid
                        file warns: costs fall back to the built-in table)
  profiles.addons       the add-on profiles in COMPOSE_PROFILES (knowledge,
                        local-llm, observability, redis) run, and the gateway
                        uses them
  images.version        the proxy, engine and dashboard images are one version,
                        their org.opencontainers.image.revision labels agree
                        with each other and with the running build, and
                        whether they were pulled or built (docker target)
  metrics.gateway       the proxy's :8002/metrics exposes trustagent_gateway_*
                        and no other trustagent_ name (docker target)
  metrics.engine        the engine's :8001/metrics exposes trustagent_engine_*
                        and no other trustagent_ name (docker target)
  traces.recent         GET /api/traces returns traces (waits for them with
                        --expect demo; --expect fresh wants none yet)
  audits.completed      --expect demo: at least one trace has a completed deep
                        audit

Checks of the static site (--target pages, --pages-url; D124):

  pages.landing         the landing page loads, with its CSP and its assets
                        under the base path
  pages.app             the replay dashboard (app/) loads the same way
  pages.fallback        404.html is the app, so deep links open it
  pages.dataset         demo/manifest.json is a labelled recording (simulated
                        tape or a real recording) whose traces are served

--only / --skip take check ids or their first part (``--only metrics``). Only
--probe makes a billed call; the default (--no-probe) reads the proxy's own
state. Keys come from TRAFFICGEN_API_KEY (environment, then .env), or from
--api-key-file / --admin-key-file; no key is ever printed.
"""

from __future__ import annotations

import argparse
import errno
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, Set, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import DEFAULT_ENV_FILE, resolve  # noqa: E402

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
# The images the release publishes; trafficgen and mock-upstream run the proxy image.
APP_SERVICES = ("proxy", "engine", "dashboard")
# Docker Engine 25 and Compose 2.24 (the compose files use !reset and !override, and
# depends_on.required).
MIN_DOCKER = (25, 0)
MIN_COMPOSE = (2, 24)
# The stack's memory limits add up to about 4.6 GB (engine 4 GB, proxy 512 MB, postgres, nginx).
MIN_MEMORY_BYTES = 4 * 1024**3
# Fast-check calls a minute, as a share of TRUTHTABLE_GATE_JEV_RPM, from which doctor warns.
RPM_WARN_SHARE = 0.8
# The add-on compose profiles (SPEC §3.4) and the services each one starts.
ADDON_PROFILES: Dict[str, Tuple[str, ...]] = {
    "knowledge": ("qdrant",),
    "local-llm": ("ollama", "ollama-init"),
    "observability": ("prometheus", "grafana"),
    "redis": ("redis",),
}
# The probe's error kinds (POST /api/decision-model/test, internal/jev kinds), mapped to the
# SPEC §6 "Errors and fixes" table: (status, what it means, the fix).
PROBE_ERRORS: Dict[str, Tuple[str, str, str]] = {
    "auth": ("FAIL", "key rejected (401/403)", "reconnect OpenRouter, or paste a new key"),
    "credits": (
        "FAIL",
        "no credits (402): the account is empty or the key's limit is reached",
        "add credits at openrouter.ai/settings/credits, or raise the key's limit",
    ),
    "data_policy": (
        "FAIL",
        "no endpoint (404): privacy settings exclude the only provider",
        "openrouter.ai/settings/privacy, or unset TRUTHTABLE_JEV_ZDR",
    ),
    "bad_content_type": (
        "FAIL",
        "got a web page (200 HTML): wrong base URL (OpenRouter URLs end in /api)",
        "fix TRUTHTABLE_JEV_BASE_URL, or leave it empty",
    ),
    "not_found": (
        "FAIL",
        "the endpoint or the model was not found (404)",
        "check TRUTHTABLE_JEV_BASE_URL and TRUTHTABLE_JEV_MODEL",
    ),
    "retryable": (
        "WARN",
        "provider busy (429, 529 or 5xx)",
        "nothing to do: retries and the breaker handle it",
    ),
    "rate_limited": (
        "WARN",
        "rate limited by the gateway (TRUTHTABLE_GATE_JEV_RPM)",
        "nothing to do, or raise TRUTHTABLE_GATE_JEV_RPM",
    ),
    "timeout": (
        "WARN",
        "timed out",
        "nothing to do: calls are retried, and an answer passes through unchecked if its "
        "route fails open",
    ),
    "breaker_open": (
        "WARN",
        "paused after repeated vendor errors (breaker open)",
        "nothing to do: it retries by itself",
    ),
    "overload": (
        "WARN",
        "too many fast-check calls in flight in the gateway (TRUTHTABLE_GATE_JEV_MAX_INFLIGHT)",
        "nothing to do: calls go through again as the ones in flight finish",
    ),
    "credentials_unavailable": (
        "FAIL",
        "the stored decision-model key cannot be read",
        "re-enter it in Settings > Connections (see credentials.store)",
    ),
    "budget": (
        "WARN",
        "today's fast-check budget is used up",
        "raise it in Settings > Budgets, or wait for tomorrow's reset",
    ),
    "off": ("WARN", "the fast check is off", "put OPENROUTER_API_KEY in .env, then make up"),
}
HTTP_TIMEOUT_S = 5.0
POLL_INTERVAL_S = 3.0
# The longest engine.health waits for the proxy to reach a healthy engine: two of its default
# 10 s pings, plus slack.
ENGINE_LINK_WAIT_S = 25.0
# The demo's mock calibration covers the evaluators its block and fallback scenarios need
# (config/calibration); fewer targets means it did not load.
MIN_DEMO_CALIBRATED_TARGETS = 2
# Decision-model states (api.ts DecisionModelStatus.state) that mean checks are not running as
# configured, and the WARN-level ones that recover by themselves or need no action to keep serving.
DECISION_MODEL_FAILED = frozenset(
    {"unauthorized", "no_credits", "budget_exhausted", "misconfigured", "unreachable"}
)
DECISION_MODEL_DEGRADED = frozenset({"checking", "degraded", "breaker_open"})
# The judge providers that need a vendor key, and the engine env var that supplies one.
JUDGE_KEY_VARS = {
    "anthropic": "ANTHROPIC_API_KEY",
    "openai": "OPENAI_API_KEY",
    "openrouter": "OPENROUTER_API_KEY",
}
JEV_DOC = "docs/JEV-ACCESS.md"
# The public site (pages.yml deploys it there once the owner turns Pages on).
PAGES_URL = "https://timkush1.github.io/TrustAgent/"
SECURITY_DOC = "docs/SECURITY.md"
# The in-container fetch of the engine's metrics (its image has Python, not wget); the URL is
# passed as an argument, never interpolated into the code.
ENGINE_METRICS_FETCH = (
    "import sys, urllib.request; "
    "sys.stdout.write(urllib.request.urlopen(sys.argv[1], timeout=5).read().decode())"
)
_METRIC_NAME = re.compile(r"[A-Za-z_:][A-Za-z0-9_:]*")


@dataclass
class Check:
    id: str
    status: str
    summary: str
    detail: str = ""
    fix: str = ""
    # Machine-readable facts behind the summary (--json only), for scripts such as
    # record_demo.py: never a key, only names, states and numbers.
    data: Dict[str, Any] = field(default_factory=dict)


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
    image_id: str = ""  # sha256:… of the image it runs
    image: str = ""  # the image reference compose gave it
    ports: List[int] = field(default_factory=list)  # host ports it publishes


Runner = Callable[[Sequence[str], float], Tuple[int, str, str]]
Fetcher = Callable[[str, Mapping[str, str]], HttpResult]
# post(url, headers, body): the one POST doctor can make (--probe only).
Poster = Callable[[str, Mapping[str, str], bytes], HttpResult]
# port_free(host, port): True when nothing listens there, False when something does, None when
# it cannot tell.
PortProbe = Callable[[str, int], Optional[bool]]


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


def http_post(url: str, headers: Mapping[str, str], body: bytes) -> HttpResult:
    """POST ``body``; HTTP errors are results, not exceptions."""
    request = urllib.request.Request(url, data=body, headers=dict(headers), method="POST")
    try:
        with urllib.request.urlopen(request, timeout=20) as response:  # nosec B310
            return HttpResult(status=response.status, body=response.read())
    except urllib.error.HTTPError as exc:
        return HttpResult(status=exc.code, body=exc.read() or b"", error=str(exc))
    except (urllib.error.URLError, OSError, ValueError) as exc:
        reason = getattr(exc, "reason", exc)
        return HttpResult(status=0, error=str(reason))


def port_free(host: str, port: int) -> Optional[bool]:
    """Whether a TCP port can be bound on ``host`` (False: something already listens there)."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        try:
            s.bind((host, port))
        except OSError as exc:
            if exc.errno in (errno.EADDRINUSE, getattr(errno, "WSAEADDRINUSE", -1)):
                return False
            return None
    return True


def parse_version(text: str) -> Optional[Tuple[int, int]]:
    """(major, minor) of the first version number in ``text`` ("v2.39.2-desktop.1" -> (2, 39))."""
    m = re.search(r"(\d+)\.(\d+)", text or "")
    return (int(m.group(1)), int(m.group(2))) if m else None


def read_key_file(path: str) -> str:
    """The key in ``path`` (first non-empty line, trimmed). Raises ValueError, never shows it."""
    try:
        text = Path(path).read_text(encoding="utf-8")
    except OSError as exc:
        raise ValueError(f"cannot read {path}: {exc.strerror or exc}") from exc
    key = next((line.strip() for line in text.splitlines() if line.strip()), "")
    if not key:
        raise ValueError(f"{path} is empty")
    return key


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
    # proxy.live's result, so the engine-link wait can bring its detail up to date.
    proxy_check: Optional[Check] = None
    # GET /api/status, read once by the first check that needs it (None: not read yet).
    status: Optional[Dict[str, Any]] = None
    status_error: Optional[Check] = None
    status_error_reported: bool = False
    # The one billed call doctor can make (D129): POST /api/decision-model/test, only with
    # --probe and an admin key read from a file.
    probe: bool = False
    admin_key: Optional[str] = None
    post: Poster = http_post
    # The environment and .env the stack reads (COMPOSE_PROFILES, the host ports), and the ports
    # make up publishes: service -> host port, on bind_host.
    environ: Mapping[str, str] = field(default_factory=dict)
    env_file: Path = DEFAULT_ENV_FILE
    bind_host: str = "127.0.0.1"
    host_ports: Dict[str, int] = field(default_factory=dict)
    port_probe: PortProbe = port_free
    # --target pages: the site's base URL (ends with "/").
    pages_url: str = ""
    platform: str = os.name

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
        detail = health_detail(body)
        if body.get("status") != "ok":
            self.proxy_check = Check(
                "proxy.live",
                "WARN",
                f"/health answered with status {body.get('status')!r}, expected 'ok'",
                detail=detail,
            )
        else:
            self.proxy_check = Check(
                "proxy.live", "PASS", f"{self.proxy_url}/health ok", detail=detail
            )
        return self.proxy_check

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
        if linked is False and self.wait_for_engine_link():
            return Check(
                "engine.health",
                "PASS",
                "engine answers its gRPC health check",
                detail="the proxy reached it after its next engine ping",
            )
        if linked is False:
            waited = f" after {self.engine_link_wait_s():.0f}s" if self.engine_link_wait_s() else ""
            return Check(
                "engine.health",
                "WARN",
                "the engine is healthy but the proxy cannot reach it (/health audit_engine=false"
                + waited
                + ")",
                fix=self.logs_hint("proxy"),
            )
        return Check("engine.health", "PASS", "engine answers its gRPC health check")

    def engine_link_wait_s(self) -> float:
        """How long --wait lets the proxy take to reach a healthy engine (0: no waiting)."""
        return min(self.wait_s, ENGINE_LINK_WAIT_S)

    def wait_for_engine_link(self) -> bool:
        """Right after `up --wait` the proxy may not have pinged the engine since it became
        healthy: the proxy pings it every TRUTHTABLE_ENGINE_HEALTH_INTERVAL (10 s by default) and
        is itself healthy before the engine is. Re-read /health until audit_engine is true,
        within the wait; the later checks then read a linked stack."""
        deadline = self.clock() + self.engine_link_wait_s()
        while self.clock() < deadline:
            self.sleep(POLL_INTERVAL_S)
            result = self.fetch(f"{self.proxy_url}/health", {})
            try:
                body = result.json() if result.status == 200 else None
            except ValueError:
                body = None
            if isinstance(body, dict):
                self.health = body
                if body.get("audit_engine") is True:
                    # proxy.live printed the /health it read first: it now says what the
                    # later checks read, so the report does not show audit_engine=False above
                    # an engine.health PASS.
                    if self.proxy_check is not None:
                        self.proxy_check.detail = (
                            health_detail(body) + " (read again after the engine link)"
                        )
                    return True
        return False

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

    # ---------- the proxy's own state (/api/status, /api/setup/status, /api/policies) ----------

    def api_headers(self) -> Dict[str, str]:
        """The proxy key header (the trafficgen key has the read scope)."""
        return {"X-TrustAgent-Key": self.api_key} if self.api_key else {}

    def read_fix(self, status: int, path: str) -> str:
        if status in (401, 403):
            return "check TRAFFICGEN_API_KEY and TRUTHTABLE_API_KEYS in .env (make configure)"
        if status == 404:
            rebuild = "make demo" if self.expect == "demo" else "make up UP_ARGS=--build"
            return f"this proxy build has no {path}; rebuild it ({rebuild})"
        return self.logs_hint("proxy") if self.target == "docker" else "make dev-go"

    def get_json(self, path: str, auth: bool = True) -> Tuple[Any, str, int]:
        """GET ``path`` on the proxy: (decoded JSON or None, error, HTTP status)."""
        result = self.fetch(f"{self.proxy_url}{path}", self.api_headers() if auth else {})
        if result.status != 200:
            what = f"HTTP {result.status}" if result.status else result.error
            return None, f"{path}: {what}", result.status
        try:
            return result.json(), "", result.status
        except ValueError:
            return None, f"{path} did not return JSON", result.status

    def peek_status(self) -> Dict[str, Any]:
        """/api/status for a check that only adds detail from it: read once like system_status,
        an empty dict when it failed (a check that needs it reports the failure)."""
        self.load_status("")
        return self.status if isinstance(self.status, dict) else {}

    def load_status(self, check_id: str) -> None:
        if self.status is None and self.status_error is None:
            body, error, code = self.get_json("/api/status")
            if isinstance(body, dict):
                self.status = body
            else:
                error = error or "/api/status did not return a JSON object"
                self.status_error = Check(
                    check_id, "FAIL", error, fix=self.read_fix(code, "/api/status")
                )

    def system_status(self, check_id: str) -> Tuple[Optional[Dict[str, Any]], Optional[Check]]:
        """GET /api/status once. A failure is reported by the first check that needs it; the
        checks after it skip."""
        self.load_status(check_id)
        if self.status is not None:
            return self.status, None
        assert self.status_error is not None
        if self.status_error_reported:
            return None, Check(check_id, "SKIP", "needs /api/status, which failed above")
        self.status_error_reported = True
        failure = self.status_error
        return None, Check(check_id, failure.status, failure.summary, failure.detail, failure.fix)

    def check_setup(self) -> Check:
        body, error, code = self.get_json("/api/setup/status", auth=False)
        if not isinstance(body, dict):
            return Check(
                "setup.status",
                "FAIL",
                error or "/api/setup/status did not return a JSON object",
                fix=self.read_fix(code, "/api/setup/status"),
            )
        warnings: List[str] = []
        fixes: List[str] = []
        detail = ", ".join(
            f"{key}={body[key]}" for key in ("ingest_auth", "readonly", "public_url") if key in body
        )
        if self.expect == "fresh":
            if body.get("setup_required") is True:
                return Check(
                    "setup.status",
                    "PASS",
                    "a fresh install: no admin yet; the one-time setup link creates one",
                    detail,
                    "make setup-url",
                )
            return Check(
                "setup.status",
                "FAIL",
                "not a fresh install: an admin already exists",
                detail,
                "start from empty volumes (docker compose down -v), or drop --expect fresh",
            )
        if body.get("setup_required") is True:
            warnings.append("no admin yet: saving policies and Playground runs need one")
            if body.get("persistence") is False:
                fixes.append(
                    "without a database there is no setup link: set TRUTHTABLE_ADMIN_KEYS "
                    "(label=key) and sign in at /app/setup with that key"
                )
            else:
                target = "make demo-setup-url" if self.project == DEMO_PROJECT else "make setup-url"
                fixes.append(f"{target}, then open the link to create the admin key")
        if body.get("ingest_auth") == "open":
            warnings.append("ingest is open: any caller that reaches the proxy can send traffic")
            fixes.append("set TRUTHTABLE_API_KEYS (label=key) in .env (make configure)")
        if warnings:
            return Check("setup.status", "WARN", "; ".join(warnings), detail, "; ".join(fixes))
        return Check("setup.status", "PASS", "an admin exists; ingest needs a key", detail)

    def check_decision_model(self) -> Check:
        status, failure = self.system_status("decision_model.state")
        if failure is not None:
            return failure
        assert status is not None
        dm = status.get("decision_model")
        if not isinstance(dm, dict):
            return Check(
                "decision_model.state",
                "FAIL",
                "/api/status has no decision_model",
                fix=self.read_fix(404, "decision_model in /api/status"),
            )
        backend = str(dm.get("backend") or "unknown")
        state = str(dm.get("state") or "unknown")
        summary = f"fast check: backend {backend}, state {state}"
        detail = describe_decision_model(dm)
        last = dm.get("last_error") if isinstance(dm.get("last_error"), dict) else None
        hint = str(last.get("hint") or "") if last else ""
        jev_doc = "docs/JEV-ACCESS.md"
        if self.expect == "demo":
            if backend != "mock" or state != "mock":
                return Check(
                    "decision_model.state",
                    "FAIL",
                    summary + ": the demo runs the mock backend",
                    detail,
                    "make demo-down && make demo (docker-compose.demo.yml sets it)",
                )
            return Check("decision_model.state", "PASS", summary + " (simulated, $0)", detail)
        if state in DECISION_MODEL_FAILED:
            message = str(last.get("message") or "") if last else ""
            return Check(
                "decision_model.state",
                "FAIL",
                summary + (f": {message}" if message else ""),
                detail,
                hint or f"see the errors table in {jev_doc}",
            )
        if state == "off" and self.expect == "fresh":
            return Check(
                "decision_model.state",
                "PASS",
                summary + ": no key yet, as on a fresh install (the setup wizard connects one)",
                detail,
            )
        if state == "off":
            return Check(
                "decision_model.state",
                "WARN",
                summary + ": answers pass through unchecked (skipped:decision_model_off)",
                detail,
                f"put OPENROUTER_API_KEY in .env, then make up ({jev_doc})",
            )
        if state == "mock":
            return Check(
                "decision_model.state",
                "WARN",
                summary + ": checks are simulated, their numbers are not measured",
                detail,
                f"set TRUTHTABLE_JEV_BACKEND=auto and OPENROUTER_API_KEY ({jev_doc})",
            )
        if state in DECISION_MODEL_DEGRADED:
            return Check(
                "decision_model.state",
                "WARN",
                summary,
                detail,
                hint or "retries and the breaker recover by themselves; run doctor again",
            )
        return Check("decision_model.state", "PASS", summary, detail)

    def check_calibration(self) -> Check:
        status, failure = self.system_status("calibration.targets")
        if failure is not None:
            return failure
        assert status is not None
        dm = status.get("decision_model") if isinstance(status.get("decision_model"), dict) else {}
        raw = dm.get("calibrated_targets")
        targets = raw if isinstance(raw, int) and not isinstance(raw, bool) else 0
        state = str(dm.get("calibration_state") or "none")
        summary = f"{targets} calibrated target(s), calibration {state}"
        if self.expect == "demo":
            if targets >= MIN_DEMO_CALIBRATED_TARGETS:
                return Check("calibration.targets", "PASS", summary + " (simulated)")
            return Check(
                "calibration.targets",
                "FAIL",
                summary + f"; the demo needs at least {MIN_DEMO_CALIBRATED_TARGETS} "
                "(its block and fallback scenarios use the mock calibration)",
                detail="TRUTHTABLE_CALIBRATION_DIR=/etc/trustagent/calibration (config/calibration)",
                fix=self.logs_hint("proxy"),
            )
        if targets == 0:
            return Check(
                "calibration.targets",
                "PASS",
                "no calibrated targets: observe and flag need none; block and fallback "
                "routes need one per evaluator (make calibrate)",
            )
        return Check("calibration.targets", "PASS", summary)

    def check_policy(self) -> Check:
        status, failure = self.system_status("policy.state")
        if failure is not None:
            return failure
        assert status is not None
        pol = status.get("policy") if isinstance(status.get("policy"), dict) else {}
        version = pol.get("version")
        source = str(pol.get("source") or "unknown")
        file_status = str(pol.get("file_status") or "unknown")
        summary = f"version {version if version is not None else 'none'} from {source}"
        summary += f", policy file {file_status}"
        body, error, _ = self.get_json("/api/policies")
        routes = body.get("effective_routes") if isinstance(body, dict) else None
        downgrades = describe_downgrades(routes) if isinstance(routes, list) else []
        # A downgrade (an unmet block-readiness item) does not always lower the effective mode,
        # so the routes that run below their configured mode are counted apart.
        below = (
            sum(
                1
                for r in routes
                if isinstance(r, dict)
                and r.get("downgrades")
                and r.get("mode") != r.get("effective_mode")
            )
            if isinstance(routes, list)
            else 0
        )
        details: List[str] = []
        if pol.get("file_error"):
            details.append(f"file error: {pol['file_error']}")
        details += downgrades
        if not isinstance(routes, list):
            details.append(f"route downgrades unknown ({error or 'no effective_routes'})")
        detail = "; ".join(details)
        if file_status == "invalid":
            return Check(
                "policy.state",
                "FAIL",
                summary + ": the file does not load, so the version above stays in effect",
                detail,
                "fix config/policies.yaml (the file error above), then it reloads by itself",
            )
        if file_status == "missing":
            return Check(
                "policy.state",
                "WARN",
                summary + ": the built-in policy is in effect",
                detail,
                "set TRUTHTABLE_POLICY_FILE (compose mounts config/policies.yaml)",
            )
        if file_status == "diverged":
            return Check(
                "policy.state",
                "WARN",
                summary + ": the dashboard version differs from the file",
                detail,
                "keep it, or adopt the file on the Policies page (POST /api/policies/adopt-file)",
            )
        if downgrades:
            return Check(
                "policy.state",
                "WARN",
                summary
                + f"; {len(downgrades)} route(s) with downgrades"
                + (f" ({below} running below the configured mode)" if below else ""),
                detail,
                "the Policies page shows each downgrade with its fix",
            )
        if not isinstance(routes, list):
            return Check("policy.state", "WARN", summary, detail, self.read_fix(0, "/api/policies"))
        return Check("policy.state", "PASS", summary, detail)

    def check_credentials(self) -> Check:
        status, failure = self.system_status("credentials.store")
        if failure is not None:
            return failure
        assert status is not None
        creds = status.get("credentials")
        if not isinstance(creds, dict):
            return Check(
                "credentials.store",
                "SKIP",
                "this proxy build reports no credential store (it predates encrypted keys)",
            )
        store = str(creds.get("store") or "unknown")
        undecryptable = [str(n) for n in creds.get("undecryptable") or [] if isinstance(n, str)]
        changed = [
            w
            for w in status.get("warning_details") or []
            if isinstance(w, dict) and w.get("code") == "master_key_changed"
        ]
        if store == "unavailable":
            return Check(
                "credentials.store",
                "FAIL",
                "the credential store is unavailable: keys saved in the dashboard cannot be used",
                "the master key (TRUTHTABLE_SECRET_KEY, else the trustagent-secrets volume) "
                "could not be loaded",
                f"{self.logs_hint('proxy')}; see the master key section of {SECURITY_DOC}",
            )
        if undecryptable or changed:
            names = undecryptable or [
                str(n) for w in changed for n in (w.get("details") or {}).get("names") or []
            ]
            listed = ", ".join(names) if names else "some saved keys"
            return Check(
                "credentials.store",
                "FAIL",
                f"{len(names) or 'some'} saved key(s) cannot be decrypted ({listed}): "
                "the master key changed since they were saved",
                "a new TRUTHTABLE_SECRET_KEY, or a lost trustagent-secrets volume, makes "
                "every stored key unreadable",
                "restore the old master key (TRUTHTABLE_SECRET_KEY or the trustagent-secrets "
                "volume), or re-enter those keys in Settings > Connections "
                f"({SECURITY_DOC})",
            )
        if store == "no_database":
            return Check(
                "credentials.store",
                "WARN",
                "no database: keys can come from .env only (the dashboard cannot save any)",
                fix="set TRUTHTABLE_DATABASE_URL (the compose stack sets it)",
            )
        if store != "ready":
            return Check("credentials.store", "WARN", f"credential store state {store!r}")
        return Check(
            "credentials.store", "PASS", "credential store ready; every saved key decrypts"
        )

    def check_runtime_config(self) -> Check:
        status, failure = self.system_status("runtime_config.version")
        if failure is not None:
            return failure
        assert status is not None
        settings, error, code = self.get_json("/api/settings")
        if code == 404:
            return Check(
                "runtime_config.version",
                "SKIP",
                "this proxy build has no /api/settings (runtime settings arrived later)",
            )
        version = settings.get("version") if isinstance(settings, dict) else None
        if not isinstance(version, int) or isinstance(version, bool):
            return Check(
                "runtime_config.version",
                "WARN",
                error or "/api/settings has no version",
                fix=self.read_fix(code, "/api/settings"),
            )
        engine = status.get("engine") if isinstance(status.get("engine"), dict) else {}
        raw = engine.get("runtime_config_version")
        # The proxy leaves out version 0 (nothing pushed or nothing saved yet).
        applied = raw if isinstance(raw, int) and not isinstance(raw, bool) else 0
        epoch = engine.get("runtime_config_epoch")
        detail = f"gateway settings version {version}, engine applied {applied}"
        if isinstance(epoch, str) and epoch:
            detail += f" (epoch {epoch})"
        if engine.get("version"):
            detail += f"; engine {engine['version']}"
        # The gateway's own words about the push (runtimecfg: "The audit engine ...").
        engine_notes = [
            str(w) for w in status.get("warnings") or [] if "audit engine" in str(w).lower()
        ]
        if engine_notes:
            detail += "; " + "; ".join(engine_notes)
        if applied == version and not engine_notes:
            summary = (
                "the engine runs the gateway's settings (none saved yet)"
                if version == 0
                else f"the engine runs settings version {version}"
            )
            return Check("runtime_config.version", "PASS", summary, detail)
        if applied == version:
            return Check(
                "runtime_config.version",
                "WARN",
                f"the engine runs settings version {version}, but: {engine_notes[0]}",
                detail,
                self.logs_hint("engine"),
            )
        if raw is None and version > 0:
            return Check(
                "runtime_config.version",
                "WARN",
                "the engine reports no applied settings: it is older than the gateway or has "
                "not been reached yet",
                detail,
                "rebuild and restart both (make up UP_ARGS=--build), then run doctor again",
            )
        return Check(
            "runtime_config.version",
            "WARN",
            f"the engine applied settings version {applied}, the gateway holds {version}: "
            "the push is pending or failed",
            detail,
            "the gateway retries by itself; if this stays, " + self.logs_hint("engine"),
        )

    def check_judge(self) -> Check:
        status, failure = self.system_status("judge.credential_source")
        if failure is not None:
            return failure
        assert status is not None
        judge = status.get("judge")
        if not isinstance(judge, dict) or "credential_source" not in judge:
            return Check(
                "judge.credential_source",
                "SKIP",
                "this proxy build does not report where the judge's key comes from",
            )
        provider = str(judge.get("provider") or "none")
        model = judge.get("model")
        source = str(judge.get("credential_source") or "none")
        state = str(judge.get("state") or "unknown")
        name = provider + (f" / {model}" if model else "")
        detail = f"provider {provider}, model {model or 'default'}, state {state}, key {source}"
        if judge.get("fallback_active"):
            detail += f"; fallback: {judge.get('fallback_reason') or 'active'}"
        if judge.get("provider") is None and state == "unknown":
            # The proxy has no engine health yet (starting, or unreachable): nothing is known about
            # the judge, so blaming a missing key would send the operator the wrong way.
            return Check(
                "judge.credential_source",
                "WARN",
                "the audit engine has not reported its judge yet (it is starting or "
                "unreachable; see engine.health)",
                detail,
                self.logs_hint("engine"),
            )
        if self.expect == "demo":
            if provider != "mock":
                return Check(
                    "judge.credential_source",
                    "FAIL",
                    f"judge {name}: the demo runs the mock judge",
                    detail,
                    "make demo-down && make demo (docker-compose.demo.yml sets LLM_PROVIDER=mock)",
                )
            return Check(
                "judge.credential_source",
                "PASS",
                "judge mock (simulated verdicts, $0); it needs no key",
                detail,
            )
        connect = (
            "connect OpenRouter in Settings > Connections, or set OPENROUTER_API_KEY in .env "
            f"and make up ({JEV_DOC})"
        )
        if provider == "none" and self.expect == "fresh":
            return Check(
                "judge.credential_source",
                "PASS",
                "no deep-audit judge yet, as on a fresh install (the setup wizard connects one)",
                detail,
            )
        if provider == "none":
            return Check(
                "judge.credential_source",
                "WARN",
                "no deep-audit judge: answers get the fast check only, no claim-level audits",
                detail,
                connect,
            )
        if provider == "mock":
            return Check(
                "judge.credential_source",
                "WARN",
                "the judge is the mock: deep-audit verdicts are simulated, not measured",
                detail,
                # Simulated checks come from .env (LLM_PROVIDER=mock, read-only in the dashboard)
                # or from the dashboard, where binding a key to the judge turns them off (D76).
                "if .env sets LLM_PROVIDER=mock, set it to auto and make up; otherwise "
                + connect
                + " (binding a key to the judge turns simulated checks off)",
            )
        env_var = JUDGE_KEY_VARS.get(provider)
        if env_var and source == "none":
            return Check(
                "judge.credential_source",
                "FAIL",
                f"judge {name} has no key: deep audits cannot run",
                detail,
                f"bind a key to the judge in Settings > Connections, or set {env_var} in .env "
                "and make up",
            )
        where = {
            "env": "its key comes from the engine's .env (read-only in the dashboard)",
            "dashboard": "its key is saved in the dashboard (encrypted)",
        }.get(source, "it needs no key")
        if judge.get("fallback_active"):
            return Check(
                "judge.credential_source",
                "WARN",
                f"judge {name} is running its fallback; {where}",
                detail,
                self.logs_hint("engine"),
            )
        if state in ("down", "degraded"):
            return Check(
                "judge.credential_source",
                "WARN",
                f"judge {name} is {state}; {where}",
                detail,
                self.logs_hint("engine"),
            )
        return Check("judge.credential_source", "PASS", f"judge {name}; {where}", detail)

    def check_budgets(self) -> Check:
        status, failure = self.system_status("budgets.today")
        if failure is not None:
            return failure
        assert status is not None
        b = status.get("budgets")
        if not isinstance(b, dict):
            return Check("budgets.today", "SKIP", "this proxy build reports no budgets")

        def usd(key: str) -> float:
            value = b.get(key)
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                return 0.0
            return float(value)

        def line(label: str, spent: float, cap: float) -> str:
            limit = f"${cap:.2f}" if cap > 0 else "no budget set"
            return f"{label} ${spent:.4f} of {limit}"

        # SystemStatus.budgets counts real calls only (simulated checks never count), each at the
        # vendor's reported cost where it gives one and otherwise priced from tokens with the price
        # table: the total is an estimate, not a measurement (honest labels, CLAUDE.md).
        label = "estimate: the vendor's reported cost where given, else priced from tokens"
        if self.expect == "demo":
            label += "; the demo's simulated checks cost $0"
        detail = (
            line("fast checks", usd("decision_spent_today_usd"), usd("decision_usd_day"))
            + "; "
            + line("deep audits", usd("llm_spent_today_usd"), usd("llm_usd_day"))
            + f" ({label})"
        )
        settings = "raise it in Settings > Budgets, or wait for tomorrow's reset"
        if b.get("llm_exhausted") is True and b.get("decision_exhausted") is True:
            return Check(
                "budgets.today",
                "WARN",
                "both daily budgets are used up: fast checks pass answers through unchecked "
                "and deep audits are paused",
                detail,
                settings,
            )
        if b.get("decision_exhausted") is True:
            return Check(
                "budgets.today",
                "WARN",
                "today's fast-check budget is used up: answers pass through unchecked "
                "(fail-closed block routes block)",
                detail,
                settings,
            )
        if b.get("llm_exhausted") is True:
            return Check(
                "budgets.today",
                "WARN",
                "Deep audits paused: today's deep-audit budget is used up",
                detail,
                settings,
            )
        if b.get("decision_reserve_only") is True:
            return Check(
                "budgets.today",
                "WARN",
                "the fast-check budget is nearly used up: only block and fallback routes are "
                "still checked (observe and flag routes skip)",
                detail,
                settings,
            )
        return Check("budgets.today", "PASS", "within today's budgets", detail)

    # ---------- Prometheus endpoints (inside the containers: the ports are not published) ----------

    def check_metrics(self, service: str, prefix: str) -> Check:
        check_id = f"metrics.{'gateway' if service == 'proxy' else 'engine'}"
        port_key, default_port = (
            ("TRUTHTABLE_METRICS_PORT", "8002") if service == "proxy" else ("METRICS_PORT", "8001")
        )
        if self.target != "docker":
            return Check(
                check_id,
                "SKIP",
                f"host target: read http://localhost:{default_port}/metrics directly",
            )
        c = self.service(service)
        if c is None:
            return Check(check_id, "FAIL", f"no {service} container", fix=self.ps_hint())
        port = c.env.get(port_key, "")
        port = port if port.isdigit() else default_port
        url = f"http://127.0.0.1:{port}/metrics"
        if service == "proxy":
            argv = ["docker", "exec", c.name, "wget", "-q", "-O", "-", url]
        else:
            argv = ["docker", "exec", c.name, "python", "-c", ENGINE_METRICS_FETCH, url]
        code, out, err = self.run(argv, 20)
        if code != 0:
            lines = (err or out).strip().splitlines()
            return Check(
                check_id,
                "FAIL",
                f"{service} :{port}/metrics did not answer (exit {code})",
                detail=lines[-1] if lines else "",
                fix=self.logs_hint(service),
            )
        names = metric_names(out)
        ours = sorted(n for n in names if n.startswith(prefix))
        foreign = sorted(
            n
            for n in names
            if (n.startswith("trustagent_") or n.startswith("truthtable_"))
            and not n.startswith(prefix)
        )
        rebuild = "make demo" if self.expect == "demo" else "make up UP_ARGS=--build"
        if foreign:
            shown = ", ".join(foreign[:5]) + (", ..." if len(foreign) > 5 else "")
            return Check(
                check_id,
                "FAIL",
                f"{service} :{port}/metrics has {len(foreign)} name(s) outside {prefix}*",
                detail=shown,
                fix=f"the image predates the metric rename; rebuild it ({rebuild})",
            )
        if not ours:
            return Check(
                check_id,
                "FAIL",
                f"{service} :{port}/metrics exposes no {prefix}* metric",
                fix=f"rebuild the image ({rebuild})",
            )
        series = sum(1 for line in out.splitlines() if line.strip().startswith(prefix))
        return Check(
            check_id, "PASS", f"{service} :{port}/metrics exposes {series} {prefix}* series"
        )

    def check_gateway_metrics(self) -> Check:
        return self.check_metrics("proxy", "trustagent_gateway_")

    def check_engine_metrics(self) -> Check:
        return self.check_metrics("engine", "trustagent_engine_")

    def fetch_traces(self) -> Traces:
        result = self.fetch(f"{self.proxy_url}/api/traces?limit=50", self.api_headers())
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
        if self.expect == "fresh":
            fresh = self.fetch_traces()
            if fresh.items is None:
                return Check(
                    "traces.recent",
                    "WARN",
                    fresh.error,
                    fix=self.read_fix(fresh.status, "/api/traces"),
                )
            if fresh.items:
                return Check(
                    "traces.recent",
                    "WARN",
                    f"not a fresh install: {len(fresh.items)} trace(s) already stored",
                    fix="start from empty volumes (docker compose down -v)",
                )
            return Check("traces.recent", "PASS", "no traces yet, as on a fresh install")
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

    # ---------- the machine (docker target): Docker, memory, ports, .env ----------

    def check_docker(self) -> Check:
        code, out, err = self.run(["docker", "version", "--format", "{{.Server.Version}}"], 20)
        if code != 0:
            return Check(
                "environment.docker",
                "FAIL",
                "cannot ask Docker for its version",
                detail=(err or out).strip()[-300:],
                fix="start Docker (Docker Desktop: enable WSL integration for this distro)",
            )
        engine = parse_version(out)
        code, out, err = self.run(["docker", "compose", "version", "--short"], 20)
        compose = parse_version(out) if code == 0 else None
        found = f"Docker {out_version(engine)}, Compose {out_version(compose)}"
        problems: List[str] = []
        if engine is None or engine < MIN_DOCKER:
            problems.append(f"Docker Engine {out_version(engine)} is older than 25")
        if compose is None or compose < MIN_COMPOSE:
            problems.append(
                f"Compose {out_version(compose)} is older than 2.24 (the compose files use "
                "!reset and !override)"
            )
        if problems:
            return Check(
                "environment.docker",
                "FAIL",
                "; ".join(problems),
                detail=found,
                fix="update Docker Desktop, or docker-ce and the compose plugin",
            )
        return Check("environment.docker", "PASS", found)

    def check_memory(self) -> Check:
        code, out, err = self.run(["docker", "info", "--format", "{{.MemTotal}}"], 20)
        raw = out.strip()
        if code != 0 or not raw.isdigit():
            return Check(
                "environment.memory",
                "SKIP",
                "Docker did not report its memory",
                detail=(err or out).strip()[-200:],
            )
        gib = int(raw) / 1024**3
        if int(raw) < MIN_MEMORY_BYTES:
            return Check(
                "environment.memory",
                "WARN",
                f"Docker has {gib:.1f} GB of memory; the stack's limits add up to about 4.6 GB "
                "(the engine alone may use 4 GB)",
                fix="give Docker more memory (Docker Desktop: Settings > Resources; WSL: "
                "memory= in %UserProfile%\\.wslconfig), or run fewer stacks at once",
            )
        return Check("environment.memory", "PASS", f"Docker has {gib:.1f} GB of memory")

    def check_ports(self) -> Check:
        """The host ports make up publishes: this stack's, or free. A service that runs is judged
        by what it publishes (the demo publishes no database port); one that does not run needs
        its port free."""
        if not self.host_ports:
            return Check("environment.ports", "SKIP", "no host ports to check")
        if self.containers is None and self.load_containers() is not None:
            return Check("environment.ports", "SKIP", "cannot list the project's containers")
        running = {c.service: c for c in self.containers or []}
        busy: List[Tuple[str, int]] = []
        lines: List[str] = []
        for service, port in self.host_ports.items():
            c = running.get(service)
            if c is not None:
                published = ", ".join(str(p) for p in c.ports) or "no port"
                lines.append(f"{port} {service}" if port in c.ports else f"{service}: {published}")
            elif self.port_probe(self.bind_host, port) is False:
                busy.append((service, port))
            else:
                lines.append(f"{port} free")
        if not busy:
            return Check("environment.ports", "PASS", "host ports: " + "; ".join(lines))
        names = ", ".join(f"{s.upper()}_HOST_PORT" for s, _ in busy)
        return Check(
            "environment.ports",
            "WARN",
            "in use by another program: "
            + ", ".join(f"{p} ({s})" for s, p in busy)
            + "; make up cannot publish there, and requests to it reach that program",
            detail="; ".join(lines),
            fix=f"stop that program, or set {names} in .env to free ports",
        )

    def check_env_file(self) -> Check:
        path = self.env_file
        if self.platform == "nt":
            return Check("environment.env_file", "SKIP", "file modes do not apply on Windows")
        try:
            mode = path.stat().st_mode & 0o777
        except FileNotFoundError:
            return Check(
                "environment.env_file",
                "SKIP",
                f"no {path.name} yet (make up and make configure create it, mode 0600)",
            )
        except OSError as exc:
            return Check("environment.env_file", "WARN", f"cannot read the mode of {path}: {exc}")
        if mode & 0o077:
            return Check(
                "environment.env_file",
                "WARN",
                f"{path.name} is readable by other users (mode {mode:04o}); it holds keys",
                fix=f"chmod 600 {path}",
            )
        return Check("environment.env_file", "PASS", f"{path.name} is private (mode {mode:04o})")

    # ---------- the images (docker target) ----------

    def check_images(self) -> Check:
        if self.containers is None and self.load_containers() is not None:
            return Check("images.version", "SKIP", "cannot list the project's containers")
        apps = [c for c in self.containers or [] if c.service in APP_SERVICES and c.image_id]
        if not apps:
            return Check("images.version", "SKIP", "no app containers (see compose.services)")
        ids = sorted({c.image_id for c in apps})
        code, out, err = self.run(["docker", "image", "inspect", *ids], 20)
        try:
            items = json.loads(out) if code == 0 else None
        except ValueError:
            items = None
        if not isinstance(items, list):
            return Check(
                "images.version",
                "WARN",
                "cannot inspect the app images",
                detail=(err or out).strip()[-300:],
            )
        by_id = {str(i.get("Id")): i for i in items if isinstance(i, dict)}
        versions: Dict[str, str] = {}
        revisions: Dict[str, str] = {}
        origin: Dict[str, str] = {}
        for c in sorted(apps, key=lambda c: APP_SERVICES.index(c.service)):
            image = by_id.get(c.image_id) or {}
            labels = (image.get("Config") or {}).get("Labels") or {}
            versions[c.service] = str(labels.get("org.opencontainers.image.version") or "?")
            revisions[c.service] = str(labels.get("org.opencontainers.image.revision") or "")
            origin[c.service] = image_origin(image, labels)
        rebuild = "make demo" if self.expect == "demo" else "make up UP_ARGS=--build"
        detail = "; ".join(
            f"{s} {versions[s]} ({revisions[s][:12] or 'no revision'}, {origin[s]})"
            for s in versions
        )
        if len(set(versions.values())) > 1:
            return Check(
                "images.version",
                "FAIL",
                "the app images are from different versions: "
                + ", ".join(f"{s} {v}" for s, v in versions.items()),
                detail=detail,
                fix=f"run one version (TRUSTAGENT_VERSION): {rebuild}",
            )
        known = {s: r for s, r in revisions.items() if r}
        status_sha = str(self.peek_status().get("build_sha") or "")
        mismatch: List[str] = []
        if len(set(known.values())) > 1:
            mismatch.append(
                "the app images come from different commits: "
                + ", ".join(f"{s} {r[:12]}" for s, r in known.items())
            )
        proxy_rev = known.get("proxy", "")
        if proxy_rev and status_sha and not same_revision(proxy_rev, status_sha):
            mismatch.append(
                f"the proxy image is labelled {proxy_rev[:12]}, but the running proxy reports "
                f"build {status_sha[:12]}"
            )
        if mismatch:
            return Check(
                "images.version",
                "WARN",
                "; ".join(mismatch),
                detail=detail,
                fix=f"rebuild or pull them together: {rebuild}",
            )
        kinds = sorted(set(origin.values()))
        revision = next(iter(known.values()), "")[:12]
        summary = f"one version {next(iter(versions.values()))}" + (
            f", one revision {revision}"
            if revision
            else ", no revision labels (built without make)"
        )
        return Check("images.version", "PASS", f"{summary}; {' and '.join(kinds)}", detail)

    # ---------- the fast check's rate and probe ----------

    def check_rpm(self) -> Check:
        status, failure = self.system_status("decision_model.rpm")
        if failure is not None:
            return failure
        assert status is not None
        dm = status.get("decision_model") if isinstance(status.get("decision_model"), dict) else {}
        backend = str(dm.get("backend") or "unknown")
        limit, used = dm.get("rpm_limit"), dm.get("rpm_used")
        if backend in ("mock", "off"):
            return Check("decision_model.rpm", "SKIP", f"the {backend} fast check calls no vendor")
        if not isinstance(limit, int) or isinstance(limit, bool) or limit <= 0:
            return Check("decision_model.rpm", "SKIP", "this proxy reports no rate limit")
        used = used if isinstance(used, int) and not isinstance(used, bool) else 0
        share = used / limit
        summary = f"{used} of {limit} fast-check calls a minute ({share:.0%})"
        if share >= RPM_WARN_SHARE:
            return Check(
                "decision_model.rpm",
                "WARN",
                summary + ": close to the limit; past it, checks skip (rate_limited)",
                fix="raise TRUTHTABLE_GATE_JEV_RPM (below the vendor's 1,200 a minute), or "
                "sample observe routes",
            )
        return Check("decision_model.rpm", "PASS", summary)

    def check_probe(self) -> Check:
        if not self.probe:
            return Check(
                "decision_model.probe",
                "SKIP",
                "not probed (--no-probe, the default); --probe --admin-key-file F sends one "
                "test call (about $0.00001)",
            )
        if not self.admin_key:
            return Check(
                "decision_model.probe", "FAIL", "--probe needs --admin-key-file", fix="see --help"
            )
        headers = {"X-TrustAgent-Key": self.admin_key, "Content-Type": "application/json"}
        result = self.post(f"{self.proxy_url}/api/decision-model/test", headers, b"{}")
        if result.status != 200:
            what = f"HTTP {result.status}" if result.status else result.error
            fix = (
                "the admin key file holds no admin key (TRUTHTABLE_ADMIN_KEYS, or a key "
                "created in Settings > Access)"
                if result.status in (401, 403)
                else self.read_fix(result.status, "/api/decision-model/test")
            )
            return Check(
                "decision_model.probe", "FAIL", f"/api/decision-model/test: {what}", fix=fix
            )
        try:
            body = result.json()
        except ValueError:
            body = None
        if not isinstance(body, dict):
            return Check(
                "decision_model.probe", "FAIL", "/api/decision-model/test did not return JSON"
            )
        backend = str(body.get("backend") or "unknown")
        facts = {k: body.get(k) for k in ("ok", "backend", "snapshot", "latency_ms", "cost_usd")}
        if body.get("ok") is True:
            latency = body.get("latency_ms")
            cost = body.get("cost_usd")
            source = str(body.get("cost_source") or "")
            label = "vendor" if source == "reported" else "estimate"
            if backend == "mock":
                label = "simulated"
            parts = [f"probe ok: {backend}, snapshot {body.get('snapshot') or 'n/a'}"]
            if isinstance(latency, (int, float)):
                parts.append(f"{latency:.0f} ms")
            if isinstance(cost, (int, float)):
                parts.append(f"${cost:.6f} ({label})")
            return Check("decision_model.probe", "PASS", ", ".join(parts), data=facts)
        error = body.get("error") if isinstance(body.get("error"), dict) else {}
        kind = str(error.get("kind") or "bad_response")
        message = str(error.get("message") or "")
        status, meaning, fix = PROBE_ERRORS.get(
            kind,
            ("FAIL", f"the probe failed ({kind})", str(error.get("hint") or JEV_DOC)),
        )
        return Check(
            "decision_model.probe",
            status,
            f"probe: {meaning}",
            detail=message,
            fix=fix,
            data={**facts, "error_kind": kind},
        )

    # ---------- configuration and add-ons ----------

    def check_price_table(self) -> Check:
        status, failure = self.system_status("config.price_table")
        if failure is not None:
            return failure
        assert status is not None
        warnings = [str(w) for w in status.get("warnings") or []]
        bad = [w for w in warnings if w.startswith("TRUTHTABLE_PRICE_TABLE_FILE")]
        if bad:
            return Check(
                "config.price_table",
                "WARN",
                "TRUTHTABLE_PRICE_TABLE_FILE is not usable: costs are priced from the built-in "
                "table",
                detail=bad[0][:300],
                fix="fix the file (schema contracts/schema/pricing.v1.schema.json) or unset "
                "TRUTHTABLE_PRICE_TABLE_FILE, then restart the proxy",
            )
        proxy = self.service("proxy") if self.target == "docker" else None
        custom = bool(proxy and proxy.env.get("TRUTHTABLE_PRICE_TABLE_FILE"))
        return Check(
            "config.price_table",
            "PASS",
            "the price table file loads" if custom else "the built-in price table",
        )

    def check_addons(self) -> Check:
        raw = resolve("COMPOSE_PROFILES", "", self.env_file, self.environ) or ""
        profiles = {p.strip() for p in raw.split(",") if p.strip()}
        enabled = [p for p in ADDON_PROFILES if p in profiles or "*" in profiles]
        running: Dict[str, Container] = {}
        if self.target == "docker" and (
            self.containers is not None or self.load_containers() is None
        ):
            running = {c.service: c for c in self.containers or []}
        status = self.peek_status()
        problems: List[str] = []
        shown: List[str] = []
        for profile, services in ADDON_PROFILES.items():
            up = [s for s in services if s in running]
            if profile not in enabled and not up:
                continue
            states = [f"{s} {describe_state(running[s])}" for s in up]
            shown.append(f"{profile} ({', '.join(states) or 'not running'})")
            if self.target == "docker":
                missing = [s for s in services if s not in running and s != "ollama-init"]
                if profile in enabled and missing:
                    problems.append(
                        f"COMPOSE_PROFILES has {profile}, but {', '.join(missing)} is not running"
                    )
        knowledge = status.get("knowledge") if isinstance(status.get("knowledge"), dict) else {}
        if knowledge.get("enabled") is True and knowledge.get("state") not in (None, "up"):
            problems.append(f"the knowledge base is {knowledge.get('state')}")
        if "knowledge" in enabled and knowledge and knowledge.get("enabled") is False:
            problems.append(
                "the knowledge profile is on, but TRUTHTABLE_KNOWLEDGE_ENABLED is not true: "
                "retrieval is off"
            )
        redis = status.get("redis") if isinstance(status.get("redis"), dict) else {}
        if redis.get("configured") is True and redis.get("state") not in (None, "up"):
            problems.append(f"redis is {redis.get('state')}")
        detail = f"COMPOSE_PROFILES={raw or '(unset)'}"
        if problems:
            return Check(
                "profiles.addons",
                "WARN",
                "; ".join(problems),
                detail=detail,
                fix="make up (it starts the profiles in COMPOSE_PROFILES); docs/CONFIGURATION.md",
            )
        if not shown:
            return Check(
                "profiles.addons",
                "PASS",
                "no add-on profile (knowledge, local-llm, observability, redis): the default set",
                detail=detail,
            )
        return Check("profiles.addons", "PASS", "add-ons: " + "; ".join(shown), detail=detail)

    # ---------- --target pages: the static site ----------

    def pages_get(self, path: str) -> Tuple[HttpResult, str]:
        url = self.pages_url + path
        result = self.fetch(url, {})
        if result.status != 200:
            what = f"HTTP {result.status}" if result.status else result.error
            return result, f"{url}: {what}"
        return result, ""

    def pages_html(self, check_id: str, path: str, what: str) -> Check:
        result, error = self.pages_get(path)
        fix = "make pages-build, then serve frontend-react/dist (npm run preview:static)"
        if error:
            return Check(check_id, "FAIL", f"{what}: {error}", fix=fix)
        html = result.body.decode("utf-8", "replace")
        base = urllib.parse.urlparse(self.pages_url).path or "/"
        problems: List[str] = []
        if "Content-Security-Policy" not in html:
            problems.append("no Content-Security-Policy meta")
        if f'="{base}assets/' not in html:
            problems.append(f"loads no asset under {base}")
        if problems:
            return Check(check_id, "FAIL", f"{what}: " + "; ".join(problems), fix=fix)
        return Check(check_id, "PASS", f"{what}: {self.pages_url}{path} (CSP, assets under {base})")

    def check_pages_landing(self) -> Check:
        return self.pages_html("pages.landing", "", "landing page")

    def check_pages_app(self) -> Check:
        return self.pages_html("pages.app", "app/", "dashboard (replay)")

    def check_pages_fallback(self) -> Check:
        result, error = self.pages_get("404.html")
        if error:
            return Check(
                "pages.fallback",
                "FAIL",
                f"no 404.html: {error}",
                fix="the static build copies app/index.html to 404.html (make pages-build)",
            )
        app, _ = self.pages_get("app/")
        if app.status == 200 and result.body != app.body:
            return Check(
                "pages.fallback",
                "WARN",
                "404.html is not a copy of app/index.html: deep links may not open the app",
                fix="make pages-build",
            )
        return Check("pages.fallback", "PASS", "404.html boots the app for deep links")

    def check_pages_dataset(self) -> Check:
        result, error = self.pages_get("demo/manifest.json")
        if error:
            return Check(
                "pages.dataset",
                "FAIL",
                f"no demo recording: {error}",
                fix="make pages-build (the dataset plugin copies demo-data/ to demo/)",
            )
        try:
            manifest = result.json()
        except ValueError:
            manifest = None
        if not isinstance(manifest, dict):
            return Check("pages.dataset", "FAIL", "demo/manifest.json is not a JSON object")
        kind, label = manifest.get("kind"), str(manifest.get("label") or "")
        traces = [t for t in manifest.get("traces") or [] if isinstance(t, dict)]
        data = {"kind": kind, "label": label, "traces": len(traces)}
        if kind == "mock" and not label.startswith("Simulated"):
            return Check(
                "pages.dataset",
                "FAIL",
                "the simulated tape is not labelled simulated",
                detail=label,
                data=data,
            )
        if kind not in ("mock", "recorded"):
            return Check("pages.dataset", "FAIL", f"unknown recording kind {kind!r}", data=data)
        if traces:
            first = str(traces[0].get("trace_id"))
            trace, terror = self.pages_get(f"demo/traces/{first}.json")
            if terror:
                return Check(
                    "pages.dataset",
                    "FAIL",
                    f"the manifest lists traces the site does not serve: {terror}",
                    data=data,
                )
        what = "simulated tape" if kind == "mock" else "recording"
        return Check(
            "pages.dataset",
            "PASS",
            f"{what}, {len(traces)} traces: “{label}”",
            data=data,
        )

    def facts(self, check_id: str) -> Dict[str, Any]:
        """The /api/status fields a check judged, for --json readers (record_demo.py)."""
        if not isinstance(self.status, dict):
            return {}
        picks = {
            "decision_model.state": ("decision_model", ("backend", "state", "snapshot")),
            "judge.credential_source": (
                "judge",
                ("provider", "model", "state", "credential_source", "fallback_active"),
            ),
            "budgets.today": (
                "budgets",
                (
                    "decision_spent_today_usd",
                    "decision_usd_day",
                    "llm_spent_today_usd",
                    "llm_usd_day",
                ),
            ),
        }
        if check_id not in picks:
            return {}
        section, keys = picks[check_id]
        value = self.status.get(section)
        if not isinstance(value, dict):
            return {}
        return {k: value.get(k) for k in keys if k in value}

    def checks(self) -> List[Tuple[str, Callable[[], Check]]]:
        plan: List[Tuple[str, Callable[[], Check]]] = []
        if self.target == "pages":
            return [
                ("pages.landing", self.check_pages_landing),
                ("pages.app", self.check_pages_app),
                ("pages.fallback", self.check_pages_fallback),
                ("pages.dataset", self.check_pages_dataset),
            ]
        if self.target == "docker":
            plan += [
                ("compose.services", self.check_services),
                ("environment.docker", self.check_docker),
                ("environment.memory", self.check_memory),
                ("environment.ports", self.check_ports),
            ]
        plan.append(("environment.env_file", self.check_env_file))
        plan += [
            ("proxy.live", self.check_proxy),
            ("engine.health", self.check_engine),
            ("dashboard.live", self.check_dashboard),
        ]
        if self.expect == "demo":
            plan.append(("demo.mode", self.check_demo_mode))
        plan += [
            ("setup.status", self.check_setup),
            ("decision_model.state", self.check_decision_model),
            ("decision_model.rpm", self.check_rpm),
            ("decision_model.probe", self.check_probe),
            ("calibration.targets", self.check_calibration),
            ("policy.state", self.check_policy),
            ("credentials.store", self.check_credentials),
            ("runtime_config.version", self.check_runtime_config),
            ("judge.credential_source", self.check_judge),
            ("budgets.today", self.check_budgets),
            ("config.price_table", self.check_price_table),
            ("profiles.addons", self.check_addons),
        ]
        if self.target == "docker":
            plan.append(("images.version", self.check_images))
        plan += [
            ("metrics.gateway", self.check_gateway_metrics),
            ("metrics.engine", self.check_engine_metrics),
        ]
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
    ports: List[int] = []
    bindings = ((item.get("NetworkSettings") or {}).get("Ports") or {}).values()
    for binding in bindings:
        for entry in binding or []:
            port = str((entry or {}).get("HostPort") or "")
            if port.isdigit() and int(port) not in ports:
                ports.append(int(port))
    return Container(
        name=str(item.get("Name", "")).lstrip("/"),
        service=service,
        state=str(state.get("Status", "unknown")),
        health=health,
        exit_code=int(state.get("ExitCode", 0) or 0),
        env=env,
        image_id=str(item.get("Image") or ""),
        image=str(config.get("Image") or ""),
        ports=ports,
    )


def out_version(version: Optional[Tuple[int, int]]) -> str:
    return f"{version[0]}.{version[1]}" if version else "unknown"


def image_origin(image: Dict[str, Any], labels: Dict[str, Any]) -> str:
    """'built locally' or 'pulled', for one `docker image inspect` item.

    Compose labels every image it builds (com.docker.compose.project), and the release images are
    built by buildx in CI, without compose, so that label is the first sign. RepoDigests alone is
    not: Docker's containerd image store (the Docker Desktop default) gives a locally built image a
    repo digest too, while the classic store gives one only to a pulled image.
    """
    if labels.get("com.docker.compose.project"):
        return "built locally"
    return "pulled" if image.get("RepoDigests") else "built locally"


def same_revision(a: str, b: str) -> bool:
    """Two git revisions name the same commit when one is a prefix of the other (short SHAs)."""
    a, b = a.lower(), b.lower()
    return bool(a and b) and (a.startswith(b) or b.startswith(a))


def describe_state(c: Container) -> str:
    """'ok', 'starting', or a short description of what is wrong."""
    if c.service in ONE_SHOT_SERVICES and c.state == "exited":
        return "ok" if c.exit_code == 0 else f"exited {c.exit_code}"
    if c.state != "running":
        return f"{c.state} (exit {c.exit_code})" if c.state == "exited" else c.state
    if c.health is None or c.health == "healthy":
        return "ok"
    return c.health


def describe_decision_model(dm: Mapping[str, Any]) -> str:
    """Why the backend was chosen, its snapshot, latency and the last 24 hours' calls and cost."""
    parts: List[str] = []
    if dm.get("backend_reason"):
        parts.append(f"why: {dm['backend_reason']}")
    parts.append(f"snapshot {dm.get('snapshot') or 'not seen yet'}")

    def ms(value: Any) -> str:
        return f"{value:.0f} ms" if isinstance(value, (int, float)) else "n/a"

    parts.append(f"latency p50 {ms(dm.get('p50_ms'))}, p95 {ms(dm.get('p95_ms'))}")
    calls = dm.get("calls_24h") if isinstance(dm.get("calls_24h"), int) else 0
    cost = dm.get("cost_24h_usd") if isinstance(dm.get("cost_24h_usd"), (int, float)) else 0.0
    label = "simulated" if dm.get("backend") == "mock" else "estimate"
    parts.append(f"last 24 h: {calls} call(s), ${cost:.4f} ({label})")
    last = dm.get("last_error")
    if isinstance(last, dict) and last.get("message"):
        parts.append(f"last error: {last.get('kind') or 'error'}: {last['message']}")
    return "; ".join(parts)


def describe_downgrades(routes: Sequence[Any]) -> List[str]:
    """One line per route that runs below its configured mode, with the reasons."""
    lines: List[str] = []
    for route in routes:
        if not isinstance(route, dict):
            continue
        downgrades = [d for d in route.get("downgrades") or [] if isinstance(d, dict)]
        if not downgrades:
            continue
        codes = ", ".join(str(d.get("code") or "?") for d in downgrades)
        mode, effective = route.get("mode"), route.get("effective_mode")
        runs = f" runs as {effective} (configured {mode})" if mode != effective else ""
        lines.append(f"{route.get('name') or '?'}{runs}: {codes}")
    return lines


def metric_names(exposition: str) -> Set[str]:
    """Metric names in a Prometheus text exposition (HELP/TYPE lines and samples)."""
    names: Set[str] = set()
    for raw in exposition.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.startswith("#"):
            parts = line.split()
            if len(parts) >= 3 and parts[1] in ("HELP", "TYPE"):
                names.add(parts[2])
            continue
        match = _METRIC_NAME.match(line)
        if match:
            names.add(match.group(0))
    return names


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


def render_text(target: str, where: str, results: Sequence[Check], color: bool) -> str:
    palette = {"PASS": "32", "WARN": "33", "FAIL": "31", "SKIP": "90"}
    lines = [f"TrustAgent doctor: target {target}, {where}"]
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


def health_detail(body: Mapping[str, Any]) -> str:
    """proxy.live's detail: the facts /health reports."""
    return ", ".join(
        f"{key}={body[key]}" for key in ("version", "demo", "audit_engine") if key in body
    )


def redact(check: Check, secrets: Sequence[str]) -> Check:
    """The check with every occurrence of a key replaced by [redacted]."""

    def clean(value: Any) -> Any:
        if isinstance(value, str):
            for secret in secrets:
                value = value.replace(secret, "[redacted]")
        return value

    check.summary, check.detail, check.fix = (
        clean(check.summary),
        clean(check.detail),
        clean(check.fix),
    )
    check.data = {k: clean(v) for k, v in check.data.items()}
    return check


def split_ids(value: str) -> List[str]:
    return [part.strip() for part in value.split(",") if part.strip()]


def main(
    argv: Optional[Sequence[str]] = None,
    run: Runner = run_command,
    fetch: Fetcher = http_get,
    environ: Optional[Mapping[str, str]] = None,
    post: Poster = http_post,
    probe_port: PortProbe = port_free,
    env_file: Optional[Path] = None,
) -> int:
    env = os.environ if environ is None else environ
    parser = argparse.ArgumentParser(
        prog="doctor.py",
        description="Diagnose a running TrustAgent stack. Exit 0: no FAIL; 1: a check failed.",
    )
    parser.add_argument(
        "--target",
        choices=("auto", "docker", "host", "pages"),
        default="auto",
        help="docker: the compose stack; host: services run with make dev-*; pages: the static "
        "site at --pages-url; auto: docker when the project's proxy container is running "
        "(default)",
    )
    parser.add_argument(
        "--expect",
        choices=("demo", "fresh"),
        help="demo: the zero-key demo (demo mode, mocks only, traces and a completed audit); "
        "fresh: a new install before setup (no admin, no key and no traces yet)",
    )
    probe = parser.add_mutually_exclusive_group()
    probe.add_argument(
        "--no-probe",
        action="store_true",
        help="make no billed call (the default; the gates pass it)",
    )
    probe.add_argument(
        "--probe",
        action="store_true",
        help="also send one fast-check test call through the gateway (about $0.00001; needs "
        "--admin-key-file)",
    )
    parser.add_argument(
        "--admin-key-file",
        metavar="FILE",
        help="file holding an admin key (for --probe; also used for reads without "
        "--api-key-file). The key is never printed",
    )
    parser.add_argument(
        "--api-key-file",
        metavar="FILE",
        help="file holding a key with the read scope (default: TRAFFICGEN_API_KEY from the "
        "environment or .env). The key is never printed",
    )
    parser.add_argument(
        "--pages-url",
        default=PAGES_URL,
        help=f"the static site's base URL for --target pages (default {PAGES_URL})",
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
    keys: Dict[str, Optional[str]] = {"admin": None, "api": None}
    for name, path in (("admin", args.admin_key_file), ("api", args.api_key_file)):
        if path:
            try:
                keys[name] = read_key_file(path)
            except ValueError as exc:
                print(f"doctor: --{name}-key-file: {exc}", file=sys.stderr)
                return 2
    if args.probe and not keys["admin"]:
        print("doctor: --probe needs --admin-key-file FILE (an admin key)", file=sys.stderr)
        return 2
    if args.target == "pages" and args.expect:
        print("doctor: --expect applies to a stack, not to --target pages", file=sys.stderr)
        return 2
    pages_url = args.pages_url.rstrip("/") + "/"
    if args.target == "pages" and not re.match(r"^https?://[^/]+", pages_url):
        print(f"doctor: --pages-url {args.pages_url!r} is not an http(s) URL", file=sys.stderr)
        return 2

    default_project = (
        resolve("COMPOSE_PROJECT_NAME", environ=env, env_file=env_file) or DEFAULT_PROJECT
    )
    project: str = args.project or (DEMO_PROJECT if args.expect == "demo" else default_project)
    host = _host_for(resolve("BIND_ADDR", "127.0.0.1", env_file, env) or "127.0.0.1")
    proxy_port = resolve("PROXY_HOST_PORT", "8080", env_file, env) or "8080"
    dashboard_port = resolve("DASHBOARD_HOST_PORT", "3000", env_file, env) or "3000"
    postgres_port = resolve("POSTGRES_HOST_PORT", "5432", env_file, env) or "5432"
    host_ports = {
        service: int(port)
        for service, port in (
            ("proxy", proxy_port),
            ("dashboard", dashboard_port),
            ("postgres", postgres_port),
        )
        if port.isdigit()
    }
    target = args.target if args.target != "auto" else detect_target(project, run)
    doctor = Doctor(
        target=target,
        project=project,
        default_project=default_project,
        proxy_url=(args.proxy_url or f"http://{host}:{proxy_port}").rstrip("/"),
        dashboard_url=(args.dashboard_url or f"http://{host}:{dashboard_port}").rstrip("/"),
        expect=args.expect,
        api_key=keys["api"] or keys["admin"] or resolve("TRAFFICGEN_API_KEY", None, env_file, env),
        wait_s=args.wait if args.wait is not None else (120.0 if args.expect == "demo" else 0.0),
        run=run,
        fetch=fetch,
        probe=args.probe,
        admin_key=keys["admin"],
        post=post,
        environ=env,
        env_file=env_file or DEFAULT_ENV_FILE,
        bind_host=host,
        host_ports=host_ports,
        port_probe=probe_port,
        pages_url=pages_url,
    )

    results: List[Check] = []
    # No key is ever printed, even one a component echoed back in an error message.
    secrets = [k for k in {keys["admin"], keys["api"], doctor.api_key} if k]
    for check_id, check in doctor.checks():
        if selected(check_id, args.only, args.skip):
            result = check()
            result.data = result.data or doctor.facts(check_id)
            results.append(redact(result, secrets))
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
        where = doctor.pages_url if target == "pages" else f"project {doctor.project}"
        print(render_text(target, where, results, color))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
