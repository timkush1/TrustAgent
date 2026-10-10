// @vitest-environment node
/**
 * The static Studio over the real live transport (D107, D108): the tab's key store, its
 * ledger and the OpenRouter clients of lib/openrouter, with only `fetch` faked. A draft and a test
 * reach openrouter.ai with the tab's key, count on the same "{n} of {max} calls" counter as the live
 * Playground (one turn is one call, never two), and a key OpenRouter refuses is forgotten.
 */
import { afterEach, describe, expect, it } from 'vitest';
import prompt from '@contracts/prompts/evaluator-drafter.v1.json';
import chat200 from '@contracts/fixtures/vendor/openrouter/chat.200_usage_cost.json';
import decisions200 from '@contracts/fixtures/vendor/openrouter/decisions.200.json';
import decisions401 from '@contracts/fixtures/vendor/openrouter/decisions.401.json';
import { isApiError } from '@/data/errors';
import { CHAT_URL, DECISIONS_URL } from '@/lib/openrouter/endpoints';
import { createKeyStore, type KeyStore } from '@/lib/openrouter/keyStore';
import { memoryStorage } from '@/lib/openrouter/testing';
import { createUsageStore, type UsageStore } from '@/lib/openrouter/usage';
import { committedDetail, committedManifest } from '@/test/recording';
import type { EvaluatorSpec, StudioSample } from '@/types/api';
import { studioLive, type StudioLiveOptions } from './live';
import { createStaticStudioPlane } from './staticPlane';
import { staticRegistry } from './staticRegistry';
import { blankSpec } from './templates';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

interface Fixture {
  status: number;
  headers: Record<string, string>;
  body: unknown;
}
const serve = (f: Fixture, body: unknown = f.body): Response => new Response(JSON.stringify(body), { status: f.status, headers: f.headers });

interface Sent {
  url: string;
  headers: Record<string, string>;
  body: Record<string, unknown>;
}

function fakeFetch(answer: (sent: Sent, n: number) => Response) {
  const sent: Sent[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const entry: Sent = { url: String(input), headers: { ...(init?.headers as Record<string, string>) }, body: JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown> };
    sent.push(entry);
    return answer(entry, sent.length - 1);
  }) as typeof globalThis.fetch;
  return { fetch, sent };
}

const disposers: (() => void)[] = [];
afterEach(() => {
  for (const d of disposers.splice(0)) d();
});

function tab(key: string | null = KEY): { store: KeyStore; usage: UsageStore } {
  const store = createKeyStore({ storage: memoryStorage(), target: null });
  const usage = createUsageStore({ storage: memoryStorage(), target: null, maxCalls: 100, maxTokens: 8000 });
  if (key !== null) store.set(key);
  disposers.push(store.dispose, usage.dispose);
  return { store, usage };
}

const manifest = committedManifest();
const registry = staticRegistry({ started: new Date('2026-10-01T00:00:00Z') });

function plane(opts: StudioLiveOptions) {
  return createStaticStudioPlane(
    { message: 'Run it yourself with make demo.', traceIds: async () => manifest.traces.map((t) => t.trace_id), trace: async (id) => committedDetail(id) },
    { live: studioLive({ checks: true, drafting: true, errorsReadable: true, ...opts }), registry },
  );
}

/** A chat completion whose message is `content` (the fixture's usage and cost). */
const chatReply = (content: string) => {
  const body = structuredClone(chat200.body) as { choices: { message: { content: string } }[] };
  body.choices[0].message.content = content;
  return serve(chat200 as Fixture, body);
};

/** A decisions answer for every question asked, at `noul` (the fixture's usage and cost). */
const decisionsReply = (sent: Sent, noul: number) => {
  const asked = (sent.body.questions ?? {}) as Record<string, { type: string }>;
  const body = { ...(decisions200.body as Record<string, unknown>), answers: Object.fromEntries(Object.keys(asked).map((q) => [q, { type: 'noul', noul }])) };
  return serve(decisions200 as Fixture, body);
};

const EXAMPLE = JSON.stringify(prompt.examples[0].output);

describe('studioLive: the tab’s key decides whether a client exists', () => {
  it('gives no client without a key or when the build turns the feature off, and both once a key is set', () => {
    const { store, usage } = tab(null);
    const live = studioLive({ store, usage, checks: true, drafting: true });
    expect(live.jev()).toBeNull();
    expect(live.chat()).toBeNull();
    store.set(KEY);
    expect(live.jev()).not.toBeNull();
    expect(live.chat()).not.toBeNull();
    const off = studioLive({ store, usage, checks: false, drafting: false });
    expect(off.jev()).toBeNull();
    expect(off.chat()).toBeNull();
  });
});

