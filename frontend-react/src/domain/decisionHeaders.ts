/**
 * The response headers of a gated answer (SPEC §2.12), rebuilt from the stored trace and its
 * decision the way the gateway writes them: backend-go internal/proxy/handler.go (TraceIDs) and
 * headers.go (setDecisionHeaders, setDeferredHeaders). Trace detail shows them under Details; the
 * Playground shows the headers its run really returned.
 *
 * - Every answer carries Trace-Id and Request-ID (the client's X-Request-ID, else the trace id).
 * - An observe route answers first and checks afterwards (unless the answer was skipped before
 *   the check, e.g. unreadable): Action observe, Status deferred, Route, Mode, Audit only when the
 *   route runs no deep audit (the one audit answer known up front), and Source.
 * - Otherwise the decision's headers. Action is what was delivered (the trace action), and
 *   Would-Action is sent when it differs from the decision's action.
 * - `headers: action_only` keeps only Trace-Id, Request-ID and Action.
 *
 * The stored decision does not keep the kind of a failed check, so `error:<kind>` reads `error`.
 */
import type { GateDecisionView, TraceAuditStatus, TraceSummary } from '@/types/api';
import { formatRisk } from './format';

export type HeadersSetting = 'full' | 'action_only';

export interface ResponseHeader {
  name: string;
  value: string;
}

export interface DecisionHeadersInput {
  summary: Pick<
    TraceSummary,
    'trace_id' | 'client_request_id' | 'route' | 'policy_version' | 'source' | 'mode' | 'action' | 'skip_reason' | 'audit' | 'timings'
  >;
  /** The decision on the model's answer (attempt 0); null while the gate has not decided. */
  decision: GateDecisionView | null;
  /** The route's `headers` setting when known; `full` otherwise. */
  headers?: HeadersSetting | null;
}

/** SPEC §2.12: at most 8 reasons. */
export const MAX_HEADER_REASONS = 8;

/** Skip reasons set before the check runs, when an observe route still answers with the decision. */
const ANSWER_STAGE_SKIPS = new Set(['unparseable_request', 'upstream_error', 'no_text', 'undecodable', 'too_long', 'unsupported_api']);

/** X-TrustAgent-Audit as the gateway answered it, from the audit's state now (a queued audit may have run since). */
export function auditHeaderValue(status: TraceAuditStatus): string | null {
  switch (status) {
    case 'queued':
    case 'running':
    case 'completed':
    case 'degraded':
    case 'failed':
      return 'queued';
    case 'sampled_out':
    case 'dropped':
    case 'disabled':
    case 'not_assessed':
      return status;
    default:
      return null;
  }
}

/** X-TrustAgent-Status: ok, partial, not_assessed, timeout, skipped:<reason> or error. */
export function statusHeaderValue(decision: Pick<GateDecisionView, 'status' | 'skip_reason'>): string {
  if (decision.status === 'skipped') return decision.skip_reason ? `skipped:${decision.skip_reason}` : 'skipped';
  return decision.status;
}

/** X-TrustAgent-Checked-Against: `request_system:1,tool_results:2`, or `none`. */
export function checkedAgainstValue(decision: Pick<GateDecisionView, 'evidence_summary'>): string {
  const used = decision.evidence_summary?.used ?? [];
  return used.length === 0 ? 'none' : used.map((u) => `${u.source}:${u.items}`).join(',');
}

/** Whether the answer went out before its check (the observe path of SPEC §2.12). */
export function deferredHeaders(summary: DecisionHeadersInput['summary'], decision: GateDecisionView | null): boolean {
  if (summary.mode !== 'observe') return false;
  const skip = decision?.skip_reason ?? summary.skip_reason;
  return !(skip && ANSWER_STAGE_SKIPS.has(skip));
}

/** The response headers in the order the gateway sets them. */
export function decisionHeaders({ summary, decision, headers }: DecisionHeadersInput): ResponseHeader[] {
  const out: ResponseHeader[] = [];
  const set = (name: string, value: string | number | null | undefined) => {
    if (value === null || value === undefined || value === '') return;
    out.push({ name, value: String(value) });
  };
  set('X-TrustAgent-Trace-Id', summary.trace_id);
  set('X-Request-ID', summary.client_request_id ?? summary.trace_id);
  const actionOnly = headers === 'action_only';
  const routeLabel = summary.policy_version === null ? summary.route : `${summary.route}@v${summary.policy_version}`;
  const source = summary.source !== 'proxy' ? summary.source : null;

  if (deferredHeaders(summary, decision)) {
    set('X-TrustAgent-Action', 'observe');
    if (actionOnly) return out;
    set('X-TrustAgent-Route', routeLabel);
    set('X-TrustAgent-Mode', summary.mode);
    set('X-TrustAgent-Status', 'deferred');
    if (summary.audit.status === 'disabled') set('X-TrustAgent-Audit', 'disabled');
    set('X-TrustAgent-Source', source);
    return out;
  }
  if (!decision) return out;

  set('X-TrustAgent-Action', summary.action ?? decision.action);
  if (actionOnly) return out;
  set('X-TrustAgent-Route', routeLabel);
  set('X-TrustAgent-Mode', decision.effective_mode);
  set('X-TrustAgent-Status', statusHeaderValue(decision));
  if (decision.would_action && decision.would_action !== decision.action) set('X-TrustAgent-Would-Action', decision.would_action);
  set('X-TrustAgent-Verdict', decision.verdict);
  if (decision.max_risk !== null) set('X-TrustAgent-Risk', formatRisk(decision.max_risk));
  set('X-TrustAgent-Calibrated', decision.calibration);
  set('X-TrustAgent-Degraded', decision.degraded);
  set('X-TrustAgent-Reasons', decision.reasons.slice(0, MAX_HEADER_REASONS).join(','));
  set('X-TrustAgent-Checked-Against', checkedAgainstValue(decision));
  set('X-TrustAgent-Decision-Model', decision.decision_model?.snapshot);
  set('X-TrustAgent-Gate-Ms', decision.gate_ms);
  set('X-TrustAgent-Overhead-Ms', summary.timings.overhead_ms);
  set('X-TrustAgent-Audit', auditHeaderValue(summary.audit.status));
  set('X-TrustAgent-Source', source);
  return out;
}
