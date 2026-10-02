/**
 * The cost plane of the control plane (SPEC §2.4 #48-#49, §7.4; D46-D53, D73): the ROI
 * time series behind the Savings tab and the effective price table behind Budgets → Prices.
 * (#47 `getRoiSummary` stays on the P3 ControlPlane.)
 *
 * The signatures are fixed; the bodies and COST_SHAPES belong to WEB-SV. A proxy
 * from before 2.0 answers 404 to both, which the pages show as "not reported by this proxy".
 */
import type { PricingTable, RoiTimeseries, TimeWindow } from '@/types/api';
import type { HttpClient } from '../http';
import { normalizeShaped, type NestedShape } from '../normalize';
import { rejectNotImplemented } from './shared';

/** GET /api/roi/timeseries (#48). */
export interface RoiTimeseriesParams {
  window: TimeWindow;
  bucket?: RoiTimeseries['bucket'];
}

export interface CostPlane {
  /** GET /api/roi/timeseries (#48): spend and same-checks counterfactual per bucket. */
  getRoiTimeseries(params: RoiTimeseriesParams, signal?: AbortSignal): Promise<RoiTimeseries>;
  /** GET /api/pricing (#49): the effective price table (overrides > price file > contracts, D53). */
  getPricing(signal?: AbortSignal): Promise<PricingTable>;
}

/** What each cost response must carry (see normalizeShaped). */
export const COST_SHAPES = {
  roiTimeseries: { strings: ['window', 'bucket'], lists: ['points'], each: { points: { strings: ['t'] } } },
  pricing: { strings: ['version'], lists: ['entries'], each: { entries: { strings: ['match', 'provider'] } } },
} as const satisfies Record<string, NestedShape>;

/** The cost plane over HTTP. */
export function liveCostPlane(http: HttpClient): CostPlane {
  return {
    async getRoiTimeseries(params, signal) {
      const query = params.bucket ? { window: params.window, bucket: params.bucket } : { window: params.window };
      const raw = await http.get<unknown>('/api/roi/timeseries', { query, signal });
      return normalizeShaped<RoiTimeseries>(raw, 'GET /api/roi/timeseries', COST_SHAPES.roiTimeseries);
    },
    async getPricing(signal) {
      const raw = await http.get<unknown>('/api/pricing', { signal });
      return normalizeShaped<PricingTable>(raw, 'GET /api/pricing', COST_SHAPES.pricing);
    },
  };
}

/** A cost plane that serves nothing (the static build, tests): every call rejects with 404 `not_implemented`. */
export function unavailableCostPlane(message?: string): CostPlane {
  const reject = rejectNotImplemented(message);
  return {
    getRoiTimeseries: reject,
    getPricing: reject,
  };
}
