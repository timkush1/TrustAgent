/**
 * The trace detail model (SPEC §3.3 "Trace detail"), as pure functions. A request's detail comes
 * from up to three places that arrive in any order: GET /api/traces/:id, the live stream
 * (trace_started, gate_decision, audit_completed) and GET /api/audits/:id. They are merged so the
 * page only moves forward: pending → decided → audited.
 */
import { applyAuditToTrace, auditRank, mergeTraceSummary } from '@/data/normalize';
import type { MarkFinding } from '@/domain/segmentClaims';
import { formatDurationMs, formatRisk } from '@/domain/format';
import {
  copy,
  fillTemplate,
  gateVerdictView,
  humanizeId,
  levelWord,
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

/** What the user received (AuditDetail.gate.delivered): the model's answer, a fallback answer or the block message. */
export type Delivered = 'original' | 'fallback' | 'blocked';

export interface TraceView {
  summary: TraceSummary;
  /** The fast check's decision on the model's answer: attempt 0 (a late decision over an on-time one). */
  decision: GateDecisionView | null;
  /** Every stored decision, one per attempt, in order; attempt 1 checked the fallback answer. */
  attempts: GateDecisionView[];
  audit: AuditDetail | null;
  body: TraceDetail['body'] | null;
  question: string | null;
  /** The answer as the model gave it; `preview` when only the list preview is known. */
  answer: { text: string; preview: boolean } | null;
  /** What the user received; null while the gate has not decided. */
  delivered: Delivered | null;
  /** The text sent in place of the model's answer (the fallback answer or the block message), when stored. */
  deliveredText: string | null;
}

/**
 * One decision per attempt, in attempt order. A late decision updates its attempt's row, so it
 * replaces an on-time copy of the same attempt; otherwise the first copy seen stays.
 */
export function decisionAttempts(list: readonly (GateDecisionView | null | undefined)[]): GateDecisionView[] {
  const byAttempt = new Map<number, GateDecisionView>();
  for (const d of list) {
    if (!d) continue;
    const prev = byAttempt.get(d.attempt);
    if (!prev || (d.late && !prev.late)) byAttempt.set(d.attempt, d);
  }
  return [...byAttempt.values()].sort((a, b) => a.attempt - b.attempt);
}

/**
 * The decision on the model's own answer (attempt 0). The fallback check (attempt 1) judged a
 * different text, so it never stands in for it: the checks, reasons and marks describe the answer
 * the model gave.
 */
export function answerDecision(attempts: readonly GateDecisionView[]): GateDecisionView | null {
  return attempts.find((d) => d.attempt === 0) ?? null;
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

/**
 * What the user received. The gateway records a delivered fallback with the flag `fallback_used`
 * (also when a failed re-check still sends it with Action: flag) and a block as the trace action;
 * the deep audit repeats what it was told. Null until the gate has decided.
 */
export function deliveredOf(summary: TraceSummary, audit: AuditDetail | null): Delivered | null {
  if ((summary.flags ?? []).includes('fallback_used') || summary.action === 'fallback') return 'fallback';
  if (summary.action === 'block') return 'blocked';
  const reported = audit?.gate?.delivered;
  if (reported) return reported;
  return summary.action === null ? null : 'original';
}

export function buildTraceView(input: TraceViewInput): TraceView | null {
  const { detail, live } = input;
  const base = detail ?? live;
  if (!base) return null;
  let summary = detail && live ? mergeTraceSummary(detail, live) : base;
  const attempts = decisionAttempts([...(detail?.decisions ?? []), input.liveDecision]);
  const decision = answerDecision(attempts);
  const audit = pickAudit(summary, [input.liveAudit, detail?.audit_detail, input.fetchedAudit]);
  if (audit) summary = applyAuditToTrace(summary, audit);

  const body = detail?.body ?? null;
  const delivered = deliveredOf(summary, audit);
  const replaced = delivered === 'fallback' || delivered === 'blocked';
  const question = body?.user_request ?? audit?.user_query ?? summary.question_preview ?? null;
  // final_response_text is what was sent; it stands in for the model's answer only when that was sent.
  const full = body?.response_text ?? (replaced ? null : body?.final_response_text) ?? audit?.llm_response ?? null;
  const answer = full
    ? { text: full, preview: false }
    : summary.response_preview
      ? { text: summary.response_preview, preview: true }
      : null;
  const sent = body?.final_response_text ?? null;
  // A store that kept only the model's answer has nothing to show for what was sent instead.
  const deliveredText = replaced && sent !== null && sent !== body?.response_text ? sent : null;
  return { summary, decision, attempts, audit, body, question, answer, delivered, deliveredText };
}

const LEVEL_RANK: Record<EvaluatorLevel, number> = { ok: 0, warn: 1, critical: 2 };

/** The highest level among a decision's enforced (active, not internal) evaluators, or null. */
export function topLevel(d: GateDecisionView): EvaluatorLevel | null {
  let top: EvaluatorLevel | null = null;
  for (const e of Object.values(d.evaluators)) {
    if (e.internal || e.stage === 'shadow' || e.level === null) continue;
    if (top === null || LEVEL_RANK[e.level] > LEVEL_RANK[top]) top = e.level;
  }
  return top;
}

const unfinished = (d: GateDecisionView) => d.status === 'timeout' || d.status === 'error' || d.status === 'skipped';

/** How the fallback check (attempt 1) rated the fallback answer: "low risk", or why it did not finish. */
export function fallbackCheckResult(check: GateDecisionView | null | undefined): string | null {
  if (!check) return null;
  if (unfinished(check)) return statusReason(check.skip_reason ?? check.status);
  const top = topLevel(check);
  return top ? levelWord(top).label.toLowerCase() : null;
}

/** "Your model's answer" for attempt 0, "Fallback check" for attempt 1. */
export function attemptLabel(attempt: number): string {
  if (attempt === 0) return copy.trace.attempt_answer;
  if (attempt === 1) return copy.trace.attempt_fallback;
  return `Attempt ${attempt}`;
}

/** One line per attempt: "Contradicted · High risk 0.97 · 5 ms", or why it was not checked. */
export function attemptSummary(d: GateDecisionView): string {
  const parts: string[] = [];
  if (unfinished(d)) {
    const reason = statusReason(d.skip_reason ?? d.status);
    parts.push(d.status === 'skipped' ? `Not checked${reason ? `: ${reason}` : ''}` : `Check failed${reason ? `: ${reason}` : ''}`);
  } else {
    if (d.verdict) parts.push(gateVerdictView(d.verdict).label);
    const top = topLevel(d);
    if (top) parts.push(`${levelWord(top).label} ${formatRisk(d.max_risk)}`);
  }
  if (d.gate_ms !== null) parts.push(formatDurationMs(d.gate_ms));
  return parts.join(' · ');
}

/** The id of the "What the user received instead" block; the outcome card links to it. */
export function receivedId(prefix: string): string {
  return `${prefix}received`;
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

/**
 * An evaluator's name: its library name from GET /api/evaluators (by id: "groundedness@1" →
 * "groundedness"), else the humanized id ("Groundedness") while the library is not at hand.
 */
export function evaluatorName(ref: string, names?: ReadonlyMap<string, string>): string {
  const id = ref.split('@')[0];
  return names?.get(id) ?? humanizeId(id);
}

function checkRow(r: EvaluatorResult, names?: ReadonlyMap<string, string>): CheckRow {
  const row = {
    ref: r.ref,
    name: evaluatorName(r.ref, names),
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

/** One row per evaluator, in route order (the decision's key order), named from the library when given. */
export function checkRows(decision: GateDecisionView | null, names?: ReadonlyMap<string, string>): CheckRow[] {
  if (!decision) return [];
  return Object.values(decision.evaluators).map((r) => checkRow(r, names));
}

// ------------------------------------------------------------------------------- detector marks

/**
 * The instant checks' findings to mark in the model's answer: every finding of a fired detector
 * that has a span (a masked secret has none: start -1). Offsets are UTF-16 on the gated text,
 * which is the model's answer (body.response_text).
 */
export function answerFindings(decision: GateDecisionView | null): MarkFinding[] {
  if (!decision) return [];
  return (decision.detectors ?? [])
    .filter((d) => d.fired)
    .flatMap((d) =>
      (d.findings ?? []).filter((f) => f.start >= 0 && f.end > f.start).map((f) => ({ detector: d.id, start: f.start, end: f.end })),
    );
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
