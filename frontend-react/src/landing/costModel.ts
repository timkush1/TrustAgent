/**
 * The landing calculator's cost model: an adapter over the TS cost core (src/lib/decision/cost.ts,
 * pinned to Go's cost.go by the cost golden vectors), fed with contracts/pricing/assumptions.v1.json
 * and prices.v1.json. contracts/validate.mjs recomputes the stated figures from the same files, so
 * the calculator, the landing's labelled lines and the contract checks cannot disagree.
 *
 * The deep-audit judge is the reader's choice: any non-Jev model prices.v1.json prices (one option
 * per model, however many providers sell it), or a custom $/MTok. The comparison lines ("asking an
 * LLM judge the same questions", "a full LLM audit of every answer") stay on the assumptions' judge,
 * so changing the judge changes only the deep-audit cost (and the total it is part of).
 */
import assumptions from '@contracts/pricing/assumptions.v1.json';
import prices from '@contracts/pricing/prices.v1.json';
import {
  checkPer1kSettings,
  checkPriceEntry,
  costPer1k,
  isLocalProvider,
  isSimulatedProvider,
  PER_1K_SETTING_KEYS,
  priceCall,
  type ModelRef,
  type Per1kSettings,
} from '@/lib/decision/cost';
import type { PriceEntry, PricingTable } from '@/types/api';

export const PRICE_TABLE = prices as PricingTable;

type AssumptionId = keyof typeof assumptions.inputs;

export function defaultInput(id: AssumptionId): number {
  return assumptions.inputs[id].value;
}

/** A model the assumptions' price alias names (its glob without the wildcard). */
function aliasModel(alias: keyof typeof assumptions.prices): ModelRef {
  const ref = assumptions.prices[alias];
  return { provider: ref.provider, model: ref.match.replaceAll('*', '') };
}

/** The fast check's model (Jev through OpenRouter). */
export const DECISION_MODEL = aliasModel('decision_model');
/** The judge every stated estimate and comparison line uses. */
export const REFERENCE_JUDGE = aliasModel('judge');

/**
 * The shipped assumptions as costPer1k settings (they share their names). A setting the
 * assumptions leave out is 0: fast_check_output_tokens (Jev's output is free).
 */
export function defaultSettings(): Per1kSettings {
  const inputs = assumptions.inputs as Partial<Record<string, { value: number }>>;
  return Object.fromEntries(PER_1K_SETTING_KEYS.map((k) => [k, inputs[k]?.value ?? 0])) as unknown as Per1kSettings;
}

// ------------------------------------------------------------------------------- judge choices

/** One deep-audit judge the calculator offers from the price table. */
export interface JudgeOption {
  /** Stable id: provider and match of the entry that prices it. */
  id: string;
  /** "Claude Haiku 4.5", "Local model (Ollama)". */
  name: string;
  /** What costPer1k prices. */
  ref: ModelRef;
  /** The price table entry: its prices, source, date and note. */
  entry: PriceEntry;
  /** Runs on the reader's machine: no per-token price. */
  local: boolean;
}

const isDecisionModelEntry = (e: PriceEntry): boolean => /(^|\/)jev-/i.test(e.match);

/** "claude-haiku-4-5*" and "anthropic/claude-haiku-4.5" are one model: "claude-haiku-4-5". */
function modelKey(match: string): string {
  return (match.replace(/\*+$/, '').split('/').pop() ?? match).toLowerCase().replaceAll('.', '-');
}

const title = (w: string): string => (w ? w[0].toUpperCase() + w.slice(1) : w);

/** A readable model name from a price entry's glob: "claude-haiku-4-5*" → "Claude Haiku 4.5". */
export function modelName(e: Pick<PriceEntry, 'match' | 'provider'>): string {
  if (isLocalProvider(e.provider)) return `Local model (${title(e.provider)})`;
  const base = e.match.replace(/\*+$/, '').split('/').pop() ?? e.match;
  return base
    .replace(/(\d)-(?=\d)/g, '$1.')
    .split('-')
    .filter(Boolean)
    .map(title)
    .join(' ');
}

/**
 * The judges the price table prices, one per model, in table order: every entry except the
 * decision model's (Jev), mocks and a paid provider's catch-all. A model sold by several providers
 * at the same price is one option, priced by the assumptions' judge provider when it is one of
 * them. A local provider's catch-all is the local model.
 */
export function judgeOptions(table: PricingTable = PRICE_TABLE): JudgeOption[] {
  const groups = new Map<string, PriceEntry[]>();
  for (const e of table.entries) {
    if (isDecisionModelEntry(e) || isSimulatedProvider(e.provider)) continue;
    const local = isLocalProvider(e.provider);
    if (!local && e.match.replace(/\*/g, '') === '') continue;
    const key = local ? `local:${e.provider}` : `${modelKey(e.match)}:${e.input_per_mtok}:${e.output_per_mtok}`;
    groups.set(key, [...(groups.get(key) ?? []), e]);
  }
  return [...groups.values()].map((entries) => {
    const entry = entries.find((e) => e.provider === REFERENCE_JUDGE.provider) ?? entries[0];
    const local = isLocalProvider(entry.provider);
    const model = entry.match.replaceAll('*', '') || 'local';
    return { id: `${entry.provider}:${entry.match}`, name: modelName(entry), ref: { provider: entry.provider, model }, entry, local };
  });
}

