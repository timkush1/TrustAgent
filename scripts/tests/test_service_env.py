"""Unit tests for scripts/service_env.py (the onboarding stack's -check-config proof, P4 DX-1)."""

from __future__ import annotations

import io
import json
import re
import sys
import unittest
from pathlib import Path
from typing import Any, Dict, Mapping, Optional

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import service_env  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
DX_MK = (ROOT / "mk" / "dx.mk").read_text(encoding="utf-8")

# Fake secrets only (P4 §0 rule 8). UNMATCHED matches no redaction pattern, so only the code's own
# care (never printing a value) keeps it out of the output.
FAKE_KEY = "sk-or-v1-e2e-0123456789abcdef0123456789abcdef"
UNMATCHED = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

# The command the tests run in place of the gateway: exit 0 when the environment is what the
# container would see, otherwise a code naming the first difference.
PROBE = (
    "import os, sys\n"
    "e = os.environ\n"
    "if e.get('TRUTHTABLE_STRICT_CONFIG') != 'true': sys.exit(10)\n"
    "if 'OPENROUTER_API_KEY' in e: sys.exit(11)\n"
    "if 'TRUTHTABLE_SHELL_ONLY' in e: sys.exit(12)\n"
    "if 'TRUTHTABLE_API_KEYS' in e: sys.exit(13)\n"
    "if e.get('TRUTHTABLE_ENV_FILE') != '': sys.exit(14)\n"
    "if e.get('KEEP_ME') != 'kept': sys.exit(15)\n"
    "if e.get('TRUTHTABLE_JEV_BACKEND') != '': sys.exit(16)\n"
    "sys.exit(0)\n"
)


def compose_config(environment: Any, service: str = "proxy") -> str:
    return json.dumps({"name": "x", "services": {service: {"environment": environment}}})


ONBOARDING_LIKE: Dict[str, Optional[str]] = {
    "TRUTHTABLE_STRICT_CONFIG": "true",
    "TRUTHTABLE_JEV_BACKEND": "",
    "TRUTHTABLE_JEV_API_KEY": "",
    "TRUTHTABLE_API_KEYS": None,
}


def run_main(
    args: list,
    stdin: str,
    inherited: Optional[Mapping[str, str]] = None,
) -> tuple:
    err = io.StringIO()
    code = service_env.main(args, stdin=io.StringIO(stdin), stderr=err, inherited=inherited or {})
    return code, err.getvalue()


class RegistryDefaultsTest(unittest.TestCase):
    def test_keys_and_aliases_carry_their_key_default(self) -> None:
        defaults = service_env.registry_defaults()
        self.assertEqual(defaults["TRUTHTABLE_JEV_BACKEND"], "auto")
        self.assertEqual(defaults["TRUTHTABLE_STRICT_CONFIG"], "false")
        self.assertEqual(defaults["TRUTHTABLE_ADMIN_KEYS"], "")
        # An alias has its key's default.
        self.assertEqual(defaults["TRUTHTABLE_ADMIN_KEY"], defaults["TRUTHTABLE_ADMIN_KEYS"])

    def test_every_default_is_a_string(self) -> None:
        for name, value in service_env.registry_defaults().items():
            self.assertIsInstance(value, str, name)


class ServiceEnvironmentTest(unittest.TestCase):
    def test_mapping_form_keeps_null_as_unset(self) -> None:
        config = json.loads(compose_config({"A": "1", "B": None, "C": "", "D": 5}))
        self.assertEqual(
            service_env.service_environment(config, "proxy"),
            {"A": "1", "B": None, "C": "", "D": "5"},
        )

    def test_list_form(self) -> None:
        config = json.loads(compose_config(["A=1", "B", "C=", "D=x=y"]))
        self.assertEqual(
            service_env.service_environment(config, "proxy"),
            {"A": "1", "B": None, "C": "", "D": "x=y"},
        )

    def test_service_without_environment_is_empty(self) -> None:
        config = {"services": {"proxy": {"image": "x"}}}
        self.assertEqual(service_env.service_environment(config, "proxy"), {})

    def test_unknown_service_and_unreadable_environment_are_usage_errors(self) -> None:
        with self.assertRaises(service_env.UsageError):
            service_env.service_environment(json.loads(compose_config({})), "engine")
        with self.assertRaises(service_env.UsageError):
            service_env.service_environment({"services": []}, "proxy")
        with self.assertRaises(service_env.UsageError):
            service_env.service_environment(json.loads(compose_config("A=1")), "proxy")


