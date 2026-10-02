# TrustAgent gateway (Go)

The gateway is the part your app talks to. It serves the OpenAI and Anthropic APIs on `/v1`,
forwards each request to the upstream LLM, checks the answer (Tier-0 detectors and one fast-check
call to Jev), applies the route's policy (observe, flag, block or fallback), and queues a deep
audit on the Python engine over gRPC. It also serves the dashboard's REST API and WebSocket v2
stream, holds the runtime settings and the encrypted vendor keys, records every call's cost in a
ledger against daily budgets, and stores traces, decisions, audits, labels, calibrations and
policy versions in Postgres.

What a client sees (modes, headers and trailers, streaming, failures, block and fallback bodies,
credentials, errors, test mode) is documented in [docs/GATEWAY.md](../docs/GATEWAY.md). The
design is [docs/v2/SPEC.md](../docs/v2/SPEC.md) (§2.9 decision algorithm, §2.12 request and
response contract, §3.1 Go change list).

## Layout

| Path | What |
|---|---|
| `cmd/proxy` | The gateway: configuration, wiring, the REST API (`*_api.go`), `/v1` routes, the CLI (`-check-config`, `-dump-config-spec`, `-new-setup-token [-force]`) |
| `internal/proxy` | The `/v1` handler: parse, forward, gate, enforce (block, fallback), stream relay and buffering, headers and trailers |
| `internal/llmapi` | LLM wire formats: OpenAI chat and completions, Anthropic Messages, SSE decoding and writing, block and fallback bodies, synthesized streams |
| `internal/upstream` | Named upstreams (OpenAI, Anthropic, fallback), passthrough and inject credentials, timeouts |
| `internal/gate` | One exchange's decision pipeline: evidence, detectors, the Jev call, `decide()`, calibration lookup, the audit plan |
| `internal/decision` | The reference decision core (`decide()`, detectors, canonical JSON); the golden vectors run against it, and the TypeScript core must match it |
| `internal/jev` | The fast-check client: backends (`openrouter-decisions`, `openrouter-systemone`, `typesafe`, `compatible`, `mock`, `off`), retries, hedging, breaker, rate limit, spend caps, snapshot tracking |
| `internal/evidence` | What an answer is checked against: system-prompt documents, `Context:` blocks, tool results, the `trustagent.context` extension |
| `internal/policy`, `internal/evaluators` | Versioned policies (file, dashboard, rollback), validation and block readiness; the evaluator registry |
| `internal/authz` | Keys and scopes (ingest, read, content, admin), sessions, CSRF, the one-time setup token, the master key |
| `internal/credentials` | Vendor keys saved in the dashboard: AES-256-GCM sealing bound to name, provider and origins, the URL rules, key checks per provider, Connect OpenRouter (PKCE) |
| `internal/runtimecfg` | Runtime settings with env precedence (`env_managed`), one version and epoch, hot reload, and the push to the engine |
| `internal/costs` | The cost ledger, the daily budgets with reservations, ROI and the price table (overrides first) |
| `internal/studio`, `internal/calibration` | Evaluator Studio runs (test bench, paid dry-run); calibrations stored, fitted from labels and activated |
| `internal/config` | The configuration registry loader and the redactor every log line and error passes through |
| `internal/store` | Postgres: traces (written in batches), gate decisions, bodies, audits, policies, settings, credentials, cost events, labels, calibrations, the append-only admin audit log; embedded migrations |
| `internal/worker`, `internal/grpc` | Deep-audit lanes and the engine client (`Evaluate`, `GetEngineInfo`, knowledge RPCs) |
| `internal/websocket` | WebSocket protocol v2: sequence numbers, replay ring, gaps, scoped event variants |
| `internal/metrics` | Prometheus metrics, all `trustagent_gateway_*` |
| `cmd/trafficgen` | Sends the demo scenarios and checks each outcome (`--once --expect`) |
| `cmd/mock-upstream` | A fake of every vendor the gateway calls, with latency knobs and scenario-keyed faults (e2e and load stacks) |
| `cmd/calibrate`, `cmd/gate-eval`, `cmd/jevcheck` | Fit a calibration, run the fast check over a JSONL file, check a Jev key and endpoint |
| `cmd/export-replay`, `cmd/vectorgen` | Export a sanitized replay recording; maintain the golden vectors |
| `api/audit/v1` | Generated gRPC stubs (gitignored: `make proto`) |

## Run it

The usual way is the whole stack in Docker: `make demo` (zero keys) or `make up` (your keys); see
[docs/GETTING-STARTED.md](../docs/GETTING-STARTED.md). To work on the gateway itself:

```bash
make proto-tools && make proto   # once on a fresh clone: the gRPC stubs
make up-infra                    # Postgres (and COMPOSE_PROFILES add-ons) in Docker
make dev-python                  # the engine on :50051, in another terminal
make dev-go                      # the gateway on :8080 (metrics :8002)
```

Configuration comes from the environment and `.env`; every key is registered in
`contracts/config/keys.v1.json` and listed in [docs/CONFIGURATION.md](../docs/CONFIGURATION.md).
`go run ./cmd/proxy -check-config` prints the effective configuration and its warnings.

```bash
curl -s http://localhost:8080/health          # {"status":"ok","version":…,"demo":…,"audit_engine":…}
make setup-url                                # a one-time link that creates the first admin key
```

## Endpoints

