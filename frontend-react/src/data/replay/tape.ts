/**
 * A recording as a tape (SPEC §2.13): what the stream had shown of each trace at any point of the
 * recording's own timeline. Pure: positions are recording milliseconds (`t_ms`), and the same fold
 * rules as streamStore (normalize.ts) turn the recorded events into trace summaries, so REST reads
 * and live events of a replay always agree.
 */
import type {
  AuditDetail,
  GateDecisionView,
  Recording,
  TraceDetail,
  TraceSummary,
  WsEvent,
} from '@/types/api';
import { applyAuditStatus, applyAuditToTrace, mergeTraceSummary } from '../normalize';

export type RecordingEvent = Recording['events'][number];

/** A recorded event whose data has the WS v2 shape of its type. */
export type TapeEvent = Extract<WsEvent, { data: unknown }> extends infer E
  ? E extends { type: infer T; data: infer D }
    ? { t_ms: number; type: T; trace_id?: string; data: D }
    : never
  : never;

interface TraceTimeline {
  final: TraceSummary;
  startedAt: number;
  events: TapeEvent[];
}

export interface TraceState {
  summary: TraceSummary;
  decision: GateDecisionView | null;
  audit: AuditDetail | null;
}

export class ReplayTape {
  readonly recording: Recording;
  /** t_ms of the last event. */
  readonly duration: number;
  readonly events: readonly TapeEvent[];
  private readonly timelines = new Map<string, TraceTimeline>();

  constructor(recording: Recording) {
    this.recording = recording;
    this.events = recording.events as TapeEvent[];
    for (const t of recording.traces) this.timelines.set(t.trace_id, { final: t, startedAt: Number.POSITIVE_INFINITY, events: [] });
    for (const ev of this.events) {
      const id = ev.trace_id ?? traceIdOf(ev);
      const line = id ? this.timelines.get(id) : undefined;
      if (!line) continue;
      line.events.push(ev);
      if (ev.type === 'trace_started') line.startedAt = Math.min(line.startedAt, ev.t_ms);
    }
    // A trace without a trace_started event counts from its first event, else from the start.
    for (const line of this.timelines.values()) {
      if (line.startedAt === Number.POSITIVE_INFINITY) line.startedAt = line.events[0]?.t_ms ?? 0;
    }
    this.duration = this.events.reduce((max, ev) => Math.max(max, ev.t_ms), 0);
  }

  /** The recording's wall-clock time of a position. */
  timeAt(ms: number): string {
    return new Date(Date.parse(this.recording.recorded_at) + ms).toISOString();
  }

  has(id: string): boolean {
    return this.timelines.has(id);
  }

  startedAt(id: string): number | null {
    return this.timelines.get(id)?.startedAt ?? null;
  }

  /** The trace ids in recording order (oldest first). */
  ids(): string[] {
    return [...this.timelines.keys()];
  }

  /** What the stream had shown of a trace at `ms`, or null before it started. */
  stateAt(id: string, ms: number): TraceState | null {
    const line = this.timelines.get(id);
    if (!line || line.startedAt > ms) return null;
    let summary: TraceSummary | undefined;
    let decision: GateDecisionView | null = null;
    let audit: AuditDetail | null = null;
    for (const ev of line.events) {
      if (ev.t_ms > ms) break;
      switch (ev.type) {
        case 'trace_started':
          summary = mergeTraceSummary(summary, ev.data.trace);
          break;
        case 'gate_decision':
          summary = mergeTraceSummary(summary, ev.data.trace);
          decision = ev.data.decision;
          break;
        case 'audit_queued':
          if (summary) summary = applyAuditStatus(summary, { audit_id: ev.data.audit_id, status: 'queued', lane: ev.data.lane });
          break;
        case 'audit_started':
          if (summary) summary = applyAuditStatus(summary, { audit_id: ev.data.audit_id, status: 'running' });
          break;
        case 'audit_completed':
          audit = ev.data.audit;
          if (summary) summary = applyAuditToTrace(summary, audit);
          break;
        case 'audit_failed':
          if (summary) summary = applyAuditStatus(summary, { audit_id: ev.data.audit_id, status: 'failed' });
          break;
        case 'audit_dropped':
          if (summary) summary = applyAuditStatus(summary, { audit_id: ev.data.audit_id, status: 'dropped' });
          break;
        default:
          break;
      }
    }
    // Once every event has played, the manifest's summary is the whole truth.
    const done = line.events.length === 0 || line.events[line.events.length - 1].t_ms <= ms;
    if (done || !summary) summary = line.final;
    return { summary, decision, audit };
  }

  /** Traces started by `ms`, newest first (the order GET /api/traces returns). */
  summariesAt(ms: number): TraceSummary[] {
    const out: TraceSummary[] = [];
    for (const id of this.timelines.keys()) {
      const s = this.stateAt(id, ms);
      if (s) out.push(s.summary);
    }
    return out.sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || (a.trace_id < b.trace_id ? 1 : -1));
  }

  /**
   * A trace file staged at `ms`: its decision and deep audit appear when their events have played.
   * Before the trace started, the whole detail (a deep link always opens the finished request).
   */
  detailAt(detail: TraceDetail, ms: number): TraceDetail {
    const state = this.stateAt(detail.trace_id, ms);
    if (!state) return detail;
    const done = state.summary === this.timelines.get(detail.trace_id)?.final;
    if (done) return detail;
    return {
      ...detail,
      ...state.summary,
      decisions: state.decision ? [state.decision] : [],
      audit_detail: state.audit,
    };
  }

  /** Events with from < t_ms <= to, in recording order. */
  eventsBetween(from: number, to: number): TapeEvent[] {
    return this.events.filter((ev) => ev.t_ms > from && ev.t_ms <= to);
  }
}

function traceIdOf(ev: TapeEvent): string | undefined {
  switch (ev.type) {
    case 'trace_started':
    case 'gate_decision':
      return ev.data.trace.trace_id;
    case 'audit_completed':
      return ev.data.audit.trace_id;
    default:
      return undefined;
  }
}

/** The WS v2 frame of a recorded event, as the gateway's hub would have sent it. */
export function envelope(tape: ReplayTape, ev: TapeEvent, seq: number, epoch: string): Exclude<WsEvent, { type: 'hb' }> {
  const frame = { v: 2 as const, seq, epoch, type: ev.type, ts: tape.timeAt(ev.t_ms), data: ev.data } as Exclude<WsEvent, { type: 'hb' }>;
  const id = ev.trace_id ?? traceIdOf(ev);
  return id ? { ...frame, trace_id: id } : frame;
}
