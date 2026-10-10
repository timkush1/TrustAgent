#!/usr/bin/env python3
"""Publish a gateway load run (D118) and the GATEWAY.md block that shows it.

`make load-test-gate LOAD_PUBLISH=1` runs this after its two passes:

    scripts/load/report.py [--report-only] [--out-dir docs/benchmarks/load] [--k6-image IMAGE]
                           [--compose "docker compose -p ... -f ..."] PASS_LOG...

It merges the `TRUSTAGENT_LOAD_RESULT {json}` lines k6 printed (scripts/load/gateway-overhead.js,
one k6 run per scenario), collects the environment (the CPU, memory, OS and kernel, the Docker
server, the proxy's limits and trace flush from the compose config, the pinned k6 image, the rate
and duration), and writes docs/benchmarks/load/<run_id>.json (contracts/schema/load-run.v1). It
refuses, writing nothing, when:

  - the tree has uncommitted tracked changes (git status --porcelain --untracked-files=no);
  - a scenario measured fewer than 500 requests;
  - part of the environment cannot be read;
  - a scenario missed its target (unless --report-only, which marks every scenario report-only);
  - a scenario outside observe has answers without X-TrustAgent-Overhead-Ms;
  - a result line covers several scenarios (its fast-check counters cannot be split) or a scenario
    appears twice.

    scripts/load/report.py --gateway-md [--check] [--runs-dir docs/benchmarks/load] [--doc docs/GATEWAY.md]

writes (or, with --check, compares) the block between `<!-- load:start -->` and `<!-- load:end -->` in
docs/GATEWAY.md from the newest clean run. Every vendor in a load run is simulated (mock-upstream
answers at once): a figure is the gateway's own time on the stated machine, never a vendor's.

Standard library only.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import platform
import re
import shlex
import subprocess  # nosec B404 - fixed argv lists (git, docker), never a shell
import sys
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional, Sequence, Tuple

ROOT = Path(__file__).resolve().parents[2]
RESULT_PREFIX = "TRUSTAGENT_LOAD_RESULT "
SCHEMA = "trustagent.load-run/v1"
VENDORS = "simulated (mock-upstream)"
MIN_N = 500
DEFAULT_OUT = ROOT / "docs" / "benchmarks" / "load"
DEFAULT_DOC = ROOT / "docs" / "GATEWAY.md"
BLOCK_START = "<!-- load:start -->"
BLOCK_END = "<!-- load:end -->"
# The trace flush the proxy runs with when the compose config leaves TRUTHTABLE_TRACE_FLUSH unset
# (contracts/config/keys.v1.json default).
DEFAULT_TRACE_FLUSH = "async"

# What each k6 scenario measures (scripts/load/gateway-overhead.js SCENARIOS; test_load.py keeps the
# two in step).
SCENARIO_WHAT = {
    "a": "gate disabled (nothing to ask)",
    "b": "observe: Tier 0 + async mock Jev",
    "c": "flag, mock Jev 0 ms",
    "d": "flag, mock Jev 150 ms",
    "e": "block, buffered streams (report only)",
    "f": "Jev hangs 15%, hedge 400 ms",
    "g": "block, JSON answers (not streamed)",
}
SOURCE_WORDS = {
    "header": "overhead header",
    "metrics": "gateway histogram",
    "total": "total time (upper bound)",
}

Runner = Callable[[Sequence[str]], str]


class Refusal(Exception):
    """A reason the run cannot be published."""


def run_command(argv: Sequence[str]) -> str:
    """Runs argv (no shell) and returns its stdout; a failure raises Refusal."""
    try:
        out = subprocess.run(  # nosec B603 - argv is a fixed list built here
            list(argv), cwd=ROOT, check=True, capture_output=True, text=True, timeout=60
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise Refusal(f"`{' '.join(argv[:3])} ...` failed: {exc}") from exc
    return out.stdout


# --------------------------------------------------------------------------------- result lines


def parse_result_lines(text: str) -> List[Dict[str, Any]]:
    """The result objects of every TRUSTAGENT_LOAD_RESULT line in a k6 log, in order."""
    out = []
    for raw in text.splitlines():
        line = raw.strip()
        # k6 may prefix its own output (time=... level=...); the line itself starts at the prefix.
        i = line.find(RESULT_PREFIX)
        if i < 0:
            continue
        try:
            obj = json.loads(line[i + len(RESULT_PREFIX) :])
        except json.JSONDecodeError as exc:
            raise Refusal(f"a result line is not JSON: {exc}") from exc
        if not isinstance(obj, dict) or not isinstance(obj.get("scenarios"), list):
            raise Refusal("a result line has no scenarios")
        out.append(obj)
    return out


def merge_results(
    results: Sequence[Dict[str, Any]], report_only: bool
) -> Tuple[List[Dict[str, Any]], Dict[str, Any]]:
    """The run's scenarios (run-file shape) and its rate, duration and error totals."""
    if not results:
        raise Refusal("no TRUSTAGENT_LOAD_RESULT line in the logs (did k6 run?)")
    rates = {r.get("rate") for r in results}
    durations = {r.get("duration") for r in results}
    if len(rates) != 1 or len(durations) != 1:
        raise Refusal(
            f"the k6 runs used different rates {sorted(map(str, rates))} or durations {sorted(map(str, durations))}"
        )
    scenarios: List[Dict[str, Any]] = []
    seen = set()
    requests = errors_5xx = 0.0
    non_200 = 0
    for r in results:
        if len(r["scenarios"]) != 1:
            ids = ", ".join(str(s.get("id")) for s in r["scenarios"])
            raise Refusal(
                f"a k6 run measured several scenarios ({ids}), so its fast-check counters cannot be split: "
                "run one scenario per k6 run (make load-test-gate does)"
            )
        if r.get("jev") is None:
            raise Refusal(
                "a k6 run could not read the proxy's /metrics or the mock's totals (jev counters missing)"
            )
        s = r["scenarios"][0]
        sid = s.get("id")
        if sid in seen:
            raise Refusal(f"scenario {sid} appears in two result lines")
        seen.add(sid)
        for k in ("n", "p50_ms", "p95_ms", "p99_ms", "max_ms"):
            if not isinstance(s.get(k), (int, float)) or isinstance(s.get(k), bool):
                raise Refusal(f"scenario {sid}: {k} is missing")
        if s["n"] < MIN_N:
            raise Refusal(f"scenario {sid} measured {s['n']} requests, fewer than {MIN_N}")
        if s.get("mode") != "observe" and s.get("without_header", 0) > 0:
            raise Refusal(
                f"scenario {sid} ({s.get('mode')}) had {s['without_header']} answer(s) without X-TrustAgent-Overhead-Ms: "
                "outside observe every answer carries it"
            )
        if s.get("source") not in SOURCE_WORDS:
            raise Refusal(f"scenario {sid}: unknown source {s.get('source')!r}")
        missed = not s.get("ok", False)
        if missed and not report_only:
            raise Refusal(
                f"scenario {sid} missed its target {s.get('target')} (publish with --report-only to keep it)"
            )
        jev = r["jev"]
        scenarios.append(
            {
                "id": sid,
                "route": s["route"],
                "mode": s["mode"],
                "streaming": bool(s["streaming"]),
                "n": int(s["n"]),
                "p50_ms": float(s["p50_ms"]),
                "p95_ms": float(s["p95_ms"]),
                "p99_ms": float(s["p99_ms"]),
                "max_ms": float(s["max_ms"]),
                "source": s["source"],
                "target": s.get("target"),
                "ok": bool(s.get("ok")),
                "report_only": bool(s.get("report_only")) or report_only,
                "jev": {
                    "attempts": int(jev["attempts"]),
                    "hedged": int(jev["hedged"]),
                    "mock_requests": int(jev["mock_requests"]),
                },
            }
        )
        req = float(s.get("requests") or 0)
        requests += req
        errors_5xx += req * float(s.get("rate_5xx") or 0)
        non_200 += int(s.get("non_200") or 0)
    scenarios.sort(key=lambda x: str(x["id"]))
    run = {
        "rate": int(results[0]["rate"]),
        "duration": str(results[0]["duration"]),
        "errors": {
            "rate_5xx": round(errors_5xx / requests, 6) if requests else 0.0,
            "non_200": non_200,
        },
    }
    return scenarios, run


