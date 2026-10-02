/**
 * Overview numbers of a replay (SPEC §2.4 #9, #47, #50), computed from the traces the tape has
 * played so far, with the labels the recording supports: a mock recording's every figure is
 * `simulated` (SPEC §0 row 4); a recorded one's counts are `measured` from the recording, and
 * its counterfactual an `estimate`. Rates stay hidden below their minimum sample (LabelledRate).
 */
import pricing from '@contracts/pricing/prices.v1.json';
import assumptions from '@contracts/pricing/assumptions.v1.json';
import { outcomeOf } from '@/domain/presentation';
import type {
  AuditDetail,
  AuditSelection,
  ClaimVerdict,
  EvidenceCoverage,
  EvidenceSource,
  GateDecisionView,
  LabelledRate,
  OverviewStats,
  Recording,
  RoiSummary,
  RoiTimeseries,
  TimeWindow,
  TraceSummary,
} from '@/types/api';

/** One played trace: its summary and, when played, its decision and deep audit. */
export interface PlayedTrace {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  audit: AuditDetail | null;
}

export interface StatsContext {
  recording: Pick<Recording, 'kind' | 'recorded_at' | 'label'>;
  /** Wall-clock time of the tape position (the end of the played window). */
  now: string;
  window: TimeWindow;
}

const MIN_N = 30;
const SOURCES: EvidenceSource[] = ['request_system', 'request_context', 'tool_results', 'extension', 'knowledge'];
const NO_VERDICTS: Record<ClaimVerdict, number> = {
  SUPPORTED: 0,
  PARTIALLY_SUPPORTED: 0,
  CONTRADICTED: 0,
  UNSUPPORTED: 0,
  UNKNOWN: 0,
  NOT_CHECKED: 0,
};

function simulated(ctx: StatsContext): boolean {
  return ctx.recording.kind === 'mock';
}

/** Nearest-rank percentile of the non-null values, or null without any. */
export function percentile(values: readonly (number | null | undefined)[], p: number): number | null {
  const xs = values.filter((v): v is number => typeof v === 'number' && Number.isFinite(v)).sort((a, b) => a - b);
  if (xs.length === 0) return null;
  const rank = Math.max(1, Math.ceil((p / 100) * xs.length));
  return xs[Math.min(rank, xs.length) - 1];
}

function p50p95(values: readonly (number | null | undefined)[]): { p50: number | null; p95: number | null } {
  return { p50: percentile(values, 50), p95: percentile(values, 95) };
}

/** Wilson 95% interval of k successes in n. */
function wilson(k: number, n: number): [number, number] | null {
  if (n === 0) return null;
  const z = 1.959963984540054;
  const p = k / n;
  const denom = 1 + (z * z) / n;
  const centre = (p + (z * z) / (2 * n)) / denom;
  const half = (z * Math.sqrt((p * (1 - p)) / n + (z * z) / (4 * n * n))) / denom;
  return [Math.max(0, centre - half), Math.min(1, centre + half)];
}

function rate(k: number, n: number, basis: string, ctx: StatsContext): LabelledRate {
  return {
    value: n >= MIN_N ? k / n : null,
    label: simulated(ctx) ? 'simulated' : 'measured',
    basis,
    n,
    ci95: n >= MIN_N ? wilson(k, n) : null,
    min_n: MIN_N,
  };
}

function bucketOf(spanMs: number): OverviewStats['bucket'] {
  if (spanMs <= 3_600_000) return '1m';
  if (spanMs <= 86_400_000) return '15m';
  if (spanMs <= 7 * 86_400_000) return '6h';
  return '1d';
}

const BUCKET_MS: Record<OverviewStats['bucket'], number> = { '1m': 60_000, '15m': 900_000, '6h': 21_600_000, '1d': 86_400_000 };

type SeriesPoint = OverviewStats['outcomes_series'][number];
type SeriesKey = Exclude<keyof SeriesPoint, 't'>;

