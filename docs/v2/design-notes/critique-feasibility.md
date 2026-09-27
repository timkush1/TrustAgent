# TrustAgent v2 canonical spec: feasibility, sequencing and testability review

**Verdict.** The canonical spec cannot be delivered in the order it describes. The first working end-to-end result would arrive at the P3 gate, after about 100 engineer-days. Several gates depend on work scheduled for later phases. The four parallel streams share files and are not independent. The mock strategy lets failures pass quietly. And none of the Docker-dependent gates can run on the owner's machine today.

Everything below was checked against the working tree. Paths are relative to `/home/timkushmar/projects/TrustAgent`.

---

## HIGH

**H1. There is no walking skeleton. The first full integration happens at the P3 gate.**

P2 builds four cores in isolation. Proto, contracts, the database, the WebSocket stream, headers and the UI are first exercised together at P3, after P1, P2 and most of P3 are done.

Fix: add a milestone **S0** between P1-core and P2 (scope is at the end of this review). Move a minimal `cmd/trafficgen --once --expect` (test mode, scenario-driven) and a minimal `make demo` from P5 into S0. Requirement 9 already demands `make demo` with zero keys, and it doubles as the e2e harness.

**H2. P1 is a serial bottleneck, and its vectors have nothing to check them against.**

P1 ships 12 schemas, 10 templates, about 180 vectors, about 60 API fixtures, 3 migrations, `api.ts`, policies, calibration files and CI, all from one owner while the four streams wait. Compile, token, canonical and normalize vectors need exact numbers: `ceil(codepoints/4)`, packing overlap scores, truncation code-point counts. Nobody can hand-compute those reliably, and nothing validates them until the first runner exists in P2.

Fix:
- Split P1 into two parts:
  - **P1-core** (about 4 days): proto, evaluator/decision/policy schemas, the `groundedness` and `contradiction` templates, migration 0002 plus the runner fix, the `api.ts` subset (TraceSummary, GateDecisionView, AuditDetail, WS envelope, SystemStatus), and about 40 vectors.
  - Everything else ships later as contract PRs: Studio, ROI and credential types before P4; `0003`/`0004` before they are first used.
- Add `contracts/tools/gen_vectors.py`, a single Python reference implementation that generates `expected` for the mechanical kinds (canonical, tokens, question_hash, compile, normalize, calibration_apply, cost).
- Hand-author only the semantic `expected` subsets (decide, detectors, evidence, lint).
- Run `gen_vectors --check` in the contracts CI job.
- Budget a vector-errata PR in the first week of P2.

**H3. The contract-change rule becomes impossible to satisfy once the runners exist.**

The decision cores live inside service worktrees: `internal/decision` in W-GO, `truthtable/decisions/contract.py` in W-PY, `src/lib/decision` in W-WEB. Any semantic change to decide, compile or detectors then has to update vectors and all three implementations in one step, "all three runners green". Service worktrees are forbidden from editing contracts, so this cannot be done inside the worktree model.

Fix: make the three decision-core directories (including detectors and evidence) **contract-owned**. Contract PRs on the integration branch edit vectors and all three cores together. Service worktrees only call the cores.

**H4. Several gates depend on work scheduled later.**

- **P3 gate uses P5 work.** It runs `docker compose exec -T proxy /trafficgen --once --expect` and the demo override, but `cmd/trafficgen` and `cmd/mock-upstream` are P5 W-GO items.
- **P3 Playwright tests need P4 auth.** "Policy edit → new version → rollback" requires admin writes. `internal/authz`, sessions and CSRF are P4. §R27 makes the browser cookie-only, so the P3 UI has no way to authenticate as admin at all.
- **B1 is scheduled in two places.** Appendix X2 puts the B1 fix in P2 but points it at G2.3 (authz, P4).
- **P4 onboarding test cannot mock OpenRouter.** It needs "mocked OpenRouter PKCE", but the Go code-for-key exchange and the `GET /api/v1/key` check have no base-URL override. §F.2 only has `OPENROUTER_BASE_URL`, and that is Python-only.

