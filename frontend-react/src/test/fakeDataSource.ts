import { vi } from 'vitest';
import { ApiError } from '@/data/errors';
import type { DataSource } from '@/data/source';

/** A DataSource with a healthy /health, empty lists, and 404 for everything else. */
export function fakeDataSource(overrides: Partial<DataSource> = {}): DataSource {
  const notServed = () =>
    Promise.reject(new ApiError({ kind: 'http', status: 404, code: 'not_found', message: 'Not served in tests.' }));
  return {
    kind: 'live',
    getHealth: vi.fn(async () => ({ status: 'ok' as const, version: '2.0.0-test', demo: false, audit_engine: true })),
    getStatus: vi.fn(notServed),
    listTraces: vi.fn(async () => ({ items: [], next_cursor: null })),
    getTrace: vi.fn(notServed),
    listDecisions: vi.fn(async () => ({ items: [], next_cursor: null })),
    listAudits: vi.fn(async () => ({ items: [], next_cursor: null })),
    getAudit: vi.fn(notServed),
    subscribe: vi.fn(() => ({ retryNow: () => {}, stop: () => {} })),
    ...overrides,
  };
}
