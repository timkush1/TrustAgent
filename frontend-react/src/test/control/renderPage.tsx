/**
 * Renders a control-plane page through the real data layer: LiveDataSource over msw for REST
 * (the control-plane handlers plus the trace/status handlers of apiHandlers.ts) and a fake socket
 * for WS v2, exactly as the app runs. Call `installMswServer()` in the test file and `stopLive()`
 * after each test.
 */
import type { SystemStatus } from '@/types/api';
import { createFakeApi, type FakeApi } from '../apiHandlers';
import { server } from '../msw';
import { renderLive } from '../renderLive';
import { systemStatus } from '../status';
import { createControlApi, type ControlApi } from './handlers';

export interface RenderPageOptions {
  control?: ControlApi;
  api?: FakeApi;
  /** Overrides for /api/status (a live, admin, writable, non-demo stack by default). */
  status?: Partial<SystemStatus> | null;
}

/** A healthy, writable, non-demo stack whose caller is an admin. */
export function adminStatus(overrides: Partial<SystemStatus> = {}): SystemStatus {
  const base = systemStatus({ demo: false, test_mode: false });
  return systemStatus({
    demo: false,
    test_mode: false,
    auth: { ...base.auth, read: 'keys', caller_scopes: ['read', 'content', 'admin'] },
    decision_model: { ...base.decision_model, backend: 'openrouter-decisions', state: 'ready', calibration_state: 'none' },
    judge: { ...base.judge, provider: 'openrouter', model: 'anthropic/claude-haiku-4.5' },
    ...overrides,
  });
}

export function renderPage(path: string, options: RenderPageOptions = {}) {
  const control = options.control ?? createControlApi();
  const status = options.status === null ? null : adminStatus(options.status ?? {});
  const api = options.api ?? createFakeApi({ status }, { controlPlane: false });
  server.use(...control.handlers);
  const app = renderLive(path, api);
  return { ...app, control, api };
}
