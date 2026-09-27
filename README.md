<div align="center">

# 🔍 TrustAgent

### An AI Hallucination Control Plane

**Drop it between your app and any LLM. Responses pass through with zero added latency while every claim is fact-checked against a verified knowledge base — live, on a dashboard.**

[![CI](https://github.com/timkush1/TrustAgent/actions/workflows/ci.yml/badge.svg)](https://github.com/timkush1/TrustAgent/actions/workflows/ci.yml)
[![Security](https://github.com/timkush1/TrustAgent/actions/workflows/security.yml/badge.svg)](https://github.com/timkush1/TrustAgent/actions/workflows/security.yml)
![Version](https://img.shields.io/badge/version-1.0.0-blue)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)
![Python](https://img.shields.io/badge/Python-3.11+-3776AB?logo=python&logoColor=white)
![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)

</div>

---

## Table of Contents

- [What it is](#what-it-is)
- [How it works](#how-it-works)
- [Screenshots](#screenshots)
- [Quick start](#quick-start)
- [The audit pipeline](#the-audit-pipeline)
- [The verified knowledge base (VERITAS-lite)](#the-verified-knowledge-base-veritas-lite)
- [Tech stack](#tech-stack)
- [Configuration](#configuration)
- [Testing & evaluation](#testing--evaluation)
- [Observability](#observability)
- [Security](#security)
- [Project structure](#project-structure)
- [Documentation](#documentation)
- [Roadmap](#roadmap)
- [Honest limitations](#honest-limitations)
- [License](#license)

---

## What it is

Large language models are fluent but unreliable — they state falsehoods with total confidence. **TrustAgent** is a control plane that catches those hallucinations without slowing anything down.

A Go reverse proxy sits in front of any OpenAI-compatible LLM endpoint. It returns the model's answer to your client **immediately**, then — asynchronously, off the request path — ships that answer to a Python audit engine. The engine decomposes the answer into atomic claims, retrieves supporting context from a Qdrant vector store, verifies each claim with an LLM judge, and scores the response's *faithfulness*. Results stream to a React dashboard over WebSocket in real time.

The knowledge base it verifies against is itself trustworthy: uploaded documents are decomposed into claims, and each claim must pass an **entailment gate** against its own source or be quarantined — and contradictions between sources are flagged at ingest time. This is the "dual-gate" design (see [docs/KB-DESIGN.md](docs/KB-DESIGN.md)).

**Why this is interesting engineering:** a zero-latency tee proxy, a graph-based audit pipeline, a self-verifying knowledge base, a deterministic eval regression gate that blocks every PR, constant-time auth, and OWASP-LLM-mapped security scanning — all wired together and observable end-to-end.

## How it works

```
  Your App                     TrustAgent (Go proxy)                 LLM upstream
     │                               │                                   │
     │── POST /v1/chat/completions ─▶│──────── forward request ─────────▶│
     │                               │◀────────── response ──────────────│
     │◀──── response (instant) ──────│                                   │
     │                               │
     │                               ├─▶ async audit job (worker pool)
     │                               │        │  gRPC
     │                               │        ▼
     │                               │   Python audit engine (LangGraph)
     │                               │   ┌───────────────────────────────┐
     │                               │   │ decompose → retrieve → verify  │
     │                               │   │            → score             │
     │                               │   └───────────────────────────────┘
     │                               │        │  Qdrant (RAG)  ·  LLM judge
     │       Dashboard  ◀── WebSocket ◀───────┘
```

The client never waits on the audit. The proxy captures a copy of the response via a thread-safe `TeeWriter` and enqueues the audit; the client already has its bytes.

## Screenshots

<p align="center">
  <img src="docs/images/dashboard.png" alt="TrustAgent dashboard — live audit feed, trust gauge, service status" width="100%"/>
  <br/><em>The dashboard: a live audit feed, a session trust gauge, knowledge-upload, and real service health.</em>
</p>

<p align="center">
  <img src="docs/images/knowledge-base.png" alt="Verified knowledge base — accepted vs quarantined claims with entailment scores" width="100%"/>
  <br/><em>The verified knowledge base: every claim carries an entailment score and is <strong>accepted</strong> or <strong>quarantined</strong> — contradictions surface as a review queue.</em>
</p>

## Quick start

**Prerequisites:** Docker & Docker Compose, Go 1.25+, Python 3.11+ with [Poetry](https://python-poetry.org/), Node.js 22+, and `protoc`.

```bash
# 1. Configure (Grafana password is required; a provider key is optional)
cp .env.example .env      # then edit: set GRAFANA_ADMIN_PASSWORD

# 2. Install all deps + protoc plugins, generate gRPC stubs
make install
make proto

# 3. Start infrastructure (redis, qdrant, postgres, ollama, prometheus, grafana)
make up
make ollama-pull          # pulls the free local fallback model (llama3.2:1b)

# 4. Run the three services (separate terminals)
make dev-python           # audit engine  → gRPC :50051, metrics :8001
make dev-go               # proxy         → HTTP + WebSocket :8080, metrics :8002
make dev-react            # dashboard     → http://localhost:5173
```

Then send an answer to audit — no upstream key needed, thanks to test-response mode:

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama3.2:1b",
    "messages": [{"role":"user","content":"What is the capital of France?"}],
    "test_response": "London is the capital of France."
  }'
```

Open <http://localhost:5173> and watch the hallucination get flagged in real time.

> **🔑 Audit quality & the free fallback.** The audit judge defaults to **Anthropic Claude Haiku** (`claude-haiku-4-5`) for strong verification. If no `ANTHROPIC_API_KEY` is set, the engine **automatically falls back to local Ollama** — so it works for free out of the box, and gets dramatically more accurate the moment you add a key. (A 1B local model is a weak fact-checker; this is expected and quantified by the eval suite.)

## The audit pipeline

The Python engine is a [LangGraph](https://github.com/langchain-ai/langgraph) state machine. Each node reads and writes a shared `AuditState`; per-step timings are recorded throughout.

| Node | What it does |
|---|---|
| **decompose** | An LLM extracts atomic, independently-checkable claims from the response (injection-hardened prompt; malformed output degrades safely). |
| **retrieve** | Embeds the query + each claim (MiniLM, 384-dim) and pulls the top matching context from Qdrant — hybrid **BM25 + dense** fused with Reciprocal Rank Fusion when the KB is enabled. |
| **verify** | Per-claim NLI: the judge labels each claim **SUPPORTED / CONTRADICTED / UNSUPPORTED / PARTIALLY_SUPPORTED** with a confidence and evidence quotes. Runs with bounded concurrency. |
| **score** | Confidence-weighted faithfulness score (0–1), an A–F grade, and a hallucination flag (any high-confidence CONTRADICTED/UNSUPPORTED claim, or >30% problematic). |

`CONTRADICTED` (a retrieved document disproves the claim) is kept distinct from `UNSUPPORTED` (nothing addresses it) end-to-end — a real hallucination vs. an unverifiable statement.

## The verified knowledge base (VERITAS-lite)

Uploads don't go straight into the vector store. Each document is:

1. **Decomposed** into atomic claims.
2. **Gate-1 checked** — a claim is only *accepted* if it's entailed by its own source text (confidence ≥ 0.7); otherwise it's *quarantined* and excluded from retrieval.
3. **Contradiction-checked** — accepted claims are compared against existing ones; conflicts are recorded on both sides and surfaced as a review queue.

Only accepted claims are indexed for retrieval. The dashboard's **Knowledge Base** tab shows the accepted/quarantined split, per-claim entailment scores, and detected conflicts. Full rationale and research lineage: [docs/KB-DESIGN.md](docs/KB-DESIGN.md).

## Tech stack

| Layer | Technology | Role |
|---|---|---|
| **Proxy** | Go 1.25 · Gin · gorilla/websocket · pgx | Zero-latency interception, worker pool, WebSocket hub, gRPC client, Postgres persistence |
| **Audit engine** | Python 3.11 · LangGraph · sentence-transformers · gRPC | Claim decomposition, RAG retrieval, NLI verification, scoring |
| **Dashboard** | React 19 · TypeScript · Vite · Zustand · Tailwind v4 | Real-time audit feed, trust gauge, KB & history views |
| **Judge (LLM)** | Anthropic Claude Haiku *(default)* · OpenAI · Ollama *(free fallback)* | Pluggable provider registry with automatic fallback |
| **Vector DB** | Qdrant | RAG knowledge base + hybrid retrieval |
| **Contract** | Protocol Buffers · gRPC | Single-source Go↔Python contract (`proto/evaluator.proto`) |
| **Storage** | Postgres · Redis | Audit history · rate-limit backend |
| **Observability** | Prometheus · Grafana | `trustagent_*` metrics + a pre-built dashboard |

## Configuration

Copy `.env.example` → `.env`. All settings have safe defaults; the only *required* one is `GRAFANA_ADMIN_PASSWORD`.

**Audit engine (Python):**

| Variable | Default | Description |
|---|---|---|
| `LLM_PROVIDER` | `anthropic` | Judge provider: `anthropic` · `openai` · `ollama` |
| `LLM_MODEL` | `claude-haiku-4-5` | Model for the configured provider |
| `ANTHROPIC_API_KEY` | — | Enables the Claude Haiku judge (never logged, server-side only) |
| `OLLAMA_FALLBACK_MODEL` | `llama3.2:1b` | Free local model used when no cloud key is set |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama endpoint |
| `QDRANT_URL` | — | Vector store; unset ⇒ no-context verification |
| `VERIFIER_CONCURRENCY` | `5` | Max claims verified in parallel |
| `RAG_ENABLED` | `true` | Toggle knowledge retrieval |

**Proxy (Go, `TRUTHTABLE_*` prefix):**

| Variable | Default | Description |
|---|---|---|
| `TRUTHTABLE_PORT` | `8080` | HTTP + WebSocket port |
| `TRUTHTABLE_UPSTREAM_URL` | `https://api.openai.com` | Upstream LLM (use `http://localhost:11434` for Ollama) |
| `TRUTHTABLE_GRPC_ADDRESS` | `localhost:50051` | Audit engine address |
| `TRUTHTABLE_GRPC_TIMEOUT` | `120s` | Per-audit timeout (generous for slow local judges) |
| `TRUTHTABLE_API_KEYS` | *(empty)* | Comma-separated keys; empty ⇒ auth disabled (dev only) |
| `TRUTHTABLE_DATABASE_URL` | *(empty)* | Postgres DSN; empty ⇒ history disabled |
| `TRUTHTABLE_WORKER_COUNT` / `TRUTHTABLE_QUEUE_SIZE` | `10` / `1000` | Audit worker pool sizing |

## Testing & evaluation

```bash
make test        # all unit suites: Python (pytest) + Go (go test) + React (vitest)
make test-e2e    # opt-in end-to-end tests (requires the full stack up)
make eval-golden # deterministic eval regression gate (the CI blocker)
make lint        # ruff + black + go vet/fmt + eslint
```

Detection quality is measured with a **two-tier** framework ([docs/EVALUATION.md](docs/EVALUATION.md)):

- **Tier 1 — CI regression gate.** A 50-example golden set runs through the *real* pipeline with recorded (mock) model responses, compared bit-exactly to a committed baseline on every PR. Any parsing/scoring/threshold change that moves a single prediction fails CI.
- **Tier 2 — public benchmark.** Weekly [HaluEval QA](https://github.com/RUCAIBox/HaluEval) runs against a live model.

Golden-tier metrics (pipeline + recorded imperfect verifier — intentionally not all 1.0):

| Precision | Recall | F1 | AUROC | ECE |
|---|---|---|---|---|
| 0.889 | 0.923 | 0.906 | 0.907 | 0.133 |

## Observability

Prometheus scrapes both services (unified `trustagent_*` namespace); Grafana ships a pre-built dashboard at <http://localhost:3001>.

Key metrics: `trustagent_audits_total{status}`, `trustagent_hallucinations_detected_total`, `trustagent_audit_duration_seconds`, `trustagent_faithfulness_score`, `trustagent_claims_total{status}`, `trustagent_active_audits`, `trustagent_websocket_clients`.

## Security

Every control maps to the [OWASP LLM Top 10](https://owasp.org/www-project-top-10-for-large-language-model-applications/) ([docs/SECURITY.md](docs/SECURITY.md)):

- **Prompt-injection defense** — untrusted claim/context is delimiter-tagged; the judge is instructed to treat it as data; strict JSON-schema validation degrades any manipulated output to `UNKNOWN`.
- **Constant-time API-key auth** (`subtle.ConstantTimeCompare`); CSWSH/CORS origin allowlisting on the WebSocket; body-size caps; per-client rate limiting.
- **Provider keys stay server-side** and are never logged or returned to the browser.
- **Blocking CI scanners** — gosec, bandit, npm-audit (Trivy informational).

## Project structure

```
TrustAgent/
├── backend-go/          # Go proxy: interception, worker pool, WS hub, gRPC client, Postgres store
├── backend-python/      # Python audit engine: LangGraph pipeline, providers, Qdrant, KB, gRPC server
│   └── tests/{unit,integration,e2e}/   # opt-in integration/e2e via pytest markers
├── frontend-react/      # React dashboard: audit feed, trust gauge, KB & history views
│   └── e2e/             # Playwright screenshot/e2e harness
├── proto/evaluator.proto  # single source of truth for the Go↔Python contract
├── config/              # Prometheus + Grafana provisioning
├── docs/                # architecture, KB design, evaluation, security, CI/CD, roadmap
│   └── archive/         # historical build logs + research
└── docker-compose.yml   # full stack
```

## Documentation

| Doc | What's inside |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System + sequence diagrams, the zero-latency tee, trade-offs |
| [docs/GETTING-STARTED.md](docs/GETTING-STARTED.md) | Zero-to-running walkthrough with troubleshooting |
| [docs/KB-DESIGN.md](docs/KB-DESIGN.md) | The dual-gate (VERITAS-lite) knowledge base |
| [docs/EVALUATION.md](docs/EVALUATION.md) | Two-tier eval methodology + CI gate |
| [docs/SECURITY.md](docs/SECURITY.md) | Threat model, OWASP-LLM mapping, scanner policy |
| [docs/CI-CD.md](docs/CI-CD.md) | The GitHub Actions pipelines & Dependabot, explained |
| [docs/DECISIONS.md](docs/DECISIONS.md) · [docs/PLAN.md](docs/PLAN.md) | ADRs · the phased roadmap |

A full index lives in [docs/README.md](docs/README.md). Component deep-dives: [backend-go](backend-go/README.md) · [backend-python](backend-python/README.md) · [frontend-react](frontend-react/README.md).

## Roadmap

v1.0.0 shipped the full audit control plane above. Actively in progress (see [docs/PLAN.md](docs/PLAN.md)):

- **Playground tab** — a live audited-chat surface: prompt a real LLM through the proxy and watch the trust audit attach to each answer inline.
- **Settings tab** — configure provider / model / API key / RAG from the UI (keys write-only, never exposed to the browser).

## Honest limitations

- **Detector quality is bounded by the judge model.** The default Claude Haiku is strong; the local fallback is weak. `make eval-compare` quantifies the gap.
- **Gate-1 verifies claims against their source, not against reality** — a consistently wrong document still poisons the KB (mitigated by contradiction detection + upload auth).
- **Golden-tier metrics characterize the pipeline, not a live model** (recorded outputs, by design).
- **Single-tenant** — no per-user data isolation or permission-aware retrieval.
- Streaming responses pass through but are audited as a whole, not incrementally.

## License

See [LICENSE](LICENSE).