# --------------------------------------------------------------------------------- environment


def read_text(path: str) -> Optional[str]:
    try:
        return Path(path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return None


def cpu_model(cpuinfo: Optional[str]) -> Optional[str]:
    for line in (cpuinfo or "").splitlines():
        key, _, value = line.partition(":")
        if key.strip() in ("model name", "Hardware", "Processor") and value.strip():
            return value.strip()
    return None


def mem_gb(meminfo: Optional[str]) -> Optional[float]:
    m = re.search(r"^MemTotal:\s+(\d+)\s+kB", meminfo or "", re.MULTILINE)
    return round(int(m.group(1)) / (1024 * 1024), 1) if m else None


def os_name(os_release: Optional[str], kernel: str) -> Optional[str]:
    m = re.search(r'^PRETTY_NAME="?([^"\n]+)"?', os_release or "", re.MULTILINE)
    if not m:
        return None
    name = m.group(1).strip()
    if "microsoft" in kernel.lower() and "WSL" not in name:
        name += " (WSL2)"
    return name


def human_bytes(value: Any) -> Optional[str]:
    """A compose memory limit as docker writes one (536870912 -> 512m); None when unset."""
    if value in (None, "", 0, "0"):
        return None
    text = str(value).strip()
    if not text.isdigit():
        return text.lower()
    n = int(text)
    for unit, size in (("g", 1 << 30), ("m", 1 << 20), ("k", 1 << 10)):
        if n % size == 0:
            return f"{n // size}{unit}"
    return text


def proxy_settings(config: Dict[str, Any]) -> Tuple[Dict[str, Any], str]:
    """The proxy's limits {cpus, mem} and its trace flush, from `docker compose config --format json`."""
    proxy = (config.get("services") or {}).get("proxy")
    if not isinstance(proxy, dict):
        raise Refusal("the compose config has no proxy service")
    limits = ((proxy.get("deploy") or {}).get("resources") or {}).get("limits") or {}
    cpus = limits.get("cpus")
    out = {
        "cpus": float(cpus) if cpus not in (None, "", 0, "0") else None,
        "mem": human_bytes(limits.get("memory")),
    }
    env = proxy.get("environment") or {}
    if isinstance(env, list):  # ["KEY=value", ...]
        env = dict(e.split("=", 1) for e in env if "=" in e)
    flush = env.get("TRUTHTABLE_TRACE_FLUSH") or DEFAULT_TRACE_FLUSH
    if flush not in ("async", "sync"):
        raise Refusal(f"the proxy's TRUTHTABLE_TRACE_FLUSH is {flush!r}")
    return out, flush


def collect_environment(
    run: Runner,
    compose: Optional[str],
    k6_image: Optional[str],
    rate: int,
    duration: str,
    files: Callable[[str], Optional[str]] = read_text,
) -> Dict[str, Any]:
    """The run's environment block; a part that cannot be read is a refusal."""
    kernel = platform.release()
    env: Dict[str, Any] = {
        "cpu_model": cpu_model(files("/proc/cpuinfo")) or platform.processor() or None,
        "cores": os.cpu_count(),
        "mem_gb": mem_gb(files("/proc/meminfo")),
        "os": os_name(files("/etc/os-release"), kernel),
        "kernel": kernel or None,
        "docker": run(["docker", "version", "--format", "{{.Server.Version}}"]).strip() or None,
    }
    if not compose:
        raise Refusal(
            "the environment needs the load stack's compose command (--compose) for the proxy limits"
        )
    try:
        config = json.loads(run(shlex.split(compose) + ["config", "--format", "json"]))
    except json.JSONDecodeError as exc:
        raise Refusal(f"the compose config is not JSON: {exc}") from exc
    env["proxy_limits"], env["trace_flush"] = proxy_settings(config)
    env["k6_image"] = k6_image or None
    env["rate"], env["duration"] = rate, duration
    missing = [k for k, v in env.items() if v is None]
    if missing:
        raise Refusal(f"the environment is incomplete: {', '.join(missing)} could not be read")
    return env


# --------------------------------------------------------------------------------- the run file


def git_state(run: Runner) -> Tuple[str, bool]:
    sha = run(["git", "rev-parse", "--short", "HEAD"]).strip()
    dirty = run(["git", "status", "--porcelain", "--untracked-files=no"]).strip() != ""
    return sha, dirty


def build_run(
    logs: Sequence[str],
    *,
    run: Runner = run_command,
    compose: Optional[str] = None,
    k6_image: Optional[str] = None,
    report_only: bool = False,
    now: Optional[dt.datetime] = None,
    run_id: Optional[str] = None,
    files: Callable[[str], Optional[str]] = read_text,
) -> Dict[str, Any]:
    """The run file of the logs' result lines, or a Refusal."""
    sha, dirty = git_state(run)
    if dirty:
        raise Refusal(
            "the tree has uncommitted tracked changes (git status --porcelain --untracked-files=no): commit first"
        )
    results: List[Dict[str, Any]] = []
    for text in logs:
        results.extend(parse_result_lines(text))
    scenarios, totals = merge_results(results, report_only)
    environment = collect_environment(
        run, compose, k6_image, totals["rate"], totals["duration"], files
    )
    now = (
        (now or dt.datetime.now(dt.timezone.utc)).astimezone(dt.timezone.utc).replace(microsecond=0)
    )
    return {
        "schema": SCHEMA,
        "run_id": run_id or f"{now.strftime('%Y%m%dT%H%M%SZ')}-{sha}",
        "created_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "git_sha": sha,
        "dirty": False,
        "vendors": VENDORS,
        "environment": environment,
        "scenarios": scenarios,
        "errors": totals["errors"],
    }


def write_run(doc: Dict[str, Any], out_dir: Path) -> Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{doc['run_id']}.json"
    if path.exists():
        raise Refusal(f"{path} exists already")
    path.write_text(json.dumps(doc, indent=2) + "\n", encoding="utf-8")
    return path


# --------------------------------------------------------------------------------- reading runs


def is_clean(doc: Dict[str, Any]) -> bool:
    """A clean run: published from a clean tree, every target met and none published report-only."""
    if doc.get("schema") != SCHEMA or doc.get("dirty") is not False:
        return False
    return all(
        s.get("ok") and (s.get("target") is None or not s.get("report_only"))
        for s in doc.get("scenarios", [])
    )


def newest_clean_run(runs_dir: Path) -> Optional[Dict[str, Any]]:
    """The newest clean run file in runs_dir (by created_at), or None."""
    best: Optional[Dict[str, Any]] = None
    for path in sorted(runs_dir.glob("*.json")) if runs_dir.is_dir() else []:
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if (
            isinstance(doc, dict)
            and is_clean(doc)
            and (best is None or doc["created_at"] > best["created_at"])
        ):
            best = doc
    return best


# --------------------------------------------------------------------------------- GATEWAY.md


def fmt_ms(value: float) -> str:
    return f"{value:.1f} ms" if value < 100 else f"{value:.0f} ms"


def target_words(target: Optional[Dict[str, Any]]) -> str:
    if not target:
        return "reported only"
    return f"{target['stat'].replace('_ms', '')} < {fmt_ms(float(target['max_ms']))}"


def gateway_block(doc: Optional[Dict[str, Any]]) -> str:
    """The GATEWAY.md block (between the markers, markers excluded) for a run, or the placeholder."""
    if doc is None:
        return (
            "No load run is published yet: `make load-test-gate LOAD_PUBLISH=1` writes one to "
            "`docs/benchmarks/load/`, and `scripts/load/report.py --gateway-md` shows it here."
        )
    env = doc["environment"]
    limits = env["proxy_limits"]
    proxy = "no limits"
    if limits["cpus"] is not None or limits["mem"] is not None:
        proxy = ", ".join(
            x
            for x in (
                f"{limits['cpus']:g} CPU" if limits["cpus"] is not None else "",
                limits["mem"] or "",
            )
            if x
        )
    lines = [
        f"<!-- generated by scripts/load/report.py --gateway-md from docs/benchmarks/load/{doc['run_id']}.json; do not edit -->",
        f"**Measured** (k6, simulated vendors: mock-upstream answers at once) on {env['cpu_model']}, "
        f"{env['cores']} cores, {env['mem_gb']:g} GB, {env['os']}; Docker {env['docker']}; proxy {proxy}; "
        f"{env['rate']} requests/s for {env['duration']} per scenario. Run `{doc['run_id']}` at `{doc['git_sha']}`. "
        "These are the gateway's own time, not a vendor's.",
        "",
        "| Scenario | Figure | n | p50 | p95 | p99 | Target | Result |",
        "|---|---|---|---|---|---|---|---|",
    ]
    for s in doc["scenarios"]:
        result = "reported only" if s["target"] is None else ("met" if s["ok"] else "missed")
        if s["target"] is not None and s["report_only"]:
            result += " (report only)"
        lines.append(
            f"| ({s['id']}) {SCENARIO_WHAT.get(s['id'], s['route'])} | {SOURCE_WORDS[s['source']]} | {s['n']} | "
            f"{fmt_ms(s['p50_ms'])} | {fmt_ms(s['p95_ms'])} | {fmt_ms(s['p99_ms'])} | {target_words(s['target'])} | {result} |"
        )
    errors = doc["errors"]
    lines += ["", f"5xx rate {errors['rate_5xx']:.2%}, {errors['non_200']} non-200 answer(s)."]
    return "\n".join(lines)


def replace_block(text: str, block: str) -> str:
    start, end = text.find(BLOCK_START), text.find(BLOCK_END)
    if start < 0 or end < 0 or end < start:
        raise Refusal(f"the document has no {BLOCK_START} ... {BLOCK_END} markers")
    return text[: start + len(BLOCK_START)] + "\n" + block + "\n" + text[end:]


# --------------------------------------------------------------------------------- CLI


def main(
    argv: Optional[Sequence[str]] = None, run: Runner = run_command, out=sys.stdout, err=sys.stderr
) -> int:
    p = argparse.ArgumentParser(
        description="Publish a gateway load run (D118), or its GATEWAY.md block."
    )
    p.add_argument(
        "logs", nargs="*", type=Path, help="k6 pass logs holding TRUSTAGENT_LOAD_RESULT lines"
    )
    p.add_argument("--out-dir", type=Path, default=DEFAULT_OUT)
    p.add_argument("--run-id")
    p.add_argument("--k6-image")
    p.add_argument(
        "--compose", help="the load stack's compose command (its config gives the proxy limits)"
    )
    p.add_argument(
        "--report-only",
        action="store_true",
        help="publish a run that missed a target, marked report-only",
    )
    p.add_argument(
        "--gateway-md",
        action="store_true",
        help="write the GATEWAY.md block from the newest clean run",
    )
    p.add_argument(
        "--check",
        action="store_true",
        help="with --gateway-md: check the block instead of writing it",
    )
    p.add_argument("--runs-dir", type=Path, default=DEFAULT_OUT)
    p.add_argument("--doc", type=Path, default=DEFAULT_DOC)
    a = p.parse_args(argv)
    try:
        if a.gateway_md:
            if a.logs:
                p.error("--gateway-md reads the published runs, not logs")
            text = a.doc.read_text(encoding="utf-8")
            want = replace_block(text, gateway_block(newest_clean_run(a.runs_dir)))
            if a.check:
                if want != text:
                    print(
                        f"report.py: the load block of {a.doc} is out of date: run scripts/load/report.py --gateway-md",
                        file=err,
                    )
                    return 1
                print(f"report.py: the load block of {a.doc} is up to date", file=out)
                return 0
            a.doc.write_text(want, encoding="utf-8")
            print(f"report.py: wrote the load block of {a.doc}", file=out)
            return 0
        if a.check:
            p.error("--check goes with --gateway-md")
        if not a.logs:
            p.error("give the k6 pass logs")
        texts = [path.read_text(encoding="utf-8", errors="replace") for path in a.logs]
        doc = build_run(
            texts,
            run=run,
            compose=a.compose,
            k6_image=a.k6_image,
            report_only=a.report_only,
            run_id=a.run_id,
        )
        path = write_run(doc, a.out_dir)
        print(f"report.py: wrote {path}", file=out)
        return 0
    except Refusal as exc:
        print(f"report.py: refused: {exc}", file=err)
        return 2
    except OSError as exc:
        print(f"report.py: {exc}", file=err)
        return 2


if __name__ == "__main__":
    sys.exit(main())
