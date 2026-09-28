/**
 * Events that flow from the StreamClient through the ring buffer into streamStore.
 */
import type { AuditSummary, GateDecisionRow, TraceSummary, WsEvent } from '@/types/api';

/** A WS v2 event that carries data (`hb` never leaves the client). */
export type LiveEvent = Exclude<WsEvent, { type: 'hb' }>;
export type LiveEventType = LiveEvent['type'];

export type ResyncReason = 'gap' | 'epoch_changed' | 'seq_hole' | 'reconnect';

/**
 * REST catch-up after a gap (SPEC §2.5 client algorithm), merged by trace_id like live events.
 * `truncated` means a page came back full, so older changes may still be missing.
 */
export interface ResyncEvent {
  type: 'resync';
  reason: ResyncReason;
  since: string;
  traces: TraceSummary[];
  decisions: GateDecisionRow[];
  audits: AuditSummary[];
  truncated: boolean;
}

export type StreamEvent = LiveEvent | ResyncEvent;

/** Connection phase shown by the Health pill and the Traffic live toggle. */
export type ConnectionPhase = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'unauthorized';

export interface ConnectionState {
  phase: ConnectionPhase;
  /** Consecutive failed attempts since the last `hello`. */
  attempt: number;
  /** Epoch ms of the next scheduled attempt while reconnecting. */
  nextRetryAt: number | null;
  /** Close code of the last disconnect (4401 unauthorized, 4429 over the connection cap). */
  lastCloseCode: number | null;
}

export const INITIAL_CONNECTION: ConnectionState = {
  phase: 'idle',
  attempt: 0,
  nextRetryAt: null,
  lastCloseCode: null,
};
