/**
 * Words, icons and tones for the costs server values (cost sources, budget states and price labels).
 * Owned by WEB-SV after Step 0b. presentation.ts re-exports this module with `export *`, so every
 * export here starts with `cost` (D86) and can never collide with another plane's words.
 * Text comes from contracts/ui (copy.v1.json, outcomes.v1.json) where a key exists; the words below
 * without one are listed for the lead to move into copy.v1.json at the next checkpoint (D86).
 */
import claimsDoc from '@contracts/claims/value-claims.v1.json';
import assumptions from '@contracts/pricing/assumptions.v1.json';
import type { AuditSelection, Counterfactual, FieldError, PriceEntry, ValueLabel } from '@/types/api';
import { copy } from '../contracts';
import { formatUsd } from '../format';
import { fillTemplate, humanizeId } from '../presentation';

// ------------------------------------------------------------------------------- counterfactuals

export interface CostCounterfactualView {
  /** "An LLM asking the same questions". */
  label: string;
  /** One line on what the number answers. */
  description: string;
}

const COUNTERFACTUALS: Record<Counterfactual['kind'], CostCounterfactualView> = {
  llm_same_checks: {
    label: 'An LLM asking the same questions',
    description: 'The fast check’s questions sent to an LLM judge instead of Jev.',
  },
  llm_full_claim_audit: {
    label: 'An LLM auditing every answer claim by claim',
    description: 'A deep audit of every answer, not only the risky ones.',
  },
};

export function costCounterfactualView(kind: Counterfactual['kind']): CostCounterfactualView {
  return COUNTERFACTUALS[kind];
}

const METHODS: Record<Counterfactual['method'], string> = {
  token_model: 'Token model: your fast-check tokens priced at the judge’s rate',
  uniform_sample: 'Mean cost of uniformly sampled deep audits × answers',
  fitted_from_audits: 'Fitted from your deep audits by answer length',
  static_assumptions: 'Published assumptions',
};

/** How a counterfactual was worked out, in words. */
export function costMethodWord(method: Counterfactual['method']): string {
  return METHODS[method];
}

/** A counterfactual that rests on assumptions rather than this gateway's own audits. */
export function costIsMeasuredMethod(method: Counterfactual['method']): boolean {
  return method !== 'static_assumptions';
}

/** A published value claim (contracts/claims/value-claims.v1.json): its figure and where it comes from. */
export interface CostClaim {
  value: number | null;
  basis: string;
}

function costClaim(id: string): CostClaim {
  const claim = (claimsDoc.claims as readonly { id: string; value: number | null; basis: string }[]).find((c) => c.id === id);
  return { value: claim?.value ?? null, basis: claim?.basis ?? '' };
}

/**
 * The published assumption for an LLM auditing one answer claim by claim, per 1,000 answers
 * (contracts value claim llm_full_audit_cost_per_1k, an estimate), or null without one. The server
 * prices its static full-audit counterfactual with the same claim.
 */
export const costFullAuditPer1k: number | null = costClaim('llm_full_audit_cost_per_1k').value;

/** Why the published full-audit figure is what it is (the value claim's basis). */
export const costFullAuditBasis: string = costClaim('llm_full_audit_cost_per_1k').basis;

/**
 * The published assumption for an LLM asking the same questions as the fast check, per 1,000
 * answers (value claim llm_same_checks_cost_per_1k, an estimate), with its basis.
 */
export const costSameChecksPublished: CostClaim = costClaim('llm_same_checks_cost_per_1k');

/**
 * The published assumption for the fast check on every answer, per 1,000 answers (value claim
 * fast_check_cost_per_1k, an estimate), with its basis: the first part of the shipped-defaults total.
 */
export const costFastCheckPublished: CostClaim = costClaim('fast_check_cost_per_1k');

/**
 * What checking costs with a real judge at the shipped defaults (value claim
 * default_total_cost_per_1k, an estimate): the fast check on every answer plus a full audit of the
 * deep-audited share. `auditShare` is that share, from the same inputs the published figure uses
 * (pricing/assumptions.v1.json): grounding-flagged answers, the pass sample of the rest and the
 * uniform sample.
 */
export const costShippedDefaults: { per1k: number | null; basis: string; auditShare: number } = (() => {
  const { grounding_flag_rate: flagged, pass_sample_rate: pass, uniform_sample_rate: uniform } = assumptions.inputs;
  const claim = costClaim('default_total_cost_per_1k');
  return { per1k: claim.value, basis: claim.basis, auditShare: flagged.value + (1 - flagged.value) * pass.value + uniform.value };
})();

/** "not measured", for a figure that comes from the published assumptions. */
export const costNotMeasured: string = copy.savings.not_measured;

// ----------------------------------------------------------------------------------- selections

const SELECTIONS: Record<AuditSelection, string> = {
  high: 'high risk',
  sample: 'pass sample',
  error_sample: 'sampled after a failed check',
  uniform: 'uniform sample',
  direct: 'sent directly',
  playground: 'from the Playground',
  studio: 'from Studio',
};

/** Why a deep audit ran, as a short phrase ("high risk", "uniform sample"). */
export function costSelectionWord(selection: AuditSelection): string {
  return SELECTIONS[selection];
}

/** "44 high risk · 10 pass sample · 4 uniform sample", largest first; "" when there are none. */
export function costSelectionBreakdown(bySelection: Partial<Record<AuditSelection, number>>): string {
  return (Object.entries(bySelection) as [AuditSelection, number][])
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1])
    .map(([s, n]) => `${n.toLocaleString('en-US')} ${costSelectionWord(s)}`)
    .join(' · ');
}

// -------------------------------------------------------------------------------------- savings

