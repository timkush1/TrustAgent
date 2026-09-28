/**
 * The single normalizer: everything the server sends passes through here before it reaches a
 * store or a component (SPEC §3.3 fixes UI #6, #12, #13).
 *
 * - WS v2 frames: one JSON object per text frame (B5), validated envelope and event data, unknown
 *   types ignored.
 * - REST pages and objects: checked for the fields the dashboard keys on and reads unguarded, so a
 *   pre-v2 body (the legacy `{"audits":[…],"total":…}` list) or an HTML fallback page becomes an
 *   `invalid_response` error instead of a crash.
 * - /health: tolerant of the pre-v2 `{"status":"healthy"}` body.
 * - Trace merging: arrival order never moves a trace backwards (a decided trace never returns to
 *   pending, a finished audit never returns to queued).
 */
import type {
  AuditDetail,
  AuditSummary,
  ClaimVerdict,
  GateDecisionRow,
  GateDecisionView,
  HealthStatus,
  Page,
  SystemStatus,
  TraceAuditStatus,
  TraceDetail,
  TraceSummary,
  WsEvent,
} from '@/types/api';
import { ApiError } from './errors';
import type { LiveEventType } from './stream/types';

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// ----------------------------------------------------------------------------------------- shapes

/**
 * What a wire object must carry before stores and pages use it: the ids they key on and the
 * nested objects they read without a null check. Deliberately not full validation (the golden
 * fixtures and the Go schema tests own that); it keeps one malformed or pre-v2 object from
 * crashing a whole stream batch or page. Lists are read tolerantly: `null` (a Go nil slice) is an
 * empty list, anything else that is not an array fails the object.
 */
interface Shape {
  strings?: readonly string[];
  lists?: readonly string[];
  records?: readonly string[];
}

const TRACE_SHAPE = {
  strings: ['trace_id', 'created_at', 'updated_at'],
  lists: ['reasons'],
  records: ['audit', 'timings'],
} as const satisfies Shape;

const SHAPES = {
  trace: TRACE_SHAPE,
  traceDetail: { ...TRACE_SHAPE, lists: ['reasons', 'decisions'] },
  decision: { strings: ['trace_id'], lists: ['reasons', 'detectors'], records: ['evaluators'] },
  decisionRow: { strings: ['trace_id', 'created_at'], lists: ['reasons'] },
  auditSummary: { strings: ['audit_id', 'trace_id', 'status'] },
  auditDetail: { strings: ['audit_id', 'trace_id', 'status'], lists: ['claims'] },
  systemStatus: { records: ['decision_model', 'engine', 'judge', 'persistence', 'budgets'] },
} as const satisfies Record<string, Shape>;

interface ShapeTypes {
  trace: TraceSummary;
  traceDetail: TraceDetail;
  decision: GateDecisionView;
  decisionRow: GateDecisionRow;
  auditSummary: AuditSummary;
  auditDetail: AuditDetail;
  systemStatus: SystemStatus;
}

export type ShapeKind = keyof ShapeTypes;

/** `v` as a `kind` object (null lists read as empty), or null when it is not one. */
function conform<K extends ShapeKind>(v: unknown, kind: K): ShapeTypes[K] | null {
  const s: Shape = SHAPES[kind];
  if (!isRecord(v)) return null;
  if (!(s.strings ?? []).every((k) => typeof v[k] === 'string')) return null;
  if (!(s.records ?? []).every((k) => isRecord(v[k]))) return null;
  let out = v;
  for (const k of s.lists ?? []) {
    if (Array.isArray(v[k])) continue;
    if (v[k] !== null && v[k] !== undefined) return null;
    if (out === v) out = { ...v };
    out[k] = [];
  }
  return out as unknown as ShapeTypes[K];
}

function unexpectedShape(what: string): ApiError {
  return new ApiError({
    kind: 'invalid_response',
    status: 200,
    code: 'invalid_response',
    message: `The answer to ${what} is not in the format this dashboard reads. The proxy may be an older version.`,
  });
}

/** A keyset page (SPEC §2.4 `{items, next_cursor}`) of `kind` objects; throws ApiError otherwise. */
export function normalizePage<K extends ShapeKind>(raw: unknown, what: string, kind: K): Page<ShapeTypes[K]> {
  if (!isRecord(raw) || !Array.isArray(raw.items)) throw unexpectedShape(what);
  const next = raw.next_cursor;
  if (next !== null && next !== undefined && typeof next !== 'string') throw unexpectedShape(what);
  const items: ShapeTypes[K][] = [];
  for (const item of raw.items) {
    const ok = conform(item, kind);
    if (!ok) throw unexpectedShape(what);
    items.push(ok);
  }
  return { items, next_cursor: typeof next === 'string' && next !== '' ? next : null };
}

