"""
GetEngineInfo, HealthCheck keys and the engine concurrency limits (SPEC
section 2.2; D10, D12, C-H13), against the real servicer on an in-process
grpc.aio server.
"""

import asyncio
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Dict, List, Optional

import grpc
import pytest
from grpc import aio

from truthtable import __version__
from truthtable.config import Settings
from truthtable.engine_info import (
    EngineContext,
    KnowledgeState,
    engine_info,
    knowledge_state_for,
    verifier_resolution,
)
from truthtable.grpc import server as server_mod
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer, create_server
from truthtable.judge import JudgeSelection
from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider

RPC_TIMEOUT_S = 5


class FakeJudge(LLMProvider):
    provider_name = "anthropic"

    def __init__(self, healthy: bool = True, structured: bool = True):
        super().__init__(model="claude-haiku-4-5")
        self.healthy = healthy
        self.structured = structured

    @property
    def supports_structured_outputs(self) -> bool:
        return self.structured

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        raise AssertionError("no LLM call expected")

    async def health_check(self) -> bool:
        return self.healthy


class FakeQdrant:
    def __init__(self, healthy: bool = True, points: int = 42):
        self.healthy = healthy
        self.points = points

    def health_check(self) -> bool:
        return self.healthy

    def count(self) -> int:
        return self.points


def settings(**values: Any) -> Settings:
    return Settings(_env_file=None, **values)


def selection(judge: Optional[LLMProvider] = None, **values: Any) -> JudgeSelection:
    if judge is None and "effective" not in values:
        return JudgeSelection.no_judge("auto", "no judge configured")
    fields: Dict[str, Any] = dict(
        provider=judge,
        configured="auto",
        effective="anthropic",
        model="claude-haiku-4-5",
        credential_source="env",
        healthy=True,
    )
    fields.update(values)
    return JudgeSelection(**fields)


@asynccontextmanager
async def serving(servicer: AuditServicer, **kwargs: Any) -> AsyncIterator[Any]:
    server = create_server(servicer, host="127.0.0.1", port=0, **kwargs)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = aio.insecure_channel(f"127.0.0.1:{port}")
    try:
        yield pb_grpc.AuditServiceStub(channel)
    finally:
        await channel.close()
        await server.stop(grace=None)


def servicer_for(
    judge: Optional[LLMProvider] = None,
    *,
    s: Optional[Settings] = None,
    judge_selection: Optional[JudgeSelection] = None,
    knowledge: Optional[KnowledgeState] = None,
    qdrant: Any = None,
    warnings: Optional[List[str]] = None,
) -> AuditServicer:
    s = s or settings()
    engine = EngineContext(
        settings=s,
        judge=judge_selection or selection(judge),
        knowledge=knowledge or knowledge_state_for(s),
        warnings=list(warnings or []),
    )
    return AuditServicer(audit_graph=None, provider=judge, qdrant_store=qdrant, engine=engine)


# ---------------------------------------------------------------------------
# GetEngineInfo
# ---------------------------------------------------------------------------


async def test_engine_info_reports_judge_limits_and_scoring() -> None:
    judge = FakeJudge()
    s = settings(engine_max_audits=8, engine_max_rpcs=32, scoring_grade_cutoffs="0.95,0.8,0.6,0.4")
    servicer = servicer_for(judge, s=s, warnings=["TRUTHTABLE_X: unknown key"])

    async with serving(servicer) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)

    assert info.version == __version__
    assert (info.max_audits, info.max_rpcs) == (8, 32)
    assert info.judge == pb.JudgeInfo(
        configured_provider="auto",
        effective_provider="anthropic",
        model="claude-haiku-4-5",
        fallback_active=False,
        fallback_reason="",
        structured_outputs=True,
        healthy=True,
        credential_source="env",
    )
    assert (info.verifier_configured, info.verifier_effective) == ("auto", "llm")
    assert "2.0" in info.verifier_reason
    assert list(info.scoring.grade_cutoffs) == pytest.approx([0.95, 0.8, 0.6, 0.4])
    assert info.scoring.partial_weight == pytest.approx(0.5)
    assert info.scoring.contradiction_flag == pytest.approx(0.35)
    assert info.scoring.unsupported_flag == pytest.approx(0.60)
    assert info.scoring.unsupported_fraction_flag == pytest.approx(0.30)
    assert info.scoring.min_coverage == pytest.approx(0.5)
    assert info.scoring.max_unknown_fraction == pytest.approx(0.5)
    assert info.scoring.unsupported_policy == "auto"
    assert info.scoring.calibrated is False
    assert list(info.warnings) == ["TRUTHTABLE_X: unknown key"]
    assert info.knowledge == pb.KnowledgeInfo(enabled=False, reason="disabled")
    assert not info.HasField("decision_model")  # v2.1


async def test_engine_info_probes_the_judge_each_time() -> None:
    judge = FakeJudge(healthy=False)

    async with serving(servicer_for(judge)) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
        assert info.judge.healthy is False
        judge.healthy = True
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
        assert info.judge.healthy is True