/** Outcome id → the outcomes_series member it is counted in. */
const SERIES_OF: Record<string, SeriesKey | null> = {
  passed: 'passed',
  would_flag: 'would_flag',
  would_block: 'would_block',
  flagged: 'flagged',
  blocked: 'blocked',
  replaced: 'replaced',
  not_checked: 'not_checked',
  error: 'error',
  checking: null,
};

const CAUGHT = new Set(['flagged', 'blocked', 'replaced', 'would_flag', 'would_block']);

const auditDone = (t: TraceSummary) => t.audit.status === 'completed' || t.audit.status === 'degraded';

function claimCounts(audits: readonly AuditDetail[]): Record<ClaimVerdict, number> {
  const out = { ...NO_VERDICTS };
  for (const a of audits) for (const [k, v] of Object.entries(a.verdict_counts)) out[k as ClaimVerdict] += v;
  return out;
}

export function overviewStats(played: readonly PlayedTrace[], ctx: StatsContext): OverviewStats {
  const traces = played.map((p) => p.summary);
  const audits = played.map((p) => p.audit).filter((a): a is AuditDetail => a !== null);
  const from = ctx.recording.recorded_at;
  const to = ctx.now;
  const bucket = bucketOf(Date.parse(to) - Date.parse(from));
  const step = BUCKET_MS[bucket];
  const start = Math.floor(Date.parse(from) / step) * step;
  const end = Math.floor(Date.parse(to) / step) * step;

  const series: SeriesPoint[] = [];
  for (let t = start; t <= end; t += step) {
    series.push({ t: new Date(t).toISOString(), passed: 0, would_flag: 0, would_block: 0, flagged: 0, blocked: 0, replaced: 0, not_checked: 0, error: 0 });
  }
  const caught = new Map<string, { count: number; latest: TraceSummary }>();
  for (const t of traces) {
    const outcome = outcomeOf(t);
    const key = SERIES_OF[outcome];
    const i = Math.floor((Date.parse(t.created_at) - start) / step);
    if (key && series[i]) series[i][key] += 1;
    if (CAUGHT.has(outcome)) {
      for (const reason of t.reasons) {
        const c = caught.get(reason);
        if (!c) caught.set(reason, { count: 1, latest: t });
        else {
          c.count += 1;
          if (Date.parse(t.created_at) > Date.parse(c.latest.created_at)) c.latest = t;
        }
      }
    }
  }

  const count = (pred: (t: TraceSummary) => boolean) => traces.filter(pred).length;
  const decisions = played.map((p) => p.decision).filter((d): d is GateDecisionView => d !== null);
  const groundednessAssessed = decisions.filter((d) => Object.values(d.evaluators).some((e) => e.ref.startsWith('groundedness@') && e.status === 'assessed')).length;
  const bySource = Object.fromEntries(SOURCES.map((s) => [s, count((t) => (t.checked_against?.used ?? []).some((u) => u.source === s && u.items > 0))])) as Record<EvidenceSource, number>;
  const uniform = audits.filter((a) => a.selection === 'uniform');

  const contradicted = new Map<string, { count: number; latest: string; at: number }>();
  for (const a of audits) {
    for (const c of a.claims) {
      if (c.status !== 'CONTRADICTED') continue;
      const k = c.claim ?? '';
      const prev = contradicted.get(k);
      const at = Date.parse(a.created_at);
      if (!prev) contradicted.set(k, { count: 1, latest: a.trace_id, at });
      else {
        prev.count += 1;
        if (at > prev.at) Object.assign(prev, { latest: a.trace_id, at });
      }
    }
  }

  const group = <K extends 'route' | 'model'>(key: K) => {
    const names = [...new Set(traces.map((t) => t[key] ?? 'unknown'))].sort();
    return names.map((name) => {
      const mine = traces.filter((t) => (t[key] ?? 'unknown') === name);
      const mineUniform = uniform.filter((a) => mine.some((t) => t.trace_id === a.trace_id));
      return {
        name,
        requests: mine.length,
        flagged: mine.filter((t) => t.action === 'flag').length,
        blocked: mine.filter((t) => t.action === 'block').length,
        audited: mine.filter(auditDone).length,
        hallucination_rate: rate(mineUniform.filter((a) => a.hallucination_detected).length, mineUniform.length, 'uniform-sample deep audits only', ctx),
      };
    });
  };

  const stepTimings = (k: string) => audits.map((a) => a.step_timings[k] ?? null);
  const hasDecision = traces.filter((t) => t.decision_model !== null);
  return {
    window: ctx.window,
    bucket,
    from,
    to,
    include_mock: simulated(ctx),
    totals: {
      requests: traces.length,
      observed: count((t) => t.action === 'observe'),
      passed: count((t) => t.action === 'pass'),
      flagged: count((t) => t.action === 'flag'),
      blocked: count((t) => t.action === 'block'),
      fallback: count((t) => t.action === 'fallback'),
      not_assessed: count((t) => t.gate_status === 'not_assessed'),
      gate_errors: count((t) => t.gate_status === 'error' || t.gate_status === 'timeout'),
      audited: count(auditDone),
      audits_failed: count((t) => t.audit.status === 'failed'),
      decision_cost_usd: traces.reduce((acc, t) => acc + (t.decision_model?.cost_usd ?? 0), 0),
      llm_cost_usd: audits.reduce((acc, a) => acc + a.costs.reduce((s, c) => s + c.cost_usd, 0), 0),
    },
    hallucination_rate: rate(uniform.filter((a) => a.hallucination_detected).length, uniform.length, 'uniform-sample deep audits only', ctx),
    audited_hallucination_share: rate(
      audits.filter((a) => a.hallucination_detected).length,
      audits.length,
      'among audited answers (selected by risk; not a traffic rate)',
      ctx,
    ),
    evidence_coverage: {
      with_evidence: count((t) => (t.checked_against?.used ?? []).some((u) => u.items > 0)),
      groundedness_assessed: groundednessAssessed,
      rate: traces.length > 0 ? groundednessAssessed / traces.length : null,
      by_source: bySource,
    },
    outcomes_series: series,
    what_was_caught: [...caught.entries()].map(([reason, c]) => ({ reason, count: c.count, latest_trace_id: c.latest.trace_id })),
    latency_ms: {
      overhead: p50p95(traces.map((t) => t.timings.overhead_ms)),
      gate: p50p95(traces.map((t) => t.timings.gate_ms)),
      upstream: p50p95(traces.map((t) => t.timings.upstream_ms)),
      audit: p50p95(audits.map((a) => a.processing_ms)),
    },
    by_route: group('route').map(({ name, ...g }) => ({ route: name, ...g })),
    by_model: group('model').map(({ name, requests, audited, hallucination_rate }) => ({ model: name, requests, audited, hallucination_rate })),
    claim_verdicts_among_audited: claimCounts(audits),
    top_contradicted: [...contradicted.entries()]
      .sort((a, b) => b[1].count - a[1].count)
      .map(([claim, c]) => ({ claim, count: c.count, latest_trace_id: c.latest })),
    stage_latency_ms: {
      detectors: { p50: null, p95: null },
      evidence: { p50: null, p95: null },
      knowledge: { p50: null, p95: null },
      decision: p50p95(decisions.map((d) => d.decision_model?.latency_ms ?? null)),
      queue_wait: { p50: null, p95: null },
      decompose: p50p95(stepTimings('decompose')),
      retrieve: { p50: null, p95: null },
      verify: p50p95(stepTimings('verify')),
      score: p50p95(stepTimings('score')),
    },
    backend_mix: hasDecision.length > 0 ? countBy(hasDecision.map((t) => t.decision_model?.backend ?? 'off')) : {},
  };
}

