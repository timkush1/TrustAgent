/**
 * Rows for the Traffic list: GET /api/traces pages combined with the live stream
 * (trafficRows.ts). REST rows are also handed to streamStore, so later stream events (audit
 * queued, started, completed, …) update them in place. While the list is paused the REST pages
 * stop refreshing too, so nothing new slides in; loading older pages at the bottom still works.
 */
import { useInfiniteQuery } from '@tanstack/react-query';
import { useEffect, useMemo } from 'react';
import { useDataSource } from '@/app/services';
import { useStreamStore } from '@/data/stream/streamStore';
import type { TraceSummary } from '@/types/api';
import { tracePagesQuery } from './trafficQuery';
import { combineTrafficRows, traceListParams, type TrafficFilters } from './trafficRows';

export function useTrafficRows(filters: TrafficFilters) {
  const ds = useDataSource();
  const { outcome, route, model, reason } = filters;
  const paused = useStreamStore((s) => s.paused);
  const known = useStreamStore((s) => s.traces);
  const order = useStreamStore((s) => s.order);
  const visibleOrder = useStreamStore((s) => s.visibleOrder);
  const decisions = useStreamStore((s) => s.decisions);

  const params = useMemo(() => traceListParams({ outcome, route, model }), [outcome, route, model]);
  const query = useInfiniteQuery({
    ...tracePagesQuery(ds, params),
    // Paused: keep the pages on screen (a first load for new filters still runs).
    enabled: (q) => !paused || q.state.data === undefined,
  });

  const rest = useMemo(() => query.data?.pages.flatMap((p) => p.items) ?? [], [query.data]);
  useEffect(() => {
    if (rest.length > 0) useStreamStore.getState().mergeTraces(rest);
  }, [rest]);
  const live = useMemo(() => {
    const out: TraceSummary[] = [];
    for (const id of visibleOrder) {
      const t = known[id];
      if (t) out.push(t);
    }
    return out;
  }, [visibleOrder, known]);

  const { rows, held } = useMemo(
    () => combineTrafficRows({ live, known, order, rest }, { outcome, route, model, reason }),
    [live, known, order, rest, outcome, route, model, reason],
  );

  return { rows, held, decisions, query };
}
