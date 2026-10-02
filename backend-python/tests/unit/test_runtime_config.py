"""
Runtime configuration of the engine (SPEC section 3.2 `runtime/`; P4-PLAN D34, D39, D42, D78).

The gateway pushes `RuntimeConfig{version, epoch, credentials, judge,
price_table_json}`; these tests pin what the engine does with it:

- the epoch and version rules (RuntimeState);
- the judge (ProviderManager): the environment wins and is never mixed with a
  push, rebuilds happen only on a real change, a replaced judge drains before
  it closes, a push without a judge reverts to the environment, mock and none,
  and allowed origins are intersected with the engine's own table;
- the servicer: the price table, Health and Info reporting, `env_managed`
  names only, and the shared push fixture
  `contracts/fixtures/grpc/RuntimeConfig.openrouter-judge.json`;
- log redaction (D42).

Provider HTTP goes through a scripted transport; nothing leaves the process.
Every secret is fake. Leak checks never print a secret: they assert absence
with a message that does not include it.
"""

import asyncio
import json
import logging
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any, AsyncIterator, Dict, List, Optional, Sequence

import httpx
import pytest
from google.protobuf import json_format
from grpc import aio

from provider_fakes import ScriptedTransport, json_reply
from truthtable import __version__
from truthtable.config import Settings
from truthtable.contracts import contract_path
from truthtable.engine_info import EngineContext
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer
from truthtable.judge import JudgeSelection
from truthtable.providers.base import (
    CompletionRequest,
    CompletionResponse,
    LLMProvider,
    Message,
    MessageRole,
)
from truthtable.providers.errors import ProviderError
from truthtable.providers.limiter import JudgeLimiter, LimitedProvider, unwrap
from truthtable.providers.mock import MockLLMProvider
from truthtable.providers.openrouter import OpenRouterProvider
from truthtable.providers.pricing import active_price_table, set_active_price_table
from truthtable.providers.swappable import SwappableProvider
from truthtable.runtime.manager import (
    CONFIGURED,
    ENV_MANAGED,
    INVALID,
    NO_JUDGE_STATUS,
    UNCHANGED,
    ProviderManager,
    env_managed_keys,
)
from truthtable.runtime.origins import canonical_origin, engine_origins
from truthtable.runtime.redaction import (
    MASKED,
    Redactor,
    install_redaction,
    redact_patterns,
    settings_secrets,
)
from truthtable.runtime.state import RuntimeState

RPC_TIMEOUT_S = 5
FIXTURE = "fixtures/grpc/RuntimeConfig.openrouter-judge.json"

# Fake secrets only. The last one matches no redaction pattern (P4-PLAN §0 rule 8).
DASHBOARD_KEY = "sk-or-v1-e2e-dashboard-1111111111111111111111111111"
ROTATED_KEY = "sk-or-v1-e2e-rotated-22222222222222222222222222222"
ENV_KEY = "sk-or-v1-e2e-env-333333333333333333333333333333333"
UNMATCHED_SECRET = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"


def assert_absent(secret: str, text: str, where: str) -> None:
    """Fails when `secret` occurs in `text`, without ever showing the secret."""
    leaked = secret in text
    assert not leaked, f"a secret leaked into {where}"


