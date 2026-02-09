# Step 2.4: Worker Pool

## 🎯 Goal

Implement a **Worker Pool** that processes audit requests in the background. This ensures:

- Non-blocking response delivery to clients
- Controlled concurrency for audit processing
- Graceful handling of backpressure

---

## 📚 Prerequisites

- Completed Step 2.3 (TeeWriter)
- Understanding of Go channels and goroutines

---

## 🧠 Concepts Explained

### Why a Worker Pool?

Without a pool:
```go
// Bad: Unlimited goroutines
go processAudit(request)  // Could spawn thousands!
```

With a pool:
```go
// Good: Controlled concurrency
pool.Submit(request)  // Max 10 workers
```

Benefits:
- **Resource Control**: Fixed number of goroutines
- **Backpressure**: Queue fills up = slow down
- **Graceful Shutdown**: Wait for in-flight work

### Worker Pool Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      Worker Pool                        │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────┐     ┌─────────────────────┐               │
│  │ Submit  │ ──→ │     Job Queue       │               │
│  └─────────┘     │  (buffered channel) │               │
│                  └──────────┬──────────┘               │
│                             │                           │
│          ┌──────────────────┼──────────────────┐       │
│          ↓                  ↓                  ↓        │
│    ┌──────────┐      ┌──────────┐      ┌──────────┐   │
│    │ Worker 1 │      │ Worker 2 │      │ Worker N │   │
│    └────┬─────┘      └────┬─────┘      └────┬─────┘   │
│         │                 │                  │          │
│         └─────────────────┼──────────────────┘          │
│                           ↓                             │
│                    Process Job                          │
│                           │                             │
│                           ↓                             │
│                   Result Callback                       │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Go Patterns Used

1. **Buffered Channel**: Acts as job queue
2. **WaitGroup**: Track active workers for shutdown
3. **Context**: Propagate cancellation
4. **Select**: Handle multiple channel operations

---

## 💻 Implementation

### Step 1: Create the Worker Pool

Create `internal/worker/pool.go`:

