# TrustAgent Gateway (Go, `backend-go/`): design spec for the Jev gate, policy engine and handler rewrite

Nothing in the repo was changed. The spec rests on the four codebase reports, the Jev research files and a re-read of these files: `handler.go`, `main.go`, `pool.go`, `hub.go`, `grpc/client.go`, `config.go`, `middleware/*`, `store/*`, `metrics.go`, `proto/evaluator.proto`, `grpc/server.py` and the compose/env files. Every bug B1–B6 and M1–M12 was confirmed against the current source. One detail was checked in the module cache: gin v1.9.1's `responseWriter` has `Unwrap()`, so `http.ResponseController` works through `c.Writer`.

---

## 0. Decisions and assumptions

| # | Decision |
|---|---|
| D1 | **Three-stage inline gate.** Tier-0 is deterministic Go checks (<2 ms). Jev is one HTTP call with one question set. The deep LangGraph audit stays async. The gate never calls the Python engine on the hot path, except an optional KB lookup that runs in parallel with the upstream call. |
| D2 | **Jev transport is raw `net/http`**, one adapter for all backends. The OpenRouter Go SDK would force `go 1.25.10`; this repo is `1.25.0`. |
| D3 | **Primary gate signal is a noul, P(grounded).** A 5-option `relation` choice is the second signal and supplies the verdict label (keeps CONTRADICTED separate from UNSUPPORTED). An optional score question is for display only. |
| D4 | **No groundedness question without context.** Jev's answers invert when there are no sources. Tier-0 reports `UNGROUNDED_NO_CONTEXT`, and the policy's `no_context` setting decides the band. |
| D5 | **Thresholds apply to a calibrated P(grounded).** Calibration is kept per snapshot and per question-set hash. Block and fallback actions are capped to flag while the returned snapshot is uncalibrated (`require_calibration: true` is the default). |
| D6 | **Policies live in YAML.** Priority order: database versions, then the file, then built-in defaults (observe everywhere). Hot reload by polling. `gopkg.in/yaml.v3` is already in `go.sum` as indirect and becomes direct. |
| D7 | **Anthropic `/v1/messages` is in scope:** non-streaming, plus streaming in `observe_only` and `buffer` modes, and a `count_tokens` passthrough. Out of scope: Bedrock, Vertex, and per-window streaming gating (deferred). |
| D8 | **The proxy's own key moves to `X-TrustAgent-Key`.** The upstream key can optionally be injected by the gateway ("virtual key" mode). |
| D9 | **WebSocket protocol v2:** one message per frame, sequence numbers, a replay ring buffer, gap markers. The React redesign ships in the same change, so there is no v1 compatibility layer. |
| D10 | **Metrics split by owner:** `trustagent_gateway_*` in Go and `trustagent_engine_*` in Python. Go stops emitting the audit-quality metrics. |

