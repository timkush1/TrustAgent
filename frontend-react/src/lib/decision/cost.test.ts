// @vitest-environment node
/**
 * Unit tests of the TS cost calculator, ported from backend-go/internal/decision/cost_test.go and
 * vectors/cost_test.go. The golden vectors (vectors/vectors.test.ts, contracts/vectors/cost) pin
 * every `fn` to the Go reference; these cover the rules one by one, the exact-decimal rounding and
 * the runner's refusals.
 */
import { describe, expect, it } from 'vitest';
import assumptions from '@contracts/pricing/assumptions.v1.json';
import prices from '@contracts/pricing/prices.v1.json';
import type { PriceEntry, PricingTable } from '@/types/api';
import {
  checkPer1kSettings,
  checkPriceEntry,
  checkPriceTable,
  costPer1k,
  effectivePrices,
  globMatch,
  lookupEffective,
  lookupPrice,
  nearestFloat,
  PER_1K_SETTING_KEYS,
  priceCall,
  tokenCostUSD,
  type ModelRef,
  type Per1kInput,
  type Per1kSettings,
} from './cost';
import { runVectorKind } from './vectors/kinds';

const contracts = prices as PricingTable;

describe('globMatch', () => {
  it.each([
    ['typesafe/jev-1.13*', 'typesafe/jev-1.13-20260917', true],
    ['typesafe/jev-1.13*', 'typesafe/jev-1.12', false],
    ['jev-*', 'jev-1.13.0', true],
    ['*', 'anything/at-all', true],
    ['*', '', true],
    ['a*b*c', 'axxbyyc', true],
    ['a*b*c', 'axxbyy', false],
    ['ab*bc', 'abc', false], // the prefix and the suffix may not overlap
    ['a*a', 'aa', true],
    ['*.5', 'claude-haiku-4.5', true],
    ['claude.haiku', 'claudeXhaiku', false], // only * is special
    ['claude?haiku', 'claudeXhaiku', false], // ? too (unlike the policy globs)
    ['exact', 'exact', true],
    ['exact', 'exact2', false],
  ])('%j ~ %j is %s', (pattern, s, want) => {
    expect(globMatch(pattern, s)).toBe(want);
  });
});

const testEntries = (): PriceEntry[] => [
  { match: 'anthropic/claude-haiku-4.5', provider: 'openrouter', input_per_mtok: 1, output_per_mtok: 5 },
  { match: '*', provider: 'ollama', input_per_mtok: 0, output_per_mtok: 0 },
  { match: 'claude-haiku-4-5*', provider: 'anthropic', input_per_mtok: 1, output_per_mtok: 5 },
  { match: 'anthropic/claude-haiku-4.5', provider: 'anthropic', input_per_mtok: 2, output_per_mtok: 10 },
  { match: '*', provider: 'mock', input_per_mtok: 9, output_per_mtok: 9 },
];

describe('lookupPrice', () => {
  const entries = testEntries();
  it.each([
    ['anthropic', 'anthropic/claude-haiku-4.5', 3], // the provider's own entry beats an earlier one
    ['anthropic', 'claude-haiku-4-5-20251001', 2], // glob
    ['compatible', 'anthropic/claude-haiku-4.5', 0], // no entry of its own: the first other match
    ['', 'anthropic/claude-haiku-4.5', 0], // no pricing provider: any provider
    ['ollama', 'llama3.1:8b', 1], // a local provider's own (free) entry
    ['ollama', 'anthropic/claude-haiku-4.5', 1],
    ['openai', 'gpt-4o-mini', -1], // the free ollama and mock catch-alls never price others
    ['', 'gpt-4o-mini', -1],
  ])('%j %j resolves to entry %i', (provider, model, want) => {
    expect(lookupPrice(entries, provider, model)).toBe(want < 0 ? null : entries[want]);
  });

  it.each(['ollama', 'mock'])('never lets %s borrow a paid price', (provider) => {
    expect(lookupPrice([{ match: '*', provider: 'openrouter', input_per_mtok: 1, output_per_mtok: 0 }], provider, 'llama3.1:8b')).toBeNull();
  });
});

