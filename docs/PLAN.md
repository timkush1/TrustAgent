# TrustAgent master plan

> Phases 0–7 took TrustAgent from a working demo (v0.2.x) to a production-credible portfolio
> project (v1.0.0); each has a progress log in [docs/archive/progress/](archive/progress/).
> Phases 8–15 build **2.0, the hallucination gateway**, to the specification in
> [docs/v2/SPEC.md](v2/SPEC.md) (its §4.2 names them P0–P7, the label used in branch names and
> commit messages).

**Status**: Done · In progress · Planned

### 1.x: level-up to v1.0.0

| Phase | Goal | Est. hours | Status |
|---|---|---|---|
| [0](#phase-0--hygiene--doc-foundation) | Hygiene & doc foundation | 5 | Done |
| [1](#phase-1--cicd) | CI/CD pipelines | 9 | Done |
| [2](#phase-2--evaluation-framework) | Evaluation framework (benchmarks + CI gates) | 18 | Done |
| [3](#phase-3--security-hardening) | Security hardening (OWASP LLM Top 10) | 14 | Done |
| [4](#phase-4--persistence) | Postgres + Redis persistence | 9 | Done |
| [5](#phase-5--multi-provider--dashboard-history) | OpenAI/Anthropic providers + history UI | 10 | Done |
| [6](#phase-6--veritas-lite-verified-knowledge-base) | VERITAS-lite verified knowledge base | 20 | Done |
| [7](#phase-7--portfolio-polish--v100) | Polish, ARCHITECTURE.md, v1.0.0 | 8 | Done |

Total: ~93 hours over ~10 weeks part-time.

### 2.0: the Jev gateway

| Phase | SPEC | Goal | Status |
|---|---|---|---|
| [8](#phase-8--baseline-and-toolchain) | P0, P0.5 | Baseline, environment, OpenRouter key; CI and Makefile split per stream; toolchain upgrade | Done (2026-09-27) |
| [9](#phase-9--contracts-and-walking-skeleton) | P1-core, S0, P1b | `contracts/`, proto v2, live vendor spikes; the zero-key walking skeleton | Done (2026-09-28) |
| [10](#phase-10--cores) | P2 | Gateway, engine, dashboard and DX cores, four streams in parallel | Done (2026-09-28) |
| [11](#phase-11--integration-and-the-m-demo-gate) | P3 | Enforcing pipeline, streaming, policies, the redesigned pages; M-Demo gate | Done (2026-10-01) |
| [12](#phase-12--security-credentials-studio-roi) | P4 | Security, credentials and Connect OpenRouter, Studio, labels and calibration, Savings | Done (2026-10-09) |
| [13](#phase-13--static-site-landing-release-tooling) | P5 | Static site with live checks, landing page, release images and tooling, launch drafts | In progress |
| [14](#phase-14--measurement-and-launch) | P6 | Measured benchmarks, a recorded demo, the 2.0.0 release and Pages | Planned |
| [15](#phase-15--v21) | P7 | v2.1: per-claim Jev cascade, knowledge gates, key rotation | Planned |

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
- [x] Create `docs/PLAN.md` (this file), `docs/archive/progress/PHASE-0.md`, `CLAUDE.md`
- [x] Promote `docs-private/GETTING-STARTED.md` → `docs/GETTING-STARTED.md`
- [x] Move VERITAS research artifact → `docs/archive/research/`
- [x] Wire `test_e2e.py` / `test_direct_audit.py` into Makefile (`make test-e2e`)

**Verify**: `make test` passes; Go builds; no orphan files at root.

## Phase 1 — CI/CD

**Goal**: every push lint+test+scan-gated across all three languages.

- [x] `.github/workflows/ci.yml` (split into `ci-{go,python,web,docker,contracts,e2e}.yml` in v2 P0.5) — parallel jobs:
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

- [x] `postgres:16-alpine` in compose; embedded migrations in
      `backend-go/internal/store/migrations/` (minimal runner — ADR-003)
- [x] Schema: `audits` + `audit_claims`
- [x] `backend-go/internal/store/postgres.go` (pgx); worker persists after broadcast,
      best-effort (ADR-004)
- [x] `GET /api/audits` (paginated, filterable by grade/flagged) + `GET /api/audits/:id`
- [x] Store integration tests (Postgres service container in CI; verified locally)
- [ ] Redis cache for result lookups / retire Python in-memory dict
      (deferred to Phase 5 alongside the dashboard-history wiring)

## Phase 5 — Multi-provider + dashboard history

- [x] `providers/openai.py`, `providers/anthropic.py` mirroring `ollama.py`; mocked-HTTP
      unit tests; registered + selectable via `LLM_PROVIDER` env (keys from env, never logged)
- [x] `run_eval` supports openai/anthropic judges; `make eval-compare` runs the
      three-judge HaluEval comparison (needs user API keys — README table pending the run)
- [x] Dashboard history page consuming `GET /api/audits`: filterable (grade/flagged),
      paginated, expandable claim detail; Live/History tab toggle in App
- [x] Frontend tests for history view (9 new; 24 total)
- [ ] Run `make eval-compare` with real keys and publish the comparison table (user action)

## Phase 6 — VERITAS-lite verified knowledge base

**Goal**: upgrade raw-chunk RAG into a claim-level, ingest-verified knowledge base.
*"Most RAG stores index unverified text; TrustAgent's KB only admits claims that pass an
entailment gate against their source, and detects contradictions between sources at ingest."*

- [x] Claim-level ingestion (`kb/ingestion.py`, reusing decomposer)
- [x] Gate-1 ingest verification (reusing verifier; below-threshold → `quarantined`)
- [x] Contradiction detection at ingest (`kb/contradiction.py`; conflicts stored on claim
      payloads in Qdrant rather than a Postgres table — see PHASE-6 deviations;
      `GET /api/kb/conflicts`)
- [x] Hybrid retrieval: BM25 + dense, RRF-fused (`kb/hybrid.py` + `retriever.py`)
- [x] Proto extensions (per-claim ingest results, `ListKBClaims`/`ListConflicts`/`GetKBStats`)
      + stub regeneration
- [x] Frontend KB page: stats, accepted/quarantined claims, conflict pairs
- [x] `docs/KB-DESIGN.md` (dual-gate model, research lineage, explicit out-of-scope list)
- [ ] Trust-weighted scoring (corroboration + ingest entailment in `scorer.py`) — deferred:
      needs claim-provenance plumbing through graph state + a deliberate baseline regen
- [ ] Eval ablation (dense-only vs hybrid) — moves with trust-weighted scoring

Out of scope (deliberate): bitemporal versioning, SUPERSEDES/REFINES edges, permission-aware
retrieval, crypto-shredding.

## Phase 7 — Portfolio polish & v1.0.0

- [x] `docs/ARCHITECTURE.md`: mermaid diagrams + design decisions & trade-offs
- [x] README overhaul: 30-second pitch, evaluation table, security posture,
      honest limitations (demo GIF = owner action, steps in PHASE-7.md)
- [x] Load test (`scripts/load/proxy-overhead.js` + `make load-test`): proxy overhead
      p95 budget under concurrency, dockerized k6
- [x] `CHANGELOG.md` + version 1.0.0 across all mirrors
- [ ] Stretch (not taken): OpenTelemetry tracing Go→gRPC→Python
- [ ] Owner: tag `v1.0.0` + GitHub release with changelog; optional demo GIF +
      `make eval-compare` judge table

---

## 2.0: the Jev gateway

Each phase below is one SPEC phase. Streams work in parallel worktrees; the lead merges them on an
integration branch (`feat/v2-int*`) after each stream's gate, runs the phase gate there, then
merges into `feat/v2-jev-gateway`. P3, P4 and P5 have their own binding plans in
[docs/v2/](v2/) (`P3-PLAN.md`, `P4-PLAN.md`, `P5-PLAN.md`).

## Phase 8 — Baseline and toolchain

**SPEC**: P0 (owner) and P0.5 · **Status**: Done (2026-09-27)

- [x] Baseline commit of the post-1.0 hardening tree; `feat/v2-jev-gateway` and the stream
      worktrees
- [x] The owner's OpenRouter key (kept only in the local `.env`); Docker Compose ≥ 2.24 in WSL
- [x] CI split into `ci-{go,python,web,contracts,docker,e2e}.yml`; the Makefile split into
      `mk/*.mk`; no `container_name`
- [x] Frontend toolchain: Vite 8, TypeScript 6, React 19.3; `toolchain go1.25.11`
- [x] The superseded-identifier check (`scripts/check-forbidden.mjs`)

**Gate**: the web build and tests pass; `docker compose config -q`.

## Phase 9 — Contracts and walking skeleton

**SPEC**: P1-core, S0 and P1b · **Status**: Done (2026-09-28)

- [x] `contracts/`: JSON Schemas, the configuration-key registry, prices and cost assumptions,
      labelled value claims, evaluator templates, question sets, rules, UI copy, draft golden
      vectors
- [x] `proto/evaluator.proto` v2 (additive); migration 0002 and the runner fix
- [x] Live vendor spikes (capped at $1), sanitized into `contracts/fixtures/vendor/`;
      [JEV-ACCESS.md](JEV-ACCESS.md) v1
- [x] **Walking skeleton**: `make demo` with zero keys shows gated traces (grounded pass,
      contradiction flag, no-evidence `not_assessed`) and one completed audit, end to end

**Gate**: `make demo`, `doctor --expect demo`, `trafficgen --once --expect`, the Go golden
vectors and the skeleton Playwright spec.

## Phase 10 — Cores

**SPEC**: P2 · **Status**: Done (2026-09-28)

- [x] **Gateway**: config registry and strict mode; scopes, ingest auth, sessions, CSRF and the
      setup token; every Jev backend with retries, hedging and the breaker; the full `decision`
      package; WebSocket hub v2; worker lanes; the ordered trace writer; `mock-upstream`
- [x] **Engine**: providers with retries and structured outputs; scoring v2 (CONTRADICTED stays
      distinct from UNSUPPORTED); decomposer v2 with UTF-16 spans; internal token and RPC caps
- [x] **Dashboard**: tokens and `DESIGN.md`, the 6-item shell, the Health pill, DataSource and
      StreamClient v2, the TypeScript decision conformer, the landing skeleton
- [x] **DX**: the compose rewrite (profiles, `127.0.0.1` binds, GHCR image names),
      root-context Dockerfiles, `configure.py --ensure`, `doctor.py`, `gen_config_docs.py`

**Gate**: Go vet, race tests and vectors; ruff, black, mypy, unit tests and the golden_v2
baseline; the web lint, types, tests, build and size budget; every compose profile validates.

## Phase 11 — Integration and the M-Demo gate

**SPEC**: P3 · **Status**: Done (2026-10-01)

- [x] The enforcing pipeline: observe, flag, block and fallback (gated again), fail semantics,
      late decisions, test mode off by default
- [x] Streaming: passthrough hold-back with trailers, buffered mode, the decisions long-poll
- [x] Policies in full: validation and block readiness, versions, divergence, dry-run
- [x] Overview, Traffic, Trace detail, Policies, Playground and Knowledge; the static build
      replaying the simulated recording
- [x] Metric renames (`trustagent_gateway_*`, `trustagent_engine_*`); eval harness v2

**Gate**: M-Demo — `make check`; on `make demo` (zero keys) every scenario passes
`trafficgen --once --expect`, fallback included; the stack Playwright specs (trace detail, inline
claims, policy edit and rollback, fallback) and the static replay and landing specs pass. WebSocket
v1 and the 1.0 `/api/audits` shape are deleted.

## Phase 12 — Security, credentials, Studio, ROI

**SPEC**: P4 · **Status**: Done (2026-10-09)

- [x] The encrypted credential store, host binding and redaction
      ([ADR-009](DECISIONS.md#adr-009-an-encrypted-credential-store-replaces-env-only-secrets));
      Connect OpenRouter (self-hosted PKCE) and Paste a key
- [x] Costs and Savings: the spend ledger, daily caps, two labelled counterfactuals, an unbiased
      uniform sample
- [x] Evaluators: the library, labels, calibration fit and adoption; Evaluator Studio (draft,
      test, dry-run, shadow, activate)
- [x] The setup wizard and the onboarding Playwright spec on a fresh stack

**Gate**: P4-PLAN §18, including Docker proofs A (demo-shaped) and B (first run).

## Phase 13 — Static site, landing, release tooling

**SPEC**: P5 · **Status**: In progress (plan: [docs/v2/P5-PLAN.md](v2/P5-PLAN.md))

- [ ] **Live checks in the browser** on the static site: Connect or paste an OpenRouter key, kept
      in the tab for at most 2 hours, courtesy caps, the 402 screen, the static PKCE callback
- [ ] **Static Studio**: templates, editor, test bench, threshold what-if, local calibration,
      Draft with AI, Copy and Download
- [ ] **Landing page**: cost calculator pinned by the `cost` vectors, the dated comparison
      table, Try it, Write your own checks, the "What Jev was asked" panel, self-host
- [ ] **Replay**: simulated and recorded datasets, the glass box rebuilt in the browser, honest
      static states, the first-visit guide, the `/og` card
- [ ] **Gateway tools**: the vendor-fault scenarios, `trafficgen --record`, the knowledge
      scenario in process, published k6 overhead runs, Tier-0 benchmarks, `jevcheck`
- [ ] **Release**: version tooling, multi-arch GHCR images with attestations, the pull-based
      quickstart, `pages.yml` (deploys only once the owner turns Pages on), `record-demo`,
      the complete doctor, `first-run` and golden-path tests
- [ ] **Docs and launch**: `check-claims`, the README hero, ADR-005 to ADR-010, the launch
      drafts in [docs/launch/](launch/), capture tooling

**Gate**: P5-PLAN §18 — the static build passes its Playwright projects (labels visible, no
network except the site and openrouter.ai, CSP meta present) and the size budgets;
`make load-test-gate` publishes the overhead run; `make check` includes `check-claims` and
`check-version`.

## Phase 14 — Measurement and launch

**SPEC**: P6 (the owner's key; bench-lite capped at $5) · **Status**: Planned

- [ ] `make bench-lite` (RAGTruth-QA: the fast check, the same checks asked of an LLM, a full
      audit) and `make calibrate WRITE=1` on the pinned snapshot
- [ ] `make record-demo` on a calibrated showcase route; OG card and hero GIF from that recording
- [ ] `make bench-publish` (refuses mock output) fills `contracts/claims/measured.json`
- [ ] Re-verify the comparison table and the market claims; the owner reviews every cell
- [ ] TypeSafe and OpenRouter terms checked for publishing benchmarks
- [ ] CHANGELOG, `scripts/bump_version.py 2.0.0`, the PR to `main`, tag `v2.0.0`, GHCR release,
      Pages turned on (owner)

**Gate**: `make check`, `scripts/check_version.py`, `node scripts/check-claims.mjs --launch` and
`scripts/check_recording.py --require-recorded frontend-react/demo-data/recorded`.

## Phase 15 — v2.1

**SPEC**: P7 · **Status**: Planned

- [ ] A Go-hosted `DecisionService.Decide`, and the per-claim Jev cascade in the deep audit
- [ ] Knowledge-base Jev gates, an injection screen, a conflict prefilter
- [ ] The `by_evidence` calibration partition as an A/B
- [ ] Master-key rotation (`SECRET_KEY_PREVIOUS`); shared PKCE nonces and caps across replicas,
      and a credential-verify limit keyed across replicas (today it is per replica)
- [ ] Route-filtered calibration fits that keep their labels under short retention (labels carry
      no route today)
- [ ] cosign image signatures, on top of the provenance and attestations
- [ ] Live verification of TypeSafe-direct; the full `eval-live` run

---

## Verification strategy

- Every phase lands with green CI (from Phase 1 onward).
- From Phase 8 on, every phase also passes its gate on the integration branch before it merges,
  and the golden vectors keep Go and TypeScript decisions identical.
- Each `docs/archive/progress/PHASE-N.md` records verification evidence (test output, screenshots).
- Full-system checks after Phases 4 and 6: `docker compose up`, `make test-e2e`, manual
  dashboard walkthrough (live audit, history page, KB upload with conflicting documents).
- If time pressure hits: cut from the bottom of Phase 6 (hybrid retrieval drops first);
  never cut Phases 1–3.
