/**
 * The control-plane seams: the planes are spread onto LiveControlPlane and unavailableControlPlane,
 * the settings pair is live (GET, PATCH with If-Match) against the msw seeds, and every other plane
 * method rejects with 404 `not_implemented` until its owner lands the body.
 */
import { describe, expect, it } from 'vitest';
import settingsEnvManaged from '@contracts/fixtures/api/SettingsView.env-managed.json';
import { unavailableControlPlane } from '@/data/controlPlane';
import { errorCodeHint, CONTROL_ERROR_CODES } from '@/data/errors';
import { createHttpClient } from '@/data/http';
import { LiveControlPlane } from '@/data/live/liveControlPlane';
import { createControlApi } from '@/test/control/handlers';
import { patchPaths } from '@/test/control/securityHandlers';
import { API, installMswServer, server } from '@/test/msw';
import type { SettingsView } from '@/types/api';
import { liveCostPlane } from './costPlane';
import { liveEvaluatorPlane } from './evaluatorPlane';
import { liveSecurityPlane } from './securityPlane';
import { liveStudioPlane } from './studioPlane';

installMswServer();

const http = () => createHttpClient({ apiBase: API, crossOrigin: false });
const plane = () => new LiveControlPlane(http());

describe('control planes', () => {
  it('puts every plane method on LiveControlPlane and on unavailableControlPlane', () => {
    const live = plane();
    const unavailable = unavailableControlPlane('Not here.');
    const methods = [liveSecurityPlane, liveCostPlane, liveEvaluatorPlane, liveStudioPlane].flatMap((make) => Object.keys(make(http())));
    expect(methods.length).toBeGreaterThan(30);
    for (const name of methods) {
      expect(typeof (live as unknown as Record<string, unknown>)[name], name).toBe('function');
      expect(typeof (unavailable as unknown as Record<string, unknown>)[name], name).toBe('function');
    }
  });

  it('rejects every method of the unavailable plane with 404 not_implemented and its message', async () => {
    // Every live plane method has a body since I-1; the static build's plane still refuses them all.
    const unavailable = unavailableControlPlane('Not in the static build.');
    const refused = { status: 404, code: 'not_implemented', message: 'Not in the static build.' };
    await expect(unavailable.listCredentials()).rejects.toMatchObject(refused);
    await expect(unavailable.getRoiTimeseries({ window: '7d' })).rejects.toMatchObject(refused);
    await expect(unavailable.getLabelQueue({})).rejects.toMatchObject(refused);
    await expect(unavailable.draftEvaluator({ description: 'x' })).rejects.toMatchObject(refused);
    await expect(unavailable.getSettings()).rejects.toMatchObject(refused);
  });

  it('reads the settings and patches them with If-Match, bumping the version', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    const live = plane();
    const settings = await live.getSettings();
    expect(settings.etag).toBe('"s7"');
    expect(settings.runtime.budgets.llm_usd_day).toBe(5);

    const next = await live.patchSettings({ budgets: { llm_usd_day: 3 } }, settings.etag);
    expect(next.version).toBe(8);
    expect(next.etag).toBe('"s8"');
    expect(next.runtime.budgets).toEqual({ decision_usd_day: 2, llm_usd_day: 3 });
    const call = api.callsTo('/api/settings', 'PATCH')[0];
    expect(call.headers.get('if-match')).toBe('"s7"');
    expect(call.body).toEqual({ budgets: { llm_usd_day: 3 } });
    expect(call.query.get('confirm')).toBeNull();
  });

  it('answers a stale If-Match with 412 and an env-managed field with 409', async () => {
    const api = createControlApi({ settings: settingsEnvManaged as SettingsView });
    server.use(...api.handlers);
    const live = plane();
    await expect(live.patchSettings({ budgets: { llm_usd_day: 3 } }, '"s1"')).rejects.toMatchObject({ status: 412, code: 'stale_version' });
    await expect(live.patchSettings({ judge: { provider: 'openrouter' } }, '"s3"')).rejects.toMatchObject({
      status: 409,
      code: 'env_managed',
      details: { fields: ['judge.provider'] },
    });
  });

  it('sends confirm=true when the caller confirmed a D77 change', async () => {
    const api = createControlApi();
    server.use(...api.handlers);
    await plane().patchSettings({ bindings: { decision_model: null } }, '"s7"', { confirm: true });
    expect(api.callsTo('/api/settings', 'PATCH')[0].query.get('confirm')).toBe('true');
  });

  it('lists the dotted paths of a patch as env_managed names them', () => {
    expect(patchPaths({ decision_model: { backend: 'mock' }, bindings: { judge: null }, pricing_overrides: [] })).toEqual([
      'decision_model.backend',
      'bindings.judge',
      'pricing_overrides',
    ]);
  });

  it('describes every settings, credentials, cost and Studio error code', async () => {
    for (const [code, { status, hint }] of Object.entries(CONTROL_ERROR_CODES)) {
      expect(status, code).toBeGreaterThanOrEqual(400);
      expect(hint, code).not.toBe('');
    }
    const err = await unavailableControlPlane('x')
      .listApiKeys()
      .catch((e: unknown) => e);
    expect(errorCodeHint(err)).toBe(CONTROL_ERROR_CODES.not_implemented.hint);
    expect(errorCodeHint(new Error('x'))).toBeNull();
  });
});