Assumptions (marked **[A]** where they appear below):
- **A1.** The OpenRouter `/api/alpha/decisions` and `/api/v1/systemone` routes accept the same body. This is documented but was not tested live.
- **A2.** OpenRouter's limit is 32k tokens combined. The state budget defaults to 24k estimated tokens.
- **A3.** Tokens are estimated at 4 characters each (the tokenizer is not published). The estimate self-corrects from `usage.input_tokens`.
- **A4.** The 1,200 RPM limit may also apply on OpenRouter. The gateway budgets 1,000 RPM.
- **A5.** Default thresholds (t_pass 0.80 from Arize's RAGTruth cutoff, t_block 0.30) are placeholders until local calibration.
- **A6.** Anthropic SDKs accept `stop_reason: "refusal"`. If not, `block.anthropic_stop_reason` can be set to `end_turn`.
- **A7.** The Python team implements the RPCs in §7.
- **A8.** Postgres 14 or later (for `date_bin`); compose uses 16.

---

## 1. Package and file layout

**New**
```
backend-go/internal/jev/        types.go, normalize.go, errors.go, backend_http.go, backend_mock.go,
                                select.go (auto), retry.go, hedge.go, breaker.go, budget.go (rate+$),
                                tokens.go (estimator/EWMA), cost.go, snapshot.go, client.go (layer stack), *_test.go
backend-go/internal/tier0/      numbers.go, dates.go, currency.go, urls.go, quotes.go, citations.go,
                                entities.go, index.go (context index), check.go, offsets.go (UTF-16), *_test.go, fuzz_test.go
backend-go/internal/gate/       gate.go (orchestrator), context.go (acquisition), state.go (layout+truncation),
                                questions.go (versioned sets), signals.go, band.go, calibrate.go, decision.go, *_test.go
backend-go/internal/policy/     schema.go, parse.go (yaml+validate), compile.go, match.go, resolve.go,
                                source_file.go, source_db.go, reload.go, dryrun.go, calibration.go, *_test.go
backend-go/internal/llmapi/     format.go (interface), openai_chat.go, openai_completions.go, anthropic.go,
                                sse.go (line reader/writer), block.go, fallback.go, *_test.go
backend-go/internal/upstream/   upstream.go (named upstreams, URL join), headers.go (filter/inject), client.go (transport/timeouts)
backend-go/internal/trace/      trace.go (Trace struct, UUIDv7 ids, request-id validation)
backend-go/internal/health/     health.go (engine pinger, component status)
backend-go/internal/store/      traces.go, gate.go, policies.go, overview.go, retention.go,
                                migrations/0002_gateway.up.sql
backend-go/internal/websocket/  protocol.go (v2 envelope/types), ring.go
backend-go/internal/middleware/ deadline.go, admin.go, wsguard.go, readonly.go
backend-go/cmd/proxy/           overview_api.go, traces_api.go, policies_api.go, settings_api.go,
                                jev_api.go, playground_api.go, export_api.go, health_api.go
backend-go/cmd/jevcheck/        main.go   (CLI: validate key/endpoint, print snapshot/latency/cost)
backend-go/cmd/mock-upstream/   main.go   (deterministic OpenAI+Anthropic upstream, SSE, latency knobs)
backend-go/cmd/export-replay/   main.go   (DB -> sanitized replay bundle for the static demo)
config/policies.yaml
scripts/load/gateway-overhead.js
docs/GATEWAY.md
docs/JEV-ACCESS.md                  (gateway/env section; the plain-language guide itself is shared)
```

**Modified:**
- `cmd/proxy/main.go`: wiring, routes, server timeouts, `SetTrustedProxies`; `go workerPool.Start()` becomes a plain call.
- `cmd/proxy/audits_api.go`: new fields, `since`, cursor.
- `cmd/proxy/kb_api.go`: error bodies.
- `internal/proxy/handler.go`: split into `pipeline.go`, `stream.go`, `respond.go`, `generic.go`.
- `internal/worker/pool.go`
- `internal/grpc/client.go`
- `internal/websocket/hub.go`
- `internal/middleware/{auth,cors,ratelimit}.go`
- `internal/store/{store,postgres}.go`
- `internal/metrics/metrics.go`
- `internal/config/config.go`: split into `config.go`, `jev.go`, `validate.go`.
- `go.mod`
- Outside `backend-go/`: `proto/evaluator.proto`, `docker-compose.yml` (`demo`/`load` profiles, `mock-upstream`, new env vars), `.env.example`, `config/grafana/dashboards/trustagent.json`, `Makefile` (`jev-check`, `load-test-gate`), `scripts/load/proxy-overhead.js` (replaced).

---

## 2. `internal/jev`: multi-backend decision client

### 2.1 Backends and auto-selection

| `TRUTHTABLE_JEV_BACKEND` | Endpoint | Default model | Key env (first match wins) |
|---|---|---|---|
| `openrouter-decisions` | `POST https://openrouter.ai/api/alpha/decisions` | `typesafe/jev-1.13` (pinned; `~typesafe/jev-latest` allowed) | `TRUTHTABLE_JEV_API_KEY`, `OPENROUTER_API_KEY` |
| `openrouter-systemone` | `POST https://openrouter.ai/api/v1/systemone` | `typesafe/jev-1.13` | same |
| `typesafe` | `POST https://api.typesafe.ai/v1/systemone` | `jev-1.13.0` (pinned; `jev-latest` allowed) | `TRUTHTABLE_JEV_API_KEY`, `TYPESAFE_API_KEY` |
| `custom` | `POST {TRUTHTABLE_JEV_BASE_URL}/v1/systemone`, or `TRUTHTABLE_JEV_ENDPOINT` as a full URL | required; if the host is `ai-gateway.vercel.sh`, defaults to `typesafe-ai/jev` | `TRUTHTABLE_JEV_API_KEY` |
| `mock` | in-process | `mock/jev-heuristic-v1` | none |
| `off` | none | none | none |
| `auto` (default) | `OPENROUTER_API_KEY` set → `openrouter-decisions`; else `TYPESAFE_API_KEY` → `typesafe`; else `TRUTHTABLE_JEV_BASE_URL` + key → `custom`; else **`off`** | | |

- `auto` never picks `mock`; the mock has to be requested explicitly, so it can never produce fake numbers silently.
- When `auto` resolves to `off`, a startup log line points to `docs/JEV-ACCESS.md`. It explains that an OpenRouter key alone is enough: no waitlist and no TypeSafe account.
- `*_API_KEY_FILE` variants are supported for Docker secrets.

### 2.2 Types

```go
type Backend string
type Question struct {
    Type         string `json:"type"`               // noul|choice|score
    Instructions any    `json:"instructions"`       // always sent (OpenRouter requires)
    Criteria     any    `json:"criteria,omitempty"` // noul: both "true" and "false" or omitted
}
type Request struct {
    Model     string              `json:"model"`
    State     any                 `json:"state"`
    Questions map[string]Question `json:"questions"`
    SessionID string              `json:"session_id,omitempty"` // OpenRouter only, <=256
    User      string              `json:"user,omitempty"`       // OpenRouter only: key_id, never the raw key
    Provider  map[string]any      `json:"provider,omitempty"`   // {"zdr":true} when TRUTHTABLE_JEV_ZDR
}
type Answer struct {
    Type          string                     `json:"type"`
    Noul          *float64                   `json:"noul,omitempty"`
    Choice        string                     `json:"choice,omitempty"`
    Score         *float64                   `json:"score,omitempty"`
    Confidence    *float64                   `json:"confidence,omitempty"`    // optional on OpenRouter
    Probabilities map[string]float64         `json:"probabilities,omitempty"`
    Legend        map[string]json.RawMessage `json:"legend,omitempty"`
    // set by Normalize():
    Margin   float64 `json:"-"` // p1-p2 after renormalisation (our own, stable metric)
    Anomaly  string  `json:"-"` // "sum_out_of_range" | "choice_mismatch" | ""
}
type Usage struct{ InputTokens, OutputTokens int; Cost *float64 `json:"cost,omitempty"` }
type Response struct {
    ID, Model, Provider string
    Answers  map[string]Answer
    Usage    Usage
    // gateway-populated
    Backend Backend; Latency time.Duration; Attempts int; Hedged bool; CostUSD float64; RequestedModel string
}
type Class int // ClassSync (flag/block/fallback) | ClassAsync (observe, dry probes)
type CallOptions struct{ Class Class; Deadline time.Time; SessionID, User string }
type Client interface {
    Decide(ctx context.Context, req *Request, opt CallOptions) (*Response, error)
    Status() Status // backend, endpoint(no key), model, breaker, lastSnapshot, rpmUsed, costToday, lastError
}
```

### 2.3 Layer stack and algorithms

`jev.New(cfg)` builds the layers from outside in: `metrics` → `budget` → `breaker` → `hedge` → `retry` → `httpBackend`.

**`httpBackend.do`**
- Sends `Content-Type: application/json` and `Authorization: Bearer <key>`.
- On OpenRouter only, adds `HTTP-Referer: $TRUTHTABLE_JEV_APP_URL` and `X-OpenRouter-Title: $TRUTHTABLE_JEV_APP_TITLE`.
- Timeout per attempt is `min(TRUTHTABLE_JEV_ATTEMPT_TIMEOUT, remaining deadline)`.
- Response bodies are capped at 1 MiB.
- **Content-Type check:** anything other than `application/json` becomes `Error{Kind: bad_content_type}`, with the message "endpoint returned HTML; check TRUTHTABLE_JEV_BASE_URL path (missing /api?)". This catches the documented `200 text/html` SPA catch-all.
- Parses `x-typesafe-request-id` and OpenRouter `id` for logs.

**`Normalize(resp, questions)`**
1. Every requested question id must be present; a missing one gives `bad_response` for that question only.
2. Values are clamped to [0, 1].
3. **Renormalisation:** with s = Σp, if s > 0 divide by s. If |s − 1| > 0.05, set `Anomaly = sum_out_of_range` (logged and counted). If s == 0 the answer is invalid.
4. Key order is ignored; everything is read by name.
5. If `Choice` is missing or disagrees with the arg-max, the arg-max is used (ties broken lexicographically) and `choice_mismatch` is set.
6. `Margin` = p(1st) − p(2nd).
7. For score answers, `Score` is recomputed as Σ i·pᵢ when probabilities are present.
8. The vendor `confidence` is stored but never used as a threshold input, because it is a concentration statistic.

**Retry**
- **Retryable:** 408, 429, 500, 502, 503, 504, 524, 529, network errors, attempt timeouts (the "hang" case), and 402 when `error.metadata.limit_source == "openrouter_in_flight_budget"`.
- **Not retryable:** 400, 401, 403, 404, 422, other 402s, `bad_content_type`.
- **413, or 400 whose message mentions context/token/too long:** returns `too_large` to the gate, which halves the context once and calls again.
- **Sync calls:** at most `TRUTHTABLE_JEV_MAX_RETRIES` (1). A retry only happens if the remaining deadline is at least `max(RetryAfter, 250ms)`; otherwise the call fails fast.
- **Async calls:** at most `TRUTHTABLE_JEV_MAX_RETRIES_ASYNC` (2). Backoff is 250 ms × 2ⁿ with ±25% jitter, capped at 2 s. `Retry-After` (seconds or HTTP-date) is honoured up to 10 s.
- A 429 or 529 carrying `Retry-After` also puts the budget into a "cooling" state for that long, so other requests skip Jev instead of piling on.

**Hedge** (`TRUTHTABLE_JEV_HEDGE_AFTER`, default 0 = off; recommended 400 ms on block routes)
- After the delay, a second identical request is sent, but only if the budget has a token.
- The first success wins; the other request is cancelled through its context. Both count toward cost and RPM.

**Breaker**
- States: closed, open, half-open.
- **Opens when:** 5 consecutive failures (`TRUTHTABLE_JEV_BREAKER_FAILURES`), or at least 50% failures over the last 20 calls with at least 10 samples. It opens immediately on 401, 403, non-transient 402 or `bad_content_type`.
- **Cooldown:** `BREAKER_COOLDOWN` (30 s) for transient failures, `AUTH_COOLDOWN` (5 m) for configuration errors.
- **Half-open:** exactly one probe (CAS on an atomic); success closes the breaker.
- The status exposes a human message, for example "OpenRouter: insufficient credits (402). Add credits at openrouter.ai/credits".

**Budget**
- A token bucket at `TRUTHTABLE_JEV_RPM` (1000) with burst = RPM/60 × 2.
- `TRUTHTABLE_JEV_SYNC_RESERVE` (0.3): async calls may only take a token while the bucket is above 30% of capacity, so sync calls get the reserve.
- An in-flight semaphore (`MAX_INFLIGHT`, 64).
- Daily spend cap `TRUTHTABLE_JEV_DAILY_BUDGET_USD` (0 = unlimited), reset at UTC midnight.
- Rejections return `Kind: rate_limited` or `budget_exhausted` without making a network call.
- The bucket is written in-house (about 60 lines); no `x/time` dependency.

**Cost:** `usage.cost` if present, otherwise `input_tokens × TRUTHTABLE_JEV_PRICE_PER_MTOK / 1e6` (default 0.042).

**Snapshot**
- The response `model` (for example `typesafe/jev-1.13-20260917`) is recorded per decision.
- When it differs from the last value seen, the gateway logs a warning, sets the gauge `trustagent_gateway_jev_snapshot_info{snapshot}=1` (the old one goes to 0), emits the WebSocket event `jev_snapshot_changed`, and looks up calibration for the new snapshot (§4.5).

**Token estimator** (`tokens.go`)
- `est(s) = latinRunes/cpt + cjkRunes×1.0`, where cpt defaults to 4.0 (`TRUTHTABLE_JEV_CHARS_PER_TOKEN`).
- A correction factor is updated after every call: `f ← 0.9f + 0.1·(input_tokens − 250 − questionTokens)/est(state)`, clamped to [0.6, 1.6].

### 2.4 Error taxonomy

```go
type ErrorKind string // auth, credits, rate_limited, overloaded, invalid_request, too_large, timeout,
                      // network, upstream_5xx, bad_content_type, bad_response, circuit_open,
                      // budget_exhausted, disabled
type Error struct{ Kind ErrorKind; Status int; Message string; RetryAfter time.Duration
                   Retryable bool; Backend Backend; Raw string /* <=512B, key-scrubbed */ }
```

Envelope parsers:
- OpenRouter: `{"error":{code,message,metadata}}`
- TypeSafe 422: `{"detail":[{loc,msg,type}]}`
- TypeSafe 403: `{"detail":{error_type,message}}`
- Vercel: `{"message","error_type"}`
- Anything else: the first 512 bytes of text.

### 2.5 Mock backend (deterministic and labelled as fake)

- Responses carry `model: "mock/jev-heuristic-v1"` and `provider: "mock"`. Every stored decision records `backend=mock`, and the API and UI badge it **MOCK**. Overview aggregates are split by backend.
- **Heuristic:**
  - Tokenise `response` and `context` into content words.
  - `r` = the share of the response's content words that also appear in the context.
  - `nm` = the count of response numbers missing from the context (reuses `tier0`).
  - `neg` = a negation mismatch flag.
  - `P(grounded) = σ(6(r−0.55) − 1.2·nm − 1.0·neg) + u`, where u ∈ [−0.03, 0.03] comes from an FNV-64 hash of (seed, canonical state JSON).
  - Values are rounded to 2 decimals.
  - The relation probabilities come from the same features. One question in five, chosen by hash, is deliberately returned summing to 0.99, so the renormalisation path is exercised. Map key order is shuffled by seed.
- **Knobs:**

  | Variable | Default |
  |---|---|
  | `TRUTHTABLE_JEV_MOCK_SEED` | 42 |
  | `…_MOCK_LATENCY` | 40 ms, ±25% seeded jitter |
  | `…_MOCK_FAIL_RATE` | 0 |
  | `…_MOCK_HANG_RATE` | 0 |
  | `…_MOCK_FIXTURES` | JSON list of `{state_contains, answers}` overrides; strict mode returns an error when nothing matches |

---

## 3. `internal/tier0`: deterministic checks

**Input:** response text, the full context (not truncated), and the question text. Numbers the user supplied count as known.

**Context index:** built once per request with O(n) cost. It holds the normalised number set, a date set of (y, m, d), a currency set, a URL set, and the whitespace- and case-normalised text used for quote lookups.

| Code | Severity | Rule |
|---|---|---|
| `NUM_NOT_IN_CONTEXT` | warn | Numbers are normalised (thousands separators removed; `12%` = `12 percent`; number words zero–billion; `1.5 million` = 1500000). A response number is grounded if it equals a context or question number exactly, or equals one rounded to the response's precision. Integers ≤ 10 without a unit are ignored (`min_integer`, per policy), as are list markers at line start. |
| `DATE_NOT_IN_CONTEXT` | warn | ISO dates, "Jan 5, 2024", "5 January 2024", "2024-01" and years 1000–2099 are normalised. A year-only mention matches any context date in that year. |
| `CURRENCY_NOT_IN_CONTEXT` | warn | (amount, currency) pairs from `$ € £ ¥`, ISO codes and "dollars". |
| `URL_NOT_IN_CONTEXT` | high | The normalised URL (scheme and host lowercased, trailing punctuation and slash stripped) must appear in the context. |
| `QUOTE_NOT_IN_CONTEXT` | high | Text inside `"…"` or `“…”` with at least 4 words must appear verbatim after normalisation. |
| `CITATION_OUT_OF_RANGE` / `CITATION_UNKNOWN_ID` | high | Markers such as `[n]`, `[^n]`, `【n】` or "Source n" must be ≤ the number of context documents; `[doc-id]` must name a real document id. |
| `UNGROUNDED_NO_CONTEXT` | warn | No context, and the response contains any number, date, URL, quote or citation. |
| `NOVEL_ENTITIES` | info | At least 50% of capitalised multi-word entities are absent from the context. Soft signal only. |
| `RESPONSE_TOO_LONG`, `CONTEXT_TRUNCATED` | info | Set by the gate. |

```go
type Finding struct{ Code string; Severity string; Text string; Start, End int /* UTF-16 units */ }
type Result  struct{ Findings []Finding; Risk float64; HasCheckables bool; Took time.Duration }
func Check(response string, ctx []ContextDoc, question string, opt Options) Result
```

- `Risk = 1 − Π(1 − w)`, with weights URL 0.6, QUOTE 0.5, CITATION 0.5, CURRENCY 0.4, NUM 0.35, DATE 0.35. It is an uncalibrated heuristic and is only used for sorting and display.
- **Band effect** is set by policy: codes in `tier0.escalate_on` (default: every warn and high code) cap the band at *uncertain*. Codes in `tier0.block_on` (default empty) force the *block* band.
- This covers the documented Jev blind spots: numbers, dates and counting are never left to Jev alone.
- Offsets are in UTF-16 code units so the UI can highlight spans directly in JS strings.

---

## 4. `internal/gate`: context, state, questions, bands

### 4.1 Context acquisition (per route `context.sources`, in order; results deduplicated by hash)

1. **`extension`:** the request-body field `"trustagent": {"context":[{"id","text","source"}], "route": "..."}`. It is **stripped** before forwarding, because OpenAI rejects unknown parameters.
2. **`tool_messages`:** OpenAI `role:"tool"` message content and Anthropic `tool_result` blocks.
3. **`rag_blocks`:** delimited blocks inside system and user messages. Default patterns are the XML tags `context|document|documents|source|sources|retrieved|search_results`. Routes can add RE2 patterns (≤ 256 characters each).
4. **`system`:** the whole system prompt as evidence. Off by default, because system prompts are instructions, not evidence.
5. **`kb`** (`off|fallback|always`, default `off`):
   - gRPC `RetrieveContext(queries=[last user message], top_k=6)` with deadline `TRUTHTABLE_KB_RETRIEVE_TIMEOUT` (150 ms).
   - It is **started concurrently with the upstream call**, so its latency hides behind generation.
   - At gate time the gateway waits only for whatever remains of the 150 ms.
   - `fallback` means it is used only when sources 1–4 found nothing.
   - A timeout adds the reason `KB_TIMEOUT` and the gate continues without KB context.

The deep audit receives request-derived context in `AuditRequest.context`, with `metadata.context_mode = request|kb|none`. KB-derived context is **not** forwarded; Python retrieves per claim itself.

### 4.2 State layout and truncation

```json
{"question": "What is the refund window for annual plans?",
 "context": [{"id":"c1","source":"tool:search_policies","text":"Annual plans can be refunded within 30 days of purchase."},
             {"id":"c2","source":"rag:document","text":"Monthly plans are non-refundable."}],
 "response": "Annual plans can be refunded within 60 days."}
```

**Budget:** `total = TRUTHTABLE_JEV_MAX_STATE_TOKENS` (24,000) − longest question (about 450) − 300 overhead.
- `question`: capped at 1,000 tokens (head 600 + " … " + tail 400).
- `response`: if it exceeds `jev.max_response_tokens` (6,000), Jev is skipped. The result is `RESPONSE_TOO_LONG`, the band is at best uncertain, and the deep audit is high priority.
- `context` budget = `min(route.context.max_tokens` (default 8,000), remaining budget). The per-route cap guards against the documented "accuracy falls with irrelevant state" weakness.
  1. Split documents into passages of at most 800 tokens at paragraph or sentence boundaries.
  2. Score each passage by content-word overlap with the response, with numbers, dates and entities counted at 3× weight.
  3. Add passages greedily by score until the budget is used.
  4. Emit them in original document order, keeping ids.
  5. If anything was dropped, set `CONTEXT_TRUNCATED`.
