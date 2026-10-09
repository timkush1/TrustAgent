// @vitest-environment node
/**
 * Studio's sentences filled from contracts/ui copy.studio: the dry-run result counts fires over the
 * whole window (extrapolated, "≈", when only a sample was tested), and the D82 confirmation says
 * what is left of today's budget, or that there is no daily budget.
 */
import { describe, expect, it } from 'vitest';
import dryRunFixture from '@contracts/fixtures/api/DryRunResult.done.json';
import type { DryRunResult } from '@/types/api';
import { riskLevelsLine, studioDryRunConfirmText, studioDryRunExtrapolated, studioDryRunResultText, studioGroupExamples, studioQuestionParts } from './studioWords';

const done = dryRunFixture as DryRunResult;

describe('studioDryRunResultText', () => {
  it('gives the extrapolated count for the window when only a sample of it was tested', () => {
    expect(studioDryRunExtrapolated(done)).toBe(true);
    expect(studioDryRunResultText(done)).toBe('Would have fired ≈ 108 times in the last 7 days (5.9% of answers, likely between 3.3% and 10%).');
  });

  it('gives the count itself when every answer of the window was tested', () => {
    const all = { ...done, window: '24h' as const, sampled: 120, population: 120, fired: 9, extrapolated_fires: 9, fire_rate: 0.075, fire_rate_ci95: [0.04, 0.137] as [number, number] };
    expect(studioDryRunExtrapolated(all)).toBe(false);
    expect(studioDryRunResultText(all)).toBe('Would have fired 9 times in the last 24 h (7.5% of answers, likely between 4% and 14%).');
  });
});

describe('studioDryRunConfirmText', () => {
  const details = { estimate_usd: 0.0021, samples: 200, population: 1840 };

  it("says what is left of today's fast-check budget", () => {
    expect(studioDryRunConfirmText({ ...details, decision_budget_left_usd: 1.84 })).toBe(
      "This test costs about $0.0021 (200 of 1,840 past answers). $1.84 of today's fast-check budget is left.",
    );
  });

  it('says there is no daily budget when the gateway sends none, instead of a number it does not have', () => {
    expect(studioDryRunConfirmText({ ...details, decision_budget_left_usd: null })).toBe(
      'This test costs about $0.0021 (200 of 1,840 past answers). No daily fast-check budget is set.',
    );
  });
});

describe('studioGroupExamples', () => {
  it('shows the same answer once with its count, in the order the examples came', () => {
    const ex = ['a', 'b', 'c', 'd', 'e', 'f'].map((id) => ({ trace_id: id }));
    const groups = studioGroupExamples(ex, ['Refund in 30 days', 'Refund in 30 days', null, 'Call us', 'Refund in 30 days', '']);
    expect(groups.map((g) => [g.example.trace_id, g.count, g.text])).toEqual([
      ['a', 3, 'Refund in 30 days'],
      // Without its text an example stays a row of its own.
      ['c', 1, null],
      ['d', 1, 'Call us'],
      ['f', 1, null],
    ]);
    expect(studioGroupExamples([], [])).toEqual([]);
  });
});

describe('studioQuestionParts', () => {
  it('names each part of the request in words, and a setting by its own label', () => {
    const parts = studioQuestionParts('Does `assistant_response` name one of `rules.competitors` or `rules.other_brand`, given `user_request`?', {
      competitors: { label: 'Competitor names' },
    });
    expect(parts.map((p) => (p.kind === 'name' ? `[${p.label}]` : p.text)).join('')).toBe(
      'Does [your model’s answer] name one of [Competitor names] or [Other brand], given [the user’s message]?',
    );
    expect(parts.filter((p) => p.kind === 'name').map((p) => (p.kind === 'name' ? p.raw : ''))).toEqual([
      'assistant_response',
      'rules.competitors',
      'rules.other_brand',
      'user_request',
    ]);
    expect(studioQuestionParts('No names here.')).toEqual([{ kind: 'text', text: 'No names here.' }]);
    expect(studioQuestionParts('`evidence` and `system_instructions`', null).map((p) => (p.kind === 'name' ? p.label : p.text))).toEqual([
      'your documents',
      ' and ',
      'your system prompt',
    ]);
  });
});

describe('riskLevelsLine', () => {
  it('names the thresholds the risk words follow, with or without a block level', () => {
    expect(riskLevelsLine({ warn_at: 0.6, critical_at: 0.98 })).toBe('Risk words follow this check’s own thresholds: Elevated risk from 0.60, High risk from 0.98.');
    expect(riskLevelsLine({ warn_at: 0.7, critical_at: null })).toBe('Risk words follow this check’s own thresholds: Elevated risk from 0.70; it has no High risk level.');
  });
});
