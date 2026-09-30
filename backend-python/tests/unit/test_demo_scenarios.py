"""
The deep audit of every demo scenario (contracts/demo/scenarios.v1.json) through the
real Evaluate RPC with the mock judge (LLM_PROVIDER=mock).

Each scenario's expect.audit_verdict and judge fixture were written by hand; the Go
tests in backend-go/cmd/trafficgen replay the gate side. Here every scenario whose
audit is queued runs through the engine as the proxy sends it: the evidence the gate
reports in expect.checked_against (the mock judge reads the scenario's fixture, not the
document text, so placeholder texts stand in for request documents; knowledge documents
are the scenario's own), the scenario's answer and its id as mock_scenario. The audit
must reach the expected verdict with the fixture's claim verdicts, and an audit without
evidence must be NOT_ASSESSED without calling the judge.
"""

from contextlib import asynccontextmanager
from typing import Any, AsyncIterator, Dict, List, Mapping

import pytest
from grpc import aio

from truthtable.contracts import DEMO_SCENARIOS, load_contract
from truthtable.graphs.audit_graph import build_audit_graph
from truthtable.grpc.pb import evaluator_pb2 as pb
from truthtable.grpc.pb import evaluator_pb2_grpc as pb_grpc
from truthtable.grpc.server import AuditServicer
from truthtable.providers.base import CompletionRequest, CompletionResponse
from truthtable.providers.mock import MockLLMProvider, parse_scenario_pack

RPC_TIMEOUT_S = 10.0

SCENARIO_PACK: Dict[str, Any] = load_contract(DEMO_SCENARIOS)
QUEUED: List[Dict[str, Any]] = [
    s for s in SCENARIO_PACK["scenarios"] if s["expect"]["audit"] == "queued"
]


class CountingMockJudge(MockLLMProvider):
    """The mock judge, counting the calls it answers or misses."""

    def __init__(self, **kwargs: Any):
        super().__init__(**kwargs)
        self.calls = 0

    async def complete(self, request: CompletionRequest) -> CompletionResponse:
        self.calls += 1
        return await super().complete(request)


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


def _evidence(scenario: Mapping[str, Any]) -> List[pb.ContextDocument]:
    """The evidence the proxy sends for a scenario, per expect.checked_against."""
    if scenario.get("knowledge"):
        return [
            pb.ContextDocument(id=k["id"], content=k["text"], source="knowledge", label=k["id"])
            for k in scenario["knowledge"]
        ]
    checked = scenario["expect"]["checked_against"]
    if checked == "none":
        return []
    documents = []
    for part in checked.split(","):
        source, items = part.split(":")
        documents += [
            pb.ContextDocument(id=f"{source}-{i}", content="Placeholder document.", source=source)
            for i in range(int(items))
        ]
    return documents


def _request(scenario: Mapping[str, Any]) -> pb.AuditRequest:
    trace_id = f"trace-{scenario['id']}"
    return pb.AuditRequest(
        request_id=trace_id,
        trace_id=trace_id,
        audit_id=f"audit-{scenario['id']}",
        query="(the user request)",
        response=scenario["response"],
        context=_evidence(scenario),
        provider=scenario.get("api_format", "openai_chat"),
        route=scenario["route"],
        source="trafficgen",
        options=pb.AuditOptions(mock_scenario=scenario["id"]),
    )


def test_the_pack_audits_every_outcome() -> None:
    verdicts = {s["expect"]["audit_verdict"] for s in QUEUED}
    assert {"GROUNDED", "HALLUCINATED", "NO_CLAIMS", "NOT_ASSESSED"} <= verdicts


@pytest.mark.parametrize("scenario", QUEUED, ids=[s["id"] for s in QUEUED])
async def test_demo_scenario_audit(scenario: Dict[str, Any]) -> None:
    judge = CountingMockJudge(scenarios=parse_scenario_pack(SCENARIO_PACK))
    servicer = AuditServicer(audit_graph=build_audit_graph(provider=judge), provider=judge)
    request = _request(scenario)

    async with serving(servicer) as stub:
        result: pb.AuditResult = await stub.Evaluate(request, timeout=RPC_TIMEOUT_S)

    expect, fixture = scenario["expect"], scenario["judge"]
    assert result.status == pb.AUDIT_STATUS_COMPLETED, result.error
    assert pb.AuditVerdict.Name(result.verdict) == f"AUDIT_VERDICT_{expect['audit_verdict']}"
    if not request.context:
        # Preflight: no evidence, knowledge off -> NOT_ASSESSED with zero LLM calls.
        assert fixture is None
        assert judge.calls == 0
        assert result.not_assessed_reason == "no_evidence"
        assert not result.claims
        return

    claims = fixture["claims"]
    assert judge.calls == 1 + len(claims)  # one decomposition, one verification per claim
    assert [c.claim for c in result.claims] == [c["claim"] for c in claims]
    assert [pb.VerificationStatus.Name(c.status) for c in result.claims] == [
        f"VERIFICATION_STATUS_{c['verdict']}" for c in claims
    ]
    assert [c.quote for c in result.claims] == [c.get("quote", "") for c in claims]
    knowledge_only = all(d.source == "knowledge" for d in request.context)
    assert result.unsupported_policy == ("unverifiable" if knowledge_only else "hallucination")
    assert result.hallucination_detected is (expect["audit_verdict"] == "HALLUCINATED")
