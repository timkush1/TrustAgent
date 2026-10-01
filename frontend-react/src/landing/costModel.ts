/**
 * The cost model behind every [estimate] figure (contracts/pricing/assumptions.v1.json over
 * prices.v1.json), evaluated exactly as contracts/validate.mjs recomputes the stated figures, with
 * the calculator's inputs substituted. So the calculator, the landing's labelled lines and the
 * contract checks can never disagree.
 */
import assumptions from '@contracts/pricing/assumptions.v1.json';
import prices from '@contracts/pricing/prices.v1.json';

type Quantity = number | string | { sum: Quantity[] } | { product: Quantity[] } | { complement: Quantity };

interface Term {
  label: string;
  figure?: string;
  weight?: Quantity;
  price?: string;
  calls?: Quantity;
  input_tokens?: Quantity;
  output_tokens?: Quantity;
}

interface Figure {
  id: string;
  per_1k_usd?: number;
  tolerance?: number;
  terms?: Term[];
}

interface PriceEntry {
  match: string;
  provider: string;
  input_per_mtok: number;
  output_per_mtok: number;
}

export type InputId = keyof typeof assumptions.inputs;
export type Inputs = Partial<Record<InputId, number>>;

const figures = new Map((assumptions.figures as Figure[]).map((f) => [f.id, f]));
const aliases = new Map(
  Object.entries(assumptions.prices).map(([alias, ref]) => [
    alias,
    (prices.entries as PriceEntry[]).find((e) => e.provider === ref.provider && e.match === ref.match),
  ]),
);

export function defaultInput(id: InputId): number {
  return assumptions.inputs[id].value;
}

function quantity(q: Quantity, inputs: Inputs): number {
  if (typeof q === 'number') return q;
  if (typeof q === 'string') {
    if (!(q in assumptions.inputs)) throw new Error(`unknown input ${q}`);
    return inputs[q as InputId] ?? defaultInput(q as InputId);
  }
  if ('sum' in q) return q.sum.reduce<number>((a, x) => a + quantity(x, inputs), 0);
  if ('product' in q) return q.product.reduce<number>((a, x) => a * quantity(x, inputs), 1);
  return 1 - quantity(q.complement, inputs);
}

/** Cost of one answer for a figure, in USD. */
export function perAnswer(id: string, inputs: Inputs = {}): number {
  const f = figures.get(id);
  if (!f?.terms) throw new Error(`figure ${id} is not a cost`);
  let total = 0;
  for (const t of f.terms) {
    if (t.figure) {
      total += quantity(t.weight ?? 1, inputs) * perAnswer(t.figure, inputs);
    } else {
      const e = t.price ? aliases.get(t.price) : undefined;
      if (!e) throw new Error(`${id}: unknown price ${t.price}`);
      total += (quantity(t.calls ?? 1, inputs) * (quantity(t.input_tokens ?? 0, inputs) * e.input_per_mtok + quantity(t.output_tokens ?? 0, inputs) * e.output_per_mtok)) / 1e6;
    }
  }
  return total;
}

/** The figure's stated per-1,000 value and tolerance (for tests). */
export function statedPer1k(id: string): { value: number; tolerance: number } | null {
  const f = figures.get(id);
  return f?.per_1k_usd === undefined ? null : { value: f.per_1k_usd, tolerance: f.tolerance ?? 0.005 };
}

export const COST_FIGURES = ['fast_check_per_1k', 'default_total_per_1k', 'llm_same_checks_per_1k', 'llm_full_audit_per_1k'] as const;

export interface CalculatorInputs {
  answersPerMonth: number;
  /** Fast-check (and same-questions) input tokens per answer. */
  tokensPerAnswer: number;
  /** Share of answers with a grounding trigger (always audited). */
  flagRate: number;
  /** Share of the other answers audited anyway. */
  passSampleRate: number;
}

export interface MonthlyCost {
  fastCheck: number;
  deepAudit: number;
  total: number;
  sameChecks: number;
  fullAudit: number;
  auditedShare: number;
}

export function monthlyCost(c: CalculatorInputs): MonthlyCost {
  const inputs: Inputs = {
    fast_check_input_tokens: c.tokensPerAnswer,
    same_checks_input_tokens: c.tokensPerAnswer,
    grounding_flag_rate: c.flagRate,
    pass_sample_rate: c.passSampleRate,
  };
  const fast = perAnswer('fast_check_per_1k', inputs) * c.answersPerMonth;
  const total = perAnswer('default_total_per_1k', inputs) * c.answersPerMonth;
  const auditedShare = c.flagRate + (1 - c.flagRate) * c.passSampleRate + defaultInput('uniform_sample_rate');
  return {
    fastCheck: fast,
    deepAudit: total - fast,
    total,
    sameChecks: perAnswer('llm_same_checks_per_1k', inputs) * c.answersPerMonth,
    fullAudit: perAnswer('llm_full_audit_per_1k', inputs) * c.answersPerMonth,
    auditedShare,
  };
}
