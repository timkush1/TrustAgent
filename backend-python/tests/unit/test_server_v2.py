"""
AuditService v2 contract tests (docs/v2/SPEC.md section 2.2).

The contract-shape tests read the generated descriptors and pin what peers rely
on: the RPC set, enum and field numbering (v1 unchanged, v2.1 reserved) and
which fields track presence. Renumbering or dropping `optional` fails here.

The RPC tests start the real AuditServicer on an in-process grpc.aio server
bound to an ephemeral loopback port and call it through the generated stub, so
the regenerated stubs, the servicer and the wire encoding are exercised
together. No LLM, Qdrant or network access is needed: the audit graph is either
stubbed or run with a scripted judge (the mock provider, or a fake one).

Evaluate is implemented in S0; its behaviour tests replay the S0 scenarios of
contracts/demo/scenarios.v1.json through the mock judge and check the audit
outcome each scenario expects. The other new v2 RPCs are not implemented yet:
the servicer inherits the generated UNIMPLEMENTED handlers, and the phases that
implement them replace the corresponding cases here with behaviour tests.
"""

import asyncio
import json
import re
import time
from collections import OrderedDict
from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Dict, List, Mapping, Optional, Sequence, Union

import grpc
import pytest
from grpc import aio

from truthtable import __version__
from truthtable.contracts import DEMO_SCENARIOS, load_contract
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.graphs.state import VerificationStatus
from truthtable.grpc import server as server_mod
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer
from truthtable.providers.base import CompletionRequest, CompletionResponse, LLMProvider
from truthtable.providers.mock import MockLLMProvider, parse_scenario_pack
from truthtable.scoring import ScoringConfig

RPC_TIMEOUT_S = 5

# New in v2 and implemented (SPEC section 4): Evaluate in S0, GetEngineInfo in
# P2 (behaviour tests in test_engine_info.py).
IMPLEMENTED_V2_RPCS = {"Evaluate", "GetEngineInfo"}

