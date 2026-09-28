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

- **Walking skeleton (S0)** — the gateway gates non-streaming OpenAI chat on
  observe and flag routes (Tier-0 detectors, evidence from the request, one Jev
  call through the scenario mock, `decide()`, the §2.12 response headers),
  records traces and decisions, streams them over WebSocket v2, and submits
  deep audits through `Evaluate`; `trafficgen --once --expect` and a zero-key
  `make demo`.
- **Fast check (Jev) backends** (`internal/jev`): `openrouter-decisions`,
  `openrouter-systemone`, `typesafe` and `compatible` next to `mock` and `off`,
  chosen by auto resolution (demo → mock; base URL + gateway key → compatible;
  OpenRouter key → openrouter-decisions; TypeSafe key → typesafe; else off).
  Retries honour Retry-After; hedging; a breaker that counts vendor faults only;
  a token-bucket limiter with a sync reserve and an in-flight cap; a daily spend
  cap with an enforcing reserve; one cost event per attempt; snapshot tracking
  (WS `decision_model_changed`); keep-warm; a startup probe. `/api/status`
  reports the live state, last error with its fix, snapshot and today's spend.
  New `cmd/jevcheck` (checks a key and endpoint with one probe) and
  `cmd/mock-upstream` (fakes every vendor the gateway calls, with keyed faults).
- **Deep-audit engine cores**: providers with retries, typed errors, structured
  outputs, a price table and an OpenRouter judge; `LLM_PROVIDER=auto` judge
  resolution with every fallback reported (`LLM_FALLBACK=none` by default);
  `GetEngineInfo`; the internal RPC token and RPC caps; decomposer v2 (the
  question, `{claim, quote}` output, UTF-16 claim spans, windows for long
  answers); a hardened verifier (repair turn, UNKNOWN instead of a defaulted
  confidence, NOT_CHECKED without evidence); one `CostRecord` per judge call,
  stored per audit and shown in the trace detail with its cost label; async
  knowledge I/O.
- **TypeScript decision core** (`frontend-react/src/lib/decision`): canonical
  JSON, hashing, tokens, compile, `decide()`, the seven detectors, evidence
  extraction and the evaluator lints, passing the same golden vectors as the Go
  reference (`make vectors-ts`); contract types generated from the schemas
  (`types.gen.ts`, `make gen-types`) and the committed api.v1 schema generator
  (`make gen-api-schema`).
- **UI kit** (DESIGN.md §11–12): Base UI primitives, page composites
  (ConfirmDialog, DisabledAction, DataTable, KeyValueList, ...), chart
  primitives, a themed toaster; axe and token-contrast tests.
- **Trace detail**: claims are marked in the answer at their UTF-16 spans (verdict
  hue, underline and icon; each mark links to its claim), paraphrased claims are
  listed as "not located in text", and Details show each tier's model and cost.
  The Health pill reads an engine without a judge as "No judge configured".
- **Contracts (P1b)**: golden vectors at the SPEC §7.6 minimums (detectors 53,
  evidence 23, decide 55, ...), 23 golden API fixtures validated against api.v1,
  the support-bot, rubric-only and replace-unverified presets, the evaluator
  drafter prompt with its `prompt.v1` schema, and the §2.8 lints in `validate.mjs`.
- **Postgres migrations 0003** (policy versions, evaluators and immutable
  evaluator versions, calibrations, labels, Studio results) **and 0004**
  (settings, credentials, API keys, admin audit log, cost events).
- **Configuration reference**: [docs/CONFIGURATION.md](docs/CONFIGURATION.md) and
  `.env.example` are generated from the key registry (`make config-docs`;
  `make config-docs-check` and ci-contracts fail on drift).

### Changed
- **Scoring v2** (ADR-001 in [docs/DECISIONS.md](docs/DECISIONS.md)): per-claim
  verdict distributions, flags, `p_hallucinated`, coverage and a trust score that
  is absent (never 0) when it cannot be computed; an audit fails with
  `judge_unavailable` when UNKNOWN reaches half the checked claims. Both golden
  baselines were regenerated on purpose. On the recorded golden v1 set (mock
  judge replaying recorded answers, so measured on fixtures, not live traffic):
  P 0.889 → 0.920, R 0.923 → 0.885, F1 0.906 → 0.902, AUROC 0.907 → 0.983,
  ECE 0.133 → 0.085 (one parse failure is now FAILED instead of a made-up 0.3).
  The new golden_v2 set (91 examples) is the CI gate alongside golden v1.
- The knowledge add-on (Qdrant, embeddings, torch) is an optional extra
  (`poetry install --extras knowledge`, engine image `EXTRAS=knowledge`) and
  needs `TRUTHTABLE_KNOWLEDGE_ENABLED=true`; the default engine imports without
  torch.
- The engine refuses a non-loopback bind without the internal token; in compose
  the proxy generates it into a shared volume on first start.
- [docs/GETTING-STARTED.md](docs/GETTING-STARTED.md) rewritten for v2 local runs
  on WSL (`make demo`, real mode, memory, troubleshooting).
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
- Migrations 0003 and 0004 apply automatically at proxy start; like 0002, they
  have no downgrade.
- A v1 `.env` may still set `LLM_MODEL` (for example `claude-haiku-4-5`). Under
  `LLM_PROVIDER=auto` it goes to whichever judge auto picks; clear it or set
  `LLM_PROVIDER` explicitly.

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
