"""
Engine configuration (SPEC sections 2.7 and 3.2).

Every setting is a key of the contracts registry `contracts/config/keys.v1.json`
that lists the engine among its services; the field's environment variable is
the registry name, and registry aliases are accepted (the canonical name wins
when both are set). A unit test holds the fields, names, aliases and defaults
to the registry.

Loading:
- Precedence: built-in default < dotenv files < process environment (the
  dotenv files never override a variable that is already set).
- Dotenv files (`ENV_FILES`): TRUTHTABLE_ENV_FILE when it names a file; none
  when it is set but empty; otherwise `<repo root>/.env` then `./.env` (the
  repo root is the first parent directory holding VERSION; a later file wins,
  missing files are skipped).
- Unknown variables are ignored (`extra="ignore"`).
- `load_settings()` never fails on a bad value: an invalid value becomes a
  ConfigWarning and the key keeps its default. It also warns about unknown
  `TRUTHTABLE_*` keys (not in the registry of any service) and misspelled
  engine keys, with a "did you mean" hint (Levenshtein distance <= 2).
  TRUTHTABLE_STRICT_CONFIG=true turns any warning into a startup failure
  (exit code 2; CI and e2e set it).
- Durations use Go syntax (`800ms`, `1.5s`, `2h45m`, `0`) in both services
  (`GoDuration`).

`python -m truthtable.config --dump-spec` prints the engine's keys (name,
aliases, field, default, secret) as JSON for scripts/gen_config_docs.py.
"""

import argparse
import json
import math
import os
import re
import sys
from dataclasses import dataclass, field
from datetime import timedelta
from pathlib import Path
from typing import (
    Annotated,
    Any,
    Dict,
    Iterable,
    List,
    Literal,
    Mapping,
    Optional,
    Sequence,
    Tuple,
)
from urllib.parse import urlparse

from pydantic import AliasChoices, BeforeValidator, Field, SecretStr, ValidationError
from pydantic.fields import FieldInfo
from pydantic_settings import BaseSettings, SettingsConfigDict

from .contracts import ContractsNotFoundError, load_contract

REGISTRY_CONTRACT = "config/keys.v1.json"
ENGINE_SERVICE = "engine"
ENV_FILE_VAR = "TRUTHTABLE_ENV_FILE"
STRICT_CONFIG_EXIT_CODE = 2

# ---------------------------------------------------------------------------
# Dotenv files
# ---------------------------------------------------------------------------


def repo_root(start: Optional[Path] = None) -> Optional[Path]:
    """The first directory at or above `start` (default: this file) that holds VERSION."""
    origin = (start or Path(__file__)).resolve()
    for directory in (origin, *origin.parents):
        if (directory / "VERSION").is_file():
            return directory
    return None


def env_files(environ: Optional[Mapping[str, str]] = None) -> Tuple[Path, ...]:
    """The dotenv files to load, in order (a later file wins)."""
    env = os.environ if environ is None else environ
    configured = env.get(ENV_FILE_VAR)
    if configured is not None:
        value = configured.strip()
        if not value:
            return ()  # set but empty: no dotenv loading (as in the proxy)
        if value.lower() != "auto":
            return (Path(value).expanduser(),)
    files: List[Path] = []
    root = repo_root()
    if root is not None:
        files.append(root / ".env")
    local = Path(".env")
    if not files or local.resolve() != files[0].resolve():
        files.append(local)
    return tuple(files)


ENV_FILES: Tuple[Path, ...] = env_files()

# ---------------------------------------------------------------------------
# Value types
# ---------------------------------------------------------------------------

_GO_DURATION_UNITS = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,  # U+00B5 micro sign
    "μs": 1e-6,  # U+03BC Greek mu
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}
_GO_DURATION_PART = re.compile(r"(\d+\.?\d*|\.\d+)(ns|us|µs|μs|ms|s|m|h)")


