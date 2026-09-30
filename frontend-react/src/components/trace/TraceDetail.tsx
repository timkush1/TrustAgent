/**
 * Trace detail (SPEC §3.3), S0: the outcome card, the answer, the Checks list, the Claims section
 * and collapsed Details. It renders from whichever source arrives first (the live stream or
 * GET /api/traces/:id) and moves forward as the gate decision and the deep audit land.
 */
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { ArrowLeft, SearchX } from 'lucide-react';
import { useId, useMemo } from 'react';
import { useDataSource } from '@/app/services';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { isApiError } from '@/data/errors';
import { auditQuery, evaluatorsQuery, traceQuery } from '@/data/queries';
import { useStreamStore } from '@/data/stream/streamStore';
import { AnswerSection } from './AnswerSection';
import { ChecksList } from './ChecksList';
import { ClaimsSection } from './ClaimsSection';
import { OutcomeCard } from './OutcomeCard';
import { TraceFacts } from './TraceFacts';
import { auditHasResult, buildTraceView, receivedId } from './traceView';

function BackToTraffic() {
  return (
    <Link
      to="/app/traffic"
      className="inline-flex items-center gap-1 text-sm text-fg-muted hover:text-fg"
    >
      <ArrowLeft aria-hidden="true" className="size-4" />
      Traffic
    </Link>
  );
}

const isNotFound = (err: unknown) => isApiError(err) && err.kind === 'http' && err.status === 404;

export function TraceDetail({ traceId }: { traceId: string }) {
  const ds = useDataSource();
  // Claim marks in the answer link to their rows in the Claims section by id.
  const claimIdPrefix = useId();
  const detailQuery = useQuery(traceQuery(ds, traceId));
  const live = useStreamStore((s) => s.traces[traceId]);
  const liveDecision = useStreamStore((s) => s.decisions[traceId]);
  const liveAudit = useStreamStore((s) => s.audits[traceId]);

  const base = useMemo(
    () => buildTraceView({ detail: detailQuery.data, live, liveDecision, liveAudit }),
    [detailQuery.data, live, liveDecision, liveAudit],
  );
  // The trace says its audit finished but neither source carried it: fetch it once.
  const auditId = base?.summary.audit.audit_id ?? null;
  const needAudit = Boolean(base && !base.audit && auditId && auditHasResult(base.summary.audit.status));
  const fetched = useQuery({ ...auditQuery(ds, auditId ?? ''), enabled: needAudit });
  // The checks are named from the evaluator library (GET /api/evaluators) when this proxy serves it.
  const evaluators = useQuery({ ...evaluatorsQuery(ds), retry: false });
  const evaluatorNames = useMemo(() => new Map((evaluators.data?.items ?? []).map((e) => [e.id, e.name])), [evaluators.data]);
  const view = useMemo(() => {
    if (!base || base.audit || !fetched.data) return base;
    return buildTraceView({ detail: detailQuery.data, live, liveDecision, liveAudit, fetchedAudit: fetched.data });
  }, [base, fetched.data, detailQuery.data, live, liveDecision, liveAudit]);

  const showLoading = useDelayedLoading(!view && detailQuery.isPending);

  if (!view) {
    let state;
    if (detailQuery.isPending) state = showLoading ? <LoadingState rows={5} /> : null;
    else if (isNotFound(detailQuery.error)) {
      state = (
        <EmptyState icon={SearchX} title="There is no request with this id">
          <p>
            It may be older than the retention window, or the link is incomplete.{' '}
            <Link to="/app/traffic" className="font-medium text-fg underline underline-offset-2">
              Back to Traffic
            </Link>
          </p>
        </EmptyState>
      );
    } else state = <ErrorState error={detailQuery.error} onRetry={() => void detailQuery.refetch()} />;
    return (
      <div className="space-y-4">
        <BackToTraffic />
        {state}
      </div>
    );
  }

  const { summary, decision, audit } = view;
  const partialError = detailQuery.isError && !isNotFound(detailQuery.error);
  return (
    <article aria-label="Request" className="@container space-y-5" data-trace-id={summary.trace_id}>
      <BackToTraffic />
      {partialError ? <ErrorState error={detailQuery.error} onRetry={() => void detailQuery.refetch()} /> : null}
      <OutcomeCard summary={summary} decision={decision} delivered={view.delivered} receivedTarget={receivedId(claimIdPrefix)} />
      <div className="grid gap-6 @4xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <AnswerSection view={view} claimIdPrefix={claimIdPrefix} />
        <div className="space-y-6">
          <ChecksList summary={summary} decision={decision} names={evaluatorNames} />
          <ClaimsSection summary={summary} audit={audit} claimIdPrefix={claimIdPrefix} />
        </div>
      </div>
      <TraceFacts summary={summary} decision={decision} attempts={view.attempts} audit={audit} />
    </article>
  );
}
