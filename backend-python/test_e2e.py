#!/usr/bin/env python3
"""
End-to-end test for the TruthTable Audit Engine.

This script tests:
1. Provider connection to Ollama
2. LangGraph workflow execution
3. Claim decomposition, verification, and scoring
"""

import asyncio
import sys

# Add src to path for local testing
sys.path.insert(0, "src")

from truthtable.providers import OllamaProvider
from truthtable.graphs.audit_graph import build_audit_graph, run_audit


async def test_ollama_connection():
    """Test 1: Can we connect to Ollama?"""
    print("=" * 60)
    print("Test 1: Ollama Connection")
    print("=" * 60)
    
    provider = OllamaProvider(
        model="llama3.2:1b",
        base_url="http://localhost:11434"
    )
    
    healthy = await provider.health_check()
    print(f"  Ollama health check: {'✓ PASS' if healthy else '✗ FAIL'}")
    
    await provider.close()
    return healthy


async def test_simple_completion():
    """Test 2: Can we get a completion from Ollama?"""
    print("\n" + "=" * 60)
    print("Test 2: Simple Completion")
    print("=" * 60)
    
    provider = OllamaProvider(
        model="llama3.2:1b",
        base_url="http://localhost:11434"
    )
    
    from truthtable.providers.base import CompletionRequest
    
    request = CompletionRequest(
        messages=provider.create_messages(
            system_prompt="You are a helpful assistant. Respond briefly.",
            user_message="Say 'Hello, TruthTable!' and nothing else."
        ),
        model="llama3.2:1b",
        temperature=0.1,
        max_tokens=50
    )
    
    try:
        response = await provider.complete(request)
        print(f"  Response: {response.content[:100]}...")
        print(f"  ✓ PASS - Got completion")
        result = True
    except Exception as e:
        print(f"  ✗ FAIL - Error: {e}")
        result = False
    
    await provider.close()
    return result


async def test_audit_workflow():
    """Test 3: Full audit workflow test"""
    print("\n" + "=" * 60)
    print("Test 3: Full Audit Workflow")
    print("=" * 60)
    
    provider = OllamaProvider(
        model="llama3.2:1b",
        base_url="http://localhost:11434"
    )
    
    # Build the audit graph
    print("  Building audit graph...")
    graph = build_audit_graph(provider)
    print("  ✓ Graph built")
    
    # Test case: A response with mixed true/false claims
    user_query = "What is the capital of France?"
    llm_response = "Paris is the capital of France. It was founded in 250 BC by Julius Caesar."
    context_docs = [
        "France is a country in Western Europe. Its capital city is Paris.",
        "Paris is known as the City of Light and is home to the Eiffel Tower.",
        "Paris was originally a Roman city called Lutetia, founded around the 3rd century BC."
    ]
    
    print(f"  User Query: {user_query}")
    print(f"  LLM Response: {llm_response}")
    print(f"  Context: {len(context_docs)} documents")
    
    try:
        print("\n  Running audit workflow...")
        result = await run_audit(
            graph=graph,
            request_id="test-001",
            user_query=user_query,
            llm_response=llm_response,
            context_docs=context_docs
        )
        
        print(f"\n  === AUDIT RESULTS ===")
        print(f"  Request ID: {result.get('request_id')}")
        print(f"  Faithfulness Score: {result.get('faithfulness_score', 0):.2f}")
        print(f"  Hallucination Detected: {result.get('hallucination_detected')}")
        
        if result.get('claims'):
            print(f"\n  Claims extracted ({len(result['claims'])}):")
            for i, claim in enumerate(result['claims'][:5], 1):
                print(f"    {i}. {claim}")
        
        if result.get('claim_verifications'):
            print(f"\n  Verifications ({len(result['claim_verifications'])}):")
            for cv in result['claim_verifications'][:5]:
                status = cv.get('status', 'unknown')
                if hasattr(status, 'value'):
                    status = status.value
                print(f"    - [{status.upper()}] {cv.get('claim', '')[:60]}...")
        
        print(f"\n  Reasoning: {result.get('reasoning_trace', 'N/A')[:200]}...")
        
        print(f"\n  ✓ PASS - Audit workflow completed")
        test_result = True
        
    except Exception as e:
        import traceback
        print(f"  ✗ FAIL - Error: {e}")
        traceback.print_exc()
        test_result = False
    
    await provider.close()
    return test_result


async def main():
    print("\n")
    print("╔════════════════════════════════════════════════════════════╗")
    print("║           TruthTable Audit Engine - E2E Test               ║")
    print("╚════════════════════════════════════════════════════════════╝")
    print()
    
    results = []
    
    # Test 1: Connection
    results.append(("Ollama Connection", await test_ollama_connection()))
    
    # Test 2: Completion
    results.append(("Simple Completion", await test_simple_completion()))
    
    # Test 3: Full workflow
    results.append(("Audit Workflow", await test_audit_workflow()))
    
    # Summary
    print("\n" + "=" * 60)
    print("SUMMARY")
    print("=" * 60)
    for name, passed in results:
        status = "✓ PASS" if passed else "✗ FAIL"
        print(f"  {status}: {name}")
    
    all_passed = all(r[1] for r in results)
    print(f"\n{'✓ All tests passed!' if all_passed else '✗ Some tests failed!'}")
    
    return 0 if all_passed else 1


if __name__ == "__main__":
    exit_code = asyncio.run(main())
    sys.exit(exit_code)
