# Step 2.5: gRPC Client

## 🎯 Goal

Implement a **gRPC client** in Go that connects to the Python audit service. This enables:

- Fast binary communication between Go proxy and Python engine
- Type-safe API based on Protocol Buffers
- Connection pooling and retry logic

---

## 📚 Prerequisites

- Completed Step 2.4 (Worker Pool)
- Completed Step 0.2 (Protobuf Setup)
- Python audit service running (Phase 1)

---

## 🧠 Concepts Explained

### gRPC in Go

Go has excellent gRPC support via `google.golang.org/grpc`:

```go
// Create connection
conn, _ := grpc.Dial("localhost:50051", grpc.WithInsecure())

// Create client stub
client := pb.NewEvaluatorServiceClient(conn)

// Call method
result, _ := client.Evaluate(ctx, &pb.AuditRequest{...})
```

### Connection Management

gRPC connections should be:
- **Long-lived**: Reuse connections across requests
- **Pool-aware**: Multiple connections for high throughput
- **Monitored**: Health checks and reconnection logic

### Retry Strategy

For transient failures:
```
Attempt 1 → Fail → Wait 1s
Attempt 2 → Fail → Wait 2s
Attempt 3 → Fail → Wait 4s
Attempt 4 → Success!
```

---

## 💻 Implementation

### Step 1: Generate Go gRPC Stubs

Create `backend-go/scripts/generate_proto.sh`:

```bash
#!/bin/bash
# Generate Go gRPC code from proto files

set -e

PROTO_DIR="../proto"
OUT_DIR="pkg/proto"

# Install protoc plugins if needed
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Create output directory
mkdir -p "$OUT_DIR"

# Generate Go code
protoc \
    -I"$PROTO_DIR" \
    --go_out="$OUT_DIR" \
    --go_opt=paths=source_relative \
    --go-grpc_out="$OUT_DIR" \
    --go-grpc_opt=paths=source_relative \
    "$PROTO_DIR/evaluator.proto"

echo "✓ Generated Go gRPC stubs in $OUT_DIR"
```

Run it:
```bash
cd backend-go
chmod +x scripts/generate_proto.sh
./scripts/generate_proto.sh
```

### Step 2: Create the gRPC Client

Create `internal/client/grpc.go`:

