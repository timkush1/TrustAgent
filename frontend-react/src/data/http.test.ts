import { http, HttpResponse, delay } from 'msw';
import { afterEach, describe, expect, it } from 'vitest';
import { API, server, installMswServer } from '@/test/msw';
import { ApiError, apiErrorFromResponse, parseRetryAfter } from './errors';
import { buildQuery, clearDashboardKey, createHttpClient, DASHBOARD_KEY_STORAGE, readDashboardKey, storeDashboardKey } from './http';

installMswServer();

const client = (crossOrigin = true) => createHttpClient({ apiBase: API, crossOrigin });

describe('http client', () => {
  it('GETs JSON with the query appended', async () => {
    let seen: URL | null = null;
    server.use(
      http.get(`${API}/api/traces`, ({ request }) => {
        seen = new URL(request.url);
        return HttpResponse.json({ items: [], next_cursor: null });
      }),
    );
    await expect(client().get('/api/traces', { query: { limit: 200, route: 'support-bot', q: undefined } })).resolves.toEqual({
      items: [],
      next_cursor: null,
    });
    expect(seen!.searchParams.toString()).toBe('limit=200&route=support-bot');
  });

  it('adds the CSRF header to same-origin writes only', async () => {
    const headers: (string | null)[] = [];
    server.use(
      http.put(`${API}/api/labels`, ({ request }) => {
        headers.push(request.headers.get('x-trustagent-csrf'));
        return new HttpResponse(null, { status: 204 });
      }),
    );
    await createHttpClient({ apiBase: API, crossOrigin: false }).request('/api/labels', { method: 'PUT', body: {} });
    await createHttpClient({ apiBase: API, crossOrigin: true }).request('/api/labels', { method: 'PUT', body: {} });
    expect(headers).toEqual(['1', null]);
  });

  it('turns the error envelope into an ApiError', async () => {
    server.use(
      http.get(`${API}/api/traces/x`, () =>
        HttpResponse.json(
          { error: { code: 'not_found', message: 'No trace x.' }, trace_id: 'x' },
          { status: 404 },
        ),
      ),
    );
    const err = await client().get('/api/traces/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ kind: 'http', status: 404, code: 'not_found', message: 'No trace x.', traceId: 'x' });
    expect((err as ApiError).transient).toBe(false);
  });

  it('reads the legacy {"error": "…"} body and Retry-After', async () => {
    server.use(
      http.get(`${API}/api/audits`, () =>
        HttpResponse.json({ error: 'persistence disabled' }, { status: 503, headers: { 'Retry-After': '3' } }),
      ),
    );
    const err = (await client().get('/api/audits').catch((e: unknown) => e)) as ApiError;
    expect(err).toMatchObject({ status: 503, code: 'unavailable', message: 'persistence disabled', retryAfterMs: 3000 });
    expect(err.transient).toBe(true);
  });

  it('reports network failures, timeouts and aborts distinctly', async () => {
    server.use(
      http.get(`${API}/down`, () => HttpResponse.error()),
      http.get(`${API}/slow`, async () => {
        await delay(200);
        return HttpResponse.json({});
      }),
    );
    await expect(client().get('/down')).rejects.toMatchObject({ kind: 'network', status: 0, code: 'network' });
    await expect(client().get('/slow', { timeoutMs: 20 })).rejects.toMatchObject({ kind: 'timeout', code: 'timeout' });
    const ac = new AbortController();
    const p = client().get('/slow', { signal: ac.signal });
    ac.abort();
    await expect(p).rejects.toMatchObject({ kind: 'aborted' });
  });

  it('rejects an unreadable success body', async () => {
    server.use(http.get(`${API}/bad`, () => new HttpResponse('{oops', { headers: { 'Content-Type': 'application/json' } })));
    await expect(client().get('/bad')).rejects.toMatchObject({ kind: 'invalid_response' });
  });
});

describe('http client stream()', () => {
  it('resolves with the response so the caller can read the body', async () => {
    let accept: string | null = null;
    server.use(
      http.post(`${API}/api/playground/run`, ({ request }) => {
        accept = request.headers.get('accept');
        return new HttpResponse('data: 1\n\n', { headers: { 'Content-Type': 'text/event-stream' } });
      }),
    );
    const res = await client().stream('/api/playground/run', { method: 'POST', body: { stream: true } });
    expect(await res.text()).toBe('data: 1\n\n');
    expect(accept).toContain('text/event-stream');
  });

  it('turns an error status into an ApiError with the server message', async () => {
    server.use(
      http.post(`${API}/api/playground/run`, () =>
        HttpResponse.json({ error: { code: 'rate_limited', message: 'Slow down.' } }, { status: 429, headers: { 'Retry-After': '6' } }),
      ),
    );
    await expect(client().stream('/api/playground/run', { method: 'POST', body: {} })).rejects.toMatchObject({
      status: 429,
      code: 'rate_limited',
      message: 'Slow down.',
      retryAfterMs: 6000,
    });
  });

  it('times out waiting for headers, and reports network failures and aborts', async () => {
    server.use(
      http.post(`${API}/slow`, async () => {
        await delay(200);
        return new HttpResponse('');
      }),
      http.post(`${API}/down`, () => HttpResponse.error()),
    );
    await expect(client().stream('/slow', { method: 'POST', timeoutMs: 20 })).rejects.toMatchObject({ kind: 'timeout' });
    await expect(client().stream('/down', { method: 'POST' })).rejects.toMatchObject({ kind: 'network' });
    const ac = new AbortController();
    const p = client().stream('/slow', { method: 'POST', signal: ac.signal });
    ac.abort();
    await expect(p).rejects.toMatchObject({ kind: 'aborted' });
  });
});

describe('errors helpers', () => {
  it('maps statuses to codes when the body has none', () => {
    expect(apiErrorFromResponse(429, undefined).code).toBe('rate_limited');
    expect(apiErrorFromResponse(418, 'teapot').code).toBe('http_error');
  });

  it('parses Retry-After seconds and dates', () => {
    expect(parseRetryAfter('2')).toBe(2000);
    expect(parseRetryAfter('Wed, 21 Oct 2015 07:28:05 GMT', Date.parse('Wed, 21 Oct 2015 07:28:00 GMT'))).toBe(5000);
    expect(parseRetryAfter('soon')).toBeNull();
  });

  it('builds repeated keys for arrays and skips empty values', () => {
    expect(buildQuery({ a: [1, 2], b: '', c: null, d: false })).toBe('?a=1&a=2&d=false');
    expect(buildQuery({})).toBe('');
  });
});

describe('cross-origin dashboard key (P4-PLAN D71)', () => {
  afterEach(() => clearDashboardKey());

  function recordHeaders() {
    const seen: { key: string | null; csrf: string | null; cookie: string | null }[] = [];
    const record = ({ request }: { request: Request }) => {
      seen.push({ key: request.headers.get('x-trustagent-key'), csrf: request.headers.get('x-trustagent-csrf'), cookie: request.headers.get('cookie') });
      return HttpResponse.json({});
    };
    server.use(http.get(`${API}/api/status`, record), http.post(`${API}/api/keys`, record));
    return seen;
  }

  it('keeps the key in sessionStorage for this tab, never in localStorage', () => {
    expect(readDashboardKey()).toBeNull();
    expect(storeDashboardKey('  ta_admin_key  ')).toBe(true);
    expect(sessionStorage.getItem(DASHBOARD_KEY_STORAGE)).toBe('ta_admin_key');
    expect(readDashboardKey()).toBe('ta_admin_key');
    expect(Object.keys(localStorage).some((k) => localStorage.getItem(k)?.includes('ta_admin_key'))).toBe(false);
    clearDashboardKey();
    expect(readDashboardKey()).toBeNull();
  });

  it('sends the stored key as X-TrustAgent-Key on every cross-origin request, without CSRF or cookies', async () => {
    const seen = recordHeaders();
    await client(true).get('/api/status');
    storeDashboardKey('ta_admin_key');
    await client(true).get('/api/status');
    await client(true).request('/api/keys', { method: 'POST', body: {} });
    expect(seen).toEqual([
      { key: null, csrf: null, cookie: null },
      { key: 'ta_admin_key', csrf: null, cookie: null },
      { key: 'ta_admin_key', csrf: null, cookie: null },
    ]);
  });

  it('never sends the stored key to a same-origin proxy, which uses the session cookie', async () => {
    const seen = recordHeaders();
    storeDashboardKey('ta_admin_key');
    await client(false).get('/api/status');
    await client(false).request('/api/keys', { method: 'POST', body: {} });
    expect(seen.map((s) => s.key)).toEqual([null, null]);
    expect(seen.map((s) => s.csrf)).toEqual([null, '1']);
  });

  it('leaves an explicit key header alone (validating a key before keeping it)', async () => {
    const seen = recordHeaders();
    storeDashboardKey('ta_old');
    await client(true).get('/api/status', { headers: { 'X-TrustAgent-Key': 'ta_candidate' } });
    expect(seen[0].key).toBe('ta_candidate');
  });
});
