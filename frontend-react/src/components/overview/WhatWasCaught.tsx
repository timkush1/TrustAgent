/**
 * "What was caught" (SPEC §3.3): the most frequent reasons behind flagged, blocked or would-be
 * actions, in plain words, as bars with their counts; each reason links to the matching requests in
 * Traffic and to its latest example.
 */
import { Link } from '@tanstack/react-router';
import { ArrowRight, ShieldCheck } from 'lucide-react';
import { BarChart } from '@/components/charts/BarChart';
import { EmptyState } from '@/components/common/StateViews';
import { Card, CardContent } from '@/components/ui/card';
import { formatCount } from '@/domain/format';
import { copy } from '@/domain/presentation';
import type { OverviewStats } from '@/types/api';
import { caughtRows, windowLong } from './overviewModel';

export function WhatWasCaught({ stats }: { stats: OverviewStats }) {
  const rows = caughtRows(stats);
  return (
    <Card>
      <CardContent className="space-y-3">
        {rows.length === 0 ? (
          <>
            <h3 className="text-sm font-semibold text-fg">{copy.overview.what_was_caught}</h3>
            <EmptyState icon={ShieldCheck} title="Nothing was caught in this window" className="py-8">
              <p>Answers that the checks flag or stop show up here with the reason in plain words.</p>
            </EmptyState>
          </>
        ) : (
          <>
            <BarChart
              title={copy.overview.what_was_caught}
              description={`Requests per reason, ${windowLong(stats.window)}`}
              valueLabel="Requests"
              data={rows.map((r) => ({ key: r.key, label: r.label, value: r.count }))}
              labelWidth={200}
            />
            <ul aria-label="Open the requests behind each reason" className="space-y-1 border-t border-border pt-2">
              {rows.map((r) => (
                <li key={r.key} className="flex flex-wrap items-baseline justify-between gap-x-3 text-xs">
                  <Link
                    to="/app/traffic"
                    search={{ reason: r.reason }}
                    className="min-w-0 truncate font-medium text-fg underline underline-offset-2"
                  >
                    {r.label}
                    <span className="sr-only">: show all {formatCount(r.count)} requests</span>
                  </Link>
                  <Link
                    to="/app/traffic/$traceId"
                    params={{ traceId: r.latestTraceId }}
                    className="inline-flex shrink-0 items-center gap-0.5 text-fg-muted underline underline-offset-2 hover:text-fg"
                  >
                    Latest example{' '}
                    <span className="sr-only">of {r.label}</span>
                    <ArrowRight aria-hidden="true" className="size-3" />
                  </Link>
                </li>
              ))}
            </ul>
          </>
        )}
      </CardContent>
    </Card>
  );
}