def parse_go_duration(text: str) -> timedelta:
    """
    Parse a Go duration string (time.ParseDuration syntax): a sequence of
    decimal numbers with units ns, us (µs), ms, s, m, h, e.g. "300ms", "1.5h",
    "2h45m", "-1m". "0" needs no unit.

    Raises:
        ValueError: If `text` is not a Go duration.
    """
    raw = text.strip()
    body = raw[1:] if raw[:1] in "+-" else raw
    sign = -1.0 if raw.startswith("-") else 1.0
    if body == "0":
        return timedelta(0)
    if not body:
        raise ValueError(f"{text!r} is not a duration (e.g. 800ms, 1.5s, 2h45m)")
    seconds = 0.0
    position = 0
    while position < len(body):
        part = _GO_DURATION_PART.match(body, position)
        if part is None:
            raise ValueError(f"{text!r} is not a duration (e.g. 800ms, 1.5s, 2h45m)")
        seconds += float(part.group(1)) * _GO_DURATION_UNITS[part.group(2)]
        position = part.end()
    return timedelta(seconds=sign * seconds)


def format_go_duration(value: timedelta) -> str:
    """A Go-syntax string for `value` (the inverse of parse_go_duration, to ms)."""
    total_ms = round(value.total_seconds() * 1000)
    if total_ms == 0:
        return "0"
    sign = "-" if total_ms < 0 else ""
    total_ms = abs(total_ms)
    hours, rest = divmod(total_ms, 3_600_000)
    minutes, rest = divmod(rest, 60_000)
    seconds, millis = divmod(rest, 1000)
    parts = []
    if hours:
        parts.append(f"{hours}h")
    if minutes:
        parts.append(f"{minutes}m")
    if seconds or (millis and (hours or minutes)):
        parts.append(f"{seconds}.{millis:03d}".rstrip("0").rstrip(".") + "s")
    elif millis:
        parts.append(f"{millis}ms")
    return sign + "".join(parts)


def _go_duration(value: Any) -> Any:
    if isinstance(value, str):
        return parse_go_duration(value)
    return value  # a timedelta, or seconds from Python callers


GoDuration = Annotated[timedelta, BeforeValidator(_go_duration)]


def _lower(value: Any) -> Any:
    return value.strip().lower() if isinstance(value, str) else value


def _upper(value: Any) -> Any:
    return value.strip().upper() if isinstance(value, str) else value


def _url(value: Any) -> Any:
    """An absolute http(s) URL, or "" (unset)."""
    if not isinstance(value, str):
        return value
    text = value.strip()
    if not text:
        return ""
    parsed = urlparse(text)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise ValueError(f"{text!r} is not an absolute http(s) URL")
    return text


def parse_cutoffs(value: str) -> Tuple[float, ...]:
    """
    TRUTHTABLE_SCORING_GRADE_CUTOFFS: four comma-separated trust-score cutoffs
    for grades A, B, C and D, strictly decreasing, each in (0, 1].

    Raises:
        ValueError: If the list is malformed.
    """
    try:
        cutoffs = tuple(float(part) for part in value.split(","))
    except ValueError:
        raise ValueError(f"{value!r} is not a comma-separated list of numbers") from None
    if len(cutoffs) != 4:
        raise ValueError(f"expected 4 cutoffs (A,B,C,D), got {len(cutoffs)}")
    if any(not (0.0 < c <= 1.0) or math.isnan(c) for c in cutoffs):
        raise ValueError("every cutoff must be in (0, 1]")
    if any(a <= b for a, b in zip(cutoffs, cutoffs[1:])):
        raise ValueError("cutoffs must be strictly decreasing (A > B > C > D)")
    return cutoffs


def _cutoffs(value: Any) -> Any:
    if isinstance(value, str):
        parse_cutoffs(value)
        return ",".join(part.strip() for part in value.split(","))
    return value


Url = Annotated[str, BeforeValidator(_url)]
Fraction = Annotated[float, Field(ge=0.0, le=1.0)]
Positive = Annotated[int, Field(ge=1)]
Port = Annotated[int, Field(ge=1, le=65535)]

LlmProvider = Literal["auto", "anthropic", "openrouter", "openai", "ollama", "mock", "none"]
ProxyLogLevel = Literal["debug", "info", "warn", "error"]
EngineLogLevel = Literal["DEBUG", "INFO", "WARNING", "ERROR"]


def key(name: str, *aliases: str, default: Any, description: str = "") -> Any:
    """A setting read from registry key `name` (and its registry aliases)."""
    return Field(
        default=default,
        validation_alias=AliasChoices(name, *aliases),
        description=description,
        json_schema_extra={"env": name, "aliases": list(aliases)},
    )


# ---------------------------------------------------------------------------
# Settings
# ---------------------------------------------------------------------------