- Tier-0 always runs on the full, untruncated context.
- An empty `context` array is never sent; the context-free set is used instead.

### 4.3 Question sets (versioned constants; the sha256 of the canonical JSON is stored with each decision)

**`gate-v1`** (context present):

```json
{
 "grounded": {"type":"noul",
  "instructions":"Is every factual statement in `response` supported by `context`? A statement is supported when `context` states it or directly implies it. Ignore greetings, opinions, questions, and offers to help.",
  "criteria":{"true":"Each factual statement in `response` is stated in or directly implied by `context`, or `response` makes no factual statements.",
              "false":"At least one factual statement in `response` is missing from `context` or conflicts with `context`."}},
 "relation": {"type":"choice",
  "instructions":"Taken as a whole, how does `response` relate to `context`?",
  "criteria":{
   "supported":{"what":"Every factual statement in `response` is stated in or directly implied by `context`","not_for":"Responses that add any fact that `context` does not contain"},
   "partially_supported":{"what":"Some factual statements in `response` are backed by `context` and others are not mentioned in `context`; nothing is contradicted","not_for":"Responses where `context` shows any statement to be false"},
   "contradicted":{"what":"`context` states the opposite of at least one statement in `response`, or shows it to be false","not_for":"Statements that `context` simply does not mention"},
   "unsupported":{"what":"The factual statements in `response` are not addressed by `context` at all","not_for":"Responses that `context` partly backs or contradicts"},
   "no_factual_claims":{"what":"`response` contains no factual statements to check: greetings, clarifying questions, refusals, or opinions only","not_for":"Responses with any factual statement"}}}
}
```

- Optional (`jev.include_score: true`, display only), `support_level` score with these levels:
  1. "`context` contradicts a statement in `response`"
  2. "Most statements in `response` are absent from `context`"
  3. "About half of the statements in `response` appear in `context`"
  4. "All statements appear in `context` except a minor detail"
  5. "Every statement in `response` is stated in or directly implied by `context`"
- Refusals are folded into `no_factual_claims`: a separate option would attract answers and isn't actionable for grounding.

**`gate-nc-v1`** (no context; only asked when `no_context: uncertain|block` needs it):

```json
{"factual":{"type":"noul","instructions":"Does `response` make specific factual claims about the world, such as names, figures, dates, events, or properties of things?",
  "criteria":{"true":"`response` states at least one specific, checkable fact","false":"`response` only greets, asks, refuses, gives opinions, or gives generic advice with no checkable facts"}}}
```

Relation choice to verdict: `supported`→SUPPORTED, `partially_supported`→PARTIALLY_SUPPORTED, `contradicted`→CONTRADICTED, `unsupported`→UNSUPPORTED, `no_factual_claims`→NO_CLAIMS. Gate-only verdicts are UNVERIFIABLE (no context) and UNKNOWN (error).

### 4.4 Band algorithm

```go
func Band(p *policy.Route, t0 tier0.Result, j *Signals, hasCtx, cal Calib) (band Band, verdict Verdict, by string, reasons []string) {
    reasons = t0.Codes()
    if t0.Any(p.Tier0.BlockOn)          { return Block, fromT0(t0), "tier0", reasons }
    if !hasCtx {
        factual := t0.HasCheckables || (j != nil && j.PFactual >= 0.5)
        switch p.NoContext { case "pass": return Pass, Unverifiable, by0(j), add(reasons,"NO_CONTEXT")
        case "block": if factual { return Block, Unverifiable, by0(j), add(reasons,"NO_CONTEXT") }
        default /*uncertain*/: if factual { return Uncertain, Unverifiable, by0(j), add(reasons,"NO_CONTEXT") } }
        return Pass, NoClaims, by0(j), reasons
    }
    if j == nil { // not sampled / skipped / error handled by caller's enforcement
        if t0.Any(p.Tier0.EscalateOn) { return Uncertain, Unknown, "tier0", reasons }
        return Pass, Unknown, "tier0", add(reasons, "JEV_NOT_EVALUATED")
    }
    if j.Rel["no_factual_claims"] >= 0.80 && len(t0.Findings) == 0 { return Pass, NoClaims, "jev", reasons }
    g, calibrated := cal.Apply(j.Snapshot, j.QuestionSetHash, "grounded", j.PGroundedRaw)
    switch { case g >= p.Thresholds.Pass: band = Pass; case g < p.Thresholds.Block: band = Block; default: band = Uncertain }
    if band == Block && j.Rel["supported"] >= 0.70 { band = Uncertain; reasons = add(reasons, "JEV_DISAGREEMENT") }
    if band == Pass && j.Rel["contradicted"] >= p.Thresholds.ContradictionFlag /*0.5*/ { band = Uncertain; reasons = add(reasons, "JEV_CONTRADICTION_SIGNAL") }
    by = "jev"; if band == Pass && t0.Any(p.Tier0.EscalateOn) { band = Uncertain; by = "tier0+jev" }
    if band == Block { reasons = add(reasons, ifElse(j.Top=="contradicted","JEV_CONTRADICTED","JEV_UNGROUNDED")) }
    if !calibrated { reasons = add(reasons, "UNCALIBRATED_SNAPSHOT") }
    return band, verdictOf(j.Top), by, reasons
}
```

### 4.5 Calibration

- **Profile schema** (stored in the policy document; imported from eval output, §5.7): `{snapshot, question_set_hash, signal:"grounded", method: isotonic|platt|identity, points:[[x,y]…] | {a,b}, n, ece, brier, created_at}`.
- `isotonic`: piecewise-linear interpolation over points with x strictly increasing and y non-decreasing.
- `platt`: y = 1/(1+e^(a·x+b)).
- **Lookup order:** exact snapshot, then the same release line (`typesafe/jev-1.13*`) only if `allow_line_fallback`, then identity with `calibrated=false`.
- **Safety:** with `require_calibration: true` (default for `block` and `fallback` modes), an uncalibrated block band is enforced as `flag`, and `UNCALIBRATED_SNAPSHOT` is added to the reasons.