```go
package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/yourorg/truthtable-proxy/internal/logger"
)

// Job represents a unit of work
type Job struct {
	ID        string
	Payload   interface{}
	CreatedAt time.Time
}

// Result represents the outcome of a job
type Result struct {
	JobID     string
	Success   bool
	Error     error
	Data      interface{}
	Duration  time.Duration
}

// Handler processes a job and returns a result
type Handler func(ctx context.Context, job Job) Result

// Pool manages a pool of workers
type Pool struct {
	name       string
	workers    int
	queueSize  int
	handler    Handler
	jobs       chan Job
	results    chan Result
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	running    atomic.Bool
	
	// Metrics
	submitted  atomic.Int64
	completed  atomic.Int64
	failed     atomic.Int64
}

// Config for the worker pool
type Config struct {
	Name       string
	Workers    int
	QueueSize  int
	Handler    Handler
}

// NewPool creates a new worker pool
func NewPool(cfg Config) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	
	p := &Pool{
		name:      cfg.Name,
		workers:   cfg.Workers,
		queueSize: cfg.QueueSize,
		handler:   cfg.Handler,
		jobs:      make(chan Job, cfg.QueueSize),
		results:   make(chan Result, cfg.QueueSize),
		ctx:       ctx,
		cancel:    cancel,
	}
	
	return p
}

// Start launches the worker goroutines
func (p *Pool) Start() {
	log := logger.Get()
	
	if p.running.Swap(true) {
		log.Warn("Pool already running", zap.String("pool", p.name))
		return
	}
	
	log.Info("Starting worker pool",
		zap.String("pool", p.name),
		zap.Int("workers", p.workers),
		zap.Int("queue_size", p.queueSize),
	)
	
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// worker is the main worker loop
func (p *Pool) worker(id int) {
	defer p.wg.Done()
	
	log := logger.Get().With(
		zap.String("pool", p.name),
		zap.Int("worker_id", id),
	)
	
	log.Debug("Worker started")
	
	for {
		select {
		case <-p.ctx.Done():
			log.Debug("Worker shutting down")
			return
			
		case job, ok := <-p.jobs:
			if !ok {
				log.Debug("Jobs channel closed")
				return
			}
			
			result := p.processJob(job)
			
			// Send result (non-blocking)
			select {
			case p.results <- result:
			default:
				log.Warn("Result channel full, dropping result",
					zap.String("job_id", job.ID),
				)
			}
		}
	}
}

// processJob executes the handler for a job
func (p *Pool) processJob(job Job) Result {
	log := logger.Get()
	start := time.Now()
	
	log.Debug("Processing job",
		zap.String("pool", p.name),
		zap.String("job_id", job.ID),
	)
	
	// Execute handler with context
	result := p.handler(p.ctx, job)
	result.Duration = time.Since(start)
	
	// Update metrics
	p.completed.Add(1)
	if !result.Success {
		p.failed.Add(1)
	}
	
	log.Debug("Job completed",
		zap.String("pool", p.name),
		zap.String("job_id", job.ID),
		zap.Bool("success", result.Success),
		zap.Duration("duration", result.Duration),
	)
	
	return result
}

// Submit adds a job to the queue
func (p *Pool) Submit(job Job) bool {
	if !p.running.Load() {
		return false
	}
	
	select {
	case p.jobs <- job:
		p.submitted.Add(1)
		return true
	default:
		// Queue is full
		logger.Get().Warn("Worker pool queue full",
			zap.String("pool", p.name),
			zap.String("job_id", job.ID),
		)
		return false
	}
}

// SubmitAndWait adds a job and waits for the result
func (p *Pool) SubmitAndWait(ctx context.Context, job Job) (Result, error) {
	if !p.Submit(job) {
		return Result{}, ErrQueueFull
	}
	
	// Wait for result with timeout
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case result := <-p.results:
			if result.JobID == job.ID {
				return result, nil
			}
			// Put back if not our result
			p.results <- result
		}
	}
}

// Results returns the results channel for async processing
func (p *Pool) Results() <-chan Result {
	return p.results
}

// Stop gracefully shuts down the pool
func (p *Pool) Stop(timeout time.Duration) {
	log := logger.Get()
	
	if !p.running.Swap(false) {
		return
	}
	
	log.Info("Stopping worker pool", zap.String("pool", p.name))
	
	// Signal workers to stop
	p.cancel()
	
	// Close jobs channel
	close(p.jobs)
	
	// Wait for workers with timeout
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		log.Info("Worker pool stopped gracefully", zap.String("pool", p.name))
	case <-time.After(timeout):
		log.Warn("Worker pool stop timed out", zap.String("pool", p.name))
	}
	
	close(p.results)
}

// Stats returns pool statistics
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		Name:       p.name,
		Workers:    p.workers,
		QueueSize:  p.queueSize,
		QueueUsed:  len(p.jobs),
		Submitted:  p.submitted.Load(),
		Completed:  p.completed.Load(),
		Failed:     p.failed.Load(),
		Running:    p.running.Load(),
	}
}

// PoolStats contains pool statistics
type PoolStats struct {
	Name       string `json:"name"`
	Workers    int    `json:"workers"`
	QueueSize  int    `json:"queue_size"`
	QueueUsed  int    `json:"queue_used"`
	Submitted  int64  `json:"submitted"`
	Completed  int64  `json:"completed"`
	Failed     int64  `json:"failed"`
	Running    bool   `json:"running"`
}
```

### Step 2: Create Error Types

Create `internal/worker/errors.go`:

```go
package worker

import "errors"

var (
	// ErrQueueFull is returned when the job queue is full
	ErrQueueFull = errors.New("worker pool queue is full")
	
	// ErrPoolStopped is returned when submitting to a stopped pool
	ErrPoolStopped = errors.New("worker pool is stopped")
	
	// ErrTimeout is returned when a job times out
	ErrTimeout = errors.New("job processing timed out")
)
```

### Step 3: Create Audit Worker

Create `internal/worker/audit.go`:

