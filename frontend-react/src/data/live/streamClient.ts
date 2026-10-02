/**
 * StreamClient: the dashboard's one WebSocket (SPEC §2.5 client algorithm, WS protocol v2).
 *
 * - Negotiates v2 with `?protocol=2`, and after a disconnect asks for replay with
 *   `since_seq=<last seq>&epoch=<epoch>`.
 * - One JSON object per text frame; `hb` frames only prove liveness.
 * - Drops duplicates (seq ≤ last seen). On `gap`, an epoch change or a seq hole it resyncs over
 *   REST from `last_event_ts − 5 s` and emits the result as one `resync` event.
 * - Reconnects with full-jitter backoff, floor 250 ms, cap 30 s, and never gives up. `online` and a
 *   `visibilitychange` to visible retry at once. After the first `hb`, 45 s of silence forces a
 *   reconnect.
 * - Cross origin (P4-PLAN D71): a browser WebSocket cannot send the key header, so with `ticket`
 *   set it asks for a one-use ticket (POST /api/ws-ticket) before every (re)connect and connects
 *   with `?ticket=`. A refused ticket (401) shows as unauthorized and keeps retrying with backoff.
 *   Never an api_key query parameter and never a bearer subprotocol: the key is never in a URL.
 * - Knows nothing about React: events go to `onEvent` (the ring buffer), state to `onConnection`.
 *   One instance per page lives in module scope (see liveStream.ts), so StrictMode double
 *   mounting cannot open a second socket.
 */
import type { WsEvent, WsHello, WsTicket } from '@/types/api';
import { readDashboardKey } from '../http';
import { parseWsFrame } from '../normalize';
import type { ConnectionState, ResyncEvent, ResyncReason, StreamEvent } from '../stream/types';
import { INITIAL_CONNECTION } from '../stream/types';
import { backoffDelay } from './backoff';

/** The subset of the browser WebSocket the client uses (a fake implements it in tests). */
export interface SocketLike {
  readonly readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  close(code?: number, reason?: string): void;
}

export type SocketFactory = (url: string) => SocketLike;

interface Timers {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
}

/** Where `online` and `visibilitychange` come from (window and document in the browser). */
export interface LifecycleSource {
  addOnline(fn: () => void): () => void;
  addVisible(fn: () => void): () => void;
}

export interface StreamClientOptions {
  /** ws(s)://…/ws without query parameters. */
  url: string;
  onEvent: (event: StreamEvent) => void;
  onConnection?: (state: ConnectionState) => void;
  /** REST catch-up from `since` (RFC3339). Resolves to the merged result; rejects to retry later. */
  resync: (since: string, reason: ResyncReason) => Promise<ResyncEvent>;
  createSocket?: SocketFactory;
  random?: () => number;
  now?: () => number;
  timers?: Timers;
  lifecycle?: LifecycleSource;
  /** Silence (after the first hb) that forces a reconnect. Default 45 s. */
  silenceMs?: number;
  /** How far before the last event a resync starts. Default 5 s. */
  resyncOverlapMs?: number;
  /** Reports the first ignored frame of each kind (default console.warn). */
  warn?: (message: string) => void;
  /**
   * A fresh one-use WebSocket ticket for the next connect (D71), or null to connect without one.
   * Called before every (re)connect; a rejection counts as a failed attempt (status 401 as
   * unauthorized). See wsTicketSource.
   */
  ticket?: (signal: AbortSignal) => Promise<string | null>;
}

/**
 * The `ticket` option of a dashboard on another origin than its proxy (D71): while this tab is
 * signed in with a key, a fresh ticket from POST /api/ws-ticket before every (re)connect; without a
 * key, none (a proxy with public read accepts the socket as it is). Same origin needs no ticket:
 * the session cookie rides on the WebSocket handshake.
 */
export function wsTicketSource(
  crossOrigin: boolean,
  createTicket: (signal?: AbortSignal) => Promise<WsTicket>,
): StreamClientOptions['ticket'] {
  if (!crossOrigin) return undefined;
  return async (signal) => (readDashboardKey() === null ? null : (await createTicket(signal)).ticket);
}

