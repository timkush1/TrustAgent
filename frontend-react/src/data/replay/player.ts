/**
 * Plays a tape's events in compressed time: each gap of the recording is divided by `speed`
 * (VITE_REPLAY_SPEED, default 20) and capped at `maxGapMs`, so a quiet minute of the recording never
 * stalls the demo. Events recorded at the same instant are delivered together, in recording order.
 */
import type { ReplayTape, TapeEvent } from './tape';

export interface ReplayClock {
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
}

export const browserClock: ReplayClock = {
  setTimeout: (fn, ms) => globalThis.setTimeout(fn, ms),
  clearTimeout: (h) => globalThis.clearTimeout(h as ReturnType<typeof setTimeout>),
};

export interface PlayerOptions {
  speed: number;
  /** Longest real pause between two events. */
  maxGapMs: number;
  clock?: ReplayClock;
}

export interface PlayerCallbacks {
  onEvent(ev: TapeEvent): void;
  /** The tape position (recording ms) after a batch of events. */
  onPosition(ms: number): void;
  onEnd(): void;
}

export const DEFAULT_MAX_GAP_MS = 1500;

export class ReplayPlayer {
  private readonly tape: ReplayTape;
  private readonly opts: Required<PlayerOptions>;
  private readonly cb: PlayerCallbacks;
  private timer: unknown = null;
  private next = 0;
  position = 0;

  constructor(tape: ReplayTape, opts: PlayerOptions, callbacks: PlayerCallbacks) {
    this.tape = tape;
    this.opts = { clock: browserClock, ...opts };
    this.cb = callbacks;
  }

  get playing(): boolean {
    return this.timer !== null;
  }

  /** Plays every event after `from` (recording ms). */
  start(from: number): void {
    this.stop();
    this.position = from;
    const events = this.tape.events;
    this.next = events.findIndex((ev) => ev.t_ms > from);
    if (this.next === -1) this.next = events.length;
    this.schedule();
  }

  stop(): void {
    if (this.timer !== null) this.opts.clock.clearTimeout(this.timer);
    this.timer = null;
  }

  /** Real milliseconds until the recording reaches `t_ms`. */
  delayTo(tMs: number): number {
    const speed = this.opts.speed > 0 && Number.isFinite(this.opts.speed) ? this.opts.speed : 1;
    return Math.min(Math.max(tMs - this.position, 0) / speed, this.opts.maxGapMs);
  }

  private schedule(): void {
    const events = this.tape.events;
    if (this.next >= events.length) {
      this.timer = null;
      this.position = Math.max(this.position, this.tape.duration);
      this.cb.onPosition(this.position);
      this.cb.onEnd();
      return;
    }
    const at = events[this.next].t_ms;
    this.timer = this.opts.clock.setTimeout(() => {
      this.timer = null;
      this.position = at;
      while (this.next < events.length && events[this.next].t_ms === at) {
        this.cb.onEvent(events[this.next]);
        this.next += 1;
      }
      this.cb.onPosition(at);
      this.schedule();
    }, this.delayTo(at));
  }
}
