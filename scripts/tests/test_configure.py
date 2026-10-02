"""Unit tests for scripts/configure.py --ensure and --with-secrets (standard library only; no Docker)."""

from __future__ import annotations

import contextlib
import io
import json
import os
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Dict, List, Optional, Sequence, Tuple
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import configure  # noqa: E402
from configure import (  # noqa: E402
    SECRET_ATTEMPTS,
    SECRET_PROMPTS,
    EnsureResult,
    ask_secrets,
    ensure,
    parse_key_list,
    postgres_volumes,
    secret_problem,
)
from envfile import EnvFile  # noqa: E402

EXAMPLE = "# TrustAgent settings\n# TRUTHTABLE_API_KEYS=\n# BIND_ADDR=127.0.0.1\n"
REPO = Path(__file__).resolve().parents[2]
# Fake keys only: one in OpenRouter's shape, and one that matches no key pattern
# at all, so an assertion on its absence cannot pass because something masked it.
FAKE_OPENROUTER = "sk-or-v1-e2e-0123456789abcdef0123456789abcdef"
UNMATCHED = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
FAKE_ANTHROPIC = "sk-ant-test-0123456789abcdef"


class FakeReader:
    """Stands in for getpass: hands out scripted answers, records the prompts it was shown."""

    def __init__(self, answers: Sequence[object]) -> None:
        self.answers = list(answers)
        self.prompts: List[str] = []

    def __call__(self, prompt: str) -> str:
        self.prompts.append(prompt)
        answer = self.answers.pop(0) if self.answers else ""
        if isinstance(answer, BaseException):
            raise answer
        return str(answer)


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
        volumes = FakeVolumes(False)
        env, result = self.run_ensure(volumes)
        self.assertTrue(result.created)
        self.assertEqual(
            result.generated,
            ["POSTGRES_PASSWORD", "GRAFANA_ADMIN_PASSWORD", "TRAFFICGEN_API_KEY"],
        )
        self.assertEqual(result.warnings, [])
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

    def test_new_env_next_to_an_old_postgres_volume_warns(self) -> None:
        # A new .env always gets a password (SPEC §3.4), whatever docker says,
        # but a volume created under an earlier .env keeps its own password.
        for answer in (True, None):
            with self.subTest(volume_exists=answer):
                env, result = self.run_ensure(FakeVolumes(answer))
                self.assertIn("POSTGRES_PASSWORD", result.generated)
                self.assertRegex(env.get("POSTGRES_PASSWORD") or "", r"^[0-9a-f]{48}$")
                warned = any("earlier .env" in w for w in result.warnings)
                self.assertEqual(warned, answer is True)

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
        # No Docker in unit tests: main() looks the probe up at call time.
        self.volumes = FakeVolumes(False)
        patcher = mock.patch.object(configure, "docker_volume_exists", self.volumes)
        patcher.start()
        self.addCleanup(patcher.stop)
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

    def test_requires_ensure_or_with_secrets(self) -> None:
        code, _, err = self.call()
        self.assertEqual(code, 2)
        self.assertIn("--ensure", err)
        self.assertIn("--with-secrets", err)

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
        self.assertEqual(len(self.volumes.calls), 1, "one docker volume query")
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        code, out, _ = self.call("--ensure")
        self.assertEqual(code, 0)
        self.assertIn("up to date", out)


