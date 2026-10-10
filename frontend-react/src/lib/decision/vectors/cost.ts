/**
 * The `cost` vector kind's TS runner (contracts/vectors/cost: call_cost, price_lookup, per_1k)
 * over lib/decision/cost.ts. Owned by WEB-LANDING: LD-1 replaces the skip with a runner, and every
 * cost vector must then pass (CP-COSTTS).
 */
import type { VectorKind } from './kinds';

export const costKind: VectorKind = {
  skip: 'TS runner with the landing calculator (LD-1, D85)',
  go: 'all fn (internal/decision/cost.go)',
};
