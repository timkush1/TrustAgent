import { QueryClient } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { audit, decision, flaggedTrace, frame, hello } from '@/test/fixtures';
import { fakeDataSource } from '@/test/fakeDataSource';
import { ApiError } from './errors';
import { liveStream, startLiveStream } from './liveStream';
import { qk } from './queries';
import type { StreamSink } from './source';
import type { FlushScheduler } from './stream/flush';
import { LEGACY_STORAGE_KEY, useStreamStore } from './stream/streamStore';
import type { StreamEvent } from './stream/types';

function manualScheduler() {
  let frames: (() => void)[] = [];
  const s: FlushScheduler = {
    requestFrame: (fn) => frames.push(fn),
    cancelFrame: () => {
      frames = [];
    },
    setTimeout: () => 0,
    clearTimeout: () => {},
    now: () => 1_000_000,
    hidden: () => false,
    onVisibilityChange: () => () => {},
  };
  return { s, runFrame: () => frames.splice(0).forEach((fn) => fn()) };
}

let sink: StreamSink | null = null;

beforeEach(() => {
  useStreamStore.getState().reset();
  sink = null;
});

afterEach(() => {
  liveStream()?.stop();
});

function start() {
  const queryClient = new QueryClient();
  const retryNow = vi.fn();
  const stop = vi.fn();
  const ds = fakeDataSource({
    subscribe: (s) => {
      sink = s;
      return { retryNow, stop };
    },
  });
  const sched = manualScheduler();
  const control = startLiveStream(ds, queryClient, { scheduler: sched.s });
  return { queryClient, control, sched, retryNow, stop, ds };
}

describe('startLiveStream', () => {
  it('is idempotent and deletes the legacy persisted list', () => {
    localStorage.setItem(LEGACY_STORAGE_KEY, '{}');
    const a = start();
    const again = startLiveStream(a.ds, a.queryClient);
    expect(again).toBe(a.control);
    expect(localStorage.getItem(LEGACY_STORAGE_KEY)).toBeNull();
    again.retryNow();
    expect(a.retryNow).toHaveBeenCalled();
  });

  it('batches events into the store once per frame and updates the query cache', () => {
    const { queryClient, sched } = start();
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries');
    sink!.onConnection({ phase: 'open', attempt: 0, nextRetryAt: null, lastCloseCode: null });
    const events = [
      frame({ type: 'hello', data: hello() }, 10),
      frame({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 11),
      frame({ type: 'audit_completed', data: { audit: audit() } }, 12),
    ] as StreamEvent[];
    events.forEach((e) => sink!.onEvent(e));
    expect(useStreamStore.getState().order).toHaveLength(0); // nothing before the frame
    sched.runFrame();
    const state = useStreamStore.getState();
    expect(state.connection.phase).toBe('open');
    expect(state.order).toEqual([flaggedTrace().trace_id]);
    expect(state.traces[flaggedTrace().trace_id].audit.status).toBe('completed');
    expect(queryClient.getQueryData(qk.audits.detail('a-1'))).toMatchObject({ verdict: 'HALLUCINATED' });
    const keys = invalidate.mock.calls.map(([f]) => JSON.stringify(f?.queryKey));
    expect(keys).toContain(JSON.stringify(qk.traces.detail(flaggedTrace().trace_id)));
    expect(keys).toContain(JSON.stringify(qk.traces.lists())); // hello refreshes lists
  });

  it('stop flushes what is buffered and releases the singleton', () => {
    const { control, stop } = start();
    sink!.onEvent(frame({ type: 'hello', data: hello() }, 10) as StreamEvent);
    control.stop();
    expect(stop).toHaveBeenCalled();
    expect(useStreamStore.getState().hello).not.toBeNull();
    expect(liveStream()).toBeNull();
  });
});

describe('startLiveStream gated on read access (CO-10)', () => {
  const refused = (status: number) => new ApiError({ kind: 'http', status, code: 'unauthorized', message: 'Not signed in.' });
  const status = { status: 'ok' } as never;

  function startGated(getStatus: () => Promise<never>) {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const stop = vi.fn();
    const subscribe = vi.fn((s: StreamSink) => {
      sink = s;
      return { retryNow: vi.fn(), stop };
    });
    const ds = fakeDataSource({ getStatus: vi.fn(getStatus), subscribe });
    const control = startLiveStream(ds, queryClient, { scheduler: manualScheduler().s, gateOnReadAccess: true });
    return { queryClient, subscribe, stop, control };
  }

  it('opens no socket before sign-in, then connects once a read succeeds', async () => {
    const { queryClient, subscribe } = startGated(() => Promise.reject(refused(401)));
    await vi.waitFor(() => expect(useStreamStore.getState().connection.phase).toBe('unauthorized'));
    expect(subscribe).not.toHaveBeenCalled();
    // Claiming the setup link signs in and refetches every query: the status now answers.
    queryClient.setQueryData(qk.status, status);
    expect(subscribe).toHaveBeenCalledTimes(1);
  });

  it('opens at once when the status answers', async () => {
    const { subscribe } = startGated(() => Promise.resolve(status));
    await vi.waitFor(() => expect(subscribe).toHaveBeenCalledTimes(1));
  });

  it('still opens when the status fails for another reason (the stream retries itself)', async () => {
    const { subscribe } = startGated(() => Promise.reject(new ApiError({ kind: 'network', status: 0, code: 'network', message: 'offline' })));
    await vi.waitFor(() => expect(subscribe).toHaveBeenCalledTimes(1));
  });

  it('pauses a refused stream until a session read succeeds, without retrying', async () => {
    const { queryClient, subscribe, stop } = startGated(() => Promise.resolve(status));
    await vi.waitFor(() => expect(subscribe).toHaveBeenCalledTimes(1));
    sink!.onConnection({ phase: 'unauthorized', attempt: 1, nextRetryAt: 5_000, lastCloseCode: 4401 });
    await vi.waitFor(() => expect(stop).toHaveBeenCalledTimes(1));
    expect(useStreamStore.getState().connection).toMatchObject({ phase: 'unauthorized', nextRetryAt: null });
    expect(subscribe).toHaveBeenCalledTimes(1);
    queryClient.setQueryData(qk.session, { actor: 'admin', scopes: ['read'] });
    expect(subscribe).toHaveBeenCalledTimes(2);
  });

  it('stop releases the cache watcher', async () => {
    const { queryClient, subscribe, control } = startGated(() => Promise.reject(refused(401)));
    await vi.waitFor(() => expect(useStreamStore.getState().connection.phase).toBe('unauthorized'));
    control.stop();
    queryClient.setQueryData(qk.status, status);
    expect(subscribe).not.toHaveBeenCalled();
  });
});
