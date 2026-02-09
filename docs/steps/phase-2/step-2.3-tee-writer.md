# Step 2.3: TeeWriter for Streaming

## 🎯 Goal

Implement a **TeeWriter** that captures streaming responses (Server-Sent Events) while simultaneously forwarding them to the client. This enables:

- Real-time response delivery (no latency added)
- Complete response capture for auditing
- Support for SSE (Server-Sent Events) streams

---

## 📚 Prerequisites

- Completed Step 2.2 (Reverse Proxy)
- Understanding of `io.Writer` interface
- Familiarity with SSE format

---

## 🧠 Concepts Explained

### The io.Writer Interface

Go's `io.Writer` is beautifully simple:

```go
type Writer interface {
    Write(p []byte) (n int, err error)
}
```

A TeeWriter writes to two destinations at once:

```
Input → TeeWriter → Primary (client)
             ↓
         Secondary (buffer for audit)
```

### Server-Sent Events (SSE)

LLMs often stream responses using SSE:

```
data: {"content": "Hello"}

data: {"content": " world"}

data: {"content": "!"}

data: [DONE]
```

Each chunk is a `data:` line followed by two newlines.

### Challenge: Streaming + Capture

Without TeeWriter:
```
Option A: Buffer everything → Slow (wait for complete response)
Option B: Stream without capture → Can't audit
```

With TeeWriter:
```
Stream immediately + Capture in background → Fast AND auditable
```

---

## 💻 Implementation

### Step 1: Create TeeWriter

Create `internal/proxy/teewriter.go`:

```go
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// TeeWriter writes to two destinations simultaneously
type TeeWriter struct {
	primary   io.Writer       // The original response writer
	secondary *bytes.Buffer   // Buffer for capturing
	mu        sync.Mutex      // Protects secondary buffer
	flusher   http.Flusher    // For streaming support
}

// NewTeeWriter creates a TeeWriter wrapping the primary writer
func NewTeeWriter(primary io.Writer) *TeeWriter {
	tw := &TeeWriter{
		primary:   primary,
		secondary: new(bytes.Buffer),
	}
	
	// Check if primary supports flushing
	if f, ok := primary.(http.Flusher); ok {
		tw.flusher = f
	}
	
	return tw
}

// Write writes data to both primary and secondary
func (tw *TeeWriter) Write(p []byte) (n int, err error) {
	// Write to primary first (client gets immediate response)
	n, err = tw.primary.Write(p)
	if err != nil {
		return n, err
	}
	
	// Copy to secondary buffer (for audit)
	tw.mu.Lock()
	tw.secondary.Write(p[:n])
	tw.mu.Unlock()
	
	// Flush if streaming
	if tw.flusher != nil {
		tw.flusher.Flush()
	}
	
	return n, nil
}

// Captured returns the captured content
func (tw *TeeWriter) Captured() []byte {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return tw.secondary.Bytes()
}

// Reset clears the captured buffer
func (tw *TeeWriter) Reset() {
	tw.mu.Lock()
	tw.secondary.Reset()
	tw.mu.Unlock()
}

// CapturedString returns the captured content as a string
func (tw *TeeWriter) CapturedString() string {
	return string(tw.Captured())
}


// ResponseTeeWriter wraps http.ResponseWriter with capture capability
type ResponseTeeWriter struct {
	http.ResponseWriter
	tee         *TeeWriter
	statusCode  int
	wroteHeader bool
}

// NewResponseTeeWriter creates a ResponseTeeWriter
func NewResponseTeeWriter(w http.ResponseWriter) *ResponseTeeWriter {
	return &ResponseTeeWriter{
		ResponseWriter: w,
		tee:            NewTeeWriter(w),
		statusCode:     http.StatusOK,
	}
}

// Write writes the data and captures it
func (rtw *ResponseTeeWriter) Write(p []byte) (int, error) {
	if !rtw.wroteHeader {
		rtw.WriteHeader(http.StatusOK)
	}
	return rtw.tee.Write(p)
}

// WriteHeader captures the status code
func (rtw *ResponseTeeWriter) WriteHeader(statusCode int) {
	if rtw.wroteHeader {
		return
	}
	rtw.statusCode = statusCode
	rtw.wroteHeader = true
	rtw.ResponseWriter.WriteHeader(statusCode)
}

// Flush implements http.Flusher
func (rtw *ResponseTeeWriter) Flush() {
	if f, ok := rtw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Captured returns the captured response body
func (rtw *ResponseTeeWriter) Captured() []byte {
	return rtw.tee.Captured()
}

// StatusCode returns the response status code
func (rtw *ResponseTeeWriter) StatusCode() int {
	return rtw.statusCode
}

// Hijack implements http.Hijacker for WebSocket support
func (rtw *ResponseTeeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rtw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("hijacking not supported")
}
```

