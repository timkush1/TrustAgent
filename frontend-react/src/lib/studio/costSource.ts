/**
 * What a static test run's cost is (D101): `reported` when OpenRouter reported every call's cost
 * (vendor), `computed` for the price table's figure (estimate), `unknown` when a call had no price.
 * StudioTestResponse (frozen api.ts) has no field for it, so the static plane notes it here beside
 * the response it made, and the test bench labels the figure with it.
 */
import type { CostRecord, StudioTestResponse } from '@/types/api';

const sources = new WeakMap<StudioTestResponse, CostRecord['cost_source']>();

export function setTestCostSource(response: StudioTestResponse, source: CostRecord['cost_source']): void {
  sources.set(response, source);
}

/** The cost source of a static run; undefined for a response the static plane did not make. */
export function testCostSource(response: StudioTestResponse): CostRecord['cost_source'] | undefined {
  return sources.get(response);
}
