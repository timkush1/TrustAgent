# P3 finish: implementation plan to the M-Demo gate

This plan was made against these branch heads:

| Branch | Planned at | The gap analyses read |
|---|---|---|
| `feat/v2-int3` | 7147879 | 5c750e1. 7147879 only changes `AtAGlance.tsx` and its test. |
| `feat/v2-p3-scen` | 57f6555 | 350f99b |
| `feat/v2-p3-static` | 755f34f | 4d50b00 |

File:line references point at the analysed commits. If a line has moved, find it again by symbol name. Go paths are under `backend-go/` and web paths under `frontend-react/` unless a path says otherwise.

---

## 0. How the wave runs

- **Step 0 (lead, done first, one after the other):**
  - Merge wave 3.
  - Then make one "seams" commit. It changes no behaviour and holds every edit to a shared wiring file. After it, no two streams need the same file.
- **Six parallel streams: GH, GS, GD, API, WEB, DX.**
  - Each works on branch `feat/v2-w4-<id>`, cut from `feat/v2-int4` after Step 0.
  - Each has its own worktree at `/home/timkushmar/projects/TrustAgent-w4-<id>`.
- **Three mid-wave checkpoints (CP-GS, CP-GH, CP-GD):**
  - The lead merges the stream's branch into `feat/v2-int4` once its checkpoint gate passes.
  - A stream that consumes the checkpoint then runs `git merge feat/v2-int4`.
  - Strict file ownership makes these merges conflict-free. A conflict means an ownership violation, and the work goes back to the owner.
- **Integration (lead):**
  - Merges, then the turn-on commit, then the end-of-P3 deletions.
  - Full local gates, the Docker proof run, and an independent verifier.

**Rules for every stream**

1. **Ownership.** Edit only files you own (see the matrix in §9). New files go only in directories you own or at paths this plan names.
2. **Frozen after Step 0.** No stream edits these:
   - `cmd/proxy/server.go`, `harness_test.go`, `wiring_test.go`, `api_test.go`, `e2e_test.go`;
   - `internal/metrics/gateway.go`;
   - `frontend-react/src/types/api.ts`;
   - `contracts/schema/**`, `contracts/config/keys.v1.json`, `contracts/superseded.v1.json`;
   - `proto/`, and `internal/store/migrations/`.

   If you need a change there, stop and report it. The lead decides.
3. **Seams.** The signatures Step 0 creates are fixed; their bodies belong to the file's owner. The `flow` struct is append-only: add fields, never rename or remove them.
4. **Fixtures.** `contracts/fixtures/api/` is append-only. Add new files named `<Type>.<case>.json`. Never edit an existing fixture.
5. **Machine limits (the laptop has about 8 GB):**
   - Run tests scoped to your packages, with `go test -race -p 2` and `vitest --maxWorkers=2`.
   - No Docker except `docker compose config`.
   - GD and API may share one `make up-infra` Postgres for `TEST_DATABASE_URL` tests; `storetest` gives each test its own schema.
   - Only DX's last task and the integration step run the full `make check`.
   - At most two streams run a heavy gate at the same time.
6. **Commits.** Conventional commits with the attribution line. No push. No `VERSION` bump: a bump makes the static recording stale.

---

## 1. Lead decisions (binding; they settle the SPEC gaps the analyses raised)