describe('tokenCostUSD', () => {
  it.each([
    [566, 0, 0.042, 0, '0.000023772'],
    [1, 0, 0.042, 0, '4.2e-8'],
    [24275, 0, 0.042, 0, '0.00101955'],
    [3525, 120, 1, 5, '0.004125'],
    [0, 0, 1, 5, '0'],
    [-10, -3, 1, 5, '0'],
    [1000, 0, 0.0421234567, 0, '0.0000421234567'], // more than six decimals
    [1_000_000_000_000, 7, 3, 15, '3000000.000105'], // beyond 2^53 pico-dollars
    [3, 0, 0.1, 0, '3e-7'], // 3 × 0.1 is not 0.30000000000000004 here
    [123456789, 987654321, 0.000001, 0.000003, '0.003086419752'], // a micro-dollar price
  ])('%i in + %i out at $%s/$%s per M is %s', (input, output, pin, pout, want) => {
    expect(JSON.stringify(tokenCostUSD(input, output, pin, pout))).toBe(want);
  });

  it('is exact where the naive float product is not', () => {
    expect((566 * 0.042) / 1e6).not.toBe(0.000023772);
    expect(tokenCostUSD(566, 3, 0.042, 0)).toBe(0.000023772);
  });

  it('refuses token counts that are not whole numbers and prices that are not finite', () => {
    expect(() => tokenCostUSD(1.5, 0, 1, 1)).toThrow(RangeError);
    expect(() => tokenCostUSD(Number.NaN, 0, 1, 1)).toThrow(/input_tokens/);
    expect(() => tokenCostUSD(1, 1, Number.POSITIVE_INFINITY, 1)).toThrow(RangeError);
  });
});

describe('nearestFloat', () => {
  // A deterministic generator (the decision core and its tests take no randomness).
  function* decimals(count: number): Generator<[bigint, number]> {
    let seed = 0x2545f491;
    const next = (): number => {
      seed = (Math.imul(seed, 1103515245) + 12345) >>> 0;
      return seed;
    };
    for (let i = 0; i < count; i++) {
      const digits = 1 + (next() % 40);
      let n = '';
      for (let d = 0; d < digits; d++) n += String(next() % 10);
      yield [BigInt(n), next() % 60];
    }
  }

  it('agrees with the correctly rounded string parse for exact decimals', () => {
    for (const [n, s] of decimals(2000)) expect(nearestFloat(n, 10n ** BigInt(s)), `${n}e-${s}`).toBe(Number(`${n}e-${s}`));
  });

  it('rounds ties to even', () => {
    const two53 = 2n ** 53n;
    expect(nearestFloat(two53 + 1n, 1n)).toBe(2 ** 53);
    expect(nearestFloat(two53 + 3n, 1n)).toBe(2 ** 53 + 4);
    expect(nearestFloat(1n, 3n)).toBe(1 / 3);
    expect(nearestFloat(-1n, 3n)).toBe(-1 / 3);
  });

  it('handles zero, subnormals and overflow', () => {
    expect(nearestFloat(0n, 7n)).toBe(0);
    expect(nearestFloat(1n, 2n ** 1074n)).toBe(Number.MIN_VALUE);
    expect(nearestFloat(1n, 2n ** 1075n)).toBe(0); // half the smallest subnormal: the tie goes to 0
    expect(nearestFloat(3n, 2n ** 1076n)).toBe(Number.MIN_VALUE);
    expect(nearestFloat(1n, 2n ** 1022n)).toBe(2 ** -1022);
    expect(nearestFloat(BigInt(Number.MAX_VALUE), 1n)).toBe(Number.MAX_VALUE);
    expect(nearestFloat(2n ** 1024n, 1n)).toBe(Number.POSITIVE_INFINITY);
    expect(() => nearestFloat(1n, 0n)).toThrow(RangeError);
  });
});