function countBy<T extends string>(xs: readonly T[]): Partial<Record<T, number>> {
  const out: Partial<Record<T, number>> = {};
  for (const x of xs) out[x] = (out[x] ?? 0) + 1;
  return out;
}

// ------------------------------------------------------------------------------------ ROI

interface PriceEntry {
  match: string;
  provider: string;
  input_per_mtok: number;
  output_per_mtok: number;
}

function judgePrice(): PriceEntry | null {
  const want = assumptions.prices.judge;
  return (pricing.entries as PriceEntry[]).find((e) => e.provider === want.provider && e.match === want.match) ?? null;
}

const isGated = (p: PlayedTrace) => p.summary.gate_status !== 'pending' && p.summary.gate_status !== 'skipped';
const decisionCost = (p: PlayedTrace) => p.summary.decision_model?.cost_usd ?? 0;
const auditCost = (a: AuditDetail | null) => (a ? a.costs.reduce((s, c) => s + c.cost_usd, 0) : 0);

/**
 * The token model of pricing/assumptions.v1.json for one gated answer: its fast-check request
 * (plus the prompt overhead) asked of the judge, with a short answer per question. 0 when the
 * recording has no token count for it.
 */
function sameChecksCost(p: PlayedTrace, price: PriceEntry): number {
  const d = p.decision;
  if (!d?.decision_model || d.decision_model.input_tokens === null) return 0;
  const model = assumptions.token_model;
  const questions = Object.keys(d.evaluators).length;
  return (
    ((d.decision_model.input_tokens + model.prompt_overhead_tokens) * price.input_per_mtok +
      model.output_tokens_per_question * questions * price.output_per_mtok) /
    1e6
  );
}

