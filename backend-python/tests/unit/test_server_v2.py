"""
AuditService v2 contract tests (docs/v2/SPEC.md section 2.2).

Each test starts the real AuditServicer on an in-process grpc.aio server bound
to an ephemeral loopback port and calls it through the generated stub, so the
regenerated stubs, the servicer and the wire encoding are exercised together.
No LLM, Qdrant or network access is needed: the audit graph is stubbed where an
RPC would run it.

At P1 the five new v2 RPCs are not implemented: the servicer inherits the
generated UNIMPLEMENTED handlers. The phases that implement them replace the
corresponding cases here with behaviour tests.
"""

from collections import OrderedDict
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Dict

import grpc
import pytest
from grpc import aio

from truthtable import __version__
from truthtable.graphs.state import VerificationStatus
from truthtable.grpc import server as server_mod
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer

RPC_TIMEOUT_S = 5

# New in v2; implemented in later phases (Evaluate/GetEngineInfo in S0-P2,
# RetrieveContext with the knowledge profile, ApplyRuntimeConfig and
# DraftEvaluator in P4).
NEW_RPCS: Dict[str, Any] = {
    "Evaluate": pb.AuditRequest(request_id="req-v2", query="q", response="r"),
    "GetEngineInfo": pb.EngineInfoRequest(),
    "RetrieveContext": pb.RetrieveContextRequest(query="q", top_k=3, max_chars=2000),
    "ApplyRuntimeConfig": pb.RuntimeConfig(version=1, epoch="epoch-1"),
    "DraftEvaluator": pb.DraftEvaluatorRequest(description="Flags refund promises"),
}

LEGACY_RPCS = {
    "SubmitAudit",
    "GetAuditResult",
    "HealthCheck",
    "IngestDocuments",
    "ListKBClaims",
    "ListConflicts",
    "GetKBStats",
}


@asynccontextmanager
async def serving(servicer: AuditServicer) -> AsyncIterator[pb_grpc.AuditServiceStub]:
    """Serve `servicer` on 127.0.0.1:<ephemeral> and yield a connected stub."""
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


@pytest.fixture
async def stub(monkeypatch: pytest.MonkeyPatch) -> AsyncIterator[pb_grpc.AuditServiceStub]:
    # A fresh legacy result store per test keeps Submit/Get cases independent.
    monkeypatch.setattr(server_mod, "_audit_results", OrderedDict())
    async with serving(AuditServicer(audit_graph=None)) as audit_stub:
        yield audit_stub


def _stub_run_audit(monkeypatch: pytest.MonkeyPatch, result: Dict[str, Any]) -> None:
    """Replace the LangGraph run with a canned AuditState."""

    async def fake_run_audit(**kwargs: Any) -> Dict[str, Any]:
        return {"request_id": kwargs["request_id"], **result}

    monkeypatch.setattr(server_mod, "run_audit", fake_run_audit)


# ---------------------------------------------------------------------------
# Contract shape
# ---------------------------------------------------------------------------


def test_service_declares_legacy_and_v2_rpcs() -> None:
    methods = {m.name: m for m in pb.DESCRIPTOR.services_by_name["AuditService"].methods}

    assert set(methods) == LEGACY_RPCS | set(NEW_RPCS)

    # The legacy polling pair is deprecated for external callers, not removed.
    deprecated = {name for name, m in methods.items() if m.GetOptions().deprecated}
    assert deprecated == {"SubmitAudit", "GetAuditResult"}

    # Evaluate reuses the v1 request/result messages.
    assert methods["Evaluate"].input_type.full_name == "truthtable.audit.v1.AuditRequest"
    assert methods["Evaluate"].output_type.full_name == "truthtable.audit.v1.AuditResult"
    assert methods["ApplyRuntimeConfig"].input_type.name == "RuntimeConfig"


