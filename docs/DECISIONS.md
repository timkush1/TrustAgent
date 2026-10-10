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

## ADR-005: Jev is the tier-1 check on every answer, behind a vendor-neutral `DecisionModel`

**Context** (2.0, [SPEC](v2/SPEC.md) §1): 1.0 audited answers after the fact with a
claim-level LLM judge. That is too slow to act on before the client reads the
answer, and too expensive for every answer: a full claim-level audit of each one
costs about <!-- claim:llm_full_audit_cost_per_1k --> ~$35 / 1,000 [estimate].
TypeSafe AI's Jev (launched 2026-09-15) is a classifier: it answers yes/no,
pick-one and scale questions with probabilities in one HTTP call, and writes no
text.
**Decision**:
- *Tiers.* Tier 0 is deterministic Go detectors (numbers, dates, URLs, quotes,
  citations, secrets, system-prompt leaks). Tier 1, the **Fast check**, asks Jev
  every evaluator attached to the route as an independent question, all in **one**
  call (split only by token budget or question count), on every answer:
  <!-- claim:fast_check_cost_per_1k --> ~$0.21 / 1,000 answers [estimate]. Tier 2,
  the deep audit, runs asynchronously on flagged answers plus small samples.
- *One interface.* Jev sits behind `DecisionModel` (`backend-go/internal/jev`)
  with the backends `openrouter-decisions`, `openrouter-systemone`, `typesafe`,
  `compatible`, `mock` and `off`. Raw HTTP, not vendor SDKs; hard per-attempt
  timeouts, hedging, retries, a breaker that counts only vendor faults, rate
  limits, daily spend caps, snapshot tracking, and fail-open by default.
- *No evidence, no question.* Groundedness needs documents. Without any, the
  result is `not_assessed` and Jev is never asked: its scores invert without
  evidence.
