/**
 * The single normalizer: everything the server sends passes through here before it reaches a
 * store or a component (SPEC §3.3 fixes UI #6, #12, #13).
 *
 * - WS v2 frames: one JSON object per text frame (B5), validated envelope, unknown types ignored.
 * - /health: tolerant of the pre-v2 `{"status":"healthy"}` body.
 * - Trace merging: arrival order never moves a trace backwards (a decided trace never returns to
 *   pending, a finished audit never returns to queued).
 */
import type {
  AuditSummary,
  ClaimVerdict,
  GateDecisionRow,
  HealthStatus,
  TraceAuditStatus,
  TraceSummary,
  WsEvent,
} from '@/types/api';
import type { LiveEventType } from './stream/types';

// ------------------------------------------------------------------------------------------ WS v2

/** Every WS v2 event type (SPEC §2.5). The `satisfies` clause fails to compile if one is missing. */
const WS_TYPES = {
  hello: true,
  hb: true,
  trace_started: true,
  gate_decision: true,
  audit_queued: true,
  audit_started: true,
  audit_completed: true,
  audit_failed: true,
  audit_dropped: true,
  gap: true,
  policy_updated: true,
  policy_error: true,
  evaluator_updated: true,
  calibration_updated: true,
  decision_model_changed: true,
  status: true,
  credentials_changed: true,
  settings_updated: true,
} as const satisfies Record<WsEvent['type'], true>;

export type FrameResult =
  | { ok: true; event: WsEvent }
  | { ok: false; reason: 'not_text' | 'invalid_json' | 'not_v2' | 'bad_envelope' | 'unknown_type' };

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * Parses one WebSocket frame. The hub sends exactly one JSON object per text frame; anything
 * else (binary, batched lines, v1 frames without `v: 2`) is rejected rather than guessed at.
 */
export function parseWsFrame(data: unknown): FrameResult {
  if (typeof data !== 'string') return { ok: false, reason: 'not_text' };
  let raw: unknown;
  try {
    raw = JSON.parse(data);
  } catch {
    return { ok: false, reason: 'invalid_json' };
  }
  if (!isRecord(raw) || raw.v !== 2) return { ok: false, reason: 'not_v2' };
  if (typeof raw.type !== 'string' || typeof raw.ts !== 'string') return { ok: false, reason: 'bad_envelope' };
  if (!Object.hasOwn(WS_TYPES, raw.type)) return { ok: false, reason: 'unknown_type' };
  if (raw.type === 'hb') return { ok: true, event: raw as WsEvent };
  if (
    typeof raw.seq !== 'number' ||
    !Number.isInteger(raw.seq) ||
    typeof raw.epoch !== 'string' ||
    !isRecord(raw.data) ||
    (raw.trace_id !== undefined && typeof raw.trace_id !== 'string')
  ) {
    return { ok: false, reason: 'bad_envelope' };
  }
  return { ok: true, event: raw as WsEvent };
}

export const LIVE_EVENT_TYPES = Object.keys(WS_TYPES).filter((t) => t !== 'hb') as LiveEventType[];

// ----------------------------------------------------------------------------------------- health

/**
 * GET /health. `demo` defaults to false and `audit_engine` to false when absent, and a legacy
 * `"healthy"` status counts as ok.
 */
export function normalizeHealth(raw: unknown): HealthStatus {
  if (!isRecord(raw)) throw new Error('The /health response is not a JSON object.');
  const status = raw.status;
  if (status !== 'ok' && status !== 'healthy') throw new Error(`/health reported status ${JSON.stringify(status)}.`);
  return {
    status: 'ok',
    version: typeof raw.version === 'string' ? raw.version : 'unknown',
    demo: raw.demo === true,
    audit_engine: raw.audit_engine === true,
  };
}

// ---------------------------------------------------------------------------------------- verdicts

const CLAIM_VERDICTS = new Set<ClaimVerdict>([
  'SUPPORTED',
  'PARTIALLY_SUPPORTED',
  'CONTRADICTED',
  'UNSUPPORTED',
  'UNKNOWN',
  'NOT_CHECKED',
]);

