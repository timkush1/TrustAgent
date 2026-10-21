/**
 * Query keys and queryOptions factories (TanStack Query). Pages call
 * `useQuery(tracesQuery(ds, params))`; the stream side effects in liveStream.ts use the same keys.
 */
import { QueryClient, queryOptions } from '@tanstack/react-query';
import type { TimeWindow } from '@/types/api';
import type { EvidenceParams, KbClaimsParams, OverviewParams, ReliabilityParams } from './controlPlane';
import { isApiError } from './errors';
import { costSideEffects } from './queries/costs';
import { evaluatorSideEffects } from './queries/evaluators';
import { securitySideEffects } from './queries/security';
import { studioSideEffects } from './queries/studio';
import type { AuditListParams, DataSource, DecisionListParams, TraceListParams } from './source';
import type { StreamEvent } from './stream/types';

// The control planes keep their keys, queryOptions and side effects in data/queries/<plane>.ts (one owner each).
export { qkcSecurity, securitySideEffects, settingsQuery } from './queries/security';
export { qkcCosts, costSideEffects } from './queries/costs';
export { qkcEvaluators, evaluatorSideEffects } from './queries/evaluators';
export { qkcStudio, studioSideEffects } from './queries/studio';

export const qk = {
  health: ['health'] as const,
  status: ['status'] as const,
  setupStatus: ['setup-status'] as const,
  session: ['session'] as const,
  traces: {
    all: ['traces'] as const,
    lists: () => ['traces', 'list'] as const,
    list: (params: TraceListParams) => ['traces', 'list', params] as const,
    detail: (id: string) => ['traces', 'detail', id] as const,
  },
  decisions: {
    all: ['decisions'] as const,
    list: (params: DecisionListParams) => ['decisions', 'list', params] as const,
  },
  audits: {
    all: ['audits'] as const,
    lists: () => ['audits', 'list'] as const,
    list: (params: AuditListParams) => ['audits', 'list', params] as const,
    detail: (id: string) => ['audits', 'detail', id] as const,
  },
};

/** Health is polled; the pill must notice a proxy that went away. */
export const HEALTH_POLL_MS = 15_000;
export const STATUS_POLL_MS = 30_000;

export function healthQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qk.health,
    queryFn: ({ signal }) => ds.getHealth(signal),
    refetchInterval: HEALTH_POLL_MS,
    retry: false,
    staleTime: 5_000,
  });
}

/**
 * GET /api/status. Absent (404) or not permitted (401/403) is a normal state for older proxies
 * and read-less sessions: callers fall back to /health and say "unknown", never "running".
 */
export function statusQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qk.status,
    queryFn: ({ signal }) => ds.getStatus(signal),
    refetchInterval: (query) => (isStatusUnavailable(query.state.error) ? false : STATUS_POLL_MS),
    retry: false,
    staleTime: 10_000,
  });
}

/** GET /api/setup/status (public): whether the one-time setup link can still create the admin. */
export function setupStatusQuery(ds: DataSource) {
  return queryOptions({ queryKey: qk.setupStatus, queryFn: ({ signal }) => ds.getSetupStatus(signal), retry: false });
}

/** GET /api/session: who the dashboard is signed in as; a 401 means not signed in. */
export function sessionQuery(ds: DataSource) {
  return queryOptions({ queryKey: qk.session, queryFn: ({ signal }) => ds.getSession(signal), retry: false });
}

export function isStatusUnavailable(err: unknown): boolean {
  return isApiError(err) && (err.status === 404 || err.status === 401 || err.status === 403 || err.status === 501);
}

export function tracesQuery(ds: DataSource, params: TraceListParams) {
  return queryOptions({
    queryKey: qk.traces.list(params),
    queryFn: ({ signal }) => ds.listTraces(params, signal),
  });
}

export function traceQuery(ds: DataSource, id: string) {
  return queryOptions({
    queryKey: qk.traces.detail(id),
    queryFn: ({ signal }) => ds.getTrace(id, signal),
  });
}

export function auditsQuery(ds: DataSource, params: AuditListParams) {
  return queryOptions({
    queryKey: qk.audits.list(params),
    queryFn: ({ signal }) => ds.listAudits(params, signal),
  });
}

export function auditQuery(ds: DataSource, id: string) {
  return queryOptions({
    queryKey: qk.audits.detail(id),
    queryFn: ({ signal }) => ds.getAudit(id, signal),
  });
}

