# TruthTable AI Hallucination Detection - Future Roadmap

> **Last Updated:** January 31, 2026  
> **Current Status:** MVP Complete (Phase 0-2)  
> **Next Milestone:** Production-Ready v1.0

---

## 📊 Executive Summary

TruthTable is an AI hallucination detection system that intercepts LLM responses, decomposes them into verifiable claims, and scores their truthfulness. The current MVP demonstrates the core architecture with a Go proxy, Python scoring engine, and React dashboard.

---

## 🎯 Current State Assessment

### ✅ What's Working
| Component | Status | Details |
|-----------|--------|---------|
| Go Proxy Server | ✅ Complete | Intercepts OpenAI-compatible requests on port 8080 |
| WebSocket Hub | ✅ Complete | Real-time updates to dashboard on port 8081 |
| Worker Pool | ✅ Complete | 10-worker async job processing |
| Python LLM Provider | ✅ Complete | Ollama integration with adapter pattern |
| React Dashboard | ✅ Complete | Real-time audit visualization |
| Docker Infrastructure | ✅ Complete | Ollama, Redis, Qdrant, Prometheus |

### ⚠️ Partially Complete
| Component | Status | Remaining Work |
|-----------|--------|----------------|
| Claim Decomposer | 🔶 70% | Needs structured output parsing |
| Fact Verifier | 🔶 50% | Vector store integration pending |
| Score Calculator | 🔶 40% | Aggregation logic incomplete |
| gRPC Integration | 🔶 60% | Python server not yet connected to Go proxy |

### ❌ Not Started
| Component | Status | Priority |
|-----------|--------|----------|
| OpenAI Provider | ❌ | High - Production use |
| Anthropic Provider | ❌ | Medium |
| Streaming Response Audit | ❌ | High - SSE support |
| Authentication/API Keys | ❌ | Critical for production |
| Rate Limiting | ❌ | High |
| Persistent Storage | ❌ | Medium |

---

## 🗺️ Development Phases

### Phase 3: Complete Core Engine (2-3 weeks)
**Goal:** Finish the Python truthfulness scoring pipeline

#### Stage 3.1: Claim Decomposer (3-4 days)
```
Priority: 🔴 Critical
Complexity: Medium
Dependencies: Ollama Provider ✅
```

**Tasks:**
1. [ ] Implement structured JSON output parsing from LLM
2. [ ] Create claim extraction prompts with examples
3. [ ] Add claim categorization (factual, opinion, temporal)
4. [ ] Handle edge cases (lists, nested claims, quotes)
5. [ ] Add unit tests with sample responses

**Success Criteria:**
- Given: "Paris is the capital of France, founded in 3rd century BC"
- Output: `[{"claim": "Paris is the capital of France", "type": "factual"}, {"claim": "Paris was founded in 3rd century BC", "type": "temporal"}]`

#### Stage 3.2: Fact Verifier (4-5 days)
```
Priority: 🔴 Critical
Complexity: High
Dependencies: Qdrant Vector Store, Claim Decomposer
```

**Tasks:**
1. [ ] Implement vector embedding generation (use sentence-transformers)
2. [ ] Create Qdrant collection for context storage
3. [ ] Build context retrieval based on claim similarity
4. [ ] Implement LLM-based verification with retrieved context
5. [ ] Add confidence scoring (0-1 scale)
6. [ ] Handle "insufficient context" cases

**Architecture:**
```
Claim → Embed → Search Qdrant → Retrieve Context → LLM Verify → Score
```

#### Stage 3.3: Score Calculator (2-3 days)
```
Priority: 🟡 High
Complexity: Low
Dependencies: Fact Verifier
```

**Tasks:**
1. [ ] Implement weighted aggregation of claim scores
2. [ ] Add overall response trustworthiness score
3. [ ] Create scoring breakdown (factual accuracy, source quality)
4. [ ] Implement score thresholds and categories

**Output Structure:**
```json
{
  "overall_score": 0.82,
  "category": "mostly_accurate",
  "claims": [
    {"text": "...", "score": 0.95, "verified": true},
    {"text": "...", "score": 0.60, "verified": false, "reason": "Date incorrect"}
  ]
}
```

---

### Phase 4: gRPC Integration (1 week)
**Goal:** Connect Python engine to Go proxy

