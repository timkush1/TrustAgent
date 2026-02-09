# 🎯 TruthTable - AI Hallucination Control Plane

> Real-time observability and fact-checking for LLM responses

## 🚀 Quick Start

### Prerequisites

- **Docker & Docker Compose** - For running infrastructure
- **Go 1.22+** - For the proxy service
- **Python 3.11+** - For the audit engine
- **Node.js 20+** - For the dashboard
- **Poetry** - Python dependency management

### Start Infrastructure Services

```bash
# Start Redis, Qdrant, Ollama, Prometheus, and Grafana
docker-compose up -d redis qdrant ollama prometheus grafana

# Pull Ollama model (first time only)
docker exec -it truthtable-ollama ollama pull llama3.2
```

### Development Mode

**Terminal 1 - Python Audit Engine:**
```bash
cd backend-python
poetry install
poetry run python -m truthtable.main
```

**Terminal 2 - Go Proxy:**
```bash
cd backend-go
go run ./cmd/proxy
```

**Terminal 3 - React Dashboard:**
```bash
cd frontend-react
npm install
npm run dev
```

**Access Points:**
- 🌐 Dashboard: http://localhost:3000
- 🔄 Proxy API: http://localhost:8080
- 📊 Prometheus: http://localhost:9090
- 📈 Grafana: http://localhost:3001 (admin/admin)

---

## 📁 Project Structure

```
trustAgent/
├── backend-go/          # Go reverse proxy
├── backend-python/      # Python audit engine
├── frontend-react/      # React dashboard
├── proto/               # Shared protobuf definitions
├── docs/                # Documentation
└── docker-compose.yml   # Infrastructure setup
```

---

## 🏗️ Architecture

TruthTable operates as a transparent proxy between your application and LLM providers:

```
Your App → TruthTable Proxy → LLM Provider
              ↓ (async)
         Audit Engine → Trust Score
              ↓
         Dashboard (real-time)
```

**Key Components:**
1. **Go Proxy** - Fast request forwarding with stream capture
2. **Python Audit Engine** - LangGraph-based fact verification
3. **React Dashboard** - Real-time monitoring UI

---

## 🧪 Testing

```bash
# Python tests
cd backend-python
poetry run pytest

# Go tests
cd backend-go
go test ./...

# React tests
cd frontend-react
npm test
```

---

## 📖 Documentation

- [Simple Plan](plan.md) - High-level overview
- [Detailed Architecture](detailed_plan.md) - In-depth design
- [Step-by-Step Guides](docs/steps/) - Implementation tutorials

---

## 🔧 Configuration

Environment variables can be set in `.env` file:

```bash
# Proxy
UPSTREAM_LLM_URL=http://localhost:11434
REDIS_URL=redis://localhost:6379
AUDIT_GRPC_ADDRESS=localhost:50051

# Audit Engine
OLLAMA_BASE_URL=http://localhost:11434
QDRANT_URL=http://localhost:6333
```

---

## 📊 Metrics

Prometheus metrics available at:
- Proxy: http://localhost:8002/metrics
- Audit Engine: http://localhost:8001/metrics

Key metrics:
- `truthtable_requests_total` - Total proxied requests
- `truthtable_faithfulness_score` - Trust score distribution
- `truthtable_hallucinations_total` - Detected hallucinations

---

## 🤝 Contributing

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) for guidelines.

---

## 📝 License

MIT License - See [LICENSE](LICENSE) file for details.

---

**Status:** 🚧 Phase 0 Complete - Ready for Phase 1 Implementation
