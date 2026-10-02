/**
 * msw handlers and state for the security plane (data/control/securityPlane.ts), spread into
 * createControlApi (handlers.ts). Owned by WEB-ON after Step 0b.
 *
 * Served from the golden fixtures (contracts/fixtures/api) and kept consistent the way the gateway
 * keeps itself consistent (P4-PLAN D29-D41, D75-D81):
 * - settings: GET, and PATCH with If-Match (428/412), env-managed paths (409) and the version bump;
 * - credentials: a key write runs a fake key check from the secret's text ("nocredits" → 402,
 *   "unauthorized" → 401, "unreachable" → network), stores per D75 (a failed check on a bound name
 *   stores nothing), binds only on success, skips env-managed targets, and leaves simulated mode (D76);
 * - DELETE asks for `?confirm=true` while enforcing routes would lose their fast check (D77);
 * - keys: create (secret once), revoke, and the last-admin-key refusal (D81);
 * - sessions and the setup status, Connect OpenRouter, the decision-model test and WS tickets.
 * Any entry can be replaced by a fixed reply (an error, say) through the state.
 */
import { http, HttpResponse, type HttpHandler } from 'msw';
import apiKeyListFixture from '@contracts/fixtures/api/ApiKeyList.default.json';
import connectCompleteFixture from '@contracts/fixtures/api/ConnectCompleteResponse.ok.json';
import connectStartFixture from '@contracts/fixtures/api/ConnectStartResponse.default.json';
import credentialListFixture from '@contracts/fixtures/api/CredentialList.default.json';
import decisionModelTestFixture from '@contracts/fixtures/api/DecisionModelTestResult.ok.json';
import adminAuditFixture from '@contracts/fixtures/api/Page-AdminAuditEntry.default.json';
import settingsAdminFixture from '@contracts/fixtures/api/SettingsView.admin.json';
import wsTicketFixture from '@contracts/fixtures/api/WsTicket.default.json';
import type {
  AdminAuditEntry,
  ApiKeyCreateRequest,
  ApiKeyList,
  ApiKeyView,
  BindingTarget,
  ConnectCompleteRequest,
  ConnectCompleteResponse,
  ConnectStartResponse,
  CreatedApiKey,
  CredentialList,
  CredentialPutRequest,
  CredentialVerification,
  CredentialVerifyStatus,
  CredentialView,
  CredentialWriteResult,
  DecisionModelTestResult,
  Page,
  RuntimeSettings,
  RuntimeSettingsPatch,
  SessionInfo,
  SettingsView,
  SetupStatus,
  WsTicket,
} from '@/types/api';
import { API } from '../msw';
import { errorReply, isError, reply, type HandlerKit, type Reply } from './kit';

/** A reply, or a plain 204 for `null`. */
type EmptyReply = Reply<null>;

export interface SecurityState {
  /** GET /api/settings (#52). */
  settings: Reply<SettingsView>;
  /** What PATCH /api/settings answers: 'echo' merges the patch into `settings` and bumps the version. */
  settingsPatch: Reply<SettingsView> | 'echo';
  /** GET /api/credentials (#54). */
  credentials: Reply<CredentialList>;
  /** PUT /api/credentials/:name (#55): 'verify' runs the fake key check and the D75 write. */
  credentialPut: Reply<CredentialWriteResult> | 'verify';
  /** POST /api/credentials/:name/verify (#57): 'verify' checks the stored key again. */
  credentialVerify: Reply<CredentialWriteResult> | 'verify';
  /** DELETE /api/credentials/:name (#56): 'echo' removes it (with the D77 confirmation). */
  credentialDelete: EmptyReply | 'echo';
  /** The outcome of the next check per credential name; otherwise the stored secret decides. */
  verifyStatus: Partial<Record<string, CredentialVerifyStatus>>;
  /** OpenRouter GET /api/v1/key: the key's spending limit (null = no limit, D87). */
  keyLimitUsd: number | null;
  /** Credentials store state (D32): writes answer 503 unless `ready`. */
  store: 'ready' | 'unavailable' | 'no_database';
  /** Enforcing fail-closed routes (D77): removing the bound fast-check key needs `?confirm=true`. */
  enforcingRoutes: string[];
  /** POST /api/connect/openrouter/start (#58). */
  connectStart: Reply<ConnectStartResponse>;
  /** POST /api/connect/openrouter/complete (#59); a success applies its bindings to `settings`. */
  connectComplete: Reply<ConnectCompleteResponse>;
  /** POST /api/decision-model/test (#60). */
  decisionModelTest: Reply<DecisionModelTestResult>;
  /** GET /api/keys (#67). */
  apiKeys: Reply<ApiKeyList>;
  /** POST /api/keys (#68): 'echo' adds the key and answers its secret once. */
  apiKeyCreate: Reply<CreatedApiKey> | 'echo';
  /** DELETE /api/keys/:id (#69): 'echo' revokes it, refusing the last admin key (D81). */
  apiKeyRevoke: EmptyReply | 'echo';
  /** TRUTHTABLE_ADMIN_KEYS is set, so revoking the last dashboard admin key is allowed (D81). */
  envAdmin: boolean;
  /** GET /api/admin/audit-log (#70). */
  adminAudit: Reply<Page<AdminAuditEntry>>;
  /** POST /api/ws-ticket (#7). */
  wsTicket: Reply<WsTicket>;
  /** GET /api/setup/status (#3). */
  setupStatus: Reply<SetupStatus>;
  /** The cookie session (GET /api/session); null answers 401. */
  session: SessionInfo | null;
  /** Keys a cross-origin tab may send in X-TrustAgent-Key, and who they are (GET /api/session). */
  keySessions: Record<string, SessionInfo>;
  /** POST /api/session: 'echo' signs in (setup token or key); a fixed reply otherwise. */
  signIn: Reply<SessionInfo> | 'echo';
  /** Whether the browser keeps the session cookie after a sign-in (false: a cross-origin proxy). */
  keepsCookie: boolean;
}

