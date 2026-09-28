import { describe, expect, it, vi } from 'vitest';
import { Flusher, type FlushScheduler } from './flush';
import { RingBuffer } from './ringBuffer';

function fakeScheduler() {
  let now = 0;
  let hidden = false;
  let frames: (() => void)[] = [];
  const timers = new Map<number, { at: number; fn: () => void }>();
  let nextId = 1;
  const visibility = new Set<() => void>();
  const s: FlushScheduler = {
    requestFrame: (fn) => {
      frames.push(fn);
      return frames.length;
    },
    cancelFrame: () => {
      frames = [];
    },
    setTimeout: (fn, ms) => {
      const id = nextId++;
      timers.set(id, { at: now + ms, fn });
      return id;
    },
    clearTimeout: (id) => {
      timers.delete(id as number);
    },
    now: () => now,
    hidden: () => hidden,
    onVisibilityChange: (fn) => {
      visibility.add(fn);
      return () => visibility.delete(fn);
    },
  };
  return {
    s,
    frame(advance = 16) {
      now += advance;
      const run = frames;
      frames = [];
      run.forEach((fn) => fn());
    },
    tick(ms: number) {
      now += ms;
      for (const [id, t] of [...timers]) {
        if (t.at <= now) {
          timers.delete(id);
          t.fn();
        }
      }
    },
    setHidden(h: boolean) {
      hidden = h;
      visibility.forEach((fn) => fn());
    },
    pendingFrames: () => frames.length,
  };
}

describe('Flusher', () => {
  it('commits once per frame window, at most every 100 ms', () => {
    const f = fakeScheduler();
    const ring = new RingBuffer<number>(10);
    const commit = vi.fn();
    const flusher = new Flusher({ ring, commit, scheduler: f.s });

    ring.push(1);
    flusher.schedule();
    ring.push(2);
    flusher.schedule();
    f.frame(16);
    expect(commit).toHaveBeenCalledTimes(1);
    expect(commit).toHaveBeenLastCalledWith([1, 2], 0);

    ring.push(3);
    flusher.schedule();
    f.frame(16); // 16 ms since the last commit: wait
    f.frame(16);
    expect(commit).toHaveBeenCalledTimes(1);
    for (let i = 0; i < 5; i += 1) f.frame(16);
    expect(commit).toHaveBeenCalledTimes(2);
    expect(commit).toHaveBeenLastCalledWith([3], 0);
  });

  it('uses a 1 s timer on a hidden tab and switches when visibility changes', () => {
    const f = fakeScheduler();
    const ring = new RingBuffer<number>(10);
    const commit = vi.fn();
    const flusher = new Flusher({ ring, commit, scheduler: f.s });

    f.setHidden(true);
    ring.push(1);
    flusher.schedule();
    expect(f.pendingFrames()).toBe(0);
    f.tick(999);
    expect(commit).not.toHaveBeenCalled();
    f.tick(1);
    expect(commit).toHaveBeenCalledWith([1], 0);

    ring.push(2);
    flusher.schedule();
    f.setHidden(false); // pending timer becomes a frame
    expect(f.pendingFrames()).toBe(1);
    f.frame(200);
    expect(commit).toHaveBeenLastCalledWith([2], 0);
  });

  it('reports overflow drops with the batch', () => {
    const f = fakeScheduler();
    const ring = new RingBuffer<number>(2);
    const commit = vi.fn();
    const flusher = new Flusher({ ring, commit, scheduler: f.s });
    [1, 2, 3, 4].forEach((n) => ring.push(n));
    flusher.flush();
    expect(commit).toHaveBeenCalledWith([3, 4], 2);
  });

  it('stop cancels pending work', () => {
    const f = fakeScheduler();
    const ring = new RingBuffer<number>(2);
    const commit = vi.fn();
    const flusher = new Flusher({ ring, commit, scheduler: f.s });
    ring.push(1);
    flusher.schedule();
    flusher.stop();
    f.frame(500);
    expect(commit).not.toHaveBeenCalled();
  });
});
