#!/usr/bin/env python3
"""
End-to-End Test via Go Proxy

Tests the FULL flow: Go Proxy -> Python Audit Engine -> WebSocket

This sends requests through the Go proxy (port 8080) using test_response mode,
which means the proxy uses the provided response (no upstream API key needed)
and dispatches an async audit job to the Python engine via gRPC.

Prerequisites:
    - Docker services: docker-compose up -d redis qdrant ollama
    - Knowledge base seeded: python scripts/seed_knowledge.py
    - Python engine running: python -m truthtable.main (port 50051)
    - Go proxy running: go run ./cmd/proxy (port 8080)
    - (Optional) React dashboard: npm run dev (port 5173)

Usage:
    python test_e2e.py
"""

import json
import sys
import time
import urllib.request
import urllib.error

# Also test gRPC directly to verify audit results
sys.path.insert(0, "backend-python/src")

PROXY_URL = "http://localhost:8080"


def check_health(name: str, url: str) -> bool:
    """Check if a service is reachable."""
    try:
        req = urllib.request.Request(url)
        with urllib.request.urlopen(req, timeout=5) as resp:
            data = resp.read().decode("utf-8")
            print(f"  [OK] {name} ({resp.status}) {data[:80]}")
            return True
    except Exception as e:
        print(f"  [FAIL] {name}: {e}")
        return False


def check_grpc_health() -> bool:
    """Check if the Python gRPC engine is reachable."""
    try:
        import grpc
        from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc

        channel = grpc.insecure_channel("localhost:50051")
        stub = evaluator_pb2_grpc.AuditServiceStub(channel)
        health = stub.HealthCheck(evaluator_pb2.HealthRequest(), timeout=5)
        print(f"  [OK] Python Audit Engine (gRPC:50051) healthy={health.healthy}, version={health.version}")
        deps = dict(health.dependencies)
        for k, v in deps.items():
            print(f"       - {k}: {v}")
        channel.close()
        return True
    except Exception as e:
        print(f"  [FAIL] Python Audit Engine (gRPC:50051): {e}")
        return False


def check_qdrant() -> bool:
    """Check if Qdrant has knowledge base data."""
    try:
        req = urllib.request.Request("http://localhost:6333/collections/truthtable_knowledge")
        with urllib.request.urlopen(req, timeout=5) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            points = data.get("result", {}).get("points_count", 0)
            print(f"  [OK] Qdrant (6333) - {points} documents in knowledge base")
            return points > 0
    except Exception as e:
        print(f"  [FAIL] Qdrant (6333): {e}")
        return False


def send_proxy_request(test_name: str, messages: list, test_response: str) -> dict:
    """Send a request through the Go proxy with test_response."""
    print(f"\n  {test_name}")
    print(f"  {'-' * 50}")

    body = {
        "model": "llama3.2",
        "messages": messages,
        "test_response": test_response,
    }

    data = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        f"{PROXY_URL}/v1/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    start = time.time()
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            elapsed = time.time() - start
            result = json.loads(resp.read().decode("utf-8"))
            request_id = result.get("id", "").replace("chatcmpl-test-", "")
            content = result["choices"][0]["message"]["content"]
            print(f"  Proxy response: {content[:80]}")
            print(f"  Request ID: {request_id} ({elapsed:.1f}s)")
            return {"ok": True, "request_id": request_id, "content": content}

    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8") if e.fp else ""
        print(f"  HTTP Error {e.code}: {error_body[:200]}")
        return {"ok": False, "error": error_body}
    except Exception as e:
        print(f"  Error: {e}")
        return {"ok": False, "error": str(e)}


def run_direct_audit(test_name: str, query: str, response: str) -> dict:
    """Run audit directly via gRPC to see full results."""
    print(f"\n  {test_name}")
    print(f"  {'-' * 50}")
    print(f"  Query:    {query}")
    print(f"  Response: {response[:80]}")

    try:
        import grpc
        from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc

        channel = grpc.insecure_channel("localhost:50051")
        stub = evaluator_pb2_grpc.AuditServiceStub(channel)

        request = evaluator_pb2.AuditRequest(
            request_id=f"e2e-test-{int(time.time())}",
            query=query,
            response=response,
            provider="test",
            model="test-model",
            timestamp_ms=int(time.time() * 1000),
        )

        start = time.time()
        result = stub.SubmitAudit(request, timeout=120)
        elapsed = time.time() - start

        if result.status == "completed" and result.audit_id:
            audit = stub.GetAuditResult(
                evaluator_pb2.AuditResultRequest(audit_id=result.audit_id),
                timeout=10,
            )
            score = audit.faithfulness_score
            grade = evaluator_pb2.TrustGrade.Name(audit.grade) if audit.grade else "?"
            detected = score < 0.7

            print(f"  Score:    {score:.0%} (Grade: {grade}) {'** HALLUCINATION **' if detected else 'PASSED'}")
            print(f"  Claims:   {len(audit.claims)} ({elapsed:.1f}s)")

            for i, claim in enumerate(audit.claims, 1):
                status = evaluator_pb2.VerificationStatus.Name(claim.status)
                print(f"    {i}. [{status}] (conf={claim.confidence:.0%}) {claim.claim[:70]}")

            channel.close()
            return {"ok": True, "score": score, "grade": grade, "claims": len(audit.claims), "detected": detected}
        else:
            print(f"  Status: {result.status}")
            channel.close()
            return {"ok": False, "status": result.status}

    except Exception as e:
        print(f"  Error: {e}")
        return {"ok": False, "error": str(e)}


