// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { applyCalibration, checkCalibrationTable } from './calibration';
import { normalize } from './normalize';
import { cloneJSON, parseRef, parseSpec, questionBody, questionId, SpecError, specHashes, specOptions, specRef } from './spec';
import { BUILTIN_TEMPLATES, builtinSpec } from './templates';

const choiceSpec = {
  spec_version: 'evaluator/v1',
  id: 'tone_check',
  version: 2,
  name: 'Tone',
  category: 'custom',
  state_fields: ['assistant_response'],
  question: {
    type: 'choice',
    instructions: 'How does `assistant_response` sound?',
    criteria: { polite_reply: { what: 'Polite' }, rude_reply: { what: 'Rude' }, too_short_to_judge: null },
  },
  evidence: { requires: 'none' },
  signal: { risk_weights: { rude_reply: 1 } },
  outcomes: { warn_at: 0.6 },
};

describe('parseSpec', () => {
  it('applies the schema defaults to the typed view and keeps the spec as written', () => {
    const s = parseSpec(choiceSpec);
    expect(specRef(s)).toBe('tone_check@2');
    expect(questionId(s)).toBe('tone_check__v2');
    expect(s.evidence.minChars).toBe(40);
    expect(s.escalation).toEqual({ escalateFrom: null, numericOrDate: false, onError: 'escalate' });
    expect(s.outcomes).toMatchObject({ criticalAt: null, minConfidence: null, blockEligible: false, requiresCorroboration: [], implies: [] });
    expect(s.verdictRole).toBeNull();
    expect(questionBody(s)).toEqual(choiceSpec.question);
    expect(s.raw).not.toBe(choiceSpec);
    expect(specOptions(s)).toEqual(['polite_reply', 'rude_reply', 'too_short_to_judge']);
  });

  it('hashes the spec as written, not the defaults', () => {
    const withDefault = parseSpec({ ...choiceSpec, evidence: { requires: 'none', min_chars: 40 } });
    const a = specHashes(parseSpec(choiceSpec));
    const b = specHashes(withDefault);
    expect(a.spec_sha256).not.toBe(b.spec_sha256);
    expect(a.question_sha256).toBe(b.question_sha256);
    expect(a.calibration_key).toBe(b.calibration_key);
  });

  it.each([
    [{ ...choiceSpec, id: 'Bad' }, /invalid id/],
    [{ ...choiceSpec, version: 0 }, /version/],
    [{ ...choiceSpec, version: 1.5 }, /version/],
    [{ ...choiceSpec, outcomes: {} }, /warn_at is required/],
    [{ ...choiceSpec, question: { ...choiceSpec.question, type: 'rank' } }, /unknown question type/],
    [{ ...choiceSpec, question: { ...choiceSpec.question, criteria: [] } }, /criteria must be an object/],
    [{ ...choiceSpec, evidence: { requires: 'sometimes' } }, /evidence.requires/],
    [{ ...choiceSpec, escalation: { on_error: 'panic' } }, /on_error/],
    [{ ...choiceSpec, params: { tone: { default: 3 } } }, /default must be a string/],
    ['not a spec', /not an object/],
  ])('rejects %#', (doc, message) => {
    expect(() => parseSpec(doc)).toThrow(SpecError);
    expect(() => parseSpec(doc)).toThrow(message);
  });

  it('parses every built-in template', () => {
    expect(Object.keys(BUILTIN_TEMPLATES).sort()).toEqual([
      'commitments@1',
      'contradiction@1',
      'factual_claims@1',
      'groundedness@1',
      'injection_success@1',
      'off_brand_tone@1',
      'off_topic@1',
      'pii_secret_leak@1',
      'refusal@1',
      'regulated_advice@1',
      'unsafe_policy@1',
    ]);
    for (const ref of Object.keys(BUILTIN_TEMPLATES)) expect(specRef(builtinSpec(ref)!)).toBe(ref);
    expect(builtinSpec('groundedness@2')).toBeUndefined();
    expect(builtinSpec('groundedness@1')?.verdictRole).toBe('unsupported');
  });

  it('parses refs', () => {
    expect(parseRef('groundedness@12')).toEqual({ id: 'groundedness', version: 12 });
    for (const bad of ['groundedness', 'groundedness@latest', 'groundedness@01', 'groundedness@0', 'G@1']) {
      expect(() => parseRef(bad)).toThrow(SpecError);
    }
  });

  it('copies JSON with Go decoder semantics', () => {
    expect(cloneJSON({ a: undefined, [String.fromCharCode(0xd800)]: 'x' })).toEqual({ '�': 'x' });
    expect(() => cloneJSON({ a: Number.NaN })).toThrow(SpecError);
  });
});

describe('normalize', () => {
  const spec = parseSpec(choiceSpec);
  it('treats absent and null members alike', () => {
    expect(normalize(null, spec).error).toBe('missing_answer');
    expect(normalize({ type: 'choice', probabilities: null, choice: null }, spec).error).toBe('missing_answer');
    // A null probability decodes as 0, as Go's decoder does.
    const n = normalize(JSON.parse('{"type":"choice","probabilities":{"polite_reply":0.2,"rude_reply":0.8,"too_short_to_judge":null}}'), spec);
    expect(n).toMatchObject({ error: null, top: 'rude_reply', confidenceSource: 'fallback_top_prob' });
    expect(n.riskRaw).toBeCloseTo(0.8, 12);
  });

  it('never reads inherited option names', () => {
    expect(normalize({ type: 'choice', probabilities: { constructor: 0.5, polite_reply: 0.5 } }, spec).error).toBe('unexpected_option');
  });
});

describe('calibration tables', () => {
  const identity = Array.from({ length: 101 }, (_, i) => i / 100);
  it('interpolates and clamps', () => {
    expect(applyCalibration(identity, 0.425)).toBeCloseTo(0.425, 12);
    expect(applyCalibration(identity, 1.5)).toBe(1);
    expect(applyCalibration(identity, -1)).toBe(0);
  });

  it.each([
    [identity.slice(1), /101/],
    [[...identity.slice(0, 100), 0.5], /decreases at point 100/],
    [[...identity.slice(0, 100), 1.5], /point 100/],
  ])('rejects a bad table %#', (table, message) => {
    expect(() => checkCalibrationTable(table)).toThrow(message);
  });
});
