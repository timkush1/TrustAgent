/**
 * msw handlers for the control-plane endpoints (SPEC §2.4 #9, #17-#29, #47, #50-#51, #61-#65)
 * over an in-memory state built from fixtures.ts. Tests change `state` to reach a page state
 * (an error, an empty list, a read-only proxy), and read `calls` to assert what the page sent.
 *
 * A state entry set to an ErrorReply answers with that error; `undefined` answers 404 (an older
 * proxy that does not serve the endpoint).
 *
 * The P4 planes add their own state and handlers from {security,cost,evaluator,studio}Handlers.ts
 * (one owner each, seeded from contracts/fixtures/api); ControlState is the P3 state plus theirs.
 */
import { delay, http, HttpResponse, type HttpHandler } from 'msw';
import type {
  EvaluatorDetail,
  EvaluatorMeta,
  EvaluatorVersion,
  EvidenceCoverage,
  EvidenceSuggestion,
  IngestResult,
  KBStats,
  OverviewStats,
  PlaygroundResult,
  PolicyDryRunResult,
  PolicyState,
  PolicyVersion,
  PolicyVersionMeta,
  ReliabilityResponse,
  RoiSummary,
  ValidateResult,
} from '@/types/api';
import type { KbClaimsPage, KbConflictsPage } from '@/data/controlPlane';
import { API } from '../msw';
import { costHandlers, defaultCostState, type CostState } from './costHandlers';
import { defaultEvaluatorState, evaluatorHandlers, type EvaluatorState } from './evaluatorHandlers';
import { isError, readBody, reply, errorReply, type ErrorReply, type HandlerKit, type Reply } from './kit';
import { defaultSecurityState, securityHandlers, type SecurityState } from './securityHandlers';
import { defaultStudioState, studioHandlers, type StudioState } from './studioHandlers';
import {
  conflictPair,
  dryRunResult,
  evaluatorDetail,
  evaluatorMetas,
  evaluatorVersion,
  evidenceCoverage,
  evidenceSuggestion,
  ingestResult,
  kbClaim,
  kbStats,
  overviewStats,
  playgroundResult,
  policyState,
  policyVersion,
  policyVersionMeta,
  reliability,
  roiSummary,
  validateResult,
} from './fixtures';

export { errorReply, type ErrorReply, type Reply };

/** The P3 control-plane state (the P4 planes' state is merged in by ControlState). */
export interface P3ControlState {
  overview: Reply<OverviewStats>;
  roi: Reply<RoiSummary>;
  coverage: Reply<EvidenceCoverage>;
  suggestions: Reply<{ items: EvidenceSuggestion[] }>;
  policies: Reply<PolicyState>;
  versions: Reply<PolicyVersionMeta[]>;
  versionBodies: Record<number, PolicyVersion>;
  validate: Reply<ValidateResult>;
  dryRun: Reply<PolicyDryRunResult>;
  /** What PUT /api/policies answers (default: the state with the new yaml and a bumped etag). */
  save: Reply<PolicyState> | 'echo';
  rollback: Reply<PolicyState> | 'echo';
  adoptFile: Reply<PolicyState> | 'echo';
  evaluators: Reply<EvaluatorMeta[]>;
  evaluatorDetails: Record<string, Reply<EvaluatorDetail>>;
  evaluatorVersions: Record<string, EvaluatorVersion>;
  reliability: Reply<ReliabilityResponse>;
  playground: Reply<PlaygroundResult>;
  /** Streamed replies: SSE text to send for `stream: true`. */
  playgroundStream: string | null;
  kbStats: Reply<KBStats>;
  kbClaims: Reply<KbClaimsPage>;
  kbConflicts: Reply<KbConflictsPage>;
  kbUpload: Reply<IngestResult>;
  /** Milliseconds to wait before answering any request (loading states). */
  latencyMs: number;
}

export type ControlState = P3ControlState & SecurityState & CostState & EvaluatorState & StudioState;

