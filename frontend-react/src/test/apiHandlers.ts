/**
 * msw handlers for the proxy's read API (SPEC §2.4) over an in-memory store of wire objects
 * (src/types/api.ts shapes; see scenarios.ts). Component tests mutate the store to move a trace
 * along, and every request is recorded so tests can assert what was asked.
 *
 * Served: GET /health (#1), /api/status (#8, 404 unless set), /api/traces (#10: route, model,
 * outcome, limit, cursor), /api/traces/:id (#11), /api/audits/:id (#14), and empty pages for the
 * resync endpoints /api/gate/decisions (#16) and /api/audits (#13).
 */
import { http, HttpResponse, type HttpHandler } from 'msw';
import { outcomeOf } from '@/domain/presentation';
import type { AuditDetail, SystemStatus, TraceDetail, TraceSummary } from '@/types/api';
import { API } from './msw';

export interface FakeApiState {
  /** Newest first, as the API orders them. */
  traces: TraceSummary[];
  details: Record<string, TraceDetail>;
  audits: Record<string, AuditDetail>;
  status: SystemStatus | null;
  /** Replaces the /api/traces response (error cases). */
  tracesFailure: { status: number; code: string; message: string } | null;
  /** Page size when the request has no `limit`. */
  defaultLimit: number;
}

export interface FakeApi {
  state: FakeApiState;
  handlers: HttpHandler[];
  /** Query strings of every GET /api/traces, in order. */
  traceQueries: URLSearchParams[];
}

function apiError(status: number, code: string, message: string) {
  return HttpResponse.json({ error: { code, message } }, { status });
}

export function createFakeApi(initial: Partial<FakeApiState> = {}): FakeApi {
  const state: FakeApiState = {
    traces: [],
    details: {},
    audits: {},
    status: null,
    tracesFailure: null,
    defaultLimit: 50,
    ...initial,
  };
  const traceQueries: URLSearchParams[] = [];

  const handlers = [
    http.get(`${API}/health`, () =>
      HttpResponse.json({ status: 'ok', version: '2.0.0-test', demo: state.status?.demo ?? false, audit_engine: true }),
    ),
    http.get(`${API}/api/status`, () =>
      state.status ? HttpResponse.json(state.status) : apiError(404, 'not_found', 'Not served in tests.'),
    ),
    http.get(`${API}/api/traces`, ({ request }) => {
      const q = new URL(request.url).searchParams;
      traceQueries.push(q);
      if (state.tracesFailure) {
        const f = state.tracesFailure;
        return apiError(f.status, f.code, f.message);
      }
      const matching = state.traces.filter(
        (t) =>
          (!q.get('route') || t.route === q.get('route')) &&
          (!q.get('model') || t.model === q.get('model')) &&
          (!q.get('outcome') || outcomeOf(t) === q.get('outcome')),
      );
      const limit = Number(q.get('limit') ?? state.defaultLimit);
      const start = Number(q.get('cursor') ?? 0);
      const items = matching.slice(start, start + limit);
      const next = start + limit < matching.length ? String(start + limit) : null;
      return HttpResponse.json({ items, next_cursor: next });
    }),
    http.get(`${API}/api/traces/:id`, ({ params }) => {
      const detail = state.details[String(params.id)];
      return detail ? HttpResponse.json(detail) : apiError(404, 'not_found', 'No trace with this id.');
    }),
    http.get(`${API}/api/audits/:id`, ({ params }) => {
      const audit = state.audits[String(params.id)];
      return audit ? HttpResponse.json(audit) : apiError(404, 'not_found', 'No audit with this id.');
    }),
    http.get(`${API}/api/audits`, () => HttpResponse.json({ items: [], next_cursor: null })),
    http.get(`${API}/api/gate/decisions`, () => HttpResponse.json({ items: [], next_cursor: null })),
  ];

  return { state, handlers, traceQueries };
}
