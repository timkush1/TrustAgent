/**
 * The savings strip on Overview (SPEC §7.4): "Your cost: $X per 1,000 answers (fast check $a +
 * deep audit $b)", what was stopped and flagged, and the early-data comparison with asking an LLM
 * the same questions. Every figure carries its value label (Measured, Estimate, Simulated…). On a
 * demo whose mock judge is free, it says why the deep audit reads $0 and links to what a real
 * judge costs on the Savings tab.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, ShieldX, Flag } from 'lucide-react';
import { useDataSource } from '@/app/services';
import { EvidenceBadge } from '@/components/common/EvidenceBadge';
import { Skeleton } from '@/components/ui/skeleton';
import { isNotServed, roiSummaryQuery } from '@/data/queries';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import type { TimeWindow } from '@/types/api';
import { savingsLine } from './overviewModel';

export function SavingsStrip({ timeWindow }: { timeWindow: TimeWindow }) {
  const ds = useDataSource();
  const q = useQuery(roiSummaryQuery(ds, timeWindow));

  let body;
  if (q.isPending) {
    body = <Skeleton className="h-5 w-2/3" />;
  } else if (q.isError) {
    body = (
      <p className="text-sm text-fg-muted">
        {isNotServed(q.error) ? 'This proxy does not report costs yet.' : `Costs are not available right now: ${q.error.message}`}
      </p>
    );
  } else if (q.data.gated_responses === 0) {
    body = <p className="text-sm text-fg-muted">Costs appear after the first checked answer.</p>;
  } else {
    const line = savingsLine(q.data);
    body = (
      <div className="space-y-1.5">
        <p className="flex flex-wrap items-center gap-2 text-sm text-fg">
          <span className="font-medium tabular">{line.headline}</span>
          <EvidenceBadge label={line.label} basis={q.data.total_per_1k.basis} subject="Your cost per 1,000 answers" />
        </p>
        {/* The headline's deep audit is $0 only because the mock judge is free: said right under it. */}
        {line.freeMockJudge ? (
          <p className="text-sm text-fg-muted">
            The deep audit reads $0 because the demo&rsquo;s mock judge is free.{' '}
            <Link
              to="/app/overview"
              search={(prev) => ({ ...prev, tab: 'savings' as const })}
              className="font-medium text-fg underline underline-offset-2"
            >
              What a real judge costs
            </Link>
          </p>
        ) : null}
        <p className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-muted">
          <span className="inline-flex items-center gap-1.5">
            <ShieldX aria-hidden="true" className="size-3.5" />
            {copy.savings.stopped}: <span className="font-medium text-fg tabular">{formatCount(line.stopped)}</span>
          </span>
          <span className="inline-flex items-center gap-1.5">
            <Flag aria-hidden="true" className="size-3.5" />
            {copy.savings.flagged}: <span className="font-medium text-fg tabular">{formatCount(line.flagged)}</span>
          </span>
          <EvidenceBadge label={line.outcomesLabel} subject="Outcome counts" />
        </p>
        {line.earlyData && line.earlyDataLabel ? (
          <p className="flex flex-wrap items-center gap-2 text-sm text-fg-muted">
            <span>{line.earlyData}</span>
            <EvidenceBadge
              label={line.earlyDataLabel}
              basis={q.data.counterfactuals.find((c) => c.kind === 'llm_same_checks')?.basis}
              subject="What an LLM would have cost"
            />
          </p>
        ) : null}
        <p className="text-xs text-fg-muted">{copy.savings.caption}</p>
      </div>
    );
  }

  return (
    <section aria-labelledby="savings-strip-title" className="rounded-lg border border-border bg-surface px-4 py-3">
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <h2 id="savings-strip-title" className="text-sm font-semibold text-fg">
          What checking costs
        </h2>
        <Link
          to="/app/overview"
          search={(prev) => ({ ...prev, tab: 'savings' as const })}
          className="inline-flex items-center gap-0.5 text-xs font-medium text-fg underline underline-offset-2"
        >
          Savings details
          <ArrowRight aria-hidden="true" className="size-3" />
        </Link>
      </div>
      {body}
    </section>
  );
}