Fix:
- P2 W-GO gets: env-key scopes (`API_KEYS`, `READ_KEYS`, `ADMIN_KEYS`), `X-TrustAgent-Key`, the B1 matrix, header stripping, and `POST /api/session {key}` issuing a cookie with CSRF.
- P4 keeps the setup token, the `api_keys` table, session epoch and credentials.
- Move `cmd/trafficgen` and `cmd/mock-upstream` to S0/P2.
- Add `TRUTHTABLE_OPENROUTER_BASE_URL` (default `https://openrouter.ai`) for PKCE, the key check and the openrouter-* backends.
- Have `mock-upstream` serve `/api/v1/auth/keys`, `/api/v1/key`, `/api/alpha/decisions`, `/api/v1/systemone` and `/api/v1/chat/completions`.

**H5. The worktrees are not independent after contracts land.**

Shared files every stream will edit:

| File | Streams that touch it |
|---|---|
| `.github/workflows/ci.yml` (one file, 4 jobs) | all four |
| `Makefile` | all four |
| `docker-compose.yml` | W-DX, plus others |
| `backend-python/Dockerfile` | W-PY (D11 lockfile Dockerfile) and W-DX (root context, in-image proto) |
| `backend-python/pyproject.toml` | W-PY (extras, jsonschema) and W-DX (F8 `addopts`) |
| `.env.example` | generated from W-GO's and W-PY's registries by W-DX's generator |

Semantic dependencies inside P2:
- W-DX's compose healthcheck (`python -m truthtable.healthcheck`) needs a W-PY module.
- `gen_config_docs` needs both `-dump-config-spec` (W-GO) and `--dump-spec` (W-PY).
- W-DX's engine image needs W-PY's extras split.

Fix:
- In P1, split CI into `ci-go.yml`, `ci-python.yml`, `ci-web.yml`, `ci-contracts.yml` and `ci-e2e.yml`, keeping the job names stable for branch protection.
- Split the Makefile into `mk/{go,py,web,dx,contracts}.mk` includes.
- Give every Dockerfile and compose file to W-DX alone.
- W-PY's first P2 commit (merged the same day) is `healthcheck.py` plus the pyproject extras.
- Move the config-docs drift check to P3.

**H6. None of the Docker gates can run on the owner's machine today.**

`docker` is not found in this WSL distro. Docker Desktop is installed at `/mnt/c/Program Files/Docker/.../docker.exe`, but WSL integration is off. There is no local Postgres. That blocks:
- the P1 gate (`TEST_DATABASE_URL`);
- every compose, e2e and `make demo` gate in P2 (W-DX), P3 and P5.

Fix: add a P0 step: Docker Desktop → Settings → Resources → WSL integration → enable for this distro. Then verify `docker compose version` is at least 2.24 and `docker run --rm hello-world` works. Add a `tools.docker` doctor check. Until that is done, mark the database and compose gates as CI-only.

**H7. Parallel worktrees collide on containers, ports and the test database.**

- `docker-compose.yml` sets `container_name: truthtable-*` on every service and uses fixed host ports, so two worktrees cannot run stacks at the same time. The dx design removes `container_name` only in P2.
- `internal/store/postgres_test.go` does `TRUNCATE audits CASCADE` against a single `TEST_DATABASE_URL`. `go test ./...` runs packages in parallel, so the new DB-backed packages (policy, settings, credentials, authz, cost) will interfere with each other.
- The `trustagent_config` LISTEN/NOTIFY channel will leak between those parallel test packages.

Fix:
- Remove `container_name` in P1.
- Set `COMPOSE_PROJECT_NAME=trustagent-<wt>` per worktree, and add a `POSTGRES_HOST_PORT` variable alongside `PROXY_HOST_PORT`/`DASHBOARD_HOST_PORT`.
- Add an `internal/storetest` helper that creates a schema per test (`CREATE SCHEMA t_<rand>` with pgx `RuntimeParams["search_path"]`, dropped on cleanup).
- Make the NOTIFY channel name configurable (`trustagent_config_<schema>` in tests).

