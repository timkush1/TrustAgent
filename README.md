# TrustAgent - AI Hallucination Detection System

A real-time proxy that intercepts LLM responses, verifies factual claims against a knowledge base, and reports hallucinations on a live dashboard.

```
Your App                    TrustAgent                     LLM (Ollama)
   |                           |                              |
   |-- POST /v1/chat --------->|-- forward request ---------->|
   |                           |<-- LLM response -------------|
   |<-- response (instant) ----|                              |
   |                           |-- async audit job            |
   |                           |      |                       |
   |                           |   [Decompose claims]         |
   |                           |   [Retrieve from Qdrant]     |
   |                           |   [Verify via NLI]           |
   |                           |   [Score faithfulness]       |
   |                           |      |                       |
   |   Dashboard <-- WebSocket-|<-----+                       |
```

## How It Works

1. **Go Proxy** (port 8080) intercepts OpenAI-compatible API requests
2. Forwards to the upstream LLM, streams response back to the user instantly
3. Asynchronously dispatches an audit job to the **Python Engine** (port 50051) via gRPC
4. Python engine runs a LangGraph pipeline:
   - **Decomposer** - extracts atomic claims from the LLM response
   - **Retriever** - searches Qdrant vector DB for relevant facts
   - **Verifier** - checks each claim against retrieved context (NLI)
   - **Scorer** - calculates a faithfulness score (0-100%)
5. Results broadcast via WebSocket to the **React Dashboard** (port 5173)

## E2E Test Results

| Test Case | Score | Grade | Result |
|-----------|-------|-------|--------|
| "Paris is the capital of France" | 100% | A | SUPPORTED |
| "London is the capital of France" | 0% | D | HALLUCINATION DETECTED |
| Speed of light correct + wrong discoverer | 50% | C | Mixed (1 supported, 1 unsupported) |

## Tech Stack

| Component | Technology | Purpose |
|-----------|-----------|---------|
| Proxy | Go + Gin | Fast HTTP reverse proxy, WebSocket hub, worker pool |
| Audit Engine | Python + LangGraph | Claim decomposition, NLI verification, scoring |
| Dashboard | React + TypeScript + Tailwind | Real-time audit visualization |
| Vector DB | Qdrant | Knowledge base for RAG retrieval |
| LLM | Ollama (llama3.2) | Local inference for claim analysis |
| Communication | gRPC + Protocol Buffers | Service-to-service RPC |
| Cache | Redis | Job queuing |
| Monitoring | Prometheus + Grafana | Metrics (infrastructure ready, dashboards planned) |

## Quick Start

### Prerequisites

- Docker & Docker Compose
- Go 1.22+
- Python 3.11+
- Node.js 22+ (or 20.19+)

### 1. Start Infrastructure

```bash
docker-compose up -d redis qdrant ollama

# Pull the LLM model (first time only, ~2GB)
docker exec -it truthtable-ollama ollama pull llama3.2
```

### 2. Seed the Knowledge Base

```bash
cd backend-python
python -m venv .venv
# Windows: .venv\Scripts\activate
# Linux/Mac: source .venv/bin/activate
pip install -e ".[dev]"
python scripts/seed_knowledge.py
```

### 3. Start Services (3 terminals)

**Terminal 1 - Python Audit Engine:**
```bash
cd backend-python
# activate venv
python -m truthtable.main
# gRPC server listening on 0.0.0.0:50051
```

**Terminal 2 - Go Proxy:**
```bash
cd backend-go
go run ./cmd/proxy
# HTTP on :8080, WebSocket on :8081
```

**Terminal 3 - React Dashboard:**
```bash
cd frontend-react
npm install
npm run dev
# Dashboard at http://localhost:5173
```

### 4. Run the E2E Test

```bash
# From project root, using the Python venv:
python test_e2e.py
```

This sends test requests through the full pipeline and shows audit scores.

## Project Structure

```
trustAgent/
├── backend-go/              # Go reverse proxy (Gin, gRPC client, WebSocket)
│   ├── cmd/proxy/           # Entry point
│   ├── internal/            # Proxy handler, worker pool, WebSocket hub, gRPC client
│   └── Dockerfile
├── backend-python/          # Python audit engine (LangGraph, gRPC server)
│   ├── src/truthtable/
│   │   ├── graphs/          # LangGraph workflow + nodes
│   │   ├── vectorstore/     # Qdrant client + embeddings
│   │   ├── providers/       # LLM providers (Ollama)
│   │   └── grpc/            # gRPC server
│   ├── data/                # Seed knowledge base (20 facts)
│   ├── scripts/             # Seed script
│   ├── tests/               # Unit + integration tests
│   └── Dockerfile
├── frontend-react/          # React dashboard (Vite, Zustand, Tailwind)
│   ├── src/
│   │   ├── components/      # Audit cards, trust gauge, claim list
│   │   ├── stores/          # Zustand state management
│   │   └── hooks/           # WebSocket hook
│   └── Dockerfile
├── proto/                   # Shared gRPC definitions
├── config/                  # Prometheus + Grafana config
├── docs/                    # Detailed documentation
├── docker-compose.yml       # All 8 services
├── test_e2e.py              # End-to-end test (Go proxy + Python audit)
└── test_direct_audit.py     # Direct gRPC audit test
```

## Service Ports

| Service | Port | URL |
|---------|------|-----|
| Go Proxy | 8080 | http://localhost:8080 |
| WebSocket | 8081 | ws://localhost:8081/ws |
| Python gRPC | 50051 | localhost:50051 |
| Dashboard | 5173 | http://localhost:5173 |
| Qdrant | 6333 | http://localhost:6333 |
| Ollama | 11434 | http://localhost:11434 |
| Redis | 6379 | localhost:6379 |
| Prometheus | 9090 | http://localhost:9090 |
| Grafana | 3001 | http://localhost:3001 |

## Testing

```bash
# Python unit + integration tests (28 tests)
cd backend-python && python -m pytest tests/ -v

# Go tests (16 tests)
cd backend-go && go test ./...

# E2E test (requires all services running)
python test_e2e.py
```

## Configuration

Copy `.env.example` to `.env` and adjust as needed:

```bash
GRAFANA_ADMIN_PASSWORD=changeme
UPSTREAM_LLM_URL=http://ollama:11434    # For proxy passthrough mode
```

Python engine config (environment variables):

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_MODEL` | `llama3.2` | Ollama model name |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama URL |
| `QDRANT_URL` | `http://localhost:6333` | Qdrant URL |
| `GRPC_PORT` | `50051` | gRPC server port |

## Documentation

Detailed docs for each component are in the [docs/](docs/) folder:

- [Getting Started](docs/GETTING-STARTED.md) - Full setup tutorial
- [Project Status](docs/PROJECT-STATUS.md) - What's implemented
- [Phase 1: Python Engine](docs/PHASE-1-PYTHON-ENGINE.md) - Audit pipeline deep dive
- [Phase 2: Go Proxy](docs/PHASE-2-GO-PROXY.md) - Proxy architecture
- [Phase 3: React Dashboard](docs/PHASE-3-REACT-DASHBOARD.md) - Frontend guide
- [Future Roadmap](docs/FUTURE-ROADMAP.md) - Planned features

## License

MIT