function isUnauthorized(err: unknown): boolean {
  const status = typeof err === 'object' && err !== null ? (err as { status?: unknown }).status : undefined;
  return status === 401;
}

const CONNECTING = 0;
const OPEN = 1;
export const CLOSE_UNAUTHORIZED = 4401;
export const CLOSE_TOO_MANY = 4429;
const CLOSE_SILENCE = 4000;

export interface StreamStats {
  malformed: number;
  duplicates: number;
  resyncs: number;
  resyncFailures: number;
}

export function browserLifecycle(): LifecycleSource {
  return {
    addOnline(fn) {
      window.addEventListener('online', fn);
      return () => window.removeEventListener('online', fn);
    },
    addVisible(fn) {
      const onChange = () => {
        if (document.visibilityState === 'visible') fn();
      };
      document.addEventListener('visibilitychange', onChange);
      return () => document.removeEventListener('visibilitychange', onChange);
    },
  };
}

export class StreamClient {
  private readonly opts: Required<
    Pick<StreamClientOptions, 'random' | 'now' | 'silenceMs' | 'resyncOverlapMs'>
  > &
    StreamClientOptions;
  private readonly createSocket: SocketFactory;
  private readonly timers: Timers;

  private ws: SocketLike | null = null;
  /** A ticket request in flight for the next connect (D71). */
  private ticketing: AbortController | null = null;
  private started = false;
  private disposed = false;
  private everOpened = false;
  private attempt = 0;
  private epoch: string | null = null;
  private lastSeq: number | null = null;
  private lastEventTs: string | null = null;
  private startedAt = 0;
  private hbSeen = false;

  private retryTimer: unknown = null;
  private silenceTimer: unknown = null;
  private resyncTimer: unknown = null;
  private resyncing = false;
  private queuedResync: ResyncReason | null = null;
  private resyncAttempt = 0;
  private unlisten: (() => void)[] = [];
  private readonly warned = new Set<string>();

  private state: ConnectionState = INITIAL_CONNECTION;
  readonly stats: StreamStats = { malformed: 0, duplicates: 0, resyncs: 0, resyncFailures: 0 };

