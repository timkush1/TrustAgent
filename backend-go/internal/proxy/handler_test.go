package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractPrompt(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "What is the capital of France?"},
	}

	prompt := extractPrompt(messages)

	if !strings.Contains(prompt, "system") {
		t.Error("Expected prompt to contain 'system'")
	}
	if !strings.Contains(prompt, "user") {
		t.Error("Expected prompt to contain 'user'")
	}
	if !strings.Contains(prompt, "capital of France") {
		t.Error("Expected prompt to contain the user's question")
	}
}

func TestExtractStreamingContent(t *testing.T) {
	sseData := `data: {"choices":[{"delta":{"content":"Hello"}}]}

data: {"choices":[{"delta":{"content":" World"}}]}

data: {"choices":[{"delta":{"content":"!"}}]}

data: [DONE]
`

	content := extractStreamingContent(sseData)

	if content != "Hello World!" {
		t.Errorf("Expected 'Hello World!', got '%s'", content)
	}
}

func TestTeeWriter(t *testing.T) {
	tee := NewTeeWriter()

	n, err := tee.Write([]byte("Hello "))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 6 {
		t.Errorf("Expected 6 bytes written, got %d", n)
	}

	tee.Write([]byte("World!"))

	if tee.String() != "Hello World!" {
		t.Errorf("Expected 'Hello World!', got '%s'", tee.String())
	}
}

func TestChatCompletionRequestParsing(t *testing.T) {
	jsonStr := `{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "Hello!"}
		],
		"stream": true
	}`

	var req ChatCompletionRequest
	err := json.Unmarshal([]byte(jsonStr), &req)
	if err != nil {
		t.Fatalf("Failed to parse request: %v", err)
	}

	if req.Model != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got '%s'", req.Model)
	}
	if !req.Stream {
		t.Error("Expected stream to be true")
	}
	if len(req.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(req.Messages))
	}
}

func TestNewHandler(t *testing.T) {
	handler := NewHandler("https://api.openai.com", nil)

	if handler.upstreamURL.Host != "api.openai.com" {
		t.Errorf("Expected host 'api.openai.com', got '%s'", handler.upstreamURL.Host)
	}
	if handler.httpClient == nil {
		t.Error("Expected httpClient to be initialized")
	}
}

func setupMockUpstream(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			resp := ChatCompletionResponse{
				ID:      "test-id",
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   "gpt-4",
				Choices: []Choice{
					{
						Index: 0,
						Message: ChatMessage{
							Role:    "assistant",
							Content: "Paris is the capital of France.",
						},
						FinishReason: "stop",
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestHandleGenericIntegration(t *testing.T) {
	upstream := setupMockUpstream(t)
	defer upstream.Close()

	handler := NewHandler(upstream.URL, nil)

	if handler.upstreamURL.Host == "" {
		t.Error("Handler not properly initialized")
	}
}
