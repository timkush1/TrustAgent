"""
Engine configuration (SPEC sections 2.7 and 3.2): registry-named keys and
aliases, Go durations, dotenv files, extra="ignore", invalid values as
warnings, unknown-key hints, strict mode and --dump-spec.
"""

import json
import os
import subprocess
import sys
from datetime import timedelta
from pathlib import Path
from typing import Any, Dict, List

import pytest

from truthtable import config
from truthtable.config import (
    Settings,
    closest,
    dump_spec,
    env_files,
    env_names_by_field,
    format_go_duration,
    load_settings,
    parse_cutoffs,
    parse_go_duration,
    registry_keys,
    unknown_key_warnings,
)

BACKEND_DIR = Path(__file__).resolve().parents[2]

REGISTRY: List[Dict[str, Any]] = registry_keys() or []
ENGINE_KEYS = {entry["name"]: entry for entry in REGISTRY if "engine" in entry["services"]}


@pytest.fixture(autouse=True)
def clean_environment(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    """No inherited engine keys and no dotenv file unless a test writes one."""
    names = {name for names in env_names_by_field().values() for name in names}
    for name in list(os.environ):
        if name in names or name.startswith("TRUTHTABLE_") or name.startswith("LLM_"):
            monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    monkeypatch.chdir(tmp_path)


def settings(**values: Any) -> Settings:
    return Settings(_env_file=None, **values)


# ---------------------------------------------------------------------------
# The registry is the source of truth
# ---------------------------------------------------------------------------


def _registry_default(field_name: str, raw: str) -> Any:
    annotation = Settings.model_fields[field_name].annotation
    if annotation is bool:
        return raw == "true"
    if annotation is timedelta:
        return parse_go_duration(raw)
    return Settings.model_validate({field_name: raw}).__getattribute__(field_name)


def test_every_engine_registry_key_is_a_setting() -> None:
    assert ENGINE_KEYS, "contracts/config/keys.v1.json not found"
    by_env = {names[0]: field for field, names in env_names_by_field().items()}

    assert set(by_env) == set(ENGINE_KEYS)
    for name, entry in ENGINE_KEYS.items():
        field_name = by_env[name]
        assert env_names_by_field()[field_name][1:] == entry["aliases"], name
        default = Settings.model_fields[field_name].get_default(call_default_factory=True)
        if hasattr(default, "get_secret_value"):
            assert entry["secret"], name
            assert default.get_secret_value() == entry["default"], name
            continue
        assert not entry["secret"], name
        assert default == _registry_default(field_name, entry["default"]), name


def test_defaults() -> None:
    s = settings()
    assert s.grpc_host == "127.0.0.1"
    assert (s.grpc_port, s.metrics_port) == (50051, 8001)
    assert (s.engine_max_rpcs, s.engine_max_audits) == (64, 16)
    assert s.llm_provider == "auto" and s.llm_fallback == "none"
    assert s.knowledge_enabled is False and s.knowledge_configured is False
    assert s.grade_cutoffs == (0.9, 0.7, 0.5, 0.3)
    assert s.knowledge_deadline == timedelta(milliseconds=150)
    assert s.effective_log_level == "INFO"


def test_registry_names_and_aliases_are_read(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("METRICS_PORT", "9101")
    monkeypatch.setenv("GRPC_HOST", "0.0.0.0")
    monkeypatch.setenv("RAG_ENABLED", "true")
    monkeypatch.setenv("VERIFIER_MODE", "llm")
    monkeypatch.setenv("TRUTHTABLE_ENGINE_MAX_AUDITS", "4")
    monkeypatch.setenv("LLM_PROVIDER", "OpenRouter")

    s = settings()

    assert s.metrics_port == 9101  # D15: METRICS_PORT is honoured
    assert s.grpc_host == "0.0.0.0"
    assert s.knowledge_enabled is True
    assert s.verifier == "llm"
    assert s.engine_max_audits == 4
    assert s.llm_provider == "openrouter"


def test_canonical_name_wins_over_an_alias(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("RAG_ENABLED", "true")
    monkeypatch.setenv("TRUTHTABLE_KNOWLEDGE_ENABLED", "false")
    assert settings().knowledge_enabled is False


def test_field_names_work_for_python_callers() -> None:
    assert settings(llm_provider="mock").llm_provider == "mock"


def test_unknown_variables_are_ignored(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SOMETHING_ELSE", "x")
    monkeypatch.setenv("REDIS_URL", "redis://r:6379")  # a proxy key
    settings()  # extra="ignore": no error


def test_secrets_are_masked() -> None:
    s = settings(anthropic_api_key="sk-ant-secret")
    assert "sk-ant-secret" not in repr(s)
    assert s.anthropic_api_key.get_secret_value() == "sk-ant-secret"


def test_log_level_precedence(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("TRUTHTABLE_LOG_LEVEL", "warn")
    assert settings().effective_log_level == "WARNING"
    monkeypatch.setenv("LOG_LEVEL", "debug")
    assert settings().effective_log_level == "DEBUG"


# ---------------------------------------------------------------------------
# Go durations
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "text, seconds",
    [
        ("0", 0.0),
        ("150ms", 0.15),
        ("1.5s", 1.5),
        ("2h45m", 9900.0),
        ("1m30s", 90.0),
        ("-1m", -60.0),
        ("+2s", 2.0),
        ("300us", 0.0003),
        ("300µs", 0.0003),
        ("5000ns", 0.000005),
        (".5s", 0.5),
        ("2160h", 7_776_000.0),
    ],
)
def test_parse_go_duration(text: str, seconds: float) -> None:
    assert parse_go_duration(text).total_seconds() == pytest.approx(seconds)


@pytest.mark.parametrize("text", ["", "5", "1.5", "abc", "1d", "5 s", "1s2", "ms", "-"])
def test_invalid_go_durations(text: str) -> None:
    with pytest.raises(ValueError, match="not a duration"):
        parse_go_duration(text)


@pytest.mark.parametrize(
    "value, text",
    [
        (timedelta(0), "0"),
        (timedelta(milliseconds=150), "150ms"),
        (timedelta(seconds=1.5), "1.5s"),
        (timedelta(minutes=2, seconds=3), "2m3s"),
        (timedelta(hours=2160), "2160h"),
        (timedelta(seconds=-2), "-2s"),
    ],
)
def test_format_go_duration_round_trips(value: timedelta, text: str) -> None:
    assert format_go_duration(value) == text
    assert parse_go_duration(text) == value


def test_duration_setting_uses_go_syntax(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("TRUTHTABLE_KNOWLEDGE_DEADLINE", "1.2s")
    assert settings().knowledge_deadline == timedelta(seconds=1.2)


# ---------------------------------------------------------------------------
# Grade cutoffs
# ---------------------------------------------------------------------------


def test_parse_cutoffs() -> None:
    assert parse_cutoffs("0.95, 0.8,0.6,0.4") == (0.95, 0.8, 0.6, 0.4)


@pytest.mark.parametrize("text", ["0.9,0.7,0.5", "0.9,0.7,0.7,0.3", "1.2,0.7,0.5,0.3", "a,b,c,d"])
def test_invalid_cutoffs(text: str) -> None:
    with pytest.raises(ValueError):
        parse_cutoffs(text)


# ---------------------------------------------------------------------------
# Dotenv files
# ---------------------------------------------------------------------------


def test_env_files_default_to_repo_root_then_cwd(tmp_path: Path) -> None:
    files = env_files({})
    root = config.repo_root()
    assert root is not None and (root / "VERSION").is_file()
    assert files[0] == root / ".env"
    assert files[-1] == Path(".env")


def test_env_file_override_and_disable(tmp_path: Path) -> None:
    assert env_files({"TRUTHTABLE_ENV_FILE": str(tmp_path / "x.env")}) == (tmp_path / "x.env",)
    assert env_files({"TRUTHTABLE_ENV_FILE": ""}) == ()
    assert env_files({"TRUTHTABLE_ENV_FILE": "auto"}) == env_files({})


def test_repo_root_is_found_from_backend_python() -> None:
    assert config.repo_root(BACKEND_DIR) == BACKEND_DIR.parent


def test_dotenv_values_load_and_process_env_wins(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    dotenv = tmp_path / "engine.env"
    dotenv.write_text("GRPC_PORT=6000\nMETRICS_PORT=7000\n", encoding="utf-8")
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", str(dotenv))
    monkeypatch.setenv("METRICS_PORT", "7100")

    s = Settings()

    assert s.grpc_port == 6000
    assert s.metrics_port == 7100


# ---------------------------------------------------------------------------
# Warnings and strict mode
# ---------------------------------------------------------------------------


def test_invalid_values_warn_and_keep_the_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GRPC_PORT", "not-a-port")
    monkeypatch.setenv("TRUTHTABLE_KNOWLEDGE_DEADLINE", "soon")
    monkeypatch.setenv("LLM_PROVIDER", "gpt")
    monkeypatch.setenv("TRUTHTABLE_SCORING_GRADE_CUTOFFS", "1,2,3")
    monkeypatch.setenv("TRUTHTABLE_ENGINE_MAX_AUDITS", "0")

    loaded = load_settings(files=())

    s = loaded.settings
    assert s.grpc_port == 50051
    assert s.knowledge_deadline == timedelta(milliseconds=150)
    assert s.llm_provider == "auto"
    assert s.grade_cutoffs == (0.9, 0.7, 0.5, 0.3)
    assert s.engine_max_audits == 16
    keys = sorted(w.key for w in loaded.warnings)
    assert keys == [
        "GRPC_PORT",
        "LLM_PROVIDER",
        "TRUTHTABLE_ENGINE_MAX_AUDITS",
        "TRUTHTABLE_KNOWLEDGE_DEADLINE",
        "TRUTHTABLE_SCORING_GRADE_CUTOFFS",
    ]
    port = next(w for w in loaded.warnings if w.key == "GRPC_PORT")
    assert "'not-a-port'" in port.message and port.hint == "using default '50051'"
    assert not loaded.strict_failure


def test_unknown_truthtable_keys_get_a_suggestion(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("TRUTHTABLE_JEV_MODLE", "x")
    monkeypatch.setenv("TRUTHTABLE_TOTALLY_NEW", "x")
    monkeypatch.setenv("LLM_MODLE", "x")
    monkeypatch.setenv("TRUTHTABLE_WORKER_COUNT", "4")  # a proxy key: known

    warnings = {w.key: w for w in load_settings(files=()).warnings}

    assert warnings["TRUTHTABLE_JEV_MODLE"].hint == "did you mean TRUTHTABLE_JEV_MODEL?"
    assert warnings["TRUTHTABLE_TOTALLY_NEW"].hint == "not in the registry"
    assert warnings["LLM_MODLE"].hint == "did you mean LLM_MODEL?"
    assert "TRUTHTABLE_WORKER_COUNT" not in warnings


def test_unprefixed_names_are_only_flagged_as_typos() -> None:
    warnings = unknown_key_warnings(["LANGSMITH_ENDPOINT", "PATH", "GRPC_POTR"], REGISTRY)
    assert [w.key for w in warnings] == ["GRPC_POTR"]


def test_unknown_keys_in_dotenv_files_are_reported(tmp_path: Path) -> None:
    dotenv = tmp_path / ".env"
    dotenv.write_text("TRUTHTABLE_GRPC_ADRESS=engine:50051\n", encoding="utf-8")

    warnings = load_settings(files=(dotenv,)).warnings

    assert [(w.key, w.hint) for w in warnings] == [
        ("TRUTHTABLE_GRPC_ADRESS", "did you mean TRUTHTABLE_GRPC_ADDRESS?")
    ]


def test_strict_config_fails_on_any_warning(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("TRUTHTABLE_STRICT_CONFIG", "true")
    assert not load_settings(files=()).strict_failure

    monkeypatch.setenv("TRUTHTABLE_STRCT", "1")
    assert load_settings(files=()).strict_failure


def test_closest() -> None:
    assert closest("GRPC_PROT", ["GRPC_PORT", "GRPC_HOST"]) == "GRPC_PORT"
    assert closest("XYZ", ["GRPC_PORT"]) is None


# ---------------------------------------------------------------------------
# --dump-spec and --check
# ---------------------------------------------------------------------------


def test_dump_spec_lists_every_engine_key() -> None:
    spec = dump_spec()
    assert spec["service"] == "engine"
    names = [entry["name"] for entry in spec["keys"]]
    assert names == sorted(ENGINE_KEYS)
    knowledge = next(e for e in spec["keys"] if e["name"] == "TRUTHTABLE_KNOWLEDGE_ENABLED")
    assert knowledge == {
        "name": "TRUTHTABLE_KNOWLEDGE_ENABLED",
        "aliases": ["KNOWLEDGE_ENABLED", "RAG_ENABLED"],
        "field": "knowledge_enabled",
        "default": "false",
        "secret": False,
    }
    deadline = next(e for e in spec["keys"] if e["name"] == "TRUTHTABLE_KNOWLEDGE_DEADLINE")
    assert deadline["default"] == "150ms"
    secrets = {e["name"] for e in spec["keys"] if e["secret"]}
    assert secrets == {n for n, e in ENGINE_KEYS.items() if e["secret"]}


def test_dump_spec_cli() -> None:
    completed = subprocess.run(
        [sys.executable, "-m", "truthtable.config", "--dump-spec"],
        env={"PYTHONPATH": str(BACKEND_DIR / "src"), "TRUTHTABLE_ENV_FILE": ""},
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert completed.returncode == 0, completed.stderr
    assert json.loads(completed.stdout)["service"] == "engine"


def test_check_cli_exits_2_on_warnings() -> None:
    completed = subprocess.run(
        [sys.executable, "-m", "truthtable.config", "--check"],
        env={
            "PYTHONPATH": str(BACKEND_DIR / "src"),
            "TRUTHTABLE_ENV_FILE": "",
            "TRUTHTABLE_JEV_MODLE": "x",
        },
        capture_output=True,
        text=True,
        timeout=60,
    )
    assert completed.returncode == 2
    assert "did you mean TRUTHTABLE_JEV_MODEL?" in completed.stderr
