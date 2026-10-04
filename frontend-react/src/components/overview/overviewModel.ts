/**
 * Pure transforms from the Overview endpoints (SPEC §2.4 #9, #47, #50) to what the page draws:
 * chart buckets, the "What was caught" bars, the savings sentence, the setup checklist and the
 * evidence breakdown. Kept apart from the components so each rule is unit-tested.
 */
import type { OutcomeBucket } from '@/components/charts/OutcomeTimeSeries';
import { evidenceGapView } from '@/domain/controlPresentation';
import { formatCount, formatUsd } from '@/domain/format';
import { copy, evidenceSourceLabel, fillTemplate, reasonCopy } from '@/domain/presentation';
import { formatPercent, share } from '@/domain/rates';
import type {
  EvidenceCoverage,
  EvidenceSource,
  LabelledValue,
  OverviewStats,
  RoiSummary,
  SystemStatus,
  TimeWindow,
  ValueLabel,
} from '@/types/api';

// ------------------------------------------------------------------------------ time window

export const TIME_WINDOWS: readonly { value: TimeWindow; label: string; long: string }[] = [
  { value: '1h', label: '1h', long: 'the last hour' },
  { value: '24h', label: '24h', long: 'the last 24 hours' },
  { value: '7d', label: '7d', long: 'the last 7 days' },
  { value: '30d', label: '30d', long: 'the last 30 days' },
];

export const DEFAULT_WINDOW: TimeWindow = '24h';

export function windowLong(window: TimeWindow): string {
  return TIME_WINDOWS.find((w) => w.value === window)?.long ?? (window === '15m' ? 'the last 15 minutes' : window);
}

const BUCKET_WORDS: Record<OverviewStats['bucket'], string> = {
  '1m': 'Per minute',
  '15m': 'Per 15 minutes',
  '6h': 'Per 6 hours',
  '1d': 'Per day',
};

export function bucketDescription(stats: Pick<OverviewStats, 'bucket' | 'window'>): string {
  return `${BUCKET_WORDS[stats.bucket] ?? 'Per interval'}, ${windowLong(stats.window)}`;
}

const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' });
const day = new Intl.DateTimeFormat('en-GB', { month: 'short', day: 'numeric' });
const dayClock = new Intl.DateTimeFormat('en-GB', { weekday: 'short', hour: '2-digit', minute: '2-digit' });

/** Axis label for a bucket: the clock within a day, the day and hour for 6 h buckets, the date for days. */
export function bucketFormatter(bucket: OverviewStats['bucket']): (iso: string) => string {
  const fmt = bucket === '1d' ? day : bucket === '6h' ? dayClock : clock;
  return (iso) => {
    const t = Date.parse(iso);
    return Number.isNaN(t) ? iso : fmt.format(t);
  };
}

// ---------------------------------------------------------------------------------- charts

/** outcomes_series → stacked buckets. Series names match the outcome ids of ui/outcomes.v1.json. */
export function outcomeBuckets(series: OverviewStats['outcomes_series']): OutcomeBucket[] {
  return series.map((p) => ({
    t: p.t,
    counts: {
      passed: p.passed,
      would_flag: p.would_flag,
      would_block: p.would_block,
      flagged: p.flagged,
      blocked: p.blocked,
      replaced: p.replaced,
      not_checked: p.not_checked,
      error: p.error,
    },
  }));
}

export interface CaughtRow {
  key: string;
  reason: string;
  label: string;
  count: number;
  latestTraceId: string;
}

/** "What was caught": top reasons in plain words, most frequent first. */
export function caughtRows(stats: Pick<OverviewStats, 'what_was_caught'>, limit = 6): CaughtRow[] {
  return [...stats.what_was_caught]
    .sort((a, b) => b.count - a.count)
    .slice(0, limit)
    .map((c) => ({ key: c.reason, reason: c.reason, label: reasonCopy(c.reason), count: c.count, latestTraceId: c.latest_trace_id }));
}

// ---------------------------------------------------------------------------------- savings

export interface SavingsLine {
  /** "Your cost: $0.80 per 1,000 answers (fast check $0.045 + deep audit $0.76)". */
  headline: string;
  label: ValueLabel;
  /** "So far: 540 answers checked for $0.43. Asking an LLM …", when there is a counterfactual. */
  earlyData: string | null;
  earlyDataLabel: ValueLabel | null;
  /** Answers stopped before users saw them (block + fallback) and answers flagged. */
  stopped: number;
  flagged: number;
  outcomesLabel: 'measured' | 'simulated';
  /**
   * The deep audit reads $0 only because the demo's mock judge is free (simulated audits, no
   * spend): the page says so next to the headline.
   */
  freeMockJudge: boolean;
}

function perThousand(usd: number, answers: number): number | null {
  return answers > 0 ? (usd / answers) * 1000 : null;
}

/** The least certain label wins: a sum of a measured and an estimated number is an estimate. */
export function weakestLabel(labels: readonly ValueLabel[]): ValueLabel {
  const order: ValueLabel[] = ['simulated', 'estimate', 'third_party', 'vendor', 'measured'];
  for (const l of order) if (labels.includes(l)) return l;
  return 'measured';
}

