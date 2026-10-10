import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '@/data/errors';
import { ALLOWED_REQUEST_HEADERS } from '@/domain/liveScenarios';
import { CHAT_URL, DECISIONS_URL, KEY_URL } from './endpoints';
import { isOpenRouterUrl, openRouterFetch, requestHeaders, siteReferer } from './http';
import { createKeyStore } from './keyStore';

const KEY = 'sk-or-v1-e2e-0123456789abcdef0123456789abcdef';
const NO_PATTERN = 'cmp0e2eZq7Lw4Rt9Yx2Vb6Nm';

interface Seen {
  url: string;
  init: RequestInit;
}

/** A fetch that records each call and answers `respond()`. */
function recordingFetch(respond: (url: string, init: RequestInit) => Promise<Response> | Response = () => json(200, { ok: true })) {
  const seen: Seen[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    seen.push({ url, init: init ?? {} });
    return respond(url, init ?? {});
  });
  return { fetch: fn as unknown as typeof fetch, seen };
}

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json', ...headers } });
}

const names = (init: RequestInit) => Object.keys(init.headers as Record<string, string>).sort();

afterEach(() => {
  vi.useRealTimers();
});

describe('openRouterFetch: what the browser sends (D100, D138)', () => {
  it('sends exactly the allowed header names on a keyed POST', async () => {
    const f = recordingFetch();
    await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 10_000 }, { fetch: f.fetch, referer: 'https://timkush1.github.io/TrustAgent/' });
    expect(names(f.seen[0].init)).toEqual([...ALLOWED_REQUEST_HEADERS].sort());
    expect(f.seen[0].init.headers).toEqual({
      authorization: `Bearer ${KEY}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'http-referer': 'https://timkush1.github.io/TrustAgent/',
      'x-openrouter-title': 'TrustAgent demo',
    });
  });

  it('sends a subset of them otherwise: no content-type without a body, no authorization without a key', async () => {
    const f = recordingFetch();
    await openRouterFetch({ url: KEY_URL, method: 'GET', key: KEY, timeoutMs: 10_000 }, { fetch: f.fetch });
    await openRouterFetch({ url: CHAT_URL, method: 'POST', body: '{}', timeoutMs: 10_000 }, { fetch: f.fetch });
    expect(names(f.seen[0].init)).toEqual(['accept', 'authorization', 'http-referer', 'x-openrouter-title']);
    expect(names(f.seen[1].init)).toEqual(['accept', 'content-type', 'http-referer', 'x-openrouter-title']);
    for (const s of f.seen) {
      for (const n of names(s.init)) expect(ALLOWED_REQUEST_HEADERS as readonly string[]).toContain(n);
      expect(names(s.init)).not.toContain('x-title');
    }
  });

  it('carries no cookies or referrer, follows no redirect and is never cached', async () => {
    const f = recordingFetch();
    await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{"a":1}', key: KEY, timeoutMs: 10_000 }, { fetch: f.fetch });
    expect(f.seen[0].init).toMatchObject({ method: 'POST', body: '{"a":1}', mode: 'cors', credentials: 'omit', referrerPolicy: 'no-referrer', redirect: 'error', cache: 'no-store' });
    expect(f.seen[0].init.signal).toBeInstanceOf(AbortSignal);
  });

  it('attributes the call to this site: location.origin + BASE_URL', async () => {
    expect(siteReferer()).toBe(`${location.origin}${import.meta.env.BASE_URL}`);
    const f = recordingFetch();
    await openRouterFetch({ url: KEY_URL, method: 'GET', key: KEY, timeoutMs: 10_000 }, { fetch: f.fetch });
    expect((f.seen[0].init.headers as Record<string, string>)['http-referer']).toBe(siteReferer());
  });

  it('attaches the key only for https://openrouter.ai exactly', () => {
    const referer = 'https://example.test/';
    for (const url of [
      'https://openrouter.ai.evil.example/api/v1/key',
      'https://evil.example/api/v1/key?openrouter.ai',
      'http://openrouter.ai/api/v1/key',
      'https://openrouter.ai:8443/api/v1/key',
      'https://user:pw@openrouter.ai/api/v1/key',
      'https://www.openrouter.ai/api/v1/key',
      'not a url',
    ]) {
      expect(isOpenRouterUrl(url)).toBe(false);
      expect(requestHeaders(url, { key: KEY, body: true, referer })).not.toHaveProperty('authorization');
    }
    expect(requestHeaders(DECISIONS_URL, { key: KEY, body: true, referer }).authorization).toBe(`Bearer ${KEY}`);
  });

  it('refuses any other host before fetching', async () => {
    const f = recordingFetch();
    await expect(openRouterFetch({ url: 'https://evil.example/api/v1/key', method: 'GET', key: KEY, timeoutMs: 1000 }, { fetch: f.fetch })).rejects.toThrow(/only https:\/\/openrouter.ai/);
    expect(f.seen).toHaveLength(0);
  });

  it('never puts the key in a URL', async () => {
    const f = recordingFetch();
    for (const url of [DECISIONS_URL, CHAT_URL, KEY_URL]) {
      await openRouterFetch({ url, method: 'POST', body: '{}', key: NO_PATTERN.padEnd(40, 'x'), timeoutMs: 1000 }, { fetch: f.fetch });
    }
    for (const s of f.seen) expect(s.url).not.toContain(NO_PATTERN);
  });
});

describe('openRouterFetch: answers and failures', () => {
  it('returns the status, headers and body text of any answer', async () => {
    const f = recordingFetch(() => json(402, { error: { code: 402, message: 'Insufficient credits' } }));
    const res = await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 1000 }, { fetch: f.fetch });
    expect(res.status).toBe(402);
    expect(res.headers.get('content-type')).toBe('application/json');
    expect(JSON.parse(res.text)).toEqual({ error: { code: 402, message: 'Insufficient credits' } });
    expect(res.latency_ms).toBeGreaterThanOrEqual(0);
  });

  it('turns a TypeError (offline, CORS, an extension) into network with no status', async () => {
    const f = recordingFetch(() => Promise.reject(new TypeError('Failed to fetch')));
    const err = await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 1000 }, { fetch: f.fetch }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ kind: 'network', code: 'network', status: 0 });
  });

  it('keeps the status when the body fails after the headers arrived', async () => {
    const broken = new Response(new ReadableStream({ start: (c) => c.error(new TypeError('connection reset')) }), { status: 200 });
    const f = recordingFetch(() => broken);
    const err = await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 1000 }, { fetch: f.fetch }).catch((e: unknown) => e);
    expect(err).toMatchObject({ code: 'network', status: 200 });
  });

  it('times out after its limit and aborts the fetch', async () => {
    vi.useFakeTimers();
    let signal: AbortSignal | undefined;
    const f = recordingFetch(
      (_url, init) =>
        new Promise((_resolve, reject) => {
          signal = init.signal ?? undefined;
          init.signal?.addEventListener('abort', () => reject(init.signal?.reason));
        }),
    );
    const p = openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 10_000 }, { fetch: f.fetch }).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(9_999);
    expect(signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await p).toMatchObject({ kind: 'timeout', code: 'timeout', status: 0 });
    expect(signal?.aborted).toBe(true);
  });

  it("follows the caller's AbortSignal, and never fetches with one already aborted", async () => {
    const f = recordingFetch((_url, init) => new Promise((_r, reject) => init.signal?.addEventListener('abort', () => reject(init.signal?.reason))));
    const before = new AbortController();
    before.abort();
    await expect(openRouterFetch({ url: KEY_URL, method: 'GET', key: KEY, timeoutMs: 1000, signal: before.signal }, { fetch: f.fetch })).rejects.toMatchObject({ code: 'aborted' });
    expect(f.seen).toHaveLength(0);

    const during = new AbortController();
    const p = openRouterFetch({ url: KEY_URL, method: 'GET', key: KEY, timeoutMs: 1000, signal: during.signal }, { fetch: f.fetch }).catch((e: unknown) => e);
    during.abort();
    expect(await p).toMatchObject({ kind: 'aborted', code: 'aborted' });
  });

  it('is aborted by Forget key while in flight, and untracked once done', async () => {
    const store = createKeyStore({ storage: () => window.sessionStorage, target: null });
    store.set(KEY);
    const tracked: AbortController[] = [];
    const track = (c: AbortController) => {
      tracked.push(c);
      return store.track(c);
    };
    const hang = recordingFetch((_url, init) => new Promise((_r, reject) => init.signal?.addEventListener('abort', () => reject(init.signal?.reason))));
    const p = openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 10_000, track }, { fetch: hang.fetch }).catch((e: unknown) => e);
    store.forget();
    expect(await p).toMatchObject({ code: 'aborted' });

    const ok = recordingFetch();
    await openRouterFetch({ url: DECISIONS_URL, method: 'POST', body: '{}', key: KEY, timeoutMs: 10_000, track }, { fetch: ok.fetch });
    store.forget();
    // The finished request was untracked, so Forget did not abort it.
    expect(tracked[1].signal.aborted).toBe(false);
    store.dispose();
    window.sessionStorage.clear();
  });
});
