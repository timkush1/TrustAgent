/**
 * The control-plane endpoints over HTTP (SPEC §2.4 #3-#6, #9, #17-#29, #47, #50-#51, #61-#65). Every body
 * passes through the single normalizer (normalize.ts) before a page sees it. LiveDataSource extends
 * this class, so pages reach these methods through the one DataSource they already have.
 */
import type {
  EvaluatorDetail,
  EvaluatorMeta,
  EvaluatorVersion,
  EvidenceCoverage,
  EvidenceSuggestion,
  IngestResult,
  KBStats,
  OverviewStats,
  Page,
  PlaygroundRequest,
  PlaygroundResult,
  PolicyDryRunResult,
  PolicyState,
  PolicyVersion,
  PolicyVersionMeta,
  ReliabilityResponse,
  RoiSummary,
  SessionInfo,
  SetupStatus,
  TimeWindow,
  ValidateResult,
  GateDecisionView,
} from '@/types/api';
import type {
  ControlPlane,
  EvidenceParams,
  KbClaimsPage,
  KbClaimsParams,
  KbConflictsPage,
  OverviewParams,
  PlaygroundStreamHandlers,
  PolicyDryRunRequest,
  PolicyInput,
  PolicySaveRequest,
  ReliabilityParams,
  SessionRequest,
} from '../controlPlane';
import { ApiError, apiErrorFromResponse } from '../errors';
import type { HttpClient, Query } from '../http';
import { normalizeShaped, normalizeShapedPage, type NestedShape } from '../normalize';
import { readSse } from '../sse';
import type { PageParams } from '../source';

/** What each response must carry (see normalizeShaped). */
export const CONTROL_SHAPES = {
  setupStatus: { strings: ['public_url', 'recovery_command'] },
  session: { strings: ['actor', 'expires_at'], lists: ['scopes'] },
  overview: {
    strings: ['window', 'from', 'to'],
    records: ['totals', 'hallucination_rate', 'audited_hallucination_share', 'evidence_coverage', 'latency_ms', 'stage_latency_ms'],
    lists: ['outcomes_series', 'what_was_caught', 'by_route', 'by_model', 'top_contradicted'],
    inner: { evidence_coverage: { records: ['by_source'] } },
  },
  roi: {
    records: ['decision_cost', 'llm_cost', 'total_per_1k', 'savings_vs_same_checks', 'outcomes', 'latency_ms', 'sampling'],
    lists: ['counterfactuals', 'third_party'],
  },
  coverage: { records: ['by_source'], lists: ['top_reasons'] },
  suggestions: { lists: ['items'] },
  policyState: {
    strings: ['etag', 'yaml'],
    records: ['doc', 'file'],
    lists: ['effective_routes', 'warnings'],
    each: {
      effective_routes: { strings: ['name'], lists: ['downgrades', 'block_readiness', 'evaluators'], records: ['settings'], each: { block_readiness: { lists: ['missing'] } } },
    },
    inner: { file: { lists: ['errors'] } },
  },
  policyVersionMeta: { strings: ['created_at', 'source'] },
  policyVersion: { strings: ['yaml'], records: ['doc'] },
  validate: { lists: ['errors', 'warnings'], records: ['block_readiness'] },
  dryRun: {
    records: ['by_action_current', 'by_action_proposed', 'deep_audit_agreement'],
    lists: ['by_route', 'examples', 'skipped', 'caveats'],
  },
  evaluators: { lists: ['items'], each: { items: { strings: ['id', 'name'], lists: ['attached_routes'] } } },
  evaluatorDetail: {
    strings: ['id', 'name'],
    lists: ['versions', 'attached_routes'],
    records: ['latest', 'calibration_progress'],
    inner: { calibration_progress: { records: ['human_labels', 'needed'] } },
  },
  evaluatorVersion: { strings: ['evaluator_id'], records: ['spec'] },
  reliability: { lists: ['bins'], records: ['n_by_source'] },
  playground: {
    strings: ['trace_id'],
    records: ['decision', 'headers'],
    inner: { decision: { lists: ['reasons', 'detectors', 'excluded'], records: ['evaluators'] } },
  },
  decision: { lists: ['reasons', 'detectors', 'excluded'], records: ['evaluators'] },
  kbStats: {},
  kbClaims: { lists: ['claims'] },
  kbConflicts: { lists: ['conflicts'] },
  ingest: { lists: ['claim_results', 'warnings'] },
} as const satisfies Record<string, NestedShape>;

