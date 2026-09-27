# TrustAgent Dashboard (frontend-react)

The real-time dashboard for TrustAgent — a React 19 + TypeScript + Vite SPA that streams audit
results over WebSocket and renders trust scores, claim breakdowns, the knowledge base, and history.

## Stack

React 19 · TypeScript · Vite 5 · **Zustand** (state) · **Tailwind v4** (config-less — styles in
`src/index.css` via `@import "tailwindcss"`, no `tailwind.config.js`) · Vitest + Testing Library.

## Scripts

```bash
npm run dev        # Vite dev server (http://localhost:5173)
npm run build      # tsc -b && vite build  (typecheck + production build)
npm run lint       # eslint
npm test           # vitest run
npm run preview    # serve the production build
```

## Structure

```
src/
├── App.tsx                     # layout + tab switching (live | history | kb)
├── main.tsx                    # entry; wraps <App/> in <WebSocketProvider>
├── context/
│   ├── WebSocketProvider.tsx   # owns the SINGLE WebSocket; dispatches audit_result → store
│   └── websocketContext.ts     # context + useWebSocketContext() hook
├── hooks/
│   ├── useWebSocket.ts         # low-level socket with auto-reconnect
│   └── useHealth.ts            # polls /health for real service status
├── stores/auditStore.ts        # Zustand: audits[], selectedAudit, getStats() (persisted to localStorage)
├── api/
│   ├── client.ts               # getApiBase/getWsUrl + fetchWithAuth (dashboard-key auth)
│   ├── audits.ts               # /api/audits history client
│   └── kb.ts                   # /api/kb/* knowledge-base client
├── components/
│   ├── audit/                  # AuditFeed, AuditRow, AuditDetail, ClaimBreakdown, PipelineView, QueryInput
│   ├── dashboard/              # MetricsPanel, TrustScoreGauge
│   ├── history/                # HistoryView (Postgres-backed, filterable)
│   ├── kb/                     # KnowledgeBaseView (accepted/quarantined claims + conflicts)
│   ├── layout/                 # Header
│   └── upload/                 # FileUpload (drag-drop JSON → KB ingestion)
└── types/audit.ts              # shared types (AuditResult, ClaimVerification, WSMessage, ...)
```

## Data flow

1. `WebSocketProvider` opens **one** connection to `ws://…:8080/ws` and, on every `audit_result`
   frame, calls `auditStore.addAudit(...)` (idempotent by `audit_id`). Components read the store;
   they never open their own sockets.
2. `AuditFeed` renders `auditStore.audits`; clicking a row opens `AuditDetail`.
3. `HistoryView` / `KnowledgeBaseView` fetch REST endpoints through `fetchWithAuth`.

### WebSocket message shape

```ts
// types/audit.ts
interface WSMessage { type: 'audit_result' | 'connected' | '...'; timestamp: string; data?: AuditResult }

interface AuditResult {
  audit_id: string; request_id: string;
  faithfulness_score: number; overall_score: number;   // no fabricated relevancy_score
  hallucination_detected: boolean;
  claims: ClaimVerification[];   // status: SUPPORTED | CONTRADICTED | UNSUPPORTED | PARTIALLY_SUPPORTED | UNKNOWN
  reasoning_trace: string; step_timings?: Record<string, number>;
}
```

## Configuration

Vite env vars (`.env` / build-time):

| Var | Default | Purpose |
|---|---|---|
| `VITE_API_URL` | `http://localhost:8080` | Proxy REST base |
| `VITE_WS_URL` | `ws://localhost:8080/ws` | WebSocket endpoint |
| `VITE_TRUTHTABLE_API_KEY` | — | **Dashboard** access key, sent to the proxy when it enforces auth |

> Two credentials, don't confuse them: the **dashboard key** above authenticates the browser to the
> proxy (low sensitivity). The **provider key** (Anthropic/OpenAI) lives server-side only and never
> reaches the browser.

## Tests

`npm test` runs the Vitest suite (store, WebSocket hook, HistoryView, KnowledgeBaseView). A Playwright
screenshot/e2e harness lives in [`e2e/`](e2e/).
