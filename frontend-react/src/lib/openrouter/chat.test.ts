// @vitest-environment node
import { afterEach, describe, expect, it } from 'vitest';
import chat200 from '@contracts/fixtures/vendor/openrouter/chat.200_usage_cost.json';
import { GateRefusedError } from '@/lib/gate/runner';
import { chatComplete, chatTokenEstimate, decodeChatResponse, encodeChatRequest } from './chat';
import { CHAT_URL } from './endpoints';
import { createKeyStore, type KeyStore } from './keyStore';
import { memoryStorage } from './testing';
import type { ChatCall } from './types';
import { createUsageStore, type UsageStore } from './usage';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';

const CALL: ChatCall = { model: 'anthropic/claude-haiku-4.5', messages: [{ role: 'user', content: 'Reply with the word OK.' }], max_tokens: 400 };

function fakeFetch(answer: () => Response | Promise<Response>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return answer();
  }) as typeof globalThis.fetch;
  return { fetch, calls };
}
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

const keyStores: KeyStore[] = [];
const usageStores: UsageStore[] = [];
function setup(opts: { maxCalls?: number; maxTokens?: number; price?: (m: string) => number | null } = {}) {
  const store = createKeyStore({ storage: memoryStorage(), target: null });
  store.set(KEY);
  const usage = createUsageStore({ maxCalls: opts.maxCalls ?? 100, maxTokens: opts.maxTokens ?? 8000, storage: memoryStorage(), target: null, price: opts.price ?? (() => null) });
  keyStores.push(store);
  usageStores.push(usage);
  return { store, usage };
}
afterEach(() => {
  for (const s of keyStores.splice(0)) s.dispose();
  for (const s of usageStores.splice(0)) s.dispose();
});

describe('chat: the request', () => {
  it('sends {model, messages, temperature, max_tokens, usage:{include:true}} and nothing else', () => {
    expect(JSON.parse(encodeChatRequest({ ...CALL, temperature: 0.2 }))).toEqual({ ...CALL, temperature: 0.2, usage: { include: true } });
    expect(JSON.parse(encodeChatRequest(CALL))).not.toHaveProperty('temperature');
  });

  it('reserves the prompt estimate plus the longest answer', () => {
    expect(chatTokenEstimate(CALL)).toBeGreaterThan(400);
    expect(chatTokenEstimate({ ...CALL, max_tokens: 1000 }) - chatTokenEstimate(CALL)).toBe(600);
  });
});

describe('chat: the call', () => {
  it('answers from the fixture and counts one call at the vendor cost', async () => {
    const { store, usage } = setup();
    const f = fakeFetch(() => json(200, chat200.body));
    const r = await chatComplete(CALL, { store, ledger: usage.forModel(CALL.model), http: { fetch: f.fetch } });
    expect(r).toMatchObject({ text: 'This is a mock answer from mock-upstream.', model: 'anthropic/claude-haiku-4.5', finish_reason: 'stop', usage: { input_tokens: 33, output_tokens: 11, cost: 0.000088 } });
    expect(f.calls[0].url).toBe(CHAT_URL);
    expect(f.calls[0].init.method).toBe('POST');
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, cost_usd: 0.000088, cost_source: 'vendor', inflight: 0 });
  });

  it('prices a call without usage at its estimate', async () => {
    const { store, usage } = setup({ price: () => 0.001 });
    const f = fakeFetch(() => json(200, { model: 'anthropic/claude-haiku-4.5', choices: [{ message: { content: 'OK' }, finish_reason: 'stop' }] }));
    const r = await chatComplete(CALL, { store, ledger: usage.forModel(CALL.model), http: { fetch: f.fetch } });
    expect(r.usage).toMatchObject({ output_tokens: 400, cost: null });
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, cost_usd: 0.001, cost_source: 'estimate' });
  });

  it('refuses over the token cap before any fetch', async () => {
    const { store, usage } = setup({ maxTokens: 500 });
    const f = fakeFetch(() => json(200, chat200.body));
    const err = await chatComplete({ ...CALL, max_tokens: 500 }, { store, ledger: usage, http: { fetch: f.fetch } }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(GateRefusedError);
    expect((err as GateRefusedError).reason).toBe('token_cap');
    expect(f.calls).toHaveLength(0);
  });

  it('refuses once the cap is reached, before any fetch', async () => {
    const { store, usage } = setup({ maxCalls: 1 });
    const f = fakeFetch(() => json(200, chat200.body));
    await chatComplete(CALL, { store, ledger: usage, http: { fetch: f.fetch } });
    await expect(chatComplete(CALL, { store, ledger: usage, http: { fetch: f.fetch } })).rejects.toMatchObject({ reason: 'cap_reached' });
    expect(f.calls).toHaveLength(1);
  });

  it('does not count a 402, counts a 401 as rejected and forgets the key', async () => {
    const { store, usage } = setup();
    await expect(chatComplete(CALL, { store, ledger: usage, http: { fetch: fakeFetch(() => json(402, { error: { message: 'Insufficient credits' } })).fetch } })).rejects.toMatchObject({ code: 'credits' });
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, rejected: 0, inflight: 0 });
    expect(store.getSnapshot().key).toBe(KEY);
    await expect(chatComplete(CALL, { store, ledger: usage, http: { fetch: fakeFetch(() => json(401, { error: { message: 'User not found.' } })).fetch } })).rejects.toMatchObject({ code: 'auth' });
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, rejected: 1, inflight: 0 });
    expect(store.getSnapshot().key).toBeNull();
  });

  it('counts a 200 it could not read', async () => {
    const { store, usage } = setup();
    const f = fakeFetch(() => json(200, { error: { message: 'Provider returned error' } }));
    const err = await chatComplete(CALL, { store, ledger: usage, http: { fetch: f.fetch } }).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'bad_response', status: 200 });
    expect((err as Error).message).toMatch(/no message: Provider returned error/);
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, inflight: 0 });
  });

  it('a network failure counts nothing', async () => {
    const { store, usage } = setup();
    const f = fakeFetch(() => Promise.reject(new TypeError('Failed to fetch')));
    await expect(chatComplete(CALL, { store, ledger: usage, http: { fetch: f.fetch } })).rejects.toMatchObject({ code: 'network', status: 0 });
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, rejected: 0, inflight: 0 });
  });

  it('sends nothing without a key', async () => {
    const { usage } = setup();
    const empty = createKeyStore({ storage: memoryStorage(), target: null });
    keyStores.push(empty);
    const f = fakeFetch(() => json(200, chat200.body));
    await expect(chatComplete(CALL, { store: empty, ledger: usage, http: { fetch: f.fetch } })).rejects.toMatchObject({ code: 'auth', status: 0 });
    expect(f.calls).toHaveLength(0);
    expect(usage.getSnapshot().inflight).toBe(0);
  });

  it('decodes a null content as empty text', () => {
    expect(decodeChatResponse(JSON.stringify({ choices: [{ message: { content: null }, finish_reason: 'content_filter' }] }), 200, 'm')).toMatchObject({ text: '', model: 'm', finish_reason: 'content_filter' });
  });
});
