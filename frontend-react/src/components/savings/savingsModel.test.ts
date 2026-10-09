import { describe, expect, it } from 'vitest';
import claimsDoc from '@contracts/claims/value-claims.v1.json';
import { costFullAuditPer1k, costShippedDefaults } from '@/domain/presentation';
import { roiSummary } from '@/test/control/fixtures';
import type { Counterfactual, RoiTimeseries } from '@/types/api';
import {
  FREE_MOCK_AUDIT_REASON,
  alternatives,
  availableBands,
  bandHasRange,
  bandLabels,
  bucketLabel,
  bucketUnit,
  chartBuckets,
  chartSpan,
  chartTotals,
  cheaperMultiple,
  costBandSummary,
  chartMax,
  componentRows,
  costBandPoints,
  endLabels,
  freeMockCounterfactual,
  fullAuditNotComparable,
  counterfactualLabel,
  rangeRatio,
  realJudgeView,
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

  it('describes the chart in one sentence, with the summary’s totals', () => {
    // The fixture summary: 540 answers, $0.0243 + $0.41 spent, the same checks $1.05–$1.40.
    const totals = chartTotals(roiSummary());
    const span = chartSpan('1h', null, 'in the last 24 hours');
    expect(costBandSummary(totals, 'same_checks', 'estimate', span)).toBe(
      'Checking 540 answers cost $0.43 in the last 24 hours. An LLM asking the same questions would have cost $1.05–$1.40 (estimate).',
    );
    expect(costBandSummary(totals, null, 'estimate', span)).toBe('Checking 540 answers cost $0.43 in the last 24 hours.');
    // No full-audit estimate in the summary: the sentence names no full-audit figure.
    expect(costBandSummary(totals, 'full_audit', 'estimate', span)).toBe('Checking 540 answers cost $0.43 in the last 24 hours.');
    expect(costBandSummary({ ...totals, answers: 1 }, null, 'estimate', 'over the replayed traffic')).toMatch(
      /^Checking 1 answer cost \$[\d.]+ over the replayed traffic\.$/,
    );
    expect(chartSpan('1h', '2026-09-28T10:00:00.000Z', 'in the last 24 hours')).toMatch(/^since \w{3} \d{2}:00$/);
  });

  it('takes the chart’s totals from the summary, the same figures as the component table', () => {
    const roi = roiSummary({ counterfactuals: [...roiSummary().counterfactuals, { ...full, method: 'static_assumptions', low: null, high: null }] });
    expect(chartTotals(roi)).toEqual({
      answers: 540,
      spent: 0.0243 + 0.41,
      bands: { same_checks: { low: 1.05, high: 1.4 }, full_audit: { low: 20, high: 20 } },
    });
    // A full audit scaled up from free mock audits has no total, as it has no row figure.
    const freeFull: Counterfactual = { ...full, method: 'uniform_sample', usd: 0, low: 0, high: 0, per_1k: 0, label: 'simulated' };
    expect(chartTotals(roiSummary({ simulated: true, counterfactuals: [freeFull] })).bands).toEqual({ same_checks: null, full_audit: null });
  });

  it('names minute buckets for the static replay', () => {
    expect(['1d', '1h', '1m'].map((b) => bucketUnit(b as never))).toEqual(['day', 'hour', 'minute']);
    expect(bucketLabel('1m', '2026-09-28T09:01:00.000Z')).toMatch(/^\d{2}:\d{2}$/);
    expect(bucketLabel('1h', '2026-09-28T09:00:00.000Z')).toMatch(/^\w{3} \d{2}:00$/);
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

  it('offers no full-audit band on a demo whose mock judge is free, and says why', () => {
    // The live demo: 44 answers, each deep-audited by the free mock judge, beside the published
    // full-audit figure. With a real judge its own audits would cost about as much as a full audit,
    // so the $0 spend against the published figure would compare two bases.
    const published: Counterfactual = { ...full, method: 'static_assumptions', per_1k: 35.5, usd: 1.562, low: null, high: null };
    const demo = roiSummary({
      simulated: true,
      gated_responses: 44,
      llm_cost: { ...roiSummary().llm_cost, usd: 0, audits: 44 },
      counterfactuals: [...roiSummary().counterfactuals, published],
    });
    expect(fullAuditNotComparable(demo)).toBe(
      'Full audit is not comparable on the simulated demo: its mock judge is free and it deep-audits every answer. Compare the published figures under “With a real judge” above.',
    );
    const points = costBandPoints(series, demo);
    expect(points.every((p) => p.full_audit === null)).toBe(true);
    expect(points[0].same_checks).not.toBeNull();
    expect(chartTotals(demo).bands.full_audit).toBeNull();
    expect(costBandSummary(chartTotals(demo), 'full_audit', 'estimate', 'today')).not.toContain('auditing every answer');
    // The replay deep-audits about 67 in 100 answers.
    const replay = { ...demo, gated_responses: 18, llm_cost: { ...demo.llm_cost, audits: 12 } };
    expect(fullAuditNotComparable(replay)).toContain('it deep-audits about 67 in 100 answers.');
    // With a judge that costs something, the full audit is a fair comparison and is drawn.
    const real = { ...demo, llm_cost: { ...demo.llm_cost, usd: 0.4 } };
    expect(fullAuditNotComparable(real)).toBeNull();
    expect(costBandPoints(series, real)[0].full_audit).toEqual({ low: (35.5 * 250) / 1000, high: (35.5 * 250) / 1000 });
    expect(chartTotals(real).bands.full_audit).toEqual({ low: 1.562, high: 1.562 });
    expect(fullAuditNotComparable(undefined)).toBeNull();
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
    // The published assumption says where its number comes from: per 1,000 answers × the answers.
    expect(rows[4].detail).toBe(`Published assumptions: $40.00 per 1,000 answers × ${roiSummary().gated_responses.toLocaleString('en-US')} answers · not measured`);
    expect(rows[4].detail).toMatch(/not measured$/);
    expect(componentRows(roiSummary({ simulated: true })).every((r) => r.valueLabel === 'simulated')).toBe(true);
  });

  it('labels the published full-audit figure Estimate even on a simulated stack', () => {
    const published: Counterfactual = { ...full, method: 'static_assumptions', low: null, high: null, per_1k: 35.5, usd: 2.343 };
    const roi = roiSummary({ simulated: true, gated_responses: 66, counterfactuals: [...roiSummary().counterfactuals, published] });
    const rows = componentRows(roi);
    expect(rows.map((r) => [r.key, r.valueLabel])).toEqual([
      ['fast_check', 'simulated'],
      ['deep_audit', 'simulated'],
      ['total', 'simulated'],
      ['llm_same_checks', 'simulated'],
      ['llm_full_claim_audit', 'estimate'],
    ]);
    // Its detail says the answers it is applied to are simulated.
    expect(rows[4].detail).toBe('Published assumptions: $35.50 per 1,000 answers × 66 simulated answers · not measured');
    expect(bandLabels(roi)).toEqual({ same_checks: 'simulated', full_audit: 'estimate' });
    // A full audit fitted to (simulated) audits stays Simulated.
    expect(counterfactualLabel(full, true)).toBe('simulated');
    expect(counterfactualLabel(full, false)).toBe('estimate');
  });

  it('says which published figure each alternative is read against on a free mock judge', () => {
    const published: Counterfactual = { ...full, method: 'static_assumptions', low: null, high: null, per_1k: 35.5, usd: 1.562 };
    const demo = roiSummary({
      simulated: true,
      gated_responses: 44,
      llm_cost: { ...roiSummary().llm_cost, usd: 0, audits: 44 },
      counterfactuals: [...roiSummary().counterfactuals, published],
    });
    const rows = componentRows(demo, realJudgeView(demo));
    const fullRow = rows.find((r) => r.key === 'llm_full_claim_audit')!;
    expect(fullRow.detail).toBe(
      'Published assumptions: $35.50 per 1,000 answers × 44 simulated answers · not measured. Compare it with TrustAgent with a real judge ($3 per 1,000 answers) above, not with this simulated total',
    );
    const sameRow = rows.find((r) => r.key === 'llm_same_checks')!;
    expect(sameRow.detail).toMatch(/Simulated on the demo’s short scripted requests; for a typical 5,000-token request the published figure is \$5\.75 per 1,000 answers \(above\)$/);
    // Without the real-judge view (a stack with a real judge), the rows keep their method only.
    expect(componentRows(demo).find((r) => r.key === 'llm_full_claim_audit')!.detail).toBe(
      'Published assumptions: $35.50 per 1,000 answers × 44 simulated answers · not measured',
    );
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

describe('endLabels', () => {
  const y = (v: number) => 100 - v; // a plot from $0 (bottom) to $100 (top)
  const point = (measured: number, same: number | null, spread = 0): CostBandPoint => ({
    t: '2026-09-22T00:00:00Z',
    gated: 10,
    measured,
    same_checks: same === null ? null : { low: same - spread, high: same + spread },
    full_audit: null,
  });

  it('labels the last spend and the last estimate where their lines end', () => {
    const labels = endLabels([point(1, 40), point(2, 60)], 'same_checks', false, y);
    expect(labels).toEqual([
      { key: 'measured', text: 'You $2.00', top: 98 },
      { key: 'band', text: 'LLM $60.00', top: 40 },
    ]);
  });

  it('labels a range by its range, at its middle', () => {
    const [, band] = endLabels([point(1, 50, 10)], 'same_checks', true, y);
    expect(band).toEqual({ key: 'band', text: 'LLM $40.00–$60.00', top: 50 });
  });

  it('pushes two close labels apart, inside the plot', () => {
    const [you, llm] = endLabels([point(1, 2)], 'same_checks', false, y);
    expect(llm.top - you.top).toBeCloseTo(-9);
    expect(Math.max(you.top, llm.top)).toBeLessThanOrEqual(100);
    expect(Math.min(you.top, llm.top)).toBeGreaterThanOrEqual(0);
  });

  it('labels only the spend without a comparison', () => {
    expect(endLabels([point(3, null)], 'same_checks', false, y).map((l) => l.key)).toEqual(['measured']);
    expect(endLabels([], null, false, y)).toEqual([]);
  });
});

describe('realJudgeView', () => {
  const shipped = { per1k: 3, basis: 'Fast check on every answer, plus a full LLM audit of the deep-audited share.', auditShare: 0.079 };

  it('leads with the published figures at the shipped defaults, never the demo’s rate scaled up', () => {
    // The live demo stack: every answer deep-audited (66 of 66) by a free mock judge.
    const demo = roiSummary({ simulated: true, gated_responses: 66, llm_cost: { ...roiSummary().llm_cost, usd: 0, audits: 66 } });
    const view = realJudgeView(demo, shipped, 35.5, 'Decompose into 8 claims.')!;
    expect([view.shippedPer1k, view.shippedShare, view.fullAuditPer1k]).toEqual(['$3', 'about 8 in 100', '$35.50']);
    expect(view.demoLine).toBe('This demo deep-audits every answer to show the audit flow: at that rate a real judge would add about $35.50 per 1,000 answers.');
    expect(view.demoBasis).toContain('66 for 66 answers');
  });

  it('words the replay’s rate in hundreds', () => {
    // The online demo's recording: 12 deep audits over 18 answers.
    const replay = roiSummary({ simulated: true, gated_responses: 18, llm_cost: { ...roiSummary().llm_cost, usd: 0, audits: 12 } });
    expect(realJudgeView(replay, shipped, 35.5, '')?.demoLine).toBe(
      'This demo deep-audits about 67 in 100 answers to show the audit flow: at that rate a real judge would add about $23.67 per 1,000 answers.',
    );
    const rare = { ...replay, gated_responses: 1000, llm_cost: { ...replay.llm_cost, audits: 3 } };
    expect(realJudgeView(rare, shipped, 35.5, '')?.demoLine).toMatch(/^This demo deep-audits fewer than 1 in 100 answers/);
    const extra = { ...replay, llm_cost: { ...replay.llm_cost, audits: 20 } };
    expect(realJudgeView(extra, shipped, 35.5, '')?.demoLine).toMatch(/^This demo ran more deep audits than answers checked \(20 for 18 answers\)/);
  });

  it('gives the published figures alone without audits, and nothing without the figures', () => {
    const roi = roiSummary();
    expect(realJudgeView({ ...roi, llm_cost: { ...roi.llm_cost, audits: 0 } }, shipped, 35.5, '')?.demoLine).toBeNull();
    expect(realJudgeView({ ...roi, gated_responses: 0 }, shipped, 35.5, '')?.demoLine).toBeNull();
    expect(realJudgeView(roi, shipped, null, '')).toBeNull();
    expect(realJudgeView(roi, { ...shipped, per1k: null }, 35.5, '')).toBeNull();
  });

  it('reads the shipped defaults from the published assumptions, consistent with their total', () => {
    const view = realJudgeView(roiSummary())!;
    expect([view.shippedPer1k, view.shippedShare, view.fullAuditPer1k]).toEqual(['$3', 'about 8 in 100', '$35.50']);
    // The total in its parts: the fast check on every answer plus a deep audit of the share.
    expect(view.shippedParts).toBe('fast check $0.21 on every answer + a deep audit of about 8 in 100 answers');
    expect(view.sameChecksPer1k).toBe('$5.75');
    expect(realJudgeView(roiSummary(), costShippedDefaults, 35.5, '', { value: null }, { value: null })).toMatchObject({
      shippedParts: 'the fast check on every answer + a deep audit of about 8 in 100 answers',
      sameChecksPer1k: null,
    });
    // The share is 5% grounding-flagged + 2% of the other 95% + 1% uniform, and the published total
    // is the fast check on every answer plus a full audit of that share.
    expect(costShippedDefaults.auditShare).toBeCloseTo(0.079, 12);
    const fastCheck = claimsDoc.claims.find((c) => c.id === 'fast_check_cost_per_1k')!.value!;
    expect(fastCheck + costShippedDefaults.auditShare * costFullAuditPer1k!).toBeCloseTo(costShippedDefaults.per1k!, 1);
  });
});

describe('alternatives', () => {
  const shipped = { per1k: 3, basis: 'Fast check on every answer, plus a full LLM audit of the deep-audited share.', auditShare: 0.079 };
  const publishedSame = { value: 5.75, basis: 'The same 5,000-token request plus about 150 output tokens.' };
  const published: Counterfactual = { ...full, method: 'static_assumptions', per_1k: 35.5, usd: 0.64, low: null, high: null, label: 'estimate', basis: 'not measured' };
  const keys = (view: ReturnType<typeof alternatives>) => view.items.map((a) => a.key);

  it('lists this window’s alternatives per 1,000 answers on a stack with a real judge, each with its own label', () => {
    const roi = roiSummary({ counterfactuals: [...roiSummary().counterfactuals, full] });
    const view = alternatives(roi, null, publishedSame);
    expect(view.title).toBe('Against the alternatives, per 1,000 answers');
    expect(keys(view)).toEqual(['llm_same_checks', 'llm_full_claim_audit']);
    expect(view.items.map((a) => [a.per1k, a.valueLabel, a.note])).toEqual([
      ['$2.26', 'estimate', 'Token model: your fast-check tokens priced at the judge’s rate'],
      ['$40.00', 'estimate', 'Fitted from your deep audits by answer length'],
    ]);
    // A badge subject apart from the component table's row of the same name.
    expect(view.items[1]!.subject).toBe('An LLM auditing every answer claim by claim, per 1,000 answers');
    // The published full audit from the server says it is not measured.
    const early = alternatives(roiSummary({ counterfactuals: [...roiSummary().counterfactuals, published] }), null, publishedSame);
    expect(early.items[1]!.note).toBe('Published assumptions · not measured');
  });

  it('compares only published estimates on a free mock judge, a real judge cheapest of the three', () => {
    const demo = roiSummary({ simulated: true, llm_cost: { ...roiSummary().llm_cost, usd: 0 }, counterfactuals: [...roiSummary().counterfactuals, published] });
    const view = alternatives(demo, realJudgeView(demo, shipped, 35.5, 'Decompose into 8 claims.'), publishedSame);
    expect(view.title).toBe('With a real judge, per 1,000 answers');
    expect(keys(view)).toEqual(['real_judge', 'llm_same_checks', 'llm_full_claim_audit']);
    // Never the demo's simulated same-questions figure ($2.26 here) beside the published ones.
    expect(view.items.map((a) => [a.per1k, a.valueLabel, a.basis])).toEqual([
      ['$3', 'estimate', shipped.basis],
      ['$5.75', 'estimate', publishedSame.basis],
      ['$35.50', 'estimate', 'Decompose into 8 claims.'],
    ]);
    expect(view.items.map((a) => a.note)).toEqual([
      'At the shipped defaults: fast check $0.21 on every answer + a deep audit of about 8 in 100 answers',
      'Published assumptions · not measured',
      'Published assumptions · not measured',
    ]);
  });

  it('leaves out what is missing and never shows a full audit scaled up from free mock audits', () => {
    const freeFull: Counterfactual = { ...full, method: 'uniform_sample', usd: 0, low: 0, high: 0, per_1k: 0, label: 'simulated' };
    expect(alternatives(roiSummary({ simulated: true, counterfactuals: [freeFull] }), null, publishedSame).items).toEqual([]);
    // A same-questions figure without a per-1,000 value is left out, never shown as $0.
    const noPrice = roiSummary({ counterfactuals: [{ ...roiSummary().counterfactuals[0]!, per_1k: null }] });
    expect(alternatives(noPrice, null, publishedSame).items).toEqual([]);
    // Without the published same-questions figure, a free mock judge's comparison keeps the other two.
    const demo = roiSummary({ simulated: true, llm_cost: { ...roiSummary().llm_cost, usd: 0 } });
    expect(keys(alternatives(demo, realJudgeView(demo, shipped, 35.5, ''), { value: null, basis: '' }))).toEqual(['real_judge', 'llm_full_claim_audit']);
  });

  it('reads the published same-questions figure from the value claims', () => {
    const demo = roiSummary({ simulated: true, llm_cost: { ...roiSummary().llm_cost, usd: 0 } });
    const same = alternatives(demo, realJudgeView(demo)).items.find((a) => a.key === 'llm_same_checks')!;
    expect(same.per1k).toBe(`$${claimsDoc.claims.find((c) => c.id === 'llm_same_checks_cost_per_1k')!.value!.toFixed(2)}`);
  });
});
