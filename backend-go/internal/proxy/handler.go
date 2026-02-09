package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/truthtable/backend-go/internal/worker"
)

// Handler is the main proxy handler that intercepts LLM requests
type Handler struct {
	upstreamURL *url.URL
	httpClient  *http.Client
	workerPool  *worker.Pool
}

// NewHandler creates a new proxy handler
func NewHandler(upstream string, pool *worker.Pool) *Handler {
	u, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("Invalid upstream URL: %v", err)
	}

	return &Handler{
		upstreamURL: u,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute, // Long timeout for streaming
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		workerPool: pool,
	}
}

// ChatCompletionRequest represents the OpenAI chat completion request format
type ChatCompletionRequest struct {
	Model        string        `json:"model"`
	Messages     []ChatMessage `json:"messages"`
	Stream       bool          `json:"stream,omitempty"`
	Temperature  float64       `json:"temperature,omitempty"`
	MaxTokens    int           `json:"max_tokens,omitempty"`
	User         string        `json:"user,omitempty"`
	TestResponse string        `json:"test_response,omitempty"` // For testing without real API
}

// ChatMessage represents a single message in the chat
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse represents the non-streaming response
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a completion choice
type Choice struct {
	Index        int          `json:"index"`
	Message      ChatMessage  `json:"message"`
	FinishReason string       `json:"finish_reason"`
	Delta        *ChatMessage `json:"delta,omitempty"` // For streaming
}

// Usage represents token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// AuditJob represents a job to be processed by the worker pool
type AuditJob struct {
	RequestID   string
	Prompt      string
	Response    string
	Model       string
	Timestamp   time.Time
	UserID      string
	RequestPath string
}

// HandleChatCompletion intercepts chat completion requests
func (h *Handler) HandleChatCompletion(c *gin.Context) {
	requestID := c.GetHeader("X-Request-ID")
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// Read the request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	// Parse the request to extract prompt
	var chatReq ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &chatReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON in request body"})
		return
	}

	// Extract the prompt from messages
	prompt := extractPrompt(chatReq.Messages)
	log.Printf("[%s] Intercepted chat completion request (model: %s, stream: %v)",
		requestID, chatReq.Model, chatReq.Stream)

	// TEST MODE: If test_response is provided, use it instead of calling upstream
	if chatReq.TestResponse != "" {
		log.Printf("[%s] TEST MODE: Using provided test_response", requestID)
		h.handleTestResponse(c, requestID, prompt, chatReq)
		return
	}

	// Create upstream request
	upstreamURL := *h.upstreamURL
	upstreamURL.Path = c.Request.URL.Path

	proxyReq, err := http.NewRequest(c.Request.Method, upstreamURL.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create proxy request"})
		return
	}

	// Copy headers (important: Authorization header for API key)
	for key, values := range c.Request.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	proxyReq.Header.Set("X-Request-ID", requestID)

	// Send request to upstream
	resp, err := h.httpClient.Do(proxyReq)
	if err != nil {
		log.Printf("[%s] Upstream request failed: %v", requestID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed"})
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	if chatReq.Stream {
		// Handle streaming response with TeeWriter
		h.handleStreamingResponse(c, resp, requestID, prompt, chatReq)
	} else {
		// Handle non-streaming response
		h.handleNonStreamingResponse(c, resp, requestID, prompt, chatReq)
	}
}

// handleStreamingResponse handles SSE streaming responses
func (h *Handler) handleStreamingResponse(c *gin.Context, resp *http.Response, requestID, prompt string, req ChatCompletionRequest) {
	// Create a TeeWriter to capture the response while streaming to client
	tee := NewTeeWriter()

	c.Status(resp.StatusCode)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Request-ID", requestID)

	// Use Gin's streaming
	c.Stream(func(w io.Writer) bool {
		buf := make([]byte, 1024)
		n, err := resp.Body.Read(buf)
		if n > 0 {
			// Write to client
			w.Write(buf[:n])
			// Capture for audit
			tee.Write(buf[:n])
		}
		if err == io.EOF {
			// Stream complete, submit audit job
			fullResponse := tee.String()
			extractedResponse := extractStreamingContent(fullResponse)

			log.Printf("[%s] Stream complete, captured %d bytes, extracted: %d chars",
				requestID, len(fullResponse), len(extractedResponse))

			// Submit to worker pool for async audit
			if h.workerPool != nil && extractedResponse != "" {
				job := &worker.AuditJob{
					RequestID:   requestID,
					Prompt:      prompt,
					Response:    extractedResponse,
					Model:       req.Model,
					Timestamp:   time.Now(),
					UserID:      req.User,
					RequestPath: "/v1/chat/completions",
				}
				h.workerPool.Submit(job)
			}
			return false
		}
		if err != nil {
			log.Printf("[%s] Stream read error: %v", requestID, err)
			return false
		}
		return true
	})
}

// handleNonStreamingResponse handles regular JSON responses
func (h *Handler) handleNonStreamingResponse(c *gin.Context, resp *http.Response, requestID, prompt string, req ChatCompletionRequest) {
	// Read entire response
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read upstream response"})
		return
	}

	// Parse response to extract assistant message
	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err == nil && len(chatResp.Choices) > 0 {
		responseContent := chatResp.Choices[0].Message.Content

		log.Printf("[%s] Non-streaming response captured (%d chars)", requestID, len(responseContent))

		// Submit to worker pool for async audit
		if h.workerPool != nil && responseContent != "" {
			job := &worker.AuditJob{
				RequestID:   requestID,
				Prompt:      prompt,
				Response:    responseContent,
				Model:       req.Model,
				Timestamp:   time.Now(),
				UserID:      req.User,
				RequestPath: "/v1/chat/completions",
			}
			h.workerPool.Submit(job)
		}
	}

	// Send response to client
	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
}

