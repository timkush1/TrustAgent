#!/usr/bin/env python3
"""Record the public demo with the real fast check and the real judge (D123, D207-D210; standard
library only).

    make record-demo                          # the plan: keys, ledger, caps, checks; nothing starts
    make record-demo CONFIRM=1                # one attempt: record, export, review (stack stays up)
    make record-demo ACCEPT=1 ATTEMPT=<id>    # take the reviewed attempt (FEATURED=<trace>, VIDEO=1)
    make record-demo DISCARD=1                # drop the open attempt and remove the stack

The static site replays a recording (SPEC §2.13). The committed one is simulated (mock checks and a
mock judge, labelled as such); this script makes the real one, kind ``recorded``, in
frontend-react/demo-data/recorded/, which the Pages build ships from a release tag on (D110).

Money (D201, D208). Every attempt is paid from one OpenRouter key, and the source of truth for what
an attempt spent is the change in that key's usage (the free GET /api/v1/key) from before the
attempt to after its stack is gone, read after a 30 s settle. tmp/record-demo/ledger.jsonl keeps
those readings across attempts and survives ``down -v``. What is left is
``R = min(RECORD_MAX_TOTAL_USD - spent, limit_remaining - 0.10)``; an attempt's daily caps are
``decision = min(0.50, R)`` and ``llm = min(3, R - decision)``, and it refuses when R < 0.50.

Without CONFIRM=1 (or with --dry-run) it prints the plan and stops: nothing starts and nothing is
spent (the key's usage is read when a key is set; that call is free). With CONFIRM=1 one attempt:

1. refuses before anything starts (exit 2) without a decision-model key and a judge key, with the
   mock or ``off`` in .env, without OPENROUTER_API_KEY (the ledger reads its usage; the generated
   answers come from OpenRouter), when VERSION is not the release (D216, H-8: the bump comes
   first), when an attempt is still open, when no calibration file exists for a real snapshot and
   ALLOW_UNCALIBRATED is not set (D207), or when R is below $0.50;
2. writes tmp/record-demo/<attempt>/stack.yml (this attempt's caps; with ALLOW_UNCALIBRATED the
   payments-faq route's require_calibration: false, D208) and checks the composed stack
   (docker-compose.yml + docker-compose.record.yml + stack.yml, project truthtable-record): the
   caps, test mode, sync traces, no judge fallback, no host port anywhere, no mock-upstream, the
   generated answers' upstream on OpenRouter with the key, images tagged p6rec;
3. records the key's usage in the ledger, starts the stack from a clean slate (down -v, then the
   p6rec images built with --build-arg VERSION=<VERSION>, so the gateway reports the release) and
   reads GET /health and GET /api/status inside the proxy container
   (the stack publishes no port, so doctor cannot reach it): the fast check ready with a snapshot
   and a vendor endpoint, the judge up, real and not on its fallback;
4. checks the snapshot against config/calibration/<snapshot>.json (D207): its quality goes into
   the manifest, or quality none with ALLOW_UNCALIBRATED=1 (the UI then says uncalibrated);
5. runs RECORD_PASSES rounds of trafficgen --record --upstream generated --model-prefix openai/
   --max-tokens 300 inside the proxy container (scripted answers by test_response, the generated
   pair through OpenRouter) and writes drift.json and a drift table (a real model's answers may
   differ from the script; the tape is never edited);
6. exports the traces of the rounds with export-replay, naming the hosts the stack called (D203)
   and the calibration, into tmp/record-demo/<attempt>/tape/, and checks it with
   check_recording.py; manifest.featured is picked by the D209 rule;
7. reads the key's usage again (after the settle), prints the review and one
   ``TRUSTAGENT_P6_RESULT {json}`` line, and leaves the stack up for the review.

Exit status of an attempt: 0 ready for review, 4 review needed (no trace meets the featured rule:
pick one with ACCEPT=1 ATTEMPT=<id> FEATURED=<trace id>, or DISCARD=1 and try again within the
cap), 1 a step failed after the stack started (the stack is removed unless --keep), 2 refused
before anything started.

ACCEPT=1 ATTEMPT=<id> takes a reviewed attempt: it refuses an attempt that is not under review or
in which a trace was skipped on budget, re-exports with the owner's FEATURED pick when given,
checks the tape with check_recording.py --require-recorded (hosts, featured, calibration and
trustagent_version == VERSION), records the video's stack beats when VIDEO=1 (D210), swaps the tape
into frontend-react/demo-data/recorded/ and removes the stack (down -v). DISCARD=1 removes the stack
and closes the open attempt in the ledger. Both end with the ledger's final reading and a result
line. No key is ever printed, nor the key's label.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, TextIO, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import resolve  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
PROJECT = "truthtable-record"
COMPOSE_FILES = ("docker-compose.yml", "docker-compose.record.yml")
PACK = ROOT / "contracts" / "demo" / "scenarios.v1.json"
COMMON_SCHEMA = ROOT / "contracts" / "schema" / "common.v1.schema.json"
DEST = ROOT / "frontend-react" / "demo-data" / "recorded"
WORK = ROOT / "tmp" / "record-demo"
CALIBRATION_DIR = ROOT / "config" / "calibration"
VERSION_FILE = ROOT / "VERSION"

# The release the tape ships with (D216; H-8: VERSION is bumped before the recording).
RELEASE_VERSION = "2.0.0"
# The record stack's image tag: built from this checkout, never pushed (D208).
IMAGE_TAG = "p6rec"

SCENARIOS = "demo"
EXCLUDE = "knowledge"
DEFAULT_PASSES = 2
# D201/D208: every attempt together, and what each attempt's daily caps may be at most.
DEFAULT_TOTAL_USD = 4.0
DECISION_CAP_MAX_USD = 0.50
LLM_CAP_MAX_USD = 3.0
# An attempt needs at least this much left; the key keeps this much unspent (D201).
MIN_REMAINING_USD = 0.50
KEY_RESERVE_USD = 0.10
# The key's usage settles this long after the last call before it is read (D208).
SETTLE_S = 30.0
# What one pass costs at list prices (an estimate; the caps bound it whatever happens).
ESTIMATE_USD_PER_PASS = 0.5
# Generated answers (D208): OpenRouter's model ids carry the vendor; answers stay short.
MODEL_PREFIX = "openai/"
MAX_TOKENS = 300

CAP_KEYS = ("TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY")
# docker-compose.record.yml must give the proxy and the engine these values (D123, D208).
PROXY_ENV = {
    "TRUTHTABLE_TEST_MODE": "true",
    "TRUTHTABLE_TRACE_FLUSH": "sync",
    "TRUTHTABLE_DEMO_MODE": "false",
    "TRUTHTABLE_STRICT_CONFIG": "true",
    "TRUTHTABLE_PUBLIC_READ": "false",
}
ENGINE_ENV = {"LLM_FALLBACK": "none", "TRUTHTABLE_DEMO_MODE": "false"}
# The policy the ALLOW_UNCALIBRATED overlay mounts (D208), and the route it relaxes.
RECORD_POLICY_PATH = "/etc/trustagent-record/policies.yaml"
UNCALIBRATED_ROUTE = "payments-faq"

# Keys the stack reads from .env: a decision-model key, and the judge's key for its provider.
DECISION_KEYS = ("OPENROUTER_API_KEY", "TYPESAFE_API_KEY", "TRUTHTABLE_JEV_API_KEY")
JUDGE_KEYS = {
    "openrouter": ("OPENROUTER_API_KEY",),
    "anthropic": ("ANTHROPIC_API_KEY",),
    "openai": ("OPENAI_API_KEY",),
}

# In the proxy container (alpine, /tmp writable by its user).
TRAFFICGEN = "/trafficgen"
EXPORT = "/export-replay"
CONTAINER_IDS = "/tmp/record-demo-ids-{n}.txt"  # nosec B108 - a path inside the container
CONTAINER_DRIFT = "/tmp/record-demo-drift-{n}.json"  # nosec B108 - a path inside the container
CONTAINER_EXPORT = "/tmp/record-demo-export"  # nosec B108 - a path inside the container
PROXY_INTERNAL_URL = "http://127.0.0.1:8080"
# GET /api/status from inside the proxy container with the stack's own read key (never printed:
# the variable is expanded by the container's shell).
STATUS_COMMAND = (
    'wget -q -O - --header "X-TrustAgent-Key: $TRAFFICGEN_API_KEY" '
    "http://127.0.0.1:8080/api/status"
)

PREFLIGHT_TIMEOUT_S = 120.0
PREFLIGHT_INTERVAL_S = 5.0
RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$")
ATTEMPT_ID = re.compile(r"^\d{8}T\d{6}Z$")
TRACE_ID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
RECORDABLE_STATES = ("ready", "degraded")
FLAGGED_ACTIONS = ("flag", "fallback", "block")

EXIT_OK = 0
EXIT_FAILED = 1
EXIT_REFUSED = 2
EXIT_REVIEW = 4

# Attempt states (attempt.json).
RUNNING, REVIEW, FAILED, ACCEPTED, DISCARDED = (
    "running",
    "review",
    "failed",
    "accepted",
    "discarded",
)

# run(argv, stream, env): (returncode, stdout, stderr). With stream=True the output goes straight
# to the terminal (long steps), and the returned strings are empty. env holds extra variables for
# the child (compose interpolation).
Runner = Callable[[Sequence[str], bool, Optional[Mapping[str, str]]], Tuple[int, str, str]]


def run_command(
    argv: Sequence[str], stream: bool = False, env: Optional[Mapping[str, str]] = None
) -> Tuple[int, str, str]:
    child_env = {**os.environ, **env} if env else None
    try:
        if stream:
            return subprocess.run(list(argv), check=False, env=child_env).returncode, "", ""
        done = subprocess.run(
            list(argv), capture_output=True, text=True, check=False, env=child_env
        )
    except FileNotFoundError:
        return 127, "", f"{argv[0]}: command not found"
    except OSError as exc:
        return 126, "", str(exc)
    return done.returncode, done.stdout, done.stderr


class Refused(Exception):
    """Refused before the stack started: exit 2."""


class Failed(Exception):
    """A step failed after the stack started: exit 1."""


class KeyReadError(Exception):
    """GET /api/v1/key could not be read (the message never holds the key)."""


# ------------------------------------------------------------------------------ the scenarios


@dataclass
class Plan:
    scenarios: List[str]
    generated: List[str]
    passes: int = DEFAULT_PASSES

    @property
    def requests(self) -> int:
        return self.passes * len(self.scenarios)


def load_plan(pack: Path = PACK, passes: int = DEFAULT_PASSES) -> Plan:
    """The scenarios trafficgen --scenarios demo --exclude knowledge sends, from the pack."""
    doc = json.loads(pack.read_text(encoding="utf-8"))
    chosen = [
        s
        for s in doc.get("scenarios", [])
        if SCENARIOS in s.get("sets", []) and EXCLUDE not in s.get("sets", [])
    ]
    return Plan(
        scenarios=[str(s["id"]) for s in chosen],
        generated=[str(s["id"]) for s in chosen if "generated" in s.get("sets", [])],
        passes=passes,
    )


# ------------------------------------------------------------------------------ keys


@dataclass
class Keys:
    """Which keys are set (never their values) and what .env picks."""

    decision: List[str]
    judge: List[str]
    judge_provider: str
    jev_backend: str
    openrouter: bool = False
    problems: List[str] = field(default_factory=list)


def judge_candidates(provider: str) -> Tuple[str, ...]:
    """The keys the judge can use: its provider's, or any provider's under auto."""
    if provider in JUDGE_KEYS:
        return JUDGE_KEYS[provider]
    return tuple(dict.fromkeys(k for keys in JUDGE_KEYS.values() for k in keys))


def read_keys(environ: Mapping[str, str], env_file: Optional[Path] = None) -> Keys:
    def has(name: str) -> bool:
        return bool(resolve(name, environ=environ, env_file=env_file))

    provider = (resolve("LLM_PROVIDER", "auto", env_file, environ) or "auto").strip().lower()
    backend = (resolve("TRUTHTABLE_JEV_BACKEND", "auto", env_file, environ) or "auto").strip()
    backend = backend.lower()
    decision = [k for k in DECISION_KEYS if has(k)]
    candidates = judge_candidates(provider)
    judge = [k for k in candidates if has(k)]
    keys = Keys(
        decision=decision,
        judge=judge,
        judge_provider=provider,
        jev_backend=backend,
        openrouter=has("OPENROUTER_API_KEY"),
    )
    if backend in ("mock", "off"):
        keys.problems.append(
            f"TRUTHTABLE_JEV_BACKEND={backend}: a recording needs the real fast check "
            "(set it to auto)"
        )
    elif not decision:
        keys.problems.append(
            "no decision-model key: set OPENROUTER_API_KEY in .env (one key covers the fast "
            "check and the judge; docs/JEV-ACCESS.md)"
        )
    if provider in ("mock", "none", "off"):
        keys.problems.append(
            f"LLM_PROVIDER={provider}: a recording needs the real judge (set it to auto)"
        )
    elif not judge:
        names = " or ".join(candidates)
        keys.problems.append(f"no judge key for LLM_PROVIDER={provider}: set {names} in .env")
    if not keys.openrouter:
        keys.problems.append(
            "no OPENROUTER_API_KEY: the generated answers come from OpenRouter, and the ledger "
            "reads that key's usage (GET /api/v1/key, D208)"
        )
    return keys


# ------------------------------------------------------------------------------ the key's usage


@dataclass
class KeyState:
    """GET /api/v1/key: what the key has spent and what its limit leaves (never its label)."""

    usage: float
    limit: Optional[float]
    limit_remaining: Optional[float]


KeyReader = Callable[[], KeyState]


def parse_key(body: Any) -> KeyState:
    data = body.get("data") if isinstance(body, dict) else None
    if not isinstance(data, dict):
        raise KeyReadError("GET /api/v1/key did not answer a key object")

    def num(name: str) -> Optional[float]:
        v = data.get(name)
        if v is None:
            return None
        if isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v):
            raise KeyReadError(f"GET /api/v1/key: {name} is not a number")
        return float(v)

    usage = num("usage")
    if usage is None:
        raise KeyReadError("GET /api/v1/key has no usage")
    limit = num("limit")
    remaining = num("limit_remaining")
    if remaining is None and limit is not None:
        remaining = limit - usage
    return KeyState(usage=usage, limit=limit, limit_remaining=remaining)


def openrouter_key_reader(key: str, base_url: str, timeout: float = 15.0) -> KeyReader:
    """Reads GET {base}/api/v1/key with the key (free). Errors never carry the key."""
    url = base_url.rstrip("/") + "/api/v1/key"
    parsed = urllib.parse.urlparse(url)
    local = parsed.hostname in ("localhost", "127.0.0.1")
    if parsed.scheme != "https" and not (parsed.scheme == "http" and local):
        raise KeyReadError(f"the OpenRouter base URL must be https (got {parsed.scheme}://)")

    def read() -> KeyState:
        req = urllib.request.Request(
            url, headers={"Authorization": f"Bearer {key}", "Accept": "application/json"}
        )
        try:
            # The URL was checked above: https (or http to localhost), never file: or ftp:.
            with urllib.request.urlopen(req, timeout=timeout) as resp:  # nosec B310
                body = json.loads(resp.read(1 << 20).decode("utf-8"))
        except urllib.error.HTTPError as exc:
            raise KeyReadError(f"GET /api/v1/key answered HTTP {exc.code}") from None
        except (urllib.error.URLError, OSError, ValueError) as exc:
            raise KeyReadError(f"GET /api/v1/key failed ({type(exc).__name__})") from None
        return parse_key(body)

    return read


# ------------------------------------------------------------------------------ the ledger


def floor_cents(value: float) -> float:
    """value rounded down to a cent (a cap never rounds up)."""
    return math.floor(round(value * 100, 6)) / 100


@dataclass
class Caps:
    """One attempt's budget (D208)."""

    remaining: float
    decision: float
    llm: float
    refused: Optional[str] = None

    @property
    def total(self) -> float:
        return round(self.decision + self.llm, 2)