- *Local calibration.* Thresholds are fitted per Jev snapshot; blocking waits for
  a calibration ([ADR-006](#adr-006-per-route-policies-decide-what-happens-to-a-risky-answer)).
- *One client.* Only Go calls Jev in 2.0. The per-claim cascade inside the deep
  audit moves to v2.1, behind a Go-hosted `DecisionService`.

**Rejected**:
- *An LLM judge on every answer.* Asking Claude Haiku the same questions costs
  <!-- claim:same_checks_cost_multiple --> about 27× [estimate] the fast check, and
  takes seconds instead of a fraction of one.
- *One call per evaluator.* Cost and latency would grow with every check added.
- *Jev clients in both Go and Python.* Two sets of retries, caps and cost records
  to keep in step (SPEC §0, critique 15).
- *Wiring the vendor in.* The interface keeps the backend swappable, so "a small
  classifier or logprobs could do this" is settled by the benchmark table, not by
  a rewrite.

## ADR-006: Per-route policies decide what happens to a risky answer

**Context** (2.0, SPEC §2.9 and §7.2): a score nobody acts on is only a chart, and
routes differ: a support bot can flag, a payments answer may need to block. A
wrong block hurts users more than a missed flag, and Jev's default 0.5 threshold
is not a safe line: one third-party test reached
<!-- claim:arize_ragtruth_accuracy_at_default_threshold --> 76% at 0.5 [third-party]
and its best accuracy only at a tuned threshold, which shows why thresholds must
be calibrated per question and snapshot.
**Decision**:
- *Modes per route.* `observe` (measure only), `flag` (response headers),
  `block` (a block body) and `fallback` (a fallback upstream's answer, gated
  again); fail open or closed; streaming passthrough or buffered.
- *One decision function.* `decide()` is contract-owned: Go is the reference
  (`internal/decision`), the browser runs a TypeScript conformer, and both pass
  the same frozen golden vectors.
- *Two signals to block.* A groundedness block needs a second, agreeing signal
  (a corroborating detector, or contradiction, which implies groundedness).
  "Nothing supports it" is not "something disagrees".
- *Calibration before blocking.* A block-set evaluator without a measured
  calibration for the current snapshot acts as flag (`calibration_missing`).
  Every downgrade is shown with its fix, never silent. The `shadow` stage
  attaches a check that is asked and stored but never acts.
- *One versioned policy.* `config/policies.yaml` (policy as code) and dashboard
  edits are versions of one document; the newest wins. A file edited after a
  dashboard save is `diverged` until someone adopts the file or keeps the
  dashboard version; `CONFIG_LOCK` makes the file authoritative. Saves go
  through a review with server validation, block readiness and a free "Preview
  impact" replay of past answers. Go is the only configuration authority; the
  engine receives runtime settings as `(epoch, version)`.

**Rejected** (SPEC §0):
- *Per-route budget shares* (critique 25): too complex. Enforcing routes get a
  25% reserve of the daily decision cap instead.
- *Cutting the policy dry-run* (critique 47): it is a pure replay through
  `decide()` and powers both "Preview impact" and block readiness.
- *Calibration partitioned by evidence source by default* (critique 14): attaching
  any evaluator would then switch blocking off.
- *Calling the mock calibration "measured"* (critique 5).

## ADR-007: Frontend stack

**Context** (2.0, SPEC §3.3): the redesign needed typed routes with all view
state in the URL, one live stream feeding a query cache, a static build that runs
under a strict CSP, and a landing page small enough to load fast on a phone.
**Decision**:
- React 19.3 (pinned), TypeScript ~6.0, Vite 8, Node 22.
- TanStack Router (file routes, validated search params, `autoCodeSplitting`) and
  TanStack Query, fed by the WebSocket v2 `StreamClient`.
- Tailwind 4 over the design tokens in `DESIGN.md`; Base UI primitives
  (shadcn over Base UI); `lucide-react` icons.
- Recharts as the only chart library; the reliability chart, confusion grid and
  waterfall are custom SVG.
- CodeMirror 6 for the policy and evaluator editors; `zod/mini` for search params
  and `/config.json`; ajv validators compiled ahead of time
  (`make gen-policy-validator`), because runtime ajv needs `eval`, which the CSP
  forbids.
- Wire types live in `src/types/api.ts` and generate
  `contracts/schema/api.v1.schema.json`; msw serves the contract fixtures in tests;
  Playwright with axe covers both themes.
- The landing page is a separate Vite entry with no router, query cache, chart
  library or motion library, held under its gzip budget by `npm run size`.

**Rejected**:
- *visx, cmdk, TanStack Table and Storybook* (SPEC §3.3): weight without a page
  that needs them.
- *Radix*: Base UI is the shadcn default and gives the same accessible primitives
  with a `render` prop (`DESIGN.md` §14).
- *zod 3 with `zod-adapter`*: Zod 4's mini build plugs into the router directly
  and keeps about 20 KB gzip out of the shell.
- *Lighthouse as a blocking gate*: informational only; the size budget blocks.

## ADR-008: The public demo is a static site that replays a recording, with live checks on the visitor's own key

**Context** (2.0, SPEC §7.5 and §8): anyone should see the gateway catch a
hallucination within a minute, with nothing to install and nobody paying for
their traffic, and every number on the page must be honest about where it came
from.
**Decision**:
- *Static build.* GitHub Pages serves `VITE_APP_MODE=static`: the landing page at
  `/` and the dashboard at `/app/…`, replaying a recording through
  `ReplayDataSource` (REST answered from the tape position, the rest played as
  WebSocket v2 events). Policies, Settings and Knowledge are hidden, and a deep
  link to them says why.
- *Two datasets, never confused.* `frontend-react/demo-data/simulated/` is
  generated from the zero-key demo's scripted scenarios and labelled Simulated
  everywhere; `demo-data/recorded/` holds only a `kind: recorded` export from
  `make record-demo` against real backends. `check_recording.py` refuses a label
  that could pass a simulated tape off as model output, and a release build
  requires the recorded one.
- *Live checks, secondary.* "Try it live in your browser" sits in the Playground
  below the replayed examples: the visitor connects OpenRouter (PKCE from the
  browser) or pastes a key. The key stays in that tab's `sessionStorage` for at
  most 2 hours (Forget key clears it) and is sent only to openrouter.ai. The
  browser runs the same decision code as the gateway (the TypeScript conformer,
  pinned by the golden vectors, PKCE included), within courtesy caps
  (`VITE_PLAYGROUND_MAX_CALLS`, `VITE_PLAYGROUND_MAX_TOKENS`) and a running cost.
- *A small attack surface.* A meta CSP allows only the site itself and
  openrouter.ai; every surface that takes a key refuses to run inside a frame;
  Playwright asserts that the site contacts nothing else.
- *The owner publishes.* The Pages workflow builds on every push but deploys
  only after the owner turns Pages on.

**Rejected**:
- *A hosted backend for visitors*: it would hold other people's keys or spend
  ours, and need abuse controls a static site does not.
- *A runtime fallback between the two Jev endpoints*: it would double billable
  calls; the endpoint is chosen at build time.
- *Keys in `localStorage`, cookies or IndexedDB*: they outlive the tab.
- *Benchmarks in the static nav* before a measured run exists.

## ADR-009: An encrypted credential store replaces env-only secrets

**Context** (2.0, SPEC §7.5): the first v2 design kept every secret in the
environment, never readable or editable in the dashboard. That made the first run
a shell exercise (edit `.env`, restart) and ruled out a one-click "Connect
OpenRouter".
**Decision**:
- *Sealed at rest.* Keys saved in the dashboard are sealed with AES-256-GCM
  (12-byte nonce; the name and provider as authenticated data) under an HKDF
  subkey of a master key: `TRUTHTABLE_SECRET_KEY`, its `_FILE`, or one generated
  (mode 0600) in the `trustagent-secrets` volume.
- *Env still wins.* A non-empty environment variable takes precedence and is
  read-only in the UI.
- *Bound to a host.* Each credential has `allowed_hosts`; every outbound call is
  checked, and pointing a key at another host means entering it again.
- *Never back to the browser.* Only a masked form and a fingerprint leave the
  gateway; logs, errors, the API and the stream pass a redactor.
- *Hot reload.* A saved key takes effect without a restart: the backend's breaker
  resets and the runtime config is pushed to the engine. A new key binds to the
  fast check and the judge only after its check passes.
- *Connect OpenRouter.* Self-hosted PKCE keeps the verifier on the server, so the
  key never reaches browser JavaScript. Paste-a-key stays available.
- *First admin.* A one-time setup token (in the URL fragment, 1 hour, claimed
  atomically) creates the first admin key; sessions are HMAC-signed cookies.

**Rejected**:
- *Env-only secrets*, the decision this replaces; the environment remains the
  highest-precedence source.
- *Showing or editing stored keys in the browser.*
- *Key rotation in 2.0*: `SECRET_KEY_PREVIOUS` is v2.1.

## ADR-010: Compose profiles, and release images on GHCR

**Context** (2.0, SPEC §3.4): 1.0 started every service, which is heavy on an
8 GB laptop, and building from source needed host tools for the gRPC stubs.
Knowledge retrieval is optional in 2.0 (evidence comes with the request).
**Decision**:
- *A small default.* `docker compose up` starts the proxy, engine, Postgres and
  dashboard. Add-ons are profiles: `knowledge` (Qdrant and embeddings),
  `local-llm` (Ollama), `observability` (Prometheus and Grafana), `redis`, and
  `demo`; the e2e, onboarding and load stacks are overlays.
- *Safe by default.* Every published port binds `${BIND_ADDR:-127.0.0.1}`, and
  no service sets `container_name`, so parallel stacks keep apart by project.
- *Docker alone builds it.* Root-context Dockerfiles generate the gRPC stubs
  inside the image.
- *Release images.* `ghcr.io/timkush1/truthtable-{proxy,engine,dashboard}` for
  `linux/amd64` and `linux/arm64`, tagged `edge`, the version and `sha-<short>`,
  with BuildKit provenance and SBOMs plus GitHub build-provenance attestations
  (`gh attestation verify`). `TRUSTAGENT_VERSION` picks the tag; on a release
  checkout `make up` and `make demo` pull, elsewhere they build.

**Rejected**:
- *Every service in the default set*: memory and start-up time for add-ons most
  users never turn on.
- *Fixed container names*: they stop two stacks (or worktrees) running side by
  side.
- *cosign signatures in 2.0*: provenance and attestations first; signing is
  v2.1.
- *A published knowledge image*: the `knowledge` profile builds its engine
  locally.
