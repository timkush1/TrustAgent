import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import auth200 from '@contracts/fixtures/vendor/openrouter/auth_keys.200.json';
import auth400 from '@contracts/fixtures/vendor/openrouter/auth_keys.400.json';
import auth403 from '@contracts/fixtures/vendor/openrouter/auth_keys.403.json';
import { beginConnect, callbackUrl, checkReturnPath, DEFAULT_RETURN, exchangeCode, resetCallbackForTests, runCallback, type CallbackDeps } from './connect';
import { AUTH_KEYS_URL } from './endpoints';
import { errorKind } from './errors';
import { createKeyStore, PKCE_TTL_MS, type KeyStore } from './keyStore';
import { challengeS256 } from './pkce';
import { MemoryStorage } from './testing';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const MOCK_KEY = (auth200 as { body: { key: string } }).body.key;

interface Fixture {
  status: number;
  headers: Record<string, string>;
  body: unknown;
}
const serve = (f: Fixture): Response => new Response(typeof f.body === 'string' ? f.body : JSON.stringify(f.body), { status: f.status, headers: f.headers });

function fakeFetch(answer: () => Response | Promise<Response>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} });
    return answer();
  }) as typeof globalThis.fetch;
  return { fetch, calls };
}

let t = 1_760_000_000_000;
const stores: KeyStore[] = [];
function keyStore(storage: Storage = new MemoryStorage()): KeyStore {
  const s = createKeyStore({ storage: () => storage, now: () => t, target: null, setTimer: () => 0, clearTimer: () => {} });
  stores.push(s);
  return s;
}

beforeEach(() => {
  resetCallbackForTests();
});
afterEach(() => {
  for (const s of stores.splice(0)) s.dispose();
  resetCallbackForTests();
});

describe('return path (D105)', () => {
  it('keeps the Playground and Studio paths it allows', () => {
    for (const p of [
      'app/playground/',
      'app/playground/#live',
      'app/playground/?scenario=refund-contradiction-flag#live',
      'app/playground/?scenario=pii-leak-flag',
      'app/evaluators/studio/',
      'app/evaluators/studio/#live',
    ]) {
      expect(checkReturnPath(p), p).toBe(p);
    }
  });

  it('falls back to the default for anything else', () => {
    for (const p of ['//evil.example', '/\\evil.example', 'app/../x', 'javascript:alert(1)', '/app/playground/', 'https://evil.example/app/playground/', 'app/playground/?scenario=other', 'app/playground', '', null, 42]) {
      expect(checkReturnPath(p), String(p)).toBe(DEFAULT_RETURN);
    }
  });
});

describe('beginConnect', () => {
  it('remembers a verifier with the checked return path and sends the S256 challenge', () => {
    const store = keyStore();
    const begun = beginConnect(store, 'app/playground/?scenario=grounded-pass#live', { origin: 'https://timkush1.github.io', base: '/TrustAgent/', fill: (b) => b.fill(0) });
    expect(begun.ok).toBe(true);
    const url = new URL((begun as { url: string }).url);
    expect(url.origin + url.pathname).toBe('https://openrouter.ai/auth');
    expect(url.searchParams.get('callback_url')).toBe('https://timkush1.github.io/TrustAgent/connect/callback/');
    expect(url.searchParams.get('code_challenge')).toBe(challengeS256('A'.repeat(43)));
    expect(url.searchParams.get('code_challenge_method')).toBe('S256');
    expect(url.searchParams.get('key_label')).toBe('TrustAgent demo');
    expect(store.takePending()).toMatchObject({ status: 'ok', verifier: 'A'.repeat(43), returnPath: 'app/playground/?scenario=grounded-pass#live' });
  });

  it('stores the default for a return path it does not allow', () => {
    const store = keyStore();
    beginConnect(store, '//evil.example', { origin: 'https://x.example', base: '/' });
    expect(store.takePending()).toMatchObject({ status: 'ok', returnPath: DEFAULT_RETURN });
  });

  it('does not start when this tab cannot store anything', () => {
    const blocked = {
      length: 0,
      clear() {},
      key: () => null,
      getItem: () => {
        throw new DOMException('blocked', 'SecurityError');
      },
      setItem: () => {
        throw new DOMException('blocked', 'SecurityError');
      },
      removeItem: () => {},
    } as Storage;
    expect(beginConnect(keyStore(blocked), DEFAULT_RETURN, { origin: 'https://x.example', base: '/' })).toEqual({ ok: false, reason: 'storage_blocked' });
  });

  it('builds the callback URL under the base', () => {
    expect(callbackUrl('http://localhost:24174', '/TrustAgent/')).toBe('http://localhost:24174/TrustAgent/connect/callback/');
    expect(callbackUrl('https://x.example', '/')).toBe('https://x.example/connect/callback/');
  });
});