@pytest.fixture(autouse=True)
def isolated(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    for name in (
        "ANTHROPIC_API_KEY",
        "OPENAI_API_KEY",
        "OPENROUTER_API_KEY",
        "LLM_MODEL",
        "LLM_PROVIDER",
        "TRUTHTABLE_OPENROUTER_BASE_URL",
        "TRUTHTABLE_DEMO_MODE",
    ):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    monkeypatch.chdir(tmp_path)


@pytest.fixture(autouse=True)
def default_price_table() -> Any:
    yield
    set_active_price_table(None)


def settings(**values: Any) -> Settings:
    return Settings(_env_file=None, **values)


class FakeJudge(LLMProvider):
    """A judge that records its calls; `gate` holds every call until it is set."""

    provider_name = "fake"

    def __init__(self, model: str = "fake-model", gate: Optional[asyncio.Event] = None):
        super().__init__(model=model)
        self.calls: List[CompletionRequest] = []
        self.closed = 0
        self.gate = gate

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.calls.append(request)
        if self.gate is not None:
            await self.gate.wait()
        return CompletionResponse(
            content="ok", model=request.model, finish_reason="stop", provider="fake"
        )

    async def health_check(self) -> bool:
        return True

    async def close(self) -> None:
        self.closed += 1


def env_judge(provider: Optional[LLMProvider], credential_source: str = "none") -> JudgeSelection:
    if provider is None:
        return JudgeSelection.no_judge("auto", "no judge configured")
    return JudgeSelection(
        provider,
        "auto",
        provider.provider_name,
        provider.model,
        credential_source=credential_source,
    )


def push(
    version: int,
    *,
    epoch: str = "epoch-1",
    provider: Optional[str] = "openrouter",
    model: str = "anthropic/claude-haiku-4.5",
    secret: str = DASHBOARD_KEY,
    fingerprint: str = "fp_aaaaaaaaaaaa",
    origins: Sequence[str] = ("https://openrouter.ai",),
    credential_provider: Optional[str] = None,
    price_table: Optional[Dict[str, Any]] = None,
) -> pb.RuntimeConfig:
    """A RuntimeConfig as the gateway builds it (D34): only the judge's credential."""
    config = pb.RuntimeConfig(version=version, epoch=epoch)
    if provider is not None:
        config.judge.CopyFrom(pb.JudgeConfig(provider=provider, model=model))
        if provider in ("openrouter", "anthropic", "openai"):
            config.judge.credential = provider
            config.credentials[provider].CopyFrom(
                pb.Credential(
                    provider=credential_provider or provider,
                    secret=secret,
                    fingerprint=fingerprint,
                    allowed_hosts=list(origins),
                )
            )
    if price_table is not None:
        config.price_table_json = json.dumps(price_table).encode()
    return config


def openrouter_transport() -> ScriptedTransport:
    return ScriptedTransport(
        json_reply(
            {
                "choices": [{"message": {"content": "ok"}, "finish_reason": "stop"}],
                "model": "anthropic/claude-haiku-4.5",
                "usage": {"prompt_tokens": 3, "completion_tokens": 1, "cost": 0.0001},
            }
        )
    )


def manager_for(
    env: Optional[LLMProvider] = None,
    *,
    credential_source: str = "none",
    transport: Optional[httpx.AsyncBaseTransport] = None,
    redactor: Optional[Redactor] = None,
    **values: Any,
) -> ProviderManager:
    swappable = SwappableProvider(env)
    return ProviderManager(
        settings(**values),
        env_judge(env, credential_source),
        swappable,
        redactor=redactor or Redactor(),
        provider_options={"transport": transport or openrouter_transport()},
    )


def request(model: str = "") -> CompletionRequest:
    return CompletionRequest(messages=[Message(MessageRole.USER, "hi")], model=model)


# ---------------------------------------------------------------------------
# RuntimeState: epoch and version
# ---------------------------------------------------------------------------


def test_state_accepts_a_new_epoch_or_a_higher_version_only() -> None:
    state = RuntimeState()
    assert (state.epoch, state.version) == ("", 0)

    assert state.apply("epoch-a", 3) is True
    assert state.apply("epoch-a", 3) is False  # a replay
    assert state.apply("epoch-a", 2) is False  # an older push that lost a race
    assert (state.epoch, state.version) == ("epoch-a", 3)

    assert state.apply("epoch-a", 4) is True
    # A new epoch (the settings store was recreated) is applied at any version.
    assert state.apply("epoch-b", 1) is True
    assert (state.epoch, state.version) == ("epoch-b", 1)
    assert state.accepts("epoch-a", 4) is True  # back to the old epoch: new again


# ---------------------------------------------------------------------------
# ProviderManager: which judge runs
# ---------------------------------------------------------------------------


async def test_a_no_judge_start_gains_a_dashboard_judge() -> None:
    transport = openrouter_transport()
    manager = manager_for(None, transport=transport)
    assert manager.has_judge is False

    outcome = await manager.apply(push(1))

    assert outcome.judge_status == CONFIGURED
    assert manager.has_judge is True
    selection = manager.selection
    assert (selection.effective, selection.model, selection.credential_source) == (
        "openrouter",
        "anthropic/claude-haiku-4.5",
        "dashboard",
    )
    judge = manager.swappable.current
    assert isinstance(judge, OpenRouterProvider)
    await manager.swappable.complete(request(judge.model))
    sent = transport.requests[-1].headers["authorization"] == f"Bearer {DASHBOARD_KEY}"
    assert sent, "the dashboard credential is used for the call"
    assert str(transport.requests[-1].url).startswith("https://openrouter.ai/api/v1/")


async def test_llm_provider_in_the_env_wins_over_a_push() -> None:
    env = FakeJudge()
    manager = manager_for(env, llm_provider="ollama")

    outcome = await manager.apply(push(1))

    assert outcome.judge_status == ENV_MANAGED
    assert "LLM_PROVIDER" in outcome.warnings[0]
    assert manager.swappable.current is env
    assert manager.selection.credential_source == "none"


async def test_an_env_api_key_wins_and_is_never_mixed_with_a_push() -> None:
    # The startup resolution found OPENROUTER_API_KEY: the env judge stays, with
    # its own key and model, even though the push names the same provider.
    transport = openrouter_transport()
    env = OpenRouterProvider(model="env/model", api_key=ENV_KEY, transport=transport)
    manager = manager_for(env, credential_source="env", openrouter_api_key=ENV_KEY)

    outcome = await manager.apply(push(1, model="pushed/model"))

    assert outcome.judge_status == ENV_MANAGED
    assert manager.swappable.current is env
    await manager.swappable.complete(request(env.model))
    headers = transport.requests[-1].headers["authorization"]
    assert headers == f"Bearer {ENV_KEY}", "the env key, never the pushed one"
    assert_absent(DASHBOARD_KEY, headers, "the env judge's request")
    assert transport.body()["model"] == "env/model"


async def test_rebuilds_only_on_a_real_change() -> None:
    manager = manager_for(FakeJudge())

    assert (await manager.apply(push(1))).judge_status == CONFIGURED
    first = manager.swappable.current

    # Same provider, model, credential and origins at a higher version.
    outcome = await manager.apply(push(2))
    assert outcome.judge_status == UNCHANGED
    assert outcome.rebuilt is False
    assert manager.swappable.current is first

    # A new model, a rotated secret (even under a stale fingerprint): rebuilds.
    assert (await manager.apply(push(3, model="openai/gpt-4o-mini"))).judge_status == CONFIGURED
    second = manager.swappable.current
    assert second is not first and second is not None
    assert second.model == "openai/gpt-4o-mini"
    rotated = await manager.apply(push(4, model="openai/gpt-4o-mini", secret=ROTATED_KEY))
    assert rotated.judge_status == CONFIGURED
    assert manager.swappable.current is not second


async def test_a_replaced_judge_drains_then_closes() -> None:
    gate = asyncio.Event()
    env = FakeJudge(gate=gate)
    manager = manager_for(env)
    swappable = manager.swappable
    running = asyncio.create_task(swappable.complete(request(env.model)))
    await asyncio.sleep(0)
    assert swappable.inflight(env) == 1

    await manager.apply(push(1))

    # New calls go to the new judge at once; the old one is still open.
    assert swappable.current is not env
    await asyncio.sleep(0.01)
    assert env.closed == 0
    gate.set()
    response = await running
    assert response.content == "ok"
    await swappable.drained()
    assert env.closed == 1


async def test_a_judge_swapped_back_in_while_draining_stays_open() -> None:
    gate = asyncio.Event()
    env = FakeJudge(gate=gate)
    manager = manager_for(env)
    running = asyncio.create_task(manager.swappable.complete(request(env.model)))
    await asyncio.sleep(0)

    await manager.apply(push(1))
    await manager.apply(pb.RuntimeConfig(version=2, epoch="epoch-1"))  # revert to env
    gate.set()
    await running
    await manager.swappable.drained()

    assert manager.swappable.current is env
    assert env.closed == 0


async def test_revert_without_a_judge_stops_using_the_old_credential() -> None:
    transport = openrouter_transport()
    env = FakeJudge()
    manager = manager_for(env, transport=transport)
    await manager.apply(push(1))
    await manager.swappable.complete(request("anthropic/claude-haiku-4.5"))
    dashboard_calls = transport.calls
    assert dashboard_calls == 1

    outcome = await manager.apply(pb.RuntimeConfig(version=2, epoch="epoch-1"))

    assert outcome.judge_status == NO_JUDGE_STATUS
    assert manager.selection is manager.env_selection
    assert manager.selection.credential_source == "none"
    # A request built from the old judge's model is sent with the env judge's.
    await manager.swappable.complete(request("anthropic/claude-haiku-4.5"))
    await manager.swappable.complete(request(env.model))
    assert transport.calls == dashboard_calls, "no further call uses the old credential"
    assert [call.model for call in env.calls] == ["fake-model", "fake-model"]
    await manager.swappable.drained()


async def test_the_mock_judge_and_none_can_be_pushed() -> None:
    manager = manager_for(FakeJudge())

    mock = await manager.apply(push(1, provider="mock", model=""))
    assert mock.judge_status == CONFIGURED
    assert isinstance(manager.swappable.current, MockLLMProvider)
    assert (manager.selection.effective, manager.selection.credential_source) == ("mock", "none")

    none = await manager.apply(push(2, provider="none"))
    assert none.judge_status == NO_JUDGE_STATUS
    assert manager.has_judge is False
    assert manager.swappable.current is None


async def test_pushed_foreign_origins_are_ignored() -> None:
    manager = manager_for(FakeJudge())

    outcome = await manager.apply(
        push(1, origins=["https://evil.example", "HTTPS://OpenRouter.ai:443/api/v1"])
    )

    assert outcome.judge_status == CONFIGURED
    assert any("https://evil.example" in w and "ignored" in w for w in outcome.warnings)
    judge = unwrap(manager.swappable)
    assert isinstance(judge, OpenRouterProvider)
    assert judge.allowed_origins == frozenset({"https://openrouter.ai"})


async def test_a_push_with_only_foreign_origins_is_invalid_and_reverts() -> None:
    env = FakeJudge()
    manager = manager_for(env)
    await manager.apply(push(1))

    outcome = await manager.apply(push(2, model="other/model", origins=["https://evil.example"]))

    assert outcome.judge_status == INVALID
    assert manager.swappable.current is env
    assert manager.selection is manager.env_selection
    await manager.swappable.drained()


async def test_the_env_openrouter_base_url_joins_the_engine_origins() -> None:
    transport = openrouter_transport()
    manager = manager_for(
        None, transport=transport, openrouter_base_url="http://mock-upstream:8090/api/v1"
    )
    assert engine_origins("openrouter", manager.settings) == frozenset(
        {"https://openrouter.ai", "http://mock-upstream:8090"}
    )

    outcome = await manager.apply(
        push(1, origins=["https://openrouter.ai", "http://mock-upstream:8090"])
    )

    assert outcome.judge_status == CONFIGURED
    await manager.swappable.complete(request("anthropic/claude-haiku-4.5"))
    assert str(transport.requests[-1].url) == "http://mock-upstream:8090/api/v1/chat/completions"


@pytest.mark.parametrize(
    "config, reason",
    [
        (push(1, provider="compatible"), "unknown judge provider"),
        (push(1, credential_provider="anthropic"), "belongs to anthropic"),
        (push(1, secret=""), "has no secret"),
    ],
    ids=["unknown-provider", "credential-of-another-provider", "empty-secret"],
)
async def test_unusable_pushes_are_invalid(config: pb.RuntimeConfig, reason: str) -> None:
    env = FakeJudge()
    manager = manager_for(env)

    outcome = await manager.apply(config)

    assert outcome.judge_status == INVALID
    assert reason in outcome.warnings[0]
    assert manager.swappable.current is env


async def test_a_judge_bound_to_a_missing_credential_is_invalid() -> None:
    config = push(1)
    config.judge.credential = "missing"
    manager = manager_for(FakeJudge())

    outcome = await manager.apply(config)

    assert outcome.judge_status == INVALID
    assert "not in the push" in outcome.warnings[0]


async def test_the_http_provider_refuses_other_origins_before_sending() -> None:
    transport = openrouter_transport()
    provider = OpenRouterProvider(
        api_key=DASHBOARD_KEY, transport=transport, allowed_origins=["https://openrouter.ai"]
    )
    with pytest.raises(ProviderError) as exc_info:
        await provider._request("GET", "https://evil.example/collect")
    assert "not one of its allowed origins" in str(exc_info.value)
    assert transport.calls == 0
    with pytest.raises(ValueError):
        OpenRouterProvider(
            api_key=DASHBOARD_KEY,
            base_url="https://evil.example",
            allowed_origins=["https://openrouter.ai"],
        )
    await provider.close()


def test_canonical_origins() -> None:
    assert canonical_origin("HTTPS://OpenRouter.AI:443/api/v1?x=1") == "https://openrouter.ai"
    assert canonical_origin("http://localhost:80") == "http://localhost"
    assert canonical_origin("http://mock-upstream:8090/") == "http://mock-upstream:8090"
    assert canonical_origin("http://[::1]:8080") == "http://[::1]:8080"
    for bad in (
        "openrouter.ai",
        "ftp://openrouter.ai",
        "https://user:pw@openrouter.ai",
        "https://",
    ):
        with pytest.raises(ValueError):
            canonical_origin(bad)


def test_env_managed_lists_the_names_of_non_empty_keys_only() -> None:
    values = settings(
        llm_model="judge-model",
        anthropic_api_key=UNMATCHED_SECRET,
        openai_api_key="",
    )
    names = env_managed_keys(values)
    assert names == ["LLM_MODEL", "ANTHROPIC_API_KEY"]
    assert_absent(UNMATCHED_SECRET, json.dumps(names), "env_managed")
    assert env_managed_keys(settings()) == []


# ---------------------------------------------------------------------------
# The servicer: ApplyRuntimeConfig, HealthCheck, GetEngineInfo
# ---------------------------------------------------------------------------


@asynccontextmanager
async def serving(servicer: AuditServicer) -> AsyncIterator[pb_grpc.AuditServiceStub]:
    server = aio.server()
    pb_grpc.add_AuditServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield pb_grpc.AuditServiceStub(channel)
    finally:
        await channel.close()
        await server.stop(grace=None)


def engine_servicer(
    env: Optional[LLMProvider] = None,
    *,
    credential_source: str = "none",
    transport: Optional[httpx.AsyncBaseTransport] = None,
    redactor: Optional[Redactor] = None,
    **values: Any,
) -> AuditServicer:
    """The servicer as main.py wires it: a swappable judge under the limiter."""
    engine_settings = settings(**values)
    selection = env_judge(env, credential_source)
    swappable = SwappableProvider(env)
    provider = LimitedProvider(swappable, JudgeLimiter(4))
    redactor = redactor or Redactor()
    manager = ProviderManager(
        engine_settings,
        selection,
        swappable,
        redactor=redactor,
        provider_options={"transport": transport or openrouter_transport()},
    )
    return AuditServicer(
        audit_graph=build_audit_graph(provider=provider) if env is not None else None,
        provider=provider,
        engine=EngineContext(settings=engine_settings, judge=selection),
        runtime=manager,
        graph_factory=lambda: build_audit_graph(provider=provider),
        redactor=redactor,
    )


def fixture_push() -> pb.RuntimeConfig:
    with open(contract_path(FIXTURE), encoding="utf-8") as f:
        return json_format.Parse(f.read(), pb.RuntimeConfig())


async def test_the_shared_fixture_push_configures_a_dashboard_judge() -> None:
    servicer = engine_servicer(None)
    async with serving(servicer) as stub:
        response = await stub.ApplyRuntimeConfig(fixture_push(), timeout=RPC_TIMEOUT_S)
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
        health = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert response.judge_status == "configured"
    assert response.decision_status == "unset"
    assert (response.applied_version, response.applied_epoch) == (
        7,
        "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90",
    )
    assert info.judge.credential_source == "dashboard"
    assert (info.judge.effective_provider, info.judge.model) == (
        "openrouter",
        "anthropic/claude-haiku-4.5",
    )
    assert (info.runtime_config_version, info.runtime_config_epoch) == (7, response.applied_epoch)
    assert (health.runtime_config_version, health.runtime_config_epoch) == (
        7,
        response.applied_epoch,
    )
    assert (health.judge_provider, health.version) == ("openrouter", __version__)
    secret = fixture_push().credentials["openrouter"].secret
    for message in (response, info, health):
        assert_absent(secret, str(message), type(message).__name__)


async def test_a_stale_push_changes_nothing() -> None:
    servicer = engine_servicer(None)
    async with serving(servicer) as stub:
        await stub.ApplyRuntimeConfig(push(5), timeout=RPC_TIMEOUT_S)
        stale = await stub.ApplyRuntimeConfig(
            push(4, provider="mock", model=""), timeout=RPC_TIMEOUT_S
        )
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)

    assert stale.judge_status == "unchanged"
    assert (stale.applied_version, stale.applied_epoch) == (5, "epoch-1")
    assert "ignored" in stale.warnings[0]
    assert info.judge.effective_provider == "openrouter"


