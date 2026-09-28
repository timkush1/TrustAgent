/**
 * The control-plane half of the DataSource (SPEC §2.4): the REST endpoints behind Overview,
 * Policies, Playground, Evaluators and Knowledge. Traces, decisions and audits (the stream-backed
 * lists) stay on DataSource itself (source.ts); this interface adds everything the P3 pages read
 * and the few writes they make.
 *
 * LiveDataSource implements it over HTTP (live/liveControlPlane.ts). A source that cannot serve an
 * endpoint (the static replay build hides Policies and Knowledge) spreads `unavailableControlPlane`
 * so every call fails with a plain ApiError the page contract already renders.
 */
import type {
  ChatMessage,
  EvaluatorDetail,
  EvaluatorMeta,
  EvaluatorVersion,
  EvidenceCoverage,
  EvidenceSuggestion,
  GateDecisionView,
  IngestResult,
  KBClaim,
  KBStats,
  ConflictPair,
  OverviewStats,
  Page,
  PlaygroundRequest,
  PlaygroundResult,
  PolicyDoc,
  PolicyDryRunResult,
  PolicyState,
  PolicyVersion,
  PolicyVersionMeta,
  ReliabilityResponse,
  RoiSummary,
  TimeWindow,
  ValidateResult,
} from '@/types/api';
import { ApiError } from './errors';
import type { PageParams } from './source';

export type { ChatMessage };

/** GET /api/overview (#9). `include_mock` defaults to the demo flag on the server. */
export interface OverviewParams {
  window: TimeWindow;
  include_mock?: boolean;
  route?: string;
}

/** GET /api/evidence/coverage (#50) and /api/evidence/suggestions (#51). */
export interface EvidenceParams {
  window: TimeWindow;
  route?: string;
}

/** A policy given as YAML text or as a parsed document (PUT, validate and dry-run accept either). */
export type PolicyInput = { yaml: string; doc?: never } | { doc: PolicyDoc; yaml?: never };

/** POST /api/policies/dry-run (#26): "Preview impact (free)". */
export type PolicyDryRunRequest = (PolicyInput | { version: number; yaml?: never; doc?: never }) & {
  window: TimeWindow;
  /** At most 100,000 stored answers. */
  limit?: number;
};

/** PUT /api/policies (#23), sent with `If-Match: <etag>`. */
export type PolicySaveRequest = PolicyInput & { message: string };

/** GET /api/gate/reliability (#17). */
export interface ReliabilityParams {
  /** "<id>@<version>" or a bare id. */
  evaluator: string;
  snapshot?: string;
  window?: TimeWindow;
  bins?: number;
  labels?: 'human' | 'uniform_audit' | 'all';
}

/** GET /api/kb/claims (#62): limit/offset paging with a total (SPEC §2.4 "Pagination"). */
export interface KbClaimsParams {
  limit?: number;
  offset?: number;
  status?: 'accepted' | 'quarantined';
}

export interface KbClaimsPage {
  claims: KBClaim[];
  total: number;
  limit: number;
  offset: number;
}

export interface KbConflictsPage {
  conflicts: ConflictPair[];
  total: number;
}

/** Events of a streamed Playground run (`stream: true`): SSE `token`, `decision`, `done`, `error`. */
export interface PlaygroundStreamHandlers {
  /** A piece of the upstream answer as it streams. */
  onToken?: (text: string) => void;
  /** The fast check's decision (it can arrive before the stream ends). */
  onDecision?: (decision: GateDecisionView) => void;
  /** A `: trustagent {...}` comment from the gateway (informational). */
  onComment?: (text: string) => void;
}

export interface ControlPlane {
  getOverview(params: OverviewParams, signal?: AbortSignal): Promise<OverviewStats>;
  getRoiSummary(window: TimeWindow, signal?: AbortSignal): Promise<RoiSummary>;
  getEvidenceCoverage(params: EvidenceParams, signal?: AbortSignal): Promise<EvidenceCoverage>;
  getEvidenceSuggestions(params: EvidenceParams, signal?: AbortSignal): Promise<{ items: EvidenceSuggestion[] }>;

  getPolicies(signal?: AbortSignal): Promise<PolicyState>;
  listPolicyVersions(params: PageParams, signal?: AbortSignal): Promise<Page<PolicyVersionMeta>>;
  getPolicyVersion(id: number, signal?: AbortSignal): Promise<PolicyVersion>;
  validatePolicy(input: PolicyInput, signal?: AbortSignal): Promise<ValidateResult>;
  dryRunPolicy(request: PolicyDryRunRequest, signal?: AbortSignal): Promise<PolicyDryRunResult>;
  savePolicy(request: PolicySaveRequest, etag: string): Promise<PolicyState>;
  rollbackPolicy(request: { version: number; message?: string }): Promise<PolicyState>;
  adoptPolicyFile(request: { message?: string }): Promise<PolicyState>;

  listEvaluators(params: { include_archived?: boolean }, signal?: AbortSignal): Promise<{ items: EvaluatorMeta[] }>;
  getEvaluator(id: string, signal?: AbortSignal): Promise<EvaluatorDetail>;
  getEvaluatorVersion(id: string, version: number, signal?: AbortSignal): Promise<EvaluatorVersion>;
  getReliability(params: ReliabilityParams, signal?: AbortSignal): Promise<ReliabilityResponse>;

  /** POST /api/playground/run without streaming. */
  runPlayground(request: PlaygroundRequest, signal?: AbortSignal): Promise<PlaygroundResult>;
  /** POST /api/playground/run with `stream: true`; resolves with the `done` result. */
  streamPlayground(request: PlaygroundRequest, handlers: PlaygroundStreamHandlers, signal?: AbortSignal): Promise<PlaygroundResult>;

  getKbStats(signal?: AbortSignal): Promise<KBStats>;
  listKbClaims(params: KbClaimsParams, signal?: AbortSignal): Promise<KbClaimsPage>;
  listKbConflicts(params: { limit?: number }, signal?: AbortSignal): Promise<KbConflictsPage>;
  /** POST /api/kb/documents: a JSON file of `[{content, metadata}]` as multipart field `file`. */
  uploadKbDocuments(file: File, signal?: AbortSignal): Promise<IngestResult>;
}

/**
 * A ControlPlane that serves nothing: every call rejects with a 404 ApiError carrying `message`.
 * For DataSources without a control plane (the static replay build), and for tests.
 */
export function unavailableControlPlane(message = 'Not available in this build.'): ControlPlane {
  const reject = () => Promise.reject(new ApiError({ kind: 'http', status: 404, code: 'not_found', message }));
  return {
    getOverview: reject,
    getRoiSummary: reject,
    getEvidenceCoverage: reject,
    getEvidenceSuggestions: reject,
    getPolicies: reject,
    listPolicyVersions: reject,
    getPolicyVersion: reject,
    validatePolicy: reject,
    dryRunPolicy: reject,
    savePolicy: reject,
    rollbackPolicy: reject,
    adoptPolicyFile: reject,
    listEvaluators: reject,
    getEvaluator: reject,
    getEvaluatorVersion: reject,
    getReliability: reject,
    runPlayground: reject,
    streamPlayground: reject,
    getKbStats: reject,
    listKbClaims: reject,
    listKbConflicts: reject,
    uploadKbDocuments: reject,
  };
}
