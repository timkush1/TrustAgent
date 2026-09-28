/**
 * The trace detail model (SPEC §3.3 "Trace detail"), as pure functions. A request's detail comes
 * from up to three places that arrive in any order: GET /api/traces/:id, the live stream
 * (trace_started, gate_decision, audit_completed) and GET /api/audits/:id. They are merged so the
 * page only moves forward: pending → decided → audited.
 */
import { applyAuditToTrace, auditRank, mergeTraceSummary } from '@/data/normalize';
import {
  copy,
  fillTemplate,
  humanizeId,
  statusReason,
} from '@/domain/presentation';
import type {
  AuditDetail,
  ClaimVerdict,
  ClaimView,
  EvaluatorLevel,
  EvaluatorResult,
  GateDecisionView,
  TraceAuditStatus,
  TraceDetail,
  TraceSummary,
} from '@/types/api';

export interface TraceViewInput {
  detail?: TraceDetail | null;
  live?: TraceSummary | null;
  liveDecision?: GateDecisionView | null;
  liveAudit?: AuditDetail | null;
  fetchedAudit?: AuditDetail | null;
}

export interface TraceView {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  audit: AuditDetail | null;
  body: TraceDetail['body'] | null;
  question: string | null;
  /** The answer as the model gave it; `preview` when only the list preview is known. */
  answer: { text: string; preview: boolean } | null;
}

/** The decision that stands: the highest attempt, a late decision over an on-time one. */
export function latestDecision(list: readonly (GateDecisionView | null | undefined)[]): GateDecisionView | null {
  let best: GateDecisionView | null = null;
  for (const d of list) {
    if (!d) continue;
    if (!best || d.attempt > best.attempt || (d.attempt === best.attempt && d.late && !best.late)) best = d;
  }
  return best;
}

/**
 * The most complete audit among the sources (live first, so ties keep the live one), limited to
 * the audit the trace points at when one of them is it.
 */
export function pickAudit(summary: TraceSummary, audits: readonly (AuditDetail | null | undefined)[]): AuditDetail | null {
  const mine = audits.filter((a): a is AuditDetail => Boolean(a) && a?.trace_id === summary.trace_id);
  const wanted = summary.audit.audit_id;
  const pool = wanted && mine.some((a) => a.audit_id === wanted) ? mine.filter((a) => a.audit_id === wanted) : mine;
  let best: AuditDetail | null = null;
  for (const a of pool) if (!best || auditRank(a.status) > auditRank(best.status)) best = a;
  return best;
}

export function buildTraceView(input: TraceViewInput): TraceView | null {
  const { detail, live } = input;
  const base = detail ?? live;
  if (!base) return null;
  let summary = detail && live ? mergeTraceSummary(detail, live) : base;
  const decision = latestDecision([...(detail?.decisions ?? []), input.liveDecision]);
  const audit = pickAudit(summary, [input.liveAudit, detail?.audit_detail, input.fetchedAudit]);
  if (audit) summary = applyAuditToTrace(summary, audit);

  const body = detail?.body ?? null;
  const question = body?.user_request ?? audit?.user_query ?? summary.question_preview ?? null;
  const full = body?.response_text ?? body?.final_response_text ?? audit?.llm_response ?? null;
  const answer = full
    ? { text: full, preview: false }
    : summary.response_preview
      ? { text: summary.response_preview, preview: true }
      : null;
  return { summary, decision, audit, body, question, answer };
}

/** The audit status is terminal and has a result to fetch. */
export function auditHasResult(status: TraceAuditStatus): boolean {
  return status === 'completed' || status === 'degraded';
}

// -------------------------------------------------------------------------------------- checks

const NO_DOCUMENTS = new Set(['no_evidence', 'evidence_too_short', 'evidence_packed_away']);

export interface CheckRow {
  ref: string;
  name: string;
  /** Asked but never enforced ("Watching"). */
  shadow: boolean;
  kind: 'assessed' | 'no_documents' | 'not_assessed' | 'error';
  level: EvaluatorLevel | null;
  risk: number | null;
  warnAt: number;
  criticalAt: number | null;
  /** Why it was not assessed or failed, in words. */
  note: string | null;
}

/** "groundedness@1" → "Groundedness" (the evaluator library names them from P4). */
export function evaluatorName(ref: string): string {
  return humanizeId(ref.split('@')[0]);
}

function checkRow(r: EvaluatorResult): CheckRow {
  const row = {
    ref: r.ref,
    name: evaluatorName(r.ref),
    shadow: r.stage === 'shadow',
    level: r.level,
    risk: r.risk,
    warnAt: r.thresholds.warn_at,
    criticalAt: r.thresholds.critical_at,
  };
  if (r.status === 'assessed' && r.risk !== null && r.level !== null) return { ...row, kind: 'assessed', note: null };
  if (r.status === 'not_assessed' && (r.reason === null || NO_DOCUMENTS.has(r.reason))) {
    return { ...row, kind: 'no_documents', note: copy.trace.not_checked_no_documents };
  }
  const why = statusReason(r.reason) ?? (r.reason ? humanizeId(r.reason).toLowerCase() : null);
  if (r.status === 'not_assessed') {
    return { ...row, kind: 'not_assessed', note: why ? `Not checked — ${why}` : 'Not checked' };
  }
  return { ...row, kind: 'error', note: why ? `Check failed — ${why}` : 'Check failed' };
}

/** One row per evaluator, in route order (the decision's key order). */
export function checkRows(decision: GateDecisionView | null): CheckRow[] {
  if (!decision) return [];
  return Object.values(decision.evaluators).map(checkRow);
}

// -------------------------------------------------------------------------------------- claims

/** The id of a claim's row in the Claims section; the answer's claim marks link to it. */
export function claimRowId(prefix: string, index: number): string {
  return `${prefix}claim-${index}`;
}

/** Claims in the order the audit found them. */
export function orderedClaims(audit: AuditDetail | null): ClaimView[] {
  return audit ? [...audit.claims].sort((a, b) => a.index - b.index) : [];
}

function claimCounts(audit: AuditDetail): { total: number; supported: number; contradicted: number } {
  // Read defensively: an engine that sends no counts must not break the Claims section.
  const c: Partial<AuditDetail['verdict_counts']> = audit.verdict_counts ?? {};
  // NOT_CHECKED is excluded from every aggregate (src/types/api.ts).
  const counted: ClaimVerdict[] = ['SUPPORTED', 'PARTIALLY_SUPPORTED', 'CONTRADICTED', 'UNSUPPORTED', 'UNKNOWN'];
  const total = counted.reduce((n, v) => n + (c[v] ?? 0), 0);
  return { total, supported: c.SUPPORTED ?? 0, contradicted: c.CONTRADICTED ?? 0 };
}

/** "1 of 2 claims supported · 1 contradicted"; null without claims. */
export function claimsSummary(audit: AuditDetail | null): string | null {
  if (!audit) return null;
  const { total, supported, contradicted } = claimCounts(audit);
  if (total === 0) return null;
  return fillTemplate(copy.trace.claims_summary, { supported, total, contradicted });
}

/** The one-line disagreement: the fast check passed the answer, the deep audit contradicted it. */
export function disagreementLine(view: Pick<TraceView, 'summary' | 'audit'>): string | null {
  if (!view.audit || view.summary.verdict !== 'supported') return null;
  const { contradicted } = claimCounts(view.audit);
  return contradicted > 0 ? fillTemplate(copy.trace.disagreement, { count: contradicted }) : null;
}