| Path | What |
|---|---|
| `POST /v1/chat/completions`, `/v1/completions`, `/v1/messages` | Gated LLM calls, JSON and SSE ([GATEWAY.md](../docs/GATEWAY.md)) |
| `GET /v1/trustagent/decisions/:trace_id` | Long-poll for a decision (`wait_ms` up to 10000) |
| `/v1/responses*`, `/v1/models*`, `/v1/embeddings`, `/v1/messages/count_tokens` | Passed through ungated |
| `GET /health` | Liveness; `audit_engine` is the last real engine ping |
| `GET /api/status` | Everything `make doctor` reads: engine, judge, fast check, calibration, policy, budgets |
| `GET /api/traces`, `/api/traces/:id` | Traffic and trace detail (decisions, evidence, audit, claims) |
| `/api/policies*`, `/api/evaluators*` | Policy versions, validation, dry-run, rollback; evaluator specs |
| `GET /api/setup/status`, `/api/session`, `/api/keys` | Whether an admin exists yet; sign-in (a key or the setup token), sign out everywhere; API keys |
| `/api/settings`, `/api/credentials*`, `/api/connect/openrouter/*` | Runtime settings (`If-Match` versions); vendor keys (saved, checked, removed); Connect OpenRouter |
| `/api/decision-model/test`, `/api/pricing`, `/api/roi/*` | One probe of the fast check; the effective price table; Savings figures |
| `/api/studio/*`, `/api/labels*`, `/api/calibration*` | Evaluator Studio (draft, test, samples, quick calibration, dry-run over SSE); labels and the label queue; calibrations |
| `GET /api/traces/:id/decision-request`, `/api/evidence/*` | The glass box (the Jev request, recompiled); evidence coverage and suggestions |
| `GET /api/admin/audit-log`, `GET /api/export/replay` | The admin audit log; a sanitized replay recording (names the build's git revision) |
| `GET /ws?protocol=2` | The dashboard's live stream (WebSocket v2; a handshake without `protocol=2` is refused) |

`docs/v2/SPEC.md` §2.4 lists every REST endpoint with its scope and limits, and §2.5 the
WebSocket events. Metrics are not on the main port: Prometheus reads `:8002/metrics` on the
compose network.

## Metrics

All names are `trustagent_gateway_*` (SPEC Appendix B); the `route` label is capped at 64
routes, and there is no model label.

| Area | Metrics |
|---|---|
| Requests | `requests_total{route,format,action}`, `overhead_seconds{route,mode}`, `upstream_seconds{format}` |
| Gate | `gate_decisions_total{route,mode,action,status}`, `gate_seconds{stage}`, `gate_errors_total{kind}`, `evaluator_levels_total{evaluator,level,stage}`, `detector_fired_total{detector}` |
| Fast check (Jev) | `jev_requests_total{backend,outcome}`, `jev_seconds{backend}`, `jev_connect_seconds`, `jev_cost_usd_total{backend,component}`, `jev_input_tokens_total{backend}`, `jev_breaker_state{backend}`, `jev_rate_limited_total{class}`, `jev_snapshot_info{snapshot}`, `jev_anomalies_total{kind}` |
| Audits and fallback | `fallback_total{outcome}`, `audit_jobs_total{lane,selection,outcome}`, `audit_queue_depth{lane}`, `audit_roundtrip_seconds`, `uniform_samples_today` |
| WebSocket | `ws_clients`, `ws_dropped_total`, `ws_gaps_total{reason}` |
| Policy, storage, spend | `policy_version`, `policy_reload_errors_total`, `trace_write_dropped_total`, `retention_lag_seconds`, `spend_usd_today{kind}`, `spend_reserved_usd{kind}`, `cost_write_dropped_total` |

The engine's metrics are `trustagent_engine_*`. The Grafana dashboard
(`config/grafana/dashboards/trustagent.json`) filters every query by `job="proxy"` or
`job="engine"`.

## Test

```bash
cd backend-go
go test ./...                                    # unit tests (Postgres tests skip without TEST_DATABASE_URL)
go test -race -p 2 ./...                         # what CI runs, with the race detector
go test ./internal/decision/... -run TestVectors # the golden vectors against the reference core
TEST_DATABASE_URL=postgres://… go test -p 1 ./internal/store/...   # one schema per test (internal/storetest)
go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream'   # every scenario, in process
```

`gofmt`, `go vet` and gosec (medium and above) must stay clean; `make lint-go` runs the first
two. Against a running stack, `trafficgen --once --expect` checks every scenario over real HTTP
(`make demo-scenarios`, `make e2e-scenarios`), and `make load-test-gate` measures the gateway's
overhead with k6 against `mock-upstream` ([GATEWAY.md §13](../docs/GATEWAY.md#13-load-test)).

## Conventions

- New HTTP middleware goes in `internal/middleware`.
- Every environment variable is registered in `contracts/config/keys.v1.json` first.
- A secret never reaches a log, an error, the API or the WebSocket: add it to the redactor before
  its first use, and test with fake keys only (`sk-test-…`, `sk-or-v1-e2e-…`, plus
  `cmp0e2eZq7Lw4Rt9Yx2Vb6Nm`, which no pattern matches).
- Docker builds get the git revision as the `GIT_SHA` build arg (set by make), because the build
  context has no `.git`, and label the image with it (`org.opencontainers.image.revision`).
- Decision logic changes start in `internal/decision` (the reference): update the golden vectors,
  then the TypeScript core in `frontend-react/src/lib/decision` must pass the same vectors.
- Never edit `api/audit/v1`: change `proto/evaluator.proto` and run `make proto`.

## Related

- [Root README](../README.md) · [Python engine](../backend-python/README.md) ·
  [Dashboard](../frontend-react/README.md) · [proto/evaluator.proto](../proto/evaluator.proto)