/** One `kind` object (a trace or audit detail, the status); throws ApiError otherwise. */
export function normalizeObject<K extends ShapeKind>(raw: unknown, what: string, kind: K): ShapeTypes[K] {
  const ok = conform(raw, kind);
  if (!ok) throw unexpectedShape(what);
  return ok;
}

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
  | { ok: false; reason: 'not_text' | 'invalid_json' | 'not_v2' | 'bad_envelope' | 'unknown_type' }
  /** A known event type whose data the dashboard cannot use (see conformEventData). */
  | { ok: false; reason: 'bad_data'; type: string };

const isSeq = (v: unknown): boolean => typeof v === 'number' && Number.isInteger(v) && v >= 0;

/**
 * The `data` of the events the stream client and streamStore read (SPEC §2.5 events table), or
 * null when it is unusable. The replay cursor comes from `hello` and `gap`, so those are checked
 * strictly; the rest for what streamStore keys on. Types that only trigger a refresh pass as is.
 */
function conformEventData(type: string, d: Record<string, unknown>): Record<string, unknown> | null {
  switch (type) {
    case 'hello':
      return typeof d.epoch === 'string' && d.epoch !== '' && isSeq(d.last_seq) ? d : null;
    case 'gap':
      return isSeq(d.from_seq) && isSeq(d.to_seq) ? d : null;
    case 'trace_started': {
      const trace = conform(d.trace, 'trace');
      return trace && { ...d, trace };
    }
    case 'gate_decision': {
      const trace = conform(d.trace, 'trace');
      const decision = conform(d.decision, 'decision');
      return trace && decision && { ...d, trace, decision };
    }
    case 'audit_completed': {
      const audit = conform(d.audit, 'auditDetail');
      return audit && { ...d, audit };
    }
    case 'audit_queued':
    case 'audit_started':
    case 'audit_failed':
      return typeof d.audit_id === 'string' ? d : null;
    case 'audit_dropped':
      return d.audit_id === null || typeof d.audit_id === 'string' ? d : null;
    case 'status':
      return typeof d.component === 'string' && typeof d.state === 'string' ? d : null;
    default:
      return d;
  }
}

/**
 * Parses one WebSocket frame. The hub sends exactly one JSON object per text frame; anything
 * else (binary, batched lines, v1 frames without `v: 2`) is rejected rather than guessed at, and
 * so is an event whose data the dashboard could not use (it would otherwise break the batch it
 * arrives in).
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
  const eventData = conformEventData(raw.type, raw.data);
  if (!eventData) return { ok: false, reason: 'bad_data', type: raw.type };
  return { ok: true, event: (eventData === raw.data ? raw : { ...raw, data: eventData }) as WsEvent };
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

// ------------------------------------------------------------------- control-plane objects (P3)

/**
 * The shape check for control-plane responses (overview, policies, evaluators, playground,
 * knowledge): the same rules as the stream objects above, plus nesting. `each` checks the objects
 * of a list field and `inner` the object of a record field, so a Go nil slice deep inside a policy
 * state (`downgrades: null`) reads as an empty list instead of crashing a card.
 */
export interface NestedShape {
  strings?: readonly string[];
  lists?: readonly string[];
  records?: readonly string[];
  each?: Readonly<Record<string, NestedShape>>;
  inner?: Readonly<Record<string, NestedShape>>;
}

function conformNested(v: unknown, s: NestedShape): Record<string, unknown> | null {
  if (!isRecord(v)) return null;
  if (!(s.strings ?? []).every((k) => typeof v[k] === 'string')) return null;
  if (!(s.records ?? []).every((k) => isRecord(v[k]))) return null;
  const out: Record<string, unknown> = { ...v };
  for (const k of s.lists ?? []) {
    if (Array.isArray(v[k])) continue;
    if (v[k] !== null && v[k] !== undefined) return null;
    out[k] = [];
  }
  for (const [k, itemShape] of Object.entries(s.each ?? {})) {
    const list = out[k];
    if (!Array.isArray(list)) continue;
    const items: unknown[] = [];
    for (const item of list) {
      const ok = conformNested(item, itemShape);
      if (!ok) return null;
      items.push(ok);
    }
    out[k] = items;
  }
  for (const [k, innerShape] of Object.entries(s.inner ?? {})) {
    if (!isRecord(out[k])) continue;
    const ok = conformNested(out[k], innerShape);
    if (!ok) return null;
    out[k] = ok;
  }
  return out;
}

/** One control-plane object checked against `shape`; throws an `invalid_response` ApiError otherwise. */
export function normalizeShaped<T>(raw: unknown, what: string, shape: NestedShape): T {
  const ok = conformNested(raw, shape);
  if (!ok) throw unexpectedShape(what);
  return ok as unknown as T;
}

/** A keyset page whose items are checked against `item`. */
export function normalizeShapedPage<T>(raw: unknown, what: string, item: NestedShape): Page<T> {
  const page = normalizeShaped<{ items: T[]; next_cursor?: unknown }>(raw, what, { lists: ['items'], each: { items: item } });
  const next = page.next_cursor;
  if (next !== null && next !== undefined && typeof next !== 'string') throw unexpectedShape(what);
  return { items: page.items, next_cursor: typeof next === 'string' && next !== '' ? next : null };
}
