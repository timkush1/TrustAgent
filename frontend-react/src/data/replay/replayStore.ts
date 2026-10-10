/**
 * What the static build's chrome shows about the replay: the recording's label, the tape
 * position and the restart control. ReplayDataSource writes it; the top bar, banner and nav read
 * it. Not persisted.
 */
import { create } from 'zustand';
import type { Recording } from '@/types/api';

export interface ReplayInfo {
  status: 'idle' | 'loading' | 'ready' | 'error';
  error: string | null;
  kind: Recording['kind'] | null;
  label: string | null;
  recordedAt: string | null;
  /** The decision model's snapshot and the judge model the recording was made with (the banner's words). */
  snapshot: string | null;
  judge: string | null;
  traces: number;
  /** Recording ms of the last event. */
  duration: number;
  /** Recording ms already on screen before the tape starts playing. */
  preroll: number;
  position: number;
  /** Requests on screen at the current position. */
  shown: number;
  playing: boolean;
  finished: boolean;
  /** The trace the "Trace" nav item opens: the first flagged or blocked answer with a contradiction. */
  featuredTraceId: string | null;
  speed: number;
}

interface ReplayStore extends ReplayInfo {
  /** Set by the data source: plays the tape again from the preroll point. */
  restart: (() => void) | null;
  set(patch: Partial<ReplayInfo> & { restart?: (() => void) | null }): void;
}

export const INITIAL_REPLAY: ReplayInfo = {
  status: 'idle',
  error: null,
  kind: null,
  label: null,
  recordedAt: null,
  snapshot: null,
  judge: null,
  traces: 0,
  duration: 0,
  preroll: 0,
  position: 0,
  shown: 0,
  playing: false,
  finished: false,
  featuredTraceId: null,
  speed: 1,
};

/**
 * The tape's clock: the recording's wall-clock time at the replay position (ms since the epoch), or
 * null when no recording is replaying (the full build).
 */
export function replayClock(s: ReplayInfo): number | null {
  if (s.status !== 'ready' || !s.recordedAt) return null;
  const start = Date.parse(s.recordedAt);
  return Number.isNaN(start) ? null : start + s.position;
}

export const useReplayStore = create<ReplayStore>()((set) => ({
  ...INITIAL_REPLAY,
  restart: null,
  set: (patch) => set(patch),
}));
