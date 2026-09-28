"""
Engine startup (SPEC section 3.2 `main.py`): LangSmith honoured only with
LANGSMITH_TRACING (D19), strict configuration, the non-loopback bind rule, the
knowledge add-on switched off cleanly, and a real `python -m truthtable.main`
run with the mock judge answering GetEngineInfo, HealthCheck and Evaluate over
loopback with the internal token.
"""

import logging
import os
import signal
import socket
import subprocess
import sys
import time
from pathlib import Path
from typing import Any, Dict, Iterator

import grpc
import pytest

from truthtable import main as main_mod
from truthtable.config import Settings
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.internal_auth import INTERNAL_TOKEN_METADATA
from truthtable.providers import MockLLMProvider
from truthtable.providers.pricing import set_active_price_table

BACKEND_DIR = Path(__file__).resolve().parents[2]
logger = logging.getLogger(__name__)


@pytest.fixture(autouse=True)
def isolated(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    for name in list(os.environ):
        if name.startswith(("TRUTHTABLE_", "LANGSMITH_", "LANGCHAIN_", "LLM_", "GRPC_")):
            monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("TRUTHTABLE_ENV_FILE", "")
    monkeypatch.chdir(tmp_path)


def settings(**values: Any) -> Settings:
    return Settings(_env_file=None, **values)


# ---------------------------------------------------------------------------
# LangSmith (D19)
# ---------------------------------------------------------------------------


def test_langsmith_stays_off_without_the_flag() -> None:
    assert main_mod.configure_langsmith(settings(langsmith_api_key="ls-key"), logger) is False
    assert "LANGCHAIN_TRACING_V2" not in os.environ
    assert "LANGCHAIN_API_KEY" not in os.environ


def test_langsmith_needs_a_key() -> None:
    assert main_mod.configure_langsmith(settings(langsmith_tracing=True), logger) is False
    assert "LANGCHAIN_TRACING_V2" not in os.environ


def test_langsmith_tracing_on(monkeypatch: pytest.MonkeyPatch) -> None:
    # Registered with monkeypatch first, so the variables the call sets are undone.
    for prefix in ("LANGCHAIN", "LANGSMITH"):
        for suffix in ("API_KEY", "PROJECT"):
            monkeypatch.setenv(f"{prefix}_{suffix}", "placeholder")
    monkeypatch.setenv("LANGCHAIN_TRACING_V2", "placeholder")
    monkeypatch.setenv("LANGSMITH_TRACING", "placeholder")
    s = settings(langsmith_tracing=True, langsmith_api_key="ls-key", langsmith_project="p")

    assert main_mod.configure_langsmith(s, logger) is True
    assert os.environ["LANGCHAIN_TRACING_V2"] == "true"
    assert os.environ["LANGCHAIN_API_KEY"] == os.environ["LANGSMITH_API_KEY"] == "ls-key"
    assert os.environ["LANGCHAIN_PROJECT"] == "p"


# ---------------------------------------------------------------------------
# init_provider, knowledge, price table
# ---------------------------------------------------------------------------


async def test_init_provider_keeps_its_tuple_contract() -> None:
    provider, effective = await main_mod.init_provider(settings(llm_provider="mock"), logger)
    assert effective == "mock" and isinstance(provider, MockLLMProvider)

    provider, effective = await main_mod.init_provider(settings(llm_provider="none"), logger)
    assert (provider, effective) == (None, "none")


@pytest.mark.parametrize(
    "values, reason",
    [
        ({}, "disabled"),
        ({"qdrant_url": "http://qdrant:6333"}, "disabled"),  # QDRANT_URL alone does nothing
        ({"knowledge_enabled": True}, "qdrant_url_unset"),
    ],
)
def test_knowledge_stays_off(values: Dict[str, Any], reason: str) -> None:
    *components, state = main_mod.init_knowledge(settings(**values), None, logger)
    assert components == [None, None, None, None]
    assert (state.enabled, state.reason) == (False, reason)


def test_bad_price_table_file_keeps_the_engine_running(tmp_path: Path) -> None:
    try:
        main_mod.load_price_table(settings(price_table_file=str(tmp_path / "nope.json")), logger)
        main_mod.load_price_table(settings(), logger)  # the contracts table
    finally:
        set_active_price_table(None)


# ---------------------------------------------------------------------------
# main(): refusals
# ---------------------------------------------------------------------------


async def test_strict_config_refuses_to_start(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("TRUTHTABLE_STRICT_CONFIG", "true")
    monkeypatch.setenv("TRUTHTABLE_JEV_MODLE", "typo")

    with pytest.raises(SystemExit) as exc_info:
        await main_mod.main()

    assert exc_info.value.code == 2


async def test_non_loopback_bind_without_a_token_refuses_to_start(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setenv("GRPC_HOST", "0.0.0.0")
    monkeypatch.setenv("TRUTHTABLE_INTERNAL_TOKEN_FILE", str(tmp_path / "missing.token"))
    real_check_bind = main_mod.check_bind

    async def quick_check_bind(host: str, source: Any) -> None:
        await real_check_bind(host, source, wait_s=0.01, poll_s=0.005)

    monkeypatch.setattr(main_mod, "check_bind", quick_check_bind)

    with pytest.raises(SystemExit) as exc_info:
        await main_mod.main()

    assert exc_info.value.code == 2


# ---------------------------------------------------------------------------
# A real engine process (mock judge, loopback, internal token)
# ---------------------------------------------------------------------------


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.fixture
def engine_process(tmp_path: Path) -> Iterator[Dict[str, Any]]:
    grpc_port, metrics_port = _free_port(), _free_port()
    token_file = tmp_path / "internal.token"
    token_file.write_text("smoke-token\n", encoding="utf-8")
    env = {
        "PATH": os.environ.get("PATH", ""),
        "PYTHONPATH": str(BACKEND_DIR / "src"),
        "TRUTHTABLE_ENV_FILE": "",
        "TRUTHTABLE_STRICT_CONFIG": "true",
        "LLM_PROVIDER": "mock",
        "GRPC_HOST": "127.0.0.1",
        "GRPC_PORT": str(grpc_port),
        "METRICS_PORT": str(metrics_port),
        "TRUTHTABLE_INTERNAL_TOKEN_FILE": str(token_file),
        "TRUTHTABLE_ENGINE_MAX_AUDITS": "3",
    }
    process = subprocess.Popen(
        [sys.executable, "-m", "truthtable.main"],
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    target = f"127.0.0.1:{grpc_port}"
    try:
        deadline = time.monotonic() + 60
        with grpc.insecure_channel(target) as channel:
            while True:
                if process.poll() is not None:
                    pytest.fail(f"engine exited early:\n{process.stdout.read()}")
                try:
                    grpc.channel_ready_future(channel).result(timeout=1)
                    break
                except grpc.FutureTimeoutError:
                    if time.monotonic() > deadline:
                        pytest.fail("engine did not start within 60 s")
        yield {"target": target, "token": "smoke-token", "metrics_port": metrics_port}
    finally:
        process.send_signal(signal.SIGTERM)
        try:
            process.wait(timeout=20)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def test_engine_process_serves_with_the_mock_judge(engine_process: Dict[str, Any]) -> None:
    metadata = ((INTERNAL_TOKEN_METADATA, engine_process["token"]),)
    with grpc.insecure_channel(engine_process["target"]) as channel:
        stub = pb_grpc.AuditServiceStub(channel)

        with pytest.raises(grpc.RpcError) as exc_info:
            stub.HealthCheck(pb.HealthRequest(), timeout=5)
        assert exc_info.value.code() == grpc.StatusCode.UNAUTHENTICATED

        health = stub.HealthCheck(pb.HealthRequest(), timeout=5, metadata=metadata)
        assert health.healthy is True
        assert dict(health.dependencies) == {"judge": True}
        assert (health.judge_provider, health.judge_model) == ("mock", "mock")

        info = stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=5, metadata=metadata)
        assert info.judge.effective_provider == "mock"
        assert info.judge.configured_provider == "mock"
        assert (info.max_audits, info.max_rpcs) == (3, 64)
        assert list(info.warnings) == []

        policy = pb.ContextDocument(
            id="refund-policy.md",
            source="request_system",
            content="Annual plans can be refunded within 30 days of purchase. "
            "Monthly plans are non-refundable.",
        )
        result = stub.Evaluate(
            pb.AuditRequest(
                request_id="smoke-1",
                query="Can I still get a refund on my annual plan? I bought it 45 days ago.",
                response="You can get a full refund within 60 days of purchase.",
                options=pb.AuditOptions(mock_scenario="refund-contradiction-flag"),
                context=[policy],
            ),
            timeout=10,
            metadata=metadata,
        )
        assert result.verdict == pb.AUDIT_VERDICT_HALLUCINATED
        assert [c.status for c in result.claims] == [pb.VERIFICATION_STATUS_CONTRADICTED]
        assert result.judge.effective_provider == "mock"

        # A strict fixture miss fails the audit rather than inventing a verdict.
        missed = stub.Evaluate(
            pb.AuditRequest(
                request_id="smoke-2",
                response="Something else entirely.",
                options=pb.AuditOptions(mock_scenario="refund-contradiction-flag"),
                context=[policy],
            ),
            timeout=10,
            metadata=metadata,
        )
        assert missed.status == pb.AUDIT_STATUS_FAILED
        assert missed.error_kind == "mock_fixture_missing"