### Step 2: Add SSE Parser

Create `internal/proxy/sse.go`:

```go
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// SSEEvent represents a single Server-Sent Event
type SSEEvent struct {
	ID    string
	Event string
	Data  string
	Retry int
}

// SSEParser parses Server-Sent Events from a byte stream
type SSEParser struct {
	events []SSEEvent
}

// NewSSEParser creates a new SSE parser
func NewSSEParser() *SSEParser {
	return &SSEParser{
		events: make([]SSEEvent, 0),
	}
}

// Parse parses SSE data from bytes
func (p *SSEParser) Parse(data []byte) []SSEEvent {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	
	var currentEvent SSEEvent
	var dataLines []string
	
	for scanner.Scan() {
		line := scanner.Text()
		
		switch {
		case line == "":
			// Empty line = end of event
			if len(dataLines) > 0 {
				currentEvent.Data = strings.Join(dataLines, "\n")
				p.events = append(p.events, currentEvent)
				currentEvent = SSEEvent{}
				dataLines = nil
			}
			
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			dataLines = append(dataLines, data)
			
		case strings.HasPrefix(line, "event:"):
			currentEvent.Event = strings.TrimPrefix(line, "event:")
			currentEvent.Event = strings.TrimSpace(currentEvent.Event)
			
		case strings.HasPrefix(line, "id:"):
			currentEvent.ID = strings.TrimPrefix(line, "id:")
			currentEvent.ID = strings.TrimSpace(currentEvent.ID)
			
		case strings.HasPrefix(line, "retry:"):
			// Ignore retry for now
		}
	}
	
	// Handle last event if no trailing newline
	if len(dataLines) > 0 {
		currentEvent.Data = strings.Join(dataLines, "\n")
		p.events = append(p.events, currentEvent)
	}
	
	return p.events
}

// ExtractContent extracts the text content from SSE events
// Assumes OpenAI-style streaming format
func (p *SSEParser) ExtractContent(events []SSEEvent) string {
	var content strings.Builder
	
	for _, event := range events {
		if event.Data == "[DONE]" {
			continue
		}
		
		// Try to parse as JSON
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(event.Data), &data); err != nil {
			continue
		}
		
		// OpenAI format: choices[0].delta.content
		if choices, ok := data["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if c, ok := delta["content"].(string); ok {
						content.WriteString(c)
					}
				}
			}
		}
		
		// Alternative: direct content field
		if c, ok := data["content"].(string); ok {
			content.WriteString(c)
		}
		
		// Alternative: response field
		if r, ok := data["response"].(string); ok {
			content.WriteString(r)
		}
	}
	
	return content.String()
}

// CombineSSEContent combines all SSE events into a single content string
func CombineSSEContent(data []byte) string {
	parser := NewSSEParser()
	events := parser.Parse(data)
	return parser.ExtractContent(events)
}

// IsSSEContent checks if content-type indicates SSE
func IsSSEContent(contentType string) bool {
	return strings.Contains(contentType, "text/event-stream")
}
```

### Step 3: Add Missing Imports

Update imports in `internal/proxy/teewriter.go`:

```go
package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)
```

### Step 4: Update Proxy Handler for SSE

Update `internal/proxy/handler.go` to handle streaming:

```go
// Add to modifyResponse method

func (h *Handler) modifyResponse(resp *http.Response) error {
	log := logger.Get()

	// Get captured request from context
	captured, ok := resp.Request.Context().Value("captured_request").(*CapturedRequest)
	if !ok {
		log.Warn("No captured request in context")
		return nil
	}

	contentType := resp.Header.Get("Content-Type")
	
	// Check if this is a streaming response
	if IsSSEContent(contentType) {
		// For SSE, we need to handle differently
		// The TeeWriter will capture as data streams
		log.Debug("SSE response detected, using stream capture")
		return nil
	}

	// For non-streaming, buffer the full response
	if !h.shouldCapture(contentType, resp.StatusCode) {
		return nil
	}

	// Read and buffer the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Failed to read response body", zap.Error(err))
		return err
	}

	// Close original body and replace with buffer
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Create captured response
	capturedResp := &CapturedResponse{
		StatusCode:  resp.StatusCode,
		Headers:     resp.Header.Clone(),
		Body:        bodyBytes,
		ContentType: contentType,
		Duration:    time.Since(captured.Timestamp),
	}

	// Notify callback (async)
	if h.onResponse != nil {
		go h.onResponse(resp.Request.Context(), captured, capturedResp)
	}

	return nil
}
```

