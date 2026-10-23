# CLAUDE.md

Guidance for AI-assisted development in this repository.

## What this project is

TrustAgent (internal package name: `truthtable`) is an open-source **hallucination gateway** for
LLM apps. Clients point their OpenAI/Anthropic SDK at a Go proxy. Each answer is checked in tiers:
Tier-0 deterministic detectors (Go, 2 ms budget), Tier-1 **Fast check (Jev)** — TypeSafe AI's decision
model, all route evaluators as questions in ONE call behind a vendor-neutral `DecisionModel` — then
a per-route policy (observe / flag / block / fallback). Tier-2 **Deep audit** runs asynchronously in
a Python LangGraph engine (decompose → optional knowledge retrieve → verify claims → score) and
streams to a React dashboard over WebSocket v2.

- **Source of truth for v2**: [docs/v2/SPEC.md](docs/v2/SPEC.md) (design notes and critiques in
  `docs/v2/design-notes/`). Follow its phase order (§4). **Never implement an identifier listed in
  SPEC Appendix A** — `scripts/check-forbidden.mjs` enforces `contracts/superseded.v1.json` in CI.
- **Evidence is pluggable; no RAG by default.** Groundedness is checked against what the request
  already carries (system-prompt documents, user-message context, tool results, the
  `trustagent.context` extension). With no evidence the result is `not_assessed` and Jev is never
  asked a groundedness question (its scores invert without evidence). Qdrant/embeddings are the
  optional `knowledge` compose profile; Ollama is the optional `local-llm` profile.
- **Naming**: user-facing surfaces say **TrustAgent**; the Go module, Python package, `TRUTHTABLE_`
  env prefix, gRPC package and compose project stay `truthtable`.
- Docs index: [docs/README.md](docs/README.md); owner guide for Jev access:
  [docs/JEV-ACCESS.md](docs/JEV-ACCESS.md); historical logs in [docs/archive/](docs/archive/).

## Layout

| Path | What |
|---|---|
| `backend-go/` | Go 1.26 gateway (gin): `internal/{gate,decision,jev,evidence,policy,llmapi,upstream,authz,store,websocket,worker,grpc,metrics}`, `cmd/{proxy,trafficgen,mock-upstream,export-replay,gate-eval,calibrate,jevcheck,vectorgen}` |
| `backend-python/` | Python 3.11 audit engine (Poetry): LangGraph graph, providers, scoring, KB (optional), gRPC server (`Evaluate`) |
| `frontend-react/` | React 19.3 + TypeScript 6 + Vite 8 + TanStack Router/Query + Tailwind 4 dashboard; house rules in `frontend-react/DESIGN.md` |
| `contracts/` | Cross-language source of truth: JSON Schemas, config-key registry (`config/keys.v1.json`), evaluator templates, question sets, rules, demo scenarios, UI copy/glossary, pricing, value claims, golden vectors |
| `proto/evaluator.proto` | Go↔Python gRPC contract (v2, additive) |
| `docker-compose.yml` (+ `.demo.yml`, `.e2e.yml`, `.onboarding.yml`, `.load*.yml`) | Default set: proxy, engine, postgres, dashboard. Profiles: `knowledge`, `local-llm`, `observability`, `redis`, `demo`, `e2e`, `load`. No fixed container names: `docker compose exec <service>` (engine service is `engine`). make passes `GIT_SHA` as a build arg |
| `Makefile`, `mk/*.mk` | Thin root Makefile including one fragment per stream (`go`, `py`, `web`, `dx`, `contracts`, `load`, `release`, `launch`) |
| `.github/workflows/` | `ci-{go,python,web,contracts,docker,e2e}.yml` (job names are branch-protection checks: keep them stable), `security.yml` (gosec, bandit, npm audit, gitleaks with `.gitleaks.toml`), `eval.yml`; release side: `pages.yml` (deploys only once the owner enables Pages), `release-images.yml` (GHCR), `eval-live.yml` |
| `VERSION` | Project version (mirrors: pyproject.toml, package.json and its lockfile, `backend-go/internal/version/version.go`, `truthtable/__init__.py`, the compose image tag) |
| `docs/` | Curated docs (index `docs/README.md`); `docs/v2/` SPEC and phase plans; `docs/launch/` launch drafts; `docs/archive/` history |

