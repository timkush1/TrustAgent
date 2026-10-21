"""Unit tests for scripts/record_demo.py (D123, D207-D210): a fake runner stands in for docker,
git, check_recording and the video recorder, and a fake reader for GET /api/v1/key, so nothing
starts and nothing is spent."""

from __future__ import annotations

import io
import json
import shutil
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import record_demo  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
SIMULATED = ROOT / "frontend-react" / "demo-data" / "simulated"
# The project's fake-key conventions; the second has no recognisable pattern.
FAKE_KEY = "sk-or-v1-e2e-recorddemotest"
PLAIN_SECRET = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
SINCE = "2026-10-10T09:00:00Z"
SNAPSHOT = "typesafe/jev-1.13-20260917"
VENDOR_HOSTS = {"decision": "openrouter.ai", "judge": "openrouter.ai", "upstream": "openrouter.ai"}


def caps_env(decision: str = "0.50", llm: str = "3.00") -> Dict[str, str]:
    return {
        record_demo.CAP_KEYS[0]: decision,
        record_demo.CAP_KEYS[1]: llm,
        "TRUTHTABLE_UPSTREAM_URL": "https://openrouter.ai/api",
        "TRUTHTABLE_UPSTREAM_API_KEY": FAKE_KEY,
    }


def good_config(decision: str = "0.50", llm: str = "3.00") -> Dict[str, Any]:
    """What `docker compose config --format json` prints for the record stack."""
    tag = record_demo.IMAGE_TAG
    return {
        "name": "truthtable-record",
        "services": {
            "proxy": {
                "image": f"ghcr.io/timkush1/truthtable-proxy:{tag}",
                "environment": {**record_demo.PROXY_ENV, **caps_env(decision, llm)},
            },
            "engine": {
                "image": f"ghcr.io/timkush1/truthtable-engine:{tag}",
                "environment": {**record_demo.ENGINE_ENV},
            },
            "postgres": {"image": "postgres:16-alpine", "environment": {}},
            "dashboard": {"image": f"ghcr.io/timkush1/truthtable-dashboard:{tag}"},
        },
    }


def status(
    state: str = "ready",
    backend: str = "openrouter-decisions",
    snapshot: Optional[str] = SNAPSHOT,
    host: str = "openrouter.ai",
    provider: str = "openrouter",
    judge_state: str = "up",
    fallback: bool = False,
) -> Dict[str, Any]:
    return {
        "test_mode": True,
        "decision_model": {
            "backend": backend,
            "state": state,
            "snapshot": snapshot,
            "endpoint_host": host,
        },
        "judge": {"state": judge_state, "provider": provider, "fallback_active": fallback},
    }


def featured_candidate(tape: Path) -> str:
    manifest = json.loads((tape / "manifest.json").read_text(encoding="utf-8"))
    for t in manifest["traces"]:
        if t.get("action") not in record_demo.FLAGGED_ACTIONS:
            continue
        detail = json.loads((tape / "traces" / f"{t['trace_id']}.json").read_text("utf-8"))
        claims = (detail.get("audit_detail") or {}).get("claims") or []
        if any(c["status"] == "CONTRADICTED" and c.get("span") for c in claims):
            return str(t["trace_id"])
    raise AssertionError("no candidate")


class FakeKey:
    """GET /api/v1/key: usage readings in order (the last repeats)."""

    def __init__(self, usages: Sequence[float], limit: Optional[float] = 8.0) -> None:
        self.usages = list(usages)
        self.limit = limit
        self.reads = 0

    def __call__(self) -> record_demo.KeyState:
        usage = self.usages[min(self.reads, len(self.usages) - 1)]
        self.reads += 1
        remaining = None if self.limit is None else self.limit - usage
        return record_demo.KeyState(usage=usage, limit=self.limit, limit_remaining=remaining)


