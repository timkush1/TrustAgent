"""
Audit judge resolution (SPEC section 3.2, `init_provider`; R23, D16).

LLM_PROVIDER picks the deep-audit judge:

- an explicit provider (anthropic, openrouter, openai, ollama) is used as is;
- `auto` (the default) takes the first available of: an Anthropic key, an
  OpenRouter key, an OpenAI key, a reachable Ollama; otherwise there is no
  judge. In demo mode (TRUTHTABLE_DEMO_MODE) `auto` means the mock judge;
- `mock` is the scenario-fixture judge of the zero-key demo and e2e stacks. It
  never falls back: a missing scenario pack stops the engine;
- `none` runs without a judge: every deep audit is NOT_ASSESSED (no_judge).

LLM_MODEL overrides the model; unset, each provider uses its own default
(Anthropic claude-haiku-4-5, OpenRouter OPENROUTER_JUDGE_MODEL, OpenAI
gpt-4o-mini, Ollama OLLAMA_FALLBACK_MODEL, D16: never a Claude model name on
Ollama).

When the chosen judge cannot be built (a missing key) or fails its startup
health check, nothing is switched silently: with LLM_FALLBACK=none (the
default) the engine keeps the unhealthy judge (audits fail with
judge_unavailable until it recovers) or runs without one, and says so; with
LLM_FALLBACK=ollama it switches to local Ollama and reports the fallback
(JudgeInfo.fallback_active / fallback_reason, EngineInfo.warnings, logs).
"""

import json
import logging
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional

from .config import Settings
from .contracts import DEMO_FAULTS, DEMO_SCENARIOS, contract_path
from .providers.anthropic import DEFAULT_ANTHROPIC_MODEL
from .providers.base import LLMProvider
from .providers.mock import MockLLMProvider, load_scenario_pack
from .providers.ollama import OllamaProvider
from .providers.openai import DEFAULT_OPENAI_MODEL
from .providers.registry import get_provider

logger = logging.getLogger(__name__)

# Reported model of the mock judge unless LLM_MODEL names one.
MOCK_JUDGE_MODEL = "mock"

NO_JUDGE = "none"
AUTO = "auto"
MOCK = "mock"
OLLAMA = "ollama"

# `auto` resolution order of the providers that need a key.
KEYED_PROVIDERS = ("anthropic", "openrouter", "openai")

# JudgeInfo.credential_source values: the engine's env, none (keyless or no
# judge), or a dashboard credential pushed with ApplyRuntimeConfig
# (runtime/manager.py).
CREDENTIAL_ENV = "env"
CREDENTIAL_NONE = "none"
CREDENTIAL_DASHBOARD = "dashboard"


@dataclass
class JudgeSelection:
    """
    The judge the engine runs with, and how it was chosen.

    Attributes:
        provider: The judge, or None when the engine has no judge.
        configured: LLM_PROVIDER as configured (auto, anthropic, ..., none).
        effective: The provider in use (anthropic, openrouter, openai, ollama,
            mock) or "none".
        model: The judge model ("" without a judge).
        credential_source: env | none | dashboard (JudgeInfo.credential_source).
        fallback_active: LLM_FALLBACK replaced the chosen judge.
        fallback_reason: Why the chosen judge was replaced.
        healthy: The startup health check result (None when not probed).
        warnings: Human-readable problems to report (EngineInfo.warnings).
    """

    provider: Optional[LLMProvider]
    configured: str
    effective: str
    model: str
    credential_source: str = CREDENTIAL_NONE
    fallback_active: bool = False
    fallback_reason: str = ""
    healthy: Optional[bool] = None
    warnings: List[str] = field(default_factory=list)

    @property
    def available(self) -> bool:
        return self.provider is not None

    @property
    def structured_outputs(self) -> bool:
        return self.provider is not None and self.provider.supports_structured_outputs

    @classmethod
    def no_judge(cls, configured: str, reason: str) -> "JudgeSelection":
        return cls(
            provider=None, configured=configured, effective=NO_JUDGE, model="", warnings=[reason]
        )


