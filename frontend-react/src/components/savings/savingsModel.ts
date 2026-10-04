/**
 * The Savings tab's numbers (SPEC §7.4; D46, D73), as pure functions over RoiSummary
 * (#47) and RoiTimeseries (#48): the counterfactual bands under the measured spend line, the
 * component table where every figure keeps its value label, the outcome split (stopped is block
 * and fallback only; flagged is separate) and the sampling status line.
 */
import { valueLabelView } from '@/domain/controlPresentation';
import { formatCount, formatDateTime, formatUsd } from '@/domain/format';
import {
  costCounterfactualView,
  costIsMeasuredMethod,
  costLabel,
  costLlmCapHit,
  costMethodWord,
  costNotMeasured,
  costRange,
  costSelectionBreakdown,
  costUniformPaused,
} from '@/domain/presentation';
import type { Counterfactual, RoiSummary, RoiTimeseries, ValueLabel } from '@/types/api';

// ------------------------------------------------------------------------------------------ bands

export type BandKind = 'same_checks' | 'full_audit';

export interface Band {
  low: number;
  high: number;
}

export interface CostBandPoint {
  t: string;
  /** Answers the fast check decided in the bucket. */
  gated: number;
  /** What checking cost in the bucket: fast check plus deep audit (USD). */
  measured: number;
  /** An LLM asking the same questions, over the summary's range (null: no estimate). */
  same_checks: Band | null;
  /** An LLM auditing every answer, over the per-1k range (null: no estimate). */
  full_audit: Band | null;
}

const finite = (x: unknown): x is number => typeof x === 'number' && Number.isFinite(x);

/** "1 answer", "1,199 answers". */
export function countOf(n: number, one: string, many = `${one}s`): string {
  return `${formatCount(n)} ${n === 1 ? one : many}`;
}

export function counterfactual(roi: RoiSummary | undefined, kind: Counterfactual['kind']): Counterfactual | null {
  return roi?.counterfactuals.find((c) => c.kind === kind) ?? null;
}

/**
 * The [low, high] multipliers of a counterfactual's range around its point estimate: low/usd and
 * high/usd, or [1, 1] when the range or the estimate is missing (the band is then the estimate).
 */
export function rangeRatio(c: Counterfactual | null): [number, number] {
  if (!c || !finite(c.usd) || c.usd <= 0 || !finite(c.low) || !finite(c.high)) return [1, 1];
  const lo = c.low / c.usd;
  const hi = c.high / c.usd;
  return [Math.min(lo, hi), Math.max(lo, hi)];
}

/** Why a counterfactual scaled up from the demo's free mock audits shows no figure. */
export const FREE_MOCK_AUDIT_REASON =
  'Not available in the simulated demo: the mock judge is free, so there is no audit cost to scale up';

/**
 * A full-audit counterfactual scaled up from free mock audits: simulated and $0, which says
 * nothing about what an LLM audit costs. It shows an em dash and no chart band (DESIGN §8: a
 * missing value is "—", never 0). The published assumption (static_assumptions) is never one.
 */
export function freeMockCounterfactual(c: Counterfactual | null, simulated: boolean): boolean {
  if (!c || c.kind !== 'llm_full_claim_audit' || c.method === 'static_assumptions') return false;
  return (simulated || c.label === 'simulated') && (c.usd === 0 || c.per_1k === 0);
}

/**
 * The points of the Savings chart (D73, no api.ts change):
 * - same-checks band: the bucket's `same_checks_usd` × [low/usd, high/usd] of the summary's
 *   `llm_same_checks`;
 * - full-audit band: the summary's `llm_full_claim_audit` per-1k range × gated / 1000.
 */
