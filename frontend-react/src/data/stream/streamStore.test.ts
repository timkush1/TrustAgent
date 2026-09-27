import { beforeEach, describe, expect, it } from 'vitest';
import { audit, decision, flaggedTrace, frame, hello, iso, trace } from '@/test/fixtures';
import type { TraceSummary } from '@/types/api';
import type { StreamEvent } from './types';
import {
  clearLegacyStorage,
  LEGACY_STORAGE_KEY,
  MAX_TRACES,
  selectVisibleTraces,
  useStreamStore,
} from './streamStore';

const ev = (e: Parameters<typeof frame>[0], seq = 1, extra?: Parameters<typeof frame>[3]) =>
  frame(e, seq, 'epoch-a', extra) as StreamEvent;
const store = () => useStreamStore.getState();
const tid = trace().trace_id;

beforeEach(() => {
  store().reset();
});

describe('streamStore merging', () => {
  it('follows a trace from start through decision to audit', () => {
    store().ingest([ev({ type: 'trace_started', data: { trace: trace() } })]);
    expect(store().traces[tid].gate_status).toBe('pending');
    store().ingest([ev({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 2)]);
    expect(store().traces[tid]).toMatchObject({ action: 'flag', gate_status: 'ok' });
    expect(store().decisions[tid].deep_audit).toBe('high');
    store().ingest([
      ev({ type: 'audit_queued', data: { audit_id: 'a-1', lane: 'high', queue_depth: 0 } }, 3, { trace_id: tid }),
      ev({ type: 'audit_started', data: { audit_id: 'a-1', queue_wait_ms: 5 } }, 4),
    ]);
    expect(store().traces[tid].audit.status).toBe('running');
    store().ingest([ev({ type: 'audit_completed', data: { audit: audit() } }, 5)]);
    expect(store().traces[tid].audit).toMatchObject({ status: 'completed', verdict: 'HALLUCINATED' });
    expect(store().audits[tid].audit_id).toBe('a-1');
  });

  it('ends in the same state when the audit arrives before the decision', () => {
    store().ingest([
      ev({ type: 'trace_started', data: { trace: trace() } }),
      ev({ type: 'audit_completed', data: { audit: audit() } }, 2),
      ev({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 3),
    ]);
    expect(store().traces[tid]).toMatchObject({ action: 'flag', audit: { status: 'completed', verdict: 'HALLUCINATED' } });
  });

  it('applies an audit that arrived before its trace', () => {
    store().ingest([ev({ type: 'audit_completed', data: { audit: audit() } })]);
    expect(store().order).toEqual([]);
    store().ingest([ev({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 2)]);
    expect(store().traces[tid].audit.status).toBe('completed');
  });

  it('keeps hello and component status transitions', () => {
    store().ingest([
      ev({ type: 'hello', data: hello() }),
      ev({ type: 'status', data: { component: 'engine', state: 'down', detail: 'ping timeout' } }, 2),
    ]);
    expect(store().hello?.demo).toBe(true);
    expect(store().components.engine).toMatchObject({ state: 'down', detail: 'ping timeout' });
  });

  it('merges a resync by trace_id', () => {
    store().ingest([ev({ type: 'trace_started', data: { trace: trace() } })]);
    const other = trace({ trace_id: 't-2', created_at: iso(1000), updated_at: iso(1000) });
    store().ingest([
      {
        type: 'resync',
        reason: 'gap',
        since: iso(),
        traces: [other],
        decisions: [
          {
            trace_id: tid,
            created_at: iso(300),
            route: 'support-bot',
            mode: 'flag',
            effective_mode: 'flag',
            action: 'flag',
            would_action: 'block',
            status: 'ok',
            verdict: 'contradicted',
            max_risk: 0.98,
            reasons: ['contradiction:critical'],
            late: false,
            decision_snapshot: null,
            decision_ms: 200,
            cost_usd: 0,
            audit_verdict: null,
            evaluator_levels: {},
          },
        ],
        audits: [audit()],
        truncated: true,
      },
    ]);
    expect(store().order).toEqual(['t-2', tid]);
    expect(store().traces[tid]).toMatchObject({ action: 'flag', audit: { status: 'completed' } });
    expect(store().counters).toMatchObject({ resyncs: 1, resyncTruncated: true });
  });

  it('counts gaps and dropped events', () => {
    store().ingest(
      [ev({ type: 'gap', data: { from_seq: 1, to_seq: 5, reason: 'slow_consumer', resync: { traces: '', decisions: '', audits: '' } } })],
      7,
    );
    expect(store().counters).toMatchObject({ gaps: 1, droppedEvents: 7 });
  });
});

describe('streamStore order, pause and bounds', () => {
  const at = (id: string, ms: number): TraceSummary => trace({ trace_id: id, created_at: iso(ms), updated_at: iso(ms) });
  const started = (t: TraceSummary) => ev({ type: 'trace_started', data: { trace: t } });

  it('orders newest first regardless of arrival order', () => {
    store().ingest([started(at('b', 2000)), started(at('a', 1000)), started(at('c', 3000))]);
    expect(store().order).toEqual(['c', 'b', 'a']);
    expect(selectVisibleTraces(store()).map((t) => t.trace_id)).toEqual(['c', 'b', 'a']);
  });

  it('freezes the visible order while paused and counts new traces', () => {
    store().ingest([started(at('a', 1000))]);
    store().pause();
    store().ingest([started(at('b', 2000)), started(at('c', 3000))]);
    store().ingest([ev({ type: 'gate_decision', data: { trace: flaggedTrace({ trace_id: 'a', created_at: iso(1000) }), decision: decision({ trace_id: 'a' }) } })]);
    expect(store().visibleOrder).toEqual(['a']);
    expect(store().pendingCount).toBe(2);
    expect(selectVisibleTraces(store())[0].action).toBe('flag'); // rows on screen still update
    store().resume();
    expect(store().visibleOrder).toEqual(['c', 'b', 'a']);
    expect(store().pendingCount).toBe(0);
  });

  it(`keeps only the newest ${MAX_TRACES} traces`, () => {
    const batch: StreamEvent[] = [];
    for (let i = 0; i < MAX_TRACES + 5; i += 1) batch.push(started(at(`t${i}`, i * 10)));
    store().ingest(batch);
    expect(store().order).toHaveLength(MAX_TRACES);
    expect(store().traces.t0).toBeUndefined();
    expect(store().traces[`t${MAX_TRACES + 4}`]).toBeDefined();
  });
});

describe('clearLegacyStorage', () => {
  it('removes the v1 persisted audit list', () => {
    localStorage.setItem(LEGACY_STORAGE_KEY, '{"state":{}}');
    clearLegacyStorage();
    expect(localStorage.getItem(LEGACY_STORAGE_KEY)).toBeNull();
  });
});