/** An admin session, as the setup claim or an admin key sign-in creates. */
export function adminSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    actor: 'setup-admin',
    scopes: ['ingest', 'read', 'content', 'admin'],
    expires_at: '2026-10-28T12:00:00Z',
    sliding: true,
    ...overrides,
  };
}

/** The proxy's GET /api/setup/status. */
export function setupStatus(overrides: Partial<SetupStatus> = {}): SetupStatus {
  return {
    setup_required: false,
    ingest_auth: 'keys',
    demo: false,
    readonly: false,
    persistence: true,
    public_url: 'http://localhost:3000',
    recovery_command: 'docker compose exec -T proxy /proxy -new-setup-token',
    ...overrides,
  };
}

/** The admin key a setup claim creates in these handlers. */
export const CLAIMED_ADMIN_KEY = 'ta_claimed0admin0key0for0tests00000000000000';

/** Fresh copies of the golden fixtures, so a test that changes one never leaks into the next. */
export function settingsView(overrides: Partial<SettingsView> = {}): SettingsView {
  return { ...(structuredClone(settingsAdminFixture) as SettingsView), ...overrides };
}

export function credentialList(): CredentialList {
  return structuredClone(credentialListFixture) as CredentialList;
}

export function defaultSecurityState(): SecurityState {
  return {
    settings: settingsView(),
    settingsPatch: 'echo',
    credentials: credentialList(),
    credentialPut: 'verify',
    credentialVerify: 'verify',
    credentialDelete: 'echo',
    verifyStatus: {},
    keyLimitUsd: 2,
    store: 'ready',
    enforcingRoutes: [],
    connectStart: structuredClone(connectStartFixture) as ConnectStartResponse,
    connectComplete: structuredClone(connectCompleteFixture) as ConnectCompleteResponse,
    decisionModelTest: structuredClone(decisionModelTestFixture) as DecisionModelTestResult,
    apiKeys: structuredClone(apiKeyListFixture) as ApiKeyList,
    apiKeyCreate: 'echo',
    apiKeyRevoke: 'echo',
    envAdmin: false,
    adminAudit: structuredClone(adminAuditFixture) as Page<AdminAuditEntry>,
    wsTicket: structuredClone(wsTicketFixture) as WsTicket,
    setupStatus: setupStatus(),
    session: adminSession(),
    keySessions: {},
    signIn: 'echo',
    keepsCookie: true,
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

/** `sk-…3f2a`, as config.MaskSecret prints a secret (D79). */
export function maskSecret(secret: string): string {
  return `${secret.slice(0, 3)}…${secret.slice(-4)}`;
}

/** A stable fake fingerprint (the gateway's is an HMAC, D30). */
function fingerprintOf(secret: string): string {
  let h = 0x811c9dc5;
  for (const ch of secret) h = Math.imul(h ^ ch.charCodeAt(0), 0x01000193) >>> 0;
  return `fp_${h.toString(16).padStart(8, '0')}${(h >>> 16).toString(16).padStart(4, '0')}`.slice(0, 15);
}

/** The fake provider's answer for a secret: its text decides, like mock-upstream's test keys. */
function statusOfSecret(secret: string): CredentialVerifyStatus {
  if (secret.includes('nocredits')) return 'no_credits';
  if (secret.includes('unauthorized')) return 'unauthorized';
  if (secret.includes('unreachable')) return 'unreachable';
  return 'ok';
}

const VENDOR_ORIGINS: Record<string, string> = {
  openrouter: 'https://openrouter.ai',
  typesafe: 'https://api.typesafe.ai',
  anthropic: 'https://api.anthropic.com',
  openai: 'https://api.openai.com',
};

function originOf(url: string | undefined): string[] {
  if (!url) return [];
  try {
    return [new URL(url).origin];
  } catch {
    return [];
  }
}

export function securityHandlers(state: SecurityState, kit: HandlerKit): HttpHandler[] {
  /** What the browser stored per credential name (the gateway keeps it encrypted). */
  const secrets: Record<string, string> = {};
  /** The nonce Connect OpenRouter is waiting for: the seeded start's, as if it began on an earlier page. */
  let pendingNonce: string | null = state.connectStart && !isError(state.connectStart) ? state.connectStart.nonce : null;

  const currentSettings = (): SettingsView | null => (state.settings && !isError(state.settings) ? state.settings : null);
  const currentCredentials = (): CredentialList | null =>
    state.credentials && !isError(state.credentials) ? state.credentials : null;

  const bump = (settings: SettingsView, runtime: RuntimeSettings): SettingsView => {
    const version = settings.version + 1;
    const next = { ...settings, version, etag: `"s${version}"`, runtime };
    state.settings = next;
    return next;
  };

  /** `used_by` of every credential from the settings' bindings and upstream credentials. */
  const syncUsedBy = () => {
    const settings = currentSettings();
    const creds = currentCredentials();
    if (!settings || !creds) return;
    const { bindings, upstream } = settings.runtime;
    for (const c of creds.items) {
      const used: CredentialView['used_by'] = [];
      if (bindings.decision_model === c.name) used.push('decision_model');
      if (bindings.judge === c.name) used.push('judge');
      if (bindings.upstream_default === c.name || upstream.default.credential === c.name) used.push('upstream_default');
      if (upstream.anthropic.credential === c.name) used.push('upstream_anthropic');
      if (upstream.fallback.credential === c.name) used.push('upstream_fallback');
      c.used_by = used;
    }
  };

  /** Applies bindings for `name` (D75), leaving simulated mode when one applies (D76). */
  const bind = (name: string, targets: readonly BindingTarget[], ok: boolean): CredentialWriteResult['bindings'] => {
    const settings = currentSettings();
    const applied: BindingTarget[] = [];
    const skipped: CredentialWriteResult['bindings']['skipped'] = [];
    for (const target of targets) {
      if (settings?.env_managed.includes(`bindings.${target}`)) skipped.push({ target, reason: 'env_managed' });
      else if (!ok) skipped.push({ target, reason: 'verification_failed' });
      else applied.push(target);
    }
    if (settings && applied.length > 0) {
      const runtime = structuredClone(settings.runtime);
      for (const target of applied) runtime.bindings[target] = name;
      if (runtime.decision_model.backend === 'mock') runtime.decision_model.backend = 'auto';
      if (runtime.judge.provider === 'mock') runtime.judge.provider = 'auto';
      bump(settings, runtime);
    }
    syncUsedBy();
    return { applied, skipped };
  };

  const verification = (name: string, status: CredentialVerifyStatus): CredentialVerification => {
    const ok = status === 'ok' || status === 'unknown';
    const openrouter = name === 'openrouter';
    return {
      ok,
      status,
      hint: status === 'no_credits' ? 'no_credits' : null,
      latency_ms: status === 'unreachable' || status === 'unknown' ? null : 231,
      cost_usd: ok && status === 'ok' ? 0.00001 : null,
      cost_source: ok && status === 'ok' ? 'reported' : null,
      // As the proxy serializes it (Go `omitempty` on pointers): a key with no limit has no limit fields.
      ...(openrouter ? { key_label: 'TrustAgent dev' } : {}),
      ...(openrouter && state.keyLimitUsd !== null ? { limit_usd: state.keyLimitUsd, limit_remaining_usd: state.keyLimitUsd } : {}),
    };
  };

  const isBound = (name: string): boolean => {
    const b = currentSettings()?.runtime.bindings;
    return b ? b.decision_model === name || b.judge === name || b.upstream_default === name : false;
  };

  const storeRefusal = () => {
    if (state.store === 'no_database') return errorReply(503, 'persistence_disabled', 'Keys cannot be saved without a database.');
    if (state.store === 'unavailable') return errorReply(503, 'credentials_unavailable', 'The credential store is not available.');
    return null;
  };

  const envRow = (name: string) => currentCredentials()?.items.find((c) => c.name === name && c.source === 'env');
  const envManaged = (name: string) =>
    errorReply(409, 'env_managed', `${name} is set by an environment variable.`, undefined, { fields: [], keys: [name] });

  /** The D75 write: verify, store (unless a failed check would replace a bound key), bind. */
  const write = (name: string, secret: string | null, request: { verify?: boolean; bind?: BindingTarget[]; base_url?: string; provider?: string }) => {
    const creds = currentCredentials();
    const existing = creds?.items.find((c) => c.name === name) ?? null;
    const verify = request.verify ?? true;
    const targets = request.bind ?? [];
    if (targets.length > 0 && !verify) {
      return errorReply(422, 'validation_failed', 'bind needs verify.', undefined, { fields: ['bind'] });
    }
    const key = secret ?? secrets[name] ?? 'sk-or-v1-e2e-ok-0000';
    const upstream = name.startsWith('upstream-');
    const status: CredentialVerifyStatus | null = !verify ? null : upstream ? 'unknown' : (state.verifyStatus[name] ?? statusOfSecret(key));
    const check = status === null ? null : verification(name, status);
    const ok = check?.ok ?? false;
    const now = new Date().toISOString();
    let stored = true;
    let view: CredentialView;
    if (secret !== null && !ok && check && existing && isBound(name)) {
      stored = false;
      view = existing;
    } else {
      if (secret !== null) secrets[name] = secret;
      view = {
        name,
        provider: (request.provider as CredentialView['provider']) ?? existing?.provider ?? 'generic',
        source: existing?.source ?? 'dashboard',
        masked: secret !== null ? maskSecret(secret) : (existing?.masked ?? maskSecret(key)),
        fingerprint: secret !== null ? fingerprintOf(secret) : (existing?.fingerprint ?? fingerprintOf(key)),
        allowed_hosts: VENDOR_ORIGINS[name] ? [VENDOR_ORIGINS[name]] : request.base_url ? originOf(request.base_url) : (existing?.allowed_hosts ?? []),
        created_at: existing?.created_at ?? now,
        rotated_at: existing && secret !== null ? now : (existing?.rotated_at ?? null),
        last_verified_at: check ? now : (existing?.last_verified_at ?? null),
        last_verify_status: status ?? existing?.last_verify_status ?? null,
        last_used_at: existing?.last_used_at ?? null,
        used_by: existing?.used_by ?? [],
        undecryptable: false,
      };
      if (creds && view.source === 'dashboard') {
        creds.items = existing ? creds.items.map((c) => (c.name === name ? view : c)) : [...creds.items, view];
      }
    }
    const bindings = bind(name, targets, ok);
    const fresh = currentCredentials()?.items.find((c) => c.name === name) ?? view;
    return { credential: fresh, stored, bindings, verification: check } satisfies CredentialWriteResult;
  };

  return [
    // ------------------------------------------------------------------- settings (#52, #53)
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
      const confirm = new URL(request.url).searchParams.get('confirm') === 'true';
      const turnsOff = patch.decision_model?.backend === 'off' || patch.bindings?.decision_model === null;
      if (turnsOff && state.enforcingRoutes.length > 0 && !confirm) {
        return reply(
          errorReply(409, 'confirmation_required', 'Turning the fast check off blocks every answer on these routes.', undefined, {
            routes: state.enforcingRoutes,
          }),
        );
      }
      const runtime = applyPatch(current.runtime, patch);
      if (patch.bindings?.decision_model || patch.bindings?.judge) {
        // D76: a binding leaves simulated mode.
        if (runtime.decision_model.backend === 'mock') runtime.decision_model.backend = 'auto';
        if (runtime.judge.provider === 'mock') runtime.judge.provider = 'auto';
      }
      const next = bump(current, runtime);
      syncUsedBy();
      return reply(next, { ETag: next.etag });
    }),

    // ------------------------------------------------------------- credentials (#54-#57)
    http.get(`${API}/api/credentials`, async ({ request }) => {
      await kit.record(request);
      return reply(state.credentials);
    }),
    http.put(`${API}/api/credentials/:name`, async ({ request, params }) => {
      const { body } = await kit.record(request);
      if (state.credentialPut !== 'verify') return reply(state.credentialPut);
      const name = String(params.name);
      if (envRow(name)) return reply(envManaged(name));
      const refusal = storeRefusal();
      if (refusal) return reply(refusal);
      const req = (body ?? {}) as CredentialPutRequest;
      if (!req.secret) return reply(errorReply(422, 'validation_failed', 'secret is required.'));
      return reply(write(name, req.secret, req));
    }),
    http.post(`${API}/api/credentials/:name/verify`, async ({ request, params }) => {
      const { body } = await kit.record(request);
      if (state.credentialVerify !== 'verify') return reply(state.credentialVerify);
      const name = String(params.name);
      const req = (body ?? {}) as { bind?: BindingTarget[] };
      const env = envRow(name);
      if (env) {
        if ((req.bind ?? []).length > 0) return reply(envManaged(name));
        const status = state.verifyStatus[name] ?? 'ok';
        return reply({ credential: { ...env, last_verify_status: status }, stored: false, bindings: { applied: [], skipped: [] }, verification: verification(name, status) });
      }
      if (!currentCredentials()?.items.some((c) => c.name === name)) return reply(errorReply(404, 'not_found', `No saved key named ${name}.`));
      return reply(write(name, null, { bind: req.bind }));
    }),
    http.delete(`${API}/api/credentials/:name`, async ({ request, params }) => {
      await kit.record(request);
      if (state.credentialDelete !== 'echo') return state.credentialDelete === null ? new HttpResponse(null, { status: 204 }) : reply(state.credentialDelete);
      const name = String(params.name);
      if (envRow(name)) return reply(envManaged(name));
      const confirm = new URL(request.url).searchParams.get('confirm') === 'true';
      const settings = currentSettings();
      if (settings?.runtime.bindings.decision_model === name && state.enforcingRoutes.length > 0 && !confirm) {
        return reply(
          errorReply(409, 'confirmation_required', 'Removing this key turns the fast check off.', undefined, { routes: state.enforcingRoutes }),
        );
      }
      const creds = currentCredentials();
      if (creds) creds.items = creds.items.filter((c) => c.name !== name);
      delete secrets[name];
      if (settings) {
        const runtime = structuredClone(settings.runtime);
        const b = runtime.bindings;
        if (b.decision_model === name) b.decision_model = null;
        if (b.judge === name) b.judge = null;
        if (b.upstream_default === name) b.upstream_default = null;
        for (const u of [runtime.upstream.default, runtime.upstream.anthropic, runtime.upstream.fallback]) {
          if (u.credential === name) u.credential = null;
        }
        bump(settings, runtime);
      }
      syncUsedBy();
      return new HttpResponse(null, { status: 204 });
    }),

    // --------------------------------------------------------------- Connect OpenRouter (#58, #59)
    http.post(`${API}/api/connect/openrouter/start`, async ({ request }) => {
      await kit.record(request);
      if (state.connectStart && !isError(state.connectStart)) pendingNonce = state.connectStart.nonce;
      return reply(state.connectStart);
    }),
    http.post(`${API}/api/connect/openrouter/complete`, async ({ request }) => {
      const { body } = await kit.record(request);
      const result = state.connectComplete;
      const req = (body ?? {}) as Partial<ConnectCompleteRequest>;
      // A known nonce is used up on its first call, whatever the outcome (D38).
      if (pendingNonce === null || req.nonce !== pendingNonce) {
        return reply(errorReply(400, 'invalid_nonce', 'This sign-in is unknown, expired or already used.'));
      }
      pendingNonce = null;
      if (result && !isError(result)) {
        const settings = currentSettings();
        if (settings && result.bindings.applied.length > 0) {
          const runtime = structuredClone(settings.runtime);
          for (const target of result.bindings.applied) runtime.bindings[target] = result.credential.name;
          if (runtime.decision_model.backend === 'mock') runtime.decision_model.backend = 'auto';
          if (runtime.judge.provider === 'mock') runtime.judge.provider = 'auto';
          bump(settings, runtime);
        }
        const creds = currentCredentials();
        if (creds) {
          const others = creds.items.filter((c) => c.name !== result.credential.name);
          creds.items = [result.credential, ...others];
        }
        syncUsedBy();
      }
      return reply(result);
    }),
    http.post(`${API}/api/decision-model/test`, async ({ request }) => {
      await kit.record(request);
      return reply(state.decisionModelTest);
    }),

    // ------------------------------------------------------------------- API keys (#67-#69)
    http.get(`${API}/api/keys`, async ({ request }) => {
      await kit.record(request);
      return reply(state.apiKeys);
    }),
    http.post(`${API}/api/keys`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.apiKeyCreate !== 'echo') return reply(state.apiKeyCreate);
      const keys = state.apiKeys && !isError(state.apiKeys) ? state.apiKeys : null;
      const req = (body ?? {}) as ApiKeyCreateRequest;
      const n = (keys?.items.length ?? 0) + 1;
      const prefix = `ta_new${String(n).padStart(3, '0')}`;
      const view: ApiKeyView = {
        id: `k_new${n}`,
        label: req.label,
        scopes: req.scopes,
        prefix,
        created_at: new Date().toISOString(),
        last_used_at: null,
        revoked_at: null,
      };
      if (keys) keys.items = [view, ...keys.items];
      return reply({ ...view, secret: `${prefix}0secret0shown0once0000000000000` });
    }),
    http.delete(`${API}/api/keys/:id`, async ({ request, params }) => {
      await kit.record(request);
      if (state.apiKeyRevoke !== 'echo') return state.apiKeyRevoke === null ? new HttpResponse(null, { status: 204 }) : reply(state.apiKeyRevoke);
      const keys = state.apiKeys && !isError(state.apiKeys) ? state.apiKeys : null;
      const key = keys?.items.find((k) => k.id === params.id);
      if (!keys || !key) return reply(errorReply(404, 'not_found', 'No such key.'));
      const otherAdmins = keys.items.filter((k) => k.id !== key.id && k.revoked_at === null && k.scopes.includes('admin'));
      if (key.scopes.includes('admin') && otherAdmins.length === 0 && !state.envAdmin) {
        return reply(
          errorReply(409, 'conflict', 'This is the last admin key; revoking it would lock everyone out.', undefined, { reason: 'last_admin_key' }),
        );
      }
      key.revoked_at = new Date().toISOString();
      return new HttpResponse(null, { status: 204 });
    }),

    // ------------------------------------------------------------------- audit log (#70)
    http.get(`${API}/api/admin/audit-log`, async ({ request }) => {
      await kit.record(request);
      return reply(state.adminAudit);
    }),

    // ------------------------------------------------- sessions, setup and WS tickets (#3-#7)
    http.get(`${API}/api/setup/status`, async ({ request }) => {
      await kit.record(request);
      return reply(state.setupStatus);
    }),
    http.get(`${API}/api/session`, async ({ request }) => {
      await kit.record(request);
      const key = request.headers.get('x-trustagent-key');
      const who = key ? state.keySessions[key] : state.session;
      return who ? reply(who) : reply(errorReply(401, 'unauthorized', key ? 'The key is not valid.' : 'Not signed in.'));
    }),
    http.post(`${API}/api/session`, async ({ request }) => {
      const { body } = await kit.record(request);
      if (state.signIn !== 'echo') return reply(state.signIn);
      const req = (body ?? {}) as { setup_token?: string; key?: string };
      const session = adminSession(req.key ? { actor: 'admin' } : {});
      if (state.keepsCookie) state.session = session;
      if (req.setup_token) {
        state.keySessions[CLAIMED_ADMIN_KEY] = { ...session, sliding: false };
        if (state.setupStatus && !isError(state.setupStatus)) state.setupStatus = { ...state.setupStatus, setup_required: false };
        return reply({ ...session, created_admin_key: CLAIMED_ADMIN_KEY });
      }
      return reply(session);
    }),
    http.delete(`${API}/api/session`, async ({ request }) => {
      await kit.record(request);
      state.session = null;
      return new HttpResponse(null, { status: 204 });
    }),
    http.post(`${API}/api/ws-ticket`, async ({ request }) => {
      await kit.record(request);
      return reply(state.wsTicket);
    }),
  ];
}