def test_enum_values_are_additive() -> None:
    # v1 numbering is unchanged ...
    assert (
        pb.VERIFICATION_STATUS_UNSPECIFIED,
        pb.VERIFICATION_STATUS_SUPPORTED,
        pb.VERIFICATION_STATUS_CONTRADICTED,
        pb.VERIFICATION_STATUS_UNSUPPORTED,
        pb.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
    ) == (0, 1, 2, 3, 4)
    assert (pb.AUDIT_STATUS_COMPLETED, pb.AUDIT_STATUS_FAILED) == (3, 4)
    assert (
        pb.TRUST_GRADE_A,
        pb.TRUST_GRADE_B,
        pb.TRUST_GRADE_C,
        pb.TRUST_GRADE_D,
        pb.TRUST_GRADE_F,
    ) == (1, 2, 3, 4, 5)

    # ... and v2 only appends values (D6: UNKNOWN is no longer UNSPECIFIED on v2 RPCs).
    assert pb.VERIFICATION_STATUS_UNKNOWN == 5
    assert pb.VERIFICATION_STATUS_NOT_CHECKED == 6
    assert pb.AUDIT_STATUS_DEGRADED == 5
    assert pb.AUDIT_VERDICT_NOT_ASSESSED == 5


# ---------------------------------------------------------------------------
# New RPCs: UNIMPLEMENTED at P1
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("method, message", sorted(NEW_RPCS.items()), ids=sorted(NEW_RPCS))
async def test_new_rpc_is_unimplemented(
    stub: pb_grpc.AuditServiceStub, method: str, message: Any
) -> None:
    # The servicer must not shadow the generated default handler yet.
    assert getattr(AuditServicer, method) is getattr(pb_grpc.AuditServiceServicer, method)

    with pytest.raises(aio.AioRpcError) as exc_info:
        await getattr(stub, method)(message, timeout=RPC_TIMEOUT_S)

    assert exc_info.value.code() == grpc.StatusCode.UNIMPLEMENTED


# ---------------------------------------------------------------------------
# Existing RPCs keep working against the regenerated stubs
# ---------------------------------------------------------------------------


async def test_health_check(stub: pb_grpc.AuditServiceStub) -> None:
    response = await stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert response.healthy is True
    assert response.version == __version__


async def test_health_check_reports_unhealthy_provider() -> None:
    class UnhealthyProvider:
        async def health_check(self) -> bool:
            return False

    servicer = AuditServicer(audit_graph=None, provider=UnhealthyProvider())
    async with serving(servicer) as audit_stub:
        response = await audit_stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert response.healthy is False
    assert list(response.dependencies.values()) == [False]


async def test_legacy_submit_and_get_keep_v1_mapping(
    stub: pb_grpc.AuditServiceStub, monkeypatch: pytest.MonkeyPatch
) -> None:
    def claim(text: str, status: Any) -> Dict[str, Any]:
        return {"claim": text, "status": status, "confidence": 0.8, "evidence": [f"ev-{text}"]}

    _stub_run_audit(
        monkeypatch,
        {
            "faithfulness_score": 0.72,
            "hallucination_detected": True,
            "reasoning_trace": "trace",
            "step_timings": {"decompose_ms": 12.7, "verify_ms": 30},
            "claim_verifications": [
                claim("s", VerificationStatus.SUPPORTED),
                claim("c", VerificationStatus.CONTRADICTED),
                claim("u", VerificationStatus.UNSUPPORTED),
                claim("p", VerificationStatus.PARTIALLY_SUPPORTED),
                claim("k", VerificationStatus.UNKNOWN),
                claim("n", "not_checked"),
            ],
        },
    )

    submission = await stub.SubmitAudit(
        pb.AuditRequest(
            request_id="req-legacy",
            query="q",
            response="r",
            context=[pb.ContextDocument(id="d1", content="doc")],
        ),
        timeout=RPC_TIMEOUT_S,
    )
    assert submission.status == "completed"
    assert submission.audit_id

    result = await stub.GetAuditResult(
        pb.AuditResultRequest(audit_id=submission.audit_id), timeout=RPC_TIMEOUT_S
    )

    assert result.audit_id == submission.audit_id
    assert result.request_id == "req-legacy"
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.faithfulness_score == pytest.approx(0.72)
    assert result.grade == pb.TRUST_GRADE_B
    assert result.hallucination_detected is True
    assert result.reasoning_trace == "trace"
    assert dict(result.step_timings) == {"decompose_ms": 12, "verify_ms": 30}
    assert [c.claim for c in result.claims] == ["s", "c", "u", "p", "k", "n"]
    assert list(result.claims[0].evidence) == ["ev-s"]
    # CONTRADICTED stays distinct from UNSUPPORTED; UNKNOWN and NOT_CHECKED
    # collapse to UNSPECIFIED so old pollers never see a value they don't know.
    assert [c.status for c in result.claims] == [
        pb.VERIFICATION_STATUS_SUPPORTED,
        pb.VERIFICATION_STATUS_CONTRADICTED,
        pb.VERIFICATION_STATUS_UNSUPPORTED,
        pb.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
        pb.VERIFICATION_STATUS_UNSPECIFIED,
        pb.VERIFICATION_STATUS_UNSPECIFIED,
    ]
    # v2-only fields stay unset on the legacy path.
    assert result.verdict == pb.AUDIT_VERDICT_UNSPECIFIED
    assert not result.HasField("scores")
    assert not result.degraded_reasons


