import { describe, expect, it } from 'vitest';
import { settingsView } from '@/test/control/securityHandlers';
import { systemStatus } from '@/test/status';
import { costOverrideFinding } from '@/domain/presentation';
import {
  budgetEnvManaged,
  budgetsPatch,
  enforcingReserve,
  isEnvManaged,
  overrideFindings,
  overridesFromRows,
  parseCap,
  rebaseCaps,
  rebaseOverrides,
  rowProblems,
  rowsFromOverrides,
  sameOverrides,
  spendToday,
} from './budgetsModel';

describe('budgets model', () => {
  it('reads dollars and cents, 0 meaning no cap', () => {
    expect(parseCap('2')).toEqual({ value: 2 });
    expect(parseCap(' 2.50 ')).toEqual({ value: 2.5 });
    expect(parseCap('0')).toEqual({ value: 0 });
    expect(parseCap('')).toHaveProperty('error');
    expect(parseCap('-1')).toHaveProperty('error');
    expect(parseCap('1e3')).toHaveProperty('error');
    expect(parseCap('2.555')).toHaveProperty('error');
    expect(parseCap('100001')).toHaveProperty('error');
  });

  it('patches only the caps that changed', () => {
    const s = settingsView();
    expect(budgetsPatch(s, { decision: 2, llm: 5 })).toBeNull();
    expect(budgetsPatch(s, { decision: 3, llm: 5 })).toEqual({ budgets: { decision_usd_day: 3 } });
    expect(budgetsPatch(s, { llm: 0 })).toEqual({ budgets: { llm_usd_day: 0 } });
  });

  it('knows an env-managed cap, also under an env-managed parent', () => {
    expect(isEnvManaged(['budgets'], 'budgets.llm_usd_day')).toBe(true);
    expect(isEnvManaged(['budgets.llm_usd_day'], 'budgets.decision_usd_day')).toBe(false);
    expect(isEnvManaged(['budgetsx'], 'budgets.llm_usd_day')).toBe(false);
    expect(budgetEnvManaged(settingsView({ env_managed: ['budgets.llm_usd_day'] }), 'llm')).toBe(true);
  });

  it('reads the enforcing reserve, falling back to the default', () => {
    const base = settingsView();
    const withReserve = (value: string) => ({
      groups: [{ name: 'Budgets', keys: [{ ...base.groups[0].keys[0], key: 'TRUTHTABLE_SPEND_ENFORCING_RESERVE', value }] }],
    });
    expect(enforcingReserve(base)).toBe(0.25);
    expect(enforcingReserve(withReserve('0.1'))).toBe(0.1);
    expect(enforcingReserve(withReserve('2'))).toBe(0.25);
    expect(enforcingReserve(withReserve('x'))).toBe(0.25);
    // An empty value is unset, never a reserve of 0: the key's own default applies.
    const unset = (fallback: string) => ({
      groups: [{ name: 'Budgets', keys: [{ ...base.groups[0].keys[0], key: 'TRUTHTABLE_SPEND_ENFORCING_RESERVE', value: '', default: fallback }] }],
    });
    expect(enforcingReserve(unset('0.3'))).toBe(0.3);
    expect(enforcingReserve(unset(''))).toBe(0.25);
  });

  it('carries cap drafts over to a newer version, and calls an edited cap changed elsewhere a conflict', () => {
    const base = { decision: 2, llm: 5 };
    // Nothing changed underneath: the drafts stay as typed.
    expect(rebaseCaps({ decision: '3', llm: '5' }, base, base)).toEqual({ decision: '3', llm: '5' });
    // The other cap changed elsewhere: it follows, the edit stays.
    expect(rebaseCaps({ decision: '3', llm: '5' }, base, { decision: 2, llm: 9 })).toEqual({ decision: '3', llm: '9' });
    // The edited cap changed elsewhere too: a conflict, never a silent overwrite.
    expect(rebaseCaps({ decision: '3', llm: '5' }, base, { decision: 4, llm: 5 })).toBeNull();
  });

  it('takes today’s spend from status, real spend only', () => {
    const status = systemStatus();
    expect(spendToday(null, 'decision')).toBeNull();
    expect(spendToday(status, 'llm')).toEqual({ spent: status.budgets.llm_spent_today_usd, exhausted: status.budgets.llm_exhausted, reserveOnly: false });
  });
});

