# Changelog

## Unreleased — hardening & audit-quality pass

Foundation work making the system demo-ready and accurate before the Playground
+ Settings feature (see [docs/PLAN.md](docs/PLAN.md)).

### Changed
- **Audit judge now defaults to Anthropic Claude Haiku** (`claude-haiku-4-5`)
  with automatic fallback to local Ollama (`llama3.2:1b`) when no
  `ANTHROPIC_API_KEY` is set — great quality with a key, free out of the box.
- **Verifier runs claims with bounded concurrency** (`asyncio.Semaphore`)
  instead of sequentially; default gRPC timeout raised to 120s for slow judges.
- **Unified Prometheus namespace** to `trustagent_*` across both services.

### Added
- **CONTRADICTED verdict is live end-to-end** (proto → Python enum/prompt/scorer
  → Go maps → dashboard badge), distinct from UNSUPPORTED; deterministic scorer
  tests lock it in.
- `tests/e2e/` pytest package (opt-in `e2e` marker) replacing the loose root
  scripts; `docs/CI-CD.md`; `docs/README.md` docs index; single-connection
  `WebSocketProvider`; auth-aware `api/client.ts`; real `/health`-driven status.

### Fixed
- **Duplicate WebSocket connections** (App + AuditFeed both connected) → one shared connection.
- Dashboard live feed broke under proxy auth (WS never sent `?api_key=`).
- Fabricated `relevancy_score` (a duplicate of faithfulness) removed.
- Grade now reaches **F** on both sides; unbounded in-memory results dict bounded (LRU).
- `make proto` fixed for fresh clones (`--go_opt=module=…`); deprecated `grpc.Dial` → `grpc.NewClient`.
- Stale `:8081` WebSocket port, fake static status panel, debug `console.log`s, and dead code removed.

## v1.0.0 — 2026-06-11

The "level-up" release: eight phases from working demo to production-credible
system. Full narrative in [docs/PLAN.md](docs/PLAN.md) and
[docs/archive/progress/](docs/archive/progress/).

### Added
- **Two-tier evaluation framework**: 50-example golden set running the real
  pipeline against recorded model outputs as a blocking CI regression gate
  (bit-exact, per-example); scheduled HaluEval benchmarks with live models;
  precision/recall/F1, AUROC, and ECE calibration metrics.
- **Security hardening mapped to the OWASP LLM Top 10**: API-key auth, CORS +
  WebSocket origin allowlists, Redis-backed rate limiting, transport-level
  body caps, prompt-injection defenses (untrusted-data delimiting,
  hidden-character sanitization, strict LLM-output schema validation),
  internal-only gRPC/metrics ports, container resource limits. Blocking
  gosec/bandit/npm-audit scanners.
- **Postgres persistence**: audit history with claims, embedded migrations,
  paginated/filterable `GET /api/audits` API, dashboard History tab.
- **OpenAI and Anthropic providers** alongside local Ollama, selectable via
  `LLM_PROVIDER`; judge-model comparison via `make eval-compare`.
- **VERITAS-lite knowledge base**: claim-level ingestion with Gate-1
  source-entailment gating and quarantine, ingest-time contradiction
  detection, hybrid BM25+dense RRF retrieval, full provenance, KB dashboard
  tab, and new gRPC/REST APIs.
- **CI/CD**: lint+typecheck+test+Docker-build matrix across Go/Python/React,
  store integration tests against a Postgres service container, Dependabot,
  weekly security scans.
- Documentation system: ARCHITECTURE, SECURITY, EVALUATION, KB-DESIGN,
  DECISIONS (ADRs), per-phase progress logs.

### Fixed along the way
- Dockerized proxy silently used `https://api.openai.com` as upstream
  (compose/env name mismatch).
- Caller-provided audit context was overwritten by Qdrant retrieval.
- React hook lint violations in the WebSocket reconnect path.
- Go 1.25 directive vs 1.24 builder image (caught by CI image builds).
- Trojan-Source pattern in the sanitizer itself (caught by bandit), int32
  overflow conversions (caught by gosec), rollup path-traversal advisory
  (caught by npm audit).

## v0.2.1 and earlier

Initial build: Go reverse proxy with async audit worker pool, Python LangGraph
pipeline (decompose → retrieve → verify → score), Qdrant RAG, React dashboard
with live WebSocket feed, Prometheus/Grafana observability.
