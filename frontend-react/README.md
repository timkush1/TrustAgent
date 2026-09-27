# TrustAgent dashboard (frontend-react)

The TrustAgent web dashboard. It shows every LLM answer the gateway checked: the outcome, the
reasons, and what the answer was checked against. Live updates arrive over WebSocket protocol v2.
The normative page list is in [docs/v2/SPEC.md §3.3](../docs/v2/SPEC.md). The house style is in
[DESIGN.md](DESIGN.md).

## Stack

- React 19.3, TypeScript 6 and Vite 8 (Rolldown).
- TanStack Router with file routes in `src/routes/`, and TanStack Query.
- Zustand for live state only. Nothing is persisted.
- Base UI primitives, styled locally in `src/components/ui/`.
- Tailwind 4 with the tokens in `src/index.css`.
- zod 4 (`zod/mini`), lucide-react and sonner.
- Tests: Vitest with Testing Library and msw. Playwright with axe runs end-to-end.

## Scripts

```bash
npm run dev        # Vite dev server on :5173; /api, /v1, /health and /ws go to the proxy on :8080
npm run build      # tsc -b && vite build
npm run lint       # eslint, including the raw-enum rule
npm test           # vitest run (unit and component tests; no network, no Docker)
npx playwright test --project=stack   # needs a running stack; see "End-to-end" below
```

## Layout

```
src/
├── main.tsx                 boot: theme → /config.json → services → live stream → router
├── router.tsx               router, page-contract pending timing (200 ms delay, 400 ms minimum)
├── routes/                  file routes (routeTree.gen.ts is generated and committed)
│   ├── app.tsx              the shell (nav, top bar, banner)
│   └── app/…                overview, traffic, evaluators, policies, playground, settings
├── config/runtime.ts        loads /config.json, then falls back to same-origin defaults
├── data/
│   ├── http.ts, errors.ts   REST client and ApiError (server message, code, Retry-After)
│   ├── normalize.ts         the single normalizer (WS frames, /health, trace merging)
│   ├── queries.ts           query keys and queryOptions factories
│   ├── source.ts            DataSource interface (LiveDataSource now; ReplayDataSource in P3/P5)
│   ├── live/                LiveDataSource, StreamClient (WS v2), backoff
│   ├── stream/              ring buffer, rAF/interval Flusher, streamStore (Zustand)
│   ├── liveStream.ts        wires the stream once per page, outside React
│   └── useSystem.ts         health, status, demo/read-only, Health pill and banner hooks
├── domain/
│   ├── presentation.ts      outcomes, reasons, level words, verdicts, tiers, glossary
│   ├── health.ts, banner.ts Health pill checklist and banner selection
│   └── format.ts            numbers, money, durations, times
├── components/
│   ├── ui/                  Button, Popover, Tooltip, Skeleton (over Base UI)
│   ├── layout/              AppShell, NavRail, TopBar, HealthPill, Banner, PhasePlaceholder
│   ├── common/              Empty, Error and Loading states, QueryState, Term, DisabledReason
│   └── outcome/             OutcomePill, VerdictBadge, AuditState
└── types/api.ts             REST and WS v2 wire types (contract-owned; see the file header)
```

## Data flow

1. `main.tsx` starts the live stream once, outside React: `LiveDataSource.subscribe` →
   `StreamClient`.
2. The client negotiates `GET /ws?protocol=2`. On reconnect it asks for replay with
   `since_seq` and `epoch`. On `gap`, an epoch change or a seq hole, it resyncs over REST
   (`/api/traces?updated_since`, `/api/gate/decisions?updated_since`,
   `/api/audits?completed_since`, from 5 s before the last event).
3. It reconnects with full-jitter backoff (250 ms floor, 30 s cap) and never gives up.
4. Events go into a 5,000-slot ring buffer. The Flusher commits them to `streamStore` at most
   once per 100 ms (rAF), or once per second on a hidden tab. It also updates the TanStack Query
   cache: audit details, trace-detail invalidation, and a list refresh after hello or resync.
5. Pages read REST through `useQuery(tracesQuery(ds, params))` and friends. They merge live rows
   with `mergeTraceLists` and render every enum through `presentation.ts`.

## Configuration

`/config.json` is written at container start from `DASHBOARD_API_BASE` and `DASHBOARD_WS_URL`.
Every field is optional. A missing file means same origin.

```json
{ "apiBase": "", "wsUrl": "" }
```

- `apiBase` is `""`, an `https://…` origin (plus an optional path), or an absolute path.
- `wsUrl` is `""` (derived as `ws(s)://<apiBase or page host>/ws`), a `wss://…` URL, or a path.

Build-time keys, none of them secret:

| Key | Values | Meaning |
|---|---|---|
| `VITE_APP_MODE` | `full`, `static` | Build flavour. The static Pages build arrives in P5 |
| `VITE_BASE_PATH` | default `/` | Vite `base` and the router base path |
| `VITE_REPO_URL` | | Repository link |

No key or secret is ever compiled into the bundle.

## End-to-end

`playwright.config.ts` defines the `stack` project. It points at the dashboard of a running stack,
`http://localhost:${DASHBOARD_HOST_PORT:-3000}`. `e2e/stack/shell.spec.ts` checks the shell, the
Health pill, the theme toggle, and axe in both themes on every route:

```bash
make demo
cd frontend-react && npx playwright install chromium && npx playwright test --project=stack
```
