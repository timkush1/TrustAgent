/**
 * The Savings tab's numbers (SPEC §7.4; D46, D73), as pure functions over RoiSummary
 * (#47) and RoiTimeseries (#48): the counterfactual bands under the measured spend line, the
 * component table where every figure keeps its value label, the outcome split (stopped is block
 * and fallback only; flagged is separate) and the sampling status line.
 */
import { savingsLine } from '@/components/overview/overviewModel';
import { valueLabelView } from '@/domain/controlPresentation';
import { formatCount, formatDateTime, formatUsd } from '@/domain/format';
import {
  costCounterfactualView,
  costFastCheckPublished,
  costFullAuditBasis,
  costFullAuditPer1k,
  costIsMeasuredMethod,
  costShippedDefaults,
  costLabel,
  costLlmCapHit,
  costMethodWord,
  costNotMeasured,
  costRange,
  costSameChecksPublished,
  costSelectionBreakdown,
  costUniformPaused,
} from '@/domain/presentation';
import type { CostBucket, CostSeries } from '@/data/control/costPlane';
import type { Counterfactual, RoiSummary, ValueLabel } from '@/types/api';

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

/** "every answer", "about 67 in 100 answers": a deep-audit rate in words. */
function auditShareWords(rate: number): string {
  if (rate >= 0.995) return 'every answer';
  const pct = Math.round(rate * 100);
  return pct < 1 ? 'fewer than 1 in 100 answers' : `about ${pct} in 100 answers`;
}

/**
 * How much of the window's traffic was deep-audited, as what the demo does ("deep-audits every
 * answer", "ran more deep audits than answers checked"), or null without any.
 */
function demoAuditShare(roi: RoiSummary): string | null {
  const n = roi.gated_responses;
  const audits = roi.llm_cost.audits;
  if (n <= 0 || audits <= 0) return null;
  return audits / n > 1.005 ? 'ran more deep audits than answers checked' : `deep-audits ${auditShareWords(audits / n)}`;
}

/**
 * Why the chart offers no full-audit comparison on a demo whose mock judge is free, or null when
 * it can draw one. The demo's spend has the deep audits at $0 while it deep-audits far more answers
 * than the shipped defaults, so setting it against what an LLM auditing every answer would cost
 * compares two bases: with a real judge, the demo's own audits would cost about as much as the full
 * audit. The published figures, on one basis, are in the "With a real judge" strip above the chart.
 */
export function fullAuditNotComparable(roi: RoiSummary | undefined): string | null {
  if (!roi || !savingsLine(roi).freeMockJudge) return null;
  const share = demoAuditShare(roi);
  return `Full audit is not comparable on the simulated demo: its mock judge is free${share ? ` and it ${share}` : ''}. Compare the published figures under “With a real judge” above.`;
}

/**
 * A counterfactual's value label. One from the published assumptions is an Estimate even on a
 * simulated stack: the figure is the published one, and the simulated answer count it is applied
 * to is said in its detail. Everything else on a simulated summary is Simulated (D46).
 */
export function counterfactualLabel(c: Counterfactual, simulated: boolean): ValueLabel {
  return c.method === 'static_assumptions' ? c.label : costLabel(simulated, c.label);
}

/** The value labels of the chart's two comparisons, as their rows in the component table say. */
export function bandLabels(roi: RoiSummary): Record<BandKind, ValueLabel> {
  const fallback: ValueLabel = roi.simulated ? 'simulated' : 'estimate';
  const same = counterfactual(roi, 'llm_same_checks');
  const full = counterfactual(roi, 'llm_full_claim_audit');
  return {
    same_checks: same ? counterfactualLabel(same, roi.simulated) : fallback,
    full_audit: full ? counterfactualLabel(full, roi.simulated) : fallback,
  };
}

/**
 * The points of the Savings chart (D73, no api.ts change):
 * - same-checks band: the bucket's `same_checks_usd` × [low/usd, high/usd] of the summary's
 *   `llm_same_checks`;
 * - full-audit band: the summary's `llm_full_claim_audit` per-1k range × gated / 1000.
 */
