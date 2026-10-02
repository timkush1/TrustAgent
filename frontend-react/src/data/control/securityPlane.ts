/**
 * The security plane of the control plane (SPEC §2.4 #6-#7, #52-#60, #67-#70; P4-PLAN D29-D41,
 * D75-D81): settings, credentials, Connect OpenRouter, the decision-model test, API keys, the admin
 * audit log and WebSocket tickets.
 *
 * Step 0b wires the settings pair (#52 GET, #53 PATCH with If-Match) because WEB-ON and WEB-SV both
 * need it; every other method rejects with 404 `not_implemented` until WEB-ON lands its body.
 * The method signatures are fixed (P4-PLAN §0 rule 3); the bodies and SECURITY_SHAPES belong to WEB-ON.
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
import { normalizeShaped, type NestedShape } from '../normalize';
import type { PageParams } from '../source';
import { rejectNotImplemented } from './shared';

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
} as const satisfies Record<string, NestedShape>;

/** The security plane over HTTP. */
export function liveSecurityPlane(http: HttpClient): SecurityPlane {
  return {
    signOutEverywhere: rejectNotImplemented(),
    createWsTicket: rejectNotImplemented(),

    async getSettings(signal) {
      const raw = await http.get<unknown>('/api/settings', { signal });
      return normalizeShaped<SettingsView>(raw, 'GET /api/settings', SECURITY_SHAPES.settings);
    },
    async patchSettings(patch, etag, options) {
      const raw = await http.request<unknown>('/api/settings', {
        method: 'PATCH',
        headers: { 'If-Match': etag },
        query: options?.confirm ? { confirm: true } : undefined,
        body: patch,
      });
      return normalizeShaped<SettingsView>(raw, 'PATCH /api/settings', SECURITY_SHAPES.settings);
    },

    listCredentials: rejectNotImplemented(),
    putCredential: rejectNotImplemented(),
    deleteCredential: rejectNotImplemented(),
    verifyCredential: rejectNotImplemented(),
    startConnect: rejectNotImplemented(),
    completeConnect: rejectNotImplemented(),
    testDecisionModel: rejectNotImplemented(),
    listApiKeys: rejectNotImplemented(),
    createApiKey: rejectNotImplemented(),
    revokeApiKey: rejectNotImplemented(),
    listAdminAudit: rejectNotImplemented(),
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