describe('Draft with AI through the real chat client', () => {
  it('asks the judge model with the tab’s key and counts one call for one turn', async () => {
    const { store, usage } = tab();
    const { fetch, sent } = fakeFetch(() => chatReply(EXAMPLE));
    const out = await plane({ store, usage, http: { fetch, referer: 'https://example.test/TrustAgent/' } }).draftEvaluator({ description: 'Flag answers that promise a refund.' });
    expect(out.draft).not.toBeNull();
    expect(out.attempts).toBe(1);
    expect(sent).toHaveLength(1);
    expect(sent[0].url).toBe(CHAT_URL);
    expect(sent[0].headers.Authorization ?? sent[0].headers.authorization).toBe(`Bearer ${KEY}`);
    expect(sent[0].body.model).toBe(studioLive().judgeModel);
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, inflight: 0, cost_source: 'vendor' });
    expect(out.cost_source).toBe('reported');
    expect(JSON.stringify(out)).not.toContain(KEY);
  });

  it('counts two calls when the first answer needs the repair turn', async () => {
    const { store, usage } = tab();
    const { fetch, sent } = fakeFetch((_s, n) => chatReply(n === 0 ? '{"id": "x"}' : EXAMPLE));
    const out = await plane({ store, usage, http: { fetch } }).draftEvaluator({ description: 'Flag answers that promise a refund.' });
    expect(out.attempts).toBe(2);
    expect(sent).toHaveLength(2);
    expect(usage.getSnapshot().calls).toBe(2);
  });

  it('forgets a key OpenRouter refuses, counts no billable call, and words it as the Playground does', async () => {
    const { store, usage } = tab();
    const { fetch } = fakeFetch(() => serve(decisions401 as Fixture));
    const err = await plane({ store, usage, http: { fetch } })
      .draftEvaluator({ description: 'Flag answers that promise a refund.' })
      .catch((e: unknown) => e);
    expect(isApiError(err) && err.code).toBe('auth');
    expect(store.currentKey()).toBeNull();
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, rejected: 1, inflight: 0 });
    expect(String((err as Error).message)).not.toContain(KEY);
  });
});

describe('the test bench through runGate and the real Jev client', () => {
  const spec: EvaluatorSpec = { ...blankSpec([]), id: 'mentions_bad', name: 'Mentions bad' };
  const samples: StudioSample[] = [
    { id: 's1', assistant_response: 'This is BAD.', expected: 'fire' },
    { id: 's2', assistant_response: `All fine here (${NO_PATTERN}).`, expected: 'no_fire' },
  ];

  it('asks Jev once per sample with the tab’s key and counts each call on the tab’s ledger', async () => {
    const { store, usage } = tab();
    const { fetch, sent } = fakeFetch((s) => decisionsReply(s, String((s.body.state as Record<string, unknown>).assistant_response).includes('BAD') ? 0.95 : 0.05));
    const out = await plane({ store, usage, http: { fetch } }).testEvaluator({ spec, samples });
    expect(sent.map((s) => s.url)).toEqual([DECISIONS_URL, DECISIONS_URL]);
    expect(sent.every((s) => (s.headers.Authorization ?? s.headers.authorization) === `Bearer ${KEY}`)).toBe(true);
    expect(out.results.map((r) => [r.sample_id, r.status, r.fires, r.agrees])).toEqual([
      ['s1', 'assessed', true, true],
      ['s2', 'assessed', false, true],
    ]);
    expect(out.summary.snapshot).toBe('typesafe/jev-1.13-20260917');
    expect(usage.getSnapshot()).toMatchObject({ calls: 2, inflight: 0, cost_source: 'vendor' });
    expect(JSON.stringify(out)).not.toContain(KEY);
  });

  it('ends the whole test when OpenRouter refuses the key, and forgets it', async () => {
    const { store, usage } = tab();
    const { fetch } = fakeFetch(() => serve(decisions401 as Fixture));
    const err = await plane({ store, usage, http: { fetch } })
      .testEvaluator({ spec, samples })
      .catch((e: unknown) => e);
    expect(isApiError(err) && err.code).toBe('auth');
    expect(store.currentKey()).toBeNull();
    expect(usage.getSnapshot()).toMatchObject({ calls: 0, inflight: 0 });
  });

  it('refuses before any call once the tab’s calls are used up', async () => {
    const { store } = tab();
    const usage = createUsageStore({ storage: memoryStorage(), target: null, maxCalls: 1, maxTokens: 8000 });
    disposers.push(usage.dispose);
    const { fetch, sent } = fakeFetch((s) => decisionsReply(s, 0.5));
    await plane({ store, usage, http: { fetch } }).testEvaluator({ spec, samples: samples.slice(0, 1) });
    const err = await plane({ store, usage, http: { fetch } })
      .testEvaluator({ spec, samples: samples.slice(0, 1) })
      .catch((e: unknown) => e);
    expect(isApiError(err) && err.code).toBe('cap_reached');
    expect(sent).toHaveLength(1);
  });
});
