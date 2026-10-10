// @vitest-environment node
import { afterEach, describe, expect, it } from 'vitest';
import goldenText from '../../../../backend-go/internal/jev/testdata/request_openrouter-decisions.golden.json?raw';
import scenarioPack from '@contracts/demo/scenarios.v1.json';
import decisions200 from '@contracts/fixtures/vendor/openrouter/decisions.200.json';
import decisions401 from '@contracts/fixtures/vendor/openrouter/decisions.401.json';
import decisions402 from '@contracts/fixtures/vendor/openrouter/decisions.402_credits.json';
import decisionsHtml from '@contracts/fixtures/vendor/openrouter/decisions.200_html.json';
import type { CompiledRequest } from '@/lib/decision/compile';
import type { RawAnswerInput } from '@/lib/decision/normalize';
import { builtinSpecs, liveRoute } from '@/lib/gate/livePolicy';
import { playgroundResult } from '@/lib/gate/playgroundResult';
import { runGate } from '@/lib/gate/runner';
import { checkAnswers, decodeDecisionResponse, encodeDecisionRequest, openRouterJevCall } from './decisions';
import { DECISIONS_URL } from './endpoints';
import { liveErrorView } from './errors';
import { createKeyStore, type KeyStore } from './keyStore';
import { memoryStorage } from './testing';
import type { JevCallRequest } from './types';
import { createUsageStore } from './usage';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

interface Fixture {
  status: number;
  headers: Record<string, string>;
  body: unknown;
}

const serve = (f: Fixture): Response => new Response(typeof f.body === 'string' ? f.body : JSON.stringify(f.body), { status: f.status, headers: f.headers });

function fakeFetch(answer: (url: string, init: RequestInit) => Response | Promise<Response>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return answer(String(input), init ?? {});
  }) as typeof globalThis.fetch;
  return { fetch, calls };
}

const stores: KeyStore[] = [];
function keyStore(key: string | null = KEY): KeyStore {
  const s = createKeyStore({ storage: memoryStorage(), target: null });
  if (key) s.set(key);
  stores.push(s);
  return s;
}
afterEach(() => {
  for (const s of stores.splice(0)) s.dispose();
});

const fixtureRequest = (): JevCallRequest => ({
  index: 0,
  body: (decisions200 as unknown as { request: { body: CompiledRequest } }).request.body,
  est_tokens: 366,
});

describe('decisions: the wire (wire.go parity)', () => {
  const golden = JSON.parse(goldenText) as Record<string, unknown>;

  it('encodes {model, state, questions}: the Go golden body without session_id and provider (D100)', () => {
    const body = encodeDecisionRequest({ model: golden.model as string, state: golden.state as Record<string, unknown>, questions: golden.questions as Record<string, unknown> });
    const { session_id: _s, provider: _p, ...want } = golden;
    expect(JSON.parse(body)).toEqual(want);
    expect(Object.keys(JSON.parse(body))).toEqual(['model', 'state', 'questions']);
    // HTML stays as written (Go turns SetEscapeHTML off).
    expect(body).toContain('<b>Monthly</b> plans & add-ons');
  });

  it('decodes the fixture answer', () => {
    const w = decodeDecisionResponse(JSON.stringify(decisions200.body));
    expect(w).toEqual({
      id: 'dec_mock_c9a765a26ba3c2f0',
      model: 'typesafe/jev-1.13-20260917',
      provider: 'TypeSafe',
      answers: { contradiction__v1: { confidence: 0.46, noul: 0.49, type: 'noul' } },
      usage: { input_tokens: 366, output_tokens: 0, cost: 0.000015372 },
    });
  });

  it.each([
    ['not json', /not a decision JSON object/],
    ['[1]', /not a decision JSON object/],
    ['{"model":"m"}', /no answers object$/],
    ['{"error":{"message":"Provider returned error"}}', /no answers object: Provider returned error/],
    ['{"answers":{"q":"yes"}}', /not a decision JSON object/],
    ['{"answers":{"q":{"noul":"0.4"}}}', /not a decision JSON object/],
    ['{"answers":{"q":{"probabilities":{"a":"x"}}}}', /not a decision JSON object/],
    ['{"answers":{},"usage":{"input_tokens":-1}}', /negative input_tokens -1/],
    ['{"answers":{},"model":7}', /not a decision JSON object/],
  ])('%s is bad_response', (text, message) => {
    expect(() => decodeDecisionResponse(text)).toThrow(message);
    try {
      decodeDecisionResponse(text);
    } catch (err) {
      expect(err).toMatchObject({ code: 'bad_response', status: 200 });
    }
  });

  it('accepts prompt_tokens, drops a negative cost and unknown answer members', () => {
    const w = decodeDecisionResponse(JSON.stringify({ answers: { q: { type: 'noul', noul: 0.2, extra: 1 }, r: null }, usage: { prompt_tokens: 12, cost: -3 } }));
    expect(w.usage).toEqual({ input_tokens: 12, cost: null });
    expect(w.answers).toEqual({ q: { type: 'noul', noul: 0.2 }, r: null });
  });

  it('checkAnswers keeps asked answers and reports the anomalies', () => {
    const questions = { a__v1: { type: 'noul' }, b__v1: { type: 'choice' }, c__v1: { type: 'noul' }, d__v1: { type: 'choice' } };
    const answers: Record<string, RawAnswerInput | null> = {
      a__v1: { type: 'noul', noul: 0.1 },
      b__v1: { type: 'noul', noul: 0.3 },
      d__v1: { type: 'choice', choice: 'x', probabilities: { x: 0.9, y: 0.2 } },
      c__v1: null,
      z__v1: { type: 'noul', noul: 0.5 },
    };
    const r = checkAnswers(questions, answers);
    expect(Object.keys(r.answers)).toEqual(['a__v1', 'b__v1', 'd__v1']);
    expect(r.anomalies.sort()).toEqual(['missing_answer', 'probability_sum', 'type_mismatch', 'unexpected_answer']);
    expect(checkAnswers({ q: { type: 'choice' } }, { q: { type: 'choice', choice: 'x', probabilities: { x: 0.99, y: 0.02 } } }).anomalies).toEqual([]);
  });
});

