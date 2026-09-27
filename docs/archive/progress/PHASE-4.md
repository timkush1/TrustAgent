# Phase 4 — Persistence (Postgres)

**Status**: ✅ Complete (2026-06-10)
**Goal**: audit history survives restarts; queryable via a paginated REST API.
Decisions recorded in [docs/DECISIONS.md](../DECISIONS.md) (ADR-002/003/004).

## What was done

### Store layer (`backend-go/internal/store/`)
- `store.go` — `Store` interface, `AuditRecord`/`ClaimRecord` types,
  `ListFilter` (limit/offset/grade/flagged), shared `GradeForScore` mapping.
- `postgres.go` — pgx/pgxpool implementation with a **minimal embedded
  migration runner**: `go:embed`-ed `*.up.sql` files applied in filename order,
  tracked in `schema_migrations` (~50 lines instead of a golang-migrate
  dependency — ADR-003).
- `migrations/0001_create_audits.up.sql` — `audits` (scores, grade, flags,
  reasoning trace, `step_timings` JSONB, indexes on created_at/grade/flagged)
  + `audit_claims` (position-ordered, evidence JSONB, FK cascade).
- `SaveAudit` is transactional and idempotent (`ON CONFLICT DO NOTHING` on the
  audit id — replays from the worker can't duplicate history).

### Worker integration (`internal/worker/pool.go`)
- `Pool.AttachStore` (constructor unchanged — existing tests untouched).
- `persistAudit` runs **after** the WebSocket broadcast with its own 5s
  timeout; failures are logged, never fail the audit pipeline (ADR-004:
  best-effort persistence, availability over history completeness).

### API (`cmd/proxy/audits_api.go`)
- `GET /api/audits?limit&offset&grade&flagged` — paginated history with
  validated query params (limit 1-200, grade A-F whitelist, strict bool).
- `GET /api/audits/:id` — full record incl. claims; 404 via `pgx.ErrNoRows`.
- Both behind the Phase-3 auth + rate-limit middleware; 503 with a helpful
  message when persistence isn't configured.

### Deployment & CI
- `postgres:16-alpine` service in compose (volume, healthcheck; password via
  `POSTGRES_PASSWORD`, dev default documented in `.env.example`); proxy gets
  `TRUTHTABLE_DATABASE_URL`. Port 5432 published for hybrid local dev.
- CI Go job now runs a Postgres service container and sets
  `TEST_DATABASE_URL`, so the integration tests run on every PR (they
  self-skip when the variable is absent locally).

## Deviations
- Redis read-cache and retiring the Python in-memory results dict deferred to
  Phase 5: the only consumer of `GetAuditResult` polling is going away when
  the dashboard reads history from `GET /api/audits`, so caching now would be
  building for a path about to be deleted.

## Tests
- `internal/store/postgres_test.go` (5): save/get round-trip incl. JSONB step
  timings + ordered claims, idempotent saves, ErrNoRows on missing id,
  pagination + grade/flagged filters, grade mapping table test.
- `cmd/proxy/audits_api_test.go` (4): filter defaults, valid parsing, 8
  invalid-input rejections, 503-without-store on both endpoints.

## Verification evidence
- Integration tests run against a real `postgres:16-alpine` container locally:
  **5/5 PASS** (migrations applied, full round-trip verified).
- `go build`/`go vet`/gofmt clean; gosec medium+ exit 0; all package tests ok.
- `docker compose config` validates with the new service.