class Settings(BaseSettings):
    """
    Engine settings: every contracts registry key with service `engine`.

    Field names are the Python names; the environment variable is the registry
    name (see `env_name`). Construct directly with field names in tests
    (`Settings(llm_provider="mock")`); the engine uses `load_settings()`.
    """

    model_config = SettingsConfigDict(
        env_file=ENV_FILES,
        env_file_encoding="utf-8",
        case_sensitive=False,
        extra="ignore",
        populate_by_name=True,
    )

    def __init__(self, **values: Any) -> None:
        # The dotenv files are resolved per instance, so TRUTHTABLE_ENV_FILE set
        # after import (a test, a wrapper script) is honoured.
        values.setdefault("_env_file", env_files() or None)
        super().__init__(**values)

    # --- Core -------------------------------------------------------------
    truthtable_log_level: Annotated[ProxyLogLevel, BeforeValidator(_lower)] = key(
        "TRUTHTABLE_LOG_LEVEL", default="info", description="Shared log level (LOG_LEVEL wins)."
    )
    env_file: str = key("TRUTHTABLE_ENV_FILE", default="auto")
    strict_config: bool = key("TRUTHTABLE_STRICT_CONFIG", default=False)
    demo_mode: bool = key("TRUTHTABLE_DEMO_MODE", default=False)
    readonly: bool = key("TRUTHTABLE_READONLY", default=False)
    public_read: bool = key("TRUTHTABLE_PUBLIC_READ", default=False)
    test_mode: bool = key("TRUTHTABLE_TEST_MODE", default=False)

    # --- Auth -------------------------------------------------------------
    internal_token: SecretStr = key("TRUTHTABLE_INTERNAL_TOKEN", default=SecretStr(""))
    internal_token_file: str = key(
        "TRUTHTABLE_INTERNAL_TOKEN_FILE", default="/var/lib/trustagent-internal/internal.token"
    )

    # --- Upstream / knowledge / decision-model keys the engine shares ------
    ollama_base_url: Url = key("OLLAMA_BASE_URL", default="http://localhost:11434")
    knowledge_enabled: bool = key(
        "TRUTHTABLE_KNOWLEDGE_ENABLED", "KNOWLEDGE_ENABLED", "RAG_ENABLED", default=False
    )
    knowledge_deadline: GoDuration = key(
        "TRUTHTABLE_KNOWLEDGE_DEADLINE", default=timedelta(milliseconds=150)
    )
    openrouter_api_key: SecretStr = key("OPENROUTER_API_KEY", default=SecretStr(""))
    openrouter_base_url: Url = key(
        "TRUTHTABLE_OPENROUTER_BASE_URL", default="https://openrouter.ai"
    )
    jev_app_url: Url = key(
        "TRUTHTABLE_JEV_APP_URL", default="https://github.com/timkush1/TrustAgent"
    )
    jev_app_title: str = key("TRUTHTABLE_JEV_APP_TITLE", default="TrustAgent")
    price_table_file: str = key("TRUTHTABLE_PRICE_TABLE_FILE", default="")

    # --- Engine: judge ----------------------------------------------------
    llm_provider: Annotated[LlmProvider, BeforeValidator(_lower)] = key(
        "LLM_PROVIDER", default="auto"
    )
    llm_model: str = key("LLM_MODEL", default="")
    llm_fallback: Annotated[Literal["none", "ollama"], BeforeValidator(_lower)] = key(
        "LLM_FALLBACK", default="none"
    )
    anthropic_api_key: SecretStr = key("ANTHROPIC_API_KEY", default=SecretStr(""))
    openai_api_key: SecretStr = key("OPENAI_API_KEY", default=SecretStr(""))
    openrouter_judge_model: str = key(
        "OPENROUTER_JUDGE_MODEL", default="anthropic/claude-haiku-4.5"
    )
    ollama_fallback_model: str = key("OLLAMA_FALLBACK_MODEL", default="llama3.2:1b")
    mock_fixtures_path: str = key("MOCK_FIXTURES_PATH", default="")
    judge_max_inflight: Positive = key("TRUTHTABLE_JUDGE_MAX_INFLIGHT", default=16)
    verifier_concurrency: Positive = key("VERIFIER_CONCURRENCY", default=5)
    anthropic_structured_outputs: Annotated[
        Literal["auto", "on", "off"], BeforeValidator(_lower)
    ] = key("TRUTHTABLE_ANTHROPIC_STRUCTURED_OUTPUTS", default="auto")

    # --- Engine: pipeline and server --------------------------------------
    verifier: Annotated[Literal["auto", "llm"], BeforeValidator(_lower)] = key(
        "TRUTHTABLE_VERIFIER", "VERIFIER_MODE", default="auto"
    )
    verify_context_scope: Annotated[Literal["per_claim", "all"], BeforeValidator(_lower)] = key(
        "TRUTHTABLE_VERIFY_CONTEXT_SCOPE", default="per_claim"
    )
    audit_extract_claims_without_evidence: bool = key(
        "TRUTHTABLE_AUDIT_EXTRACT_CLAIMS_WITHOUT_EVIDENCE", default=False
    )
    scoring_partial_weight: Fraction = key("TRUTHTABLE_SCORING_PARTIAL_WEIGHT", default=0.5)
    scoring_contradiction_flag: Fraction = key(
        "TRUTHTABLE_SCORING_CONTRADICTION_FLAG", default=0.35
    )
    scoring_unsupported_flag: Fraction = key("TRUTHTABLE_SCORING_UNSUPPORTED_FLAG", default=0.60)
    scoring_unsupported_fraction_flag: Fraction = key(
        "TRUTHTABLE_SCORING_UNSUPPORTED_FRACTION_FLAG", default=0.30
    )
    scoring_min_coverage: Fraction = key("TRUTHTABLE_SCORING_MIN_COVERAGE", default=0.5)
    scoring_max_unknown_fraction: Fraction = key(
        "TRUTHTABLE_SCORING_MAX_UNKNOWN_FRACTION", default=0.5
    )
    scoring_grade_cutoffs: Annotated[str, BeforeValidator(_cutoffs)] = key(
        "TRUTHTABLE_SCORING_GRADE_CUTOFFS", default="0.9,0.7,0.5,0.3"
    )
    unsupported_policy: Annotated[
        Literal["auto", "hallucination", "unverifiable"], BeforeValidator(_lower)
    ] = key("TRUTHTABLE_UNSUPPORTED_POLICY", default="auto")
    max_claims: Positive = key("TRUTHTABLE_MAX_CLAIMS", default=25)
    retriever_top_k: Positive = key("TRUTHTABLE_RETRIEVER_TOP_K", default=3)
    retriever_score_threshold: Fraction = key("TRUTHTABLE_RETRIEVER_SCORE_THRESHOLD", default=0.3)
    qdrant_url: Url = key("QDRANT_URL", default="")
    embedding_model: str = key("TRUTHTABLE_EMBEDDING_MODEL", default="all-MiniLM-L6-v2")
    grpc_host: str = key("GRPC_HOST", default="127.0.0.1")
    grpc_port: Port = key("GRPC_PORT", default=50051)
    metrics_port: Port = key("METRICS_PORT", default=8001)
    engine_max_rpcs: Positive = key("TRUTHTABLE_ENGINE_MAX_RPCS", default=64)
    engine_max_audits: Positive = key("TRUTHTABLE_ENGINE_MAX_AUDITS", default=16)
    log_level: Annotated[EngineLogLevel, BeforeValidator(_upper)] = key("LOG_LEVEL", default="INFO")
    langsmith_api_key: SecretStr = key("LANGSMITH_API_KEY", default=SecretStr(""))
    langsmith_project: str = key("LANGSMITH_PROJECT", default="trustagent")
    langsmith_tracing: bool = key("LANGSMITH_TRACING", default=False)
    contracts_dir: str = key("TRUTHTABLE_CONTRACTS_DIR", default="auto")

    # --- Derived values ---------------------------------------------------

    @property
    def grade_cutoffs(self) -> Tuple[float, ...]:
        """TRUTHTABLE_SCORING_GRADE_CUTOFFS as numbers (A, B, C, D)."""
        return parse_cutoffs(self.scoring_grade_cutoffs)

    @property
    def effective_log_level(self) -> str:
        """LOG_LEVEL when set, else TRUTHTABLE_LOG_LEVEL, as a logging level name."""
        if "log_level" in self.model_fields_set:
            return self.log_level
        if "truthtable_log_level" in self.model_fields_set:
            return {"warn": "WARNING"}.get(
                self.truthtable_log_level, self.truthtable_log_level.upper()
            )
        return self.log_level

    @property
    def knowledge_configured(self) -> bool:
        """The knowledge add-on is on and has a vector store to use."""
        return self.knowledge_enabled and bool(self.qdrant_url)

    @classmethod
    def env_name(cls, field_name: str) -> str:
        """The registry (environment) name of a field."""
        return _env_names(cls.model_fields[field_name])[0]