## Commands

```bash
make help           # list every target (root Makefile + mk/*.mk)
make demo           # zero-key demo: mock Jev + mock judge + scripted traffic, opens http://localhost:3000
make demo-down      # stop it (make demo-clean also deletes its volumes)
make up / down      # the real stack (proxy, engine, postgres, dashboard); connect keys in the setup wizard or .env (docs/JEV-ACCESS.md)
make configure WITH_SECRETS=1                     # .env plus vendor keys at hidden prompts (never echoed)
make onboarding-up / onboarding-setup-url / onboarding-down   # fresh-install stack (no admin, no keys; mock-upstream OpenRouter)
make onboarding-check-config   # proxy -check-config with that stack's environment, no container (part of make check)
make e2e-up / e2e-scenarios / e2e-down            # every vendor on mock-upstream (real HTTP paths)
make up-infra       # backing services only, for make dev-go / dev-python / dev-react
make doctor         # diagnose a running stack (ARGS="--expect demo")
make check          # the local gate: lint + unit tests + contracts + compose config
make install        # deps for all projects;  make proto  # regenerate gRPC stubs after editing proto
make contracts-check / contracts-test / vectors   # validate contracts; regenerate mechanical vectors
make vectors-ts     # run the same golden vectors against the TypeScript decision core
make gen-types / gen-api-schema (+ -check)        # regenerate types.gen.ts / api.v1.schema.json
make config-docs (+ -check)                       # regenerate docs/CONFIGURATION.md + .env.example from the key registry
make gen-policy-validator (+ -check)              # regenerate the CSP-safe policy.v1 validator (src/lib/policy)
make gen-evaluator-validator (+ -check)           # regenerate the CSP-safe evaluator validator (src/lib/evaluator)
make setup-url      # print a new one-time SETUP link (claim it at /app/setup to create the admin key)
make gate-eval      # build backend-go/bin/gate-eval;  make calibrate EVALUATOR=… IN=… [WRITE=1]
make export-replay REPLAY_OUT=<dir>               # sanitized replay recording from the database
make bench-lite / bench-publish / eval-live       # capped benchmark runs (real keys); publish refuses mock output
make test-e2e-web   # Playwright specs against a running stack
make e2e-faults     # vendor-fault scenarios against the running e2e stack
make jev-check      # check the decision-model connection (cmd/jevcheck)
make bench-go / bench-go-smoke                    # Go benchmarks into backend-go/bench / each one once
make load-test-gate [LOAD_PUBLISH=1]              # k6 gateway overhead on mock-upstream; publishing writes docs/benchmarks/load
make build-static / test-e2e-static               # the static Pages site / its Playwright specs (vite preview)
make recording (+ -check)                         # rebuild the simulated demo tape (frontend-react/demo-data/simulated)
make record-demo    # record the real demo dataset: a dry run with an estimate unless CONFIRM=1 (real keys)
make pages-build    # build the Pages site exactly as pages.yml does (never deploys)
make version-bump V=X.Y.Z / check-version         # set VERSION, every mirror and the CHANGELOG heading / check they agree
make first-run      # a fresh stack to its first flagged trace, timed (Playwright)
make lint-workflows # actionlint + zizmor over .github/workflows
make check-claims   # every <!-- claim:ID --> figure in README/docs vs contracts/claims; claims-write regenerates measured tables
make check-claims-launch                          # launch gate: recorded dataset, measured run, reviewed comparison, og.png
make og / hero-gif / screenshots                  # launch captures (og and hero-gif refuse the simulated tape; screenshots show its label)
```

Per-project:
- Python: `cd backend-python && poetry run pytest` (asyncio_mode=auto). `integration` and `e2e`
  markers are opt-in (`addopts` deselects them; an explicit `-m` overrides).
- Go: `cd backend-go && go test ./...` (Postgres tests skip unless `TEST_DATABASE_URL` is set; they use
  `internal/storetest`, one schema per test). Golden vectors: `go test ./internal/decision/... -run TestVectors`.
- React: `cd frontend-react && npm test` (vitest); Playwright: `npx playwright test --project=stack`.

## Conventions

