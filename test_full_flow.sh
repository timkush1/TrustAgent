#!/bin/bash
# Full flow test script for TrustAgent

echo "🧪 Testing Full Flow: Go Proxy → Python Audit → WebSocket → Dashboard"
echo "=================================================================="

# Check if Go proxy is running
if ! lsof -i:8080 > /dev/null 2>&1; then
    echo "❌ Go proxy is not running on port 8080"
    echo "   Start it with: cd backend-go && go run ./cmd/proxy"
    exit 1
fi
echo "✅ Go proxy is running on port 8080"

# Check WebSocket port
if ! lsof -i:8081 > /dev/null 2>&1; then
    echo "❌ WebSocket server is not running on port 8081"
    exit 1
fi
echo "✅ WebSocket server is running on port 8081"

# Check Python gRPC
if ! lsof -i:50051 > /dev/null 2>&1; then
    echo "❌ Python gRPC server is not running on port 50051"
    echo "   Start it with: cd backend-python && python -m truth_table.grpc_server"
    exit 1
fi
echo "✅ Python gRPC server is running on port 50051"

echo ""
echo "📤 Sending test request..."
echo ""

# Send a test request
RESPONSE=$(curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-test-key" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "What is the capital of France?"}],
    "test_response": "The capital of France is Paris. Paris has been the capital since 508 AD when Clovis I conquered the city."
  }')

echo "Response from proxy:"
echo "$RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$RESPONSE"

echo ""
echo "✅ Request completed!"
echo ""
echo "📊 Open the dashboard at http://localhost:5174 to see the audit results"
echo "   The audit should show:"
echo "   - Claim 1 (Paris is capital): SUPPORTED"
echo "   - Claim 2 (508 AD): UNSUPPORTED (hallucination!)"