#### Stage 4.1: Python gRPC Server (2-3 days)
```
Priority: 🔴 Critical
Complexity: Medium
```

**Tasks:**
1. [ ] Implement `EvaluatorService` from proto definition
2. [ ] Wire up decomposer → verifier → scorer pipeline
3. [ ] Add async request handling
4. [ ] Implement health checks
5. [ ] Add timeout handling

#### Stage 4.2: Go-Python Integration (2-3 days)
```
Priority: 🔴 Critical
Complexity: Medium
```

**Tasks:**
1. [ ] Test end-to-end gRPC calls from Go → Python
2. [ ] Handle serialization/deserialization
3. [ ] Add retry logic with exponential backoff
4. [ ] Implement circuit breaker pattern
5. [ ] Add metrics for latency tracking

---

### Phase 5: Production Hardening (2-3 weeks)
**Goal:** Make the system production-ready

#### Stage 5.1: Security (3-4 days)
```
Priority: 🔴 Critical
```

**Tasks:**
1. [ ] Add API key authentication to Go proxy
2. [ ] Implement rate limiting (token bucket algorithm)
3. [ ] Add request/response logging with PII redaction
4. [ ] Secure inter-service communication (mTLS or service mesh)
5. [ ] Add CORS configuration for dashboard
6. [ ] Environment-based configuration (dev/staging/prod)

**Security Recommendations:**
```yaml
# docker-compose.prod.yml changes
redis:
  command: redis-server --requirepass ${REDIS_PASSWORD}
  ports:
    - "127.0.0.1:6379:6379"  # Bind to localhost only

qdrant:
  environment:
    - QDRANT__SERVICE__API_KEY=${QDRANT_API_KEY}
```

#### Stage 5.2: Reliability (3-4 days)
```
Priority: 🟡 High
```

**Tasks:**
1. [ ] Add graceful shutdown handling
2. [ ] Implement request queuing during Python engine restarts
3. [ ] Add dead letter queue for failed audits
4. [ ] Create health check aggregation endpoint
5. [ ] Add structured logging (JSON format)
6. [ ] Implement request tracing (OpenTelemetry)

#### Stage 5.3: Performance (2-3 days)
```
Priority: 🟡 High
```

**Tasks:**
1. [ ] Add caching for repeated claims (Redis)
2. [ ] Implement connection pooling for gRPC
3. [ ] Add async batch processing for multiple claims
4. [ ] Profile and optimize hot paths
5. [ ] Add load testing (k6 or Locust)

**Target Metrics:**
| Metric | Target |
|--------|--------|
| P50 Latency | < 500ms |
| P99 Latency | < 2s |
| Throughput | > 100 req/s |
| Error Rate | < 0.1% |

---

### Phase 6: OpenAI Integration (1 week)
**Goal:** Support real OpenAI API calls

#### Stage 6.1: OpenAI Provider (2-3 days)
```
Priority: 🟡 High
Complexity: Low
```

**Tasks:**
1. [ ] Create `OpenAIProvider` implementing `LLMProvider` interface
2. [ ] Handle API key management securely
3. [ ] Implement streaming support
4. [ ] Add token usage tracking
5. [ ] Handle rate limits gracefully

#### Stage 6.2: Proxy Passthrough Mode (2-3 days)
```
Priority: 🟡 High
```

**Tasks:**
1. [ ] Forward requests to real OpenAI API
2. [ ] Capture responses for auditing
3. [ ] Handle streaming responses (SSE)
4. [ ] Add request/response correlation
5. [ ] Implement timeout handling

---

### Phase 7: Dashboard Enhancements (2 weeks)
**Goal:** Rich visualization and interaction

#### Stage 7.1: Audit Details View (3-4 days)
**Tasks:**
1. [ ] Show claim breakdown with individual scores
2. [ ] Display evidence/sources used for verification
3. [ ] Add color-coded confidence indicators
4. [ ] Show processing timeline
5. [ ] Add export functionality (JSON/CSV)

#### Stage 7.2: Historical Analysis (3-4 days)
**Tasks:**
1. [ ] Store audits in persistent database (PostgreSQL)
2. [ ] Create audit history table with filtering
3. [ ] Add trend charts (accuracy over time)
4. [ ] Implement search functionality
5. [ ] Add model comparison views

