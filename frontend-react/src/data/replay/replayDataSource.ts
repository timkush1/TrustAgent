/**
 * ReplayDataSource (SPEC §3.3): the static build's DataSource. It serves a recording
 * (served at demo/ from demo-data/, trustagent.recording/v1; D110) through the same interface as the live gateway,
 * so Overview, Traffic and Trace render unchanged:
 *
 * - REST reads answer from the tape at its current position: traces, decisions and audits the
 *   stream has "shown" so far, plus Overview, ROI (summary and time series) and evidence-coverage
 *   numbers computed from them, and the built-in price table,
 *   labelled as the recording allows (a mock recording is simulated throughout).
 * - subscribe() plays the rest of the tape as WS v2 frames in compressed time (VITE_REPLAY_SPEED),
 *   after a `hello` that marks the stream read-only. The first half of the traces is already on
 *   screen when the page opens, so Traffic is never empty; the second half arrives live.
 * - Evidence suggestions run the gateway's scan in the browser over the answers that carried no
 *   evidence (suggest.ts), and "Show exact Jev request" rebuilds a decision's requests with TS
 *   compile() (glassBox.ts, D109); both load on demand.
 * - Everything a recording cannot answer (Policies, Settings, Knowledge, the Playground's gateway
 *   run, evaluator details) comes from unavailableControlPlane with a message that points to
 *   running the stack locally.
 */
import type {
  AuditDetail,
  AuditSummary,
  GateDecisionRow,
  HealthStatus,
  Page,
  PricingTable,
  SystemStatus,
  TraceDetail,
  TraceSummary,
  WsHello,
} from '@/types/api';
import { unavailableControlPlane, type ControlPlane } from '../controlPlane';
import { ApiError } from '../errors';
import type { AuditListParams, DataSource, DecisionListParams, LiveControl, PageParams, StreamSink, TraceListParams } from '../source';
import { hasReason } from '@/components/traffic/trafficRows';
import { outcomeOf } from '@/domain/presentation';
import { browserClock, DEFAULT_MAX_GAP_MS, ReplayPlayer, type ReplayClock } from './player';
import { loadRecording, type DemoDataset, type RecordingFiles } from './recording';
import { INITIAL_REPLAY, useReplayStore } from './replayStore';
import { replayEvaluatorReads } from './replayEvaluatorReads';
import { replayStudioPlane, type ReplayPlaneDeps } from './replayStudioPlane';
import pricingTable from '@contracts/pricing/prices.v1.json';
import { evidenceCoverage, overviewStats, roiSummary, roiTimeseries, type PlayedTrace } from './stats';
import { envelope, ReplayTape } from './tape';

export const STATIC_UNAVAILABLE =
  'The static demo replays a recording, so there is no gateway to ask. Run it yourself with `make demo` (Docker only, no keys).';

export interface ReplayOptions {
  /** The site's base path (config.basePath), e.g. "/TrustAgent/". */
  basePath: string;
  fetchImpl?: typeof fetch;
  /** VITE_REPLAY_SPEED: recording time per real time (default 20). */
  speed?: number;
  maxGapMs?: number;
  /** VITE_DEMO_DATASET. */
  dataset?: DemoDataset;
  clock?: ReplayClock;
  /** Share of the traces already on screen when the page opens (0-1, default 0.5). */
  preroll?: number;
}

export interface ReplayDataSource extends DataSource {
  readonly kind: 'replay';
  /** Resolves with the tape once manifest.json is loaded (rejects with an ApiError). */
  readonly ready: Promise<ReplayTape>;
  /** Plays the tape again from the preroll point (the caller clears streamStore and caches). */
  restart(): void;
}

const DEFAULT_LIMIT = 50;
const MAX_LIMIT = 200;

function page<T>(items: readonly T[], params: PageParams): Page<T> {
  const limit = Math.min(Math.max(params.limit ?? DEFAULT_LIMIT, 1), MAX_LIMIT);
  const offset = params.cursor ? Math.max(Number.parseInt(params.cursor, 10) || 0, 0) : 0;
  const slice = items.slice(offset, offset + limit);
  return { items: slice, next_cursor: offset + limit < items.length ? String(offset + limit) : null };
}

function after(iso: string | undefined, value: string): boolean {
  return !iso || Date.parse(value) >= Date.parse(iso);
}

