import { describe, expect, it } from 'vitest';
import { ApiError } from '@/data/errors';
import { credentialList, settingsView } from '@/test/control/securityHandlers';
import { cardInfo, CARDS, confirmationRoutes, credentialState, defaultBindTargets, envVarOf, otherCredentials, simulatedOn } from './credentialModel';

describe('credential model', () => {
  it('has the five cards of SPEC §3.3 with the D29 env vars', () => {
    expect(CARDS.map((c) => c.title)).toEqual(['OpenRouter', 'TypeSafe', 'Anthropic', 'OpenAI', 'Other gateway']);
    expect(CARDS.map((c) => envVarOf(c.name))).toEqual(['OPENROUTER_API_KEY', 'TYPESAFE_API_KEY', 'ANTHROPIC_API_KEY', 'OPENAI_API_KEY', 'TRUTHTABLE_JEV_API_KEY']);
    expect(envVarOf('upstream-fallback')).toBe('TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY');
    expect(envVarOf('nope')).toBeNull();
  });

  it('tells saved, gateway-env, engine-env (D80), unreadable and missing keys apart', () => {
    const items = credentialList().items;
    expect(credentialState(cardInfo('openrouter'), items).kind).toBe('saved');
    expect(credentialState(cardInfo('typesafe'), items).kind).toBe('env');
    expect(credentialState(cardInfo('anthropic'), items).kind).toBe('engine_env');
    expect(credentialState(cardInfo('openai'), items).kind).toBe('none');
    expect(credentialState(cardInfo('compatible'), items).kind).toBe('undecryptable');
    expect(otherCredentials(items).map((c) => c.name)).toEqual(['upstream-default']);
  });

  it('binds by default only to checks that are free, already this key, and not env-managed (D75)', () => {
    const bound = settingsView();
    expect(defaultBindTargets(cardInfo('openrouter'), bound)).toEqual(['decision_model', 'judge']);
    expect(defaultBindTargets(cardInfo('anthropic'), bound)).toEqual([]);
    const free = settingsView();
    free.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
    expect(defaultBindTargets(cardInfo('anthropic'), free)).toEqual(['judge']);
    const env = settingsView({ env_managed: ['bindings.judge'] });
    expect(defaultBindTargets(cardInfo('openrouter'), env)).toEqual(['decision_model']);
    expect(defaultBindTargets(cardInfo('typesafe'), undefined)).toEqual(['decision_model']);
  });

  it('reads simulated mode and D77 routes', () => {
    const s = settingsView();
    expect(simulatedOn(s)).toBe(false);
    s.runtime.judge.provider = 'mock';
    expect(simulatedOn(s)).toBe(true);
    const err = new ApiError({ kind: 'http', status: 409, code: 'confirmation_required', message: 'x', details: { routes: ['payments-faq'] } });
    expect(confirmationRoutes(err)).toEqual(['payments-faq']);
    expect(confirmationRoutes(new ApiError({ kind: 'http', status: 409, code: 'conflict', message: 'x' }))).toBeNull();
  });
});