export function roiSummary(played: readonly PlayedTrace[], ctx: StatsContext): RoiSummary {
  const sim = simulated(ctx);
  const traces = played.map((p) => p.summary);
  const gated = played.filter(isGated);
  const n = gated.length;
  const audits = played.map((p) => p.audit).filter((a): a is AuditDetail => a !== null);
  const decisionUsd = played.reduce((acc, p) => acc + decisionCost(p), 0);
  const llmUsd = audits.reduce((acc, a) => acc + auditCost(a), 0);
  const per1k = (usd: number) => (n > 0 ? (usd / n) * 1000 : null);

  const price = judgePrice();
  const model = assumptions.token_model;
  const sameUsd: number | null = price ? gated.reduce((acc, p) => acc + sameChecksCost(p, price), 0) : null;
  const bySelection: Partial<Record<AuditSelection, number>> = countBy(audits.map((a) => a.selection).filter((s): s is AuditSelection => s !== null));
  const costLabel = sim ? 'simulated' : 'measured';
  const decisionBasis = sim
    ? 'Mock fast check priced at the Jev list price per input token; no call was billed.'
    : 'Fast-check cost reported with the recorded calls.';
  const llmBasis = sim ? 'Mock judge: no LLM call was made, so the deep audit cost nothing.' : 'Deep-audit cost reported with the recorded audits.';
  const gateTimes = gated.map((p) => p.summary.timings.gate_ms);

  return {
    window: ctx.window,
    gated_responses: n,
    simulated: sim,
    decision_cost: { value: per1k(decisionUsd), label: costLabel, basis: decisionBasis, usd: decisionUsd, per_1k: per1k(decisionUsd) },
    llm_cost: { value: llmUsd, label: costLabel, basis: llmBasis, usd: llmUsd, audits: audits.length, by_selection: bySelection },
    total_per_1k: {
      value: per1k(decisionUsd + llmUsd),
      label: costLabel,
      basis: `Fast check plus deep audit per 1,000 answers in this ${sim ? 'simulated ' : ''}recording.`,
    },
    counterfactuals:
      sameUsd === null
        ? []
        : [
            {
              kind: 'llm_same_checks',
              method: 'token_model',
              usd: sameUsd,
              low: null,
              high: null,
              per_1k: per1k(sameUsd),
              n,
              label: sim ? 'simulated' : 'estimate',
              basis: `The same fast-check requests (plus ${model.prompt_overhead_tokens} prompt tokens) asked of the LLM judge at $${price?.input_per_mtok}/$${price?.output_per_mtok} per million tokens.`,
            },
          ],
    savings_vs_same_checks: {
      usd_low: sameUsd === null ? null : sameUsd - decisionUsd,
      usd_high: sameUsd === null ? null : sameUsd - decisionUsd,
      label: sim ? 'simulated' : 'estimate',
    },
    outcomes: {
      flagged: traces.filter((t) => t.action === 'flag').length,
      blocked: traces.filter((t) => t.action === 'block').length,
      fallback: traces.filter((t) => t.action === 'fallback').length,
      not_assessed: traces.filter((t) => t.gate_status === 'not_assessed').length,
      label: costLabel,
    },
    latency_ms: { gate_p50: percentile(gateTimes, 50), gate_p95: percentile(gateTimes, 95), deadline_miss_rate: null, label: costLabel },
    // A recording does not carry the gateway's sampling state; nothing here was sampled uniformly.
    sampling: {
      uniform_rate: 0,
      uniform_today: audits.filter((a) => a.selection === 'uniform').length,
      uniform_cap: 50,
      uniform_stopped_at: null,
      llm_cap_hit_at: null,
    },
    third_party: [],
  };
}

