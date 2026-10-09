/**
 * The security plane of the control plane (SPEC §2.4 #6-#7, #52-#60, #67-#70; D29-D41,
 * D75-D81): settings, credentials, Connect OpenRouter, the decision-model test, API keys, the admin
 * audit log and WebSocket tickets.
 *
 * Every response is checked against SECURITY_SHAPES before a page sees it, so a proxy that answers
 * something else fails with `invalid_response` instead of rendering half a card. Writes that may
 * take a provider round trip (verify, Connect, the decision-model probe) get a 30 s timeout.
 * The method signatures are fixed; the bodies and SECURITY_SHAPES belong to WEB-ON.
 */
import type {
  AdminAuditEntry,
  ApiKeyCreateRequest,
  ApiKeyList,
  ConnectCompleteRequest,
  ConnectCompleteResponse,
  ConnectStartRequest,
  ConnectStartResponse,
  CreatedApiKey,
  CredentialList,
  CredentialPutRequest,
  CredentialVerifyRequest,
  CredentialWriteResult,
  DecisionModelTestResult,
  Page,
  RuntimeSettingsPatch,
  SettingsView,
  WsTicket,
} from '@/types/api';
import type { HttpClient } from '../http';
import { normalizeShaped, normalizeShapedPage, type NestedShape } from '../normalize';
import type { PageParams } from '../source';
import { enc, rejectNotImplemented } from './shared';

/** D77: a write that would leave fail-closed routes without a decision model needs `?confirm=true`. */
export interface ConfirmOption {
  confirm?: boolean;
}

export interface SecurityPlane {
  /** DELETE /api/session?all=true (#6, admin): ends every dashboard session ("Sign out everywhere"). */
  signOutEverywhere(): Promise<void>;
  /** POST /api/ws-ticket (#7, D71): a one-use ticket for `?ticket=` before each cross-origin WebSocket (re)connect. */
  createWsTicket(signal?: AbortSignal): Promise<WsTicket>;

  /** GET /api/settings (#52): secrets masked; `etag` is the If-Match value for the next PATCH. */
  getSettings(signal?: AbortSignal): Promise<SettingsView>;
  /**
   * PATCH /api/settings (#53) with `If-Match: <etag>`. 412 `stale_version` when someone saved first,
   * 409 `env_managed` / `ingest_key_required` / `confirmation_required` (retry with `confirm`).
   */
  patchSettings(patch: RuntimeSettingsPatch, etag: string, options?: ConfirmOption): Promise<SettingsView>;

  /** GET /api/credentials (#54, admin). */
  listCredentials(signal?: AbortSignal): Promise<CredentialList>;
  /** PUT /api/credentials/:name (#55): store (and by default verify) a key; `bind` applies only when verified (D75). */
  putCredential(name: string, request: CredentialPutRequest): Promise<CredentialWriteResult>;
  /** DELETE /api/credentials/:name (#56): 204; 409 `confirmation_required` names the routes (D77). */
  deleteCredential(name: string, options?: ConfirmOption): Promise<void>;
  /** POST /api/credentials/:name/verify (#57): "Test" and "I added credits, check again" (with `bind`). */
  verifyCredential(name: string, request?: CredentialVerifyRequest): Promise<CredentialWriteResult>;
  /** POST /api/connect/openrouter/start (#58, D38): the browser then goes to `auth_url`. */
  startConnect(request: ConnectStartRequest): Promise<ConnectStartResponse>;
  /** POST /api/connect/openrouter/complete (#59): the `/connect/callback` page; navigate only to the echoed `return_to`. */
  completeConnect(request: ConnectCompleteRequest): Promise<ConnectCompleteResponse>;
  /** POST /api/decision-model/test (#60): one probe (≈ $0.00001); 429 carries Retry-After. */
  testDecisionModel(): Promise<DecisionModelTestResult>;

  /** GET /api/keys (#67, admin). */
  listApiKeys(signal?: AbortSignal): Promise<ApiKeyList>;
  /** POST /api/keys (#68): the secret is in the answer once and never again. */
  createApiKey(request: ApiKeyCreateRequest): Promise<CreatedApiKey>;
  /** DELETE /api/keys/:id (#69): 409 `conflict` with `details.reason: "last_admin_key"` (D81). */
  revokeApiKey(id: string): Promise<void>;

  /** GET /api/admin/audit-log (#70, admin): keyset pages, newest first. */
  listAdminAudit(params: PageParams, signal?: AbortSignal): Promise<Page<AdminAuditEntry>>;
}

/** A provider round trip (key check, OpenRouter code exchange, the probe) may take longer than a plain read. */
export const PROVIDER_TIMEOUT_MS = 30_000;

const credential: NestedShape = {
  strings: ['name', 'provider', 'source', 'masked', 'fingerprint'],
  lists: ['allowed_hosts', 'used_by'],
};
const credentialWrite: NestedShape = {
  records: ['credential', 'bindings'],
  inner: { credential, bindings: { lists: ['applied', 'skipped'] } },
};
const apiKey: NestedShape = { strings: ['id', 'label', 'prefix'], lists: ['scopes'] };

/** What each security response must carry (see normalizeShaped). */
export const SECURITY_SHAPES = {
  settings: {
    strings: ['etag'],
    records: ['runtime'],
    lists: ['groups', 'env_managed', 'warnings'],
    each: { groups: { strings: ['name'], lists: ['keys'] } },
    inner: {
      runtime: {
        records: ['decision_model', 'judge', 'upstream', 'bindings', 'budgets'],
        lists: ['pricing_overrides'],
      },
    },
  },
  credential,
  credentialList: { lists: ['items'], each: { items: credential } },
  credentialWrite,
  connectStart: { strings: ['auth_url', 'nonce'] },
  connectComplete: { ...credentialWrite, strings: ['return_to'] },
  decisionModelTest: { strings: ['backend'] },
  apiKey,
  apiKeyList: { lists: ['items'], each: { items: apiKey } },
  createdApiKey: { ...apiKey, strings: [...(apiKey.strings ?? []), 'secret'] },
  adminAuditEntry: { strings: ['at', 'actor', 'action'] },
  wsTicket: { strings: ['ticket'] },
} as const satisfies Record<string, NestedShape>;