def plan_caps(total_usd: float, spent: float, limit_remaining: Optional[float]) -> Caps:
    """R = min(total - spent, limit_remaining - 0.10); decision = min(0.50, R), llm = min(3,
    R - decision); refused when R < 0.50."""
    remaining = total_usd - spent
    if limit_remaining is not None:
        remaining = min(remaining, limit_remaining - KEY_RESERVE_USD)
    remaining = round(remaining, 6)
    if remaining < MIN_REMAINING_USD:
        why = (
            f"${max(remaining, 0):.2f} is left (RECORD_MAX_TOTAL_USD ${total_usd:.2f} less "
            f"${spent:.4f} spent"
            + (
                f", the key's limit leaving ${limit_remaining:.2f} less the ${KEY_RESERVE_USD:.2f} "
                "reserve"
                if limit_remaining is not None
                else ""
            )
            + f"); an attempt needs at least ${MIN_REMAINING_USD:.2f}"
        )
        return Caps(remaining=remaining, decision=0.0, llm=0.0, refused=why)
    decision = floor_cents(min(DECISION_CAP_MAX_USD, remaining))
    llm = floor_cents(min(LLM_CAP_MAX_USD, remaining - decision))
    return Caps(remaining=remaining, decision=decision, llm=llm)


@dataclass
class AttemptBook:
    """What the ledger says about one attempt."""

    attempt: str
    usage_before: Optional[float] = None
    spent: Optional[float] = None
    ended: bool = False
    outcome: Optional[str] = None