describe('priceCall', () => {
  const table: PricingTable = {
    version: '2026-09-27',
    currency: 'USD',
    entries: [{ match: 'anthropic/claude-sonnet-*', provider: 'openrouter', input_per_mtok: 3, output_per_mtok: 15, label: 'override' }, ...testEntries()],
  };
  const haiku = { provider: 'openrouter', model: 'anthropic/claude-haiku-4.5', input_tokens: 3525, output_tokens: 120, table };

  it.each([
    ['computed', haiku, 0.004125, 'computed', '2026-09-27'],
    ['reported wins', { ...haiku, reported_cost_usd: 0.0031 }, 0.0031, 'reported', '2026-09-27'],
    ['override', { ...haiku, model: 'anthropic/claude-sonnet-4.5', input_tokens: 1000, output_tokens: 100 }, 0.0045, 'computed', 'override'],
    ['local', { ...haiku, provider: 'ollama', model: 'llama3.1:8b', input_tokens: 5000, output_tokens: 500 }, 0, 'free_local', '2026-09-27'],
    ['reported $0', { ...haiku, reported_cost_usd: 0 }, 0, 'reported', '2026-09-27'],
    ['negative report ignored', { ...haiku, reported_cost_usd: -0.002 }, 0.004125, 'computed', '2026-09-27'],
    ['mock beats reported', { ...haiku, provider: 'mock', model: 'mock-judge', reported_cost_usd: 0.0031 }, 0, 'simulated', '2026-09-27'],
    ['unknown', { ...haiku, provider: 'openai', model: 'gpt-4o-mini' }, null, 'unknown', '2026-09-27'],
  ] as const)('%s', (_name, input, usd, source, version) => {
    expect(priceCall(input)).toEqual({ cost_usd: usd, cost_source: source, price_table_version: version });
  });

  it.each([Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY])('ignores a reported cost of %s', (bad) => {
    expect(priceCall({ provider: 'openai', model: 'gpt-4o-mini', input_tokens: 0, output_tokens: 0, reported_cost_usd: bad, table })).toEqual({
      cost_usd: null,
      cost_source: 'unknown',
      price_table_version: '2026-09-27',
    });
  });

  it('prices a local provider whose own entry has a price like any other', () => {
    const paidLocal: PricingTable = { version: 'v', currency: 'USD', entries: [{ match: '*', provider: 'ollama', input_per_mtok: 0.5, output_per_mtok: 0 }] };
    expect(priceCall({ provider: 'ollama', model: 'm', input_tokens: 2_000_000, output_tokens: 0, table: paidLocal })).toEqual({
      cost_usd: 1,
      cost_source: 'computed',
      price_table_version: 'v',
    });
  });

  it('refuses a fractional token count only when it prices the call from the table', () => {
    expect(() => priceCall({ ...haiku, input_tokens: 1.5 })).toThrow(RangeError);
    expect(priceCall({ ...haiku, input_tokens: 1.5, reported_cost_usd: 0.001 }).cost_source).toBe('reported');
    expect(priceCall({ ...haiku, provider: 'openai', model: 'gpt-4o-mini', output_tokens: 0.5 }).cost_source).toBe('unknown');
  });

  it('marshals an unknown cost as null, so it is never summed as $0', () => {
    expect(JSON.stringify(priceCall({ provider: 'openai', model: 'x', input_tokens: 1, output_tokens: 1, table }))).toContain('"cost_usd":null');
  });
});

describe('effectivePrices and lookupEffective', () => {
  const base: PricingTable = { version: '2026-09-27', currency: 'USD', entries: [{ match: 'jev-*', provider: 'typesafe', input_per_mtok: 0.042, output_per_mtok: 0, label: 'vendor_price' }] };
  const file: PricingTable = { version: 'acme-1', currency: 'USD', entries: [{ match: 'jev-*', provider: 'typesafe', input_per_mtok: 0.04, output_per_mtok: 0 }] };
  const overrides: PriceEntry[] = [{ match: 'jev-1.13*', provider: 'typesafe', input_per_mtok: 0.03, output_per_mtok: 0, note: 'negotiated' }];

  it('layers overrides, then the file, then contracts, without modifying them', () => {
    const got = effectivePrices(base, file, overrides);
    expect(got.version).toBe('acme-1');
    expect(got.entries).toEqual([{ ...overrides[0], label: 'override' }, file.entries[0], base.entries[0]]);
    expect(overrides[0].label).toBeUndefined();
    expect(lookupPrice(got.entries, 'typesafe', 'jev-1.13.0')?.input_per_mtok).toBe(0.03);
    expect(lookupPrice(got.entries, 'typesafe', 'jev-2.0')?.input_per_mtok).toBe(0.04);
    expect(effectivePrices(base, null, [])).toEqual({ version: '2026-09-27', currency: 'USD', entries: base.entries });
  });

  it('resolves the provider match first, then overrides, and reports the version that priced it', () => {
    const acme: PricingTable = { version: 'acme-2026-10', currency: 'USD', entries: [{ match: 'typesafe/jev-1.13*', provider: 'openrouter', input_per_mtok: 0.04, output_per_mtok: 0 }] };
    const input = { contracts, file: acme, overrides: [{ match: '*jev*', provider: 'openrouter', input_per_mtok: 0.05, output_per_mtok: 0 }], provider: 'typesafe', model: 'jev-1.13' };
    const own = lookupEffective(input);
    expect(own.entry?.provider).toBe('typesafe');
    expect(own.price_table_version).toBe('acme-2026-10');
    expect(lookupEffective({ ...input, provider: 'openrouter', model: 'typesafe/jev-1.13' })).toEqual({
      entry: { match: '*jev*', provider: 'openrouter', input_per_mtok: 0.05, output_per_mtok: 0, label: 'override' },
      price_table_version: 'override',
    });
    expect(lookupEffective({ ...input, provider: 'openai', model: 'gpt-5' })).toEqual({ entry: null, price_table_version: 'acme-2026-10' });
  });

  it('returns the entry with only its non-empty optional members, as Go marshals it', () => {
    const t: PricingTable = { version: 'v', currency: 'USD', entries: [{ match: 'm', provider: 'p', input_per_mtok: 1, output_per_mtok: 2, source: '', note: 'n' }] };
    expect(lookupEffective({ contracts: t, file: null, overrides: [], provider: 'p', model: 'm' }).entry).toEqual({
      match: 'm',
      provider: 'p',
      input_per_mtok: 1,
      output_per_mtok: 2,
      note: 'n',
    });
  });
});

