/**
 * msw handlers and state for the security plane (data/control/securityPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-ON after Step 0b.
 *
 * Step 0b serves the settings pair (#52 GET, #53 PATCH with If-Match), which WEB-ON and WEB-SV both
 * use; the other entries are seeds from the golden fixtures (contracts/fixtures/api) for WEB-ON to serve.
 */
import { http, type HttpHandler } from 'msw';
import apiKeyListFixture from '@contracts/fixtures/api/ApiKeyList.default.json';
import credentialListFixture from '@contracts/fixtures/api/CredentialList.default.json';
import adminAuditFixture from '@contracts/fixtures/api/Page-AdminAuditEntry.default.json';
import settingsAdminFixture from '@contracts/fixtures/api/SettingsView.admin.json';
import type {
  AdminAuditEntry,
  ApiKeyList,
  CredentialList,
  Page,
  RuntimeSettings,
  RuntimeSettingsPatch,
  SettingsView,
} from '@/types/api';
import { API } from '../msw';
import { errorReply, isError, reply, type HandlerKit, type Reply } from './kit';

export interface SecurityState {
  /** GET /api/settings (#52). */
  settings: Reply<SettingsView>;
  /** What PATCH /api/settings answers: 'echo' merges the patch into `settings` and bumps the version. */
  settingsPatch: Reply<SettingsView> | 'echo';
  /** Seeds for WEB-ON (not served yet). */
  credentials: Reply<CredentialList>;
  apiKeys: Reply<ApiKeyList>;
  adminAudit: Reply<Page<AdminAuditEntry>>;
}

/** Fresh copies of the golden fixtures, so a test that changes one never leaks into the next. */
export function settingsView(overrides: Partial<SettingsView> = {}): SettingsView {
  return { ...(structuredClone(settingsAdminFixture) as SettingsView), ...overrides };
}

export function defaultSecurityState(): SecurityState {
  return {
    settings: settingsView(),
    settingsPatch: 'echo',
    credentials: structuredClone(credentialListFixture) as CredentialList,
    apiKeys: structuredClone(apiKeyListFixture) as ApiKeyList,
    adminAudit: structuredClone(adminAuditFixture) as Page<AdminAuditEntry>,
  };
}

/** The dotted paths a patch sets (`decision_model.backend`, `pricing_overrides`, …), as env_managed lists them. */
export function patchPaths(patch: RuntimeSettingsPatch): string[] {
  const paths: string[] = [];
  const walk = (value: unknown, prefix: string) => {
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      for (const [k, v] of Object.entries(value)) walk(v, prefix ? `${prefix}.${k}` : k);
    } else if (prefix) {
      paths.push(prefix);
    }
  };
  walk(patch, '');
  return paths;
}

/** RuntimeSettings with a patch applied (nested objects merged, arrays replaced), as the gateway does. */
export function applyPatch(runtime: RuntimeSettings, patch: RuntimeSettingsPatch): RuntimeSettings {
  const merge = (base: unknown, change: unknown): unknown => {
    if (change === undefined) return base;
    if (change === null || typeof change !== 'object' || Array.isArray(change)) return change;
    const out: Record<string, unknown> = { ...(base as Record<string, unknown>) };
    for (const [k, v] of Object.entries(change)) out[k] = merge(out[k], v);
    return out;
  };
  return merge(runtime, patch) as RuntimeSettings;
}

export function securityHandlers(state: SecurityState, kit: HandlerKit): HttpHandler[] {
  return [
    http.get(`${API}/api/settings`, async ({ request }) => {
      await kit.record(request);
      const s = state.settings;
      return reply(s, s && !isError(s) ? { ETag: s.etag } : undefined);
    }),
    http.patch(`${API}/api/settings`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.settingsPatch !== 'echo') return reply(state.settingsPatch);
      const current = state.settings;
      if (current === undefined || isError(current)) return reply(current);
      const ifMatch = request.headers.get('if-match');
      if (!ifMatch) return reply(errorReply(428, 'precondition_required', 'Send If-Match with the settings version you edited.'));
      if (ifMatch !== current.etag) return reply(errorReply(412, 'stale_version', 'The settings changed since you loaded them.'));
      const patch = (body ?? {}) as RuntimeSettingsPatch;
      const locked = patchPaths(patch).filter((p) => current.env_managed.some((m) => p === m || p.startsWith(`${m}.`)));
      if (locked.length > 0) {
        return reply(errorReply(409, 'env_managed', 'These settings are set by environment variables.', undefined, { fields: locked, keys: [] }));
      }
      const version = current.version + 1;
      const next: SettingsView = { ...current, version, etag: `"s${version}"`, runtime: applyPatch(current.runtime, patch) };
      state.settings = next;
      return reply(next, { ETag: next.etag });
    }),
  ];
}