- **Contract-owned paths** change only deliberately (lead/contract PRs) and keep Go, TS and Python in
  sync: `proto/`, `contracts/`, `backend-go/internal/store/migrations/`, `backend-go/internal/decision/`,
  `frontend-react/src/lib/decision/`, `frontend-react/src/types/api.ts`. Go is the reference for decision
  logic; vectors move `draft → frozen` only when the Go reference passes them.
- **Config**: every env var is registered in `contracts/config/keys.v1.json` (`TRUTHTABLE_*` for the
  gateway); never invent a key outside the registry.
- **Proto changes**: edit `proto/evaluator.proto` only, then `make proto`. Never hand-edit generated
  code in `backend-go/api/audit/v1/` or `backend-python/src/truthtable/grpc/pb/`.
- **Python**: black (line-length 100), ruff, mypy strict on new packages. New LLM providers implement
  `providers/base.py::LLMProvider` and register in `providers/registry.py`.
- **Go**: gofmt; new HTTP middleware in `internal/middleware/`; gosec (medium) must stay clean.
- **Frontend**: follow `DESIGN.md` (colour means a verdict or outcome only; no emoji/glow/gradient;
  every page has empty/loading/error/degraded/demo/readonly states; no dead buttons). Wire types in
  `src/types/api.ts`; words/icons for server values via `src/domain/presentation.ts` (never render raw
  enums); data access through `src/data/` (DataSource, StreamClient v2).
- **Verdicts**: claims are SUPPORTED / CONTRADICTED / UNSUPPORTED / PARTIALLY_SUPPORTED, plus UNKNOWN
  (judge/parse failure) and NOT_CHECKED (no evidence for that claim). Audits are GROUNDED /
  HALLUCINATED / UNVERIFIABLE / NO_CLAIMS / NOT_ASSESSED. CONTRADICTED (evidence disproves) stays
  distinct from UNSUPPORTED (nothing addresses it) end to end. Text spans are UTF-16, half-open.
- **Honest numbers**: every user-facing figure carries a label (measured / estimate / third-party /
  vendor / simulated); never present mock output as measured. In README and docs a figure from
  `contracts/claims/value-claims.v1.json` follows a `<!-- claim:ID -->` marker with its chip (for
  example `[estimate]`) and any caveat within 3 lines; measured figures come only from
  `contracts/claims/measured.json`, through the generated `<!-- measured:start/end -->` tables.
  `make check-claims` enforces this.
- **Judge**: `LLM_PROVIDER=auto` picks explicit > Anthropic key > OpenRouter key > OpenAI key >
  Ollama (if reachable) > none (mock in demo mode). A failing judge is never swapped silently:
  `LLM_FALLBACK=none` is the default, so audits fail with `judge_unavailable` until it recovers.
- **Metrics**: gateway `trustagent_gateway_*`, engine `trustagent_engine_*`.
- **Versioning**: `make version-bump V=X.Y.Z` (`scripts/bump_version.py`) writes `VERSION`, every
  mirror and the CHANGELOG heading together; `make check-version` verifies them.

## Gotchas

- Generated gRPC stubs are gitignored; run `make proto-tools` once, then `make proto` on a fresh clone.
- The zero-key demo uses mock backends: its numbers are simulated and labelled as such.
- Real checks need an OpenRouter key (one key covers Jev and the Claude judge) — see docs/JEV-ACCESS.md.
  Never commit keys; `.env` is gitignored. Tests use fake keys only (`sk-test-…`, `sk-or-v1-e2e-…`,
  plus `cmp0e2eZq7Lw4Rt9Yx2Vb6Nm` in leak tests); gitleaks scans the whole history in CI.
- Keys saved in the dashboard are sealed with the master key (`TRUTHTABLE_SECRET_KEY`, else the
  `trustagent-secrets` volume): a fresh volume makes them undecryptable (`make doctor` reports it).
- Windows/WSL dev environment: Docker Desktop needs WSL integration enabled; prefer cross-platform
  commands in Makefile/scripts. The dev laptop is small (~8 GB RAM): avoid running several heavy
  builds/test suites at once, and run `make check VITEST_FLAGS=--maxWorkers=2` (with every vitest
  worker, the axe tests in `kit.a11y.test.tsx` can exceed vitest's 5 s timeout under load).
- Curated public docs live in `docs/`; historical build-journal material is under `docs/archive/`.
