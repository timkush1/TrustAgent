// @vitest-environment node
/**
 * The pieces of the static test bench (D107, D135): CompileUnsaved, the one-evaluator route, a
 * sample as a client request, and the D150 pricing of browser calls (vendor or estimate, never a
 * silent $0 for tokens nobody reported).
 */
import { describe, expect, it } from 'vitest';
import { CompileError } from '@/lib/decision/compile';
import { extractEvidence } from '@/lib/decision/evidence';
import type { PolicyInput } from '@/lib/gate/types';
import { combinedCost, compileUnsaved, failureReason, liveCallCost, sampleRequest, STUDIO_ROUTE, studioTestRoute } from './staticTest';
import { blankSpec } from './templates';

describe('compileUnsaved', () => {
  it('sets version to latest + 1 and keeps every member in its place', () => {
    const doc = blankSpec([]);
    const u = compileUnsaved(doc, 3);
    expect(Object.keys(u.doc)).toEqual(Object.keys(doc));
    expect(u.doc.version).toBe(4);
    expect(u.ref).toBe(`${doc.id}@4`);
    expect(u.spec.version).toBe(4);
    expect(doc.version).toBe(1);
    expect(() => compileUnsaved(doc, -1)).toThrow(RangeError);
  });
});

describe('studioTestRoute', () => {
  const policy: PolicyInput = {
    version: 1,
    defaults: { mode: 'block', on_no_evidence: 'flag', require_calibration: true, detectors: { min_integer: 99 }, evidence: { sources: ['request_system'] }, evaluators: [{ ref: 'refusal@1' }] },
    overrides: { client_rules: false },
    routes: [{ name: 'other' }],
  };

  it('keeps the defaults but runs only the tested evaluator, observing, with every evidence source and client rules', () => {
    const route = studioTestRoute(policy, 'mine@1', { warn_at: 0.5 });
    expect(route).toMatchObject({ name: STUDIO_ROUTE, mode: 'observe', onNoEvidence: 'ignore', requireCalibration: false, clientRules: true });
    expect(route.evaluators).toEqual([{ ref: 'mine@1', thresholds: { warn_at: 0.5 } }]);
    expect(route.detectors.min_integer).toBe(99);
    expect(route.evidence.sources).toEqual(['request_system', 'request_context', 'tool_results', 'extension']);
    expect(studioTestRoute(policy, 'mine@1').evaluators).toEqual([{ ref: 'mine@1' }]);
  });
});

describe('sampleRequest', () => {
  it('carries the texts, the documents as trustagent.context and the rules', () => {
    const body = sampleRequest({
      id: 's',
      system_instructions: 'Be brief.',
      user_request: 'Refund?',
      assistant_response: 'Yes, 30 days.',
      evidence: [{ id: 'kb1', text: 'Refunds within 30 days.', source: 'request_system' }, { text: '  ' }],
      rules: { competitors: 'Globex' },
    });
    const x = extractEvidence('openai_chat', body);
    expect(x.system_instructions).toBe('Be brief.');
    expect(x.user_request).toBe('Refund?');
    expect(x.items.map((i) => i.text)).toEqual(['Refunds within 30 days.']);
    expect((body.trustagent as { rules: unknown }).rules).toEqual({ competitors: 'Globex' });
    expect(sampleRequest({ id: 'bare', assistant_response: 'Hi' })).toEqual({ model: 'studio-test', messages: [] });
  });
});

describe('pricing browser calls (D150)', () => {
  it('takes the vendor figure, else tokens at the table price', () => {
    expect(liveCallCost({ input_tokens: 500, cost: 0.001 }, 'typesafe/jev-1.13')).toMatchObject({ cost_usd: 0.001, cost_source: 'reported' });
    expect(liveCallCost({ input_tokens: 1000, cost: null }, 'typesafe/jev-1.13')).toMatchObject({ cost_usd: 0.000042, cost_source: 'computed' });
    expect(liveCallCost({ input_tokens: 1000, output_tokens: 200, cost: null }, 'anthropic/claude-haiku-4.5')).toMatchObject({ cost_usd: 0.002, cost_source: 'computed' });
  });

  it('rounds a fractional token count up instead of throwing', () => {
    expect(liveCallCost({ input_tokens: 999.2, cost: null }, 'typesafe/jev-1.13').cost_usd).toBe(0.000042);
  });

  it('leaves a call unknown when its output has a price and no count, and an unpriced model unknown', () => {
    expect(liveCallCost({ input_tokens: 1000, cost: null }, 'anthropic/claude-haiku-4.5')).toMatchObject({ cost_usd: null, cost_source: 'unknown' });
    expect(liveCallCost({ input_tokens: 1000, output_tokens: 1, cost: null }, 'someone/else')).toMatchObject({ cost_source: 'unknown' });
  });

  it('combines: reported only when every call was, unknown when any was', () => {
    const reported = { cost_usd: 0.001, cost_source: 'reported' as const, price_table_version: 'v' };
    const computed = { cost_usd: 0.002, cost_source: 'computed' as const, price_table_version: 'v' };
    const unknown = { cost_usd: null, cost_source: 'unknown' as const, price_table_version: 'v' };
    expect(combinedCost([reported, reported])).toEqual({ cost_usd: 0.002, cost_source: 'reported' });
    expect(combinedCost([reported, computed]).cost_source).toBe('computed');
    expect(combinedCost([reported, unknown])).toEqual({ cost_usd: 0.001, cost_source: 'unknown' });
    expect(combinedCost([])).toEqual({ cost_usd: 0, cost_source: 'computed' });
  });
});

describe('failureReason', () => {
  it('names the stage or the transport code', () => {
    expect(failureReason(new CompileError('question does not fit in a request with the state: q needs 99999 tokens'))).toBe('error:too_large');
    expect(failureReason(new CompileError('no spec for evaluator x@1'))).toBe('error:compile');
    expect(failureReason({ code: 'timeout' })).toBe('error:timeout');
    expect(failureReason(new Error('boom'))).toBe('error:internal');
  });
});