describe('exchangeCode', () => {
  it('posts the code and verifier with no key and returns the key', async () => {
    const f = fakeFetch(() => serve(auth200 as Fixture));
    const key = await exchangeCode('code-1', 'v'.repeat(43), { http: { fetch: f.fetch, referer: 'https://x.example/' } });
    expect(key).toBe(MOCK_KEY);
    expect(f.calls).toHaveLength(1);
    expect(f.calls[0].url).toBe(AUTH_KEYS_URL);
    expect(JSON.parse(String(f.calls[0].init.body))).toEqual({ code: 'code-1', code_verifier: 'v'.repeat(43), code_challenge_method: 'S256' });
    expect(Object.keys(f.calls[0].init.headers as Record<string, string>)).not.toContain('authorization');
  });

  it.each([
    ['a refused code (400)', auth400, 'bad_request'],
    ['a refused verifier (403)', auth403, 'auth'],
  ])('rejects %s', async (_name, fixture, kind) => {
    const f = fakeFetch(() => serve(fixture as Fixture));
    const err = await exchangeCode('c', 'v', { http: { fetch: f.fetch, referer: 'r' } }).catch((e: unknown) => e);
    expect(errorKind(err)).toBe(kind);
  });

  it('rejects an answer without an OpenRouter key', async () => {
    const f = fakeFetch(() => new Response(JSON.stringify({ key: 'not-a-key' }), { status: 200, headers: { 'content-type': 'application/json' } }));
    const err = await exchangeCode('c', 'v', { http: { fetch: f.fetch, referer: 'r' } }).catch((e: unknown) => e);
    expect(errorKind(err)).toBe('bad_response');
  });
});

describe('runCallback (D105)', () => {
  function deps(store: KeyStore, search: string, extra: Partial<CallbackDeps> = {}) {
    const events: string[] = [];
    const f = fakeFetch(() => {
      events.push('fetch');
      return serve(auth200 as Fixture);
    });
    const d: CallbackDeps = {
      store,
      search,
      framed: false,
      scrubUrl: () => events.push('scrub'),
      leave: (url) => events.push(`leave ${url}`),
      base: '/TrustAgent/',
      http: { fetch: f.fetch, referer: 'r' },
      ...extra,
    };
    return { d, events, calls: f.calls };
  }

  it('scrubs the URL before the exchange, stores the key and leaves for the return path', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), 'app/playground/?scenario=refund-contradiction-flag#live');
    const { d, events, calls } = deps(store, '?code=abc');
    await expect(runCallback(d)).resolves.toBe('done');
    expect(events).toEqual(['scrub', 'fetch', 'leave /TrustAgent/app/playground/?scenario=refund-contradiction-flag#live']);
    expect(JSON.parse(String(calls[0].init.body)).code_verifier).toBe('v'.repeat(43));
    expect(store.currentKey()).toBe(MOCK_KEY);
    expect(store.hasPending()).toBe(false);
  });

  it('runs once per page load: a second call sends nothing', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), null);
    const first = deps(store, '?code=abc');
    const second = deps(store, '');
    const [a, b] = await Promise.all([runCallback(first.d), runCallback(second.d)]);
    expect([a, b]).toEqual(['done', 'done']);
    expect(first.calls.length + second.calls.length).toBe(1);
    expect(first.events.at(-1)).toBe('leave /TrustAgent/app/playground/#live');
  });

  it('never exchanges a code without this tab’s verifier (login CSRF)', async () => {
    const { d, events, calls } = deps(keyStore(), '?code=attacker');
    await expect(runCallback(d)).resolves.toBe('missing_verifier');
    expect(calls).toHaveLength(0);
    expect(events).toEqual(['scrub']);
  });

  it('turns any error into refused, and takes the verifier', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), null);
    const { d, calls } = deps(store, '?error=Paste+your+key+at+evil.example&error_description=x');
    await expect(runCallback(d)).resolves.toBe('refused');
    expect(calls).toHaveLength(0);
    expect(store.hasPending()).toBe(false);
  });

  it('expires a sign-in older than 10 minutes', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), null);
    t += PKCE_TTL_MS + 1;
    const { d, calls } = deps(store, '?code=abc');
    await expect(runCallback(d)).resolves.toBe('expired');
    expect(calls).toHaveLength(0);
  });

  it('says already connected on a reload after success', async () => {
    const store = keyStore();
    store.set(KEY);
    const { d, calls } = deps(store, '');
    await expect(runCallback(d)).resolves.toBe('already_connected');
    resetCallbackForTests();
    const again = deps(store, '?code=abc');
    await expect(runCallback(again.d)).resolves.toBe('already_connected');
    expect(calls.length + again.calls.length).toBe(0);
  });

  it('is missing_verifier without a code or a key', async () => {
    const { d } = deps(keyStore(), '');
    await expect(runCallback(d)).resolves.toBe('missing_verifier');
  });

  it('exchanges nothing inside a frame (D106)', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), null);
    const { d, events, calls } = deps(store, '?code=abc', { framed: true });
    await expect(runCallback(d)).resolves.toBe('framed');
    expect(calls).toHaveLength(0);
    expect(events).toEqual(['scrub']);
  });

  it('says network when openrouter.ai cannot be reached, refused when it says no', async () => {
    const store = keyStore();
    store.savePending('v'.repeat(43), null);
    const offline = deps(store, '?code=abc', {
      exchange: () => Promise.reject(new TypeError('Failed to fetch')),
    });
    // A bare TypeError (the browser blocked the request) is the network state, never a crash.
    await expect(runCallback(offline.d)).resolves.toBe('network');
    resetCallbackForTests();
    store.savePending('v'.repeat(43), null);
    const blocked = deps(store, '?code=abc', {
      http: { fetch: (() => Promise.reject(new TypeError('Failed to fetch'))) as unknown as typeof fetch, referer: 'r' },
    });
    await expect(runCallback(blocked.d)).resolves.toBe('network');
    resetCallbackForTests();
    store.savePending('v'.repeat(43), null);
    const refused = deps(store, '?code=abc', { http: { fetch: (async () => serve(auth403 as Fixture)) as typeof fetch, referer: 'r' } });
    await expect(runCallback(refused.d)).resolves.toBe('refused');
    expect(store.currentKey()).toBeNull();
  });
});
