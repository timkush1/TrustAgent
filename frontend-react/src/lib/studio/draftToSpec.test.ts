// @vitest-environment node
import { describe, expect, it } from 'vitest';
import draftFixture from '@contracts/fixtures/api/StudioDraftResponse.ok.json';
import { checkSpecDocument } from '@/lib/evaluator/checkSpec';
import type { EvaluatorDraft } from '@/types/api';
import { draftSamples, draftToSpec, EVALUATOR_ID_PATTERN, evaluatorIdFrom } from './draftToSpec';

const draft = draftFixture.draft as unknown as EvaluatorDraft;

describe('draftToSpec', () => {
  it('drops the draft-only members, adds id and version 1, and keeps the rest in order', () => {
    const spec = draftToSpec(draft);
    expect(Object.keys(spec)).toEqual([
      'spec_version',
      'id',
      'version',
      'name',
      'category',
      'state_fields',
      'question',
      'evidence',
      'signal',
      'outcomes',
      'escalation',
    ]);
    expect(spec.id).toBe('blames_the_customer');
    expect(spec.version).toBe(1);
    expect(spec.question).toEqual(draft.question);
    expect(checkSpecDocument(spec)).toEqual({ errors: [], lints: [] });
  });

  it('copies, so editing the spec leaves the draft as it was', () => {
    const spec = draftToSpec(draft, { id: 'my_check' });
    expect(spec.id).toBe('my_check');
    spec.outcomes.warn_at = 0.1;
    expect(draft.outcomes.warn_at).toBe(0.6);
  });

  it('labels the six samples as drafted', () => {
    const samples = draftSamples(draft);
    expect(samples).toHaveLength(6);
    expect(samples.map((s) => s.id)).toEqual(['d1', 'd2', 'd3', 'd4', 'd5', 'd6']);
    expect(samples.map((s) => s.expected)).toEqual(draft.test_samples.map((s) => (s.should_fire ? 'fire' : 'no_fire')));
    expect(samples[0]).toEqual({ id: 'd1', user_request: 'My order never arrived.', assistant_response: draft.test_samples[0].assistant_response, expected: 'no_fire' });
    const withEvidence = draftSamples({ ...draft, test_samples: [{ assistant_response: 'A', evidence: ['Doc one'], should_fire: true }] } as EvaluatorDraft);
    expect(withEvidence[0].evidence).toEqual([{ id: 'd1-e1', text: 'Doc one' }]);
  });
});

describe('evaluatorIdFrom', () => {
  it.each([
    ['Blames the customer', 'blames_the_customer'],
    ['  Refund promises (EU) ', 'refund_promises_eu'],
    ['Café crème', 'cafe_creme'],
    ['42 checks', 'check_42_checks'],
    ['!!', 'check'],
    ['x', 'custom_check'],
  ])('%s → %s', (name, id) => {
    expect(evaluatorIdFrom(name)).toBe(id);
    expect(id).toMatch(EVALUATOR_ID_PATTERN);
  });

  it('stays within 48 characters and avoids taken ids', () => {
    const long = evaluatorIdFrom('a'.repeat(80));
    expect(long).toHaveLength(48);
    expect(evaluatorIdFrom('Blames the customer', ['blames_the_customer', 'blames_the_customer_2'])).toBe('blames_the_customer_3');
    const suffixed = evaluatorIdFrom('a'.repeat(80), [long]);
    expect(suffixed).toMatch(/_2$/);
    expect(suffixed.length).toBeLessThanOrEqual(48);
  });
});
