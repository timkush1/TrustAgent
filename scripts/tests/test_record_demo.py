"""Unit tests for scripts/record_demo.py (D123): a fake runner stands in for docker, git, doctor
and check_recording, so nothing starts and nothing is spent."""

from __future__ import annotations

import io
import json
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import record_demo  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
SIMULATED = ROOT / "frontend-react" / "demo-data" / "simulated"
# The project's fake-key conventions; the second has no recognisable pattern.
FAKE_KEY = "sk-or-v1-e2e-recorddemotest"
PLAIN_SECRET = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
SINCE = "2026-10-10T09:00:00Z"


def good_config() -> Dict[str, Any]:
    """What `docker compose config --format json` prints for the record stack."""
    return {
        "name": "truthtable-record",
        "services": {
            "proxy": {
                "environment": dict(record_demo.PROXY_ENV),
                "ports": [{"mode": "ingress", "host_ip": "127.0.0.1", "target": 8080}],
            },
            "engine": {"environment": dict(record_demo.ENGINE_ENV)},
            "postgres": {"environment": {}},
            "dashboard": {"environment": {}},
        },
    }


def doctor_report(checks: List[Dict[str, Any]]) -> str:
    return json.dumps({"target": "docker", "project": "truthtable-record", "checks": checks})


def dm_check(status: str = "PASS", backend: str = "openrouter-decisions", state: str = "ready"):
    return {
        "id": "decision_model.state",
        "status": status,
        "summary": f"fast check: backend {backend}, state {state}",
        "detail": "",
        "fix": "",
        "data": {"backend": backend, "state": state, "snapshot": "typesafe/jev-1.13"},
    }


def judge_check(status: str = "PASS", provider: str = "openrouter", fallback: bool = False):
    return {
        "id": "judge.credential_source",
        "status": status,
        "summary": f"judge {provider}",
        "detail": "",
        "fix": "",
        "data": {"provider": provider, "state": "up", "fallback_active": fallback},
    }


