// @vitest-environment node
/**
 * End-to-end use of the decision core as the static demo runs it (extract → compile → detectors →
 * decide), plus decide()'s structural checks. Behaviour is pinned by the decide vectors; these tests
 * cover the API seams around them.
 */
import { describe, expect, it } from 'vitest';
import type { DecideInput } from '@/lib/contracts/types.gen';
import { compile, decideEvaluators } from './compile';
import { decide, DecideError } from './decide';
import { detect, detectorStates } from './detectors';
import { decisionItems, extractEvidence } from './evidence';
import { type Spec } from './spec';
import { BUILTIN_TEMPLATES, builtinSpec } from './templates';

const specs = new Map<string, Spec>(Object.keys(BUILTIN_TEMPLATES).map((ref) => [ref, builtinSpec(ref)!]));

const body = {
  model: 'gpt-4o-mini',
  messages: [
    {
      role: 'system',
      content:
        'You are the Acme support assistant.\n\n<document id="refund-policy.md">Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.</document>',
    },
    { role: 'user', content: 'Can I still get a refund on my annual plan? I bought it 45 days ago.' },
  ],
};
const answer = 'You can get a full refund within 60 days of purchase.';

/** The support-bot flow: evidence, compile, mock answers, detectors, decide. */
function gate(mode: 'observe' | 'flag' | 'block', answers: DecideInput['answers'], overrides: Partial<DecideInput['meta']> = {}) {
  const ex = extractEvidence('openai_chat', body);
  const route = {
    evaluators: [{ ref: 'groundedness@1' }, { ref: 'contradiction@1' }],
    on_no_evidence: 'ignore' as const,
    evidence_sources: ['request_system', 'request_context', 'tool_results', 'extension'],
    client_rules: false,
    model: 'mock/jev-heuristic-v1',
  };
  const items = decisionItems(ex.items);
  const compiled = compile(route, specs, {
    user_request: ex.user_request,
    assistant_response: answer,
    system_instructions: ex.system_instructions,
    evidence: items,
  });
  const detectors = detect({
    response: answer,
    evidence: items,
    userRequest: ex.user_request,
    systemInstructions: ex.system_instructions,
    preDedupItems: ex.pre_dedup_items,
    maxMarker: ex.max_marker,
  });
  const input: DecideInput = {
    policy: {
      configured_mode: mode,
      fail: 'open',
      require_calibration: true,
      on_no_evidence: 'ignore',
      detectors: { flag_on: ['urls_not_in_evidence', 'quotes_not_in_evidence', 'citation_out_of_range', 'secret_pattern', 'system_prompt_leak'], block_on: [] },
    },
    evaluators: decideEvaluators(compiled, specs),
    answers,
    question_map: compiled.question_map,
    excluded: compiled.excluded,
    calibration: {},
    meta: {
      backend: 'mock',
      stream: 'none',
      evidence_present: items.length > 0,
      evidence_truncated: compiled.truncated,
      knowledge_only: false,
      skip_reason: null,
      request_status: Object.fromEntries(compiled.requests.map((_, k) => [String(k), 'ok'])),
      qid_request: compiled.qid_request,
      question_errors: {},
      ...overrides,
    },
    detectors: detectorStates(detectors),
  };
  return { compiled, detectors, input, decision: decide(input) };
}

const contradicted = { groundedness__v1: { type: 'noul' as const, noul: 0.96 }, contradiction__v1: { type: 'noul' as const, noul: 0.98 } };