export function decisionsQuery(ds: DataSource, params: DecisionListParams) {
  return queryOptions({
    queryKey: qk.decisions.list(params),
    queryFn: ({ signal }) => ds.listDecisions(params, signal),
  });
}

/** Retry transient failures (network, timeout, 429, 5xx) twice; never a 4xx. */
export function shouldRetry(failureCount: number, err: unknown): boolean {
  if (isApiError(err)) return err.transient && failureCount < 2;
  return failureCount < 2;
}

/** Honours Retry-After, else exponential from 1 s. */
export function retryDelay(failureCount: number, err: unknown): number {
  if (isApiError(err) && err.retryAfterMs !== null) return Math.min(30_000, err.retryAfterMs);
  return Math.min(30_000, 1000 * 2 ** failureCount);
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        retry: shouldRetry,
        retryDelay,
        refetchOnWindowFocus: false,
      },
    },
  });
}

// ------------------------------------------------------------- control plane (P3 pages)

/** Query keys of the control-plane endpoints (controlPlane.ts). */
export const qkc = {
  overview: {
    all: ['overview'] as const,
    stats: (params: OverviewParams) => ['overview', 'stats', params] as const,
    roi: (window: TimeWindow) => ['overview', 'roi', window] as const,
  },
  evidence: {
    all: ['evidence'] as const,
    coverage: (params: EvidenceParams) => ['evidence', 'coverage', params] as const,
    suggestions: (params: EvidenceParams) => ['evidence', 'suggestions', params] as const,
  },
  policies: {
    all: ['policies'] as const,
    state: ['policies', 'state'] as const,
    versions: ['policies', 'versions'] as const,
    version: (id: number) => ['policies', 'version', id] as const,
  },
  /**
   * The review of a policy draft (validate and "Preview impact"): keyed by the draft text and kept
   * apart from `policies`, so the stream's invalidations (every reconnect's `hello`) never re-run a
   * rate-limited replay of past answers while the review is open.
   */
  policyReview: {
    validate: (yaml: string) => ['policy-review', 'validate', yaml] as const,
    dryRun: (window: TimeWindow, yaml: string) => ['policy-review', 'dry-run', window, yaml] as const,
  },
  evaluators: {
    all: ['evaluators'] as const,
    list: (includeArchived: boolean) => ['evaluators', 'list', includeArchived] as const,
    detail: (id: string) => ['evaluators', 'detail', id] as const,
    version: (id: string, version: number) => ['evaluators', 'version', id, version] as const,
    reliability: (params: ReliabilityParams) => ['evaluators', 'reliability', params] as const,
  },
  kb: {
    all: ['kb'] as const,
    stats: ['kb', 'stats'] as const,
    claims: (params: KbClaimsParams) => ['kb', 'claims', params] as const,
    conflicts: ['kb', 'conflicts'] as const,
  },
};

/** Overview numbers move with traffic; the stream's `hello` and resyncs refresh them too. */
export const OVERVIEW_POLL_MS = 30_000;

/** A missing endpoint (older proxy, or an add-on that is off) is a state, not an error to retry. */
export function isNotServed(err: unknown): boolean {
  return isApiError(err) && (err.status === 404 || err.status === 501);
}

export function overviewQuery(ds: DataSource, params: OverviewParams) {
  return queryOptions({
    queryKey: qkc.overview.stats(params),
    queryFn: ({ signal }) => ds.getOverview(params, signal),
    refetchInterval: OVERVIEW_POLL_MS,
  });
}

export function roiSummaryQuery(ds: DataSource, window: TimeWindow) {
  return queryOptions({
    queryKey: qkc.overview.roi(window),
    queryFn: ({ signal }) => ds.getRoiSummary(window, signal),
    refetchInterval: OVERVIEW_POLL_MS,
  });
}

export function evidenceCoverageQuery(ds: DataSource, params: EvidenceParams) {
  return queryOptions({
    queryKey: qkc.evidence.coverage(params),
    queryFn: ({ signal }) => ds.getEvidenceCoverage(params, signal),
  });
}

/** Admin-only, and missing on an older proxy: a 401/403/404 is an expected answer, so it is not retried. */
export function evidenceSuggestionsQuery(ds: DataSource, params: EvidenceParams) {
  return queryOptions({
    queryKey: qkc.evidence.suggestions(params),
    queryFn: ({ signal }) => ds.getEvidenceSuggestions(params, signal),
    retry: false,
    staleTime: 60_000,
  });
}