class Ledger:
    """tmp/record-demo/ledger.jsonl: one JSON object per line, appended, never rewritten. It lives
    outside every volume, so ``down -v`` leaves it (D208)."""

    def __init__(self, path: Path) -> None:
        self.path = path

    def entries(self) -> List[Dict[str, Any]]:
        if not self.path.is_file():
            return []
        out: List[Dict[str, Any]] = []
        for n, line in enumerate(self.path.read_text(encoding="utf-8").splitlines(), 1):
            if not line.strip():
                continue
            try:
                entry = json.loads(line)
            except ValueError as exc:
                raise Refused(f"{self.path} line {n} is not JSON ({exc}); fix it by hand") from exc
            if isinstance(entry, dict):
                out.append(entry)
        return out

    def append(self, entry: Dict[str, Any]) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self.path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(entry, sort_keys=True) + "\n")
            f.flush()
            os.fsync(f.fileno())

    def books(self) -> Dict[str, AttemptBook]:
        books: Dict[str, AttemptBook] = {}
        for e in self.entries():
            attempt = str(e.get("attempt") or "")
            book = books.setdefault(attempt, AttemptBook(attempt=attempt))
            event = e.get("event")
            if event == "start":
                book.usage_before = _number(e.get("usage_before_usd"))
            elif event in ("measure", "end") and not book.ended:
                spent = _number(e.get("spent_usd"))
                if spent is not None:
                    book.spent = spent
                if event == "end":
                    book.ended, book.outcome = True, str(e.get("outcome") or "")
        return books

    def spent(self) -> float:
        """Every attempt's spend: its final reading, else its latest one."""
        return round(sum(b.spent or 0.0 for b in self.books().values()), 6)

    def open_attempt(self) -> Optional[str]:
        """The attempt that started and has not ended (at most one: a new one is refused)."""
        for book in self.books().values():
            if not book.ended:
                return book.attempt
        return None


def _number(v: Any) -> Optional[float]:
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        return None
    return float(v)


def utc_datetime() -> datetime:
    return datetime.now(timezone.utc)


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# ------------------------------------------------------------------------------ hosts (D203)


def vendor_hosts() -> Tuple[str, ...]:
    """common.v1 VendorHost: the only hosts a recorded tape may name."""
    doc = json.loads(COMMON_SCHEMA.read_text(encoding="utf-8"))
    return tuple(doc["$defs"]["VendorHost"]["enum"])


def host_of(url: str) -> str:
    try:
        return (urllib.parse.urlparse(url.strip()).hostname or "").lower()
    except ValueError:
        return ""


def judge_host(provider: str, openrouter_base: str) -> str:
    """The host a judge provider calls (mirrors recording.JudgeHost in Go)."""
    p = provider.lower()
    if p == "openrouter":
        return host_of(openrouter_base) if openrouter_base else "openrouter.ai"
    if p == "anthropic":
        return "api.anthropic.com"
    if p == "openai":
        return "api.openai.com"
    return ""


# ------------------------------------------------------------------------------ calibration (D207)


def calibration_file(snapshot: str, directory: Path = CALIBRATION_DIR) -> Path:
    """config/calibration/<snapshot with / as __>.json (decision.CalibrationFileName)."""
    name = snapshot.replace("/", "__")
    if not re.match(r"^[A-Za-z0-9._-]+$", name) or name.startswith("."):
        raise Failed(f"the snapshot {snapshot!r} does not make a calibration file name")
    return directory / f"{name}.json"


def calibration_quality(path: Path) -> str:
    """measured when every entry is measured, else provisional; a mock file never backs a tape."""
    doc = json.loads(path.read_text(encoding="utf-8"))
    qualities = [str(e.get("quality")) for e in doc.get("entries") or [] if isinstance(e, dict)]
    if not qualities:
        raise Failed(f"{path.name} has no calibration entries")
    if "mock" in qualities:
        raise Failed(f"{path.name} is a mock calibration; it cannot back a recorded tape")
    return "measured" if all(q == "measured" for q in qualities) else "provisional"


def real_calibrations(directory: Path = CALIBRATION_DIR) -> List[str]:
    """Calibration files for a real (not mock) snapshot."""
    out: List[str] = []
    for path in sorted(directory.glob("*.json")):
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            continue
        if (doc.get("decision_model") or {}).get("backend") not in (None, "mock"):
            out.append(path.name)
    return out


def uncalibrated_policy(text: str) -> str:
    """config/policies.yaml with require_calibration: false on the payments-faq route (D208)."""
    lines = text.splitlines(keepends=True)
    start = next(
        (i for i, line in enumerate(lines) if line.rstrip() == f"  - name: {UNCALIBRATED_ROUTE}"),
        None,
    )
    if start is None:
        raise Refused(f"config/policies.yaml has no {UNCALIBRATED_ROUTE} route")
    end = next(
        (i for i in range(start + 1, len(lines)) if lines[i].startswith("  - name: ")),
        len(lines),
    )
    if any(line.strip().startswith("require_calibration:") for line in lines[start:end]):
        raise Refused(f"the {UNCALIBRATED_ROUTE} route already sets require_calibration")
    mode = next(
        (i for i in range(start + 1, end) if lines[i].startswith("    mode: ")),
        None,
    )
    if mode is None:
        raise Refused(f"the {UNCALIBRATED_ROUTE} route has no mode line")
    note = "    require_calibration: false # record-demo ALLOW_UNCALIBRATED=1 (D208)\n"
    return "".join(lines[: mode + 1] + [note] + lines[mode + 1 :])


# ------------------------------------------------------------------------------ the stack


def verify_stack(config: Mapping[str, Any], caps: Optional[Caps] = None) -> List[str]:
    """What docker compose config must show for the record stack (D123, D208). Values are never
    quoted back: the configuration holds the upstream key."""
    problems: List[str] = []
    services = config.get("services") or {}
    for name in ("proxy", "engine", "postgres"):
        if name not in services:
            problems.append(f"the stack has no {name} service")
    for name in ("mock-upstream", "trafficgen"):
        if name in services:
            problems.append(f"{name} must not run in the record stack (its profile is on)")

    def env(service: str) -> Mapping[str, Any]:
        return (services.get(service) or {}).get("environment") or {}

    for service, wanted in (("proxy", PROXY_ENV), ("engine", ENGINE_ENV)):
        have = env(service)
        for key, value in wanted.items():
            if str(have.get(key)) != value:
                problems.append(f"{service}: {key} must be {value} (docker-compose.record.yml)")
    proxy = env("proxy")
    if caps is not None:
        for key, want in zip(CAP_KEYS, (caps.decision, caps.llm)):
            try:
                have_cap = float(str(proxy.get(key)))
            except ValueError:
                have_cap = float("nan")
            if not math.isclose(have_cap, want, abs_tol=1e-9):
                problems.append(f"proxy: {key} must be this attempt's cap {want:.2f} (stack.yml)")
    upstream = host_of(str(proxy.get("TRUTHTABLE_UPSTREAM_URL") or ""))
    if upstream != "openrouter.ai":
        problems.append(
            "proxy: TRUTHTABLE_UPSTREAM_URL must be OpenRouter's (https://openrouter.ai/api): the "
            "generated answers come from there (D208)"
        )
    if not str(proxy.get("TRUTHTABLE_UPSTREAM_API_KEY") or ""):
        problems.append(
            "proxy: TRUTHTABLE_UPSTREAM_API_KEY is empty: the overlay passes OPENROUTER_API_KEY "
            "from .env"
        )
    for name, service in services.items():
        if (service or {}).get("ports"):
            problems.append(f"{name} publishes a port; the record stack publishes none (D208)")
    for name in ("proxy", "engine", "dashboard"):
        image = str((services.get(name) or {}).get("image") or "")
        if name in services and f":{IMAGE_TAG}" not in image:
            problems.append(
                f"{name}: the image must be tagged {IMAGE_TAG} (TRUSTAGENT_VERSION={IMAGE_TAG})"
            )
    return problems


