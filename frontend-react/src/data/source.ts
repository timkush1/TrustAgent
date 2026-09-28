/**
 * DataSource: everything the pages read, behind one interface (SPEC §3.3). LiveDataSource talks
 * to the proxy (REST + WS v2); ReplayDataSource (static build, P3/P5) replays a recording through
 * the same interface, so pages never know which one they have.
 */
import type {
  AuditDetail,
  AuditSelection,
  AuditStatus,
  AuditSummary,
  AuditVerdict,
  EvaluatorLevel,
  GateAction,
  GateDecisionRow,
  GateStatus,
  GateVerdict,
  HealthStatus,
  Page,
  SystemStatus,
  TraceAuditStatus,
  TraceDetail,
  TraceSource,
  TraceSummary,
} from '@/types/api';
import type { OutcomeId } from '@/domain/contracts';
import type { ConnectionState, StreamEvent } from './stream/types';

export interface PageParams {
  /** 1-200, server default 50. */
  limit?: number;
  cursor?: string;
}

/** GET /api/traces (SPEC §2.4 #10). `q` requires `since` within 7 days. */
export interface TraceListParams extends PageParams {
  since?: string;
  until?: string;
  updated_since?: string;
  route?: string;
  model?: string;
  action?: GateAction;
  gate_status?: GateStatus | 'pending';
  verdict?: GateVerdict;
  audit_status?: TraceAuditStatus;
  audit_verdict?: AuditVerdict;
  source?: TraceSource;
  outcome?: OutcomeId;
  q?: string;
}

/** GET /api/gate/decisions (#16). */
export interface DecisionListParams extends PageParams {
  since?: string;
  updated_since?: string;
  route?: string;
  action?: GateAction;
  status?: GateStatus;
  evaluator?: string;
  level?: EvaluatorLevel;
  disagreement?: boolean;
  late?: boolean;
}

/** GET /api/audits (#13). */
export interface AuditListParams extends PageParams {
  since?: string;
  completed_since?: string;
  route?: string;
  source?: TraceSource;
  status?: AuditStatus;
  verdict?: AuditVerdict;
  selection?: AuditSelection;
}

/** Receives live events and connection changes. */
export interface StreamSink {
  onEvent(event: StreamEvent): void;
  onConnection(state: ConnectionState): void;
}

export interface LiveControl {
  /** Reconnect now if disconnected ("Retry now", online, tab visible). */
  retryNow(): void;
  stop(): void;
}

export interface DataSource {
  readonly kind: 'live' | 'replay';
  getHealth(signal?: AbortSignal): Promise<HealthStatus>;
  getStatus(signal?: AbortSignal): Promise<SystemStatus>;
  listTraces(params: TraceListParams, signal?: AbortSignal): Promise<Page<TraceSummary>>;
  getTrace(id: string, signal?: AbortSignal): Promise<TraceDetail>;
  listDecisions(params: DecisionListParams, signal?: AbortSignal): Promise<Page<GateDecisionRow>>;
  listAudits(params: AuditListParams, signal?: AbortSignal): Promise<Page<AuditSummary>>;
  getAudit(id: string, signal?: AbortSignal): Promise<AuditDetail>;
  /** Starts delivering live events to `sink`. One subscription per page (see liveStream.ts). */
  subscribe(sink: StreamSink): LiveControl;
}
