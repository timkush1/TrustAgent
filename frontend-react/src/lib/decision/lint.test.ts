// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { lintCodes, lintSpec, NOT_ATTACHED } from './lint';
import { BUILTIN_TEMPLATES } from './templates';

const noul = {
  spec_version: 'evaluator/v1',
  id: 'custom_check',
  version: 1,
  name: 'Custom check',
  category: 'custom',
  state_fields: ['user_request', 'assistant_response'],
  question: {
    type: 'noul',
    instructions: 'Does `assistant_response` promise a delivery date?',
    criteria: { true: '`assistant_response` promises a date', false: 'No date is promised' },
  },
  evidence: { requires: 'none' },
  signal: { invert: false },
  outcomes: { warn_at: 0.6, critical_at: null, block_eligible: false, requires_corroboration: [], implies: [] },
  escalation: { escalate_from: null, numeric_or_date: false, on_error: 'ignore' },
};

const codes = (doc: unknown, options?: Parameters<typeof lintSpec>[1]) => lintCodes(lintSpec(doc, options));

describe('lintSpec', () => {
  it.each(Object.keys(BUILTIN_TEMPLATES))('finds nothing in the built-in %s', (ref) => {
    expect(lintSpec(BUILTIN_TEMPLATES[ref], { registry: Object.values(BUILTIN_TEMPLATES).map((t) => t.id) })).toEqual([]);
  });

  it('reports a spec that does not parse as one error', () => {
    expect(lintSpec({ ...noul, id: 'X' })).toEqual([expect.objectContaining({ code: 'invalid_spec', severity: 'error' })]);
  });

  it('checks risk weights and levels against the question', () => {
    const choice = {
      ...noul,
      question: { type: 'choice', instructions: 'Which fits `assistant_response`?', criteria: { rude_reply: 'Rude', polite_reply: 'Polite' } },
      signal: { risk_weights: { rude_reply: 1, angry_reply: 1 } },
    };
    expect(codes(choice).errors).toEqual(['unknown_risk_weight_option']);
    const score = {
      ...noul,
      question: { type: 'score', instructions: 'How rude is `assistant_response`?', criteria: ['Polite', 'Curt', '3'] },
      signal: { risk_levels: [1, 3] },
    };
    expect(codes(score)).toEqual({ errors: ['risk_level_out_of_range'], lints: ['numeric_only_score_levels', 'score_used_for_policy'] });
  });

  it('checks the threshold order after route overrides', () => {
    const spec = { ...noul, outcomes: { ...noul.outcomes, critical_at: 0.9 }, escalation: { ...noul.escalation, escalate_from: 0.4 } };
    expect(codes(spec).errors).toEqual([]);
    expect(codes(spec, { thresholds: { warn_at: 0.95 } }).errors).toEqual(['threshold_order']);
    expect(codes(spec, { thresholds: { escalate_from: 0.6 } }).errors).toEqual(['threshold_order']);
    expect(codes(spec, { thresholds: { warn_at: 0.95, critical_at: null } }).errors).toEqual([]);
  });

  it('checks corroborator and implied references against the registry and the route', () => {
    const spec = { ...noul, outcomes: { ...noul.outcomes, requires_corroboration: ['groundedness', 'det:numbers_not_in_evidence', 'det:vibes'], implies: ['ghost'] } };
    expect(codes(spec).errors).toEqual(['unknown_detector']);
    expect(codes(spec, { registry: ['groundedness'] }).errors).toEqual(['unknown_detector', 'unknown_evaluator_reference']);
    const onRoute = lintSpec({ ...spec, outcomes: { ...spec.outcomes, implies: [] } }, { registry: ['groundedness'], attached: [] });
    expect(onRoute.filter((l) => l.code === NOT_ATTACHED)).toHaveLength(1);
  });

  it('flags non-descriptive option keys', () => {
    const spec = {
      ...noul,
      question: { type: 'choice', instructions: 'Which fits `assistant_response`?', criteria: { option_a: 'Rude', choice_2: 'Polite', no_reply: 'Empty' } },
      signal: { risk_weights: { option_a: 1 } },
    };
    const found = lintSpec(spec).filter((l) => l.code === 'non_descriptive_key');
    expect(found.map((l) => l.path)).toEqual(['question.criteria.option_a', 'question.criteria.choice_2']);
  });

  it('reads instructions given as a text tree', () => {
    const spec = { ...noul, question: { ...noul.question, instructions: ['Does `assistant_response` promise a date?', { note: 'Answer how many dates.' }] } };
    expect(codes(spec).lints).toEqual(['counting_or_arithmetic']);
    const unresolved = { ...noul, question: { ...noul.question, criteria: { true: ['`rules.tone` is ignored'], false: 'ok' } } };
    expect(codes(unresolved).errors).toEqual(['unresolved_state_reference']);
  });

  it('compares at most the first 255 options for near-duplicates (bounded work, like lint.go)', () => {
    // 600 options described the same way: every pair is a near-duplicate, but only the pairs among
    // the first 255 (the schema's limit) are compared and reported.
    const criteria = Object.fromEntries(Array.from({ length: 600 }, (_, i) => [`same_reply_${i}`, 'The same description']));
    const spec = { ...noul, question: { type: 'choice', instructions: 'Which fits `assistant_response`?', criteria }, signal: { risk_weights: { same_reply_0: 1 } } };
    const found = lintSpec(spec).filter((l) => l.code === 'near_duplicate_options');
    expect(found).toHaveLength((255 * 254) / 2);
    expect(found.at(-1)?.message).toBe('same_reply_253 and same_reply_254 are described almost the same way; add not_for to separate them');
  });
});
