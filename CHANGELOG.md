# Changelog

## Unreleased — 2.0.0 "Jev gateway" (in progress)

TrustAgent 2.0 turns the async auditor into a **hallucination gateway**: a fast
Jev decision check on every answer, per-route policies, pluggable evidence (no
RAG by default), Evaluator Studio, and a redesigned dashboard. The full
specification is [docs/v2/SPEC.md](docs/v2/SPEC.md); progress is tracked by
phase (P0 → P7) in [docs/PLAN.md](docs/PLAN.md), and the reasons behind the big
choices are ADR-005 to ADR-010 in [docs/DECISIONS.md](docs/DECISIONS.md). This
section sums the release up; the work as it landed, phase by phase, is under
*Build log by phase* at its end.

### Highlights
- **A gateway in the request path.** Point an OpenAI or Anthropic SDK at the
  proxy. Every answer gets the Tier-0 detectors (deterministic Go checks inside a
  2 ms budget) and one **Fast check (Jev)** call that asks all of the route's
  checks at once; the route's policy then observes, flags, blocks or falls back.
  OpenAI chat and completions and Anthropic Messages, JSON and SSE
  ([docs/GATEWAY.md](docs/GATEWAY.md)).
- **Evidence from the request; no RAG by default.** Groundedness is checked
  against what the request already carries: system-prompt documents, user
  context, tool results and the `trustagent.context` extension. With no evidence
  the result is *not assessed* and Jev is never asked. Qdrant and embeddings are
  the optional `knowledge` profile.
- **Deep audit, asynchronously.** The LangGraph engine splits an answer into
  claims, checks each against the evidence with a judge and scores the answer.
  CONTRADICTED (the evidence disproves it) stays distinct from UNSUPPORTED
  (nothing addresses it) end to end, and results stream to the dashboard over
  WebSocket v2.
- **Policies, checks and Evaluator Studio.** Versioned per-route policies with a
  free impact preview and rollback, 11 built-in checks, and a Studio that drafts,
  tests and calibrates your own. A route blocks only with a calibration for the
  current Jev snapshot; until then it flags and says why.
- **Keys, costs and access in the dashboard.** Connect OpenRouter (PKCE) or paste
  a key; keys are sealed with AES-256-GCM. Scoped app and admin keys, sessions, a
  cost ledger, daily budgets, and a Savings page that sets the gateway beside an
  LLM judge doing the same checks.
- **Try it without installing anything.** A static site with a landing page, a
  replay of the demo tape, Evaluator Studio and a Playground that runs Jev in your
  browser with your own OpenRouter key. A zero-key `make demo` runs the whole
  stack on mocks, labelled Simulated.
- **Honest numbers.** Every figure on a page carries its label (measured,
  estimate, vendor, third-party or simulated); `make check-claims` holds the
  README and docs to `contracts/claims/`, and mock output is never shown as
  measured.

### Upgrading from 1.x: every breaking change at a glance
Details are under *Build log by phase* (*Breaking changes (P3)* and
*Breaking changes (P4)*); the steps to take follow this list.
- **Metric names.** The engine exports `trustagent_engine_*` and the gateway
  `trustagent_gateway_*`; every Grafana query filters by `job` (table under
  *Breaking changes (P3)*).
- **WebSocket v2 only.** `GET /ws?protocol=2` with the v2 events; the 1.0
  `audit_result` frames and `/api/audits` shape are gone.