def free_port() -> int:
    """A free TCP port on 127.0.0.1 for the video's dashboard (D210), chosen at the attempt's start
    so the proxy can allow its origin from the first moment."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def stack_override(
    caps: Caps, policy: Optional[Path], video_port: Optional[int] = None, admin_key: str = ""
) -> str:
    """tmp/record-demo/<attempt>/stack.yml: this attempt's caps, the ALLOW_UNCALIBRATED policy when
    there is one, and what the video's stack beats need (D210): an admin key of this attempt only
    and the origin of the dashboard port launch_video.py publishes on 127.0.0.1 for them."""
    lines = [
        "# Written by scripts/record_demo.py for one attempt (D208); removed with tmp/.",
        "services:",
        "  proxy:",
        "    environment:",
        f'      {CAP_KEYS[0]}: "{caps.decision:.2f}"',
        f'      {CAP_KEYS[1]}: "{caps.llm:.2f}"',
    ]
    if admin_key:
        lines.append(f'      TRUTHTABLE_ADMIN_KEYS: "video={admin_key}"')
    if video_port is not None:
        origins = f"http://localhost:{video_port},http://127.0.0.1:{video_port}"
        lines += [
            f'      TRUTHTABLE_ALLOWED_ORIGINS: "{origins}"',
            f'      TRUTHTABLE_PUBLIC_URL: "http://localhost:{video_port}"',
        ]
    if policy is not None:
        lines += [
            f"      TRUTHTABLE_POLICY_FILE: {RECORD_POLICY_PATH}",
            "    volumes:",
            f"      - {json.dumps(str(policy) + ':' + RECORD_POLICY_PATH + ':ro')}",
        ]
    return "\n".join(lines) + "\n"


def shown_path(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(ROOT))
    except ValueError:
        return str(path)


def money(value: Any) -> str:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return "unknown"
    return f"${value:.4f}"


def scenario_of(trace: Mapping[str, Any]) -> str:
    """The demo scenario of a trafficgen trace (X-Request-ID trafficgen:<scenario>:<n>)."""
    parts = str(trace.get("client_request_id") or "").split(":")
    return parts[1] if len(parts) == 3 and parts[0] == "trafficgen" else ""


def skipped_reasons(tape: Path, manifest: Mapping[str, Any]) -> List[str]:
    """Traces of the tape that a budget cut short (D208: such an attempt cannot be accepted): a
    fast check skipped or degraded on budget, a deep audit dropped."""
    reasons: List[str] = []
    for t in manifest.get("traces") or []:
        tid = str(t.get("trace_id"))
        why: List[str] = []
        if t.get("skip_reason") == "budget":
            why.append("its fast check was skipped on the decision budget")
        if "llm_budget" in (t.get("flags") or []):
            why.append("its deep audit was dropped on the LLM budget")
        elif (t.get("audit") or {}).get("status") == "dropped":
            why.append("its deep audit was dropped")
        path = tape / "traces" / f"{tid}.json"
        if path.is_file():
            try:
                detail = json.loads(path.read_text(encoding="utf-8"))
            except ValueError:
                detail = {}
            for d in detail.get("decisions") or []:
                if isinstance(d, dict) and "budget" in (d.get("degraded") or []):
                    why.append("its fast check ran degraded on budget")
                    break
        if why:
            reasons.append(f"{tid}: " + "; ".join(why))
    return reasons


@dataclass
class Recorder:
    run: Runner = run_command
    environ: Mapping[str, str] = field(default_factory=lambda: dict(os.environ))
    env_file: Optional[Path] = None
    dest: Path = DEST
    work: Path = WORK
    calibration_dir: Path = CALIBRATION_DIR
    version_file: Path = VERSION_FILE
    policy_file: Path = ROOT / "config" / "policies.yaml"
    out: TextIO = sys.stdout
    err: TextIO = sys.stderr
    sleep: Callable[[float], None] = time.sleep
    clock: Callable[[], float] = time.monotonic
    now: Callable[[], datetime] = utc_datetime
    python: str = sys.executable or "python3"
    key_reader: Optional[KeyReader] = None
    port_finder: Callable[[], int] = free_port
    keep: bool = False
    wait_timeout: int = 600
    since: Optional[str] = None
    preflight_timeout: float = PREFLIGHT_TIMEOUT_S
    settle_s: float = SETTLE_S
    passes: int = DEFAULT_PASSES
    total_usd: float = DEFAULT_TOTAL_USD
    allow_uncalibrated: bool = False
    release_version: str = RELEASE_VERSION

    def say(self, message: str) -> None:
        print(message, file=self.out, flush=True)

    @property
    def ledger(self) -> Ledger:
        return Ledger(self.work / "ledger.jsonl")

    def attempt_dir(self, attempt: str) -> Path:
        return self.work / attempt

    def compose(self, attempt: Optional[str] = None) -> List[str]:
        argv = ["docker", "compose", "-p", PROJECT]
        for name in COMPOSE_FILES:
            argv += ["-f", str(ROOT / name)]
        if attempt is not None:
            override = self.attempt_dir(attempt) / "stack.yml"
            if override.is_file():
                argv += ["-f", str(override)]
        return argv

    @property
    def compose_env(self) -> Dict[str, str]:
        """The variables compose interpolates: the images are tagged p6rec (D208)."""
        return {"TRUSTAGENT_VERSION": IMAGE_TAG}

    def build_args(self) -> List[str]:
        """`docker compose build` arguments that stamp the release into the binaries: compose
        passes TRUSTAGENT_VERSION (p6rec) as the VERSION build argument, and the tape names the
        version the proxy reports, which must be VERSION's."""
        sha = self.must(
            ["git", "-C", str(ROOT), "rev-parse", "--short=12", "HEAD"],
            "git rev-parse",
            compose=False,
        ).strip()
        version = self.repo_version() or self.release_version
        return ["--build-arg", f"VERSION={version}", "--build-arg", f"GIT_SHA={sha}"]

    def keys(self) -> Keys:
        return read_keys(self.environ, self.env_file)

    def reader(self) -> Optional[KeyReader]:
        if self.key_reader is not None:
            return self.key_reader
        key = resolve("OPENROUTER_API_KEY", environ=self.environ, env_file=self.env_file)
        if not key:
            return None
        base = (
            resolve("TRUTHTABLE_OPENROUTER_BASE_URL", None, self.env_file, self.environ)
            or "https://openrouter.ai"
        )
        return openrouter_key_reader(key, base)

    def read_key(self) -> KeyState:
        reader = self.reader()
        if reader is None:
            raise KeyReadError("no OPENROUTER_API_KEY")
        return reader()

    def repo_version(self) -> str:
        try:
            return self.version_file.read_text(encoding="utf-8").strip()
        except OSError:
            return ""

    # ---------- the plan ----------

    def print_plan(self, plan: Plan, keys: Keys) -> Optional[Caps]:
        def state(names: Sequence[str], wanted: Sequence[str]) -> str:
            return ", ".join(f"{n} set" for n in names) or ("missing (" + " or ".join(wanted) + ")")

        judge_wanted = judge_candidates(keys.judge_provider)
        ledger = self.ledger
        spent = ledger.spent()
        books = ledger.books()
        open_attempt = ledger.open_attempt()
        blocked = list(keys.problems)
        lines = [
            "Record the public demo (make record-demo, D123, D207-D210)",
            f"  Stack     project {PROJECT}: " + " + ".join(COMPOSE_FILES) + " + this attempt's "
            f"stack.yml; images tagged {IMAGE_TAG}, built from this checkout; no host port",
            "  Backends  from .env: the real fast check and the real judge, no fallback; "
            "the mock, off and mock-upstream are refused",
            f"  Keys      fast check: {state(keys.decision, DECISION_KEYS)}; judge "
            f"(LLM_PROVIDER={keys.judge_provider}): {state(keys.judge, judge_wanted)} "
            "(values never shown)",
            f"  Traffic   {plan.passes} pass(es) of trafficgen --record --scenarios {SCENARIOS} "
            f"--exclude {EXCLUDE}: {len(plan.scenarios)} scenarios, {plan.requests} requests; "
            f"{len(plan.generated)} answers per pass come from OpenRouter (model {MODEL_PREFIX}…, "
            f"max_tokens {MAX_TOKENS}: " + ", ".join(plan.generated) + "), the rest are scripted",
        ]
        caps: Optional[Caps] = None
        key_line = "  Key       no OPENROUTER_API_KEY: plan only (no usage read)"
        limit_remaining: Optional[float] = None
        if keys.openrouter or self.key_reader is not None:
            try:
                ks = self.read_key()
                limit_remaining = ks.limit_remaining
                limit = (
                    f"limit ${ks.limit:.2f}, ${ks.limit_remaining:.4f} left"
                    if ks.limit is not None and ks.limit_remaining is not None
                    else "no limit set (D201 sets one: $8)"
                )
                key_line = f"  Key       usage ${ks.usage:.4f} (GET /api/v1/key, free); {limit}"
            except KeyReadError as exc:
                key_line = f"  Key       could not read the key's usage: {exc}"
                blocked.append(f"the key's usage could not be read ({exc})")
        lines.append(key_line)
        lines.append(
            f"  Ledger    {shown_path(ledger.path)}: ${spent:.4f} spent over {len(books)} "
            f"attempt(s) of RECORD_MAX_TOTAL_USD ${self.total_usd:.2f}"
            + (f"; attempt {open_attempt} is open (ACCEPT or DISCARD it)" if open_attempt else "")
        )
        if open_attempt:
            blocked.append(
                f"attempt {open_attempt} is still open: make record-demo ACCEPT=1 "
                f"ATTEMPT={open_attempt}, or DISCARD=1"
            )
        if keys.openrouter or self.key_reader is not None:
            caps = plan_caps(self.total_usd, spent, limit_remaining)
            if caps.refused:
                lines.append(f"  Caps      refused: {caps.refused}")
                blocked.append(caps.refused)
            else:
                lines.append(
                    f"  Caps      R ${caps.remaining:.2f} left: this attempt's daily caps are "
                    f"fast checks ${caps.decision:.2f}, deep audits ${caps.llm:.2f} (hard; a trace "
                    "skipped on budget makes the attempt unacceptable)"
                )
        else:
            lines.append(
                f"  Caps      decision min($0.50, R), deep audits min($3, R - decision); "
                f"R = min(${self.total_usd:.2f} - spent, key limit left - $0.10)"
            )
        lines.append(
            f"  Cost      about ${ESTIMATE_USD_PER_PASS * plan.passes:.2f} (estimate, at list "
            "prices; the caps bound it; the ledger records the key's real usage change)"
        )
        version = self.repo_version()
        if version != self.release_version:
            blocked.append(
                f"VERSION is {version or 'missing'}, not {self.release_version}: bump first "
                f"(make version-bump V={self.release_version}; D216, H-8)"
            )
        lines.append(
            f"  Version   VERSION {version or 'missing'}; the tape ships with "
            f"{self.release_version} (check_recording --require-recorded)"
        )
        cals = real_calibrations(self.calibration_dir)
        if cals:
            lines.append(
                "  Calib.    " + ", ".join(cals) + " (the snapshot's file is checked after start)"
            )
        elif self.allow_uncalibrated:
            lines.append(
                f"  Calib.    none; ALLOW_UNCALIBRATED=1: {UNCALIBRATED_ROUTE} runs with "
                "require_calibration: false and the tape says uncalibrated"
            )
        else:
            lines.append("  Calib.    no calibration file for a real snapshot (D207)")
            blocked.append(
                "no calibration for a real snapshot in config/calibration (make calib-pass, then "
                "make calibrate WRITE=1), or ALLOW_UNCALIBRATED=1"
            )
        lines.append(
            f"  Output    {shown_path(self.work)}/<attempt>/ (tape, drift.json), reviewed with the "
            f"stack up; ACCEPT=1 ATTEMPT=<id> copies it to {shown_path(self.dest)}"
        )
        for line in lines:
            self.say(line)
        for problem in blocked:
            self.say(f"  Blocked   {problem} (a CONFIRM=1 run refuses)")
        return caps

    # ---------- steps ----------

    def must(
        self,
        argv: Sequence[str],
        what: str,
        stream: bool = False,
        refuse: bool = False,
        compose: bool = True,
    ) -> str:
        code, out, err = self.run(argv, stream, self.compose_env if compose else None)
        if code != 0:
            tail = (err or out).strip().splitlines()[-5:]
            message = f"{what} failed (exit {code})" + (": " + " / ".join(tail) if tail else "")
            raise Refused(message) if refuse else Failed(message)
        return out

    def stack_config(self, attempt: str) -> Dict[str, Any]:
        raw = self.must(
            self.compose(attempt) + ["config", "--format", "json"],
            "docker compose config",
            refuse=True,
        )
        try:
            config = json.loads(raw)
        except ValueError as exc:
            raise Refused("docker compose config did not print JSON") from exc
        if not isinstance(config, dict):
            raise Refused("docker compose config did not print an object")
        return config

    def check_stack_config(self, attempt: str, caps: Caps) -> Dict[str, Any]:
        if self.run(["docker", "compose", "version"], False, None)[0] != 0:
            raise Refused("docker compose is not available (Docker Desktop or the compose plugin)")
        config = self.stack_config(attempt)
        problems = verify_stack(config, caps)
        if problems:
            raise Refused(
                "the record stack is not configured as D123 and D208 say: " + "; ".join(problems)
            )
        return config

    def status(self, attempt: str) -> Dict[str, Any]:
        """GET /api/status from inside the proxy container."""
        code, out, err = self.run(
            self.compose(attempt) + ["exec", "-T", "proxy", "sh", "-c", STATUS_COMMAND],
            False,
            self.compose_env,
        )
        if code != 0:
            raise Failed(f"GET /api/status in the proxy container failed (exit {code})")
        try:
            doc = json.loads(out)
        except ValueError as exc:
            raise Failed("GET /api/status did not answer JSON") from exc
        if not isinstance(doc, dict):
            raise Failed("GET /api/status did not answer an object")
        return doc

    def preflight(self, attempt: str) -> Dict[str, Any]:
        """The fast check ready with a snapshot on a vendor endpoint; the judge up, real, no
        fallback. Waits while the first check runs."""
        deadline = self.clock() + self.preflight_timeout
        vendors = vendor_hosts()
        while True:
            st = self.status(attempt)
            dm = st.get("decision_model") or {}
            judge = st.get("judge") or {}
            backend, state = str(dm.get("backend") or ""), str(dm.get("state") or "")
            provider = str(judge.get("provider") or "")
            if backend in ("mock", "off") or state in ("mock", "off"):
                raise Failed(
                    f"preflight: the fast check is {backend or state}; a recording needs the real "
                    "one (TRUTHTABLE_JEV_BACKEND=auto and OPENROUTER_API_KEY)"
                )
            host = str(dm.get("endpoint_host") or "")
            if host and host not in vendors:
                raise Failed(
                    f"preflight: non_vendor_host: the fast check calls {host}, not a vendor host "
                    f"({', '.join(vendors)}); the tape could only be simulated (D203)"
                )
            if provider in ("", "mock", "none"):
                raise Failed(
                    f"preflight: the judge is {provider or 'missing'}; a recording needs the real "
                    "judge (LLM_PROVIDER=auto and a judge key)"
                )
            if judge.get("fallback_active") is True:
                raise Failed(
                    "preflight: the judge is running its fallback; a fallback verdict cannot be "
                    "labelled recorded"
                )
            if state in ("unauthorized", "no_credits", "misconfigured", "budget_exhausted"):
                raise Failed(f"preflight: the fast check is {state} (docs/JEV-ACCESS.md)")
            ready = (
                state in RECORDABLE_STATES
                and bool(dm.get("snapshot"))
                and bool(host)
                and judge.get("state") == "up"
            )
            if ready:
                self.check_version(attempt)
                self.say(
                    f"  preflight: fast check {backend} ({state}) on {host}, snapshot "
                    f"{dm.get('snapshot')}; judge {provider} up"
                )
                return st
            if self.clock() >= deadline:
                raise Failed(
                    f"preflight: GET /api/status did not show a ready fast check with a snapshot "
                    f"and an up judge within {self.preflight_timeout:.0f}s (fast check {state or '?'}"
                    f", snapshot {dm.get('snapshot') or 'not seen'}, judge "
                    f"{judge.get('state') or '?'})"
                )
            self.sleep(PREFLIGHT_INTERVAL_S)

    def check_version(self, attempt: str) -> None:
        """The gateway reports VERSION's release (GET /health): the tape names that version."""
        code, out, _ = self.run(
            self.compose(attempt)
            + ["exec", "-T", "proxy", "wget", "-q", "-O", "-", "http://127.0.0.1:8080/health"],
            False,
            self.compose_env,
        )
        try:
            reported = str(json.loads(out).get("version")) if code == 0 else ""
        except (ValueError, AttributeError):
            reported = ""
        want = self.repo_version() or self.release_version
        if reported != want:
            raise Failed(
                f"preflight: the gateway reports version {reported or 'unknown'}, not VERSION's "
                f"{want}: the images were not built with --build-arg VERSION={want}"
            )

    def calibration(self, snapshot: str) -> Dict[str, Any]:
        path = calibration_file(snapshot, self.calibration_dir)
        if path.is_file():
            quality = calibration_quality(path)
            self.say(f"  calibration: {path.name} ({quality})")
            return {"snapshot": snapshot, "quality": quality}
        if self.allow_uncalibrated:
            self.say(
                f"  calibration: none for {snapshot}; ALLOW_UNCALIBRATED=1, so the tape says "
                "uncalibrated"
            )
            return {"snapshot": None, "quality": "none"}
        raise Failed(
            f"the snapshot {snapshot} has no {shown_path(path)} (D207): calibrate it first, or "
            "pass ALLOW_UNCALIBRATED=1"
        )

    def hosts(self, status: Mapping[str, Any], config: Mapping[str, Any]) -> Dict[str, str]:
        services = config.get("services") or {}
        engine_env = (services.get("engine") or {}).get("environment") or {}
        proxy_env = (services.get("proxy") or {}).get("environment") or {}
        provider = str((status.get("judge") or {}).get("provider") or "")
        hosts = {
            "decision": str((status.get("decision_model") or {}).get("endpoint_host") or ""),
            "judge": judge_host(
                provider, str(engine_env.get("TRUTHTABLE_OPENROUTER_BASE_URL") or "")
            ),
            "upstream": host_of(
                str(proxy_env.get("TRUTHTABLE_UPSTREAM_URL") or "https://api.openai.com")
            ),
        }
        vendors = vendor_hosts()
        bad = [f"{k} {v or 'unknown'}" for k, v in hosts.items() if v not in vendors]
        if bad:
            raise Failed(
                "non_vendor_host: " + ", ".join(bad) + f" (vendor hosts: {', '.join(vendors)})"
            )
        return hosts

    def container_since(self, attempt: str) -> str:
        """Now on the proxy container's clock (the clock that stamps the traces), to the second."""
        out = self.must(
            self.compose(attempt) + ["exec", "-T", "proxy", "date", "-u", "+%Y-%m-%dT%H:%M:%SZ"],
            "reading the proxy container's clock",
        ).strip()
        if not RFC3339.match(out):
            raise Failed(f"the proxy container's clock read {out!r}, not an RFC 3339 time")
        return out

    def traffic(self, attempt: str, folder: Path) -> Tuple[List[str], List[Dict[str, Any]]]:
        ids: List[str] = []
        drift: List[Dict[str, Any]] = []
        for n in range(1, self.passes + 1):
            remote_ids, remote_drift = CONTAINER_IDS.format(n=n), CONTAINER_DRIFT.format(n=n)
            self.say(
                f"  pass {n}/{self.passes}: trafficgen --record (drift is reported, not failing)"
            )
            argv = self.compose(attempt) + [
                "exec",
                "-T",
                "proxy",
                TRAFFICGEN,
                "--record",
                "--scenarios",
                SCENARIOS,
                "--exclude",
                EXCLUDE,
                "--upstream",
                "generated",
                "--model-prefix",
                MODEL_PREFIX,
                "--max-tokens",
                str(MAX_TOKENS),
                "--base-url",
                PROXY_INTERNAL_URL,
                "--trace-ids-out",
                remote_ids,
                "--drift-out",
                remote_drift,
            ]
            code, _, _ = self.run(argv, True, self.compose_env)
            if code == 2:
                raise Failed(
                    f"pass {n}: trafficgen refused to record (its 'trafficgen: --record refused:' "
                    "line above says why); nothing was sent"
                )
            if code != 0:
                raise Failed(f"pass {n}: trafficgen failed (exit {code}; see above)")
            local_ids, local_drift = folder / f"ids-{n}.txt", folder / f"drift-{n}.json"
            self.must(
                self.compose(attempt) + ["cp", f"proxy:{remote_ids}", str(local_ids)],
                f"copying pass {n}'s trace ids",
            )
            self.must(
                self.compose(attempt) + ["cp", f"proxy:{remote_drift}", str(local_drift)],
                f"copying pass {n}'s drift",
            )
            round_ids = [
                line.strip()
                for line in local_ids.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]
            if not round_ids:
                raise Failed(f"pass {n}: trafficgen answered no request")
            ids += round_ids
            try:
                report = json.loads(local_drift.read_text(encoding="utf-8"))
            except ValueError as exc:
                raise Failed(f"pass {n}: drift.json is not JSON") from exc
            drift.append({"pass": n, **(report if isinstance(report, dict) else {})})
        return ids, drift

    def export(
        self,
        attempt: str,
        since: str,
        staging: Path,
        hosts: Mapping[str, str],
        calibration: Mapping[str, Any],
        featured: Optional[str] = None,
    ) -> Dict[str, Any]:
        sha = self.must(
            ["git", "-C", str(ROOT), "rev-parse", "--short=12", "HEAD"],
            "git rev-parse",
            compose=False,
        ).strip()
        argv = self.compose(attempt) + [
            "exec",
            "-T",
            "proxy",
            EXPORT,
            "--out",
            CONTAINER_EXPORT,
            "--since",
            since,
            "--source",
            "trafficgen",
            "--git-sha",
            sha,
            "--decision-host",
            hosts["decision"],
            "--judge-host",
            hosts["judge"],
            "--generated-host",
            hosts["upstream"],
            "--calibration-quality",
            str(calibration["quality"]),
            "--force",
        ]
        if calibration.get("snapshot"):
            argv += ["--calibration-snapshot", str(calibration["snapshot"])]
        if featured:
            argv += ["--featured", featured]
        self.must(argv, "export-replay", stream=True)
        if staging.exists():
            shutil.rmtree(staging)
        staging.parent.mkdir(parents=True, exist_ok=True)
        self.must(
            self.compose(attempt) + ["cp", f"proxy:{CONTAINER_EXPORT}", str(staging)],
            "copying the export",
        )
        try:
            doc = json.loads((staging / "manifest.json").read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise Failed(f"the export has no readable manifest.json ({exc})") from exc
        if not isinstance(doc, dict):
            raise Failed("the export's manifest.json is not a JSON object")
        return doc

    def check(self, tape: Path, require_recorded: bool) -> None:
        argv = [self.python, str(ROOT / "scripts" / "check_recording.py"), str(tape)]
        if require_recorded:
            argv.append("--require-recorded")
        code, out, err = self.run(argv, False, None)
        if code != 0:
            shown = (out + err).strip().splitlines()[:20]
            flag = " --require-recorded" if require_recorded else ""
            raise Failed(f"check_recording.py{flag} refused the tape:\n    " + "\n    ".join(shown))

    def swap_in(self, staging: Path) -> None:
        """Copy the tape into dest; the old recording comes back if the swap fails."""
        tmp_new = self.dest.with_name(f".{self.dest.name}.new")
        old = self.dest.with_name(f".{self.dest.name}.old")
        for p in (tmp_new, old):
            if p.exists():
                shutil.rmtree(p)
        shutil.copytree(staging, tmp_new)
        if self.dest.exists():
            os.replace(self.dest, old)
        try:
            os.replace(tmp_new, self.dest)
        except OSError:
            if old.exists() and not self.dest.exists():
                os.replace(old, self.dest)
            raise
        shutil.rmtree(old, ignore_errors=True)

    def down(self, attempt: Optional[str]) -> None:
        code, _, err = self.run(
            self.compose(attempt) + ["down", "-v", "--remove-orphans"], False, self.compose_env
        )
        if code != 0:
            print(f"record_demo: docker compose down -v failed: {err.strip()}", file=self.err)

    def stack_running(self, attempt: str) -> bool:
        code, out, _ = self.run(
            self.compose(attempt) + ["ps", "--status", "running", "--services"],
            False,
            self.compose_env,
        )
        return code == 0 and "proxy" in out.split()

    def settled_usage(self) -> Optional[KeyState]:
        """The key's usage after the settle (D208); None when it cannot be read."""
        self.sleep(self.settle_s)
        try:
            return self.read_key()
        except KeyReadError as exc:
            print(
                f"record_demo: could not read the key's usage ({exc}); the ledger keeps the last "
                "reading",
                file=self.err,
            )
            return None

    # ---------- attempt.json ----------

    def load_attempt(self, attempt: str) -> Dict[str, Any]:
        path = self.attempt_dir(attempt) / "attempt.json"
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise Refused(f"no reviewed attempt {attempt} ({shown_path(path)}: {exc})") from exc
        if not isinstance(doc, dict):
            raise Refused(f"{shown_path(path)} is not a JSON object")
        return doc

    def save_attempt(self, attempt: str, doc: Mapping[str, Any]) -> None:
        path = self.attempt_dir(attempt) / "attempt.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(doc, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        os.replace(tmp, path)

    def new_attempt_id(self) -> str:
        attempt = self.now().astimezone(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        if self.attempt_dir(attempt).exists():
            raise Refused(f"attempt {attempt} already exists; wait a second and run again")
        return attempt

    # ---------- the result line ----------

    def result(
        self,
        step: str,
        attempt: str,
        code: int,
        cost: Optional[float],
        cap: float,
        key: Optional[KeyState],
        doc: Mapping[str, Any],
    ) -> None:
        hosts = doc.get("hosts") or {}
        counts = doc.get("counts") or {}
        line = {
            "step": step,
            "run_id": attempt,
            "exit": code,
            "cost_usd": round(cost, 6) if cost is not None else None,
            "cap_usd": round(cap, 2),
            "key_usage_usd": round(key.usage, 6) if key else None,
            "key_limit_remaining_usd": (
                round(key.limit_remaining, 6) if key and key.limit_remaining is not None else None
            ),
            "label": doc.get("label") or "",
            "snapshot": doc.get("snapshot"),
            "hosts": {
                "decision": hosts.get("decision") or "",
                "judge": hosts.get("judge") or "",
                "upstream": hosts.get("upstream"),
            },
            "counts": {
                "n": int(counts.get("n") or 0),
                "scored": int(counts.get("scored") or 0),
                "failed": int(counts.get("failed") or 0),
            },
        }
        self.say("TRUSTAGENT_P6_RESULT " + json.dumps(line, sort_keys=False))

    # ---------- an attempt ----------

    def attempt(self, plan: Plan, keys: Keys) -> int:
        if keys.problems:
            raise Refused("; ".join(keys.problems))
        version = self.repo_version()
        if version != self.release_version:
            raise Refused(
                f"VERSION is {version or 'missing'}, not {self.release_version}: bump it first "
                f"(make version-bump V={self.release_version}; D216, H-8), so the tape names the "
                "release it ships with"
            )
        ledger = self.ledger
        open_attempt = ledger.open_attempt()
        if open_attempt:
            raise Refused(
                f"attempt {open_attempt} is still open: review it, then make record-demo ACCEPT=1 "
                f"ATTEMPT={open_attempt}, or DISCARD=1"
            )
        if not real_calibrations(self.calibration_dir) and not self.allow_uncalibrated:
            raise Refused(
                "no calibration for a real snapshot in config/calibration (D207): calibrate first "
                "(make calib-pass, make calibrate WRITE=1), or pass ALLOW_UNCALIBRATED=1"
            )
        try:
            before = self.read_key()
        except KeyReadError as exc:
            raise Refused(f"the key's usage could not be read: {exc}") from exc
        caps = plan_caps(self.total_usd, ledger.spent(), before.limit_remaining)
        if caps.refused:
            raise Refused(caps.refused)

        attempt = self.new_attempt_id()
        folder = self.attempt_dir(attempt)
        folder.mkdir(parents=True)
        policy: Optional[Path] = None
        if self.allow_uncalibrated:
            policy = folder / "policies.yaml"
            policy.write_text(
                uncalibrated_policy(self.policy_file.read_text(encoding="utf-8")), encoding="utf-8"
            )
        video_port = self.port_finder()
        admin_key = secrets.token_hex(24)
        key_file = folder / "admin.key"
        key_file.touch(mode=0o600)
        key_file.write_text(admin_key + "\n", encoding="utf-8")
        (folder / "stack.yml").write_text(
            stack_override(caps, policy, video_port, admin_key), encoding="utf-8"
        )
        doc: Dict[str, Any] = {
            "attempt": attempt,
            "state": RUNNING,
            "created_at": utc_now(),
            "passes": self.passes,
            "caps": {
                "remaining_usd": caps.remaining,
                "decision_usd": caps.decision,
                "llm_usd": caps.llm,
            },
            "allow_uncalibrated": self.allow_uncalibrated,
            "video_port": video_port,
        }
        self.save_attempt(attempt, doc)
        try:
            config = self.check_stack_config(attempt, caps)
        except Refused:
            shutil.rmtree(folder, ignore_errors=True)
            raise

        ledger.append(
            {
                "at": utc_now(),
                "attempt": attempt,
                "event": "start",
                "usage_before_usd": before.usage,
                "limit_remaining_usd": before.limit_remaining,
                "remaining_usd": caps.remaining,
                "caps": {"decision_usd": caps.decision, "llm_usd": caps.llm},
            }
        )
        self.say(
            f"Attempt {attempt}: caps fast checks ${caps.decision:.2f}, deep audits "
            f"${caps.llm:.2f} (R ${caps.remaining:.2f}); key usage before ${before.usage:.4f}"
        )
        try:
            return self.record(attempt, doc, config, caps, before)
        except Failed as exc:
            print(f"record_demo: failed: {exc}", file=self.err)
            if not self.keep:
                self.down(attempt)
                print(
                    "record_demo: nothing was kept; the record stack was removed (down -v)",
                    file=self.err,
                )
            else:
                self.say(
                    f"  --keep: the record stack is still up; read its logs with docker compose -p "
                    f"{PROJECT} logs, then make record-demo DISCARD=1"
                )
            after = self.settled_usage() if not self.keep else None
            spent = round(after.usage - before.usage, 6) if after else None
            doc["state"] = FAILED if not self.keep else doc["state"]
            doc["error"] = str(exc)
            self.save_attempt(attempt, doc)
            if not self.keep:
                ledger.append(
                    {
                        "at": utc_now(),
                        "attempt": attempt,
                        "event": "end",
                        "outcome": FAILED,
                        "usage_after_usd": after.usage if after else None,
                        "spent_usd": spent,
                    }
                )
            self.result("record-demo", attempt, EXIT_FAILED, spent, caps.total, after, doc)
            return EXIT_FAILED

    def record(
        self,
        attempt: str,
        doc: Dict[str, Any],
        config: Dict[str, Any],
        caps: Caps,
        before: KeyState,
    ) -> int:
        folder = self.attempt_dir(attempt)
        self.say("Starting the record stack (a clean slate: its old volumes are removed first)")
        self.must(
            self.compose(attempt) + ["down", "-v", "--remove-orphans"], "docker compose down -v"
        )
        self.must(
            self.compose(attempt) + ["build"] + self.build_args() + ["proxy", "engine"],
            "docker compose build",
            stream=True,
        )
        up = ["up", "-d", "--wait", "--wait-timeout", str(self.wait_timeout)]
        self.must(
            self.compose(attempt) + up + ["proxy", "engine"], "docker compose up", stream=True
        )
        status = self.preflight(attempt)
        snapshot = str((status.get("decision_model") or {}).get("snapshot"))
        calibration = self.calibration(snapshot)
        hosts = self.hosts(status, config)
        doc.update(snapshot=snapshot, calibration=calibration, hosts=hosts)
        self.save_attempt(attempt, doc)

        since = self.since or self.container_since(attempt)
        self.say(f"  exporting traces created since {since}")
        work = folder / "work"
        if work.exists():
            shutil.rmtree(work)
        work.mkdir(parents=True)
        ids, drift = self.traffic(attempt, work)
        (folder / "drift.json").write_text(
            json.dumps({"attempt": attempt, "passes": drift}, indent=2) + "\n", encoding="utf-8"
        )
        tape = folder / "tape"
        manifest = self.export(attempt, since, tape, hosts, calibration)
        exported = [
            str(t.get("trace_id")) for t in manifest.get("traces") or [] if isinstance(t, dict)
        ]
        missing, extra = sorted(set(ids) - set(exported)), sorted(set(exported) - set(ids))
        if missing or extra:
            raise Failed(
                f"the export does not match the passes: {len(missing)} answered trace(s) missing, "
                f"{len(extra)} unexpected (first: {(missing or extra)[0]})"
            )
        self.check(tape, require_recorded=False)
        skipped = skipped_reasons(tape, manifest)
        featured = manifest.get("featured")
        # The gateway's own count of today's spend: the breakdown, an estimate (the vendor's
        # reported cost where given, else priced from tokens); the ledger's figure is the key's.
        budgets = self.status(attempt).get("budgets") or {}
        gateway = {
            "decision_usd": budgets.get("decision_spent_today_usd"),
            "llm_usd": budgets.get("llm_spent_today_usd"),
        }
        doc.update(
            since=since,
            label=manifest.get("label"),
            kind=manifest.get("kind"),
            featured=featured,
            skipped=skipped,
            counts=self.counts(manifest),
            gateway_spend=gateway,
        )
        after = self.settled_usage()
        spent = round(after.usage - before.usage, 6) if after else None
        self.ledger.append(
            {
                "at": utc_now(),
                "attempt": attempt,
                "event": "measure",
                "usage_usd": after.usage if after else None,
                "spent_usd": spent,
            }
        )
        doc["state"] = REVIEW
        doc["spent_usd"] = spent
        self.save_attempt(attempt, doc)
        shutil.rmtree(work, ignore_errors=True)

        self.say(
            f"Recorded {len(exported)} traces into {shown_path(tape)} (kind {manifest.get('kind')})"
        )
        self.say(f"  Label: {manifest.get('label')}")
        self.say(
            f"  Spent so far: {money(spent)} (the key's usage change after a {self.settle_s:.0f} s "
            "settle; the vendor's figure)"
        )
        self.say(
            f"  Breakdown: fast checks {money(gateway['decision_usd'])}, deep audits "
            f"{money(gateway['llm_usd'])} (the gateway's own count, an estimate)"
        )
        self.drift_summary(drift)
        for reason in skipped:
            self.say(f"  Skipped: {reason}")
        if skipped:
            self.say(
                "  This attempt cannot be accepted: a trace was cut short by a budget (D208). "
                "make record-demo DISCARD=1, then try again (cards never cross 00:00 UTC)."
            )
        code = EXIT_OK
        if isinstance(featured, dict):
            self.say(f"  Featured: {featured.get('trace_id')} (rule {featured.get('rule')})")
        else:
            code = EXIT_REVIEW
            self.say(
                "  Featured: none: no flag, fallback or block trace has a CONTRADICTED claim with a "
                "span (D209). Review needed: pick the best flagged trace below with ACCEPT=1 "
                f"ATTEMPT={attempt} FEATURED=<trace id> (rule best_flagged), or DISCARD=1 and try "
                "again within the cap; the launch copy is reworded to match."
            )
            for line in self.flagged_table(manifest):
                self.say("    " + line)
        self.say(
            f"  Review it (the stack stays up): {shown_path(tape)} and {shown_path(folder / 'drift.json')}; "
            f"then make record-demo ACCEPT=1 ATTEMPT={attempt} [VIDEO=1], or DISCARD=1"
        )
        self.result("record-demo", attempt, code, spent, caps.total, after, doc)
        return code

    @staticmethod
    def counts(manifest: Mapping[str, Any]) -> Dict[str, int]:
        traces = [t for t in manifest.get("traces") or [] if isinstance(t, dict)]
        scored = sum(
            1 for t in traces if (t.get("audit") or {}).get("status") in ("completed", "degraded")
        )
        failed = sum(
            1
            for t in traces
            if (t.get("audit") or {}).get("status") == "failed" or t.get("gate_status") == "error"
        )
        return {"n": len(traces), "scored": scored, "failed": failed}

    def drift_summary(self, drift: Sequence[Mapping[str, Any]]) -> None:
        for report in drift:
            counts = report.get("counts") or {}
            self.say(
                f"  Drift pass {report.get('pass')}: {counts.get('ok', 0)} ok, "
                f"{counts.get('drift', 0)} drift, {counts.get('unchecked', 0)} unchecked "
                f"(generated), {counts.get('failed', 0)} failed"
            )
            for e in report.get("scenarios") or []:
                if e.get("outcome") == "drift":
                    fields = ", ".join(str(m.get("field")) for m in e.get("mismatches") or [])
                    self.say(f"    {e.get('scenario')}: {fields}")

    @staticmethod
    def flagged_table(manifest: Mapping[str, Any]) -> List[str]:
        rows = [
            t
            for t in manifest.get("traces") or []
            if isinstance(t, dict) and t.get("action") in FLAGGED_ACTIONS
        ]
        if not rows:
            return ["(no flag, fallback or block trace: DISCARD=1 and try again)"]
        out = ["trace id                              outcome   route         scenario"]
        for t in rows[:20]:
            out.append(
                f"{t.get('trace_id')}  {str(t.get('action')):<8}  {str(t.get('route')):<12}  "
                f"{scenario_of(t) or '-'}"
            )
        if len(rows) > 20:
            out.append(f"... and {len(rows) - 20} more")
        return out

    # ---------- accept and discard ----------

    def accept(self, attempt: str, featured: Optional[str], video: bool) -> int:
        if not ATTEMPT_ID.match(attempt):
            raise Refused(f"ATTEMPT={attempt!r} is not an attempt id (like 20261021T093000Z)")
        if featured is not None and not TRACE_ID.match(featured):
            raise Refused(f"FEATURED={featured!r} is not a trace id")
        doc = self.load_attempt(attempt)
        if doc.get("state") != REVIEW:
            raise Refused(
                f"attempt {attempt} is {doc.get('state')}, not under review: ACCEPT takes an "
                "attempt that make record-demo CONFIRM=1 recorded and left for review"
            )
        if doc.get("skipped"):
            raise Refused(
                f"attempt {attempt} cannot be accepted: a trace was cut short by a budget (D208): "
                + "; ".join(doc["skipped"][:3])
            )
        tape = self.attempt_dir(attempt) / "tape"
        if featured:
            if not self.stack_running(attempt):
                raise Refused(
                    "FEATURED re-exports from the attempt's stack, which is not running; "
                    "DISCARD=1 and record another attempt"
                )
            self.say(f"Re-exporting with the owner's featured pick {featured} (rule best_flagged)")
            old_ids = self.tape_ids(tape)
            staging = self.attempt_dir(attempt) / "tape.new"
            manifest = self.export(
                attempt,
                str(doc.get("since")),
                staging,
                doc.get("hosts") or {},
                doc.get("calibration") or {},
                featured,
            )
            if self.tape_ids(staging) != old_ids:
                shutil.rmtree(staging, ignore_errors=True)
                raise Failed("the re-export does not hold the reviewed traces; DISCARD=1")
            shutil.rmtree(tape)
            os.replace(staging, tape)
            doc["featured"] = manifest.get("featured")
            self.save_attempt(attempt, doc)
        if not isinstance(doc.get("featured"), dict):
            raise Refused(
                f"attempt {attempt} has no featured trace (D209): pass FEATURED=<trace id> of a "
                "flag, fallback or block trace, or DISCARD=1"
            )
        self.check(tape, require_recorded=True)
        if video:
            self.say("Recording the video's stack beats (D210) before the stack goes")
            code, _, _ = self.run(
                [
                    self.python,
                    str(ROOT / "scripts" / "launch_video.py"),
                    "stack",
                    "--attempt",
                    attempt,
                ],
                True,
                self.compose_env,
            )
            if code != 0:
                raise Failed(
                    f"the video's stack beats failed (exit {code}); nothing was swapped in and the "
                    "stack is still up: run ACCEPT again, or DISCARD=1"
                )
        self.swap_in(tape)
        self.say(f"Accepted attempt {attempt} into {shown_path(self.dest)}")
        self.down(attempt)
        return self.close(attempt, doc, ACCEPTED, "record-demo-accept")

    def discard(self) -> int:
        attempt = self.ledger.open_attempt()
        self.say("Removing the record stack (down -v)")
        self.down(attempt)
        if attempt is None:
            self.say("No open attempt in the ledger; nothing else to do")
            return EXIT_OK
        try:
            doc = self.load_attempt(attempt)
        except Refused:
            doc = {"attempt": attempt}
        return self.close(attempt, doc, DISCARDED, "record-demo-discard")

    def close(self, attempt: str, doc: Dict[str, Any], outcome: str, step: str) -> int:
        """The ledger's final reading of an attempt, after its stack is gone."""
        book = self.ledger.books().get(attempt) or AttemptBook(attempt=attempt)
        after = self.settled_usage()
        spent: Optional[float] = None
        if after is not None and book.usage_before is not None:
            spent = round(after.usage - book.usage_before, 6)
        elif book.spent is not None:
            spent = book.spent
        self.ledger.append(
            {
                "at": utc_now(),
                "attempt": attempt,
                "event": "end",
                "outcome": outcome,
                "usage_after_usd": after.usage if after else None,
                "spent_usd": spent,
            }
        )
        doc["state"] = outcome
        doc["spent_usd"] = spent
        if self.attempt_dir(attempt).is_dir():
            self.save_attempt(attempt, doc)
        self.say(
            f"Attempt {attempt} {outcome}: spent {money(spent)}; every attempt together "
            f"{money(self.ledger.spent())} of ${self.total_usd:.2f}"
        )
        caps = doc.get("caps") or {}
        cap = float(caps.get("decision_usd") or 0) + float(caps.get("llm_usd") or 0)
        self.result(step, attempt, EXIT_OK, spent, cap, after, doc)
        return EXIT_OK

    @staticmethod
    def tape_ids(tape: Path) -> List[str]:
        doc = json.loads((tape / "manifest.json").read_text(encoding="utf-8"))
        return sorted(str(t.get("trace_id")) for t in doc.get("traces") or [])


def main(argv: Optional[Sequence[str]] = None, recorder: Optional[Recorder] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="record_demo.py",
        description="Record the public demo with the real fast check and judge (D123, D207-D210). "
        "A dry run unless CONFIRM=1 is set.",
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="print the plan only (the default without CONFIRM=1)"
    )
    parser.add_argument("--keep", action="store_true", help="leave the stack up when a step fails")
    parser.add_argument(
        "--since",
        help="export traces from this RFC 3339 time (default: the proxy's clock before pass 1)",
    )
    parser.add_argument(
        "--wait-timeout", type=int, default=600, metavar="SECONDS", help="how long up --wait waits"
    )
    parser.add_argument(
        "--passes", type=int, default=DEFAULT_PASSES, help="trafficgen passes (RECORD_PASSES)"
    )
    parser.add_argument(
        "--max-total-usd",
        type=float,
        default=DEFAULT_TOTAL_USD,
        help="every attempt together (RECORD_MAX_TOTAL_USD)",
    )
    parser.add_argument(
        "--allow-uncalibrated",
        action="store_true",
        help="record a snapshot without a calibration file (ALLOW_UNCALIBRATED=1)",
    )
    parser.add_argument(
        "--release-version", default=RELEASE_VERSION, help="the VERSION the tape must ship with"
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--accept", metavar="ATTEMPT", help="accept a reviewed attempt (ACCEPT=1)")
    mode.add_argument(
        "--discard", action="store_true", help="drop the open attempt and its stack (DISCARD=1)"
    )
    parser.add_argument(
        "--featured", metavar="TRACE_ID", help="the owner's featured pick (FEATURED)"
    )
    parser.add_argument(
        "--video",
        action="store_true",
        help="with --accept, record the video's stack beats (VIDEO=1)",
    )
    args = parser.parse_args(argv)
    r = recorder or Recorder()
    r.keep, r.wait_timeout = args.keep, args.wait_timeout
    r.allow_uncalibrated = r.allow_uncalibrated or args.allow_uncalibrated
    r.release_version = args.release_version
    if not 1 <= args.passes <= 5:
        print("record_demo: --passes must be 1 to 5", file=r.err)
        return EXIT_REFUSED
    if not 0 < args.max_total_usd <= 20:
        print("record_demo: --max-total-usd must be above 0 and at most 20", file=r.err)
        return EXIT_REFUSED
    r.passes, r.total_usd = args.passes, args.max_total_usd
    if args.since is not None and not RFC3339.match(args.since):
        print(
            f"record_demo: --since {args.since!r} is not an RFC 3339 time (2026-10-10T09:00:00Z)",
            file=r.err,
        )
        return EXIT_REFUSED
    if (args.featured or args.video) and not args.accept:
        print("record_demo: FEATURED and VIDEO go with ACCEPT=1 ATTEMPT=<id>", file=r.err)
        return EXIT_REFUSED
    r.since = args.since

    try:
        if args.accept:
            return r.accept(args.accept, args.featured, args.video)
        if args.discard:
            return r.discard()
        plan = load_plan(passes=r.passes)
        keys = r.keys()
        r.print_plan(plan, keys)
        if args.dry_run or r.environ.get("CONFIRM") != "1":
            r.say("Dry run: nothing started, nothing spent. To record: make record-demo CONFIRM=1")
            return EXIT_OK
        return r.attempt(plan, keys)
    except Refused as exc:
        print(f"record_demo: refused: {exc}", file=r.err)
        return EXIT_REFUSED
    except Failed as exc:
        print(f"record_demo: failed: {exc}", file=r.err)
        return EXIT_FAILED


if __name__ == "__main__":
    sys.exit(main())