describe('decisions: the call from the browser', () => {
  it('POSTs the encoded body to the build-time path with the key and returns the answer', async () => {
    const f = fakeFetch(() => serve(decisions200));
    const call = openRouterJevCall({ store: keyStore(), model: 'typesafe/jev-1.13', http: { fetch: f.fetch, referer: 'https://timkush1.github.io/TrustAgent/' } });
    expect(call.backend).toBe('openrouter-decisions');
    const r = await call.call(fixtureRequest());
    expect(r).toMatchObject({
      answers: { contradiction__v1: { confidence: 0.46, noul: 0.49, type: 'noul' } },
      usage: { input_tokens: 366, output_tokens: 0, cost: 0.000015372 },
      snapshot: 'typesafe/jev-1.13-20260917',
    });
    expect(f.calls).toHaveLength(1);
    expect(f.calls[0].url).toBe(DECISIONS_URL);
    expect(JSON.parse(f.calls[0].init.body as string)).toEqual(decisions200.request.body);
    expect((f.calls[0].init.headers as Record<string, string>).authorization).toBe(`Bearer ${KEY}`);
  });

  it('counts a missing usage block at the estimate, with no vendor cost', async () => {
    const f = fakeFetch(() => new Response(JSON.stringify({ answers: {}, model: '' }), { status: 200, headers: { 'content-type': 'application/json' } }));
    const r = await openRouterJevCall({ store: keyStore(), http: { fetch: f.fetch } }).call(fixtureRequest());
    expect(r.usage).toEqual({ input_tokens: 366, cost: null });
    expect(r.snapshot).toBe('typesafe/jev-1.13');
  });

  it('forgets a refused key at once, and keeps a key the account is out of credits on', async () => {
    const store = keyStore();
    await expect(openRouterJevCall({ store, http: { fetch: fakeFetch(() => serve(decisions402)).fetch } }).call(fixtureRequest())).rejects.toMatchObject({ code: 'credits' });
    expect(store.getSnapshot().key).toBe(KEY);
    const err = await openRouterJevCall({ store, http: { fetch: fakeFetch(() => serve(decisions401)).fetch } }).call(fixtureRequest()).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'auth', status: 401 });
    expect(store.getSnapshot().key).toBeNull();
    expect(liveErrorView(err).message).toBe('errors.unauthorized');
  });

  it('refuses a web page instead of JSON', async () => {
    await expect(openRouterJevCall({ store: keyStore(), http: { fetch: fakeFetch(() => serve(decisionsHtml)).fetch } }).call(fixtureRequest())).rejects.toMatchObject({ code: 'bad_content_type', status: 200 });
  });

  it('sends nothing without a key, and says nothing about it', async () => {
    const f = fakeFetch(() => serve(decisions200));
    const err = await openRouterJevCall({ store: keyStore(null), http: { fetch: f.fetch } }).call(fixtureRequest()).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'auth', status: 0 });
    expect(liveErrorView(err).silent).toBe(true);
    expect(f.calls).toHaveLength(0);
  });

  it('explains an unreadable failure with the free key check when error bodies are opaque', async () => {
    const store = keyStore();
    const f = fakeFetch((url) => {
      if (url.endsWith('/v1/key')) return new Response(JSON.stringify({ error: { code: 402, message: 'Insufficient credits' } }), { status: 402, headers: { 'content-type': 'application/json' } });
      return Promise.reject(new TypeError('Failed to fetch'));
    });
    const err = await openRouterJevCall({ store, http: { fetch: f.fetch }, errorsReadable: false }).call(fixtureRequest()).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'credits', status: 0 });
    expect(liveErrorView(err).screen).toBe('no_credits');
    expect(f.calls.map((c) => c.init.method)).toEqual(['POST', 'GET']);
    // With readable errors (this build), the network failure stands and no key check is made.
    const g = fakeFetch(() => Promise.reject(new TypeError('Failed to fetch')));
    await expect(openRouterJevCall({ store, http: { fetch: g.fetch } }).call(fixtureRequest())).rejects.toMatchObject({ code: 'network' });
    expect(g.calls).toHaveLength(1);
  });

  it('never leaks the key into a message, a URL or a body', async () => {
    const store = keyStore(`sk-or-v1-${NO_PATTERN}`);
    const echo = fakeFetch(() => new Response(JSON.stringify({ error: { message: `key sk-or-v1-${NO_PATTERN} is invalid` } }), { status: 401, headers: { 'content-type': 'application/json' } }));
    const err = (await openRouterJevCall({ store, http: { fetch: echo.fetch } }).call(fixtureRequest()).catch((e: unknown) => e)) as Error;
    expect(err.message).not.toContain(NO_PATTERN);
    expect(echo.calls[0].url).not.toContain(NO_PATTERN);
    expect(String(echo.calls[0].init.body)).not.toContain(NO_PATTERN);
  });
});

