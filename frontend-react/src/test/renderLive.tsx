/**
 * Renders the app against the real data layer: LiveDataSource over msw (REST) and StreamClient
 * over a FakeWebSocket (WS v2), wired by startLiveStream exactly as main.tsx does. Tests push WS
 * frames with `ws.send(...)` and watch the page update.
 *
 * Call `installMswServer()` in the test file, and `stopLive()` in afterEach (the live stream is a
 * per-page singleton).
 */
import { LiveDataSource } from '@/data/live/liveDataSource';
import { liveStream, startLiveStream } from '@/data/liveStream';
import type { FlushScheduler } from '@/data/stream/flush';
import { useStreamStore } from '@/data/stream/streamStore';
import type { WsEvent } from '@/types/api';
import type { FakeApi } from './apiHandlers';
import { fakeLifecycle, socketFactory } from './fakes/FakeWebSocket';
import { frame, hello } from './fixtures';
import { API, server } from './msw';
import { createTestQueryClient, renderApp } from './renderApp';

const EPOCH = 'epoch-test';

/** Commits every batch on the next macrotask (no rAF in jsdom, no 100 ms throttle). */
function testScheduler(): FlushScheduler {
  let clock = 0;
  return {
    requestFrame: (fn) => setTimeout(fn, 0),
    cancelFrame: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
    now: () => (clock += 1000),
    hidden: () => false,
    onVisibilityChange: () => () => {},
  };
}

type Envelope<E> = E extends { type: infer T; data: infer D } ? { type: T; data: D } : never;
export type LiveFrame = Envelope<Exclude<WsEvent, { type: 'hb' | 'hello' }>>;

export function renderLive(path: string, api: FakeApi) {
  server.use(...api.handlers);
  useStreamStore.getState().reset();
  const sockets = socketFactory();
  const lifecycle = fakeLifecycle();
  const dataSource = new LiveDataSource(
    { apiBase: API, crossOrigin: true, wsUrl: 'ws://proxy.test/ws' },
    // No D71 ticket: these tests open the socket synchronously (ws.connect); the ticket wiring has
    // its own tests in liveDataSource.test.ts.
    { stream: { createSocket: sockets.create, lifecycle: lifecycle.source, ticket: undefined } },
  );
  const queryClient = createTestQueryClient();
  startLiveStream(dataSource, queryClient, { scheduler: testScheduler() });
  const app = renderApp(path, dataSource, { queryClient });

  let seq = 0;
  const ws = {
    /** Opens the socket and sends `hello` (which refreshes REST lists). */
    connect() {
      sockets.last.open();
      sockets.last.message(frame({ type: 'hello', data: hello({ epoch: EPOCH, last_seq: seq }) }, seq, EPOCH));
    },
    /** Sends one sequenced frame; `trace_id` goes in the envelope like the hub does. */
    send(e: LiveFrame, traceId?: string) {
      seq += 1;
      sockets.last.message(frame(e, seq, EPOCH, { ts: new Date().toISOString(), trace_id: traceId }));
    },
  };
  return { ...app, ws, sockets };
}

export function stopLive(): void {
  liveStream()?.stop();
  useStreamStore.getState().reset();
}