const ROI_BUCKET_MS: Record<RoiTimeseries['bucket'], number> = { '1h': 3_600_000, '1d': 86_400_000 };

/**
 * Spend per UTC bucket over the played traces (SPEC §2.4 #48): every bucket from the recording's
 * start to the tape position, empty ones zero-filled, as the gateway serves them. An answer's
 * fast-check and deep-audit costs land in the bucket it was created in; the same-checks
 * counterfactual is the token model above (null without the judge's price).
 */
export function roiTimeseries(played: readonly PlayedTrace[], ctx: StatsContext, bucket: RoiTimeseries['bucket']): RoiTimeseries {
  const step = ROI_BUCKET_MS[bucket];
  const from = Date.parse(ctx.recording.recorded_at);
  const to = Date.parse(ctx.now);
  const points: RoiTimeseries['points'] = [];
  if (Number.isNaN(from) || Number.isNaN(to)) return { window: ctx.window, bucket, points };
  const price = judgePrice();
  const start = Math.floor(from / step) * step;
  const end = Math.floor(Math.max(from, to) / step) * step;
  for (let t = start; t <= end; t += step) {
    points.push({ t: new Date(t).toISOString(), decision_usd: 0, llm_usd: 0, same_checks_usd: price ? 0 : null, gated: 0 });
  }
  for (const p of played) {
    const created = Date.parse(p.summary.created_at);
    const i = Math.floor((created - start) / step);
    const point = points[i];
    if (Number.isNaN(created) || point === undefined) continue;
    point.decision_usd += decisionCost(p);
    point.llm_usd += auditCost(p.audit);
    if (!isGated(p)) continue;
    point.gated += 1;
    if (price && point.same_checks_usd !== null) point.same_checks_usd += sameChecksCost(p, price);
  }
  return { window: ctx.window, bucket, points };
}

// ------------------------------------------------------------------------------------ coverage

export function evidenceCoverage(played: readonly PlayedTrace[], ctx: StatsContext, route?: string): EvidenceCoverage {
  const mine = played.filter((p) => !route || p.summary.route === route);
  const reasons = new Map<EvidenceCoverage['top_reasons'][number]['reason'], number>();
  let assessed = 0;
  for (const p of mine) {
    const g = p.decision ? Object.values(p.decision.evaluators).find((e) => e.ref.startsWith('groundedness@')) : undefined;
    if (g?.status === 'assessed') {
      assessed += 1;
      continue;
    }
    const why = g?.reason === 'evidence_too_short' || g?.reason === 'evidence_packed_away' || g?.reason === 'too_long' ? g.reason : 'no_evidence';
    reasons.set(why, (reasons.get(why) ?? 0) + 1);
  }
  const summaries = mine.map((p) => p.summary);
  return {
    window: ctx.window,
    route: route ?? null,
    requests: mine.length,
    with_evidence: summaries.filter((t) => (t.checked_against?.used ?? []).some((u) => u.items > 0)).length,
    groundedness_assessed: assessed,
    by_source: Object.fromEntries(
      SOURCES.map((s) => [s, summaries.filter((t) => (t.checked_against?.used ?? []).some((u) => u.source === s && u.items > 0)).length]),
    ) as Record<EvidenceSource, number>,
    top_reasons: [...reasons.entries()].map(([reason, count]) => ({ reason, count })),
  };
}
