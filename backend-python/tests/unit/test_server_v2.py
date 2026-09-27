"""
AuditService v2 contract tests (docs/v2/SPEC.md section 2.2).

The contract-shape tests read the generated descriptors and pin what peers rely
on: the RPC set, enum and field numbering (v1 unchanged, v2.1 reserved) and
which fields track presence. Renumbering or dropping `optional` fails here.

The RPC tests start the real AuditServicer on an in-process grpc.aio server
bound to an ephemeral loopback port and call it through the generated stub, so
the regenerated stubs, the servicer and the wire encoding are exercised
together. No LLM, Qdrant or network access is needed: the audit graph is
stubbed where an RPC would run it.

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

# New in v2; implemented in later phases (SPEC section 4): Evaluate in S0,
# GetEngineInfo in P2, RetrieveContext (knowledge profile) in P3,
# ApplyRuntimeConfig and DraftEvaluator in P4.
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


# Complete enum numbering. v1 values are unchanged and v2 only appends (D6:
# UNKNOWN and NOT_CHECKED get their own values on the v2 RPCs). Exact equality,
# so adding a value is a deliberate contract change that updates this table.
ENUMS: Dict[str, Dict[str, int]] = {
    "AuditStatus": {
        "AUDIT_STATUS_UNSPECIFIED": 0,
        "AUDIT_STATUS_QUEUED": 1,
        "AUDIT_STATUS_PROCESSING": 2,
        "AUDIT_STATUS_COMPLETED": 3,
        "AUDIT_STATUS_FAILED": 4,
        "AUDIT_STATUS_DEGRADED": 5,
    },
    "VerificationStatus": {
        "VERIFICATION_STATUS_UNSPECIFIED": 0,
        "VERIFICATION_STATUS_SUPPORTED": 1,
        "VERIFICATION_STATUS_CONTRADICTED": 2,
        "VERIFICATION_STATUS_UNSUPPORTED": 3,
        "VERIFICATION_STATUS_PARTIALLY_SUPPORTED": 4,
        "VERIFICATION_STATUS_UNKNOWN": 5,
        "VERIFICATION_STATUS_NOT_CHECKED": 6,
    },
    "AuditVerdict": {
        "AUDIT_VERDICT_UNSPECIFIED": 0,
        "AUDIT_VERDICT_GROUNDED": 1,
        "AUDIT_VERDICT_HALLUCINATED": 2,
        "AUDIT_VERDICT_UNVERIFIABLE": 3,
        "AUDIT_VERDICT_NO_CLAIMS": 4,
        "AUDIT_VERDICT_NOT_ASSESSED": 5,
    },
    "TrustGrade": {
        "TRUST_GRADE_UNSPECIFIED": 0,
        "TRUST_GRADE_A": 1,
        "TRUST_GRADE_B": 2,
        "TRUST_GRADE_C": 3,
        "TRUST_GRADE_D": 4,
        "TRUST_GRADE_F": 5,
    },
    "KBClaimStatus": {
        "KB_CLAIM_STATUS_UNSPECIFIED": 0,
        "KB_CLAIM_STATUS_ACCEPTED": 1,
        "KB_CLAIM_STATUS_QUARANTINED": 2,
    },
}

# Every field a v1 peer knows keeps its name and number (wire compatibility for
# legacy callers of SubmitAudit/GetAuditResult and the KB RPCs).
V1_FIELDS: Dict[str, Dict[str, int]] = {
    "AuditRequest": {
        "request_id": 1,
        "query": 2,
        "response": 3,
        "context": 4,
        "model": 5,
        "provider": 6,
        "timestamp_ms": 7,
        "metadata": 8,
    },
    "ContextDocument": {"id": 1, "content": 2, "metadata": 3, "relevance_score": 4},
    "AuditResult": {
        "audit_id": 1,
        "request_id": 2,
        "status": 3,
        "faithfulness_score": 4,
        "grade": 5,
        "claims": 6,
        "reasoning_trace": 7,
        "completed_at_ms": 8,
        "processing_time_ms": 9,
        "hallucination_detected": 10,
        "step_timings": 11,
    },
    "ClaimVerification": {"claim": 1, "status": 2, "confidence": 3, "evidence": 4},
    "HealthResponse": {"healthy": 1, "version": 2, "dependencies": 3},
    "IngestResponse": {
        "documents_ingested": 1,
        "status": 2,
        "claim_results": 3,
        "claims_accepted": 4,
        "claims_quarantined": 5,
        "conflicts_detected": 6,
    },
    "ClaimIngestResult": {
        "claim_id": 1,
        "claim": 2,
        "source_doc_id": 3,
        "status": 4,
        "entailment_score": 5,
        "conflicts_with": 6,
    },
    "KBClaim": {
        "claim_id": 1,
        "claim": 2,
        "source_doc_id": 3,
        "source_excerpt": 4,
        "status": 5,
        "entailment_score": 6,
        "conflicts_with": 7,
        "ingested_at_ms": 8,
    },
}

# v2.1 fields whose numbering is reserved now and which 2.0 leaves unset
# (SPEC section 0 row 15: the Jev cascade and KB gates move behind a Go-hosted
# DecisionService in v2.1).
V21_FIELDS: Dict[str, Dict[str, int]] = {
    "AuditOptions": {"decision_model_disabled": 5},
    "AuditResult": {"cascade": 18, "decision_model": 21},
    "ClaimVerification": {
        "jev_verdict": 9,
        "jev_probabilities": 10,
        "jev_confidence": 11,
        "jev_snapshot": 12,
        "llm_verdict": 13,
        "llm_confidence": 14,
        "escalation_reason": 15,
        "shadow": 16,
    },
    "ClaimIngestResult": {"decided_by": 7, "quarantine_reason": 8, "injection_risk": 9},
    "EngineInfo": {"decision_model": 3},
    "RuntimeConfig": {"decision": 5},
}

# proto3 `optional` fields: "absent" must stay distinguishable from 0 on the
# wire (e.g. grade is UNSPECIFIED when scores.trust_score is absent).
PRESENCE_FIELDS = {
    ("GateDecision", "max_risk"),
    ("AuditScores", "trust_score"),
    ("AuditScores", "faithfulness"),
    ("AuditScores", "contradiction_rate"),
    ("AuditScores", "unverified_rate"),
    ("AuditScores", "coverage"),
    ("AuditScores", "accuracy_addressed"),
    ("AuditScores", "max_contradiction"),
    ("AuditScores", "p_hallucinated"),
    ("ClaimVerification", "jev_confidence"),
    ("ClaimVerification", "llm_confidence"),
    ("ClaimIngestResult", "injection_risk"),
}


@pytest.mark.parametrize("enum_name", sorted(ENUMS))
def test_enum_numbering_is_pinned(enum_name: str) -> None:
    values = pb.DESCRIPTOR.enum_types_by_name[enum_name].values
    assert {v.name: v.number for v in values} == ENUMS[enum_name]


def _field_numbers(message: str) -> Dict[str, int]:
    return {f.name: f.number for f in pb.DESCRIPTOR.message_types_by_name[message].fields}


@pytest.mark.parametrize("message", sorted(V1_FIELDS))
def test_v1_field_numbers_are_unchanged(message: str) -> None:
    numbers = _field_numbers(message)
    assert {name: numbers.get(name) for name in V1_FIELDS[message]} == V1_FIELDS[message]


@pytest.mark.parametrize("message", sorted(V21_FIELDS))
def test_v21_field_numbers_are_reserved(message: str) -> None:
    numbers = _field_numbers(message)
    assert {name: numbers.get(name) for name in V21_FIELDS[message]} == V21_FIELDS[message]


def test_optional_fields_track_presence() -> None:
    # Only the fields the spec marks `optional` carry synthetic oneofs (the
    # contract has no real oneofs), so this is exactly the presence set.
    declared = {
        (message.name, field.name)
        for message in pb.DESCRIPTOR.message_types_by_name.values()
        for field in message.fields
        if field.containing_oneof is not None
    }
    assert declared == PRESENCE_FIELDS

    for message_name, field_name in sorted(PRESENCE_FIELDS):
        message_cls = getattr(pb, message_name)
        message = message_cls()
        assert not message.HasField(field_name)

        setattr(message, field_name, 0.0)
        decoded = message_cls.FromString(message.SerializeToString())
        assert decoded.HasField(field_name), f"{message_name}.{field_name} lost presence"
        assert getattr(decoded, field_name) == 0.0

    # Counts are plain proto3 scalars (always present, default 0).
    assert pb.AuditScores().claims_total == 0
    with pytest.raises(ValueError):
        pb.AuditScores().HasField("claims_total")


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
