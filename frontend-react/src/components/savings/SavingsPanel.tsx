/**
 * The Overview "Savings" tab (SPEC §7.4 "Panel", §3.3; SV-2): what checking costs and
 * what it saves, where every number says where it comes from.
 *
 * Top to bottom: the headline ("Your cost: $X per 1,000 answers (fast check $a + deep audit $b)")
 * with the savings range against an LLM asking the same questions; the spend line over the
 * counterfactual bands (with its table); the component table, every figure with its value label
 * (Measured, Estimate, Third-party, Vendor or Simulated); what was stopped (block and fallback)
 * apart from what was flagged; fast-check speed; and the sampling status, including when the
 * deep-audit budget ran out.
 *
 * Page contract: the delayed skeleton, the server's message with Retry, a degraded state for a
 * proxy that does not report costs (an older version), an empty state before the first checked
 * answer, and simulated labels on a demo or mock stack (D46). Nothing here writes.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, Flag, Gauge, PiggyBank, ShieldX } from 'lucide-react';
import type { ReactNode } from 'react';
import { useDataSource } from '@/app/services';
import { DegradedState } from '@/components/common/DegradedState';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { EmptyState, ErrorState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { savingsLine, windowLong } from '@/components/overview/overviewModel';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { isNotServed, roiSummaryQuery } from '@/data/queries';
import { roiBucket, roiTimeseriesQuery } from '@/data/queries/costs';
import { formatCount, formatDurationMs } from '@/domain/format';
import { copy, costLabel } from '@/domain/presentation';
import { formatPercent } from '@/domain/rates';
import { cn } from '@/lib/cn';
import type { RoiSummary, TimeWindow } from '@/types/api';
import { CostBandChart } from './CostBandChart';
import { componentRows, costBandPoints, countOf, outcomeSplit, samplingLines, savingsRange, type SavingsRange } from './savingsModel';

const linkClass = 'inline-flex items-center gap-0.5 text-sm font-medium text-fg underline underline-offset-2';

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

/** The headline, the savings range and the early-data sentence. */
function Headline({ roi }: { roi: RoiSummary }) {
  const line = savingsLine(roi);
  const saved = savingsRange(roi);
  const same = roi.counterfactuals.find((c) => c.kind === 'llm_same_checks');
  return (
    <section aria-labelledby="savings-headline" className="space-y-2 rounded-lg border border-border bg-surface px-4 py-4">
      <h2 id="savings-headline" className="flex flex-wrap items-center gap-2 text-lg font-semibold text-fg">
        <span className="tabular">{line.headline}</span>
        <EvidenceBadge label={line.label} basis={roi.total_per_1k.basis} subject="Your cost per 1,000 answers" />
      </h2>
      {saved ? (
        <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
          <PiggyBank aria-hidden="true" className="size-4 text-fg-muted" />
          <span>
            {SAVED_WORDS[saved.direction][0]}
            <span className="font-medium tabular">{saved.text}</span>
            {SAVED_WORDS[saved.direction][1]} an LLM asking the same questions
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
      <p className="text-xs text-fg-muted">{copy.savings.caption}</p>
    </section>
  );
}

/** The spend over the counterfactual bands, with its own loading, error and older-proxy states. */
function SpendChart({ roi, timeWindow }: { roi: RoiSummary; timeWindow: TimeWindow }) {
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
        // The line is fast check plus deep audit: the weaker label of the two, as the total's (D46).
        measuredLabel={costLabel(roi.simulated, roi.total_per_1k.label)}
        bandLabel={roi.simulated ? 'simulated' : 'estimate'}
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

/** Every component of the cost, then the alternatives, each figure with its value label. */
function ComponentTable({ roi, timeWindow }: { roi: RoiSummary; timeWindow: TimeWindow }) {
  const rows = componentRows(roi);
  return (
    <Card>
      <CardHeader level={2} title="Where the cost comes from" description={`In ${windowLong(timeWindow)}, and per 1,000 checked answers.`} />
      <CardContent>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <caption className="sr-only">What checking cost, and what the alternatives would have cost</caption>
            <thead>
              <tr className="border-b border-border text-xs text-fg-muted">
                <th scope="col" className="py-1.5 pr-3 text-left font-medium">
                  Component
                </th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">
                  Total
                </th>
                <th scope="col" className="px-3 py-1.5 text-right font-medium">
                  Per 1,000
                </th>
                <th scope="col" className="py-1.5 pl-3 text-left font-medium">
                  Source
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
                    <span className={cn('block text-fg', r.key === 'total' && 'font-medium')}>{r.label}</span>
                    {r.detail ? <span className="block text-xs text-fg-muted">{r.detail}</span> : null}
                  </th>
                  <td className="px-3 py-2 text-right align-top text-fg tabular">{r.total}</td>
                  <td className="px-3 py-2 text-right align-top text-fg tabular">{r.per1k}</td>
                  <td className="py-2 pl-3 align-top">
                    <EvidenceBadge label={r.valueLabel} basis={r.basis} subject={r.label} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {roi.third_party.length > 0 ? (
          <p className="mt-3 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            <EvidenceBadge label="third_party" basis={roi.third_party.join('; ')} subject="Figures from third parties" />
            <span>Uses third-party figures: {roi.third_party.join('; ')}</span>
          </p>
        ) : null}
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
function OutcomesAndSpeed({ roi }: { roi: RoiSummary }) {
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
            </div>
          </dl>
          <EvidenceBadge label={latencyLabel} subject="Fast-check speed" />
        </div>
      </CardContent>
    </Card>
  );
}

/** Uniform sampling and the deep-audit budget today. */
function Sampling({ roi }: { roi: RoiSummary }) {
  const lines = samplingLines(roi);
  const capHit = lines.some((l) => l.key === 'llm_cap');
  return (
    <section aria-labelledby="savings-sampling" className="space-y-1.5 rounded-lg border border-border bg-surface px-4 py-3">
      <h2 id="savings-sampling" className="text-sm font-semibold text-fg">
        Sampling status
      </h2>
      <ul className="space-y-1 text-sm">
        {lines.map((l) => (
          <li key={l.key} data-sampling={l.key} className={l.stopped ? 'text-fg' : 'text-fg-muted'}>
            {l.text}
          </li>
        ))}
      </ul>
      <p className="text-xs text-fg-muted">
        Uniform samples are the only unbiased deep audits: they feed the hallucination rate.{' '}
        {capHit ? (
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
      <SpendChart roi={roi} timeWindow={timeWindow} />
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <ComponentTable roi={roi} timeWindow={timeWindow} />
        <div className="space-y-4">
          <OutcomesAndSpeed roi={roi} />
          <Sampling roi={roi} />
        </div>
      </div>
    </div>
  );
}
