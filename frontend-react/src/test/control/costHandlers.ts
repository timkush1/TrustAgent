/**
 * msw handlers and state for the cost plane (data/control/costPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-SV after Step 0b: the state is seeded from the golden
 * fixtures (contracts/fixtures/api); the handlers arrive with SV-1.
 */
import type { HttpHandler } from 'msw';
import pricingFixture from '@contracts/fixtures/api/PricingTable.default.json';
import roiTimeseriesFixture from '@contracts/fixtures/api/RoiTimeseries.7d.json';
import type { PricingTable, RoiTimeseries } from '@/types/api';
import type { HandlerKit, Reply } from './kit';

export interface CostState {
  /** GET /api/roi/timeseries (#48). */
  roiTimeseries: Reply<RoiTimeseries>;
  /** GET /api/pricing (#49). */
  pricing: Reply<PricingTable>;
}

export function defaultCostState(): CostState {
  return {
    roiTimeseries: structuredClone(roiTimeseriesFixture) as RoiTimeseries,
    pricing: structuredClone(pricingFixture) as PricingTable,
  };
}

export function costHandlers(_state: CostState, _kit: HandlerKit): HttpHandler[] {
  return [];
}