def _env_names(info: FieldInfo) -> List[str]:
    alias = info.validation_alias
    if isinstance(alias, AliasChoices):
        return [str(choice) for choice in alias.choices]
    return [str(alias)] if alias else []


def env_names_by_field() -> Dict[str, List[str]]:
    """Every Settings field with its environment names (canonical first)."""
    return {name: _env_names(info) for name, info in Settings.model_fields.items()}


# ---------------------------------------------------------------------------
# Loading with warnings
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ConfigWarning:
    """An invalid or unknown configuration key (never contains a secret value)."""

    key: str
    message: str
    hint: str = ""

    def __str__(self) -> str:
        return f"{self.key}: {self.message}" + (f" ({self.hint})" if self.hint else "")


@dataclass
class LoadedSettings:
    """The result of load_settings: the settings and every warning."""

    settings: Settings
    warnings: List[ConfigWarning] = field(default_factory=list)
    env_files: Tuple[Path, ...] = ()

    @property
    def strict_failure(self) -> bool:
        """TRUTHTABLE_STRICT_CONFIG is on and there is at least one warning."""
        return self.settings.strict_config and bool(self.warnings)


def levenshtein(a: str, b: str) -> int:
    """Edit distance between two strings."""
    if len(a) < len(b):
        a, b = b, a
    previous = list(range(len(b) + 1))
    for i, char_a in enumerate(a, 1):
        current = [i]
        for j, char_b in enumerate(b, 1):
            current.append(
                min(previous[j] + 1, current[j - 1] + 1, previous[j - 1] + (char_a != char_b))
            )
        previous = current
    return previous[-1]