class FakeRunner:
    """Answers the commands record_demo runs; each answer can be replaced per test."""

    def __init__(self, tmp: Path) -> None:
        self.tmp = tmp
        self.calls: List[List[str]] = []
        self.envs: List[Optional[Mapping[str, str]]] = []
        self.config: Optional[Dict[str, Any]] = None  # None: follow the attempt's stack.yml
        self.statuses: List[Dict[str, Any]] = [status()]
        self.trafficgen_codes: List[int] = []
        self.export_code = 0
        self.check_code = 0
        self.video_code = 0
        self.running = True
        self.extra_id: Optional[str] = None
        # The export: the simulated tape, relabelled as a real recording through vendor hosts.
        self.export = tmp / "export-source"
        shutil.copytree(SIMULATED, self.export)
        self.candidate = featured_candidate(self.export)
        manifest = json.loads((self.export / "manifest.json").read_text(encoding="utf-8"))
        manifest["kind"] = "recorded"
        manifest["label"] = (
            "Recorded 2026-10-10 · typesafe/jev-1.13-20260917 + anthropic/claude-haiku-4.5 · "
            "scripted answers · replayed, not live"
        )
        manifest["decision_model"].update(backend="openrouter-decisions", host="openrouter.ai")
        manifest["judge"].update(provider="openrouter", host="openrouter.ai")
        manifest["featured"] = {"trace_id": self.candidate, "rule": "contradicted_with_span"}
        manifest["calibration"] = {"snapshot": SNAPSHOT, "quality": "measured"}
        self.write_manifest(manifest)
        ids = [t["trace_id"] for t in manifest["traces"]]
        half = len(ids) // 2
        self.round_ids = [ids[:half], ids[half:]]

    def manifest(self) -> Dict[str, Any]:
        return json.loads((self.export / "manifest.json").read_text(encoding="utf-8"))

    def write_manifest(self, doc: Dict[str, Any]) -> None:
        (self.export / "manifest.json").write_text(json.dumps(doc), encoding="utf-8")

    @staticmethod
    def rest(argv: Sequence[str]) -> List[str]:
        """The compose subcommand and its arguments (after -p and every -f)."""
        i = 4  # docker compose -p truthtable-record
        while i < len(argv) and argv[i] == "-f":
            i += 2
        return list(argv[i:])

    def kind(self, argv: Sequence[str]) -> str:
        if list(argv[:3]) == ["docker", "compose", "version"]:
            return "version"
        if len(argv) > 1 and argv[1].endswith("check_recording.py"):
            return "check"
        if len(argv) > 1 and argv[1].endswith("launch_video.py"):
            return "video"
        if argv[0] == "git":
            return "git"
        if list(argv[:2]) == ["docker", "compose"]:
            rest = self.rest(argv)
            if rest[:1] == ["exec"]:
                return "exec " + rest[3]
            return rest[0]
        return "unknown"

    def stack_config(self, argv: Sequence[str]) -> Dict[str, Any]:
        if self.config is not None:
            return self.config
        files = [argv[i + 1] for i in range(len(argv) - 1) if argv[i] == "-f"]
        decision, llm = "0.50", "3.00"
        for f in files:
            if f.endswith("stack.yml"):
                text = Path(f).read_text(encoding="utf-8")
                decision = text.split(record_demo.CAP_KEYS[0] + ': "')[1].split('"')[0]
                llm = text.split(record_demo.CAP_KEYS[1] + ': "')[1].split('"')[0]
        return good_config(decision, llm)

    def __call__(
        self, argv: Sequence[str], stream: bool, env: Optional[Mapping[str, str]] = None
    ) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        self.envs.append(env)
        k = self.kind(argv)
        if k == "version":
            return 0, "Docker Compose version v2.39.2", ""
        if k == "config":
            return 0, json.dumps(self.stack_config(argv)), ""
        if k in ("down", "up"):
            self.running = k == "up"
            return 0, "", ""
        if k == "ps":
            return 0, "proxy\nengine\n" if self.running else "", ""
        if k == "exec sh":
            st = self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]
            return 0, json.dumps(st), ""
        if k == "exec date":
            return 0, SINCE + "\n", ""
        if k == "exec /trafficgen":
            return (self.trafficgen_codes.pop(0) if self.trafficgen_codes else 0), "", ""
        if k == "cp":
            src, dst = argv[-2], Path(argv[-1])
            if "ids" in src:
                n = int(src.rsplit("-", 1)[1].split(".")[0])
                ids = list(self.round_ids[n - 1])
                if n == 2 and self.extra_id:
                    ids.append(self.extra_id)
                dst.write_text("\n".join(ids) + "\n", encoding="utf-8")
            elif "drift" in src:
                n = int(src.rsplit("-", 1)[1].split(".")[0])
                report = {
                    "scenarios": [
                        {
                            "scenario": "grounded-pass",
                            "trace_id": self.round_ids[n - 1][0],
                            "outcome": "drift",
                            "mismatches": [
                                {"field": "X-TrustAgent-Action", "want": "pass", "got": "flag"}
                            ],
                            "error": None,
                        }
                    ],
                    "counts": {"ok": 25, "drift": 1, "unchecked": 2, "failed": 0},
                }
                dst.write_text(json.dumps(report), encoding="utf-8")
            else:
                shutil.copytree(self.export, dst)
            return 0, "", ""
        if k == "git":
            return 0, "abcdef123456\n", ""
        if k == "exec /export-replay":
            return self.export_code, "", "export-replay: no traces" if self.export_code else ""
        if k == "check":
            return self.check_code, "FAIL manifest.json: kind mock" if self.check_code else "ok", ""
        if k == "video":
            return self.video_code, "", ""
        raise AssertionError(f"unexpected command: {argv}")

    def kinds(self) -> List[str]:
        return [self.kind(c) for c in self.calls]

    def argv_of(self, kind: str, last: bool = False) -> List[str]:
        found = [c for c in self.calls if self.kind(c) == kind]
        return found[-1] if last else found[0]


class RecordDemoTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="record-demo-"))
        self.env_file = self.tmp / ".env"
        self.env_file.write_text("", encoding="utf-8")
        self.dest = self.tmp / "demo-data" / "recorded"
        self.dest.parent.mkdir()
        self.work = self.tmp / "tmp" / "record-demo"
        self.version = self.tmp / "VERSION"
        self.version.write_text(record_demo.RELEASE_VERSION + "\n", encoding="utf-8")
        self.calib = self.tmp / "calibration"
        self.calib.mkdir()
        self.write_calibration(SNAPSHOT, ["measured", "measured"])
        self.runner = FakeRunner(self.tmp)
        # The plan's reading, the attempt's usage before, its reading after review, its close.
        self.key = FakeKey([1.0, 1.0, 1.25, 1.30])
        self.out = io.StringIO()
        self.err = io.StringIO()
        self.slept: List[float] = []
        self.clock = [0.0]
        self.at = datetime(2026, 10, 21, 9, 30, 0, tzinfo=timezone.utc)

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def write_calibration(self, snapshot: str, qualities: List[str], backend: str = "x") -> None:
        doc = {
            "decision_model": {"backend": backend, "snapshot": snapshot},
            "entries": [{"quality": q} for q in qualities],
        }
        name = snapshot.replace("/", "__") + ".json"
        (self.calib / name).write_text(json.dumps(doc), encoding="utf-8")

    def recorder(self, environ: Optional[Dict[str, str]] = None) -> record_demo.Recorder:
        def sleep(seconds: float) -> None:
            self.slept.append(seconds)
            self.clock[0] += seconds

        def now() -> datetime:
            self.at += timedelta(seconds=1)
            return self.at

        env = {"CONFIRM": "1", "OPENROUTER_API_KEY": FAKE_KEY, "TRAFFICGEN_API_KEY": PLAIN_SECRET}
        return record_demo.Recorder(
            run=self.runner,
            environ=env if environ is None else environ,
            env_file=self.env_file,
            dest=self.dest,
            work=self.work,
            calibration_dir=self.calib,
            version_file=self.version,
            out=self.out,
            err=self.err,
            sleep=sleep,
            clock=lambda: self.clock[0],
            now=now,
            python="python3",
            key_reader=self.key,
            port_finder=lambda: 43210,
        )

    def main(self, argv: Sequence[str] = (), environ: Optional[Dict[str, str]] = None) -> int:
        return record_demo.main(list(argv), self.recorder(environ))

    def output(self) -> str:
        return self.out.getvalue() + self.err.getvalue()

    def ledger(self) -> List[Dict[str, Any]]:
        path = self.work / "ledger.jsonl"
        if not path.is_file():
            return []
        return [json.loads(line) for line in path.read_text("utf-8").splitlines() if line]

    def result(self) -> Dict[str, Any]:
        lines = [
            x for x in self.out.getvalue().splitlines() if x.startswith("TRUSTAGENT_P6_RESULT ")
        ]
        self.assertTrue(lines, self.output())
        return json.loads(lines[-1].split(" ", 1)[1])

    def attempts(self) -> List[str]:
        return (
            sorted(p.name for p in self.work.iterdir() if p.is_dir()) if self.work.is_dir() else []
        )

    def assert_no_key_printed(self) -> None:
        self.assertNotIn(FAKE_KEY, self.output())
        self.assertNotIn(PLAIN_SECRET, self.output())

    def record_attempt(self) -> str:
        self.assertEqual(self.main(), 0, self.output())
        return self.attempts()[-1]

    # -------------------------------------------------------------------------- the caps

    def test_cap_arithmetic(self) -> None:
        caps = record_demo.plan_caps(4.0, 0.0, None)
        self.assertEqual(
            (caps.remaining, caps.decision, caps.llm, caps.refused), (4.0, 0.5, 3.0, None)
        )
        # Spent across attempts lowers R; the decision cap is taken first.
        caps = record_demo.plan_caps(4.0, 2.9, None)
        self.assertEqual((caps.decision, caps.llm), (0.5, 0.6))
        # The key's limit_remaining (less the $0.10 reserve) wins when it is lower.
        caps = record_demo.plan_caps(4.0, 0.0, 1.30)
        self.assertEqual((caps.remaining, caps.decision, caps.llm), (1.2, 0.5, 0.7))
        caps = record_demo.plan_caps(4.0, 1.0, 0.65)
        self.assertEqual((caps.decision, caps.llm), (0.5, 0.05))
        # Below $0.50 it refuses, whichever side runs short.
        self.assertIsNotNone(record_demo.plan_caps(4.0, 3.6, 8.0).refused)
        refused = record_demo.plan_caps(4.0, 0.0, 0.55).refused
        self.assertIsNotNone(refused)
        self.assertIn("key's limit", str(refused))
        # Caps never round up.
        caps = record_demo.plan_caps(4.0, 0.0, 0.6789)
        self.assertEqual((caps.decision, caps.llm), (0.5, 0.07))

    def test_caps_follow_the_ledger_across_attempts(self) -> None:
        ledger = record_demo.Ledger(self.work / "ledger.jsonl")
        ledger.append({"attempt": "a1", "event": "start", "usage_before_usd": 1.0})
        ledger.append({"attempt": "a1", "event": "measure", "spent_usd": 0.8})
        ledger.append({"attempt": "a1", "event": "end", "outcome": "discarded", "spent_usd": 1.1})
        ledger.append({"attempt": "a2", "event": "start", "usage_before_usd": 2.1})
        ledger.append({"attempt": "a2", "event": "end", "outcome": "failed", "spent_usd": 0.4})
        self.assertAlmostEqual(ledger.spent(), 1.5)
        self.assertIsNone(ledger.open_attempt())
        self.key.usages = [2.5]
        self.assertEqual(self.main(["--dry-run"]), 0)
        self.assertIn("$1.5000 spent over 2 attempt(s)", self.output())
        # R = min(4 - 1.5, 8 - 2.5 - 0.1) = 2.5: decision 0.50, deep audits 2.00.
        self.assertIn("fast checks $0.50, deep audits $2.00", self.output())
        ledger.append({"attempt": "a3", "event": "start", "usage_before_usd": 2.5})
        self.assertEqual(ledger.open_attempt(), "a3")

    # -------------------------------------------------------------------------- the plan

    def test_the_dry_run_prints_the_plan_and_starts_nothing(self) -> None:
        self.assertEqual(self.main(["--dry-run"]), 0)
        out = self.output()
        for want in (
            "Record the public demo",
            "images tagged p6rec",
            "no host port",
            "OPENROUTER_API_KEY set",
            "usage $1.0000 (GET /api/v1/key, free); limit $8.00, $7.0000 left",
            "R $4.00 left",
            "model openai/…, max_tokens 300",
            "generated-monthly-refund",
            "Dry run: nothing started, nothing spent",
        ):
            self.assertIn(want, out)
        self.assertEqual(self.runner.calls, [])
        self.assertEqual(self.ledger(), [])
        self.assert_no_key_printed()

    def test_without_a_key_the_plan_is_all_it_does(self) -> None:
        r = self.recorder({"CONFIRM": "1"})
        r.key_reader = None
        self.assertEqual(record_demo.main([], r), 2)
        self.assertIn("no OPENROUTER_API_KEY: plan only", self.output())
        self.assertIn("refused: no decision-model key", self.output())
        self.assertEqual(self.runner.calls, [])

    def test_without_confirm_it_is_a_dry_run(self) -> None:
        self.assertEqual(self.main([], {"OPENROUTER_API_KEY": FAKE_KEY}), 0)
        self.assertIn("Dry run", self.output())
        self.assertEqual(self.runner.calls, [])

    def test_the_plan_reads_the_scenario_pack(self) -> None:
        plan = record_demo.load_plan(passes=3)
        self.assertEqual(plan.passes, 3)
        self.assertEqual(
            sorted(plan.generated), ["generated-monthly-refund", "generated-weekend-shipping"]
        )
        self.assertEqual(plan.requests, 3 * len(plan.scenarios))

    # -------------------------------------------------------------------------- refusals

    def test_a_version_mismatch_is_refused(self) -> None:
        self.version.write_text("1.0.0\n", encoding="utf-8")
        self.assertEqual(self.main(), 2)
        self.assertIn("VERSION is 1.0.0, not 2.0.0", self.output())
        self.assertIn("make version-bump V=2.0.0", self.output())
        self.assertEqual(self.runner.calls, [])
        self.assertEqual(self.ledger(), [])

    def test_an_uncalibrated_snapshot_is_refused(self) -> None:
        # No file for a real snapshot at all: refused before anything starts.
        for p in self.calib.iterdir():
            p.unlink()
        self.write_calibration("mock/jev-heuristic-v1", ["mock"], backend="mock")
        self.assertEqual(self.main(), 2)
        self.assertIn("no calibration for a real snapshot", self.output())
        self.assertEqual(self.runner.calls, [])
        # A file for another snapshot: the stack's snapshot is checked after the start.
        self.write_calibration("typesafe/jev-1.12-20260801", ["measured"])
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(), 1)
        self.assertIn(f"the snapshot {SNAPSHOT} has no", self.output())
        self.assertIn("ALLOW_UNCALIBRATED=1", self.output())
        self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertNotIn("exec /trafficgen", self.runner.kinds())
        self.assertEqual(self.ledger()[-1]["outcome"], "failed")

    def test_allow_uncalibrated_writes_the_overlay(self) -> None:
        for p in self.calib.iterdir():
            p.unlink()
        self.assertEqual(self.main(["--allow-uncalibrated"]), 0, self.output())
        folder = self.work / self.attempts()[-1]
        policy = (folder / "policies.yaml").read_text(encoding="utf-8")
        stack = (folder / "stack.yml").read_text(encoding="utf-8")
        route = policy.split("  - name: payments-faq\n", 1)[1].split("  - name: ", 1)[0]
        self.assertIn("    require_calibration: false", route)
        self.assertEqual(policy.count("require_calibration: false"), 1)
        self.assertIn(f"TRUTHTABLE_POLICY_FILE: {record_demo.RECORD_POLICY_PATH}", stack)
        self.assertIn(
            str(folder / "policies.yaml") + ":" + record_demo.RECORD_POLICY_PATH + ":ro", stack
        )
        export = self.runner.argv_of("exec /export-replay")
        self.assertEqual(export[export.index("--calibration-quality") + 1], "none")
        self.assertNotIn("--calibration-snapshot", export)
        self.assertIn("uncalibrated", self.output())

    def test_uncalibrated_policy_touches_only_payments_faq(self) -> None:
        text = (ROOT / "config" / "policies.yaml").read_text(encoding="utf-8")
        out = record_demo.uncalibrated_policy(text)
        self.assertEqual(len(out.splitlines()), len(text.splitlines()) + 1)
        with self.assertRaises(record_demo.Refused):
            record_demo.uncalibrated_policy(out)  # already set: refused, never doubled
        with self.assertRaises(record_demo.Refused):
            record_demo.uncalibrated_policy("routes: []\n")

    def test_it_refuses_without_a_key(self) -> None:
        r = self.recorder({"CONFIRM": "1"})
        self.assertEqual(record_demo.main([], r), 2)
        self.assertIn("refused: no decision-model key", self.output())
        self.assertEqual(self.runner.calls, [])

    def test_a_key_in_dot_env_counts_and_is_never_printed(self) -> None:
        self.env_file.write_text(f"OPENROUTER_API_KEY={FAKE_KEY}\n", encoding="utf-8")
        self.assertEqual(self.main([], {"CONFIRM": "1", "TRAFFICGEN_API_KEY": PLAIN_SECRET}), 0)
        self.assertIn("OPENROUTER_API_KEY set", self.output())
        self.assert_no_key_printed()

    def test_it_refuses_the_mock_or_off_in_dot_env(self) -> None:
        for line in (
            "TRUTHTABLE_JEV_BACKEND=mock",
            "LLM_PROVIDER=mock",
            "TRUTHTABLE_JEV_BACKEND=off",
        ):
            self.env_file.write_text(line + "\n", encoding="utf-8")
            self.out, self.err = io.StringIO(), io.StringIO()
            self.assertEqual(self.main(), 2, line)
            self.assertIn("refused", self.output())
        self.assertEqual(self.runner.calls, [])

    def test_the_cap_refusal_comes_before_anything_starts(self) -> None:
        self.key.usages, self.key.limit = [7.5], 8.0  # $0.40 left under the reserve
        self.assertEqual(self.main(), 2)
        self.assertIn("an attempt needs at least $0.50", self.output())
        self.assertEqual(self.runner.calls, [])

    def test_an_open_attempt_blocks_a_new_one(self) -> None:
        attempt = self.record_attempt()
        self.out, self.err = io.StringIO(), io.StringIO()
        calls = len(self.runner.calls)
        self.assertEqual(self.main(), 2)
        self.assertIn(f"attempt {attempt} is still open", self.output())
        self.assertEqual(len(self.runner.calls), calls)

    def test_it_refuses_a_stack_without_its_caps_or_with_a_port(self) -> None:
        for change, needle in (
            (
                lambda c: c["services"]["proxy"]["environment"].update(
                    {record_demo.CAP_KEYS[0]: "5"}
                ),
                "this attempt's cap",
            ),
            (
                lambda c: c["services"]["proxy"].update(ports=[{"target": 8080}]),
                "proxy publishes a port",
            ),
            (
                lambda c: c["services"]["proxy"]["environment"].update(
                    TRUTHTABLE_UPSTREAM_URL="http://mock-upstream:8090"
                ),
                "OpenRouter's",
            ),
            (
                lambda c: c["services"]["proxy"]["environment"].update(
                    TRUTHTABLE_UPSTREAM_API_KEY=""
                ),
                "UPSTREAM_API_KEY is empty",
            ),
            (
                lambda c: c["services"]["proxy"].update(
                    image="ghcr.io/timkush1/truthtable-proxy:2.0.0"
                ),
                "tagged p6rec",
            ),
            (lambda c: c["services"].update({"mock-upstream": {}}), "mock-upstream must not run"),
        ):
            self.runner.config = good_config()
            change(self.runner.config)
            self.out, self.err = io.StringIO(), io.StringIO()
            self.assertEqual(self.main(), 2, needle)
            self.assertIn(needle, self.output())
            self.assertNotIn("up", self.runner.kinds())
        self.assertEqual(self.ledger(), [])
        self.assertEqual(self.attempts(), [])
        self.assert_no_key_printed()

    def test_the_overlay_has_the_upstream_and_no_port(self) -> None:
        overlay = (ROOT / "docker-compose.record.yml").read_text(encoding="utf-8")
        proxy = overlay.split("\n  proxy:\n", 1)[1].split("\n  engine:\n", 1)[0]
        self.assertIn("TRUTHTABLE_UPSTREAM_URL: https://openrouter.ai/api", proxy)
        self.assertIn("TRUTHTABLE_UPSTREAM_API_KEY: ${OPENROUTER_API_KEY:-}", proxy)
        self.assertIn("ports: !reset []", proxy)
        self.assertIn('TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY: "0.50"', proxy)
        self.assertIn('TRUTHTABLE_SPEND_CAP_LLM_USD_DAY: "3"', proxy)
        self.assertNotIn("127.0.0.1::8080", overlay)

    # -------------------------------------------------------------------------- the preflight

    def test_it_refuses_a_mock_or_local_fast_check(self) -> None:
        for st, needle in (
            (status(backend="mock", state="mock"), "the fast check is mock"),
            (status(host="mock-upstream"), "non_vendor_host: the fast check calls mock-upstream"),
            (status(provider="mock"), "the judge is mock"),
            (status(fallback=True), "running its fallback"),
            (status(state="no_credits"), "the fast check is no_credits"),
        ):
            self.runner.statuses = [st]
            self.out, self.err = io.StringIO(), io.StringIO()
            self.assertEqual(self.main(), 1, needle)
            self.assertIn(needle, self.output())
            self.assertNotIn("exec /trafficgen", self.runner.kinds())
            self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertTrue(all(e["outcome"] == "failed" for e in self.ledger() if e["event"] == "end"))

    def test_the_preflight_waits_for_the_first_check(self) -> None:
        self.runner.statuses = [
            status(state="checking", snapshot=None),
            status(judge_state="starting"),
            status(),
        ]
        self.assertEqual(self.main(), 0, self.output())
        self.assertEqual(self.slept.count(record_demo.PREFLIGHT_INTERVAL_S), 2)

    def test_the_preflight_gives_up(self) -> None:
        self.runner.statuses = [status(state="checking", snapshot=None)]
        self.assertEqual(self.main(), 1)
        self.assertIn("did not show a ready fast check", self.output())

    def test_a_judge_off_vendor_is_refused(self) -> None:
        orig = self.runner.stack_config

        def with_mock_judge(argv: Sequence[str]) -> Dict[str, Any]:
            c = orig(argv)
            c["services"]["engine"]["environment"][
                "TRUTHTABLE_OPENROUTER_BASE_URL"
            ] = "http://mock-upstream:8090"
            return c

        self.runner.stack_config = with_mock_judge  # type: ignore[method-assign]
        self.assertEqual(self.main(), 1)
        self.assertIn("non_vendor_host: judge mock-upstream", self.output())

    # -------------------------------------------------------------------------- an attempt

    def test_an_attempt_records_and_waits_for_review(self) -> None:
        self.assertEqual(self.main(), 0, self.output())
        attempt = self.attempts()[-1]
        folder = self.work / attempt
        kinds = self.runner.kinds()
        self.assertEqual(kinds[:3], ["version", "config", "down"])
        self.assertIn("up", kinds)
        self.assertEqual(kinds.count("exec /trafficgen"), 2)
        self.assertNotEqual(kinds[-1], "down")  # the stack stays up for the review
        # Every compose call names the attempt's stack.yml and runs with the p6rec tag.
        for argv, env in zip(self.runner.calls, self.runner.envs):
            if self.runner.kind(argv) not in ("version", "git", "check"):
                self.assertIn(str(folder / "stack.yml"), argv)
                self.assertEqual((env or {}).get("TRUSTAGENT_VERSION"), "p6rec")
        up = self.runner.argv_of("up")
        self.assertIn("--build", up)
        tg = self.runner.argv_of("exec /trafficgen")
        for flag, value in (
            ("--model-prefix", "openai/"),
            ("--max-tokens", "300"),
            ("--upstream", "generated"),
        ):
            self.assertEqual(tg[tg.index(flag) + 1], value)
        self.assertIn("--drift-out", tg)
        export = self.runner.argv_of("exec /export-replay")
        for flag, value in (
            ("--decision-host", "openrouter.ai"),
            ("--judge-host", "openrouter.ai"),
            ("--generated-host", "openrouter.ai"),
            ("--calibration-quality", "measured"),
            ("--calibration-snapshot", SNAPSHOT),
            ("--since", SINCE),
        ):
            self.assertEqual(export[export.index(flag) + 1], value)
        self.assertTrue((folder / "tape" / "manifest.json").is_file())
        # What the video's stack beats need (D210): an admin key of this attempt only (never
        # printed), and the dashboard origin it will publish on 127.0.0.1.
        key = (folder / "admin.key").read_text(encoding="utf-8").strip()
        self.assertRegex(key, r"^[0-9a-f]{48}$")
        self.assertEqual((folder / "admin.key").stat().st_mode & 0o777, 0o600)
        stack = (folder / "stack.yml").read_text(encoding="utf-8")
        self.assertIn(f'TRUTHTABLE_ADMIN_KEYS: "video={key}"', stack)
        self.assertIn(
            'TRUTHTABLE_ALLOWED_ORIGINS: "http://localhost:43210,http://127.0.0.1:43210"', stack
        )
        self.assertNotIn(key, self.output())
        drift = json.loads((folder / "drift.json").read_text(encoding="utf-8"))
        self.assertEqual([p["pass"] for p in drift["passes"]], [1, 2])
        self.assertIn("grounded-pass: X-TrustAgent-Action", self.output())
        doc = json.loads((folder / "attempt.json").read_text(encoding="utf-8"))
        self.assertEqual(doc["state"], "review")
        self.assertEqual(doc["hosts"], VENDOR_HOSTS)
        self.assertEqual(doc["featured"]["trace_id"], self.runner.candidate)
        self.assertEqual(doc["skipped"], [])
        self.assertEqual(doc["video_port"], 43210)
        # Nothing reaches demo-data/recorded before ACCEPT.
        self.assertFalse(self.dest.exists())
        # The ledger: the usage before, and the reading after a 30 s settle.
        events = [(e["event"], e.get("spent_usd")) for e in self.ledger()]
        self.assertEqual(events, [("start", None), ("measure", 0.25)])
        self.assertIn(record_demo.SETTLE_S, self.slept)
        result = self.result()
        self.assertEqual(result["step"], "record-demo")
        self.assertEqual(result["run_id"], attempt)
        self.assertEqual(result["exit"], 0)
        self.assertEqual(result["cost_usd"], 0.25)
        self.assertEqual(result["cap_usd"], 3.5)
        self.assertEqual(result["key_usage_usd"], 1.25)
        self.assertEqual(result["key_limit_remaining_usd"], 6.75)
        self.assertEqual(result["snapshot"], SNAPSHOT)
        self.assertEqual(result["hosts"], VENDOR_HOSTS)
        self.assertEqual(result["counts"]["n"], 80)
        self.assertIn(f"ACCEPT=1 ATTEMPT={attempt}", self.output())
        self.assert_no_key_printed()

    def test_no_featured_trace_means_review_needed(self) -> None:
        manifest = self.runner.manifest()
        del manifest["featured"]
        self.runner.write_manifest(manifest)
        self.assertEqual(self.main(), 4, self.output())
        self.assertIn("Review needed", self.output())
        self.assertIn("FEATURED=<trace id>", self.output())
        self.assertIn(self.runner.candidate, self.output())  # listed among the flagged traces
        self.assertEqual(self.result()["exit"], 4)
        self.assertNotEqual(self.runner.kinds()[-1], "down")

    def test_a_trafficgen_refusal_stops_the_attempt(self) -> None:
        self.runner.trafficgen_codes = [2]
        self.assertEqual(self.main(), 1)
        self.assertIn("trafficgen refused to record", self.output())
        self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertEqual(self.ledger()[-1]["outcome"], "failed")
        self.assertEqual(self.result()["exit"], 1)

    def test_the_export_must_hold_exactly_the_answered_traces(self) -> None:
        self.runner.extra_id = "01a0e700-0000-7000-8000-00000000abcd"
        self.assertEqual(self.main(), 1)
        self.assertIn("1 answered trace(s) missing", self.output())

    def test_a_refused_tape_fails_the_attempt(self) -> None:
        self.runner.check_code = 1
        self.assertEqual(self.main(), 1)
        self.assertIn("check_recording.py refused the tape", self.output())

    def test_keep_leaves_a_failed_stack_up(self) -> None:
        self.runner.trafficgen_codes = [1]
        self.assertEqual(self.main(["--keep"]), 1)
        self.assertIn("--keep: the record stack is still up", self.output())
        self.assertEqual(self.runner.kinds().count("down"), 1)  # the clean start only
        # The attempt stays open until DISCARD=1 closes it.
        self.assertIsNotNone(record_demo.Ledger(self.work / "ledger.jsonl").open_attempt())

    def test_the_ledger_survives_down_v(self) -> None:
        attempt = self.record_attempt()
        ledger = self.work / "ledger.jsonl"
        before = ledger.read_text(encoding="utf-8")
        self.out = io.StringIO()
        self.assertEqual(self.main(["--discard"]), 0, self.output())
        # down -v removes volumes; the ledger is a file under tmp/, untouched, then appended to.
        self.assertTrue(ledger.read_text(encoding="utf-8").startswith(before))
        self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertIn("-v", self.runner.calls[-1])
        end = self.ledger()[-1]
        self.assertEqual(
            (end["attempt"], end["event"], end["outcome"]), (attempt, "end", "discarded")
        )
        self.assertAlmostEqual(end["spent_usd"], 0.30)
        self.assertEqual(
            json.loads((self.work / attempt / "attempt.json").read_text("utf-8"))["state"],
            "discarded",
        )
        self.assertEqual(self.result()["step"], "record-demo-discard")

    def test_attempts_add_up_against_the_total(self) -> None:
        # Attempt 1 spends 0.30 (1.00 -> 1.30), then attempt 2 sees R = min(1.0 - 0.30, ...).
        self.record_attempt()
        self.assertEqual(self.main(["--discard"]), 0)
        self.key.usages, self.key.reads = [1.30, 1.30, 1.40], 0
        self.out = io.StringIO()
        self.assertEqual(self.main(["--max-total-usd", "1.0"]), 0, self.output())
        self.assertIn("fast checks $0.50, deep audits $0.20", self.output())
        self.assertEqual(self.result()["cap_usd"], 0.7)
        self.key.usages, self.key.reads = [1.60], 0  # attempt 2 spent 0.30: 0.60 in all
        self.assertEqual(self.main(["--discard"]), 0)
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(["--max-total-usd", "1.0"]), 2)
        self.assertIn("an attempt needs at least $0.50", self.output())

    # -------------------------------------------------------------------------- accept

    def test_accept_without_a_reviewed_attempt_is_refused(self) -> None:
        self.assertEqual(self.main(["--accept", "20261021T093000Z"]), 2)
        self.assertIn("no reviewed attempt 20261021T093000Z", self.output())
        self.assertEqual(self.main(["--accept", "latest"]), 2)
        self.assertIn("is not an attempt id", self.output())
        # A failed attempt is not under review.
        self.runner.trafficgen_codes = [1]
        self.assertEqual(self.main(), 1)
        failed = self.attempts()[-1]
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(["--accept", failed]), 2)
        self.assertIn("is failed, not under review", self.output())
        self.assertFalse(self.dest.exists())

    def test_a_budget_skipped_attempt_is_refused(self) -> None:
        manifest = self.runner.manifest()
        manifest["traces"][3]["skip_reason"] = "budget"
        manifest["traces"][5]["flags"] = ["llm_budget"]
        self.runner.write_manifest(manifest)
        attempt = self.record_attempt()
        self.assertIn("cannot be accepted", self.output())
        doc = json.loads((self.work / attempt / "attempt.json").read_text("utf-8"))
        self.assertEqual(len(doc["skipped"]), 2)
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(["--accept", attempt]), 2)
        self.assertIn("cut short by a budget", self.output())
        self.assertFalse(self.dest.exists())

    def test_accept_swaps_the_tape_in_and_removes_the_stack(self) -> None:
        attempt = self.record_attempt()
        self.out = io.StringIO()
        self.assertEqual(self.main(["--accept", attempt]), 0, self.output())
        check = self.runner.argv_of("check", last=True)
        self.assertIn("--require-recorded", check)
        self.assertTrue((self.dest / "manifest.json").is_file())
        self.assertEqual(
            json.loads((self.dest / "manifest.json").read_text("utf-8"))["kind"], "recorded"
        )
        self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertNotIn("video", self.runner.kinds())
        end = self.ledger()[-1]
        self.assertEqual((end["event"], end["outcome"]), ("end", "accepted"))
        self.assertEqual(self.result()["step"], "record-demo-accept")
        self.assertEqual(
            json.loads((self.work / attempt / "attempt.json").read_text("utf-8"))["state"],
            "accepted",
        )
        # Accepted once: a second ACCEPT is refused.
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(["--accept", attempt]), 2)
        self.assertIn("is accepted, not under review", self.output())

    def test_accept_with_video_records_the_stack_beats_first(self) -> None:
        attempt = self.record_attempt()
        self.assertEqual(self.main(["--accept", attempt, "--video"]), 0, self.output())
        kinds = self.runner.kinds()
        self.assertLess(kinds.index("video"), len(kinds) - 1)
        self.assertEqual(kinds[-1], "down")
        video = self.runner.argv_of("video")
        self.assertEqual(video[2:], ["stack", "--attempt", attempt])

    def test_a_failed_video_swaps_nothing_and_keeps_the_stack(self) -> None:
        attempt = self.record_attempt()
        self.runner.video_code = 1
        self.assertEqual(self.main(["--accept", attempt, "--video"]), 1)
        self.assertIn("the video's stack beats failed", self.output())
        self.assertFalse(self.dest.exists())
        self.assertNotEqual(self.runner.kinds()[-1], "down")

    def test_accept_with_the_owners_featured_pick_reexports(self) -> None:
        manifest = self.runner.manifest()
        del manifest["featured"]
        self.runner.write_manifest(manifest)
        self.assertEqual(self.main(), 4)
        attempt = self.attempts()[-1]
        # Without a pick the attempt cannot be accepted.
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(self.main(["--accept", attempt]), 2)
        self.assertIn("has no featured trace", self.output())
        manifest["featured"] = {"trace_id": self.runner.candidate, "rule": "best_flagged"}
        self.runner.write_manifest(manifest)
        self.out, self.err = io.StringIO(), io.StringIO()
        self.assertEqual(
            self.main(["--accept", attempt, "--featured", self.runner.candidate]), 0, self.output()
        )
        export = self.runner.argv_of("exec /export-replay", last=True)
        self.assertEqual(export[export.index("--featured") + 1], self.runner.candidate)
        self.assertEqual(export[export.index("--since") + 1], SINCE)
        self.assertEqual(
            json.loads((self.dest / "manifest.json").read_text("utf-8"))["featured"]["rule"],
            "best_flagged",
        )

    def test_featured_needs_a_running_stack_and_a_trace_id(self) -> None:
        attempt = self.record_attempt()
        self.assertEqual(self.main(["--accept", attempt, "--featured", "nope"]), 2)
        self.assertIn("is not a trace id", self.output())
        self.runner.running = False
        self.assertEqual(self.main(["--accept", attempt, "--featured", self.runner.candidate]), 2)
        self.assertIn("not running", self.output())
        self.assertEqual(self.main(["--featured", self.runner.candidate]), 2)
        self.assertIn("go with ACCEPT=1", self.output())

    def test_a_tape_refused_at_accept_is_not_swapped_in(self) -> None:
        attempt = self.record_attempt()
        self.runner.check_code = 1
        self.assertEqual(self.main(["--accept", attempt]), 1)
        self.assertIn("--require-recorded refused the tape", self.output())
        self.assertFalse(self.dest.exists())

    def test_accept_replaces_the_previous_recording(self) -> None:
        self.dest.mkdir()
        (self.dest / "manifest.json").write_text("{}", encoding="utf-8")
        attempt = self.record_attempt()
        self.assertEqual(self.main(["--accept", attempt]), 0)
        self.assertNotEqual((self.dest / "manifest.json").read_text("utf-8"), "{}")
        self.assertFalse(self.dest.with_name(".recorded.old").exists())

    def test_discard_without_an_attempt_only_removes_the_stack(self) -> None:
        self.assertEqual(self.main(["--discard"]), 0)
        self.assertEqual(self.runner.kinds(), ["down"])
        self.assertIn("No open attempt", self.output())
        self.assertEqual(self.ledger(), [])

    def test_bad_arguments(self) -> None:
        for args in (
            ["--passes", "0"],
            ["--max-total-usd", "50"],
            ["--since", "yesterday"],
            ["--video"],
        ):
            self.out, self.err = io.StringIO(), io.StringIO()
            self.assertEqual(self.main(args), 2, args)
        self.assertEqual(self.runner.calls, [])