/** The savings line over a range: "$0.60–$0.97", or one figure when both ends agree. */
export function costRange(low: number | null, high: number | null): string {
  if (low === null && high === null) return formatUsd(null);
  if (low === null || high === null || Math.abs(high - low) < 1e-9) return formatUsd(low ?? high);
  return `${formatUsd(Math.min(low, high))}–${formatUsd(Math.max(low, high))}`;
}

/** "Uniform sampling paused at 14:02 (daily cap 50)". */
export function costUniformPaused(time: string, cap: number): string {
  return fillTemplate(copy.savings.uniform_paused, { time, cap: cap.toLocaleString('en-US') });
}

/** "Deep-audit budget reached at 14:02". */
export function costLlmCapHit(time: string): string {
  return fillTemplate(copy.savings.llm_cap_hit, { time });
}

/** The label a figure carries on a simulated summary: Simulated wins over everything (D46). */
export function costLabel(simulated: boolean, label: ValueLabel): ValueLabel {
  return simulated ? 'simulated' : label;
}

// ---------------------------------------------------------------------------------------- budgets

export type CostBudgetKind = 'decision' | 'llm';

export interface CostBudgetView {
  /** "Fast-check budget". */
  title: string;
  /** What the cap covers and what happens past it. */
  description: string;
  /** The RuntimeSettings path of its cap. */
  path: `budgets.${'decision_usd_day' | 'llm_usd_day'}`;
}

const BUDGETS: Record<CostBudgetKind, CostBudgetView> = {
  decision: {
    title: 'Fast-check budget',
    description:
      'Daily cap on Jev calls. Observe and flag routes stop being checked once their share is spent; routes that block or fall back keep the reserve, then follow their fail mode.',
    path: 'budgets.decision_usd_day',
  },
  llm: {
    title: 'Deep-audit budget',
    description: 'Daily cap on the LLM judge. Past it, deep audits pause until midnight UTC; fast checks keep running.',
    path: 'budgets.llm_usd_day',
  },
};

export function costBudgetView(kind: CostBudgetKind): CostBudgetView {
  return BUDGETS[kind];
}

// ----------------------------------------------------------------------------------------- prices

/**
 * Where a price comes from: the vendor's list price, your own override, or neither (an entry the
 * table carries without a source, such as a local model whose compute is not priced): an assumption.
 */
export function costPriceSourceWord(label: PriceEntry['label']): string {
  if (label === 'override') return 'Your price';
  return label === 'vendor_price' ? 'Vendor list price' : 'Assumed price';
}

const PRICE_PROVIDERS: Record<string, string> = {
  openrouter: 'OpenRouter',
  typesafe: 'TypeSafe',
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  ollama: 'Ollama (on your machine)',
  mock: 'Mock (simulated)',
};

/** Words for PriceEntry.provider ("OpenRouter", "Ollama (on your machine)"), never the raw id. */
export function costPriceProviderLabel(provider: string): string {
  return PRICE_PROVIDERS[provider] ?? humanizeId(provider);
}

/**
 * A price entry's note in plain words. The contract marks a price still to be re-verified before
 * `make bench-publish` with a leading "[A]" (contracts/README.md); the note itself already says it
 * has not been re-checked, so the marker is dropped here.
 */
export function costPriceNote(note: string | null | undefined): string | null {
  const text = (note ?? '').replace(/^\[A\]\s*/, '').trim();
  return text === '' ? null : text;
}

/**
 * Whether a price line of RoiSummary.third_party is the viewer's own price override. The proxy
 * lists there the prices its counterfactuals use (cmd/proxy/roi_api.go priceNote), each naming its
 * source in brackets: a vendor's list price ("(Anthropic, as of …)") or "(your price override)".
 * A vendor's price is labelled Vendor, as in the Prices table; an override is never labelled so.
 */
export function costPriceLineIsOverride(line: string): boolean {
  return /\(your price override\b/.test(line);
}

/** "$0.042 / $0 per million tokens (in / out)". */
export function costPricePair(entry: Pick<PriceEntry, 'input_per_mtok' | 'output_per_mtok'>): string {
  return `${formatUsd(entry.input_per_mtok)} in · ${formatUsd(entry.output_per_mtok)} out`;
}

/** The note under the overrides: they change estimates, never what a provider bills. */
export const costOverrideNote: string = copy.settings.prices_override_note;

const num = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);

/**
 * The words for one ValidateOverrides finding (D53), by code: `invalid_price_entry`,
 * `price_zero_not_local`, `duplicate_override` and the warning `price_far_above_contracts`. A code
 * without words, or a finding without the numbers its words need, shows the server's message.
 */
export function costOverrideFinding(finding: Pick<FieldError, 'code' | 'message' | 'details'>, provider?: string): string {
  const d = finding.details ?? {};
  switch (finding.code) {
    case 'invalid_price_entry':
      return fillTemplate(copy.settings.price_invalid_entry, { message: finding.message });
    case 'price_zero_not_local': {
      const name = typeof d.provider === 'string' ? d.provider : provider;
      return name ? fillTemplate(copy.settings.price_zero_not_local, { provider: name }) : finding.message;
    }
    case 'duplicate_override': {
      const index = num(d.index);
      return index === null ? finding.message : fillTemplate(copy.settings.price_duplicate_override, { n: index + 1 });
    }
    case 'price_far_above_contracts': {
      const ratio = num(d.ratio);
      const listed = num(d.contracts_per_mtok);
      if (ratio === null || listed === null) return finding.message;
      return fillTemplate(copy.settings.price_far_above_contracts, {
        ratio: ratio.toLocaleString('en-US'),
        contracts_per_mtok: listed.toLocaleString('en-US', { maximumFractionDigits: 6 }),
      });
    }
    default:
      return finding.message;
  }
}
