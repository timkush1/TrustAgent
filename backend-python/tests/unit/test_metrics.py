"""
Engine metrics (docs/v2/SPEC.md Appendix B, M11): every name is
trustagent_engine_*, and the audit, judge, RPC, gate and knowledge-base
metrics are recorded where the gRPC server does the work.
"""

import asyncio
from typing import Any, Dict, Iterator, List, Optional, Tuple

import pytest
from prometheus_client import REGISTRY, Metric
from prometheus_client.metrics import MetricWrapperBase

from truthtable import metrics
from truthtable.config import Settings
from truthtable.engine_info import EngineContext
from truthtable.grpc import server as server_mod
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.server import AuditServicer
from truthtable.judge import JudgeSelection
from truthtable.providers.costs import CallCost

PREFIX = "trustagent_engine_"


def _value(name: str, **labels: str) -> float:
    return REGISTRY.get_sample_value(name, labels) or 0.0


def _engine_metrics() -> Iterator[Tuple[str, MetricWrapperBase]]:
    for attr in dir(metrics):
        value = getattr(metrics, attr)
        if isinstance(value, MetricWrapperBase):
            yield attr, value


def test_every_engine_metric_is_named_trustagent_engine() -> None:
    declared = list(_engine_metrics())
    assert len(declared) == 14
    for attr, metric in declared:
        families: List[Metric] = list(metric.describe())
        assert families, attr
        for family in families:
            assert family.name.startswith(PREFIX), f"{attr}: {family.name}"


def test_no_other_trustagent_name_is_registered() -> None:
    # The server module registers nothing of its own: everything comes from
    # truthtable.metrics, so the gateway's trustagent_gateway_* names never clash.
    assert server_mod.AUDITS_TOTAL is metrics.AUDITS_TOTAL
    names = [family.name for family in REGISTRY.collect()]
    ours = [n for n in names if n.startswith(("trustagent", "truthtable"))]
    assert ours
    assert [n for n in ours if not n.startswith(PREFIX)] == []


def test_spec_appendix_b_labels() -> None:
    expected = {
        "AUDITS_TOTAL": ("status",),
        "CLAIMS_TOTAL": ("verdict",),
        "STAGE_DURATION": ("stage",),
        "JUDGE_REQUESTS": ("provider", "outcome"),
        "JUDGE_TOKENS": ("provider", "direction"),
        "JUDGE_COST_USD": ("provider",),
        "RPC_INFLIGHT": ("method",),
        "GATE_VS_AUDIT": ("gate_action", "audit_verdict"),
        "KB_INGEST_CLAIMS": ("status",),
    }
    for attr, labels in expected.items():
        assert getattr(metrics, attr)._labelnames == labels, attr  # noqa: SLF001


def _result(status: int, verdict: int, action: Optional[str] = "flag") -> pb.AuditResult:
    result = pb.AuditResult(audit_id="a1", status=status, verdict=verdict)
    if action is not None:
        result.gate.CopyFrom(pb.GateDecision(action=action, mode="flag", route="support-bot"))
    return result


def _call(provider: str, status: str = "ok", cost: Optional[float] = 0.002) -> CallCost:
    return CallCost(
        component="deep_audit_verify",
        provider=provider,
        model="m",
        input_tokens=120,
        output_tokens=30,
        cost_usd=cost,
        cost_source="table",
        latency_ms=40,
        status=status,
    )


