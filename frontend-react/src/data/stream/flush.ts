/**
 * Moves events from the ring buffer into React state in batches: at most once per 100 ms on a
 * visible tab (requestAnimationFrame), once per second on a hidden one (rAF pauses there).
 * The socket handler only pushes and schedules; it never touches React (UI #2).
 */
import type { RingBuffer } from './ringBuffer';

export interface FlushScheduler {
  requestFrame(fn: () => void): unknown;
  cancelFrame(id: unknown): void;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
  now(): number;
  hidden(): boolean;
  onVisibilityChange(fn: () => void): () => void;
}

export interface FlusherOptions<T> {
  ring: RingBuffer<T>;
  commit: (items: T[], dropped: number) => void;
  /** Minimum time between two commits on a visible tab. Default 100 ms. */
  minIntervalMs?: number;
  /** Commit period on a hidden tab. Default 1000 ms. */
  hiddenIntervalMs?: number;
  scheduler?: FlushScheduler;
}

export function browserScheduler(): FlushScheduler {
  return {
    requestFrame: (fn) => window.requestAnimationFrame(fn),
    cancelFrame: (id) => window.cancelAnimationFrame(id as number),
    setTimeout: (fn, ms) => window.setTimeout(fn, ms),
    clearTimeout: (id) => window.clearTimeout(id as number),
    now: () => performance.now(),
    hidden: () => document.visibilityState === 'hidden',
    onVisibilityChange: (fn) => {
      document.addEventListener('visibilitychange', fn);
      return () => document.removeEventListener('visibilitychange', fn);
    },
  };
}

export class Flusher<T> {
  private readonly opts: FlusherOptions<T>;
  private readonly s: FlushScheduler;
  private readonly minIntervalMs: number;
  private readonly hiddenIntervalMs: number;
  private frame: unknown = null;
  private timer: unknown = null;
  private lastCommit = Number.NEGATIVE_INFINITY;
  private stopVisibility: (() => void) | null = null;

  constructor(opts: FlusherOptions<T>) {
    this.opts = opts;
    this.s = opts.scheduler ?? browserScheduler();
    this.minIntervalMs = opts.minIntervalMs ?? 100;
    this.hiddenIntervalMs = opts.hiddenIntervalMs ?? 1000;
    this.stopVisibility = this.s.onVisibilityChange(() => {
      // Switch mechanism: a pending rAF never fires on a hidden tab.
      if (this.pending) {
        this.cancel();
        this.schedule();
      }
    });
  }

  get pending(): boolean {
    return this.frame !== null || this.timer !== null;
  }

  /** Call after every push. Coalesces into one commit per frame window. */
  schedule(): void {
    if (this.pending) return;
    if (this.s.hidden()) {
      this.timer = this.s.setTimeout(() => {
        this.timer = null;
        this.flush();
      }, this.hiddenIntervalMs);
    } else {
      this.frame = this.s.requestFrame(this.onFrame);
    }
  }

  /** Commits whatever is buffered now. */
  flush(): void {
    this.cancel();
    this.lastCommit = this.s.now();
    const { items, dropped } = this.opts.ring.drain();
    if (items.length > 0 || dropped > 0) this.opts.commit(items, dropped);
  }

  stop(): void {
    this.cancel();
    this.stopVisibility?.();
    this.stopVisibility = null;
  }

  private readonly onFrame = () => {
    this.frame = null;
    if (this.s.now() - this.lastCommit >= this.minIntervalMs) this.flush();
    else this.frame = this.s.requestFrame(this.onFrame);
  };

  private cancel(): void {
    if (this.frame !== null) {
      this.s.cancelFrame(this.frame);
      this.frame = null;
    }
    if (this.timer !== null) {
      this.s.clearTimeout(this.timer);
      this.timer = null;
    }
  }
}
