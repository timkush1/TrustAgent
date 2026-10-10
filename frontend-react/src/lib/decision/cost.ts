/**
 * The TypeScript port of backend-go internal/decision/cost.go (SPEC §7.4, D52, D53, D85): the
 * price lookup and the per-call and per-1,000-answers arithmetic the landing calculator, the live
 * Playground's running cost (D101) and the static Studio's estimates use. The cost golden vectors
 * (contracts/vectors/cost, every `fn`) pin it to the Go reference.
 *
 * Arithmetic is exact decimal, as in Go: a price is the decimal its shortest spelling names (the
 * spelling `String(x)` gives), products and sums are kept as exact BigInt decimals (an integer and a
 * power of ten), and each result is the float64 nearest the exact value, rounded once, ties to even.
 * 566 tokens at $0.042/M is therefore 2.3772e-5 exactly, never the naive 2.3772000000000002e-5.
 *
 * Pure: no clocks, randomness or I/O (purity.test.ts).
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

/** The settings of the per-1k calculator (Per1kInput.settings). */
export type Per1kSettings = Per1kInput['settings'];

/** The names of the settings, in the order Go validates them. */
export const PER_1K_SETTING_KEYS: readonly (keyof Per1kSettings)[] = [
  'fast_check_input_tokens',
  'fast_check_output_tokens',
  'claims_per_answer',
  'decompose_input_tokens',
  'decompose_output_tokens',
  'evidence_context_tokens',
  'verifier_prompt_tokens',
  'claim_tokens',
  'verify_output_tokens',
  'grounding_flag_rate',
  'pass_sample_rate',
  'uniform_sample_rate',
];

const RATE_KEYS: ReadonlySet<keyof Per1kSettings> = new Set(['grounding_flag_rate', 'pass_sample_rate', 'uniform_sample_rate']);

/** PriceEntry.label of a dashboard override, and the price_table_version of a cost it priced. */
export const PRICE_LABEL_OVERRIDE = 'override';
/** PriceEntry.label of a price table entry. */
export const PRICE_LABEL_VENDOR = 'vendor_price';

/** Whether provider runs on the operator's own hardware (Ollama), whose compute is not priced. */
export function isLocalProvider(provider: string): boolean {
  return provider === 'ollama';
}

/** Whether provider is a mock that calls nothing. */
export function isSimulatedProvider(provider: string): boolean {
  return provider === 'mock';
}

/** A number of dollars or tokens the calculator accepts: finite and not negative. */
function validAmount(x: number): boolean {
  return Number.isFinite(x) && x >= 0;
}

const isFree = (e: PriceEntry): boolean => e.input_per_mtok === 0 && e.output_per_mtok === 0;

// ------------------------------------------------------------------------------- exact decimals

/** An exact decimal: n × 10^-s, s ≥ 0. */
interface Dec {
  n: bigint;
  s: number;
}

const ZERO: Dec = { n: 0n, s: 0 };
const ONE: Dec = { n: 1n, s: 0 };
const THOUSAND: Dec = { n: 1000n, s: 0 };
const pow10 = (k: number): bigint => 10n ** BigInt(k);
const SPELLING = /^(-?)(\d+)(?:\.(\d+))?(?:e([+-]?\d+))?$/;

/** The exact decimal x spells: its shortest round-trip spelling (Go strconv 'g', -1; Python repr). */
function dec(x: number): Dec {
  const m = SPELLING.exec(String(x));
  if (m === null) throw new RangeError(`not a finite number: ${String(x)}`);
  const [, sign, whole, frac = '', exp = '0'] = m;
  const scale = frac.length - Number(exp);
  let n = BigInt(whole + frac);
  if (sign === '-') n = -n;
  return scale >= 0 ? { n, s: scale } : { n: n * pow10(-scale), s: 0 };
}

function add(a: Dec, b: Dec): Dec {
  const s = Math.max(a.s, b.s);
  return { n: a.n * pow10(s - a.s) + b.n * pow10(s - b.s), s };
}

