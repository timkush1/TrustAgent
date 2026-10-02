/**
 * Security side effects (ON-1): a live `settings_updated` refreshes the settings and the
 * status; `credentials_changed` also refreshes the credentials; other events change nothing here.
 */
import { QueryClient } from '@tanstack/react-query';
import { describe, expect, it } from 'vitest';
import credentialsChanged from '@contracts/fixtures/api/WsEvent.credentials_changed.json';
import settingsUpdated from '@contracts/fixtures/api/WsEvent.settings_updated.json';
import { qk } from '@/data/queries';
import type { StreamEvent } from '@/data/stream/types';
import { qkcSecurity, securitySideEffects, STATUS_KEY } from './security';

function seeded() {
  const qc = new QueryClient();
  for (const key of [qkcSecurity.settings, qkcSecurity.credentials, qkcSecurity.apiKeys, qk.status, qk.traces.all]) qc.setQueryData(key, {});
  return qc;
}

const stale = (qc: QueryClient) =>
  qc
    .getQueryCache()
    .getAll()
    .filter((q) => q.state.isInvalidated)
    .map((q) => JSON.stringify(q.queryKey))
    .sort();

describe('securitySideEffects', () => {
  it('uses the same status key as queries.ts', () => {
    expect(STATUS_KEY).toEqual(qk.status);
  });

  it('refreshes settings and status on settings_updated', () => {
    const qc = seeded();
    securitySideEffects(settingsUpdated as unknown as StreamEvent, qc);
    expect(stale(qc)).toEqual(['["security","settings"]', '["status"]']);
  });

  it('refreshes credentials, settings and status on credentials_changed', () => {
    const qc = seeded();
    securitySideEffects(credentialsChanged as unknown as StreamEvent, qc);
    expect(stale(qc)).toEqual(['["security","credentials"]', '["security","settings"]', '["status"]']);
  });

  it('ignores other events', () => {
    const qc = seeded();
    for (const type of ['policy_updated', 'hello', 'gate_decision']) securitySideEffects({ type } as unknown as StreamEvent, qc);
    expect(stale(qc)).toEqual([]);
  });
});
