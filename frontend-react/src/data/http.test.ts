import { http, HttpResponse, delay } from 'msw';
import { describe, expect, it } from 'vitest';
import { API, server, installMswServer } from '@/test/msw';
import { ApiError, apiErrorFromResponse, parseRetryAfter } from './errors';
import { buildQuery, createHttpClient } from './http';

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
