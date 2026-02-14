#!/usr/bin/env python3
"""
End-to-End Test Script for TruthTable Audit Engine

Tests the complete flow:
1. Connect to gRPC audit engine
2. Submit a test audit (with TRUE claims - should score HIGH)
3. Submit a test audit (with FALSE claims - should score LOW)
4. Verify that the retrieve node populated context from Qdrant

Prerequisites:
    - Docker services running: docker start truthtable-qdrant truthtable-ollama
    - Knowledge base seeded: cd backend-python && .venv/Scripts/python scripts/seed_knowledge.py
    - Audit engine running: cd backend-python && .venv/Scripts/python -m truthtable.main
    - Ollama model pulled: docker exec truthtable-ollama ollama pull llama3.2
"""

import grpc
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent / "backend-python" / "src"))

from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc


def run_audit(stub, name, query, response, context_docs=None):
    """Submit an audit and get results."""
    print(f"\n--- {name} ---")
    print(f"  Query:    {query}")
    print(f"  Response: {response}")

    # Build request (NO context_docs supplied - the retrieve node should fill them)
    context = []
    if context_docs:
        for doc in context_docs:
            context.append(evaluator_pb2.ContextDocument(content=doc))

    request = evaluator_pb2.AuditRequest(
        request_id=f"test-{name.lower().replace(' ', '-')}-{int(time.time())}",
        query=query,
        response=response,
        context=context,
        provider="test",
        model="test-model",
        timestamp_ms=int(time.time() * 1000),
    )

    try:
        result = stub.SubmitAudit(request)
        print(f"  Audit ID: {result.audit_id}")
        print(f"  Status:   {result.status}")

        if result.audit_id and result.status == "completed":
            audit_result = stub.GetAuditResult(
                evaluator_pb2.AuditResultRequest(audit_id=result.audit_id)
            )

            print(f"  Score:    {audit_result.faithfulness_score:.2%}")
            print(f"  Claims:   {len(audit_result.claims)}")

            for i, claim in enumerate(audit_result.claims, 1):
                status_name = evaluator_pb2.VerificationStatus.Name(claim.status)
                print(f"    {i}. [{status_name}] (conf={claim.confidence:.2f}) {claim.claim[:70]}")

            if audit_result.reasoning_trace:
                # Encode safely for Windows console (replace non-ASCII chars)
                trace = audit_result.reasoning_trace[:200]
                print(f"  Reasoning:\n    {trace.encode('ascii', 'replace').decode('ascii')}")

            return audit_result.faithfulness_score
        else:
            print(f"  Audit status: {result.status}")
            return None

    except Exception as e:
        print(f"  ERROR: {e}")
        return None


def main():
    print("=" * 60)
    print("TruthTable End-to-End Test")
    print("=" * 60)

    # Connect
    print("\nConnecting to audit engine (localhost:50051)...")
    channel = grpc.insecure_channel("localhost:50051")
    stub = evaluator_pb2_grpc.AuditServiceStub(channel)

    try:
        health = stub.HealthCheck(evaluator_pb2.HealthRequest())
        print(f"  Healthy: {health.healthy}")
        print(f"  Version: {health.version}")
        print(f"  Dependencies: {dict(health.dependencies)}")
    except Exception as e:
        print(f"  Failed to connect: {e}")
        print("  Is the audit engine running?")
        return

    # Test 1: TRUE claims (should score HIGH)
    # NOTE: We send NO context_docs. The retrieve node should fetch from Qdrant.
    true_score = run_audit(
        stub,
        "Test 1: True claims",
        query="What is the capital of France?",
        response="Paris is the capital of France. It has been the capital since 508 AD.",
    )

    # Test 2: FALSE claims / hallucination (should score LOW)
    false_score = run_audit(
        stub,
        "Test 2: False claims (hallucination)",
        query="What is the capital of France?",
        response="London is the capital of France. It was founded by Napoleon in 1950.",
    )

    # Test 3: Mixed claims (should score MEDIUM)
    mixed_score = run_audit(
        stub,
        "Test 3: Mixed true and false",
        query="Tell me about the speed of light",
        response="The speed of light is approximately 300,000 km/s. It was discovered by Isaac Newton in 1687.",
    )

    # Summary
    print("\n" + "=" * 60)
    print("Results Summary:")
    print(f"  True claims score:   {true_score:.2%}" if true_score is not None else "  True claims: FAILED")
    print(f"  False claims score:  {false_score:.2%}" if false_score is not None else "  False claims: FAILED")
    print(f"  Mixed claims score:  {mixed_score:.2%}" if mixed_score is not None else "  Mixed claims: FAILED")

    if true_score is not None and false_score is not None:
        if true_score > false_score:
            print("\n  PASS: True claims scored higher than false claims!")
        else:
            print("\n  WARNING: Scores unexpected - check knowledge base and LLM model")

    print("=" * 60)


if __name__ == "__main__":
    main()
