# Understanding TruthTable: Phase 2 - The Go Proxy

## Table of Contents
1. [What is Phase 2?](#what-is-phase-2)
2. [Architecture Overview](#architecture-overview)
3. [How the Proxy Works](#how-the-proxy-works)
4. [Component Deep Dive](#component-deep-dive)
5. [Data Flow](#data-flow)
6. [Configuration](#configuration)
7. [Testing the Proxy](#testing-the-proxy)
8. [Troubleshooting](#troubleshooting)

---

## What is Phase 2?

Phase 2 implements the **Go Interceptor Proxy** - the component that sits between your application and the LLM API (like OpenAI). Think of it as a "man in the middle" that:

1. **Intercepts** all LLM requests and responses
2. **Forwards** requests to the upstream LLM (OpenAI, Anthropic, etc.)
3. **Captures** the response (even streaming ones!)
4. **Dispatches** the prompt/response pair to the Python audit engine
5. **Broadcasts** audit results to connected dashboards via WebSocket

```
Your App → Go Proxy → OpenAI API
              ↓
         Python Audit Engine
              ↓
         React Dashboard (WebSocket)
```

### Why Go?

Go is perfect for this component because:
- **Performance**: Handles thousands of concurrent connections
- **goroutines**: Lightweight threads for async processing
- **Low latency**: Critical for a proxy - we can't slow down LLM calls
- **Excellent HTTP/gRPC support**: Native support for both protocols

---

## Architecture Overview

### File Structure

```
backend-go/
├── cmd/
│   └── proxy/
│       └── main.go           # Entry point - starts everything
├── internal/
│   ├── config/
│   │   └── config.go         # Environment-based configuration
│   ├── proxy/
│   │   ├── handler.go        # HTTP handlers for LLM requests
│   │   └── handler_test.go   # Unit tests
│   ├── worker/
│   │   ├── pool.go           # Worker pool for async audit dispatch
│   │   └── pool_test.go      # Unit tests
│   ├── grpc/
│   │   └── client.go         # gRPC client to Python audit engine
│   └── websocket/
│       ├── hub.go            # WebSocket hub for real-time updates
│       └── hub_test.go       # Unit tests
├── api/
│   └── audit/
│       └── v1/
│           ├── evaluator.pb.go       # Generated protobuf types
│           └── evaluator_grpc.pb.go  # Generated gRPC client/server
├── go.mod                    # Go module definition
└── go.sum                    # Dependency checksums
```

### Key Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/gin-gonic/gin` | Fast HTTP router |
| `github.com/gorilla/websocket` | WebSocket implementation |
| `google.golang.org/grpc` | gRPC client for Python communication |
| `github.com/google/uuid` | Generate unique request IDs |

---

## How the Proxy Works

### 1. Request Interception

When your app sends a request to `/v1/chat/completions`, the proxy:

```go
// From internal/proxy/handler.go
func (h *Handler) HandleChatCompletion(c *gin.Context) {
    // 1. Read the request body
    bodyBytes, _ := io.ReadAll(c.Request.Body)
    
    // 2. Parse to extract the prompt
    var chatReq ChatCompletionRequest
    json.Unmarshal(bodyBytes, &chatReq)
    
    // 3. Extract user messages for auditing
    prompt := extractPrompt(chatReq.Messages)
    
    // 4. Forward to upstream (OpenAI)
    proxyReq, _ := http.NewRequest(c.Request.Method, upstreamURL, bytes.NewReader(bodyBytes))
    resp, _ := h.httpClient.Do(proxyReq)
    
    // 5. Handle streaming or non-streaming response
    if chatReq.Stream {
        h.handleStreamingResponse(c, resp, requestID, prompt, chatReq)
    } else {
        h.handleNonStreamingResponse(c, resp, requestID, prompt, chatReq)
    }
}
```

### 2. The TeeWriter Pattern

For streaming responses, we use a **TeeWriter** to capture the response while simultaneously sending it to the client. This is critical because:
- We can't buffer the entire response (user would wait forever)
- We can't read the stream twice (it's consumed on first read)

```go
// TeeWriter captures data while it's being written
type TeeWriter struct {
    buf bytes.Buffer
    mu  sync.Mutex
}

func (t *TeeWriter) Write(p []byte) (n int, err error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    return t.buf.Write(p)
}

func (t *TeeWriter) String() string {
    return t.buf.String()
}
```

The streaming handler uses it like this:

```go
func (h *Handler) handleStreamingResponse(c *gin.Context, resp *http.Response, ...) {
    tee := NewTeeWriter()
    
    c.Stream(func(w io.Writer) bool {
        buf := make([]byte, 1024)
        n, err := resp.Body.Read(buf)
        if n > 0 {
            w.Write(buf[:n])      // Send to client immediately
            tee.Write(buf[:n])    // Capture for audit
        }
        if err == io.EOF {
            // Stream complete - submit audit job
            fullResponse := extractStreamingContent(tee.String())
            h.workerPool.Submit(&worker.AuditJob{
                Prompt:   prompt,
                Response: fullResponse,
                // ...
            })
            return false
        }
        return true
    })
}
```

### 3. SSE Parsing

OpenAI uses Server-Sent Events (SSE) for streaming. The format looks like:

```
data: {"choices":[{"delta":{"content":"Hello"}}]}

data: {"choices":[{"delta":{"content":" World"}}]}

data: [DONE]
```

We parse this to extract the actual content:

```go
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
        json.Unmarshal([]byte(data), &chunk)
        if len(chunk.Choices) > 0 {
            contentParts = append(contentParts, chunk.Choices[0].Delta.Content)
        }
    }
    
    return strings.Join(contentParts, "")
}
```

---

## Component Deep Dive

### Worker Pool (`internal/worker/pool.go`)

The worker pool handles async audit dispatch. Key concepts:

**Why a worker pool?**
- We can't block the HTTP response waiting for audits (that would add seconds of latency)
- We need to limit concurrent gRPC calls to the Python engine
- We need graceful shutdown

```go
type Pool struct {
    workers     int              // Number of concurrent workers
    queue       chan *AuditJob   // Buffered channel as queue
    auditClient *grpc.AuditClient
    wsHub       *websocket.Hub
    wg          sync.WaitGroup   // Wait for workers on shutdown
    ctx         context.Context
    cancel      context.CancelFunc
}
```

**Worker lifecycle:**

```go
func (p *Pool) worker(id int) {
    defer p.wg.Done()
    
    for {
        select {
        case <-p.ctx.Done():
            // Shutdown signal received
            return
        case job, ok := <-p.queue:
            if !ok {
                return // Queue closed
            }
            p.processJob(id, job)
        }
    }
}
```

**Fail-open design:**
If the queue is full, we drop the audit job rather than blocking:

```go
func (p *Pool) Submit(job *AuditJob) {
    select {
    case p.queue <- job:
        log.Printf("[%s] Job submitted", job.RequestID)
    default:
        log.Printf("[%s] Queue full, dropping audit job", job.RequestID)
    }
}
```

### WebSocket Hub (`internal/websocket/hub.go`)

The hub broadcasts audit results to all connected dashboards:

```go
type Hub struct {
    clients    map[*Client]bool  // Connected clients
    broadcast  chan *AuditEvent  // Events to send
    register   chan *Client      // New client connections
    unregister chan *Client      // Client disconnections
}
```

**Event types:**
- `connected`: Client successfully connected
- `audit_complete`: Audit finished with results
- `audit_error`: Audit failed

**Broadcast pattern:**

```go
func (h *Hub) Run() {
    for {
        select {
        case client := <-h.register:
            h.clients[client] = true
            
        case client := <-h.unregister:
            delete(h.clients, client)
            close(client.send)
            
        case event := <-h.broadcast:
            data, _ := json.Marshal(event)
            for client := range h.clients {
                select {
                case client.send <- data:
                default:
                    // Client buffer full, disconnect them
                    close(client.send)
                    delete(h.clients, client)
                }
            }
        }
    }
}
```

### gRPC Client (`internal/grpc/client.go`)

Communicates with the Python audit engine:

```go
func (c *AuditClient) Evaluate(ctx context.Context, requestID, prompt, response string) (*AuditResult, error) {
    // 1. Submit the audit
    submission, err := c.client.SubmitAudit(ctx, &pb.AuditRequest{
        RequestId: requestID,
        Query:     prompt,
        Response:  response,
    })
    
    // 2. Poll for result (async pattern)
    for i := 0; i < 30; i++ {
        result, _ := c.client.GetAuditResult(ctx, &pb.AuditResultRequest{
            AuditId: submission.AuditId,
        })
        if result.Status == pb.AuditStatus_AUDIT_STATUS_COMPLETED {
            return convertResult(result), nil
        }
        time.Sleep(100 * time.Millisecond)
    }
    
    return nil, fmt.Errorf("audit timed out")
}
```

---

## Data Flow

### Complete Request Flow

```
1. App sends POST /v1/chat/completions
        ↓
2. Gin router → HandleChatCompletion
        ↓
3. Parse request, extract prompt
        ↓
4. Forward to upstream (OpenAI)
        ↓
5. Stream response to client via TeeWriter
        ↓
6. On stream complete, submit AuditJob to worker pool
        ↓
7. Worker picks up job from queue
        ↓
8. Worker calls Python gRPC: SubmitAudit + GetAuditResult
        ↓
9. Worker broadcasts result to WebSocket hub
        ↓
10. Hub sends to all connected dashboards
```

### Timing Diagram

```
Client          Go Proxy           OpenAI         Python Engine     Dashboard
  |                |                  |                 |                |
  |---Request----->|                  |                 |                |
  |                |---Forward------->|                 |                |
  |                |<--Stream chunk---|                 |                |
  |<--Stream chunk-|                  |                 |                |
  |                |<--Stream chunk---|                 |                |
  |<--Stream chunk-|                  |                 |                |
  |                |<--[DONE]---------|                 |                |
  |<--[DONE]-------|                  |                 |                |
  |                |                  |                 |                |
  |                |  (async)         |                 |                |
  |                |-------SubmitAudit--------------->|                |
  |                |<------AuditSubmission------------|                |
  |                |-------GetAuditResult------------->|                |
  |                |<------AuditResult----------------|                |
  |                |                  |                 |                |
  |                |---------WebSocket broadcast----------------------->|
  |                |                  |                 |                |
```

---

## Configuration

All configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUTHTABLE_PORT` | 8080 | HTTP server port |
| `TRUTHTABLE_WS_PORT` | 8081 | WebSocket server port |
| `TRUTHTABLE_UPSTREAM_URL` | https://api.openai.com | LLM API to proxy |
| `TRUTHTABLE_GRPC_ADDRESS` | localhost:50051 | Python audit engine |
| `TRUTHTABLE_GRPC_TIMEOUT` | 30s | Audit timeout |
| `TRUTHTABLE_WORKER_COUNT` | 10 | Concurrent audit workers |
| `TRUTHTABLE_QUEUE_SIZE` | 1000 | Max pending audits |
| `TRUTHTABLE_READ_TIMEOUT` | 30s | HTTP read timeout |
| `TRUTHTABLE_WRITE_TIMEOUT` | 120s | HTTP write timeout (long for streaming) |

---

## Testing the Proxy

### Unit Tests

```bash
cd backend-go
go test ./... -v
```

Current test coverage:
- `internal/proxy`: 6 tests (TeeWriter, request parsing, prompt extraction)
- `internal/websocket`: 5 tests (Hub, client ID generation)
- `internal/worker`: 5 tests (Pool, job handling, truncation)

### Running the Proxy

```bash
# Build the binary
cd backend-go
go build -o proxy ./cmd/proxy

# Run with defaults (connects to local Python engine)
./proxy

# Or with custom config
TRUTHTABLE_UPSTREAM_URL=http://localhost:11434 \
TRUTHTABLE_PORT=9090 \
./proxy
```

### Integration Test

```bash
# Start Python engine first
cd ../backend-python
poetry run python -m truthtable.main

# Start proxy
cd ../backend-go
./proxy

# Test the proxy
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "What is 2+2?"}]
  }'
```

### WebSocket Test

```javascript
// Connect to WebSocket
const ws = new WebSocket('ws://localhost:8081/ws');

ws.onmessage = (event) => {
    const data = JSON.parse(event.data);
    console.log('Audit result:', data);
};
```

---

## Troubleshooting

### Proxy starts but audits fail

**Symptom:** Requests work but no audits happen
**Check:** 
```bash
# Is Python engine running?
curl http://localhost:50051/health

# Check proxy logs for:
# "⚠️ Warning: Could not connect to audit engine"
```

### Queue full warnings

**Symptom:** `[req-xxx] Worker queue full, dropping audit job`
**Fix:** Increase worker count or queue size:
```bash
TRUTHTABLE_WORKER_COUNT=20 TRUTHTABLE_QUEUE_SIZE=5000 ./proxy
```

### WebSocket not connecting

**Symptom:** Dashboard shows disconnected
**Check:**
```bash
# Test WebSocket endpoint
websocat ws://localhost:8081/ws

# Should receive: {"type":"connected","request_id":"..."}
```

### Streaming responses broken

**Symptom:** Client receives jumbled or incomplete responses
**Check:**
- Ensure `Content-Type: text/event-stream` header is preserved
- Check for proxy timeouts (increase `TRUTHTABLE_WRITE_TIMEOUT`)

---

## What's Next?

**Phase 3: React Dashboard**
- Connect to WebSocket for real-time updates
- Display audit results with trust scores
- Show claim verification details

**Phase 4: Integration Testing**
- Full end-to-end tests
- Load testing with multiple concurrent requests
- Chaos engineering (what happens when Python dies?)

---

## Quick Reference

### Start Everything

```bash
# Terminal 1: Docker services
cd /path/to/trustAgent
docker compose up -d

# Terminal 2: Python engine
cd backend-python
source .venv/bin/activate
poetry run python -m truthtable.main

# Terminal 3: Go proxy
cd backend-go
go run ./cmd/proxy

# Terminal 4: Test
curl -X POST http://localhost:8080/health
```

### Key Files to Understand

1. [cmd/proxy/main.go](../backend-go/cmd/proxy/main.go) - Entry point
2. [internal/proxy/handler.go](../backend-go/internal/proxy/handler.go) - HTTP handling
3. [internal/worker/pool.go](../backend-go/internal/worker/pool.go) - Async dispatch
4. [internal/websocket/hub.go](../backend-go/internal/websocket/hub.go) - Real-time updates

---

*Document created as part of Phase 2 implementation. All tests pass: 16/16 Go tests.*