export function policiesQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkc.policies.state,
    queryFn: ({ signal }) => ds.getPolicies(signal),
  });
}

export function policyVersionQuery(ds: DataSource, id: number) {
  return queryOptions({
    queryKey: qkc.policies.version(id),
    queryFn: ({ signal }) => ds.getPolicyVersion(id, signal),
    // A stored version never changes.
    staleTime: Number.POSITIVE_INFINITY,
  });
}

export function evaluatorsQuery(ds: DataSource, includeArchived = false) {
  return queryOptions({
    queryKey: qkc.evaluators.list(includeArchived),
    queryFn: ({ signal }) => ds.listEvaluators({ include_archived: includeArchived || undefined }, signal),
  });
}

export function evaluatorQuery(ds: DataSource, id: string) {
  return queryOptions({
    queryKey: qkc.evaluators.detail(id),
    queryFn: ({ signal }) => ds.getEvaluator(id, signal),
  });
}

export function evaluatorVersionQuery(ds: DataSource, id: string, version: number) {
  return queryOptions({
    queryKey: qkc.evaluators.version(id, version),
    queryFn: ({ signal }) => ds.getEvaluatorVersion(id, version, signal),
    // Evaluator versions are immutable (SPEC §2.8).
    staleTime: Number.POSITIVE_INFINITY,
  });
}

export function reliabilityQuery(ds: DataSource, params: ReliabilityParams) {
  return queryOptions({
    queryKey: qkc.evaluators.reliability(params),
    queryFn: ({ signal }) => ds.getReliability(params, signal),
  });
}

export function kbStatsQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkc.kb.stats,
    queryFn: ({ signal }) => ds.getKbStats(signal),
    retry: (count, err) => !isKnowledgeDisabled(err) && shouldRetry(count, err),
  });
}

export function kbClaimsQuery(ds: DataSource, params: KbClaimsParams) {
  return queryOptions({
    queryKey: qkc.kb.claims(params),
    queryFn: ({ signal }) => ds.listKbClaims(params, signal),
    retry: (count, err) => !isKnowledgeDisabled(err) && shouldRetry(count, err),
  });
}

export function kbConflictsQuery(ds: DataSource) {
  return queryOptions({
    queryKey: qkc.kb.conflicts,
    queryFn: ({ signal }) => ds.listKbConflicts({ limit: 50 }, signal),
    retry: (count, err) => !isKnowledgeDisabled(err) && shouldRetry(count, err),
  });
}

/** 409 `knowledge_disabled`: the knowledge add-on is off (SPEC §2.4 #62-65). */
export function isKnowledgeDisabled(err: unknown): boolean {
  return isApiError(err) && err.code === 'knowledge_disabled';
}

/**
 * Keeps control-plane queries in step with the live stream (called from liveStream.ts for every
 * committed batch): policy and evaluator changes refetch their pages; (re)connects and resyncs
 * refresh the overview, its evidence coverage and the policy. Each event also goes to the control planes'
 * side effects (data/queries/<plane>.ts).
 */
export function applyControlSideEffects(queryClient: QueryClient, batch: readonly StreamEvent[]): void {
  let overview = false;
  let policies = false;
  let evaluators = false;
  for (const ev of batch) {
    securitySideEffects(ev, queryClient);
    costSideEffects(ev, queryClient);
    evaluatorSideEffects(ev, queryClient);
    studioSideEffects(ev, queryClient);
    switch (ev.type) {
      case 'hello':
      case 'resync':
        overview = true;
        policies = true;
        break;
      case 'policy_updated':
      case 'policy_error':
        policies = true;
        evaluators = true; // attached routes and stages follow the policy
        break;
      case 'evaluator_updated':
      case 'calibration_updated':
      case 'decision_model_changed':
        evaluators = true;
        policies = true; // block readiness depends on calibration and the snapshot
        break;
      default:
        break;
    }
  }
  if (overview) {
    void queryClient.invalidateQueries({ queryKey: qkc.overview.all, refetchType: 'active' });
    // Evidence coverage is part of the overview, under its own key.
    void queryClient.invalidateQueries({ queryKey: qkc.evidence.all, refetchType: 'active' });
  }
  if (policies) void queryClient.invalidateQueries({ queryKey: qkc.policies.all, refetchType: 'active' });
  if (evaluators) void queryClient.invalidateQueries({ queryKey: qkc.evaluators.all, refetchType: 'active' });
}
