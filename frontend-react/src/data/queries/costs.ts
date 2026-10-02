/**
 * Query keys, queryOptions and stream side effects of the cost plane (data/control/costPlane.ts).
 * Owned by WEB-SV after Step 0b; queries.ts re-exports `qkcCosts` and calls `costSideEffects`.
 */
import { queryOptions, type QueryClient } from '@tanstack/react-query';
import type { RoiTimeseries, TimeWindow } from '@/types/api';
import type { RoiTimeseriesParams } from '../control/costPlane';
import { qk, qkc } from '../queries';
import type { DataSource } from '../source';
import type { StreamEvent } from '../stream/types';

export const qkcCosts = {
  all: ['costs'] as const,
  roiTimeseries: (params: RoiTimeseriesParams) => ['costs', 'roi-timeseries', params] as const,
  pricing: ['costs', 'pricing'] as const,
};

/** The bucket a window is drawn in: hours up to a day, days beyond (the pairs #48 serves). */
export function roiBucket(window: TimeWindow): RoiTimeseries['bucket'] {
  return window === '7d' || window === '30d' ? '1d' : '1h';
}

/** GET /api/roi/timeseries (#48): spend and the same-checks counterfactual per bucket. */
export function roiTimeseriesQuery(ds: DataSource, window: TimeWindow) {
  const params: RoiTimeseriesParams = { window, bucket: roiBucket(window) };
  return queryOptions({
    queryKey: qkcCosts.roiTimeseries(params),
    queryFn: ({ signal }) => ds.getRoiTimeseries(params, signal),
  });
}

/** GET /api/pricing (#49): the effective price table (your overrides first, D53). */
export function pricingQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkcCosts.pricing,
    queryFn: ({ signal }) => ds.getPricing(signal),
  });
}

/** A settings path is a pricing override (`pricing_overrides`, `pricing_overrides[2].match`). */
function isPricingPath(key: string): boolean {
  return key === 'pricing_overrides' || key.startsWith('pricing_overrides[') || key.startsWith('pricing_overrides.');
}

/**
 * Keeps cost queries in step with one live event:
 * - `status{component:"budget"}` (D49: the worse of the two caps; `detail` names which) refetches
 *   /api/status, whose budgets the banner and Budgets read, and the spend and sampling numbers;
 * - `settings_updated` touching the price overrides refetches the effective price table.
 */
export function costSideEffects(event: StreamEvent, queryClient: QueryClient): void {
  if (event.type === 'status' && event.data.component === 'budget') {
    void queryClient.invalidateQueries({ queryKey: qk.status, refetchType: 'active' });
    // The ROI summary of every window (its sampling line says when the deep-audit budget ran out).
    void queryClient.invalidateQueries({ queryKey: [...qkc.overview.all, 'roi'], refetchType: 'active' });
    void queryClient.invalidateQueries({ queryKey: qkcCosts.all, refetchType: 'active' });
    return;
  }
  if (event.type === 'settings_updated' && event.data.keys.some(isPricingPath)) {
    void queryClient.invalidateQueries({ queryKey: qkcCosts.pricing, refetchType: 'active' });
  }
}