---

## 5. `internal/policy`

### 5.1 Schema (`config/policies.yaml`)

```yaml
version: 1
defaults:
  mode: observe                 # observe | flag | block | fallback
  enforcement: enforce_ignore_errors   # audit | enforce_ignore_errors | enforce
  fail: open                    # open | closed  (only consulted when enforcement=enforce)
  deadline_ms: 800              # sync gate budget (validated 100..5000)
  sample_rate: 1.0              # observe routes: fraction sent to Jev (tier0 always runs)
  streaming: observe_only       # observe_only | buffer
  stream_annotations: comment   # comment | none  (SSE ": trustagent {...}" before [DONE]/message_stop)
  buffer_keepalive_s: 15        # buffer mode: SSE comment keepalives (commits 200 => stream blocks use content_filter)
  thresholds: { pass: 0.80, block: 0.30, contradiction_flag: 0.50 }
  require_calibration: true
  no_context: uncertain         # pass | uncertain | block
  context: { sources: [extension, tool_messages, rag_blocks], kb: off, max_tokens: 8000, patterns: [] }
  tier0: { enabled: true, escalate_on: [NUM_NOT_IN_CONTEXT, DATE_NOT_IN_CONTEXT, CURRENCY_NOT_IN_CONTEXT, URL_NOT_IN_CONTEXT, QUOTE_NOT_IN_CONTEXT, CITATION_OUT_OF_RANGE, CITATION_UNKNOWN_ID, UNGROUNDED_NO_CONTEXT], block_on: [], min_integer: 10 }
  jev: { question_set: gate-v1, include_score: false, max_response_tokens: 6000 }
  deep_audit: { sample_pass: 1.0, high_priority_on: [uncertain, block, flag, fallback, error, tier0] }
  block: { style: content_filter, status: 422, message: "I can't provide a verified answer to that right now.", anthropic_stop_reason: refusal }
overrides: { client_mode: none }  # none | stricter_only | any  (X-TrustAgent-Mode header)
routes:
  - name: support-bot
    match: { headers: { X-TrustAgent-Route: support-bot } }
    mode: flag
  - name: payments-faq
    match: { paths: ["/v1/chat/completions"], models: ["gpt-4o*"], tenants: [payments] }
    mode: fallback
    enforcement: enforce
    fail: closed
    streaming: buffer
    deadline_ms: 1500
    context: { kb: fallback }
    tier0: { block_on: [URL_NOT_IN_CONTEXT, CITATION_OUT_OF_RANGE] }
    fallback:
      upstream: default          # default | fallback (named in env only; never a URL in policy => no SSRF/key exfil)
      model: gpt-4o
      system_prompt: "Answer using only the information in CONTEXT. If CONTEXT does not contain the answer, say you don't have that information. Do not add facts, numbers, dates, names or links that are not in CONTEXT."
      max_attempts: 1            # validated == 1
      on_fail: block             # block | flag | original
calibration: { allow_line_fallback: false, profiles: [] }
```

### 5.2 Matching and overrides

- Routes are evaluated in file order; **the first match wins**. There is no specificity scoring.
- Within one route, all given matchers must match. Available matchers: `paths` (exact or `*` glob), `models` (glob on the request `model`), `tenants` (key names from `TRUTHTABLE_API_KEYS=name=key,…`; unnamed keys get `key-<sha256[:8]>`), `key_ids`, `headers` (case-insensitive exact values).
- No match means `defaults`, with route name `default`.
- **Header override `X-TrustAgent-Mode`:**
  - `none`: ignored.
  - `stricter_only`: allowed only in the direction observe → flag → block/fallback.
  - `any`: any mode.
  - Downgrades are refused by default, so a client cannot bypass blocking.
- `X-TrustAgent-Route: <name>` is only a match input; the route must declare it under `match.headers`.

### 5.3 Mode × band → action; enforcement ladder

| mode \ band | pass | uncertain | block |
|---|---|---|---|
| observe | observe (async, never alters) | observe | observe (would_action recorded) |
| flag | pass + headers | flag | flag |
| block | pass | flag | **block** |
| fallback | pass | flag | **fallback**, then re-gate |

| enforcement | Effect |
|---|---|
| `audit` | The gate runs **inline** so real latency is measured, but block and fallback are recorded only as `would_action`. Headers are emitted, including `X-TrustAgent-Would-Action`. |
| `enforce_ignore_errors` | Bands are enforced. On a gate error (Jev timeout, breaker, budget, KB failure) the action is `pass` with `X-TrustAgent-Gate: error`. |
| `enforce` | Bands are enforced. On a gate error, `fail: open` means pass and `fail: closed` means block with reason `FAIL_CLOSED`. |

**Late decisions:** when the deadline expires, the Jev call continues on a detached context (up to `TRUTHTABLE_JEV_ASYNC_TIMEOUT`, 8 s). Its result is persisted with `late=true` and streamed to the dashboard, but never enforced.

### 5.4 Block and fallback shapes (`internal/llmapi`)

**OpenAI chat, `content_filter` style (default)**
- HTTP 200, keeping the upstream `id`, `model`, `usage`, `system_fingerprint`.
- The body is patched as a raw map, not re-marshalled through structs: `choices[i].message.content = block.message`, `tool_calls` removed, `finish_reason = "content_filter"`.
- Streaming version:
  ```
  data: {...chunk with delta.role+content...}
  data: {...delta:{}, finish_reason:"content_filter"}
  data: [DONE]
  ```

**OpenAI `error` style**
- HTTP `block.status` (422 by default; allowed values 400, 403, 409, 422, 451; never 429 or 5xx, which SDKs retry).
- Body: `{"error":{"message":…,"type":"trustagent_policy_block","param":null,"code":"groundedness_block"}}`.

**Anthropic, non-streaming block**
```
{"id":"msg_trustagent_<trace>","type":"message","role":"assistant","model":…,"content":[{"type":"text","text":msg}],
 "stop_reason":"refusal","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}
```
- Streaming: the full event sequence, `message_start` → `content_block_start` → `content_block_delta` → `content_block_stop` → `message_delta{stop_reason}` → `message_stop`.
- Error style: HTTP 400 `{"type":"error","error":{"type":"invalid_request_error","message":…}}`.

**Fallback**
1. Build a new upstream request: set `model`, insert the grounding system prompt plus `\n\nCONTEXT:\n<context>…</context>` (packed context, up to 8k tokens) as the first system message (OpenAI) or prepended to `system` (Anthropic), and force non-streaming.
2. Send it once.
3. Re-gate the answer with a fresh deadline.
4. If the new band is pass or uncertain, return the fallback answer (`Action: fallback`). If it is block, apply `on_fail`.
5. Both gate decisions are stored (`attempt` 0 and 1).
6. Without context, the prompt becomes "…if you are not certain, say so; do not include specific numbers, dates, URLs or quotations."
7. In `buffer` streaming, the final text is re-emitted as a synthesized stream.

### 5.5 Response headers (every entry is in `Access-Control-Expose-Headers`)

| Header | Values |
|---|---|
| `X-TrustAgent-Trace-Id` | always; echoed `X-Request-ID` |
| `X-TrustAgent-Policy` | `payments-faq@v12/fallback/enforce` |
| `X-TrustAgent-Action` | pass, flag, block, fallback, observe, skipped |
| `X-TrustAgent-Band` | pass, uncertain, block |
| `X-TrustAgent-Verdict` | supported, partially_supported, contradicted, unsupported, no_claims, unverifiable, unknown |
| `X-TrustAgent-Grounded` | calibrated P, 2 decimals |
| `X-TrustAgent-Calibrated` | true, false |
| `X-TrustAgent-Reasons` | at most 8 codes, comma-separated |
| `X-TrustAgent-Decided-By` | tier0, jev, tier0+jev, policy, none |
| `X-TrustAgent-Gate` | ok, error, timeout, skipped:<why> |
| `X-TrustAgent-Gate-Ms`, `X-TrustAgent-Overhead-Ms` | total gateway time excluding upstream |
| `X-TrustAgent-Jev-Model` | snapshot id |
| `X-TrustAgent-Audit` | queued, sampled_out, dropped, disabled |
| `X-TrustAgent-Would-Action` | only with `enforcement=audit` |

Observe mode sends only Trace-Id, Policy, `Action: observe` and Audit. For streams in `observe_only` mode, the same fields are declared as HTTP `Trailer` and also written as an SSE comment before the terminal event.

### 5.6 Storage, hot reload, API edits

- **Source priority:**
  1. Database `policy_versions` where `active` is true.
  2. `TRUTHTABLE_POLICY_FILE` (default `config/policies.yaml`). On first start with a database and no stored versions, the file is imported as v1.
  3. Built-in defaults.
- **Reload:**
  - The file is polled for mtime and sha256 every `TRUTHTABLE_POLICY_RELOAD_INTERVAL` (5 s), and reloaded on SIGHUP.
  - The database is polled with `SELECT max(version)` every 5 s, which keeps multiple replicas consistent.
  - The compiled policy is swapped atomically through `atomic.Pointer[policy.Compiled]`.
  - An invalid policy keeps the old one: the error is logged, `trustagent_gateway_policy_reload_errors_total` is incremented, and a WebSocket `policy_error` event is sent.
- **Writes:** `PUT` stores canonical YAML (comments are lost on UI edits; documented) and requires `If-Match: "v<N>"`, returning 409 if it is stale. When the policy source is a file with no database, the API returns 409 `{"error":"policy is file-managed; edit config/policies.yaml"}`, and `GET` reports `editable:false` so the UI shows no dead buttons.
- **Validation limits:** at most 64 routes; unique names matching `[a-z0-9-]{1,48}`; 0 ≤ block ≤ pass ≤ 1; sample rate in [0, 1]; `deadline_ms` in [100, 5000]; `fallback.max_attempts` == 1; RE2 patterns compile and are ≤ 256 characters; message ≤ 2,000 characters; `fallback.upstream` must be `default` or `fallback`. Errors include line and column (yaml.v3 `Node`).