class FakeRunner:
    """Answers the commands record_demo runs; each answer can be replaced per test."""

    def __init__(self, tmp: Path) -> None:
        self.tmp = tmp
        self.calls: List[List[str]] = []
        self.config: Dict[str, Any] = good_config()
        self.preflight: List[Tuple[Dict[str, Any], Dict[str, Any]]] = [(dm_check(), judge_check())]
        self.trafficgen_codes: List[int] = [0, 0]
        self.export_code = 0
        self.check_code = 0
        self.extra_id: Optional[str] = None
        # The export: the simulated tape, relabelled as a real recording.
        self.export = tmp / "export-source"
        shutil.copytree(SIMULATED, self.export)
        manifest = json.loads((self.export / "manifest.json").read_text(encoding="utf-8"))
        manifest["kind"] = "recorded"
        manifest[
            "label"
        ] = "Recorded 10 Oct 2026 · typesafe/jev-1.13 + x · scripted answers · replayed, not live"
        (self.export / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
        ids = [t["trace_id"] for t in manifest["traces"]]
        half = len(ids) // 2
        self.round_ids = [ids[:half], ids[half:]]

    def kind(self, argv: Sequence[str]) -> str:
        if list(argv[:3]) == ["docker", "compose", "version"]:
            return "version"
        if len(argv) > 1 and argv[1].endswith("doctor.py"):
            return "doctor-" + argv[argv.index("--only") + 1]
        if len(argv) > 1 and argv[1].endswith("check_recording.py"):
            return "check"
        if argv[0] == "git":
            return "git"
        if argv[:2] == ["docker", "compose"]:
            rest = list(argv[argv.index(str(record_demo.ROOT / "docker-compose.record.yml")) + 1 :])
            if rest[:1] == ["exec"]:
                return "exec " + rest[3]
            return rest[0]
        return "unknown"

    def __call__(self, argv: Sequence[str], stream: bool) -> Tuple[int, str, str]:
        argv = list(argv)
        self.calls.append(argv)
        k = self.kind(argv)
        if k == "version":
            return 0, "Docker Compose version v2.39.2", ""
        if k == "config":
            return 0, json.dumps(self.config), ""
        if k in ("down", "up"):
            return 0, "", ""
        if k == "port":
            return 0, "127.0.0.1:49999\n", ""
        if k == "doctor-decision_model,judge":
            dm, judge = self.preflight.pop(0) if len(self.preflight) > 1 else self.preflight[0]
            return 0 if dm["status"] != "FAIL" else 1, doctor_report([dm, judge]), ""
        if k == "doctor-budgets":
            data = {"decision_spent_today_usd": 0.0412, "llm_spent_today_usd": 0.5531}
            check = {"id": "budgets.today", "status": "PASS", "summary": "", "data": data}
            return 0, doctor_report([check]), ""
        if k == "exec date":
            return 0, SINCE + "\n", ""
        if k == "exec /trafficgen":
            return self.trafficgen_codes.pop(0), "", ""
        if k == "cp":
            src, dst = argv[-2], Path(argv[-1])
            if "ids" in src:
                n = int(src.rsplit("-", 1)[1].split(".")[0])
                ids = list(self.round_ids[n - 1])
                if n == 2 and self.extra_id:
                    ids.append(self.extra_id)
                dst.write_text("\n".join(ids) + "\n", encoding="utf-8")
            else:
                shutil.copytree(self.export, dst)
            return 0, "", ""
        if k == "git":
            return 0, "abcdef123456\n", ""
        if k == "exec /export-replay":
            return self.export_code, "", "export-replay: no traces" if self.export_code else ""
        if k == "check":
            return self.check_code, "FAIL manifest.json: kind mock" if self.check_code else "ok", ""
        raise AssertionError(f"unexpected command: {argv}")

    def kinds(self) -> List[str]:
        return [self.kind(c) for c in self.calls]

    def argv_of(self, kind: str) -> List[str]:
        return next(c for c in self.calls if self.kind(c) == kind)


class RecordDemoTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp(prefix="record-demo-"))
        self.env_file = self.tmp / ".env"
        self.env_file.write_text("", encoding="utf-8")
        self.dest = self.tmp / "demo-data" / "recorded"
        self.dest.parent.mkdir()
        self.runner = FakeRunner(self.tmp)
        self.out = io.StringIO()
        self.err = io.StringIO()
        self.slept: List[float] = []
        self.now = [0.0]

    def tearDown(self) -> None:
        shutil.rmtree(self.tmp, ignore_errors=True)

    def recorder(self, environ: Optional[Dict[str, str]] = None) -> record_demo.Recorder:
        def sleep(seconds: float) -> None:
            self.slept.append(seconds)
            self.now[0] += seconds

        env = {"CONFIRM": "1", "OPENROUTER_API_KEY": FAKE_KEY, "TRAFFICGEN_API_KEY": PLAIN_SECRET}
        return record_demo.Recorder(
            run=self.runner,
            environ=env if environ is None else environ,
            env_file=self.env_file,
            dest=self.dest,
            out=self.out,
            err=self.err,
            sleep=sleep,
            clock=lambda: self.now[0],
            python="python3",
        )

    def main(self, argv: Sequence[str] = (), environ: Optional[Dict[str, str]] = None) -> int:
        return record_demo.main(list(argv), self.recorder(environ))

    def output(self) -> str:
        return self.out.getvalue() + self.err.getvalue()

    def assert_no_key_printed(self) -> None:
        self.assertNotIn(FAKE_KEY, self.output())
        self.assertNotIn(PLAIN_SECRET, self.output())

    def assert_removed_at_the_end(self) -> None:
        self.assertEqual(self.runner.kinds()[-1], "down")
        self.assertIn("-v", self.runner.calls[-1])

    # -------------------------------------------------------------------------- the plan

    def test_the_dry_run_prints_the_plan_and_starts_nothing(self) -> None:
        self.assertEqual(self.main(["--dry-run"]), 0)
        self.assertEqual(self.runner.calls, [])
        plan = record_demo.load_plan()
        text = self.output()
        self.assertIn(f"{len(plan.scenarios)} scenarios, {2 * len(plan.scenarios)} requests", text)
        self.assertIn("--scenarios demo --exclude knowledge --upstream generated", text)
        self.assertIn("about $1.00 (estimate", text)
        self.assertIn("fast checks $0.50/day, deep audits $3/day (hard)", text)
        self.assertIn("fast check: OPENROUTER_API_KEY set", text)
        self.assertIn("Dry run: nothing started, nothing spent", text)
        self.assert_no_key_printed()

    def test_the_plan_reads_the_scenario_pack(self) -> None:
        plan = record_demo.load_plan()
        doc = json.loads(record_demo.PACK.read_text(encoding="utf-8"))
        demo = [s["id"] for s in doc["scenarios"] if "demo" in s["sets"]]
        knowledge = {s["id"] for s in doc["scenarios"] if "knowledge" in s["sets"]}
        self.assertEqual(plan.scenarios, [i for i in demo if i not in knowledge])
        self.assertTrue(plan.generated)
        self.assertTrue(set(plan.generated) <= set(plan.scenarios))
        self.assertEqual(plan.requests, record_demo.ROUNDS * len(plan.scenarios))

    def test_without_confirm_it_is_a_dry_run(self) -> None:
        env = {"OPENROUTER_API_KEY": FAKE_KEY, "CONFIRM": "yes"}
        self.assertEqual(self.main([], env), 0)
        self.assertEqual(self.runner.calls, [])
        self.assertIn("To record: make record-demo CONFIRM=1", self.output())
        # --dry-run wins over CONFIRM=1.
        self.assertEqual(self.main(["--dry-run"]), 0)
        self.assertEqual(self.runner.calls, [])

    # -------------------------------------------------------------------------- refusals

    def test_it_refuses_without_a_key(self) -> None:
        self.assertEqual(self.main([], {"CONFIRM": "1"}), 2)
        self.assertEqual(self.runner.calls, [])
        self.assertIn("refused: no decision-model key: set OPENROUTER_API_KEY", self.output())
        self.assertIn("no judge key for LLM_PROVIDER=auto", self.output())

    def test_a_key_in_dot_env_counts_and_is_never_printed(self) -> None:
        self.env_file.write_text(f"OPENROUTER_API_KEY={FAKE_KEY}\n", encoding="utf-8")
        self.assertEqual(self.main(["--dry-run"], {"CONFIRM": "1"}), 0)
        self.assertIn("OPENROUTER_API_KEY set", self.output())
        self.assertNotIn("Blocked", self.output())
        self.assert_no_key_printed()

    def test_it_refuses_the_mock_or_off_in_dot_env(self) -> None:
        for name, value in (
            ("TRUTHTABLE_JEV_BACKEND", "mock"),
            ("TRUTHTABLE_JEV_BACKEND", "off"),
            ("LLM_PROVIDER", "mock"),
        ):
            with self.subTest(name=name, value=value):
                self.err = io.StringIO()
                env = {"CONFIRM": "1", "OPENROUTER_API_KEY": FAKE_KEY, name: value}
                self.assertEqual(self.main([], env), 2)
                self.assertIn(f"{name}={value}", self.err.getvalue())
        self.assertEqual(self.runner.calls, [])

    def test_it_refuses_a_stack_without_its_caps(self) -> None:
        del self.runner.config["services"]["proxy"]["environment"][
            "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY"
        ]
        self.runner.config["services"]["postgres"]["ports"] = [{"published": "5432"}]
        self.assertEqual(self.main(), 2)
        self.assertNotIn("up", self.runner.kinds())
        self.assertIn("TRUTHTABLE_SPEND_CAP_LLM_USD_DAY must be 3", self.output())
        self.assertIn("postgres publishes a port", self.output())

    def test_the_caps_are_in_the_overlay(self) -> None:
        overlay = (ROOT / "docker-compose.record.yml").read_text(encoding="utf-8")
        for key, value in {**record_demo.PROXY_ENV, **record_demo.ENGINE_ENV}.items():
            self.assertRegex(overlay, rf'(?m)^  +{key}: "?{re.escape(value)}"?$', key)
        self.assertIn("      LLM_FALLBACK: none\n", overlay)
        self.assertIn("    ports: !reset []\n", overlay.split("\n  postgres:\n", 1)[1])
        self.assertIn('      - "127.0.0.1::8080"\n', overlay)
        self.assertNotIn("mock-upstream:", overlay)
        self.assertEqual(record_demo.verify_stack(good_config()), [])

    def test_it_refuses_a_mock_preflight(self) -> None:
        self.runner.preflight = [(dm_check("WARN", "mock", "mock"), judge_check())]
        self.assertEqual(self.main(), 1)
        self.assertIn("preflight: the fast check is mock", self.output())
        self.assertNotIn("exec /trafficgen", self.runner.kinds())
        self.assert_removed_at_the_end()
        self.assertFalse(self.dest.exists())

    def test_it_refuses_a_mock_or_fallback_judge(self) -> None:
        for judge, needle in (
            (judge_check("WARN", "mock"), "the judge is mock"),
            (judge_check("WARN", "openrouter", fallback=True), "running its fallback"),
        ):
            with self.subTest(needle=needle):
                self.runner.calls.clear()
                self.err = io.StringIO()
                self.runner.preflight = [(dm_check(), judge)]
                self.assertEqual(self.main(), 1)
                self.assertIn(needle, self.err.getvalue())
                self.assertNotIn("exec /trafficgen", self.runner.kinds())
                self.assert_removed_at_the_end()

    def test_the_preflight_waits_while_the_fast_check_is_being_checked(self) -> None:
        self.runner.preflight = [
            (dm_check("WARN", state="checking"), judge_check()),
            (dm_check(), judge_check()),
        ]
        self.assertEqual(self.main(), 0)
        self.assertEqual(self.slept, [record_demo.PREFLIGHT_INTERVAL_S])

    def test_the_preflight_gives_up(self) -> None:
        self.runner.preflight = [(dm_check("WARN", state="degraded"), judge_check())]
        self.assertEqual(self.main(), 1)
        self.assertIn("did not pass within 120s", self.output())
        self.assertIn("decision_model.state WARN", self.output())
        self.assertNotIn("exec /trafficgen", self.runner.kinds())

    def test_a_failing_preflight_does_not_wait(self) -> None:
        failing = dm_check("FAIL", state="no_credits")
        failing["fix"] = "add credits at openrouter.ai/settings/credits"
        self.runner.preflight = [(failing, judge_check())]
        self.assertEqual(self.main(), 1)
        self.assertEqual(self.slept, [])
        self.assertIn("decision_model.state FAIL", self.output())
        self.assertIn("fix: add credits", self.output())
        self.assert_removed_at_the_end()

    def test_a_trafficgen_refusal_stops_the_run(self) -> None:
        self.runner.trafficgen_codes = [2]
        self.assertEqual(self.main(), 1)
        self.assertIn("trafficgen refused to record", self.output())
        self.assertNotIn("exec /export-replay", self.runner.kinds())
        self.assert_removed_at_the_end()

    # -------------------------------------------------------------------------- the run

    def test_a_full_run_swaps_the_recording_in(self) -> None:
        self.assertEqual(self.main(), 0)
        self.assertEqual(
            self.runner.kinds(),
            [
                "version",
                "config",
                "down",
                "up",
                "port",
                "doctor-decision_model,judge",
                "exec date",
                "exec /trafficgen",
                "cp",
                "exec /trafficgen",
                "cp",
                "git",
                "exec /export-replay",
                "cp",
                "check",
                "doctor-budgets",
                "down",
            ],
        )
        manifest = json.loads((self.dest / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["kind"], "recorded")
        text = self.output()
        self.assertIn("Label: Recorded 10 Oct 2026", text)
        self.assertIn("Spend today: fast checks $0.0412, deep audits $0.5531 (estimate", text)
        self.assert_no_key_printed()
        # Nothing is left beside the dataset.
        self.assertEqual(sorted(p.name for p in self.dest.parent.iterdir()), ["recorded"])
        up = self.runner.argv_of("up")
        self.assertEqual(up[-5:], ["--wait", "--wait-timeout", "600", "proxy", "engine"])
        self.assertNotIn("--build", up)
        doctor = self.runner.argv_of("doctor-decision_model,judge")
        self.assertIn("http://127.0.0.1:49999", doctor)
        self.assertIn("--no-probe", doctor)
        traffic = self.runner.argv_of("exec /trafficgen")
        self.assertIn("--record", traffic)
        for flag, value in (
            ("--scenarios", "demo"),
            ("--exclude", "knowledge"),
            ("--upstream", "generated"),
        ):
            self.assertEqual(traffic[traffic.index(flag) + 1], value)
        check = self.runner.argv_of("check")
        self.assertIn("--require-recorded", check)

    def test_build_passes_through(self) -> None:
        self.assertEqual(self.main(["--build"]), 0)
        self.assertIn("--build", self.runner.argv_of("up"))

    def test_since_comes_from_the_container_clock_before_the_first_round(self) -> None:
        self.assertEqual(self.main(), 0)
        kinds = self.runner.kinds()
        self.assertLess(kinds.index("exec date"), kinds.index("exec /trafficgen"))
        export = self.runner.argv_of("exec /export-replay")
        self.assertEqual(export[export.index("--since") + 1], SINCE)
        self.assertEqual(export[export.index("--source") + 1], "trafficgen")
        self.assertEqual(export[export.index("--git-sha") + 1], "abcdef123456")

    def test_an_explicit_since_is_used_and_checked(self) -> None:
        self.assertEqual(self.main(["--since", "2026-10-01T00:00:00+02:00"]), 0)
        self.assertNotIn("exec date", self.runner.kinds())
        export = self.runner.argv_of("exec /export-replay")
        self.assertEqual(export[export.index("--since") + 1], "2026-10-01T00:00:00+02:00")
        self.runner.calls.clear()
        self.assertEqual(self.main(["--since", "yesterday"]), 2)
        self.assertEqual(self.runner.calls, [])
        self.assertIn("is not an RFC 3339 time", self.output())

    def test_an_export_failure_propagates(self) -> None:
        self.runner.export_code = 1
        self.assertEqual(self.main(), 1)
        self.assertIn("export-replay failed (exit 1)", self.output())
        self.assertNotIn("check", self.runner.kinds())
        self.assertFalse(self.dest.exists())
        self.assert_removed_at_the_end()
        self.assertEqual(list(self.dest.parent.iterdir()), [])

    def test_the_export_must_hold_exactly_the_answered_traces(self) -> None:
        self.runner.extra_id = "019a0000-0000-7000-8000-000000000000"
        self.assertEqual(self.main(), 1)
        self.assertIn(
            "the export does not match the rounds: 1 answered trace(s) missing", self.output()
        )
        self.assertFalse(self.dest.exists())

    def test_a_refused_export_keeps_the_previous_recording(self) -> None:
        self.dest.mkdir()
        (self.dest / "manifest.json").write_text(
            '{"kind": "recorded", "old": true}', encoding="utf-8"
        )
        self.runner.check_code = 1
        self.assertEqual(self.main(), 1)
        self.assertIn("check_recording.py --require-recorded refused the export", self.output())
        self.assertIn('"old": true', (self.dest / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(sorted(p.name for p in self.dest.parent.iterdir()), ["recorded"])

    def test_a_new_recording_replaces_the_previous_one(self) -> None:
        self.dest.mkdir()
        (self.dest / "stale.json").write_text("{}", encoding="utf-8")
        self.assertEqual(self.main(), 0)
        self.assertFalse((self.dest / "stale.json").exists())
        self.assertTrue((self.dest / "manifest.json").is_file())

    def test_keep_leaves_a_failed_stack_up(self) -> None:
        self.runner.export_code = 1
        self.assertEqual(self.main(["--keep"]), 1)
        self.assertEqual(self.runner.kinds().count("down"), 1)  # the clean start only
        self.assertIn("--keep: the record stack is still up", self.output())


class RecordTargetTest(unittest.TestCase):
    def test_make_record_demo_is_a_dry_run_unless_confirm(self) -> None:
        mk = (ROOT / "mk" / "release.mk").read_text(encoding="utf-8")
        recipe = mk.split("\nrecord-demo:", 1)[1].split("\n\n", 1)[0]
        self.assertIn('CONFIRM="$(CONFIRM)" $(PYTHON) scripts/record_demo.py', recipe)
        self.assertRegex(mk, r"(?m)^RECORD_BUILD\s+\?= \$\(APP_BUILD\)$")
        check = mk.split("\nrecord-check-config:", 1)[1].split("\n\n", 1)[0]
        self.assertIn("go -C backend-go run ./cmd/proxy -check-config", check)
        for kv in ("TRUTHTABLE_TEST_MODE=true", "TRUTHTABLE_STRICT_CONFIG=true"):
            self.assertIn(kv, mk.split("\nRECORD_EXPECT = ", 1)[1].split("\n\n", 1)[0])
        self.assertIn("check: record-check-config", mk)


if __name__ == "__main__":
    unittest.main()
