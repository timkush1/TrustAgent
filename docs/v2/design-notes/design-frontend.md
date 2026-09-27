# TrustAgent Dashboard and Landing Page: Frontend Design Spec

Scope: everything under `frontend-react/`, plus the dashboard container, the GitHub Pages workflow and the demo-fixture tooling. Where the UI depends on backend work, section 0 lists it as a contract that the gateway and engine designers must satisfy.

I checked package versions with `npm view` on 2026-09-27 and read the current source to confirm the bugs. **[A]** marks an assumption.

---

## 0. Backend contracts the UI depends on

The UI still works when an item is missing (section 4.6 covers fallbacks). Each item is needed for the full experience.

| # | Contract | Owner | Fixes |
|---|---|---|---|
| C1 | **One JSON message per WebSocket frame.** Drop the `\n` batching in `hub.go:276-280`. Every message uses the v2 envelope (4.3). Queue overflow drops the oldest message and emits `gap{dropped}` rather than dropping silently. | Go | B5, UI #1 |
| C2 | `audit_error` and `connected` move to the same envelope. | Go | M10, UI #3 |
| C3 | `GET /api/system/status` (auth required) returns a real engine ping (`Ping`/HealthCheck with a 1 s deadline), plus judge, Qdrant, DB, Jev and gate state (type in 4.2). `/health` stays as a liveness check. | Go + Py | M4, UI #4 |
| C4 | `GET /api/audits?since=<RFC3339>&cursor=&limit=&verdict=&gate_action=&route=&q=` (cursor paging, `since` for resync) and `GET /api/audits/:id` with the full detail from 4.2. | Go | UI #17/#18 |
| C5 | `GET /api/stats/overview?window=&bucket=`, aggregated in Postgres. Returns 503 when there is no DB. | Go | UI #5/#10 |
| C6 | `GET /api/gate/decisions?since=&cursor=`<br>`GET/PUT /api/gate/policies` (ETag / If-Match; 409 on conflict)<br>`GET /api/gate/policies/history`<br>`POST /api/gate/policies/dry-run`<br>`GET/PUT /api/gate/calibration`<br>`POST /api/gate/test` (one small noul call) | Go | — |
| C7 | Claims carry `span_start` / `span_end` (**Unicode code-point** offsets into `llm_response`, `-1` if unknown), `decided_by`, `escalation_reason`, `jev{choice, probabilities, confidence}` and `explanation`. The audit carries `judge{provider, model, fallback_active}`, `gate`, `cascade` and `messages[]`. | Py + proto | UI #14, #23 |
| C8 | The proxy accepts a client `X-Request-ID` only if it matches `^[A-Za-z0-9._-]{8,64}$`, and returns 409 on a duplicate. `/api/audit` returns 503 `queue_full` / `engine_unavailable` instead of an unconditional 202. | Go | B4, M6 |
| C9 | The dashboard key goes in `X-API-Key`, which the proxy strips before forwarding. The upstream key is injected server-side. The WebSocket also accepts the subprotocol `trustagent.v2, bearer.<key>` (keeps the key out of URLs), with `?api_key=` still supported. | Go | B1 |
| C10 | CORS: `Access-Control-Expose-Headers: X-TrustAgent-*, X-Request-ID`; methods `GET, POST, PUT, OPTIONS`. Only needed when the API base is cross-origin, because the default is same-origin (section 8). | Go | — |
| C11 | Test mode (`test_response`) sits behind `TRUTHTABLE_ENABLE_TEST_MODE`, and `status.proxy.test_mode` reports it. | Go | M7 |

---

## 1. Stack (versions verified today)

**Runtime dependencies**

| Package | Version | Why |
|---|---|---|
| react, react-dom, @types/react(-dom) | **19.3.0** | `<ViewTransition>` is stable. Used through the router's `defaultViewTransition` for list→detail. |
| @tanstack/react-router, @tanstack/router-plugin | **1.170.40**, **1.168.41** | File routes, typed and validated search params (all view state lives in the URL), `autoCodeSplitting`, `basepath` for GitHub Pages. |
| @tanstack/zod-adapter, zod | **1.167.0**, **4.6.5** | Search-param schemas. The same schemas validate the policy, calibration and eval files. |
| @tanstack/react-query (+ devtools, dev only) | **5.104.0** | Cache, abort on key change (fixes the fetch races), `setQueryData` from the stream. |
| @tanstack/react-table | **9.2.4** | Headless tables. v9 registers only the features you use via `tableFeatures()` [A: API per the v9 docs]. |
| @tanstack/react-virtual | **3.14.13** | The only virtualizer: Live feed and decisions table. |
| zustand | **5.0.15** | Stream buffer, UI and settings. No `persist` for audit data. |
| @base-ui/react | **1.8.0** | Accessible primitives (Dialog, Tabs, Menu, Slider, Popover, Toggle). This is the shadcn default since July 2026. |
| shadcn CLI (run with `npx shadcn@4.21.0`, not installed) | 4.21.0 | Generates `components/ui/*` over Base UI. Pulls in class-variance-authority **0.7.1**, clsx **2.1.1**, tailwind-merge **3.7.0**, tw-animate-css **1.4.0**. |
| tailwindcss, @tailwindcss/vite | **4.3.3** | `@theme inline` tokens, `@custom-variant dark` on `[data-theme]`. |
| recharts (+ react-is 19.3.0) | **3.10.1** | Standard charts: timeline, bar lists, Pareto scatter. The accessibility layer is on by default. |
| @visx/scale, shape, axis, group, heatmap, responsive, tooltip | **4.0.0** | Signature charts: pipeline waterfall, reliability diagram, confusion matrix, funnel. |
| motion (`motion/react`, `LazyMotion` + `domAnimation`) | **13.4.4** | App only (drawers, pill). The landing page uses CSS only, to stay within the JS budget. |
| lucide-react | **1.48.0** | Icons. Removes all emoji. |
| sonner | **2.0.8** | Toasts and `toast.promise`. |
| cmdk | **1.1.1** | ⌘K palette only, isolated in one file. It was last released 2025-03, so it is replaceable. |
| react-resizable-panels | **4.14.1** | Three-pane audit detail, through the shadcn `resizable` wrapper. |
| diff | **9.0.0** | `diffWords` between a claim and its evidence. |
| @fontsource-variable/inter, @fontsource-variable/jetbrains-mono | **5.3.0** | Self-hosted. Mono is used for IDs and JSON only. |

**Dev dependencies**

| Package | Version | Note |
|---|---|---|
| vite | **8.3.1** | Rolldown. Needs Node ≥ 22.12; keep `node:22-alpine` / CI Node 22 [A: ≥ 22.12]. |
| @vitejs/plugin-react | **6.1.1** | Peer `vite ^8`. |
| typescript | **~6.0.3** | **Not 7.0.2.** typescript-eslint 8.70.1 peers `typescript >=4.8.4 <6.1.0`. |
| eslint, @eslint/js | **9.39.5** | **Not 10.** eslint-plugin-jsx-a11y 6.10.2 peers eslint ≤ ^9. |
| typescript-eslint | **8.70.1** | |
| eslint-plugin-react-hooks | **7.1.1** | |
| eslint-plugin-react-refresh | **0.5.7** | |
| eslint-plugin-jsx-a11y | **6.10.2** | |
| @tanstack/eslint-plugin-query | **5.104.0** | |
| @tanstack/eslint-plugin-router | **1.162.0** | |
| vitest, @vitest/coverage-v8 | **5.0.2** | Ends the Vite 5 build / Vite 8 test split. |
| jsdom | **30.1.1** | |
| @testing-library/react | **16.3.3** | |
| @testing-library/user-event | **14.6.7** | |
| @testing-library/jest-dom | **7.0.1** | |
| msw | **2.15.0** | |
| @playwright/test | **1.63.0** | |
| @axe-core/playwright | **4.13.0** | |
| @lhci/cli | **0.15.1** | Landing page budget. |
| React Compiler: babel-plugin-react-compiler, @rolldown/plugin-babel | **1.0.0**, **0.2.4** | Optional peers of plugin-react 6. Separate task F1b: enable only if the build and tests stay green [A: wiring per the plugin-react 6 README]. |

**Not used, and why:**
- **@tremor/react:** peers React 18 only; last published 2025-01.
- **TanStack DB:** still 0.x.
- **ECharts:** no canvas-scale series here.
- **react-virtuoso:** one virtualizer is enough; fall back to it only if prepend jank shows up in the perf test.
- **Radix:** Base UI is the default now.
- **react-router, axios, date-fns:** replaced by `Intl.RelativeTimeFormat` / `Intl.DateTimeFormat`.
- **@number-flow/react:** pre-1.0.
- **shiki:** heavy for one JSON tab; a plain `<pre>` is enough.
- **@xyflow:** the waterfall covers the pipeline.
- **Storybook:** replaced by a states-gallery route plus Playwright snapshots.
- **TypeScript 7 and ESLint 10:** peer conflicts above.
- **Any Jev SDK in the browser:** keys never reach the client.

