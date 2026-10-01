/**
 * Query keys, queryOptions and stream side effects of the Studio plane (data/control/studioPlane.ts).
 * Owned by WEB-ST after Step 0b; queries.ts re-exports `qkcStudio` and calls `studioSideEffects`.
 */
import type { QueryClient } from '@tanstack/react-query';
import type { StreamEvent } from '../stream/types';

export const qkcStudio = {
  all: ['studio'] as const,
};

/** Keeps Studio queries in step with one live event. No-op until WEB-ST STW-1. */
export function studioSideEffects(_event: StreamEvent, _queryClient: QueryClient): void {}