- **The gateway's key travels in `X-TrustAgent-Key`.** 1.0 read its own key from
  `Authorization`, the header provider SDKs fill with the upstream key (bug B1).
  2.0 reads `X-TrustAgent-Key` first; `Authorization` and `x-api-key` go upstream
  untouched unless the gateway injects its own upstream key (inject mode, where a
  proxy key in the SDK's key slot is accepted). Cross-origin browsers may send only
  `X-TrustAgent-Key` (*Breaking changes (P4)*).
- **Test mode is off by default.** `test_response` and `X-TrustAgent-Scenario` are
  honoured only with `TRUTHTABLE_TEST_MODE=true` (1.0 honoured them on every
  request, M7); otherwise `/v1` answers 400 `test_mode_disabled`. `make demo` and
  the e2e stack set it.
- **`VITE_TRUTHTABLE_API_KEY` is removed.** The dashboard bundle carries no key;
  it signs in with a session (the one-time setup link creates the first admin
  key). Delete the variable from your build environment.
- **`/health` is liveness only.** It always answers 200
  `{status, version, demo, audit_engine}`, with `audit_engine` from a cached real
  gRPC ping (1.0 reported only that a client existed). Readiness is
  `/health/ready` (`?strict=1` answers 503 until ready); component detail is
  `/api/status` for signed-in users.
- **Ingest keys are required by default.** `TRUTHTABLE_INGEST_AUTH=keys`: `/v1`
  traffic needs a key with the `ingest` scope (the setup wizard mints one;
  `scripts/configure.py --ensure` mints the traffic generator's). `open` must be
  set explicitly and is refused while the gateway injects an upstream key.
- **The compose service `audit-engine` is now `engine`** (and so is its Prometheus
  job). Services set no `container_name`: use `docker compose exec engine …`.
- **Ports bind to `127.0.0.1`.** Every published port binds
  `${BIND_ADDR:-127.0.0.1}`; set `BIND_ADDR=0.0.0.0` only behind a firewall.
- **Images come from GHCR.** Compose names
  `ghcr.io/timkush1/truthtable-{proxy,engine,dashboard}`, tagged by
  `TRUSTAGENT_VERSION`. On a release checkout `make up` and `make demo` pull them;
  elsewhere they build from source as before. The `knowledge` profile builds its
  engine locally: no knowledge image is published.
- **No downgrade after migration 0002.** Back up the database before upgrading.

**Steps when you upgrade:**
- **Before saving keys in the dashboard,** back up the `trustagent-secrets`
  volume or set `TRUTHTABLE_SECRET_KEY` in `.env`: saved keys decrypt only with
  the master key they were sealed with, and a Postgres backup alone means
  entering them again ([docs/SECURITY.md](docs/SECURITY.md#master-key)).
- **Migrations 0003 to 0006** apply at proxy start and, like 0002, have no
  downgrade. Upgrade the proxy and the engine together.
- **Production origins:** the default `TRUTHTABLE_ALLOWED_ORIGINS` includes
  `http://localhost:5173` for the Vite dev server; set the variable explicitly
  in production.
- **Several proxy replicas** need sticky sessions for Connect OpenRouter (its
  nonce lives in one process for 10 minutes), and each enforces the budgets on
  its own between 60 s resyncs.
- **Metrics:** replace the 1.0 names in dashboards and alerts using the table
  under *Breaking changes (P3)*, and add a `job` filter to every query
  (`config/grafana/dashboards/trustagent.json` shows the pattern). The
  provisioned Grafana datasource now has the uid `prometheus`.
- **Before turning on block or fallback,** read the block-readiness checklist
  on the Policies page: a route without a measured calibration for the current
  Jev snapshot, or a passthrough stream, acts as flag until fixed.
- A v1 `.env` may still set `LLM_MODEL` (for example `claude-haiku-4-5`). Under
  `LLM_PROVIDER=auto` it goes to whichever judge auto picks; clear it or set
  `LLM_PROVIDER` explicitly.
- Dashboard writes (saving a policy, Playground runs) need an admin: claim the
  `SETUP: open …` link the proxy prints at first start, or sign in at
  `/app/setup` with a key from `TRUTHTABLE_ADMIN_KEYS`. Keep
  `TRUTHTABLE_DATA_DIR` on a persistent volume (compose does), or sessions end
  at every restart.

### Added
- **The gated `/v1` handler**: OpenAI chat and completions and Anthropic Messages,
  JSON and SSE; passthrough streams send the decision as an SSE comment and HTTP
  trailers, `streaming: buffer` routes can block and fall back on streams,
  fallback answers are gated again before they are sent, and
  `GET /v1/trustagent/decisions/:trace_id` long-polls a decision.
- **Fast check (Jev) backends**: `openrouter-decisions`, `openrouter-systemone`,
  `typesafe` and `compatible` next to `mock` and `off`, chosen automatically; with
  retries that honour Retry-After, hedging, a breaker that counts vendor faults
  only, a rate limiter, a daily spend cap, snapshot tracking and keep-warm.
  `make jev-check` checks a key and endpoint with one probe.
- **Policies and evaluators**: versioned per-route policies (file or dashboard,
  rollback, hot reload across replicas, a free impact preview on stored answers),
  11 built-in checks, a calibration store (isotonic or Platt fits with Wilson
  bounds) and block readiness per route.
- **Evaluator Studio**: draft a check with AI, test it on samples, label answers,
  calibrate, and a paid dry run with an estimate and a confirmation; also on the
  static site through your browser key.
- **Deep-audit engine 2.0**: `Evaluate`, `GetEngineInfo`, `RetrieveContext`,
  `ApplyRuntimeConfig` and `DraftEvaluator` over gRPC; judge auto-resolution
  (`LLM_PROVIDER=auto`) with no silent fallback; UTF-16 claim spans; UNKNOWN and
  NOT_CHECKED verdicts; scoring v2; one cost record per judge call.
- **The dashboard, rebuilt**: Overview, Traffic and trace detail (claims marked in
  the answer, the exact Jev request in the glass box), Policies, Evaluators,
  Studio, Savings, Playground, Settings (keys, models, budgets, prices), Access
  and Knowledge; light and dark themes, phone widths, and the empty, loading,
  error, degraded, demo and read-only states on every page.
- **Keys and settings at runtime**: Connect OpenRouter and Paste a key, a check of
  each key with its provider before use, runtime settings pushed to the engine
  without a restart, and the "Try it without a key" simulated mode.
- **Costs**: a cost ledger of every decision and judge call, daily budgets that
  reserve before a call, and `GET /api/roi/*` and `GET /api/pricing`.
- **`contracts/`**: the cross-language source of truth (JSON Schemas, the
  configuration-key registry, pricing, labelled value claims, UI copy, evaluator
  templates, rules and golden vectors), with a TypeScript decision core that
  passes the same golden vectors as the Go reference.
- **The public site**: a landing page with a cost calculator and a sourced,
  dated comparison table, the replayed demo, Studio and the live Playground in
  the browser (`pages.yml`).
- **Release tooling**: `make version-bump` and `make check-version`, multi-arch
  GHCR images with SBOMs and build provenance (`release-images.yml`), a
  pull-based quick start, `make release-notes TAG=…` (the CHANGELOG section,
  every image digest and the commands that verify it, each measured figure
  with its label; it refuses a missing digest, and notes made from canned
  inspect output say so at the top) and `make record-demo` (a dry run unless
  `CONFIRM=1`).
- **Launch tooling**: `make linkedin-post` prints the launch post from the measured run and the
  recorded tape and exits 1 on any missing value; the launch checks, the release notes and the
  post count a tape as recorded only when the hosts it names are vendor hosts, never
  mock-upstream; `make clip FILE=…` copies text to the Windows
  clipboard from WSL without garbling it; `make compare-review` re-reads every comparison source
  and prints each cell beside its source's own words; `make hygiene-check` (in `make check`) and
  `make gitleaks-history` keep local paths, personal emails and keys out of the repository.
- **Measurement tools**: `make gate-eval`, `make calibrate`, `make bench-lite`
  and `make bench-publish` (each paid run is a dry run with an estimate unless
  `CONFIRM=1`, and publishing refuses mock output), the Tier-0 benchmarks and k6
  gateway-overhead runs.
- **Developer experience**: `make check` runs every CI gate, `make doctor`
  diagnoses a running stack, `make configure WITH_SECRETS=1` asks for keys at
  hidden prompts, the onboarding and e2e stacks run every vendor on
  mock-upstream, and `make docs-links` checks every relative link.
- **Community files**: [CONTRIBUTING.md](CONTRIBUTING.md),
  [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), issue forms and a pull request
  template; security reports go through GitHub's private vulnerability reporting
  ([docs/SECURITY.md](docs/SECURITY.md#reporting)).

### Changed
- **Scoring v2** (ADR-001): per-claim verdict distributions, a trust score that is
  absent (never 0) when it cannot be computed, and `judge_unavailable` when
  UNKNOWN reaches half the checked claims.
- **The knowledge add-on is optional** (`poetry install --extras knowledge`,
  `TRUTHTABLE_KNOWLEDGE_ENABLED=true`); the default engine imports without torch.
- **Configuration** is read through the key registry (`.env`, then the process
  environment), validated with typo hints, and documented in the generated
  [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
- **CI** is split into `ci-go`, `ci-python`, `ci-web`, `ci-docker`,
  `ci-contracts` and `ci-e2e` (job names unchanged for branch protection), and
  every action is pinned to a commit SHA.
- **The HaluEval evaluation workflow (`eval.yml`) runs only when started by
  hand**: it no longer runs weekly, its actions are pinned to commit SHAs, and its
  badge left the README, because its live-model figures carried no label.
- **Pages on a release tag** ships the recorded tape; the repository variable
  `PAGES_TAG_DATASET` = `simulated` ships the simulated one instead, for a release that may not
  publish recorded model output.
- **Toolchains**: Go 1.25, Vite 8, TypeScript 6, React 19.3; the Makefile is
  split into `mk/*.mk` fragments.
- `SubmitAudit` and `GetAuditResult` are deprecated in favour of `Evaluate`.

### Fixed
- **The gateway read its own key from `Authorization`** (B1), the header SDKs fill
  with the upstream key; it reads `X-TrustAgent-Key` first.
- **Test answers on every request** (M7): `test_response` works only in test mode.
- **Every audit was counted twice in Prometheus** (M11): the two services now
  export separate metric names.
- **Duplicate ids corrupted audit history** (B4): ids are server-generated
  UUIDv7.
- **Tier-0 on long documents** was quadratic; 32k tokens of evidence now take
  about 0.4 ms on the dev laptop (measured; GATEWAY.md's Tier-0 table).
- **A key or credit pause read as a fault pause**, so Settings → Test could not
  lift it; the pause keeps its cause.
- Many smaller fixes to wording, phone layouts and the simulated demo's Savings
  figures are listed under *Build log by phase*.

### Security
- **Access control**: scopes ingest, read, content and admin; scoped API keys and
  signed dashboard sessions with CSRF protection and sliding expiry; single-use
  WebSocket tickets; a one-time setup link instead of a default admin key; an
  append-only admin audit log that refuses UPDATE, DELETE and TRUNCATE.
- **Keys at rest and in flight**: dashboard keys are sealed with AES-256-GCM under
  the master key, bound to their name, provider and allowed origins; every
  outbound call checks the destination origin; link-local and cloud-metadata
  addresses are refused at dial time, after DNS.
- **Redaction** of every received or decrypted key in Go logs and errors, gRPC and
  WebSocket status details, relayed upstream error bodies and the engine's logs.
- **Safer defaults**: ports bind to `127.0.0.1`, `/v1` needs an app key, test
  mode is off, CORS allows exactly the request headers the gateway reads, and the
  engine refuses a non-loopback bind without its internal token.
- **Supply chain**: workflows pinned to commit SHAs and linted (actionlint,
  zizmor), gitleaks over the whole history, gosec, bandit and npm audit in CI, and
  images with SBOMs and build provenance attestations.

### Known limits
- **The fast check needs a decision-model key** (OpenRouter or TypeSafe). Without
  one the gateway runs the Tier-0 detectors only; `make demo` uses a mock,
  labelled Simulated.
- **No evidence, no groundedness verdict.** A request that carries no documents,
  context or tool results gets *not assessed* for groundedness, by design.
- **Blocking needs a calibration** for the current Jev snapshot. Until a route has
  one, and whenever the snapshot changes, block and fallback act as flag and say
  why (`calibration_missing`, `uncalibrated_snapshot`).
- **Passthrough streams cannot be blocked**: they are flagged in the stream's
  terminal event; use `streaming: buffer` on a route that must block streams.
  `n > 1` gates choice 0 only, and tool-call-only answers are not enforced.
- **The deep audit needs a judge.** With `LLM_FALLBACK=none` (the default) an
  audit fails with `judge_unavailable` while the judge is down; nothing is
  swapped in silently.
- **Several proxy replicas** need sticky sessions for Connect OpenRouter, and each
  replica enforces the daily budgets on its own between 60 s resyncs.
- **Figures from others**: the comparison table and the vendor and third-party
  figures are as of the date beside each one; measured figures are only those
  listed with this release's measured run.

### Build log by phase

The work as it landed, phase by phase; the sections above sum it up.

#### Breaking changes (P4)
- **CORS allows exactly the SPEC request headers (D43).** A cross-origin browser
  may send `Content-Type`, `If-Match`, `X-TrustAgent-Key`, `X-TrustAgent-CSRF`,
  `X-TrustAgent-Route`, `X-TrustAgent-Mode` and `X-Request-ID`; `Authorization`
  and `X-API-Key` are no longer allowed cross-origin, so such a preflight fails.
  Browser clients send their key as `X-TrustAgent-Key`. SDK and server clients
  are unaffected: the gateway still reads `Authorization: Bearer` and
  `X-API-Key` on `/api` (and on `/v1` in inject mode).
- **Upstream keys are environment or credential store only (D35).**
  `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` and
  `TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY` are not runtime settings; the
  dashboard keeps such keys as the encrypted `upstream-anthropic` and
  `upstream-fallback` credentials. No secret registry key may be runtime
  settable (`contracts/validate.mjs` refuses it). New compose key
  `MOCK_UPSTREAM_HOST_PORT` (default 18090).
- **Credential writes return `CredentialWriteResult` (D75).**
  `PUT /api/credentials/:name`, `POST /api/credentials/:name/verify` and
  `POST /api/connect/openrouter/complete` answer
  `{credential, stored, bindings: {applied, skipped}, verification}` (complete
  adds `return_to`) instead of a bare `CredentialView`. Bindings apply only when
  the key's check passes; a failed check never replaces a key in use.
- **A new calibration cap reason, `calibration_missing` (D56).** A block
  capped at flag because a block-set check has no usable, adopted calibration
  for the current snapshot now reports `calibration_missing`;
  `uncalibrated_snapshot` is kept for checks calibrated only on other
  snapshots (the decision model changed). Clients and alerts that match
  `uncalibrated_snapshot` to find every calibration cap should match both.
  A calibration fitted with different state fields still applies and sets the
  evaluator result's `calibration_warning`.
- **The engine implements `ApplyRuntimeConfig` and `DraftEvaluator`.** The
  gateway pushes its runtime settings (judge, the judge's key, prices) as
  `(epoch, version)`, and the Studio drafts through the engine. An engine from
  before this phase answers `UNIMPLEMENTED`: the gateway logs one warning and
  retries, but the dashboard judge and Studio drafts need both upgraded.

#### Breaking changes (P3)
- **Metric renames (M11).** Both services exported the same `trustagent_*`
  names, so Prometheus counted every audit twice. The engine's metrics are now
  `trustagent_engine_*` and the gateway's `trustagent_gateway_*`, and every
  query of the provisioned Grafana dashboard filters by `job="proxy"` or
  `job="engine"`. Update your dashboards and alerts:

  | 1.0 name (exported by) | 2.0 name |
  |---|---|
  | `trustagent_audits_total{status}` (engine) | `trustagent_engine_audits_total{status}`; `status` adds `degraded` |
  | `trustagent_audits_total{status}` (proxy) | `trustagent_gateway_audit_jobs_total{lane,selection,outcome}` |
  | `trustagent_audit_duration_seconds` (engine) | `trustagent_engine_audit_duration_seconds` |
  | `trustagent_audit_duration_seconds` (proxy) | `trustagent_gateway_audit_roundtrip_seconds` (queue to stored result) |
  | `trustagent_faithfulness_score` (both) | `trustagent_engine_faithfulness_score` |
  | `trustagent_hallucinations_detected_total` (both) | `trustagent_engine_hallucinations_detected_total` |
  | `trustagent_active_audits` (engine) | `trustagent_engine_active_audits` |
  | `trustagent_active_audits` (proxy) | `trustagent_gateway_audit_queue_depth{lane}` |
  | `trustagent_claims_total{status}` (both) | `trustagent_engine_claims_total{verdict}` (label renamed) |
  | `trustagent_websocket_clients` (proxy) | `trustagent_gateway_ws_clients` |

  New engine series: `stage_duration_seconds{stage}`,
  `judge_requests_total{provider,outcome}`, `judge_tokens_total{provider,direction}`,
  `judge_cost_usd_total{provider}`, `judge_fallback_active`, `rpc_inflight{method}`,
  `gate_vs_audit_total{gate_action,audit_verdict}`, `kb_ingest_claims_total{status}`.
  The gateway's full set is in SPEC Appendix B and [docs/GATEWAY.md](docs/GATEWAY.md#14-metrics).
- **WebSocket v1 is removed (D13).** A `GET /ws` handshake without `protocol=2`
  answers 400 `bad_request` before the upgrade ("WS v1 was removed; connect
  with ?protocol=2"). The `audit_result` frames and the 1.0 `/api/audits`
  response shape are gone; clients read the v2 events (`trace_started`,
  `gate_decision`, `audit_completed`, ...) and `GET /api/traces`.
- **`POST /api/upload` is removed.** Upload knowledge documents with
  `POST /api/kb/documents` (admin scope; the knowledge add-on must be on).
- **`/v1` enforces.** Block and fallback routes now change what your client
  receives (block bodies, fallback answers, see
  [docs/GATEWAY.md](docs/GATEWAY.md)); P2 only observed and flagged.
  `/v1/messages` (Anthropic) and `/v1/completions` are gated like chat
  completions, and TrustAgent's own errors on `/v1` come in the route's API
  shape (Anthropic routes answer in Anthropic's error format).
- **`test_mode_disabled` is 400** on `/v1` (a `test_response` sent without
  `TRUTHTABLE_TEST_MODE`), now in the route's error shape. SPEC §2.4 listed it
  under 403; that list is for `/api`, and the SPEC is corrected (D8).
- **trafficgen:** the default phase is `p3`, and `--expect` exits 1 when a
  selected scenario is skipped because of its phase unless `--allow-skip` is
  given, so a check can no longer pass without testing anything.

#### Added (P0 to P3: contracts, walking skeleton, gateway)
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
- **Configuration loader**: every proxy setting is read through the key registry
  (`.env` then process env, aliases and shorthands, validation with typo hints,
  secrets masked in logs and gin's panic dumps). `proxy -check-config`,
  `proxy -dump-config-spec`, and `TRUTHTABLE_STRICT_CONFIG=true` (exit 2 on any
  warning).
- **Access control**: scopes ingest / read / content / admin; env keys,
  `api_keys` rows and dashboard sessions (signed cookie, CSRF, sliding expiry,
  revoke-all); single-use WebSocket tickets; a one-time setup token printed at
  start as `SETUP: open …/app/setup#token=…` (`make setup-url` prints a new one)
  that creates the first admin key; `GET/POST/DELETE /api/keys`. The dashboard's
  new sign-in page claims the setup link or signs in with a key.
- **Policies and evaluators**: versioned policies (file, dashboard, rollback,
  adopt-file) with validation, block readiness, hot reload across replicas
  (NOTIFY plus polling), `TRUTHTABLE_CONFIG_LOCK`, a diverged-file state and a
  free "Preview impact" replay of stored answers; the evaluator registry
  (built-in, file and stored versions); `/api/policies*` and `/api/evaluators*`.
  The gate reads the policy in effect on every request, so a saved or reloaded
  version applies without a restart.
- **Overview numbers**: `GET /api/overview`, `/api/roi/summary` and
  `/api/evidence/coverage`, counted from stored traces, decisions, audits and
  cost rows; every figure says whether it is measured, an estimate or simulated,
  and mock traffic is left out unless asked for (the demo includes it).
- **LLM API library** (`internal/llmapi`): OpenAI chat and completions and
  Anthropic messages, tolerant parsing, an SSE reader and writer with held-back
  terminal events and annotations, block and fallback bodies and synthesized
  streams. The /v1 handler adopts it in the P3 handler work.
- **Measurement tools**: `make calibrate` (isotonic or Platt fits with Wilson
  bounds, `trustagent.calibration/v1` files), `make gate-eval` (the fast check
  over a JSONL file, with a hard cost cap), `make export-replay` (sanitized
  demo recordings), and the eval harness v2 (`make bench-lite`,
  `make bench-publish`, `make eval-live`: RAGTruth and LLM-AggreFact loaders,
  judges including the gateway itself, cost caps, a benchmark report).
- **Engine**: `RetrieveContext` for the knowledge add-on; per-claim evidence
  context (`TRUTHTABLE_VERIFY_CONTEXT_SCOPE`).
- **Dashboard pages**: Overview (setup checklist, outcomes over time, what was
  caught, evidence coverage with "Why not?", the cost strip), Policies (route
  cards, presets, a YAML editor with schema checks, review with diff and preview
  impact, versions and rollback), Playground, Evaluators (library, detail,
  reliability) and Knowledge.

- **P3 gateway** ([docs/GATEWAY.md](docs/GATEWAY.md)): one gated handler for
  OpenAI chat, completions and Anthropic Messages, JSON and SSE. Passthrough
  streams hold the terminal event for the decision and send it as an SSE
  comment and HTTP trailers; `streaming: buffer` routes can block and fall
  back on streams; `GET /v1/trustagent/decisions/:trace_id` long-polls a
  decision. Fallback re-gates the second answer before sending it. Upstream
  failures, tool-call-only answers and late decisions are never enforced;
  `n > 1` gates choice 0. Inject mode is decided per upstream (OpenAI,
  Anthropic, fallback). Calibration files in `TRUTHTABLE_CALIBRATION_DIR` are
  re-read every 30 s (WS `calibration_updated`).
- **P3 REST:** `GET /health/ready`, `/api/gate/decisions`,
  `/api/gate/reliability`, `/api/pricing`, `/api/export/replay` (admin) and
  `POST /api/playground/run` (admin).
- **Developer experience (P3):** `make demo` and `make up` open the one-time
  setup link (or the dashboard when an admin exists), `make demo-setup-url`
  prints it alone and `make setup-url` finds the running stack; `make check`
  runs every CI gate (superseded identifiers, generated types, the api.v1
  schema, `tsc -b`, both web builds, the size budget, the recording);
  `make doctor` checks setup, the fast check (backend, reason, snapshot,
  latency, cost), calibration, policy and route downgrades, and both metrics
  endpoints; `npx playwright test --project=static` serves the static site
  itself; ci-e2e runs the M-Demo gate and the e2e stack's p3 scenarios;
  `make load-test-gate` measures the gateway's overhead with k6 against
  mock-upstream. New [docs/GATEWAY.md](docs/GATEWAY.md) and
  [docs/EVALUATORS.md](docs/EVALUATORS.md); the README and
  [docs/DEMO.md](docs/DEMO.md) describe 2.0.

#### Added (P4: security, credentials, Studio, ROI)
- **Keys in the dashboard.** **Connect OpenRouter** (self-hosted PKCE: the
  verifier and the key never reach the browser) and **Paste a key** for
  OpenRouter, TypeSafe, Anthropic, OpenAI, a compatible gateway and the
  upstreams. Keys are sealed with AES-256-GCM under the master key, with the
  name, provider and allowed origins as authenticated data; every outbound call
  checks the destination origin first. Each key is checked with its provider
  before use (OpenRouter: key lookup plus one probe; a 402 stores it unbound with
  "no credits"). A non-empty environment value always wins and is read-only in
  the dashboard. Endpoints: `GET /api/credentials`,
  `PUT|DELETE /api/credentials/:name` (deleting a key that fail-closed routes
  depend on needs `?confirm=true`), `POST /api/credentials/:name/verify`,
  `POST /api/connect/openrouter/start|complete`, `POST /api/decision-model/test`.
- **Runtime settings.** `GET /api/settings` and `PATCH /api/settings`
  (`If-Match: "s<version>"`; 428 without it, 412 when stale, 409 `env_managed`):
  the decision model, the judge, upstreams, bindings, budgets and price
  overrides apply without a restart and are pushed to the engine. **Try it
  without a key (simulated checks, $0)** switches both checks to mocks, labelled
  Simulated everywhere, and turns off by itself when a key is bound.
- **Access.** `GET|POST /api/keys`, `DELETE /api/keys/:id` (the last admin key
  cannot be revoked), `DELETE /api/session?all=true` (sign out everywhere), and
  the append-only admin audit log (`GET /api/admin/audit-log`) of setup,
  sign-ins, keys, settings, credentials, evaluators, calibrations, Studio
  dry-runs, uploads and exports. Redaction covers every received or decrypted
  key in Go logs and errors, gRPC and WS status details, upstream error bodies
  relayed in inject mode, and every root log handler of the engine.
- **Costs and budgets.** A cost ledger records every decision call (hedge losers
  included) and every judge call, linked to its trace or audit, with its source
  (reported, estimated or simulated; simulated never counts). The daily budgets
  reserve before a call and settle after it; the deep-audit budget is hard in
  every lane (`POST /api/audit` answers 503 `budget_exhausted`, sampled audits
  are dropped with `audit_dropped{reason: llm_budget}`). Savings and ROI:
  `GET /api/roi/summary`, `GET /api/roi/timeseries`; `GET /api/pricing` serves
  the effective table (overrides, then `TRUTHTABLE_PRICE_TABLE_FILE`, then the
  contracts). Metrics `spend_reserved_usd{kind}` and `cost_write_dropped_total`.
- **Evaluator Studio and calibration.** `POST /api/studio/draft|test|samples|calibrate`
  and the paid `POST /api/studio/dry-run` (SSE, with an estimate and a
  confirmation above `TRUTHTABLE_STUDIO_CONFIRM_ABOVE_USD`); evaluator writes
  (`POST /api/evaluators`, `/api/evaluators/:id/versions`, `/archive`) validated
  by the Go linter, the reference for the TypeScript one, which
  `POST /api/evaluators/validate` runs without saving; labels
  (`PUT|DELETE /api/labels`, `GET /api/labels/queue`); calibrations stored in
  the database (`GET|POST /api/calibration`, `GET /api/calibration/:id`,
  `POST /api/calibration/fit`, `/:id/activate`, `/:id/deactivate`) and the
  adopt flow, with
  `calibration_ids` in `POST /api/policies/dry-run` for a free preview.
  `GET /api/traces/:id/decision-request` recompiles the exact Jev request (the
  glass box); `GET /api/evidence/suggestions` proposes evidence patterns.
- **WebSocket events:** `credentials_changed {name, action, fingerprint}`
  (the fingerprint only, never the key), `settings_updated {version, keys}`,
  `evaluator_updated` on an archive change, and `status {component: "budget"}`
  naming the cap (`decision`, `llm` or both).
- **Gateway (P3 leftovers).** Traces are written in ordered batches by default
  (`TRUTHTABLE_TRACE_FLUSH=async`; a 2,000-trace memory copy serves reads
  meanwhile); knowledge retrieval runs alongside the upstream call within
  `TRUTHTABLE_KNOWLEDGE_DEADLINE`; `/v1/models` goes to the Anthropic upstream
  for Anthropic clients; the Playground streams chat answers from the upstream;
  `GET /api/traces?reason=` filters by gate reason.
- **`/api/status`** adds the credential store's state and undecryptable keys,
  real spend against both budgets, the settings version and epoch the engine
  applied, and the warnings `master_key_changed` and `fail_closed_unprotected`.
- **Migration 0005**: cost-event links (`audit_id`, `hedge`, `late`,
  `estimated`), `traces.response_chars`, unique file calibrations, and the
  append-only trigger on `admin_audit_log`. **Migration 0006** makes that log
  refuse `TRUNCATE` as well.
- **Savings on the simulated demo:** deep audits from the free mock judge
  ($0, no tokens) no longer scale up to a $0 "LLM auditing every answer"
  figure: the full-audit counterfactual falls back to the published assumption
  (≈ $35.50 per 1,000, labelled not measured), and the deep-audit basis says the
  mock judge is free. The mock fast check also answers the Studio drafter's own
  samples by their `should_fire`, so the demo's test bench shows the intended
  matches instead of hash answers. Under the headline, the alternatives stand
  side by side in its unit, per 1,000 answers, each with its value label and
  on one basis: on a stack with a real judge, this window's LLM asking the
  same questions and LLM auditing every answer claim by claim; on the demo's
  free mock judge, the published estimates only, "With a real judge":
  TrustAgent at the shipped defaults (about $3, with about 8 in 100 answers
  deep-audited), an LLM asking the same questions ($5.75) and a full audit of
  every answer ($35.50). A line below says what the demo's own (much higher)
  deep-audit rate would add. The published full-audit figure is labelled
  Estimate even on a simulated stack (`/api/roi/summary` keeps its label), the chart's sentence states the
  summary's totals (the spend series polls with it), and the static demo prices
  the same full-audit counterfactual, draws its spend per minute, words every
  span "over the replayed traffic" and says sampling status is not part of the
  recording. Today's spend in Budgets carries an Estimate label.
- **Label queue words:** the queue holds answers the check flagged and answers
  it let through, so it asks "Should this check flag this answer?" with
  **Yes, it has the problem** (Y or C, `should_fire`) and **No, it is fine** (N
  or F, `should_not_fire`). Calibration counts read "with the problem" and
  "fine"; "Was this right?" on a flagged trace keeps Correct and False alarm.
  Library cards keep the label queue and link to Calibrate on the check's
  page, and every built-in check has a "Flags answers that …" line (contract
  copy for the templates without a description).
- **Key check words:** a passed key check says what its test call measured
  ("Ready: the test call took 231 ms and cost $0.00001", the time labelled
  Measured and the cost with its own value label), and the Savings footnote
  labels the price it uses Vendor, as the Prices table does (a price override
  reads "Your price"). An OpenRouter key that does not run the judge because
  .env pins it says so on its Connections card, with the way to Settings →
  Models.
- **Policies and Studio words:** block readiness lists the Block mode last and
  stays neutral on a Flag or Observe route; Studio's Simple tab reads the
  question back with readable names ("your model's answer"), and a dry run
  shows identical answers once with "×N".
- **`make phase-refs-check`** (in `make check` and ci-contracts) fails on a code
  or comment line that names the delivery phase it shipped in, including a name
  that runs on into an identifier (`P4_…`, `isP4…`), except the lines in
  `scripts/phase-refs.allow`.
- **Developer experience (P4):** `make configure WITH_SECRETS=1` asks for the
  vendor keys at hidden prompts (never echoed, `.env` mode 0600);
  `make onboarding-up` / `onboarding-setup-url` / `onboarding-down` run a fresh
  install (no admin, no keys) against mock-upstream's OpenRouter, which CI walks
  with the onboarding spec; `make doctor` adds the credential store, the
  engine's settings version, the judge's key source and today's budgets;
  Grafana gains a Spend row; nginx never logs `/connect/callback`; gitleaks scans
  the whole history in CI (`.gitleaks.toml`; the fake-key conventions are
  allowed in test files only); `make gen-evaluator-validator`;
  images carry their git revision (`GIT_SHA`): the proxy reports it as
  `build_sha` and `/api/export/replay` names it, and every image has it as
  `org.opencontainers.image.revision`. [docs/SECURITY.md](docs/SECURITY.md) and
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) are rewritten for 2.0;
  [docs/JEV-ACCESS.md](docs/JEV-ACCESS.md), [docs/GATEWAY.md](docs/GATEWAY.md),
  [docs/EVALUATORS.md](docs/EVALUATORS.md) and the getting-started and demo
  guides cover the new pages.
- **Integration (P4):** `GET /api/session` names the key a session was opened
  with (`key_id`), so Access marks exactly one key "This browser";
  `TraceDetail.labels[].mine` says which labels are yours (only those can be
  undone); `GET /api/overview` counts `would_block` apart from `would_flag`, and
  the Overview chart draws both; a deep audit a mock judge gave flags its trace
  `mock_judge` (Simulated, and out of the measured figures); Benchmarks renders
  in the static build; a cross-origin dashboard's live stream uses a WS ticket.

#### Added (P5: static site, landing, release tooling)
- **Try it live in your browser.** The static site's Playground gains a `#live`
  section below the replayed examples: the three showcase scenarios (a grounded
  answer, the refund contradiction, a PII leak) or your own text, checked by Jev
  with your own OpenRouter key. Connect (PKCE from the browser, through the static
  `/connect/callback/` page) or paste a key; it stays in that tab's
  `sessionStorage` for at most 2 hours (Forget key clears it) and is sent only to
  openrouter.ai. Courtesy caps (`VITE_PLAYGROUND_MAX_CALLS`,
  `VITE_PLAYGROUND_MAX_TOKENS`), a running cost, a dedicated screen for 402 (no
  credits), and the same decision code as the gateway, pinned by the golden
  vectors (PKCE vectors included).
- **Evaluator Studio on the static site.** Templates, the editor, a test bench on
  the browser Jev client, the free threshold what-if, quick calibration on labels
  made in the page, Draft with AI through the browser key, and Copy JSON /
  Download .json in place of Save; watching, dry-runs and activation point to
  your own stack.
- **Landing page.** A cost calculator pinned by the `cost` vectors, the comparison
  table (each vendor cell dated and sourced, "Not yet verified" until the owner
  reviews it), Try it, Write your own checks, "What Jev was asked" (the exact
  request, rebuilt from the refund example), and self-hosting.
- **Replay.** Simulated and recorded datasets (`VITE_DEMO_DATASET` `mock`,
  `recorded` or `auto`, from `frontend-react/demo-data/`), a richer simulated
  tape, the glass box rebuilt in the browser for replayed traces, the "scripted
  showcase mix" caveat, a first-visit guide, and the `/og` social card (built only
  for capture).
- **Gateway tools.** The vendor-fault scenario pack (`make e2e-faults`),
  `trafficgen --record`, the knowledge scenario run in process, published k6
  overhead runs (`docs/benchmarks/load/`), Tier-0 benchmarks (`make bench-go`,
  `make bench-go-smoke`, and `make bench-tier0-publish`, which keeps a run with its
  commit in `docs/benchmarks/tier0/` and writes the measured figures into
  GATEWAY.md) and `make jev-check`.
- **Link check.** `make docs-links` (`scripts/check_links.py`, part of `make check`
  and ci-contracts) checks every relative link and heading anchor of the README and
  docs.
- **Release tooling.** `scripts/bump_version.py` and `check_version.py` keep the
  version and every mirror in step; multi-arch GHCR images with provenance, SBOMs
  and build attestations (`release-images.yml`); the pull-based quickstart;
  `pages.yml`, which builds on every push and deploys only once the owner turns
  Pages on; `eval-live.yml`; workflows pinned to commit SHAs and linted
  (`make lint-workflows`); `make record-demo` (a dry run unless `CONFIRM=1`);
  `doctor.py` checks the environment, images and add-ons, with an opt-in
  `--probe`; `make first-run` and the golden-path test.
- **Honest-numbers check.** `scripts/check-claims.mjs` (`make check-claims`)
  holds every `<!-- claim:ID -->` figure in the README and docs to
  `contracts/claims/value-claims.v1.json`: its display, its label chip, its caveat
  next to it, and measured values only from a measured run. `--write` regenerates
  the measured-results tables; `--launch` refuses a simulated recording, an
  unmeasured run, an unreviewed comparison cell or a missing social card.
- **Docs and launch kit.** A README that says what TrustAgent is in a minute
  (the live demo, three labelled value bullets, the tier diagram, the
  measured-results table, a pull-based quick start); the walkthrough rewritten
  for 2.0; GETTING-STARTED and DEMO for the browser demo and build or pull;
  SECURITY on the static demo and the supply chain; a release runbook in CI-CD
  (with `gh attestation verify`); the Tier-0 section and the load-run table in
  GATEWAY. ADR-005 to ADR-010, phases 8–15 in [docs/PLAN.md](docs/PLAN.md), and
  launch drafts in [docs/launch/](docs/launch/) (social card spec, 60-second
  demo script, GitHub metadata, a LinkedIn post with its figures left as
  placeholders until measured, interview talking points, the launch checklist).
- **Capture tooling.** `make og` (the 1280×640 social card), `make hero-gif`
  (the README GIF, at most 8 MB, and the release MP4, through
  `scripts/make_hero_gif.py`) and `make screenshots` (every static page in both
  themes), as the Playwright `launch` project. The card and the hero refuse a
  simulated tape; the screenshots carry its Simulated label.

#### Changed (P0 to P5)
- **Security (P4 integration):** link-local and cloud-metadata addresses are
  refused at dial time, after DNS, by the upstream proxy, the decision-model
  client and key checks, and the OpenRouter key exchange; inject mode masks the
  gateway's key in upstream response headers as well as bodies; WS status
  details pass through the redactor; a vendor message cut across an echoed key
  leaves no prefix of it.
- **Costs (P4 integration):** an LLM call no price covers counts toward the
  daily LLM cap at the reference judge's price (D91); `/api/status`
  `decision_model.calls_24h` and `cost_24h_usd` come from the cost ledger and
  survive a restart (D92).
- **Settings (P4 integration):** a bind that moves the fast-check backend or the
  judge to auto also resets that model to the new provider's default (D94);
  binding an upstream key for every caller needs an app key (the `ingest`
  scope itself; an admin key does not count, D95).
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
- Registry defaults now apply where the walking skeleton hard-coded others:
  `TRUTHTABLE_WRITE_TIMEOUT=0` (per-route write deadlines instead of one server
  timeout: 60 s for `/api`, the upstream timeout + 30 s for `/v1`, the ingest
  call's bound + 30 s for the knowledge upload) and
  `TRUTHTABLE_SHUTDOWN_TIMEOUT=30s`.
- Rate limits are counted per key, or per session for dashboard sign-ins, before
  falling back to the client IP.
- Knowledge-base endpoints answer 409 `knowledge_disabled` while the add-on is
  off; `POST /api/kb/documents` (admin) is the upload's v2 path.

#### Fixed (P0 to P5)
- **The landing's comparison credited one vendor's docs to a whole row**: the
  observability row named Langfuse, Arize and Datadog while every cell cited
  Langfuse, and silent docs read "Varies". Each cell now names the product whose
  docs it cites ("Yes · Langfuse"), a silent doc reads "Not documented", a row names
  only the products its cells cite, and `contracts/validate.mjs` enforces all three
  (`compare.v1` gains `vendor` and `not_documented`). Every cell still awaits the
  owner's review before launch.
- **Code on a phone**: the landing's policies.yaml, SDK line and no-make command
  wrapped into broken YAML and shell at 400 px; code now keeps its lines and
  scrolls sideways inside a named, focusable region.
- **A replay's hallucination rate drew a 0-of-30 meter** under the sentence saying
  a replay cannot have one, and a mock fast-check time was the Overview's headline:
  the meter is gone in a replay, and a simulated p50 / p95 shows a dash with the
  mock's milliseconds in the note.
- **The Overview's 24-hour axis** switched from whole hours to ":15" labels and cut
  its last label at the edge; it now labels one round step of the clock.
- **The Pages artifact shipped the build manifests** (`dist/.vite`): `make
  pages-size` drops them after the size check, and `make pages-files` refuses them.
- **Tier-0 on long documents**: the number and URL scans over the request's
  documents were quadratic per document (Go's regexp backtracker clears a visited
  set as long as the text on every call), so 32k tokens of evidence took about 9 ms.
  They now scan short runs, read the documents only when the answer has something
  to check against them, and the shipped number pattern has a hand-written scanner,
  with the same findings: about 0.4 ms at 32k tokens on the dev laptop, inside the
  "< 2 ms" budget (see GATEWAY.md's Tier-0 table).
- **A key or credit pause named as a fault pause**: a success already in flight
  (the startup probe, a slower answer) that landed after a 401 or 402 turned the
  decision model's status from `unauthorized` / `no_credits` into `breaker_open`,
  so Settings → Test could not lift the pause. The pause keeps its cause.
- **The landing's refund example cost**: the hero card and "What Jev was asked"
  showed $0.0002 next to the request's 565 tokens; at the Jev price that request
  costs about $0.000024 (now its labelled estimate, checked against the tokens), and
  every claim's caveat shows next to its chip.
- **A judge bound behind the engine's back right after a start**: until the
  engine's first health report the gateway did not know that the engine's env
  (`LLM_PROVIDER`) sets the judge, so a Connect OpenRouter in the first seconds
  bound the judge and said so. A write that binds the judge now asks a
  not-yet-reported engine once first, and skips the judge as env-managed.
- **Phone widths**: Studio's example buttons, a check's page header and the
  "How well its risk matches your labels" card no longer widen the page at
  400 px; a route's block progress has its own line.
- **Savings on the free-mock-judge demo**: the chart no longer sets the demo's
  spend (deep audits at $0, on far more answers than the shipped defaults)
  against the published full-audit estimate. Its Full audit option is disabled
  with the reason, and the table's full-audit row says to compare it with
  TrustAgent with a real judge (about $3 per 1,000 answers, shown as the fast
  check $0.21 plus a deep audit of about 8 in 100 answers). A line says why the
  published figures, which assume a 5,000-token request, are above the demo's
  simulated ones. The published full audit stays an Estimate over simulated
  traffic (D97).
- **A corroboration warning that gave wrong advice**: the policy check warned
  that a check "needs Contradiction to agree" when any one of its listed
  corroborators is enough, and the number and date checks could still confirm
  it. It now warns only on a Block or Fallback route where none of them can
  run; the evaluator lint does the same (D98). The shipped policies have no
  such warning.
- **Calibrate now**: its reason no longer says every deep audit counts. Only
  deep audits of the uniform sample become labels, and the reason says how
  many there are so far.
- **`/api/status` right after traffic**: the decision model's 24-hour calls and
  cost are re-read when older than 5 s, so the first status read after traffic
  shows it rather than the boot read (D92).
- **Wording**: the demo banner names the published estimates as the
  exception to "simulated"; block readiness says how to calibrate and links the
  label queue; the label queue's key hint shows the buttons' keys; Setup calls
  the one-time link a link and points a `.env` judge to Settings → Models; the
  Prices table words its catch-all pattern; a firing Studio test row shows a
  flag; the top bar is solid, and a command in a banner never breaks across
  lines.
- **The demo's setup wizard no longer dead-ends** (`make demo` opens it): step
  2 says the demo already runs simulated checks and nothing needs connecting,
  Next opens step 3, and its check runs like the Playground's, so a visitor
  goes from the setup link to a caught wrong answer and on to the dashboard
  (a vitest and the `demo setup` stack spec walk it).
- **The real-judge figure states its unit**: "a fast check on every answer
  ($0.21) + a deep audit of about 8 in 100 answers (about $2.80)" per 1,000
  answers, where it read as 21 cents per answer; one Estimate line sums the
  published figures up (about 12x less than an LLM auditing every answer).
- **Fix round 5 polish**: Savings groups the alternatives under their own
  heading and labels the sampling counts; Policies says why a calibration
  cannot block by its state; one verb per calibration action, and short
  library-card steps; Studio's steps are numbered circles, its dry run names
  the thresholds its risk words follow, and the mock fast check no longer
  fires a drafted competitor check at random; the label queue's cards line
  up; the glass box shows documents as excerpts; Settings tabs stay on one
  row and an assumed price is an Estimate; only `GET /api/status` re-reads
  the decision day (the status loop, readiness and the WebSocket hello read
  the cache); the dial guard also refuses Alibaba Cloud metadata and judges
  the target behind an HTTP(S) proxy.
- **B4 — duplicate ids corrupted audit history**: trace/audit ids are now
  server-generated UUIDv7; a client `X-Request-ID` is stored only as a
  non-unique `client_request_id`, and a repeated id can no longer append claims
  to an existing audit.

#### Post-1.0 hardening and audit-quality pass

Foundation work after 1.0 that made the system demo-ready and accurate before the
2.0 work began; it was never released on its own (see [docs/PLAN.md](docs/PLAN.md)).

##### Changed
- **Audit judge now defaults to Anthropic Claude Haiku** (`claude-haiku-4-5`)
  with automatic fallback to local Ollama (`llama3.2:1b`) when no
  `ANTHROPIC_API_KEY` is set — great quality with a key, free out of the box.
- **Verifier runs claims with bounded concurrency** (`asyncio.Semaphore`)
  instead of sequentially; default gRPC timeout raised to 120s for slow judges.
- **Unified Prometheus namespace** to `trustagent_*` across both services.

##### Added
- **CONTRADICTED verdict is live end-to-end** (proto → Python enum/prompt/scorer
  → Go maps → dashboard badge), distinct from UNSUPPORTED; deterministic scorer
  tests lock it in.
- `tests/e2e/` pytest package (opt-in `e2e` marker) replacing the loose root
  scripts; `docs/CI-CD.md`; `docs/README.md` docs index; single-connection
  `WebSocketProvider`; auth-aware `api/client.ts`; real `/health`-driven status.

##### Fixed
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