const sum = (...xs: Dec[]): Dec => xs.reduce(add, ZERO);
const sub = (a: Dec, b: Dec): Dec => add(a, { n: -b.n, s: b.s });
const mul = (a: Dec, b: Dec): Dec => ({ n: a.n * b.n, s: a.s + b.s });
/** a / 10^k. */
const shift = (a: Dec, k: number): Dec => ({ n: a.n, s: a.s + k });
const cmp = (a: Dec, b: Dec): number => {
  const d = sub(a, b).n;
  return d < 0n ? -1 : d > 0n ? 1 : 0;
};
const toNumber = (a: Dec): number => nearestFloat(a.n, pow10(a.s));

const bitLength = (x: bigint): number => x.toString(2).length;
const TWO53 = 1n << 53n;

/**
 * The float64 nearest p / q (q > 0), ties to even, rounded once: Go's big.Rat Float64. Subnormal
 * results round at their own precision; results beyond the float64 range are ±Infinity.
 */
export function nearestFloat(p: bigint, q: bigint): number {
  if (q <= 0n) throw new RangeError('nearestFloat: the denominator must be positive');
  if (p < 0n) return -nearestFloat(-p, q);
  if (p === 0n) return 0;
  // e is the exponent of the last significand bit: p/q = m × 2^e with 2^52 ≤ m < 2^53 for normal
  // results; subnormal results keep e at -1074 and have fewer bits.
  let e = Math.max(bitLength(p) - bitLength(q) - 53, -1074);
  let m: bigint;
  let r: bigint;
  let den: bigint;
  for (;;) {
    const num = e >= 0 ? p : p << BigInt(-e);
    den = e >= 0 ? q << BigInt(e) : q;
    m = num / den;
    r = num - m * den;
    if (m < TWO53) break;
    e++;
  }
  const twice = 2n * r;
  if (twice > den || (twice === den && (m & 1n) === 1n)) m++;
  if (m === TWO53) {
    m = TWO53 >> 1n;
    e++;
  }
  return fromParts(m, e);
}

/** The float64 m × 2^e, for m < 2^53 with m ≥ 2^52 or e = -1074, assembled from its IEEE bits. */
function fromParts(m: bigint, e: number): number {
  const normal = m >= TWO53 >> 1n;
  const biased = normal ? e + 52 + 1023 : 0;
  if (biased >= 2047) return Infinity;
  const fraction = normal ? m - (TWO53 >> 1n) : m;
  const view = new DataView(new ArrayBuffer(8));
  view.setUint32(0, (biased << 20) | Number(fraction >> 32n));
  view.setUint32(4, Number(fraction & 0xffffffffn));
  return view.getFloat64(0);
}

// ---------------------------------------------------------------------------------- price table

/** `*` matches any run of characters (including none), everything else itself (cost.go GlobMatch). */
export function globMatch(pattern: string, s: string): boolean {
  const parts = pattern.split('*');
  if (parts.length === 1) return pattern === s;
  const first = parts[0];
  if (!s.startsWith(first)) return false;
  let rest = s.slice(first.length);
  for (const p of parts.slice(1, -1)) {
    const i = rest.indexOf(p);
    if (i < 0) return false;
    rest = rest.slice(i + p.length);
  }
  return rest.endsWith(parts[parts.length - 1]);
}

/**
 * The entry that prices model on provider (cost.go LookupPrice, D53): the first entry of that
 * provider whose glob matches; when the provider has none, the first matching entry of any other
 * provider, except that a local or mock provider never borrows a paid price and a local or mock
 * entry (a free `*` catch-all) never prices another provider's call. An empty provider takes the
 * second rule only. Null when no entry prices it.
 */