async def test_the_price_table_is_applied() -> None:
    table = {
        "version": "pushed-2026-10",
        "currency": "USD",
        "entries": [
            {
                "match": "anthropic/*",
                "provider": "openrouter",
                "input_per_mtok": 2.0,
                "output_per_mtok": 8.0,
                "label": "override",
            }
        ],
    }
    servicer = engine_servicer(None)
    async with serving(servicer) as stub:
        applied = await stub.ApplyRuntimeConfig(push(1, price_table=table), timeout=RPC_TIMEOUT_S)
        broken = pb.RuntimeConfig(version=2, epoch="epoch-1", price_table_json=b"{not json")
        rejected = await stub.ApplyRuntimeConfig(broken, timeout=RPC_TIMEOUT_S)

    assert applied.judge_status == "configured"
    assert active_price_table().version == "pushed-2026-10"
    cost = active_price_table().cost("openrouter", "anthropic/claude-haiku-4.5", 1_000_000, 0)
    assert (cost.cost_usd, cost.cost_source) == (2.0, "computed")
    # A table that does not parse keeps the one in use and says so.
    assert any("price table not applied" in w for w in rejected.warnings)
    assert active_price_table().version == "pushed-2026-10"


async def test_info_reports_env_managed_names_and_the_env_credential_source() -> None:
    transport = openrouter_transport()
    env = OpenRouterProvider(model="env/model", api_key=ENV_KEY, transport=transport)
    servicer = engine_servicer(
        env,
        credential_source="env",
        transport=transport,
        openrouter_api_key=ENV_KEY,
        anthropic_api_key=UNMATCHED_SECRET,
    )
    async with serving(servicer) as stub:
        response = await stub.ApplyRuntimeConfig(push(1), timeout=RPC_TIMEOUT_S)
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)

    assert response.judge_status == "env_managed"
    assert list(info.env_managed) == ["ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"]
    assert info.judge.credential_source == "env"
    wire = info.SerializeToString().decode("utf-8", errors="replace")
    for secret in (ENV_KEY, UNMATCHED_SECRET, DASHBOARD_KEY):
        assert_absent(secret, wire, "EngineInfo")