describe('checkPriceTable and checkPriceEntry', () => {
  const ok: PriceEntry = { match: '*', provider: 'ollama', input_per_mtok: 0, output_per_mtok: 0 };

  it('accepts the contracts table', () => {
    expect(checkPriceTable(contracts)).toBeNull();
  });

  it.each([
    [{ version: '' }, 'version must be 1-32 characters'],
    [{ version: 'x'.repeat(33) }, 'version must be 1-32 characters'],
    [{ currency: 'EUR' }, 'currency must be USD'],
    [{ entries: [] }, 'entries must not be empty'],
    [{ entries: [ok, { ...ok, provider: 'Ollama' }] }, 'entries[1].provider must be 1-32 lowercase letters'],
  ])('refuses %j', (patch, problem) => {
    expect(checkPriceTable({ version: 'v', currency: 'USD', entries: [ok], ...patch } as PricingTable)).toContain(problem);
  });

  it('counts characters as code points, as JSON Schema maxLength does', () => {
    expect(checkPriceTable({ version: '\u{1F600}'.repeat(32), currency: 'USD', entries: [ok] })).toBeNull();
  });

  it.each([
    [{ match: '' }, 'match'],
    [{ match: 'm'.repeat(129) }, 'match'],
    [{ provider: '' }, 'provider'],
    [{ input_per_mtok: -1 }, 'input_per_mtok'],
    [{ output_per_mtok: Number.NaN }, 'output_per_mtok'],
    [{ source: 's'.repeat(301) }, 'source'],
    [{ label: 'list_price' }, 'label'],
    [{ as_of: '2026-02-29' }, 'as_of'],
    [{ as_of: '2026-13-01' }, 'as_of'],
    [{ as_of: '27.09.2026' }, 'as_of'],
    [{ note: 'n'.repeat(301) }, 'note'],
  ])('flags %j at %s', (patch, field) => {
    expect(checkPriceEntry({ ...ok, ...patch } as PriceEntry)?.field).toBe(field);
  });

  it.each(['2024-02-29', '2000-02-29', '2026-09-27', '0000-01-01'])('accepts the date %s', (asOf) => {
    expect(checkPriceEntry({ ...ok, as_of: asOf, label: 'vendor_price' })).toBeNull();
  });

  it('refuses 1900-02-29 (not a leap year)', () => {
    expect(checkPriceEntry({ ...ok, as_of: '1900-02-29' })?.field).toBe('as_of');
  });
});

/**
 * The shipped assumptions as calculator settings (they share their names). An input the assumptions
 * leave out is 0, as in Go's JSON round trip: fast_check_output_tokens (Jev's output is free).
 */
function assumedSettings(): Per1kSettings {
  const inputs = assumptions.inputs as Partial<Record<string, { value: number }>>;
  return Object.fromEntries(PER_1K_SETTING_KEYS.map((k) => [k, inputs[k]?.value ?? 0])) as unknown as Per1kSettings;
}