---

## 2. Information architecture and routes

File routes under `src/routes/`. Search params are validated with zod. `w` (time window) is defined on `/app` and inherited by child routes.

| Route | File | Search params |
|---|---|---|
| `/` | `index.tsx` | Landing (section 7) |
| `/app` | `app.tsx` (AppShell layout) | `w: '15m'\|'1h'\|'24h'\|'7d'` = `24h` |
| `/app/` | `app/index.tsx` | redirect → `/app/overview` |
| `/app/overview` | `app/overview.tsx` | — |
| `/app/live` | `app/live.tsx` | `peek?: string`, `gate?: GateAction[]`, `v?: Verdict[]`, `route?: string` |
| `/app/audits` | `app/audits/index.tsx` | `q, v[], gate[], route, model, grade, flagged, sort='-created_at', cursor, view` |
| `/app/audits/$auditId` | `app/audits/$auditId.tsx` | `claim?: number`, `show?: Verdict[]`, `tab: 'text'\|'json'` |
| `/app/gateway` | `app/gateway.tsx` (tabs) → `index` (decisions), `policies`, `calibration` | decisions: `action[], route, disagree, cursor`; policies: `route?: string`; calibration: `snapshot?` |
| `/app/knowledge` | `app/knowledge.tsx` | `tab: 'claims'\|'conflicts'`, `status, q, cursor, doc` |
| `/app/evals` | `app/evals.tsx` | `run?, baseline?, tab: 'bench'\|'calibration'\|'confusion'\|'cascade'\|'pareto'` |
| `/app/playground` | `app/playground.tsx` | `route?`, `mode: 'chat'\|'audit'` (the form stays in component state, not the URL) |
| `/app/settings` | `app/settings.tsx` | `section: 'connection'\|'jev'\|'judge'\|'appearance'\|'shortcuts'\|'about'` |
| `/app/_gallery` | dev and test builds only (`VITE_GALLERY=1`) | Every component in every state, for visual snapshots |
| `*` | `notFoundComponent` in `__root.tsx` | |

**Shell:**
- The left sidebar is resizable and collapsible, a notch dimmer than the content area. It has 8 items with lucide icons, and a pinned "Settings" item at the bottom.
- The top bar holds: the page title, the time-window picker (`w`), `ConnectionIndicator` (the single place connection state is shown), `DemoBadge` (demo only), the theme toggle and ⌘K.
- Degraded-mode banners sit under the top bar, driven by `SystemStatus`:
  - fallback judge active
  - Qdrant unreachable
  - persistence off
  - Jev circuit breaker open
  - Jev off
- Below 1280px the sidebar collapses to an icon rail. Below 768px it becomes a sheet, opened from the top bar.

---

## 3. Configuration

Precedence: **browser Settings override → `/config.json` (runtime) → `VITE_*` (build time) → default.**

| Key | Where | Default | Meaning |
|---|---|---|---|
| `VITE_DEMO` | build | `0` | `1` selects `ReplayDataSource`, disables writes, shows the demo badge |
| `VITE_BASE_PATH` | build | `/` | `/TrustAgent/` for Pages; feeds Vite `base` and router `basepath` |
| `VITE_DEMO_DATASET` | build | `auto` | `recorded` \| `mock` \| `auto` (recorded if its manifest exists, otherwise mock) |
| `VITE_REPLAY_SPEED` | build | `20` | Time-compression factor |
| `VITE_GALLERY` | build | `0` | Enables `/app/_gallery` |
| `apiBase` | `/config.json` ← env `TRUSTAGENT_API_BASE` | `""` (same origin) | Proxy base URL |
| `wsUrl` | `/config.json` ← env `TRUSTAGENT_WS_URL` | derived: `ws(s)://<apiBase or host>/ws` | |
| `repoUrl`, `docsUrl` | `/config.json` | GitHub URLs | |
| `auditTimeoutMs` | `/config.json` | `150000` | Pending audit becomes "still auditing" (gRPC timeout 120 s plus margin) |
| `trustagent.settings.v1` | localStorage | — | `{apiBase?, theme, density, reducedMotion, savedViews[]}` |
| dashboard key | sessionStorage by default; localStorage if "Remember on this device" is on | none | Never compiled into the bundle. `VITE_TRUTHTABLE_API_KEY` is removed. |

`src/config/runtime.ts` exports `loadRuntimeConfig(): Promise<RuntimeConfig>`, which fetches `${BASE_URL}config.json` with `cache:'no-store'` and falls back to `{}` on error. `main.tsx` awaits it before creating the DataSource.

---

## 4. Data layer

### 4.1 Layout

```
src/data/
  source.ts            DataSource interface + Capabilities
  http.ts              request<T>(path, {signal, method, body, timeoutMs}) → T | throws ApiError
  errors.ts            ApiError {status, code, message, retryAfterMs?}  (parses {"error": "..."} and {"error":{code,message}})
  normalize.ts         legacy → v2 (WS AuditResult, legacy AuditEvent, golden_v1 eval, "[role]: …" prompts)
  aggregate.ts         pure: AuditDetail[] → OverviewStats (replay + session fallback)
  queries.ts           query keys + queryOptions factories
  DataSourceProvider.tsx
  live/liveDataSource.ts  live/streamClient.ts  live/backoff.ts
  replay/replayDataSource.ts  replay/scheduler.ts  replay/manifest.ts
  stream/ringBuffer.ts  stream/streamStore.ts  stream/flush.ts
```

### 4.2 Types (`src/types/api.ts`, the single source; replaces `types/audit.ts` and the duplicated History types)

```ts
export type Verdict = 'CONTRADICTED'|'UNSUPPORTED'|'PARTIALLY_SUPPORTED'|'SUPPORTED'|'UNKNOWN'; // severity order
export type GateAction = 'pass'|'flag'|'block'|'fallback'|'not_assessed'|'pending'|'skipped'|'error';
export type PolicyMode = 'observe'|'flag'|'block'|'fallback';
export type JevBackend = 'openrouter_decisions'|'openrouter_systemone'|'typesafe'|'compatible'|'mock'|'off';
export type DecidedBy = 'jev'|'llm'|'jev>llm';

export interface JevAnswerView { choice?: string; noul?: number; probabilities: Record<string, number>; confidence?: number }
export interface GateDecision {
  request_id: string; route: string; mode: PolicyMode; action: GateAction; decided_at: string;
  risk: number|null; contradiction_risk: number|null; category?: string; category_confidence?: number;
  category_probabilities?: Record<string, number>; faithfulness_levels?: Record<string, number>;
  disagreement: boolean; reason?: string; backend: JevBackend; model_snapshot?: string; question_set?: string;
  latency_ms: number; tiers?: { tier0_ms?: number; jev_ms?: number }; cost_usd?: number;
  thresholds?: { flag_risk?: number; flag_contradiction?: number; block_contradiction?: number; block_min_confidence?: number };
  context_source?: string[];
}
export interface ClaimView {
  index: number; claim: string; status: Verdict; confidence: number; evidence: string[];
  span?: { start: number; end: number } | null;      // code points
  decided_by?: DecidedBy; escalation_reason?: string; jev?: JevAnswerView; explanation?: string;
}
export interface AuditSummary {
  audit_id: string; request_id: string; created_at: string; route?: string; upstream_model?: string;
  score: number; grade: 'A'|'B'|'C'|'D'|'F'; hallucination_detected: boolean;
  verdict_counts: Record<Verdict, number>; gate_action?: GateAction; processing_ms: number;
  judge?: { provider: string; model: string; fallback_active: boolean }; query_preview: string;
}
export interface AuditDetail extends AuditSummary {
  messages: { role: 'system'|'user'|'assistant'|'tool'; content: string }[];
  llm_response: string; claims: ClaimView[]; reasoning_trace: string;
  step_timings: Record<string, number>;            // decompose_ms, retrieve_ms, verify_ms, jev_verify_ms, llm_escalate_ms, score_ms, queue_wait_ms, upstream_ms
  gate?: GateDecision; cascade?: { claims_total: number; resolved_by_jev: number; escalated: number; escalation_reasons: Record<string, number>; jev_cost_usd?: number; llm_cost_usd?: number };
  recorded_at?: string;                            // replay only
}
export interface SystemStatus {
  version: string;
  proxy: { test_mode: boolean; auth_required: boolean; limits: { max_text_chars: number; max_upload_bytes: number } };
  engine: { reachable: boolean; latency_ms?: number; error?: string };
  judge: { provider: string; model: string; fallback_active: boolean; fallback_reason?: string };
  verifier_mode: 'llm'|'jev'|'cascade';
  qdrant: { reachable: boolean }; database: { configured: boolean; reachable: boolean };
  jev: { backend: JevBackend; endpoint_host?: string; model_requested?: string; model_snapshot?: string; key_present: boolean;
         breaker: 'closed'|'open'|'half_open'; last_error?: { kind: string; at: string; message: string };
         p50_ms?: number; p95_ms?: number; calls_24h?: number; cost_24h_usd?: number; calibrated_snapshots: string[] };
  gate: { enabled: boolean; default_mode: PolicyMode; policy_version: string };
}
```