class KeyTest(unittest.TestCase):
    def test_parse_key(self) -> None:
        ks = record_demo.parse_key(
            {
                "data": {
                    "label": "sk-or-v1-abc...xyz",
                    "usage": 1.5,
                    "limit": 8,
                    "limit_remaining": 6.5,
                }
            }
        )
        self.assertEqual((ks.usage, ks.limit, ks.limit_remaining), (1.5, 8.0, 6.5))
        ks = record_demo.parse_key({"data": {"usage": 2, "limit": 8}})
        self.assertEqual(ks.limit_remaining, 6.0)
        ks = record_demo.parse_key({"data": {"usage": 2, "limit": None}})
        self.assertIsNone(ks.limit_remaining)
        for bad in ({}, {"data": {}}, {"data": {"usage": "1"}}, {"data": {"usage": True}}):
            with self.assertRaises(record_demo.KeyReadError):
                record_demo.parse_key(bad)

    def test_the_reader_needs_https(self) -> None:
        with self.assertRaises(record_demo.KeyReadError):
            record_demo.openrouter_key_reader(FAKE_KEY, "http://openrouter.ai")
        record_demo.openrouter_key_reader(FAKE_KEY, "https://openrouter.ai")
        record_demo.openrouter_key_reader(FAKE_KEY, "http://127.0.0.1:18090")

    def test_hosts(self) -> None:
        self.assertEqual(record_demo.judge_host("openrouter", ""), "openrouter.ai")
        self.assertEqual(
            record_demo.judge_host("openrouter", "http://mock-upstream:8090"), "mock-upstream"
        )
        self.assertEqual(record_demo.judge_host("anthropic", "x"), "api.anthropic.com")
        self.assertEqual(record_demo.judge_host("ollama", ""), "")
        self.assertEqual(record_demo.host_of("https://OpenRouter.ai/api"), "openrouter.ai")
        self.assertEqual(
            record_demo.vendor_hosts(), ("openrouter.ai", "api.typesafe.ai", "api.anthropic.com")
        )

    def test_calibration_quality(self) -> None:
        tmp = Path(tempfile.mkdtemp(prefix="calib-"))
        try:
            path = record_demo.calibration_file("typesafe/jev-1.13-20260917", tmp)
            self.assertEqual(path.name, "typesafe__jev-1.13-20260917.json")
            for qualities, want in (
                (["measured"], "measured"),
                (["measured", "provisional"], "provisional"),
            ):
                path.write_text(
                    json.dumps({"entries": [{"quality": q} for q in qualities]}), "utf-8"
                )
                self.assertEqual(record_demo.calibration_quality(path), want)
            path.write_text(json.dumps({"entries": [{"quality": "mock"}]}), "utf-8")
            with self.assertRaises(record_demo.Failed):
                record_demo.calibration_quality(path)
            with self.assertRaises(record_demo.Failed):
                record_demo.calibration_file("../etc/passwd", tmp)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)


