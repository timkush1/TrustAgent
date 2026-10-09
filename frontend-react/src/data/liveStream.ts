/**
 * Wires the live stream once per page: DataSource.subscribe → ring buffer → Flusher →
 * streamStore, plus TanStack Query side effects. Module scope, started from main.tsx outside React,
 * so StrictMode and remounts can never open a second socket.
 */
import type { QueryClient } from '@tanstack/react-query';
import { applyControlSideEffects, qk } from './queries';
import type { DataSource, LiveControl } from './source';
import { Flusher, type FlushScheduler } from './stream/flush';
import { RingBuffer } from './stream/ringBuffer';
import { clearLegacyStorage, useStreamStore } from './stream/streamStore';
import type { StreamEvent } from './stream/types';

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

/** Starts the page's live stream (idempotent) and returns its control. */
export function startLiveStream(
  ds: DataSource,
  queryClient: QueryClient,
  options: { scheduler?: FlushScheduler; capacity?: number } = {},
): LiveControl {
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
  const control = ds.subscribe({
    onEvent(ev) {
      ring.push(ev);
      flusher.schedule();
    },
    onConnection(state) {
      useStreamStore.getState().setConnection(state);
    },
  });
  const self: LiveControl = {
    retryNow: () => control.retryNow(),
    stop: () => {
      control.stop();
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