```go
package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

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

// AuditResult contains the audit outcome
type AuditResult struct {
	RequestID        string
	TrustScore       float64
	HallucinationRate float64
	Grade            string
	Verdict          string
	Claims           []ClaimResult
	ProcessingTime   time.Duration
}

// ClaimResult contains a single claim verification
type ClaimResult struct {
	Claim      string
	Supported  bool
	Confidence float64
	Evidence   []string
	Reasoning  string
}

// AuditHandler processes audit jobs
type AuditHandler struct {
	grpcClient interface{} // Will be the gRPC client
	timeout    time.Duration
}

// NewAuditHandler creates a new audit handler
func NewAuditHandler(timeout time.Duration) *AuditHandler {
	return &AuditHandler{
		timeout: timeout,
	}
}

// SetGRPCClient sets the gRPC client (called after client is created)
func (h *AuditHandler) SetGRPCClient(client interface{}) {
	h.grpcClient = client
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
	
	// TODO: Call gRPC client (Step 2.5)
	// For now, simulate processing
	result := h.simulateAudit(auditJob)
	
	return Result{
		JobID:   job.ID,
		Success: true,
		Data:    result,
	}
}

// simulateAudit creates a mock audit result for testing
func (h *AuditHandler) simulateAudit(job AuditJob) AuditResult {
	// Simulate some processing time
	time.Sleep(100 * time.Millisecond)
	
	return AuditResult{
		RequestID:        job.RequestID,
		TrustScore:       85.0,
		HallucinationRate: 15.0,
		Grade:            "B",
		Verdict:          "Generally reliable response",
		Claims: []ClaimResult{
			{
				Claim:      "Sample claim from response",
				Supported:  true,
				Confidence: 0.9,
			},
		},
		ProcessingTime: 100 * time.Millisecond,
	}
}
```

Add the missing import at the top:
```go
import (
	"context"
	"errors"
	"time"
	// ...
)
```

### Step 4: Integrate with Proxy

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
	"time"

	"go.uber.org/zap"

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

	// Create audit handler
	auditHandler := worker.NewAuditHandler(cfg.Audit.Timeout)

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

	// Register health routes
	healthHandler := handlers.NewHealthHandler(version)
	healthHandler.RegisterRoutes(srv.Engine())

	// Create proxy handler
	proxyHandler, err := proxy.NewHandler(&cfg.Upstream)
	if err != nil {
		log.Fatal("Failed to create proxy handler", zap.Error(err))
	}
	defer proxyHandler.Close()

	// Set response callback to submit to worker pool
	proxyHandler.SetResponseCallback(func(ctx context.Context, req *proxy.CapturedRequest, resp *proxy.CapturedResponse) {
		// Parse request body for user query and context
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

	// Stop worker pool first
	auditPool.Stop(cfg.Audit.Timeout)

	// Graceful server shutdown
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown", zap.Error(err))
	}

	log.Info("Server stopped")
}

// processAuditResults handles completed audits
func processAuditResults(results <-chan worker.Result) {
	log := logger.Get()

	for result := range results {
		if !result.Success {
			log.Error("Audit failed",
				zap.String("job_id", result.JobID),
				zap.Error(result.Error),
			)
			continue
		}

		auditResult, ok := result.Data.(worker.AuditResult)
		if !ok {
			continue
		}

		log.Info("Audit completed",
			zap.String("request_id", auditResult.RequestID),
			zap.Float64("trust_score", auditResult.TrustScore),
			zap.String("grade", auditResult.Grade),
			zap.Duration("processing_time", auditResult.ProcessingTime),
		)

		// TODO: Broadcast to WebSocket clients (Step 2.6)
	}
}

