import { describe, expect, it } from 'vitest';
import { roiSummary } from '@/test/control/fixtures';
import type { Counterfactual, RoiTimeseries } from '@/types/api';
import {
  FREE_MOCK_AUDIT_REASON,
  availableBands,
  bandHasRange,
  chartBuckets,
  chartSpan,
  cheaperMultiple,
  costBandSummary,
  chartMax,
  componentRows,
  costBandPoints,
  freeMockCounterfactual,
  rangeRatio,
  samplingLines,
  savingsRange,
  type CostBandPoint,
} from './savingsModel';

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
    const span = chartSpan('1h', null, 'the last 24 hours');
    expect(costBandSummary(points, 'same_checks', 'estimate', span)).toBe(
      'Checking 250 answers cost $0.21 in the last 24 hours. An LLM asking the same questions would have cost $0.53–$0.70 (estimate).',
    );
    expect(costBandSummary(points, null, 'estimate', span)).toBe('Checking 250 answers cost $0.21 in the last 24 hours.');
    expect(costBandSummary([{ ...points[0], gated: 1 }], null, 'estimate', span)).toMatch(/^Checking 1 answer cost \$[\d.]+ in the last 24 hours\.$/);
    expect(chartSpan('1h', '2026-09-28T10:00:00.000Z', 'the last 24 hours')).toMatch(/^since \w{3} \d{2}:00$/);
  });

  const bucketAt = (h: number, gated: number, measured: number): CostBandPoint => ({
    t: `2026-09-28T${String(h).padStart(2, '0')}:00:00.000Z`,
    gated,
    measured,
    same_checks: { low: measured * 50, high: measured * 50 },
    full_audit: null,
  });

  it('starts the chart one bucket before the first traffic, never with fewer than two buckets', () => {
    const day = Array.from({ length: 25 }, (_, h) => bucketAt(h % 24, 0, 0));
    // A fresh stack: traffic only in the last hour. The chart keeps the hour before it (from zero).
    const fresh = [...day.slice(0, 24), bucketAt(23, 1123, 0.05)];
    const a = chartBuckets(fresh);
    expect(a.points).toHaveLength(2);
    expect(a.since).toBe(fresh[23].t);
    // Traffic from the third hour on: the chart starts at the second.
    const later = day.map((p, i) => (i >= 2 ? bucketAt(i % 24, 10, 0.01) : p));
    expect(chartBuckets(later).points).toHaveLength(24);
    // Traffic from the start, no traffic at all, or a single bucket: the window stays whole.
    for (const whole of [day.map((p, i) => (i === 0 ? bucketAt(0, 5, 0.01) : p)), day, [bucketAt(3, 4, 0.01)]]) {
      expect(chartBuckets(whole)).toEqual({ points: whole, since: null });
    }
  });

  it('draws a band as a range only when it has one', () => {
    const point = bucketAt(1, 10, 0.01);
    expect(bandHasRange([point], 'same_checks')).toBe(false);
    expect(bandHasRange([{ ...point, same_checks: { low: 0.4, high: 0.6 } }], 'same_checks')).toBe(true);
    expect(bandHasRange([point], 'full_audit')).toBe(false);
  });

  it('shows no figure and no band for a full audit scaled up from free mock audits', () => {
    const freeFull: Counterfactual = { ...full, method: 'uniform_sample', usd: 0, low: 0, high: 0, per_1k: 0, label: 'simulated' };
    const roi = roiSummary({ simulated: true, counterfactuals: [...roiSummary().counterfactuals, freeFull] });
    expect(freeMockCounterfactual(freeFull, true)).toBe(true);
    // A real $0 (a free local judge on real traffic) and the published assumption keep their figures.
    expect(freeMockCounterfactual({ ...freeFull, label: 'estimate' }, false)).toBe(false);
    expect(freeMockCounterfactual({ ...freeFull, method: 'static_assumptions' }, true)).toBe(false);
    expect(freeMockCounterfactual({ ...full, label: 'simulated' }, true)).toBe(false);
    const row = componentRows(roi).find((r) => r.key === 'llm_full_claim_audit')!;
    expect([row.total, row.per1k, row.detail]).toEqual(['—', '—', FREE_MOCK_AUDIT_REASON]);
    expect(row.basis).toContain(FREE_MOCK_AUDIT_REASON);
    const points = costBandPoints(series, roi);
    expect(points.every((p) => p.full_audit === null)).toBe(true);
    expect(availableBands(points)).toEqual(['same_checks']);
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
    expect(savingsRange(roiSummary())).toEqual({ text: '$0.60–$0.97', label: 'estimate', direction: 'saved', multiple: '50×' });
    // Cost more: the amounts read as positive after "Cost … more than".
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: -0.2, usd_high: -0.1, label: 'estimate' } }))).toEqual({
      text: '$0.10–$0.20',
      label: 'estimate',
      direction: 'cost_more',
      multiple: null,
    });
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: null, usd_high: -0.3, label: 'estimate' } }))?.text).toBe('$0.30');
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: -0.1, usd_high: 0, label: 'estimate' } }))?.direction).toBe('cost_more');
    // A range across zero: "Between $0.10 more and $0.20 less than …", never "Saved -$0.10–$0.20".
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: -0.1, usd_high: 0.2, label: 'estimate' } }))).toEqual({
      text: '$0.10 more and $0.20 less',
      label: 'estimate',
      direction: 'mixed',
      multiple: null,
    });
    expect(savingsRange(roiSummary({ simulated: true }))?.label).toBe('simulated');
    expect(savingsRange(roiSummary({ savings_vs_same_checks: { usd_low: null, usd_high: null, label: 'estimate' } }))).toBeNull();
  });

  it('states how many times cheaper the fast check was, from 2× up', () => {
    expect(cheaperMultiple(roiSummary())).toBe('50×');
    const base = roiSummary();
    expect(cheaperMultiple({ ...base, decision_cost: { ...base.decision_cost, usd: 0.25 } })).toBe('4.9×');
    expect(cheaperMultiple({ ...base, decision_cost: { ...base.decision_cost, usd: 0.7 } })).toBeNull();
    expect(cheaperMultiple({ ...base, decision_cost: { ...base.decision_cost, usd: 0 } })).toBeNull();
    expect(cheaperMultiple(roiSummary({ counterfactuals: [] }))).toBeNull();
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
