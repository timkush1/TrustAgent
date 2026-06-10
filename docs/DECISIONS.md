# Architecture Decision Records

Lightweight ADRs — one section per decision, newest last. Each records the
context, the decision, and what was deliberately rejected.

## ADR-001: CI evals run the real pipeline with a recorded (mock) model

**Context** (Phase 2): a hallucination detector needs regression protection in
CI, but live LLM calls are slow, paid, and nondeterministic.
**Decision**: two tiers. Tier 1 runs the real LangGraph pipeline against 50
golden examples with a fixture-replay `MockLLMProvider` (content-matched, so
prompt-template changes don't invalidate fixtures) and compares per-example
results against a committed baseline with zero tolerance. Tier 2 runs real
models against HaluEval on a schedule.
**Rejected**: LLM-in-CI on every PR (flaky, slow, costly); pure unit tests of
the scorer (don't catch graph-wiring or parsing regressions).

## ADR-002: Postgres over SQLite for audit persistence

**Context** (Phase 4): audit results lived in an in-memory dict and browser
localStorage; restarts lost everything.
**Decision**: Postgres 16 in the existing compose stack; the Go worker pool is
the single writer (it already holds the complete result before broadcasting);
history is served by `GET /api/audits` from the same Go process.
**Rejected**:
- *SQLite* — the stack is already container-orchestrated so one more service
  is free, and a file DB would need awkward shared-volume access if the Python
  engine ever reads it. Postgres is also the employer-default choice.
- *Writing from Python* — dual writers complicate ownership; the Go side has
  the complete result and the HTTP surface.
- *Redis as primary store* — wrong durability model; Redis stays as the
  rate-limiter backend (and future cache).

## ADR-003: Minimal embedded migration runner instead of golang-migrate

**Context** (Phase 4): the schema needs versioned migrations applied on
startup.
**Decision**: embed `*.up.sql` files via `go:embed`, apply in filename order,
track in a `schema_migrations` table (~50 lines in `internal/store/postgres.go`).
**Rejected**: golang-migrate — a heavyweight dependency whose main value
(down-migrations, multiple sources, CLI tooling) isn't needed for a linear,
append-only schema history owned by one binary. Revisit if migrations ever
need rollback or out-of-band execution.

## ADR-004: Persistence is best-effort relative to the live pipeline

**Context** (Phase 4): a Postgres outage must not take down auditing.
**Decision**: the worker persists *after* broadcasting, with its own 5s
timeout; failures are logged and dropped. Similarly the rate limiter fails
open when Redis is down. Availability of the core audit path is prioritized
over completeness of history/limits.
**Rejected**: write-before-broadcast (couples user-visible latency to DB
health); failing audits on persistence errors.
