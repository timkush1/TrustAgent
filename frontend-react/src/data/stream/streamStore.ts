/**
 * Live state from the stream (SPEC §3.3): recent traces with their latest decision and audit,
 * connection state, the `hello` flags and component status transitions.
 *
 * - Not persisted. The v1 dashboard's persisted `trustagent-audit-storage` key is deleted on boot.
 * - Keyed by trace_id; merge rules live in normalize.ts, so arrival order does not matter.
 * - Bounded: the newest MAX_TRACES traces are kept (by created_at).
 * - Pause freezes the visible order for the Traffic list; updates to rows already on screen still
 *   apply, new rows are counted ("Paused · 12 new · Resume").
 */
import { create } from 'zustand';
import type {
  AuditDetail,
  GateDecisionView,
  ISODateTime,
  TraceId,
  TraceSummary,
  WsEvent,
  WsHello,
} from '@/types/api';
import {
  applyAuditStatus,
  applyAuditToTrace,
  applyDecisionRow,
  mergeTraceSummary,
} from '../normalize';
import type { ConnectionState, ResyncEvent, StreamEvent } from './types';
import { INITIAL_CONNECTION } from './types';

export const MAX_TRACES = 2000;
export const LEGACY_STORAGE_KEY = 'trustagent-audit-storage';

type StatusEvent = Extract<WsEvent, { type: 'status' }>;
export type ComponentName = StatusEvent['data']['component'];

export interface ComponentTransition {
  state: string;
  detail?: string;
  at: ISODateTime;
}

export interface StreamCounters {
  /** Events lost to ring-buffer overflow before they reached the store. */
  droppedEvents: number;
  gaps: number;
  resyncs: number;
  lastResyncAt: ISODateTime | null;
  /** The last resync hit its page limit, so older changes may be missing (open Traffic for all). */
  resyncTruncated: boolean;
}

export interface StreamState {
  traces: Record<TraceId, TraceSummary>;
  /** Latest decision per trace, from `gate_decision`. */
  decisions: Record<TraceId, GateDecisionView>;
  /** Completed audit per trace, from `audit_completed` (may arrive before its trace). */
  audits: Record<TraceId, AuditDetail>;
  /** audit_id → trace_id for lifecycle events without an envelope trace_id. */
  auditTrace: Record<string, TraceId>;
  /** All known trace ids, newest first. */
  order: TraceId[];
  /** What the Traffic list shows: `order`, frozen while paused. */
  visibleOrder: TraceId[];
  paused: boolean;
  /** New traces that arrived while paused. */
  pendingCount: number;
  hello: WsHello | null;
  components: Partial<Record<ComponentName, ComponentTransition>>;
  connection: ConnectionState;
  counters: StreamCounters;

  ingest(events: StreamEvent[], dropped?: number): void;
  /** Adds REST rows so later stream events update them in place. */
  mergeTraces(items: TraceSummary[]): void;
  setConnection(state: ConnectionState): void;
  pause(): void;
  resume(): void;
  reset(): void;
}

const initialCounters: StreamCounters = {
  droppedEvents: 0,
  gaps: 0,
  resyncs: 0,
  lastResyncAt: null,
  resyncTruncated: false,
};

function initialData() {
  return {
    traces: {} as Record<TraceId, TraceSummary>,
    decisions: {} as Record<TraceId, GateDecisionView>,
    audits: {} as Record<TraceId, AuditDetail>,
    auditTrace: {} as Record<string, TraceId>,
    order: [] as TraceId[],
    visibleOrder: [] as TraceId[],
    paused: false,
    pendingCount: 0,
    hello: null as WsHello | null,
    components: {} as Partial<Record<ComponentName, ComponentTransition>>,
    connection: INITIAL_CONNECTION,
    counters: initialCounters,
  };
}

type Data = ReturnType<typeof initialData>;

/** Mutable working copy for one batch; copies each record at most once. */
class Draft {
  traces: Data['traces'];
  decisions: Data['decisions'];
  audits: Data['audits'];
  auditTrace: Data['auditTrace'];
  hello: Data['hello'];
  components: Data['components'];
  counters: StreamCounters;
  newIds: TraceId[] = [];
  changed = false;
  private readonly base: Data;
  private readonly copied = new Set<string>();

  constructor(base: Data) {
    this.base = base;
    this.traces = base.traces;
    this.decisions = base.decisions;
    this.audits = base.audits;
    this.auditTrace = base.auditTrace;
    this.hello = base.hello;
    this.components = base.components;
    this.counters = base.counters;
  }

  /** The record to mutate in this batch (copied from the committed state on first write). */
  write<K extends 'traces' | 'decisions' | 'audits' | 'auditTrace' | 'components'>(key: K): Data[K] {
    if (!this.copied.has(key)) {
      this.copied.add(key);
      (this as unknown as Record<K, Data[K]>)[key] = { ...this.base[key] };
    }
    this.changed = true;
    return this[key] as Data[K];
  }

