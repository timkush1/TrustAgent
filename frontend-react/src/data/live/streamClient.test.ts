import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fakeLifecycle, socketFactory } from '@/test/fakes/FakeWebSocket';
import { decision, flaggedTrace, frame, hb, hello, iso, T0, trace } from '@/test/fixtures';
import type { ResyncEvent, ResyncReason, StreamEvent } from '../stream/types';
import { CLOSE_UNAUTHORIZED, StreamClient } from './streamClient';

function setup(opts: { random?: () => number; resync?: (since: string, reason: ResyncReason) => Promise<ResyncEvent> } = {}) {
  const sockets = socketFactory();
  const lifecycle = fakeLifecycle();
  const events: StreamEvent[] = [];
  const phases: string[] = [];
  const warn = vi.fn();
  const resync = vi.fn(
    opts.resync ??
      (async (since: string, reason: ResyncReason): Promise<ResyncEvent> => ({
        type: 'resync',
        reason,
        since,
        traces: [],
        decisions: [],
        audits: [],
        truncated: false,
      })),
  );
  const client = new StreamClient({
    url: 'ws://proxy.test/ws',
    onEvent: (e) => events.push(e),
    onConnection: (s) => phases.push(s.phase),
    resync,
    createSocket: sockets.create,
    lifecycle: lifecycle.source,
    random: opts.random ?? (() => 0.5),
    now: () => Date.now(),
    warn,
  });
  return { client, sockets, lifecycle, events, phases, resync, warn };
}

/** Opens the current socket and sends `hello`. */
function connect(s: ReturnType<typeof setup>, h = hello()) {
  s.sockets.last.open();
  s.sockets.last.message(frame({ type: 'hello', data: h }, h.last_seq, h.epoch));
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
});

afterEach(() => {
  vi.useRealTimers();
});

describe('StreamClient handshake and framing', () => {
  it('negotiates protocol 2 without a cursor on the first connection', () => {
    const s = setup();
    s.client.start();
    expect(s.sockets.sockets).toHaveLength(1);
    expect(s.sockets.last.url.searchParams.get('protocol')).toBe('2');
    expect(s.sockets.last.url.searchParams.has('since_seq')).toBe(false);
    expect(s.phases.at(-1)).toBe('connecting');
    connect(s);
    expect(s.client.connection.phase).toBe('open');
    expect(s.client.cursor).toEqual({ epoch: 'epoch-a', lastSeq: 10, lastEventTs: null });
    expect(s.events.map((e) => e.type)).toEqual(['hello']);
  });

  it('delivers one event per frame, in order, and drops duplicates', () => {
    const s = setup();
    s.client.start();
    connect(s);
    const ws = s.sockets.last;
    ws.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11));
    ws.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11));
    ws.message(frame({ type: 'trace_started', data: { trace: trace() } }, 9));
    ws.message(frame({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 12));
    expect(s.events.map((e) => e.type)).toEqual(['hello', 'trace_started', 'gate_decision']);
    expect(s.client.stats.duplicates).toBe(2);
    expect(s.client.cursor.lastSeq).toBe(12);
  });

  it('rejects batched, binary, v1 and unknown frames instead of guessing', () => {
    const s = setup();
    s.client.start();
    connect(s);
    const ws = s.sockets.last;
    const a = JSON.stringify(frame({ type: 'trace_started', data: { trace: trace() } }, 11));
    const b = JSON.stringify(frame({ type: 'trace_started', data: { trace: trace() } }, 12));
    ws.message(`${a}\n${b}`); // v1 batching
    ws.onmessage?.({ data: new ArrayBuffer(4) });
    ws.message({ type: 'audit_result', data: {} }); // v1 frame (no v)
    ws.message({ v: 2, type: 'mystery', seq: 11, epoch: 'epoch-a', ts: iso(), data: {} });
    expect(s.events.map((e) => e.type)).toEqual(['hello']);
    expect(s.client.stats.malformed).toBe(4);
  });

  it('drops an event whose data it could not use, without moving the cursor', () => {
    const s = setup();
    s.client.start();
    connect(s);
    const ws = s.sockets.last;
    // A decision without evaluators, and a trace without its audit block.
    const { evaluators: _evaluators, ...noEvaluators } = decision();
    ws.message({ ...frame({ type: 'gate_decision', data: { trace: flaggedTrace(), decision: decision() } }, 11), data: { trace: flaggedTrace(), decision: noEvaluators } });
    const { audit: _audit, ...noAudit } = trace();
    ws.message({ ...frame({ type: 'trace_started', data: { trace: trace() } }, 11), data: { trace: noAudit } });
    ws.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11));
    expect(s.events.map((e) => e.type)).toEqual(['hello', 'trace_started']);
    expect(s.client.stats.malformed).toBe(2);
    expect(s.client.cursor.lastSeq).toBe(11);
    // Said once per kind, then only counted.
    expect(s.warn.mock.calls.map(([m]) => String(m).match(/\((.+?)\)/)?.[1])).toEqual([
      'gate_decision with unusable data',
      'trace_started with unusable data',
    ]);
  });

  it('refuses a hello without a usable replay cursor', () => {
    const s = setup();
    s.client.start();
    s.sockets.last.open();
    s.sockets.last.message({ v: 2, type: 'hello', seq: 0, epoch: 'epoch-a', ts: iso(), data: { protocol: 2, demo: true } });
    expect(s.events).toEqual([]);
    expect(s.client.cursor).toEqual({ epoch: null, lastSeq: null, lastEventTs: null });
    expect(s.client.stats.malformed).toBe(1);
  });

  it('never delivers hb frames', () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.message(hb());
    expect(s.events.map((e) => e.type)).toEqual(['hello']);
  });
});