/** The security plane over HTTP. */
export function liveSecurityPlane(http: HttpClient): SecurityPlane {
  const confirmQuery = (options?: ConfirmOption) => (options?.confirm ? { confirm: true } : undefined);
  return {
    async signOutEverywhere() {
      await http.request<unknown>('/api/session', { method: 'DELETE', query: { all: true } });
    },
    async createWsTicket(signal) {
      const raw = await http.request<unknown>('/api/ws-ticket', { method: 'POST', signal, timeoutMs: 5_000 });
      return normalizeShaped<WsTicket>(raw, 'POST /api/ws-ticket', SECURITY_SHAPES.wsTicket);
    },

    async getSettings(signal) {
      const raw = await http.get<unknown>('/api/settings', { signal });
      return normalizeShaped<SettingsView>(raw, 'GET /api/settings', SECURITY_SHAPES.settings);
    },
    async patchSettings(patch, etag, options) {
      const raw = await http.request<unknown>('/api/settings', {
        method: 'PATCH',
        headers: { 'If-Match': etag },
        query: confirmQuery(options),
        body: patch,
      });
      return normalizeShaped<SettingsView>(raw, 'PATCH /api/settings', SECURITY_SHAPES.settings);
    },

    async listCredentials(signal) {
      const raw = await http.get<unknown>('/api/credentials', { signal });
      return normalizeShaped<CredentialList>(raw, 'GET /api/credentials', SECURITY_SHAPES.credentialList);
    },
    async putCredential(name, request) {
      const raw = await http.request<unknown>(`/api/credentials/${enc(name)}`, {
        method: 'PUT',
        body: request,
        timeoutMs: PROVIDER_TIMEOUT_MS,
      });
      return normalizeShaped<CredentialWriteResult>(raw, 'PUT /api/credentials/:name', SECURITY_SHAPES.credentialWrite);
    },
    async deleteCredential(name, options) {
      await http.request<unknown>(`/api/credentials/${enc(name)}`, { method: 'DELETE', query: confirmQuery(options) });
    },
    async verifyCredential(name, request) {
      const raw = await http.request<unknown>(`/api/credentials/${enc(name)}/verify`, {
        method: 'POST',
        body: request ?? {},
        timeoutMs: PROVIDER_TIMEOUT_MS,
      });
      return normalizeShaped<CredentialWriteResult>(raw, 'POST /api/credentials/:name/verify', SECURITY_SHAPES.credentialWrite);
    },
    async startConnect(request) {
      const raw = await http.request<unknown>('/api/connect/openrouter/start', { method: 'POST', body: request });
      return normalizeShaped<ConnectStartResponse>(raw, 'POST /api/connect/openrouter/start', SECURITY_SHAPES.connectStart);
    },
    async completeConnect(request) {
      const raw = await http.request<unknown>('/api/connect/openrouter/complete', {
        method: 'POST',
        body: request,
        timeoutMs: PROVIDER_TIMEOUT_MS,
      });
      return normalizeShaped<ConnectCompleteResponse>(raw, 'POST /api/connect/openrouter/complete', SECURITY_SHAPES.connectComplete);
    },
    async testDecisionModel() {
      const raw = await http.request<unknown>('/api/decision-model/test', { method: 'POST', timeoutMs: PROVIDER_TIMEOUT_MS });
      return normalizeShaped<DecisionModelTestResult>(raw, 'POST /api/decision-model/test', SECURITY_SHAPES.decisionModelTest);
    },

    async listApiKeys(signal) {
      const raw = await http.get<unknown>('/api/keys', { signal });
      return normalizeShaped<ApiKeyList>(raw, 'GET /api/keys', SECURITY_SHAPES.apiKeyList);
    },
    async createApiKey(request) {
      const raw = await http.request<unknown>('/api/keys', { method: 'POST', body: request });
      return normalizeShaped<CreatedApiKey>(raw, 'POST /api/keys', SECURITY_SHAPES.createdApiKey);
    },
    async revokeApiKey(id) {
      await http.request<unknown>(`/api/keys/${enc(id)}`, { method: 'DELETE' });
    },

    async listAdminAudit(params, signal) {
      const raw = await http.get<unknown>('/api/admin/audit-log', { query: { limit: params.limit, cursor: params.cursor }, signal });
      return normalizeShapedPage<AdminAuditEntry>(raw, 'GET /api/admin/audit-log', SECURITY_SHAPES.adminAuditEntry);
    },
  };
}

/** A security plane that serves nothing (the static build, tests): every call rejects with 404 `not_implemented`. */
export function unavailableSecurityPlane(message?: string): SecurityPlane {
  const reject = rejectNotImplemented(message);
  return {
    signOutEverywhere: reject,
    createWsTicket: reject,
    getSettings: reject,
    patchSettings: reject,
    listCredentials: reject,
    putCredential: reject,
    deleteCredential: reject,
    verifyCredential: reject,
    startConnect: reject,
    completeConnect: reject,
    testDecisionModel: reject,
    listApiKeys: reject,
    createApiKey: reject,
    revokeApiKey: reject,
    listAdminAudit: reject,
  };
}