### Step 5: Create Streaming Handler

Create `internal/proxy/streaming.go`:

```go
package proxy

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// StreamingHandler handles SSE streaming responses
type StreamingHandler struct {
	onComplete StreamCompleteCallback
}

// StreamCompleteCallback is called when streaming completes
type StreamCompleteCallback func(ctx context.Context, req *CapturedRequest, content string, duration time.Duration)

// NewStreamingHandler creates a new streaming handler
func NewStreamingHandler() *StreamingHandler {
	return &StreamingHandler{}
}

// SetOnComplete sets the completion callback
func (sh *StreamingHandler) SetOnComplete(cb StreamCompleteCallback) {
	sh.onComplete = cb
}

// WrapResponseWriter wraps the response writer for streaming capture
func (sh *StreamingHandler) WrapResponseWriter(c *gin.Context) *ResponseTeeWriter {
	return NewResponseTeeWriter(c.Writer)
}

// HandleStreamComplete processes the completed stream
func (sh *StreamingHandler) HandleStreamComplete(
	ctx context.Context,
	req *CapturedRequest,
	tee *ResponseTeeWriter,
) {
	log := logger.Get()

	// Get captured data
	capturedBytes := tee.Captured()
	duration := time.Since(req.Timestamp)

	log.Debug("Stream completed",
		zap.String("request_id", req.ID),
		zap.Int("captured_bytes", len(capturedBytes)),
		zap.Duration("duration", duration),
	)

	// Parse SSE content
	content := CombineSSEContent(capturedBytes)

	log.Debug("Extracted content",
		zap.String("request_id", req.ID),
		zap.Int("content_length", len(content)),
	)

	// Notify callback
	if sh.onComplete != nil {
		go sh.onComplete(ctx, req, content, duration)
	}
}


// StreamCopyWithCapture copies from reader to writer while capturing
func StreamCopyWithCapture(dst io.Writer, src io.Reader) ([]byte, error) {
	tee := NewTeeWriter(dst)
	
	_, err := io.Copy(tee, src)
	if err != nil {
		return nil, err
	}
	
	return tee.Captured(), nil
}
```

---

## ✅ Testing

### Test 1: TeeWriter Unit Tests

Create `internal/proxy/teewriter_test.go`:

```go
package proxy

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTeeWriter(t *testing.T) {
	primary := new(bytes.Buffer)
	tee := NewTeeWriter(primary)

	data := []byte("Hello, World!")
	n, err := tee.Write(data)

	assert.NoError(t, err)
	assert.Equal(t, len(data), n)
	assert.Equal(t, "Hello, World!", primary.String())
	assert.Equal(t, "Hello, World!", tee.CapturedString())
}

func TestTeeWriterMultipleWrites(t *testing.T) {
	primary := new(bytes.Buffer)
	tee := NewTeeWriter(primary)

	tee.Write([]byte("Hello"))
	tee.Write([]byte(", "))
	tee.Write([]byte("World!"))

	assert.Equal(t, "Hello, World!", primary.String())
	assert.Equal(t, "Hello, World!", tee.CapturedString())
}

func TestResponseTeeWriter(t *testing.T) {
	w := httptest.NewRecorder()
	rtw := NewResponseTeeWriter(w)

	rtw.WriteHeader(200)
	rtw.Write([]byte("Response body"))

	assert.Equal(t, 200, rtw.StatusCode())
	assert.Equal(t, "Response body", string(rtw.Captured()))
	assert.Equal(t, "Response body", w.Body.String())
}
```

### Test 2: SSE Parser Tests

Create `internal/proxy/sse_test.go`:

```go
package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSSEParser(t *testing.T) {
	data := []byte(`data: {"content": "Hello"}

data: {"content": " world"}

data: [DONE]

