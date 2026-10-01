/**
 * Query keys, queryOptions and stream side effects of the security plane (data/control/securityPlane.ts).
 * Owned by WEB-ON after Step 0b; queries.ts re-exports `qkcSecurity` and calls `securitySideEffects`.
 *
 * The settings query is live from Step 0b because Settings (WEB-ON) and Budgets (WEB-SV) both read it.
 */
import { queryOptions, type QueryClient } from '@tanstack/react-query';
import type { DataSource, PageParams } from '../source';
import type { StreamEvent } from '../stream/types';

export const qkcSecurity = {
  all: ['security'] as const,
  settings: ['security', 'settings'] as const,
  credentials: ['security', 'credentials'] as const,
  apiKeys: ['security', 'api-keys'] as const,
  adminAudit: (params: PageParams) => ['security', 'admin-audit', params] as const,
};

/** GET /api/settings (#52): its `etag` goes back as If-Match on PATCH. */
export function settingsQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkcSecurity.settings,
    queryFn: ({ signal }) => ds.getSettings(signal),
  });
}

/** Keeps security queries in step with one live event (settings_updated, credentials_changed, …). No-op until WEB-ON ON-1. */
export function securitySideEffects(_event: StreamEvent, _queryClient: QueryClient): void {}