| # | Question | Decision | Owner |
|---|---|---|---|
| D1 | The demo stack has no upstream for `payments-fallback`. | **Test-mode scripted fallback**, as the contract already says (p3-scen `contracts/README.md` `fallback` row). The fallback call is answered from `fallback.response` in `contracts.FS demo/scenarios.v1.json`, with no upstream call, only when all three hold: `TEST_MODE` is on, the attempt-0 answer came from `test_response`, and `X-TrustAgent-Scenario` names a scenario that has a `fallback`. Otherwise the gateway calls the upstream; in e2e, mock-upstream answers any request carrying the CONTEXT block. mock-upstream is not added to the demo compose. | GH |
| D2 | The fallback call fails (transport error, non-2xx, non-JSON, no text). | No re-gate. Add trace reason `fallback_failed`. `on_fail: block` sends the block body (Action block). `on_fail: flag` or `original` sends the original with Action flag. Metric `fallback_total{outcome="failed"}`. | GH |
| D3 | The re-gate fails or times out on a closed route. | The fallback is delivered only when the attempt-1 **action** is `pass` or `flag`. Anything else (fail-closed `block`, or `fallback` from would=block) applies `on_fail`. | GH |
| D4 | Upstream non-2xx or a transport error on a closed route. | **Never enforced.** A non-2xx body passes through as it is. A transport error becomes 502/504 in the route's error shape. `Status: skipped:upstream_error`, `Final.Action=pass`, and the stored decision has `enforced:false` (`Exchange.Unenforced`). A 2xx that is non-JSON, oversize or undecodable SSE is gated as a skip, so closed block/fallback routes **block**. The ungated `a.rest` relay (`handler.go:345-354`) stays only on observe and flag routes. | GH, GS |
| D5 | A tool-call-only answer (`no_text`). | Same as D4: pass through with `enforced:false`. SPEC §2.10 says tool calls are ungated. | GH |
| D6 | n>1 | Gate choice 0 only. Set trace flag `multi_choice` (the `traces.flags` array is free text). `BlockJSON` patches every choice. | GH |
| D7 | Client disconnects mid-stream. | After a finish reason: gate unenforced (`enforced:false`), flag `client_disconnected`, run the audit. Before one: skip with reason `client_disconnected`. | GS |
| D8 | Status of `test_mode_disabled` on `/v1`. | **400** (§2.12, M7), in the route's error shape. The 403 list in §2.4 is for `/api` (SPEC errata). | GH |
| D9 | Stream headers versus trailers. | **Passthrough streams:** `Trailer:` declares Mode, Action, Would-Action, Status, Verdict, Risk, Calibrated, Degraded, Reasons, Checked-Against, Decision-Model, Gate-Ms, Overhead-Ms and Audit. Trace-Id, Request-ID, Route and Source go up front. With `action_only`, the only trailer is Action. **Buffer:** send headers while the response is uncommitted, otherwise the comment plus trailers. **Observe:** deferred headers up front, and the terminal event is not held. `stream_annotations: none` means no comment. | GS |
| D10 | Which rows #16 returns. | One row per trace: **attempt 0** (`WHERE attempt = 0`). A late upsert shows through `late` and `updated_at`. Attempt-1 rows appear only in `TraceDetail.decisions`. `api.ts` does not change. | API |
| D11 | What #66 returns. | The `Recording` manifest only, sanitized. The full directory stays with `cmd/export-replay`; a zip bundle is P5. | API |
| D12 | #61 semantics | The admin `mode_override` bypasses `overrides.client_mode`. Every run submits a deep audit with selection `playground` (`disabled` when there is no engine). Chat mode needs an injected upstream credential, else 503 `credentials_unavailable`. The upstream is always called with `stream:false`; with `stream:true` the answer is then sent as `token` SSE events (real token streaming is P4). `scenario` is honoured only in TEST_MODE. The endpoint has its own limit of 10/min. | API |
| D13 | WS after v1 is removed. | A handshake without `protocol=2` gets 400 `bad_request` before upgrade: "WS v1 was removed; connect with ?protocol=2". Listed in CHANGELOG. | API, DX |
| D14 | Calibration in P3. | A read-only file lookup from `TRUTHTABLE_CALIBRATION_DIR`, with a reload poll every 30 s and the WS event `calibration_updated`. The P4 calibration store (#42–46) builds on the same package. | GD |
| D15 | Inject mode across upstreams. | Per upstream: an upstream injects only when its own key is set (`UPSTREAM_API_KEY`, `ANTHROPIC_UPSTREAM_API_KEY`, `FALLBACK_UPSTREAM_API_KEY`); otherwise client credentials pass through. `Injecting()` is true when any key is set; it controls whether a Bearer or `x-api-key` may be the proxy key, and the open-auth refusal. The proxy key is never forwarded. | GS |
| D16 | P3 REST scope | #2, #15 (trace row), #16, #17, #49, #61, #66 and #71–74. WS events: `audit_queued`, `audit_started`, late `gate_decision`, `status` transitions, `calibration_updated`. Everything else goes to §13. | API, GD, GH |
| D17 | P2 debts nobody listed | The retention job (§2.3 lines 566-575) is GD's last task: it does not block the gate but must land in P3. The async ordered trace writer and the LRU fallback move to P4. | GD |
| D18 | Checks that pass without testing anything | `trafficgen --expect` exits 1 when a selected scenario is skipped by phase, unless `--allow-skip` is given. Stack specs throw in CI instead of skipping. | GH, WEB |
| D19 | Fallback reason strings | `fallback_failed` and `fallback_format_mismatch` are appended to `traces.reasons` through `Final.Reasons`, never into `decision/v1`. WEB adds their wording. | GH, WEB |
| D20 | An oversize answer on a fail-open block or fallback route (GH and GS reviews). | **Blocked**, as D4's last sentence says: failing open never licenses delivering bytes the gateway did not read. `Status: skipped:upstream_error`, the decision stays pass, `Final.Action=block`. No new reason in P3 (D19 keeps two). SPEC erratum at I-3: the §2.12 "passes through" rule holds on observe and flag routes only. | GH, GS (both done), lead |
| D21 | A passthrough stream whose upstream fails or stalls before its first event. | While the headers are uncommitted, answer it as a failed upstream call, the way GH's `h.upstreamFailed` does: 502, or 504 for the idle timeout, in the route's error shape; `skipped:upstream_error`, unenforced, Final pass, counted once. Not a 200 stream that holds only the annotation. | GS (GS-8) |
| D22 | Who records the fallback's attempt 1. | `h.fallback` records nothing; it returns `fallbackOut.Result1`. Each caller records it with `sink.AttemptDecided(f.record, Result1)` and then calls `decided(res0)` once. GS's buffer path already does; GH's `fallbackJSON` adds it in GH-6. | GH, GS |
| D23 | A fallback upstream without its own key (D15 follow-up, B1). | D15 stays. GS adds a configuration warning (so `TRUTHTABLE_STRICT_CONFIG` refuses to start) when `TRUTHTABLE_FALLBACK_UPSTREAM_URL` is set without `TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY` and its host differs from the host of the primary upstream of `TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT`, because clients' credentials would reach another host. The warning names keys, never a value. | GS |
| D24 | An undecodable 2xx body on the JSON path. | Recorded as `skipped:undecodable` (`gate.SkipUndecodable`), not `upstream_error`. Delivery is unchanged: a 502 in the route's shape, unenforced, because there is no readable body to gate or deliver. | GH |
| D25 | The `/v1/completions` prompt as evidence. | Go stays the reference: the prompt reads as one user message and its blocks are extracted. No parity-vector change in P3. GH corrects the `completions-extension-grounded` wording in `contracts/demo`; SPEC §2.10 erratum at I-3. | GH, lead |
| D26 | `/v1/models` from an Anthropic SDK. | P3 keeps `/v1/models` on `UPSTREAM_URL` (§2.4 row 72). Choosing the family from `anthropic-version` or `x-api-key` goes to §13; DX notes it in `docs/GATEWAY.md` at integration. | DX, lead |
| D27 | A Playground run on a sampled observe route. | A Playground run is always checked: the gate skips the observe sampling draw when `Exchange.AuditSelection` is `playground` (the `SampleRate` condition in `gate.go`). API-10 sets the selection (D12). | GD, API |
| D28 | Calibration signals §2.11 names that P3 does not produce: `calibration_warning` "calibrated with different state fields", and `calibration_missing`. | Deferred to the P4 calibration store (#42-46). In P3 a snapshot change shows as `uncalibrated_snapshot` (raw risk, capped to flag). SPEC erratum at I-3. | lead |

D20-D28 were added at the wave-4 checkpoint merges (CP-GS, CP-GD, CP-GH) to settle what the stream reviews asked of the lead.

---

## 2. Step 0 (lead, before the streams start)

### 0.1 Merge wave 3

1. Start from the verified wave-3 result. If wave 3 has not merged the three branches yet:
   - merge `feat/v2-p3-scen` into int3; only `mk/dx.mk` overlaps, in separate hunks;
   - then merge `feat/v2-p3-static`. Regenerate `frontend-react/src/routeTree.gen.ts` with `npm run build`; never fix it by hand.
2. Check `WhatWasCaught.tsx` by eye (static's `span` prop next to int3's 24 px targets).
3. Run `make config-docs`: p3-scen regenerated `docs/CONFIGURATION.md` but not `.env.example`.
4. Create `feat/v2-int4` from the result.

### 0.2 The seams commit (no behaviour change, about 400 LOC)

**a. `cmd/proxy/server.go`** (frozen after this commit)

1. **The `/v1` group.** Replace the `/v1` group (`:229-240`, from `h := s.gateway.handler` through the `/embeddings` route) with `s.mountV1(r, v1RateLimit, bodyLimit)`. New `cmd/proxy/v1_routes.go` (owner **GH**) holds:
   - that block, unchanged;
   - `v1.GET("/trustagent/decisions/:trace_id", s.handleDecisionLongPoll)`;
   - `const gatewayEnforces = false`.
2. **Enforcing flag.** `:128` `enforcing: false` becomes `enforcing: gatewayEnforces`.
3. **Calibration hooks.**
   - Call `s.initCalibration()` before `newPolicyServices`, which sets `s.calib`.
   - In `policyRuntime` (`:289`): `Calibrations: s.calibrationLookup()`.
   - In `snapshotChanged` (`:372`): `"calibration": s.calibrationState(c.Snapshot)`.
   - In `start()`: `s.startDecisionLoops(ctx, &s.wg)`.

   Stubs go in new `cmd/proxy/decision_wiring.go` (owner **GD**): `policy.NoCalibration{}`, `"none"`, and a loop that does nothing.
4. **Long-poll hooks.**
   - `s.waiters = newDecisionWaiters()`.
   - The `traceSink` literal gets `waiters: s.waiters`.
   - Stub `(*server).handleDecisionLongPoll` answers with `proxy.Deny(c, 404, codeNotFound, …)`, the same answer NoRoute gives today. It lives in new `cmd/proxy/decisions_longpoll_api.go` (owner **GD**).
5. **P3 REST hook.**
   - After `s.mountStats(...)`, call `s.mountP3API(r, api, apiGuards{…existing…, limitBy: rateLimitBy})`.
   - `apiGuards` (`policies_api.go:35`) gains `limitBy func(int, time.Duration) gin.HandlerFunc`.
   - Stub `mountP3API` in new `cmd/proxy/p3_api.go` (owner **API**) registers nothing.
6. **Status-event hooks.**
   - Move the body of `engineChanged` to `s.publishEngineStatus(h)`.
   - `start()` calls `s.startStatusWatch(ctx, &s.wg)`.
   - Both go in new `cmd/proxy/status_events.go` (owner **API**).
7. **Struct and constructor changes.**
   - `server` gets fields `pg *store.PostgresStore` (= `d.pg`), `calib policy.CalibrationLookup` and `waiters *decisionWaiters`.
   - `auditsAPI` gets `pub worker.Publisher` = `s.hub`.
   - `newGateway` receives `gatewayHooks{snapshotChanged: …, calibrations: s.calib}`.

**b. `cmd/proxy/gateway.go`** (owner **GH** afterwards)

- The `upstream.New(…)` block (`:164-170`) becomes `ups, err := newUpstreams(cfg, auth, log)`. New `cmd/proxy/upstreams.go` (owner **GS**) returns `&upstream.Set{OpenAI: c, Anthropic: c}` built from today's config, plus today's log line.
- `proxy.Options{Upstreams: ups, …}`.
- `gate.Config{…, Calibrations: hooks.calibrations, OnLate: sink.LateDecided}`.
- `gatewayHooks` gains `calibrations policy.CalibrationLookup`.

**c. `cmd/proxy/sink.go`** (owner **GD**)

- A `waiters *decisionWaiters` field.
- `AttemptDecided` and `LateDecided` methods that do nothing yet.

**d. `internal/gate`** (owner **GD** afterwards)

- **Exchange fields.** `Exchange` (`gate.go:99-113`) gains `Stream string`, `Attempt int`, `Unenforced bool`, `NoAudit bool`, `AuditSelection string` and `KnowledgeOnly bool`.
- **Wired now:**
  - `Meta.Stream = x.Stream`, defaulting to `"none"` (replaces the literal at `:193`);
  - `Decision.Attempt = x.Attempt`;
  - `NoAudit` skips `planAudit` (`:281`), so the selection is empty;
  - `Unenforced` sets the decision core's `Enforced=false` after `decide()`.

  `AuditSelection` and `KnowledgeOnly` get fields only here; GD-1 implements them.
- **Skip reasons.** The constants (`gate.go:31-38`) gain `SkipClientDisconnected`, `SkipMultiChoice`, `SkipUndecodable` and `SkipUnsupportedAPI`. `hasText` (`:116`) treats `client_disconnected` as no text.
- **Flags.** The constants (`types.go:28-32`) gain `FlagClientDisconnected`, `FlagLateDecision`, `FlagFallbackUsed` and `FlagMultiChoice`.
- **New types in `types.go`:**
  - `type Final struct{ Action, Delivered, Text string; Flags, Reasons []string }`. `Delivered` is `original`, `fallback` or `blocked`.
  - `Result.Final *Final`; nil means the decision's action and the original text.
  - `Upstream.TTFTMs *int64`.
- **Sink interface** (`sink.go:15`) gains:
  - `AttemptDecided(ctx, *Result)`: writes the attempt-N `gate_decisions` row only. No audit, no trace fields.
  - `LateDecided(ctx, *Result)`: writes the late row, the trace flag and a WS event. Never enforced, never audited.

  `NopSink` implements both.
- **Config** gains `Calibrations policy.CalibrationLookup` (nil means none) and `OnLate func(context.Context, *Result)`. Neither is used yet.

**e. `internal/upstream`** (owner **GS** afterwards)

- `type Set struct{ OpenAI, Anthropic, Fallback *Client; FallbackFormat string }`.
- `func (s *Set) For(family string) *Client`: Anthropic when the family is `anthropic` and that client is non-nil, else OpenAI.
- `func (c *Client) StreamBody(resp *http.Response, cancel context.CancelFunc) io.ReadCloser`: stub returns `resp.Body`.
- `func DecodeBody(resp *http.Response) io.ReadCloser`: stub returns `resp.Body`.

**f. `internal/proxy`**

- **`handler.go` (GH):** `Options.Upstream` becomes `Upstreams *upstream.Set`, with `h.up = o.Upstreams.OpenAI` so behaviour stays the same.
- **New `exchange.go` (GH):**
  ```go
  // flow is one gated /v1 exchange. Fields are append-only.
  type flow struct {
      c      *gin.Context
      format string              // llmapi format id
      req    *llmapi.ChatRequest // nil when unparseable
      raw    []byte              // client body
      x      *gate.Exchange
      route  *policy.Route
      up     *upstream.Client
      start  time.Time
      record context.Context     // context.WithoutCancel(request ctx)
  }
  ```
- **New `enforce.go` (GH):**
  - `func (h *Handler) decided(f *flow, res *gate.Result, up gate.Upstream, end time.Time, fin *gate.Final) string` fills `TotalMs`, `OverheadMs`, `Upstream` and `Final`, then calls `sink.GateDecided`. It is moved out of `finish` (`:413`).
  - `func blockOf(r *policy.Route) llmapi.Block` maps `policy.BlockSettings` (`policy/types.go:171-176`).
- **New `fallback.go` (GH):**
  - `type fallbackOut struct{ Action, Delivered, Text string; Body []byte; Response *llmapi.Response; Result1 *gate.Result; Flags, Reasons []string }`.
  - Stub `func (h *Handler) fallback(ctx context.Context, f *flow, res0 *gate.Result) *fallbackOut` returns block with reason `fallback_failed`.
- **New `stream.go` (GS):** stubs `func (h *Handler) serveStream(f *flow, resp *http.Response, body io.Reader)` (io.Copy) and `func (h *Handler) synthStream(f *flow, text string) io.Reader`.
- **`handler_test.go` `recSink`:** records `AttemptDecided` and `LateDecided` calls.

**g. New `internal/metrics/gateway.go`**

- Declares every missing Appendix B gateway series:
  - `requests_total{route,format,action}`, `overhead_seconds{route,mode}`, `upstream_seconds{format}`;
  - `gate_decisions_total{route,mode,action,status}`, `gate_seconds{stage}`, `gate_errors_total{kind}`;
  - `evaluator_levels_total{evaluator,level,stage}`, `detector_fired_total{detector}`;
  - `fallback_total{outcome}`, `uniform_samples_today`, `retention_lag_seconds`;
  - `spend_usd_today{kind}`, `spend_reserved_usd{kind}`.
- Helpers: `ObserveRequest`, `ObserveUpstream`, `ObserveGate`, `ObserveGateStage`, `GateError`, `EvaluatorLevel`, `DetectorFired`, `Fallback`, `SetUniformSamplesToday`, `SetRetentionLag`, `SetSpend`.
- `RouteLabel` caps the `route` label at 64 distinct values, then `other`.

### 0.3 Step-0 gate

```bash
cd backend-go && test -z "$(gofmt -l .)" && go vet ./... && go test -race -p 2 ./...
go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1
cd .. && make check && make test-e2e-static
```

---

## 3. GH: gateway handler (formats, routes, errors, block, fallback, turn-on)

- **Branch:** `feat/v2-w4-gh`.
- **Size:** L, about 3.5 days. This is the critical path.
- **Read first:** SPEC §2.12 (1770-1849); §2.9 decide and FAIL (1620-1660); §2.10 gated text and n>1 (1686-1724); §2.4 rows 71-74 (≈696-700); §3.1 `upstream` and `llmapi` rows (1867-1934); §4.2 P3 and the M-Demo gate (2340-2405); `docs/v2/design-notes/design-gateway.md`; p3-scen `contracts/README.md` "Demo scenarios".
- **Owns:**
  - `internal/proxy/{handler,headers,exchange,enforce,fallback,testfallback}.go` and their tests (`handler_test.go`, new `enforce_test.go`, `fallback_test.go`, `routes_test.go`);
  - `internal/llmapi/{anthropic,block,bodies,fallback,format,request,response,strip}.go` with `block_test`, `fallback_test`, `formats_test`, `llmapi_test`, `misc_test`;
  - `internal/policy/**`, `internal/evidence/**`;
  - `cmd/proxy/{v1_routes,gateway,policy_wiring,policies_api}.go`, `cmd/proxy/scenarios_e2e_test.go`, `cmd/trafficgen/**`;
  - `config/policies.yaml`, `config/policies.pending.yaml`, `contracts/demo/**`, `contracts/README.md`.
- **Consumes:** CP-GS before GH-6; CP-GD before GH-9.

### Tasks

1. **GH-1 (M), G3: one gated handler for every format.**
   - Replace `ChatCompletions` (`handler.go:194-248`) with `Serve`:
     - pick the format with `llmapi.FormatForPath` (`format.go:29`), parse with `ParseRequest` (`request.go:133`), forward with `ForwardBody`;
     - `newExchange` sets `APIFormat` (hard-coded at `:266`) and `TraceStart.Stream`;
     - `f.up = h.ups.For(llmapi.Family(format))` (`format.go:49`).
   - Branch on the **response**:
     - `text/event-stream` with 2xx → `h.serveStream(f, resp, f.up.StreamBody(resp, cancel))`;
     - `test_response` with `stream:true` → `h.serveStream(f, nil, h.synthStream(f, text))`;
     - anything else takes the JSON path with `ParseResponse` (`response.go:54`).
   - Delete the request-based stream divert (`:212-218`) and `StatusSkippedStream` (`headers.go:53`).
   - In `v1_routes.go`:
     - POST `/v1/chat/completions`, `/v1/completions` and `/v1/messages` → `h.Serve`;
     - `/v1/messages/count_tokens` → Passthrough on the Anthropic upstream;
     - `/v1/responses*` stays `Passthrough(skipped:unsupported_api)` on OpenAI.
   - `Passthrough` (`:167-191`) gets an upstream per path (one ReverseProxy per client) and a format-aware `test_response` check.
2. **GH-2 (S), G4: errors in the route's shape.**
   - `Deny` (`:149`) and `writeOpenAIError` (`:460`) write `llmapi.ErrorBody(FormatForPath(path), status, code, msg)` (`bodies.go:77`; `AnthropicErrorType` `:60`).
   - This covers ingest authz, the v1 limiter (`Retry-After` kept, `middleware/ratelimit.go:132`), the `/v1` NoRoute branch, `readError`, `testModeDisabled` (400, D8), `upstreamFailed` (`:442`) and the ReverseProxy error hook (`:105-112`).
3. **GH-3 (S), G5 JSON part.** `answer` (`:318-329`) uses `llmapi.SyntheticResponse(format, …)` (`bodies.go:133`) for all three formats.
4. **GH-4 (M), G6 plus D4, D5, D6: enforce on JSON.**
   - After `gate.Evaluate`, switch on `Action`:
     - `block` → `BlockJSON(format, blockOf(route), BlockTarget{TraceID, Model, Created, Upstream: body})` (`block.go:107`), with `BlockStatus` (`:54`) for the error style;
     - `fallback` → GH-6;
     - `pass`, `flag`, `observe` → the upstream bytes.
   - Record through `decided(…, &gate.Final{…})`.
   - `no_text` and non-2xx/transport answers get `x.Unenforced` and `Final.Action=pass`.
   - Oversize on block/fallback routes blocks: no `a.rest` relay.
   - n>1 → `FlagMultiChoice` (`MultiChoice` `response.go:132`).
   - `setDecisionHeaders` (`headers.go:63`) sends `Final.Action` as Action; Would-Action comes from the decision.
5. **GH-5 (S): metrics on the JSON path.** `ObserveRequest` and the overhead histogram.

   **→ CP-GH** (gate below). Frozen from here, for API and GS: `flow`, `newFlow`, `decided`, `blockOf`, `fallback`, `enforceJSON`.
6. **GH-6 (L), G7 plus D1, D2, D3: fallback and re-gate, after merging CP-GS.**
   - Build the request with `BuildFallbackRequest(format, raw, Fallback{Model, SystemPrompt, Evidence: trusted x.Evidence.Items, MaxTokens: 8000, CountTokens})` (`llmapi/fallback.go:79`).
   - Pick the upstream:
     - `same` = `f.up` with the client's headers;
     - `fallback` = `Set.Fallback`, after the `FallbackFormatCompatible` check (`format.go:62`). A mismatch becomes block with reason `fallback_format_mismatch`.
   - D1 scripted source in `testfallback.go`: reads `contracts.FS` once, maps id to `fallback.response`.
   - Before the call, extend the write deadline with `http.ResponseController` (G17).
   - Re-gate with `gate.Evaluate(ctx with route.DeadlineMs, &Exchange{same Trace/Policy/Route/Request/Evidence, Response: fbText, Attempt: 1, NoAudit: true})`.
   - Choose the outcome per D2, D3 and §2.12 steps 4-5.
   - Record in this order: `sink.AttemptDecided(res1)`; set `res0.Final{…, Flags: [fallback_used] when delivered}`; then `decided(res0)`, exactly once. The audit stays on the original answer.
   - Deliver the fallback JSON, or `BlockJSON`. Count `fallback_total{outcome}` with outcomes `delivered`, `on_fail_block`, `on_fail_flag`, `on_fail_original`, `failed`, `format_mismatch`.
7. **GH-7 (S): policy check for the fallback upstream.**
   - `policy.Env` (`load.go:57-65`) gains `FallbackUpstream bool`, set in `policy_wiring.go` from `cfg.FallbackUpstreamURL`.
   - `check.go:409` refuses `fallback.upstream: fallback` without it, as a field error.
8. **GH-8 (S): docs in code.** Rewrite the package doc (`handler.go:5-8`) to describe P3 behaviour, and document the frozen helpers for API.
9. **GH-9, the integration turn-on (I-2 in §11).**

### Tests

- **Formats (`routes_test.go`):**
  - `/v1/messages` and `/v1/completions` are gated for both `test_response` and an httptest upstream, and the stored `api_format` is right.
  - `count_tokens` reaches the Anthropic base.
  - `/v1/responses` returns `skipped:unsupported_api`.
  - The Anthropic error shape appears for ingest 401, v1 429 (with `Retry-After`), NoRoute 404 and `test_mode_disabled` 400.
- **Block (`enforce_test.go`):**
  - OpenAI message style patches every choice, sets `content_filter` and removes `tool_calls`.
  - Error style returns the configured status and `trustagent_blocked`.
  - Anthropic message and error styles; completions `text`.
  - `action_only` headers.
- **Fail matrix on closed block and fallback routes:**
  - `unparseable_request`, 2xx non-JSON, oversize, `overload` and `decision_model_off` all block.
  - Non-2xx, transport errors and `no_text` pass with `enforced:false`.
  - `sampled_out` is exempt.
- **Fallback (`fallback_test.go`):**
  - The upstream sees the CONTEXT prompt, `model gpt-4o`, `stream:false`, no `trustagent` and no `test_response`.
  - Format mismatch.
  - Re-gate outcomes: pass, would-block and timeout, each crossed with `on_fail` block, flag and original.
  - Fallback call failure (D2).
  - `AttemptDecided` gets `Attempt=1` with no audit; `GateDecided` runs once with `Final`.
  - The scripted scenario `payments-fallback` makes zero upstream calls.
  - The write deadline is extended.
  - `fallback_total` counts.
- **n>1:** flag `multi_choice`.
- **Policy (`check_test.go`):** `fallback.upstream: fallback` is refused without a URL.

### Gate

```bash
cd backend-go && test -z "$(gofmt -l .)" && go vet ./... \
 && go test -race -p 2 ./internal/proxy/... ./internal/llmapi/... ./internal/policy/... ./internal/evidence/... ./cmd/proxy/... ./cmd/trafficgen/... \
 && go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1   # still --phase p2: every non-p3 scenario passes
```

---

## 4. GS: gateway transport and streaming (config, upstreams, SSE relay and buffer)

- **Branch:** `feat/v2-w4-gs`.
- **Size:** L, about 3 days.
- **Read first:** §2.12 forwarding and streaming (1770-1823); §2.7 config (1093-1299: upstream keys); §3.1 `upstream` row; Appendix C B1, B2, M1, M2, M3, M12 (2973-3026); `design-gateway.md` streaming.
- **Owns:**
  - `internal/config/**`, `internal/upstream/**`, `internal/authz/**` (only if inject mode needs it);
  - `internal/llmapi/{sse,stream,writer,relay,synth}.go` with `sse_test`, `sse_fuzz_test`, `stream_test`, `writer_test`, `gin_test`, `example_test` and `testdata/`;
  - `internal/proxy/{stream,buffer}.go` with `stream_test.go` and `buffer_test.go`;
  - `cmd/proxy/{upstreams,main}.go`, `cmd/mock-upstream/**`.

### Tasks

1. **GS-1 (S), G1: bind the missing keys.**
   - Add fields to `config.go` (gateway group `:59-66`): `UpstreamAuthHeader`, `AnthropicUpstreamURL`/`APIKey`, `FallbackUpstreamURL`/`APIKey`/`Format` (enum `openai|anthropic`), `StreamIdleTimeout`, `CalibrationDir` (repopath), `CalibrationFamilyFallback`, `KnowledgeEnabled`/`Deadline`.
   - Validate them in `load.go`.
   - Change `Injecting()` (`config.go:206`) and the open-auth refusal (`load.go:564`) per D15.
   - Update `mask_test.go:170`.
   - The registry does not change.
2. **GS-2 (M), G2: named upstreams, inject mode and stream transport.**
   - `upstream.Config{APIKey, AuthHeader, IdleTimeout}`.
   - `OutboundHeaders` (`:101-134`): in inject mode, strip `Authorization`, `X-Api-Key` and `Api-Key`, then set `Bearer <key>` on `authorization` or the raw value on other headers. Keep `anthropic-version`. Never forward the proxy key.
   - `StreamBody`: an idle-timer reader that cancels after `STREAM_IDLE_TIMEOUT` between reads. `Do` applies the total timeout to JSON reads only.
   - `DecodeBody`: move the defensive gunzip in `ReadBody` (`:181-195`) here.
   - Keep the M2 URL join (`URL` `:80-92`).
3. **GS-3 (S): build the upstreams.**
   - `newUpstreams` builds `openai` (UPSTREAM_*), `anthropic` (ANTHROPIC_UPSTREAM_*, key on `x-api-key`) and the optional `fallback` (FALLBACK_UPSTREAM_*, with `FallbackFormat`).
   - Log each with a redacted URL and the passthrough/inject mode.
   - Count `upstream_seconds{format}`.

   **→ CP-GS.**
4. **GS-4 (S), G5 stream part.** `synthStream` = `llmapi.NewReader(EncodeEvents(SynthesizeStream(format, SynthStream{…})))` (`synth.go:53`). This fixes the bug where a `stream:true` test request gets a JSON body.
5. **GS-5 (L), G8: passthrough relay in `stream.go`.**
   - Up front: `CopyResponseHeaders`, then Route, Trace-Id, Request-ID, Source; `sw.DeclareTrailers` (`writer.go:78`) with the D9 set.
   - Relay: `asm.SetMaxBytes(MaxUpstreamResponseBytes)` (`stream.go:158`), then `Relay(rd, asm, sw)` (`relay.go:53`).
   - Flag, block and fallback routes gate synchronously with `x.Stream="passthrough"`; `decide()` downgrades to flag with `stream_not_buffered`. Then `sw.Finish(annotation unless stream_annotations: none, trailers)` (`:259`).
   - Observe routes: `setDeferredHeaders` (`headers.go:106`), terminal event not held, gate asynchronously through the handler's semaphore.
   - Failures:
     - `ErrEventTooLarge` → `io.Copy(sw, rd.Rest())` (`sse.go:309`) with `skipped:upstream_error`;
     - `asm.TooLarge()` or `asm.Err()` → `skipped:upstream_error`.
   - Disconnect (D7) with `res.ClientGone` (`relay.go:35`): when `asm.Finished()`, gate on `context.WithoutCancel` with `Unenforced` and flag `client_disconnected`; otherwise skip with `client_disconnected`.
   - Record `TTFTMs`. Gated text is `asm.Response().Gated()`. Record through `decided`.
6. **GS-6 (M), G9: buffer mode in `buffer.go`** (`route.Streaming == buffer`, `policy/load.go:332`).
   - `Collect(rd, asm, CollectOptions{MaxBytes, Keepalive: buffer_keepalive_s, Writer: sw})` (`relay.go:119`) with `x.Stream="buffer"`.
   - Not committed yet → decision headers; an error-style block may still set its status.
   - Committed → comment plus trailers, and a block is forced to message style: `sw.WriteEvents(format, BlockStream(format, blockOf(route), BlockTarget{…Response: asm.Response(), IncludeUsage}), ann)` (`block.go:150`, `writer.go:191`).
   - Fallback → `h.fallback(...)`, then `StreamFromResponse(format, out.Response, trace, created, IncludeUsage)` (`synth.go:70`).
   - Too large → write the events read so far, then `rd.Rest()`, ungated.
7. **GS-7 (S), G16/G17 stream parts.**
   - A trailer helper in `stream.go`: `setDecisionHeaders` into a scratch header, then `sw.SetTrailers` (`:279`).
   - `sw.ExtendWriteDeadline` (`:293`) on keepalives, on long relays and before the fallback call.
8. **GS-8 (S): end-to-end router tests, after merging CP-GH.** Stream requests for all three formats go through `h.Serve`. Count `requests_total` on the stream paths.

### Tests

- **Config:** each new key loads and validates; the format enum; D15 `Injecting()`; masking.
- **Upstream:**
  - The inject matrix: strip all three headers; Bearer versus raw header; passthrough untouched; `anthropic-version` kept; proxy key never forwarded.
  - `For` routing.
  - The idle timeout cancels a stalled stream but not a slow, steady one.
  - Gunzip of SSE.
  - URL join with a base path and query.
- **Stream:**
  - Events flush as they arrive; `[DONE]` or `message_stop` is held until the decision.
  - The `: trustagent {…}` comment comes before the terminal event.
  - Trailers are declared and set; a Go `http.Client` reads `resp.Trailer`.
  - On a block route: effective flag plus `stream_not_buffered`.
  - Observe gives deferred headers; `stream_annotations: none` gives no comment; `action_only` gives only the Action trailer.
  - Non-SSE 2xx takes the JSON path (M3); non-2xx SSE passes through.
  - Oversize event.
  - Disconnect after versus before finish.
  - `ttft`.
  - Anthropic parity.
- **Buffer:**
  - Keepalive commits the headers.
  - Block before versus after commit.
  - OpenAI block chunk sequence per §2.12, with the `usage` chunk only with `include_usage`.
  - Anthropic block event sequence.
  - Fallback via `StreamFromResponse` from a hand-built `fallbackOut`.
  - Too-large tail.
- **Synthesized:** a `stream:true` `test_response` yields valid SSE for all three formats.

### Gate

```bash
cd backend-go && test -z "$(gofmt -l .)" && go vet ./... \
 && go test -race -p 2 ./internal/config/... ./internal/upstream/... ./internal/llmapi/... ./internal/proxy/... ./internal/authz/... ./cmd/proxy/... ./cmd/mock-upstream/...
cd .. && make config-docs-check
```

---

## 5. GD: gate, decisions, calibration and sink

- **Branch:** `feat/v2-w4-gd`.
- **Size:** L, about 3.5 days.
- **Read first:** §2.9 compile step 6 and `too_large` (1538-1600, ≈1570), decide (1620-1660); §2.11 (1725-1769); §2.3 writers and retention (556-575); §2.4 row 74; §2.5 events (704-757); §7.2 block readiness (2614+); Appendix B (2953-2972).
- **Owns:**
  - `internal/gate/**`, new `internal/calibration/**`;
  - `internal/decision/compile.go` (only the `EvidenceCap` field; contract-owned);
  - `internal/jev/**`;
  - `internal/store/**`, except API's two new files;
  - `internal/metrics/**` except the frozen declarations in `gateway.go`;
  - `config/calibration/**`;
  - `cmd/proxy/{sink,decision_wiring,decisions_longpoll_api,status_api,evaluators_api}.go` with `sink_test.go`, `status_api_test.go`, `memstore_test.go`.

### Tasks

1. **GD-1 (S): exchange semantics.**
   - `AuditSelection` → `planAudit` (`gate/audit.go:14`) uses that selection, lane high, probability 1.
   - `KnowledgeOnly` → `Meta.KnowledgeOnly`.
   - Tests for the fields Step 0 wired: Stream, Attempt, NoAudit, Unenforced.
2. **GD-2 (M): `internal/calibration` (D14).**
   - Load `*.json` with `decision.ParseCalibrationFile` (`calibrate_file.go:288`) and `.Check` (`:313`).
   - Key by `EvaluatorTarget(...).Key()` (`:176`, `:140`) and snapshot, with the `SnapshotFamily` fallback (`:104`) when `CALIBRATION_FAMILY_FALLBACK` is set.
   - Implement `policy.CalibrationLookup` (`readiness.go:120`), setting `Stale`.
   - `Entries(route evaluators, params, snapshot) map[string]decision.CalibrationEntry` and `State(snapshot) (state, targets)`.
   - `Reload()` reports what changed.
3. **GD-3 (M), G15: fill calibration in the gate.** `gate.go:191` fills `in.Calibration` for the snapshot the Jev response reports, from `cfg.Calibrations`.
4. **GD-4 (S): calibration wiring.**
   - `decision_wiring.go`: `initCalibration` from `cfg.CalibrationDir`; `calibrationLookup`; `calibrationState`; `startDecisionLoops` polls every 30 s and publishes `calibration_updated{snapshot, imported}`.
   - `status_api.go:464`: `calibration_state` and `calibrated_targets`.
   - `evaluators_api.go:54`.
5. **GD-5 (M), G14: the sink.**
   - `decidedRecord` (`sink.go:166-189`): the trace action is `Final.Action`; add `Final.Flags` and `Final.Reasons`; `ttft_ms` from `Upstream.TTFTMs`.
   - `bodyRecord` (`:268`): `FinalResponseText = Final.Text`.
   - `gateDecision` (`:287`, `:336`): `Delivered` comes from `Final`.
   - `AttemptDecided`: new store method `RecordAttempt` writes the `gate_decisions` row only.
   - Add the method to the `Store` interface and to `memstore_test.go`.

   **→ CP-GD.**
6. **GD-6 (M), G11: late decisions.**
   - `call()` keeps the compiled requests and the `decision.Input`.
   - `lateDecision` (`gate.go:398-411`) merges the answers and sets `request_status[k]=ok`. When every timed-out request has resolved, it runs `decide()` again with `Late=true` for the same attempt and calls `cfg.OnLate`.
   - `LateDecided`: new store method `RecordLateDecision` upserts the `gate_decisions` row (`traces.go:233-240`), appends the trace flag `late_decision`, and does not go through `traceDecisionUpdate` (`:303-333`). It publishes `gate_decision` with `late=true` and notifies waiters.
7. **GD-7 (M), G12: `too_large`.**
   - Add `Compiled.EvidenceCap int \`json:"-"\`` (`compile.go:259`).
   - A 413 or a size-related 400 (`jev/classify.go:73-76`) halves the cap once, recompiles and resends within the same deadline.
   - A second failure sets `error:too_large` on the affected questions.
8. **GD-8 (M), G13: the long-poll in `decisions_longpoll_api.go`.**
   - A waiter hub plus a bounded LRU (4,096 entries, 10 min), fed by `GateDecided` and `LateDecided`.
   - Database fallback: `GetTrace` (`traces.go:518`) with a `key_label` check against `authz.KeyLabel(c)` (`authz.go:544`); a mismatch answers 404. Then `TraceDecisions` (`:548`).
   - `wait_ms` is clamped to 10,000. Response `{trace_id, state, decision}`.
   - With `action_only`, return only `action` and `status`.
   - Errors in the `/v1` shape.
9. **GD-9 (S): gate metrics.**
   - `gate_decisions_total`, `gate_seconds{stage}` (detectors, compile, jev, decide), `gate_errors_total`, `evaluator_levels_total`, `detector_fired_total`.
   - `uniform_samples_today` in `gate/audit.go`.
   - `spend_*` in `jev/spend.go`.
10. **GD-10 (M), D17: retention.**
    - New `internal/store/retention.go`: hourly adaptive batches (5,000, halving on timeout, `SET LOCAL statement_timeout='5s'`).
    - Rules from §2.3: traces and decisions and audits after `RETENTION_DAYS`; bodies at `expires_at` plus flag `body_expired`; audit text at `text_expires_at`; label samples; cost events.
    - `SetRetentionLag`. Started from `startDecisionLoops`.

### Tests

- **Calibration:**
  - The committed `config/calibration/mock__jev-heuristic-v1.json` parses and passes `Check`.
  - Its keys equal `EvaluatorTarget(groundedness@1)` and `EvaluatorTarget(contradiction@1)`.
  - The key matches the snapshot the mock backend reports.
  - Family fallback, stale, reload.
- **Gate (policytest):**
  - The payments-faq route with the mock calibration gives action `fallback`; without it, flag plus `uncalibrated_snapshot`.
  - Passthrough downgrade; `Attempt` 1 with no audit; `Unenforced`; `AuditSelection`.
- **Late decisions:** a fake Jev fires `OnLate`; the late row, flag and WS event appear, and the trace action is unchanged.
- **`too_large`:** recompile succeeds; a second failure gives `error:too_large`; TestVectors are unchanged.
- **Sink:**
  - `Final` becomes the trace action, final text, `delivered` and flags.
  - `AttemptDecided` leaves trace fields untouched.
  - `LateDecided`.
  - `ttft`.
- **Postgres (`TEST_DATABASE_URL`):** `RecordAttempt`, `RecordLateDecision`, retention batches.
- **Long-poll:** wake-up, timeout pending, key mismatch 404, unknown 404, `action_only`, database fallback, clamp.
- **Status:** `calibration_state: mock` and `calibrated_targets: 2` under demo config.
- **Metrics:** each gate series.

### Gate

```bash
cd backend-go && test -z "$(gofmt -l .)" && go vet ./... \
 && go test -race -p 2 ./internal/gate/... ./internal/calibration/... ./internal/jev/... ./internal/store/... ./internal/decision/... ./cmd/proxy/... \
 && go test ./internal/decision/... -run TestVectors
TEST_DATABASE_URL=postgres://truthtable:${POSTGRES_PASSWORD}@127.0.0.1:5432/truthtable?sslmode=disable go test -p 1 ./internal/store/...
cd .. && make vectors-ts
```

---

## 6. API: REST, WS and the Playground

- **Branch:** `feat/v2-w4-api`.
- **Size:** L, about 3.5 days.
- **Read first:** §2.4 (580-703) rows 2, 15-17, 49, 61, 66; §2.5 (704-757); §2.6 types (758-1092): `ReadyStatus`, `GateDecisionRow`, `ReliabilityResponse`, `PricingTable`, `PlaygroundRequest`/`Result`, `Recording`; §3.3 Playground (2002-2117).
- **Owns:**
  - `internal/worker/**`, `internal/websocket/**`, `internal/recording/**`, `cmd/export-replay/**`;
  - new `internal/store/{decisions_list,reliability}.go` (+ tests): methods on `*PostgresStore` only, not on the interface;
  - new `internal/proxy/playground.go` (+ test);
  - `cmd/proxy/{p3_api,gate_api,ready_api,pricing_api,export_api,playground_api,status_events,audits_api,traces_api,stats_api,kb_api}.go` and new tests for them;
  - new fixtures `PlaygroundResult.*`, `ReliabilityResponse.*`, `PricingTable.*`, `Recording.*`, `WsEvent.status.*`, `WsEvent.calibration_updated.*` (the last is shared with GD's event; API writes the file).

### Tasks

1. **API-1 (S), W1/W2: audit events.**
   - `Pool.Submit` (`worker/pool.go:171-185`) publishes `audit_queued{audit_id, lane, queue_depth}` with envelope `trace_id`.
   - `Pool.run` (`:286-287`) publishes `audit_started{audit_id, queue_wait_ms}` and persists trace `audit_status=running` via `SetTraceAudit`.
   - Fix the stale comment at `pool.go:8`.
2. **API-2 (S), D13: remove WS v1.**
   - Delete from `hub.go`: `:35`, `:64-73`, `:261-262`, `:305-307`, the protocol branches at `:335`, `:387`, `:435`, `:513-519` and `:576-582`.
   - Delete from `result.go`: `:247-248` and `v1Audit` (`:266-288`).
   - Update `hub_test.go:288-301`.
   - A handshake without `protocol=2` gets 400 before upgrade.
3. **API-3 (S), #2:** `handleReady` maps `statusReporter.system()` (`status_api.go:263`, read-only use) to `ReadyStatus`; `strict=1` and not ready → 503.
4. **API-4 (M), #16:** `ListDecisions(ctx, DecisionFilter)`.
   - Keyset paging as in `ListTraces`.
   - `WHERE attempt=0`.
   - `evaluator_levels` = `jsonb_object_agg` over `decision->'evaluators'`.
   - `audit_verdict` through `traces.audit_id`.
   - Every filter (`evaluator` and `level` via jsonb, `disagreement`, `late`, `since`, `updated_since`).
   - Reuse `queryLimit`/`queryTime`/`queryEnum`/`newPage` (`traces_api.go:31-38,97-127`) and indexes `idx_gd_updated`/`idx_gd_created`.
5. **API-5 (M), #17:** `ReliabilitySamples` from human labels (0003:53-59) and uniform audits (`gate_decisions.decision->'evaluators'->ref->>'risk_raw'` joined to `audits` with `selection='uniform'`, excluding mock and test).
   - Compute with `decision.Reliability` (`calibrate.go:348`).
   - `evaluator` missing → 400. Report `n_by_source`.
6. **API-6 (S), #49:** serve `contracts.FS pricing/prices.v1.json` as `PricingTable` (the same file `jev/cost.go:127` reads).
7. **API-7 (S-M), #66 (D11):**
   - Move `dbSource` (`cmd/export-replay/source.go:45-106`) to `internal/recording/dbsource.go`; the CLI uses it.
   - The handler requires admin; `since`, `until`, `limit≤200`, `source`.
   - Reuse `recording.Build` (`:114`) and `Sanitizer` (`sanitize.go:67,236`).
8. **API-8 (S-M), #15:** `audits.direct` (`audits_api.go:157`) records a trace (source `direct`, api_format `direct`, gate_status `skipped`, route from the request or `direct`) through `StartTrace`, publishes `trace_started`, and links the audit id. Map 503 `no_judge` from `judgeState` (`status_api.go:359`).
9. **API-9 (S), W4:** `publishEngineStatus` emits `status` for engine, judge and knowledge on transitions. `startStatusWatch` polls `jevStatus()` every 5 s for decision-model breaker and budget transitions.
10. **API-10 (L), #61 (D12). Merge CP-GH, CP-GS and CP-GD first.**
    - `Handler.Playground(ctx, c, PlaygroundInput, onToken)` in `playground.go`:
      - build the flow with `newFlow`; force the mode (the admin override);
      - `AuditSelection: "playground"`, source `playground`;
      - audit mode answers through the `test_response` path with `upstream=null`;
      - chat mode needs `ups.For(...)` in inject mode, else 503 `credentials_unavailable`;
      - `enforceJSON` and `fallback` produce `delivered_text`;
      - headers come from `setDecisionHeaders` (`headers.go:63`).
    - `playground_api.go`:
      - JSON `PlaygroundResult`, or SSE `token`→`decision`→`done` or `error` over `llmapi.StreamWriter` (`writer.go:58`, `ExtendWriteDeadline` `:293`);
      - errors 422 (`validateDirect`, `audits_api.go:220`), 403, 429, 502, 503;
      - mounted with `writable`, `admin`, `limitBy(10, time.Minute)` and `bodyLimit`.

### Tests

- **Events:**
  - `audit_queued` and `audit_started` payloads match `WsEvent.audit_queued.json` and `WsEvent.audit_started.json`.
  - `running` is persisted.
  - No v1 frames; handshake 400.
- **Endpoints:**
  - Ready: ready, not ready, strict.
  - #16: paging, every filter, attempt-0 only, validates against `Page-GateDecisionRow.recent.json`, 503 without a database.
  - #17: 400, the sources, bins.
  - #49 shape.
  - #66: admin only, limit, sanitized, CLI tests still pass.
  - #15: the trace appears in `GET /api/traces` and `/:id`.
- **Status events:** fire once per transition.
- **Playground:**
  - 422; audit mode with `mode_override` block gives the block text; chat mode without a credential gives 503; chat mode with an httptest upstream; the override bypasses `client_mode: none`; `scenario` only in TEST_MODE.
  - SSE event order; error event; trace source `playground` and audit selection `playground`; the limit is counted once; readonly 403.
  - Every new fixture validates.

### Gate

```bash
cd backend-go && test -z "$(gofmt -l .)" && go vet ./... \
 && go test -race -p 2 ./internal/worker/... ./internal/websocket/... ./internal/recording/... ./internal/proxy/... ./cmd/proxy/... ./cmd/export-replay/...
TEST_DATABASE_URL=… go test -p 1 ./internal/store/... ./cmd/export-replay/...
cd .. && make contracts-check contracts-test
```

---

## 7. WEB: dashboard P3 gaps and the stack specs

- **Branch:** `feat/v2-w4-web`.
- **Size:** M-L, about 2.5 days.
- **Read first:** §3.3 (2002-2117); §2.9 "Outcome for the UI" (≈1670-1685); §2.6 `TraceDetail`, `GateDecisionView`, `PlaygroundResult`; `frontend-react/DESIGN.md`.
- **Owns:**
  - `frontend-react/**`, except `playwright.config.ts`, `package.json` and `package-lock.json`;
  - `contracts/ui/**`;
  - new fixtures `contracts/fixtures/api/TraceDetail.payments-fallback.json` and `TraceDetail.help-center-link-block.json`, derived by hand from the scenario `expect` blocks and validated by `contracts-check`.

### Tasks

1. **WEB-1 (M): what the user received.**
   - `traceView.ts:78` exposes `delivered` and `deliveredText` from `trace.action`, `body.final_response_text` and `audit_detail.gate.delivered`.
   - `AnswerSection.tsx` gets a "What the user received instead" block for fallback and block. Claim marks stay on the original.
   - `OutcomeCard` wording.
   - Reuse `ResultTimeline.tsx:150-168`, `outcomes.v1.json:65` and `presentation.ts:148`.
2. **WEB-2 (M): detector marks.**
   - Mark `decision.detectors[].findings{start,end}` (UTF-16) on `response_text`.
   - Extend `domain/segmentClaims.ts` so claim and detector spans can overlap or nest.
   - Icon plus underline style, never colour alone. The tooltip uses the `det:*` reason copy.
   - Remove the "Detector marks follow in P3" doc line (`AnswerSection.tsx:6`).
3. **WEB-3 (M): Details.**
   - `TraceFacts.tsx` gets a custom-SVG timing waterfall (upstream, ttft, gate, overhead).
   - Response headers from new `domain/decisionHeaders.ts`, mirroring `headers.go:63-120` including `action_only`.
   - Raw `decision/v1` for each attempt, collapsed.
   - An attempts list, with attempt 1 labelled "Fallback check".
   - Flag words: `fallback_used`, `multi_choice`, `client_disconnected`, `late_decision`.
   - Keep "Show exact Jev request" as P4.
4. **WEB-4 (S): wording.**
   - `contracts/ui/outcomes.v1.json` reasons `fallback_failed` and `fallback_format_mismatch` (D19).
   - Flag and late wording in `copy.v1.json`.
   - Everything goes through `presentation.ts`.
5. **WEB-5 (S): Playground.**
   - `PlaygroundPage.tsx:50` `NOT_SERVED_REASON` and `:52-65` `RunNotServed` become a generic "older proxy" 404 state.
   - `playgroundStore.ts:21` comment.
   - The timeline shows `delivered_text` and `headers`. msw data is local to the tests.
6. **WEB-6 (S): banner and cache fixes.**
   - `useSystem.ts:59-63` passes `enforcingRoutes` (`banner.ts:23-24`) from `effective_routes` whose mode is block or fallback.
   - `applyControlSideEffects` (`queries.ts:302-331`) invalidates `qkc.evidence` on hello and resync.
   - Stale comments: `OutcomeCard.tsx:3-4`, `TopBar.tsx:18-19`, `traceView.ts:110` (evaluator names from `GET /api/evaluators`).
   - Checks show "Arrived late, not enforced" when `decision.late`.
7. **WEB-7 (S): the static site after the merge.**
   - `/app/setup` goes through `StaticRouteGuard` and `STATIC_APP_PAGES` (`vite.site.ts`).
   - No session or setup probe in the static build (`replayDataSource.ts:268`).
8. **WEB-8 (M): stack specs (D18).**
   - New `e2e/stack/fallback.spec.ts`, `describe('fallback')`:
     - a Traffic row shows Replaced and `refund-timing.md`;
     - the detail shows "5 to 10 business days" as delivered;
     - after the audit, "2 business days" is marked Contradicted;
     - axe check.
   - `trace-detail.spec.ts`: a test titled "inline claims" covering claim marks, plus the detector mark `urls_not_in_evidence` on `help-center-link-block`.
   - `policy-edit.spec.ts`: split out a test titled "rollback".
   - `policy-edit.spec.ts:55-58` and `trace-detail.spec.ts:33` throw in CI, like `stack.ts:34-37`.
   - Move `signInAsAdmin` into `stack.ts`, preferring `TRUSTAGENT_ADMIN_KEY`; the setup link works once, so it breaks under retries.

### Tests

vitest covers every item above, including emoji and CJK spans for the detector marks (the pattern of 82c291e). Playwright specs compile (`--list`). The static specs run.

### Gate

```bash
cd frontend-react && npm ci && npm run lint && npx tsc -b && npx vitest run --maxWorkers=2 \
 && npm run build && npm run build:static && npm run size && npx playwright test --list
cd .. && make gen-types-check gen-api-schema-check gen-policy-validator-check contracts-check && make test-e2e-static
```

---

## 8. DX: make, CI, doctor, observability and docs

- **Branch:** `feat/v2-w4-dx`.
- **Size:** L, about 3.5 days. It is not on the critical path.
- **Read first:** §3.4 (2118-2167); §3.5 (2168+); §4.2 W-DX and the M-Demo gate (2340-2405); Appendix B (2953-2972); §6 "Verify: make doctor" (2516+).
- **Owns:**
  - `Makefile`, `mk/**`, `scripts/**` (including `scripts/load/**`), `.github/workflows/**`;
  - `docker-compose*.yml`;
  - `frontend-react/{playwright.config.ts,package.json,package-lock.json}` and `frontend-react/nginx/**` (no change expected);
  - `backend-python/**` (metrics only);
  - `config/grafana/**`, `config/prometheus.yml`;
  - `docs/**` except `docs/v2/SPEC.md`; `README.md`, `CHANGELOG.md`, `backend-go/README.md`.

### Tasks

1. **DX-1 (S): the setup URL.**
   - `make demo` runs `$(DEMO_COMPOSE) exec -T proxy /proxy -new-setup-token`, parses the `SETUP: open ` prefix (`authz/setup.go:23`), prints it and opens it with `scripts/open_url.py`, respecting `OPEN=0`.
   - When an admin already exists (`cli.go:118-120`), it opens the dashboard instead.
   - New `demo-setup-url` prints only the URL.
   - Fix `setup-url` (`mk/dx.mk:73-74`), which uses the wrong project.
2. **DX-2 (S), G13: `make check` matches CI.** Add `node scripts/check-forbidden.mjs` (with `--self-test`), `gen-types-check`, `gen-policy-validator-check`, `gen-api-schema-check`, `tsc -b`, `build`, `build-static`, `size-landing` and `recording-check`, run one after another.
3. **DX-3 (S): static Playwright server.** `playwright.config.ts` starts `vite preview --mode static` when `process.argv` selects `--project=static` (or `PLAYWRIGHT_STATIC_PREVIEW=1`), so the literal gate command works. Keep `reuseExistingServer` off in CI.
4. **DX-4 (S-M): `ci-e2e.yml`.** Keep the job names "DX scripts" and "E2E (compose stack)".
   - Before `make demo`, write a random `TRUTHTABLE_ADMIN_KEYS` into `.env` and export `TRUSTAGENT_ADMIN_KEY`.
   - Replace `--scenarios s0` (`:115-116`) with the literal gate command.
   - Replace `-g skeleton` (`:134-138`) with the M-Demo grep, and keep the skeleton run too.
   - Add `doctor --expect demo`.
   - After `make demo-clean`, run `make e2e-up E2E_BUILD= && make e2e-scenarios E2E_PHASE=p3 && make e2e-down`.
5. **DX-5 (M): `scripts/doctor.py`.**
   - Replace the S0 skeleton header (`:12-27`).
   - New checks read `GET /api/status`:
     - `decision_model`: backend, reason, snapshot, state, calls and cost;
     - `policy`: version, source, file_status and file_error, downgrades;
     - `calibration`: with `--expect demo`, `calibrated_targets ≥ 2`;
     - `setup` via `/api/setup/status`;
     - `metrics` for the docker target: `:8002` exposes `trustagent_gateway_*` and `:8001` exposes `trustagent_engine_*`.
   - Tests in `scripts/tests/test_doctor.py`.
6. **DX-6 (M), M11 Python.**
   - `backend-python/src/truthtable/metrics.py:7-39` moves to `trustagent_engine_*`; `claims_total` is labelled `verdict`.
   - Add `stage_duration_seconds{stage}`, `judge_requests_total`, `judge_tokens_total`, `judge_cost_usd_total`, `judge_fallback_active`, `rpc_inflight{method}`, `gate_vs_audit_total{gate_action,audit_verdict}` and `kb_ingest_claims_total{status}`.
   - Instrument them at `grpc/server.py:46-51, 183, 409-634`.
   - Add a unit test that every name starts with `trustagent_engine_`.
7. **DX-7 (M), F15 Grafana.**
   - Rewrite all 13 queries in `config/grafana/dashboards/trustagent.json` with `{job="proxy"}` and `{job="engine"}` and the new names.
   - New `scripts/tests/test_grafana.py` asserts every expression has a `job` filter and uses only `trustagent_gateway_` or `trustagent_engine_` names.
8. **DX-8 (M): docs.**
   - New `docs/GATEWAY.md`: modes, headers and trailers (D9), streaming limits, fail semantics (D4, D5), block readiness, fallback (D1-D3), long-poll, inject mode (D15), error shapes, test mode (D8).
   - New `docs/EVALUATORS.md`, short.
   - `README.md` quick start: `make demo`, Jev, the new metric names; remove the legacy `/api/audits` text at `:215`.
   - Rewrite `docs/DEMO.md`.
   - `backend-go/README.md:241-343`; the note at `docs/WALKTHROUGH.md:252`; the `docs/README.md` index.
9. **DX-9 (S): `CHANGELOG.md`.**
   - "Breaking: metric renames" with an old-to-new table.
   - WS v1 removed (D13); `POST /api/upload` removed.
   - `/v1` now enforces block and fallback; `/v1/messages` and `/v1/completions` are gated.
   - trafficgen's default phase is p3 and `--allow-skip` exists; `test_mode_disabled` is 400.
10. **DX-10 (M; may slip to P5 without blocking M-Demo): load test.**
    - `scripts/load/gateway-overhead.js`: a k6 Trend read from `X-TrustAgent-Overhead-Ms`, scenarios a-f per `design-gateway.md` §14.
    - A load compose override with upstream and Jev on mock-upstream (`-latency`, `-jev-latency`, `-chunk-delay`, fault `hang`).
    - A `load-test-gate` target.
11. **DX-11 (S): the full `make check` on the DX branch.**

### Gate

```bash
make lint-scripts test-scripts compose-check config-docs-check
node scripts/check-forbidden.mjs --self-test && node scripts/check-forbidden.mjs
cd backend-python && poetry run ruff check . && poetry run black --check . \
 && poetry run mypy src/truthtable/scoring src/truthtable/runtime src/truthtable/text src/truthtable/providers \
 && poetry run pytest tests/unit -q
cd .. && make check     # DX-11 only
```

---

## 9. Ownership matrix (after Step 0)

| Path | Owner |
|---|---|
| `cmd/proxy/server.go`, `harness_test.go`, `wiring_test.go`, `api_test.go`, `e2e_test.go` | frozen (lead, integration only) |
| `cmd/proxy/{v1_routes,gateway,policy_wiring,policies_api}.go`, `scenarios_e2e_test.go` | GH |
| `cmd/proxy/{upstreams,main}.go` | GS |
| `cmd/proxy/{sink,decision_wiring,decisions_longpoll_api,status_api,evaluators_api}.go`, `sink_test.go`, `status_api_test.go`, `memstore_test.go` | GD |
| `cmd/proxy/{p3_api,gate_api,ready_api,pricing_api,export_api,playground_api,status_events,audits_api,traces_api,stats_api,kb_api}.go` and their new tests | API |
| `cmd/proxy/{session_api,setup_api,keys_api,cli,internal_token}.go` | nobody |
| `internal/proxy/{handler,headers,exchange,enforce,fallback,testfallback}.go` + tests | GH |
| `internal/proxy/{stream,buffer}.go` + tests | GS |
| `internal/proxy/playground.go` + test | API |
| `internal/llmapi/{anthropic,block,bodies,fallback,format,request,response,strip}.go` + tests | GH |
| `internal/llmapi/{sse,stream,writer,relay,synth}.go` + tests, `testdata/` | GS |
| `internal/{config,upstream,authz}/**`, `cmd/mock-upstream/**` | GS |
| `internal/{policy,evidence}/**`, `cmd/trafficgen/**` | GH |
| `internal/{gate,calibration,jev}/**`, `internal/decision/compile.go` (`EvidenceCap` only), `internal/store/**` except the next row, `internal/metrics/**` except the frozen `gateway.go` declarations | GD |
| `internal/store/{decisions_list,reliability}.go` + tests (new), `internal/{worker,websocket,recording}/**`, `cmd/export-replay/**` | API |
| `config/policies*.yaml`, `contracts/demo/**`, `contracts/README.md` | GH |
| `config/calibration/**` | GD |
| `config/grafana/**`, `config/prometheus.yml` | DX |
| `contracts/ui/**` | WEB |
| `contracts/fixtures/api/**` | append-only, new files by the endpoint owner |
| `contracts/superseded.v1.json`, `contracts/schema/**`, `contracts/config/**`, `docs/v2/SPEC.md` | lead (integration) |
| `frontend-react/**`, except the next row | WEB |
| `frontend-react/{playwright.config.ts,package.json,package-lock.json,nginx/**}` | DX |
| `backend-python/**`, `mk/**`, `Makefile`, `scripts/**`, `.github/**`, `docker-compose*.yml`, `docs/**`, `README.md`, `CHANGELOG.md`, `backend-go/README.md` | DX |

---

## 10. Dependencies and critical path

```
Step 0 ─┬─ GS-1..3 ──► CP-GS ─┬──────────────► GH-6 (fallback) ─► GH-7/8 ─┐
        │                     └► GS-4..7 ─► (CP-GH) GS-8 ────────────────┤
        ├─ GH-1..5 ──► CP-GH ─┬► GS-8, API-10 ───────────────────────────┤
        ├─ GD-1..5 ──► CP-GD ─┴► API-10, GH-9 ───► GD-6..10 ─────────────┤
        ├─ API-1..9 ─────────────────────────────► API-10 ───────────────┤
        ├─ WEB-1..8 ─────────────────────────────────────────────────────┤
        └─ DX-1..11 ─────────────────────────────────────────────────────┴─► Integration
```

- **Critical path:** Step 0 (0.5 d) → GH-1..5 (1.5 d) → GH-6..8 (1.5 d; CP-GS lands around day 1) → I-1..I-3 (0.5 d) → I-4..I-6 (1 d). That is about **5 working days**.
- **In parallel:** API-10 starts at CP-GH (about day 2); GS, GD and API end around day 3.5.

---

## 11. Integration (lead, on `feat/v2-int4`)

1. **I-1: merge.** Merge in this order: GS → GD → GH → API → WEB → DX. After each merge, run that stream's gate. A conflict means an ownership violation; send the work back to the owner.
2. **I-2: turn-on commit.** The GH agent does this, touching GH-owned files only.
   - `v1_routes.go`: `gatewayEnforces = true`.
   - Move `payments-faq` and `help-center` from `config/policies.pending.yaml` into `config/policies.yaml`. `handbook` stays pending. Update the header comments of both files.
   - `cmd/trafficgen/scenario.go:136`: `defaultPhase = "p3"`; update `phase_test.go`.
   - D18: `--expect` exits 1 on a phase skip unless `--allow-skip`, in the run loop of `main.go`, with tests.
   - `cmd/proxy/scenarios_e2e_test.go`:
     - the demo environment sets `cfg.CalibrationDir` to the repo's `config/calibration`;
     - `runScenarios` expects every selected scenario to pass: demo set = 26 (6 of them p3), e2e set = 23, zero "skipping".
   - `contracts/README.md`: the Policies and Phases paragraphs (≈195-211).
   - Gate:
     ```bash
     go test ./cmd/proxy -run 'TestDemoScenariosEndToEnd|TestE2EScenariosAgainstMockUpstream' -count=1
     go test ./cmd/trafficgen/... ./internal/policy/...
     make contracts-check
     ```
3. **I-3: end-of-P3 deletions (lead).**
   - `server.go`: remove `api.POST("/upload", …)` (`:214`) and fix the `serveWS` comment (`:340`).
   - `server.go`: the `/v1` NoRoute branch sends `X-TrustAgent-Trace-Id` and `X-Request-ID`, which §2.12 lists as "Always" (GH review). In the same commit, `TestV1ErrorShapesOnTheServer` expects them on its 404 (`traced` true).
   - `contracts/superseded.v1.json`, error level: `ProtocolV1`, `v1Frame`, `audit_result`, and `\btrustagent_(?!gateway_|engine_)[a-z]`. Promote `?api_key=` and `VITE_TRUTHTABLE_API_KEY` to error.
   - SPEC errata:
     - D8: the §2.4 403 list;
     - D6: add `multi_choice` to the flags list at line 409;
     - D10, D11, D12, D13, D14, D15, D17 and D19;
     - `CalibrationLookup` doc: "P3 file lookup".
     - D20, D25 and D28 (checkpoint decisions);
     - §2.11: `mock/*` calibrations also load when `TEST_MODE` runs an explicit mock backend, not only under `DEMO_MODE` (GD review);
     - §2.5: a handshake without `protocol=2` gets 400, not v1 frames (D13).
   - Run `node scripts/check-forbidden.mjs`.
4. **I-4: local gates, one after another.**
   ```bash
   cd backend-go && test -z "$(gofmt -l .)" && go vet ./... && go test -race -p 2 ./... && go test ./internal/decision/... -run TestVectors
   TEST_DATABASE_URL=… go test -p 1 ./internal/store/... ./cmd/export-replay/...
   go -C backend-go test -race ./internal/authz/... -run 'Scope|Ingest|B1|CSRF|Setup'
   cd backend-python && poetry run ruff check . && poetry run black --check . && poetry run mypy src/truthtable/scoring src/truthtable/runtime src/truthtable/text src/truthtable/providers \
     && poetry run pytest tests/unit -q && poetry run python -m evals.run_eval --dataset golden_v2 --provider mock --check-baseline
   cd .. && make check && make vectors-ts
   ```
5. **I-5: Docker proof.** Run the M-Demo gate (§12), then the compose e2e run, then `make doctor ARGS="--expect demo"`.
6. **I-6: independent verifier** (read-only agent). It checks:
   - trafficgen prints `ok` for all 26 and never "skipping";
   - `payments-fallback` delivered "5 to 10 business days" with Action fallback, and the stored trace has attempt 0 and attempt 1;
   - stream trailers are present;
   - `curl /v1/messages` with a bad key returns an Anthropic-shaped 401;
   - no `test.skip` path runs in CI;
   - `GET /metrics` shows only `trustagent_gateway_*` and `trustagent_engine_*`;
   - WS without `protocol=2` gets 400;
   - `/api/gate/decisions`, `/api/gate/reliability`, `/health/ready`, `/api/pricing`, `/api/export/replay` and `/api/playground/run` answer per their fixtures;
   - the static site makes no off-site requests.
7. **I-7: finish.**
   - Merge `feat/v2-int4` into `feat/v2-jev-gateway`.
   - Rebuild the owner's local demo with `make demo-clean && make demo`.
   - Take screenshots: Traffic (Replaced), trace detail (sent instead, detector marks), Playground, Policies.

---

## 12. M-Demo gate (exact commands, from the repo root)

```bash
make demo-clean
make check
make demo OPEN=0 && docker compose -p truthtable-demo exec -T proxy /trafficgen --once --expect
#   default --scenarios demo, --phase p3: 26 × "ok", 0 × "skipping", exit 0 (incl. payments-fallback with the mock calibration)
export TRUSTAGENT_SETUP_URL="$(make -s demo-setup-url)"      # or TRUSTAGENT_ADMIN_KEY from TRUTHTABLE_ADMIN_KEYS
cd frontend-react && npx playwright test --project=stack -g "trace detail|inline claims|policy edit|rollback|fallback"
VITE_APP_MODE=static VITE_BASE_PATH=/TrustAgent/ npm run build && npx playwright test --project=static -g "replay|landing"
cd .. && python3 scripts/doctor.py --target docker --expect demo
make demo-clean && make e2e-up E2E_BUILD= && make e2e-scenarios E2E_PHASE=p3 && make e2e-down
```

At the end of the phase these must also hold:
- WS v1 and the legacy `/api/audits` shape are gone, and the `superseded` entries are at error level.
- Metrics are renamed (M11) and Grafana uses `job` filters (F15).
- `ci-e2e` runs all of the above.

---

## 13. Deferred

**P4**
- **REST not in P3:**
  - #12 `decision-request` ("Show exact Jev request").
  - #30-33 evaluator validate and writes; #34-38 Studio.
  - #39-41 labels. Until then #17's `human` source is empty.
  - #42-46 calibration API, import and fit; the file lookup lands now (D14).
  - #48 `roi/timeseries` and the Savings tab; #51 evidence suggestions.
  - #52-60 settings, credentials, PKCE and the decision-model test.
  - #70 the admin audit log.
  - The #10 `reason` filter.
- **WS:** `credentials_changed` and `settings_updated`.
- **Web:** cross-origin dashboard auth (key header and `ws-ticket`).
- **Knowledge on the hot path (G21):**
  - a Go `RetrieveContext` client, started alongside the upstream call, and `KnowledgeOnly`;
  - the `handbook` route and the `knowledge-only-capped` scenario.
- **Playground:** real token streaming (D12).
- **Store:** the async ordered trace writer and the in-memory LRU trace fallback.
- **Spend:** `budget_exhausted` on `POST /api/audit` and the persisted spend ledger.

**P5**
- The k6 `gateway-overhead.js` and `load-test-gate`, if DX-10 slips.
- `make record-demo` with a real recording, and a zip bundle for #66.
- Rewrites of `WALKTHROUGH.md`, `ARCHITECTURE.md`, `PLAN.md` and `CI-CD.md`.
- Vendor verification of the landing Compare table (the dated TODO cells).
- The `VERSION` bump and release, followed by `make recording`.

---

## 14. Risks and how the plan handles them

- **Calibration key mismatch.** The mock calibration must key to the snapshot the mock backend reports. GD's test pins it; the I-2 in-process demo test proves it before Docker.
- **Trailers.** gin carries them (`llmapi/gin_test.go`). nginx drops them, so the dashboard never depends on them; trafficgen talks to the proxy directly.
- **Write deadlines.** `/v1` has the upstream timeout plus 30 s. Buffer keepalives and the fallback call extend it (GS-7, GH-6).
- **Checks that pass without testing anything.** Closed by D18, the CI throws, and the exact 26/23 counts in I-2.
- **Machine memory.** Package-scoped gates, `-p 2`, at most two heavy gates at once, and Docker only in I-5.