class CommandEnvironmentTest(unittest.TestCase):
    REGISTERED = frozenset({"OPENROUTER_API_KEY", "LOG_LEVEL", "TRUTHTABLE_ADMIN_KEYS"})

    def test_the_shell_cannot_add_a_setting_compose_does_not_pass(self) -> None:
        inherited = {
            "PATH": "/usr/bin",
            "HOME": "/home/x",
            "OPENROUTER_API_KEY": FAKE_KEY,  # registered: only compose may set it
            "TRUTHTABLE_SHELL_ONLY": UNMATCHED,  # any TRUTHTABLE_* name
            "TRUTHTABLE_ENV_FILE": "/home/x/.env",
            "SERVICE_DECLARED": "from-shell",  # declared by the service (null): stays unset
        }
        env = service_env.command_environment(
            inherited,
            {"TRUTHTABLE_ADMIN_KEYS": "", "SERVICE_DECLARED": None, "LOG_LEVEL": "debug"},
            self.REGISTERED,
        )
        self.assertEqual(
            env,
            {
                "PATH": "/usr/bin",
                "HOME": "/home/x",
                "TRUTHTABLE_ADMIN_KEYS": "",
                "LOG_LEVEL": "debug",
                "TRUTHTABLE_ENV_FILE": "",
            },
        )
        self.assertNotIn(FAKE_KEY, env.values())
        self.assertNotIn(UNMATCHED, env.values())

    def test_compose_value_wins_over_the_shell(self) -> None:
        env = service_env.command_environment(
            {"LOG_LEVEL": "info"}, {"LOG_LEVEL": "debug"}, self.REGISTERED
        )
        self.assertEqual(env["LOG_LEVEL"], "debug")

    def test_the_env_file_is_always_disabled(self) -> None:
        # Even when the service itself names a dotenv file: the container has none.
        env = service_env.command_environment({}, {"TRUTHTABLE_ENV_FILE": "/app/.env"}, frozenset())
        self.assertEqual(env["TRUTHTABLE_ENV_FILE"], "")


class ParseArgsTest(unittest.TestCase):
    def test_service_expectations_and_command(self) -> None:
        inv = service_env.parse_args(
            [
                "proxy",
                "--expect",
                "TRUTHTABLE_STRICT_CONFIG=true",
                "--expect-default",
                "TRUTHTABLE_JEV_BACKEND",
                "--expect",
                "A=x=y",
                "--",
                "go",
                "--",
                "-check-config",
            ]
        )
        self.assertEqual(inv.service, "proxy")
        self.assertEqual(inv.expect, {"TRUTHTABLE_STRICT_CONFIG": "true", "A": "x=y"})
        self.assertEqual(inv.expect_default, ["TRUTHTABLE_JEV_BACKEND"])
        # Only the first -- separates: the command keeps its own.
        self.assertEqual(inv.command, ["go", "--", "-check-config"])

    def test_expect_with_an_empty_value(self) -> None:
        inv = service_env.parse_args(["proxy", "--expect", "A=", "--", "true"])
        self.assertEqual(inv.expect, {"A": ""})

    def test_usage_errors(self) -> None:
        for argv in (
            [],
            ["proxy"],
            ["proxy", "--"],
            ["--", "true"],
            ["", "--", "true"],
            ["--expect", "--", "true"],
            ["proxy", "true"],
            ["proxy", "--expect", "--", "true"],
            ["proxy", "--expect", "NOEQUALS", "--", "true"],
            ["proxy", "--expect", "=v", "--", "true"],
            ["proxy", "--expect-default", "A=b", "--", "true"],
            ["proxy", "--unknown", "A", "--", "true"],
            ["proxy", "extra", "--", "true"],
        ):
            with self.subTest(argv=argv), self.assertRaises(service_env.UsageError):
                service_env.parse_args(argv)