export function lookupPrice(entries: readonly PriceEntry[], provider: string, model: string): PriceEntry | null {
  if (provider !== '') {
    const own = entries.find((e) => e.provider === provider && globMatch(e.match, model));
    if (own !== undefined) return own;
    if (isLocalProvider(provider) || isSimulatedProvider(provider)) return null;
  }
  return entries.find((e) => !isLocalProvider(e.provider) && !isSimulatedProvider(e.provider) && globMatch(e.match, model)) ?? null;
}

/**
 * The price table costs are computed with (cost.go EffectivePrices, D53): the dashboard overrides
 * (labelled override), then the TRUTHTABLE_PRICE_TABLE_FILE table when one is set, then the
 * contracts table. The version is the file's when a file is set, otherwise the contracts version.
 * The inputs are not modified.
 */
export function effectivePrices(base: PricingTable, file: PricingTable | null, overrides: readonly PriceEntry[]): PricingTable {
  return {
    version: file !== null ? file.version : base.version,
    currency: 'USD',
    entries: [
      ...overrides.map((e): PriceEntry => ({ ...e, label: PRICE_LABEL_OVERRIDE })),
      ...(file !== null ? file.entries : []),
      ...base.entries,
    ],
  };
}

/** The price_table_version of a cost priced by e from t: "override" for an override, else t's version. */
function versionOf(t: PricingTable, e: PriceEntry): string {
  return e.label === PRICE_LABEL_OVERRIDE ? PRICE_LABEL_OVERRIDE : t.version;
}

const MAX_VERSION = 32;
const MAX_MATCH = 128;
const MAX_TEXT = 300;
const PROVIDER = /^[a-z0-9_-]{1,32}$/;
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;
const codePoints = (s: string): number => Array.from(s).length;

/** Whether s is a calendar date in the YYYY-MM-DD form (JSON Schema format date; Go time.DateOnly). */
function validDate(s: string): boolean {
  const m = DATE.exec(s);
  if (m === null) return false;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return mo >= 1 && mo <= 12 && d >= 1 && d <= days[mo - 1];
}

/** A problem with one price entry: the offending member and what is wrong with it. */
export interface PriceEntryProblem {
  field: keyof PriceEntry;
  problem: string;
}

/** Checks one entry against the pricing.v1 schema (cost.go CheckPriceEntry); null when it is valid. */
export function checkPriceEntry(e: PriceEntry): PriceEntryProblem | null {
  const text = (v: string | undefined): number => codePoints(v ?? '');
  if (e.match === '' || text(e.match) > MAX_MATCH) return { field: 'match', problem: `must be 1-${MAX_MATCH} characters` };
  if (!PROVIDER.test(e.provider)) return { field: 'provider', problem: 'must be 1-32 lowercase letters, digits, - or _' };
  if (!validAmount(e.input_per_mtok)) return { field: 'input_per_mtok', problem: 'must be a number of at least 0' };
  if (!validAmount(e.output_per_mtok)) return { field: 'output_per_mtok', problem: 'must be a number of at least 0' };
  if (text(e.source) > MAX_TEXT) return { field: 'source', problem: `must be at most ${MAX_TEXT} characters` };
  const label: string = e.label ?? '';
  if (label !== '' && label !== PRICE_LABEL_VENDOR && label !== PRICE_LABEL_OVERRIDE) {
    return { field: 'label', problem: 'must be vendor_price or override' };
  }
  if ((e.as_of ?? '') !== '' && !validDate(e.as_of ?? '')) return { field: 'as_of', problem: 'must be a date (YYYY-MM-DD)' };
  if (text(e.note) > MAX_TEXT) return { field: 'note', problem: `must be at most ${MAX_TEXT} characters` };
  return null;
}

/**
 * Checks a pricing.v1 table (cost.go PriceTable.Validate): a version of 1-32 characters, the USD
 * currency, at least one entry and every entry's rules. Null when it is valid, otherwise the problem.
 */