export interface ControlCall {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

export interface ControlApi {
  state: ControlState;
  handlers: HttpHandler[];
  calls: ControlCall[];
  /** Calls to one path (and method). */
  callsTo(path: string, method?: string): ControlCall[];
}

export function defaultControlState(): ControlState {
  const versions = [policyVersionMeta(3), policyVersionMeta(2), policyVersionMeta(1)];
  const details = Object.fromEntries(evaluatorMetas().map((m) => [m.id, evaluatorDetail(m.id)]));
  return {
    ...defaultSecurityState(),
    ...defaultCostState(),
    ...defaultEvaluatorState(),
    ...defaultStudioState(),
    overview: overviewStats(),
    roi: roiSummary(),
    coverage: evidenceCoverage(),
    suggestions: { items: [evidenceSuggestion()] },
    policies: policyState(),
    versions,
    versionBodies: { 1: policyVersion(1), 2: policyVersion(2), 3: policyVersion(3) },
    validate: validateResult(),
    dryRun: dryRunResult(),
    save: 'echo',
    rollback: 'echo',
    adoptFile: 'echo',
    evaluators: evaluatorMetas(),
    evaluatorDetails: details,
    evaluatorVersions: {},
    reliability: reliability(),
    playground: playgroundResult(),
    playgroundStream: null,
    kbStats: kbStats(),
    kbClaims: { claims: Array.from({ length: 3 }, (_, i) => kbClaim(i)), total: 3, limit: 50, offset: 0 },
    kbConflicts: { conflicts: [conflictPair()], total: 1 },
    kbUpload: ingestResult(),
    latencyMs: 0,
  };
}

export function createControlApi(overrides: Partial<ControlState> = {}): ControlApi {
  const state: ControlState = { ...defaultControlState(), ...overrides };
  const calls: ControlCall[] = [];

  const record: HandlerKit['record'] = async (request: Request) => {
    const url = new URL(request.url);
    const body = request.method === 'GET' ? undefined : await readBody(request.clone());
    calls.push({ method: request.method, path: url.pathname, query: url.searchParams, body, headers: request.headers });
    if (state.latencyMs > 0) await delay(state.latencyMs);
    return { url, body };
  };

  const echoPolicy = (body: unknown, message: string): PolicyState | ErrorReply | undefined => {
    if (isError(state.policies) || state.policies === undefined) return state.policies;
    const current = state.policies;
    const b = (body ?? {}) as { yaml?: string; message?: string };
    const id = current.id + 1;
    const next: PolicyState = {
      ...current,
      id,
      etag: `"v${id}"`,
      source: 'dashboard',
      author: 'setup-admin',
      message: b.message ?? message,
      yaml: b.yaml ?? current.yaml,
      created_at: new Date().toISOString(),
    };
    state.policies = next;
    return next;
  };

  const handlers: HttpHandler[] = [
    http.get(`${API}/api/overview`, async ({ request }) => {
      await record(request);
      return reply(state.overview);
    }),
    http.get(`${API}/api/roi/summary`, async ({ request }) => {
      await record(request);
      return reply(state.roi);
    }),
    http.get(`${API}/api/evidence/coverage`, async ({ request }) => {
      await record(request);
      return reply(state.coverage);
    }),
    http.get(`${API}/api/evidence/suggestions`, async ({ request }) => {
      await record(request);
      return reply(state.suggestions);
    }),
    http.get(`${API}/api/policies`, async ({ request }) => {
      await record(request);
      return reply(state.policies);
    }),
    http.get(`${API}/api/policies/versions`, async ({ request }) => {
      await record(request);
      if (!Array.isArray(state.versions)) return reply(state.versions);
      return HttpResponse.json({ items: state.versions, next_cursor: null });
    }),
    http.get(`${API}/api/policies/versions/:id`, async ({ request, params }) => {
      await record(request);
      const v = state.versionBodies[Number(params.id)];
      return v ? HttpResponse.json(v) : reply(undefined);
    }),
    http.post(`${API}/api/policies/validate`, async ({ request }) => {
      await record(request);
      return reply(state.validate);
    }),
    http.post(`${API}/api/policies/dry-run`, async ({ request }) => {
      await record(request);
      return reply(state.dryRun);
    }),
    http.put(`${API}/api/policies`, async ({ request }) => {
      const { body } = await record(request);
      if (state.save !== 'echo') return reply(state.save);
      if (!request.headers.get('if-match')) return reply(errorReply(428, 'precondition_required', 'Send If-Match with the version you edited.'));
      if (!isError(state.policies) && state.policies && request.headers.get('if-match') !== state.policies.etag) {
        return reply(errorReply(412, 'stale_version', 'The policy changed since you loaded it.'));
      }
      return reply(echoPolicy(body, 'Edited in the dashboard'));
    }),
    http.post(`${API}/api/policies/rollback`, async ({ request }) => {
      const { body } = await record(request);
      if (state.rollback !== 'echo') return reply(state.rollback);
      const version = (body as { version?: number } | undefined)?.version ?? 0;
      const target = state.versionBodies[version];
      const next = echoPolicy({ yaml: target?.yaml, message: `Rolled back to version ${version}` }, 'Rollback');
      if (next && !isError(next)) next.source = 'rollback';
      return reply(next);
    }),
    http.post(`${API}/api/policies/adopt-file`, async ({ request }) => {
      const { body } = await record(request);
      if (state.adoptFile !== 'echo') return reply(state.adoptFile);
      const next = echoPolicy(body, 'Adopted policies.yaml');
      if (next && !isError(next)) {
        next.source = 'adopt_file';
        next.file = { ...next.file, status: 'ok' };
      }
      return reply(next);
    }),
    http.get(`${API}/api/evaluators`, async ({ request }) => {
      const { url } = await record(request);
      if (!Array.isArray(state.evaluators)) return reply(state.evaluators);
      const all = url.searchParams.get('include_archived') === 'true';
      return HttpResponse.json({ items: state.evaluators.filter((e) => all || !e.archived) });
    }),
    http.get(`${API}/api/evaluators/:id`, async ({ request, params }) => {
      await record(request);
      return reply(state.evaluatorDetails[String(params.id)]);
    }),
    http.get(`${API}/api/evaluators/:id/versions/:v`, async ({ request, params }) => {
      await record(request);
      const key = `${String(params.id)}@${String(params.v)}`;
      return HttpResponse.json(state.evaluatorVersions[key] ?? evaluatorVersion(String(params.id), Number(params.v)));
    }),
    http.get(`${API}/api/gate/reliability`, async ({ request }) => {
      await record(request);
      return reply(state.reliability);
    }),
    http.post(`${API}/api/playground/run`, async ({ request }) => {
      const { body } = await record(request);
      const wantsStream = (body as { stream?: boolean } | undefined)?.stream === true;
      if (wantsStream && state.playgroundStream !== null) {
        return new HttpResponse(state.playgroundStream, { headers: { 'Content-Type': 'text/event-stream' } });
      }
      return reply(state.playground);
    }),
    http.get(`${API}/api/kb/stats`, async ({ request }) => {
      await record(request);
      return reply(state.kbStats);
    }),
    http.get(`${API}/api/kb/claims`, async ({ request }) => {
      const { url } = await record(request);
      const page = state.kbClaims;
      if (!page || isError(page)) return reply(page);
      const offset = Number(url.searchParams.get('offset') ?? 0);
      const limit = Number(url.searchParams.get('limit') ?? 50);
      const status = url.searchParams.get('status');
      const matching = page.claims.filter((c) => !status || c.status === status);
      return HttpResponse.json({
        claims: matching.slice(offset, offset + limit),
        total: status ? matching.length : page.total,
        limit,
        offset,
      });
    }),
    http.get(`${API}/api/kb/conflicts`, async ({ request }) => {
      await record(request);
      return reply(state.kbConflicts);
    }),
    http.post(`${API}/api/kb/documents`, async ({ request }) => {
      await record(request);
      return reply(state.kbUpload);
    }),
    // P4 planes (one owner each).
    ...securityHandlers(state, { record }),
    ...costHandlers(state, { record }),
    ...evaluatorHandlers(state, { record }),
    ...studioHandlers(state, { record }),
  ];

  return {
    state,
    handlers,
    calls,
    callsTo: (path, method) => calls.filter((c) => c.path === path && (!method || c.method === method)),
  };
}