describe('StreamClient reconnect', () => {
  it('reconnects with the replay cursor after a drop', () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11));
    s.sockets.last.serverClose(1006);
    expect(s.client.connection.phase).toBe('reconnecting');
    vi.advanceTimersByTime(30_000);
    expect(s.sockets.sockets).toHaveLength(2);
    const url = s.sockets.last.url;
    expect(url.searchParams.get('protocol')).toBe('2');
    expect(url.searchParams.get('since_seq')).toBe('11');
    expect(url.searchParams.get('epoch')).toBe('epoch-a');
  });

  it('uses full-jitter backoff with a 250 ms floor and a 30 s cap, and never gives up', () => {
    const s = setup({ random: () => 0.999 });
    s.client.start();
    const delays: number[] = [];
    for (let attempt = 0; attempt < 14; attempt += 1) {
      s.sockets.last.serverClose();
      const state = s.client.connection;
      delays.push((state.nextRetryAt ?? 0) - Date.now());
      vi.advanceTimersByTime(30_000);
    }
    expect(s.sockets.sockets).toHaveLength(15);
    expect(delays.slice(0, 4)).toEqual([499, 999, 1998, 3996]);
    expect(Math.max(...delays)).toBeLessThanOrEqual(30_000);
    expect(delays.at(-1)).toBe(29_970);
    expect(s.client.connection.attempt).toBe(14);

    const low = setup({ random: () => 0 });
    low.client.start();
    low.sockets.last.serverClose();
    expect((low.client.connection.nextRetryAt ?? 0) - Date.now()).toBe(250);
  });

  it('resets the attempt counter on hello', () => {
    const s = setup();
    s.client.start();
    s.sockets.last.serverClose();
    vi.advanceTimersByTime(30_000);
    s.sockets.last.serverClose();
    vi.advanceTimersByTime(30_000);
    expect(s.client.connection.attempt).toBe(2);
    connect(s);
    expect(s.client.connection.attempt).toBe(0);
  });

  it('never opens a second socket while one is connecting or open', () => {
    const s = setup();
    s.client.start();
    s.client.retryNow();
    s.lifecycle.goOnline();
    expect(s.sockets.sockets).toHaveLength(1);
    connect(s);
    s.lifecycle.becomeVisible();
    expect(s.sockets.sockets).toHaveLength(1);
  });

  it('retries at once on online and when the tab becomes visible', () => {
    const s = setup();
    s.client.start();
    s.sockets.last.serverClose();
    expect(s.sockets.sockets).toHaveLength(1);
    s.lifecycle.goOnline();
    expect(s.sockets.sockets).toHaveLength(2);
    s.sockets.last.serverClose();
    s.lifecycle.becomeVisible();
    expect(s.sockets.sockets).toHaveLength(3);
  });

  it('reports unauthorized (4401) and keeps retrying', () => {
    const s = setup();
    s.client.start();
    s.sockets.last.serverClose(CLOSE_UNAUTHORIZED);
    expect(s.client.connection.phase).toBe('unauthorized');
    expect(s.client.connection.lastCloseCode).toBe(4401);
    vi.advanceTimersByTime(30_000);
    expect(s.sockets.sockets).toHaveLength(2);
    expect(s.client.connection.phase).toBe('unauthorized');
  });

  it('stops for good after dispose', () => {
    const s = setup();
    s.client.start();
    connect(s);
    const ws = s.sockets.last;
    s.client.dispose();
    expect(ws.closedWith).toEqual({ code: 1000, reason: 'dispose' });
    ws.serverClose();
    s.lifecycle.goOnline();
    vi.advanceTimersByTime(120_000);
    expect(s.sockets.sockets).toHaveLength(1);
    expect(s.lifecycle.listeners()).toBe(0);
    expect(s.client.connection.phase).toBe('idle');
  });

  it('forces a reconnect after 45 s of silence, but only once hb has been seen', () => {
    const s = setup();
    s.client.start();
    connect(s);
    vi.advanceTimersByTime(60_000);
    expect(s.sockets.sockets).toHaveLength(1); // not armed yet
    s.sockets.last.message(hb());
    vi.advanceTimersByTime(44_000);
    s.sockets.last.message(hb());
    vi.advanceTimersByTime(44_000);
    expect(s.sockets.sockets).toHaveLength(1);
    const stale = s.sockets.last;
    vi.advanceTimersByTime(1_001);
    expect(stale.closedWith?.code).toBe(4000);
    vi.advanceTimersByTime(30_000);
    expect(s.sockets.sockets).toHaveLength(2);
  });
});

