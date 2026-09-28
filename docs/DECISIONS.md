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

**Re-baseline for scoring v2** (v2 gateway, P2; SPEC section 3.2): both golden
baselines were regenerated on purpose, in the same change as scoring v2,
decomposer v2 and the verifier hardening. The gate itself is unchanged (exact
match, zero tolerance); what it compares changed:

- *Scoring.* Each decided claim is a verdict distribution; CONTRADICTED counts
  as wrong and UNSUPPORTED as baseless (request evidence) or unverifiable
  (knowledge evidence); the verdict comes from the contradiction and baseless
  flags instead of the v1 ratio rule. UNKNOWN and NOT_CHECKED claims are left
  out, a judge outage is a FAILED audit with no score (D4: v1 scored it about
  0.3), and `faithfulness_score` now carries the trust score.
- *Report.* Each example records its status, verdict, degraded reasons, policy,
  trust score, `p_hallucinated` and per-claim verdicts with UTF-16 spans (D17).
  FAILED audits are counted and left out of the detection metrics, whose
  ranking score is `p_hallucinated` (v1 used 1 - faithfulness).
- *golden_v1* (dataset and fixtures frozen, still runnable as
  `--dataset golden`): precision 0.889 -> 0.920, recall 0.923 -> 0.885,
  F1 0.906 -> 0.902, balanced accuracy 0.899 -> 0.899, AUROC 0.907 -> 0.983,
  ECE 0.133 -> 0.085 (49 of 50 scored). Three examples moved:
  `med-insulin-faithful` (a parse failure) is FAILED instead of scoring 0.3;
  `special-bees-boundary` (one partial claim among three) is no longer a false
  positive; `special-greatwall-partial` becomes a false negative, because its
  recorded judge says PARTIALLY_SUPPORTED where the v2 prompt rule (any part
  stated false is CONTRADICTED) makes it CONTRADICTED, as golden_v2 records it.
- *golden_v2* (new CI gate, 91 examples: faithful, contradicting and baseless
  variants of every fact, NO_CLAIMS, the short-answer and knowledge-evidence
  cases, capped evidence, UTF-16 spans, repairs, fallbacks and judge outages):
  precision 0.960, recall 0.941, F1 0.950, balanced accuracy 0.944,
  AUROC 0.985, ECE 0.076 over 88 scored examples; 3 FAILED by design;
  86 of 91 expected outcomes met (the 5 misses are the recorded judge's
  deliberate errors).

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
