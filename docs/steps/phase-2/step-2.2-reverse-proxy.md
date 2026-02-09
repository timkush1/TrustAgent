# Step 2.2: Reverse Proxy

## 🎯 Goal

Build a **reverse proxy** that forwards requests to the upstream RAG/LLM backend. The proxy will:

- Forward incoming requests transparently
- Preserve headers, cookies, and query parameters
- Handle streaming responses (SSE)
- Capture responses for auditing

---

## 📚 Prerequisites

- Completed Step 2.1 (Gin Server)
- Understanding of HTTP proxying

---

## 🧠 Concepts Explained

### What is a Reverse Proxy?

A reverse proxy sits between clients and backend servers:

```
┌──────────┐     ┌─────────────────┐     ┌─────────────┐
│  Client  │ ──→ │  Reverse Proxy  │ ──→ │   Backend   │
│          │ ←── │  (TruthTable)   │ ←── │   (LLM)     │
└──────────┘     └─────────────────┘     └─────────────┘
                        │
                        ↓
                   Audit Engine
```

Benefits:
- **Transparency**: Client doesn't know about the proxy
- **Interception**: Can inspect/modify requests and responses
- **Load Balancing**: Can route to multiple backends

### Go's httputil.ReverseProxy

Go has a built-in reverse proxy:

```go
proxy := &httputil.ReverseProxy{
    Director: func(req *http.Request) {
        req.URL.Scheme = "http"
        req.URL.Host = "backend:8000"
    },
}
```

But we need more control, so we'll wrap it.

### Request/Response Flow

```
1. Client Request arrives at Gin
2. Director modifies the request (target URL)
3. Transport sends to backend
4. Response comes back
5. ModifyResponse processes it
6. TeeWriter copies to audit queue
7. Response sent to client
```

---

## 💻 Implementation

### Step 1: Create the Proxy Handler

Create `internal/proxy/handler.go`:

```go
package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// Handler implements the reverse proxy
type Handler struct {
	config     *config.UpstreamConfig
	proxy      *httputil.ReverseProxy
	transport  *http.Transport
	targetURL  *url.URL
	onResponse ResponseCallback
}

// ResponseCallback is called when a response is received
type ResponseCallback func(ctx context.Context, req *CapturedRequest, resp *CapturedResponse)

// CapturedRequest contains information about the proxied request
type CapturedRequest struct {
	ID          string
	Method      string
	Path        string
	Query       string
	Headers     http.Header
	Body        []byte
	ContentType string
	Timestamp   time.Time
}

// CapturedResponse contains information about the proxied response
type CapturedResponse struct {
	StatusCode  int
	Headers     http.Header
	Body        []byte
	ContentType string
	Duration    time.Duration
}

// NewHandler creates a new proxy handler
func NewHandler(cfg *config.UpstreamConfig) (*Handler, error) {
	log := logger.Get()

	// Parse target URL
	target, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}

	log.Info("Configuring upstream proxy",
		zap.String("target", target.String()),
	)

	// Create custom transport with connection pooling
	transport := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConns,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // Keep original encoding
	}

	h := &Handler{
		config:    cfg,
		targetURL: target,
		transport: transport,
	}

	// Create the reverse proxy
	h.proxy = &httputil.ReverseProxy{
		Director:       h.director,
		Transport:      transport,
		ModifyResponse: h.modifyResponse,
		ErrorHandler:   h.errorHandler,
		BufferPool:     newBufferPool(),
	}

	return h, nil
}

// SetResponseCallback sets the callback for captured responses
func (h *Handler) SetResponseCallback(cb ResponseCallback) {
	h.onResponse = cb
}

// Handle is the Gin handler function
func (h *Handler) Handle(c *gin.Context) {
	log := logger.Get()

	// Generate request ID
	requestID := generateRequestID()
	c.Set("request_id", requestID)

	// Capture the request
	captured, err := h.captureRequest(c, requestID)
	if err != nil {
		log.Error("Failed to capture request", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request"})
		return
	}

	// Store captured request in context for response handler
	ctx := context.WithValue(c.Request.Context(), "captured_request", captured)
	c.Request = c.Request.WithContext(ctx)

	// Forward to upstream
	h.proxy.ServeHTTP(c.Writer, c.Request)
}

// director modifies the outgoing request
func (h *Handler) director(req *http.Request) {
	// Set target
	req.URL.Scheme = h.targetURL.Scheme
	req.URL.Host = h.targetURL.Host

	// Preserve original path or map it
	if h.targetURL.Path != "" && h.targetURL.Path != "/" {
		req.URL.Path = singleJoiningSlash(h.targetURL.Path, req.URL.Path)
	}

	// Set appropriate headers
	if _, ok := req.Header["User-Agent"]; !ok {
		req.Header.Set("User-Agent", "TruthTable-Proxy/1.0")
	}

	// Forward the host header
	req.Host = h.targetURL.Host

	// Add proxy headers
	if clientIP := req.Header.Get("X-Forwarded-For"); clientIP == "" {
		if req.RemoteAddr != "" {
			req.Header.Set("X-Forwarded-For", strings.Split(req.RemoteAddr, ":")[0])
		}
	}
	req.Header.Set("X-Forwarded-Proto", "http")
}

// modifyResponse processes the response from upstream
func (h *Handler) modifyResponse(resp *http.Response) error {
	log := logger.Get()

	// Get captured request from context
	captured, ok := resp.Request.Context().Value("captured_request").(*CapturedRequest)
	if !ok {
		log.Warn("No captured request in context")
		return nil
	}

	// Check if this is a response we should capture
	contentType := resp.Header.Get("Content-Type")
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

// errorHandler handles proxy errors
func (h *Handler) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	log := logger.Get()

	log.Error("Proxy error",
		zap.Error(err),
		zap.String("path", r.URL.Path),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	w.Write([]byte(`{"error": "upstream unavailable"}`))
}

// captureRequest reads and stores the request body
func (h *Handler) captureRequest(c *gin.Context, requestID string) (*CapturedRequest, error) {
	// Read body
	var body []byte
	if c.Request.Body != nil {
		var err error
		body, err = io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}
		// Restore body for forwarding
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
	}

	return &CapturedRequest{
		ID:          requestID,
		Method:      c.Request.Method,
		Path:        c.Request.URL.Path,
		Query:       c.Request.URL.RawQuery,
		Headers:     c.Request.Header.Clone(),
		Body:        body,
		ContentType: c.ContentType(),
		Timestamp:   time.Now(),
	}, nil
}

// shouldCapture determines if we should capture this response
func (h *Handler) shouldCapture(contentType string, statusCode int) bool {
	// Only capture successful JSON responses
	if statusCode < 200 || statusCode >= 300 {
		return false
	}

	// Look for JSON content
	return strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "text/event-stream")
}

// Close cleans up resources
func (h *Handler) Close() {
	h.transport.CloseIdleConnections()
}

// Helper functions

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// Buffer pool for efficient memory use
type bufferPool struct {
	pool *sync.Pool
}

func newBufferPool() *bufferPool {
	return &bufferPool{
		pool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, 32*1024) // 32KB buffers
			},
		},
	}
}

func (bp *bufferPool) Get() []byte {
	return bp.pool.Get().([]byte)
}

func (bp *bufferPool) Put(buf []byte) {
	bp.pool.Put(buf)
}
```

