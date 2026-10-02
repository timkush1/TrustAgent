/**
 * Query keys, queryOptions and stream side effects of the cost plane (data/control/costPlane.ts).
 * Owned by WEB-SV after Step 0b; queries.ts re-exports `qkcCosts` and calls `costSideEffects`.
 */
import type { QueryClient } from '@tanstack/react-query';
import type { RoiTimeseriesParams } from '../control/costPlane';
import type { StreamEvent } from '../stream/types';

export const qkcCosts = {
  all: ['costs'] as const,
  roiTimeseries: (params: RoiTimeseriesParams) => ['costs', 'roi-timeseries', params] as const,
  pricing: ['costs', 'pricing'] as const,
};

/** Keeps cost queries in step with one live event (budget status, D49). No-op until WEB-SV SV-1. */
export function costSideEffects(_event: StreamEvent, _queryClient: QueryClient): void {}
