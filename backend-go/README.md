# TrustAgent Go Proxy

HTTP/WebSocket reverse proxy for LLM API interception with async audit job dispatching.

## Overview

The Go proxy is the entry point for the TrustAgent system. It intercepts OpenAI-compatible API requests, forwards them to the upstream LLM, captures responses using a `TeeWriter`, and dispatches audit jobs to the Python engine via gRPC.

**Key Responsibilities:**
- Reverse proxy for `/v1/chat/completions` and `/v1/completions`
- Async audit job submission (no latency added to LLM responses)
- Worker pool for parallel audit processing
- WebSocket hub for broadcasting audit results to dashboard clients
- REST API endpoints (`/api/audit`, `/api/upload`, `/health`, `/metrics`)
- Prometheus metrics exposure

## Features

### HTTP Reverse Proxy
- Forwards requests to upstream LLM (Ollama)
- Streams responses back to client immediately
- TeeWriter captures response body for auditing
- Supports both streaming and non-streaming responses

### Async Audit Pipeline
- Worker pool pattern with configurable goroutines
- Buffered job queue (default: 100 jobs)
- gRPC client for Python audit engine
- Graceful degradation if audit engine unavailable

### WebSocket Hub
- Broadcast audit results to all connected clients
- Automatic client registration/unregistration
- Goroutine-safe with mutex-protected client map

### REST API Endpoints
- `POST /api/audit` - Submit query+response for manual auditing
- `POST /api/upload` - Upload JSON documents to Qdrant knowledge base
- `GET /health` - Health check with audit engine connectivity status
- `GET /metrics` - Prometheus metrics endpoint

### Observability
- Prometheus metrics on port 8002
- Per-request audit duration tracking
- Hallucination detection counters
- Active audits gauge
- WebSocket client count

## Architecture

```
HTTP Request → Gin Handler → Upstream LLM
                   ↓ (TeeWriter captures response)
              Worker Pool ← Audit Job
                   ↓
              gRPC Client → Python Engine
                   ↓
              WebSocket Hub → Dashboard Clients
```

### Component Breakdown

**`cmd/proxy/main.go`**
- Application entry point
- Gin router setup
- Service initialization and wiring
- Graceful shutdown

**`internal/proxy/handler.go`**
- `HandleChatCompletion` - Intercepts `/v1/chat/completions`
- `HandleCompletion` - Intercepts `/v1/completions`
- TeeWriter implementation for capturing response bodies

**`internal/worker/pool.go`**
- Worker pool with configurable goroutines
- Buffered channel for job queue
- Context-based shutdown for graceful termination
- Metrics instrumentation

**`internal/websocket/hub.go`**
- Client registration/unregistration
- Broadcast message fan-out to all clients
- Goroutine-safe operations

**`internal/grpc/client.go`**
- AuditClient wrapper for evaluator.proto service
- `SubmitAudit` method with timeout and retry
- `IngestDocuments` method for RAG uploads
- Health check support

**`internal/metrics/metrics.go`**
- Prometheus metric definitions
- Counters, histograms, and gauges
- Labels for status tracking

## Quick Start

### Prerequisites
- Go 1.22+
- Python audit engine running on `localhost:50051` (see [../backend-python/README.md](../backend-python/README.md))

### Run Locally

```bash
cd backend-go

# Install dependencies
go mod download

# Run with default config
go run ./cmd/proxy

# Run with custom config
export SERVER_PORT=9000
export GRPC_ADDRESS=localhost:50051
go run ./cmd/proxy
```

Server will start on:
- HTTP: http://localhost:8080
- WebSocket: ws://localhost:8080/ws
- Metrics: http://localhost:8080/metrics

### Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PORT` | `8080` | HTTP server port |
| `UPSTREAM_URL` | `http://localhost:11434` | LLM API URL |
| `GRPC_ADDRESS` | `localhost:50051` | Python audit engine gRPC address |
| `GRPC_TIMEOUT` | `60s` | gRPC call timeout |
| `WORKER_COUNT` | `4` | Number of worker goroutines |
| `QUEUE_SIZE` | `100` | Audit job queue buffer size |
| `LOG_LEVEL` | `info` | Log level (debug/info/warn/error) |
| `READ_TIMEOUT` | `5m` | HTTP read timeout |
| `WRITE_TIMEOUT` | `5m` | HTTP write timeout |

## API Endpoints

### `POST /v1/chat/completions`
OpenAI-compatible chat completion endpoint.

**Request:**
```json
{
  "model": "llama3.2",
  "messages": [
    {"role": "user", "content": "What is the capital of France?"}
  ],
  "stream": false
}
```

**Response:**
Proxied from upstream LLM. Audit job submitted asynchronously.

### `POST /v1/completions`
OpenAI-compatible completion endpoint.

**Request:**
```json
{
  "model": "llama3.2",
  "prompt": "What is the capital of France?",
  "max_tokens": 100
}
```

**Response:**
Proxied from upstream LLM. Audit job submitted asynchronously.

### `POST /api/audit`
Submit a query+response pair for manual auditing (bypasses LLM proxy).

**Request:**
```json
{
  "query": "What is the capital of France?",
  "response": "London is the capital of France.",
  "model": "test"
}
```

**Response:**
```json
{
  "request_id": "abc-123-def-456",
  "status": "submitted"
}
```

### `POST /api/upload`
Upload JSON documents to Qdrant knowledge base.

**Request:**
- Content-Type: `multipart/form-data`
- Field name: `file`
- File format: JSON array of documents
- Max size: 10MB

```json
[
  {
    "content": "The Eiffel Tower is in Paris, France.",
    "metadata": {"source": "facts.txt", "category": "geography"}
  },
  {
    "content": "Paris is the capital of France.",
    "metadata": {"source": "facts.txt", "category": "geography"}
  }
]
```