function enc(id: string): string {
  return encodeURIComponent(id);
}

function policyBody(input: PolicyInput): { yaml?: string; doc?: unknown } {
  return input.yaml !== undefined ? { yaml: input.yaml } : { doc: input.doc };
}

/** Longer than the default: a dry run replays up to 100,000 stored answers. */
const DRY_RUN_TIMEOUT_MS = 120_000;
/** A chat run waits on the upstream model. */
const PLAYGROUND_TIMEOUT_MS = 120_000;
const UPLOAD_TIMEOUT_MS = 300_000;

export class LiveControlPlane implements ControlPlane {
  protected readonly api: HttpClient;

  constructor(http: HttpClient) {
    this.api = http;
  }

  async getSetupStatus(signal?: AbortSignal): Promise<SetupStatus> {
    const raw = await this.api.get<unknown>('/api/setup/status', { signal });
    return normalizeShaped(raw, 'GET /api/setup/status', CONTROL_SHAPES.setupStatus);
  }

  async getSession(signal?: AbortSignal): Promise<SessionInfo> {
    const raw = await this.api.get<unknown>('/api/session', { signal });
    return normalizeShaped(raw, 'GET /api/session', CONTROL_SHAPES.session);
  }

  async createSession(request: SessionRequest): Promise<SessionInfo> {
    const raw = await this.api.request<unknown>('/api/session', { method: 'POST', body: request });
    return normalizeShaped(raw, 'POST /api/session', CONTROL_SHAPES.session);
  }

  async deleteSession(): Promise<void> {
    await this.api.request<unknown>('/api/session', { method: 'DELETE' });
  }

  async getOverview(params: OverviewParams, signal?: AbortSignal): Promise<OverviewStats> {
    const raw = await this.api.get<unknown>('/api/overview', { query: params as unknown as Query, signal });
    return normalizeShaped(raw, 'GET /api/overview', CONTROL_SHAPES.overview);
  }

  async getRoiSummary(window: TimeWindow, signal?: AbortSignal): Promise<RoiSummary> {
    const raw = await this.api.get<unknown>('/api/roi/summary', { query: { window }, signal });
    return normalizeShaped(raw, 'GET /api/roi/summary', CONTROL_SHAPES.roi);
  }

  async getEvidenceCoverage(params: EvidenceParams, signal?: AbortSignal): Promise<EvidenceCoverage> {
    const raw = await this.api.get<unknown>('/api/evidence/coverage', { query: params as unknown as Query, signal });
    return normalizeShaped(raw, 'GET /api/evidence/coverage', CONTROL_SHAPES.coverage);
  }

  async getEvidenceSuggestions(params: EvidenceParams, signal?: AbortSignal): Promise<{ items: EvidenceSuggestion[] }> {
    const raw = await this.api.get<unknown>('/api/evidence/suggestions', { query: params as unknown as Query, signal });
    return normalizeShaped(raw, 'GET /api/evidence/suggestions', CONTROL_SHAPES.suggestions);
  }

  async getPolicies(signal?: AbortSignal): Promise<PolicyState> {
    const raw = await this.api.get<unknown>('/api/policies', { signal });
    return normalizeShaped(raw, 'GET /api/policies', CONTROL_SHAPES.policyState);
  }