def closest(name: str, candidates: Iterable[str], max_distance: int = 2) -> Optional[str]:
    """The candidate nearest to `name` within `max_distance` edits, if any."""
    best: Optional[Tuple[int, str]] = None
    for candidate in candidates:
        distance = levenshtein(name, candidate)
        if distance <= max_distance and (best is None or (distance, candidate) < best):
            best = (distance, candidate)
    return best[1] if best else None


def registry_keys(environ: Optional[Mapping[str, str]] = None) -> Optional[List[Dict[str, Any]]]:
    """The contracts registry entries, or None when the contracts are not available."""
    try:
        document = load_contract(REGISTRY_CONTRACT, environ)
    except (ContractsNotFoundError, OSError, ValueError):
        return None
    entries = document.get("keys") if isinstance(document, dict) else None
    return [e for e in entries if isinstance(e, dict)] if isinstance(entries, list) else None


def _dotenv_names(files: Sequence[Path]) -> List[str]:
    from dotenv import dotenv_values  # a pydantic-settings dependency

    names: List[str] = []
    for path in files:
        if path.is_file():
            names.extend(str(name) for name in dotenv_values(path))
    return names


def unknown_key_warnings(
    names: Iterable[str], registry: Sequence[Mapping[str, Any]]
) -> List[ConfigWarning]:
    """
    Warnings for configuration names that look like TrustAgent keys but are not:

    - any TRUTHTABLE_* name missing from the registry (of every service);
    - an unprefixed name within 2 edits of an engine key (a likely typo, e.g.
      LLM_MODLE), unless it is itself a registry key.
    """
    known = set()
    for entry in registry:
        known.add(str(entry.get("name", "")))
        known.update(str(alias) for alias in entry.get("aliases") or [])
    engine_unprefixed = sorted(
        {
            name
            for entry in registry
            if ENGINE_SERVICE in (entry.get("services") or [])
            for name in [str(entry.get("name", "")), *map(str, entry.get("aliases") or [])]
            if name and not name.startswith("TRUTHTABLE_")
        }
    )
    warnings: List[ConfigWarning] = []
    for name in sorted(set(names)):
        upper = name.upper()
        if upper in known:
            continue
        if upper.startswith("TRUTHTABLE_"):
            suggestion = closest(upper, sorted(known))
            warnings.append(
                ConfigWarning(
                    key=name,
                    message="unknown key",
                    hint=f"did you mean {suggestion}?" if suggestion else "not in the registry",
                )
            )
            continue
        prefix = upper.split("_", 1)[0] + "_"
        candidates = [k for k in engine_unprefixed if k.startswith(prefix)]
        suggestion = closest(upper, candidates)
        if suggestion is not None:
            warnings.append(
                ConfigWarning(key=name, message="unknown key", hint=f"did you mean {suggestion}?")
            )
    return warnings