/** Any unexpected verdict string (e.g. a proto UNSPECIFIED) becomes UNKNOWN. */
export function normalizeClaimVerdict(v: unknown): ClaimVerdict {
  return typeof v === 'string' && CLAIM_VERDICTS.has(v as ClaimVerdict) ? (v as ClaimVerdict) : 'UNKNOWN';
}

// ------------------------------------------------------------------------------------------ traces

/** How far along a trace's deep audit is; terminal states all rank 3. */
export function auditRank(status: TraceAuditStatus): number {
  switch (status) {
    case 'none':
      return 0;
    case 'queued':
      return 1;
    case 'running':
      return 2;
    default:
      return 3;
  }
}

const GATE_FIELDS = [
  'effective_mode',
  'action',
  'would_action',
  'gate_status',
  'skip_reason',
  'verdict',
  'max_risk',
  'reasons',
  'checked_against',
  'timings',
  'decision_model',
] as const satisfies readonly (keyof TraceSummary)[];

function time(iso: string): number {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
}

/**
 * Merges two versions of one trace. The later `updated_at` wins (ties go to `next`), except that
 * gate fields never return to pending and the audit never moves back to an earlier state.
 */
export function mergeTraceSummary(prev: TraceSummary | undefined, next: TraceSummary): TraceSummary {
  if (!prev) return next;
  const nextIsNewer = time(next.updated_at) >= time(prev.updated_at);
  const newer = nextIsNewer ? next : prev;
  const older = nextIsNewer ? prev : next;
  const merged: TraceSummary = { ...newer };
  if (newer.gate_status === 'pending' && older.gate_status !== 'pending') {
    for (const k of GATE_FIELDS) (merged as unknown as Record<string, unknown>)[k] = older[k];
  }
  if (auditRank(older.audit.status) > auditRank(newer.audit.status)) {
    merged.audit = older.audit;
  }
  return merged;
}

/** Folds an audit (summary or detail) into its trace, unless the trace already knows more. */
export function applyAuditToTrace(trace: TraceSummary, audit: AuditSummary): TraceSummary {
  const status: TraceAuditStatus = audit.status;
  if (auditRank(trace.audit.status) > auditRank(status)) return trace;
  if (trace.audit.audit_id && trace.audit.audit_id !== audit.audit_id && auditRank(trace.audit.status) >= auditRank(status)) {
    return trace;
  }
  return {
    ...trace,
    audit: {
      ...trace.audit,
      audit_id: audit.audit_id,
      status,
      selection: audit.selection ?? trace.audit.selection,
      verdict: audit.verdict,
      trust_score: audit.trust_score,
      grade: audit.grade,
      verdict_counts: audit.verdict_counts,
    },
  };
}

/** Sets the audit status from a lifecycle event (queued, started, failed, dropped). */
export function applyAuditStatus(
  trace: TraceSummary,
  update: { audit_id: string | null; status: TraceAuditStatus; lane?: 'high' | 'low' },
): TraceSummary {
  if (auditRank(trace.audit.status) > auditRank(update.status)) return trace;
  if (auditRank(trace.audit.status) === 3) return trace;
  return {
    ...trace,
    audit: {
      ...trace.audit,
      audit_id: update.audit_id ?? trace.audit.audit_id,
      status: update.status,
      lane: update.lane ?? trace.audit.lane,
    },
  };
}

/** Folds a decision row (REST resync) into a known trace. */
export function applyDecisionRow(trace: TraceSummary, row: GateDecisionRow): TraceSummary {
  if (trace.gate_status !== 'pending' && time(trace.updated_at) > time(row.created_at) && !row.late) return trace;
  return {
    ...trace,
    effective_mode: row.effective_mode,
    action: row.action,
    would_action: row.would_action,
    gate_status: row.status,
    verdict: row.verdict,
    max_risk: row.max_risk,
    reasons: row.reasons,
  };
}
