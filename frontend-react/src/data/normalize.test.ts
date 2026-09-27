import { describe, expect, it } from 'vitest';
import { audit, decision, flaggedTrace, frame, hb, hello, iso, trace } from '@/test/fixtures';
import { ApiError } from './errors';
import {
  applyAuditStatus,
  applyAuditToTrace,
  applyDecisionRow,
  mergeTraceSummary,
  normalizeClaimVerdict,
  normalizeHealth,
  normalizeObject,
  normalizePage,
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

  const env = (type: string, data: unknown) => JSON.stringify({ v: 2, type, seq: 3, epoch: 'e', ts: iso(), data });
  const { evaluators: _evaluators, ...noEvaluators } = decision();

  it.each([
    ['a hello without epoch', env('hello', { ...hello(), epoch: undefined })],
    ['a hello with a fractional last_seq', env('hello', { ...hello(), last_seq: 2.5 })],
    ['a gap without to_seq', env('gap', { from_seq: 1, reason: 'slow_consumer' })],
    ['a trace without its audit block', env('trace_started', { trace: { ...trace(), audit: null } })],
    ['a trace without an id', env('trace_started', { trace: { ...trace(), trace_id: 7 } })],
    ['a decision without evaluators', env('gate_decision', { trace: flaggedTrace(), decision: noEvaluators })],
    ['an audit whose claims are not a list', env('audit_completed', { audit: { ...audit(), claims: {} } })],
    ['an audit_queued without audit_id', env('audit_queued', { lane: 'high', queue_depth: 1 })],
    ['a status without state', env('status', { component: 'engine' })],
  ])('rejects %s as bad_data', (_what, data) => {
    expect(parseWsFrame(data)).toEqual({ ok: false, reason: 'bad_data', type: JSON.parse(data).type });
  });

  it('reads a list sent as null (a Go nil slice) as empty', () => {
    const r = parseWsFrame(env('gate_decision', { trace: { ...flaggedTrace(), reasons: null }, decision: { ...decision(), detectors: null } }));
    expect(r).toMatchObject({ ok: true, event: { data: { trace: { reasons: [] }, decision: { detectors: [] } } } });
    const { claims: _claims, ...noClaims } = audit();
    expect(parseWsFrame(env('audit_completed', { audit: noClaims }))).toMatchObject({ ok: true, event: { data: { audit: { claims: [] } } } });
  });

  it('accepts the event data the dashboard can use', () => {
    expect(parseWsFrame(env('hello', hello()))).toMatchObject({ ok: true });
    expect(parseWsFrame(env('gate_decision', { trace: flaggedTrace(), decision: decision() }))).toMatchObject({ ok: true });
    expect(parseWsFrame(env('audit_completed', { audit: audit() }))).toMatchObject({ ok: true });
    expect(parseWsFrame(env('audit_dropped', { audit_id: null, reason: 'queue_full' }))).toMatchObject({ ok: true });
    // Refresh-only events are not inspected.
    expect(parseWsFrame(env('policy_updated', {}))).toMatchObject({ ok: true });
  });
});

describe('normalizePage and normalizeObject', () => {
  it('passes a v2 page through and reads an empty cursor as the end', () => {
    const page = { items: [trace(), flaggedTrace()], next_cursor: 'c2' };
    expect(normalizePage(page, 'GET /api/traces', 'trace')).toEqual(page);
    expect(normalizePage({ items: [], next_cursor: '' }, 'GET /api/traces', 'trace')).toEqual({ items: [], next_cursor: null });
    expect(normalizePage({ items: [audit()] }, 'GET /api/audits', 'auditSummary')).toMatchObject({ next_cursor: null });
  });

  it.each([
    ['the pre-v2 audit list', { audits: [audit()], total: 1, limit: 50, offset: 0 }],
    ['an HTML fallback page', '<!doctype html><html></html>'],
    ['an empty body', undefined],
    ['a numeric cursor', { items: [], next_cursor: 5 }],
    ['an item without the ids it is keyed on', { items: [{ ...audit(), trace_id: undefined }], next_cursor: null }],
  ])('rejects %s as an invalid_response ApiError', (_what, body) => {
    const err = (() => {
      try {
        normalizePage(body, 'GET /api/audits', 'auditSummary');
      } catch (e) {
        return e;
      }
      return null;
    })();
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ kind: 'invalid_response', code: 'invalid_response', transient: false });
    expect((err as ApiError).message).toContain('GET /api/audits');
  });

  it('reads null lists as empty in pages too', () => {
    const page = normalizePage({ items: [{ ...trace(), reasons: null }], next_cursor: null }, 'GET /api/traces', 'trace');
    expect(page.items[0].reasons).toEqual([]);
  });

  it('checks single objects the same way', () => {
    expect(normalizeObject(audit(), 'GET /api/audits/:id', 'auditDetail')).toEqual(audit());
    // The pre-v2 audit record has no trace_id and no claims array.
    expect(() => normalizeObject({ id: 'a1', trust_score: 80 }, 'GET /api/audits/:id', 'auditDetail')).toThrow(ApiError);
    expect(() => normalizeObject({ ...trace(), timings: null }, 'GET /api/traces/:id', 'traceDetail')).toThrow(/older version/);
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
