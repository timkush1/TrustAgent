/**
 * LiveDataSource: the proxy's REST API (SPEC §2.4) plus WS v2 through StreamClient (§2.5).
 */
import type { RuntimeConfig } from '@/config/runtime';
import type {
  AuditDetail,
  AuditSummary,
  GateDecisionRow,
  HealthStatus,
  Page,
  SystemStatus,
  TraceDetail,
  TraceSummary,
} from '@/types/api';
import { createHttpClient, type HttpClient, type Query } from '../http';
import { normalizeHealth, normalizeObject, normalizePage } from '../normalize';
import type {
  AuditListParams,
  DataSource,
  DecisionListParams,
  LiveControl,
  StreamSink,
  TraceListParams,
} from '../source';
import type { ResyncEvent, ResyncReason } from '../stream/types';
import { StreamClient, type StreamClientOptions } from './streamClient';

/** Page size of each resync request (the API maximum). */
export const RESYNC_LIMIT = 200;

export interface LiveDataSourceOptions {
  http?: HttpClient;
  /** Overrides for tests (socket factory, timers, lifecycle). */
  stream?: Partial<Omit<StreamClientOptions, 'url' | 'onEvent' | 'onConnection' | 'resync'>>;
}

function enc(id: string): string {
  return encodeURIComponent(id);
}

export class LiveDataSource implements DataSource {
  readonly kind = 'live' as const;
  private readonly config: Pick<RuntimeConfig, 'apiBase' | 'crossOrigin' | 'wsUrl'>;
  private readonly options: LiveDataSourceOptions;
  private readonly http: HttpClient;
  private client: StreamClient | null = null;

  constructor(config: Pick<RuntimeConfig, 'apiBase' | 'crossOrigin' | 'wsUrl'>, options: LiveDataSourceOptions = {}) {
    this.config = config;
    this.options = options;
    this.http = options.http ?? createHttpClient(config);
  }

  async getHealth(signal?: AbortSignal): Promise<HealthStatus> {
    return normalizeHealth(await this.http.get<unknown>('/health', { signal, timeoutMs: 5000 }));
  }

  async getStatus(signal?: AbortSignal): Promise<SystemStatus> {
    return normalizeObject(await this.http.get<unknown>('/api/status', { signal }), 'GET /api/status', 'systemStatus');
  }

  async listTraces(params: TraceListParams, signal?: AbortSignal): Promise<Page<TraceSummary>> {
    const raw = await this.http.get<unknown>('/api/traces', { query: params as Query, signal });
    return normalizePage(raw, 'GET /api/traces', 'trace');
  }

  async getTrace(id: string, signal?: AbortSignal): Promise<TraceDetail> {
    const raw = await this.http.get<unknown>(`/api/traces/${enc(id)}`, { signal });
    return normalizeObject(raw, 'GET /api/traces/:id', 'traceDetail');
  }

  async listDecisions(params: DecisionListParams, signal?: AbortSignal): Promise<Page<GateDecisionRow>> {
    const raw = await this.http.get<unknown>('/api/gate/decisions', { query: params as Query, signal });
    return normalizePage(raw, 'GET /api/gate/decisions', 'decisionRow');
  }

  async listAudits(params: AuditListParams, signal?: AbortSignal): Promise<Page<AuditSummary>> {
    const raw = await this.http.get<unknown>('/api/audits', { query: params as Query, signal });
    return normalizePage(raw, 'GET /api/audits', 'auditSummary');
  }

  async getAudit(id: string, signal?: AbortSignal): Promise<AuditDetail> {
    const raw = await this.http.get<unknown>(`/api/audits/${enc(id)}`, { signal });
    return normalizeObject(raw, 'GET /api/audits/:id', 'auditDetail');
  }

  subscribe(sink: StreamSink): LiveControl {
    this.client?.dispose();
    const client = new StreamClient({
      ...this.options.stream,
      url: this.config.wsUrl,
      onEvent: (ev) => sink.onEvent(ev),
      onConnection: (state) => sink.onConnection(state),
      resync: (since, reason) => this.resync(since, reason),
    });
    this.client = client;
    client.start();
    return {
      retryNow: () => client.retryNow(),
      stop: () => {
        client.dispose();
        if (this.client === client) this.client = null;
      },
    };
  }

  /**
   * SPEC §2.5 resync: traces by updated_since, decisions by updated_since and audits by
   * completed_since, merged by trace_id in the store. Traces are required; the other two are
   * best effort (an older proxy may not serve them). Rejects when traces cannot be read, so the
   * client retries.
   */
  async resync(since: string, reason: ResyncReason): Promise<ResyncEvent> {
    const [traces, decisions, audits] = await Promise.allSettled([
      this.listTraces({ updated_since: since, limit: RESYNC_LIMIT }),
      this.listDecisions({ updated_since: since, limit: RESYNC_LIMIT }),
      this.listAudits({ completed_since: since, limit: RESYNC_LIMIT }),
    ]);
    if (traces.status === 'rejected') throw traces.reason;
    const items = <T>(r: PromiseSettledResult<Page<T>>): T[] => (r.status === 'fulfilled' ? r.value.items : []);
    const full = <T>(r: PromiseSettledResult<Page<T>>) =>
      r.status === 'fulfilled' && r.value.next_cursor !== null && r.value.items.length >= RESYNC_LIMIT;
    return {
      type: 'resync',
      reason,
      since,
      traces: traces.value.items,
      decisions: items(decisions),
      audits: items(audits),
      truncated: full(traces) || full(decisions) || full(audits),
    };
  }
}