  upsertTrace(next: TraceSummary): void {
    const prev = this.traces[next.trace_id];
    let merged = mergeTraceSummary(prev, next);
    const audit = this.audits[next.trace_id];
    if (audit) merged = applyAuditToTrace(merged, audit);
    if (merged.audit.audit_id) this.linkAudit(merged.audit.audit_id, merged.trace_id);
    this.write('traces')[next.trace_id] = merged;
    if (!prev) this.newIds.push(next.trace_id);
  }

  updateTrace(id: TraceId, fn: (t: TraceSummary) => TraceSummary): void {
    const prev = this.traces[id];
    if (!prev) return;
    const next = fn(prev);
    if (next !== prev) this.write('traces')[id] = next;
  }

  linkAudit(auditId: string, traceId: TraceId): void {
    if (this.auditTrace[auditId] !== traceId) this.write('auditTrace')[auditId] = traceId;
  }

  traceForAudit(auditId: string | null, envelopeTraceId: TraceId | undefined): TraceId | undefined {
    return envelopeTraceId ?? (auditId ? this.auditTrace[auditId] : undefined);
  }

  counter(update: Partial<StreamCounters>): void {
    this.counters = { ...this.counters, ...update };
    this.changed = true;
  }
}

function applyEvent(d: Draft, ev: StreamEvent): void {
  switch (ev.type) {
    case 'hello':
      d.hello = ev.data;
      d.changed = true;
      return;
    case 'trace_started':
      d.upsertTrace(ev.data.trace);
      return;
    case 'gate_decision': {
      d.upsertTrace(ev.data.trace);
      const prev = d.decisions[ev.data.trace.trace_id];
      if (!prev || ev.data.decision.attempt >= prev.attempt || ev.data.decision.late) {
        d.write('decisions')[ev.data.trace.trace_id] = ev.data.decision;
      }
      return;
    }
    case 'audit_queued': {
      const id = d.traceForAudit(ev.data.audit_id, ev.trace_id);
      if (!id) return;
      d.linkAudit(ev.data.audit_id, id);
      d.updateTrace(id, (t) => applyAuditStatus(t, { audit_id: ev.data.audit_id, status: 'queued', lane: ev.data.lane }));
      return;
    }
    case 'audit_started': {
      const id = d.traceForAudit(ev.data.audit_id, ev.trace_id);
      if (!id) return;
      d.linkAudit(ev.data.audit_id, id);
      d.updateTrace(id, (t) => applyAuditStatus(t, { audit_id: ev.data.audit_id, status: 'running' }));
      return;
    }
    case 'audit_failed': {
      const id = d.traceForAudit(ev.data.audit_id, ev.trace_id);
      if (!id) return;
      d.updateTrace(id, (t) => applyAuditStatus(t, { audit_id: ev.data.audit_id, status: 'failed' }));
      return;
    }
    case 'audit_dropped': {
      const id = d.traceForAudit(ev.data.audit_id, ev.trace_id);
      if (!id) return;
      d.updateTrace(id, (t) => applyAuditStatus(t, { audit_id: ev.data.audit_id, status: 'dropped' }));
      return;
    }
    case 'audit_completed': {
      const audit = ev.data.audit;
      d.write('audits')[audit.trace_id] = audit;
      d.linkAudit(audit.audit_id, audit.trace_id);
      d.updateTrace(audit.trace_id, (t) => applyAuditToTrace(t, audit));
      return;
    }
    case 'gap':
      d.counter({ gaps: d.counters.gaps + 1 });
      return;
    case 'status':
      d.write('components')[ev.data.component] = {
        state: ev.data.state,
        detail: ev.data.detail,
        at: ev.ts,
      };
      return;
    case 'resync':
      applyResync(d, ev);
      return;
    default:
      // Policy, evaluator, calibration, settings and credential events drive query
      // invalidation (liveStream.ts), not this store.
      return;
  }
}

function applyResync(d: Draft, ev: ResyncEvent): void {
  for (const t of ev.traces) d.upsertTrace(t);
  for (const row of ev.decisions) d.updateTrace(row.trace_id, (t) => applyDecisionRow(t, row));
  for (const a of ev.audits) {
    d.linkAudit(a.audit_id, a.trace_id);
    d.updateTrace(a.trace_id, (t) => applyAuditToTrace(t, a));
  }
  d.counter({
    resyncs: d.counters.resyncs + 1,
    lastResyncAt: new Date().toISOString(),
    resyncTruncated: ev.truncated,
  });
}

function createdAt(t: TraceSummary | undefined): number {
  const v = t ? Date.parse(t.created_at) : Number.NaN;
  return Number.isNaN(v) ? 0 : v;
}