function matchesTrace(t: TraceSummary, p: TraceListParams): boolean {
  if (p.route && t.route !== p.route) return false;
  if (p.model && t.model !== p.model) return false;
  if (p.action && t.action !== p.action) return false;
  if (p.gate_status && t.gate_status !== p.gate_status) return false;
  if (p.verdict && t.verdict !== p.verdict) return false;
  if (p.audit_status && t.audit.status !== p.audit_status) return false;
  if (p.audit_verdict && t.audit.verdict !== p.audit_verdict) return false;
  if (p.source && t.source !== p.source) return false;
  if (p.outcome && outcomeOf(t) !== p.outcome) return false;
  // A whole code, or every code a colon-ended prefix starts (D68). The proxy needs `since` with it;
  // the replay reads it over the whole recording.
  if (p.reason && !hasReason(t.reasons, p.reason)) return false;
  if (!after(p.since, t.created_at) || !after(p.updated_since, t.updated_at)) return false;
  if (p.until && Date.parse(t.created_at) >= Date.parse(p.until)) return false;
  if (p.q) {
    const q = p.q.toLowerCase();
    if (![t.question_preview, t.response_preview, t.trace_id].some((s) => s?.toLowerCase().includes(q))) return false;
  }
  return true;
}

function auditSummary(a: AuditDetail): AuditSummary {
  return {
    audit_id: a.audit_id,
    trace_id: a.trace_id,
    created_at: a.created_at,
    route: a.route,
    source: a.source,
    model: a.model,
    status: a.status,
    verdict: a.verdict,
    not_assessed_reason: a.not_assessed_reason,
    trust_score: a.trust_score,
    grade: a.grade,
    hallucination_detected: a.hallucination_detected,
    verdict_counts: a.verdict_counts,
    processing_ms: a.processing_ms,
    judge: a.judge,
    gate_action: a.gate_action,
    selection: a.selection,
    inclusion_prob: a.inclusion_prob,
    query_preview: a.query_preview,
    redacted: a.redacted,
  };
}

function decisionRow(p: PlayedTrace): GateDecisionRow | null {
  const d = p.decision;
  if (!d) return null;
  return {
    trace_id: d.trace_id,
    created_at: d.created_at,
    route: d.route,
    mode: d.mode,
    effective_mode: d.effective_mode,
    action: d.action,
    would_action: d.would_action,
    status: d.status,
    verdict: d.verdict,
    max_risk: d.max_risk,
    reasons: d.reasons,
    late: d.late,
    decision_snapshot: d.decision_model?.snapshot ?? null,
    decision_ms: d.decision_model?.latency_ms ?? null,
    cost_usd: d.decision_model?.cost_usd ?? null,
    audit_verdict: p.audit?.verdict ?? null,
    evaluator_levels: Object.fromEntries(Object.entries(d.evaluators).map(([ref, e]) => [ref, e.level])),
  };
}

/** The first flagged or blocked answer the deep audit found contradicted (the refund example). */
export function featuredTrace(tape: ReplayTape): string | null {
  const traces = tape.recording.traces;
  const caught = traces.filter((t) => t.action === 'flag' || t.action === 'block' || t.action === 'fallback');
  const contradicted = caught.find((t) => t.verdict === 'contradicted' || t.audit.verdict === 'HALLUCINATED');
  return (contradicted ?? caught[0] ?? traces[0])?.trace_id ?? null;
}

function envSpeed(): number {
  const raw = Number(import.meta.env.VITE_REPLAY_SPEED);
  return Number.isFinite(raw) && raw > 0 ? raw : 20;
}

