# TrustTable Documentation

> **Project**: TrustAgent - AI Hallucination Detection System
> **Version**: v0.2.1

---

## Documentation Index

| Document | Purpose |
|----------|---------|
| [GETTING-STARTED.md](GETTING-STARTED.md) | Step-by-step setup: prerequisites, start services, first audit |
| [PROJECT-STATUS.md](PROJECT-STATUS.md) | What's implemented, test results, known limitations |
| [PHASE-0-SUMMARY.md](PHASE-0-SUMMARY.md) | Infrastructure and project initialization |
| [PHASE-1-PYTHON-ENGINE.md](PHASE-1-PYTHON-ENGINE.md) | Python audit engine: LangGraph, NLI, RAG pipeline |
| [PHASE-2-GO-PROXY.md](PHASE-2-GO-PROXY.md) | Go proxy: HTTP handler, TeeWriter, worker pool, WebSocket |
| [PHASE-3-REACT-DASHBOARD.md](PHASE-3-REACT-DASHBOARD.md) | React dashboard: components, Zustand, WebSocket hook |
| [FUTURE-ROADMAP.md](FUTURE-ROADMAP.md) | Planned features: metrics, auth, multi-provider |
| [steps/](steps/) | Step-by-step implementation guides for each phase |

---

## For New Developers

```
1. Read PROJECT-STATUS.md      -> What's built and working?
2. Read GETTING-STARTED.md     -> How do I run it?
3. Read PHASE-1-PYTHON-ENGINE  -> How does the audit engine work?
4. Read PHASE-2-GO-PROXY       -> How does the proxy work?
5. Read PHASE-3-REACT-DASHBOARD -> How does the UI work?
```

---

## Service Ports

| Service | Port | Protocol |
|---------|------|----------|
| Go Proxy HTTP | 8080 | HTTP |
| Go Proxy WebSocket | 8081 | WS |
| Python gRPC | 50051 | gRPC |
| React Dashboard | 5173 (dev) | HTTP |
| Ollama LLM | 11434 | HTTP |
| Redis | 6379 | TCP |
| Qdrant | 6333 | HTTP |
| Prometheus | 9090 | HTTP |
| Grafana | 3001 | HTTP |