describe('the decision core end to end', () => {
  it('flags the refund contradiction like the S0 scenario', () => {
    const { detectors, decision } = gate('flag', contradicted);
    expect(detectors.find((d) => d.id === 'numbers_not_in_evidence')).toMatchObject({ fired: true, findings: [{ text: '60', start: 33, end: 35 }] });
    expect(decision).toMatchObject({
      status: 'ok',
      action: 'flag',
      would_action: 'block',
      verdict: 'contradicted',
      max_risk: 0.98,
      deep_audit: 'high',
      degraded: null,
      reasons: ['contradiction:critical', 'groundedness:critical', 'det:numbers_not_in_evidence'],
    });
    expect(decision.evaluators['groundedness@1']).toMatchObject({ level: 'critical', corroborated: true, escalate: 'numeric_or_date_unverified' });
  });

  it('caps an uncalibrated block to flag and records would_action in observe', () => {
    expect(gate('block', contradicted).decision).toMatchObject({ action: 'flag', degraded: 'uncalibrated' });
    // No calibration entry at all: the calibration is missing, not a snapshot change (D56).
    expect(gate('block', contradicted).decision.reasons.at(-1)).toBe('calibration_missing');
    expect(gate('observe', contradicted).decision).toMatchObject({ action: 'observe', would_action: 'block', enforced: false });
  });

  it('passes a grounded answer', () => {
    const { decision } = gate('flag', { groundedness__v1: { type: 'noul', noul: 0.05 }, contradiction__v1: { type: 'noul', noul: 0.02 } });
    // The unverified number escalates groundedness to the deep audit without flagging.
    expect(decision).toMatchObject({ action: 'pass', would_action: 'pass', verdict: 'supported', deep_audit: 'high', reasons: [] });
  });

  it('marks a timed-out request as degraded without blocking (fail open)', () => {
    const { decision } = gate('block', null, { request_status: { '0': 'timeout' } });
    expect(decision).toMatchObject({ status: 'timeout', action: 'pass', degraded: 'timeout', deep_audit: 'sample_error', verdict: 'unknown' });
    expect(decision.evaluators['contradiction@1']).toMatchObject({ status: 'error', reason: 'timeout', escalate: 'error' });
  });

  it('keeps an explicit null threshold override distinct from an absent one', () => {
    const { input } = gate('flag', contradicted);
    const withNull = structuredClone(input);
    withNull.evaluators[0].thresholds.critical_at = null;
    const d = decide(withNull);
    expect(d.evaluators['groundedness@1']).toMatchObject({ level: 'warn', thresholds: { critical_at: null, warn_at: 0.8, escalate_from: 0.4 } });
    const absent = decide(input);
    expect(absent.evaluators['groundedness@1'].thresholds.critical_at).toBe(0.95);
  });

  it('records a null answer probability as 0, as the Go reference decodes it', () => {
    const answers = {
      groundedness__v1: { type: 'noul' as const, noul: 0.05 },
      contradiction__v1: { type: 'noul' as const, noul: 0.02 },
    };
    const { input } = gate('flag', answers);
    const refusal = builtinSpec('refusal@1')!;
    const withChoice: DecideInput = {
      ...input,
      evaluators: [...input.evaluators, { ref: 'refusal@1', spec: refusal.raw, stage: 'active', internal: false, thresholds: { space: 'raw' } }],
      answers: {
        ...answers,
        refusal__v1: {
          type: 'choice',
          choice: 'answers_request',
          probabilities: { answers_request: 0.9, declines_or_deflects: null as unknown as number, nothing_to_answer: 0.1 },
          legend: { x: null },
        },
      },
      question_map: { ...input.question_map, refusal__v1: 'refusal@1' },
      meta: { ...input.meta, qid_request: { ...input.meta.qid_request, refusal__v1: 0 } },
    };
    const r = decide(withChoice).evaluators['refusal@1'];
    expect(r.answer).toEqual({
      type: 'choice',
      choice: 'answers_request',
      probabilities: { answers_request: 0.9, declines_or_deflects: 0, nothing_to_answer: 0.1 },
      legend: { x: null },
    });
    expect(r).toMatchObject({ status: 'assessed', level: 'ok', risk_raw: 0 });
  });

  it.each([
    ['configured_mode', (i: DecideInput) => ({ ...i, policy: { ...i.policy, configured_mode: 'enforce' as never } }), /configured_mode/],
    ['fail', (i: DecideInput) => ({ ...i, policy: { ...i.policy, fail: 'shut' as never } }), /fail mode/],
    ['on_no_evidence', (i: DecideInput) => ({ ...i, policy: { ...i.policy, on_no_evidence: 'warn' as never } }), /on_no_evidence/],
    ['duplicate', (i: DecideInput) => ({ ...i, evaluators: [i.evaluators[0], i.evaluators[0]] }), /duplicate evaluator/],
    ['stage', (i: DecideInput) => ({ ...i, evaluators: [{ ...i.evaluators[0], stage: 'canary' as never }] }), /unknown stage/],
    ['space', (i: DecideInput) => ({ ...i, evaluators: [{ ...i.evaluators[0], thresholds: { space: 'log' as never } }] }), /threshold space/],
    ['table', (i: DecideInput) => ({ ...i, calibration: { 'groundedness@1': { table: [0, 1], quality: 'measured' as const, state: 'measured' as const } } }), /101/],
  ])('rejects a structurally invalid input: %s', (_name, mutate, message) => {
    const { input } = gate('flag', contradicted);
    expect(() => decide(mutate(input))).toThrow(DecideError);
    expect(() => decide(mutate(input))).toThrow(message);
  });
});

