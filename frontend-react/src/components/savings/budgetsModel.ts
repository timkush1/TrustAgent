/**
 * Settings → Budgets as pure functions (SPEC §3.3 "Budgets: two inputs with today's spend bars";
 * P4-PLAN D33, D46, D53, D77): reading the caps and what is env-managed from SettingsView, the
 * spend bars from /api/status (real spend only), the patch a save sends, and the price-override
 * rows with their checks and the server's findings.
 */
import type { FieldError, PriceEntry, RuntimeSettings, RuntimeSettingsPatch, SettingsView, SystemStatus } from '@/types/api';
import { costBudgetView, type CostBudgetKind } from '@/domain/presentation';

export const BUDGET_KINDS: readonly CostBudgetKind[] = ['decision', 'llm'];

/** The env var that pins each cap (D33: `budgets.*` ← `TRUTHTABLE_SPEND_CAP_{DECISION,LLM}_USD_DAY`). */
export const BUDGET_ENV: Record<CostBudgetKind, string> = {
  decision: 'TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY',
  llm: 'TRUTHTABLE_SPEND_CAP_LLM_USD_DAY',
};

const RESERVE_KEY = 'TRUTHTABLE_SPEND_ENFORCING_RESERVE';
/** The registry default of the enforcing reserve: observe and flag routes stop at 75% of the cap. */
export const DEFAULT_RESERVE = 0.25;
/** The largest cap the form accepts (USD per day); anything above is almost surely a typo. */
export const MAX_CAP_USD = 100_000;

const field = (kind: CostBudgetKind): keyof RuntimeSettings['budgets'] => (kind === 'decision' ? 'decision_usd_day' : 'llm_usd_day');

export function capOf(settings: Pick<SettingsView, 'runtime'>, kind: CostBudgetKind): number {
  return settings.runtime.budgets[field(kind)];
}

/** A settings path is env-managed itself or under an env-managed parent (`budgets`). */
export function isEnvManaged(envManaged: readonly string[], path: string): boolean {
  return envManaged.some((m) => path === m || path.startsWith(`${m}.`));
}

export function budgetEnvManaged(settings: Pick<SettingsView, 'env_managed'>, kind: CostBudgetKind): boolean {
  return isEnvManaged(settings.env_managed, costBudgetView(kind).path);
}

/**
 * The enforcing reserve (TRUTHTABLE_SPEND_ENFORCING_RESERVE, 0-1) as SettingsView lists it, or the
 * default when it is not listed or not a share.
 */
export function enforcingReserve(settings: Pick<SettingsView, 'groups'>): number {
  for (const g of settings.groups) {
    const key = g.keys.find((k) => k.key === RESERVE_KEY);
    if (!key) continue;
    // An empty value is unset (Number('') would read it as 0): the key's default applies.
    const text = key.value.trim() !== '' ? key.value : key.default;
    const v = text.trim() === '' ? Number.NaN : Number(text);
    return Number.isFinite(v) && v >= 0 && v <= 1 ? v : DEFAULT_RESERVE;
  }
  return DEFAULT_RESERVE;
}

export interface Spend {
  spent: number;
  exhausted: boolean;
  /** The decision cap's share for observe and flag routes is spent: only enforcing routes are checked. */
  reserveOnly: boolean;
}

/** Today's real spend (D46: simulated checks never count) from /api/status, or null without it. */
export function spendToday(status: SystemStatus | null, kind: CostBudgetKind): Spend | null {
  const b = status?.budgets;
  if (!b) return null;
  return kind === 'decision'
    ? { spent: b.decision_spent_today_usd, exhausted: b.decision_exhausted, reserveOnly: b.decision_reserve_only }
    : { spent: b.llm_spent_today_usd, exhausted: b.llm_exhausted, reserveOnly: false };
}

/** Parses a cap typed in USD per day: a number from 0 (no cap) to MAX_CAP_USD, at most 2 decimals. */
export function parseCap(text: string): { value: number } | { error: string } {
  const t = text.trim();
  if (t === '') return { error: 'Enter a daily amount in USD (0 means no cap).' };
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return { error: 'Use dollars and cents, like 2 or 2.50.' };
  const value = Number(t);
  if (value > MAX_CAP_USD) return { error: `Use at most $${MAX_CAP_USD.toLocaleString('en-US')} a day.` };
  return { value };
}

/** The text a cap shows in its input ("2", "2.5"). */
export function capText(value: number): string {
  return String(Math.round(value * 100) / 100);
}

export type Caps = Record<CostBudgetKind, number>;
export type CapDrafts = Record<CostBudgetKind, string>;

export function capsOf(settings: Pick<SettingsView, 'runtime'>): Caps {
  return { decision: capOf(settings, 'decision'), llm: capOf(settings, 'llm') };
}

export function capDrafts(caps: Caps): CapDrafts {
  return { decision: capText(caps.decision), llm: capText(caps.llm) };
}

/**
 * The cap drafts carried over to newer saved caps (useSettingsDraft): a cap changed elsewhere
 * replaces an unedited draft, and is a conflict (null) when the viewer edited that cap too.
 */
