# The TrustAgent gateway

TrustAgent sits between your app and the LLM API. Your app keeps its OpenAI or
Anthropic SDK and only changes the base URL. For every answer, the gateway:

1. forwards the request to the upstream model and reads the answer;
2. runs the **Tier-0 detectors** (deterministic Go checks: invented numbers,
   dates, URLs, quotes and citations, secrets, system-prompt leaks);
3. asks the **fast check (Jev)** every evaluator of the route as questions in
   **one** call, with the evidence the request already carries;
4. decides with the route's **policy**: observe, flag, block or fallback;
5. queues a **deep audit** (claim by claim, by an LLM judge) off the request
   path, and streams everything to the dashboard.

This page is the reference for what reaches your client: modes, headers and
trailers, streaming, failures, block and fallback, credentials, errors and
test mode. The design is in [v2/SPEC.md](v2/SPEC.md) §2.9 and §2.12; every
setting named here is in [CONFIGURATION.md](CONFIGURATION.md).

- [1. Point a client at it](#1-point-a-client-at-it)
- [2. Modes](#2-modes)
- [3. Response headers](#3-response-headers)
- [4. Streaming](#4-streaming)
- [5. Reading a decision later (long-poll)](#5-reading-a-decision-later-long-poll)
- [6. When something fails](#6-when-something-fails)
- [7. Block](#7-block)
- [8. Fallback](#8-fallback)
- [9. Credentials: passthrough and inject mode](#9-credentials-passthrough-and-inject-mode)
- [10. Errors](#10-errors)
- [11. Test mode](#11-test-mode)
- [12. Capacity and sampling](#12-capacity-and-sampling)
- [13. Load test](#13-load-test)
- [14. Metrics](#14-metrics)

---

## 1. Point a client at it

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",          # the gateway, not api.openai.com
    api_key="sk-...",                              # your OpenAI key (passthrough mode)
    default_headers={
        "X-TrustAgent-Key": "<a key from TRUTHTABLE_API_KEYS>",
        "X-TrustAgent-Route": "support-bot",       # which policy route applies
    },
)
```

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://localhost:8080",              # the SDK adds /v1/messages
    api_key="sk-ant-...",
    default_headers={"X-TrustAgent-Key": "<key>", "X-TrustAgent-Route": "support-bot"},
)
```

- **The proxy key** (`X-TrustAgent-Key`) says who is calling; it is never
  forwarded. `Authorization` / `x-api-key` belong to the upstream (see
  [§9](#9-credentials-passthrough-and-inject-mode)).
- **The route** is picked by the first `routes[]` entry in
  `config/policies.yaml` whose `match` (headers, paths, models, key labels)
  fits the request; with no match, `defaults` apply as route `default`.
- **Evidence** is what the request already carries: documents in the system
  prompt, a `Context:` block in the user message, tool results, or the body
  extension below. There is no retrieval by default; with no evidence,
  groundedness is `not_assessed` and Jev is never asked about it.
  With the optional knowledge base on (`TRUTHTABLE_KNOWLEDGE_ENABLED`, the
  `knowledge` profile), retrieval starts together with the upstream call and
  must finish within `TRUTHTABLE_KNOWLEDGE_DEADLINE`; a miss means no
  knowledge evidence for that answer, never a failure.

  ```json
  {"model": "gpt-4o-mini", "messages": [...],
   "trustagent": {"context": [{"id": "refund-policy", "text": "Annual plans: refund within 30 days."}]}}
  ```

  The `trustagent` object is removed before the request is forwarded.

**Which endpoints are gated:**

| Endpoint | Gated | Notes |
|---|---|---|
| `POST /v1/chat/completions` | yes | OpenAI chat, JSON and SSE |
| `POST /v1/completions` | yes | Legacy completions (`choices[i].text`) |
| `POST /v1/messages` | yes | Anthropic Messages, JSON and SSE; errors in Anthropic's shape |
| `POST /v1/messages/count_tokens` | no | Passed to the Anthropic upstream |
| `/v1/responses*` | no | Passed through with `X-TrustAgent-Status: skipped:unsupported_api` |
| `/v1/models*` | no | Passed through: to the Anthropic upstream when the request carries `anthropic-version` or an `x-api-key` that is not a proxy key (what the Anthropic SDK sends), otherwise to `TRUTHTABLE_UPSTREAM_URL` |
| `/v1/embeddings` | no | Passed through to `TRUTHTABLE_UPSTREAM_URL` |
| `GET /v1/trustagent/decisions/:trace_id` | — | The decision of an earlier request ([§5](#5-reading-a-decision-later-long-poll)) |

---

## 2. Modes

Each route has a `mode` (`TRUTHTABLE_GATE_DEFAULT_MODE` seeds the default,
`observe`). The **action** is what the gateway did to this answer.

| Mode | What your client gets | Sync? | Action header |
|---|---|---|---|
| `observe` | The upstream bytes, unchanged. The decision is recorded for the dashboard only. | No: gated after the answer is sent | `observe` (`Status: deferred`) |
| `flag` | The upstream bytes, plus the decision in headers (or trailers). | Yes | `pass` or `flag` |
| `block` | A configured block message instead of an answer the policy blocks. | Yes | `pass`, `flag` or `block` |
| `fallback` | Where `block` would block: a second, grounded answer, checked again before it is sent. | Yes | `pass`, `flag`, `fallback` or `block` |

- **Would-action.** In observe mode, and wherever the action is milder than
  what the evaluators found, `X-TrustAgent-Would-Action` says what a stricter
  mode would have done (the dashboard shows "would block").
- **Effective mode.** A route can run below its configured mode; the
  dashboard's Policies page and `make doctor` list each downgrade with its fix:
  - `block`/`fallback` on a **passthrough stream** run as `flag`
    (`stream_not_buffered`): the answer is already on its way. Use
    `streaming: buffer` ([§4](#4-streaming)).
  - With `require_calibration: true` (the default), a block needs every
    block-set evaluator to have a usable, adopted calibration for the current
    Jev snapshot; otherwise the action is capped at `flag`, and the reason
    says why:
    - `calibration_missing`: at least one of those checks has no calibration
      that can enable blocking for this snapshot (none at all, a quality below
      `measured`, or route thresholds not yet switched to the calibrated
      space, which adopting a calibration does). Label answers, calibrate and
      adopt.
    - `uncalibrated_snapshot`: each check that lacks one has calibrations, but
      only for other snapshots: the decision model changed since they were
      fitted. Recalibrate for the new snapshot.

    A detector in `detectors.block_on` still blocks: it needs no calibration.
    See [EVALUATORS.md](EVALUATORS.md#calibration).
- **Client override.** With `overrides.client_mode: stricter_only`, a client
  may ask for a stricter mode with `X-TrustAgent-Mode` (observe → flag →
  block/fallback), never a milder one.
- **Scope in 2.0 P3.** Observe and flag are production-ready. Block and
  fallback are demonstrable in `make demo` (a simulated, `mock` calibration)
  and in production after a measured calibration, or on a route with
  `require_calibration: false`.

---

## 3. Response headers

Every header is listed in `Access-Control-Expose-Headers`. With
`headers: action_only`, a route sends only Trace-Id, Request-ID and Action.

| Header | Values | When |
|---|---|---|
| `X-TrustAgent-Trace-Id` / `X-Request-ID` | UUIDv7 / your `X-Request-ID`, else the trace id | Always |
| `X-TrustAgent-Route` | `<route>@v<policy version>` | Gated routes |
| `X-TrustAgent-Mode` / `X-TrustAgent-Action` | effective mode / action | Gated routes |
| `X-TrustAgent-Would-Action` | `pass`, `flag` or `block` | When it differs from the action |
| `X-TrustAgent-Status` | `ok`, `partial`, `not_assessed`, `skipped:<reason>`, `timeout`, `error:<kind>`, `deferred` | Gated routes |
| `X-TrustAgent-Verdict`, `-Risk` (2 decimals), `-Calibrated`, `-Degraded` | per decision | Synchronous decisions |
| `X-TrustAgent-Reasons` | up to 8, comma-separated (`groundedness:critical`, `det:urls_not_in_evidence`, `fail_closed`, ...) | When there are any |
| `X-TrustAgent-Checked-Against` | `request_system:2,tool_results:1` or `none` | Gated routes |
| `X-TrustAgent-Decision-Model` | the Jev snapshot | When Jev was called |
| `X-TrustAgent-Gate-Ms` / `-Overhead-Ms` | integers | Gated routes |
| `X-TrustAgent-Audit` | `queued`, `sampled_out`, `dropped`, `disabled`, `not_assessed` | Gated routes |
| `X-TrustAgent-Source` | `test`, `trafficgen`, `playground` | Traffic that is not a real client |

`X-TrustAgent-Overhead-Ms` is the time the gateway added: the whole request
minus the upstream's own time.

---

## 4. Streaming

A request with `"stream": true` is relayed as server-sent events. The branch
is chosen from the **response**: a 2xx `text/event-stream` answer is relayed
as a stream; anything else takes the JSON path.

### Passthrough (the default, `streaming: passthrough`)

Events reach your client as the model writes them; the gateway assembles the
text alongside.

- **Up front:** `X-TrustAgent-Trace-Id`, `X-Request-ID`, `-Route` and
  `-Source`, and a `Trailer:` header that declares the decision headers.
- **At the end:** the terminal event (`data: [DONE]` or
  `event: message_stop`) is held until the decision arrives or the route's
  deadline passes. Just before it the gateway writes an SSE comment (unless
  `stream_annotations: none`):

  ```
  : trustagent {"trace_id":"…","action":"flag","would_action":"flag","status":"ok","verdict":"contradicted","max_risk":0.97,"reasons":["contradiction:critical"]}
  ```

  and then sends the decision as **trailers**: Mode, Action, Would-Action,
  Status, Verdict, Risk, Calibrated, Degraded, Reasons, Checked-Against,
  Decision-Model, Gate-Ms, Overhead-Ms and Audit. With
  `headers: action_only` the only trailer is Action.
- **Observe routes** send their deferred headers up front and do not hold the
  terminal event.
- **Block and fallback** cannot take back text that was already sent, so on a
  passthrough stream they run as flag (`stream_not_buffered`).
- **Client disconnects:** if the client leaves after the model's finish
  reason, the text is still gated (never enforced; flag
  `client_disconnected`) and audited. If it leaves before, the gate is skipped
  (`skipped:client_disconnected`).

**Limits.** SDKs drop SSE comments, and browsers and nginx (the dashboard's
proxy) drop trailers. A client that needs the decision of a stream calls the
long-poll ([§5](#5-reading-a-decision-later-long-poll)); `trafficgen` reads
trailers because it talks to the proxy directly.

### Buffer (`streaming: buffer`)

The gateway reads the whole stream, decides, then sends the stream it
decided on, so block and fallback work on streams.

- While it waits it sends a keepalive comment every `buffer_keepalive_s`
  (15 s). The first keepalive commits the response headers: after that, the
  decision travels as the comment and trailers, and an error-style block is
  sent in message style instead.
- A blocked stream is a synthesized one: one content chunk with the block
  message, then `finish_reason: "content_filter"` (OpenAI) or the route's
  `anthropic_stop_reason` (Anthropic), then the terminal event. A fallback
  answer is re-emitted as a stream that ends with `stop` / `end_turn`.
- Your client sees the first token only when the whole answer is decided:
  buffer routes trade time-to-first-token for enforcement.

### Stream limits

| Limit | Default | Setting |
|---|---|---|
| Largest answer the gateway gates | 8 MiB | `TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES` (a larger answer is a skip: it passes on observe, flag and `fail: open`, and blocks on closed block/fallback routes, [§6](#6-when-something-fails)) |
| Longest gap between upstream events | 120 s | `TRUTHTABLE_STREAM_IDLE_TIMEOUT` |
| Time to the upstream's response headers | 60 s | `TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT` |
| Whole upstream request | 5 min | `TRUTHTABLE_UPSTREAM_TIMEOUT` |
| `/v1` write deadline | upstream timeout + 30 s, extended by keepalives and before a fallback call | `TRUTHTABLE_WRITE_TIMEOUT=0` (per-route deadlines) |

---

## 5. Reading a decision later (long-poll)

```bash
curl -H "X-TrustAgent-Key: $KEY" \
  "http://localhost:8080/v1/trustagent/decisions/$TRACE_ID?wait_ms=5000"
```

```json
{"trace_id": "0192f3…", "state": "decided", "decision": {"action": "flag", "status": "ok", "…": "…"}}
```

- `wait_ms` is at most 10000; the call returns as soon as the decision exists,
  else with `state: "pending"` and `decision: null`.
- Only the key label that sent the request can read its decision (ingest
  scope). A route with `headers: action_only` returns only `action` and
  `status`.
- The trace id comes from `X-TrustAgent-Trace-Id`, which a stream sends up
  front.

---

## 6. When something fails

Every route has `fail: open` (the default, `TRUTHTABLE_GATE_FAIL_MODE`) or
`fail: closed`. **`closed` only matters in block and fallback modes.**

**The fast check fails** (off, timeout, error, budget, breaker, rate limit,
overload, missing credentials): `X-TrustAgent-Status` says so
(`skipped:<reason>`, `timeout`, `error:<kind>`) and `-Degraded` names it.

| Route | What happens |
|---|---|
| observe, flag | The answer passes; the detectors' and assessed evaluators' findings still apply. |
| block/fallback, `fail: open` | The same: the action comes from what was assessed. |
| block/fallback, `fail: closed` | Block (reason `fail_closed`). A `sampled_out` skip never blocks. |

**The upstream fails.** These are never enforced: the stored decision has
`enforced: false`, and the action is `pass`.

| Upstream answer | What your client gets | Status |
|---|---|---|
| Non-2xx | The upstream's own status and body (in inject mode, with the injected key masked: see [§9](#9-credentials-passthrough-and-inject-mode)) | `skipped:upstream_error` |
| Transport error or timeout | 502 `upstream_error` / 504 `upstream_timeout`, in the route's error shape | `skipped:upstream_error` |
| 2xx that is not JSON, too large, or an undecodable stream | Gated as a skip: passes on observe, flag and `fail: open`; **blocks** on closed block/fallback routes | `skipped:<reason>` |

**Answers the gateway does not gate.**

- **Tool calls only** (no text): passed through unenforced. Tool calls are
  not checked in 2.0.
- **Several choices** (`n > 1`): only choice 0 is gated (trace flag
  `multi_choice`); a block replaces every choice.
- **A decision that arrives after the deadline** is recorded (`late`) and
  shown on the dashboard, but never enforced.

---

## 7. Block

A route blocks only when the policy is sure: a **critical** evaluator that is
block-eligible and **corroborated** by a second signal (another critical
evaluator or a Tier-0 detector), or a detector listed in `detectors.block_on`.
Numbers and dates never flag or block alone.

**Block readiness.** For a route in block or fallback mode, the Policies page
shows a checklist per block-eligible evaluator; unmet items become downgrades:

1. evidence sources that can produce evidence, for evaluators that require it;
2. at least one reachable corroborator;
3. a measured calibration for the current snapshot (`threshold_space:
   calibrated`), or `require_calibration: false`;
4. `streaming: buffer` if the route streams;
5. a warning if an enforcing route trusts `request_context` (end users could
   inject "documents").

**What a blocked client receives** (`block:` in the route; the message
defaults to "I can't provide a verified answer to that right now."):

| Format | `style: message` (default) | `style: error` |
|---|---|---|
| OpenAI chat | HTTP 200; every `choices[i].message` becomes the block message (tool calls removed), `finish_reason: "content_filter"`; id, model and usage kept | HTTP `block.status` (400, 403, 409, 422 or 451; default 422): `{"error":{"type":"trustagent_policy_block","code":"trustagent_blocked",…}}` |
| OpenAI completions | HTTP 200; `choices[i].text` is the message, `finish_reason: "content_filter"` | as above |
| Anthropic | HTTP 200; one text block, `stop_reason` from `anthropic_stop_reason` (`refusal` or `end_turn`) | HTTP 400 `invalid_request_error` |

Choose `end_turn` for Anthropic SDKs that reject `stop_reason: "refusal"`.

---

## 8. Fallback

In `fallback` mode, an answer the policy would block is replaced by a second
answer written only from the evidence:

1. The gateway builds a same-format request: the route's `fallback.model` (or
   the original model), a system prompt that says to answer only from
   `CONTEXT`, the evidence packed as `[id] text` lines (up to 8k tokens) and
   `stream: false`. With no evidence the prompt instead tells the model to
   say when it is not certain and to leave out numbers, dates, URLs and
   quotations.
2. It sends it **once**, to the same upstream (`fallback.upstream: same`) or
   to `TRUTHTABLE_FALLBACK_UPSTREAM_URL` (`fallback`). A fallback upstream of
   another API format blocks instead (`fallback_format_mismatch`).
3. It **re-gates** the new answer (attempt 1, a fresh deadline).
4. If the re-gate's action is `pass` or `flag`, your client gets the fallback
   answer with `Action: fallback` (trace flag `fallback_used`).
5. Otherwise `on_fail` applies: `block` (the block body above), `flag` (the
   fallback answer, `Action: flag`) or `original` (the original answer,
   `Action: flag`). A re-gate that fails or times out on a closed route counts
   as "otherwise".
6. If the fallback call itself fails (transport error, non-2xx, non-JSON, no
   text), there is no re-gate: reason `fallback_failed`, then `on_fail:
   block` blocks and `flag`/`original` deliver the original with `Action:
   flag`.

The deep audit always checks the **original** answer. The trace keeps both
decisions (attempt 0 and attempt 1). `trustagent_gateway_fallback_total` counts
the outcomes: `delivered`, `on_fail_block`, `on_fail_flag`, `on_fail_original`,
`failed` and `format_mismatch`.

**In the demo.** The demo has no real upstream, so in test mode the fallback
answer of a scripted scenario comes from `fallback.response` in
`contracts/demo/scenarios.v1.json`, with no upstream call, when the first
answer was a `test_response` and `X-TrustAgent-Scenario` names a scenario that
has a fallback. Everywhere else the gateway calls the upstream.

---

## 9. Credentials: passthrough and inject mode

Each upstream decides on its own:

| Upstream | Base URL | Key that turns on inject mode |
|---|---|---|
| OpenAI format | `TRUTHTABLE_UPSTREAM_URL` (empty: api.openai.com) | `TRUTHTABLE_UPSTREAM_API_KEY` (sent on `TRUTHTABLE_UPSTREAM_AUTH_HEADER`, default `Authorization: Bearer`) |
| Anthropic | `TRUTHTABLE_ANTHROPIC_UPSTREAM_URL` | `TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY` (sent as `x-api-key`) |
| Fallback | `TRUTHTABLE_FALLBACK_UPSTREAM_URL` | `TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY` |

- **Passthrough** (that upstream has no key): your client's `Authorization` /
  `x-api-key` go upstream untouched.
- **Inject** (that upstream has a key): every client credential is stripped
  and the configured one is set. `anthropic-version` still passes through.
- When **any** upstream injects, a client may send its proxy key as the Bearer
  token or `x-api-key` (the SDK's usual key slot), and open ingest
  (`TRUTHTABLE_INGEST_AUTH=open`) is refused: anonymous callers would spend
  your upstream credential.
- The proxy key is never forwarded, whatever the mode.
- **Keys in the dashboard.** Each upstream key can also be saved on
  **Settings → Connections** (stored encrypted as `upstream-default`,
  `upstream-anthropic`, `upstream-fallback`) and bound to its upstream there.
  An environment key wins and makes that binding read-only. A saved key is
  bound to the URL's origin: it is only ever sent there, and pointing the
  upstream at another origin requires entering the key again
  ([SECURITY.md](SECURITY.md#encryption-and-origin-binding)).
  `TRUTHTABLE_UPSTREAM_URL` in `.env` pins the upstream for good: the
  dashboard cannot move it.
- **Rules the dashboard enforces.** Turning inject mode on needs an ingest key
  first (409 `ingest_key_required`): a dashboard key with the `ingest` scope
  (the setup wizard mints one), or an environment ingest or admin key, so your
  apps are never locked out. A fallback upstream on another host
  needs its own key (422 `fallback_key_required`), so your main key never goes
  to a third party.
- **Upstream error bodies.** In inject mode the upstream may echo the key it
  received in an error. A non-2xx body is masked before it reaches your
  client (the injected key and every key-shaped string become `[redacted]`)
  and cut to 64 KiB.

---

## 10. Errors

Errors the gateway itself returns on `/v1` come in the route's API shape, so
SDKs parse them:

```json
{"error": {"message": "…", "type": "trustagent_error", "param": null, "code": "rate_limited"}}
```

```json
{"type": "error", "error": {"type": "rate_limit_error", "message": "…"}}
```

| Status | OpenAI `code` | Anthropic `type` | When |
|---|---|---|---|
| 400 | `bad_request`, `test_mode_disabled` | `invalid_request_error` | Unreadable body; `test_response` without test mode |
| 401 | `unauthorized` | `authentication_error` | Missing or wrong proxy key |
| 403 | `forbidden` | `invalid_request_error` | The key lacks the ingest scope |
| 404 | `not_found` | `invalid_request_error` | A `/v1` path the gateway does not serve |
| 413 | `request_too_large` | `invalid_request_error` | Body over `TRUTHTABLE_MAX_BODY_BYTES` |
| 429 | `rate_limited` (with `Retry-After`) | `rate_limit_error` | Over `TRUTHTABLE_RATE_LIMIT_PER_MINUTE` for this key |
| 502 | `upstream_error` | `api_error` | The upstream could not be reached |
| 504 | `upstream_timeout` | `api_error` | The upstream did not answer in time |

Upstream errors are not rewritten: a non-2xx answer from the upstream reaches
your client as the upstream sent it ([§6](#6-when-something-fails)).

---

## 11. Test mode

`TRUTHTABLE_TEST_MODE=true` lets a request carry its own answer and scenario:

- `"test_response": "…"` in the body: the gateway skips the upstream and
  gates that text, as if the model had written it (`"stream": true` gets a
  synthesized stream).
- `X-TrustAgent-Scenario: <id>`: the mock decision model and mock judge
  answer from that scenario of `contracts/demo/scenarios.v1.json`.

Without test mode, `test_response` returns **400 `test_mode_disabled`** in the
route's error shape. `make demo` and the e2e stack turn test mode on;
production must not.

---

## 12. Capacity and sampling

Each gated answer makes **one** decision call, whatever the number of
evaluators (they are questions of the same call).

- **Synchronous gates** (flag, block, fallback) may use the whole rate limit:
  `sync_rps ≈ TRUTHTABLE_GATE_JEV_RPM × (1 − 0) / 60`, about 16 answers per
  second at the default 1,000 calls per minute (the vendor allows 1,200).
- **Observe gates** are asynchronous and may use only
  `1 − TRUTHTABLE_GATE_JEV_SYNC_RESERVE` of it (70% by default, about 11.7
  answers per second). Above that, lower the route's `sample_rate` (observe
  only; the detectors always run): unsampled answers are
  `skipped:sampled_out`.
- `TRUTHTABLE_GATE_JEV_MAX_INFLIGHT` (64) caps concurrent decision calls and
  `TRUTHTABLE_GATE_MAX_CONCURRENCY` (256) concurrent asynchronous gates;
  overflow is `skipped:overload`.
- **Spend.** Every decision call and every deep audit is recorded with its
  cost in the cost ledger (the vendor's reported cost where it gives one,
  otherwise estimated from tokens). Past the daily fast-check budget
  (Settings → Budgets, seeded by `TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY`),
  fast checks stop (`skipped:budget`): observe and flag routes stop at 75% of
  it (`TRUTHTABLE_SPEND_ENFORCING_RESERVE`), so enforcing routes keep a
  reserve. Playground, Studio and key-check calls can never spend that
  reserve. The deep-audit budget (`TRUTHTABLE_SPEND_CAP_LLM_USD_DAY`) is hard
  in every lane: past it, sampled audits are dropped (`audit_dropped`,
  reason `llm_budget`) and `POST /api/audit` answers 503 `budget_exhausted`.
  The **Savings** page shows what was spent, each figure labelled (a cost
  the vendor did not report is an estimate); typical costs are in
  [JEV-ACCESS.md](JEV-ACCESS.md).

---

## 13. Load test

`scripts/load/gateway-overhead.js` (k6) measures the time the gateway adds,
from `X-TrustAgent-Overhead-Ms`, against `mock-upstream` (the `load` compose
profile), so no vendor is called and nothing is spent:

```bash
make load-test-gate            # builds and starts the load stack, runs k6, stops the stack
```

| Scenario | Target (overhead p95) |
|---|---|
| (a) gate disabled | < 3 ms |
| (b) observe: Tier-0 + asynchronous mock Jev | < 5 ms |
| (c) flag, mock Jev answering in 0 ms | < 10 ms |
| (d) flag, mock Jev answering in 150 ms | < 175 ms |
| (e) block with buffered streams | reported only |
| (f) Jev hangs 15% of the time, hedge at 400 ms | p99 ≤ the deadline, 0% 5xx |

These are design targets, not measurements. Published results state the
hardware they ran on.

---

## 14. Metrics

The proxy serves Prometheus metrics on `TRUTHTABLE_METRICS_PORT` (8002, on the
compose network only), all named `trustagent_gateway_*`; the engine's are
`trustagent_engine_*` on port 8001. The `route` label is capped at 64 routes;
there is no model label.

| Area | Metrics |
|---|---|
| Requests | `requests_total{route,format,action}`, `overhead_seconds{route,mode}`, `upstream_seconds{format}` |
| Gate | `gate_decisions_total{route,mode,action,status}`, `gate_seconds{stage}`, `gate_errors_total{kind}`, `evaluator_levels_total{evaluator,level,stage}`, `detector_fired_total{detector}` |
| Fast check | `jev_requests_total{backend,outcome}`, `jev_seconds{backend}`, `jev_cost_usd_total{backend,component}`, `jev_breaker_state{backend}`, `jev_snapshot_info{snapshot}`, ... |
| Audits and fallback | `fallback_total{outcome}`, `audit_jobs_total{lane,selection,outcome}`, `audit_queue_depth{lane}`, `audit_roundtrip_seconds`, `uniform_samples_today` |
| WebSocket, policy, storage | `ws_clients`, `ws_gaps_total{reason}`, `policy_version`, `trace_write_dropped_total`, `retention_lag_seconds` |
| Spend | `spend_usd_today{kind}`, `spend_reserved_usd{kind}`, `cost_write_dropped_total` (cost-ledger rows lost: above zero, spend undercounts) |

`COMPOSE_PROFILES=observability make up` adds Prometheus and Grafana
(<http://localhost:3001>, with a Spend row: today's spend and reservations,
fast-check spend by component, audits paused by the budget, dropped cost
rows); every dashboard query filters by `job="proxy"` or `job="engine"`, and
`scripts/tests/test_grafana.py` fails on a query naming a metric the code does
not register. `make doctor` checks that each endpoint serves only its own
prefix.