```go
package client

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/logger"
	pb "github.com/yourorg/truthtable-proxy/pkg/proto"
)

// AuditClient wraps the gRPC client with connection management
type AuditClient struct {
	config     *config.AuditConfig
	conn       *grpc.ClientConn
	client     pb.EvaluatorServiceClient
	mu         sync.RWMutex
	healthy    bool
	lastCheck  time.Time
}

// NewAuditClient creates a new gRPC client
func NewAuditClient(cfg *config.AuditConfig) (*AuditClient, error) {
	c := &AuditClient{
		config: cfg,
	}

	if err := c.connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// connect establishes the gRPC connection
func (c *AuditClient) connect() error {
	log := logger.Get()

	log.Info("Connecting to audit service",
		zap.String("address", c.config.GRPCAddress),
	)

	// Connection options
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024), // 50MB
			grpc.MaxCallSendMsgSize(50*1024*1024),
		),
	}

	conn, err := grpc.Dial(c.config.GRPCAddress, opts...)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.client = pb.NewEvaluatorServiceClient(conn)
	c.healthy = true
	c.mu.Unlock()

	log.Info("Connected to audit service")
	return nil
}

// Evaluate sends an audit request and returns the result
func (c *AuditClient) Evaluate(ctx context.Context, req *AuditRequest) (*AuditResult, error) {
	log := logger.Get()

	// Convert to protobuf
	pbReq := c.toProtoRequest(req)

	// Execute with retry
	var pbResp *pb.AuditResult
	var err error

	for attempt := 0; attempt <= c.config.RetryAttempts; attempt++ {
		if attempt > 0 {
			backoff := c.config.RetryBackoff * time.Duration(1<<(attempt-1))
			log.Debug("Retrying audit request",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
			)
			time.Sleep(backoff)
		}

		// Set timeout
		callCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
		pbResp, err = c.client.Evaluate(callCtx, pbReq)
		cancel()

		if err == nil {
			break
		}

		// Check if retryable
		if !c.isRetryable(err) {
			log.Error("Non-retryable audit error",
				zap.Error(err),
				zap.String("request_id", req.RequestID),
			)
			return nil, err
		}

		log.Warn("Audit request failed, will retry",
			zap.Error(err),
			zap.Int("attempt", attempt),
		)
	}

	if err != nil {
		c.markUnhealthy()
		return nil, err
	}

	// Convert response
	return c.fromProtoResponse(pbResp), nil
}

// HealthCheck verifies the connection
func (c *AuditClient) HealthCheck(ctx context.Context) (bool, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := client.HealthCheck(ctx, &pb.HealthRequest{})
	if err != nil {
		c.markUnhealthy()
		return false, err
	}

	healthy := resp.Status == "healthy"
	c.mu.Lock()
	c.healthy = healthy
	c.lastCheck = time.Now()
	c.mu.Unlock()

	return healthy, nil
}

// IsHealthy returns cached health status
func (c *AuditClient) IsHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.healthy
}

// Close closes the gRPC connection
func (c *AuditClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Reconnect attempts to reconnect
func (c *AuditClient) Reconnect() error {
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()

	return c.connect()
}

// markUnhealthy sets the client as unhealthy
func (c *AuditClient) markUnhealthy() {
	c.mu.Lock()
	c.healthy = false
	c.mu.Unlock()
}

// isRetryable checks if an error is retryable
func (c *AuditClient) isRetryable(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}

	switch st.Code() {
	case codes.Unavailable,
		codes.ResourceExhausted,
		codes.Aborted,
		codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// toProtoRequest converts to protobuf request
func (c *AuditClient) toProtoRequest(req *AuditRequest) *pb.AuditRequest {
	pbDocs := make([]*pb.ContextDocument, len(req.ContextDocs))
	for i, doc := range req.ContextDocs {
		pbDocs[i] = &pb.ContextDocument{
			Id:       doc.ID,
			Content:  doc.Content,
			Source:   doc.Source,
			Metadata: doc.Metadata,
		}
	}

	return &pb.AuditRequest{
		RequestId:        req.RequestID,
		UserQuery:        req.UserQuery,
		LlmResponse:      req.LLMResponse,
		ContextDocuments: pbDocs,
	}
}

// fromProtoResponse converts from protobuf response
func (c *AuditClient) fromProtoResponse(resp *pb.AuditResult) *AuditResult {
	verifications := make([]ClaimVerification, len(resp.Verifications))
	for i, v := range resp.Verifications {
		verifications[i] = ClaimVerification{
			Claim:      v.Claim,
			Supported:  v.Supported,
			Confidence: v.Confidence,
			Evidence:   v.Evidence,
			Reasoning:  v.Reasoning,
		}
	}

	return &AuditResult{
		RequestID:        resp.RequestId,
		TrustScore:       resp.TrustScore,
		HallucinationRate: resp.HallucinationRate,
		TotalClaims:      int(resp.TotalClaims),
		SupportedClaims:  int(resp.SupportedClaims),
		UnsupportedClaims: int(resp.UnsupportedClaims),
		Grade:            resp.Grade,
		Verdict:          resp.Verdict,
		Verifications:    verifications,
		ProcessingTimeMs: int(resp.ProcessingTimeMs),
	}
}
```

### Step 3: Define Client Types

Create `internal/client/types.go`:

```go
package client

// AuditRequest is the request to the audit service
type AuditRequest struct {
	RequestID   string
	UserQuery   string
	LLMResponse string
	ContextDocs []ContextDocument
}

// ContextDocument represents a context document
type ContextDocument struct {
	ID       string
	Content  string
	Source   string
	Metadata map[string]string
}

// AuditResult is the result from the audit service
type AuditResult struct {
	RequestID         string
	TrustScore        float64
	HallucinationRate float64
	TotalClaims       int
	SupportedClaims   int
	UnsupportedClaims int
	Grade             string
	Verdict           string
	Verifications     []ClaimVerification
	ProcessingTimeMs  int
}

// ClaimVerification is the verification result for a single claim
type ClaimVerification struct {
	Claim      string
	Supported  bool
	Confidence float64
	Evidence   []string
	Reasoning  string
}
```

### Step 4: Update the Audit Handler

Update `internal/worker/audit.go`:

