/**
 * The static build's replay controls for components: whether this build replays a recording,
 * the recording's facts, and "Replay again" (which also clears what the stream already showed).
 */
import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect } from 'react';
import { useRuntimeConfig } from '@/app/services';
import { qk, qkc } from '@/data/queries';
import { useReplayStore, type ReplayInfo } from '@/data/replay/replayStore';
import { useStreamStore } from '@/data/stream/streamStore';

export function useIsStatic(): boolean {
  return useRuntimeConfig().appMode === 'static';
}

export function useReplayInfo(): ReplayInfo {
  return useReplayStore();
}

/** The landing page's "Run it yourself" section (the landing is the static site's root). */
export function useRunItYourselfHref(): string {
  return `${useRuntimeConfig().basePath}#self-host`;
}

const dateFormat = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });

/** "28 Sep 2026" (the recording's UTC date). */
export function recordingDate(iso: string | null): string {
  if (!iso) return 'an unknown date';
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 'an unknown date' : dateFormat.format(t);
}

/** Plays the tape again from the start of the live part: the list and every cache start over. */
export function useReplayRestart(): (() => void) | null {
  const queryClient = useQueryClient();
  const restart = useReplayStore((s) => s.restart);
  const run = useCallback(() => {
    if (!restart) return;
    useStreamStore.getState().reset();
    for (const key of [qk.traces.all, qk.audits.all, qk.decisions.all, qkc.overview.all, qkc.evidence.all]) {
      queryClient.removeQueries({ queryKey: key });
    }
    restart();
  }, [queryClient, restart]);
  return restart ? run : null;
}

/** Refreshes the numbers once the tape has played to the end. */
export function useRefreshWhenFinished(): void {
  const queryClient = useQueryClient();
  const finished = useReplayStore((s) => s.finished);
  useEffect(() => {
    if (!finished) return;
    void queryClient.invalidateQueries({ queryKey: qkc.overview.all });
    void queryClient.invalidateQueries({ queryKey: qkc.evidence.all });
    void queryClient.invalidateQueries({ queryKey: qk.traces.lists() });
  }, [finished, queryClient]);
}