export function costBandPoints(series: CostSeries, roi: RoiSummary | undefined): CostBandPoint[] {
  const same = counterfactual(roi, 'llm_same_checks');
  const full = counterfactual(roi, 'llm_full_claim_audit');
  const [sameLo, sameHi] = rangeRatio(same);
  const [fullLo, fullHi] = rangeRatio(full);
  const comparable = !freeMockCounterfactual(full, roi?.simulated ?? false) && fullAuditNotComparable(roi) === null;
  const per1k = full && finite(full.per_1k) && comparable ? full.per_1k : null;
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

/** How close (in % of the plot's height) two end labels may sit before they are pushed apart. */
const LABEL_GAP = 9;

export interface EndLabel {
  key: 'measured' | 'band';
  text: string;
  /** Vertical position in % of the plot, already kept clear of the other label. */
  top: number;
}

/**
 * The direct labels at the right end of the lines: "You $0.0029" at the last bucket's spend and
 * "LLM $0.15" at the band's last estimate (its range when it has one). When the two would overlap
 * they move apart around their midpoint, staying inside the plot.
 */
export function endLabels(
  points: readonly CostBandPoint[],
  band: BandKind | null,
  ranged: boolean,
  y: (v: number) => number,
): EndLabel[] {
  const last = points[points.length - 1];
  if (!last) return [];
  const labels: EndLabel[] = [{ key: 'measured', text: `You ${formatUsd(last.measured)}`, top: y(last.measured) }];
  const b = band ? [...points].reverse().find((p) => p[band])?.[band] : undefined;
  if (b) labels.push({ key: 'band', text: `LLM ${ranged ? costRange(b.low, b.high) : formatUsd(b.high)}`, top: y(ranged ? (b.low + b.high) / 2 : b.high) });
  if (labels.length === 2 && Math.abs(labels[0].top - labels[1].top) < LABEL_GAP) {
    const [upper, lower] = labels[0].top <= labels[1].top ? [labels[0], labels[1]] : [labels[1], labels[0]];
    const mid = Math.min(100 - LABEL_GAP / 2, Math.max(LABEL_GAP / 2, (upper.top + lower.top) / 2));
    upper.top = mid - LABEL_GAP / 2;
    lower.top = mid + LABEL_GAP / 2;
  }
  return labels;
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
const minuteFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' });

/** A bucket's name: the UTC day ("Mon 22 Sep"), the local hour ("Mon 14:00") or minute ("14:02"). */
export function bucketLabel(bucket: CostBucket, iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return (bucket === '1d' ? dayFormat : bucket === '1m' ? minuteFormat : hourFormat).format(t);
}

/** What one bucket is called: "day", "hour" or "minute". */
export function bucketUnit(bucket: CostBucket): 'day' | 'hour' | 'minute' {
  return bucket === '1d' ? 'day' : bucket === '1m' ? 'minute' : 'hour';
}

export function bandWords(kind: BandKind): string {
  return costCounterfactualView(kind === 'same_checks' ? 'llm_same_checks' : 'llm_full_claim_audit').label;
}

/**
 * When the chart's buckets run: "since Mon 14:00" when it starts at the first traffic, else the
 * span in words ("in the last 24 hours", "over the replayed traffic").
 */
export function chartSpan(bucket: CostBucket, since: string | null, spanWords: string): string {
  return since ? `since ${bucketLabel(bucket, since)}` : spanWords;
}

/**
 * The totals the chart's sentence states: the summary's own (the headline and the component table
 * read the same response), so one window never shows two costs. A comparison scaled up from the
 * demo's free mock audits has none, and neither has the full audit on a demo whose mock judge is
 * free (fullAuditNotComparable).
 */
export interface ChartTotals {
  answers: number;
  spent: number;
  bands: Record<BandKind, Band | null>;
}

export function chartTotals(roi: RoiSummary): ChartTotals {
  const total = (c: Counterfactual | null): Band | null => {
    if (!c || !finite(c.usd) || freeMockCounterfactual(c, roi.simulated)) return null;
    return finite(c.low) && finite(c.high) ? { low: Math.min(c.low, c.high), high: Math.max(c.low, c.high) } : { low: c.usd, high: c.usd };
  };
  return {
    answers: roi.gated_responses,
    spent: roi.decision_cost.usd + roi.llm_cost.usd,
    bands: {
      same_checks: total(counterfactual(roi, 'llm_same_checks')),
      full_audit: fullAuditNotComparable(roi) === null ? total(counterfactual(roi, 'llm_full_claim_audit')) : null,
    },
  };
}

/** The chart in one sentence, for screen readers and the figure description. */
export function costBandSummary(totals: ChartTotals, band: BandKind | null, bandLabel: ValueLabel, span: string): string {
  let line = `Checking ${countOf(totals.answers, 'answer')} cost ${formatUsd(totals.spent)} ${span}.`;
  const b = band ? totals.bands[band] : null;
  if (band && b) {
    line += ` ${bandWords(band)} would have cost ${costRange(b.low, b.high)} (${valueLabelView(bandLabel).label.toLowerCase()}).`;
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

/**
 * The Savings component table: what you paid, then what the alternatives would have cost. On a
 * demo whose mock judge is free (`realJudge` set), the alternatives' rows say which published
 * figure in the "With a real judge" strip to read them against, since the demo's simulated totals
 * and the published estimates are not on one basis.
 */
export function componentRows(roi: RoiSummary, realJudge: RealJudgeView | null = null): ComponentRow[] {
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
      // Every audit started counts (a failed one may still have spent on the judge), so they are
      // "attempted", not all completed.
      detail: `${countOf(audits, 'deep audit')} attempted${selections ? `: ${selections}` : ''}`,
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
    // The published assumption says where its number comes from: the server's per-1,000 figure
    // (contracts value claim llm_full_audit_cost_per_1k) times the answers in the window.
    const method =
      c.method === 'static_assumptions' && finite(c.per_1k)
        ? `${costMethodWord(c.method)}: ${formatUsd(c.per_1k)} per 1,000 answers × ${countOf(n, sim ? 'simulated answer' : 'answer')}`
        : costMethodWord(c.method);
    const detail = notMeasured ? `${method} · ${costNotMeasured}` : method;
    rows.push({
      key: c.kind,
      label: costCounterfactualView(c.kind).label,
      detail: realJudge ? `${detail}. ${realJudgeRowNote(c.kind, realJudge)}` : detail,
      total: finite(c.low) && finite(c.high) ? costRange(c.low, c.high) : formatUsd(c.usd),
      per1k: formatUsd(c.per_1k),
      valueLabel: counterfactualLabel(c, sim),
      basis: c.basis,
      notMeasured,
    });
  }
  return rows;
}

/**
 * Which published figure an alternative's row on a free-mock-judge demo is read against: the full
 * audit against TrustAgent with a real judge (not the demo's simulated total, whose deep audits are
 * free), and the same questions against the published figure for a typical request.
 */
function realJudgeRowNote(kind: Counterfactual['kind'], realJudge: RealJudgeView): string {
  if (kind === 'llm_full_claim_audit') {
    return `Compare it with TrustAgent with a real judge (${realJudge.shippedPer1k} per 1,000 answers) above, not with this simulated total`;
  }
  return realJudge.sameChecksPer1k
    ? `Simulated on the demo’s short scripted requests; for a typical 5,000-token request the published figure is ${realJudge.sameChecksPer1k} per 1,000 answers (above)`
    : 'Simulated on the demo’s short scripted requests';
}

/** "about $3", "about $35.50": whole dollars without cents. */
function aboutUsd(usd: number): string {
  return Number.isInteger(usd) ? `$${usd}` : formatUsd(usd);
}

export interface RealJudgeView {
  /** The published total at the shipped defaults, per 1,000 answers ("$3"). */
  shippedPer1k: string;
  /** The same total as a number of dollars (3). */
  shippedPer1kUsd: number;
  /**
   * What the shipped-defaults total per 1,000 answers is made of, each part with its own cost per
   * 1,000 answers: "a fast check on every answer ($0.21) + a deep audit of about 8 in 100 answers
   * (about $2.80)" (value claims fast_check_cost_per_1k, llm_full_audit_cost_per_1k and
   * default_total_cost_per_1k). Never "$0.21 on every answer": that reads as a price per answer.
   */
  shippedParts: string;
  /**
   * How many times less checking with a real judge costs than an LLM auditing every answer
   * ("about 12×"), from the two published figures; null when it is not at least 2×.
   */
  fullAuditMultiple: string | null;
  /** The published figure for an LLM asking the same questions, per 1,000 answers ("$5.75"), or null. */
  sameChecksPer1k: string | null;
  /** How many answers the shipped defaults deep-audit ("about 8 in 100"). */
  shippedShare: string;
  basis: string;
  /** The published full claim-by-claim audit of every answer, per 1,000 answers ("$35.50"). */
  fullAuditPer1k: string;
  fullAuditBasis: string;
  /**
   * What this demo does instead: "This demo deep-audits every answer to show the audit flow: at
   * that rate a real judge would add about $35.50 per 1,000 answers." Null without deep audits.
   */
  demoLine: string | null;
  demoBasis: string;
}

/**
 * What a real judge would cost, for a demo whose mock judge is free (SPEC §7.4; value claims
 * default_total_cost_per_1k and llm_full_audit_cost_per_1k, both estimates). The headline is the
 * published figure at the shipped defaults, never the demo's own deep-audit rate scaled up: the
 * demo audits far more answers than the defaults do, to show the audit flow, and says so in
 * `demoLine` with what that rate would add. Null without the published figures.
 */
export function realJudgeView(
  roi: RoiSummary,
  shipped: typeof costShippedDefaults = costShippedDefaults,
  fullAuditPer1k: number | null = costFullAuditPer1k,
  fullAuditBasis: string = costFullAuditBasis,
  fastCheck: { value: number | null } = costFastCheckPublished,
  sameChecks: { value: number | null } = costSameChecksPublished,
): RealJudgeView | null {
  if (!finite(shipped.per1k) || shipped.per1k <= 0 || !finite(fullAuditPer1k) || fullAuditPer1k <= 0) return null;
  const n = roi.gated_responses;
  const audits = roi.llm_cost.audits;
  let demoLine: string | null = null;
  if (n > 0 && audits > 0) {
    const rate = audits / n;
    const adds = `a real judge would add about ${formatUsd(fullAuditPer1k * rate)} per 1,000 answers`;
    demoLine =
      rate > 1.005
        ? `This demo ran more deep audits than answers checked (${formatCount(audits)} for ${countOf(n, 'answer')}) to show the audit flow: at that rate ${adds}.`
        : `This demo deep-audits ${auditShareWords(rate)} to show the audit flow: at that rate ${adds}.`;
  }
  const shippedShare = `about ${Math.round(shipped.auditShare * 100)} in 100`;
  // Each part in the total's unit (per 1,000 answers), in brackets after what it buys.
  const fast = finite(fastCheck.value) && fastCheck.value > 0 ? `a fast check on every answer (${formatUsd(fastCheck.value)})` : 'a fast check on every answer';
  const deep = `a deep audit of ${shippedShare} answers (about ${formatUsd(shipped.auditShare * fullAuditPer1k)})`;
  const multiple = fullAuditPer1k / shipped.per1k;
  return {
    shippedPer1k: aboutUsd(shipped.per1k),
    shippedPer1kUsd: shipped.per1k,
    shippedParts: `${fast} + ${deep}`,
    fullAuditMultiple: multiple >= 2 ? `about ${Math.round(multiple)}×` : null,
    sameChecksPer1k: finite(sameChecks.value) && sameChecks.value > 0 ? aboutUsd(sameChecks.value) : null,
    shippedShare,
    basis: shipped.basis,
    fullAuditPer1k: aboutUsd(fullAuditPer1k),
    fullAuditBasis,
    demoLine,
    demoBasis: `The published full-audit figure (${aboutUsd(fullAuditPer1k)} per 1,000 answers) times this demo’s deep audits per answer checked (${formatCount(audits)} for ${countOf(n, 'answer')}). Not measured.`,
  };
}

export interface Alternative {
  key: 'llm_same_checks' | 'real_judge' | 'llm_full_claim_audit';
  /** What it is ("An LLM asking the same questions"). */
  label: string;
  /** Its cost per 1,000 answers ("$0.91"). */
  per1k: string;
  valueLabel: ValueLabel;
  basis: string;
  /** How the figure was worked out, in one short line. */
  note: string;
  /** The badge's subject: unique on the page, apart from the component table's rows. */
  subject: string;
}

export interface Alternatives {
  /** "Against the alternatives, per 1,000 answers" or, on a free mock judge, "With a real judge, …". */
  title: string;
  items: Alternative[];
  /**
   * On a free mock judge, what the published figures add up to, in one sentence ("With a real
   * judge, TrustAgent costs about 12× less than …"), an estimate with its basis; null otherwise.
   */
  takeaway: { text: string; basis: string } | null;
}

/** "Published assumptions · not measured". */
const PUBLISHED_NOTE = `${costMethodWord('static_assumptions')} · ${costNotMeasured}`;

/**
 * The comparison beside the headline's cost, all in its unit (per 1,000 answers) and on one basis,
 * so the figures can be read against each other:
 * - on a stack with a real judge, this window's alternatives: an LLM asking the same questions and
 *   an LLM auditing every answer claim by claim, each with the summary's label;
 * - on a demo whose mock judge is free, the published estimates only: TrustAgent with a real judge
 *   at the shipped defaults, an LLM asking the same questions and an LLM auditing every answer.
 *   The demo's simulated same-questions figure stays in the sentences above and the table: set
 *   beside a published one, its smaller simulated token counts would make a real judge look dearer
 *   than asking the same questions.
 * A figure that is missing is left out, never shown as $0, and a full audit scaled up from free
 * mock audits never appears.
 */
export function alternatives(
  roi: RoiSummary,
  realJudge: RealJudgeView | null,
  publishedSame: { value: number | null; basis: string } = costSameChecksPublished,
): Alternatives {
  const sameLabel = costCounterfactualView('llm_same_checks').label;
  const fullLabel = costCounterfactualView('llm_full_claim_audit').label;
  if (realJudge) {
    const items: Alternative[] = [
      {
        key: 'real_judge',
        label: 'TrustAgent with a real judge',
        per1k: realJudge.shippedPer1k,
        valueLabel: 'estimate',
        basis: realJudge.basis,
        note: `Per 1,000 answers at the shipped defaults: ${realJudge.shippedParts}`,
        subject: 'Checking at the shipped defaults with a real judge',
      },
    ];
    if (finite(publishedSame.value) && publishedSame.value > 0) {
      items.push({
        key: 'llm_same_checks',
        label: sameLabel,
        per1k: aboutUsd(publishedSame.value),
        valueLabel: 'estimate',
        basis: publishedSame.basis,
        note: PUBLISHED_NOTE,
        subject: `${sameLabel}, published, per 1,000 answers`,
      });
    }
    items.push({
      key: 'llm_full_claim_audit',
      label: fullLabel,
      per1k: realJudge.fullAuditPer1k,
      valueLabel: 'estimate',
      basis: realJudge.fullAuditBasis,
      note: PUBLISHED_NOTE,
      subject: `${fullLabel}, published, per 1,000 answers`,
    });
    return { title: 'With a real judge, per 1,000 answers', items, takeaway: realJudgeTakeaway(realJudge, publishedSame.value) };
  }
  const items: Alternative[] = [];
  const same = counterfactual(roi, 'llm_same_checks');
  if (same && finite(same.per_1k)) {
    items.push({
      key: 'llm_same_checks',
      label: sameLabel,
      per1k: formatUsd(same.per_1k),
      valueLabel: counterfactualLabel(same, roi.simulated),
      basis: same.basis,
      note: costMethodWord(same.method),
      subject: `${sameLabel}, per 1,000 answers`,
    });
  }
  const full = counterfactual(roi, 'llm_full_claim_audit');
  if (full && finite(full.per_1k) && !freeMockCounterfactual(full, roi.simulated)) {
    items.push({
      key: 'llm_full_claim_audit',
      label: fullLabel,
      per1k: formatUsd(full.per_1k),
      valueLabel: counterfactualLabel(full, roi.simulated),
      basis: full.basis,
      note: costIsMeasuredMethod(full.method) ? costMethodWord(full.method) : PUBLISHED_NOTE,
      subject: `${fullLabel}, per 1,000 answers`,
    });
  }
  return { title: 'Against the alternatives, per 1,000 answers', items, takeaway: null };
}

/**
 * The published figures in one sentence: how many times less a real judge at the shipped defaults
 * costs than an LLM auditing every answer, and (when it is) that it costs less than an LLM asking
 * the same questions although it also deep-audits a share of answers. Null without a multiple.
 */
function realJudgeTakeaway(realJudge: RealJudgeView, sameChecksPer1k: number | null): { text: string; basis: string } | null {
  if (!realJudge.fullAuditMultiple) return null;
  const belowSame = finite(sameChecksPer1k) && sameChecksPer1k > realJudge.shippedPer1kUsd;
  const text =
    `With a real judge, TrustAgent costs ${realJudge.fullAuditMultiple} less than an LLM auditing every answer` +
    (belowSame ? `, and less than an LLM asking the same questions while it also deep-audits ${realJudge.shippedShare} answers.` : '.');
  const basis =
    `${realJudge.fullAuditPer1k} ÷ ${realJudge.shippedPer1k} per 1,000 answers` +
    (belowSame ? `, and ${realJudge.shippedPer1k} against ${aboutUsd(sameChecksPer1k)}` : '') +
    `: published estimates for a typical 5,000-token request, ${costNotMeasured}.`;
  return { text, basis };
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
