# TruthTable Documentation - Table of Contents

> **Project**: TruthTable - AI Hallucination Detection System  
> **Last Updated**: January 2025  
> **Status**: ✅ All Phases Complete (37 Tests Passing)

---

## 🚀 Quick Start

| I want to... | Go to... |
|--------------|----------|
| **Set up the project** | [GETTING-STARTED.md](GETTING-STARTED.md) |
| **See what's working** | [PROJECT-STATUS.md](PROJECT-STATUS.md) |
| **Understand the system** | Read Phase docs 1 → 2 → 3 |

---

## 📚 Phase Documentation (Comprehensive)

These are the main documentation files. Each one is **400+ lines** with file-by-file explanations for junior developers.

| Phase | Document | What You'll Learn | Lines |
|-------|----------|-------------------|-------|
| **0** | [PHASE-0-SUMMARY.md](PHASE-0-SUMMARY.md) | Docker, protobuf, project setup | ~100 |
| **1** | [PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md) | LangGraph workflow, NLI verification, gRPC server | ~450 |
| **2** | [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md) | HTTP proxy, TeeWriter, worker pool, WebSocket hub | ~500 |
| **3** | [PHASE-3-REACT-DASHBOARD.md](PHASE-3-REACT-DASHBOARD.md) | React components, Zustand, WebSocket hook, Tailwind | ~500 |

---

## 📖 Document Descriptions

### Core Documentation

| Document | Purpose |
|----------|---------|
| **[GETTING-STARTED.md](GETTING-STARTED.md)** | Step-by-step setup: prerequisites, clone, start services, first audit |
| **[PROJECT-STATUS.md](PROJECT-STATUS.md)** | What's implemented, test counts, verified E2E flows |
| **[PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md)** | Complete guide to Python audit engine with LangGraph |
| **[PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md)** | Complete guide to Go interceptor proxy |
| **[PHASE-3-REACT-DASHBOARD.md](PHASE-3-REACT-DASHBOARD.md)** | Complete guide to React real-time dashboard |

### Reference Documents

| Document | Purpose |
|----------|---------|
| [PHASE-0-SUMMARY.md](PHASE-0-SUMMARY.md) | Infrastructure and project initialization |
| [detailed_plan.md](../detailed_plan.md) | Original architecture blueprint |
| [UNDERSTANDING-THE-PROJECT.md](UNDERSTANDING-THE-PROJECT.md) | High-level system overview |

---

## 🎯 How to Use This Documentation

### For New Developers (Start Here!)

```
Step 1: Read PROJECT-STATUS.md      → What's built?
Step 2: Read GETTING-STARTED.md     → How do I run it?
Step 3: Read PHASE-1-PYTHON-ENGINE  → How does audit work?
Step 4: Read PHASE-2-GO-PROXY       → How does proxy work?
Step 5: Read PHASE-3-REACT-DASHBOARD → How does UI work?
```

### Quick Reference

| I want to... | Read... |
|--------------|---------|
| Start the project | [GETTING-STARTED.md](GETTING-STARTED.md) |
| Add a new LLM provider | [PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md) → Section 13 |
| Change scoring logic | [PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md) → Section 13 |
| Add HTTP endpoint | [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md) → Section 13 |
| Change WebSocket messages | [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md) → Section 13 |
| Add UI component | [PHASE-3-REACT-DASHBOARD.md](PHASE-3-REACT-DASHBOARD.md) → Section 13 |
| Debug connection issues | Each phase doc → Section 14 (Troubleshooting) |

---

## Project Structure Overview

```
trustAgent/
├── backend-go/          # Go Proxy (Phase 2)
│   ├── cmd/proxy/       # Entry point
│   └── internal/        # Business logic
├── backend-python/      # Python Engine (Phase 1)
│   └── src/truthtable/  # Main package
├── frontend-react/      # React Dashboard (Phase 3)
│   └── src/             # React components
├── proto/               # gRPC definitions (Phase 0)
├── config/              # Infrastructure config
├── docs/                # 📍 You are here
└── docker-compose.yml   # Local development
```

---

## Service Ports Reference

| Service | Port | Protocol | URL |
|---------|------|----------|-----|
| Go Proxy HTTP | 8080 | HTTP | http://localhost:8080 |
| Go Proxy WebSocket | 8081 | WS | ws://localhost:8081/ws |
| Python gRPC | 50051 | gRPC | localhost:50051 |
| React Dashboard | 5173+ | HTTP | http://localhost:5173 |
| Ollama LLM | 11434 | HTTP | http://localhost:11434 |
| Redis | 6379 | TCP | localhost:6379 |
| Qdrant | 6333 | HTTP | http://localhost:6333 |

---

## Quick Commands

```bash
# Start everything
docker-compose up -d                          # Infrastructure
cd backend-python && python -m truthtable.main  # Python
cd backend-go && go run ./cmd/proxy            # Go
cd frontend-react && npm run dev               # React

# Run tests
cd backend-python && pytest tests/ -v          # Python: 21 tests
cd backend-go && go test ./... -v              # Go: 16 tests

# Send test audit
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Test"}],"test_response":"Test response."}'
```

---

*Navigate to any document above to learn more about that component.*
