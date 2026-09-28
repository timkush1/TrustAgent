import { describe, expect, it } from 'vitest';
import { INITIAL_CONNECTION } from '@/data/stream/types';
import { hello } from '@/test/fixtures';
import { systemStatus } from '@/test/status';
import { summarizeHealth, type HealthInputs } from './health';

const healthy = { status: 'ok' as const, version: '2.0.0', demo: true, audit_engine: true };
const open = { ...INITIAL_CONNECTION, phase: 'open' as const };

function inputs(overrides: Partial<HealthInputs> = {}): HealthInputs {
  return {
    health: { data: healthy, pending: false },
    status: null,
    connection: open,
    hello: null,
    components: {},
    ...overrides,
  };
}

describe('summarizeHealth', () => {
  it('is ready when every known component is fine, and says unknown for the rest', () => {
    const s = summarizeHealth(inputs());
    expect(s.label).toBe('All systems ready');
    expect(s.tone).toBe('success');
    expect(s.checks.find((c) => c.id === 'engine')).toMatchObject({ state: 'ok' });
    expect(s.checks.find((c) => c.id === 'fast_check')).toMatchObject({ state: 'unknown' });
    expect(s.checks.find((c) => c.id === 'judge')?.detail).toBe('Unknown (status not available)');
  });

  it('uses hello and full status when available', () => {
    const s = summarizeHealth(inputs({ hello: hello(), status: systemStatus() }));
    expect(s.checks.map((c) => [c.id, c.state])).toEqual([
      ['proxy', 'ok'],
      ['live', 'ok'],
      ['fast_check', 'info'],
      ['engine', 'ok'],
      ['judge', 'ok'],
      ['database', 'ok'],
    ]);
    expect(s.issues).toBe(0);
  });

  it('counts issues with the right grammar', () => {
    const one = summarizeHealth(inputs({ health: { data: { ...healthy, audit_engine: false }, pending: false } }));
    expect(one).toMatchObject({ issues: 1, label: '1 issue', tone: 'warning' });
    const two = summarizeHealth(
      inputs({
        health: { data: { ...healthy, audit_engine: false }, pending: false },
        connection: { ...INITIAL_CONNECTION, phase: 'reconnecting', attempt: 3 },
      }),
    );
    expect(two).toMatchObject({ issues: 2, label: '2 issues' });
    expect(two.checks.find((c) => c.id === 'live')?.detail).toBe('Reconnecting (attempt 3)');
  });

  it('is danger when the latest /health poll failed, even with older data', () => {
    const s = summarizeHealth(inputs({ health: { data: healthy, error: new Error('down'), pending: false } }));
    expect(s.tone).toBe('danger');
    expect(s.checks[0].state).toBe('issue');
    expect(s.checks.find((c) => c.id === 'engine')?.state).toBe('unknown');
  });

  it('lets status transitions override older snapshots', () => {
    const s = summarizeHealth(
      inputs({ status: systemStatus(), components: { engine: { state: 'down', at: '2026-09-27T12:00:00Z' } } }),
    );
    expect(s.checks.find((c) => c.id === 'engine')?.state).toBe('issue');
  });

  it('falls back to /health when hello carries no status summary', () => {
    const bare = { ...hello(), status: undefined } as unknown as ReturnType<typeof hello>;
    const s = summarizeHealth(inputs({ hello: bare }));
    expect(s.checks.find((c) => c.id === 'engine')).toMatchObject({ state: 'ok', detail: 'Reachable' });
    expect(s.checks.find((c) => c.id === 'fast_check')).toMatchObject({ state: 'unknown' });
    expect(s.checks.find((c) => c.id === 'judge')).toMatchObject({ state: 'unknown' });
  });

  it('notes a missing database without calling it an issue', () => {
    const s = summarizeHealth(
      inputs({ status: systemStatus({ persistence: { state: 'disabled', configured: false, migration: null } }) }),
    );
    expect(s.checks.find((c) => c.id === 'database')).toMatchObject({ state: 'info' });
    expect(s.issues).toBe(0);
  });
});