export function createReplayDataSource(opts: ReplayOptions): ReplayDataSource {
  const fetchImpl = opts.fetchImpl ?? globalThis.fetch.bind(globalThis);
  const speed = opts.speed ?? envSpeed();
  const clock = opts.clock ?? browserClock;
  const store = useReplayStore.getState();
  store.set({ ...INITIAL_REPLAY, status: 'loading', speed });

  let files: RecordingFiles | null = null;
  let tape: ReplayTape | null = null;
  let preroll = 0;
  let position = 0;
  let player: ReplayPlayer | null = null;
  let sink: StreamSink | null = null;
  let seq = 0;

  const shownAt = (t: ReplayTape, ms: number) => t.ids().filter((id) => (t.startedAt(id) ?? Number.POSITIVE_INFINITY) <= ms).length;

  const ready: Promise<ReplayTape> = loadRecording(opts.basePath, fetchImpl, opts.dataset ?? 'auto').then(
    (loaded) => {
      files = loaded;
      tape = new ReplayTape(loaded.manifest);
      const ids = tape.ids();
      const share = Math.min(Math.max(opts.preroll ?? 0.5, 0), 1);
      const firstLive = ids[Math.floor(ids.length * share)];
      const liveFrom = firstLive === undefined ? tape.duration : (tape.startedAt(firstLive) ?? 0);
      preroll = share >= 1 ? tape.duration : Math.max(liveFrom - 1, 0);
      position = preroll;
      useReplayStore.getState().set({
        status: 'ready',
        kind: loaded.manifest.kind,
        label: loaded.manifest.label,
        recordedAt: loaded.manifest.recorded_at,
        traces: loaded.manifest.traces.length,
        duration: tape.duration,
        preroll,
        position,
        shown: shownAt(tape, position),
        featuredTraceId: featuredTrace(tape),
        restart,
      });
      return tape;
    },
    (err: unknown) => {
      const message = err instanceof Error ? err.message : String(err);
      useReplayStore.getState().set({ status: 'error', error: message });
      throw err;
    },
  );
  // Callers see the rejection through their own awaits; this one only keeps it from being unhandled.
  ready.catch(() => undefined);

  const played = (t: ReplayTape): PlayedTrace[] =>
    t.ids().flatMap((id) => {
      const s = t.stateAt(id, position);
      return s ? [s] : [];
    });
  const ctx = (t: ReplayTape, window: Parameters<ControlPlane['getRoiSummary']>[0]) => ({
    recording: t.recording,
    now: t.timeAt(position),
    end: t.timeAt(t.duration),
    window,
  });

  function hello(t: ReplayTape): void {
    if (!sink) return;
    const m = t.recording;
    seq = t.events.filter((ev) => ev.t_ms <= position).length;
    const data: WsHello = {
      protocol: 2,
      server_version: m.trustagent_version,
      epoch: `replay-${m.recorded_at}`,
      last_seq: seq,
      replayed_from: null,
      demo: m.kind === 'mock',
      readonly: true,
      status: { version: m.trustagent_version, demo: m.kind === 'mock', readonly: true, engine: 'unknown', decision_model: m.kind === 'mock' ? 'mock' : 'ready', judge: 'unknown' },
    };
    sink.onEvent({ v: 2, seq, epoch: data.epoch, type: 'hello', ts: t.timeAt(position), data });
  }

  function play(t: ReplayTape): void {
    if (!sink) return;
    const epoch = `replay-${t.recording.recorded_at}`;
    sink.onConnection({ phase: 'open', attempt: 0, nextRetryAt: null, lastCloseCode: null });
    hello(t);
    player?.stop();
    player = new ReplayPlayer(t, { speed, maxGapMs: opts.maxGapMs ?? DEFAULT_MAX_GAP_MS, clock }, {
      onEvent(ev) {
        position = Math.max(position, ev.t_ms);
        seq += 1;
        sink?.onEvent(envelope(t, ev, seq, epoch));
      },
      onPosition(ms) {
        position = Math.max(position, ms);
        useReplayStore.getState().set({ position, shown: shownAt(t, position), playing: player?.playing ?? false });
      },
      onEnd() {
        useReplayStore.getState().set({ position, playing: false, finished: true });
      },
    });
    player.start(position);
    useReplayStore.getState().set({ playing: true, finished: false });
  }

  function restart(): void {
    if (!tape) return;
    player?.stop();
    position = preroll;
    useReplayStore.getState().set({ position, shown: shownAt(tape, position), finished: false });
    play(tape);
  }

  // The Studio plane and the evaluator reads answer from the tape and the browser (WEB-STUDIO).
  const planeDeps: ReplayPlaneDeps = {
    message: STATIC_UNAVAILABLE,
    traceIds: async () => (await ready).ids(),
    trace: (id, signal) => source.getTrace(id, signal),
  };

  const control: ControlPlane = {
    ...unavailableControlPlane(STATIC_UNAVAILABLE),
    ...replayStudioPlane(planeDeps),
    ...replayEvaluatorReads(planeDeps),
    async getOverview(params) {
      const t = await ready;
      return overviewStats(played(t), ctx(t, params.window));
    },
    async getRoiSummary(window) {
      const t = await ready;
      return roiSummary(played(t), ctx(t, window));
    },
    async getRoiTimeseries(params) {
      const t = await ready;
      // A recording is one fixed stretch of minutes to hours, so the series comes in the bucket
      // that fits the whole recording (per hour for the two-hour simulated tape), whatever the
      // window asks for and however far the tape has played.
      return roiTimeseries(played(t), ctx(t, params.window));
    },
    async getDecisionRequest(traceId, params, signal) {
      const t = await ready;
      const detail = await source.getTrace(traceId, signal);
      // The glass box rebuilds the request in the browser (D109): compile() and the templates load
      // only when the panel opens.
      const [{ rebuildDecisionRequest }, { policy }] = await Promise.all([import('./glassBox'), import('virtual:trustagent/policy')]);
      return rebuildDecisionRequest(detail, params?.attempt ?? 0, policy, t.recording.decision_model.snapshot);
    },
    async getPricing() {
      // A recording has no dashboard overrides: the built-in table is the effective one.
      return structuredClone(pricingTable) as PricingTable;
    },
    async getEvidenceCoverage(params) {
      const t = await ready;
      return evidenceCoverage(played(t), ctx(t, params.window), params.route);
    },
    async getEvidenceSuggestions(params, signal) {
      const t = await ready;
      // The gateway's scan (D64) over the played answers that carried no evidence, most recent
      // first; none gives no suggestion, and the card says so.
      const untagged = played(t)
        .map((p) => p.summary)
        .filter((s) => (!params.route || s.route === params.route) && s.source !== 'direct' && (s.checked_against?.used ?? []).length === 0)
        .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || (a.trace_id < b.trace_id ? 1 : -1));
      if (untagged.length === 0) return { items: [] };
      const [{ MAX_SUGGEST_SCAN, policyEvidenceSettings, suggestEvidence }, { policy }] = await Promise.all([
        import('./suggest'),
        import('virtual:trustagent/policy'),
      ]);
      const details = await Promise.all(untagged.slice(0, MAX_SUGGEST_SCAN).map((s) => source.getTrace(s.trace_id, signal)));
      const bodies = details
        .filter((d) => d.body.available)
        .map((d) => ({ traceId: d.trace_id, route: d.route, messages: d.body.messages }));
      return { items: suggestEvidence(bodies, policyEvidenceSettings(policy)) };
    },
  };

  const source: ReplayDataSource = {
    ...control,
    kind: 'replay',
    ready,
    restart,

    async getHealth(): Promise<HealthStatus> {
      const t = await ready;
      return { status: 'ok', version: t.recording.trustagent_version, demo: t.recording.kind === 'mock', audit_engine: false };
    },

    async getStatus(): Promise<SystemStatus> {
      throw new ApiError({ kind: 'http', status: 404, code: 'not_found', message: STATIC_UNAVAILABLE });
    },

    async listTraces(params: TraceListParams): Promise<Page<TraceSummary>> {
      const t = await ready;
      return page(t.summariesAt(position).filter((s) => matchesTrace(s, params)), params);
    },

    async getTrace(id: string, signal?: AbortSignal): Promise<TraceDetail> {
      const t = await ready;
      if (!files || !t.has(id)) {
        throw new ApiError({ kind: 'http', status: 404, code: 'not_found', message: 'This recording has no request with that id.' });
      }
      return t.detailAt(await files.detail(id, signal), position);
    },

    async listDecisions(params: DecisionListParams): Promise<Page<GateDecisionRow>> {
      const t = await ready;
      const rows = played(t)
        .map(decisionRow)
        .filter((r): r is GateDecisionRow => r !== null)
        .filter((r) => (!params.route || r.route === params.route) && (!params.action || r.action === params.action) && (!params.status || r.status === params.status))
        .filter((r) => after(params.since, r.created_at) && after(params.updated_since, r.created_at))
        .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
      return page(rows, params);
    },

    async listAudits(params: AuditListParams): Promise<Page<AuditSummary>> {
      const t = await ready;
      const rows = played(t)
        .map((p) => p.audit)
        .filter((a): a is AuditDetail => a !== null)
        .filter((a) => (!params.route || a.route === params.route) && (!params.status || a.status === params.status) && (!params.verdict || a.verdict === params.verdict))
        .filter((a) => (!params.selection || a.selection === params.selection) && after(params.since, a.created_at))
        .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))
        .map(auditSummary);
      return page(rows, params);
    },

    async getAudit(id: string): Promise<AuditDetail> {
      const t = await ready;
      const found = played(t).find((p) => p.audit?.audit_id === id)?.audit;
      if (!found) throw new ApiError({ kind: 'http', status: 404, code: 'not_found', message: 'This recording has no finished deep audit with that id yet.' });
      return found;
    },

    subscribe(next: StreamSink): LiveControl {
      sink = next;
      let stopped = false;
      next.onConnection({ phase: 'connecting', attempt: 0, nextRetryAt: null, lastCloseCode: null });
      ready.then(
        (t) => {
          if (!stopped) play(t);
        },
        () => {
          if (!stopped) next.onConnection({ phase: 'reconnecting', attempt: 1, nextRetryAt: null, lastCloseCode: null });
        },
      );
      return {
        retryNow: () => undefined,
        stop: () => {
          stopped = true;
          player?.stop();
          if (sink === next) sink = null;
        },
      };
    },
  };
  return source;
}