  async listPolicyVersions(params: PageParams, signal?: AbortSignal): Promise<Page<PolicyVersionMeta>> {
    const raw = await this.api.get<unknown>('/api/policies/versions', { query: params as Query, signal });
    return normalizeShapedPage(raw, 'GET /api/policies/versions', CONTROL_SHAPES.policyVersionMeta);
  }

  async getPolicyVersion(id: number, signal?: AbortSignal): Promise<PolicyVersion> {
    const raw = await this.api.get<unknown>(`/api/policies/versions/${id}`, { signal });
    return normalizeShaped(raw, 'GET /api/policies/versions/:id', CONTROL_SHAPES.policyVersion);
  }

  async validatePolicy(input: PolicyInput, signal?: AbortSignal): Promise<ValidateResult> {
    const raw = await this.api.request<unknown>('/api/policies/validate', { method: 'POST', body: policyBody(input), signal });
    return normalizeShaped(raw, 'POST /api/policies/validate', CONTROL_SHAPES.validate);
  }

  async dryRunPolicy(request: PolicyDryRunRequest, signal?: AbortSignal): Promise<PolicyDryRunResult> {
    const raw = await this.api.request<unknown>('/api/policies/dry-run', {
      method: 'POST',
      body: request,
      signal,
      timeoutMs: DRY_RUN_TIMEOUT_MS,
    });
    return normalizeShaped(raw, 'POST /api/policies/dry-run', CONTROL_SHAPES.dryRun);
  }

  async savePolicy(request: PolicySaveRequest, etag: string): Promise<PolicyState> {
    const { message, ...input } = request;
    const raw = await this.api.request<unknown>('/api/policies', {
      method: 'PUT',
      headers: { 'If-Match': etag },
      body: { ...policyBody(input as PolicyInput), message },
    });
    return normalizeShaped(raw, 'PUT /api/policies', CONTROL_SHAPES.policyState);
  }

  async rollbackPolicy(request: { version: number; message?: string }): Promise<PolicyState> {
    const raw = await this.api.request<unknown>('/api/policies/rollback', { method: 'POST', body: request });
    return normalizeShaped(raw, 'POST /api/policies/rollback', CONTROL_SHAPES.policyState);
  }

  async adoptPolicyFile(request: { message?: string }): Promise<PolicyState> {
    const raw = await this.api.request<unknown>('/api/policies/adopt-file', { method: 'POST', body: request });
    return normalizeShaped(raw, 'POST /api/policies/adopt-file', CONTROL_SHAPES.policyState);
  }

  async listEvaluators(params: { include_archived?: boolean }, signal?: AbortSignal): Promise<{ items: EvaluatorMeta[] }> {
    const raw = await this.api.get<unknown>('/api/evaluators', { query: params, signal });
    return normalizeShaped(raw, 'GET /api/evaluators', CONTROL_SHAPES.evaluators);
  }

  async getEvaluator(id: string, signal?: AbortSignal): Promise<EvaluatorDetail> {
    const raw = await this.api.get<unknown>(`/api/evaluators/${enc(id)}`, { signal });
    return normalizeShaped(raw, 'GET /api/evaluators/:id', CONTROL_SHAPES.evaluatorDetail);
  }

  async getEvaluatorVersion(id: string, version: number, signal?: AbortSignal): Promise<EvaluatorVersion> {
    const raw = await this.api.get<unknown>(`/api/evaluators/${enc(id)}/versions/${version}`, { signal });
    return normalizeShaped(raw, 'GET /api/evaluators/:id/versions/:v', CONTROL_SHAPES.evaluatorVersion);
  }

  async getReliability(params: ReliabilityParams, signal?: AbortSignal): Promise<ReliabilityResponse> {
    const raw = await this.api.get<unknown>('/api/gate/reliability', { query: params as unknown as Query, signal });
    return normalizeShaped(raw, 'GET /api/gate/reliability', CONTROL_SHAPES.reliability);
  }