@pytest.mark.parametrize(
    "score, grade",
    [
        (1.0, pb.TRUST_GRADE_A),
        (0.9, pb.TRUST_GRADE_A),
        (0.89, pb.TRUST_GRADE_B),
        (0.7, pb.TRUST_GRADE_B),
        (0.69, pb.TRUST_GRADE_C),
        (0.5, pb.TRUST_GRADE_C),
        (0.49, pb.TRUST_GRADE_D),
        (0.3, pb.TRUST_GRADE_D),
        (0.29, pb.TRUST_GRADE_F),
        (0.0, pb.TRUST_GRADE_F),
    ],
)
async def test_legacy_grade_cutoffs(
    stub: pb_grpc.AuditServiceStub, monkeypatch: pytest.MonkeyPatch, score: float, grade: int
) -> None:
    # Cutoffs documented on TrustGrade: A>=0.9, B>=0.7, C>=0.5, D>=0.3, F<0.3 (D18).
    _stub_run_audit(monkeypatch, {"faithfulness_score": score, "claim_verifications": []})

    submission = await stub.SubmitAudit(pb.AuditRequest(request_id="r"), timeout=RPC_TIMEOUT_S)
    result = await stub.GetAuditResult(
        pb.AuditResultRequest(audit_id=submission.audit_id), timeout=RPC_TIMEOUT_S
    )

    assert result.grade == grade


async def test_legacy_submit_reports_pipeline_failure(
    stub: pb_grpc.AuditServiceStub, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def failing_run_audit(**kwargs: Any) -> Dict[str, Any]:
        raise RuntimeError("judge down")

    monkeypatch.setattr(server_mod, "run_audit", failing_run_audit)

    submission = await stub.SubmitAudit(
        pb.AuditRequest(request_id="req-fail"), timeout=RPC_TIMEOUT_S
    )
    assert submission.status == "failed"

    result = await stub.GetAuditResult(
        pb.AuditResultRequest(audit_id=submission.audit_id), timeout=RPC_TIMEOUT_S
    )
    assert result.status == pb.AUDIT_STATUS_FAILED
    assert result.request_id == "req-fail"
    assert "judge down" in result.reasoning_trace


async def test_legacy_get_unknown_audit_is_not_found(stub: pb_grpc.AuditServiceStub) -> None:
    with pytest.raises(aio.AioRpcError) as exc_info:
        await stub.GetAuditResult(
            pb.AuditResultRequest(audit_id="does-not-exist"), timeout=RPC_TIMEOUT_S
        )

    assert exc_info.value.code() == grpc.StatusCode.NOT_FOUND
