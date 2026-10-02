/**
 * msw handlers and state for the cost plane (data/control/costPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-SV after Step 0b: the state is seeded from the golden
 * fixtures (contracts/fixtures/api), and the time series answers with the window and bucket asked
 * for, as #48 does.
 */
import { http, type HttpHandler } from 'msw';
import pricingFixture from '@contracts/fixtures/api/PricingTable.default.json';
import roiTimeseriesFixture from '@contracts/fixtures/api/RoiTimeseries.7d.json';
import type { PricingTable, RoiTimeseries, TimeWindow } from '@/types/api';
import { API } from '../msw';
import { isError, reply, type HandlerKit, type Reply } from './kit';

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

export function costHandlers(state: CostState, kit: HandlerKit): HttpHandler[] {
  return [
    http.get(`${API}/api/roi/timeseries`, async ({ request }) => {
      const { url } = await kit.record(request);
      const series = state.roiTimeseries;
      if (series === undefined || isError(series)) return reply(series);
      const window = (url.searchParams.get('window') as TimeWindow | null) ?? series.window;
      const bucket = (url.searchParams.get('bucket') as RoiTimeseries['bucket'] | null) ?? series.bucket;
      return reply<RoiTimeseries>({ ...series, window, bucket });
    }),
    http.get(`${API}/api/pricing`, async ({ request }) => {
      await kit.record(request);
      return reply(state.pricing);
    }),
  ];
}
