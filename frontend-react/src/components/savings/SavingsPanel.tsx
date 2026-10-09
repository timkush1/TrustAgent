/**
 * The Overview "Savings" tab (SPEC §7.4 "Panel", §3.3; SV-2): what checking costs and
 * what it saves, where every number says where it comes from.
 *
 * Top to bottom: the headline ("Your cost: $X per 1,000 answers (fast check $a + deep audit $b)")
 * with the savings range against an LLM asking the same questions, then the alternatives side by
 * side in the headline's unit (per 1,000 answers); the spend line over the counterfactual bands
 * (with its table); the component table, every figure with its value label (Measured, Estimate,
 * Third-party, Vendor or Simulated); what was stopped (block and fallback) apart from what was
 * flagged; fast-check speed; and the sampling status, including when the deep-audit budget ran out.
 *
 * On a demo whose mock judge is free, the alternatives include what checking costs with a real
 * judge at the shipped defaults (the published estimate), and a line below says what this demo's
 * own, far higher, deep-audit rate would add. The static replay words every span "over the
 * replayed traffic" and says the recording carries no sampling state, since it has no window to
 * pick and no gateway to ask.
 *
 * Page contract: the delayed skeleton, the server's message with Retry, a degraded state for a
 * proxy that does not report costs (an older version), an empty state before the first checked
 * answer, and simulated labels on a demo or mock stack (D46). Nothing here writes.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, Flag, Gauge, PiggyBank, ShieldX } from 'lucide-react';
import type { ReactNode } from 'react';
import { useDataSource, useRuntimeConfig } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { savingsLine, windowLong } from '@/components/overview/overviewModel';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { isNotServed, roiSummaryQuery } from '@/data/queries';
import { roiBucket, roiTimeseriesQuery } from '@/data/queries/costs';
import { formatCount, formatDurationMs } from '@/domain/format';
import { copy, costLabel, costPriceLineIsOverride } from '@/domain/presentation';
import { formatPercent } from '@/domain/rates';
import { cn } from '@/lib/cn';
import type { RoiSummary, TimeWindow } from '@/types/api';
import { CostBandChart } from './CostBandChart';
import {
  alternatives,
  bandLabels,
  chartTotals,
  componentRows,
  costBandPoints,
  countOf,
  outcomeSplit,
  realJudgeView,
  samplingLines,
  savingsRange,
  type Alternative,
  type RealJudgeView,
  type SavingsRange,
} from './savingsModel';

const linkClass = 'inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2';

/** The span every figure covers, in words: the window's, or the static replay's recording. */
const REPLAY_SPAN = 'over the replayed traffic';
const spanWords = (timeWindow: TimeWindow, replay: boolean): string => (replay ? REPLAY_SPAN : `in ${windowLong(timeWindow)}`);

function SavingsSkeleton() {
  return (
    <div aria-busy="true" className="space-y-4">
      <span role="status" className="sr-only">
        {copy.states.loading}
      </span>
      <Skeleton className="h-24 w-full" />
      <Skeleton className="h-64 w-full" />
      <div className="grid gap-4 lg:grid-cols-2">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    </div>
  );
}

/** The words around the savings amounts, by which way the range points. */
const SAVED_WORDS: Record<SavingsRange['direction'], [string, string]> = {
  saved: ['Saved ', ' against'],
  cost_more: ['Cost ', ' more than'],
  mixed: ['Between ', ' than'],
};

/** Tailwind needs whole class names: the strip's columns by how many alternatives it has. */
const ALTERNATIVE_COLS: Record<number, string> = { 1: 'sm:grid-cols-1', 2: 'sm:grid-cols-2', 3: 'sm:grid-cols-3' };

/**
 * The alternatives in the headline's unit, side by side: one surface with hairline dividers, not a
 * row of stat cards (DESIGN.md), each figure with its own value label.
 */
