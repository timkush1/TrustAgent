/**
 * Playground in the static build (SPEC §7.5 "Static GitHub Pages demo"). Replay is the primary
 * experience: the three example requests open their runs on the tape (simulated for a mock
 * recording, which the header says). Checking your own answers live
 * in the browser (your OpenRouter key, kept in this tab) arrives in P5, so it is described as coming
 * soon, with no control that pretends to work.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowRight, Hourglass } from 'lucide-react';
import { useMemo } from 'react';
import { useDataSource } from '@/app/services';
import { CodeBlock } from '@/components/common/CodeBlock';
import { PageHeader } from '@/components/common/PageHeader';
import { ErrorState, LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { OutcomePill } from '@/components/outcome/OutcomePill';
import { tracesQuery } from '@/data/queries';
import { checkedAgainst, commandText, copy, headlineReason, outcomeOf } from '@/domain/presentation';
import type { TraceSummary } from '@/types/api';
import { distinctExamples } from './examples';
import { useReplayInfo } from './useReplay';

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

export function StaticPlayground() {
  const ds = useDataSource();
  const q = useQuery(tracesQuery(ds, { limit: 200 }));
  const examples = useMemo(() => distinctExamples(q.data?.items ?? []), [q.data]);
  const showLoading = useDelayedLoading(q.isPending);
  const demo = commandText('demo') ?? 'make demo';
  const simulated = useReplayInfo().kind === 'mock';

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

      <section aria-labelledby="live-title" className="max-w-3xl space-y-3 rounded-lg border border-dashed border-border-strong p-4">
        <div className="flex flex-wrap items-center gap-2">
          <h2 id="live-title" className="text-base font-semibold text-fg">
            Check your own answers, live in this tab
          </h2>
          <span className="inline-flex h-6 items-center gap-1 rounded-full border border-dashed border-border-strong px-2 text-xs font-medium text-fg-muted">
            <Hourglass aria-hidden="true" className="size-3.5" />
            Coming soon
          </span>
        </div>
        <p className="text-sm text-fg-muted">
          Connect your OpenRouter account and run the same checks on your own question, answer and documents, right here.
          It is not switched on yet. When it is:
        </p>
        <ul className="space-y-1.5 text-sm text-fg">
          {copy.playground_static.consent.map((line) => (
            <li key={line} className="flex gap-2">
              <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-fg-subtle" />
              {line}
            </li>
          ))}
        </ul>
        <div className="space-y-2 pt-1">
          <p className="text-sm text-fg">Until then, run everything locally. Docker only, no keys, $0:</p>
          <CodeBlock code={demo} label="Start the demo stack" />
        </div>
      </section>
    </div>
  );
}