**H8. The mock Jev is not enough for deterministic CI or a coherent demo.**

- **(a) Two unrelated mocks.** The Go gate mock is a heuristic; the Python engine mock is fixtures/hash/heuristic. Gate-vs-deep-audit disagreement in the demo becomes an artifact of two mocks.
- **(b) Fail-open hides fixture misses.** A strict-mode fixture miss becomes `status=error` with `action=pass`, so CI stays green.
- **(c) The HTTP path is never exercised.** The in-process mock skips the HTTP adapter, retry, breaker, hedging and the Content-Type check in every stack test.
- **(d) The fallback scenario may never fire.** It is undefined whether `mock__jev-heuristic-v1.json` counts as `measured`. If it doesn't, `require_calibration` caps the payments-faq fallback scenario to flag.
- **(e) Fault injection is not deterministic.** The seeded `MOCK_FAIL_RATE`/`HANG_RATE` random stream changes with goroutine scheduling.

Fix:
- Put scenario-keyed mock answers in `contracts/demo/scenarios.v1.json`: `mock.gate_answers{qid: RawAnswer}`, `mock.cascade{claim_index: probs}` and `mock.judge`.
- Carry the scenario through `X-TrustAgent-Scenario` (test mode only) into a new `AuditOptions.mock_scenario = 10`, honoured only when the backend is mock.
- A strict miss gets `error_kind=mock_fixture_missing`. `trafficgen --expect` asserts `X-TrustAgent-Status: ok` for each scenario, and the e2e job asserts that the anomaly metric is 0.
- The e2e compose uses the `compatible` backend pointed at `mock-upstream /v1/systemone`, with scenario-keyed faults (`hang`, `html200`, `402_inflight`, `529`). `make demo` keeps the in-process mock.
- Add a calibration state `mock` that satisfies `require_calibration` only when the backend is `mock`.
- Decide faults as `hash(seed, question_set_sha, state_sha)`, not from an RNG stream.

**H9. Scope versus reality.**

Today's code is about 12.6k lines: Go 3.5k, Python 3.9k source plus 1.8k tests plus 1k evals, TypeScript 2.5k. The spec implies roughly 50–70k lines and about 195–210 engineer-days.

Risk #12's claim that the product is "shippable at P3 with block" is false. `require_calibration` defaults to true, so block and fallback are capped to flag until a measured calibration exists. That needs a live key and `make calibrate`, which is P6.

Fix:
- Restate the P3 exit as observe + flag only.
- Move "owner creates an OpenRouter key and adds $5 of credit" to P0. There is no waitlist and it unblocks P1g and early calibration.
- Apply the cuts in M11.

---

## MEDIUM

**M1. Frontend dependency freshness.**

| Package | Proposed | Release status |
|---|---|---|
| vitest | 5.0.2 | 5.0.0 released 2026-09-03; 5.0.2 is two days old |
| @tanstack/react-table | 9.2.4 | 9.0.0 released 2026-08-04; the spec itself marks the API as an assumption |
| React | 19.3.0 | 18 days old |
| Vite | 8.3.1 | 3 days old (the Vite 8 line itself dates from March) |

The current lockfile's vitest 4.1.8 already runs on a nested Vite 8.0.16, and its peer range is `^6 || ^7 || ^8`. So vitest 5 is unnecessary.

Fix:
- Upgrade (F1): vite ^8, @vitejs/plugin-react 6.1.1, TypeScript ~6.0.3.
- Keep: vitest ^4.1.11.
- Downgrade/pin: @tanstack/react-table ^8.21.3 (or no table library at all); pin React to exactly 19.3.0.
- Land F1 as a standalone P0.5 PR while the old UI still builds.
- Run `npm audit --omit=dev --audit-level=high` in each dependency PR. It is blocking in `security.yml`, and the spec adds about 35 packages.