function Alternatives({ items }: { items: Alternative[] }) {
  return (
    <div className="space-y-1.5 border-t border-border pt-3">
      <h3 id="savings-alternatives" className="text-sm font-medium text-fg">
        Against the alternatives, per 1,000 answers
      </h3>
      <dl
        aria-labelledby="savings-alternatives"
        className={cn(
          'grid grid-cols-1 divide-border rounded-md border border-border sm:divide-x max-sm:[&>*]:border-b max-sm:[&>*]:border-border max-sm:[&>*:last-child]:border-b-0',
          ALTERNATIVE_COLS[items.length],
        )}
      >
        {items.map((a) => (
          <div key={a.key} data-alternative={a.key} className="min-w-0 space-y-1 px-3 py-2.5">
            <dt className="text-xs text-fg-muted">{a.label}</dt>
            <dd className="flex flex-wrap items-center gap-2">
              <span className="text-lg font-semibold text-fg tabular">{a.per1k}</span>
              <EvidenceBadge label={a.valueLabel} basis={a.basis} subject={a.subject} />
            </dd>
            <dd className="text-xs text-fg-muted">{a.note}</dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

/**
 * Why the deep audit reads $0 on a demo whose mock judge is free, and what a real judge would add
 * at this demo's own deep-audit rate, which is far above the shipped defaults to show the audit flow.
 */
function FreeMockJudge({ view }: { view: RealJudgeView | null }) {
  return (
    <p className="text-sm text-fg-muted">
      <span className="tabular">
        The deep audit reads $0 because the demo&rsquo;s mock judge is free.{' '}
        {view?.demoLine ?? 'With a real judge, every deep audit adds an LLM cost.'}
      </span>
      {view?.demoLine ? (
        <>
          {' '}
          <EvidenceBadge label="estimate" basis={view.demoBasis} subject="What a real judge would add at this demo’s rate" className="align-middle" />
        </>
      ) : null}
    </p>
  );
}

/**
 * The headline, the savings range and the early-data sentence; then the alternatives per 1,000
 * answers (on a free mock judge, with a real judge's cost at the shipped defaults among them).
 */
function Headline({ roi }: { roi: RoiSummary }) {
  const line = savingsLine(roi);
  const saved = savingsRange(roi);
  const same = roi.counterfactuals.find((c) => c.kind === 'llm_same_checks');
  const realJudge = line.freeMockJudge ? realJudgeView(roi) : null;
  const others = alternatives(roi, realJudge);
  return (
    <section aria-labelledby="savings-headline" className="space-y-3 rounded-lg border border-border bg-surface px-4 py-4">
      <div className="space-y-2">
        <h2 id="savings-headline" className="flex flex-wrap items-center gap-2 text-lg font-semibold text-fg">
          <span className="tabular">{line.headline}</span>
          <EvidenceBadge label={line.label} basis={roi.total_per_1k.basis} subject="Your cost per 1,000 answers" />
        </h2>
        {saved ? (
          <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
            {/* The icon stays on the sentence's first line, however narrow the screen. */}
            <span>
              <PiggyBank aria-hidden="true" className="mr-1.5 inline-block size-4 align-[-0.15em] text-fg-muted" />
              {SAVED_WORDS[saved.direction][0]}
              <span className="font-medium tabular">{saved.text}</span>
              {SAVED_WORDS[saved.direction][1]} an LLM asking the same questions
              {saved.multiple ? <span className="tabular">{` (the fast check was about ${saved.multiple} cheaper)`}</span> : null}
            </span>
            <EvidenceBadge label={saved.label} basis={same?.basis} subject="Savings against an LLM asking the same questions" />
          </p>
        ) : null}
        {line.earlyData && line.earlyDataLabel ? (
          <p className="flex flex-wrap items-center gap-2 text-sm text-fg-muted">
            <span>{line.earlyData}</span>
            <EvidenceBadge label={line.earlyDataLabel} basis={same?.basis} subject="What an LLM would have cost" />
          </p>
        ) : null}
      </div>
      {others.length > 0 ? <Alternatives items={others} /> : null}
      {line.freeMockJudge ? <FreeMockJudge view={realJudge} /> : null}
      <p className="text-xs text-fg-muted">{copy.savings.caption}</p>
    </section>
  );
}

/** The spend over the counterfactual bands, with its own loading, error and older-proxy states. */
function SpendChart({ roi, timeWindow, replay }: { roi: RoiSummary; timeWindow: TimeWindow; replay: boolean }) {
  const ds = useDataSource();
  const q = useQuery(roiTimeseriesQuery(ds, timeWindow));
  let body: ReactNode;
  if (roiBucket(timeWindow) === null) {
    body = (
      <p className="rounded-md border border-dashed border-border px-3 py-6 text-center text-sm text-fg-muted">
        Spend over time starts at the 1-hour window: pick 1h or longer to see it. The totals above and the table below cover{' '}
        {windowLong(timeWindow)}.
      </p>
    );
  } else if (q.isPending) {
    body = <Skeleton className="h-64 w-full" />;
  } else if (q.isError) {
    body = isNotServed(q.error) ? (
      <DegradedState
        title="Spend over time is not available on this proxy"
        reason="It does not serve GET /api/roi/timeseries (an older version)."
        stillWorks="The totals above and the table below cover the whole window."
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  } else {
    body = (
      <CostBandChart
        points={costBandPoints(q.data, roi)}
        bucket={q.data.bucket}
        totals={chartTotals(roi)}
        // The line is fast check plus deep audit: the weaker label of the two, as the total's (D46).
        measuredLabel={costLabel(roi.simulated, roi.total_per_1k.label)}
        bandLabels={bandLabels(roi)}
        spanWords={spanWords(timeWindow, replay)}
        windowPicker={!replay}
        headingLevel={2}
      />
    );
  }
  return (
    <Card>
      <CardContent>{body}</CardContent>
    </Card>
  );
}

/**
 * Every component of the cost, then the alternatives, each figure with its value label. The label
 * sits next to the row's name, so the table keeps three columns and fits a phone.
 */
function ComponentTable({ roi, timeWindow, replay }: { roi: RoiSummary; timeWindow: TimeWindow; replay: boolean }) {
  const rows = componentRows(roi);
  const span = spanWords(timeWindow, replay);
  return (
    <Card>
      <CardHeader level={2} title="Where the cost comes from" description={`${span[0].toUpperCase()}${span.slice(1)}, and per 1,000 checked answers.`} />
      <CardContent>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">What checking cost, and what the alternatives would have cost</caption>
            <thead>
              <tr className="border-b border-border text-xs text-fg-muted">
                <th scope="col" className="py-1.5 pr-3 text-left font-medium">
                  Component
                </th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium whitespace-nowrap">
                  Total
                </th>
                <th scope="col" className="py-1.5 pl-3 text-right font-medium whitespace-nowrap">
                  Per 1,000
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr
                  key={r.key}
                  data-row={r.key}
                  className={cn('border-b border-border last:border-b-0', r.key === 'total' && 'bg-surface-sunken')}
                >
                  <th scope="row" className="py-2 pr-3 text-left align-top font-normal">
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span className={cn('text-fg', r.key === 'total' && 'font-medium')}>{r.label}</span>
                      <EvidenceBadge label={r.valueLabel} basis={r.basis} subject={r.label} />
                    </span>
                    {r.detail ? <span className="mt-0.5 block text-xs text-fg-muted">{r.detail}</span> : null}
                  </th>
                  <td className="px-3 py-2 text-right align-top whitespace-nowrap text-fg tabular">{r.total}</td>
                  <td className="py-2 pl-3 text-right align-top whitespace-nowrap text-fg tabular">{r.per1k}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {/* The prices the alternatives use, each with its own source: a vendor's list price is
            labelled Vendor, as in the Prices table; the viewer's own override is "Your price". */}
        {roi.third_party.map((line) => (
          <p key={line} className="mt-3 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            {costPriceLineIsOverride(line) ? (
              <Badge size="sm">Your price</Badge>
            ) : (
              <EvidenceBadge label="vendor" basis={line} subject="Price used for the alternatives" />
            )}
            <span>Priced with: {line}</span>
          </p>
        ))}
      </CardContent>
    </Card>
  );
}

function Stat({ label, value, icon: Icon }: { label: string; value: string; icon: typeof Flag }) {
  return (
    <div className="space-y-0.5">
      <dt className="flex items-center gap-1.5 text-xs text-fg-muted">
        <Icon aria-hidden="true" className="size-3.5" />
        {label}
      </dt>
      <dd className="text-xl font-semibold text-fg tabular">{value}</dd>
    </div>
  );
}

/** Stopped (block and fallback) apart from flagged; then the fast check's speed. */
function OutcomesAndSpeed({ roi, replay }: { roi: RoiSummary; replay: boolean }) {
  const split = outcomeSplit(roi);
  const latencyLabel = roi.simulated ? 'simulated' : roi.latency_ms.label;
  return (
    <Card>
      <CardHeader level={2} title="What the checks did" />
      <CardContent className="space-y-4">
        <div className="space-y-2">
          <dl className="grid grid-cols-2 gap-3">
            <Stat label={copy.savings.stopped} value={formatCount(split.stopped)} icon={ShieldX} />
            <Stat label={copy.savings.flagged} value={formatCount(split.flagged)} icon={Flag} />
          </dl>
          <p className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            <span>
              Stopped counts blocked answers and fallback answers only. {countOf(split.notAssessed, 'answer')}{' '}
              {split.notAssessed === 1 ? 'was' : 'were'} not assessed.
            </span>
            <EvidenceBadge label={split.label} subject="Stopped and flagged answers" />
          </p>
        </div>
        <div className="space-y-2 border-t border-border pt-3">
          <h3 className="flex items-center gap-1.5 text-sm font-medium text-fg">
            <Gauge aria-hidden="true" className="size-4 text-fg-muted" />
            Fast-check speed
          </h3>
          <dl className="grid grid-cols-3 gap-3 text-sm">
            <div>
              <dt className="text-xs text-fg-muted">p50</dt>
              <dd className="font-medium text-fg tabular">{formatDurationMs(roi.latency_ms.gate_p50)}</dd>
            </div>
            <div>
              <dt className="text-xs text-fg-muted">p95</dt>
              <dd className="font-medium text-fg tabular">{formatDurationMs(roi.latency_ms.gate_p95)}</dd>
            </div>
            <div>
              <dt className="text-xs text-fg-muted">Missed the deadline</dt>
              <dd className="font-medium text-fg tabular">{formatPercent(roi.latency_ms.deadline_miss_rate)}</dd>
              {replay && roi.latency_ms.deadline_miss_rate === null ? (
                <dd className="text-2xs text-fg-muted">Not in the recording</dd>
              ) : null}
            </div>
          </dl>
          <EvidenceBadge label={latencyLabel} subject="Fast-check speed" />
        </div>
      </CardContent>
    </Card>
  );
}

/** How the two kinds of sampled deep audit differ (the component table counts both). */
const SAMPLE_KINDS =
  'Uniform samples are picked before the fast check decides, so they are the only unbiased deep audits: they feed the hallucination rate. Pass samples are drawn from answers that passed, at each route’s sample rate.';

/** Uniform sampling and the deep-audit budget today; in the static replay, that it is not recorded. */
function Sampling({ roi, replay }: { roi: RoiSummary; replay: boolean }) {
  const lines = samplingLines(roi);
  const capHit = lines.some((l) => l.key === 'llm_cap');
  return (
    <section aria-labelledby="savings-sampling" className="space-y-1.5 rounded-lg border border-border bg-surface px-4 py-3">
      <h2 id="savings-sampling" className="text-sm font-semibold text-fg">
        Sampling status
      </h2>
      {replay ? (
        <p data-sampling="not_recorded" className="text-sm text-fg-muted">
          Sampling status is not part of the recording. On your own stack this card shows today&rsquo;s uniform samples against their
          daily cap, and when the deep-audit budget ran out.
        </p>
      ) : (
        <ul className="space-y-1 text-sm">
          {lines.map((l) => (
            <li key={l.key} data-sampling={l.key} className={l.stopped ? 'text-fg' : 'text-fg-muted'}>
              {l.text}
            </li>
          ))}
        </ul>
      )}
      <p className="text-xs text-fg-muted">
        {SAMPLE_KINDS}{' '}
        {capHit && !replay ? (
          <Link to="/app/settings/$section" params={{ section: 'budgets' }} className={linkClass}>
            Raise the deep-audit budget
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        ) : null}
      </p>
    </section>
  );
}

export function SavingsPanel({ timeWindow }: { timeWindow: TimeWindow }) {
  const ds = useDataSource();
  const replay = useRuntimeConfig().appMode === 'static';
  const q = useQuery(roiSummaryQuery(ds, timeWindow));
  const showLoading = useDelayedLoading(q.isPending);

  if (q.isPending) return showLoading ? <SavingsSkeleton /> : null;
  if (q.isError) {
    return isNotServed(q.error) ? (
      <DegradedState
        title="Costs are not reported by this proxy"
        reason="It does not serve GET /api/roi/summary yet (an older version)."
        stillWorks="Each request in Traffic still shows what its fast check cost."
        action={
          <Link to="/app/traffic" className={linkClass}>
            Open Traffic
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Link>
        }
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  }
  const roi = q.data;
  if (roi.gated_responses === 0) {
    if (replay) {
      return (
        <EmptyState icon={PiggyBank} title="No checked answers in the replay yet">
          Costs and savings appear once the replay reaches its first checked answer.
        </EmptyState>
      );
    }
    return (
      <EmptyState icon={PiggyBank} title={`No checked answers in ${windowLong(timeWindow)}`} action={
        <Link to="/app/playground" className={linkClass}>
          Try one in the Playground
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Link>
      }>
        Costs and savings appear once the fast check has decided its first answer. Send a request through the gateway, or
        pick a longer window.
      </EmptyState>
    );
  }
  return (
    <div className="space-y-5">
      <Headline roi={roi} />
      <SpendChart roi={roi} timeWindow={timeWindow} replay={replay} />
      {/* One shrinkable column below lg, so a wide table scrolls inside its card, never the page. */}
      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <ComponentTable roi={roi} timeWindow={timeWindow} replay={replay} />
        <div className="space-y-4">
          <OutcomesAndSpeed roi={roi} replay={replay} />
          <Sampling roi={roi} replay={replay} />
        </div>
      </div>
    </div>
  );
}
