/**
 * What a Studio action will cost, before it runs (SPEC §7.3: "Run" first shows the estimated cost;
 * "Draft with AI" shows "≈ $0.005" [E]). Every figure is labelled: an Estimate from the price table
 * (contracts/pricing/prices.v1.json, the same entries the gateway starts from), Simulated ($0) for
 * a mock model, or no figure at all when the model has no known price. The server's own numbers
 * replace these once a run reports them.
 */
import prices from '@contracts/pricing/prices.v1.json';
import { DEFAULT_CPT, est, globMatch, OVERHEAD } from '@/lib/decision';
import type { DecisionBackend, EvaluatorSpec, PriceEntry, StudioSample, ValueLabel } from '@/types/api';

export interface CostEstimate {
  /** USD, or null when there is no price to estimate from. */
  usd: number | null;
  label: Extract<ValueLabel, 'estimate' | 'simulated'>;
  /** How the figure was produced, for the value badge's popover. */
  basis: string;
}

/** One draft: about 1,500 prompt and 700 answer tokens (the drafter prompt, temperature 0.2). */
export const DRAFT_TOKENS = { input: 1500, output: 700 } as const;
/** SPEC §7.3's figure for one draft on the default judge (anthropic/claude-haiku-4.5 at $1 / $5 per million). */
export const DRAFT_ESTIMATE_USD = 0.005;

const PROVIDERS: Partial<Record<DecisionBackend, string>> = {
  'openrouter-decisions': 'openrouter',
  'openrouter-systemone': 'openrouter',
  typesafe: 'typesafe',
};

const ENTRIES = (prices as { entries: PriceEntry[] }).entries;

/**
 * The price-table entry for a model (D53 without the server's overrides): an entry for the
 * model's provider first, then another provider's entry that names the model (a catch-all "*",
 * such as the local models' $0, applies only to its own provider).
 */
export function priceEntryFor(model: string, provider: string | null): PriceEntry | null {
  const bare = model.includes('/') ? model.slice(model.indexOf('/') + 1) : model;
  const matches = (e: PriceEntry) => globMatch(e.match, model) || globMatch(e.match, bare);
  return ENTRIES.find((e) => e.provider === provider && matches(e)) ?? ENTRIES.find((e) => e.match !== '*' && matches(e)) ?? null;
}

const tokens = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });

/**
 * The estimate for one draft by the judge, at the judge model's table price (≈ $0.005 on the default
 * judge); Simulated when the judge is a mock, and no figure when the model has no known price.
 */
export function draftEstimate(judge: { provider: string | null; model: string | null }): CostEstimate {
  if (judge.provider === 'mock') return { usd: 0, label: 'simulated', basis: 'The mock judge drafts for free; its draft is simulated.' };
  const entry = judge.model ? priceEntryFor(judge.model, judge.provider) : null;
  if (!entry) {
    return {
      usd: null,
      label: 'estimate',
      basis: `One drafting call of about 1,500 prompt and 700 answer tokens; the price of ${judge.model ?? 'the judge model'} is not in the price table.`,
    };
  }
  if (entry.input_per_mtok === 0 && entry.output_per_mtok === 0) {
    return { usd: 0, label: 'estimate', basis: `A local model (${judge.model}): no API fee; compute is not priced.` };
  }
  return {
    usd: (DRAFT_TOKENS.input * entry.input_per_mtok + DRAFT_TOKENS.output * entry.output_per_mtok) / 1e6,
    label: 'estimate',
    basis: `One drafting call to ${judge.model}: about 1,500 prompt and 700 answer tokens at $${entry.input_per_mtok} and $${entry.output_per_mtok} per million (${entry.match}, price table).`,
  };
}

/** The fast-check tokens of one sample: the fixed overhead, the question, and the sample's text. */
export function sampleTokens(spec: Pick<EvaluatorSpec, 'question'>, sample: StudioSample): number {
  const state = {
    user_request: sample.user_request ?? '',
    assistant_response: sample.assistant_response,
    evidence: (sample.evidence ?? []).map((e) => e.text),
    system_instructions: sample.system_instructions ?? '',
  };
  return OVERHEAD + est(spec.question, DEFAULT_CPT) + est(state, DEFAULT_CPT);
}

/**
 * The estimate for testing `samples` on the fast check: one call per sample, billed on input
 * tokens, at the decision model's table price.
 */
export function testEstimate(
  spec: Pick<EvaluatorSpec, 'question'>,
  samples: readonly StudioSample[],
  model: { backend: DecisionBackend; name: string | null },
): CostEstimate {
  if (model.backend === 'mock') return { usd: 0, label: 'simulated', basis: 'The mock fast check answers for free; its answers are simulated.' };
  const total = samples.reduce((sum, s) => sum + sampleTokens(spec, s), 0);
  const entry = model.name ? priceEntryFor(model.name, PROVIDERS[model.backend] ?? null) : null;
  if (!entry) {
    return { usd: null, label: 'estimate', basis: `About ${tokens.format(total)} input tokens; the price of this fast-check model is not in the price table.` };
  }
  const usd = (total * entry.input_per_mtok) / 1e6;
  return {
    usd,
    label: 'estimate',
    basis: `${samples.length} ${samples.length === 1 ? 'call' : 'calls'}, about ${tokens.format(total)} input tokens at $${entry.input_per_mtok} per million (${entry.match}, price table).`,
  };
}