describe('price overrides', () => {
  it('round-trips rows, sends them labelled as yours, and knows when nothing changed', () => {
    const saved = [{ provider: 'openai', match: 'gpt-4o-mini*', input_per_mtok: 0.12, output_per_mtok: 0.5, label: 'override' as const, note: 'negotiated' }];
    const rows = rowsFromOverrides(saved);
    expect(sameOverrides(rows, saved)).toBe(true);
    expect(overridesFromRows(rows)).toEqual(saved);
    expect(sameOverrides([{ ...rows[0], input: '0.2' }], saved)).toBe(false);
    expect(sameOverrides([], saved)).toBe(false);
  });

  it('carries override rows over to a newer list only when that loses no edit', () => {
    const mine = { provider: 'openai', match: 'gpt-4o-mini*', input_per_mtok: 0.12, output_per_mtok: 0.5, label: 'override' as const };
    const theirs = { ...mine, match: 'gpt-4o*', input_per_mtok: 2.5 };
    const rows = rowsFromOverrides([mine]);
    // The list did not change underneath: the rows stay, edited or not.
    const edited = [{ ...rows[0], input: '0.2' }];
    expect(rebaseOverrides(edited, [mine], [{ ...mine }])).toBe(edited);
    // Unedited rows follow the newer list.
    expect(overridesFromRows(rebaseOverrides(rows, [mine], [theirs]) ?? [])).toEqual([theirs]);
    // Edited rows over a changed list: a conflict.
    expect(rebaseOverrides(edited, [mine], [theirs])).toBeNull();
  });

  it('checks a row against the pricing.v1 rules', () => {
    expect(rowProblems({ id: 'a', provider: 'OpenAI', match: '', input: '-1', output: 'x', note: '' })).toEqual({
      provider: expect.any(String),
      match: expect.any(String),
      input_per_mtok: expect.any(String),
      output_per_mtok: expect.any(String),
    });
    expect(rowProblems({ id: 'b', provider: 'ollama', match: 'llama3*', input: '0', output: '0', note: '' })).toEqual({});
  });

  it('puts the server’s findings on their rows and words them by code', () => {
    const findings = overrideFindings([
      { path: 'pricing_overrides[1].match', code: 'duplicate_override', message: 'Override 1 already prices it.', details: { index: 0 } },
      { path: 'pricing_overrides[0]', code: 'invalid_price_entry', message: 'label must be override.' },
      { path: 'budgets.llm_usd_day', message: 'must be at least 0' },
    ]);
    expect(findings.map((f) => [f.index, f.field])).toEqual([
      [1, 'match'],
      [0, null],
    ]);
    expect(costOverrideFinding(findings[0].finding)).toBe('Price 1 already covers this model, so this one would never apply.');
    expect(costOverrideFinding(findings[1].finding)).toBe('Check this price: label must be override.');
    expect(costOverrideFinding({ code: 'price_far_above_contracts', message: 'raw', details: { ratio: 200, contracts_per_mtok: 2.5 } })).toBe(
      'This is 200× the built-in price of $2.5 per million tokens. Check the units: USD per million tokens.',
    );
    // Without the numbers its words need, the server's own message.
    expect(costOverrideFinding({ code: 'price_far_above_contracts', message: 'raw' })).toBe('raw');
    expect(costOverrideFinding({ code: 'price_zero_not_local', message: 'raw' }, 'openai')).toBe('$0 is only allowed for local models. openai bills its calls.');
  });
});
