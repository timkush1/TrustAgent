#!/usr/bin/env python3
"""Record the public demo with the real fast check and the real judge (D123; standard library only).

    python3 scripts/record_demo.py --dry-run      # the plan, its estimate and which keys are set
    make record-demo                              # the same: a dry run unless CONFIRM=1
    make record-demo CONFIRM=1                    # record it (your OpenRouter key, about $1)

The static site replays a recording (SPEC §2.13). The committed one is simulated (mock checks and a
mock judge, labelled as such); this script makes the real one, kind ``recorded``, in
frontend-react/demo-data/recorded/, which the Pages build ships from a release tag on (D110).

Without CONFIRM=1 in the environment (or with --dry-run) it prints the plan and stops: nothing
starts and nothing is spent. With CONFIRM=1 it:

1. refuses without a decision-model key and a judge key (.env or the environment; one
   OPENROUTER_API_KEY covers both), or when .env picks the mock or turns either off;
2. checks the composed stack (docker-compose.yml + docker-compose.record.yml, project
   truthtable-record): the spend caps ($0.50 of fast checks, $3 of deep audits a day), test mode,
   sync traces, no judge fallback, no published database or dashboard port, no mock-upstream;
3. starts it from a clean slate (down -v first) and waits until it is healthy;
4. preflight: doctor --only decision_model,judge must PASS both, with no mock, `off` or fallback
   (it waits while the fast check is still being checked);
5. runs trafficgen --record --scenarios demo --exclude knowledge --upstream generated twice inside
   the proxy container (scripted answers through test_response, the generated pair through the
   real upstream); trafficgen refuses a gateway that is not fit to record, and reports drift from
   the scripted expectations without failing;
6. exports the traces sent since the first round (in the container: /export-replay --since ...
   --source trafficgen), copies the export out and checks that it holds exactly the traces the
   rounds answered;
7. checks it with check_recording.py --require-recorded;
8. swaps it into frontend-react/demo-data/recorded/ (the previous recording, if any, is replaced
   only once the new one has passed);
9. prints the label and today's spend, and removes the stack with its volumes (down -v), also when
   a step fails (--keep leaves a failed stack up to read its logs).

No key is ever printed: the plan says whether each one is set. Exit status: 0 recorded, or a dry
run; 1 a step failed after the stack started (nothing is swapped in); 2 refused before anything
started (a missing key, no docker compose, a stack without its caps, a bad --since).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, TextIO, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parent))
from envfile import resolve  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
PROJECT = "truthtable-record"
COMPOSE_FILES = ("docker-compose.yml", "docker-compose.record.yml")
PACK = ROOT / "contracts" / "demo" / "scenarios.v1.json"
DEST = ROOT / "frontend-react" / "demo-data" / "recorded"

ROUNDS = 2
SCENARIOS = "demo"
EXCLUDE = "knowledge"
# What a recording costs at list prices: about two rounds of the demo scenarios' fast checks and
# deep audits (an estimate; the caps below bound it whatever happens).
ESTIMATE_USD = 1.0
# docker-compose.record.yml must give the proxy and the engine these values (D123).
CAPS = {
    "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY": "0.50",
    "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY": "3",
}
PROXY_ENV = {
    **CAPS,
    "TRUTHTABLE_TEST_MODE": "true",
    "TRUTHTABLE_TRACE_FLUSH": "sync",
    "TRUTHTABLE_DEMO_MODE": "false",
    "TRUTHTABLE_STRICT_CONFIG": "true",
    "TRUTHTABLE_PUBLIC_READ": "false",
}
ENGINE_ENV = {"LLM_FALLBACK": "none", "TRUTHTABLE_DEMO_MODE": "false"}

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
CONTAINER_EXPORT = "/tmp/record-demo-export"  # nosec B108 - a path inside the container
PROXY_INTERNAL_URL = "http://127.0.0.1:8080"

PREFLIGHT_TIMEOUT_S = 120.0
PREFLIGHT_INTERVAL_S = 5.0
RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$")

# run(argv, stream): (returncode, stdout, stderr). With stream=True the output goes straight to
# the terminal (long steps), and the returned strings are empty.
Runner = Callable[[Sequence[str], bool], Tuple[int, str, str]]


def run_command(argv: Sequence[str], stream: bool = False) -> Tuple[int, str, str]:
    try:
        if stream:
            return subprocess.run(list(argv), check=False).returncode, "", ""
        done = subprocess.run(list(argv), capture_output=True, text=True, check=False)
    except FileNotFoundError:
        return 127, "", f"{argv[0]}: command not found"
    except OSError as exc:
        return 126, "", str(exc)
    return done.returncode, done.stdout, done.stderr


class Refused(Exception):
    """Refused before the stack started: exit 2."""


class Failed(Exception):
    """A step failed after the stack started: exit 1."""


@dataclass
class Plan:
    scenarios: List[str]
    generated: List[str]
    rounds: int = ROUNDS

    @property
    def requests(self) -> int:
        return self.rounds * len(self.scenarios)


def load_plan(pack: Path = PACK) -> Plan:
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
    )


@dataclass
class Keys:
    """Which keys are set (never their values) and what .env picks."""

    decision: List[str]
    judge: List[str]
    judge_provider: str
    jev_backend: str
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
    keys = Keys(decision=decision, judge=judge, judge_provider=provider, jev_backend=backend)
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
    return keys


def verify_stack(config: Mapping[str, Any]) -> List[str]:
    """What docker compose config must show for the record stack (D123)."""
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
    for service in ("postgres", "dashboard"):
        if (services.get(service) or {}).get("ports"):
            problems.append(f"{service} publishes a port; the record stack publishes none")
    for port in (services.get("proxy") or {}).get("ports") or []:
        if not isinstance(port, dict) or port.get("host_ip") != "127.0.0.1":
            problems.append("the proxy must publish on 127.0.0.1 only")
        elif port.get("published") not in (None, "", "0"):
            problems.append(
                "the proxy publishes a fixed port; the record stack uses an ephemeral one"
            )
    return problems


def shown_path(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(ROOT))
    except ValueError:
        return str(path)


def money(value: Any) -> str:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        return "unknown"
    return f"${value:.4f}"


@dataclass
class Recorder:
    run: Runner = run_command
    environ: Mapping[str, str] = field(default_factory=lambda: dict(os.environ))
    env_file: Optional[Path] = None
    dest: Path = DEST
    out: TextIO = sys.stdout
    err: TextIO = sys.stderr
    sleep: Callable[[float], None] = time.sleep
    clock: Callable[[], float] = time.monotonic
    python: str = sys.executable or "python3"
    build: bool = False
    keep: bool = False
    wait_timeout: int = 600
    since: Optional[str] = None
    preflight_timeout: float = PREFLIGHT_TIMEOUT_S

    def say(self, message: str) -> None:
        print(message, file=self.out, flush=True)

    @property
    def compose(self) -> List[str]:
        argv = ["docker", "compose", "-p", PROJECT]
        for name in COMPOSE_FILES:
            argv += ["-f", str(ROOT / name)]
        return argv

    # ---------- the plan ----------

    def print_plan(self, plan: Plan, keys: Keys) -> None:
        def state(names: Sequence[str], wanted: Sequence[str]) -> str:
            return ", ".join(f"{n} set" for n in names) or ("missing (" + " or ".join(wanted) + ")")

        judge_wanted = judge_candidates(keys.judge_provider)
        lines = [
            "Record the public demo (make record-demo, D123)",
            f"  Stack     project {PROJECT}: " + " + ".join(COMPOSE_FILES) + "; removed (down -v) "
            "at the end",
            "  Backends  from .env: the real fast check and the real judge, no fallback; "
            "the mock, off and mock-upstream are refused",
            f"  Keys      fast check: {state(keys.decision, DECISION_KEYS)}; judge "
            f"(LLM_PROVIDER={keys.judge_provider}): {state(keys.judge, judge_wanted)} "
            "(values never shown)",
            f"  Traffic   {plan.rounds} rounds of trafficgen --record --scenarios {SCENARIOS} "
            f"--exclude {EXCLUDE} --upstream generated: {len(plan.scenarios)} scenarios, "
            f"{plan.requests} requests; {len(plan.generated)} answers per round come from the "
            "real upstream (" + ", ".join(plan.generated) + "), the rest are scripted",
            f"  Caps      fast checks ${CAPS['TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY']}/day, deep "
            f"audits ${CAPS['TRUTHTABLE_SPEND_CAP_LLM_USD_DAY']}/day (hard)",
            f"  Cost      about ${ESTIMATE_USD:.2f} (estimate, at list prices; the caps bound it)",
            f"  Output    {shown_path(self.dest)} (kind recorded, checked with "
            "check_recording.py --require-recorded)",
        ]
        for line in lines:
            self.say(line)
        for problem in keys.problems:
            self.say(f"  Blocked   {problem} (a CONFIRM=1 run refuses)")

    # ---------- steps ----------

    def must(
        self, argv: Sequence[str], what: str, stream: bool = False, refuse: bool = False
    ) -> str:
        code, out, err = self.run(argv, stream)
        if code != 0:
            tail = (err or out).strip().splitlines()[-5:]
            message = f"{what} failed (exit {code})" + (": " + " / ".join(tail) if tail else "")
            raise Refused(message) if refuse else Failed(message)
        return out

    def check_stack_config(self) -> None:
        if self.run(["docker", "compose", "version"], False)[0] != 0:
            raise Refused("docker compose is not available (Docker Desktop or the compose plugin)")
        raw = self.must(
            self.compose + ["config", "--format", "json"], "docker compose config", refuse=True
        )
        try:
            config = json.loads(raw)
        except ValueError as exc:
            raise Refused(f"docker compose config did not print JSON ({exc})") from exc
        problems = verify_stack(config)
        if problems:
            raise Refused("the record stack is not configured as D123 says: " + "; ".join(problems))

    def proxy_url(self) -> str:
        out = self.must(self.compose + ["port", "proxy", "8080"], "docker compose port proxy")
        address = out.strip().splitlines()[0] if out.strip() else ""
        m = re.match(r"^(?:127\.0\.0\.1|localhost):(\d+)$", address)
        if not m:
            raise Failed(
                f"the proxy is not published on 127.0.0.1 (docker compose port: {address!r})"
            )
        return f"http://127.0.0.1:{m.group(1)}"

    def doctor(self, url: str, only: str) -> Dict[str, Dict[str, Any]]:
        argv = [
            self.python,
            str(ROOT / "scripts" / "doctor.py"),
            "--target",
            "docker",
            "--project",
            PROJECT,
            "--proxy-url",
            url,
            "--only",
            only,
            "--no-probe",
            "--json",
        ]
        code, out, err = self.run(argv, False)
        try:
            report = json.loads(out)
        except ValueError as exc:
            raise Failed(f"doctor did not print JSON (exit {code}): {err.strip()[-300:]}") from exc
        return {str(c.get("id")): c for c in report.get("checks", []) if isinstance(c, dict)}

    def preflight(self, url: str) -> None:
        """doctor --only decision_model,judge: both PASS, with no mock, off or fallback."""
        deadline = self.clock() + self.preflight_timeout
        while True:
            checks = self.doctor(url, "decision_model,judge")
            dm = checks.get("decision_model.state") or {}
            judge = checks.get("judge.credential_source") or {}
            dm_data, judge_data = dm.get("data") or {}, judge.get("data") or {}
            backend = str(dm_data.get("backend") or "")
            provider = str(judge_data.get("provider") or "")
            if backend in ("mock", "off") or str(dm_data.get("state")) in ("mock", "off"):
                raise Failed(
                    f"preflight: the fast check is {backend or dm_data.get('state')}; a recording "
                    "needs the real one (TRUTHTABLE_JEV_BACKEND=auto and OPENROUTER_API_KEY)"
                )
            if provider in ("mock", "none"):
                raise Failed(
                    f"preflight: the judge is {provider}; a recording needs the real judge "
                    "(LLM_PROVIDER=auto and a judge key)"
                )
            if judge_data.get("fallback_active") is True:
                raise Failed(
                    "preflight: the judge is running its fallback; a fallback verdict cannot be "
                    "labelled recorded"
                )
            if dm.get("status") == "PASS" and judge.get("status") == "PASS":
                self.say(f"  preflight: {dm.get('summary')}; {judge.get('summary')}")
                return
            lines = [
                f"{c.get('id')} {c.get('status')}: {c.get('summary')}"
                + (f" (fix: {c.get('fix')})" if c.get("fix") else "")
                for c in (dm, judge)
                if c
            ]
            # A rejected key or an empty account does not recover by waiting.
            if "FAIL" in (dm.get("status"), judge.get("status")):
                raise Failed("preflight: " + "; ".join(lines))
            if self.clock() >= deadline:
                raise Failed(
                    "preflight: doctor --only decision_model,judge did not pass within "
                    f"{self.preflight_timeout:.0f}s: " + "; ".join(lines or ["no result"])
                )
            self.sleep(PREFLIGHT_INTERVAL_S)

    def container_since(self) -> str:
        """Now on the proxy container's clock (the clock that stamps the traces), to the second."""
        out = self.must(
            self.compose + ["exec", "-T", "proxy", "date", "-u", "+%Y-%m-%dT%H:%M:%SZ"],
            "reading the proxy container's clock",
        ).strip()
        if not RFC3339.match(out):
            raise Failed(f"the proxy container's clock read {out!r}, not an RFC 3339 time")
        return out

    def traffic(self, work: Path) -> List[str]:
        ids: List[str] = []
        for n in range(1, ROUNDS + 1):
            remote = CONTAINER_IDS.format(n=n)
            self.say(
                f"  round {n}/{ROUNDS}: trafficgen --record (drift lines are reported, not failures)"
            )
            argv = self.compose + [
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
                "--base-url",
                PROXY_INTERNAL_URL,
                "--trace-ids-out",
                remote,
            ]
            code, _, _ = self.run(argv, True)
            if code == 2:
                raise Failed(
                    f"round {n}: trafficgen refused to record (its 'trafficgen: --record refused:' "
                    "line above says why); nothing was sent"
                )
            if code != 0:
                raise Failed(f"round {n}: trafficgen failed (exit {code}; see above)")
            local = work / f"ids-{n}.txt"
            self.must(
                self.compose + ["cp", f"proxy:{remote}", str(local)],
                f"copying round {n}'s trace ids",
            )
            round_ids = [
                line.strip()
                for line in local.read_text(encoding="utf-8").splitlines()
                if line.strip()
            ]
            if not round_ids:
                raise Failed(f"round {n}: trafficgen answered no request")
            ids += round_ids
        return ids

    def export(self, since: str, staging: Path) -> Dict[str, Any]:
        sha = self.must(
            ["git", "-C", str(ROOT), "rev-parse", "--short=12", "HEAD"], "git rev-parse"
        ).strip()
        self.must(
            self.compose
            + [
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
                "--force",
            ],
            "export-replay",
            stream=True,
        )
        if staging.exists():
            shutil.rmtree(staging)
        self.must(
            self.compose + ["cp", f"proxy:{CONTAINER_EXPORT}", str(staging)], "copying the export"
        )
        manifest = staging / "manifest.json"
        try:
            doc = json.loads(manifest.read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise Failed(f"the export has no readable manifest.json ({exc})") from exc
        if not isinstance(doc, dict):
            raise Failed("the export's manifest.json is not a JSON object")
        return doc

    def check(self, staging: Path) -> None:
        argv = [
            self.python,
            str(ROOT / "scripts" / "check_recording.py"),
            str(staging),
            "--require-recorded",
        ]
        code, out, err = self.run(argv, False)
        if code != 0:
            shown = (out + err).strip().splitlines()[:20]
            raise Failed(
                "check_recording.py --require-recorded refused the export:\n    "
                + "\n    ".join(shown)
            )

    def swap_in(self, staging: Path) -> None:
        """Replace dest with staging; the old recording comes back if the second rename fails."""
        old = self.dest.with_name(f".{self.dest.name}.old")
        if old.exists():
            shutil.rmtree(old)
        if self.dest.exists():
            os.replace(self.dest, old)
        try:
            os.replace(staging, self.dest)
        except OSError:
            if old.exists() and not self.dest.exists():
                os.replace(old, self.dest)
            raise
        shutil.rmtree(old, ignore_errors=True)

    def spend(self, url: str) -> str:
        budgets = (self.doctor(url, "budgets").get("budgets.today") or {}).get("data") or {}
        return (
            f"fast checks {money(budgets.get('decision_spent_today_usd'))}, deep audits "
            f"{money(budgets.get('llm_spent_today_usd'))} (estimate: the vendor's reported cost "
            "where given, else priced from tokens)"
        )

    # ---------- the run ----------

    def record(self, plan: Plan) -> int:
        self.check_stack_config()
        staging = self.dest.with_name(f".{self.dest.name}.new")
        work = self.dest.with_name(f".{self.dest.name}.work")
        started = False
        ok = False
        try:
            self.say("Starting the record stack (a clean slate: its old volumes are removed first)")
            self.must(self.compose + ["down", "-v", "--remove-orphans"], "docker compose down -v")
            started = True
            up = ["up", "-d"] + (["--build"] if self.build else [])
            up += ["--wait", "--wait-timeout", str(self.wait_timeout), "proxy", "engine"]
            self.must(self.compose + up, "docker compose up", stream=True)
            url = self.proxy_url()
            self.preflight(url)
            since = self.since or self.container_since()
            self.say(f"  exporting traces created since {since}")
            if work.exists():
                shutil.rmtree(work)
            work.mkdir(parents=True)
            ids = self.traffic(work)
            manifest = self.export(since, staging)
            exported = [
                str(t.get("trace_id")) for t in manifest.get("traces") or [] if isinstance(t, dict)
            ]
            missing, extra = sorted(set(ids) - set(exported)), sorted(set(exported) - set(ids))
            if missing or extra:
                raise Failed(
                    f"the export does not match the rounds: {len(missing)} answered trace(s) missing, "
                    f"{len(extra)} unexpected (first: {(missing or extra)[0]})"
                )
            self.check(staging)
            spent = self.spend(url)
            self.swap_in(staging)
            ok = True
            self.say(f"Recorded {len(exported)} traces into {self.dest}")
            self.say(f"  Label: {manifest.get('label')}")
            self.say(f"  Spend today: {spent}")
            self.say(
                "  Next: review it (cd frontend-react && VITE_DEMO_DATASET=recorded npm run "
                "build:static && npm run preview:static), then commit demo-data/recorded/"
            )
            return 0
        finally:
            shutil.rmtree(work, ignore_errors=True)
            if not ok:
                shutil.rmtree(staging, ignore_errors=True)
            if started and (ok or not self.keep):
                code, _, err = self.run(self.compose + ["down", "-v", "--remove-orphans"], False)
                if code != 0:
                    print(
                        f"record_demo: docker compose down -v failed: {err.strip()}", file=self.err
                    )
            elif started:
                self.say(
                    "  --keep: the record stack is still up; read its logs with docker compose -p "
                    f"{PROJECT} logs, then remove it: docker compose -p {PROJECT} "
                    + " ".join(f"-f {n}" for n in COMPOSE_FILES)
                    + " down -v"
                )


def main(argv: Optional[Sequence[str]] = None, recorder: Optional[Recorder] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="record_demo.py",
        description="Record the public demo with the real fast check and judge (D123). "
        "A dry run unless CONFIRM=1 is set.",
    )
    parser.add_argument(
        "--dry-run", action="store_true", help="print the plan only (the default without CONFIRM=1)"
    )
    parser.add_argument(
        "--build", action="store_true", help="build the images from this checkout first"
    )
    parser.add_argument("--keep", action="store_true", help="leave the stack up when a step fails")
    parser.add_argument(
        "--since",
        help="export traces from this RFC 3339 time (default: the proxy's clock before round 1)",
    )
    parser.add_argument(
        "--wait-timeout", type=int, default=600, metavar="SECONDS", help="how long up --wait waits"
    )
    args = parser.parse_args(argv)
    r = recorder or Recorder()
    r.build, r.keep, r.wait_timeout = args.build, args.keep, args.wait_timeout
    if args.since is not None and not RFC3339.match(args.since):
        print(
            f"record_demo: --since {args.since!r} is not an RFC 3339 time (2026-10-10T09:00:00Z)",
            file=r.err,
        )
        return 2
    r.since = args.since

    plan = load_plan()
    keys = read_keys(r.environ, r.env_file)
    r.print_plan(plan, keys)
    if args.dry_run or r.environ.get("CONFIRM") != "1":
        r.say("Dry run: nothing started, nothing spent. To record: make record-demo CONFIRM=1")
        return 0
    try:
        if keys.problems:
            raise Refused("; ".join(keys.problems))
        return r.record(plan)
    except Refused as exc:
        print(f"record_demo: refused: {exc}", file=r.err)
        return 2
    except Failed as exc:
        print(f"record_demo: failed: {exc}", file=r.err)
        removed = "" if r.keep else "; the record stack was removed (down -v)"
        print(f"record_demo: nothing was swapped in{removed}", file=r.err)
        return 1


if __name__ == "__main__":
    sys.exit(main())
