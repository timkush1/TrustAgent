/**
 * The security plane over HTTP (P4-PLAN §10 ON-1) against the msw seeds: request shapes (method,
 * path, query, body, If-Match), the D75 credential write, D76 leaving simulated mode, the D77
 * confirmation, the D81 last-admin refusal, paging, and shape checks on every response.
 */
import { describe, expect, it } from 'vitest';
import { createHttpClient } from '@/data/http';
import { createControlApi, errorReply } from '@/test/control/handlers';
import { settingsView } from '@/test/control/securityHandlers';
import { API, installMswServer, server } from '@/test/msw';
import { http, HttpResponse } from 'msw';
import { liveSecurityPlane } from './securityPlane';

installMswServer();

function setup(overrides: Parameters<typeof createControlApi>[0] = {}) {
  const api = createControlApi(overrides);
  server.use(...api.handlers);
  return { api, plane: liveSecurityPlane(createHttpClient({ apiBase: API, crossOrigin: false })) };
}

describe('security plane: credentials', () => {
  it('lists masked credentials and never a secret', async () => {
    const { plane } = setup();
    const list = await plane.listCredentials();
    expect(list.items.map((c) => c.name)).toEqual(['openrouter', 'typesafe', 'anthropic', 'upstream-default', 'compatible']);
    expect(list.items[0]).toMatchObject({ masked: 'sk-…3f2a', used_by: ['decision_model', 'judge'] });
  });

  it('stores a pasted key, verifies it and binds it, leaving simulated mode (D75, D76)', async () => {
    const settings = settingsView();
    settings.runtime.decision_model.backend = 'mock';
    settings.runtime.judge.provider = 'mock';
    settings.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
    const { plane, api } = setup({ settings });
    const result = await plane.putCredential('openrouter', {
      provider: 'openrouter',
      secret: 'sk-or-v1-e2e-ok-1234567890',
      verify: true,
      bind: ['decision_model', 'judge'],
    });
    expect(result.stored).toBe(true);
    expect(result.verification).toMatchObject({ ok: true, status: 'ok', latency_ms: 231 });
    expect(result.bindings).toEqual({ applied: ['decision_model', 'judge'], skipped: [] });
    expect(result.credential.masked).toBe('sk-…7890');
    expect(JSON.stringify(result)).not.toContain('sk-or-v1-e2e-ok-1234567890');
    const call = api.callsTo('/api/credentials/openrouter', 'PUT')[0];
    expect(call.body).toEqual({ provider: 'openrouter', secret: 'sk-or-v1-e2e-ok-1234567890', verify: true, bind: ['decision_model', 'judge'] });
    const after = await plane.getSettings();
    expect(after.runtime.decision_model.backend).toBe('auto');
    expect(after.runtime.judge.provider).toBe('auto');
    expect(after.runtime.bindings.decision_model).toBe('openrouter');
  });

  it('stores a key with no credits unbound, and keeps a bound key when the new one fails (D75)', async () => {
    const unbound = settingsView();
    unbound.runtime.bindings = { decision_model: null, judge: null, upstream_default: null };
    const first = setup({ settings: unbound });
    const noCredits = await first.plane.putCredential('openrouter', {
      provider: 'openrouter',
      secret: 'sk-or-v1-e2e-nocredits-0001',
      bind: ['decision_model', 'judge'],
    });
    expect(noCredits.stored).toBe(true);
    expect(noCredits.verification).toMatchObject({ ok: false, status: 'no_credits', hint: 'no_credits' });
    expect(noCredits.bindings.applied).toEqual([]);
    expect(noCredits.bindings.skipped.map((s) => s.reason)).toEqual(['verification_failed', 'verification_failed']);

    server.resetHandlers();
    const second = setup();
    const kept = await second.plane.putCredential('openrouter', { provider: 'openrouter', secret: 'sk-or-v1-unauthorized-9', bind: ['decision_model'] });
    expect(kept.stored).toBe(false);
    expect(kept.credential.masked).toBe('sk-…3f2a');
    expect(kept.verification?.status).toBe('unauthorized');
  });

  it('skips env-managed binding targets (D75)', async () => {
    const settings = settingsView({ env_managed: ['judge.provider', 'bindings.judge'] });
    const { plane } = setup({ settings });
    const result = await plane.putCredential('openrouter', { provider: 'openrouter', secret: 'sk-or-v1-e2e-ok-55', bind: ['decision_model', 'judge'] });
    expect(result.bindings).toEqual({ applied: ['decision_model'], skipped: [{ target: 'judge', reason: 'env_managed' }] });
  });

  it('checks a key again with bind (#57) and refuses env rows with bind', async () => {
    const { plane, api } = setup({ verifyStatus: { openrouter: 'ok' } });
    const again = await plane.verifyCredential('openrouter', { bind: ['decision_model'] });
    expect(again.verification?.ok).toBe(true);
    expect(api.callsTo('/api/credentials/openrouter/verify', 'POST')[0].body).toEqual({ bind: ['decision_model'] });
    await expect(plane.verifyCredential('typesafe', { bind: ['decision_model'] })).rejects.toMatchObject({ status: 409, code: 'env_managed' });
    const plain = await plane.verifyCredential('typesafe');
    expect(plain.stored).toBe(false);
    expect(api.callsTo('/api/credentials/typesafe/verify', 'POST').at(-1)?.body).toEqual({});
  });

  it('asks for a confirmation before removing the bound fast-check key, then removes it with ?confirm=true (D77)', async () => {
    const { plane, api } = setup({ enforcingRoutes: ['payments-faq'] });
    await expect(plane.deleteCredential('openrouter')).rejects.toMatchObject({
      status: 409,
      code: 'confirmation_required',
      details: { routes: ['payments-faq'] },
    });
    await plane.deleteCredential('openrouter', { confirm: true });
    expect(api.callsTo('/api/credentials/openrouter', 'DELETE').map((c) => c.query.get('confirm'))).toEqual([null, 'true']);
    expect((await plane.listCredentials()).items.some((c) => c.name === 'openrouter')).toBe(false);
    expect((await plane.getSettings()).runtime.bindings.decision_model).toBeNull();
  });

  it('starts and completes Connect OpenRouter, and refuses a stale nonce (D38)', async () => {
    const { plane, api } = setup();
    const start = await plane.startConnect({ return_to: '/app/setup?step=connect' });
    expect(start.auth_url).toMatch(/^https:\/\/openrouter\.ai\/auth\?callback_url=/);
    expect(api.callsTo('/api/connect/openrouter/start')[0].body).toEqual({ return_to: '/app/setup?step=connect' });
    await expect(plane.completeConnect({ code: 'c', nonce: 'not-the-nonce' })).rejects.toMatchObject({ status: 400, code: 'invalid_nonce' });
    const done = await plane.completeConnect({ code: 'c', nonce: start.nonce });
    expect(done.return_to).toMatch(/^\/app\//);
    await expect(plane.completeConnect({ code: 'c', nonce: start.nonce })).rejects.toMatchObject({ code: 'invalid_nonce' });
  });

  it('runs the decision-model probe (#60) and keeps Retry-After on 429', async () => {
    const { plane } = setup();
    expect((await plane.testDecisionModel()).ok).toBe(true);
    server.resetHandlers();
    const limited = setup({ decisionModelTest: errorReply(429, 'rate_limited', 'Try again shortly.', 8000) });
    await expect(limited.plane.testDecisionModel()).rejects.toMatchObject({ status: 429, retryAfterMs: 8000 });
  });
});

describe('security plane: keys, audit log, sessions', () => {
  it('creates a key once, revokes it, and refuses the last admin key (D81)', async () => {
    const { plane, api } = setup();
    const created = await plane.createApiKey({ label: 'my-app', scopes: ['ingest'] });
    expect(created.secret).toMatch(/^ta_/);
    expect(api.callsTo('/api/keys', 'POST')[0].body).toEqual({ label: 'my-app', scopes: ['ingest'] });
    const listed = await plane.listApiKeys();
    expect(listed.items[0]).toMatchObject({ label: 'my-app' });
    expect(JSON.stringify(listed)).not.toContain(created.secret);
    await plane.revokeApiKey(created.id);
    expect((await plane.listApiKeys()).items[0].revoked_at).not.toBeNull();
    await expect(plane.revokeApiKey('k_01a0e6f1')).rejects.toMatchObject({ status: 409, code: 'conflict', details: { reason: 'last_admin_key' } });
  });

  it('reads the audit log in keyset pages', async () => {
    const { plane, api } = setup();
    const page = await plane.listAdminAudit({ limit: 20, cursor: 'c2' });
    expect(page.items[0]).toMatchObject({ action: 'settings.update', actor: 'setup-admin' });
    expect(page.next_cursor).toBeNull();
    const q = api.callsTo('/api/admin/audit-log')[0].query;
    expect([q.get('limit'), q.get('cursor')]).toEqual(['20', 'c2']);
  });

  it('signs out everywhere and asks for a WebSocket ticket', async () => {
    const { plane, api } = setup();
    await plane.signOutEverywhere();
    expect(api.callsTo('/api/session', 'DELETE')[0].query.get('all')).toBe('true');
    expect(await plane.createWsTicket()).toEqual({ ticket: 'wst_N3c8vQe2Lk5RzT0bYh7MfA', expires_in: 30 });
  });

  it('sends ?confirm=true on a confirmed settings patch', async () => {
    const { plane, api } = setup({ enforcingRoutes: ['payments-faq'] });
    const s = await plane.getSettings();
    await expect(plane.patchSettings({ decision_model: { backend: 'off' } }, s.etag)).rejects.toMatchObject({ code: 'confirmation_required' });
    const next = await plane.patchSettings({ decision_model: { backend: 'off' } }, s.etag, { confirm: true });
    expect(next.runtime.decision_model.backend).toBe('off');
    expect(api.callsTo('/api/settings', 'PATCH').map((c) => c.query.get('confirm'))).toEqual([null, 'true']);
  });

  it('rejects a response without the fields a page needs', async () => {
    const plane = liveSecurityPlane(createHttpClient({ apiBase: API, crossOrigin: false }));
    server.use(
      http.get(`${API}/api/credentials`, () => HttpResponse.json({ items: [{ name: 'openrouter' }] })),
      http.post(`${API}/api/ws-ticket`, () => HttpResponse.json({ expires_in: 30 })),
    );
    await expect(plane.listCredentials()).rejects.toMatchObject({ code: 'invalid_response' });
    await expect(plane.createWsTicket()).rejects.toMatchObject({ code: 'invalid_response' });
  });
});