The normalizer maps `faithfulness_score` and `overall_score` onto one `score` (UI #13). It also maps UNSPECIFIED/`unknown` to `UNKNOWN`.

### 4.3 Stream events and the WS v2 envelope

```ts
type Env<T extends string, D> = { v: 2; type: T; seq: number; ts: string; data: D };
export type WsMessage =
  | Env<'hello', { server_version: string; seq_head: number; caps: string[] }>
  | Env<'gate_decision', GateDecision>
  | Env<'audit_queued', { request_id: string; lane: 'high'|'low'; queue_depth: number }>
  | Env<'audit_result', AuditSummary & { detail?: AuditDetail }>
  | Env<'audit_error', { request_id: string; kind: 'engine_unavailable'|'timeout'|'judge_error'|'dropped'|'internal'; message: string }>
  | Env<'gap', { dropped: number }>
  | Env<'status', Partial<SystemStatus>>
  | { v: 2; type: 'hb'; ts: string };
```

Legacy frames with no `v` are normalized:
- `audit_result` with `data: AuditResult` becomes the v2 shape.
- `audit_error` with top-level `request_id` and `error` becomes `{kind:'internal'}`.
- `connected` is ignored.

### 4.4 DataSource

```ts
export interface Capabilities { write: boolean; playground: 'live'|'recorded'; policiesEdit: boolean; upload: boolean }
export interface DataSource {
  readonly kind: 'live'|'replay'; readonly caps: Capabilities;
  listAudits(q: AuditQuery, signal?: AbortSignal): Promise<Page<AuditSummary>>;
  getAudit(id: string, signal?: AbortSignal): Promise<AuditDetail>;
  listGateDecisions(q: DecisionQuery, signal?: AbortSignal): Promise<Page<GateDecision>>;
  getOverview(q: { window: TimeWindow }, signal?: AbortSignal): Promise<OverviewStats>;
  getSystemStatus(signal?: AbortSignal): Promise<SystemStatus>;
  getPolicies(): Promise<{ doc: PolicyDoc; etag: string }>;
  putPolicies(doc: PolicyDoc, etag: string): Promise<{ etag: string; version: string }>;
  dryRunPolicies(doc: PolicyDoc, window: TimeWindow): Promise<DryRunResult>;
  policyHistory(): Promise<PolicyVersion[]>;
  getCalibration(): Promise<CalibrationSet[]>; putCalibration(c: CalibrationFile): Promise<void>;
  testJev(): Promise<JevTestResult>;
  listKbClaims(q: KbQuery, signal?: AbortSignal): Promise<Page<KbClaim>>; getKbStats(): Promise<KbStats>;
  listConflicts(q: Paging): Promise<Page<ConflictPair>>;
  uploadDocuments(file: File, onProgress: (f: number) => void, signal?: AbortSignal): Promise<UploadResult>; // XHR for progress
  submitAudit(i: { query: string; response: string; model?: string }, requestId: string): Promise<void>;
  chat(req: PlaygroundRequest, signal: AbortSignal): AsyncIterable<PlaygroundChunk>; // non-stream yields one chunk
  subscribe(fn: (batch: StreamEvent[]) => void): () => void;
  connection: ExternalStore<ConnectionState>;
  resync(sinceIso: string): Promise<void>;
}
type ConnectionState = { phase: 'connecting'|'open'|'reconnecting'|'offline'|'unauthorized'|'replay';
                         attempt: number; nextRetryAt?: number; lastEventAt?: number };
```

Evals do not go through the DataSource. They are committed build-time artifacts (section 6.8), which works identically in live and demo builds.

### 4.5 StreamClient (`live/streamClient.ts`, a module-scope singleton that StrictMode cannot duplicate)

Behaviour:
- **Connect.**
  - Guard on `ws && ws.readyState <= OPEN` (covers CONNECTING; UI #8).
  - `new WebSocket(url, ['trustagent.v2', 'bearer.'+b64url(key)])` when a key is set. If the handshake fails twice in a row with a key present, fall back to `?api_key=` (C9 not yet deployed).
- **Receive.** `onmessage` does `event.data.split('\n')`, then JSON.parse each part (tolerates legacy batching; UI #1), then a type guard, then normalize, then `ring.push`. Nothing touches React here (UI #2).
- **Close and retry.** On close, if not disposed: `delay = random(0, min(30000, 500·2^attempt))`, floor 250 ms (full jitter), state `reconnecting`. It **never gives up** (UI #7).
- **Retry shortcuts.** The `online` event and a `visibilitychange` to visible both retry immediately. "Retry now" in the indicator does the same.
- **Unauthorized.** If the last REST call returned 401, the state becomes `unauthorized` and retrying stops until the key changes (`settingsStore` subscription).
- **Staleness.** No message (including `hb`) for 45 s forces a reconnect. This only arms after the server has sent at least one `hb` (caps negotiation via `hello`).
- **Dispose.** Null every handler, then `close(1000)`, then clear timers (UI #8).
- **Resync on reopen.** If `hello.seq_head > lastSeq + 1`, or on any `gap`, or if `hello` is missing (legacy), resync:
  - `resync(lastEventAt - 5000)` calls `listAudits({since, limit: 200})` and `listGateDecisions({since})`;
  - results are merged into the stream store and deduplicated by `request_id`;
  - if the page is full, the result is marked "more events during outage · open Audits".

**Flush** (`stream/flush.ts`):
- `RingBuffer<StreamEvent>` has capacity 5000 and an overflow counter.
- When the tab is visible, a rAF loop commits at most once every 100 ms. When hidden, a 1000 ms interval takes over (rAF pauses in background tabs).
- Each flush drains the ring into **one** `streamStore.ingest(batch)` call, then does the Query side effects:
  - `setQueryData(['audit', id], detail)` when the event carries a detail;
  - a throttled (10 s) invalidation of `['overview']`, `['audits']` and `['gate', 'decisions']`.

**`streamStore`** (Zustand, **not persisted**; on boot, delete the legacy `trustagent-audit-storage` key; UI #10):

```ts
interface LiveItem { request_id: string; phase: 'queued'|'gated'|'auditing'|'done'|'failed'|'timeout';
  gate?: GateDecision; audit?: AuditSummary; error?: { kind: string; message: string };
  optimistic?: boolean; firstSeenAt: number; updatedAt: number }
interface StreamState {
  items: Map<string, LiveItem>; order: string[];   // newest first, cap 2000 (evict oldest non-optimistic)
  visibleOrder: string[]; paused: boolean; pendingCount: number; droppedWhilePaused: number;
  ingest(b: StreamEvent[]): void; addOptimistic(id: string, meta: Partial<LiveItem>): void;
  pause(reason: 'hover'|'scroll'|'user'): void; resume(): void;
}
```

Merge rules, keyed by `request_id`, independent of arrival order:
- `gate_decision`: set `gate`; phase becomes `gated` unless it is already `done` or `failed`.
- `audit_queued`: phase `auditing`.
- `audit_result`: set `audit`; phase `done`.
- `audit_error`: phase `failed`.

Pause and timeout:
- While paused, `visibleOrder` is frozen and new ids increment `pendingCount` (cap 2000; extra ids increment `droppedWhilePaused`).
- `resume()` recomputes `visibleOrder`.
- A 5 s sweep marks optimistic or `auditing` items older than `auditTimeoutMs` as `timeout`. A late result still reconciles (phase becomes `done`).

### 4.6 Fallbacks (the UI never fabricates numbers)

| Missing | Behaviour |
|---|---|
| C5 overview (503 / no DB) | Overview computes `aggregate(streamStore)` and labels it **"This browser session · since 14:02 · persistence off"**. |
| C4 audits list (503) | EmptyState "Audit history needs Postgres (`TRUTHTABLE_DATABASE_URL`)", plus session items from the stream. |
| C3 status | Falls back to `/health`. Engine shows **"unknown (status endpoint unavailable)"**, never "running". |
| C7 spans | Fuzzy matching (6.4). |
| C6 gate endpoints (404) | The Gateway page shows "Gate not enabled on this proxy" with a link to the Settings → Jev guide. |

### 4.7 ReplayDataSource

Files live under `public/demo/<dataset>/`:
- `manifest.json`: `{schema:'trustagent.demo/v1', kind:'recorded'|'mock', recorded_at, git_sha, judge:{provider,model,fallback_active}, jev:{backend,model_snapshot}, count, audits:[AuditSummary], decisions:[GateDecision], timeline:[{request_id, gate_at_ms, audit_at_ms}]}`
- `audits/<id>.json`: the `AuditDetail`, fetched on demand.
- `hero.json`: about 30 events for the landing page.

The scheduler (`replay/scheduler.ts`, a pure function plus a timer driver):
- Emission time: `t'_i = clamp((t_i − t_{i−1})/speed, 150ms, 4000ms)`.
- `gate_decision` is emitted at `gate_at`, and `audit_result` at `gate_at + (audit_at − gate_at)/speed`, so the asynchronous resolution stays visible.
- After the last event, pause 3 s, then loop. Loop *n* gives ids the suffix `~n`, and `created_at` is rebased so the newest event is "now".
- `AuditDetail.recorded_at` keeps the original timestamp, shown in the detail view as "Recorded 2026-10-03 14:22 UTC".

Replay capabilities are `{write:false, playground:'recorded'}`. List and overview queries answer from the full recorded set, via `aggregate()` over the recorded audits, labelled "Recorded set · N audits". `connection.phase = 'replay'`.

---

## 5. Visual system (the rules; DESIGN.md in section 12)

**Principle:** the chrome is achromatic, and **hue always means a verdict**.
- Gate actions are achromatic chips with icons: pass = `circle-check` outline, flag = `flag`, block = inverted solid fg chip with `shield-x`, fallback = `corner-down-right`, not_assessed = `minus`.
- The focus ring is fg-colored, 2px, offset 2px.
- Primary buttons use inverted fg.

**Tokens** (`src/styles/tokens.css`; computed OKLCH values from the design research):

```css
@import "tailwindcss";
@import "tw-animate-css";
@custom-variant dark (&:where([data-theme=dark], [data-theme=dark] *));
:root{ --bg:oklch(.985 .002 265); --surface:oklch(1 0 0); --fg:oklch(.22 .01 265); --fg-muted:oklch(.47 .012 265);
  --border:oklch(.91 .004 265); --v-supported:oklch(.50 .10 165); --v-contradicted:oklch(.44 .16 25);
  --v-unsupported:oklch(.54 .115 68); --v-partial:oklch(.47 .12 252); --v-unknown:oklch(.50 .012 265); --v-tint:8%; }
[data-theme=dark]{ color-scheme:dark; --bg:oklch(.155 .006 265); --surface:oklch(.19 .006 265); --fg:oklch(.93 .004 265);
  --fg-muted:oklch(.70 .01 265); --border:oklch(.27 .006 265); --v-supported:oklch(.82 .13 165);
  --v-contradicted:oklch(.68 .17 27); --v-unsupported:oklch(.83 .145 80); --v-partial:oklch(.72 .12 248);
  --v-unknown:oklch(.70 .01 265); --v-tint:16%; }
@theme inline{ --color-*: initial; --color-bg:var(--bg); --color-surface:var(--surface); --color-fg:var(--fg);
  --color-fg-muted:var(--fg-muted); --color-border:var(--border); --color-v-supported:var(--v-supported); /* …all 5 */
  --font-sans:"Inter Variable",ui-sans-serif,system-ui,sans-serif; --font-mono:"JetBrains Mono Variable",ui-monospace,monospace; }
mark[data-v]{ color:inherit; background:color-mix(in oklab,var(--vc) var(--v-tint),transparent);
  text-decoration-line:underline; text-decoration-color:var(--vc); text-underline-offset:3px; }
mark[data-v=contradicted]{--vc:var(--v-contradicted); text-decoration-style:wavy}
mark[data-v=unsupported]{--vc:var(--v-unsupported); text-decoration-style:dotted}
mark[data-v=partial]{--vc:var(--v-partial); text-decoration-style:dashed}
mark[data-v=supported]{--vc:var(--v-supported); text-decoration-style:solid; text-decoration-thickness:1px}
mark[data-fuzzy]{outline:1px dashed var(--vc); outline-offset:1px}
```

**Verdict encoding** (`domain/verdicts.ts`, `satisfies Record<Verdict, VerdictToken>` so a missing verdict fails type-checking):

| Verdict | Label | Icon | Underline | Key |
|---|---|---|---|---|
| CONTRADICTED | Contradicted | x-octagon | wavy | 1 |
| UNSUPPORTED | Unsupported | circle-help | dotted | 2 |
| PARTIALLY_SUPPORTED | Partial | circle-half | dashed | 3 |
| SUPPORTED | Supported | check | thin solid | 4 |
| UNKNOWN | Unknown (judge error) | circle-dashed | none | 5 |

**Type:**
- Sizes: 11/16, 12/16, **13/20 base**, 14/20, **15/24 response text**, 16/24, 20/28, 28/34 (hero KPI only).
- Weights: 400, 500 and 600 only. `tabular-nums` on every number. Sentence case everywhere.
- Radius 6/8/12, 4px grid, 1px hairlines. Dark mode shows elevation through lightness, not shadows.

**Motion:**
- 150–200 ms ease-out, animating `transform` and `opacity` only.
- New Live rows get a CSS background flash that fades over 1 s.
- `prefers-reduced-motion`, or the Settings override, disables the flash, replay tweening and view transitions.

**Scores and grades** (UI #12):
- One source of truth: the server grade (A ≥ .9, B ≥ .7, C ≥ .5, D ≥ .3, F). The UI shows score and grade in neutral fg; there are no extra color bands.
- "Flagged" is shown separately, with the reason (for example "1 contradicted claim at confidence 0.82").
- The hallucination rate has no arbitrary red threshold.

**Charts:**
- Verdict series use verdict tokens. Everything else uses fg / fg-muted / dashed only.
- Every chart has a "View as table" toggle.

---

## 6. Page specs

### 6.1 Shared components

**`common/`**
- `EmptyState`, `ErrorState` (shows `ApiError.message` and status, plus Retry; UI #21)
- `LoadingDelay` (200 ms show-delay, 400 ms minimum)
- `RelativeTime` ("3m ago"; absolute time in the tooltip; the date once older than 24 h; UI #24)
- `CopyButton`, `Num`, `Kbd`

**`verdict/`**
- `VerdictBadge`, `VerdictIcon`
- `VerdictBarcode`: an inline SVG of one audit's claims sorted by severity, 1 px gaps, `aria-label` "3 supported, 1 contradicted"

**`gate/`**
- `GateChip`: action, risk, latency
- `ProbabilityBar`: stacked segments with a tick at each threshold, plus an sr-only list
- `ThresholdTrack`
- `DecidedByBadge`: `Jev` / `LLM` / `Jev → LLM · numeric`
- `JevStatusCard`

### 6.2 Overview

1. **Summary strip** (one row of quiet text, not cards):
   - audits in window
   - flagged rate
   - gate actions (pass / flag / block counts)
   - gate p50/p95
   - judge `claude-haiku-4-5`, with a "fallback: llama3.2:1b — low accuracy" badge when `fallback_active`
2. **Hero chart:** Recharts ComposedChart. Claims per bucket as bars stacked by verdict, and mean score as a line on a second axis.
3. **Hallucination rate by route and by model:** bar lists with n shown.
4. **Top contradicted claims:** grouped by normalized text; each links to its latest audit.
5. **Per-step latency p50/p95:** tier0, jev_gate, queue_wait, decompose, retrieve, jev_verify, llm_escalate, score.
6. **Cascade share:** the share of claims resolved by Jev vs escalated, by reason. Hidden when `verifier_mode=llm`, with a note.

First-run empty state: "No traffic yet", with a copyable curl through the proxy and a Playground link.

### 6.3 Live

- **Virtualized feed** (TanStack Virtual, stable keys). Each row is an `<a>`:
  - time
  - route
  - upstream model
  - `GateChip` (appears at `gate_decision`)
  - phase: "auditing…" with a 4-step skeleton, `failed · engine unavailable`, or `timeout · still auditing`
  - `VerdictBarcode`, score/grade, latency
- **Pausing:** on pointer hover over the list, on scroll away from the top, or with Space.
- **"N new · resume" pill:** a motion spring. When overflowed it reads "N new · M not shown · open Audits".
- **Peek:** `?peek=` opens a Base UI Dialog side sheet with the summary and gate probabilities, plus "Open full audit".
- **Keys:** j/k to move, Enter to peek, o to open, Space to pause.
- **Filters:** gate action, verdict present, route (all in the URL).

### 6.4 Audits list

- A metric strip above the table (flagged rate over `w`).
- A TanStack Table v9 with server cursor paging and sorting. Columns: time, route, upstream model, gate, score/grade, barcode, claims, decided-by mix (Jev %), judge, latency.
- Saved views: named search-param sets in `uiStore`.
- Rows are links. Shift-click selects a range for "Compare" (v2 of this page; out of scope now).

### 6.5 Audit detail (`/app/audits/$auditId`)

**Header strip:**
- score + grade, and the flagged reason
- verdict counts (each clickable, which sets `show`)
- gate chip, "Jev said *flag 0.87* · deep audit found *1 contradicted*"
- judge model and fallback badge
- decided-by mix
- wall time
- cost (if present)
- request_id (copy), route, upstream model, absolute timestamp, "Recorded …" in replay

**Three panes** (react-resizable-panels, layout auto-saved). Below 1024px they become Base UI Tabs.

**Left pane: `PipelineWaterfall`** (visx), with two lanes and independent labelled scales:
- The **In-request** lane: `upstream_ms`, `tier0_ms`, `jev_ms`.
- The **Async audit** lane: `queue_wait_ms`, decompose, retrieve, then verify split into parallel `jev_verify` and `llm_escalate` bars, then score.
- Bars are laid out sequentially from durations. The caption "durations only; order from pipeline" appears unless `step_starts` is present.
- A footer shows **wall time = processing_ms** and **Σ steps**, with the difference labelled "overhead" (UI #15).

**Center pane:**
- `PromptMessages`: role-labelled and collapsible; `parseLegacyPrompt` splits `^\[(system|user|assistant|tool)\]: ` (UI #23).
- `ResponseWithClaims`: 15/24 text with a "problems only" toggle and a legend.

**Right pane: `EvidenceRail`,** one `ClaimCard` per claim:
- verdict icon and label; claim text; P(verdict)
- `DecidedByBadge`, with the escalation reason in a tooltip
- the Jev `ProbabilityBar` over {supported, partial, contradicted, not_addressed}, with the accept-threshold tick
- evidence passages, with matched content words emphasized via `CSS.highlights` (fallback: `<mark data-term>`)
- for CONTRADICTED claims, a `ClaimDiff`: `diffWords(claim, bestEvidenceSentence)`, with insertions and deletions styled by underline and strikethrough (not color alone), numbers bolded
- the LLM `explanation` if present
- the label "retrieved, not quoted" when Jev decided the claim

**Keyboard:**
- j/k: next/previous visible claim (focuses the card, scrolls the mark into view, sets `?claim=`)
- 1–5: toggle verdict filters; 0 clears them
- Enter on a mark focuses its card
- g: gate panel
- ?: shortcuts sheet

**Claim segmentation** (`domain/segmentClaims.ts`, pure and unit-tested):

```ts
type Anchor = { claimIndex: number; start: number; end: number; kind: 'span'|'exact'|'fuzzy' };
export function anchorClaims(text: string, claims: ClaimView[]): { anchors: Anchor[]; unanchored: number[] };
export function segment(text: string, anchors: Anchor[]): { start: number; end: number; claims: number[] }[];
```

1. **Span.** Convert code points to UTF-16 (`cpToUtf16` table built once). Accept the span if `0 ≤ s < e ≤ len` and `normalize(slice)` contains ≥ 60% of the claim's content tokens.
2. **Exact.** Search `normalize(claim)` inside `normalize(text)`. Normalization lowercases, collapses whitespace and maps smart quotes, and keeps an index map back to the original text.
3. **Fuzzy.** Split into sentences with `Intl.Segmenter('en', {granularity:'sentence'})`. Score each sentence, and each window of two adjacent sentences, by containment: |claim content tokens ∩ sentence tokens| / |claim tokens|. Stopwords are removed; numbers and capitalized tokens get weight 2. Take the best score if it is ≥ 0.6 and beats the runner-up by ≥ 0.1.
4. **Unanchored.** Otherwise the rail shows "paraphrased — not located in text".

**Segmentation** uses a boundary sweep over all start and end points. It produces non-overlapping segments, each with its set of active claims. A `<mark>` takes the most severe verdict, sets `data-claims`, is `tabIndex=0` with `aria-describedby` pointing at its card, and shows a count dot when more than one claim covers it. Complexity is O(n log n) and works up to the 20k-character cap.

### 6.6 Gateway

- **Decisions tab:**
  - A virtualized server-paged table: time, route, mode, action, risk, contradiction risk, category and confidence, disagreement, latency, snapshot, deep-audit outcome (joined).
  - A risk histogram with each route's thresholds.
  - Action over time.
  - A `JevStatusCard`: backend, endpoint host, requested model vs returned snapshot (a mismatch warns "thresholds calibrated for …"), breaker, p50/p95, error kinds, cost over 24h.
- **Policies tab:**
  - A route list plus an editor for:
    - match (header `X-TrustAgent-Route`, path, key label)
    - **mode** as a segmented control, with a plain-language line each:
      - observe: "never changes responses; adds no latency"
      - flag: "adds `X-TrustAgent-*` headers; waits ≤ deadline"
      - block: "replaces risky responses; streaming requires buffering"
      - fallback: "retries with a fallback model/message"
    - a Base UI Slider plus a 0.01-step number input for each threshold
    - deadline, hedge, fail open/closed, `on_block`, context sources, sample rate, stream policy
  - zod `policySchema.ts` mirrors the Go schema [A: final field names come from the gateway spec].
  - **Dry-run** calls `POST /dry-run {doc, window}`. The server re-applies the proposed thresholds to stored decision probabilities, with no Jev calls. The UI shows:
    - pass/flag/block counts and the delta vs current;
    - "k of the would-block responses had a CONTRADICTED deep audit; m were overturned";
    - a sample table.
  - **Save** shows a JSON diff, then a confirm dialog, then `PUT` with If-Match. A 409 shows "Policy changed by someone else — reload & reapply". History has a revert action.
  - There is no optimistic update here, because policy changes are consequential.
- **Calibration tab:**
  - Import a `trustagent.calibration/v1` file:
    ```ts
    { schema; snapshot: string; question_set: string; question: string; method: 'isotonic'|'platt';
      table?: number[/*101*/]; platt?: {a: number; b: number};
      fitted_on: { dataset: string; n: number; labels_source: 'human'|'public'|'llm_proxy' };
      metrics: { ece_before; ece_after; brier_before; brier_after };
      suggested_thresholds: Record<string, { value: number; precision: number; recall: number }> }
    ```
  - Preview a reliability diagram before and after. `labels_source: 'llm_proxy'` is labelled "agreement with LLM judge, not accuracy".
  - A snapshot mismatch blocks activation until the user overrides it with a typed confirmation.
  - Activate with `PUT`, then "Apply suggested thresholds" opens the Policies tab prefilled (dry-run first).

### 6.7 Knowledge

- **Claims table:**
  - server paging with `total`; status filter (refetches claims only); search
  - claims line-clamped to 3 lines with an expand control; columns for source doc and ingested time
  - duplicate normalized text grouped as "×2" (UI #16, #33)
  - independent queries (no `Promise.all` blanking)
- **Conflicts tab:** pairs side by side, each linking to its claim row.
- **Upload dialog:**
  - a Base UI button and a label-wrapped input; drag-and-drop is an enhancement
  - case-insensitive `.json`; size limit from `status.proxy.limits`
  - pre-validates that `metadata` values are strings, with a specific message
  - XHR progress
  - the full result: ingested, accepted, quarantined, conflicts, and a per-claim table
  - invalidates `['kb']` afterwards (UI #19)
- A Qdrant-down banner. KB stats are shown as text, with conflicts in neutral color when zero.

### 6.8 Evals

Data comes from **committed files only**: `import.meta.glob('@evals/**/*.json')`. The alias `@evals` points to `../backend-python/evals/results` (the new eval-harness output directory) and `../backend-python/evals/baselines`.

`domain/evalSchema.ts` defines `trustagent.evalrun/v1`:

```ts
{ id; created_at; git_sha; mock: boolean; mode: 'llm'|'jev'|'cascade'|'gate';
  dataset: { name; n; split; labels_source };
  judge?: { provider; model }; jev?: { backend; model_snapshot; question_set };
  metrics: { precision; recall; f1; accuracy; balanced_accuracy; auroc; ece; brier? };
  confusion?: Record<Verdict, Record<Verdict, number>>;
  reliability?: { lo; hi; n; mean_pred; frac_pos }[];
  latency?: { p50_ms; p95_ms }; cost?: { usd_per_1k };
  cascade?: { escalation_rate; reasons: Record<string, number>; agreement?: number };
  sweep?: { threshold; precision; recall; f1; usd_per_1k }[] }
```

`golden_v1.json` is adapted by the normalizer and labelled "mock-provider replay (CI regression gate) — not a model measurement".

| Tab | Content |
|---|---|
| Benchmarks | Rows (mode × judge) by columns (dataset metrics, p50, $/1k). Missing cells read "not yet measured" and link to `make eval-*`. `?baseline=` pins a run and shows deltas. |
| Calibration | visx reliability diagram: bars of `frac_pos` per bin, a diagonal, an n histogram and ECE. |
| Confusion | visx heatmap, 5×5 gold vs predicted. Opacity ramp of fg (no hue), diagonal outlined, counts printed. |
| Cascade | Funnel: claims → resolved by Jev (per verdict) → escalated (per reason) → LLM agreed/overturned. |
| Pareto | Recharts scatter: x = $/1k on a log scale, y = balanced accuracy. Points for Jev (sweep), LLM and cascade (sweep), and the frontier. |

### 6.9 Playground (replaces QueryInput and FileUpload in the sidebar)

- **Left panel:**
  - mode (Chat through proxy | Audit a pasted response)
  - route (from policies)
  - upstream model (from the `/v1/models` passthrough, free text allowed)
  - system and user messages
  - optional context documents, sent as the `trustagent.context` extension
  - stream toggle
  - a character counter against `limits.max_text_chars`
- **Send:**
  1. `id = crypto.randomUUID()` and `streamStore.addOptimistic(id)`.
  2. `fetch('/v1/chat/completions', {headers: {'X-Request-ID': id, 'X-TrustAgent-Route': route, 'X-API-Key': key}})`.
  3. In stream mode, parse SSE, render tokens live, and parse the `: trustagent {...}` comment line.
- **Right panel: `ResultTimeline`:**
  1. upstream status and ms
  2. gate decision, from the response headers or the WS `gate_decision` (whichever arrives first)
  3. what the client actually received: the body, highlighted if block or fallback replaced it
  4. deep-audit progress through to the result, with "Open full audit"
  5. a curl equivalent (key masked)
- The form is **not cleared** (UI #20), so "Run again" works.
- Errors: 409 duplicate id, 503 `queue_full`, and 401 (links to Settings) each get a specific message.
- **In replay:** preset prompts replay their recorded exchange with the original timing. The free-text box is disabled and says "Run the stack locally to send your own prompts" (the notice links to the quickstart, so the control is never a dead button).

### 6.10 Settings

- **Connection:**
  - API base (blank = same origin)
  - dashboard key (password field, "Remember on this device", Clear)
  - **Test connection**: `/health` then `/api/system/status`, with per-step pass/fail and the server's message
- **Jev:**
  - `JevStatusCard` and **Run test decision** (`POST /api/gate/test`; shows latency, returned snapshot, answer and cost about $0.00001)
  - **`JevSetupGuide`**: a stepper whose content comes from `src/content/jevAccess.ts`:
    - "Which path?" OpenRouter Decisions (**recommended: only an OpenRouter key, no waitlist, no TypeSafe account**) / OpenRouter System One compat / TypeSafe direct (console.typesafe.ai key; `jev-latest` or `jev-1.13.0`) / compatible gateway (e.g. `https://ai-gateway.vercel.sh/typesafe`) / Mock / Off
    - per-path steps: create an account; add credits (otherwise HTTP 402); create a key at openrouter.ai/settings/keys; add the generated `.env` block (copy); `docker compose up -d proxy audit-engine`; press "Run test decision"
    - why keys are server-side only
    - a link to `docs/JEV-ACCESS.md`
  - A unit test asserts that every env var named in `jevAccess.ts` exists in `/.env.example`, so the guide cannot drift.
- **Judge:** provider and model, the fallback warning ("llama3.2:1b scores at chance level on LLM-AggreFact; verdicts are unreliable"), `verifier_mode`, Qdrant and DB status.
- **Appearance:** theme (system/light/dark), density, reduced motion.
- **Shortcuts:** the sheet.
- **About:** version, build sha, data source kind, demo manifest.

---

## 7. Landing page (`/`)

Components live in `components/landing/`. Every number comes from `landingFacts.ts` as `{value, source: 'measured'|'vendor'|'third_party', href, asOf}`, rendered with a source marker. Only `measured` numbers can appear unqualified.

1. **Nav:** SVG wordmark, How it works, Policies, Benchmarks, Docs, GitHub, and "Open live demo" pointing to `/app/live`.
2. **Hero:**
   - H1 "Catch hallucinations before your users do." A subline about the open-source gateway with a fast Jev check inline, per-route policy, and claim-level audits asynchronously.
   - CTAs: Live demo, Quickstart.
   - Right side: **`GatewayTape`**, a CSS-animated replay of `hero.json` (recorded real decisions). Each row shows: request, then the risk bar fills, then an action chip, then the verdict barcode resolving a few seconds later.
   - Caption: "Recorded traffic, replayed · <recorded_at> · Jev <snapshot> + <judge>", or "Synthetic (mock Jev)" when the dataset is mock.
   - With reduced motion it is a static list.
3. **How it works:** a responsive inline-SVG tier diagram.
   - Client → Go proxy **Tier-0** deterministic checks → upstream LLM → **Tier-1 Jev** (sync, deadline, fail-open) → policy → client.
   - An async branch goes to **Tier-2** LangGraph (decompose → retrieve → verify, Jev then LLM cascade → score) → dashboard.
   - Latency labels appear only as `vendor` or `measured` facts.
4. **Policy modes:** a comparison table (mode | what the client sees | added latency | streaming | when to use), plus a sample of response headers.
5. **Quickstart:** tabs for Python, TypeScript and curl. The OpenAI SDK `base_url="http://localhost:8080/v1"`, the `.env` block, and `docker compose up`, each with a copy button.
6. **Benchmarks:** the same eval glob as section 6.8. The table shows committed runs only; empty cells read "not yet measured".
   - A separate "External reports (not ours)" list, with links: Arize on RAGTruth (87% accuracy / AUC 0.94 at 0.80; 76% at 0.5), and OpenRouter's faithfulness test (95.5%, Brier 0.043).
7. **Honest limitations:**
   - Jev gives no rationales, is weak on numbers, dates and negation, and publishes no calibration data (local calibration is required).
   - The alpha endpoint has latency tails.
   - Blocking a stream requires buffering.
   - The local fallback judge is weak.
   - Without Qdrant, claims come back UNSUPPORTED.
   - English first.
8. **Call to action and footer:** GitHub, docs, the Jev access guide; license and version.

**Budgets** (enforced in CI by `scripts/check-bundle-size.mjs` reading `dist/.vite/manifest.json`, plus Lighthouse CI on the demo build):

| Target | Budget |
|---|---|
| Landing JS (initial) | ≤ 110 KB gzip. No Recharts, visx or motion. |
| Landing CSS | ≤ 25 KB gzip |
| Fonts | 1 preloaded woff2 (Inter latin) |
| Lighthouse (mobile) | LCP ≤ 2.0 s, CLS ≤ 0.02, TBT ≤ 150 ms, Performance ≥ 90, Accessibility 100 |
| App route chunks | ≤ 180 KB gzip each |
| Stream perf test | 200 msg/s for 60 s: ≤ 1 store commit per 100 ms, long tasks p95 < 50 ms, heap stable (±10%) |

**Accessibility:**
- WCAG 2.2 AA; targets ≥ 24px; visible focus; skip link.
- Charts carry table alternatives.
- The connection and Playground status regions use `aria-live=polite`; Live rows are not announced one by one.

---

## 8. Build and deploy

**`vite.config.ts`** (rewritten):
- Plugins in order: `tanstackRouter({target:'react', autoCodeSplitting:true})`, `react()`, `tailwindcss()`.
- `base: process.env.VITE_BASE_PATH ?? '/'`.
- `resolve.alias {'@': 'src', '@evals': '../backend-python/evals'}`, with `server.fs.allow` including it.
- `define: {__BUILD_SHA__, __BUILD_TIME__}`.
- **Dev proxy**: `/api`, `/v1`, `/health` → `http://localhost:8080` (no rewrite; fixes the wrong dev proxy); `/ws` → `ws://localhost:8080` with `ws:true`.
- Tests move to `vitest.config.ts` via `mergeConfig`: jsdom, `setupFiles: src/test/setup.ts`.
- `routeTree.gen.ts` is committed, because `tsc -b` runs before `vite build`.

**Docker** (`frontend-react/Dockerfile`, with the build context moved to the repo root):
- `ARG VITE_DEMO=0 VITE_BASE_PATH=/`.
- Copy `frontend-react/` and `backend-python/evals/{results,baselines}`, then run `npm ci && npm run build`.
- Serve stage: `nginx:1.31-alpine` with `USER nginx` and `listen 3000` [A: or `nginxinc/nginx-unprivileged`; resolves the "dashboard not non-root" gap].
- `nginx/templates/default.conf.template` (envsubst runs at start):
  - `location /api/`, `/v1/`, `/health` → `proxy_pass ${TRUSTAGENT_UPSTREAM:-http://proxy:8080}`
  - `location /ws` with upgrade headers
  - `location = /config.json` served from `/etc/nginx/runtime/config.json`, which an entrypoint script writes from `TRUSTAGENT_API_BASE` / `TRUSTAGENT_WS_URL`
  - SPA `try_files`, immutable caching for `/assets/*`
  - CSP: `default-src 'self'; connect-src 'self' ${TRUSTAGENT_CONNECT_SRC}; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'sha256-<theme-init>'; frame-ancestors 'none'`
  - drop `X-XSS-Protection`

**`docker-compose.yml`** (dashboard service):
- `build: {context: ., dockerfile: frontend-react/Dockerfile}`.
- Remove the `VITE_*` runtime environment variables.
- `depends_on: proxy: condition: service_healthy`.

Same-origin removes the dashboard's need for CORS and fixes UI #9.

**`index.html`:**
- SVG favicon.
- An inline theme-init script (sets `data-theme` before paint).
- A preload for Inter.

**GitHub Pages** (`.github/workflows/pages.yml`):
- Triggers: push to `main` on paths `frontend-react/**`, `backend-python/evals/results/**`, `backend-python/evals/baselines/**`; plus `workflow_dispatch`.
- `permissions: {contents: read, pages: write, id-token: write}`; concurrency group `pages`.
- **Build job:**
  1. checkout, setup-node 22 with npm cache, `npm ci`
  2. `VITE_DEMO=1 VITE_BASE_PATH=/TrustAgent/ npm run build`
  3. `cp dist/index.html dist/404.html` (SPA deep-link fallback) and `touch dist/.nojekyll`
  4. `npx playwright install --with-deps chromium && npm run e2e:demo` (the demo project against `vite preview`)
  5. `npm run size`, then `lhci autorun`
  6. `actions/upload-pages-artifact`
- **Deploy job:** `actions/deploy-pages`. Actions are pinned by SHA and maintained by Dependabot [A: current majors].
- **Owner step (manual):** Settings → Pages → Source: GitHub Actions.
- The site is `https://timkush1.github.io/TrustAgent/`. Deep links return status 404 with the correct content, which is acceptable for a demo; `/app/*` sets `noindex`.

**Fixture recording** (`frontend-react/scripts/record-demo.mjs`, plain Node 22 ESM, run as `npm run demo:record -- --api http://localhost:8080 --scenarios scripts/demo-scenarios.json --out public/demo/recorded`):
1. Read `status`. The run is labelled `kind:'recorded'` only if `jev.backend ∉ {mock, off}` and `!judge.fallback_active`; otherwise it refuses unless `--allow-mock` is passed, which writes `kind:'mock'`.
2. Open the WS, then for each scenario (about 40: grounded, unsupported, contradicted, numeric, refusal, off-topic, and multiple routes and modes):
   - POST `/v1/chat/completions` with `X-Request-ID` and the route header;
   - capture status, `X-TrustAgent-*` headers, body and client latency;
   - wait up to 180 s for `gate_decision` and `audit_result`/`audit_error`;
   - `GET /api/audits/:id`.
3. Redact: drop keys and hosts, rehash ids to stable `demo-<n>`. Write the per-audit files, `manifest.json` and `hero.json` (the 30 most varied decisions).
4. Validate every file against the zod schemas before writing.

**Curated fallback set:**
- `public/demo/mock/` is produced by the same script against `docker-compose.e2e.yml`: Jev mock backend, `LLM_PROVIDER=mock` with the golden fixtures, and test mode, which gives deterministic upstream text.
- It is committed and labelled **"Demo — synthetic data (mock Jev + mock judge), not measurements"**.
- The real recorded set replaces it once the owner has a key, and the badge text switches automatically from the manifest.

---

## 9. Tests

**Vitest units (`src/**/*.test.ts`):**
- `ringBuffer` (wrap, overflow count)
- `backoff` (seeded random; cap; floor)
- `streamClient` with a `FakeWebSocket`:
  - multi-line frames give N events
  - legacy envelopes are normalized
  - reconnect continues past 10 attempts
  - no reconnect after `dispose`
  - no second socket while CONNECTING
  - resync is called on a `hello` gap or `gap`
  - `unauthorized` stops retrying until the key changes
  - `online` / `visibilitychange` trigger an immediate retry
- `streamStore`:
  - gate then result, and result then gate, both end `done`
  - error gives `failed`
  - an optimistic item reconciles
  - timeout, then a late result reconciles
  - pause/resume counts and overflow
  - eviction keeps optimistic items
- `segmentClaims`: spans, astral-plane characters, overlaps, fuzzy threshold, paraphrase goes unanchored, 20k-character performance < 20 ms
- `verdicts` (exhaustive), `grades`, `normalize` (golden_v1, legacy WS, `parseLegacyPrompt`), `aggregate`, `scheduler` (compression, clamp, loop suffix, rebasing), `policySchema`, `calibrationSchema`, `evalSchema`, `errors`, `runtime` config precedence, `jevAccess` versus `.env.example`

**Component tests** (RTL + msw):
- AuditDetail: j/k/1–5/0, mark ↔ card focus, "paraphrased" label
- LivePage: pause pill, peek via the URL
- PolicyEditor: dry-run rendering, 409 flow, save confirmation
- Settings: key storage choice, Test connection errors
- UploadDialog: validation and error surfacing
- **Regression** for UI #6: every verdict renders its own label and icon in the audits table, detail and rail
- Overview: 503 leads to the session fallback label

**Playwright** (`playwright.config.ts`, two projects):
- **`demo`** (`vite preview` of the demo build; runs in the CI frontend job and on Pages):
  - landing sections, CTAs and "not yet measured" cells
  - the demo badge and manifest dialog
  - replay events arriving
  - write controls explained
  - deep link via 404.html
- **`stack`** (a new CI job `e2e-stack`, run nightly and on the `e2e` label):
  - brings up `docker compose -f docker-compose.yml -f docker-compose.e2e.yml up -d --wait` with Jev mock, mock judge, test mode, Postgres and Qdrant
  - specs:
    - playground → gate chip → audit resolves → detail marks
    - policies edit → dry-run → save → a new decision reflects the threshold
    - `ws-resilience` (`docker compose restart proxy` → reconnecting → open → an audit created during the outage appears via resync)
    - auth (key required → Settings → data loads)
    - KB upload
    - 200 msg/s perf using a synthetic replay

**axe:** `@axe-core/playwright` on every route in both themes, with tags `wcag2a, wcag2aa, wcag21aa, wcag22aa`. Zero serious or critical violations.

**Visual:**
- `toHaveScreenshot` on the demo build and `/app/_gallery`, with `page.clock.install` (frozen time), reduced motion, 1440×900 and 390×844, and both themes.
- Run inside `mcr.microsoft.com/playwright:v1.63.0-noble` so fonts are stable [A: tag name]. Updating snapshots is a manual workflow input.

**CI frontend job:** `npm ci`, lint, `tsc -b`, `vitest run --coverage` (thresholds: lines 80% on `src/data`, `src/domain`), build, `size`, then `e2e:demo`.

---

## 10. Where each dashboard bug is fixed (react-ui report numbering)

| # | Fix location |
|---|---|
| 1 | `live/streamClient.ts` splits on `\n` + C1 |
| 2 | `onmessage` → ring buffer → `flush.ts` (no React state per message) |
| 3 | `WsMessage` union; `streamStore` `failed`; LiveRow and Playground error states + C2 |
| 4 | C3 + `SystemStatus` UI; `/health` fallback shows "unknown" |
| 5 | Server overview (C5) or selector-derived session aggregate; `getStats` removed |
| 6 | `domain/verdicts.ts` (`satisfies Record<Verdict,…>`) used everywhere; HistoryView deleted; regression test |
| 7 | `backoff.ts` never gives up; online/visibility retry; "Retry now" |
| 8 | Singleton, handlers nulled in `dispose`, CONNECTING guard |
| 9 | Same-origin nginx proxy + `/config.json` + build ARGs for demo/base only |
| 10 | `persist` removed; legacy key deleted; server stats |
| 11 | `EmptyState`; gauge removed |
| 12 | Single grade source (server); flag reason shown separately |
| 13 | `normalize.ts` single `score` |
| 14 | `judge{}` + "Upstream model" label (C7); "proxy" provider no longer shown |
| 15 | Waterfall footer: wall time vs Σ steps vs overhead |
| 16 | Knowledge: paging with total, independent queries, pair links, source/time columns |
| 17 | `/app/audits/$auditId` + live invalidation |
| 18 | TanStack Query keys + AbortSignal |
| 19 | UploadDialog full summary, invalidation, metadata pre-validation |
| 20 | Playground: request_id link, form kept, limit from status |
| 21 | `errors.ts` + `ErrorState` |
| 22 | One `ConnectionIndicator`; the URL appears only in Settings |
| 23 | `messages[]` (C7) + `parseLegacyPrompt` |
| 24 | `RelativeTime` |
| 25 | New shell; stat cards removed |
| 26 | Base UI Dialog / Tabs; detail is a route |
| 27 | Rows are `<a>`; upload uses a button and a label |
| 28 | Tokens ≥ 4.5:1 on body text; axe gate |
| 29 | lucide + `favicon.svg` |
| 30 | Responsive shell, overflow wrappers, tabbed detail |
| 31 | Legacy files deleted (section 11); `send()`, `MetricUpdate` and stale comments gone |
| 32 | `types/api.ts` only |
| 33 | KB grouping and clamping. The data cleanup itself belongs to the backend. |

---

## 11. File plan

**New**
- `src/main.tsx`, `src/router.tsx`, `src/routeTree.gen.ts`, and everything under `src/routes/**` (section 2)
- `src/config/runtime.ts`, `src/data/**` (4.1), `src/types/api.ts`
- `src/domain/`: `verdicts.ts`, `grades.ts`, `segmentClaims.ts`, `fuzzyMatch.ts`, `wordDiff.ts`, `legacyPrompt.ts`, `policySchema.ts`, `calibrationSchema.ts`, `evalSchema.ts`, `format.ts`
- `src/stores/`: `uiStore.ts`, `settingsStore.ts`
- `src/components/ui/*`: shadcn/Base UI button, dialog, sheet, tabs, tooltip, popover, select, slider, switch, input, textarea, toggle-group, badge, skeleton, dropdown-menu, command, kbd, separator, scroll-area, resizable
- `src/components/{layout,common,verdict,gate,audit,live,audits,overview,gateway,knowledge,evals,playground,settings,landing}/*` (names in section 6)
- `src/content/jevAccess.ts`, `src/styles/{tokens.css,index.css}`
- `src/test/{setup.ts,msw/handlers.ts,fakes/FakeWebSocket.ts,fixtures/*}`
- `public/favicon.svg`, `public/config.json`, `public/demo/{mock,recorded}/…`
- `scripts/record-demo.mjs`, `scripts/demo-scenarios.json`, `scripts/check-bundle-size.mjs`
- `nginx/templates/default.conf.template`, `nginx/entrypoint.d/40-runtime-config.sh`
- `vitest.config.ts`, `playwright.config.ts`, `components.json`, `lighthouserc.json`, `e2e/{demo,stack}/*.spec.ts`
- `DESIGN.md`
- Repo root: `.github/workflows/pages.yml`, `docker-compose.e2e.yml`, `docs/JEV-ACCESS.md` (content co-owned with the gateway spec)

**Modified**
- `package.json` (deps; scripts `dev`, `build`, `test`, `e2e:demo`, `e2e:stack`, `size`, `demo:record`, `lint`)
- `vite.config.ts`, `eslint.config.js` (add jsx-a11y and the tanstack plugins), `tsconfig.app.json` (paths `@/*`), `index.html`, `Dockerfile`, `README.md`
- Root files: `docker-compose.yml` (dashboard service), `.github/workflows/ci.yml` (frontend job additions and the `e2e-stack` job), `.env.example` (`TRUSTAGENT_API_BASE`)

**Deleted**
- `src/App.tsx`, `src/api/*`, `src/context/*`, `src/hooks/*`, `src/stores/auditStore.ts` (+ its test), `src/types/audit.ts`
- `src/components/{audit,dashboard,history,kb,upload,layout/Header}.tsx` (the old tests are replaced by the tests in section 9)
- `nginx.conf`, `e2e/capture.mjs` (replaced by the screenshots spec, which writes to `docs/images/` on demand)

---

## 12. DESIGN.md outline

1. **Purpose and point of view.** "Color means verdict"; quiet chrome; density with hierarchy; one home per fact.
2. **Tokens.** The light and dark tables with WCAG and APCA values; tints; elevation by lightness; how to add a token.
3. **Verdict encoding.** Color + icon + underline + label. The CVD rationale (light-mode ΔE 3.7, SUPPORTED vs PARTIAL for tritanopes). Never color alone.
4. **Gate encoding.** Achromatic chips with icons; block is the inverted chip.
5. **Typography.** Scale, weights, tabular numbers, mono only for IDs and JSON, sentence case.
6. **Layout.** 4px grid, radii, hairlines, shell breakpoints, pane rules.
7. **Motion.** Durations, properties, reduced motion, row flash, view transitions.
8. **Data honesty rules.**
   - Every number has a source.
   - "not yet measured" instead of placeholders.
   - Demo and synthetic badges come from the manifest.
   - Labels separate session, recorded set and server figures.
   - "Agreement" is never called "accuracy".
9. **States checklist per view.** Empty, loading (delay and minimum time), error with the server message, offline, reconnecting, degraded (fallback judge, Qdrant, DB, Jev breaker), read-only demo.
10. **Charts.** Recharts vs visx; verdict hues vs neutrals; axis and number formats; a table alternative for each chart.
11. **Interaction.** URL state, keyboard map, ⌘K, peek vs open, optimistic vs confirmed actions (policies are always confirmed).
12. **Accessibility.** WCAG 2.2 AA, focus ring, target size, live regions, axe gate.
13. **Voice.** Plain, specific, no exclamation marks, no all caps, no emoji.
14. **Don't list.** Glows, gradients, neon, rows of four identical stat cards, colored left-border cards, emoji icons, Geist + zinc + blue CTA.
15. **ADRs.** Base UI over Radix; Recharts + visx; TanStack Virtual over Virtuoso; no persisted audits; same-origin nginx; TS 6.0 and ESLint 9 pins.

---

## 13. Ordered task list

| # | Task | Size | Depends on |
|---|---|---|---|
| F1 | Toolchain: React 19.3, Vite 8, plugin-react 6, Vitest 5, TS 6.0.3, ESLint 9.39.5 + plugins; old UI still builds | S | — |
| F1b | React Compiler (optional; keep only if build and tests are green) | S | F1 |
| F2 | Tokens, fonts, `DESIGN.md`, shadcn (Base UI) primitives, theme init | M | F1 |
| F3 | Router, AppShell, TopBar, ConnectionIndicator, ⌘K, 404, gallery route, landing stub | M | F2 |
| F4 | Runtime config, http/errors, types, normalize, DataSource, LiveDataSource, StreamClient, ring and flush, streamStore, queries, SystemStatus | L | F3, (C1–C5 in parallel) |
| F5 | ReplayDataSource, scheduler, manifest, DemoBadge, mock fixture seed (hand-assembled temporary set) | M | F4 |
| F6 | Live page | M | F4 |
| F7 | Audits list | M | F4 |
| F8 | Audit detail: segmentation, waterfall, rail, diff, keyboard | L | F7, C7 |
| F9 | Overview (+ session fallback) | M | F4, C5 |
| F10 | Gateway: decisions, policies editor + dry-run + history, calibration import | L | F4, C6 |
| F11 | Knowledge | M | F4 |
| F12 | Evals + eval schema + normalizer | M | F2, eval harness output |
| F13 | Playground | M | F4, C8–C11 |
| F14 | Settings + JevSetupGuide + `docs/JEV-ACCESS.md` | M | F4, C3, C6 |
| F15 | Landing page (tape, diagram, quickstart, benchmarks, limitations) | L | F5, F12 |
| F16 | Docker/nginx/runtime config, compose, `pages.yml` | M | F5 |
| F17 | `record-demo.mjs`, scenarios, `docker-compose.e2e.yml`, regenerated mock set; recorded set once a key exists | M | F13, backend gate |
| F18 | Tests: units throughout, then Playwright demo + stack, axe, visual, perf, size and Lighthouse gates | L | F6–F17 |
| F19 | Delete legacy code, refresh README screenshots (`e2e` capture), CHANGELOG, VERSION bump with its 4 mirrors | S | F18 |

The critical path is F1 → F4 → F8/F10 → F18. The frontend can proceed on msw mocks of contracts C1–C11 until the backend lands; the mock handlers double as the contract documentation.

---
