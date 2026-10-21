"""End-to-end audit-pipeline tests via direct gRPC to the Python engine.

These assert STRUCTURAL correctness of the full pipeline (decompose -> retrieve
-> verify -> score returns a well-formed result with claims, a reasoning trace,
per-step timings, and a valid grade). Semantic accuracy (true scores higher than
false) depends on the judge model and is covered deterministically by the eval
golden gate (`make eval-golden`), so it is not asserted here.
"""

import time

import pytest

pytestmark = pytest.mark.e2e


def _run_audit(stub, query: str, response: str, evidence: str):
    from truthtable.grpc.pb import evaluator_pb2

    # Evidence is required: without it (and without a knowledge base) the
    # preflight answers NOT_ASSESSED with no claims (SPEC 2.2 legacy mapping).
    request = evaluator_pb2.AuditRequest(
        request_id=f"e2e-{int(time.time() * 1000)}",
        query=query,
        response=response,
        context=[
            evaluator_pb2.ContextDocument(id="e2e-doc", content=evidence, source="request_context")
        ],
    )
    submission = stub.SubmitAudit(request, timeout=180)
    assert submission.audit_id, "SubmitAudit returned no audit_id"
    return stub.GetAuditResult(
        evaluator_pb2.AuditResultRequest(audit_id=submission.audit_id), timeout=10
    )


def test_audit_returns_wellformed_result(grpc_stub):
    result = _run_audit(
        grpc_stub,
        query="Tell me about physics",
        response="The speed of light in a vacuum is 299,792,458 meters per second.",
        evidence="Light travels through a vacuum at exactly 299,792,458 metres per second.",
    )

    from truthtable.grpc.pb import evaluator_pb2

    assert result.status == evaluator_pb2.AUDIT_STATUS_COMPLETED
    assert 0.0 <= result.faithfulness_score <= 1.0
    assert len(result.claims) >= 1, "expected at least one extracted claim"
    assert result.reasoning_trace, "expected a human-readable reasoning trace"
    timings = dict(result.step_timings)
    assert "verify_ms" in timings and "score_ms" in timings


def test_audit_grade_is_valid(grpc_stub):
    result = _run_audit(
        grpc_stub,
        query="What is the capital of France?",
        response="Paris is the capital of France.",
        evidence="Paris is the capital and largest city of France.",
    )
    from truthtable.grpc.pb import evaluator_pb2

    grade_name = evaluator_pb2.TrustGrade.Name(result.grade)
    assert grade_name in {
        "TRUST_GRADE_A",
        "TRUST_GRADE_B",
        "TRUST_GRADE_C",
        "TRUST_GRADE_D",
        "TRUST_GRADE_F",
    }