class UnmetExpectationsTest(unittest.TestCase):
    DEFAULTS = {"TRUTHTABLE_JEV_BACKEND": "auto", "TRUTHTABLE_JEV_API_KEY": ""}

    def test_met(self) -> None:
        env = {"S": "true", "TRUTHTABLE_JEV_BACKEND": "auto", "TRUTHTABLE_JEV_API_KEY": None}
        self.assertEqual(
            service_env.unmet_expectations(
                env,
                {"S": "true"},
                ["TRUTHTABLE_JEV_BACKEND", "TRUTHTABLE_JEV_API_KEY", "ABSENT"],
                self.DEFAULTS,
            ),
            [],
        )
        # Empty is the default too: the program falls back to it.
        self.assertEqual(
            service_env.unmet_expectations(
                {"TRUTHTABLE_JEV_BACKEND": ""}, {}, ["TRUTHTABLE_JEV_BACKEND"], self.DEFAULTS
            ),
            [],
        )

    def test_unmet_lines_name_the_variable_never_the_value(self) -> None:
        env = {
            "S": "false",
            "TRUTHTABLE_JEV_BACKEND": "typesafe",
            "TRUTHTABLE_JEV_API_KEY": FAKE_KEY,
            "UNREGISTERED": UNMATCHED,
        }
        problems = service_env.unmet_expectations(
            env,
            {"S": "true", "MISSING": "1"},
            ["TRUTHTABLE_JEV_BACKEND", "TRUTHTABLE_JEV_API_KEY", "UNREGISTERED"],
            self.DEFAULTS,
        )
        self.assertEqual(len(problems), 5)
        for name in (
            "S",
            "MISSING",
            "TRUTHTABLE_JEV_BACKEND",
            "TRUTHTABLE_JEV_API_KEY",
            "UNREGISTERED",
        ):
            self.assertTrue(any(line.startswith(name + " ") for line in problems), name)
        text = "\n".join(problems)
        for value in (FAKE_KEY, UNMATCHED, "typesafe", "false"):
            self.assertNotIn(value, text)


class MainTest(unittest.TestCase):
    def test_runs_the_command_with_the_service_environment(self) -> None:
        stdin = compose_config({**ONBOARDING_LIKE, "OPENROUTER_API_KEY": None})
        code, err = run_main(
            [
                "proxy",
                "--expect",
                "TRUTHTABLE_STRICT_CONFIG=true",
                "--expect-default",
                "TRUTHTABLE_JEV_BACKEND",
                "--",
                sys.executable,
                "-c",
                PROBE,
            ],
            stdin,
            inherited={
                "KEEP_ME": "kept",
                "OPENROUTER_API_KEY": FAKE_KEY,
                "TRUTHTABLE_SHELL_ONLY": UNMATCHED,
                "TRUTHTABLE_STRICT_CONFIG": "false",
            },
        )
        self.assertEqual(code, 0, err)
        # 3 of the 5 names carry a value (OPENROUTER_API_KEY and TRUTHTABLE_API_KEYS are null).
        self.assertRegex(err, r"proxy service's environment from compose \(3 variables")
        self.assertNotIn(FAKE_KEY, err)
        self.assertNotIn(UNMATCHED, err)

    def test_the_command_exit_code_is_returned(self) -> None:
        code, _ = run_main(
            ["proxy", "--", sys.executable, "-c", "import sys; sys.exit(2)"],
            compose_config({}),
        )
        self.assertEqual(code, 2)
        code, _ = run_main(
            ["proxy", "--", sys.executable, "-c", "import sys; sys.exit(7)"],
            compose_config({}),
        )
        self.assertEqual(code, 7)

    def test_an_unmet_expectation_stops_before_the_command(self) -> None:
        marker = ROOT / "scripts" / "tests" / ".service-env-should-not-exist"
        self.addCleanup(lambda: marker.unlink(missing_ok=True))
        code, err = run_main(
            [
                "proxy",
                "--expect-default",
                "TRUTHTABLE_JEV_API_KEY",
                "--",
                sys.executable,
                "-c",
                f"open({str(marker)!r}, 'w').close()",
            ],
            compose_config({**ONBOARDING_LIKE, "TRUTHTABLE_JEV_API_KEY": UNMATCHED}),
        )
        self.assertEqual(code, 1)
        self.assertFalse(marker.exists())
        self.assertIn("proxy: TRUTHTABLE_JEV_API_KEY is set to a value other than its default", err)
        self.assertIn("did not run", err)
        self.assertNotIn(UNMATCHED, err)

    def test_input_and_usage_errors_exit_2(self) -> None:
        for args, stdin, message in (
            (["proxy", "--", "true"], "not json", "stdin is not JSON"),
            (["proxy", "--", "true"], "[]", "not a compose configuration"),
            (["engine", "--", "true"], compose_config({}), "no service 'engine'"),
            (["proxy"], compose_config({}), "usage:"),
        ):
            with self.subTest(message=message):
                code, err = run_main(args, stdin)
                self.assertEqual(code, 2)
                self.assertIn(message, err)

    def test_a_missing_command_exits_2(self) -> None:
        code, err = run_main(["proxy", "--", "trustagent-no-such-command-x"], compose_config({}))
        self.assertEqual(code, 2)
        self.assertIn("command not found", err)


