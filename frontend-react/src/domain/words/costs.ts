/**
 * Words, icons and tones for the P4 costs server values (cost sources, budget states and price labels).
 * Owned by WEB-SV after Step 0b. presentation.ts re-exports this module with `export *`, so every
 * export here starts with `cost` (P4-PLAN D86) and can never collide with another plane's words.
 * Text comes from contracts/ui (copy.v1.json, outcomes.v1.json) where a key exists; the words below
 * without one are listed for the lead to move into copy.v1.json at the next checkpoint (D86).
 */
import type { AuditSelection, Counterfactual, FieldError, PriceEntry, ValueLabel } from '@/types/api';
import { copy } from '../contracts';
import { formatUsd } from '../format';
import { fillTemplate } from '../presentation';

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
  static_assumptions: 'Published assumptions (≈ $35.50 per 1,000 answers)',
};

/** How a counterfactual was worked out, in words. */
export function costMethodWord(method: Counterfactual['method']): string {
  return METHODS[method];
}

/** A counterfactual that rests on assumptions rather than this gateway's own audits. */
export function costIsMeasuredMethod(method: Counterfactual['method']): boolean {
  return method !== 'static_assumptions';
}

/** "not measured", for a figure that comes from the published assumptions. */
export const costNotMeasured: string = copy.savings.not_measured;

// ----------------------------------------------------------------------------------- selections

const SELECTIONS: Record<AuditSelection, string> = {
  high: 'high risk',
  sample: 'sampled',
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

/** "44 high risk · 10 sampled · 4 uniform sample", largest first; "" when there are none. */
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

/** Where a price comes from: the vendor's list price, or your own override. */
export function costPriceSourceWord(label: PriceEntry['label']): string {
  return label === 'override' ? 'Your price' : 'Vendor list price';
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