/** Re-sorts `order` with the new ids and evicts the oldest past MAX_TRACES. */
function nextOrder(d: Draft, order: TraceId[]): { order: TraceId[]; evicted: TraceId[] } {
  if (d.newIds.length === 0) return { order, evicted: [] };
  const merged = [...order, ...d.newIds];
  merged.sort((a, b) => createdAt(d.traces[b]) - createdAt(d.traces[a]));
  if (merged.length <= MAX_TRACES) return { order: merged, evicted: [] };
  return { order: merged.slice(0, MAX_TRACES), evicted: merged.slice(MAX_TRACES) };
}

function evict(d: Draft, ids: TraceId[]): void {
  if (ids.length === 0) return;
  const drop = new Set(ids);
  const traces = d.write('traces');
  const decisions = d.write('decisions');
  const audits = d.write('audits');
  for (const id of drop) {
    delete traces[id];
    delete decisions[id];
    delete audits[id];
  }
  const auditTrace = d.write('auditTrace');
  for (const [auditId, traceId] of Object.entries(auditTrace)) if (drop.has(traceId)) delete auditTrace[auditId];
}

/** Audits whose trace never arrived are bounded too. */
function boundOrphanAudits(d: Draft): void {
  const ids = Object.keys(d.audits);
  let count = ids.length;
  if (count <= MAX_TRACES) return;
  const audits = d.write('audits');
  for (const id of ids) {
    if (count <= MAX_TRACES) break;
    if (!d.traces[id]) {
      delete audits[id];
      count -= 1;
    }
  }
}

export const useStreamStore = create<StreamState>()((set, get) => {
  function commit(apply: (d: Draft) => void, dropped = 0): void {
    const state = get();
    const d = new Draft(state);
    apply(d);
    if (dropped > 0) d.counter({ droppedEvents: d.counters.droppedEvents + dropped });
    if (!d.changed && d.newIds.length === 0) return;
    const { order, evicted } = nextOrder(d, state.order);
    evict(d, evicted);
    boundOrphanAudits(d);
    const added = d.newIds.filter((id) => d.traces[id]).length;
    set({
      traces: d.traces,
      decisions: d.decisions,
      audits: d.audits,
      auditTrace: d.auditTrace,
      hello: d.hello,
      components: d.components,
      counters: d.counters,
      order,
      visibleOrder: state.paused ? state.visibleOrder.filter((id) => d.traces[id]) : order,
      pendingCount: state.paused ? Math.min(MAX_TRACES, state.pendingCount + added) : 0,
    });
  }

  return {
    ...initialData(),
    ingest(events, dropped = 0) {
      commit((d) => {
        for (const ev of events) applyEvent(d, ev);
      }, dropped);
    },
    mergeTraces(items) {
      commit((d) => {
        for (const t of items) d.upsertTrace(t);
      });
    },
    setConnection(connection) {
      set({ connection });
    },
    pause() {
      if (!get().paused) set({ paused: true, pendingCount: 0 });
    },
    resume() {
      set((s) => ({ paused: false, pendingCount: 0, visibleOrder: s.order }));
    },
    reset() {
      set(initialData());
    },
  };
});

/** Deletes the v1 dashboard's persisted audit list (SPEC §3.3; UI #10). */
export function clearLegacyStorage(storage: Pick<Storage, 'removeItem'> | null = safeLocalStorage()): void {
  try {
    storage?.removeItem(LEGACY_STORAGE_KEY);
  } catch {
    // Storage blocked: nothing to clean.
  }
}

function safeLocalStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

// ------------------------------------------------------------------------------------ selectors

/** Traces in Traffic-list order (frozen while paused). */
export function selectVisibleTraces(s: StreamState): TraceSummary[] {
  const out: TraceSummary[] = [];
  for (const id of s.visibleOrder) {
    const t = s.traces[id];
    if (t) out.push(t);
  }
  return out;
}

/**
 * Combines a REST page with live traces: live versions win per trace_id (merged), live-only
 * traces newer than the page's newest row are prepended. Pure; the caller memoizes.
 */
export function mergeTraceLists(live: readonly TraceSummary[], rest: readonly TraceSummary[]): TraceSummary[] {
  const liveById = new Map(live.map((t) => [t.trace_id, t]));
  const newestRest = rest.reduce((max, t) => Math.max(max, createdAt(t)), 0);
  const restIds = new Set(rest.map((t) => t.trace_id));
  const head = live.filter((t) => !restIds.has(t.trace_id) && createdAt(t) >= newestRest);
  const body = rest.map((t) => {
    const l = liveById.get(t.trace_id);
    return l ? mergeTraceSummary(t, l) : t;
  });
  return [...head, ...body];
}
