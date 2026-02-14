# TruthTable AI Hallucination Detection - Future Roadmap

> **Last Updated:** February 14, 2026
> **Current Status:** v0.2.1 - RAG Pipeline Active, Security Hardened
> **Next Milestone:** Production-Ready v1.0

---

## Current State Assessment

### What's Complete (v0.2.1)

| Component | Status | Details |
|-----------|--------|---------|
| Go Proxy Server | Complete | Intercepts OpenAI-compatible requests, port 8080 |
| WebSocket Hub | Complete | Real-time updates to dashboard, port 8081 |
| Worker Pool | Complete | 10-worker async job processing |
| Python Audit Engine | Complete | Full RAG pipeline: decompose -> retrieve -> verify -> score |
| Vector Store (Qdrant) | Complete | Embedding + semantic search + knowledge seeding |
| React Dashboard | Complete | Real-time audit visualization with Zustand + WebSocket |
| Docker Infrastructure | Complete | All 8 services containerized with health checks |
| gRPC Integration | Complete | Go proxy <-> Python engine communication |
| Security Hardening | Complete | Non-root containers, pinned versions, security headers |
| Integration Tests | Complete | 28 Python + 16 Go + 7 RAG integration + E2E |

### What's NOT Working Yet

| Component | Status | Impact |
|-----------|--------|--------|
| Prometheus metrics | Stub only | Go proxy returns placeholder; Python has no metrics endpoint |
| Grafana dashboards | Empty | No dashboard definitions provisioned |
| Authentication | None | All endpoints are open |
| Persistent audit storage | In-memory only | Results lost on restart |
| Streaming response audit | Not implemented | SSE/streaming responses not audited |

---

## Phase 5: Observability & Metrics (Next Priority)

**Goal:** Make Prometheus and Grafana actually work

### 5.1 Python Metrics Endpoint (1-2 days)
- [ ] Add `prometheus_client` HTTP server on port 8001 in `main.py`
- [ ] Expose metrics: `audit_requests_total`, `audit_duration_seconds`, `audit_score_histogram`
- [ ] Add per-node timing: decompose/retrieve/verify/score duration
- [ ] Track Qdrant query latency and result counts

### 5.2 Go Proxy Prometheus Metrics (1-2 days)
- [ ] Replace stub `/metrics` with `promhttp.Handler()`
- [ ] Track: `proxy_requests_total`, `proxy_request_duration_seconds`
- [ ] Track: `proxy_upstream_errors_total`, `websocket_connections_active`
- [ ] Track: `worker_pool_queue_depth`, `audit_job_duration_seconds`

### 5.3 Grafana Dashboard Definitions (1 day)
- [ ] Create `config/grafana/dashboards/truthtable.json` with:
  - Request rate panel
  - Audit score distribution histogram
  - P50/P95/P99 latency panels
  - Error rate panel
  - Active WebSocket connections gauge
- [ ] Create dashboard provisioning YAML in `config/grafana/dashboards/`

---

## Phase 6: Production Hardening

### 6.1 Authentication & Authorization (3-4 days)
- [ ] Add API key middleware to Go proxy (`X-API-Key` header)
- [ ] Create API key management (generate, revoke, list)
- [ ] Add gRPC interceptor for auth tokens
- [ ] Restrict CORS to configured origins
- [ ] Validate WebSocket origin against whitelist

### 6.2 Input Validation & Safety (2 days)
- [ ] Add request body size limit (10MB) to Go proxy
- [ ] Validate gRPC request fields (max query/response length)
- [ ] Add rate limiting (token bucket) to Go proxy
- [ ] Add Redis-backed rate limiting per API key

### 6.3 Persistent Storage (2-3 days)
- [ ] Replace in-memory `_audit_results` dict with Redis
- [ ] Add TTL-based expiration (e.g., 24 hours)
- [ ] Add PostgreSQL for long-term audit history
- [ ] Create audit history API endpoint

### 6.4 Logging & Observability (1-2 days)
- [ ] Switch to structured JSON logging
- [ ] Add request tracing (correlation IDs across Go/Python)
- [ ] Truncate sensitive data in logs (user queries, LLM responses)
- [ ] Add OpenTelemetry traces for audit pipeline steps

---

## Phase 7: LLM Provider Expansion

### 7.1 OpenAI Provider (2-3 days)
- [ ] Create `OpenAIProvider` implementing `LLMProvider` interface
- [ ] Secure API key management (env var, not hardcoded)
- [ ] Handle streaming responses (SSE)
- [ ] Token usage tracking
- [ ] Rate limit handling with exponential backoff

### 7.2 Anthropic Provider (1-2 days)
- [ ] Create `AnthropicProvider` implementing `LLMProvider`
- [ ] Handle Claude-specific message format
- [ ] Support tool use / structured output