describe('calibration signals (P4-PLAN D56)', () => {
  const measured = Array.from({ length: 101 }, (_, i) => Math.min(1, (i + 2) / 100));
  const otherSnapshot = { state: 'other_snapshot' as const };

  it('says uncalibrated_snapshot only when every uncalibrated block-set member is calibrated on another snapshot', () => {
    const { input } = gate('block', contradicted);
    const moved = decide({ ...input, calibration: { 'groundedness@1': otherSnapshot, 'contradiction@1': otherSnapshot } });
    expect(moved).toMatchObject({ action: 'flag', degraded: 'uncalibrated' });
    expect(moved.reasons.at(-1)).toBe('uncalibrated_snapshot');
    const mixed = decide({ ...input, calibration: { 'groundedness@1': otherSnapshot } });
    expect(mixed.reasons.at(-1)).toBe('calibration_missing');
    expect(mixed.reasons).not.toContain('uncalibrated_snapshot');
  });

  it('never reports other_snapshot as a calibration_state', () => {
    const { input } = gate('block', contradicted);
    const inputs: DecideInput['calibration'][] = [{}, { 'groundedness@1': otherSnapshot }, { 'groundedness@1': otherSnapshot, 'contradiction@1': otherSnapshot }];
    for (const calibration of inputs) {
      const d = decide({ ...input, calibration });
      for (const r of Object.values(d.evaluators)) {
        expect(r.calibration_state).toBe('none');
        expect(r.calibrated).toBe(false);
      }
    }
  });

  it('warns when a calibration was fitted with other state fields, and still applies it', () => {
    const { input } = gate('block', contradicted);
    const calibrated = {
      ...input,
      evaluators: input.evaluators.map((e) => ({ ...e, thresholds: { space: 'calibrated' as const } })),
      calibration: {
        'groundedness@1': { table: measured, quality: 'measured' as const, state: 'measured' as const, fit_state_fields: ['assistant_response', 'evidence', 'user_request'] },
        'contradiction@1': { table: measured, quality: 'measured' as const, state: 'measured' as const, fit_state_fields: ['evidence', 'assistant_response'] },
      },
    };
    const d = decide(calibrated);
    expect(d.evaluators['groundedness@1']).toMatchObject({ calibrated: true, calibration_warning: 'calibrated with different state fields' });
    expect(d.evaluators['contradiction@1']).toMatchObject({ calibrated: true, calibration_warning: null });
    expect(d).toMatchObject({ action: 'block', calibration: 'all' });
    expect(d.reasons).not.toContain('calibration_missing');
  });

  it('rejects an other_snapshot entry that carries a table', () => {
    const { input } = gate('block', contradicted);
    expect(() => decide({ ...input, calibration: { 'groundedness@1': { state: 'other_snapshot', table: measured } as never } })).toThrow(DecideError);
  });
});
