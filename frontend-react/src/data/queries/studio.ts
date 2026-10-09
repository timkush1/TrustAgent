/**
 * Query keys, queryOptions and stream side effects of the Studio plane (data/control/studioPlane.ts).
 * Owned by WEB-ST; queries.ts re-exports `qkcStudio` and calls `studioSideEffects`.
 *
 * Every Studio call is something the viewer starts (draft, test, load samples, test on past traffic)
 * and its result belongs to the open Studio session, so Studio runs them as mutations and keeps the
 * results in the page. The one cached read, the evaluator version a Studio session starts from, uses
 * the evaluators plane's key (`qkc.evaluators.version`), which the evaluator events already refresh.
 */
import type { QueryClient } from '@tanstack/react-query';
import type { StreamEvent } from '../stream/types';

export const qkcStudio = {
  all: ['studio'] as const,
};

/**
 * Keeps Studio queries in step with one live event. Nothing to do: Studio caches no server state
 * of its own (see above), and a result the viewer is looking at must not change under them.
 */
export function studioSideEffects(_event: StreamEvent, _queryClient: QueryClient): void {}