/** A model that the aliased assumptions price entry's glob matches. */
function assumedModel(alias: 'decision_model' | 'judge'): ModelRef {
  const ref = assumptions.prices[alias];
  return { provider: ref.provider, model: ref.match.replaceAll('*', '') };
}

interface Figure {
  id: string;
  per_1k_usd?: number;
  tolerance?: number;
}

describe('costPer1k', () => {
  const defaults = (): Per1kInput => ({ table: contracts, decision_model: assumedModel('decision_model'), judge: assumedModel('judge'), settings: assumedSettings() });

  it('reproduces every cost figure of the published assumptions', () => {
    const got = costPer1k(defaults());
    const judge = assumedModel('judge');
    const inputs = assumptions.inputs as Record<string, { value: number }>;
    const sameChecks = priceCall({ ...judge, input_tokens: inputs.same_checks_input_tokens.value, output_tokens: inputs.same_checks_output_tokens.value, table: contracts });
    const figures: Record<string, number | null> = {
      fast_check_per_1k: got.fast_check_per_1k,
      llm_full_audit_per_1k: got.deep_audit_usd === null ? null : got.deep_audit_usd * 1000,
      default_total_per_1k: got.total_per_1k,
      llm_same_checks_per_1k: sameChecks.cost_usd === null ? null : sameChecks.cost_usd * 1000,
    };
    const stated = (assumptions.figures as Figure[]).filter((f) => f.per_1k_usd !== undefined && f.id in figures);
    expect(stated.map((f) => f.id).sort()).toEqual(Object.keys(figures).sort());
    for (const f of stated) expect(Math.abs((figures[f.id] ?? Number.NaN) - (f.per_1k_usd ?? 0)), f.id).toBeLessThanOrEqual(f.tolerance ?? 0.005);
  });

  it('is exact at the shipped defaults: $0.21 + 0.079 × $35.5 = $3.0145', () => {
    expect(costPer1k(defaults())).toEqual({
      audit_rate: 0.079,
      fast_check_usd: 0.00021,
      fast_check_per_1k: 0.21,
      deep_audit_usd: 0.0355,
      deep_audit_per_1k: 2.8045,
      total_per_1k: 3.0145,
      decision_cost_source: 'computed',
      judge_cost_source: 'computed',
    });
  });

  it('labels a local judge free, an unpriced judge unknown and a mock decision model simulated', () => {
    const local = costPer1k({ ...defaults(), judge: { provider: 'ollama', model: 'llama3.1:8b' } });
    expect([local.judge_cost_source, local.deep_audit_per_1k, local.total_per_1k]).toEqual(['free_local', 0, 0.21]);

    const unknown = costPer1k({ ...defaults(), judge: { provider: 'openai', model: 'gpt-4o-mini' } });
    expect([unknown.judge_cost_source, unknown.deep_audit_usd, unknown.deep_audit_per_1k, unknown.total_per_1k, unknown.fast_check_per_1k]).toEqual([
      'unknown',
      null,
      null,
      null,
      0.21,
    ]);

    const mock = costPer1k({ ...defaults(), decision_model: { provider: 'mock', model: 'mock/jev' } });
    expect([mock.decision_cost_source, mock.fast_check_usd]).toEqual(['simulated', 0]);
  });

  it('audits every answer at most once when the rates add up past 1', () => {
    const s = { ...assumedSettings(), grounding_flag_rate: 0.9, pass_sample_rate: 0.9, uniform_sample_rate: 0.9 };
    expect(costPer1k({ ...defaults(), settings: s }).audit_rate).toBe(1);
  });

  it('changes only the deep-audit part when the judge changes', () => {
    const haiku = costPer1k(defaults());
    const sonnetTable: PricingTable = { ...contracts, entries: [...contracts.entries, { match: 'claude-sonnet-4-5*', provider: 'anthropic', input_per_mtok: 3, output_per_mtok: 15 }] };
    const sonnet = costPer1k({ ...defaults(), table: sonnetTable, judge: { provider: 'anthropic', model: 'claude-sonnet-4-5' } });
    expect([sonnet.fast_check_per_1k, sonnet.audit_rate]).toEqual([haiku.fast_check_per_1k, haiku.audit_rate]);
    expect(sonnet.deep_audit_usd).toBe(0.1065);
    expect([sonnet.deep_audit_per_1k, sonnet.total_per_1k]).toEqual([8.4135, 8.6235]);
  });

  it.each([
    [{ pass_sample_rate: 1.5 }, 'pass_sample_rate must be between 0 and 1'],
    [{ grounding_flag_rate: -0.1 }, 'grounding_flag_rate must be between 0 and 1'],
    [{ claims_per_answer: -1 }, 'claims_per_answer must be a number of at least 0'],
    [{ fast_check_input_tokens: Number.POSITIVE_INFINITY }, 'fast_check_input_tokens must be a number of at least 0'],
    [{ verify_output_tokens: Number.NaN }, 'verify_output_tokens must be a number of at least 0'],
  ])('refuses the settings %j', (patch, problem) => {
    const settings = { ...assumedSettings(), ...patch };
    expect(checkPer1kSettings(settings)).toContain(problem);
    expect(() => costPer1k({ ...defaults(), settings })).toThrow(RangeError);
  });
});

