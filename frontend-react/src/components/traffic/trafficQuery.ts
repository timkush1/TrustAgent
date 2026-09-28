/**
 * GET /api/traces as an infinite query (keyset pages, SPEC §2.4 #10) for the Traffic list. The key
 * sits under qk.traces.lists(), so the list refreshes with every other trace list after a WS
 * `hello` or `resync` (liveStream.ts).
 */
import { infiniteQueryOptions } from '@tanstack/react-query';
import { qk } from '@/data/queries';
import type { DataSource, TraceListParams } from '@/data/source';

export function tracePagesQuery(ds: DataSource, params: Omit<TraceListParams, 'cursor'>) {
  return infiniteQueryOptions({
    queryKey: [...qk.traces.lists(), 'pages', params] as const,
    queryFn: ({ pageParam, signal }) => ds.listTraces({ ...params, cursor: pageParam ?? undefined }, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
  });
}
