# TrustAgent Level-Up Master Plan

> Roadmap to take TrustAgent from a working demo (v0.2.x) to a top-tier, production-credible
> portfolio project (v1.0.0). Each phase lands as a reviewed change with tests, and gets a
> progress log in [docs/progress/](progress/).

**Status legend**: ⬜ not started · 🟨 in progress · ✅ done

| Phase | Goal | Est. hours | Status |
|---|---|---|---|
| [0](#phase-0--hygiene--doc-foundation) | Hygiene & doc foundation | 5 | ✅ |
| [1](#phase-1--cicd) | CI/CD pipelines | 9 | ✅ |
| [2](#phase-2--evaluation-framework) | Evaluation framework (benchmarks + CI gates) | 18 | ✅ |
| [3](#phase-3--security-hardening) | Security hardening (OWASP LLM Top 10) | 14 | ✅ |
| [4](#phase-4--persistence) | Postgres + Redis persistence | 9 | ⬜ |
| [5](#phase-5--multi-provider--dashboard-history) | OpenAI/Anthropic providers + history UI | 10 | ⬜ |
| [6](#phase-6--veritas-lite-verified-knowledge-base) | VERITAS-lite verified knowledge base | 20 | ⬜ |
| [7](#phase-7--portfolio-polish--v100) | Polish, ARCHITECTURE.md, v1.0.0 | 8 | ⬜ |

Total: ~93 hours over ~10 weeks part-time.

## Why this ordering

1. **CI/CD first** — cheap, gates everything after it; "no CI" is an instant red flag to reviewers.
2. **Eval framework second** — the single highest-credibility item. A hallucination detector
   without measured precision/recall on public benchmarks is a demo, not a product. It also
   becomes the regression safety net for every later change to prompts, providers, and retrieval.
3. **Security third** — current state (no auth, CORS `*`, plaintext public gRPC, root containers)
   would fail any review; fixing it with an OWASP LLM Top 10 narrative is exactly what employers
   screen for. Doing it after CI means scanners enforce it forever.
4. **Persistence fourth** — small, unlocks audit history and finally justifies Redis.
5. **Features fifth/sixth** — multi-provider + dashboard history (high demo value), then the
   differentiator: a claim-level, entailment-gated knowledge base (VERITAS-lite).
6. **Polish last** — benchmark numbers in README, architecture docs, demo GIF, v1.0.0 tag.

**Cut deliberately** (poor solo-portfolio ROI): client SDKs, streaming-response audit, full
bitemporal claim graph, permission-aware retrieval, crypto-shredding, gRPC mTLS (replaced by
Docker network isolation).

---

## Phase 0 — Hygiene & doc foundation

**Goal**: eliminate red flags a reviewer hits in the first 5 minutes.

- [x] Align all versions to `0.4.0`; add root `VERSION` file as source of truth
      (`backend-python/pyproject.toml`, `frontend-react/package.json`,
      `backend-go/internal/version/version.go`, `truthtable/__init__.py`)
- [x] Create `docs/PLAN.md` (this file), `docs/progress/PHASE-0.md`, `CLAUDE.md`
- [x] Promote `docs-private/GETTING-STARTED.md` → `docs/GETTING-STARTED.md`
- [x] Move VERITAS research artifact → `docs/research/`
- [x] Wire `test_e2e.py` / `test_direct_audit.py` into Makefile (`make test-e2e`)

**Verify**: `make test` passes; Go builds; no orphan files at root.

## Phase 1 — CI/CD

**Goal**: every push lint+test+scan-gated across all three languages.

- [x] `.github/workflows/ci.yml` — parallel jobs:
  - **go**: gofmt check, `go vet`, `go test -race -coverprofile` in `backend-go/`
  - **python**: `ruff check`, `black --check`, `pytest --cov` (unit only, CPU-only torch)
  - **frontend**: `npm ci`, `eslint`, `tsc -b`, `vitest run`
  - **docker**: build all three images with GHA layer cache
- [x] `.github/workflows/security.yml` — gosec, bandit, `npm audit`, Trivy
      (non-blocking initially; flipped to blocking at end of Phase 3)
- [x] Frontend test toolchain: vitest + @testing-library/react; first tests for
      `auditStore.ts` and `useWebSocket.ts`
- [x] `.github/dependabot.yml` (gomod, pip, npm, actions, docker)
- [x] README badges (build, security, version)

**Verify**: green checks on a test push; a deliberately broken test fails the pipeline.

## Phase 2 — Evaluation framework

**Goal**: published precision/recall/F1 + calibration numbers on public benchmarks, with CI
regression gates. The credibility centerpiece.

Two tiers:
- **Tier 1 — CI regression gate** (every PR, deterministic, <2 min): `MockLLMProvider` in the
  provider registry replays recorded fixtures; a frozen golden set (~50 examples,
  `evals/datasets/golden/golden_v1.jsonl`) runs through the real pipeline; metrics compared
  against a committed baseline with zero tolerance.
- **Tier 2 — Benchmark runs** (weekly schedule + `make eval`): HaluEval QA subset (300+300
  stratified) and RAGTruth slice (~500) via downloader scripts; results to
  `docs/benchmarks/` + generated markdown tables.

Metrics (`evals/metrics.py`): precision/recall/F1/balanced accuracy on hallucination detection,
AUROC on faithfulness score, **ECE calibration** on per-claim confidence, latency p50/p95 per stage.

Key enabler: retriever uses caller-provided context (already in proto as
`repeated ContextDocument`) instead of Qdrant when present.

- [x] `evals/` package (in `backend-python/evals/`): `run_eval.py`, `metrics.py`,
      `datasets/download.py`, `build_golden.py`, `baselines/`
- [x] `MockLLMProvider` (content-matched fixture replay) + generated fixtures
- [x] Context-injection mode in `retriever.py`
- [x] Golden set (50 curated examples incl. simulated model errors) + committed baseline
- [x] Tier-1 step in CI (blocking); `.github/workflows/eval.yml` for Tier-2 (HaluEval)
- [x] `docs/EVALUATION.md` + benchmark table in README
- [ ] RAGTruth dataset support (deferred — HaluEval first; revisit in Phase 5)

**Verify**: golden run deterministic across two runs; CI fails when scorer threshold is perturbed.

## Phase 3 — Security hardening

**Goal**: every audit finding fixed, mapped to OWASP LLM Top 10, enforced by scanners.

- [x] API-key auth middleware (`backend-go/internal/middleware/auth.go`, constant-time compare,
      `TRUTHTABLE_API_KEYS` env; `/health` exempt)
- [x] CORS allowlist from `TRUTHTABLE_ALLOWED_ORIGINS` env (replaces `*`)
- [x] WebSocket origin check + API key on connect (`?api_key=`)
- [x] Rate limiting — Redis fixed window per key/IP with in-memory fallback
      (`internal/middleware/ratelimit.go`)
- [x] Input validation: `http.MaxBytesReader`, max prompt/response lengths, upload caps
- [x] Remove public `/metrics` from main router; stop host-publishing :8001/:8002/:50051
- [x] Prompt-injection hardening in `decomposer.py` / `verifier.py` via new
      `truthtable/security.py` (delimited untrusted text, hidden-char stripping,
      strict schema parse with safe fallback, length truncation)
- [x] Resource limits on app containers (Go/Python images already non-root)
- [x] Secrets audit; Grafana password now required (no `changeme` default)
- [x] `docs/SECURITY.md`: threat model + OWASP LLM Top 10 mapping
- [x] Flip security scanners to blocking (gosec medium+, bandit, npm audit high+;
      Trivy informational until first reviewed run)
- [x] Bonus: fixed compose env-name mismatch (proxy was silently defaulting to
      `https://api.openai.com` as upstream in Docker)

**Verify**: middleware unit tests (401/403/429, CORS preflight, WS origin rejection);
injection-payload suite passes.

## Phase 4 — Persistence

**Goal**: audit history survives restarts. **Postgres** (ADR in `docs/DECISIONS.md`); single
writer = Go worker pool.

- [ ] `postgres:16-alpine` in compose; migrations in `backend-go/migrations/` (golang-migrate)
- [ ] Schema: `audits` + `audit_claims`
- [ ] `backend-go/internal/store/postgres.go` (pgx); persist after broadcast
- [ ] `GET /api/audits` (paginated, filterable) + `GET /api/audits/:id`
- [ ] Redis cache for result lookups; retire Python in-memory dict
- [ ] Store integration tests

## Phase 5 — Multi-provider + dashboard history

- [ ] `providers/openai.py`, `providers/anthropic.py` mirroring `ollama.py`; mocked-HTTP unit tests
- [ ] Tier-2 eval with frontier judge models → README comparison table (approved budget ~$5–20)
- [ ] Dashboard history page consuming `GET /api/audits` (filterable table, sparkline)
- [ ] Frontend tests for history view + store hydration (stores/hooks ≥ 70% coverage)

## Phase 6 — VERITAS-lite verified knowledge base

**Goal**: upgrade raw-chunk RAG into a claim-level, ingest-verified knowledge base.
*"Most RAG stores index unverified text; TrustAgent's KB only admits claims that pass an
entailment gate against their source, and detects contradictions between sources at ingest."*

- [ ] Claim-level ingestion (`kb/ingestion.py`, reusing decomposer)
- [ ] Gate-1 ingest verification (reusing verifier; below-threshold → `quarantined`)
- [ ] Contradiction detection at ingest (`kb/contradiction.py`; Postgres `claim_relations`;
      `GET /api/kb/conflicts`)
- [ ] Hybrid retrieval: BM25 + dense, RRF-fused (`kb/hybrid.py` + `retriever.py`); eval ablation
- [ ] Trust-weighted scoring (corroboration count + ingest entailment in `scorer.py`)
- [ ] Proto extensions (`per-claim ingest results`, `ListConflicts`, `GetKBStats`) + `make proto`
- [ ] Frontend KB page: accepted/quarantined claims + conflict pairs
- [ ] `docs/KB-DESIGN.md` (dual-gate model, research lineage, explicit out-of-scope list)

Out of scope (deliberate): bitemporal versioning, SUPERSEDES/REFINES edges, permission-aware
retrieval, crypto-shredding.

## Phase 7 — Portfolio polish & v1.0.0

- [ ] `docs/ARCHITECTURE.md`: mermaid diagrams + design decisions & trade-offs
- [ ] README overhaul: 30-second pitch, benchmark + ablation tables, demo GIF, security posture,
      honest limitations
- [ ] Load test (`scripts/load/`): proxy overhead p95 under concurrency
- [ ] Stretch: OpenTelemetry tracing Go→gRPC→Python
- [ ] Tag `v1.0.0` + GitHub release with changelog

---

## Verification strategy

- Every phase lands with green CI (from Phase 1 onward).
- Each `docs/progress/PHASE-N.md` records verification evidence (test output, screenshots).
- Full-system checks after Phases 4 and 6: `docker compose up`, `make test-e2e`, manual
  dashboard walkthrough (live audit, history page, KB upload with conflicting documents).
- If time pressure hits: cut from the bottom of Phase 6 (hybrid retrieval drops first);
  never cut Phases 1–3.
