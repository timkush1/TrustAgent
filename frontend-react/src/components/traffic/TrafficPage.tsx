/**
 * Traffic (SPEC §3.3): "What happened to this request?" Every request, newest first, live by
 * default. Page contract: loading after 200 ms, the server's message with Retry, an empty state
 * with the next step, and a filtered-empty state that clears the filters.
 */
import { Link } from '@tanstack/react-router';
import { FlaskConical, SearchX } from 'lucide-react';
import { useCallback, useMemo } from 'react';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/StateViews';
import { useDelayedLoading } from '@/components/common/useDelayedLoading';
import { copy } from '@/domain/presentation';
import { TrafficTable } from './TrafficTable';
import { PausedPill, TrafficToolbar } from './TrafficToolbar';
import { distinctValues, hasFilters, type TrafficFilters } from './trafficRows';
import type { TrafficSearchParams } from './search';
import { useLiveMode } from './useLiveMode';
import { useTrafficRows } from './useTrafficRows';

export function TrafficPage({ search }: { search: TrafficSearchParams }) {
  const { outcome, route, model, reason } = search;
  const filters: TrafficFilters = useMemo(() => ({ outcome, route, model, reason }), [outcome, route, model, reason]);
  const { live, setLive } = useLiveMode(search.live);
  const { rows, held, decisions, query } = useTrafficRows(filters);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query;
  const loadMore = useCallback(() => void fetchNextPage(), [fetchNextPage]);

  const routes = useMemo(() => distinctValues(rows, 'route', route), [rows, route]);
  const models = useMemo(() => distinctValues(rows, 'model', model), [rows, model]);
  const showLoading = useDelayedLoading(query.isPending && rows.length === 0);

  let body;
  if (rows.length > 0) {
    body = (
      <>
        {query.isError ? <ErrorState error={query.error} onRetry={() => void query.refetch()} /> : null}
        <TrafficTable
          rows={rows}
          decisions={decisions}
          hasNextPage={Boolean(hasNextPage) && !query.isError}
          isFetchingNextPage={isFetchingNextPage}
          onLoadMore={loadMore}
        />
      </>
    );
  } else if (query.isPending) {
    body = showLoading ? <LoadingState rows={6} /> : null;
  } else if (query.isError) {
    body = <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  } else if (hasFilters(filters)) {
    body = (
      <EmptyState icon={SearchX} title="No requests match these filters">
        <p>
          <Link
            from="/app/traffic/"
            search={(prev) => ({ live: prev.live })}
            className="font-medium text-fg underline underline-offset-2"
          >
            Clear filters
          </Link>
        </p>
      </EmptyState>
    );
  } else {
    body = (
      <EmptyState
        title={copy.states.empty_traffic}
        action={
          <Link
            to="/app/playground"
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-strong bg-surface px-3 text-sm font-medium text-fg hover:bg-surface-hover"
          >
            <FlaskConical aria-hidden="true" className="size-4" />
            Open the Playground
          </Link>
        }
      >
        <p>New requests appear here as they happen.</p>
      </EmptyState>
    );
  }

  return (
    <section aria-label="Traffic" className="space-y-3">
      <TrafficToolbar filters={filters} routes={routes} models={models} live={live} onLiveChange={setLive} />
      {body}
      {live ? null : <PausedPill held={held} onResume={() => setLive(true)} />}
    </section>
  );
}
