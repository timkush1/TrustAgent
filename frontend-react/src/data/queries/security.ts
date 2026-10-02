/**
 * Query keys, queryOptions and stream side effects of the security plane (data/control/securityPlane.ts).
 * Owned by WEB-ON after Step 0b; queries.ts re-exports `qkcSecurity` and calls `securitySideEffects`.
 *
 * The settings query is live from Step 0b because Settings (WEB-ON) and Budgets (WEB-SV) both read it.
 */
import { infiniteQueryOptions, queryOptions, type QueryClient } from '@tanstack/react-query';
import type { DataSource, PageParams } from '../source';
import type { StreamEvent } from '../stream/types';

export const qkcSecurity = {
  all: ['security'] as const,
  settings: ['security', 'settings'] as const,
  credentials: ['security', 'credentials'] as const,
  apiKeys: ['security', 'api-keys'] as const,
  adminAudit: (params: PageParams) => ['security', 'admin-audit', params] as const,
  /** The outcome of the last Connect OpenRouter in this tab, handed from /connect/callback to the page it returns to. */
  connectResult: ['security', 'connect-result'] as const,
};

/**
 * `qk.status` of queries.ts, repeated here because queries.ts imports this module (a cycle would
 * leave it undefined while the modules load). security.test.ts checks the two stay equal.
 */
export const STATUS_KEY = ['status'] as const;

/** GET /api/settings (#52): its `etag` goes back as If-Match on PATCH. */
export function settingsQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkcSecurity.settings,
    queryFn: ({ signal }) => ds.getSettings(signal),
  });
}

/** GET /api/credentials (#54, admin): masked keys, where each is used, and how its last check went. */
export function credentialsQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkcSecurity.credentials,
    queryFn: ({ signal }) => ds.listCredentials(signal),
  });
}

/** GET /api/keys (#67, admin): every API key, revoked ones included; secrets are never listed. */
export function apiKeysQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkcSecurity.apiKeys,
    queryFn: ({ signal }) => ds.listApiKeys(signal),
  });
}

/** GET /api/admin/audit-log (#70): keyset pages, newest first. */
export function adminAuditQuery(ds: DataSource, limit = 50) {
  return infiniteQueryOptions({
    queryKey: qkcSecurity.adminAudit({ limit }),
    queryFn: ({ pageParam, signal }) => ds.listAdminAudit({ limit, cursor: pageParam ?? undefined }, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
  });
}

function invalidate(queryClient: QueryClient, keys: readonly (readonly unknown[])[]): void {
  for (const queryKey of keys) void queryClient.invalidateQueries({ queryKey, refetchType: 'active' });
}

/**
 * Keeps security queries in step with one live event (P4-PLAN §10 ON-1):
 * - `settings_updated` (any runtime-config write, from this tab or another): settings and status;
 * - `credentials_changed` (put, delete, rotation): credentials, settings (bindings) and status.
 * Writes from this tab also invalidate directly, so the page never waits for the socket; after a
 * reconnect, `hello` refreshes the status (liveStream.ts) and the pages refetch on focus.
 */
export function securitySideEffects(event: StreamEvent, queryClient: QueryClient): void {
  switch (event.type) {
    case 'settings_updated':
      invalidate(queryClient, [qkcSecurity.settings, STATUS_KEY]);
      break;
    case 'credentials_changed':
      invalidate(queryClient, [qkcSecurity.credentials, qkcSecurity.settings, STATUS_KEY]);
      break;
    default:
      break;
  }
}