def test_evaluate_metrics_record_outcome_stages_judge_and_gate() -> None:
    state: Dict[str, Any] = {
        "claim_verifications": [{"claim": "c", "status": "supported"}],
        "step_timings": {"decompose_ms": 250, "verify_ms": 1500, "total_ms": 1800, "odd": 5},
    }
    before = {
        "degraded": _value("trustagent_engine_audits_total", status="degraded"),
        "claims": _value("trustagent_engine_claims_total", verdict="supported"),
        "verify": _value("trustagent_engine_stage_duration_seconds_count", stage="verify"),
        "verify_sum": _value("trustagent_engine_stage_duration_seconds_sum", stage="verify"),
        "total": _value("trustagent_engine_stage_duration_seconds_count", stage="total"),
        "ok": _value("trustagent_engine_judge_requests_total", provider="p1", outcome="ok"),
        "err": _value("trustagent_engine_judge_requests_total", provider="p1", outcome="error"),
        "in": _value("trustagent_engine_judge_tokens_total", provider="p1", direction="input"),
        "out": _value("trustagent_engine_judge_tokens_total", provider="p1", direction="output"),
        "usd": _value("trustagent_engine_judge_cost_usd_total", provider="p1"),
        "gate": _value(
            "trustagent_engine_gate_vs_audit_total", gate_action="flag", audit_verdict="grounded"
        ),
    }
    result = _result(pb.AUDIT_STATUS_DEGRADED, pb.AUDIT_VERDICT_GROUNDED)
    calls = [_call("p1"), _call("p1", status="error", cost=None)]

    AuditServicer._record_evaluate_metrics(result, state, 1.9, calls)  # noqa: SLF001

    assert _value("trustagent_engine_audits_total", status="degraded") == before["degraded"] + 1
    assert _value("trustagent_engine_claims_total", verdict="supported") == before["claims"] + 1
    assert (
        _value("trustagent_engine_stage_duration_seconds_count", stage="verify")
        == before["verify"] + 1
    )
    assert _value("trustagent_engine_stage_duration_seconds_sum", stage="verify") == pytest.approx(
        before["verify_sum"] + 1.5
    )
    # total_ms is the audit's own duration, not a stage; unknown keys are ignored.
    assert _value("trustagent_engine_stage_duration_seconds_count", stage="total") == 0
    assert (
        _value("trustagent_engine_judge_requests_total", provider="p1", outcome="ok")
        == before["ok"] + 1
    )
    assert (
        _value("trustagent_engine_judge_requests_total", provider="p1", outcome="error")
        == before["err"] + 1
    )
    assert (
        _value("trustagent_engine_judge_tokens_total", provider="p1", direction="input")
        == before["in"] + 240
    )
    assert (
        _value("trustagent_engine_judge_tokens_total", provider="p1", direction="output")
        == before["out"] + 60
    )
    # Only the known cost counts.
    assert _value("trustagent_engine_judge_cost_usd_total", provider="p1") == pytest.approx(
        before["usd"] + 0.002
    )
    assert (
        _value(
            "trustagent_engine_gate_vs_audit_total", gate_action="flag", audit_verdict="grounded"
        )
        == before["gate"] + 1
    )


@pytest.mark.parametrize(
    "status,verdict,action,label",
    [
        (
            pb.AUDIT_STATUS_COMPLETED,
            pb.AUDIT_VERDICT_HALLUCINATED,
            "block",
            ("block", "hallucinated"),
        ),
        (pb.AUDIT_STATUS_FAILED, pb.AUDIT_VERDICT_UNSPECIFIED, "pass", ("pass", "failed")),
        (
            pb.AUDIT_STATUS_COMPLETED,
            pb.AUDIT_VERDICT_NOT_ASSESSED,
            "fallback",
            ("fallback", "not_assessed"),
        ),
        (pb.AUDIT_STATUS_COMPLETED, pb.AUDIT_VERDICT_NO_CLAIMS, "surprise", ("other", "no_claims")),
    ],
)
def test_gate_vs_audit_labels_are_bounded(
    status: int, verdict: int, action: str, label: Tuple[str, str]
) -> None:
    name = "trustagent_engine_gate_vs_audit_total"
    before = _value(name, gate_action=label[0], audit_verdict=label[1])
    server_mod._record_gate_vs_audit(_result(status, verdict, action))  # noqa: SLF001
    assert _value(name, gate_action=label[0], audit_verdict=label[1]) == before + 1


