/**
 * Playground in the static build (SPEC §7.5 "Static GitHub Pages demo"; D99). Replay is the primary
 * experience: the three example requests open their runs on the tape (simulated for a mock
 * recording, which the header says). Below them, the `#live` section runs the fast check from this
 * tab with the visitor's own OpenRouter key (components/replay/live/LivePanel, loaded lazily so the
 * live feature stays out of the app entry, D112). `scenario` preselects a live example and `paste`
 * opens the consent dialog (routes/app/playground.tsx validates both).
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight } from 'lucide-react';
import { lazy, Suspense, useEffect, useMemo } from 'react';
import { useDataSource } from '@/app/services';
import { PageHeader } from '@/components/common/PageHeader';
import { ErrorState, LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { tracesQuery } from '@/data/queries';
import type { LiveScenarioId } from '@/domain/liveScenarios';
import { checkedAgainst, copy, headlineReason, outcomeOf } from '@/domain/presentation';
import { liveText } from './live/liveText';
import type { TraceSummary } from '@/types/api';
import { distinctExamples } from './examples';
import { useReplayInfo } from './useReplay';

/** The live check (lazy: nothing of lib/openrouter, lib/gate or replay/live in the app entry, D112). */
const LivePanel = lazy(() => import('./live/LivePanel'));

function ExampleCard({ trace }: { trace: TraceSummary }) {
  const outcome = outcomeOf(trace);
  const against = checkedAgainst(trace.checked_against);
  const reason = headlineReason(trace.reasons);
  return (
    <li className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
      <div className="flex flex-wrap items-center gap-2">
        <OutcomePill outcome={outcome} />
        <span className="text-xs text-fg-muted">{against.text}</span>
      </div>
      <div className="space-y-1.5">
        <p className="text-xs font-medium text-fg-muted">The user asked</p>
        <p className="text-sm text-fg">{trace.question_preview}</p>
      </div>
      <div className="space-y-1.5">
        <p className="text-xs font-medium text-fg-muted">The assistant answered</p>
        <p className="text-md text-fg">{trace.response_preview}</p>
      </div>
      {reason ? <p className="text-xs text-fg-muted">{reason}</p> : null}
      <Link
        to="/app/traffic/$traceId"
        params={{ traceId: trace.trace_id }}
        className="mt-auto inline-flex items-center gap-1 text-sm font-medium text-fg underline underline-offset-2"
      >
        Open this request
        <ArrowRight aria-hidden="true" className="size-3.5" />
      </Link>
    </li>
  );
}

export interface StaticPlaygroundProps {
  /** `?scenario=`: the live example to preselect. */
  scenario?: LiveScenarioId;
  /** `?paste=1`: open the consent dialog on arrival. */
  paste?: boolean;
}

export function StaticPlayground({ scenario, paste = false }: StaticPlaygroundProps) {
  const ds = useDataSource();
  const q = useQuery(tracesQuery(ds, { limit: 200 }));
  const examples = useMemo(() => distinctExamples(q.data?.items ?? []), [q.data]);
  const showLoading = useDelayedLoading(q.isPending);
  const simulated = useReplayInfo().kind === 'mock';

  // A link to #live (the landing's "Try it live", the callback's way back) lands on the section.
  useEffect(() => {
    if (window.location.hash === '#live') document.getElementById('live')?.scrollIntoView({ block: 'start' });
  }, []);

  let body;
  if (q.isPending) body = showLoading ? <LoadingState rows={3} /> : null;
  else if (q.isError) body = <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  else
    body = (
      <ul aria-label="Example requests" className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        {examples.map((t) => (
          <ExampleCard key={t.trace_id} trace={t} />
        ))}
      </ul>
    );

  return (
    <div className="space-y-8">
      <PageHeader
        description={
          simulated
            ? 'Try it now: the example requests below are on the replay, checked by the demo’s mock checks and mock judge (simulated, not model output). Open one to see what the fast check and the deep audit found.'
            : 'Try it now: the example requests below were checked by a running gateway and recorded. Open one to see what the fast check and the deep audit found.'
        }
      />
      <section aria-labelledby="examples-title" className="space-y-3">
        <h2 id="examples-title" className="text-base font-semibold text-fg">
          Example requests
        </h2>
        {body}
      </section>

      <section id="live" aria-labelledby="live-title" className="max-w-4xl scroll-mt-16 space-y-4 rounded-lg border border-border bg-surface p-4 sm:p-5">
        <div className="space-y-1.5">
          <h2 id="live-title" className="text-base font-semibold text-fg">
            {liveText.section_title}
          </h2>
          <p className="text-sm text-fg-muted">{liveText.section_intro}</p>
          {simulated ? <p className="text-xs text-fg-muted">{copy.playground_live.recording_is_simulated}</p> : null}
        </div>
        <Suspense fallback={<LoadingState rows={2} />}>
          <LivePanel scenario={scenario} openConsent={paste} />
        </Suspense>
      </section>
    </div>
  );
}
