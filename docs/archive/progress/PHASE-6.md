# Phase 6 — VERITAS-lite Knowledge Base

**Status**: ✅ Complete (2026-06-11)
**Goal**: upgrade the raw-chunk RAG store into a claim-level, entailment-gated
knowledge base — the project's differentiator. Design + research lineage:
[docs/KB-DESIGN.md](../KB-DESIGN.md).

## What was done

### gRPC contract (`proto/evaluator.proto` + `make proto`)
- `IngestResponse` extended with per-claim results (accept/quarantine status,
  entailment score, conflicts) and aggregate counters.
- New RPCs: `ListKBClaims` (paged, status-filterable), `ListConflicts`,
  `GetKBStats`. Stubs regenerated for Go and Python (protoc 35.0 installed
  locally; module-mode flags match CI).

### Python KB package (`src/truthtable/kb/`)
- **`ingestion.py` — Gate-1**: documents → atomic claims (reusing the audit
  decomposer) → each claim verified against *its own source* (reusing the
  audit verifier) → `accepted` (SUPPORTED ≥ 0.7 confidence;
  PARTIALLY_SUPPORTED counts at half) or `quarantined` (stored + visible,
  never retrievable). Full provenance per claim: source doc id, excerpt,
  entailment score, timestamp.
- **`contradiction.py`**: accepted claims are NLI-checked (CONTRADICTS /
  CONSISTENT, strict JSON, ≥ 0.7 confidence) against nearest accepted
  neighbors; pairs recorded on both claims' payloads. Same injection defenses
  as the audit pipeline (delimited untrusted data, sanitization, strict
  output validation).
- **`hybrid.py`**: ~40-line Okapi BM25 over claim texts + dense search, fused
  with RRF (k=60); lazy index rebuild on ingest (`mark_dirty`); quarantined
  claims excluded from both paths.
- `QdrantStore` gained the KB operations: `upsert_points` (explicit ids),
  `search_filtered` / `scroll_points` (payload filters), `set_payload`.
- `RetrieverNode` / `build_audit_graph` accept an optional hybrid retriever;
  `main.py` wires ingestor + detector + hybrid retrieval when RAG is enabled.
  Legacy chunk ingestion remains the fallback without an LLM.

### Go proxy
- Client wrappers for the new RPCs; `IngestDocuments` now returns the full
  claim-level report (with a 4× timeout — ingestion makes several LLM calls
  per document).
- New endpoints behind auth + rate limiting: `GET /api/kb/claims`,
  `GET /api/kb/conflicts`, `GET /api/kb/stats`; `/api/upload` response now
  includes per-claim results.

### Frontend
- New **Knowledge Base** tab: stat cards (total/accepted/quarantined/
  conflicts), contradiction pairs panel (the demo money shot), claims table
  with status filter and entailment scores, refresh, empty/error states.

## Deviations
- **Trust-weighted scoring deferred** (planned item 5): blending corroboration
  counts into the audit score requires plumbing claim provenance through the
  graph state, and would have forced a golden-baseline regeneration in the
  same commit as a large feature. Tracked in docs/PLAN.md; the eval ablation
  for hybrid retrieval moves with it.
- BM25 implemented directly instead of adding `rank-bm25` (tiny corpus,
  standard formula, no new dependency).
- Conflicts stored on claim payloads (Qdrant) rather than a Postgres
  `claim_relations` table: the conflict data lives where the claims live, and
  the Go side stays a thin proxy. Revisit if conflict workflows need history.

## Tests (+20: 77 Python, 30 frontend, Go suites)
- `tests/unit/test_kb.py` (16): accept/quarantine/threshold/partial-credit
  paths, **planted-contradiction detection with bidirectional payload
  updates**, consistent-claims negative case, garbage-judge safety,
  injection-defense prompt assertions, BM25 ranking + tokenization, RRF
  agreement-rewarding + order preservation, quarantine exclusion + lazy
  rebuild in hybrid retrieval, end-to-end ingest with MockLLMProvider.
- Go `kb_api_test.go` (4): param parsing defaults/valid/invalid, 503s without
  engine.
- Frontend `KnowledgeBaseView.test.tsx` (6): stats+claims render, quarantine
  badge, conflict pairs, status filter, empty state, engine-unavailable error.

## Verification evidence
- Python: **77 passed**; ruff + black clean; **golden baseline unchanged**
  (hybrid retrieval only activates when a HybridClaimRetriever is wired, and
  the eval harness injects context, so the regression gate is unaffected).
- Go: build/vet/gofmt clean, all package tests ok.
- Frontend: eslint + tsc clean, **30 passed**.
