/**
 * Query keys and queryOptions factories (TanStack Query). Pages call
 * `useQuery(tracesQuery(ds, params))`; the stream side effects in liveStream.ts use the same keys.
 */
import { QueryClient, queryOptions } from '@tanstack/react-query';
import { isApiError } from './errors';
import type { AuditListParams, DataSource, DecisionListParams, TraceListParams } from './source';

export const qk = {
  health: ['health'] as const,
  status: ['status'] as const,
  traces: {
    all: ['traces'] as const,
    lists: () => ['traces', 'list'] as const,
    list: (params: TraceListParams) => ['traces', 'list', params] as const,
    detail: (id: string) => ['traces', 'detail', id] as const,
  },
  decisions: {
    all: ['decisions'] as const,
    list: (params: DecisionListParams) => ['decisions', 'list', params] as const,
  },
  audits: {
    all: ['audits'] as const,
    lists: () => ['audits', 'list'] as const,
    list: (params: AuditListParams) => ['audits', 'list', params] as const,
    detail: (id: string) => ['audits', 'detail', id] as const,
  },
};

/** Health is polled; the pill must notice a proxy that went away. */
export const HEALTH_POLL_MS = 15_000;
export const STATUS_POLL_MS = 30_000;

export function healthQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qk.health,
    queryFn: ({ signal }) => ds.getHealth(signal),
    refetchInterval: HEALTH_POLL_MS,
    retry: false,
    staleTime: 5_000,
  });
}

/**
 * GET /api/status. Absent (404) or not permitted (401/403) is a normal state for older proxies
 * and read-less sessions: callers fall back to /health and say "unknown", never "running".
 */
export function statusQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qk.status,
    queryFn: ({ signal }) => ds.getStatus(signal),
    refetchInterval: (query) => (isStatusUnavailable(query.state.error) ? false : STATUS_POLL_MS),
    retry: false,
    staleTime: 10_000,
  });
}

export function isStatusUnavailable(err: unknown): boolean {
  return isApiError(err) && (err.status === 404 || err.status === 401 || err.status === 403 || err.status === 501);
}

export function tracesQuery(ds: DataSource, params: TraceListParams) {
  return queryOptions({
    queryKey: qk.traces.list(params),
    queryFn: ({ signal }) => ds.listTraces(params, signal),
  });
}

export function traceQuery(ds: DataSource, id: string) {
  return queryOptions({
    queryKey: qk.traces.detail(id),
    queryFn: ({ signal }) => ds.getTrace(id, signal),
  });
}

export function auditsQuery(ds: DataSource, params: AuditListParams) {
  return queryOptions({
    queryKey: qk.audits.list(params),
    queryFn: ({ signal }) => ds.listAudits(params, signal),
  });
}

export function auditQuery(ds: DataSource, id: string) {
  return queryOptions({
    queryKey: qk.audits.detail(id),
    queryFn: ({ signal }) => ds.getAudit(id, signal),
  });
}

export function decisionsQuery(ds: DataSource, params: DecisionListParams) {
  return queryOptions({
    queryKey: qk.decisions.list(params),
    queryFn: ({ signal }) => ds.listDecisions(params, signal),
  });
}

/** Retry transient failures (network, timeout, 429, 5xx) twice; never a 4xx. */
export function shouldRetry(failureCount: number, err: unknown): boolean {
  if (isApiError(err)) return err.transient && failureCount < 2;
  return failureCount < 2;
}

/** Honours Retry-After, else exponential from 1 s. */
export function retryDelay(failureCount: number, err: unknown): number {
  if (isApiError(err) && err.retryAfterMs !== null) return Math.min(30_000, err.retryAfterMs);
  return Math.min(30_000, 1000 * 2 ** failureCount);
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        retry: shouldRetry,
        retryDelay,
        refetchOnWindowFocus: false,
      },
    },
  });
}
