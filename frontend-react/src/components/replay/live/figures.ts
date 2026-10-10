/**
 * The figures the consent dialog and the live panel show (D101, D114), each with its value label:
 *
 * - one check: the published estimate (value claim `fast_check_cost_per_check`, "~$0.0002 per
 *   check" shown as "$0.0002"), labelled as the claim says;
 * - the worst case with these caps: `maxCalls × max(priceCall(Jev, maxTokens),
 *   priceCall(judge, maxTokens))` from the price table, an estimate. Each model's call is priced at
 *   the dearer split of the per-call token cap (all input, or all output), so the figure never
 *   understates (D150): a drafting call's answer is output, and the cap bounds prompt plus answer.
 *
 * priceCall refuses a token count that is not whole (D150); the caps are whole numbers by
 * construction (config/runtime.ts), and a figure that cannot be computed is omitted, never shown
 * as $0. Owned by WEB-LIVE (LV-6).
 */
import claimsDoc from '@contracts/claims/value-claims.v1.json';
import prices from '@contracts/pricing/prices.v1.json';
import type { PlaygroundConfig } from '@/config/runtime';
import { priceCall } from '@/lib/decision/cost';
import type { PricingTable, ValueLabel } from '@/types/api';

const TABLE = prices as unknown as PricingTable;

interface Claim {
  id: string;
  display: string;
  label: ValueLabel;
  basis: string | null;
  as_of: string | null;
  source_url: string | null;
}

/** A figure with where it comes from. */
export interface Figure {
  text: string;
  label: ValueLabel;
  basis: string | null;
  asOf?: string | null;
  sourceUrl?: string | null;
}

const PER_CHECK_CLAIM = (claimsDoc.claims as unknown as readonly Claim[]).find((c) => c.id === 'fast_check_cost_per_check');

/** One fast check, from the published claim; null if the claim is missing. */
export function perCheckFigure(): Figure | null {
  if (!PER_CHECK_CLAIM) return null;
  return {
    text: PER_CHECK_CLAIM.display.replace(/^[~≈]\s*/, '').replace(/ per check$/, ''),
    label: PER_CHECK_CLAIM.label,
    basis: PER_CHECK_CLAIM.basis,
    asOf: PER_CHECK_CLAIM.as_of,
    sourceUrl: PER_CHECK_CLAIM.source_url,
  };
}

/** The most one call of `model` can cost at `tokens` (all input or all output, whichever is dearer); null if unpriced. */
export function dearestCall(model: string, tokens: number, table: PricingTable = TABLE): number | null {
  try {
    const price = (input: number, output: number) =>
      priceCall({ provider: 'openrouter', model, input_tokens: input, output_tokens: output, reported_cost_usd: null, table });
    const asInput = price(tokens, 0);
    const asOutput = price(0, tokens);
    if (asInput.cost_source !== 'computed' || asOutput.cost_source !== 'computed' || asInput.cost_usd === null || asOutput.cost_usd === null) return null;
    return Math.max(asInput.cost_usd, asOutput.cost_usd);
  } catch {
    return null;
  }
}

/** D101's worst case: maxCalls × the dearer of one Jev call and one judge call at maxTokens; null if either is unpriced. */
export function worstCaseSpend(cfg: Pick<PlaygroundConfig, 'maxCalls' | 'maxTokens' | 'jevModel' | 'judgeModel'>, table: PricingTable = TABLE): number | null {
  const jev = dearestCall(cfg.jevModel, cfg.maxTokens, table);
  const judge = dearestCall(cfg.judgeModel, cfg.maxTokens, table);
  if (jev === null || judge === null) return null;
  return cfg.maxCalls * Math.max(jev, judge);
}
