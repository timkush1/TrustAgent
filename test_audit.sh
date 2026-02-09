#!/bin/bash

# Test script to trigger an audit through the TruthTable proxy
# This simulates an LLM request and triggers the full audit pipeline

echo "🚀 Sending test request through TruthTable proxy..."
echo ""

# Send a request to the proxy
# Note: This will try to forward to OpenAI and may fail without API key,
# but the audit should still be triggered

curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [
      {
        "role": "system",
        "content": "You are a helpful assistant."
      },
      {
        "role": "user",
        "content": "What is the capital of France? Is it Paris?"
      }
    ],
    "temperature": 0.7,
    "max_tokens": 150
  }' \
  --max-time 30 \
  -w "\n\n⏱️  Response time: %{time_total}s\n" \
  2>&1

echo ""
echo "✅ Request sent!"
echo ""
echo "👀 Check your dashboard at: http://localhost:5173"
echo "   You should see a new audit appear in the feed"
echo ""