class OnboardingTargetTest(unittest.TestCase):
    """make onboarding-check-config runs the proof against the real onboarding stack (D70)."""

    def recipe(self) -> str:
        return DX_MK.split("\nonboarding-check-config:", 1)[1].split("\n\n", 1)[0]

    def test_it_runs_check_config_with_the_proxy_environment_of_the_onboarding_stack(self) -> None:
        recipe = self.recipe()
        self.assertIn(
            "$(ONBOARDING_CONFIG_ENV) $(ONBOARDING_COMPOSE) --env-file /dev/null config --format json",
            recipe,
        )
        self.assertIn("scripts/service_env.py proxy $(ONBOARDING_EXPECT) --", recipe)
        self.assertIn("./cmd/proxy -check-config", recipe)
        self.assertRegex(DX_MK, r"(?m)^check: onboarding-check-config$")

    def test_compose_resolves_the_committed_files_only(self) -> None:
        # A developer's .env (configure.py writes the trafficgen key into TRUTHTABLE_API_KEYS) and
        # the shell must not reach `docker compose config`: --env-file /dev/null replaces .env, and
        # env -i keeps only what the docker CLI needs to run.
        line = re.search(r"(?m)^ONBOARDING_CONFIG_ENV = (.*)$", DX_MK)
        assert line is not None
        value = line.group(1)
        self.assertTrue(value.startswith("env -i "), value)
        self.assertEqual(set(re.findall(r"\b([A-Z_]+)=", value)), {"PATH", "HOME", "DOCKER_CONFIG"})

    def test_it_requires_strict_config_backend_auto_and_no_key(self) -> None:
        expect = DX_MK.split("\nONBOARDING_EXPECT = ", 1)[1].split("\n#", 1)[0]
        self.assertIn("--expect TRUTHTABLE_STRICT_CONFIG=true", expect)
        names = set(
            re.findall(r"\b(?:TRUTHTABLE_[A-Z_]+|OPENROUTER_API_KEY|TYPESAFE_API_KEY)\b", expect)
        )
        for name in (
            "TRUTHTABLE_JEV_BACKEND",
            "TRUTHTABLE_JEV_API_KEY",
            "TRUTHTABLE_JEV_API_KEY_FILE",
            "OPENROUTER_API_KEY",
            "TYPESAFE_API_KEY",
            "TRUTHTABLE_ADMIN_KEYS",
            "TRUTHTABLE_ADMIN_KEY",
            "TRUTHTABLE_API_KEYS",
            "TRUTHTABLE_READ_KEYS",
            "TRUTHTABLE_SETUP_TOKEN",
        ):
            self.assertIn(name, names)
        self.assertIn("--expect-default $(k)", expect)


if __name__ == "__main__":
    unittest.main()
