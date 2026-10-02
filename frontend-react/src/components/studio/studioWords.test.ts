// @vitest-environment node
/**
 * Studio's sentences filled from contracts/ui copy.studio: the dry-run result counts fires over the
 * whole window (extrapolated, "≈", when only a sample was tested), and the D82 confirmation says
 * what is left of today's budget, or that there is no daily budget.
 */
import { describe, expect, it } from 'vitest';
import dryRunFixture from '@contracts/fixtures/api/DryRunResult.done.json';
import type { DryRunResult } from '@/types/api';
import { studioDryRunConfirmText, studioDryRunExtrapolated, studioDryRunResultText } from './studioWords';

const done = dryRunFixture as DryRunResult;

describe('studioDryRunResultText', () => {
  it('gives the extrapolated count for the window when only a sample of it was tested', () => {
    expect(studioDryRunExtrapolated(done)).toBe(true);
    expect(studioDryRunResultText(done)).toBe('Would have fired ≈ 108 times (5.9%, 95% CI 3.3%–10.2%) on the last 7 days; this test cost $0.00082.');
  });

  it('gives the count itself when every answer of the window was tested', () => {
    const all = { ...done, window: '24h' as const, sampled: 120, population: 120, fired: 9, extrapolated_fires: 9, fire_rate: 0.075, fire_rate_ci95: [0.04, 0.137] as [number, number] };
    expect(studioDryRunExtrapolated(all)).toBe(false);
    expect(studioDryRunResultText(all)).toBe('Would have fired 9 times (7.5%, 95% CI 4.0%–13.7%) on the last 24 h; this test cost $0.00082.');
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