def judge_model(settings: Settings, provider: str) -> str:
    """The model for `provider`: LLM_MODEL, else the provider default (D16)."""
    explicit = settings.llm_model.strip()
    if explicit:
        return explicit
    defaults: Dict[str, str] = {
        "anthropic": DEFAULT_ANTHROPIC_MODEL,
        "openrouter": settings.openrouter_judge_model,
        "openai": DEFAULT_OPENAI_MODEL,
        OLLAMA: settings.ollama_fallback_model,
        MOCK: MOCK_JUDGE_MODEL,
    }
    return defaults[provider]


def provider_key(settings: Settings, provider: str) -> str:
    """The configured API key of a keyed provider ("" when unset)."""
    secret = {
        "anthropic": settings.anthropic_api_key,
        "openrouter": settings.openrouter_api_key,
        "openai": settings.openai_api_key,
    }.get(provider)
    return secret.get_secret_value().strip() if secret is not None else ""


def build_mock_judge(settings: Settings, model: str) -> MockLLMProvider:
    """
    The mock judge: the contracts scenario packs, or MOCK_FIXTURES_PATH when set
    (a trustagent.scenarios/v1 pack, or a list of match fixtures).

    The default loads both packs (D146): a faults-pack scenario id reaches the
    engine as mock_scenario just like a main-pack one (D119), so its judge
    fixtures must be known or the audit fails with mock_fixture_missing.

    Raises:
        ContractsNotFoundError, OSError, ValueError: If the fixtures cannot be loaded,
            or a scenario id appears in both packs.
    """
    configured = settings.mock_fixtures_path.strip()
    if not configured:
        scenarios = load_scenario_pack(contract_path(DEMO_SCENARIOS))
        faults = load_scenario_pack(contract_path(DEMO_FAULTS))
        shared = sorted(scenarios.keys() & faults.keys())
        if shared:
            raise ValueError(f"scenario ids in both {DEMO_SCENARIOS} and {DEMO_FAULTS}: {shared}")
        return MockLLMProvider(model=model, scenarios={**scenarios, **faults})
    path = Path(configured).expanduser()
    with open(path, encoding="utf-8") as f:
        document = json.load(f)
    if isinstance(document, list):
        return MockLLMProvider(model=model, fixtures_path=path)
    return MockLLMProvider(model=model, scenarios_path=path)


def build_provider(
    settings: Settings, provider: str, model: str, options: Optional[Mapping[str, Any]] = None
) -> LLMProvider:
    """
    Construct `provider` from the settings. `options` are extra constructor
    arguments of the HTTP providers (tests pass a transport).

    Raises:
        ValueError: If a keyed provider has no key.
    """
    extra: Dict[str, Any] = dict(options or {})
    if provider == MOCK:
        return build_mock_judge(settings, model)
    if provider == OLLAMA:
        return get_provider(OLLAMA, model=model, base_url=settings.ollama_base_url, **extra)
    api_key = provider_key(settings, provider)
    if not api_key:
        raise ValueError(f"{provider} needs {Settings.env_name(provider + '_api_key')}")
    if provider == "anthropic":
        extra["structured_outputs"] = settings.anthropic_structured_outputs
    elif provider == "openrouter":
        extra.update(
            base_url=settings.openrouter_base_url,
            app_url=settings.jev_app_url,
            app_title=settings.jev_app_title,
        )
    return get_provider(provider, model=model, api_key=api_key, **extra)


async def _close(provider: Optional[LLMProvider]) -> None:
    if provider is not None:
        try:
            await provider.close()
        except Exception as exc:  # best effort; nothing depends on it
            logger.debug(f"Closing the unused {provider.provider_name} judge failed: {exc}")