export function checkPriceTable(t: PricingTable): string | null {
  const bad = (msg: string): string => `invalid price table: ${msg}`;
  if (t.version === '' || codePoints(t.version) > MAX_VERSION) return bad(`version must be 1-${MAX_VERSION} characters`);
  if ((t.currency as string) !== 'USD') return bad('currency must be USD');
  if (t.entries.length === 0) return bad('entries must not be empty');
  for (const [i, e] of t.entries.entries()) {
    const p = checkPriceEntry(e);
    if (p !== null) return bad(`entries[${i}].${p.field} ${p.problem}`);
  }
  return null;
}

// ----------------------------------------------------------------------------------- arithmetic

/** A token count as an exact decimal: a whole number, negative counts as 0. */
function tokens(x: number, what: string): Dec {
  if (!Number.isInteger(x)) throw new RangeError(`${what} must be a whole number of tokens, got ${String(x)}`);
  return dec(Math.max(x, 0));
}

/**
 * inputTokens × inputPerMTok + outputTokens × outputPerMTok, divided by one million, as the float64
 * nearest its exact decimal value (cost.go TokenCostUSD). Negative token counts count as 0; a token
 * count that is not a whole number or a price that is not finite throws a RangeError.
 */
export function tokenCostUSD(inputTokens: number, outputTokens: number, inputPerMTok: number, outputPerMTok: number): number {
  const usd = add(mul(tokens(inputTokens, 'input_tokens'), dec(inputPerMTok)), mul(tokens(outputTokens, 'output_tokens'), dec(outputPerMTok)));
  return toNumber(shift(usd, 6));
}

// ------------------------------------------------------------------------------------ call_cost

/**
 * The cost of one call (cost.go PriceCall, SPEC §7.4). The first rule that applies wins:
 *
 *   a mock provider                           → $0, simulated
 *   the provider reported the cost            → that cost, reported
 *   a local provider with no or a free entry  → $0, free_local
 *   no entry prices the model (lookupPrice)   → unknown (cost null)
 *   otherwise                                 → tokens × the entry's prices, computed
 *
 * A reported cost below $0 (or not finite) is not a cost: it is ignored and the call is priced from
 * the table, so a malformed report can never lower the spend. price_table_version is "override"
 * when a dashboard override priced the call, otherwise the table's version.
 */
export function priceCall(input: CallCostInput): CallCost {
  const version = input.table.version;
  if (isSimulatedProvider(input.provider)) return { cost_usd: 0, cost_source: 'simulated', price_table_version: version };
  const reported = input.reported_cost_usd;
  if (typeof reported === 'number' && validAmount(reported)) {
    return { cost_usd: reported, cost_source: 'reported', price_table_version: version };
  }
  const e = lookupPrice(input.table.entries, input.provider, input.model);
  const out: CallCost = { cost_usd: null, cost_source: 'unknown', price_table_version: e !== null ? versionOf(input.table, e) : version };
  if (isLocalProvider(input.provider) && (e === null || isFree(e))) return { ...out, cost_usd: 0, cost_source: 'free_local' };
  if (e === null) return out;
  return { ...out, cost_usd: tokenCostUSD(input.input_tokens, input.output_tokens, e.input_per_mtok, e.output_per_mtok), cost_source: 'computed' };
}

// --------------------------------------------------------------------------------- price_lookup

/** The entry as Go marshals it: the optional members only when they are not empty. */
function entryOut(e: PriceEntry): PriceEntry {
  const out: PriceEntry = { match: e.match, provider: e.provider, input_per_mtok: e.input_per_mtok, output_per_mtok: e.output_per_mtok };
  if (e.source !== undefined && e.source !== '') out.source = e.source;
  if (e.label !== undefined && (e.label as string) !== '') out.label = e.label;
  if (e.as_of !== undefined && e.as_of !== '') out.as_of = e.as_of;
  if (e.note !== undefined && e.note !== '') out.note = e.note;
  return out;
}

/**
 * The entry that prices `model` on `provider` in the effective table, and the price_table_version a
 * cost priced by it records (cost.go LookupEffective, D53). The entry is null when none prices it.
 */