### 5.7 Dry-run and calibration import

- `POST /api/policies/dry-run {yaml|version, window:"24h", limit≤100000}`:
  - Replays stored `gate_decisions`, which hold every match input and raw signal (path, model, tenant, route header, raw P, relation probabilities, tier0 codes, snapshot), through `Resolve` + `Band` + `actionFor`.
  - Returns:
    ```
    {evaluated, window, by_action_new:{…}, by_action_actual:{…}, changed:N,
     newly_blocked:N, by_route:[…], estimated_from_sampling:{…}, examples:[trace_id…≤20],
     caveats:["fallback re-gate outcomes not simulated","observe decisions scaled by 1/sample_rate"]}
    ```
  - Decisions where Jev was skipped are replayed on tier0 alone and counted separately.
- `POST /api/policies/calibration`:
  - Body (produced by the Python eval: `schema: "trustagent.calibration/v1"`):
    ```
    {snapshot, question_set_hash, signal, method, points|{a,b}, metrics{n,ece,brier,auroc},
     recommended{pass,block,target_false_block_rate}, apply_to_routes?:[…]}
    ```
  - Checks monotonicity, n ≥ 200, and that the question-set hash is known.
  - Creates a new policy version with the profile added (and thresholds, if requested).
- `GET /api/gate/reliability?snapshot&window`:
  - 10 bins of `{p_mean, observed_supported_rate, n}` from gate decisions joined with completed audits.
  - Labelled "agreement with deep audit (proxy labels), not ground truth".

---

## 6. Handler restructure (`internal/proxy`)

### 6.1 Non-streaming pipeline (chat, completions, messages share it through `llmapi.Format`)

```go
type Format interface {
    Name() string                                   // openai_chat | openai_completions | anthropic_messages
    ParseRequest(raw []byte) (*ParsedRequest, error) // tolerant: content string|parts[]; never 400 on unknown shape
    StripExtensions(p *ParsedRequest) []byte        // removes "trustagent", "test_response"
    ExtractResponse(body []byte) ([]Choice, *Usage, error)
    NewStreamParser() StreamParser                   // OnLine(line) ; Text(i) ; Done()
    BlockResponse(p *ParsedRequest, up []byte, cfg BlockCfg, stream bool) (status int, hdr http.Header, body []byte)
    FallbackRequest(p *ParsedRequest, cfg FallbackCfg, ctx []ContextDoc) []byte
}
```

1. **Trace setup:** create `trace := trace.New(c)` (UUIDv7). A client `X-Request-ID` is kept as `client_request_id` only if it matches `^[A-Za-z0-9._:-]{1,128}$` (fixes B4).
2. **Read the body.** A `*http.MaxBytesError` returns 413.
3. **Parse.** On failure, forward the body unchanged and mark the gate `skipped:unparseable_request`.
4. **Test mode:** `test_response` returns 400 unless `TRUTHTABLE_TEST_MODE=true` (fixes M7). When enabled, the synthetic response goes through the full gate (G3), with `source=test`.
5. **Resolve the policy:** `pol := policies.Resolve(RouteInput{path, model, tenant, keyID, headers})`.
6. **Start KB retrieval** (goroutine) if the route asks for it.
7. **Call upstream** through `upstream.Do(ctx=c.Request.Context()+timeout, …)`. See §6.3 for URL joining, headers, auth and timeouts.
8. **Non-2xx, non-JSON, or a body over `MAX_UPSTREAM_RESPONSE_BYTES`:** pass it through unchanged (with filtered headers). The trace records the upstream status and the gate is `skipped:upstream_error`.
9. **Extract choices.** Tool-call-only answers or `message.refusal` give `skipped:no_text` / `no_claims`. With n > 1, up to 4 choices are gated in parallel and the worst band wins.
10. **Observe mode:** write the response now. Then `gateAsync.Go(…)`, bounded by the `TRUTHTABLE_GATE_MAX_CONCURRENCY` (256) semaphore; on overflow the result is `skipped:overload` with tier0 only.
11. **Sync modes:** `d := gate.Evaluate(ctx(deadline), in, pol)`, then `act := actionFor(pol, d)`. For fallback, run §5.4.
12. **Write the response:** header copy uses `Add` (multi-value), minus hop-by-hop headers, and minus Content-Length/Content-Encoding whenever the body is rewritten. `X-TrustAgent-*` headers are added.
13. **After the response:** `recorder.Record(trace, decisions)` (async batch), WebSocket `gate_decision`, and `pool.Submit(job, lane(d))`, which returns an error that the trace records.

### 6.2 Streaming

- **Branch choice** is based on the **response**, not the request: `stream && 2xx && Content-Type starts with text/event-stream`. Anything else goes to the non-streaming passthrough (fixes the part of M3 where JSON errors were forced to `text/event-stream`).
- **`observe_only`:**
  - A line-aware relay (`bufio.Reader`, maximum line 1 MiB, `data:` with or without a space, CRLF handled) forwards each line and flushes at every event boundary.
  - The parser accumulates per-index `delta.content` (OpenAI) or `text_delta` (Anthropic).
  - In `flag` mode with `stream_annotations: comment`, the terminal event (`data: [DONE]` / `message_stop`) is **held**. The gate runs within the deadline, then `: trustagent {"band":…,"verdict":…,"grounded":…,"reasons":[…],"trace_id":…}` is written, followed by the held event. In observe mode nothing is held.
  - If the client disconnects mid-stream, the upstream call is cancelled and the trace is marked `client_disconnected`. No gate or audit runs, because partial text would give misleading verdicts.
- **`buffer`:**
  - The whole SSE stream is buffered, with an optional `: keepalive` comment every `buffer_keepalive_s`.
  - Pass or flag: headers are set, then the bytes are replayed verbatim.
  - Block: a synthesized content_filter stream (§5.4). The 4xx error style is possible only while no keepalive has been sent yet.
  - Fallback: synthesized from the re-gated text.
- **Idle timeout:** `TRUTHTABLE_STREAM_IDLE_TIMEOUT` (120 s) resets on every read; the write deadline is extended per flush through `http.ResponseController`.
- **Deferred:** per-window gating during streaming (NeMo-style) is out of scope for v1 because of per-window Jev latency and RPM cost.

### 6.3 Upstream and auth (`internal/upstream`, `middleware/auth.go`)

**Named upstreams** (env only):

