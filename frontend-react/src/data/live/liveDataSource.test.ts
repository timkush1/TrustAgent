import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { API, server, installMswServer } from '@/test/msw';
import { audit, flaggedTrace, iso } from '@/test/fixtures';
import { LiveDataSource, RESYNC_LIMIT } from './liveDataSource';

installMswServer();

const ds = () => new LiveDataSource({ apiBase: API, crossOrigin: true, wsUrl: 'ws://proxy.test/ws' });

describe('LiveDataSource REST', () => {
  it('normalizes /health', async () => {
    server.use(http.get(`${API}/health`, () => HttpResponse.json({ status: 'healthy', version: '1.4.0', audit_engine: true })));
    await expect(ds().getHealth()).resolves.toEqual({ status: 'ok', version: '1.4.0', demo: false, audit_engine: true });
  });

  it('encodes ids in paths', async () => {
    server.use(http.get(`${API}/api/audits/:id`, ({ params }) => HttpResponse.json(audit({ audit_id: String(params.id) }))));
    await expect(ds().getAudit('a/b')).resolves.toMatchObject({ audit_id: 'a/b' });
  });

  it('turns a body that is not the v2 shape into an invalid_response error, not a crash', async () => {
    const html = '<!doctype html><html><body>SPA fallback</body></html>';
    server.use(
      http.get(`${API}/api/traces`, () => HttpResponse.html(html)),
      http.get(`${API}/api/status`, () => HttpResponse.html(html)),
      // The pre-v2 proxy's audit record: no trace_id, no claims array.
      http.get(`${API}/api/audits/:id`, () => HttpResponse.json({ id: 'a1', trust_score: 80, grade: 'B' })),
    );
    const invalid = { kind: 'invalid_response', code: 'invalid_response' };
    await expect(ds().listTraces({ limit: 50 })).rejects.toMatchObject({ ...invalid, message: expect.stringContaining('GET /api/traces') });
    await expect(ds().getStatus()).rejects.toMatchObject(invalid);
    await expect(ds().getAudit('a1')).rejects.toMatchObject(invalid);
  });
});

describe('LiveDataSource resync (SPEC §2.5)', () => {
  it('asks the three endpoints with the right cursors and merges the pages', async () => {
    const seen: string[] = [];
    server.use(
      http.get(`${API}/api/traces`, ({ request }) => {
        seen.push(new URL(request.url).search);
        return HttpResponse.json({ items: [flaggedTrace()], next_cursor: null });
      }),
      http.get(`${API}/api/gate/decisions`, ({ request }) => {
        seen.push(new URL(request.url).search);
        return HttpResponse.json({ items: [], next_cursor: null });
      }),
      http.get(`${API}/api/audits`, ({ request }) => {
        seen.push(new URL(request.url).search);
        return HttpResponse.json({ items: [audit()], next_cursor: null });
      }),
    );
    const since = iso(55_000);
    const r = await ds().resync(since, 'gap');
    const q = encodeURIComponent(since);
    expect(seen.sort()).toEqual(
      [`?completed_since=${q}&limit=${RESYNC_LIMIT}`, `?updated_since=${q}&limit=${RESYNC_LIMIT}`, `?updated_since=${q}&limit=${RESYNC_LIMIT}`].sort(),
    );
    expect(r).toMatchObject({ type: 'resync', reason: 'gap', since, truncated: false });
    expect(r.traces).toHaveLength(1);
    expect(r.audits).toHaveLength(1);
  });

  it('tolerates missing decision and audit endpoints but not traces', async () => {
    server.use(
      http.get(`${API}/api/traces`, () =>
        HttpResponse.json({ items: Array.from({ length: RESYNC_LIMIT }, () => flaggedTrace()), next_cursor: 'c1' }),
      ),
      http.get(`${API}/api/gate/decisions`, () => HttpResponse.json({ error: { code: 'not_found', message: 'no' } }, { status: 404 })),
      http.get(`${API}/api/audits`, () => HttpResponse.json({ error: 'persistence disabled' }, { status: 503 })),
    );
    const r = await ds().resync(iso(), 'epoch_changed');
    expect(r).toMatchObject({ decisions: [], audits: [], truncated: true });

    // A pre-v2 proxy answers /api/audits with its own list shape: skipped like a missing endpoint.
    server.use(
      http.get(`${API}/api/traces`, () => HttpResponse.json({ items: [flaggedTrace()], next_cursor: null })),
      http.get(`${API}/api/audits`, () => HttpResponse.json({ audits: [audit()], total: 1, limit: 200, offset: 0 })),
    );
    await expect(ds().resync(iso(), 'gap')).resolves.toMatchObject({ audits: [], decisions: [], truncated: false });

    server.use(http.get(`${API}/api/traces`, () => HttpResponse.error()));
    await expect(ds().resync(iso(), 'gap')).rejects.toMatchObject({ kind: 'network' });
  });
});