**M2. Canonical JSON bans numbers but is used to hash documents that contain them.**

The policy `sha256` and `evaluator_versions.spec_sha256` (also used by TypeScript as `draft:<spec_sha256[:16]>`) hash documents with numbers in them. Python's `json.dumps(1.0)` gives `1.0`; Go and JavaScript give `1`.

Fix:
- Use RFC 8785 (JCS) number formatting for canonical JSON.
- Keep "no numbers" only as a Jev `state`/`questions` rule.
- Restrict the evaluator schema's `content` type to strings, string objects and string arrays.
- Add canonical vectors for 1.0, 0.1, 1e-7, 1e21 and -0.

**M3. Floating-point determinism.**

The summation order over probability maps is unspecified; Go map iteration is random. The `abs(s−1) > max(0.10, 0.005·k)` check at exactly 0.90 or 1.10 then depends on that order. Python `round` rounds half-to-even, which differs from JavaScript and Go.

Fix:
- Sum in sorted-key order with plain float64 addition.
- Compare with `> tol + EPS`.
- Ban `round()`; use `floor(x*100 + 0.5)/100`.
- Add edge vectors for sums of 0.90 and 1.10.

**M4. Caller-side paths have no `decide()` input defined.**

`skipped` (budget, breaker, rate_limited, overload, the playground call cap) is not defined as a decide input. Go and the TypeScript static playground will diverge on it.

Fix: add `meta.skip_reason`, which yields `status=skipped`, every evaluator `error(<reason>)`, and a mapped `degraded` value. Add at least 4 vectors.

**M5. Fixture type checks will not work as written.**

A JSON import widens literal unions to `string`, so `const x: TraceDetail = fixture` fails to type-check. The Go structs are mirrored by hand. `tsconfig.app.json` has no `resolveJsonModule`.

Fix:
- In the contracts job, generate `contracts/schema/api.v1.schema.json` from `api.ts` (ts-json-schema-generator) and validate the fixtures with ajv.
- Go handler golden tests validate responses against it (santhosh-tekuri/jsonschema v6).
- Build the msw handlers from the validated fixtures.
- Add `resolveJsonModule: true` to `tsconfig.app.json`.

**M6. Strict config mode will break because the key registries are split.**

Go `registry.go` and Python `config.py` are separate registries. With `TRUTHTABLE_STRICT_CONFIG` and "did you mean" warnings, each service flags the other's `TRUTHTABLE_*` keys; the Go dotenv loader walks up to the shared root `.env`. The result is exit 2 in CI and e2e.

Fix: one `contracts/config/keys.v1.json` (name, aliases, services, kind, default, secret) read by both loaders and by `gen_config_docs`. "Unknown" means not in the union.

**M7. The dashboard breaks between the P2 merge and P3.**

WS v2 and the new REST shapes land in P2 (W-GO). The new UI lands in P3. In between, the integration branch has a dashboard that doesn't work, and CI is still green.

Fix:
- The hub keeps sending v1 frames unless the client asks for `Sec-WebSocket-Protocol: trustagent.v2` (or `?protocol=2`).
- Keep the legacy `/api/audits` shape until the new Audits page ships.
- Delete v1 at the end of P3.

**M8. The trace batch writer races the live feed.**

The writer flushes every 250 ms and drops rows when full, while `trace_started`/`gate_decision` go out over WebSocket immediately. Clicking a live row returns 404 for up to 250 ms, and forever if the row was dropped. The "gate chip → open trace" Playwright test will be flaky.

Fix:
- `GET /api/traces/:id` falls back to an in-memory LRU of the last `WS_REPLAY_BUFFER` traces.
- Persist audits before broadcasting them.
- Add `TRUTHTABLE_TRACE_FLUSH=sync` for test mode.

**M9. The same-origin nginx breaks streaming.**

`proxy_buffering` is on by default, which buffers SSE for `/v1/*` and `/api/playground/run`. nginx also does not forward HTTP trailers.