### Step 2: Add Missing Imports

Update the imports at the top of `internal/proxy/handler.go`:

```go
package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/logger"
)
```

### Step 3: Register Proxy Routes

Update `cmd/proxy/main.go`:

```go
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/handlers"
	"github.com/yourorg/truthtable-proxy/internal/logger"
	"github.com/yourorg/truthtable-proxy/internal/proxy"
	"github.com/yourorg/truthtable-proxy/internal/server"
)

const version = "0.1.0"

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		panic("Failed to load config: " + err.Error())
	}

	// Initialize logger
	logger.Init(cfg.Server.Mode)
	defer logger.Sync()

	log := logger.Get()
	log.Info("Starting TruthTable Proxy",
		zap.String("version", version),
	)

	// Create server
	srv := server.New(&cfg.Server)

	// Register health routes
	healthHandler := handlers.NewHealthHandler(version)
	healthHandler.RegisterRoutes(srv.Engine())

	// Create proxy handler
	proxyHandler, err := proxy.NewHandler(&cfg.Upstream)
	if err != nil {
		log.Fatal("Failed to create proxy handler", zap.Error(err))
	}
	defer proxyHandler.Close()

	// Set response callback (for now just log)
	proxyHandler.SetResponseCallback(func(ctx context.Context, req *proxy.CapturedRequest, resp *proxy.CapturedResponse) {
		log.Info("Captured response",
			zap.String("request_id", req.ID),
			zap.Int("status", resp.StatusCode),
			zap.Int("body_size", len(resp.Body)),
			zap.Duration("duration", resp.Duration),
		)
	})

	// Register proxy routes - catch all unmatched routes
	srv.Engine().NoRoute(proxyHandler.Handle)

	// Alternative: specific routes
	// srv.Engine().Any("/api/*path", proxyHandler.Handle)
	// srv.Engine().Any("/v1/*path", proxyHandler.Handle)

	// Start server in goroutine
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("Server error", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info("Received shutdown signal")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", zap.Error(err))
	}

	log.Info("Server stopped")
}
```

