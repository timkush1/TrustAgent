/**
 * The cost plane of the control plane (SPEC §2.4 #48-#49, §7.4; P4-PLAN D46-D53, D73): the ROI
 * time series behind the Savings tab and the effective price table behind Budgets → Prices.
 * (#47 `getRoiSummary` stays on the P3 ControlPlane.)
 *
 * Step 0b declares the signatures; every method rejects with 404 `not_implemented` until WEB-SV
 * lands its body. The signatures are fixed (P4-PLAN §0 rule 3); the bodies and COST_SHAPES belong to WEB-SV.
 */
import type { PricingTable, RoiTimeseries, TimeWindow } from '@/types/api';
import type { HttpClient } from '../http';
import type { NestedShape } from '../normalize';
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
  roiTimeseries: { strings: ['window', 'bucket'], lists: ['points'] },
  pricing: { strings: ['version'], lists: ['entries'] },
} as const satisfies Record<string, NestedShape>;

/** The cost plane over HTTP. */
export function liveCostPlane(_http: HttpClient): CostPlane {
  return {
    getRoiTimeseries: rejectNotImplemented(),
    getPricing: rejectNotImplemented(),
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
