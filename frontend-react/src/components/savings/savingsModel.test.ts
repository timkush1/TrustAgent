import { describe, expect, it } from 'vitest';
import { roiSummary } from '@/test/control/fixtures';
import type { Counterfactual, RoiTimeseries } from '@/types/api';
import { availableBands, costBandSummary, chartMax, componentRows, costBandPoints, rangeRatio, samplingLines, savingsRange } from './savingsModel';

const full: Counterfactual = {
  kind: 'llm_full_claim_audit',
  method: 'fitted_from_audits',
  usd: 20,
  low: 15,
  high: 30,
  per_1k: 40,
  n: 500,
  label: 'estimate',
  basis: 'fitted',
};

const series: RoiTimeseries = {
  window: '24h',
  bucket: '1h',
  points: [
    { t: '2026-09-28T10:00:00.000Z', decision_usd: 0.01, llm_usd: 0.2, same_checks_usd: 0.61, gated: 250 },
    { t: '2026-09-28T11:00:00.000Z', decision_usd: 0, llm_usd: 0, same_checks_usd: null, gated: 0 },
  ],
};

describe('Savings bands (D73)', () => {
  it('scales the same-checks bucket by the summary range and the full audit by answers', () => {
    const roi = roiSummary({ counterfactuals: [...roiSummary().counterfactuals, full] });
    const [a, b] = costBandPoints(series, roi);
    expect(a.measured).toBeCloseTo(0.21, 12);
    // same_checks_usd × [1.05/1.22, 1.40/1.22].
    expect(a.same_checks!.low).toBeCloseTo((0.61 * 1.05) / 1.22, 12);
    expect(a.same_checks!.high).toBeCloseTo((0.61 * 1.4) / 1.22, 12);
    // per_1k × [15/20, 30/20] × gated / 1000.
    expect(a.full_audit!.low).toBeCloseTo(7.5, 12);
    expect(a.full_audit!.high).toBeCloseTo(15, 12);
    // A bucket with no estimate has no same-checks band; no answers, no full-audit cost.
    expect(b.same_checks).toBeNull();
    expect(b.full_audit).toEqual({ low: 0, high: 0 });
    expect(availableBands([a, b])).toEqual(['same_checks', 'full_audit']);
    expect(chartMax([a, b], 'same_checks')).toBeCloseTo((0.61 * 1.4) / 1.22, 12);
    expect(chartMax([a, b], 'full_audit')).toBe(15);
  });

  it('collapses a band to the estimate when the range is missing, and draws none without one', () => {
    expect(rangeRatio(null)).toEqual([1, 1]);
    expect(rangeRatio({ ...full, low: null, high: null })).toEqual([1, 1]);
    expect(rangeRatio({ ...full, usd: 0 })).toEqual([1, 1]);
    expect(rangeRatio({ ...full, low: 30, high: 15 })).toEqual([0.75, 1.5]);
    const points = costBandPoints(series, roiSummary({ counterfactuals: [] }));
    expect(points[0].same_checks).toEqual({ low: 0.61, high: 0.61 });
    expect(points.every((p) => p.full_audit === null)).toBe(true);
    expect(availableBands(costBandPoints({ ...series, points: [{ ...series.points[1] }] }, roiSummary({ counterfactuals: [] })))).toEqual([]);
  });

  it('describes the chart in one sentence', () => {
    const points = costBandPoints(series, roiSummary());
    expect(costBandSummary(points, 'same_checks', 'estimate')).toBe(
      'Checking 250 answers cost $0.21 over 2 intervals. An LLM asking the same questions would have cost $0.53–$0.70 (estimate).',
    );
    expect(costBandSummary(points, null, 'estimate')).toBe('Checking 250 answers cost $0.21 over 2 intervals.');
  });
});

describe('Savings figures', () => {
  it('lists what you paid, then the alternatives, each with its label', () => {
    const rows = componentRows(roiSummary({ counterfactuals: [...roiSummary().counterfactuals, { ...full, method: 'static_assumptions' }] }));
    expect(rows.map((r) => [r.key, r.total, r.per1k, r.valueLabel])).toEqual([
      ['fast_check', '$0.02', '$0.05', 'measured'],
      ['deep_audit', '$0.41', '$0.76', 'measured'],
      ['total', '$0.43', '$0.80', 'measured'],
      ['llm_same_checks', '$1.05–$1.40', '$2.26', 'estimate'],
      ['llm_full_claim_audit', '$15.00–$30.00', '$40.00', 'estimate'],
    ]);
    expect(rows[4].notMeasured).toBe(true);
    expect(rows[4].detail).toMatch(/not measured$/);
    expect(componentRows(roiSummary({ simulated: true })).every((r) => r.valueLabel === 'simulated')).toBe(true);
  });

  it('says when checking cost more than asking an LLM', () => {
    expect(savingsRange(roiSummary())).toEqual({ text: '$0.60–$0.97', label: 'estimate', negative: false });
    // Cost more: the amounts read as positive after "Cost … more than".
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: -0.2, usd_high: -0.1, label: 'estimate' } }))).toEqual({
      text: '$0.10–$0.20',
      label: 'estimate',
      negative: true,
    });
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: null, usd_high: -0.3, label: 'estimate' } }))?.text).toBe('$0.30');
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: null, usd_high: null, label: 'estimate' } }))).toBeNull();
  });

  it('dates a stop from an earlier day in full', () => {
    const now = Date.parse('2026-09-28T12:00:00Z');
    const lines = samplingLines(
      roiSummary({ sampling: { uniform_rate: 0, uniform_today: 3, uniform_cap: 50, uniform_stopped_at: null, llm_cap_hit_at: '2026-09-26T08:30:00Z' } }),
      now,
    );
    expect(lines[0]).toEqual({ key: 'uniform', text: 'Uniform sampling: 3 of 50 audits today', stopped: false });
    expect(lines[1].text).toMatch(/^Deep-audit budget reached at 26 Sept?/);
  });
});