---

## ✅ Testing

### Test 1: Start a Mock Backend

First, create a simple mock backend:

```bash
# In a separate terminal, start a simple Python server
python3 -c "
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(length)
        
        response = {
            'response': 'This is a test response from the mock LLM.',
            'model': 'mock-llm',
        }
        
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps(response).encode())
    
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(b'{\"status\": \"ok\"}')

HTTPServer(('', 8000), Handler).serve_forever()
"
```

### Test 2: Run the Proxy

```bash
cd backend-go
TRUTHTABLE_UPSTREAM_URL=http://localhost:8000 go run ./cmd/proxy
```

### Test 3: Test the Proxy

```bash
# GET request through proxy
curl http://localhost:8080/api/test
# Should return {"status": "ok"}

# POST request through proxy
curl -X POST http://localhost:8080/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Hello"}'
# Should return mock LLM response
```

### Test 4: Unit Tests

Create `internal/proxy/handler_test.go`:

```go
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/truthtable-proxy/internal/config"
)

func TestNewHandler(t *testing.T) {
	cfg := &config.UpstreamConfig{
		URL:          "http://localhost:8000",
		Timeout:      30 * time.Second,
		MaxIdleConns: 10,
	}

	handler, err := NewHandler(cfg)
	require.NoError(t, err)
	assert.NotNil(t, handler)
}

func TestProxyForwarding(t *testing.T) {
	// Create mock backend
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer backend.Close()

	// Create proxy handler
	cfg := &config.UpstreamConfig{
		URL:          backend.URL,
		Timeout:      5 * time.Second,
		MaxIdleConns: 10,
	}
	handler, err := NewHandler(cfg)
	require.NoError(t, err)

	// Setup Gin
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/*path", handler.Handle)

	// Make request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "success")
}

func TestResponseCapture(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"answer": "captured"}`))
	}))
	defer backend.Close()

	cfg := &config.UpstreamConfig{
		URL:          backend.URL,
		Timeout:      5 * time.Second,
		MaxIdleConns: 10,
	}
	handler, err := NewHandler(cfg)
	require.NoError(t, err)

	// Track captured response
	var capturedBody []byte
	handler.SetResponseCallback(func(ctx context.Context, req *CapturedRequest, resp *CapturedResponse) {
		capturedBody = resp.Body
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/*path", handler.Handle)

	req := httptest.NewRequest("POST", "/chat", bytes.NewReader([]byte(`{"prompt": "test"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Wait for async callback
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, string(capturedBody), "captured")
}

func TestShouldCapture(t *testing.T) {
	handler := &Handler{}

	tests := []struct {
		contentType string
		statusCode  int
		expected    bool
	}{
		{"application/json", 200, true},
		{"application/json; charset=utf-8", 200, true},
		{"text/event-stream", 200, true},
		{"text/html", 200, false},
		{"application/json", 404, false},
		{"application/json", 500, false},
	}

	for _, tt := range tests {
		result := handler.shouldCapture(tt.contentType, tt.statusCode)
		assert.Equal(t, tt.expected, result, "contentType=%s, status=%d", tt.contentType, tt.statusCode)
	}
}
```

Run tests:
```bash
go test ./internal/proxy/... -v
```

---

## 🐛 Common Issues

### Issue: "connection refused" to upstream

**Cause:** Backend not running or wrong URL
**Solution:** Check `TRUTHTABLE_UPSTREAM_URL` and ensure backend is running

### Issue: Request body not forwarded

**Cause:** Body already read before proxy
**Solution:** Ensure body is restored after reading (we do this with `io.NopCloser`)

### Issue: Large responses timing out

**Solution:** Increase timeouts:
```bash
TRUTHTABLE_UPSTREAM_TIMEOUT=120s ./bin/proxy
```

---

## 📊 Flow Diagram

```
┌───────────────────────────────────────────────────────────────┐
│                        Proxy Handler                          │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│  1. Request Arrives                                           │
│     ↓                                                         │
│  2. captureRequest() - Read & store body                      │
│     ↓                                                         │
│  3. director() - Modify URL, add headers                      │
│     ↓                                                         │
│  4. Transport - Send to upstream                              │
│     ↓                                                         │
│  5. modifyResponse() - Capture response body                  │
│     ↓                                                         │
│  6. ResponseCallback - Async notification                     │
│     ↓                                                         │
│  7. Response sent to client                                   │
│                                                               │
└───────────────────────────────────────────────────────────────┘
```

---

## ⏭️ Next Step

Continue to [Step 2.3: TeeWriter](step-2.3-tee-writer.md) to implement streaming response capture.