// extractUserQuery parses the user query from request body
func extractUserQuery(body []byte) string {
	// Simple extraction - enhance based on your API format
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return ""
	}

	if query, ok := data["query"].(string); ok {
		return query
	}
	if prompt, ok := data["prompt"].(string); ok {
		return prompt
	}
	if messages, ok := data["messages"].([]interface{}); ok && len(messages) > 0 {
		if msg, ok := messages[len(messages)-1].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				return content
			}
		}
	}

	return ""
}
```

Add the missing import:
```go
import (
	"encoding/json"
	// ... other imports
)
```

---

## ✅ Testing

### Test 1: Unit Tests

Create `internal/worker/pool_test.go`:

```go
package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolStartStop(t *testing.T) {
	pool := NewPool(Config{
		Name:      "test",
		Workers:   2,
		QueueSize: 10,
		Handler: func(ctx context.Context, job Job) Result {
			return Result{JobID: job.ID, Success: true}
		},
	})

	pool.Start()
	stats := pool.Stats()
	assert.True(t, stats.Running)
	assert.Equal(t, 2, stats.Workers)

	pool.Stop(time.Second)
	stats = pool.Stats()
	assert.False(t, stats.Running)
}

func TestPoolSubmit(t *testing.T) {
	var processed atomic.Int32

	pool := NewPool(Config{
		Name:      "test",
		Workers:   2,
		QueueSize: 10,
		Handler: func(ctx context.Context, job Job) Result {
			processed.Add(1)
			return Result{JobID: job.ID, Success: true}
		},
	})

	pool.Start()
	defer pool.Stop(time.Second)

	// Submit jobs
	for i := 0; i < 5; i++ {
		ok := pool.Submit(Job{ID: fmt.Sprintf("job-%d", i)})
		assert.True(t, ok)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, int32(5), processed.Load())
}

func TestPoolQueueFull(t *testing.T) {
	pool := NewPool(Config{
		Name:      "test",
		Workers:   1,
		QueueSize: 2,
		Handler: func(ctx context.Context, job Job) Result {
			time.Sleep(100 * time.Millisecond) // Slow handler
			return Result{JobID: job.ID, Success: true}
		},
	})

	pool.Start()
	defer pool.Stop(time.Second)

	// Fill the queue
	pool.Submit(Job{ID: "1"})
	pool.Submit(Job{ID: "2"})
	pool.Submit(Job{ID: "3"})

	// This should fail (queue full)
	ok := pool.Submit(Job{ID: "4"})
	assert.False(t, ok)
}

func TestPoolGracefulShutdown(t *testing.T) {
	var completed atomic.Int32

	pool := NewPool(Config{
		Name:      "test",
		Workers:   2,
		QueueSize: 10,
		Handler: func(ctx context.Context, job Job) Result {
			time.Sleep(50 * time.Millisecond)
			completed.Add(1)
			return Result{JobID: job.ID, Success: true}
		},
	})

	pool.Start()

	// Submit jobs
	for i := 0; i < 4; i++ {
		pool.Submit(Job{ID: fmt.Sprintf("job-%d", i)})
	}

	// Stop with enough time to complete
	pool.Stop(time.Second)

	// All jobs should be completed
	assert.Equal(t, int32(4), completed.Load())
}
```

Add missing import:
```go
import "fmt"
```

### Test 2: Integration Test

```bash
# Start the proxy
cd backend-go
go run ./cmd/proxy

# Send requests (they'll be queued for audit)
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{"query": "What is the capital of France?"}'

# Check logs for "Audit completed" messages
```

---

## 📊 Metrics Dashboard

Add a stats endpoint:

```go
// In internal/handlers/stats.go

type StatsHandler struct {
	auditPool *worker.Pool
}

func (h *StatsHandler) Stats(c *gin.Context) {
	c.JSON(200, gin.H{
		"audit_pool": h.auditPool.Stats(),
	})
}
```

---

## 🐛 Common Issues

### Issue: Jobs not processing

**Cause:** Pool not started
**Solution:** Ensure `pool.Start()` is called before submitting

### Issue: Memory growing indefinitely

**Cause:** Results channel not being consumed
**Solution:** Always consume from `pool.Results()` in a goroutine

### Issue: Slow shutdown

**Cause:** Workers stuck on long operations
**Solution:** Use context cancellation in handler:
```go
select {
case <-ctx.Done():
    return Result{Error: ctx.Err()}
default:
    // Process
}
```

---

## ⏭️ Next Step

Continue to [Step 2.5: gRPC Client](step-2.5-grpc-client.md) to connect to the Python audit service.
