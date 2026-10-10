/**
 * Wires the live stream once per page: DataSource.subscribe → ring buffer → Flusher →
 * streamStore, plus TanStack Query side effects. Module scope, started from main.tsx outside React,
 * so StrictMode and remounts can never open a second socket.
 *
 * With `gateOnReadAccess` (the full dashboard; CO-10) the socket opens only once this browser may
 * read: GET /api/status answers (public read, or a session or key that reads). Before sign-in (the
 * setup page on a fresh stack) there is no ticket request and no WebSocket at all, and a stream the
 * proxy refuses with 401 pauses instead of retrying, until the status or the session is read
 * successfully again (signing in invalidates every query).
 */
import type { Query, QueryClient } from '@tanstack/react-query';
import { isApiError } from './errors';
import { applyControlSideEffects, qk, statusQuery } from './queries';
import type { DataSource, LiveControl } from './source';
import { Flusher, type FlushScheduler } from './stream/flush';
import { RingBuffer } from './stream/ringBuffer';
import { clearLegacyStorage, useStreamStore } from './stream/streamStore';
import type { ConnectionState, StreamEvent } from './stream/types';

export const RING_CAPACITY = 5000;

let running: LiveControl | null = null;

/** Keeps cached queries consistent with a committed batch. */
export function applyQuerySideEffects(queryClient: QueryClient, batch: readonly StreamEvent[]): void {
  const traceDetails = new Set<string>();
  let lists = false;
  let status = false;
  for (const ev of batch) {
    switch (ev.type) {
      case 'hello':
      case 'resync':
        // (Re)connected or caught up: lists fetched before this point may be missing rows.
        lists = true;
        status = true;
        break;
      case 'gate_decision':
        traceDetails.add(ev.data.trace.trace_id);
        break;
      case 'audit_completed':
        queryClient.setQueryData(qk.audits.detail(ev.data.audit.audit_id), ev.data.audit);
        traceDetails.add(ev.data.audit.trace_id);
        break;
      case 'audit_failed':
      case 'audit_dropped':
        if (ev.trace_id) traceDetails.add(ev.trace_id);
        break;
      case 'status':
      case 'policy_updated':
      case 'decision_model_changed':
      case 'calibration_updated':
      case 'settings_updated':
      case 'credentials_changed':
        status = true;
        break;
      default:
        break;
    }
  }
  for (const id of traceDetails) {
    void queryClient.invalidateQueries({ queryKey: qk.traces.detail(id), refetchType: 'active' });
  }
  if (lists) {
    void queryClient.invalidateQueries({ queryKey: qk.traces.lists(), refetchType: 'active' });
    void queryClient.invalidateQueries({ queryKey: qk.audits.lists(), refetchType: 'active' });
    void queryClient.invalidateQueries({ queryKey: qk.decisions.all, refetchType: 'active' });
  }
  if (status) void queryClient.invalidateQueries({ queryKey: qk.status, refetchType: 'active' });
  // The control-plane pages, and per event the control planes (security, cost, evaluator, Studio side effects).
  applyControlSideEffects(queryClient, batch);
}

export interface LiveStreamOptions {
  scheduler?: FlushScheduler;
  capacity?: number;
  /** Open only once this browser may read, and pause on 401 until it may again (CO-10). */
  gateOnReadAccess?: boolean;
}

/** What the Health pill shows while the stream waits for a sign-in. */
export const WAITING_FOR_SIGN_IN: ConnectionState = { phase: 'unauthorized', attempt: 0, nextRetryAt: null, lastCloseCode: null };

const sameKey = (query: Query, key: readonly unknown[]) => JSON.stringify(query.queryKey) === JSON.stringify(key);

/** A read answered 401 or 403: nobody is signed in that may read. */
function refusesReader(err: unknown): boolean {
  return isApiError(err) && (err.status === 401 || err.status === 403);
}

/** Starts the page's live stream (idempotent) and returns its control. */
export function startLiveStream(ds: DataSource, queryClient: QueryClient, options: LiveStreamOptions = {}): LiveControl {
  if (running) return running;
  clearLegacyStorage();
  const ring = new RingBuffer<StreamEvent>(options.capacity ?? RING_CAPACITY);
  const flusher = new Flusher<StreamEvent>({
    ring,
    scheduler: options.scheduler,
    commit: (items, dropped) => {
      useStreamStore.getState().ingest(items, dropped);
      applyQuerySideEffects(queryClient, items);
    },
  });
  const gated = options.gateOnReadAccess === true;
  let control: LiveControl | null = null;
  let stopped = false;
  let checking = false;

  const open = () => {
    if (control || stopped) return;
    control = ds.subscribe({
      onEvent(ev) {
        ring.push(ev);
        flusher.schedule();
      },
      onConnection(state) {
        useStreamStore.getState().setConnection(state);
        // Refused (a 4401 close or a refused ticket): stop retrying until a read succeeds again.
        if (gated && state.phase === 'unauthorized') queueMicrotask(pause);
      },
    });
  };

  const pause = () => {
    if (!control || stopped) return;
    const current = control;
    control = null;
    current.stop();
    useStreamStore.getState().setConnection(WAITING_FOR_SIGN_IN);
  };

  /** Opens when GET /api/status answers; waits on 401/403; any other failure opens (the stream retries). */
  const check = () => {
    if (control || stopped || checking) return;
    checking = true;
    queryClient.fetchQuery(statusQuery(ds)).then(
      () => {
        checking = false;
        open();
      },
      (err: unknown) => {
        checking = false;
        if (refusesReader(err)) useStreamStore.getState().setConnection(WAITING_FOR_SIGN_IN);
        else open();
      },
    );
  };

  // A successful status or session read (polling, or every query refetched after a sign-in)
  // resumes a waiting stream.
  const unwatch = gated
    ? queryClient.getQueryCache().subscribe((event) => {
        if (event.type !== 'updated' || event.action.type !== 'success') return;
        if (sameKey(event.query, qk.status) || sameKey(event.query, qk.session)) open();
      })
    : () => {};

  if (gated) check();
  else open();

  const self: LiveControl = {
    retryNow: () => (control ? control.retryNow() : gated ? check() : open()),
    stop: () => {
      stopped = true;
      unwatch();
      control?.stop();
      control = null;
      flusher.flush();
      flusher.stop();
      if (running === self) running = null;
    },
  };
  running = self;
  return self;
}

/** The running stream, if any (the Health pill's "Retry now"). */
export function liveStream(): LiveControl | null {
  return running;
}

if (import.meta.hot) {
  import.meta.hot.dispose(() => running?.stop());
}
