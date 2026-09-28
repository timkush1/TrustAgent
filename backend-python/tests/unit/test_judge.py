"""
Judge resolution (SPEC section 3.2 `init_provider`; R23, D16): explicit >
Anthropic key > OpenRouter key > OpenAI key > Ollama if reachable > none;
mock in demo mode; LLM_FALLBACK=none by default and any fallback reported.
Provider HTTP goes through a scripted transport; nothing leaves the process.
"""

import json
import logging
from pathlib import Path
from typing import Any, Dict

import httpx
import pytest

from provider_fakes import json_reply
from truthtable.config import Settings
from truthtable.judge import MOCK_JUDGE_MODEL, build_provider, judge_model, resolve_judge
from truthtable.providers import (
    AnthropicProvider,
    MockLLMProvider,
    OllamaProvider,
    OpenAIProvider,
    OpenRouterProvider,
)

logger = logging.getLogger(__name__)


@pytest.fixture(autouse=True)
def isolated(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    for name in ("ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "LLM_MODEL"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    monkeypatch.chdir(tmp_path)


def settings(**values: Any) -> Settings:
    return Settings(_env_file=None, **values)


class FakeUpstreams(httpx.MockTransport):
    """Answers every provider's health probe; `down` hosts refuse connections."""

    def __init__(self, *, down: tuple = (), models: tuple = ("llama3.2:1b",)):
        self.down = set(down)
        self.models = models
        self.paths: list = []
        super().__init__(self._handle)

    def _handle(self, request: httpx.Request) -> httpx.Response:
        self.paths.append(f"{request.url.host}{request.url.path}")
        if request.url.host in self.down:
            raise httpx.ConnectError("refused")
        if request.url.path == "/api/tags":
            return json_reply({"models": [{"name": m} for m in self.models]})
        return json_reply({"data": []})


def options(transport: httpx.MockTransport) -> Dict[str, Any]:
    async def no_sleep(_: float) -> None:
        return None

    return {"transport": transport, "sleep": no_sleep}


async def resolve(s: Settings, transport: httpx.MockTransport = None, **kwargs: Any):
    return await resolve_judge(
        s, log=logger, options=options(transport or FakeUpstreams()), **kwargs
    )


# ---------------------------------------------------------------------------
# auto
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "keys, expected, provider_class",
    [
        (
            {"anthropic_api_key": "a", "openrouter_api_key": "r", "openai_api_key": "o"},
            "anthropic",
            AnthropicProvider,
        ),
        ({"openrouter_api_key": "r", "openai_api_key": "o"}, "openrouter", OpenRouterProvider),
        ({"openai_api_key": "o"}, "openai", OpenAIProvider),
    ],
)
async def test_auto_prefers_keys_in_order(
    keys: Dict[str, str], expected: str, provider_class: type
) -> None:
    selection = await resolve(settings(**keys))

    assert selection.effective == expected
    assert selection.configured == "auto"
    assert isinstance(selection.provider, provider_class)
    assert selection.credential_source == "env"
    assert selection.healthy is True
    assert not selection.fallback_active and not selection.warnings


async def test_auto_uses_a_reachable_ollama_without_keys() -> None:
    selection = await resolve(settings())

    assert selection.effective == "ollama"
    assert isinstance(selection.provider, OllamaProvider)
    assert selection.model == "llama3.2:1b"  # OLLAMA_FALLBACK_MODEL (D16)
    assert selection.credential_source == "none"


async def test_auto_without_keys_or_ollama_has_no_judge() -> None:
    selection = await resolve(settings(), FakeUpstreams(down=("localhost",)))

    assert selection.provider is None
    assert selection.effective == "none"
    assert selection.model == ""
    assert "no judge configured" in selection.warnings[0]
    assert "NOT_ASSESSED (no_judge)" in selection.warnings[0]


async def test_auto_in_demo_mode_is_the_mock_judge() -> None:
    selection = await resolve(settings(demo_mode=True, anthropic_api_key="a"))

    assert selection.effective == "mock"
    assert isinstance(selection.provider, MockLLMProvider)
    assert selection.model == MOCK_JUDGE_MODEL


# ---------------------------------------------------------------------------
# explicit providers, models (D16)
# ---------------------------------------------------------------------------


async def test_explicit_provider_wins_over_keys() -> None:
    selection = await resolve(
        settings(llm_provider="openai", anthropic_api_key="a", openai_api_key="o")
    )
    assert selection.effective == "openai"
    assert selection.model == "gpt-4o-mini"


async def test_explicit_ollama_without_llm_model_uses_the_ollama_model() -> None:
    # D16: never ask Ollama for claude-haiku-4-5.
    selection = await resolve(settings(llm_provider="ollama"))
    assert selection.model == "llama3.2:1b"
    assert selection.provider.model == "llama3.2:1b"


@pytest.mark.parametrize(
    "provider, expected",
    [
        ("anthropic", "claude-haiku-4-5"),
        ("openrouter", "anthropic/claude-haiku-4.5"),
        ("openai", "gpt-4o-mini"),
        ("ollama", "llama3.2:1b"),
        ("mock", "mock"),
    ],
)
def test_default_models(provider: str, expected: str) -> None:
    assert judge_model(settings(), provider) == expected
    assert judge_model(settings(llm_model="custom"), provider) == "custom"


async def test_openrouter_judge_is_configured_from_settings() -> None:
    s = settings(
        llm_provider="openrouter",
        openrouter_api_key="sk-or",
        openrouter_base_url="http://mock-upstream:8081",
        jev_app_title="TrustAgent CI",
        openrouter_judge_model="anthropic/claude-haiku-4.5",
    )
    judge = build_provider(s, "openrouter", judge_model(s, "openrouter"))

    assert isinstance(judge, OpenRouterProvider)
    assert judge.base_url == "http://mock-upstream:8081"
    assert judge.app_title == "TrustAgent CI"
    assert judge.model == "anthropic/claude-haiku-4.5"


async def test_anthropic_structured_outputs_mode_is_passed_through() -> None:
    s = settings(anthropic_api_key="a", anthropic_structured_outputs="off")
    judge = build_provider(s, "anthropic", "claude-haiku-4-5")
    assert judge.supports_structured_outputs is False


async def test_llm_provider_none_has_no_judge() -> None:
    selection = await resolve(settings(llm_provider="none", anthropic_api_key="a"))
    assert selection.provider is None
    assert selection.configured == "none" and selection.effective == "none"


# ---------------------------------------------------------------------------
# Failures and LLM_FALLBACK
# ---------------------------------------------------------------------------


async def test_missing_key_without_fallback_means_no_judge() -> None:
    selection = await resolve(settings(llm_provider="anthropic"))

    assert selection.provider is None
    assert "ANTHROPIC_API_KEY" in selection.warnings[0]
    assert "LLM_FALLBACK=ollama" in selection.warnings[0]
    assert not selection.fallback_active


async def test_unhealthy_judge_is_kept_and_reported_without_fallback() -> None:
    transport = FakeUpstreams(down=("api.anthropic.com",))

    selection = await resolve(settings(anthropic_api_key="a"), transport)

    assert selection.effective == "anthropic"
    assert isinstance(selection.provider, AnthropicProvider)
    assert selection.healthy is False
    assert not selection.fallback_active
    assert "failed its startup health check" in selection.warnings[0]
    assert "api.anthropic.com/v1/models" in transport.paths
    assert not any(path.endswith("/api/tags") for path in transport.paths)  # no silent Ollama


async def test_fallback_to_ollama_is_explicit_and_reported() -> None:
    transport = FakeUpstreams(down=("api.anthropic.com",))

    selection = await resolve(settings(anthropic_api_key="a", llm_fallback="ollama"), transport)

    assert selection.effective == "ollama"
    assert isinstance(selection.provider, OllamaProvider)
    assert selection.model == "llama3.2:1b"
    assert selection.fallback_active is True
    assert selection.fallback_reason == "anthropic failed its startup health check"
    assert selection.healthy is True
    assert selection.configured == "auto"
    assert "judge fallback active: anthropic -> ollama" in selection.warnings[0]


async def test_fallback_covers_a_missing_key() -> None:
    selection = await resolve(settings(llm_provider="openrouter", llm_fallback="ollama"))

    assert selection.effective == "ollama"
    assert selection.fallback_active is True
    assert "OPENROUTER_API_KEY" in selection.fallback_reason


async def test_explicit_ollama_never_falls_back() -> None:
    transport = FakeUpstreams(models=())  # reachable, model not pulled

    selection = await resolve(settings(llm_provider="ollama", llm_fallback="ollama"), transport)

    assert selection.effective == "ollama"
    assert selection.healthy is False
    assert not selection.fallback_active


# ---------------------------------------------------------------------------
# Mock judge
# ---------------------------------------------------------------------------


async def test_mock_uses_the_contract_scenarios() -> None:
    selection = await resolve(settings(llm_provider="mock"))

    assert selection.effective == "mock"
    assert selection.provider.scenarios
    assert "grounded-pass" in selection.provider.scenarios


async def test_mock_fixtures_path_accepts_match_fixtures(tmp_path: Path) -> None:
    fixtures = tmp_path / "fixtures.json"
    fixtures.write_text(json.dumps([{"match": "x", "response": "y"}]), encoding="utf-8")

    selection = await resolve(settings(llm_provider="mock", mock_fixtures_path=str(fixtures)))

    assert selection.provider.fixtures == [{"match": "x", "response": "y"}]
    assert selection.provider.scenarios is None


async def test_mock_fixtures_path_must_exist(tmp_path: Path) -> None:
    with pytest.raises(OSError):
        await resolve(settings(llm_provider="mock", mock_fixtures_path=str(tmp_path / "missing")))
