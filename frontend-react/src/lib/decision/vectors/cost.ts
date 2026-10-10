/**
 * The `cost` vector kind's TS runner (contracts/vectors/cost) over lib/decision/cost.ts, mirroring
 * backend-go/internal/decision/vectors/cost.go. The input's `fn` names the function; the other
 * members are its arguments, and every price table is inline in the pricing.v1 shape:
 *
 *   call_cost:    {fn, provider, model, input_tokens, output_tokens, reported_cost_usd?, table}
 *                 -> CallCost {cost_usd, cost_source, price_table_version}
 *   price_lookup: {fn, contracts, file?, overrides, provider, model}
 *                 -> PriceLookup {entry, price_table_version}
 *   per_1k:       {fn, table, decision_model, judge, settings}
 *                 -> Per1k
 *
 * Decoding is as strict as Go's (DisallowUnknownFields at every depth; an absent or null member is
 * its zero value), and the tables are checked against pricing.v1 before use, so a malformed vector
 * fails instead of pinning a wrong expected value.
 */
import type { PriceEntry, PricingTable } from '@/types/api';
import {
  checkPriceEntry,
  checkPriceTable,
  costPer1k,
  lookupEffective,
  PER_1K_SETTING_KEYS,
  priceCall,
  type ModelRef,
  type Per1kSettings,
} from '../cost';
import type { VectorKind } from './kinds';

type Obj = Record<string, unknown>;

/** The value at `at` as an object with only the allowed members. */
function object(v: unknown, at: string, allowed: readonly string[]): Obj {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) throw new Error(`${at}: want an object`);
  for (const k of Object.keys(v)) if (!allowed.includes(k)) throw new Error(`${at}: unknown field ${JSON.stringify(k)}`);
  return v as Obj;
}

/** Member k of o, or undefined when it is absent or null (Go leaves the zero value). */
const member = (o: Obj, k: string): unknown => (o[k] === null ? undefined : o[k]);

function str(o: Obj, k: string, at: string): string {
  const v = member(o, k);
  if (v === undefined) return '';
  if (typeof v !== 'string') throw new Error(`${at}.${k}: want a string`);
  return v;
}

function num(o: Obj, k: string, at: string): number {
  const v = member(o, k);
  if (v === undefined) return 0;
  if (typeof v !== 'number') throw new Error(`${at}.${k}: want a number`);
  return v;
}

function int(o: Obj, k: string, at: string): number {
  const v = num(o, k, at);
  if (!Number.isSafeInteger(v)) throw new Error(`${at}.${k}: want a whole number`);
  return v;
}

const ENTRY_FIELDS = ['match', 'provider', 'input_per_mtok', 'output_per_mtok', 'source', 'label', 'as_of', 'note'] as const;

function entry(v: unknown, at: string): PriceEntry {
  const o = object(v, at, ENTRY_FIELDS);
  const e: PriceEntry = {
    match: str(o, 'match', at),
    provider: str(o, 'provider', at),
    input_per_mtok: num(o, 'input_per_mtok', at),
    output_per_mtok: num(o, 'output_per_mtok', at),
  };
  for (const k of ['source', 'label', 'as_of', 'note'] as const) {
    const s = str(o, k, at);
    // checkPriceEntry refuses a label other than the two it knows.
    if (s !== '') (e as unknown as Obj)[k] = s;
  }
  return e;
}

function entries(v: unknown, at: string): PriceEntry[] {
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v)) throw new Error(`${at}: want an array`);
  return v.map((x, i) => entry(x, `${at}[${i}]`));
}

/** A pricing.v1 table at `at`, checked against the schema (the error is prefixed with `at`). */
function table(v: unknown, at: string): PricingTable {
  const o = object(v ?? {}, at, ['version', 'currency', 'entries']);
  const t = { version: str(o, 'version', at), currency: str(o, 'currency', at), entries: entries(member(o, 'entries'), `${at}.entries`) } as PricingTable;
  const problem = checkPriceTable(t);
  if (problem !== null) throw new Error(`${at}: ${problem}`);
  return t;
}

function modelRef(v: unknown, at: string): ModelRef {
  const o = object(v ?? {}, at, ['provider', 'model']);
  return { provider: str(o, 'provider', at), model: str(o, 'model', at) };
}

function settings(v: unknown): Per1kSettings {
  const o = object(v ?? {}, 'settings', PER_1K_SETTING_KEYS);
  return Object.fromEntries(PER_1K_SETTING_KEYS.map((k) => [k, num(o, k, 'settings')])) as unknown as Per1kSettings;
}

const FNS = ['call_cost', 'price_lookup', 'per_1k'] as const;

function runCost(input: unknown): unknown {
  if (typeof input !== 'object' || input === null || Array.isArray(input)) throw new Error('input: want an object');
  const fn = member(input as Obj, 'fn') ?? '';
  switch (fn) {
    case 'call_cost': {
      const x = object(input, 'input', ['fn', 'provider', 'model', 'input_tokens', 'output_tokens', 'reported_cost_usd', 'table']);
      const reported = member(x, 'reported_cost_usd');
      return priceCall({
        provider: str(x, 'provider', 'input'),
        model: str(x, 'model', 'input'),
        input_tokens: int(x, 'input_tokens', 'input'),
        output_tokens: int(x, 'output_tokens', 'input'),
        reported_cost_usd: reported === undefined ? null : num(x, 'reported_cost_usd', 'input'),
        table: table(member(x, 'table'), 'table'),
      });
    }
    case 'price_lookup': {
      const x = object(input, 'input', ['fn', 'contracts', 'file', 'overrides', 'provider', 'model']);
      const contracts = table(member(x, 'contracts'), 'contracts');
      const file = member(x, 'file') === undefined ? null : table(x.file, 'file');
      const overrides = entries(member(x, 'overrides'), 'overrides');
      for (const [i, e] of overrides.entries()) {
        const p = checkPriceEntry(e);
        if (p !== null) throw new Error(`overrides[${i}].${p.field} ${p.problem}`);
      }
      return lookupEffective({ contracts, file, overrides, provider: str(x, 'provider', 'input'), model: str(x, 'model', 'input') });
    }
    case 'per_1k': {
      const x = object(input, 'input', ['fn', 'table', 'decision_model', 'judge', 'settings']);
      return costPer1k({
        table: table(member(x, 'table'), 'table'),
        decision_model: modelRef(member(x, 'decision_model'), 'decision_model'),
        judge: modelRef(member(x, 'judge'), 'judge'),
        settings: settings(member(x, 'settings')),
      });
    }
  }
  throw new Error(`cost vector fn ${JSON.stringify(typeof fn === 'string' ? fn : String(fn))} is not one of ${FNS.join(', ')}`);
}

export const costKind: VectorKind = {
  run: runCost,
  go: 'all fn (internal/decision/cost.go)',
};