/** The option that prices the reference judge (the default selection). */
export function referenceJudge(options: readonly JudgeOption[] = judgeOptions()): JudgeOption {
  const found = options.find((o) => o.ref.provider === REFERENCE_JUDGE.provider && modelKey(o.entry.match) === modelKey(REFERENCE_JUDGE.model));
  if (!found) throw new Error('contracts/pricing/prices.v1.json no longer prices the assumptions’ judge');
  return found;
}

/** A judge from the table, or the reader's own $/MTok. */
export type JudgeChoice = { kind: 'table'; option: JudgeOption } | { kind: 'custom'; inputPerMTok: number; outputPerMTok: number };

/** The provider and model a custom price is filed under; never a real provider. */
const CUSTOM: ModelRef = { provider: 'custom', model: 'custom-judge' };

// ------------------------------------------------------------------------------- the calculation

export interface CalculatorInputs {
  answersPerMonth: number;
  /** Fast-check (and same-questions) input tokens per answer. */
  tokensPerAnswer: number;
  /** Share of answers with a grounding trigger (always audited). */
  flagRate: number;
  /** Share of the other answers audited anyway. */
  passSampleRate: number;
  judge: JudgeChoice;
}

export interface MonthlyCost {
  fastCheck: number;
  deepAudit: number;
  total: number;
  /** The same questions asked of the reference judge, every answer. */
  sameChecks: number;
  /** A claim-level audit of every answer by the reference judge. */
  fullAudit: number;
  /** Share of answers deep-audited (flagged + sampled + uniform, at most 1). */
  auditedShare: number;
  /** Total per 1,000 answers. */
  totalPer1k: number;
  /** How many times the fast check fits in the same questions asked of an LLM judge. */
  sameChecksMultiple: number;
}

/** A field the calculator cannot compute with, and why. */
export interface CalculatorProblem {
  field: 'custom_input' | 'custom_output' | 'settings';
  message: string;
}

export type CalculatorResult = { ok: true; cost: MonthlyCost } | { ok: false; problem: CalculatorProblem };

/** The calculator's monthly figures, or the field that stops it (never a thrown RangeError). */
export function monthlyCost(c: CalculatorInputs): CalculatorResult {
  // Whole token counts (D150): round an estimate up, so a worst case is never understated.
  const tokens = Math.ceil(c.tokensPerAnswer);
  const settings: Per1kSettings = { ...defaultSettings(), fast_check_input_tokens: tokens, grounding_flag_rate: c.flagRate, pass_sample_rate: c.passSampleRate };
  const settingsProblem = checkPer1kSettings(settings);
  if (settingsProblem !== null) return { ok: false, problem: { field: 'settings', message: settingsProblem } };

  let table = PRICE_TABLE;
  let judge: ModelRef;
  if (c.judge.kind === 'custom') {
    const entry: PriceEntry = { match: CUSTOM.model, provider: CUSTOM.provider, input_per_mtok: c.judge.inputPerMTok, output_per_mtok: c.judge.outputPerMTok };
    const problem = checkPriceEntry(entry);
    if (problem !== null) {
      const field = problem.field === 'output_per_mtok' ? 'custom_output' : 'custom_input';
      return { ok: false, problem: { field, message: 'Enter a price of $0 or more per million tokens.' } };
    }
    table = { ...PRICE_TABLE, entries: [entry, ...PRICE_TABLE.entries] };
    judge = CUSTOM;
  } else {
    judge = c.judge.option.ref;
  }

  const chosen = costPer1k({ decision_model: DECISION_MODEL, judge, settings, table });
  const reference = costPer1k({ decision_model: DECISION_MODEL, judge: REFERENCE_JUDGE, settings, table: PRICE_TABLE });
  const same = priceCall({
    ...REFERENCE_JUDGE,
    input_tokens: tokens,
    output_tokens: defaultInput('same_checks_output_tokens'),
    table: PRICE_TABLE,
  });
  if (chosen.fast_check_per_1k === null || chosen.deep_audit_per_1k === null || chosen.total_per_1k === null || reference.deep_audit_usd === null || same.cost_usd === null) {
    return { ok: false, problem: { field: 'settings', message: 'A model in this estimate has no price in contracts/pricing/prices.v1.json.' } };
  }
  const per = c.answersPerMonth / 1000;
  const sameChecks = same.cost_usd * c.answersPerMonth;
  const fastCheck = chosen.fast_check_per_1k * per;
  return {
    ok: true,
    cost: {
      fastCheck,
      deepAudit: chosen.deep_audit_per_1k * per,
      total: chosen.total_per_1k * per,
      sameChecks,
      fullAudit: reference.deep_audit_usd * c.answersPerMonth,
      auditedShare: chosen.audit_rate,
      totalPer1k: chosen.total_per_1k,
      sameChecksMultiple: fastCheck > 0 ? sameChecks / fastCheck : Number.POSITIVE_INFINITY,
    },
  };
}

/** The calculator's starting inputs: the shipped assumptions and the reference judge. */
export function defaultCalculatorInputs(answersPerMonth = 100_000): CalculatorInputs {
  return {
    answersPerMonth,
    tokensPerAnswer: defaultInput('fast_check_input_tokens'),
    flagRate: defaultInput('grounding_flag_rate'),
    passSampleRate: defaultInput('pass_sample_rate'),
    judge: { kind: 'table', option: referenceJudge() },
  };
}

/** "≈ 27×": whole numbers from 10 up, one decimal below. */
export function formatMultiple(x: number): string {
  if (!Number.isFinite(x)) return 'many times';
  return `≈ ${x >= 10 ? Math.round(x) : Math.round(x * 10) / 10}×`;
}