  constructor(options: StreamClientOptions) {
    this.opts = {
      random: Math.random,
      now: Date.now,
      silenceMs: 45_000,
      resyncOverlapMs: 5_000,
      ...options,
    };
    this.createSocket = options.createSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike);
    this.timers = options.timers ?? {
      setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
      clearTimeout: (id) => globalThis.clearTimeout(id as ReturnType<typeof setTimeout>),
    };
  }

  get connection(): ConnectionState {
    return this.state;
  }

  /** Current replay cursor, for diagnostics. */
  get cursor(): { epoch: string | null; lastSeq: number | null; lastEventTs: string | null } {
    return { epoch: this.epoch, lastSeq: this.lastSeq, lastEventTs: this.lastEventTs };
  }

  start(): void {
    if (this.started || this.disposed) return;
    this.started = true;
    this.startedAt = this.opts.now();
    const lifecycle = this.opts.lifecycle ?? (typeof window !== 'undefined' ? browserLifecycle() : null);
    if (lifecycle) {
      this.unlisten.push(lifecycle.addOnline(() => this.retryNow()));
      this.unlisten.push(lifecycle.addVisible(() => this.retryNow()));
    }
    this.connect();
  }

  /** Connects now if not connected (the "Retry now" button, `online`, tab becomes visible). */
  retryNow(): void {
    if (!this.started || this.disposed) return;
    if (this.ticketing || (this.ws && this.ws.readyState <= OPEN)) return;
    this.clearTimer('retryTimer');
    this.connect();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.clearTimer('retryTimer');
    this.clearTimer('silenceTimer');
    this.clearTimer('resyncTimer');
    this.ticketing?.abort();
    this.ticketing = null;
    this.unlisten.forEach((fn) => fn());
    this.unlisten = [];
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      ws.close(1000, 'dispose');
    }
    this.setState({ ...INITIAL_CONNECTION });
  }

  // ------------------------------------------------------------------------------------ socket

  private buildUrl(ticket: string | null): string {
    const url = new URL(this.opts.url);
    url.searchParams.set('protocol', '2');
    if (this.epoch !== null && this.lastSeq !== null) {
      url.searchParams.set('since_seq', String(this.lastSeq));
      url.searchParams.set('epoch', this.epoch);
    }
    if (ticket) url.searchParams.set('ticket', ticket);
    return url.toString();
  }

  private connect(): void {
    if (this.disposed) return;
    // CONNECTING or OPEN, or a ticket on its way: never a second socket (UI #8).
    if (this.ticketing || (this.ws && this.ws.readyState <= OPEN)) return;
    this.setState({
      ...this.state,
      phase: this.everOpened || this.attempt > 0 ? this.reconnectPhase() : 'connecting',
      nextRetryAt: null,
    });
    const getTicket = this.opts.ticket;
    if (!getTicket) {
      this.open(null);
      return;
    }
    const pending = new AbortController();
    this.ticketing = pending;
    getTicket(pending.signal).then(
      (ticket) => {
        if (this.ticketing !== pending) return; // Disposed meanwhile.
        this.ticketing = null;
        this.open(ticket);
      },
      (err: unknown) => {
        if (this.ticketing !== pending) return;
        this.ticketing = null;
        this.scheduleReconnect(isUnauthorized(err) ? CLOSE_UNAUTHORIZED : null);
      },
    );
  }

  private open(ticket: string | null): void {
    if (this.disposed) return;
    let ws: SocketLike;
    try {
      ws = this.createSocket(this.buildUrl(ticket));
    } catch {
      // Invalid URL or a blocked constructor: treat as a failed attempt.
      this.scheduleReconnect(null);
      return;
    }
    this.ws = ws;
    ws.onopen = () => {
      if (this.ws !== ws) return;
      this.everOpened = true;
      this.setState({ ...this.state, phase: 'open', nextRetryAt: null });
    };
    ws.onmessage = (ev) => {
      if (this.ws !== ws) return;
      this.onFrame(ev.data);
    };
    ws.onerror = () => {
      // A close event always follows; reconnect happens there.
    };
    ws.onclose = (ev) => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.clearTimer('silenceTimer');
      if (this.disposed) return;
      this.scheduleReconnect(ev.code);
    };
  }

  private reconnectPhase(): ConnectionState['phase'] {
    return this.state.lastCloseCode === CLOSE_UNAUTHORIZED ? 'unauthorized' : 'reconnecting';
  }

  private scheduleReconnect(code: number | null): void {
    const delay = backoffDelay(this.attempt, this.opts.random);
    this.attempt += 1;
    const lastCloseCode = code ?? this.state.lastCloseCode;
    this.setState({
      ...this.state,
      phase: lastCloseCode === CLOSE_UNAUTHORIZED ? 'unauthorized' : 'reconnecting',
      attempt: this.attempt,
      nextRetryAt: this.opts.now() + delay,
      lastCloseCode,
    });
    this.clearTimer('retryTimer');
    this.retryTimer = this.timers.setTimeout(() => {
      this.retryTimer = null;
      this.connect();
    }, delay);
  }

  /** No frame for `silenceMs` after the first hb: the socket is dead even if it looks open. */
  private armSilence(): void {
    if (!this.hbSeen) return;
    this.clearTimer('silenceTimer');
    this.silenceTimer = this.timers.setTimeout(() => {
      this.silenceTimer = null;
      const ws = this.ws;
      if (!ws || this.disposed) return;
      this.ws = null;
      ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
      ws.close(CLOSE_SILENCE, 'silence');
      this.scheduleReconnect(null);
    }, this.opts.silenceMs);
  }

  // ------------------------------------------------------------------------------------ frames

  private onFrame(data: unknown): void {
    const parsed = parseWsFrame(data);
    if (!parsed.ok) {
      this.stats.malformed += 1;
      this.warnOnce(parsed.reason === 'bad_data' ? `${parsed.type} with unusable data` : parsed.reason);
      this.armSilence();
      return;
    }
    const event = parsed.event;
    if (event.type === 'hb') {
      this.hbSeen = true;
      this.armSilence();
      return;
    }
    this.armSilence();
    if (event.type === 'hello') {
      this.onHello(event.data);
      this.opts.onEvent(event);
      return;
    }
    this.onSequenced(event);
  }

  private onHello(hello: WsHello): void {
    this.attempt = 0;
    this.setState({ ...this.state, phase: 'open', attempt: 0, nextRetryAt: null });
    const previous = this.epoch;
    this.epoch = hello.epoch;
    if (previous === null) {
      // First connection of this page: start counting from the server's head.
      if (this.lastSeq === null) this.lastSeq = hello.last_seq;
    } else if (previous !== hello.epoch) {
      // The hub restarted: seqs start over and nothing can be replayed.
      this.lastSeq = hello.last_seq;
      this.requestResync('epoch_changed');
    }
    // Same epoch: the server replays from since_seq+1, or sends a gap when it can't.
  }

  private onSequenced(event: Exclude<WsEvent, { type: 'hb' } | { type: 'hello' }>): void {
    if (this.epoch !== null && event.epoch !== this.epoch) {
      this.epoch = event.epoch;
      this.lastSeq = event.seq;
      this.lastEventTs = event.ts;
      this.opts.onEvent(event);
      this.requestResync('epoch_changed');
      return;
    }
    if (event.type === 'gap') {
      this.lastSeq = Math.max(this.lastSeq ?? event.data.to_seq, event.data.to_seq, event.seq);
      this.opts.onEvent(event);
      this.requestResync('gap');
      return;
    }
    if (this.lastSeq !== null && event.seq <= this.lastSeq) {
      this.stats.duplicates += 1;
      return;
    }
    const hole = this.lastSeq !== null && event.seq > this.lastSeq + 1;
    this.lastSeq = event.seq;
    this.lastEventTs = event.ts;
    this.opts.onEvent(event);
    if (hole) this.requestResync('seq_hole');
  }

  // ------------------------------------------------------------------------------------ resync

  private resyncSince(): string {
    const last = this.lastEventTs ? Date.parse(this.lastEventTs) : Number.NaN;
    const base = Number.isNaN(last) ? this.startedAt : last;
    return new Date(base - this.opts.resyncOverlapMs).toISOString();
  }

  private requestResync(reason: ResyncReason): void {
    if (this.disposed) return;
    if (this.resyncing || this.resyncTimer !== null) {
      this.queuedResync = reason;
      return;
    }
    this.resyncing = true;
    this.opts
      .resync(this.resyncSince(), reason)
      .then((result) => {
        if (this.disposed) return;
        this.stats.resyncs += 1;
        this.resyncAttempt = 0;
        this.opts.onEvent(result);
      })
      .catch(() => {
        if (this.disposed) return;
        // The REST side is unreachable too; try again with the same backoff as the socket.
        this.stats.resyncFailures += 1;
        this.queuedResync ??= reason;
        const delay = backoffDelay(this.resyncAttempt, this.opts.random);
        this.resyncAttempt += 1;
        this.resyncTimer = this.timers.setTimeout(() => {
          this.resyncTimer = null;
          this.runQueuedResync();
        }, delay);
      })
      .finally(() => {
        this.resyncing = false;
        if (this.resyncTimer === null) this.runQueuedResync();
      });
  }

  private runQueuedResync(): void {
    const next = this.queuedResync;
    this.queuedResync = null;
    if (next) this.requestResync(next);
  }

  // ------------------------------------------------------------------------------------ helpers

  private clearTimer(name: 'retryTimer' | 'silenceTimer' | 'resyncTimer'): void {
    if (this[name] !== null) {
      this.timers.clearTimeout(this[name]);
      this[name] = null;
    }
  }

  /** One console line per kind of ignored frame, so a proxy that is off-contract is visible. */
  private warnOnce(kind: string): void {
    if (this.warned.has(kind)) return;
    this.warned.add(kind);
    const warn = this.opts.warn ?? ((m: string) => console.warn(m));
    warn(`[TrustAgent] Ignored a live-update frame (${kind}); the proxy may not speak WS protocol v2 as this dashboard expects. Later frames like it are only counted.`);
  }

  private setState(next: ConnectionState): void {
    this.state = next;
    this.opts.onConnection?.(next);
  }
}

export { CONNECTING as SOCKET_CONNECTING, OPEN as SOCKET_OPEN };