export function lookupEffective(input: PriceLookupInput): PriceLookup {
  const t = effectivePrices(input.contracts, input.file, input.overrides);
  const e = lookupPrice(t.entries, input.provider, input.model);
  return e === null ? { entry: null, price_table_version: t.version } : { entry: entryOut(e), price_table_version: versionOf(t, e) };
}

// --------------------------------------------------------------------------------------- per_1k

/**
 * The problem with per-1k settings (cost.go Per1kSettings.Validate), or null: every number must be
 * finite and at least 0, and the three rates at most 1.
 */
export function checkPer1kSettings(s: Per1kSettings): string | null {
  for (const k of PER_1K_SETTING_KEYS) {
    const v = s[k];
    if (RATE_KEYS.has(k) && !(validAmount(v) && v <= 1)) return `invalid per-1k settings: ${k} must be between 0 and 1`;
    if (!validAmount(v)) return `invalid per-1k settings: ${k} must be a number of at least 0`;
  }
  return null;
}

/** The exact cost of the tokens on m priced from t and its cost source; the cost is null when unknown. */
function estimate(t: PricingTable, m: ModelRef, input: Dec, output: Dec): [Dec | null, CallCostSource] {
  if (isSimulatedProvider(m.provider)) return [ZERO, 'simulated'];
  const e = lookupPrice(t.entries, m.provider, m.model);
  if (isLocalProvider(m.provider) && (e === null || isFree(e))) return [ZERO, 'free_local'];
  if (e === null) return [null, 'unknown'];
  return [shift(add(mul(input, dec(e.input_per_mtok)), mul(output, dec(e.output_per_mtok))), 6), 'computed'];
}

/**
 * The estimated cost of 1,000 answers (cost.go CostPer1k; SPEC §1 cost story, §7.4): one fast check
 * per answer, plus a deep audit of the audited share. A deep audit is one decompose call and one
 * verify call per claim; the audited share is flagged + (1 − flagged) × pass sample + uniform
 * sample, at most 1. A cost is null when its model's price is unknown, and the total is null when
 * either part is. With the shipped defaults of contracts/pricing/assumptions.v1.json it is $3.0145.
 *
 * @throws RangeError when the settings are invalid (checkPer1kSettings).
 */
export function costPer1k(input: Per1kInput): Per1k {
  const s = input.settings;
  const problem = checkPer1kSettings(s);
  if (problem !== null) throw new RangeError(problem);

  const flagged = dec(s.grounding_flag_rate);
  let rate = sum(flagged, mul(sub(ONE, flagged), dec(s.pass_sample_rate)), dec(s.uniform_sample_rate));
  if (cmp(rate, ONE) > 0) rate = ONE;

  const [fast, decisionSource] = estimate(input.table, input.decision_model, dec(s.fast_check_input_tokens), dec(s.fast_check_output_tokens));
  const claims = dec(s.claims_per_answer);
  const auditIn = add(dec(s.decompose_input_tokens), mul(claims, sum(dec(s.evidence_context_tokens), dec(s.verifier_prompt_tokens), dec(s.claim_tokens))));
  const auditOut = add(dec(s.decompose_output_tokens), mul(claims, dec(s.verify_output_tokens)));
  const [audit, judgeSource] = estimate(input.table, input.judge, auditIn, auditOut);

  const fastPer1k = fast !== null ? mul(fast, THOUSAND) : null;
  const auditPer1k = audit !== null ? mul(mul(audit, rate), THOUSAND) : null;
  const num = (d: Dec | null): number | null => (d !== null ? toNumber(d) : null);
  return {
    audit_rate: toNumber(rate),
    fast_check_usd: num(fast),
    fast_check_per_1k: num(fastPer1k),
    deep_audit_usd: num(audit),
    deep_audit_per_1k: num(auditPer1k),
    total_per_1k: fastPer1k !== null && auditPer1k !== null ? toNumber(add(fastPer1k, auditPer1k)) : null,
    decision_cost_source: decisionSource,
    judge_cost_source: judgeSource,
  };
}