async def resolve_judge(
    settings: Settings,
    *,
    log: logging.Logger = logger,
    probe: bool = True,
    options: Optional[Mapping[str, Any]] = None,
) -> JudgeSelection:
    """
    Choose and build the audit judge (see the module docstring).

    Args:
        settings: Engine settings.
        log: Where resolution is reported.
        probe: Run the startup health checks (free calls; disabled in some tests).
        options: Extra constructor arguments for the HTTP providers.

    Raises:
        ContractsNotFoundError, OSError, ValueError: Only for LLM_PROVIDER=mock
            whose fixtures cannot be loaded (the mock judge never falls back).
    """
    configured = settings.llm_provider

    if configured == MOCK or (configured == AUTO and settings.demo_mode):
        model = judge_model(settings, MOCK)
        mock = build_mock_judge(settings, model)
        source = (
            settings.mock_fixtures_path.strip() or f"contracts {DEMO_SCENARIOS} and {DEMO_FAULTS}"
        )
        log.info(f"Judge: mock (fixtures from {source}); audit verdicts are simulated")
        return JudgeSelection(mock, configured, MOCK, model, healthy=True)

    if configured == NO_JUDGE:
        reason = "LLM_PROVIDER=none: no judge; deep audits are NOT_ASSESSED (no_judge)"
        log.warning(reason)
        return JudgeSelection.no_judge(configured, reason)

    candidate: Optional[str] = None
    reachable_ollama: Optional[LLMProvider] = None
    if configured == AUTO:
        candidate = next((p for p in KEYED_PROVIDERS if provider_key(settings, p)), None)
        if candidate is None:
            ollama = build_provider(settings, OLLAMA, judge_model(settings, OLLAMA), options)
            if not isinstance(ollama, OllamaProvider):
                raise TypeError(f"the ollama provider is a {type(ollama).__name__}")
            if probe and await ollama.reachable():
                candidate, reachable_ollama = OLLAMA, ollama
            else:
                await _close(ollama)
                reason = (
                    "no judge configured: set ANTHROPIC_API_KEY or OPENROUTER_API_KEY "
                    f"(or OPENAI_API_KEY), or run Ollama at {settings.ollama_base_url}; "
                    "deep audits are NOT_ASSESSED (no_judge)"
                )
                log.warning(reason)
                return JudgeSelection.no_judge(configured, reason)
    else:
        candidate = configured

    model = judge_model(settings, candidate)
    credential = CREDENTIAL_ENV if candidate in KEYED_PROVIDERS else CREDENTIAL_NONE
    provider: Optional[LLMProvider] = reachable_ollama
    failure = ""
    if provider is None:
        try:
            provider = build_provider(settings, candidate, model, options)
        except ValueError as exc:
            failure = str(exc)
    healthy: Optional[bool] = None
    if provider is not None and probe:
        log.info(f"Judge: checking {candidate} (model {model})...")
        healthy = await provider.health_check()
        if not healthy:
            failure = f"{candidate} failed its startup health check"

    if not failure:
        log.info(f"Judge: {candidate} (model {model})")
        return JudgeSelection(provider, configured, candidate, model, credential, healthy=healthy)

    if settings.llm_fallback == OLLAMA and candidate != OLLAMA:
        await _close(provider)
        fallback_model = settings.ollama_fallback_model
        fallback = build_provider(settings, OLLAMA, fallback_model, options)
        fallback_healthy = await fallback.health_check() if probe else None
        warning = (
            f"judge fallback active: {candidate} -> ollama ({fallback_model}) because "
            f"{failure} (LLM_FALLBACK=ollama)"
        )
        log.warning(warning)
        if fallback_healthy is False:
            log.warning("The Ollama fallback judge failed its health check too")
        return JudgeSelection(
            fallback,
            configured,
            OLLAMA,
            fallback_model,
            CREDENTIAL_NONE,
            fallback_active=True,
            fallback_reason=failure,
            healthy=fallback_healthy,
            warnings=[warning],
        )

    if provider is None:
        reason = (
            f"judge {candidate} unavailable: {failure}; deep audits are NOT_ASSESSED "
            "(no_judge). Set LLM_FALLBACK=ollama to fall back to a local judge"
        )
        log.warning(reason)
        return JudgeSelection.no_judge(configured, reason)

    warning = (
        f"{failure}; keeping {candidate} (model {model}): audits fail with judge_unavailable "
        "until it recovers (LLM_FALLBACK=none)"
    )
    log.warning(warning)
    return JudgeSelection(
        provider, configured, candidate, model, credential, healthy=False, warnings=[warning]
    )