export function rebaseCaps(drafts: CapDrafts, base: Caps, next: Caps): CapDrafts | null {
  const out = { ...drafts };
  for (const kind of BUDGET_KINDS) {
    if (next[kind] === base[kind]) continue;
    if (drafts[kind] !== capText(base[kind])) return null;
    out[kind] = capText(next[kind]);
  }
  return out;
}

/** The patch a save sends: only the caps that changed (null when nothing did). */
export function budgetsPatch(settings: Pick<SettingsView, 'runtime'>, next: Partial<Record<CostBudgetKind, number>>): RuntimeSettingsPatch | null {
  const budgets: Partial<RuntimeSettings['budgets']> = {};
  for (const kind of BUDGET_KINDS) {
    const v = next[kind];
    if (v !== undefined && v !== capOf(settings, kind)) budgets[field(kind)] = v;
  }
  return Object.keys(budgets).length > 0 ? { budgets } : null;
}

// ------------------------------------------------------------------------------------- overrides

export interface OverrideRow {
  /** A key for React; never sent. */
  id: string;
  provider: string;
  match: string;
  input: string;
  output: string;
  note: string;
}

let rowSeq = 0;
export function newRowId(): string {
  rowSeq += 1;
  return `override-${rowSeq}`;
}

export function rowsFromOverrides(overrides: readonly PriceEntry[]): OverrideRow[] {
  return overrides.map((o) => ({
    id: newRowId(),
    provider: o.provider,
    match: o.match,
    input: String(o.input_per_mtok),
    output: String(o.output_per_mtok),
    note: o.note ?? '',
  }));
}

export function emptyRow(): OverrideRow {
  return { id: newRowId(), provider: 'openrouter', match: '', input: '', output: '', note: '' };
}

const PROVIDER_RE = /^[a-z0-9_-]{1,32}$/;
const PRICE_RE = /^\d+(\.\d+)?$/;

export type RowField = 'provider' | 'match' | 'input_per_mtok' | 'output_per_mtok' | 'note';

/** What is wrong with a row before it is sent (pricing.v1 rules), per field. */
export function rowProblems(row: OverrideRow): Partial<Record<RowField, string>> {
  const out: Partial<Record<RowField, string>> = {};
  if (!PROVIDER_RE.test(row.provider.trim())) out.provider = 'Use the provider id: lowercase letters, digits, - or _.';
  const match = row.match.trim();
  if (match === '' || match.length > 128) out.match = 'Name the model (a * matches any ending, like claude-haiku-4-5*).';
  if (!PRICE_RE.test(row.input.trim())) out.input_per_mtok = 'A price of 0 or more, in USD per million tokens.';
  if (!PRICE_RE.test(row.output.trim())) out.output_per_mtok = 'A price of 0 or more, in USD per million tokens.';
  if (row.note.length > 300) out.note = 'Keep the note under 300 characters.';
  return out;
}

/** The overrides a save sends, in order (first match wins, D53); each labelled `override`. */
export function overridesFromRows(rows: readonly OverrideRow[]): PriceEntry[] {
  return rows.map((r) => ({
    provider: r.provider.trim(),
    match: r.match.trim(),
    input_per_mtok: Number(r.input.trim()),
    output_per_mtok: Number(r.output.trim()),
    label: 'override',
    ...(r.note.trim() ? { note: r.note.trim() } : {}),
  }));
}

/** Two override lists price the same models the same way, in the same order. */
export function sameEntries(a: readonly PriceEntry[], b: readonly PriceEntry[]): boolean {
  return (
    a.length === b.length &&
    a.every((e, i) => {
      const s = b[i];
      return (
        e.provider === s.provider &&
        e.match === s.match &&
        e.input_per_mtok === s.input_per_mtok &&
        e.output_per_mtok === s.output_per_mtok &&
        (e.note ?? '') === (s.note ?? '')
      );
    })
  );
}

/** Rows and saved overrides say the same thing (so there is nothing to save). */
export function sameOverrides(rows: readonly OverrideRow[], saved: readonly PriceEntry[]): boolean {
  return rows.length === saved.length && sameEntries(overridesFromRows(rows), saved);
}

/**
 * The override rows carried over to a newer saved list (useSettingsDraft): kept when the list did
 * not change, replaced when the viewer had not edited it, and a conflict (null) otherwise.
 */
export function rebaseOverrides(rows: OverrideRow[], base: readonly PriceEntry[], next: readonly PriceEntry[]): OverrideRow[] | null {
  if (sameEntries(next, base)) return rows;
  return sameOverrides(rows, base) ? rowsFromOverrides(next) : null;
}

const OVERRIDE_PATH = /^pricing_overrides\[(\d+)\](?:\.([a-z_]+))?$/;

export interface RowFinding {
  index: number;
  field: string | null;
  finding: FieldError;
}

/** The server's field errors on `pricing_overrides[i].field`, by row (others are left out). */
export function overrideFindings(errors: readonly FieldError[]): RowFinding[] {
  const out: RowFinding[] = [];
  for (const e of errors) {
    const m = OVERRIDE_PATH.exec(e.path);
    if (m) out.push({ index: Number(m[1]), field: m[2] ?? null, finding: e });
  }
  return out;
}
