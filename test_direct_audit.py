#!/usr/bin/env python3
"""
Test script to send a mock audit directly to the Python engine
and verify the full system is working
"""

import grpc
import sys
import time
from pathlib import Path

# Add the backend-python src to path
sys.path.insert(0, str(Path(__file__).parent / "backend-python" / "src"))

from truthtable.grpc.pb import evaluator_pb2, evaluator_pb2_grpc

def test_audit():
    print("🧪 Testing TruthTable Audit Engine")
    print("=" * 50)
    print()
    
    # Connect to gRPC server
    print("1️⃣  Connecting to audit engine (localhost:50051)...")
    channel = grpc.insecure_channel('localhost:50051')
    stub = evaluator_pb2_grpc.AuditServiceStub(channel)
    
    # Check health
    try:
        health = stub.HealthCheck(evaluator_pb2.HealthRequest())
        print(f"   ✅ Connected! Health: {health.healthy}, Version: {health.version}")
        if not health.healthy:
            print(f"   ⚠️  Warning: Health check returned False")
            print(f"   Dependencies: {dict(health.dependencies)}")
    except Exception as e:
        print(f"   ❌ Failed to connect: {e}")
        return
    
    print()
    print("2️⃣  Submitting test audit...")
    
    # Create a test audit request
    request = evaluator_pb2.AuditRequest(
        request_id="test-" + str(int(time.time())),
        query="What is the capital of France?",
        response="The capital of France is Paris. Paris has been the capital since 508 AD.",
        context=[
            evaluator_pb2.ContextDocument(
                content="France is a country in Western Europe. Its capital and largest city is Paris."
            )
        ],
        provider="test",
        model="test-model",
        timestamp_ms=int(time.time() * 1000)
    )
    
    try:
        # Submit the audit
        print(f"   Request ID: {request.request_id}")
        print(f"   Query: {request.query}")
        print(f"   Response: {request.response}")
        print()
        
        result = stub.SubmitAudit(request)
        print(f"   ✅ Audit submitted!")
        print(f"   Audit ID: {result.audit_id}")
        print(f"   Status: {result.status}")
        print()
        
        # Get the result
        if result.audit_id:
            print("3️⃣  Retrieving audit result...")
            time.sleep(1)  # Give it a moment to process
            
            result_req = evaluator_pb2.AuditResultRequest(audit_id=result.audit_id)
            audit_result = stub.GetAuditResult(result_req)
            
            print(f"   📊 Faithfulness Score: {audit_result.faithfulness_score:.2%}")
            print(f"   🎯 Claims Verified: {len(audit_result.claims)}")
            print()
            
            if audit_result.claims:
                print("   Claims breakdown:")
                for i, claim in enumerate(audit_result.claims, 1):
                    status_emoji = "✅" if claim.status == evaluator_pb2.VERIFICATION_STATUS_SUPPORTED else "❌"
                    print(f"   {status_emoji} {i}. {claim.claim[:60]}...")
            
            print()
        
        print("=" * 50)
        print("✅ Test complete!")
        print()
        print("📊 Dashboard: http://localhost:5173")
        print("   The audit should appear in the feed")
        print()
        
    except Exception as e:
        print(f"   ❌ Error during audit: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    test_audit()