```go
package worker

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/client"
	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// AuditJob contains the data needed for an audit
type AuditJob struct {
	RequestID    string
	UserQuery    string
	LLMResponse  string
	ContextDocs  []ContextDoc
	Timestamp    time.Time
}

// ContextDoc represents a context document
type ContextDoc struct {
	ID       string
	Content  string
	Source   string
	Metadata map[string]string
}

// AuditHandler processes audit jobs using gRPC
type AuditHandler struct {
	client  *client.AuditClient
	timeout time.Duration
}

// NewAuditHandler creates a new audit handler
func NewAuditHandler(auditClient *client.AuditClient, timeout time.Duration) *AuditHandler {
	return &AuditHandler{
		client:  auditClient,
		timeout: timeout,
	}
}

// Handle processes an audit job
func (h *AuditHandler) Handle(ctx context.Context, job Job) Result {
	log := logger.Get()

	auditJob, ok := job.Payload.(AuditJob)
	if !ok {
		return Result{
			JobID:   job.ID,
			Success: false,
			Error:   errors.New("invalid job payload type"),
		}
	}

	log.Debug("Processing audit job",
		zap.String("request_id", auditJob.RequestID),
		zap.Int("response_length", len(auditJob.LLMResponse)),
	)

	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	// Convert to client request
	req := &client.AuditRequest{
		RequestID:   auditJob.RequestID,
		UserQuery:   auditJob.UserQuery,
		LLMResponse: auditJob.LLMResponse,
		ContextDocs: h.convertContextDocs(auditJob.ContextDocs),
	}

	// Call the audit service
	result, err := h.client.Evaluate(ctx, req)
	if err != nil {
		log.Error("Audit request failed",
			zap.String("request_id", auditJob.RequestID),
			zap.Error(err),
		)
		return Result{
			JobID:   job.ID,
			Success: false,
			Error:   err,
		}
	}

	log.Info("Audit completed",
		zap.String("request_id", auditJob.RequestID),
		zap.Float64("trust_score", result.TrustScore),
		zap.String("grade", result.Grade),
	)

	return Result{
		JobID:   job.ID,
		Success: true,
		Data:    result,
	}
}

// convertContextDocs converts worker context docs to client format
func (h *AuditHandler) convertContextDocs(docs []ContextDoc) []client.ContextDocument {
	result := make([]client.ContextDocument, len(docs))
	for i, doc := range docs {
		result[i] = client.ContextDocument{
			ID:       doc.ID,
			Content:  doc.Content,
			Source:   doc.Source,
			Metadata: doc.Metadata,
		}
	}
	return result
}
```

### Step 5: Update Main to Use Client

Update `cmd/proxy/main.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/client"
	"github.com/yourorg/truthtable-proxy/internal/config"
	"github.com/yourorg/truthtable-proxy/internal/handlers"
	"github.com/yourorg/truthtable-proxy/internal/logger"
	"github.com/yourorg/truthtable-proxy/internal/proxy"
	"github.com/yourorg/truthtable-proxy/internal/server"
	"github.com/yourorg/truthtable-proxy/internal/worker"
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

	// Create gRPC client
	var auditClient *client.AuditClient
	if cfg.Audit.Enabled {
		var err error
		auditClient, err = client.NewAuditClient(&cfg.Audit)
		if err != nil {
			log.Warn("Failed to connect to audit service, auditing disabled",
				zap.Error(err),
			)
		} else {
			defer auditClient.Close()
		}
	}

	// Create audit handler
	auditHandler := worker.NewAuditHandler(auditClient, cfg.Audit.Timeout)

	// Create worker pool for audits
	auditPool := worker.NewPool(worker.Config{
		Name:      "audit",
		Workers:   cfg.Audit.WorkerCount,
		QueueSize: cfg.Audit.QueueSize,
		Handler:   auditHandler.Handle,
	})
	auditPool.Start()

	// Start result processor in background
	go processAuditResults(auditPool.Results())

	// Create server
	srv := server.New(&cfg.Server)

	// Register health routes (with audit client status)
	healthHandler := handlers.NewHealthHandler(version)
	if auditClient != nil {
		healthHandler.SetAuditClient(auditClient)
	}
	healthHandler.RegisterRoutes(srv.Engine())

	// Create proxy handler
	proxyHandler, err := proxy.NewHandler(&cfg.Upstream)
	if err != nil {
		log.Fatal("Failed to create proxy handler", zap.Error(err))
	}
	defer proxyHandler.Close()

	// Set response callback to submit to worker pool
	proxyHandler.SetResponseCallback(func(ctx context.Context, req *proxy.CapturedRequest, resp *proxy.CapturedResponse) {
		if auditClient == nil {
			return
		}

		auditJob := worker.AuditJob{
			RequestID:   req.ID,
			UserQuery:   extractUserQuery(req.Body),
			LLMResponse: string(resp.Body),
			Timestamp:   req.Timestamp,
		}

		job := worker.Job{
			ID:        req.ID,
			Payload:   auditJob,
			CreatedAt: time.Now(),
		}

		if !auditPool.Submit(job) {
			log.Warn("Failed to submit audit job",
				zap.String("request_id", req.ID),
			)
		}
	})

	// Register proxy routes
	srv.Engine().NoRoute(proxyHandler.Handle)

	// Start server
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

	// Stop worker pool
	auditPool.Stop(cfg.Audit.Timeout)

	// Graceful server shutdown
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", zap.Error(err))
	}

	log.Info("Server stopped")
}

// ... rest of the code (processAuditResults, extractUserQuery)
```