export function costBandPoints(series: RoiTimeseries, roi: RoiSummary | undefined): CostBandPoint[] {
  const same = counterfactual(roi, 'llm_same_checks');
  const full = counterfactual(roi, 'llm_full_claim_audit');
  const [sameLo, sameHi] = rangeRatio(same);
  const [fullLo, fullHi] = rangeRatio(full);
  const per1k = full && finite(full.per_1k) && !freeMockCounterfactual(full, roi?.simulated ?? false) ? full.per_1k : null;
  return series.points.map((p) => {
    const gated = finite(p.gated) ? p.gated : 0;
    const sameUsd = finite(p.same_checks_usd) ? p.same_checks_usd : null;
    return {
      t: p.t,
      gated,
      measured: (finite(p.decision_usd) ? p.decision_usd : 0) + (finite(p.llm_usd) ? p.llm_usd : 0),
      same_checks: sameUsd === null ? null : { low: sameUsd * sameLo, high: sameUsd * sameHi },
      full_audit: per1k === null ? null : { low: ((per1k * fullLo) * gated) / 1000, high: ((per1k * fullHi) * gated) / 1000 },
    };
  });
}

/** A bucket with nothing in it: no answer checked and nothing spent. */
const emptyBucket = (p: CostBandPoint): boolean => p.gated === 0 && p.measured === 0;

/**
 * The buckets the chart draws: a fresh stack's day is mostly empty hours, so the chart starts one
 * bucket before the first one with traffic (the line then rises from zero) rather than at the
 * window's start. `since` is the first drawn bucket's time when the start moved, else null. A
 * series without any traffic, or with too few buckets to trim, stays whole.
 */
export function chartBuckets(points: readonly CostBandPoint[]): { points: CostBandPoint[]; since: string | null } {
  const first = points.findIndex((p) => !emptyBucket(p));
  const start = Math.min(Math.max(0, first - 1), Math.max(0, points.length - 2));
  if (first <= 0 || start === 0) return { points: [...points], since: null };
  return { points: points.slice(start), since: points[start].t };
}

/** Whether a band has any spread in the series (else it is a single estimate per bucket: a line). */
export function bandHasRange(points: readonly CostBandPoint[], band: BandKind): boolean {
  return points.some((p) => {
    const b = p[band];
    return b !== null && b.high - b.low > 1e-9;
  });
}

/** The comparisons the chart can draw: the ones with at least one band in the series. */
export function availableBands(points: readonly CostBandPoint[]): BandKind[] {
  return (['same_checks', 'full_audit'] as const).filter((k) => points.some((p) => p[k] !== null));
}

/** The highest value the chart must fit: the measured spend and the chosen band's top. */
export function chartMax(points: readonly CostBandPoint[], band: BandKind | null): number {
  let max = 0;
  for (const p of points) {
    max = Math.max(max, p.measured);
    const b = band ? p[band] : null;
    if (b) max = Math.max(max, b.high);
  }
  return max;
}

// ------------------------------------------------------------------------------------ chart words