**Response:**
```json
{
  "documents_ingested": 2,
  "status": "success"
}
```

### `GET /health`
Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "version": "1.0.0",
  "audit_engine": true
}
```

### `GET /metrics`
Prometheus metrics in text format.

**Example:**
```
# HELP trustagent_audits_total Total number of audits processed
# TYPE trustagent_audits_total counter
trustagent_audits_total{status="success"} 42
trustagent_audits_total{status="error"} 3

# HELP trustagent_hallucinations_detected_total Total hallucinations detected
# TYPE trustagent_hallucinations_detected_total counter
trustagent_hallucinations_detected_total 12
```

### `GET /ws`
WebSocket endpoint for real-time audit updates.

**Message Format (JSON):**
```json
{
  "request_id": "abc-123",
  "query": "What is the capital of France?",
  "response": "London is the capital of France.",
  "trust_score": 0.0,
  "trust_grade": "TRUST_GRADE_D",
  "hallucination_detected": true,
  "claims": [
    {
      "claim": "London is the capital of France",
      "status": "UNSUPPORTED",
      "confidence": 0.95,
      "evidence": ["Paris is the capital of France"]
    }
  ],
  "reasoning_trace": "The claim contradicts the knowledge base...",
  "step_timings": {
    "decompose_ms": 150,
    "retrieve_ms": 800,
    "verify_ms": 1200,
    "score_ms": 50
  }
}
```

## Testing

### Unit Tests

```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package
go test ./internal/proxy -v

# Run with race detector
go test -race ./...
```

**Test Coverage:**
- `internal/proxy/handler_test.go` - HTTP handler tests
- `internal/worker/pool_test.go` - Worker pool tests
- `internal/websocket/hub_test.go` - WebSocket hub tests

### Integration Testing

```bash
# Start Python audit engine first
cd ../backend-python
python -m truthtable.main

# In another terminal, start proxy
cd backend-go
go run ./cmd/proxy

# Send test request
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "test",
    "messages": [{"role": "user", "content": "Test"}],
    "test_response": "Paris is in France."
  }'

# Check metrics
curl http://localhost:8080/metrics | grep trustagent
```

## Prometheus Metrics

All metrics are exposed at `GET /metrics` in Prometheus text format.

**Counters:**
- `trustagent_audits_total{status="success"|"error"}` - Total audits processed
- `trustagent_hallucinations_detected_total` - Hallucinations detected
- `trustagent_claims_total{status="SUPPORTED"|"PARTIALLY_SUPPORTED"|"UNSUPPORTED"}` - Claims by status

**Histograms:**
- `trustagent_audit_duration_seconds` - Audit processing latency (buckets: 0.5s, 1s, 2s, 5s, 10s, 30s, 60s)
- `trustagent_faithfulness_score` - Trust score distribution (buckets: 0.1-1.0)

**Gauges:**
- `trustagent_active_audits` - Currently processing audits
- `trustagent_websocket_clients` - Connected WebSocket clients

## Performance

Based on benchmarking:
- **Proxy latency overhead:** <5ms (TeeWriter + worker pool submit)
- **Audit duration (p50):** ~2.5s (Python LangGraph pipeline)
- **Audit duration (p95):** ~8s
- **WebSocket broadcast latency:** <10ms
- **Worker pool throughput:** ~10 audits/sec with 4 workers

## Dependencies

Key dependencies (from `go.mod`):
- `github.com/gin-gonic/gin` - HTTP framework
- `github.com/gorilla/websocket` - WebSocket library
- `google.golang.org/grpc` - gRPC client
- `google.golang.org/protobuf` - Protobuf runtime
- `github.com/prometheus/client_golang` - Prometheus metrics
- `github.com/google/uuid` - UUID generation

## Development

### Code Structure
```
backend-go/
├── cmd/proxy/           # Application entry point
│   └── main.go         # Server setup, routing, graceful shutdown
├── internal/
│   ├── config/         # Configuration loading
│   ├── proxy/          # HTTP handler, TeeWriter
│   ├── worker/         # Worker pool, audit job processing
│   ├── websocket/      # WebSocket hub, client management
│   ├── grpc/           # gRPC client for Python engine
│   └── metrics/        # Prometheus metric definitions
├── api/audit/v1/       # Generated protobuf code
├── go.mod              # Go module definition
└── go.sum              # Dependency checksums
```

### Adding New Endpoints

1. Define handler in appropriate file (e.g., `internal/proxy/handler.go`)
2. Register route in `cmd/proxy/main.go`
3. Add tests
4. Update this README

### Adding New Metrics

1. Define metric in `internal/metrics/metrics.go`
2. Instrument code where metric should be recorded
3. Verify with `curl http://localhost:8080/metrics`

## Troubleshooting

**gRPC connection failed:**
```
⚠️  Warning: Could not connect to audit engine: connection refused
```
→ Ensure Python audit engine is running on `localhost:50051`

**Worker queue full:**
```
[abc-123] Worker queue full, dropping audit job
```
→ Increase `QUEUE_SIZE` or `WORKER_COUNT` environment variables

**WebSocket disconnects:**
→ Check firewall rules, proxy timeouts, client ping/pong implementation

**Metrics not updating:**
→ Ensure Prometheus is scraping the correct port (8080 by default)

## Related Documentation

- Root README: [../README.md](../README.md)
- Python Audit Engine: [../backend-python/README.md](../backend-python/README.md)
- React Dashboard: [../frontend-react/README.md](../frontend-react/README.md)
- Protocol Buffers: [../proto/evaluator.proto](../proto/evaluator.proto)