---

## ✅ Testing

### Test 1: Start Python Audit Service

```bash
cd backend-python
poetry run python -m truthtable.grpc.server --port 50051
```

### Test 2: Run Go Proxy

```bash
cd backend-go
TRUTHTABLE_AUDIT_GRPC_ADDRESS=localhost:50051 go run ./cmd/proxy
```

### Test 3: Unit Tests

Create `internal/client/grpc_test.go`:

```go
package client

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yourorg/truthtable-proxy/internal/config"
)

func TestNewAuditClient(t *testing.T) {
	// Skip if no server running
	cfg := &config.AuditConfig{
		GRPCAddress:   "localhost:50051",
		Timeout:       5 * time.Second,
		RetryAttempts: 1,
		RetryBackoff:  100 * time.Millisecond,
	}

	client, err := NewAuditClient(cfg)
	if err != nil {
		t.Skip("Audit service not running")
	}
	defer client.Close()

	assert.NotNil(t, client)
}

func TestHealthCheck(t *testing.T) {
	cfg := &config.AuditConfig{
		GRPCAddress:   "localhost:50051",
		Timeout:       5 * time.Second,
		RetryAttempts: 1,
		RetryBackoff:  100 * time.Millisecond,
	}

	client, err := NewAuditClient(cfg)
	if err != nil {
		t.Skip("Audit service not running")
	}
	defer client.Close()

	ctx := context.Background()
	healthy, err := client.HealthCheck(ctx)
	
	require.NoError(t, err)
	assert.True(t, healthy)
}

func TestEvaluate(t *testing.T) {
	cfg := &config.AuditConfig{
		GRPCAddress:   "localhost:50051",
		Timeout:       30 * time.Second,
		RetryAttempts: 1,
		RetryBackoff:  100 * time.Millisecond,
	}

	client, err := NewAuditClient(cfg)
	if err != nil {
		t.Skip("Audit service not running")
	}
	defer client.Close()

	req := &AuditRequest{
		RequestID:   "test-123",
		UserQuery:   "What is the capital of France?",
		LLMResponse: "The capital of France is Paris.",
		ContextDocs: []ContextDocument{
			{
				ID:      "doc1",
				Content: "Paris is the capital and largest city of France.",
				Source:  "Wikipedia",
			},
		},
	}

	ctx := context.Background()
	result, err := client.Evaluate(ctx, req)
	
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-123", result.RequestID)
	assert.Greater(t, result.TrustScore, 0.0)
	assert.NotEmpty(t, result.Grade)
}
```

### Test 4: Integration Test

```bash
# Terminal 1: Python service
cd backend-python
poetry run python -m truthtable.grpc.server

# Terminal 2: Mock backend
python3 -c "
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(json.dumps({
            'response': 'Paris is the capital of France. It has a population of 2 million.'
        }).encode())

HTTPServer(('', 8000), Handler).serve_forever()
"

# Terminal 3: Go proxy
cd backend-go
TRUTHTABLE_UPSTREAM_URL=http://localhost:8000 \
TRUTHTABLE_AUDIT_GRPC_ADDRESS=localhost:50051 \
go run ./cmd/proxy

# Terminal 4: Send request
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{"query": "What is the capital of France?"}'
```

Check the Go proxy logs for audit completion messages.

---

## 🐛 Common Issues

### Issue: "connection refused"

**Cause:** Python audit service not running
**Solution:** Start the Python service first

### Issue: "context deadline exceeded"

**Cause:** Timeout too short for LLM processing
**Solution:** Increase `TRUTHTABLE_AUDIT_TIMEOUT=60s`

### Issue: Proto type mismatch

**Cause:** Proto files out of sync
**Solution:** Regenerate both Go and Python stubs

---

## ⏭️ Next Step

Continue to [Step 2.6: WebSocket Hub](step-2.6-websocket-hub.md) to broadcast audit results to the dashboard.