class WithSecretsTest(unittest.TestCase):
    """``--with-secrets``: hidden prompts, never a value in any output."""

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.env_path = self.dir / ".env"

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def ask(self, env: EnvFile, answers: Sequence[object]) -> Tuple[FakeReader, str, object]:
        reader = FakeReader(answers)
        out = io.StringIO()
        result = ask_secrets(env, reader, out)
        return reader, out.getvalue(), result

    def test_prompts_are_registry_secrets_of_a_service_that_reads_them(self) -> None:
        registry = json.loads(
            (REPO / "contracts" / "config" / "keys.v1.json").read_text(encoding="utf-8")
        )
        keys = {entry["name"]: entry for entry in registry["keys"]}
        names = [item.key for item in SECRET_PROMPTS]
        self.assertEqual(names[0], "OPENROUTER_API_KEY", "the one key most setups need comes first")
        self.assertEqual(len(names), len(set(names)))
        # Only an explicit or compatible backend reads it: not a headless-setup question.
        self.assertNotIn("TRUTHTABLE_JEV_API_KEY", names)
        for name in names:
            self.assertTrue(keys[name]["secret"], name)
            self.assertTrue({"proxy", "engine"} & set(keys[name]["services"]), name)

    def test_saves_a_new_key_on_its_documented_line_and_never_shows_it(self) -> None:
        env = EnvFile(
            path=self.env_path, lines=["# Decision model", "# OPENROUTER_API_KEY=", "X=1"]
        )
        reader, out, result = self.ask(env, [FAKE_OPENROUTER, "", UNMATCHED, ""])
        self.assertEqual(result.saved, ["OPENROUTER_API_KEY", "ANTHROPIC_API_KEY"])
        self.assertEqual(env.lines[1], f"OPENROUTER_API_KEY={FAKE_OPENROUTER}")
        self.assertEqual(env.get("ANTHROPIC_API_KEY"), UNMATCHED)
        self.assertIn("# --- added by scripts/configure.py --with-secrets", env.render())
        self.assertIsNone(env.get("TYPESAFE_API_KEY"), "Enter skips a key that is not set")
        shown = out + "".join(reader.prompts) + repr(result)
        for secret in (FAKE_OPENROUTER, UNMATCHED):
            self.assertNotIn(secret, shown)
        self.assertEqual(len(reader.prompts), len(SECRET_PROMPTS))

    def test_enter_keeps_a_set_key_and_dash_removes_one(self) -> None:
        env = EnvFile(
            path=self.env_path,
            lines=[f"OPENROUTER_API_KEY={FAKE_OPENROUTER}", f"OPENAI_API_KEY={UNMATCHED}"],
        )
        reader, out, result = self.ask(env, ["", "", "", "-"])
        self.assertEqual(env.get("OPENROUTER_API_KEY"), FAKE_OPENROUTER)
        self.assertEqual(env.get("OPENAI_API_KEY"), "")
        self.assertEqual((result.saved, result.removed), ([], ["OPENAI_API_KEY"]))
        self.assertIn("Enter keeps it", reader.prompts[0])
        self.assertIn("Enter skips", reader.prompts[1])
        for secret in (FAKE_OPENROUTER, UNMATCHED):
            self.assertNotIn(secret, out + "".join(reader.prompts))

    def test_the_same_value_again_is_not_a_change(self) -> None:
        env = EnvFile(path=self.env_path, lines=[f"OPENROUTER_API_KEY={FAKE_OPENROUTER}"])
        _, _, result = self.ask(env, [FAKE_OPENROUTER])
        self.assertFalse(result.changed)

    def test_a_refused_value_is_asked_again_then_left_alone(self) -> None:
        pasted = f"{UNMATCHED} trailing words"
        env = EnvFile(path=self.env_path, lines=[f"OPENROUTER_API_KEY={FAKE_OPENROUTER}"])
        reader, out, result = self.ask(env, [pasted] * SECRET_ATTEMPTS)
        self.assertEqual(env.get("OPENROUTER_API_KEY"), FAKE_OPENROUTER)
        self.assertFalse(result.changed)
        self.assertEqual(out.count("Not saved:"), SECRET_ATTEMPTS)
        self.assertIn("OPENROUTER_API_KEY left unchanged", out)
        self.assertNotIn(UNMATCHED, out + "".join(reader.prompts))
        # A good answer on the second try is saved.
        env = EnvFile(path=self.env_path)
        _, out, result = self.ask(env, ["short", UNMATCHED])
        self.assertEqual(result.saved, ["OPENROUTER_API_KEY"])
        self.assertIn("try again (2 left)", out)

    def test_secret_problem_names_the_reason_not_the_value(self) -> None:
        for good in (FAKE_OPENROUTER, UNMATCHED, "AbC+/x=_-.:~12"):
            self.assertIsNone(secret_problem(good), good)
        for bad in ("short", "a" * 513, f"{UNMATCHED}$x", f'"{UNMATCHED}"', f"{UNMATCHED}\nX=1"):
            problem = secret_problem(bad)
            self.assertIsNotNone(problem)
            self.assertNotIn(UNMATCHED, problem or "")

    def test_an_unexpected_prefix_warns_by_name_and_still_saves(self) -> None:
        env = EnvFile(path=self.env_path)
        _, _, result = self.ask(env, [UNMATCHED])
        self.assertEqual(result.saved, ["OPENROUTER_API_KEY"])
        self.assertEqual(len(result.warnings), 1)
        self.assertIn("sk-or-", result.warnings[0])
        self.assertNotIn(UNMATCHED, result.warnings[0])
        _, _, result = self.ask(EnvFile(path=self.env_path), ["", "", FAKE_ANTHROPIC])
        self.assertEqual(result.warnings, [])


class WithSecretsMainTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        (self.dir / ".env.example").write_text(
            EXAMPLE + "# OPENROUTER_API_KEY=\n", encoding="utf-8"
        )
        for name, value in (
            ("docker_volume_exists", FakeVolumes(False)),
            ("stdin_is_interactive", lambda: True),
        ):
            patcher = mock.patch.object(configure, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.path = self.dir / ".env"
        self.args = ["--env-file", str(self.path), "--example", str(self.dir / ".env.example")]

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def call(self, reader: FakeReader, *extra: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(configure, "read_secret", reader):
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                code = configure.main([*self.args, *extra])
        return code, out.getvalue(), err.getvalue()

    def test_writes_the_keys_to_a_private_file_and_prints_names_only(self) -> None:
        reader = FakeReader([FAKE_OPENROUTER, "", "", UNMATCHED])
        code, out, err = self.call(reader, "--with-secrets")
        self.assertEqual(code, 0, err)
        values = EnvFile.load(self.path).values()
        self.assertEqual(values["OPENROUTER_API_KEY"], FAKE_OPENROUTER)
        self.assertEqual(values["OPENAI_API_KEY"], UNMATCHED)
        self.assertTrue(values["POSTGRES_PASSWORD"], "--with-secrets also does what --ensure does")
        self.assertIn("saved OPENROUTER_API_KEY, OPENAI_API_KEY (values not shown)", out)
        self.assertIn("make doctor", out)
        for secret in (FAKE_OPENROUTER, UNMATCHED, values["POSTGRES_PASSWORD"]):
            self.assertNotIn(secret, out + err + "".join(reader.prompts))
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)

    def test_refuses_without_a_terminal_and_never_reads_a_key(self) -> None:
        reader = FakeReader([FAKE_OPENROUTER])
        with mock.patch.object(configure, "stdin_is_interactive", lambda: False):
            code, _, err = self.call(reader, "--with-secrets")
        self.assertEqual(code, 2)
        self.assertIn("needs a terminal", err)
        self.assertEqual(reader.prompts, [])
        self.assertFalse(self.path.exists())

    def test_cancel_at_a_prompt_writes_nothing(self) -> None:
        for interrupt in (KeyboardInterrupt(), EOFError()):
            reader = FakeReader([FAKE_OPENROUTER, interrupt])
            code, _, err = self.call(reader, "--with-secrets")
            self.assertEqual(code, 130)
            self.assertIn("cancelled", err)
            self.assertFalse(self.path.exists())
            self.assertNotIn(FAKE_OPENROUTER, err)

    def test_dry_run_asks_but_writes_nothing(self) -> None:
        code, out, _ = self.call(FakeReader([FAKE_OPENROUTER]), "--with-secrets", "--dry-run")
        self.assertEqual(code, 0)
        self.assertIn("dry run, nothing written: saved OPENROUTER_API_KEY", out)
        self.assertFalse(self.path.exists())
        self.assertNotIn(FAKE_OPENROUTER, out)

    def test_make_configure_passes_with_secrets_only_when_asked(self) -> None:
        mk = (REPO / "mk" / "dx.mk").read_text(encoding="utf-8")
        recipe = mk.split("\nconfigure:", 1)[1].split("\n\n", 1)[0]
        self.assertIn(
            "scripts/configure.py --ensure$(if $(filter 1,$(WITH_SECRETS)), --with-secrets)",
            recipe,
        )


if __name__ == "__main__":
    unittest.main()