async def test_engine_info_reports_an_active_fallback() -> None:
    judge = FakeJudge()
    judge_selection = selection(
        judge,
        effective="ollama",
        model="llama3.2:1b",
        credential_source="none",
        fallback_active=True,
        fallback_reason="anthropic failed its startup health check",
        warnings=["judge fallback active: anthropic -> ollama (llama3.2:1b)"],
    )

    async with serving(servicer_for(judge, judge_selection=judge_selection)) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)

    assert info.judge.effective_provider == "ollama"
    assert info.judge.fallback_active is True
    assert info.judge.fallback_reason == "anthropic failed its startup health check"
    assert "judge fallback active" in info.warnings[0]


async def test_engine_info_without_a_judge() -> None:
    async with serving(servicer_for(None)) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)

    assert info.judge.effective_provider == "none"
    assert info.judge.healthy is False
    assert info.verifier_effective == "none"
    assert "no judge configured" in list(info.warnings)


async def test_engine_info_knowledge_state() -> None:
    s = settings(knowledge_enabled=True, qdrant_url="http://qdrant:6333")

    async with serving(servicer_for(FakeJudge(), s=s, qdrant=FakeQdrant(points=7))) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
    assert info.knowledge == pb.KnowledgeInfo(
        enabled=True,
        qdrant_reachable=True,
        embedding_model="all-MiniLM-L6-v2",
        kb_claims=7,
        reason="",
    )

    down = FakeQdrant(healthy=False)
    async with serving(servicer_for(FakeJudge(), s=s, qdrant=down)) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
    assert info.knowledge.qdrant_reachable is False
    assert info.knowledge.reason == "qdrant_unreachable"
    assert "knowledge add-on enabled but not running: qdrant_unreachable" in info.warnings


def test_knowledge_state_for_settings() -> None:
    assert knowledge_state_for(settings()).reason == "disabled"
    assert knowledge_state_for(settings(knowledge_enabled=True)).reason == "qdrant_url_unset"
    on = knowledge_state_for(settings(knowledge_enabled=True, qdrant_url="http://q:6333"))
    assert on.enabled and on.reason == "" and on.embedding_model == "all-MiniLM-L6-v2"


def test_verifier_resolution() -> None:
    judge = selection(FakeJudge())
    assert verifier_resolution(settings(verifier="llm"), judge) == ("llm", "llm", "")
    assert verifier_resolution(settings(), selection(None))[1] == "none"


def test_engine_info_builder_is_pure() -> None:
    context = EngineContext(settings=settings(), judge=selection(FakeJudge()))
    info = engine_info(context, judge_healthy=None, qdrant_reachable=False, kb_claims=-3)
    assert info.judge.healthy is False
    assert info.knowledge.kb_claims == 0
    assert (info.max_audits, info.max_rpcs) == (16, 64)


async def test_bare_servicer_reports_defaults(monkeypatch: pytest.MonkeyPatch) -> None:
    async with serving(AuditServicer(audit_graph=None)) as stub:
        info = await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
    assert info.version == __version__
    assert (info.max_audits, info.max_rpcs) == (16, 64)


# ---------------------------------------------------------------------------
# HealthCheck (D10)
# ---------------------------------------------------------------------------


async def test_health_keys_are_judge_and_qdrant() -> None:
    s = settings(knowledge_enabled=True, qdrant_url="http://qdrant:6333")
    servicer = servicer_for(FakeJudge(), s=s, qdrant=FakeQdrant(points=5))

    async with serving(servicer) as stub:
        health = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert dict(health.dependencies) == {"judge": True, "qdrant": True}
    assert health.healthy is True
    assert (health.judge_provider, health.judge_model) == ("anthropic", "claude-haiku-4-5")
    assert health.judge_fallback_active is False
    assert health.knowledge_enabled is True
    assert health.kb_claims == 5


async def test_unreachable_qdrant_makes_the_engine_unhealthy() -> None:
    servicer = servicer_for(FakeJudge(), qdrant=FakeQdrant(healthy=False))

    async with serving(servicer) as stub:
        health = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert dict(health.dependencies) == {"judge": True, "qdrant": False}
    assert health.healthy is False


async def test_no_judge_is_reported_unhealthy() -> None:
    async with serving(servicer_for(None)) as stub:
        health = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert dict(health.dependencies) == {"judge": False}
    assert health.healthy is False
    assert health.judge_provider == "none"


async def test_a_hanging_qdrant_probe_is_bounded(monkeypatch: pytest.MonkeyPatch) -> None:
    class HangingQdrant(FakeQdrant):
        def health_check(self) -> bool:
            import time

            time.sleep(1.0)
            return True

    monkeypatch.setattr(server_mod, "QDRANT_PROBE_TIMEOUT_S", 0.05)
    async with serving(servicer_for(FakeJudge(), qdrant=HangingQdrant())) as stub:
        health = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert dict(health.dependencies)["qdrant"] is False


