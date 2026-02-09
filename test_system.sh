#!/bin/bash

# Test audit with mock/simulated data
# This creates a test audit without needing external API keys

echo "🧪 Testing TruthTable Audit System"
echo "=================================="
echo ""

# Test 1: Check if all services are responding
echo "1️⃣  Checking services..."

if curl -s http://localhost:5173 > /dev/null 2>&1; then
    echo "   ✅ React Dashboard (port 5173)"
else
    echo "   ❌ React Dashboard not running"
fi

if curl -s http://localhost:8080/health > /dev/null 2>&1; then
    echo "   ✅ Go Proxy (port 8080)"
else
    echo "   ❌ Go Proxy not running"
fi

if lsof -i :8081 > /dev/null 2>&1; then
    echo "   ✅ WebSocket Server (port 8081)"
else
    echo "   ❌ WebSocket Server not running"
fi

if lsof -i :50051 > /dev/null 2>&1; then
    echo "   ✅ Python Audit Engine (port 50051)"
else
    echo "   ❌ Python Audit Engine not running"
fi

echo ""
echo "2️⃣  Sending test request..."
echo ""

# Create a test using Ollama (local, no API key needed)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Upstream-URL: http://localhost:11434/api/chat" \
  -d '{
    "model": "llama3.2",
    "messages": [
      {
        "role": "user",
        "content": "What is 2+2? Answer in one sentence."
      }
    ],
    "stream": false
  }' \
  --max-time 30 \
  -s \
  2>&1 | head -c 200

echo ""
echo ""
echo "=================================="
echo "✅ Test complete!"
echo ""
echo "📊 Open dashboard: http://localhost:5173"
echo "   Look for new audit in the feed"
echo ""
echo "💡 The audit may take a few seconds to appear"
echo "   as it's being processed by the Python engine"
echo ""