Fix:
- The template sets `proxy_buffering off; proxy_http_version 1.1; proxy_set_header Connection ""; proxy_read_timeout 600s` on those locations.
- Document the SSE comment as the only annotation channel through nginx.
- Add a stack test that streams the playground through `:3000`.

**M10. The Python extras split will break the default CI job.**

`graphs/audit_graph.py:28-29` imports vectorstore eagerly, and `tests/unit/test_kb.py` imports the kb modules.

Fix:
- Land the lazy imports and the extras split in one PR.
- Mark the KB tests `knowledge` with `importorskip("sentence_transformers")`.
- CI gets `unit-core` (no extras, plus the no-torch import check) and `unit-knowledge`.

The P2 mypy gate will also fail: mypy is not in CI today and has never run (no `.mypy_cache`), and it follows imports into untyped modules. Add a `[[tool.mypy.overrides]]` setting `follow_imports="silent"` for the legacy `truthtable.*` modules.

**M11. Cut list (saves about 35–45 engineer-days; the owner's requirements are still met).**

| Defer or cut | Why it's safe |
|---|---|
| Python cascade: `decisions/` client, cascade verifier, hardness rules, CascadeStats population, R32 RPM split | Not an owner requirement. The ROI counterfactual uses `uniform_sample_rate` deep audits with `verifier=llm`, which doesn't need it. |
| KB Jev gate, injection screen and prefilter | Knowledge is an optional profile |
| `partition: by_evidence`; the `batched_*` layouts | A/B options only |
| Go `calibrate_fit` and Go `lint` | Studio calibration and lint become client-side TypeScript in both builds |
| Policy dry-run replay | Studio dry-run stays |
| Async dry-run jobs | Replace with a synchronous run of ≤200 samples plus SSE progress |
| `SECRET_KEY_PREVIOUS` rotation | — |
| Go Evaluate→Submit fallback | The services always ship together |
| Visual snapshots and Lighthouse | Keep, but informational |
| `_gallery` route; ⌘K | — |

Proto fields stay; they are additive and can simply be left unpopulated.

**M12. P1g cannot verify vendor behaviour without a key.**

The "[A] identical bodies" check is assigned to "P2 backend tests", but httptest cannot verify a vendor.

Fix: with the owner's key (from P0), P1g captures real responses into `contracts/fixtures/vendor/*.json` after sanitising them: decisions, systemone, `/api/v1/key`, and chat with `usage.include`. These become the httptest replay fixtures and shape the mock: key order, 2-decimal rounding, whether `confidence` is present, the snapshot string. TypeSafe-direct stays [A] because it needs a separate account.

---

## LOW

- **L1. Go toolchain.** go1.25.0 and go1.25.11 are already in the module cache; `go version` in `backend-go` prints go1.25.0. Risk #10 is overstated. go1.25.0 lacks stdlib patches, though, so the new govulncheck job will be noisy. Add `toolchain go1.25.11` to `go.mod`.
- **L2. R56's premise is wrong.** The installed langgraph 0.0.20 has `add_conditional_edges(start_key, condition, mapping)`; it only rejects coroutine conditions. Skipping inside nodes is still fine; correct the rationale.
- **L3. The P1a Python stub work is free.** `AuditServicer` already subclasses the generated servicer, so new RPCs return UNIMPLEMENTED automatically. Replace the task with a test that asserts the UNIMPLEMENTED codes.
- **L4. "Apply twice" tests nothing.** The runner tracks filenames, so a second run is a no-op. Instead, execute each SQL file twice in a transaction, then migrate a seeded 0001 database (mixed-case statuses, duplicate `request_id`s).
- **L5. P0 contradicts itself.** "Porcelain empty" conflicts with "record the `make test` result in `docs/PLAN.md`"; record it in the first feature commit instead. Also, `exit 1` inside the pasted snippet would kill an interactive shell; run it as a script with `set -e`.
- **L6. gitleaks will block on detector vectors.** They will contain `sk-ant-…`, `AKIA…` and PEM headers. Allow-list `contracts/vectors/detectors/` in `.gitleaks.toml` and use obviously fake bodies.
- **L7. Golden eval baseline shift.** `run_eval` injects golden context with no `source`. Under R19 `auto` that becomes `unverifiable`, which silently moves the baseline. Tag injected documents `request_context`.
- **L8. Playwright on WSL.** Chromium is cached, but `--with-deps` needs sudo. Visual tests must run only in the official Playwright container. Tag them `@visual` and exclude them from local gates.
- **L9. Dev PKCE callback.** It points at `:3000` while `make dev-react` serves `:5173`. Have doctor warn, and document `TRUTHTABLE_PUBLIC_URL=http://localhost:5173` for dev.
- **L10. Contracts module ordering.** The `backend-go` `require/replace ../contracts` breaks the current CI docker job (`context: backend-go`) and the Dockerfile's `go mod download` layer. Add it in the same PR that moves builds to the repo-root context.
- **L11. Landing budget.** ≤110 KB gzip for the landing page inside one SPA with TanStack Router is tight. Use a separate Vite entry for the landing page; make `size` blocking and Lighthouse informational.

---

## S0 walking skeleton (about 9 engineer-days, 1 calendar week with 3 streams)

| Area | Scope |
|---|---|
| **Go** | UUIDv7 trace ids; `X-TrustAgent-Key` plus the B1 fix; tolerant parse (B3); evidence from `request_system` tags and `extension`; compile and decide for `groundedness@1` + `contradiction@1`; in-process mock with scenario fixtures; observe and flag modes on non-streaming chat; §H headers; synchronous inserts into `traces`/`gate_decisions`; WS v2 `gate_decision` (negotiated); worker calls `Evaluate`. |
| **Python** | `Evaluate` wrapping the current graph; UNKNOWN=5; the NOT_ASSESSED preflight; the zero-claims fix. |
| **Web** | New shell, StreamClient v2, Live list, trace detail (response, reasons, "checked against"). |
| **Tools and CI** | `trafficgen --once --expect` with 3 scenarios (pass, contradiction flag, no-evidence not_assessed); `ci-e2e` running compose (proxy, engine, postgres, dashboard), trafficgen, and one Playwright spec. |

**Exit criterion:** zero keys, `make demo`, and the dashboard shows 3 traces with gate chips and a completed audit, with CI green.

---

## Effort estimate (AI-assisted engineer-days, excluding owner review)

| Phase | As specified | With the M11 cuts |
|---|---|---|
| P0, P0.5 (toolchain) | 1.5 | 1.5 |
| P1-core, then later contract PRs | 10 | 8 |
| S0 skeleton | — | 9 |
| P2 | 60 | 45 |
| P3 | 52 | 40 |
| P4 | 34 | 29 |
| P5 | 22 | 18 |
| P6 | 12 | 8 |
| **Total** | **≈195–210** | **≈155** |

Calendar time with 3–4 streams and a single reviewer: about 11–14 weeks as specified, 8–10 weeks with the cuts. The binding constraint is review and merge throughput (about 150 PR-sized commits), not coding.

## Riskiest items, in order

1. **Streaming gate paths**: hold-back, buffer mode, streams synthesized for 2 formats, fallback re-gate, late decisions. Ship non-streaming plus observe-passthrough first; put buffer and fallback behind a flag after S0.
2. **Parity across three languages**: numbers, float ordering, regex, UTF-16 offsets. H3 and M2/M3 address it.
3. **Auth, session and credential crypto, and PKCE**: security-sensitive and late (P4).
4. **Live Jev behaviour**: alpha endpoint, snapshot drift, hangs, no calibration data. Unknown until the owner has a key.
5. **Frontend breadth**: about 10 pages plus landing, static mode and replay. This is the largest amount of code.
6. **Environment**: Docker not enabled in WSL.