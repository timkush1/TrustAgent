// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { compile, CompileError, defaultBudget, NONE_PROVIDED, type CompileInputs, type CompileRoute } from './compile';
import { sha256Hex } from './jcs';
import { parseSpec, type Spec } from './spec';
import { BUILTIN_TEMPLATES, builtinSpec } from './templates';

const specs = new Map<string, Spec>(Object.keys(BUILTIN_TEMPLATES).map((ref) => [ref, builtinSpec(ref)!]));

const route = (refs: string[], over: Partial<CompileRoute> = {}): CompileRoute => ({
  evaluators: refs.map((ref) => ({ ref })),
  on_no_evidence: 'ignore',
  evidence_sources: ['request_system', 'request_context', 'tool_results', 'extension'],
  client_rules: false,
  model: 'typesafe/jev-1.13',
  ...over,
});

const refund: CompileInputs = {
  user_request: 'Can I still get a refund on my annual plan? I bought it 45 days ago.',
  assistant_response: 'You can get a full refund within 60 days of purchase.',
  system_instructions: 'You are the Acme support assistant.',
  evidence: [
    {
      id: 'refund-policy.md',
      source: 'request_system',
      text: 'Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.',
    },
  ],
};

describe('compile', () => {
  it('builds one request with the union of state fields and hashes it', () => {
    const c = compile(route(['groundedness@1', 'contradiction@1', 'refusal@1']), specs, refund);
    expect(c.requests).toHaveLength(1);
    const [req] = c.requests;
    expect(Object.keys(req.state).sort()).toEqual(['assistant_response', 'evidence', 'user_request']);
    expect(req.state.evidence).toEqual([{ id: 'refund-policy.md', source: 'request_system', text: refund.evidence[0].text }]);
    expect(Object.keys(req.questions).sort()).toEqual(['contradiction__v1', 'groundedness__v1', 'refusal__v1']);
    expect(c.question_map).toEqual({ groundedness__v1: 'groundedness@1', contradiction__v1: 'contradiction@1', refusal__v1: 'refusal@1' });
    expect(c.request_sha256[0]).toBe(sha256Hex(req));
    expect(c.packed_ids).toEqual(['refund-policy.md']);
    expect(c.excluded).toEqual([]);
    expect(c.est_tokens[0]).toBeGreaterThan(275);
  });

  it('never sends evidence-requiring evaluators without evidence and adds factual_claims when asked', () => {
    const c = compile(route(['groundedness@1', 'refusal@1'], { on_no_evidence: 'flag' }), specs, { ...refund, evidence: [] });
    expect(c.excluded).toEqual([{ ref: 'groundedness@1', reason: 'no_evidence' }]);
    expect(c.evaluators.at(-1)).toEqual({ ref: 'factual_claims@1', stage: 'active', internal: true });
    expect(Object.keys(c.requests[0].questions).sort()).toEqual(['factual_claims__v1', 'refusal__v1']);
    expect(c.requests[0].state).not.toHaveProperty('evidence');
  });

  it('sends "(none provided)" to optional evaluators and honours client rules only when allowed', () => {
    const inputs = { ...refund, evidence: [], rules: { allowed_scope: 'Billing only' } };
    const off = compile(route(['pii_secret_leak@1', 'off_topic@1']), specs, inputs);
    expect(off.requests[0].state.evidence).toBe(NONE_PROVIDED);
    expect(off.requests[0].state.rules).toEqual({ allowed_scope: 'Any subject' });
    const on = compile(route(['off_topic@1'], { client_rules: true }), specs, inputs);
    expect(on.requests[0].state.rules).toEqual({ allowed_scope: 'Billing only' });
    const routeParam = compile(
      { ...route([]), evaluators: [{ ref: 'off_topic@1', params: { allowed_scope: '' } }] },
      specs,
      inputs,
    );
    expect(routeParam.requests[0].state.rules).toEqual({ allowed_scope: NONE_PROVIDED });
  });

  it('skips disabled evaluators and keeps shadow ones', () => {
    const c = compile(
      { ...route([]), evaluators: [{ ref: 'refusal@1', enabled: false }, { ref: 'off_topic@1', stage: 'shadow' }] },
      specs,
      refund,
    );
    expect(c.evaluators).toEqual([{ ref: 'off_topic@1', stage: 'shadow', internal: false }]);
  });

  it('excludes everything when the response is too long', () => {
    const c = compile(route(['refusal@1']), specs, refund, { ...defaultBudget(), max_response_tokens: 5 });
    expect(c.excluded).toEqual([{ ref: 'refusal@1', reason: 'too_long' }]);
    expect(c.requests).toEqual([]);
    expect(c.question_set_sha256).toBe(sha256Hex([]));
  });

  it('splits by question count', () => {
    const refs = ['refusal@1', 'off_topic@1', 'unsafe_policy@1', 'regulated_advice@1'];
    const c = compile(route(refs), specs, refund, { ...defaultBudget(), max_questions: 3 });
    expect(c.requests).toHaveLength(2);
    expect(new Set(Object.values(c.qid_request))).toEqual(new Set([0, 1]));
    expect(c.request_sha256[0]).not.toBe(c.request_sha256[1]);
  });

  it('fails when one question cannot fit with the state', () => {
    const huge = { ...refund, assistant_response: 'word '.repeat(30000) };
    expect(() => compile(route(['refusal@1']), specs, huge, { ...defaultBudget(), max_response_tokens: 100000 })).toThrow(/does not fit/);
  });

  it.each([
    [route(['nope@1']), /no spec for evaluator nope@1/],
    [route(['refusal@1', 'refusal@1']), /attached twice/],
    [route(['refusal@1'], { model: '' }), /decision model/],
    [route(['refusal@1'], { on_no_evidence: 'maybe' as never }), /on_no_evidence/],
    [{ ...route([]), evaluators: [{ ref: 'refusal@1', stage: 'canary' as never }] }, /unknown stage/],
  ])('rejects an unusable route %#', (r, message) => {
    expect(() => compile(r, specs, refund)).toThrow(CompileError);
    expect(() => compile(r, specs, refund)).toThrow(message);
  });

  it('rejects an invalid budget and numbers in the payload', () => {
    expect(() => compile(route(['refusal@1']), specs, refund, { ...defaultBudget(), max_questions: 0 })).toThrow(/invalid budget/);
    expect(() => compile(route(['refusal@1']), specs, refund, { ...defaultBudget(), cpt: 0 })).toThrow(/cpt/);
    const numeric = parseSpec({ ...BUILTIN_TEMPLATES['refusal@1'], id: 'numeric', question: { type: 'noul', instructions: 'x', criteria: { true: 1, false: 'no' } }, signal: {} });
    const withNumeric = new Map(specs).set('numeric@1', numeric);
    expect(() => compile(route(['numeric@1']), withNumeric, refund)).toThrow(/only strings, arrays and objects/);
  });
});
