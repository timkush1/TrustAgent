"""Unit tests for scripts/configure.py --ensure (standard library only; no Docker)."""

from __future__ import annotations

import contextlib
import io
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Dict, List, Optional, Sequence, Tuple

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import configure  # noqa: E402
from configure import EnsureResult, ensure, parse_key_list, postgres_volumes  # noqa: E402
from envfile import EnvFile  # noqa: E402

EXAMPLE = "# TrustAgent settings\n# TRUTHTABLE_API_KEYS=\n# BIND_ADDR=127.0.0.1\n"


class FakeVolumes:
    """Stands in for docker: records the queried names, answers a fixed value."""

    def __init__(self, answer: Optional[bool]) -> None:
        self.answer = answer
        self.calls: List[Sequence[str]] = []

    def __call__(self, names: Sequence[str]) -> Optional[bool]:
        self.calls.append(list(names))
        return self.answer


class EnsureTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.env_path = self.dir / ".env"
        self.example = self.dir / ".env.example"
        self.example.write_text(EXAMPLE, encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def run_ensure(
        self, volumes: Optional[FakeVolumes] = None, environ: Optional[Dict[str, str]] = None
    ) -> Tuple[EnvFile, EnsureResult]:
        env = EnvFile.load(self.env_path)
        result = ensure(env, self.example, volumes or FakeVolumes(False), environ or {})
        return env, result

    def test_new_env_is_created_from_example_with_every_generated_value(self) -> None:
        volumes = FakeVolumes(True)
        env, result = self.run_ensure(volumes)
        self.assertTrue(result.created)
        self.assertEqual(
            result.generated,
            ["POSTGRES_PASSWORD", "GRAFANA_ADMIN_PASSWORD", "TRAFFICGEN_API_KEY"],
        )
        self.assertEqual(volumes.calls, [], "a new .env never asks docker")
        self.assertEqual(env.lines[:3], EXAMPLE.splitlines())
        values = env.values()
        self.assertRegex(values["POSTGRES_PASSWORD"], r"^[0-9a-f]{48}$")
        self.assertRegex(values["GRAFANA_ADMIN_PASSWORD"], r"^[0-9a-f]{32}$")
        self.assertRegex(values["TRAFFICGEN_API_KEY"], r"^[0-9a-f]{64}$")
        self.assertEqual(
            values["TRUTHTABLE_API_KEYS"], "trafficgen=" + values["TRAFFICGEN_API_KEY"]
        )
        self.assertTrue(
            any(line.startswith("# --- added by scripts/configure.py") for line in env.lines)
        )

    def test_second_run_changes_nothing(self) -> None:
        env, _ = self.run_ensure()
        env.write()
        before = self.env_path.read_text(encoding="utf-8")
        env2, result = self.run_ensure()
        self.assertFalse(result.changed)
        self.assertEqual(env2.render(), before)

    def test_existing_values_are_never_overwritten(self) -> None:
        self.env_path.write_text(
            "POSTGRES_PASSWORD=keep-pg\nGRAFANA_ADMIN_PASSWORD=keep-gf\n"
            "TRAFFICGEN_API_KEY=tg\nTRUTHTABLE_API_KEYS=ci=abc,trafficgen=tg\n",
            encoding="utf-8",
        )
        env, result = self.run_ensure()
        self.assertFalse(result.changed)
        self.assertEqual(env.get("POSTGRES_PASSWORD"), "keep-pg")
        self.assertEqual(env.get("TRUTHTABLE_API_KEYS"), "ci=abc,trafficgen=tg")

    def test_empty_values_are_filled_in_place(self) -> None:
        self.env_path.write_text(
            "GRAFANA_ADMIN_PASSWORD=\nTRUTHTABLE_API_KEYS=\n", encoding="utf-8"
        )
        env, result = self.run_ensure(FakeVolumes(False))
        self.assertIn("GRAFANA_ADMIN_PASSWORD", result.generated)
        self.assertTrue(env.lines[0].startswith("GRAFANA_ADMIN_PASSWORD="))
        self.assertNotEqual(env.lines[0], "GRAFANA_ADMIN_PASSWORD=")
        self.assertTrue(env.lines[1].startswith("TRUTHTABLE_API_KEYS=trafficgen="))

    def test_postgres_password_on_an_existing_env_depends_on_the_volume(self) -> None:
        self.env_path.write_text("POSTGRES_PASSWORD=\n", encoding="utf-8")
        volumes = FakeVolumes(False)
        env, result = self.run_ensure(volumes)
        self.assertIn("POSTGRES_PASSWORD", result.generated)
        self.assertEqual(
            volumes.calls, [["truthtable_postgres-data", "truthtable-demo_postgres-data"]]
        )

        env, result = self.run_ensure(FakeVolumes(True))
        self.assertNotIn("POSTGRES_PASSWORD", result.generated)
        self.assertEqual(env.get("POSTGRES_PASSWORD"), "")
        self.assertTrue(any("volume already exists" in w for w in result.warnings))

        env, result = self.run_ensure(FakeVolumes(None))
        self.assertNotIn("POSTGRES_PASSWORD", result.generated)
        self.assertTrue(any("docker could not be asked" in w for w in result.warnings))

    def test_project_name_selects_the_volumes(self) -> None:
        self.env_path.write_text("COMPOSE_PROJECT_NAME=truthtable-go\n", encoding="utf-8")
        volumes = FakeVolumes(False)
        self.run_ensure(volumes)
        self.assertEqual(
            volumes.calls, [["truthtable-go_postgres-data", "truthtable-demo_postgres-data"]]
        )
        volumes = FakeVolumes(False)
        self.run_ensure(volumes, environ={"COMPOSE_PROJECT_NAME": "truthtable-demo"})
        self.assertEqual(volumes.calls, [["truthtable-demo_postgres-data"]])

    def test_trafficgen_entry_is_appended_to_existing_keys(self) -> None:
        self.env_path.write_text(
            "POSTGRES_PASSWORD=p\nTRUTHTABLE_API_KEYS=ci=abc,\n", encoding="utf-8"
        )
        env, result = self.run_ensure()
        key = env.get("TRAFFICGEN_API_KEY")
        self.assertEqual(env.get("TRUTHTABLE_API_KEYS"), f"ci=abc,trafficgen={key}")
        self.assertIn("added the trafficgen entry to TRUTHTABLE_API_KEYS", result.notes)

    def test_trafficgen_key_is_recovered_from_the_key_list(self) -> None:
        self.env_path.write_text(
            "POSTGRES_PASSWORD=p\nTRUTHTABLE_API_KEYS=trafficgen=known\n", encoding="utf-8"
        )
        env, result = self.run_ensure()
        self.assertEqual(env.get("TRAFFICGEN_API_KEY"), "known")
        self.assertEqual(env.get("TRUTHTABLE_API_KEYS"), "trafficgen=known")

    def test_existing_trafficgen_key_is_listed(self) -> None:
        self.env_path.write_text("POSTGRES_PASSWORD=p\nTRAFFICGEN_API_KEY=mine\n", encoding="utf-8")
        env, _ = self.run_ensure()
        self.assertEqual(env.get("TRAFFICGEN_API_KEY"), "mine")
        self.assertEqual(env.get("TRUTHTABLE_API_KEYS"), "trafficgen=mine")

    def test_mismatched_trafficgen_keys_are_reported_not_changed(self) -> None:
        self.env_path.write_text(
            "POSTGRES_PASSWORD=p\nTRAFFICGEN_API_KEY=a\nTRUTHTABLE_API_KEYS=trafficgen=b\n",
            encoding="utf-8",
        )
        env, result = self.run_ensure()
        self.assertEqual(env.get("TRAFFICGEN_API_KEY"), "a")
        self.assertEqual(env.get("TRUTHTABLE_API_KEYS"), "trafficgen=b")
        self.assertTrue(any("differs" in w for w in result.warnings))

    def test_unlabelled_legacy_keys_are_reported(self) -> None:
        self.env_path.write_text(
            "POSTGRES_PASSWORD=p\nTRUTHTABLE_API_KEYS=legacykey\n", encoding="utf-8"
        )
        env, result = self.run_ensure()
        self.assertTrue(str(env.get("TRUTHTABLE_API_KEYS")).startswith("legacykey,trafficgen="))
        self.assertTrue(any("without a label" in w for w in result.warnings))

    def test_parse_key_list(self) -> None:
        self.assertEqual(parse_key_list(""), [])
        self.assertEqual(
            parse_key_list(" ci = a , legacy ,, trafficgen=b"),
            [("ci", "a"), (None, "legacy"), ("trafficgen", "b")],
        )

    def test_postgres_volumes(self) -> None:
        self.assertEqual(
            postgres_volumes("truthtable"),
            ["truthtable_postgres-data", "truthtable-demo_postgres-data"],
        )
        self.assertEqual(postgres_volumes("truthtable-demo"), ["truthtable-demo_postgres-data"])


class MainTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        (self.dir / ".env.example").write_text(EXAMPLE, encoding="utf-8")
        self.args = [
            "--env-file",
            str(self.dir / ".env"),
            "--example",
            str(self.dir / ".env.example"),
        ]

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def call(self, *extra: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = configure.main([*self.args, *extra])
        return code, out.getvalue(), err.getvalue()

    def test_requires_ensure(self) -> None:
        code, _, err = self.call()
        self.assertEqual(code, 2)
        self.assertIn("--ensure", err)

    def test_dry_run_writes_nothing(self) -> None:
        code, out, _ = self.call("--ensure", "--dry-run")
        self.assertEqual(code, 0)
        self.assertIn("dry run", out)
        self.assertFalse((self.dir / ".env").exists())

    def test_ensure_writes_a_private_file_and_never_prints_secrets(self) -> None:
        code, out, err = self.call("--ensure")
        self.assertEqual(code, 0)
        path = self.dir / ".env"
        values = EnvFile.load(path).values()
        for secret in ("POSTGRES_PASSWORD", "GRAFANA_ADMIN_PASSWORD", "TRAFFICGEN_API_KEY"):
            self.assertNotIn(values[secret], out + err)
        self.assertIn("created", out)
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        code, out, _ = self.call("--ensure")
        self.assertEqual(code, 0)
        self.assertIn("up to date", out)


if __name__ == "__main__":
    unittest.main()
