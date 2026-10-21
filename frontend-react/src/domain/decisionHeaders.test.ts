/**
 * The response headers rebuilt from a stored trace, against backend-go internal/proxy/headers.go:
 * the decided path, the observe (deferred) path, action_only, and the values' formats.
 */
import { describe, expect, it } from 'vitest';
import { p3Detail } from '@/test/p3Fixtures';
import { s0Decision, s0Trace } from '@/test/scenarios';
import { auditHeaderValue, checkedAgainstValue, decisionHeaders, deferredHeaders, statusHeaderValue } from './decisionHeaders';

const REFUND = 'refund-contradiction-flag' as const;

const asRecord = (headers: { name: string; value: string }[]) => Object.fromEntries(headers.map((h) => [h.name, h.value]));

describe('decisionHeaders', () => {
  it('writes a flagged answer\'s headers in the gateway\'s order', () => {
    const summary = s0Trace(REFUND, 'decided');
    const headers = decisionHeaders({ summary, decision: s0Decision(REFUND) });
    expect(headers.map((h) => h.name)).toEqual([
      'X-TrustAgent-Trace-Id',
      'X-Request-ID',
      'X-TrustAgent-Action',
      'X-TrustAgent-Route',
      'X-TrustAgent-Mode',
      'X-TrustAgent-Status',
      'X-TrustAgent-Would-Action',
      'X-TrustAgent-Verdict',
      'X-TrustAgent-Risk',
      'X-TrustAgent-Calibrated',
      'X-TrustAgent-Reasons',
      'X-TrustAgent-Checked-Against',
      'X-TrustAgent-Decision-Model',
      'X-TrustAgent-Gate-Ms',
      'X-TrustAgent-Overhead-Ms',
      'X-TrustAgent-Audit',
      'X-TrustAgent-Source',
    ]);
    expect(asRecord(headers)).toMatchObject({
      'X-TrustAgent-Trace-Id': summary.trace_id,
      'X-Request-ID': summary.trace_id,
      'X-TrustAgent-Action': 'flag',
      'X-TrustAgent-Route': 'support-bot@v1',
      'X-TrustAgent-Mode': 'flag',
      'X-TrustAgent-Status': 'ok',
      'X-TrustAgent-Would-Action': 'block',
      'X-TrustAgent-Verdict': 'contradicted',
      'X-TrustAgent-Risk': '0.98',
      'X-TrustAgent-Calibrated': 'none',
      'X-TrustAgent-Reasons': 'contradiction:critical,groundedness:critical,det:numbers_not_in_evidence',
      'X-TrustAgent-Checked-Against': 'request_system:1',
      'X-TrustAgent-Decision-Model': 'mock/jev',
      'X-TrustAgent-Gate-Ms': '5',
      'X-TrustAgent-Overhead-Ms': '3',
      'X-TrustAgent-Audit': 'queued',
      'X-TrustAgent-Source': 'trafficgen',
    });
  });

  it('sends the delivered action and the decision\'s would-action for a replaced answer', () => {
    const detail = p3Detail('payments-fallback');
    const h = asRecord(decisionHeaders({ summary: detail, decision: detail.decisions[0] }));
    expect(h['X-TrustAgent-Action']).toBe('fallback');
    expect(h['X-TrustAgent-Would-Action']).toBe('block');
    expect(h['X-TrustAgent-Calibrated']).toBe('all');
    expect(h['X-TrustAgent-Route']).toBe('payments-faq@v1');
    // The audit has finished since; the answer said it was queued.
    expect(h['X-TrustAgent-Audit']).toBe('queued');
  });

  it('writes the headers of the check that timed out when its decision arrived late', () => {
    // The trace keeps what was sent: a timeout that failed open on a flag route, flagged by a
    // detector. The late decision upserted the stored row with the model's full verdict.
    const decided = s0Trace(REFUND, 'decided');
    const summary = {
      ...decided,
      action: 'flag' as const,
      would_action: 'flag' as const,
      gate_status: 'timeout' as const,
      verdict: null,
      max_risk: null,
      reasons: ['det:numbers_not_in_evidence'],
      decision_model: { backend: 'mock' as const, snapshot: null, cost_usd: null },
      timings: { ...decided.timings, gate_ms: 1500 },
    };
    const late = { ...s0Decision(REFUND), late: true };
    const h = asRecord(decisionHeaders({ summary, decision: late }));
    expect(h).toMatchObject({
      'X-TrustAgent-Action': 'flag',
      'X-TrustAgent-Mode': 'flag',
      'X-TrustAgent-Status': 'timeout',
      'X-TrustAgent-Calibrated': 'none',
      'X-TrustAgent-Degraded': 'timeout',
      'X-TrustAgent-Reasons': 'det:numbers_not_in_evidence',
      'X-TrustAgent-Checked-Against': 'request_system:1',
      'X-TrustAgent-Gate-Ms': '1500',
    });
    // Nothing of the late result was sent: no verdict, risk, model or would-block.
    for (const name of ['X-TrustAgent-Verdict', 'X-TrustAgent-Risk', 'X-TrustAgent-Decision-Model', 'X-TrustAgent-Would-Action']) {
      expect(h[name], name).toBeUndefined();
    }
    // An on-time decision is written as it is.
    expect(asRecord(decisionHeaders({ summary: decided, decision: s0Decision(REFUND) }))['X-TrustAgent-Verdict']).toBe('contradicted');
  });

  it('keeps only the trace ids and the action with headers: action_only', () => {
    const detail = p3Detail('help-center-link-block');
    const headers = decisionHeaders({ summary: detail, decision: detail.decisions[0], headers: 'action_only' });
    expect(headers).toEqual([
      { name: 'X-TrustAgent-Trace-Id', value: detail.trace_id },
      { name: 'X-Request-ID', value: detail.trace_id },
      { name: 'X-TrustAgent-Action', value: 'block' },
    ]);
  });

  it('answers an observe route before its check: Action observe, Status deferred', () => {
    const summary = { ...s0Trace(REFUND, 'decided'), mode: 'observe' as const, action: 'observe' as const, source: 'proxy' as const };
    const decision = { ...s0Decision(REFUND), mode: 'observe' as const, effective_mode: 'observe' as const, action: 'observe' as const };
    expect(deferredHeaders(summary, decision)).toBe(true);
    expect(decisionHeaders({ summary, decision })).toEqual([
      { name: 'X-TrustAgent-Trace-Id', value: summary.trace_id },
      { name: 'X-Request-ID', value: summary.trace_id },
      { name: 'X-TrustAgent-Action', value: 'observe' },
      { name: 'X-TrustAgent-Route', value: 'support-bot@v1' },
      { name: 'X-TrustAgent-Mode', value: 'observe' },
      { name: 'X-TrustAgent-Status', value: 'deferred' },
    ]);
    // Only a route without deep audits knows its audit answer up front.
    const disabled = decisionHeaders({ summary: { ...summary, audit: { ...summary.audit, status: 'disabled' } }, decision });
    expect(asRecord(disabled)['X-TrustAgent-Audit']).toBe('disabled');
    expect(decisionHeaders({ summary, decision, headers: 'action_only' }).map((h) => h.name)).toEqual([
      'X-TrustAgent-Trace-Id',
      'X-Request-ID',
      'X-TrustAgent-Action',
    ]);
  });

  it('answers with the decision when an observe answer was skipped before its check', () => {
    const summary = { ...s0Trace(REFUND, 'decided'), mode: 'observe' as const, action: 'pass' as const, skip_reason: 'no_text' as const };
    const decision = { ...s0Decision(REFUND), mode: 'observe' as const, status: 'skipped' as const, skip_reason: 'no_text' as const, action: 'pass' as const };
    expect(deferredHeaders(summary, decision)).toBe(false);
    expect(asRecord(decisionHeaders({ summary, decision }))['X-TrustAgent-Status']).toBe('skipped:no_text');
  });

  it('echoes the client request id and caps the reasons at eight', () => {
    const summary = { ...s0Trace(REFUND, 'decided'), client_request_id: 'req_42' };
    const reasons = Array.from({ length: 10 }, (_, i) => `check_${i}:warn`);
    const h = asRecord(decisionHeaders({ summary, decision: { ...s0Decision(REFUND), reasons } }));
    expect(h['X-Request-ID']).toBe('req_42');
    expect(h['X-TrustAgent-Reasons'].split(',')).toHaveLength(8);
  });

  it('formats status, checked-against and audit values as the gateway does', () => {
    expect(statusHeaderValue({ status: 'skipped', skip_reason: 'budget' })).toBe('skipped:budget');
    expect(statusHeaderValue({ status: 'skipped', skip_reason: null })).toBe('skipped');
    expect(statusHeaderValue({ status: 'timeout', skip_reason: null })).toBe('timeout');
    expect(checkedAgainstValue({ evidence_summary: null })).toBe('none');
    expect(auditHeaderValue('completed')).toBe('queued');
    expect(auditHeaderValue('sampled_out')).toBe('sampled_out');
    expect(auditHeaderValue('none')).toBeNull();
  });
});