describe('StreamClient resync', () => {
  it('resyncs from last_event_ts − 5 s on gap and delivers the result', async () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11, 'epoch-a', { ts: iso(60_000) }));
    s.sockets.last.message(
      frame(
        {
          type: 'gap',
          data: { from_seq: 12, to_seq: 40, reason: 'slow_consumer', resync: { traces: '', decisions: '', audits: '' } },
        },
        41,
      ),
    );
    await vi.runOnlyPendingTimersAsync();
    expect(s.resync).toHaveBeenCalledTimes(1);
    expect(s.resync).toHaveBeenCalledWith(iso(55_000), 'gap');
    expect(s.events.map((e) => e.type)).toEqual(['hello', 'trace_started', 'gap', 'resync']);
    expect(s.client.cursor.lastSeq).toBe(41);
  });

  it('resyncs when the epoch changes across a reconnect', async () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.message(frame({ type: 'trace_started', data: { trace: trace() } }, 11, 'epoch-a', { ts: iso(10_000) }));
    s.sockets.last.serverClose();
    vi.advanceTimersByTime(30_000);
    connect(s, hello({ epoch: 'epoch-b', last_seq: 3 }));
    await vi.runOnlyPendingTimersAsync();
    expect(s.resync).toHaveBeenCalledWith(iso(5_000), 'epoch_changed');
    expect(s.client.cursor).toMatchObject({ epoch: 'epoch-b', lastSeq: 3 });
  });

  it('does not resync on a same-epoch reconnect (the server replays)', async () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.serverClose();
    vi.advanceTimersByTime(30_000);
    connect(s, hello({ replayed_from: 11 }));
    await vi.runOnlyPendingTimersAsync();
    expect(s.resync).not.toHaveBeenCalled();
  });

  it('resyncs on a seq hole without a gap frame', async () => {
    const s = setup();
    s.client.start();
    connect(s);
    s.sockets.last.message(frame({ type: 'trace_started', data: { trace: trace() } }, 15));
    await vi.runOnlyPendingTimersAsync();
    expect(s.resync).toHaveBeenCalledWith(expect.any(String), 'seq_hole');
    expect(s.events.map((e) => e.type)).toEqual(['hello', 'trace_started', 'resync']);
  });

  it('retries a failed resync with backoff, and coalesces requests while one runs', async () => {
    let fail = true;
    const s = setup({
      resync: async (since, reason) => {
        if (fail) throw new Error('proxy down');
        return { type: 'resync', reason, since, traces: [], decisions: [], audits: [], truncated: false };
      },
    });
    s.client.start();
    connect(s);
    const gap = (seq: number) =>
      frame({ type: 'gap', data: { from_seq: seq, to_seq: seq, reason: 'hub_overflow', resync: { traces: '', decisions: '', audits: '' } } }, seq);
    s.sockets.last.message(gap(11));
    s.sockets.last.message(gap(12)); // queued behind the first
    await vi.advanceTimersByTimeAsync(0);
    expect(s.resync).toHaveBeenCalledTimes(1);
    expect(s.client.stats.resyncFailures).toBe(1);
    fail = false;
    await vi.advanceTimersByTimeAsync(30_000);
    expect(s.resync).toHaveBeenCalledTimes(2);
    expect(s.events.filter((e) => e.type === 'resync')).toHaveLength(1);
  });
});