def main():
    print("=" * 60)
    print("  TrustAgent End-to-End Test")
    print("=" * 60)

    # ── Step 1: Health checks ──
    print("\n[Step 1] Checking all services...")
    proxy_ok = check_health("Go Proxy (8080)", f"{PROXY_URL}/health")
    grpc_ok = check_grpc_health()
    qdrant_ok = check_qdrant()

    if not proxy_ok:
        print("\n  Go proxy not running. Start it: cd backend-go && go run ./cmd/proxy")
    if not grpc_ok:
        print("\n  Python engine not running. Start it: cd backend-python && python -m truthtable.main")
    if not qdrant_ok:
        print("\n  Qdrant empty. Seed it: cd backend-python && python scripts/seed_knowledge.py")

    if not (proxy_ok and grpc_ok and qdrant_ok):
        print("\nFix the above issues and re-run.")
        sys.exit(1)

    print("\n  All services healthy!")

    # ── Step 2: Test proxy endpoint ──
    print(f"\n{'='*60}")
    print("[Step 2] Testing Go Proxy endpoint (test_response mode)...")

    r1 = send_proxy_request(
        "Proxy Test 1: True claim",
        messages=[{"role": "user", "content": "What is the capital of France?"}],
        test_response="Paris is the capital of France.",
    )

    r2 = send_proxy_request(
        "Proxy Test 2: False claim (hallucination)",
        messages=[{"role": "user", "content": "What is the capital of France?"}],
        test_response="London is the capital of France.",
    )

    r3 = send_proxy_request(
        "Proxy Test 3: Mixed claims",
        messages=[{"role": "user", "content": "Tell me about physics"}],
        test_response="The speed of light is approximately 300,000 km/s. It was discovered by Isaac Newton in 1687.",
    )

    # ── Step 3: Test audit pipeline directly (see actual scores) ──
    print(f"\n{'='*60}")
    print("[Step 3] Testing Audit Pipeline (gRPC direct - see actual scores)...")

    a1 = run_direct_audit(
        "Audit 1: True claim (expect HIGH score)",
        query="What is the capital of France?",
        response="Paris is the capital of France.",
    )

    a2 = run_direct_audit(
        "Audit 2: False claim (expect LOW score / hallucination detected)",
        query="What is the capital of France?",
        response="London is the capital of France.",
    )

    a3 = run_direct_audit(
        "Audit 3: Mixed claims (expect MEDIUM score)",
        query="Tell me about physics",
        response="The speed of light is approximately 300,000 km/s. It was discovered by Isaac Newton in 1687.",
    )

    # ── Summary ──
    print(f"\n{'='*60}")
    print("  RESULTS SUMMARY")
    print(f"{'='*60}")

    print("\n  Proxy Endpoint (Go -> returns LLM response):")
    print(f"    Test 1 (true):   {'PASS' if r1.get('ok') else 'FAIL'}")
    print(f"    Test 2 (false):  {'PASS' if r2.get('ok') else 'FAIL'}")
    print(f"    Test 3 (mixed):  {'PASS' if r3.get('ok') else 'FAIL'}")

    print("\n  Audit Pipeline (Python -> verifies claims):")
    if a1.get("ok"):
        print(f"    Audit 1 (true):  {a1['score']:.0%} Grade {a1['grade']} - {'PASS' if a1['score'] >= 0.5 else 'UNEXPECTED'}")
    else:
        print(f"    Audit 1 (true):  FAIL")
    if a2.get("ok"):
        print(f"    Audit 2 (false): {a2['score']:.0%} Grade {a2['grade']} - {'PASS (hallucination caught!)' if a2['detected'] else 'UNEXPECTED'}")
    else:
        print(f"    Audit 2 (false): FAIL")
    if a3.get("ok"):
        print(f"    Audit 3 (mixed): {a3['score']:.0%} Grade {a3['grade']} - {'PASS' if 0.2 <= a3['score'] <= 0.9 else 'UNEXPECTED'}")
    else:
        print(f"    Audit 3 (mixed): FAIL")

    # Overall
    all_proxy = all(r.get("ok") for r in [r1, r2, r3])
    all_audit = all(a.get("ok") for a in [a1, a2, a3])

    print(f"\n  {'='*50}")
    if all_proxy and all_audit:
        if a1.get("ok") and a2.get("ok") and a1["score"] > a2["score"]:
            print("  ALL TESTS PASSED! True claims score higher than false claims.")
        elif all_audit:
            print("  ALL TESTS COMPLETED. Check scores above.")
        print("\n  The audit pipeline is working end-to-end!")
        print("  Open http://localhost:5173 to see results on the dashboard.")
    else:
        print("  SOME TESTS FAILED. Check the output above.")
    print(f"  {'='*50}")


if __name__ == "__main__":
    main()