class RecordTargetTest(unittest.TestCase):
    def test_make_record_demo_maps_its_variables(self) -> None:
        mk = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")
        recipe = mk.split("\nrecord-demo:", 1)[1].split("\n\n", 1)[0]
        self.assertIn(
            'CONFIRM="$(CONFIRM)" $(PYTHON) scripts/record_demo.py $(RECORD_DEMO_ARGS)', recipe
        )
        args = mk.split("\nRECORD_DEMO_ARGS = ", 1)[1].split("\n\n", 1)[0]
        for want in (
            "--passes $(RECORD_PASSES)",
            "--max-total-usd $(RECORD_MAX_TOTAL_USD)",
            "--allow-uncalibrated",
            '--accept "$(ATTEMPT)"',
            '--featured "$(FEATURED)"',
            "--video",
            "--discard",
        ):
            self.assertIn(want, args)
        self.assertRegex(mk, r"(?m)^RECORD_MAX_TOTAL_USD \?= 4$")
        self.assertRegex(mk, r"(?m)^RECORD_PASSES +\?= 2$")
        check = mk.split("\nrecord-check-config:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("go -C backend-go run ./cmd/proxy -check-config", check)
        expect = mk.split("\nRECORD_EXPECT = ", 1)[1].split("\n\n", 1)[0]
        for kv in (
            "TRUTHTABLE_TEST_MODE=true",
            "TRUTHTABLE_STRICT_CONFIG=true",
            "TRUTHTABLE_UPSTREAM_URL=https://openrouter.ai/api",
        ):
            self.assertIn(kv, expect)
        self.assertIn("check: record-check-config", mk)


if __name__ == "__main__":
    unittest.main()