def test_an_audit_without_a_gate_decision_is_not_counted_against_the_gate() -> None:
    name = "trustagent_engine_gate_vs_audit_total"
    samples_before = sum(
        s.value for m in REGISTRY.collect() if m.name == name[: -len("_total")] for s in m.samples
    )
    server_mod._record_gate_vs_audit(  # noqa: SLF001
        _result(pb.AUDIT_STATUS_COMPLETED, pb.AUDIT_VERDICT_GROUNDED, action=None)
    )
    samples_after = sum(
        s.value for m in REGISTRY.collect() if m.name == name[: -len("_total")] for s in m.samples
    )
    assert samples_after == samples_before


async def test_rpc_inflight_counts_a_call_while_it_is_served() -> None:
    gate = asyncio.Event()
    seen: List[float] = []

    @server_mod._inflight("Probe")  # noqa: SLF001
    async def probe(fail: bool) -> str:
        seen.append(_value("trustagent_engine_rpc_inflight", method="Probe"))
        await gate.wait()
        if fail:
            raise RuntimeError("boom")
        return "done"

    before = _value("trustagent_engine_rpc_inflight", method="Probe")
    task = asyncio.create_task(probe(False))
    await asyncio.sleep(0)
    assert seen == [before + 1]
    gate.set()
    assert await task == "done"
    assert _value("trustagent_engine_rpc_inflight", method="Probe") == before
    with pytest.raises(RuntimeError):
        await probe(True)
    assert _value("trustagent_engine_rpc_inflight", method="Probe") == before
    assert probe.__name__ == "probe"


def test_every_served_rpc_is_counted_in_flight() -> None:
    served = [
        "Evaluate",
        "SubmitAudit",
        "GetAuditResult",
        "HealthCheck",
        "GetEngineInfo",
        "RetrieveContext",
        "IngestDocuments",
        "ListKBClaims",
        "ListConflicts",
        "GetKBStats",
    ]
    for method in served:
        handler = AuditServicer.__dict__[method]
        assert hasattr(handler, "__wrapped__"), method
        assert asyncio.iscoroutinefunction(handler), method


def _engine(fallback_active: bool) -> EngineContext:
    judge = JudgeSelection(
        provider=None,
        configured="anthropic",
        effective="openrouter",
        model="m",
        fallback_active=fallback_active,
    )
    return EngineContext(settings=Settings(_env_file=None), judge=judge)


def test_judge_fallback_gauge_follows_the_engine() -> None:
    AuditServicer(audit_graph=None, engine=_engine(True))
    assert _value("trustagent_engine_judge_fallback_active") == 1
    AuditServicer(audit_graph=None, engine=_engine(False))
    assert _value("trustagent_engine_judge_fallback_active") == 0


async def test_evaluate_counts_the_gate_against_the_audit(monkeypatch: pytest.MonkeyPatch) -> None:
    async def fake_run_audit(**kwargs: Any) -> Dict[str, Any]:
        return {
            "request_id": kwargs["request_id"],
            "verdict": "HALLUCINATED",
            "audit_status": "completed",
            "claim_verifications": [{"claim": "c", "status": "contradicted"}],
            "step_timings": {"score_ms": 3},
        }

    monkeypatch.setattr(server_mod, "run_audit", fake_run_audit)
    name = "trustagent_engine_gate_vs_audit_total"
    before = _value(name, gate_action="pass", audit_verdict="hallucinated")
    before_hall = _value("trustagent_engine_hallucinations_detected_total")
    request = pb.AuditRequest(
        request_id="r1",
        query="q",
        response="a",
        gate=pb.GateDecision(action="pass", mode="observe", route="support-bot"),
    )
    result = await AuditServicer(audit_graph=None).Evaluate(request, None)
    assert result.verdict == pb.AUDIT_VERDICT_HALLUCINATED
    assert _value(name, gate_action="pass", audit_verdict="hallucinated") == before + 1
    assert _value("trustagent_engine_hallucinations_detected_total") == before_hall + 1