# New in v2; implemented in later phases (SPEC section 4): RetrieveContext
# (knowledge profile) in P3, ApplyRuntimeConfig and DraftEvaluator in P4.
NEW_RPCS: Dict[str, Any] = {
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

    assert set(methods) == LEGACY_RPCS | IMPLEMENTED_V2_RPCS | set(NEW_RPCS)

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
# New RPCs not implemented yet: UNIMPLEMENTED
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("method", sorted(IMPLEMENTED_V2_RPCS))
def test_implemented_v2_rpc_is_overridden(method: str) -> None:
    assert getattr(AuditServicer, method) is not getattr(pb_grpc.AuditServiceServicer, method)


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
    assert dict(response.dependencies) == {"judge": True}  # D10: no "ollama" key


async def test_health_check_reports_unhealthy_provider() -> None:
    class UnhealthyProvider:
        async def health_check(self) -> bool:
            return False

    servicer = AuditServicer(audit_graph=None, provider=UnhealthyProvider())
    async with serving(servicer) as audit_stub:
        response = await audit_stub.HealthCheck(pb.HealthRequest(), timeout=RPC_TIMEOUT_S)

    assert response.healthy is False
    assert dict(response.dependencies) == {"judge": False}


async def test_health_check_bounds_a_hanging_judge_probe(monkeypatch: pytest.MonkeyPatch) -> None:
    # A judge that never answers its health probe shows as judge=False; the RPC
    # still answers well inside its deadline (the compose healthcheck's 3 s).
    class HangingProvider:
        async def health_check(self) -> bool:
            await asyncio.sleep(RPC_TIMEOUT_S * 2)
            return True

    monkeypatch.setattr(server_mod, "JUDGE_HEALTH_TIMEOUT_S", 0.05)
    servicer = AuditServicer(audit_graph=None, provider=HangingProvider())
    async with serving(servicer) as audit_stub:
        response = await audit_stub.HealthCheck(pb.HealthRequest(), timeout=1.0)

    assert response.healthy is False
    assert dict(response.dependencies) == {"judge": False}


async def test_health_check_probes_the_judge_and_qdrant_concurrently(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # Both probes hang to their bounds; run one after the other they would
    # outlast the deadline, so the check must run them at the same time.
    class HangingProvider:
        async def health_check(self) -> bool:
            await asyncio.sleep(RPC_TIMEOUT_S * 2)
            return True

    class HangingQdrant:
        def health_check(self) -> bool:
            time.sleep(0.8)
            return True

    monkeypatch.setattr(server_mod, "JUDGE_HEALTH_TIMEOUT_S", 0.5)
    monkeypatch.setattr(server_mod, "QDRANT_PROBE_TIMEOUT_S", 0.5)
    servicer = AuditServicer(
        audit_graph=None, provider=HangingProvider(), qdrant_store=HangingQdrant()
    )
    async with serving(servicer) as audit_stub:
        response = await audit_stub.HealthCheck(pb.HealthRequest(), timeout=0.9)

    assert dict(response.dependencies) == {"judge": False, "qdrant": False}
    assert response.healthy is False


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


# ---------------------------------------------------------------------------
# Evaluate (S0)
# ---------------------------------------------------------------------------

SCENARIO_PACK: Dict[str, Any] = load_contract(DEMO_SCENARIOS)
S0_SCENARIOS: List[Dict[str, Any]] = [s for s in SCENARIO_PACK["scenarios"] if "s0" in s["sets"]]

# What the proxy sends as request_system evidence in S0: the <document> blocks of
# the system prompt.
_DOCUMENT_BLOCK = re.compile(r'<document id="([^"]+)">(.*?)</document>', re.DOTALL)
_CLAIM_BLOCK = re.compile(r"<claim>\n(.*?)\n</claim>", re.DOTALL)


def _claim_status(verdict: str) -> int:
    return pb.VerificationStatus.Value(f"VERIFICATION_STATUS_{verdict}")


def _audit_verdict(verdict: str) -> int:
    return pb.AuditVerdict.Value(f"AUDIT_VERDICT_{verdict}")


def _scenario_request(scenario: Mapping[str, Any]) -> pb.AuditRequest:
    """The Evaluate request the proxy sends for one scenario's trace."""
    messages = scenario["request"]["messages"]
    documents = [
        pb.ContextDocument(
            id=match.group(1), content=match.group(2), source="request_system", label=match.group(1)
        )
        for message in messages
        if message["role"] == "system"
        for match in _DOCUMENT_BLOCK.finditer(message["content"])
    ]
    expect = scenario["expect"]
    trace_id = f"trace-{scenario['id']}"
    return pb.AuditRequest(
        request_id=trace_id,
        trace_id=trace_id,
        audit_id=f"audit-{scenario['id']}",
        query=[m["content"] for m in messages if m["role"] == "user"][-1],
        response=scenario["response"],
        context=documents,
        model=scenario["request"]["model"],
        provider=scenario["api_format"],
        route=scenario["route"],
        source="trafficgen",
        gate=pb.GateDecision(
            action=expect["action"],
            mode=expect["mode"],
            route=scenario["route"],
            would_action=expect["would_action"],
        ),
        evidence_summary=pb.EvidenceSummary(
            configured=["request_system"],
            used=(
                [pb.EvidenceSourceCount(source="request_system", items=len(documents))]
                if documents
                else []
            ),
            empty=[] if documents else ["request_system"],
            knowledge="not_requested",
        ),
        options=pb.AuditOptions(mock_scenario=scenario["id"]),
    )


def _scenario(scenario_id: str) -> Dict[str, Any]:
    return next(s for s in SCENARIO_PACK["scenarios"] if s["id"] == scenario_id)


class CountingMockJudge(MockLLMProvider):
    """The mock judge, counting the calls it answers or misses."""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self.calls = 0

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.calls += 1
        return await super().complete(request)


class ScriptedJudge(LLMProvider):
    """
    A judge that decomposes every answer into `claims` and answers each claim's
    verification from `verdicts`. An exception in either place is raised, the
    way a provider outage surfaces.
    """

    def __init__(
        self,
        claims: Union[Sequence[str], Exception],
        verdicts: Optional[Mapping[str, Union[Dict[str, Any], Exception]]] = None,
    ):
        super().__init__(model="scripted")
        self.claims = claims
        self.verdicts = dict(verdicts or {})
        self.calls = 0

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.calls += 1
        claim = _CLAIM_BLOCK.search(request.messages[-1].content)
        answer: Union[Sequence[str], Dict[str, Any], Exception] = (
            self.claims if claim is None else self.verdicts[claim.group(1)]
        )
        if isinstance(answer, Exception):
            raise answer
        return CompletionResponse(
            content=json.dumps(answer), model=self.model, finish_reason="stop"
        )

    async def health_check(self) -> bool:
        return True


def _verdict(status: str, confidence: float = 0.9) -> Dict[str, Any]:
    return {"status": status, "confidence": confidence, "evidence": [], "reasoning": "scripted"}


def _request(
    documents: Sequence[str] = ("Paris is the capital and largest city of France.",),
    source: str = "request_system",
    **options: Any,
) -> pb.AuditRequest:
    return pb.AuditRequest(
        request_id="req-eval",
        query="Tell me about France.",
        response="Paris is the capital of France.",
        context=[
            pb.ContextDocument(id=f"doc-{i}", content=text, source=source)
            for i, text in enumerate(documents)
        ],
        options=pb.AuditOptions(**options),
    )


async def _evaluate(judge: LLMProvider, request: pb.AuditRequest) -> pb.AuditResult:
    servicer = AuditServicer(audit_graph=build_audit_graph(provider=judge), provider=judge)
    async with serving(servicer) as audit_stub:
        result: pb.AuditResult = await audit_stub.Evaluate(request, timeout=RPC_TIMEOUT_S)
        return result


def _assert_failed(result: pb.AuditResult, error_kind: str) -> None:
    assert result.status == pb.AUDIT_STATUS_FAILED
    assert result.error_kind == error_kind
    assert result.error
    # A failed audit reports no verdict and no scores, never a fake score.
    assert result.verdict == pb.AUDIT_VERDICT_UNSPECIFIED
    assert not result.HasField("scores")
    assert result.faithfulness_score == 0.0
    assert result.grade == pb.TRUST_GRADE_UNSPECIFIED
    assert result.hallucination_detected is False


@pytest.fixture
def mock_judge() -> CountingMockJudge:
    return CountingMockJudge(scenarios=parse_scenario_pack(SCENARIO_PACK))


@pytest.fixture
async def judge_stub(mock_judge: CountingMockJudge) -> AsyncIterator[pb_grpc.AuditServiceStub]:
    servicer = AuditServicer(
        audit_graph=build_audit_graph(provider=mock_judge), provider=mock_judge
    )
    async with serving(servicer) as audit_stub:
        yield audit_stub


def test_s0_scenarios_cover_each_audit_outcome() -> None:
    # The walking skeleton's three traces: one grounded, one hallucinated, one
    # with nothing to check against.
    assert sorted(s["expect"]["audit_verdict"] for s in S0_SCENARIOS) == [
        "GROUNDED",
        "HALLUCINATED",
        "NOT_ASSESSED",
    ]


@pytest.mark.parametrize("scenario", S0_SCENARIOS, ids=[s["id"] for s in S0_SCENARIOS])
async def test_evaluate_s0_scenario(
    judge_stub: pb_grpc.AuditServiceStub,
    mock_judge: CountingMockJudge,
    scenario: Dict[str, Any],
) -> None:
    request = _scenario_request(scenario)

    result = await judge_stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    expect, judge = scenario["expect"], scenario["judge"]
    assert (result.audit_id, result.trace_id, result.request_id) == (
        request.audit_id,
        request.trace_id,
        request.request_id,
    )
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == _audit_verdict(expect["audit_verdict"])
    assert result.error_kind == "" and result.error == ""
    assert result.gate == request.gate
    assert result.evidence_summary == request.evidence_summary
    assert result.scoring_version == "v2"
    assert result.completed_at_ms > 0
    assert result.step_timings["total_ms"] == result.processing_time_ms

    if judge is None:
        # Preflight: no evidence, knowledge off -> NOT_ASSESSED with zero LLM calls.
        assert mock_judge.calls == 0
        assert not result.costs
        assert result.not_assessed_reason == "no_evidence"
        assert not result.claims
        assert not result.HasField("scores")
        assert result.faithfulness_score == 0.0
        assert result.grade == pb.TRUST_GRADE_UNSPECIFIED
        assert result.hallucination_detected is False
        assert set(result.step_timings) == {"total_ms"}
        return

    claims = judge["claims"]
    assert mock_judge.calls == 1 + len(claims)  # one decomposition, one verification per claim
    # One CostRecord per judge call, labelled simulated (the mock judge costs nothing).
    assert sorted(c.component for c in result.costs) == sorted(
        ["deep_audit_decompose"] + ["deep_audit_verify"] * len(claims)
    )
    assert {(c.kind, c.provider, c.cost_source, c.status) for c in result.costs} == {
        ("llm", "mock", "simulated", "ok")
    }
    assert sum(c.cost_usd for c in result.costs) == 0.0
    assert result.not_assessed_reason == ""
    assert [c.index for c in result.claims] == list(range(len(claims)))
    assert [c.claim for c in result.claims] == [c["claim"] for c in claims]
    assert [c.status for c in result.claims] == [_claim_status(c["verdict"]) for c in claims]
    assert [c.confidence for c in result.claims] == pytest.approx([c["confidence"] for c in claims])
    assert [list(c.evidence) for c in result.claims] == [c["evidence"] for c in claims]
    assert {c.decided_by for c in result.claims} == {"llm"}
    # Decomposer v2 locates each claim's quote in the answer (UTF-16, half-open;
    # the demo answers are ASCII, so code units are characters).
    assert [(c.quote, c.span_start, c.span_end, c.span_method) for c in result.claims] == [
        (
            c["quote"],
            request.response.index(c["quote"]),
            request.response.index(c["quote"]) + len(c["quote"]),
            "exact",
        )
        for c in claims
    ]
    for claim in result.claims:
        # Scoring v2's per-claim distribution: P(verdict) = confidence.
        assert claim.probability_source == "llm_confidence"
        assert set(claim.verdict_probabilities) == {
            "supported",
            "partially_supported",
            "contradicted",
            "unsupported",
        }
        assert sum(claim.verdict_probabilities.values()) == pytest.approx(1.0, abs=1e-6)
        assert claim.verdict_probabilities[
            pb.VerificationStatus.Name(claim.status).removeprefix("VERIFICATION_STATUS_").lower()
        ] == pytest.approx(claim.confidence)
    assert result.hallucination_detected is (expect["audit_verdict"] == "HALLUCINATED")
    assert result.unsupported_policy == "hallucination"  # the evidence came from the request
    assert result.scores.claims_total == result.scores.claims_decided == len(claims)
    assert result.scores.claims_unknown == 0
    for name in ("trust_score", "faithfulness", "contradiction_rate", "coverage", "p_hallucinated"):
        assert result.scores.HasField(name), name
    assert result.faithfulness_score == pytest.approx(result.scores.trust_score)
    assert result.grade == server_mod.grade_for_score(result.scores.trust_score)
    assert {"decompose_ms", "verify_ms", "score_ms", "total_ms"} <= set(result.step_timings)


async def test_evaluate_unknown_mock_scenario_fails_as_fixture_missing(
    judge_stub: pb_grpc.AuditServiceStub,
) -> None:
    request = _scenario_request(_scenario("grounded-pass"))
    request.options.mock_scenario = "no-such-scenario"

    result = await judge_stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    _assert_failed(result, "mock_fixture_missing")
    assert "no-such-scenario" in result.error
    assert result.audit_id == request.audit_id


@pytest.mark.parametrize(
    "response",
    [
        "Refunds are available for 90 days on every plan.",
        # The scenario's answer plus an extra (unaudited) sentence is not its answer.
        _scenario("grounded-pass")["response"] + " Refunds are paid in cash.",
    ],
    ids=["other-answer", "extra-sentence"],
)
async def test_evaluate_answer_outside_the_scenario_fails_as_fixture_missing(
    judge_stub: pb_grpc.AuditServiceStub, response: str
) -> None:
    # The mock judge only decomposes the scenario's own response.
    request = _scenario_request(_scenario("grounded-pass"))
    request.response = response

    result = await judge_stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    _assert_failed(result, "mock_fixture_missing")


async def test_evaluate_judge_null_scenario_rejects_any_judge_call(
    judge_stub: pb_grpc.AuditServiceStub, mock_judge: CountingMockJudge
) -> None:
    # With evidence the preflight passes, so the judge is asked, but the scenario
    # expects no judge call at all.
    request = _scenario_request(_scenario("no-evidence-not-assessed"))
    request.context.append(
        pb.ContextDocument(
            id="refund-policy.md", content="Refunds: 30 days.", source="request_system"
        )
    )

    result = await judge_stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    _assert_failed(result, "mock_fixture_missing")
    assert mock_judge.calls == 1
    assert "judge is null" in result.error


async def test_evaluate_verifier_fixture_miss_fails_the_audit() -> None:
    # The verifier turns a provider error into an UNKNOWN claim; a fixture miss
    # must still fail the audit instead of scoring around it.
    judge = MockLLMProvider(
        fixtures=[
            {
                "match": "Extract all factual claims",
                "response": json.dumps(["Paris is the capital of France."]),
            }
        ]
    )

    result = await _evaluate(judge, _request())

    _assert_failed(result, "mock_fixture_missing")
    assert [c.status for c in result.claims] == [pb.VERIFICATION_STATUS_UNKNOWN]


async def test_evaluate_zero_claims_is_no_claims() -> None:
    judge = ScriptedJudge(claims=[])

    result = await _evaluate(judge, _request())

    assert judge.calls == 1  # decomposition only
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == pb.AUDIT_VERDICT_NO_CLAIMS
    assert not result.claims
    assert result.reasoning_trace == "No verifiable claims were found in the response."
    assert result.scores.claims_total == 0
    assert not result.scores.HasField("trust_score")
    assert result.faithfulness_score == 0.0
    assert result.grade == pb.TRUST_GRADE_UNSPECIFIED
    assert result.hallucination_detected is False


async def test_evaluate_blank_evidence_is_not_assessed_without_llm_calls() -> None:
    judge = ScriptedJudge(claims=["Paris is the capital of France."])

    result = await _evaluate(judge, _request(documents=("", " \n ")))

    assert judge.calls == 0
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == pb.AUDIT_VERDICT_NOT_ASSESSED
    assert result.not_assessed_reason == "no_evidence"
    assert not result.HasField("scores")


async def test_evaluate_judge_outage_fails_with_judge_unavailable() -> None:
    claims = ["Paris is the capital of France.", "Paris is in Europe."]
    judge = ScriptedJudge(claims, {claim: ConnectionError("judge down") for claim in claims})

    result = await _evaluate(judge, _request())

    _assert_failed(result, "judge_unavailable")
    assert "2 of 2" in result.error
    assert [c.status for c in result.claims] == [pb.VERIFICATION_STATUS_UNKNOWN] * 2
    assert {c.decided_by for c in result.claims} == {"none"}
    # A failed audit still reports every judge call it made.
    assert sorted((c.component, c.status) for c in result.costs) == [
        ("deep_audit_decompose", "ok"),
        ("deep_audit_verify", "error"),
        ("deep_audit_verify", "error"),
    ]


async def test_evaluate_some_unknown_claims_is_degraded() -> None:
    claims = ["Paris is the capital of France.", "Paris is in Europe.", "Paris hosts the Louvre."]
    judge = ScriptedJudge(
        claims,
        {
            claims[0]: _verdict("SUPPORTED"),
            claims[1]: _verdict("SUPPORTED"),
            claims[2]: ConnectionError("judge down"),
        },
    )

    result = await _evaluate(judge, _request())

    assert result.status == pb.AUDIT_STATUS_DEGRADED
    assert list(result.degraded_reasons) == ["unknown_claims"]
    assert result.verdict == pb.AUDIT_VERDICT_GROUNDED
    assert [c.status for c in result.claims] == [
        pb.VERIFICATION_STATUS_SUPPORTED,
        pb.VERIFICATION_STATUS_SUPPORTED,
        pb.VERIFICATION_STATUS_UNKNOWN,
    ]
    assert (result.scores.claims_total, result.scores.claims_decided) == (3, 2)
    assert result.scores.claims_unknown == 1
    assert result.scores.HasField("trust_score")


async def test_evaluate_decomposer_failure() -> None:
    judge = ScriptedJudge(claims=ConnectionError("judge down"))

    result = await _evaluate(judge, _request())

    _assert_failed(result, "decomposer_failed")
    assert "judge down" in result.error


@pytest.mark.parametrize(
    "source, policy, status, verdict, resolved",
    [
        # auto: only knowledge-base evidence makes an unsupported claim unverifiable
        ("knowledge", "", "UNSUPPORTED", pb.AUDIT_VERDICT_UNVERIFIABLE, "unverifiable"),
        ("request_context", "auto", "UNSUPPORTED", pb.AUDIT_VERDICT_HALLUCINATED, "hallucination"),
        (
            "knowledge",
            "hallucination",
            "UNSUPPORTED",
            pb.AUDIT_VERDICT_HALLUCINATED,
            "hallucination",
        ),
        (
            "request_system",
            "unverifiable",
            "UNSUPPORTED",
            pb.AUDIT_VERDICT_UNVERIFIABLE,
            "unverifiable",
        ),
        # A contradicted claim is wrong under either policy.
        (
            "knowledge",
            "unverifiable",
            "CONTRADICTED",
            pb.AUDIT_VERDICT_HALLUCINATED,
            "unverifiable",
        ),
    ],
)
async def test_evaluate_unsupported_policy(
    source: str, policy: str, status: str, verdict: int, resolved: str
) -> None:
    claim = "Paris is the capital of France."
    judge = ScriptedJudge([claim], {claim: _verdict(status)})

    result = await _evaluate(judge, _request(source=source, unsupported_policy=policy))

    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == verdict
    assert result.unsupported_policy == resolved


async def test_evaluate_leaves_an_uncomputable_trust_score_absent() -> None:
    # Knowledge-only evidence (unverifiable policy) that addresses nothing: the
    # scorer has no accuracy_addressed, so there is no trust score and no grade,
    # never a made-up 0.0 graded F.
    claim = "Paris is the capital of France."
    judge = ScriptedJudge([claim], {claim: _verdict("UNSUPPORTED", 1.0)})

    result = await _evaluate(judge, _request(source="knowledge"))

    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == pb.AUDIT_VERDICT_UNVERIFIABLE
    assert result.unsupported_policy == "unverifiable"
    assert not result.scores.HasField("trust_score")
    assert not result.scores.HasField("accuracy_addressed")
    assert result.scores.coverage == pytest.approx(0.0)
    assert result.faithfulness_score == 0.0
    assert result.grade == pb.TRUST_GRADE_UNSPECIFIED


@pytest.mark.parametrize("where", ["document", "summary"])
async def test_evaluate_truncated_evidence_is_context_trimmed(where: str) -> None:
    claim = "Paris is the capital of France."
    judge = ScriptedJudge([claim], {claim: _verdict("UNSUPPORTED")})
    request = _request()
    if where == "document":
        request.context[0].truncated = True
    else:
        request.evidence_summary.truncated = True

    result = await _evaluate(judge, request)

    # The judge may not have seen the support: UNSUPPORTED counts as unverifiable.
    assert result.status == pb.AUDIT_STATUS_DEGRADED
    assert list(result.degraded_reasons) == ["context_trimmed"]
    assert result.unsupported_policy == "unverifiable"
    assert result.verdict == pb.AUDIT_VERDICT_UNVERIFIABLE


async def test_evaluate_grades_with_the_configured_cutoffs() -> None:
    claim = "Paris is the capital of France."
    judge = ScriptedJudge([claim], {claim: _verdict("SUPPORTED", 0.9)})
    scoring = ScoringConfig(grade_cutoffs=(0.99, 0.98, 0.97, 0.96))
    servicer = AuditServicer(
        audit_graph=build_audit_graph(provider=judge, scoring=scoring), provider=judge
    )

    async with serving(servicer) as audit_stub:
        result = await audit_stub.Evaluate(_request(), timeout=RPC_TIMEOUT_S)

    # About 0.88: graded B by the default cutoffs, F below every configured one.
    assert 0.7 <= result.scores.trust_score < 0.96
    assert result.grade == pb.TRUST_GRADE_F


async def test_evaluate_extracts_claims_without_evidence_when_asked() -> None:
    claim = "Paris is the capital of France."
    judge = ScriptedJudge([claim])

    result = await _evaluate(judge, _request(documents=(), extract_claims_without_evidence=True))

    assert judge.calls == 1  # the decomposition; nothing is verified
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.verdict == pb.AUDIT_VERDICT_NOT_ASSESSED
    assert result.not_assessed_reason == "no_evidence"
    assert [c.status for c in result.claims] == [pb.VERIFICATION_STATUS_NOT_CHECKED]
    assert result.claims[0].decided_by == "none"
    assert not result.HasField("scores")


async def test_evaluate_with_an_exhausted_llm_budget_skips_optional_retries() -> None:
    claim = "Paris is the capital of France."
    unusable = {"status": "SUPPORTED", "evidence": [], "reasoning": "no confidence"}
    judge = ScriptedJudge([claim], {claim: unusable})

    result = await _evaluate(judge, _request(llm_budget_exhausted=True))

    assert judge.calls == 2  # decomposition + one verification, no repair turn
    assert [c.status for c in result.claims] == [pb.VERIFICATION_STATUS_UNKNOWN]
    _assert_failed(result, "judge_unavailable")


async def test_evaluate_maps_every_claim_status(
    stub: pb_grpc.AuditServiceStub, monkeypatch: pytest.MonkeyPatch
) -> None:
    def claim(text: str, status: Any) -> Dict[str, Any]:
        return {"claim": text, "status": status, "confidence": 0.8, "evidence": [f"ev-{text}"]}

    _stub_run_audit(
        monkeypatch,
        {
            "verdict": "GROUNDED",
            "audit_status": "degraded",
            "degraded_reasons": ["unknown_claims"],
            "faithfulness_score": 0.8,
            "scores": {
                "trust_score": 0.8,
                "faithfulness": 0.85,
                "contradiction_rate": 0.05,
                "unverified_rate": None,
                "coverage": None,
                "accuracy_addressed": None,
                "max_contradiction": 0.2,
                "p_hallucinated": 0.3,
                "claims_total": 7,
                "claims_decided": 4,
                "claims_unknown": 2,
            },
            "grade": "B",
            "scoring_version": "v2",
            "unsupported_policy": "hallucination",
            "claim_verifications": [
                claim("s", VerificationStatus.SUPPORTED),
                claim("c", VerificationStatus.CONTRADICTED),
                claim("u", VerificationStatus.UNSUPPORTED),
                claim("p", VerificationStatus.PARTIALLY_SUPPORTED),
                claim("k", VerificationStatus.UNKNOWN),
                claim("n", "not_checked"),
                claim("x", "not-a-status"),
            ],
        },
    )

    result = await stub.Evaluate(
        pb.AuditRequest(request_id="req-map", query="q", response="r"), timeout=RPC_TIMEOUT_S
    )

    # v2 keeps UNKNOWN (5) and NOT_CHECKED (6) distinct (D6); the legacy
    # mapping sends both as UNSPECIFIED (test_legacy_submit_and_get_keep_v1_mapping).
    assert [c.status for c in result.claims] == [
        pb.VERIFICATION_STATUS_SUPPORTED,
        pb.VERIFICATION_STATUS_CONTRADICTED,
        pb.VERIFICATION_STATUS_UNSUPPORTED,
        pb.VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
        pb.VERIFICATION_STATUS_UNKNOWN,
        pb.VERIFICATION_STATUS_NOT_CHECKED,
        pb.VERIFICATION_STATUS_UNKNOWN,  # an unrecognised status is a parse failure
    ]
    assert pb.VERIFICATION_STATUS_UNKNOWN == 5 and pb.VERIFICATION_STATUS_NOT_CHECKED == 6
    assert [c.decided_by for c in result.claims] == ["llm"] * 4 + ["none"] * 3
    assert {(c.span_start, c.span_end) for c in result.claims} == {(-1, -1)}
    assert list(result.claims[1].evidence) == ["ev-c"]
    assert result.status == pb.AUDIT_STATUS_DEGRADED
    assert result.verdict == pb.AUDIT_VERDICT_GROUNDED
    assert (result.scores.claims_total, result.scores.claims_decided) == (7, 4)
    assert result.scores.claims_unknown == 2
    assert result.scores.trust_score == pytest.approx(0.8)
    assert result.scores.faithfulness == pytest.approx(0.85)
    # A score the scorer could not compute stays absent on the wire.
    assert not result.scores.HasField("coverage")
    assert not result.scores.HasField("unverified_rate")
    assert result.grade == pb.TRUST_GRADE_B
    assert result.scoring_version == "v2"
    # The server fills in ids the proxy left empty.
    assert result.audit_id and result.trace_id == result.request_id == "req-map"


async def test_evaluate_reports_a_pipeline_exception_as_failed(
    stub: pb_grpc.AuditServiceStub, monkeypatch: pytest.MonkeyPatch
) -> None:
    async def failing_run_audit(**kwargs: Any) -> Dict[str, Any]:
        raise ValueError("graph exploded")

    monkeypatch.setattr(server_mod, "run_audit", failing_run_audit)

    result = await stub.Evaluate(
        pb.AuditRequest(request_id="req-boom", audit_id="audit-boom"), timeout=RPC_TIMEOUT_S
    )

    _assert_failed(result, "internal")
    assert result.error == "graph exploded"
    assert result.audit_id == "audit-boom"


async def test_legacy_submit_without_evidence_is_not_assessed_with_score_zero(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(server_mod, "_audit_results", OrderedDict())
    judge = ScriptedJudge(claims=["Paris is the capital of France."])
    servicer = AuditServicer(audit_graph=build_audit_graph(provider=judge), provider=judge)

    async with serving(servicer) as audit_stub:
        submission = await audit_stub.SubmitAudit(
            pb.AuditRequest(request_id="req-legacy-none", query="q", response="Paris."),
            timeout=RPC_TIMEOUT_S,
        )
        result = await audit_stub.GetAuditResult(
            pb.AuditResultRequest(audit_id=submission.audit_id), timeout=RPC_TIMEOUT_S
        )

    # NOT_ASSESSED maps to COMPLETED with score 0 on the legacy path, and the
    # preflight made no LLM call (it used to verify against empty context).
    assert judge.calls == 0
    assert submission.status == "completed"
    assert result.status == pb.AUDIT_STATUS_COMPLETED
    assert result.faithfulness_score == 0.0
    assert result.hallucination_detected is False
    assert not result.claims
    assert result.verdict == pb.AUDIT_VERDICT_UNSPECIFIED
