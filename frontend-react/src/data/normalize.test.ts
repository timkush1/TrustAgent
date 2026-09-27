import { describe, expect, it } from 'vitest';
import { audit, flaggedTrace, frame, hb, iso, trace } from '@/test/fixtures';
import {
  applyAuditStatus,
  applyAuditToTrace,
  applyDecisionRow,
  mergeTraceSummary,
  normalizeClaimVerdict,
  normalizeHealth,
  parseWsFrame,
} from './normalize';

describe('parseWsFrame', () => {
  it('accepts v2 envelopes and hb', () => {
    const ev = frame({ type: 'trace_started', data: { trace: trace() } }, 5);
    expect(parseWsFrame(JSON.stringify(ev))).toEqual({ ok: true, event: ev });
    expect(parseWsFrame(JSON.stringify(hb()))).toMatchObject({ ok: true, event: { type: 'hb' } });
  });

  it.each([
    [new Uint8Array([1]), 'not_text'],
    ['{nope', 'invalid_json'],
    ['{"a":1}\n{"b":2}', 'invalid_json'],
    [JSON.stringify({ type: 'audit_result', data: {} }), 'not_v2'],
    [JSON.stringify({ v: 1, type: 'hello', seq: 1, epoch: 'e', ts: iso(), data: {} }), 'not_v2'],
    [JSON.stringify({ v: 2, type: 'hello', epoch: 'e', ts: iso(), data: {} }), 'bad_envelope'],
    [JSON.stringify({ v: 2, type: 'hello', seq: 1.5, epoch: 'e', ts: iso(), data: {} }), 'bad_envelope'],
    [JSON.stringify({ v: 2, type: 'hello', seq: 1, epoch: 'e', ts: iso(), data: [] }), 'bad_envelope'],
    [JSON.stringify({ v: 2, type: 'retired_event', seq: 1, epoch: 'e', ts: iso(), data: {} }), 'unknown_type'],
  ])('rejects %s as %s', (data, reason) => {
    expect(parseWsFrame(data)).toEqual({ ok: false, reason });
  });
});

describe('normalizeHealth', () => {
  it('reads the v2 body', () => {
    expect(normalizeHealth({ status: 'ok', version: '2.0.0', demo: true, audit_engine: true })).toEqual({
      status: 'ok',
      version: '2.0.0',
      demo: true,
      audit_engine: true,
    });
  });

  it('tolerates the pre-v2 body', () => {
    expect(normalizeHealth({ status: 'healthy', version: '1.4.0', audit_engine: false })).toEqual({
      status: 'ok',
      version: '1.4.0',
      demo: false,
      audit_engine: false,
    });
  });

  it('rejects anything else', () => {
    expect(() => normalizeHealth('ok')).toThrow();
    expect(() => normalizeHealth({ status: 'down' })).toThrow();
  });
});

describe('normalizeClaimVerdict', () => {
  it('maps unknown values to UNKNOWN', () => {
    expect(normalizeClaimVerdict('CONTRADICTED')).toBe('CONTRADICTED');
    expect(normalizeClaimVerdict('CLAIM_STATUS_UNSPECIFIED')).toBe('UNKNOWN');
    expect(normalizeClaimVerdict(undefined)).toBe('UNKNOWN');
  });
});

describe('mergeTraceSummary', () => {
  it('lets the newer version win', () => {
    const older = trace({ updated_at: iso(0), model: 'a' });
    const newer = trace({ updated_at: iso(10), model: 'b' });
    expect(mergeTraceSummary(older, newer).model).toBe('b');
    expect(mergeTraceSummary(newer, older).model).toBe('b');
  });

  it('never returns a decided trace to pending', () => {
    const decided = flaggedTrace({ updated_at: iso(100) });
    const pendingButNewer = trace({ updated_at: iso(200) });
    const merged = mergeTraceSummary(decided, pendingButNewer);
    expect(merged.gate_status).toBe('ok');
    expect(merged.action).toBe('flag');
    expect(merged.reasons).toEqual(decided.reasons);
    expect(merged.updated_at).toBe(iso(200));
  });

  it('never moves the audit backwards', () => {
    const done = applyAuditToTrace(flaggedTrace({ updated_at: iso(100) }), audit());
    const stale = flaggedTrace({ updated_at: iso(200) }); // audit still queued
    expect(mergeTraceSummary(done, stale).audit.status).toBe('completed');
    expect(mergeTraceSummary(stale, done).audit.verdict).toBe('HALLUCINATED');
  });
});

describe('audit folding', () => {
  it('applies a completed audit to its trace', () => {
    const t = applyAuditToTrace(flaggedTrace(), audit());
    expect(t.audit).toMatchObject({ audit_id: 'a-1', status: 'completed', verdict: 'HALLUCINATED', lane: 'high' });
  });

  it('ignores lifecycle events that would move the audit backwards', () => {
    const done = applyAuditToTrace(flaggedTrace(), audit());
    expect(applyAuditStatus(done, { audit_id: 'a-1', status: 'running' })).toBe(done);
    expect(applyAuditStatus(done, { audit_id: 'a-1', status: 'failed' })).toBe(done);
    const running = applyAuditStatus(flaggedTrace(), { audit_id: 'a-1', status: 'running' });
    expect(running.audit.status).toBe('running');
    expect(applyAuditStatus(running, { audit_id: 'a-1', status: 'failed' }).audit.status).toBe('failed');
  });

  it('folds a decision row into a pending trace', () => {
    const t = applyDecisionRow(trace(), {
      trace_id: trace().trace_id,
      created_at: iso(300),
      route: 'support-bot',
      mode: 'flag',
      effective_mode: 'flag',
      action: 'pass',
      would_action: 'pass',
      status: 'ok',
      verdict: 'supported',
      max_risk: 0.06,
      reasons: [],
      late: false,
      decision_snapshot: null,
      decision_ms: 120,
      cost_usd: 0,
      audit_verdict: null,
      evaluator_levels: {},
    });
    expect(t).toMatchObject({ gate_status: 'ok', action: 'pass', max_risk: 0.06 });
  });
});