export function savingsLine(roi: RoiSummary): SavingsLine {
  const n = roi.gated_responses;
  const fastPer1k = roi.decision_cost.per_1k ?? perThousand(roi.decision_cost.usd, n);
  const auditPer1k = perThousand(roi.llm_cost.usd, n);
  const headline = fillTemplate(copy.savings.headline, {
    total_per_1k: formatUsd(roi.total_per_1k.value),
    fast_check: formatUsd(fastPer1k),
    deep_audit: formatUsd(auditPer1k),
  });
  const same = roi.counterfactuals.find((c) => c.kind === 'llm_same_checks' && c.usd !== null) ?? null;
  const earlyData = same
    ? fillTemplate(copy.savings.early_data, {
        n: formatCount(n),
        cost: formatUsd(roi.decision_cost.usd + roi.llm_cost.usd),
        counterfactual: formatUsd(same.usd),
      })
    : null;
  return {
    headline,
    label: roi.simulated ? 'simulated' : weakestLabel([roi.total_per_1k.label, roi.decision_cost.label, roi.llm_cost.label]),
    earlyData,
    earlyDataLabel: same ? (roi.simulated ? 'simulated' : same.label) : null,
    stopped: roi.outcomes.blocked + roi.outcomes.fallback,
    flagged: roi.outcomes.flagged,
    outcomesLabel: roi.simulated ? 'simulated' : roi.outcomes.label,
    freeMockJudge: roi.simulated && roi.llm_cost.audits > 0 && roi.llm_cost.usd === 0,
  };
}

/** A LabelledValue built on the spot (a sum shown with the weakest of its parts' labels). */
export function derivedValue(value: number | null, label: ValueLabel, basis: string): LabelledValue {
  return { value, label, basis };
}

// ---------------------------------------------------------------------------------- evidence

export const EVIDENCE_SOURCES: readonly EvidenceSource[] = ['request_system', 'request_context', 'tool_results', 'extension', 'knowledge'];

export interface CoverageSummary {
  /** Share of requests whose groundedness was checked, or null without traffic. */
  rate: number | null;
  /** "Groundedness checked on 85% of requests". */
  headline: string;
  bySource: { source: EvidenceSource; label: string; count: number; share: number | null }[];
  gaps: { reason: string; label: string; hint: string; count: number }[];
  requests: number;
}

export function coverageSummary(
  requests: number,
  coverage: Pick<EvidenceCoverage, 'groundedness_assessed' | 'by_source'> & { top_reasons?: EvidenceCoverage['top_reasons'] },
): CoverageSummary {
  const rate = share(coverage.groundedness_assessed, requests);
  return {
    rate,
    requests,
    headline: fillTemplate(copy.overview.evidence_coverage, { rate: formatPercent(rate) }),
    bySource: EVIDENCE_SOURCES.map((source) => {
      const count = coverage.by_source[source] ?? 0;
      return { source, label: evidenceSourceLabel(source), count, share: share(count, requests) };
    }),
    gaps: (coverage.top_reasons ?? [])
      .filter((g) => g.count > 0)
      .sort((a, b) => b.count - a.count)
      .map((g) => ({ reason: g.reason, count: g.count, ...evidenceGapView(g.reason) })),
  };
}

// ---------------------------------------------------------------------------------- checklist

export interface ChecklistItem {
  id: 'secure' | 'fast_check' | 'judge' | 'database' | 'first_request' | 'evidence';
  label: string;
  done: boolean;
  /** Shown while not done: what to do next. */
  hint: string;
  /** Where the fix is (a route inside the app); a setup-wizard fix names its step and its link words. */
  fix:
    | { to: '/app/settings/$section'; section: 'connections' | 'models' | 'addons' }
    | { to: '/app/playground' | '/app/policies' | '/app/overview' }
    | { to: '/app/setup'; search: { step: 'secure' | 'connect' }; label: string }
    | null;
}

/**
 * The setup checklist (SPEC §3.3 Overview). Each item is derived from state the dashboard already
 * has; an unknown state (no /api/status) is left out rather than guessed.
 */
export function setupChecklist(input: {
  status: SystemStatus | null;
  requests: number | null;
  firstRequestSeen: boolean;
  evidenceRequests: number | null;
}): ChecklistItem[] {
  const { status } = input;
  const items: ChecklistItem[] = [];
  if (status) {
    const dm = status.decision_model.state;
    items.push({
      id: 'secure',
      label: copy.setup.step_secure.title,
      // An admin key exists: the one-time setup link is used up.
      done: !status.auth.setup_required,
      hint: 'Claim the one-time setup link to create the admin key.',
      fix: { to: '/app/setup', search: { step: 'secure' }, label: 'Open setup' },
    });
    items.push({
      id: 'fast_check',
      label: `${copy.tiers.fast_check} connected`,
      done: dm === 'ready' || dm === 'mock' || dm === 'degraded',
      hint: 'Connect OpenRouter so every answer gets a fast check.',
      fix: { to: '/app/setup', search: { step: 'connect' }, label: 'Connect OpenRouter' },
    });
    items.push({
      id: 'judge',
      label: `${copy.tiers.deep_audit} judge ready`,
      done: status.judge.state === 'up',
      hint: 'The deep audit needs an LLM judge (the same OpenRouter key works).',
      fix: { to: '/app/settings/$section', section: 'models' },
    });
    items.push({
      id: 'database',
      label: 'History saved in a database',
      done: status.persistence.configured && status.persistence.state === 'up',
      hint: 'Without a database, traces, policy versions and labels are lost on restart.',
      fix: null,
    });
  }
  const seen = input.firstRequestSeen || (input.requests ?? 0) > 0;
  items.push({
    id: 'first_request',
    label: 'First request through the gateway',
    done: seen,
    hint: 'Point your app at the gateway, or send one from the Playground.',
    fix: { to: '/app/playground' },
  });
  if (input.evidenceRequests !== null) {
    items.push({
      id: 'evidence',
      label: 'Requests carry documents to check against',
      done: input.evidenceRequests > 0,
      hint: 'Attach documents with trustagent.context so groundedness can be checked.',
      fix: { to: '/app/overview' },
    });
  }
  return items;
}