#### Stage 7.3: Configuration UI (2-3 days)
**Tasks:**
1. [ ] Settings page for LLM provider selection
2. [ ] Threshold configuration
3. [ ] Alert configuration
4. [ ] API key management

---

### Phase 8: Advanced Features (4+ weeks)
**Goal:** Enterprise-grade capabilities

#### Stage 8.1: Multi-Model Verification
```
Complexity: High
Value: Very High
```

Use multiple LLMs to cross-verify claims:
```
Claim → [Llama, GPT-4, Claude] → Consensus Score
```

#### Stage 8.2: Custom Context Upload
```
Complexity: Medium
Value: High
```

Allow users to upload documents for domain-specific verification:
- PDF/Word document parsing
- Chunking and embedding
- Qdrant collection per user/org

#### Stage 8.3: Webhook Integrations
```
Complexity: Low
Value: High
```

Send audit results to external systems:
- Slack notifications for low-trust responses
- PagerDuty alerts for critical failures
- Custom webhook endpoints

#### Stage 8.4: SDK Development
```
Complexity: Medium
Value: Very High
```

Create SDKs for easy integration:
- Python SDK
- JavaScript/TypeScript SDK
- Go SDK

---

## 🏗️ Technical Debt Backlog

| Issue | Priority | Effort | Impact |
|-------|----------|--------|--------|
| Add comprehensive error handling | High | 2 days | Reliability |
| Implement proper logging strategy | High | 1 day | Debugging |
| Add integration tests | Medium | 3 days | Quality |
| Refactor config management | Medium | 1 day | Maintainability |
| Document API endpoints | Medium | 1 day | Usability |
| Add Swagger/OpenAPI spec | Low | 1 day | Documentation |
| Containerize Python service | High | 1 day | Deployment |

---

## 📅 Suggested Timeline

### Month 1: Core Completion
- Week 1-2: Phase 3 (Claim Decomposer, Fact Verifier, Score Calculator)
- Week 3: Phase 4 (gRPC Integration)
- Week 4: Testing and bug fixes

### Month 2: Production Readiness
- Week 1: Phase 5 (Security)
- Week 2: Phase 5 (Reliability, Performance)
- Week 3: Phase 6 (OpenAI Integration)
- Week 4: Load testing and optimization

### Month 3: Polish and Deploy
- Week 1-2: Phase 7 (Dashboard Enhancements)
- Week 3: Documentation and cleanup
- Week 4: Beta deployment and monitoring

---

## 🎓 Learning Resources

### For Claim Decomposition
- [LangChain Structured Output](https://python.langchain.com/docs/modules/model_io/output_parsers/)
- [Few-Shot Prompting](https://www.promptingguide.ai/techniques/fewshot)

### For Vector Search
- [Qdrant Documentation](https://qdrant.tech/documentation/)
- [Sentence Transformers](https://www.sbert.net/)

### For Production Go
- [Effective Go](https://go.dev/doc/effective_go)
- [Go gRPC Best Practices](https://grpc.io/docs/languages/go/basics/)

### For Observability
- [OpenTelemetry in Go](https://opentelemetry.io/docs/instrumentation/go/)
- [Prometheus Best Practices](https://prometheus.io/docs/practices/naming/)

---

## 🔐 Security Checklist for Production

- [ ] All secrets in environment variables, not code
- [ ] API authentication on all endpoints
- [ ] Rate limiting implemented
- [ ] HTTPS/TLS for all external traffic
- [ ] Network isolation between services
- [ ] Regular dependency updates (Dependabot)
- [ ] Security scanning in CI/CD (Snyk/Trivy)
- [ ] Audit logging enabled
- [ ] Input validation on all endpoints
- [ ] PII/sensitive data handling policy

---

## 📞 Quick Start Commands

```bash
# Start all infrastructure
docker-compose up -d

# Start Go proxy
cd backend-go && go run ./cmd/proxy

# Start Python engine (when complete)
cd backend-python && poetry run python -m truthtable.main

# Start React dashboard
cd frontend-react && npm run dev

# Run E2E test
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test" \
  -d '{"model": "gpt-4", "messages": [{"role": "user", "content": "Test"}], "test_response": "Test response"}'
```

---

**🚀 The foundation is solid. Now it's time to build!**