| Name | URL | Key | Auth header |
|---|---|---|---|
| `default` | `TRUTHTABLE_UPSTREAM_URL` | `TRUTHTABLE_UPSTREAM_API_KEY` | `TRUTHTABLE_UPSTREAM_AUTH_HEADER`: `authorization` (Bearer), `api-key` (Azure) or `x-api-key` |
| `anthropic` | `TRUTHTABLE_ANTHROPIC_UPSTREAM_URL` (https://api.anthropic.com) | `…_API_KEY` | `x-api-key` |
| `fallback` | `TRUTHTABLE_FALLBACK_UPSTREAM_URL` | `…_API_KEY` | |

**URL building:** `singleJoiningSlash(base.Path, req.Path)` plus a merge of the base query and the client query, so `?api-version=` is kept (fixes M2). `HandleGeneric` becomes a single `httputil.ReverseProxy` built once, using `Rewrite` with `pr.SetURL(base)` (joins path and query, strips hop-by-hop) plus the same header policy.

**Forwarded headers:** always strip `X-TrustAgent-*`, hop-by-hop headers, `Accept-Encoding` (Go's transport then negotiates gzip and decompresses transparently; fixes B2), `Cookie`, `Host`, `Content-Length`.

**Auth modes:**
- **Passthrough** (no upstream key configured):
  - `/v1/*` authenticates only `X-TrustAgent-Key`.
  - The client's `Authorization` / `x-api-key` go to the upstream untouched.
  - The proxy key is never forwarded (fixes B1).
- **Inject** (upstream key configured):
  - The proxy key is accepted from `X-TrustAgent-Key`, `Authorization: Bearer` (OpenAI SDK `api_key`) or `x-api-key` (Anthropic SDK).
  - All client credential headers are stripped and the configured key is set.
- **`/api/*`:** accepts `X-TrustAgent-Key`, then Bearer, then `X-API-Key` (back-compat; nothing is forwarded from these routes).
- **Admin routes** (policy writes, `jev/test`) require `TRUTHTABLE_ADMIN_KEYS`; if that is empty, any valid key is admin, with a startup warning.
- **`/ws`:** `?ticket=`, a 30 s single-use ticket from `POST /api/ws-ticket` (keeps the key out of URLs), or legacy `?api_key=`.

**Timeouts (M12):**
- The client `Timeout` is removed. The transport gets `ResponseHeaderTimeout` (`TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT`, 60 s).
- The non-streaming context gets `TRUTHTABLE_UPSTREAM_TIMEOUT` (5 m); streams use the idle timeout.
- Server `WriteTimeout` defaults to 0, and `ReadHeaderTimeout` is 10 s. A `middleware.Deadline` sets a default write deadline per route (60 s for `/api`), which handlers extend: LLM calls get the upstream timeout + 30 s, and `/api/upload` gets `TRUTHTABLE_INGEST_TIMEOUT` (6 m) + 30 s.
- Config validation rejects `IngestTimeout ≥ write deadline`.

### 6.4 Bug fixes (all verified in source)

| Bug | Fix |
|---|---|
| **B1** auth collision | §6.3: `X-TrustAgent-Key`, passthrough and inject modes, credential stripping, proxy key never forwarded. |
| **B2** Accept-Encoding | Strip it before forwarding (transparent gunzip). Defensive: if `Content-Encoding` is still present, decode gzip or deflate; otherwise `skipped:undecodable`, with enforcement semantics applied. |
| **B3** content-part arrays | Tolerant `json.RawMessage` parsing: string, `[{type:"text"\|"input_text", text}]`, image parts ignored. Parse failure never causes a 400; the body is forwarded as-is. |
| **B4** duplicate IDs | `audit_id` = server UUIDv7 and `trace_id` = server UUIDv7; the client id is a validated, non-unique `client_request_id`. `SaveAudit`: if the `INSERT … ON CONFLICT DO NOTHING` reports `RowsAffected()==0`, return `ErrDuplicate` **before** inserting claims. |
| **B5** frame batching | `writePump` writes one `WriteMessage` per message; no `\n` joins. |
| **B6** hub race | `Run` is the only owner of the clients map (no mutex). Every broadcast, including audit results, goes through the `h.events` channel (buffer 4096). Slow clients: the drop is counted and a pending gap is recorded (§10); a client with no progress for 30 s is closed only by `Run`, and the gauge is decremented there. `ClientCount` and `Stats` go through atomics. |
| **M1** | `http.NewRequestWithContext(c.Request.Context())`. |
| **M2** | Base path and query join (§6.3). |
| **M3** | Multi-value `Add`, hop-by-hop filter, Content-Length/Content-Encoding removed on rewrite and on handler errors (`c.Writer.Header().Del` before writing), stream branch chosen from the response Content-Type. |
| **M4** | `health.Pinger`: `HealthCheck` every 10 s with a 2 s timeout, cached. `/health` returns `components{engine{status,latency_ms,checked_at,detail},jev{backend,state,snapshot,last_error},database,policy{version,source}}` and keeps `audit_engine = engine.status=="up"`. `/ready` is 200 once the policy has loaded. |
| **M5** | New unary `Audit` RPC; the polling loop is deleted. Sends model, provider/format, `timestamp_ms`, context, metadata (`trace_id`, route, source, `context_mode`) and `GateSummary`. `AUDIT_STATUS_FAILED` and `error` become `audit_failed`. |
| **M6** | `Submit(job) error` (`ErrQueueFull`, `ErrStopped`, `ErrNoEngine`). `/api/audit` returns 503 with `Retry-After` on these. Two lanes: high (`TRUTHTABLE_AUDIT_QUEUE_HIGH`, 200) and low (`TRUTHTABLE_QUEUE_SIZE`, 1000); workers drain high first; low is dropped first. `Stop(ctx)` sets `stopped` under a mutex (so there is no send on a closed channel), closes the queues, drains until `TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT` (20 s), then cancels. `Start()` is called synchronously. |
| **M7** | `TRUTHTABLE_TEST_MODE` (default false); test traffic is gated and labelled. |
| **M8** | `router.SetTrustedProxies(cfg.TrustedProxies)` (default none). Rate-limit bucket = key_id if the key is valid, else client IP. Redis keys use `sha256(key)[:16]`. The memory limiter sweeps expired windows instead of resetting everything. `/ws` caps: `WS_MAX_CLIENTS` (200) and `WS_MAX_PER_IP` (10). |
| **M9** | `/v1/completions` uses the `openai_completions` format (`prompt` as string or array; `choices[].text`) and is fully gated and audited. |
| **M10** | WebSocket v2 `audit_failed` and `audit_dropped` events in the same envelope (§10). |
| **M11** | Metrics renamed by owner (§11) and Grafana rewritten. |
| **M12** | §6.3 timeouts. |

Low-severity items folded in:
- Response bodies are capped.
- `MaxTextChars` counts runes.
- `readPump` no longer logs payloads.
- One timestamp type (RFC3339Nano).
- Startup logs say "TrustAgent".
- `verdictMap` becomes a package variable.
- `AuditRequest.query` = the last user turn instead of `"[system]:…[user]:…"`.
- Dead `TeeWriter`, `OriginAllowed`, `ClaimInfo` and `AuditEvent` are deleted.
- The migration runner applies each file and its `schema_migrations` row in one transaction under `pg_advisory_lock(7212026)`.

---

## 7. Worker, gRPC and proto contract (Go needs these; Python implements them)

```proto
rpc Audit(AuditRequest) returns (AuditResult);                              // synchronous; replaces Submit+poll
rpc RetrieveContext(RetrieveContextRequest) returns (RetrieveContextResponse); // fast KB lookup, run embed in thread, 60s cache
message AuditRequest { /* existing 1-8 */ GateSummary gate = 9; string trace_id = 10; string priority = 11; }
message GateSummary { string action=1; string band=2; string verdict=3; float p_grounded=4; bool calibrated=5;
  map<string,float> relation_probs=6; repeated string reasons=7; string jev_model=8; string decided_by=9; string question_set=10; }
enum VerificationStatus { /* existing */ VERIFICATION_STATUS_UNKNOWN = 5; }   // stop mapping UNKNOWN->UNSPECIFIED
message ClaimVerification { /* 1-4 */ string decided_by=5; int32 span_start=6; int32 span_end=7;  // UTF-16, -1 if unaligned
  string escalation_reason=8; float jev_confidence=9; }
message AuditResult { /* 1-11 */ string judge_provider=12; string judge_model=13; double cost_usd=14; bool judge_degraded=15; string error=16; }
message RetrieveContextRequest { repeated string queries=1; int32 top_k=2; float min_score=3; int32 max_chars=4; }
message RetrieveContextResponse { repeated ContextDocument documents=1; int64 took_ms=2; string index_state=3; }
message HealthResponse { /* 1-3 */ string judge_provider=4; string judge_model=5; bool judge_fallback_active=6; bool rag_enabled=7; int64 kb_claims=8; }
```

- `judge_degraded=true` (for example, all claims UNKNOWN because the judge was down) is shown as a degraded audit, never as a normal grade.
- `AuditJob` gains: `TraceID, AuditID, ClientRequestID, Source (proxy|direct|playground|test), Route, Format, Model, Question, Response, Context []ContextDoc, ContextMode, Gate *GateSummary, Lane, UpstreamMs`.

---

## 8. Persistence: `migrations/0002_gateway.up.sql`

```sql
CREATE TABLE traces (
  trace_id TEXT PRIMARY KEY, client_request_id TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source TEXT NOT NULL, api_format TEXT NOT NULL, path TEXT NOT NULL, route TEXT NOT NULL, policy_version INT,
  mode TEXT, enforcement TEXT, tenant TEXT, key_id TEXT, route_header TEXT, model TEXT, stream BOOLEAN NOT NULL,
  upstream_status INT, upstream_ms INT, ttft_ms INT, total_ms INT, overhead_ms INT, gate_ms INT,
  prompt_tokens INT, completion_tokens INT, action TEXT NOT NULL, would_action TEXT, band TEXT, verdict TEXT,
  decided_by TEXT, reasons TEXT[] NOT NULL DEFAULT '{}', audit_status TEXT, audit_lane TEXT, audit_id TEXT,
  question_text TEXT, response_text TEXT, final_response_text TEXT,  -- NULL when TRUTHTABLE_STORE_BODIES=false
  flags TEXT[] NOT NULL DEFAULT '{}'                                   -- client_disconnected, late, fallback_used...
);
CREATE INDEX ON traces (created_at DESC); CREATE INDEX ON traces (route, created_at DESC); CREATE INDEX ON traces (action, created_at DESC);

CREATE TABLE gate_decisions (
  id BIGSERIAL PRIMARY KEY, trace_id TEXT NOT NULL REFERENCES traces(trace_id) ON DELETE CASCADE, attempt SMALLINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), policy_version INT, route TEXT, mode TEXT, enforcement TEXT,
  band TEXT, action TEXT, would_action TEXT, verdict TEXT, decided_by TEXT, reasons TEXT[] NOT NULL DEFAULT '{}',
  p_grounded_raw REAL, p_grounded REAL, calibrated BOOLEAN, relation JSONB, relation_top TEXT, relation_margin REAL,
  vendor_confidence REAL, support_level REAL, tier0 JSONB NOT NULL DEFAULT '[]', tier0_risk REAL,
  context_sources TEXT[], context_docs INT, context_tokens INT, context_truncated BOOLEAN, kb_ms INT, kb_error TEXT,
  jev_backend TEXT, jev_requested_model TEXT, jev_snapshot TEXT, question_set TEXT, question_set_hash TEXT,
  jev_sampled BOOLEAN, jev_ms INT, jev_attempts SMALLINT, jev_hedged BOOLEAN, jev_input_tokens INT, jev_cost_usd DOUBLE PRECISION,
  jev_anomalies TEXT[], error_kind TEXT, error_message TEXT, fail_mode_applied TEXT, late BOOLEAN NOT NULL DEFAULT false, gate_ms INT
);
CREATE INDEX ON gate_decisions (created_at DESC); CREATE INDEX ON gate_decisions (trace_id); CREATE INDEX ON gate_decisions (jev_snapshot, created_at DESC);

CREATE TABLE policy_versions (
  version INT PRIMARY KEY, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), created_by TEXT, comment TEXT,
  source TEXT NOT NULL, yaml TEXT NOT NULL, sha256 TEXT NOT NULL, active BOOLEAN NOT NULL DEFAULT false);
CREATE UNIQUE INDEX one_active_policy ON policy_versions (active) WHERE active;

ALTER TABLE audits ADD COLUMN trace_id TEXT, ADD COLUMN client_request_id TEXT, ADD COLUMN source TEXT NOT NULL DEFAULT 'proxy',
  ADD COLUMN route TEXT, ADD COLUMN api_format TEXT, ADD COLUMN upstream_ms INT, ADD COLUMN priority TEXT,
  ADD COLUMN status TEXT NOT NULL DEFAULT 'completed', ADD COLUMN error TEXT, ADD COLUMN judge_provider TEXT,
  ADD COLUMN judge_model TEXT, ADD COLUMN judge_degraded BOOLEAN NOT NULL DEFAULT false, ADD COLUMN cost_usd DOUBLE PRECISION,
  ADD COLUMN context_mode TEXT;
CREATE INDEX ON audits (trace_id);
ALTER TABLE audit_claims ADD COLUMN decided_by TEXT, ADD COLUMN span_start INT, ADD COLUMN span_end INT,
  ADD COLUMN escalation_reason TEXT, ADD COLUMN jev_confidence DOUBLE PRECISION;
```

- **Trace writer:** channel of 10k entries; flushes every 250 ms or 200 rows with `pgx.Batch`. When full it drops and increments `trustagent_gateway_trace_write_dropped_total`.
- **Retention:** `TRUTHTABLE_RETENTION_DAYS` (30) runs an hourly `DELETE … WHERE created_at < now()-interval` in batches of 5,000.
- **Query safety:** every analytics query runs under `SET LOCAL statement_timeout = '5s'`.

---

## 9. REST API additions (auth required unless noted; mutating routes return 403 when `TRUTHTABLE_DEMO_READONLY=true`)

| Method and path | Purpose |
|---|---|
| `GET /api/overview?window=1h\|24h\|7d\|30d` | Server-side buckets (1m, 15m, 6h, 1d) via `date_bin` + `generate_series`, so empty buckets are zero. Totals: requests, gated, flagged, blocked, fallback, audited, hallucination rate (audited only), p50/p95 overhead, gate and upstream ms, Jev cost, judge cost. Also `series[]`, `by_route[]`, `by_model[]` (top 10), claim verdict mix, `jev_backend_mix` (mock traffic reported separately and excluded from rates by default; `include_mock=true`). Returns 503 `persistence_disabled` without a database. |
| `GET /api/traces?limit&cursor&since&route&model&action&band&verdict&source` | Live-traffic list with keyset pagination. |
| `GET /api/traces/:id` | Trace + gate decisions + audit + claims with spans + tier0 findings with spans + response texts. |
| `GET /api/gate/decisions?…` | Paged list. |
| `GET /api/gate/reliability` | §5.7. |
| `GET /api/policies` | `{version, source, editable, yaml, parsed, effective_routes, calibration_profiles, updated_at, updated_by}` |
| `GET /api/policies/versions`, `GET /api/policies/versions/:v` | Version history. |
| `POST /api/policies/validate` | Returns `{ok, errors[{line,col,msg}]}`. |
| `PUT /api/policies` (admin, `If-Match`) | Creates and activates a new version. |
| `POST /api/policies/activate/:v` (admin) | Rollback. |
| `POST /api/policies/dry-run`, `POST /api/policies/calibration` (admin) | §5.7. |
| `GET /api/settings` | Effective config grouped `{key, env, value, default, source: env\|default\|file, secret}`. Secrets appear as `"set (…ab12)"` or `"not set"`; a test asserts no key substring ever appears. Read-only; the UI shows which env var to change. |
| `GET /api/engine` | Cached `HealthResponse` extension + pinger status. |
| `GET /api/jev/status` | `jev.Client.Status()` + today's cost + RPM used + snapshot history. |
| `POST /api/jev/test` (admin, 1 per 10 s) | Runs one canned decision (about 275 tokens, about $0.00001) and returns backend, snapshot, latency, cost, or a human-readable error. Also available as `cmd/jevcheck` / `make jev-check`. |
| `POST /api/playground/run` (10/min) | `{messages, context[], response_override?, route?, mode_override?}`. Runs the real pipeline (upstream call unless overridden); returns `{response, headers, gate_decision, trace_id}`; the audit arrives over WebSocket; `source=playground`. |
| `GET /api/export/replay?since&limit≤200` (admin) | Sanitised bundle `{schema:"trustagent.replay/v1", recorded_at, jev_backend, jev_snapshot, judge_model, traces:[{trace, gate_decisions, audit, events:[{offset_ms,type,data}]}]}` for the static demo. The same code runs as `cmd/export-replay`. |
| `POST /api/ws-ticket` | `{ticket, expires_in:30}` |
| `GET /api/audits`, `GET /api/audits/:id` | Gain `since`, `route`, `source`, `status` filters and the new fields. |

---

## 10. WebSocket protocol v2

```json
{"v":2,"seq":1042,"type":"audit_completed","ts":"2026-09-27T12:00:00.123456Z","trace_id":"0192…","data":{…}}
```

**Event types:**
- `hello`: `{protocol:2, server_version, last_seq, replayed_from, demo, jev:{backend,state,snapshot}, engine:{status}}`
- `gate_decision`: trace summary with previews (question ≤ 200 characters, response ≤ 300); full text via REST.
- `audit_queued`: `{lane, queue_depth}`
- `audit_started`
- `audit_completed`: full audit including claims with spans, `audit_id`, `trace_id`.
- `audit_failed`: `{kind, message}`
- `audit_dropped`
- `gap`: `{from_seq, to_seq, reason: slow_consumer|buffer_evicted, resync:"/api/traces?since=<ts>"}`
- `policy_updated`, `policy_error`
- `jev_snapshot_changed`
- `health`: sent only when a component's state changes.

**Server behaviour:**
- `Run` assigns `seq` and keeps a ring of the last `TRUTHTABLE_WS_REPLAY_BUFFER` (2,000) events.
- A connection with `?since_seq=N` gets a replay if N+1 is still in the ring; otherwise it gets `gap{…resync}`.
- A client with a full buffer drops events; before its next delivered event it receives `gap{from,to}`.

**Client algorithm** (documented for the React team): remember `last_seq`; reconnect with `since_seq`; on `gap`, fetch `GET /api/traces?since=` and `GET /api/audits?since=` and merge by `trace_id`.

---

## 11. Metrics (`trustagent_gateway_*`; `route` label bounded by the 64-route limit; no `model` label)

- `requests_total{route,format,action}`
- `overhead_seconds{route,mode}` with buckets 0.5 ms to 2 s
- `upstream_seconds{format}`
- `gate_decisions_total{route,mode,band,action,decided_by}`
- `gate_seconds{stage=tier0|context|kb|jev|total}`
- `gate_errors_total{kind}`
- `tier0_findings_total{code}`
- `jev_requests_total{backend,outcome}`
- `jev_seconds{backend}`
- `jev_cost_usd_total{backend}`
- `jev_input_tokens_total{backend}`
- `jev_breaker_state{backend}` (0/1/2)
- `jev_rate_limited_total{class}`
- `jev_snapshot_info{snapshot}`
- `jev_anomalies_total{kind}`
- `fallback_total{outcome}`
- `audit_jobs_total{lane,outcome=queued|completed|failed|dropped|sampled_out}`
- `audit_queue_depth{lane}`
- `audit_roundtrip_seconds`
- `ws_clients`, `ws_dropped_total`, `ws_gaps_total`
- `policy_version`, `policy_reload_errors_total`
- `trace_write_dropped_total`

The Go versions of `trustagent_{audits_total,hallucinations_detected_total,audit_duration_seconds,faithfulness_score,active_audits,claims_total}` are removed; the engine owns them as `trustagent_engine_*` (Python task). The Grafana JSON is rewritten with job-qualified queries. This is a breaking change and goes in the CHANGELOG.

---

## 12. New configuration keys (defaults)

**Upstream**

| Key | Default |
|---|---|
| `TRUTHTABLE_UPSTREAM_API_KEY` | "" |
| `TRUTHTABLE_UPSTREAM_AUTH_HEADER` | authorization |
| `TRUTHTABLE_ANTHROPIC_UPSTREAM_URL` | https://api.anthropic.com |
| `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` | "" |
| `TRUTHTABLE_FALLBACK_UPSTREAM_URL` / `…_API_KEY` | "" |
| `TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT` | 60s |
| `TRUTHTABLE_UPSTREAM_TIMEOUT` | 5m |
| `TRUTHTABLE_STREAM_IDLE_TIMEOUT` | 120s |
| `TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES` | 8MiB |

**Server**

| Key | Default |
|---|---|
| `TRUTHTABLE_WRITE_TIMEOUT` | 0 (per-route deadlines) |
| `TRUTHTABLE_READ_HEADER_TIMEOUT` | 10s |
| `TRUTHTABLE_INGEST_TIMEOUT` | 6m |
| `TRUTHTABLE_TEST_MODE` | false |
| `TRUTHTABLE_TRUSTED_PROXIES` | "" |
| `TRUTHTABLE_ADMIN_KEYS` | "" |
| `TRUTHTABLE_API_KEYS` | now accepts `name=key` entries |
| `TRUTHTABLE_DEMO_READONLY` | false |

**WebSocket, queues, storage**

| Key | Default |
|---|---|
| `TRUTHTABLE_WS_MAX_CLIENTS` | 200 |
| `TRUTHTABLE_WS_MAX_PER_IP` | 10 |
| `TRUTHTABLE_WS_REPLAY_BUFFER` | 2000 |
| `TRUTHTABLE_AUDIT_QUEUE_HIGH` | 200 |
| `TRUTHTABLE_SHUTDOWN_DRAIN_TIMEOUT` | 20s |
| `TRUTHTABLE_STORE_BODIES` | true |
| `TRUTHTABLE_RETENTION_DAYS` | 30 |

**Gate and policy**

| Key | Default |
|---|---|
| `TRUTHTABLE_POLICY_FILE` | config/policies.yaml |
| `TRUTHTABLE_POLICY_RELOAD_INTERVAL` | 5s |
| `TRUTHTABLE_GATE_MAX_CONCURRENCY` | 256 |
| `TRUTHTABLE_KB_RETRIEVE_TIMEOUT` | 150ms |

**Jev**

| Key | Default |
|---|---|
| `TRUTHTABLE_JEV_BACKEND` | auto |
| `OPENROUTER_API_KEY`, `TYPESAFE_API_KEY`, `TRUTHTABLE_JEV_API_KEY` (+ `_FILE`) | none |
| `TRUTHTABLE_JEV_MODEL` | per backend |
| `TRUTHTABLE_JEV_BASE_URL`, `TRUTHTABLE_JEV_ENDPOINT` | per backend |
| `…_ATTEMPT_TIMEOUT` | 2s |
| `…_ASYNC_TIMEOUT` | 8s |
| `…_MAX_RETRIES` | 1 |
| `…_MAX_RETRIES_ASYNC` | 2 |
| `…_HEDGE_AFTER` | 0 |
| `…_RPM` | 1000 |
| `…_SYNC_RESERVE` | 0.3 |
| `…_MAX_INFLIGHT` | 64 |
| `…_MAX_STATE_TOKENS` | 24000 |
| `…_CHARS_PER_TOKEN` | 4.0 |
| `…_BREAKER_FAILURES` | 5 |
| `…_BREAKER_COOLDOWN` | 30s |
| `…_AUTH_COOLDOWN` | 5m |
| `…_PRICE_PER_MTOK` | 0.042 |
| `…_DAILY_BUDGET_USD` | 0 (unlimited) |
| `…_APP_URL` | https://github.com/timkush1/TrustAgent |
| `…_APP_TITLE` | TrustAgent |
| `…_ZDR` | false |
| `…_SEND_SESSION_ID` | true |
| `…_MOCK_SEED` | 42 |
| `…_MOCK_LATENCY` | 40ms |
| `…_MOCK_FAIL_RATE` | 0 |
| `…_MOCK_HANG_RATE` | 0 |
| `…_MOCK_FIXTURES` | "" |

Invalid values now **fail startup** with a message naming the variable, instead of silently falling back to defaults (current behaviour, `config.go:91-107`). Compose passes `OPENROUTER_API_KEY` and `TYPESAFE_API_KEY` to the proxy.

---

## 13. Tests

**`internal/jev`**
- Normalisation: sum 0.99, sum 0, missing `confidence`/`choice`, shuffled keys, score recomputation.
- Per backend (`httptest`): path, model default, Bearer header, OpenRouter attribution headers, `session_id` only on OpenRouter.
- `200 text/html` gives `bad_content_type` and opens the breaker.
- Each error envelope, per status.
- 402 in-flight budget with `Retry-After` retries; 402 for credits does not retry and applies the auth cooldown.
- 429 with seconds and with HTTP-date `Retry-After`; 529; 524.
- A hang hits the attempt timeout, retries and succeeds.
- A `Retry-After` longer than the remaining deadline fails fast.
- 413 leads to the gate shrinking context once.
- Breaker state machine and half-open single probe under `-race`.
- Token bucket rates and sync reserve.
- Hedging: first wins, loser cancelled, both counted.
- Daily budget cutoff.
- Mock: deterministic per seed, labelled, produces 0.99 sums.
- `auto` selection over an env matrix.
- Token-estimator EWMA.

**`internal/tier0`**
- Table tests per code, plus equivalences: `1,200`=`1200`, `12%`=`12 percent`, `thirty`=`30`, `Jan 5, 2024`=`2024-01-05`.
- Rounding rule; URL trailing punctuation; quotes; citation range and ids; question-number exemption; `min_integer`.
- UTF-16 offsets with emoji and CJK.
- `FuzzCheck` (no panics; offsets in bounds).
- `BenchmarkCheck8kTokens` under 2 ms.

**`internal/gate`**
- Context extraction: extension stripped from the forwarded body; tool and Anthropic `tool_result`; rag tags; system opt-in; KB fallback vs always; KB timeout.
- State builder: budget respected, ranking, windowing, `RESPONSE_TOO_LONG`, tier0 sees full context.
- Band table: thresholds, disagreement guard, contradiction signal, tier0 escalate and block, `no_context` × 3, `no_factual_claims`, `JEV_NOT_EVALUATED`.
- Calibration: interpolation monotonic, Platt, lookup order, uncalibrated cap.

**`internal/policy`**
- Parse and validate errors with line numbers.
- First-match order; glob matching; tenants; header override (`none`, `stricter_only`, `any`).
- File reload (valid swaps; invalid keeps old); database version polling.
- Dry-run counts on fixture decisions, including scaling by sample rate.
- Calibration import validation.
- The SSRF guard rejects a URL in `fallback.upstream`.

**`internal/proxy`** (end to end with a fake upstream and mock Jev)
- **Modes:**
  - observe: body byte-identical, minimal headers, decision recorded asynchronously.
  - flag: headers plus CORS exposure.
  - block: content_filter and error styles for OpenAI and Anthropic; correct Content-Length; no stale Content-Encoding.
  - fallback: pass after fallback; still-bad leads to block; exactly one extra upstream call.
- **Enforcement:**
  - audit: `Would-Action` header present, nothing enforced.
  - enforce_ignore_errors + Jev 500: pass.
  - enforce + closed + breaker open: block.
  - Deadline exceeded: late decision recorded.
- **Encoding, parsing, auth:**
  - gzip upstream with client `Accept-Encoding: gzip` is still gated (B2).
  - Content-part arrays are forwarded (B3).
  - Auth matrix (B1): passthrough and inject modes, Bearer, x-api-key, `X-TrustAgent-Key` never reaches upstream.
- **Upstream plumbing:**
  - Base path and query kept for chat and generic (M2).
  - Client cancel propagates to upstream (M1).
  - Multi-value `Set-Cookie` preserved; hop-by-hop stripped (M3).
  - Upstream JSON error with `stream:true` passes through as JSON.
- **Streaming:**
  - observe_only: byte-identical; first chunk arrives before upstream finishes; annotation comment comes before `[DONE]`.
  - buffer: block gives a content_filter stream.
  - Anthropic streaming extraction.
  - Idle timeout.
  - Client disconnect gives no audit.
- **Other:** test mode off returns 400 and on is gated (M7); `X-Request-ID` validation and duplicates (B4); 413; `/v1/completions` gated (M9).

**Remaining packages**
- `internal/websocket`: one message per frame (B5); a race test with 50 goroutines broadcasting, slow and unregistering clients under `-race` (B6); seq monotonic; replay; gap on drop; connection caps.
- `internal/worker`: `ErrQueueFull`; high-lane priority; `Stop` drains (M6); no panic on submit after stop; `audit_failed` events.
- `internal/store` (`TEST_DATABASE_URL`): 0002 applies on top of 0001 and is idempotent; concurrent migrators; duplicate `SaveAudit` adds no claims; batch writer; overview zero-filled buckets; dry-run query; activate and rollback atomicity; retention.
- `cmd/proxy`: overview 503 without a database; settings masking; admin-only writes; demo read-only; `jev/test` rate limit; CORS allow and expose lists; trusted proxies; memory limiter eviction (M8).

---

## 14. Load test

- `scripts/load/gateway-overhead.js` runs against `cmd/mock-upstream` (compose `load` profile, configurable upstream latency).
- It parses `X-TrustAgent-Overhead-Ms` into a k6 `Trend`.

| Scenario | Target (overhead p95) |
|---|---|
| (a) gate disabled | < 3 ms |
| (b) observe, tier0 + async mock Jev | < 5 ms |
| (c) flag, mock Jev 0 ms | < 10 ms |
| (d) flag, mock Jev 150 ms | < 175 ms |
| (e) block with buffered streams | report only |
| (f) Jev hang rate 15% with hedge 400 ms | p99 ≤ deadline, 0% 5xx |

- Go benchmarks: tier0, state build, SSE relay per MB.
- `make load-test-gate` runs it. Results go in `docs/GATEWAY.md` with the hardware stated.

---

## 15. Ordered task list (S ≤ ½ day, M 1–2 days, L 3–5 days)

0. (Owner) Baseline commit as `timkush1 <timk@mail.tau.ac.il>`, then branch `feat/jev-gateway`. Root untracked files `ts_sdk_python*.md` look like research notes; the owner should decide whether they belong in the baseline. **S**
1. Proto additions (§7) + `make proto` + stub Python servicers returning `UNIMPLEMENTED`. **S**
2. Config restructure: new keys, fail-fast validation, `name=key` API keys, key ids, masking helpers. **M**
3. `internal/trace` + upstream plumbing: URL join, header filter, auth modes, transport and timeouts, deadline middleware, 413, ReverseProxy `Rewrite` (B1, B2, M1, M2, M3, M12). **L**
4. `internal/llmapi`: formats for OpenAI chat, completions and Anthropic; tolerant parsing; SSE reader and parsers; block and fallback builders (B3, M9). **L**
5. WebSocket hub v2: single owner, per-frame, seq, ring, gap, caps, tickets (B5, B6, M10). **M**
6. Worker lanes + `Submit` error + drain + `Audit` RPC client + health pinger (M4, M5, M6). **M**
7. `internal/tier0` + fuzz and benchmarks. **M**
8. `internal/jev` core: types, normalise, HTTP backends, errors, auto-select, mock. **M**
9. `internal/jev` resilience: retry, hedge, breaker, budget, cost, snapshot, estimator. **M**
10. `internal/gate`: context acquisition (KB concurrent), state builder, question sets, band, calibration. **L**
11. `internal/policy`: schema, parse, validate, match, resolve, file source + reload, header overrides. **M**
12. Handler pipeline, non-streaming: modes, enforcement ladder, fallback + re-gate, headers, late decisions, test-mode gating (M7). **L**
13. Streaming: observe_only relay + annotation hold-back, buffer mode, synthesized streams, `/v1/messages` streaming. **L**
14. Migration 0002 + runner transaction and advisory lock + trace batch writer + `SaveAudit` fix (B4) + retention. **M**
15. Policy database source, versions API, dry-run, calibration import, reliability endpoint. **M**
16. REST: overview, traces, trace detail, settings, engine, `jev/status` + `jev/test`, playground, export-replay (+ CLI), `cmd/jevcheck`. **L**
17. Middleware: CORS expose and allow, trusted proxies, limiter fixes, admin scope, demo read-only (M8). **S**
18. Metrics rename + new gateway metrics + Grafana rewrite (M11). **M**
19. `cmd/mock-upstream`, compose profiles (`demo`, `load`), `.env.example`, `config/policies.yaml`. **M**
20. Load test + benchmarks + published numbers. **M**
21. Docs: `GATEWAY.md` (modes, headers, policy reference, capacity note: about 16 RPS of synchronous Jev at the default RPM budget), the env section of `JEV-ACCESS.md`, ARCHITECTURE and PLAN Phase 8, CHANGELOG (breaking: metrics, WS v2, proxy auth header), version bump across `VERSION` and its 4 mirrors. **M**

Tests are written inside each task. Critical path: 1 → 3 → 4 → 8 → 10 → 11 → 12 → 13. Tasks 5, 6, 7, 9 and 14 can run in parallel after task 2.

**Cross-team dependencies:**
- Python: `Audit`/`RetrieveContext` RPCs, the `UNKNOWN` enum value, claim spans in UTF-16, `judge_degraded`, `trustagent_engine_*` metric names, a cached `HealthCheck` with the dependency key renamed from "ollama" to "judge", and the calibration export in `trustagent.calibration/v1` format.
- React: WS v2 client and resync, exposed headers, the `editable`/`source` flags on policies, MOCK badges, inline highlighting from spans.