async def test_a_pushed_mock_judge_runs_audits_on_an_engine_started_without_one() -> None:
    from test_server_v2 import _scenario, _scenario_request

    servicer = engine_servicer(None)
    request_pb = _scenario_request(_scenario("grounded-pass"))
    async with serving(servicer) as stub:
        before = await stub.Evaluate(request_pb, timeout=RPC_TIMEOUT_S)
        await stub.ApplyRuntimeConfig(push(1, provider="mock", model=""), timeout=RPC_TIMEOUT_S)
        after = await stub.Evaluate(request_pb, timeout=RPC_TIMEOUT_S)

    assert (before.verdict, before.not_assessed_reason) == (
        pb.AUDIT_VERDICT_NOT_ASSESSED,
        "no_judge",
    )
    assert after.verdict == pb.AUDIT_VERDICT_GROUNDED
    assert after.judge.effective_provider == "mock"
    assert after.costs and all(c.cost_source == "simulated" for c in after.costs)


async def test_a_servicer_without_a_runtime_manager_refuses_pushes() -> None:
    async with serving(AuditServicer(audit_graph=None)) as stub:
        with pytest.raises(aio.AioRpcError) as exc_info:
            await stub.ApplyRuntimeConfig(push(1), timeout=RPC_TIMEOUT_S)
    assert exc_info.value.details() == "runtime_config_unavailable"