# ---------------------------------------------------------------------------
# Evaluate without a judge; AuditResult.judge
# ---------------------------------------------------------------------------


async def test_evaluate_without_a_judge_is_not_assessed(monkeypatch: pytest.MonkeyPatch) -> None:
    async def must_not_run(**kwargs: Any) -> Dict[str, Any]:
        raise AssertionError("the audit graph must not run without a judge")

    monkeypatch.setattr(server_mod, "run_audit", must_not_run)
    request = pb.AuditRequest(
        request_id="req-1",
        trace_id="trace-1",
        audit_id="audit-1",
        response="Paris is the capital of France.",
        context=[pb.ContextDocument(id="d1", content="Paris is the capital of France.")],
        gate=pb.GateDecision(action="pass"),
    )

    async with serving(servicer_for(None)) as stub:
        result = await stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == pb.AUDIT_VERDICT_NOT_ASSESSED
    assert result.not_assessed_reason == "no_judge"
    assert (result.audit_id, result.trace_id, result.request_id) == ("audit-1", "trace-1", "req-1")
    assert result.gate.action == "pass"
    assert not result.HasField("scores")
    assert result.judge.effective_provider == "none"


async def test_evaluate_results_carry_the_judge(monkeypatch: pytest.MonkeyPatch) -> None:
    async def fake_run_audit(**kwargs: Any) -> Dict[str, Any]:
        return {
            "request_id": kwargs["request_id"],
            "verdict": "GROUNDED",
            "audit_status": "completed",
            "faithfulness_score": 1.0,
            "claim_verifications": [],
        }

    monkeypatch.setattr(server_mod, "run_audit", fake_run_audit)

    async with serving(servicer_for(FakeJudge())) as stub:
        result = await stub.Evaluate(pb.AuditRequest(request_id="r"), timeout=RPC_TIMEOUT_S)

    assert result.verdict == pb.AUDIT_VERDICT_GROUNDED
    assert result.judge.effective_provider == "anthropic"
    assert result.judge.model == "claude-haiku-4-5"
    assert result.judge.healthy is True


# ---------------------------------------------------------------------------
# Concurrency limits (C-H13)
# ---------------------------------------------------------------------------


async def test_evaluate_runs_at_most_max_audits_at_once(monkeypatch: pytest.MonkeyPatch) -> None:
    running = 0
    peak = 0

    async def slow_run_audit(**kwargs: Any) -> Dict[str, Any]:
        nonlocal running, peak
        running += 1
        peak = max(peak, running)
        await asyncio.sleep(0.05)
        running -= 1
        return {
            "request_id": kwargs["request_id"],
            "verdict": "NO_CLAIMS",
            "claim_verifications": [],
        }

    monkeypatch.setattr(server_mod, "run_audit", slow_run_audit)
    servicer = servicer_for(FakeJudge(), s=settings(engine_max_audits=2))
    assert servicer.max_audits == 2

    async with serving(servicer) as stub:
        results = await asyncio.gather(
            *(
                stub.Evaluate(pb.AuditRequest(request_id=f"r{i}"), timeout=RPC_TIMEOUT_S)
                for i in range(6)
            )
        )

    assert len(results) == 6
    assert peak == 2


async def test_rpcs_beyond_max_rpcs_are_rejected(monkeypatch: pytest.MonkeyPatch) -> None:
    release = asyncio.Event()

    async def blocked_run_audit(**kwargs: Any) -> Dict[str, Any]:
        await release.wait()
        return {
            "request_id": kwargs["request_id"],
            "verdict": "NO_CLAIMS",
            "claim_verifications": [],
        }

    monkeypatch.setattr(server_mod, "run_audit", blocked_run_audit)
    servicer = servicer_for(FakeJudge())

    async with serving(servicer, max_rpcs=1) as stub:
        first = asyncio.ensure_future(
            stub.Evaluate(pb.AuditRequest(request_id="r1"), timeout=RPC_TIMEOUT_S)
        )
        await asyncio.sleep(0.2)  # the first call now holds the only RPC slot
        with pytest.raises(aio.AioRpcError) as exc_info:
            await stub.GetEngineInfo(pb.EngineInfoRequest(), timeout=RPC_TIMEOUT_S)
        release.set()
        await first

    assert exc_info.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED


async def test_result_judge_health_follows_the_audit(monkeypatch: pytest.MonkeyPatch) -> None:
    async def failing_run_audit(**kwargs: Any) -> Dict[str, Any]:
        raise RuntimeError("judge down")

    monkeypatch.setattr(server_mod, "run_audit", failing_run_audit)
    monkeypatch.setattr(server_mod, "_failure_kind", lambda exc: "judge_unavailable")

    async with serving(servicer_for(FakeJudge())) as stub:
        result = await stub.Evaluate(pb.AuditRequest(request_id="r"), timeout=RPC_TIMEOUT_S)

    assert result.status == pb.AUDIT_STATUS_FAILED
    assert result.judge.healthy is False