`)

	parser := NewSSEParser()
	events := parser.Parse(data)

	assert.Len(t, events, 3)
	assert.Equal(t, `{"content": "Hello"}`, events[0].Data)
	assert.Equal(t, `{"content": " world"}`, events[1].Data)
	assert.Equal(t, "[DONE]", events[2].Data)
}

func TestSSEContentExtraction(t *testing.T) {
	data := []byte(`data: {"choices":[{"delta":{"content":"Hello"}}]}

data: {"choices":[{"delta":{"content":" world"}}]}

data: {"choices":[{"delta":{"content":"!"}}]}

data: [DONE]

`)

	content := CombineSSEContent(data)
	assert.Equal(t, "Hello world!", content)
}

func TestSSEContentExtractionSimple(t *testing.T) {
	// Alternative format with direct content field
	data := []byte(`data: {"content": "Test"}

data: {"content": " message"}

`)

	content := CombineSSEContent(data)
	assert.Equal(t, "Test message", content)
}

func TestIsSSEContent(t *testing.T) {
	assert.True(t, IsSSEContent("text/event-stream"))
	assert.True(t, IsSSEContent("text/event-stream; charset=utf-8"))
	assert.False(t, IsSSEContent("application/json"))
	assert.False(t, IsSSEContent("text/plain"))
}
```

### Test 3: Integration Test with Mock SSE Server

```bash
# Create mock SSE server
python3 -c "
from http.server import HTTPServer, BaseHTTPRequestHandler
import time

class SSEHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Cache-Control', 'no-cache')
        self.end_headers()
        
        chunks = ['Hello', ' streaming', ' world', '!']
        for i, chunk in enumerate(chunks):
            data = f'data: {{\"content\": \"{chunk}\"}}\n\n'
            self.wfile.write(data.encode())
            self.wfile.flush()
            time.sleep(0.1)
        
        self.wfile.write(b'data: [DONE]\n\n')
        self.wfile.flush()

HTTPServer(('', 8000), SSEHandler).serve_forever()
"
```

Run the proxy and test:
```bash
# Start proxy
TRUTHTABLE_UPSTREAM_URL=http://localhost:8000 go run ./cmd/proxy

# Test SSE
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{"prompt": "test"}'
# Should see streaming output: Hello streaming world!
```

---

## 📊 Data Flow Diagram

```
┌──────────────────────────────────────────────────────────────────┐
│                     Streaming Response Flow                       │
├──────────────────────────────────────────────────────────────────┤
│                                                                   │
│  Backend          TeeWriter           Client                      │
│     │                 │                  │                        │
│     │──chunk 1────────│──────────────────│                        │
│     │                 │──capture chunk 1─│                        │
│     │──chunk 2────────│──────────────────│                        │
│     │                 │──capture chunk 2─│                        │
│     │──chunk N────────│──────────────────│                        │
│     │                 │──capture chunk N─│                        │
│     │──[DONE]─────────│──────────────────│                        │
│     │                 │                  │                        │
│     │                 │                  │                        │
│     │           ┌─────▼─────┐            │                        │
│     │           │ Combined  │            │                        │
│     │           │  Content  │            │                        │
│     │           └─────┬─────┘            │                        │
│     │                 │                  │                        │
│     │                 ▼                  │                        │
│     │          Audit Queue               │                        │
│                                                                   │
└──────────────────────────────────────────────────────────────────┘
```

---

## 🐛 Common Issues

### Issue: SSE chunks not flushing

**Cause:** Response writer doesn't implement `http.Flusher`
**Solution:** Ensure you're using the standard `http.ResponseWriter` from Gin

### Issue: Content not fully captured

**Cause:** Stream closed before final chunk
**Solution:** Use proper stream completion detection (look for `[DONE]`)

### Issue: Memory usage high with large streams

**Solution:** Add max capture size:
```go
const maxCaptureSize = 1 * 1024 * 1024 // 1MB

func (tw *TeeWriter) Write(p []byte) (n int, err error) {
    n, err = tw.primary.Write(p)
    if err != nil {
        return n, err
    }
    
    tw.mu.Lock()
    if tw.secondary.Len() < maxCaptureSize {
        tw.secondary.Write(p[:n])
    }
    tw.mu.Unlock()
    
    return n, nil
}
```

---

## ⏭️ Next Step

Continue to [Step 2.4: Worker Pool](step-2.4-worker-pool.md) to implement background audit processing.