# ---------------------------------------------------------------------------
# Redaction (D42)
# ---------------------------------------------------------------------------


def test_the_unmatched_secret_needs_registration() -> None:
    # The pattern set alone leaves it as it is: only a registered secret is masked.
    assert redact_patterns(f"key={UNMATCHED_SECRET}") == f"key={UNMATCHED_SECRET}"
    assert Redactor([UNMATCHED_SECRET]).redact(f"key={UNMATCHED_SECRET}") == f"key={MASKED}"


@pytest.mark.parametrize(
    "text, expected",
    [
        ("auth sk-test-abcdefgh12345 done", f"auth {MASKED} done"),
        ("Authorization: Bearer abc.def", f"Authorization: Bearer {MASKED}"),
        ("x-api-key: 12345678", f"x-api-key: {MASKED}"),
        ("X-TrustAgent-Key: ta_live", f"X-TrustAgent-Key: {MASKED}"),
        ("cookie ta_session=abc.def; Path=/", f"cookie ta_session={MASKED}; Path=/"),
        ("GET /ws?ticket=t0k3n&x=1", f"GET /ws?ticket={MASKED}&x=1"),
        ("key ta_" + "A" * 43, f"key {MASKED}"),
    ],
)
def test_the_gateway_patterns_are_masked(text: str, expected: str) -> None:
    assert redact_patterns(text) == expected


