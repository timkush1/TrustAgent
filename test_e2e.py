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
            hallucination_detected = audit.hallucination_detected
            reasoning_trace = audit.reasoning_trace
            step_timings = dict(audit.step_timings)

            print(f"  Score:    {score:.0%} (Grade: {grade}) {'** HALLUCINATION **' if hallucination_detected else 'PASSED'}")
            print(f"  Hallucination detected: {hallucination_detected}")
            print(f"  Claims:   {len(audit.claims)} ({elapsed:.1f}s)")
            print(f"  Reasoning trace: {'present' if reasoning_trace else 'MISSING'} ({len(reasoning_trace)} chars)")
            print(f"  Step timings: {step_timings or 'MISSING'}")

            for i, claim in enumerate(audit.claims, 1):
                status = evaluator_pb2.VerificationStatus.Name(claim.status)
                evidence_count = len(claim.evidence)
                print(f"    {i}. [{status}] (conf={claim.confidence:.0%}) {claim.claim[:60]}... [evidence: {evidence_count}]")

            channel.close()
            return {
                "ok": True,
                "score": score,
                "grade": grade,
                "claims": len(audit.claims),
                "hallucination_detected": hallucination_detected,
                "has_reasoning_trace": bool(reasoning_trace),
                "has_step_timings": bool(step_timings),
                "has_evidence": any(len(c.evidence) > 0 for c in audit.claims),
            }
        else:
            print(f"  Status: {result.status}")
            channel.close()
            return {"ok": False, "status": result.status}

    except Exception as e:
        print(f"  Error: {e}")
        return {"ok": False, "error": str(e)}


def test_direct_audit_endpoint() -> dict:
    """Test the /api/audit endpoint on the Go proxy."""
    print("\n  Testing POST /api/audit...")

    body = json.dumps({"query": "What is gravity?", "response": "Gravity is a fundamental force."}).encode("utf-8")
    req = urllib.request.Request(
        f"{PROXY_URL}/api/audit",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            request_id = data.get("request_id", "")
            status = data.get("status", "")
            print(f"  Response: request_id={request_id}, status={status}")
            assert request_id, "/api/audit did not return request_id"
            assert status == "submitted", f"/api/audit status was '{status}', expected 'submitted'"
            return {"ok": True, "request_id": request_id}
    except Exception as e:
        print(f"  Error: {e}")
        return {"ok": False, "error": str(e)}


def test_file_upload_endpoint() -> dict:
    """Test the /api/upload endpoint on the Go proxy."""
    print("\n  Testing POST /api/upload...")

    docs = [{"content": "E2E test document: the sky is blue.", "metadata": {"source": "test"}}]
    file_data = json.dumps(docs).encode("utf-8")

    boundary = "----E2ETestBoundary"
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="test.json"\r\n'
        f"Content-Type: application/json\r\n\r\n"
    ).encode("utf-8") + file_data + f"\r\n--{boundary}--\r\n".encode("utf-8")

    req = urllib.request.Request(
        f"{PROXY_URL}/api/upload",
        data=body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            count = data.get("documents_ingested", 0)
            status = data.get("status", "")
            print(f"  Response: documents_ingested={count}, status={status}")
            assert count >= 1, f"Expected at least 1 document ingested, got {count}"
            assert status == "success", f"Upload status was '{status}', expected 'success'"
            return {"ok": True, "count": count}
    except urllib.error.HTTPError as e:
        error_body = e.read().decode("utf-8") if e.fp else ""
        print(f"  HTTP Error {e.code}: {error_body[:200]}")
        return {"ok": False, "error": error_body}
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

    # ── Step 4: Test new API endpoints ──
    print(f"\n{'='*60}")
    print("[Step 4] Testing new API endpoints...")

    api_audit = test_direct_audit_endpoint()
    api_upload = test_file_upload_endpoint()

    # ── Summary & Assertions ──
    print(f"\n{'='*60}")
    print("  RESULTS SUMMARY")
    print(f"{'='*60}")

    failures = []

    print("\n  Proxy Endpoint (Go -> returns LLM response):")
    for name, result in [("Test 1 (true)", r1), ("Test 2 (false)", r2), ("Test 3 (mixed)", r3)]:
        ok = result.get("ok")
        print(f"    {name}: {'PASS' if ok else 'FAIL'}")
        if not ok:
            failures.append(f"Proxy {name} failed")

    print("\n  Audit Pipeline (Python -> verifies claims):")
    for name, result, checks in [
        ("Audit 1 (true claim)", a1, [
            ("score >= 0.5", lambda r: r["score"] >= 0.5),
            ("hallucination_detected is False", lambda r: not r["hallucination_detected"]),
            ("has reasoning_trace", lambda r: r["has_reasoning_trace"]),
            ("has step_timings", lambda r: r["has_step_timings"]),
        ]),
        ("Audit 2 (false claim)", a2, [
            ("hallucination_detected is True", lambda r: r["hallucination_detected"]),
            ("has reasoning_trace", lambda r: r["has_reasoning_trace"]),
            ("has step_timings", lambda r: r["has_step_timings"]),
        ]),
        ("Audit 3 (mixed)", a3, [
            ("score between 0.1 and 0.95", lambda r: 0.1 <= r["score"] <= 0.95),
            ("has reasoning_trace", lambda r: r["has_reasoning_trace"]),
            ("has step_timings", lambda r: r["has_step_timings"]),
        ]),
    ]:
        if not result.get("ok"):
            print(f"    {name}: FAIL (audit did not complete)")
            failures.append(f"{name} did not complete")
            continue

        score_str = f"{result['score']:.0%}" if "score" in result else "?"
        print(f"    {name}: score={score_str} grade={result.get('grade', '?')}")
        for check_name, check_fn in checks:
            passed = check_fn(result)
            print(f"      {'PASS' if passed else 'FAIL'}: {check_name}")
            if not passed:
                failures.append(f"{name}: {check_name}")

    # Cross-audit assertions
    if a1.get("ok") and a2.get("ok"):
        if a1["score"] > a2["score"]:
            print(f"\n    PASS: True claim scores higher than false claim ({a1['score']:.0%} > {a2['score']:.0%})")
        else:
            msg = f"True claim should score higher than false ({a1['score']:.0%} vs {a2['score']:.0%})"
            print(f"\n    FAIL: {msg}")
            failures.append(msg)

    print("\n  API Endpoints:")
    for name, result in [("POST /api/audit", api_audit), ("POST /api/upload", api_upload)]:
        ok = result.get("ok")
        print(f"    {name}: {'PASS' if ok else 'FAIL'}")
        if not ok:
            failures.append(f"{name} failed: {result.get('error', 'unknown')}")

    # Final verdict
    print(f"\n  {'='*50}")
    if not failures:
        print("  ALL TESTS PASSED!")
        print("\n  The audit pipeline is working end-to-end!")
        print("  Open http://localhost:5173 to see results on the dashboard.")
    else:
        print(f"  {len(failures)} ASSERTION(S) FAILED:")
        for f in failures:
            print(f"    - {f}")
    print(f"  {'='*50}")

    if failures:
        sys.exit(1)


if __name__ == "__main__":
    main()
