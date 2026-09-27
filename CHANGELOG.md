# Changelog

## Unreleased — 2.0.0 "Jev gateway" (in progress)

TrustAgent 2.0 turns the async auditor into a **hallucination gateway**: a fast
Jev decision check on every answer, per-route policies, pluggable evidence (no
RAG by default), Evaluator Studio, and a redesigned dashboard. The full
specification is [docs/v2/SPEC.md](docs/v2/SPEC.md); progress is tracked by
phase (P0 → P7).

### Added (so far)
- **v2 specification and design notes** (`docs/v2/`), plus recorded baseline
  test results.
- **`contracts/`** — the cross-language source of truth: JSON Schemas (2020-12),
  the configuration-key registry, pricing and cost assumptions, labelled
  value claims, UI outcomes/copy/glossary, 11 built-in evaluator templates,
  question sets, detector/secret/evidence rules, and draft golden vectors;
  `make contracts-check`, `contracts-test`, `contracts-go`.
- **gRPC contract v2** (additive): `Evaluate`, `GetEngineInfo`,
  `RetrieveContext`, `ApplyRuntimeConfig`, `DraftEvaluator`; verdicts
  `UNKNOWN=5` and `NOT_CHECKED=6`; v2.1 field numbers reserved. New RPCs return
  `UNIMPLEMENTED` until their phase lands.
- **Postgres migration 0002** (`traces` and related tables, backfill of legacy
  audits) with a runner that takes an advisory lock and applies each migration
  in one transaction; `internal/storetest` gives every test its own schema.
- **Typed API contract** (`frontend-react/src/types/api.ts` +
  `contracts/schema/api.v1.schema.json`, drift-tested) and the owner guide
  [docs/JEV-ACCESS.md](docs/JEV-ACCESS.md).
- `scripts/check-forbidden.mjs` enforces the superseded-identifier list.

### Changed
- CI split into `ci-go`, `ci-python`, `ci-web`, `ci-docker`, `ci-contracts`
  and `ci-e2e` workflows (job ids unchanged for branch protection).
- Makefile split into `mk/*.mk` fragments; `make check`; WSL-aware URL opener;
  `make up` also starts Postgres and waits for health.
- Frontend toolchain: Vite 8, TypeScript 6, React 19.3 (pinned).
- Go toolchain pinned to `go1.25.11`; pytest deselects `e2e`/`integration`
  by default; `.gitattributes` and per-service `.dockerignore` files.
- `SubmitAudit`/`GetAuditResult` are deprecated in favour of `Evaluate`.

### Fixed
- **B4 — duplicate ids corrupted audit history**: trace/audit ids are now
  server-generated UUIDv7; a client `X-Request-ID` is stored only as a
  non-unique `client_request_id`, and a repeated id can no longer append claims
  to an existing audit.

### Migration notes
- **No downgrade after migration 0002**: v1 binaries cannot scan the nullable
  score columns 0002 introduces. Back up the database before upgrading.
- Compose services no longer set `container_name`; use
  `docker compose exec <service> …` instead of `docker exec truthtable-<service> …`.

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
