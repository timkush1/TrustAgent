/**
 * The TypeScript port of backend-go internal/decision/cost.go (SPEC §7.4, D52, D53, D85): the
 * price lookup and the per-call and per-1,000-answers arithmetic the landing calculator, the live
 * Playground's running cost (D101) and the static Studio's estimates use. The cost golden vectors
 * (contracts/vectors/cost) pin it to the Go reference.
 *
 * Step 0b stub: the frozen signatures; WEB-LANDING fills the bodies in LD-1 (CP-COSTTS), and every
 * cost vector must then pass (vectors/cost.ts replaces the skip).
 */
import type { PriceEntry, PricingTable } from '@/types/api';

/** cost_events.cost_source of a priced call (internal/decision/cost.go). */
export type CallCostSource = 'reported' | 'computed' | 'free_local' | 'simulated' | 'unknown';

/** The `call_cost` vector input. */
export interface CallCostInput {
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  /** The vendor's own figure (usage.cost); wins when present and not negative. */
  reported_cost_usd?: number | null;
  table: PricingTable;
}

export interface CallCost {
  cost_usd: number | null;
  cost_source: CallCostSource;
  price_table_version: string | null;
}

/** The `price_lookup` vector input: the effective table is overrides, then the file, then contracts (D53). */
export interface PriceLookupInput {
  provider: string;
  model: string;
  overrides: readonly PriceEntry[];
  file: PricingTable | null;
  contracts: PricingTable;
}

export interface PriceLookup {
  entry: PriceEntry | null;
  price_table_version: string | null;
}

export interface ModelRef {
  provider: string;
  model: string;
}

/** The `per_1k` vector input (the SPEC §7.4 assumptions). */
export interface Per1kInput {
  decision_model: ModelRef;
  judge: ModelRef;
  settings: {
    fast_check_input_tokens: number;
    fast_check_output_tokens: number;
    claims_per_answer: number;
    decompose_input_tokens: number;
    decompose_output_tokens: number;
    evidence_context_tokens: number;
    verifier_prompt_tokens: number;
    claim_tokens: number;
    verify_output_tokens: number;
    grounding_flag_rate: number;
    pass_sample_rate: number;
    uniform_sample_rate: number;
  };
  table: PricingTable;
}

export interface Per1k {
  audit_rate: number;
  fast_check_usd: number | null;
  fast_check_per_1k: number | null;
  deep_audit_usd: number | null;
  deep_audit_per_1k: number | null;
  total_per_1k: number | null;
  decision_cost_source: CallCostSource;
  judge_cost_source: CallCostSource;
}

const notYet = (fn: string): never => {
  throw new Error(`lib/decision/cost.ts ${fn}: not implemented yet (LD-1)`);
};

/** The cost of one call (cost.go CallCost). */
export function priceCall(_input: CallCostInput): CallCost {
  return notYet('priceCall');
}

/** The entry that prices `model` on `provider` in the effective table, and that table's version (D53). */
export function lookupEffective(_input: PriceLookupInput): PriceLookup {
  return notYet('lookupEffective');
}

/** The cost per 1,000 answers of the fast check and the deep audit (SPEC §7.4). */
export function costPer1k(_input: Per1kInput): Per1k {
  return notYet('costPer1k');
}

/** `*` matches any run of characters, everything else itself (cost.go GlobMatch). */
export function globMatch(_pattern: string, _s: string): boolean {
  return notYet('globMatch');
}