const dayFormat = new Intl.DateTimeFormat('en-GB', { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' });
const hourFormat = new Intl.DateTimeFormat('en-GB', { weekday: 'short', hour: '2-digit', minute: '2-digit' });

/** A bucket's name: the UTC day ("Mon 22 Sep") or the local hour ("Mon 14:00"). */
export function bucketLabel(bucket: RoiTimeseries['bucket'], iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return (bucket === '1d' ? dayFormat : hourFormat).format(t);
}

export function bandWords(kind: BandKind): string {
  return costCounterfactualView(kind === 'same_checks' ? 'llm_same_checks' : 'llm_full_claim_audit').label;
}

/**
 * When the chart's buckets run: "since Mon 14:00" when it starts at the first traffic, else the
 * window in words ("in the last 24 hours").
 */
export function chartSpan(bucket: RoiTimeseries['bucket'], since: string | null, windowWords: string): string {
  return since ? `since ${bucketLabel(bucket, since)}` : `in ${windowWords}`;
}

/** The chart in one sentence, for screen readers and the figure description. */
export function costBandSummary(points: readonly CostBandPoint[], band: BandKind | null, bandLabel: ValueLabel, span: string): string {
  const spent = points.reduce((s, p) => s + p.measured, 0);
  const answers = points.reduce((s, p) => s + p.gated, 0);
  let line = `Checking ${countOf(answers, 'answer')} cost ${formatUsd(spent)} ${span}.`;
  if (band) {
    const present = points.flatMap((p) => (p[band] ? [p[band]] : []));
    if (present.length > 0) {
      const low = present.reduce((s, b) => s + b.low, 0);
      const high = present.reduce((s, b) => s + b.high, 0);
      line += ` ${bandWords(band)} would have cost ${costRange(low, high)} (${valueLabelView(bandLabel).label.toLowerCase()}).`;
    }
  }
  return line;
}

// ------------------------------------------------------------------------------- component table

export interface ComponentRow {
  key: string;
  /** "Fast check (Jev)". */
  label: string;
  /** "58 deep audits: 44 high risk · …" or the method of a counterfactual. */
  detail: string | null;
  /** Spend in the window, or the counterfactual's total (with its range). */
  total: string;
  /** Per 1,000 checked answers. */
  per1k: string;
  valueLabel: ValueLabel;
  basis: string | null;
  /** A counterfactual from the published assumptions, not this gateway's own audits. */
  notMeasured: boolean;
}

const perThousand = (usd: number, n: number): number | null => (n > 0 ? (usd / n) * 1000 : null);

/** The Savings component table: what you paid, then what the alternatives would have cost. */
export function componentRows(roi: RoiSummary): ComponentRow[] {
  const n = roi.gated_responses;
  const sim = roi.simulated;
  const decisionPer1k = roi.decision_cost.per_1k ?? perThousand(roi.decision_cost.usd, n);
  const audits = roi.llm_cost.audits;
  const selections = costSelectionBreakdown(roi.llm_cost.by_selection);
  const rows: ComponentRow[] = [
    {
      key: 'fast_check',
      label: 'Fast check (Jev)',
      detail: `${countOf(n, 'answer')} checked`,
      total: formatUsd(roi.decision_cost.usd),
      per1k: formatUsd(decisionPer1k),
      valueLabel: costLabel(sim, roi.decision_cost.label),
      basis: roi.decision_cost.basis,
      notMeasured: false,
    },
    {
      key: 'deep_audit',
      label: 'Deep audit (LLM judge)',
      detail: `${audits.toLocaleString('en-US')} deep audit${audits === 1 ? '' : 's'}${selections ? `: ${selections}` : ''}`,
      total: formatUsd(roi.llm_cost.usd),
      per1k: formatUsd(perThousand(roi.llm_cost.usd, n)),
      valueLabel: costLabel(sim, roi.llm_cost.label),
      basis: roi.llm_cost.basis,
      notMeasured: false,
    },
    {
      key: 'total',
      label: 'Your total',
      detail: null,
      total: formatUsd(roi.decision_cost.usd + roi.llm_cost.usd),
      per1k: formatUsd(roi.total_per_1k.value),
      valueLabel: costLabel(sim, roi.total_per_1k.label),
      basis: roi.total_per_1k.basis,
      notMeasured: false,
    },
  ];
  for (const c of roi.counterfactuals) {
    const notMeasured = !costIsMeasuredMethod(c.method);
    if (freeMockCounterfactual(c, sim)) {
      rows.push({
        key: c.kind,
        label: costCounterfactualView(c.kind).label,
        detail: FREE_MOCK_AUDIT_REASON,
        total: formatUsd(null),
        per1k: formatUsd(null),
        valueLabel: costLabel(sim, c.label),
        basis: `${FREE_MOCK_AUDIT_REASON}. ${c.basis}`,
        notMeasured,
      });
      continue;
    }
    rows.push({
      key: c.kind,
      label: costCounterfactualView(c.kind).label,
      detail: notMeasured ? `${costMethodWord(c.method)} · ${costNotMeasured}` : costMethodWord(c.method),
      total: finite(c.low) && finite(c.high) ? costRange(c.low, c.high) : formatUsd(c.usd),
      per1k: formatUsd(c.per_1k),
      valueLabel: costLabel(sim, c.label),
      basis: c.basis,
      notMeasured,
    });
  }
  return rows;
}

// ---------------------------------------------------------------------------------------- savings

export interface SavingsRange {
  /**
   * The amounts, always positive: "$0.60–$0.97" saved, "$0.10–$0.20" more, or for a range that
   * crosses zero "$0.10 more and $0.20 less".
   */
  text: string;
  label: ValueLabel;
  /**
   * `saved`: checking with Jev cost less across the whole range; `cost_more`: it cost more across
   * the whole range; `mixed`: the range runs from costing more to saving.
   */
  direction: 'saved' | 'cost_more' | 'mixed';
  /**
   * How many times cheaper the fast check was than an LLM asking the same questions ("60×"), when
   * it saved and by at least 2×; else null.
   */
  multiple: string | null;
}

/** "60×", "4.5×": the same-checks counterfactual over the fast-check spend, or null below 2×. */
export function cheaperMultiple(roi: RoiSummary): string | null {
  const same = counterfactual(roi, 'llm_same_checks');
  const spent = roi.decision_cost.usd;
  if (!same || !finite(same.usd) || !finite(spent) || spent <= 0) return null;
  const x = same.usd / spent;
  if (!Number.isFinite(x) || x < 2) return null;
  return `${x < 10 ? x.toLocaleString('en-US', { maximumFractionDigits: 1 }) : formatCount(Math.round(x))}×`;
}

/** What the fast check saved against an LLM asking the same questions, or null without an estimate. */
export function savingsRange(roi: RoiSummary): SavingsRange | null {
  const { usd_low, usd_high } = roi.savings_vs_same_checks;
  const lo = usd_low ?? usd_high;
  const hi = usd_high ?? usd_low;
  if (lo === null || hi === null) return null;
  const low = Math.min(lo, hi);
  const high = Math.max(lo, hi);
  const label = costLabel(roi.simulated, roi.savings_vs_same_checks.label);
  // "Cost $0.10–$0.20 more than …", never "Cost -$0.20–-$0.10 more than …".
  if (low < 0 && high <= 0) return { text: costRange(-high, -low), label, direction: 'cost_more', multiple: null };
  if (low < 0) return { text: `${formatUsd(-low)} more and ${formatUsd(high)} less`, label, direction: 'mixed', multiple: null };
  return { text: costRange(low, high), label, direction: 'saved', multiple: cheaperMultiple(roi) };
}

// ------------------------------------------------------------------------------------- sampling

export interface SamplingLine {
  key: 'uniform' | 'llm_cap';
  text: string;
  /** A cap stopped something today. */
  stopped: boolean;
}

/** Clock time of a stop ("14:02"), or the full date when it was not today. */
function stopTime(iso: string, now: number): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const sameDay = new Date(t).toDateString() === new Date(now).toDateString();
  return sameDay ? new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' }).format(t) : formatDateTime(iso);
}

/**
 * Sampling status (SPEC §7.4): uniform sampling ("12 of 50 today", or paused at its cap with the
 * time), and the deep-audit budget when it was reached.
 */
export function samplingLines(roi: RoiSummary, now = Date.now()): SamplingLine[] {
  const s = roi.sampling;
  const lines: SamplingLine[] = [];
  if (s.uniform_stopped_at) {
    lines.push({ key: 'uniform', text: costUniformPaused(stopTime(s.uniform_stopped_at, now), s.uniform_cap), stopped: true });
  } else {
    const rate = finite(s.uniform_rate) && s.uniform_rate > 0 ? ` (${(s.uniform_rate * 100).toLocaleString('en-US', { maximumFractionDigits: 2 })}% of answers)` : '';
    lines.push({
      key: 'uniform',
      text: `Uniform sampling: ${s.uniform_today.toLocaleString('en-US')} of ${s.uniform_cap.toLocaleString('en-US')} audits today${rate}`,
      stopped: false,
    });
  }
  if (s.llm_cap_hit_at) lines.push({ key: 'llm_cap', text: costLlmCapHit(stopTime(s.llm_cap_hit_at, now)), stopped: true });
  return lines;
}

// ------------------------------------------------------------------------------------- outcomes

export interface OutcomeSplit {
  /** Block and fallback: the user never saw the answer. */
  stopped: number;
  /** Delivered with a warning to your app. */
  flagged: number;
  notAssessed: number;
  label: ValueLabel;
}

export function outcomeSplit(roi: RoiSummary): OutcomeSplit {
  return {
    stopped: roi.outcomes.blocked + roi.outcomes.fallback,
    flagged: roi.outcomes.flagged,
    notAssessed: roi.outcomes.not_assessed,
    label: costLabel(roi.simulated, roi.outcomes.label),
  };
}
