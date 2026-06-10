# Phase 5 — Multi-Provider + Dashboard History

**Status**: ✅ Complete (2026-06-10) — except the paid judge-comparison run,
which needs the owner's API keys (see "Pending user action").

## What was done

### Cloud LLM providers (`backend-python/src/truthtable/providers/`)
- **`openai.py`** — OpenAI Chat Completions; key from `OPENAI_API_KEY`
  (constructor raises without one; the key is never logged); health check via
  `GET /v1/models`.
- **`anthropic.py`** — Anthropic Messages API; handles the two real API-shape
  differences: the system prompt is a top-level `system` parameter (not a
  message role), and responses carry content as typed blocks.
- Both registered in the existing registry; selectable via `LLM_PROVIDER` /
  `LLM_MODEL` env (compose passes the keys through; `.env.example` documents).
- `main.py` fix: the Ollama `base_url` was being passed to every provider —
  now only Ollama receives it, so cloud providers keep their own endpoints.

### Eval-judge support
- `run_eval --provider openai|anthropic` works end-to-end.
- `make eval-compare`: three-judge HaluEval comparison
  (llama3.2 local / gpt-4o-mini / claude-haiku) producing three JSON reports.

### Dashboard history (frontend)
- `src/api/audits.ts` — typed client for `GET /api/audits` (+ single audit),
  with a distinct user-facing message for the 503 persistence-disabled case.
- `src/components/history/HistoryView.tsx` — paginated table (20/page) with
  grade and hallucinations-only filters, expandable rows showing the response
  and per-claim verdicts, grade-colored badges matching the live feed.
- `App.tsx` — Live Feed / History tab toggle (accessible `role=tab` semantics).

### CI fix discovered en route
- The Phase-4 `go mod tidy` silently bumped the module's `go` directive to
  **1.25.0** (pulled up by pgx v5.10), which broke the proxy *Docker image*
  build (`golang:1.24-alpine` builder) while the CI go job (which reads
  `go-version-file`) kept passing — a nice example of why CI builds the
  images too. Builder bumped to `golang:1.25-alpine`; README/CLAUDE.md
  prerequisites updated.

## Tests (+17: 61 Python, 24 frontend)
- `tests/unit/test_cloud_providers.py` (8): missing-key errors, env-key
  pickup, completion parsing (incl. Anthropic text blocks + usage math),
  HTTP-429 error surfacing, system-prompt placement assertions for both
  APIs, registry integration.
- `src/components/history/HistoryView.test.tsx` (9): load/render, query
  params, grade + flagged filters resetting pagination, row expansion with
  claims, hallucination badge, empty state, 503 error state, next/prev
  pagination.

## Pending user action
`make eval-compare` needs `OPENAI_API_KEY` + `ANTHROPIC_API_KEY` (~$1–5 of
usage at 100 samples). Once run, add the three-way comparison table to the
README Evaluation section (the owner approved the spend in planning).

## Verification evidence
- Python: 61 unit tests pass; ruff + black clean; golden baseline unchanged.
- Frontend: eslint + `tsc -b` clean; 24 tests pass.
- Security on `c349b66`: **green** (Trivy tag + bandit cwd fixes confirmed).
- CI go/python/frontend jobs green incl. the first Postgres-service store
  test run; Docker job failure root-caused (go 1.25 directive vs 1.24 image)
  and fixed this phase.