def _field_for_loc(loc: Any) -> Optional[str]:
    wanted = str(loc).lower()
    for field_name, names in env_names_by_field().items():
        if wanted == field_name or wanted in (n.lower() for n in names):
            return field_name
    return None


def _format_default(value: Any) -> str:
    if isinstance(value, SecretStr):
        return "" if not value.get_secret_value() else "(set)"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, timedelta):
        return format_go_duration(value)
    return str(value)


def load_settings(files: Optional[Sequence[Path]] = None) -> LoadedSettings:
    """
    Load the engine settings from the environment and dotenv files.

    Invalid values keep their key's default and are reported as warnings (their
    values are never echoed for secret keys); unknown TRUTHTABLE_* keys and
    likely misspellings are reported too. Never raises for configuration
    problems: the caller decides (TRUTHTABLE_STRICT_CONFIG).
    """
    dotenv = tuple(env_files() if files is None else files)
    overrides: Dict[str, Any] = {}
    warnings: List[ConfigWarning] = []
    settings: Optional[Settings] = None
    for _ in range(len(Settings.model_fields) + 1):
        try:
            settings = Settings(_env_file=dotenv or None, **overrides)
            break
        except ValidationError as exc:
            progressed = False
            for error in exc.errors():
                field_name = _field_for_loc(error["loc"][0]) if error["loc"] else None
                if field_name is None or field_name in overrides:
                    continue
                info = Settings.model_fields[field_name]
                secret = info.annotation is SecretStr
                shown = "" if secret else f"={str(error.get('input'))!r}"
                default = info.get_default(call_default_factory=True)
                warnings.append(
                    ConfigWarning(
                        key=Settings.env_name(field_name),
                        message=f"invalid value{shown}: {error['msg']}",
                        hint=f"using default {_format_default(default)!r}",
                    )
                )
                overrides[field_name] = default
                progressed = True
            if not progressed:
                raise
    assert settings is not None

    registry = registry_keys()
    if registry is not None:
        names = list(os.environ) + _dotenv_names(dotenv)
        warnings.extend(unknown_key_warnings(names, registry))
    return LoadedSettings(settings=settings, warnings=warnings, env_files=dotenv)


# Global settings instance
_settings: Optional[Settings] = None


def get_settings() -> Settings:
    """Get or create settings instance (invalid values fall back to defaults)."""
    global _settings
    if _settings is None:
        _settings = load_settings().settings
    return _settings


# ---------------------------------------------------------------------------
# --dump-spec
# ---------------------------------------------------------------------------


def dump_spec() -> Dict[str, Any]:
    """The engine's configuration keys, for scripts/gen_config_docs.py."""
    keys = []
    for field_name, info in Settings.model_fields.items():
        names = _env_names(info)
        keys.append(
            {
                "name": names[0],
                "aliases": names[1:],
                "field": field_name,
                "default": _format_default(info.get_default(call_default_factory=True)),
                "secret": info.annotation is SecretStr,
            }
        )
    keys.sort(key=lambda entry: str(entry["name"]))
    return {"service": ENGINE_SERVICE, "keys": keys}


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m truthtable.config", description="TrustAgent engine configuration."
    )
    parser.add_argument(
        "--dump-spec", action="store_true", help="print the engine's keys as JSON and exit"
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="load the configuration, print warnings, exit 2 if there are any",
    )
    args = parser.parse_args(argv)
    if args.dump_spec:
        json.dump(dump_spec(), sys.stdout, indent=2)
        sys.stdout.write("\n")
        return 0
    if args.check:
        loaded = load_settings()
        for warning in loaded.warnings:
            print(f"warning: {warning}", file=sys.stderr)
        return STRICT_CONFIG_EXIT_CODE if loaded.warnings else 0
    parser.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main())