// HandleCompletion handles legacy /v1/completions endpoint
func (h *Handler) HandleCompletion(c *gin.Context) {
	// For now, forward as-is (legacy endpoint, less common)
	h.HandleGeneric(c)
}

// HandleGeneric forwards any request as-is to upstream
func (h *Handler) HandleGeneric(c *gin.Context) {
	// Create reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(h.upstreamURL)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = h.upstreamURL.Scheme
		req.URL.Host = h.upstreamURL.Host
		req.Host = h.upstreamURL.Host
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

// extractPrompt extracts the user prompt from chat messages
func extractPrompt(messages []ChatMessage) string {
	var parts []string
	for _, msg := range messages {
		if msg.Role == "user" || msg.Role == "system" {
			parts = append(parts, fmt.Sprintf("[%s]: %s", msg.Role, msg.Content))
		}
	}
	return strings.Join(parts, "\n")
}

// extractStreamingContent parses SSE data to extract the actual content
func extractStreamingContent(sseData string) string {
	var contentParts []string
	lines := strings.Split(sseData, "\n")

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err == nil {
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				contentParts = append(contentParts, chunk.Choices[0].Delta.Content)
			}
		}
	}

	return strings.Join(contentParts, "")
}

// TeeWriter captures data while allowing it to be written elsewhere
type TeeWriter struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

// NewTeeWriter creates a new TeeWriter
func NewTeeWriter() *TeeWriter {
	return &TeeWriter{}
}

// Write captures the data
func (t *TeeWriter) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(p)
}

// String returns the captured data as a string
func (t *TeeWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// Bytes returns the captured data as bytes
func (t *TeeWriter) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Bytes()
}

// handleTestResponse handles test mode requests without calling upstream
func (h *Handler) handleTestResponse(c *gin.Context, requestID, prompt string, req ChatCompletionRequest) {
	// Create a mock response
	response := ChatCompletionResponse{
		ID:      "chatcmpl-test-" + requestID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: req.TestResponse,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     len(prompt) / 4,
			CompletionTokens: len(req.TestResponse) / 4,
			TotalTokens:      (len(prompt) + len(req.TestResponse)) / 4,
		},
	}

	// Submit audit job
	if h.workerPool != nil {
		log.Printf("[%s] Submitting test response for audit (prompt: %d chars, response: %d chars)",
			requestID, len(prompt), len(req.TestResponse))
		h.workerPool.Submit(&worker.AuditJob{
			RequestID: requestID,
			Prompt:    prompt,
			Response:  req.TestResponse,
			Model:     req.Model,
			Timestamp: time.Now(),
			UserID:    req.User,
		})
	}

	// Return the mock response
	c.Header("X-Request-ID", requestID)
	c.Header("X-TrustAgent-Mode", "test")
	c.JSON(http.StatusOK, response)
}