describe('the cost vector runner', () => {
  const table = '{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"ollama","input_per_mtok":0,"output_per_mtok":0}]}';

  it.each([
    ['unknown fn', '{"fn":"savings"}', 'fn "savings"'],
    ['missing fn', '{"provider":"x"}', 'fn ""'],
    ['not an object', '[]', 'want an object'],
    ['unknown field', `{"fn":"call_cost","provider":"x","model":"m","input_tokens":1,"output_tokens":0,"price":1,"table":${table}}`, 'unknown field'],
    ['fractional tokens', `{"fn":"call_cost","provider":"x","model":"m","input_tokens":1.5,"output_tokens":0,"table":${table}}`, 'whole number'],
    ['call table', '{"fn":"call_cost","provider":"x","model":"m","input_tokens":1,"output_tokens":0,"table":{"version":"v1","currency":"EUR","entries":[]}}', 'table:'],
    ['entry member', '{"fn":"call_cost","provider":"x","model":"m","table":{"version":"v1","currency":"USD","entries":[{"match":"*","provider":"x","input_per_mtok":0,"output_per_mtok":0,"cached":1}]}}', 'unknown field'],
    ['lookup contracts', '{"fn":"price_lookup","provider":"x","model":"m","overrides":[],"contracts":{"version":"","currency":"USD","entries":[]}}', 'contracts:'],
    ['lookup file', `{"fn":"price_lookup","provider":"x","model":"m","overrides":[],"file":{"version":"f","currency":"USD","entries":[]},"contracts":${table}}`, 'file:'],
    ['lookup override', `{"fn":"price_lookup","provider":"x","model":"m","overrides":[{"match":"","provider":"x","input_per_mtok":1,"output_per_mtok":0}],"contracts":${table}}`, 'overrides[0].match'],
    ['per_1k bad rate', `{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"ollama","model":"m"},"settings":{"pass_sample_rate":2},"table":${table}}`, 'pass_sample_rate'],
    ['per_1k bad table', '{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"ollama","model":"m"},"settings":{},"table":{"version":"v","currency":"USD","entries":[]}}', 'table:'],
    ['per_1k bad member', `{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m","price":1},"judge":{"provider":"ollama","model":"m"},"settings":{},"table":${table}}`, 'unknown field'],
    ['per_1k string setting', `{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"ollama","model":"m"},"settings":{"claims_per_answer":"8"},"table":${table}}`, 'want a number'],
  ])('refuses %s', (_name, input, problem) => {
    expect(() => runVectorKind('cost', JSON.parse(input))).toThrow(problem);
  });

  it('marshals an unknown cost as a present null and zero-fills absent settings like Go', () => {
    const call = `{"fn":"call_cost","provider":"openai","model":"gpt-5","input_tokens":10,"output_tokens":0,"table":${table}}`;
    expect(runVectorKind('cost', JSON.parse(call))).toEqual({ cost_usd: null, cost_source: 'unknown', price_table_version: 'v1' });
    const per1k = `{"fn":"per_1k","decision_model":{"provider":"ollama","model":"m"},"judge":{"provider":"mock","model":"j"},"settings":null,"table":${table}}`;
    expect(runVectorKind('cost', JSON.parse(per1k))).toMatchObject({ audit_rate: 0, total_per_1k: 0, decision_cost_source: 'free_local', judge_cost_source: 'simulated' });
  });
});