### 7.3 Proxy Passthrough Mode (2-3 days)
- [ ] Forward requests to real cloud LLM APIs
- [ ] Capture and audit streaming responses
- [ ] Add request/response correlation tracking
- [ ] Support model routing (different models for different endpoints)

---

## Phase 8: Dashboard Enhancements

### 8.1 Audit Details View (3-4 days)
- [ ] Claim breakdown with individual scores and evidence
- [ ] Color-coded confidence indicators (green/yellow/red)
- [ ] Processing timeline visualization
- [ ] Export functionality (JSON/CSV)

### 8.2 Historical Analysis (3-4 days)
- [ ] Audit history table with filtering and pagination
- [ ] Trend charts (accuracy over time, by model)
- [ ] Search functionality across past audits
- [ ] Model comparison views

### 8.3 Configuration UI (2-3 days)
- [ ] Settings page for LLM provider selection
- [ ] Threshold configuration (what score triggers "hallucination")
- [ ] Alert configuration (email/Slack on low trust scores)
- [ ] API key management interface

---

## Phase 9: Advanced Features

### 9.1 Custom Knowledge Upload
Allow users to upload their own documents for domain-specific verification:
- [ ] PDF/Word document parsing and chunking
- [ ] Per-user/org Qdrant collections
- [ ] Document management UI (upload, delete, list)
- [ ] Automatic re-embedding on model updates

### 9.2 Multi-Model Verification
Use multiple LLMs to cross-verify claims:
```
Claim -> [Llama, GPT-4, Claude] -> Consensus Score
```
- [ ] Multi-provider verification pipeline
- [ ] Consensus scoring algorithm
- [ ] Model agreement visualization

### 9.3 Webhook Integrations
- [ ] Slack notifications for low-trust responses
- [ ] PagerDuty alerts for critical failures
- [ ] Custom webhook endpoints
- [ ] Email digest of daily audit stats

### 9.4 SDK Development
- [ ] Python SDK (`pip install truthtable-client`)
- [ ] JavaScript/TypeScript SDK (`npm install @truthtable/client`)
- [ ] Go SDK
- [ ] OpenAI-compatible drop-in replacement client

---

## Technical Debt Backlog

| Issue | Priority | Effort | Impact |
|-------|----------|--------|--------|
| Add comprehensive error handling | High | 2 days | Reliability |
| Add Swagger/OpenAPI spec for Go proxy | Medium | 1 day | Documentation |
| Implement proper logging strategy | High | 1 day | Debugging |
| Add CI/CD pipeline (GitHub Actions) | High | 1 day | Automation |
| Add Dependabot for dependency updates | Low | 30 min | Security |
| Add Docker image security scanning (Trivy) | Medium | 1 day | Security |
| Production `docker-compose.prod.yml` | Medium | 1 day | Deployment |
| Kubernetes Helm chart | Low | 3 days | Enterprise deployment |

---

## Security Checklist for Production

- [x] All secrets in environment variables, not code
- [x] Non-root Docker containers
- [x] Pinned Docker image versions
- [x] Nginx security headers
- [x] No secrets in git history
- [ ] API authentication on all endpoints
- [ ] Rate limiting implemented
- [ ] HTTPS/TLS for all external traffic
- [ ] CORS restricted to known origins
- [ ] Redis authentication enabled
- [ ] Qdrant API key enabled
- [ ] Network isolation (remove unnecessary port mappings)
- [ ] Regular dependency updates (Dependabot)
- [ ] Security scanning in CI/CD
- [ ] Input validation on all endpoints
- [ ] PII/sensitive data redaction in logs

---

## Ideas for What You Can Build With This

1. **AI Code Review Tool** - Feed code review outputs through TruthTable to verify the LLM's claims about bugs/improvements
2. **Medical Information Verifier** - Seed with medical knowledge, verify LLM medical advice
3. **Legal Document Checker** - Verify LLM-generated legal summaries against actual law
4. **Education Platform** - Verify LLM tutoring responses for accuracy
5. **News Fact-Checker** - Verify AI-generated news summaries against known facts
6. **Customer Support QA** - Audit chatbot responses for accuracy before sending to customers
7. **Research Paper Validator** - Verify LLM summaries of scientific papers against source material

---

## Quick Start Commands

```powershell
# Start infrastructure
docker-compose up -d redis qdrant ollama prometheus grafana

# Seed knowledge base (first time only)
cd backend-python
.venv\Scripts\activate
python scripts/seed_knowledge.py

# Start Python engine
$env:QDRANT_URL="http://localhost:6333"
$env:OLLAMA_BASE_URL="http://localhost:11434"
python -m truthtable.main

# Start Go proxy (new terminal)
cd backend-go
go run ./cmd/proxy

# Start React dashboard (new terminal)
cd frontend-react
npm run dev

# Run E2E test (new terminal, Python engine must be running)
python test_direct_audit.py
```