describe('decisions + runGate: a live check end to end (mocked OpenRouter)', () => {
  interface PackScenario {
    id: string;
    route: string;
    request: unknown;
    response: string;
    mock_answers: Record<string, RawAnswerInput>;
  }
  const s = (scenarioPack as unknown as { scenarios: PackScenario[] }).scenarios.find((x) => x.id === 'refund-contradiction-flag')!;

  it('the refund example is flagged with a contradiction, the vendor cost and 1 of 100 calls', async () => {
    const f = fakeFetch((_url, init) => {
      const asked = JSON.parse(init.body as string) as { questions: Record<string, unknown> };
      const answers = Object.fromEntries(Object.keys(asked.questions).map((q) => [q, s.mock_answers[q]]));
      return new Response(JSON.stringify({ id: 'dec_1', model: 'typesafe/jev-1.13-20260917', provider: 'TypeSafe', answers, usage: { input_tokens: 412, output_tokens: 0, cost: 0.0000173 } }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      });
    });
    const usage = createUsageStore({ maxCalls: 100, maxTokens: 8000, storage: memoryStorage(), target: null, price: () => null });
    const call = openRouterJevCall({ store: keyStore(), http: { fetch: f.fetch } });
    const gate = await runGate({ route: liveRoute(s.route), specs: builtinSpecs(), request: s.request, response: s.response, apiFormat: 'openai_chat', call, ledger: usage.forModel(call.model), maxTokens: 8000 });
    const result = playgroundResult(gate, call, s.response, { traceId: 't-1', createdAt: 0, policyVersion: 1 });

    expect(result.decision).toMatchObject({ action: 'flag', would_action: 'block', verdict: 'contradicted', status: 'ok' });
    expect(result.decision.reasons).toContain('contradiction:critical');
    expect(result.decision.decision_model).toMatchObject({ backend: 'openrouter-decisions', snapshot: 'typesafe/jev-1.13-20260917', cost_usd: 0.0000173, cost_source: 'reported', input_tokens: 412 });
    expect(usage.getSnapshot()).toMatchObject({ calls: 1, maxCalls: 100, cost_source: 'vendor', input_tokens: 412 });
    expect(f.calls).toHaveLength(1);
  });
});