def test_settings_secrets_are_the_non_empty_secret_settings() -> None:
    values = settings(anthropic_api_key=UNMATCHED_SECRET, internal_token="internal-token-value")
    assert sorted(settings_secrets(values)) == sorted([UNMATCHED_SECRET, "internal-token-value"])


async def test_logs_never_carry_a_secret(caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.DEBUG)
    root = logging.getLogger()
    saved = [(handler, handler.formatter, list(handler.filters)) for handler in root.handlers]
    redactor = Redactor([UNMATCHED_SECRET])
    install_redaction(redactor)
    try:
        # A pushed secret joins the redactor when the push is applied.
        manager = manager_for(FakeJudge(), redactor=redactor)
        config = push(1, secret=DASHBOARD_KEY.replace("sk-or-v1-", "orkey-"))
        await manager.apply(config)
        pushed = config.credentials["openrouter"].secret

        child = logging.getLogger("truthtable.tests.redaction.child")
        child.info("env key %s in use", UNMATCHED_SECRET)
        try:
            raise RuntimeError(f"upstream refused {UNMATCHED_SECRET} and {pushed}")
        except RuntimeError:
            child.exception("call failed")
        logging.getLogger("truthtable.tests.redaction").debug("push %s", config)
    finally:
        for handler, formatter, filters in saved:
            handler.setFormatter(formatter)
            handler.filters = filters

    assert len(caplog.records) >= 3
    for record in caplog.records:
        rendered = record.getMessage() + (record.exc_text or "")
        for secret in (UNMATCHED_SECRET, pushed):
            assert_absent(secret, rendered, f"the log record {record.name}")
    for secret in (UNMATCHED_SECRET, pushed):
        assert_absent(secret, caplog.text, "the captured log text")
    assert MASKED in caplog.text
    assert "call failed" in caplog.text and "RuntimeError" in caplog.text