  async runPlayground(request: PlaygroundRequest, signal?: AbortSignal): Promise<PlaygroundResult> {
    const raw = await this.api.request<unknown>('/api/playground/run', {
      method: 'POST',
      body: { ...request, stream: false },
      signal,
      timeoutMs: PLAYGROUND_TIMEOUT_MS,
    });
    return normalizeShaped(raw, 'POST /api/playground/run', CONTROL_SHAPES.playground);
  }

  async streamPlayground(
    request: PlaygroundRequest,
    handlers: PlaygroundStreamHandlers,
    signal?: AbortSignal,
  ): Promise<PlaygroundResult> {
    const what = 'POST /api/playground/run';
    const res = await this.api.stream('/api/playground/run', {
      method: 'POST',
      body: { ...request, stream: true },
      signal,
      timeoutMs: PLAYGROUND_TIMEOUT_MS,
    });
    // A proxy may answer a stream request with one JSON result (no upstream stream to relay).
    if (!(res.headers.get('content-type') ?? '').includes('text/event-stream') || !res.body) {
      let raw: unknown;
      try {
        raw = (await res.json()) as unknown;
      } catch {
        // Not JSON either (an HTML error page, say): the shape check reports it as invalid_response.
        raw = undefined;
      }
      return normalizeShaped(raw, what, CONTROL_SHAPES.playground);
    }
    for await (const item of readSse(res.body, signal)) {
      if (item.kind === 'comment') {
        handlers.onComment?.(item.text);
        continue;
      }
      const { event, data } = item.message;
      let payload: unknown;
      try {
        payload = JSON.parse(data);
      } catch {
        throw invalidStream(what);
      }
      switch (event) {
        case 'token': {
          const text = (payload as { text?: unknown } | null)?.text;
          if (typeof text === 'string') handlers.onToken?.(text);
          break;
        }
        case 'decision':
          handlers.onDecision?.(normalizeShaped<GateDecisionView>(payload, what, CONTROL_SHAPES.decision));
          break;
        case 'done':
          return normalizeShaped(payload, what, CONTROL_SHAPES.playground);
        case 'error':
          throw apiErrorFromResponse(502, payload);
        default:
          break;
      }
    }
    if (signal?.aborted) {
      throw new ApiError({ kind: 'aborted', status: 0, code: 'aborted', message: 'The request was cancelled.' });
    }
    throw invalidStream(what);
  }

  async getKbStats(signal?: AbortSignal): Promise<KBStats> {
    const raw = await this.api.get<unknown>('/api/kb/stats', { signal });
    return normalizeShaped(raw, 'GET /api/kb/stats', CONTROL_SHAPES.kbStats);
  }

  async listKbClaims(params: KbClaimsParams, signal?: AbortSignal): Promise<KbClaimsPage> {
    const raw = await this.api.get<unknown>('/api/kb/claims', { query: params as Query, signal });
    return normalizeShaped(raw, 'GET /api/kb/claims', CONTROL_SHAPES.kbClaims);
  }

  async listKbConflicts(params: { limit?: number }, signal?: AbortSignal): Promise<KbConflictsPage> {
    const raw = await this.api.get<unknown>('/api/kb/conflicts', { query: params, signal });
    return normalizeShaped(raw, 'GET /api/kb/conflicts', CONTROL_SHAPES.kbConflicts);
  }

  async uploadKbDocuments(file: File, signal?: AbortSignal): Promise<IngestResult> {
    const form = new FormData();
    form.append('file', file, file.name);
    const raw = await this.api.request<unknown>('/api/kb/documents', {
      method: 'POST',
      body: form,
      signal,
      timeoutMs: UPLOAD_TIMEOUT_MS,
    });
    return normalizeShaped(raw, 'POST /api/kb/documents', CONTROL_SHAPES.ingest);
  }
}

function invalidStream(what: string): ApiError {
  return new ApiError({
    kind: 'invalid_response',
    status: 200,
    code: 'invalid_response',
    message: `The stream from ${what} ended without a result. The proxy may be an older version.`,
  });
}
