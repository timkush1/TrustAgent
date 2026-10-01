import { useEffect, useState } from 'react';
import { replayClock, useReplayStore } from '@/data/replay/replayStore';

/**
 * The current time, refreshed every `intervalMs`, so relative times ("12 s ago") stay true
 * without every row keeping a timer of its own. While the static build replays a recording it is
 * the tape's clock instead (the recording's time at the replay position, as the replay's Overview
 * counts it): a replayed request reads "just now" as it arrives, not "28 Sept, 12:03" days after
 * the recording was made. The exact time stays in each row's title.
 */
export function useNow(intervalMs = 15_000): number {
  const [now, setNow] = useState(() => Date.now());
  const tape = useReplayStore(replayClock);
  const replaying = tape !== null;
  useEffect(() => {
    if (replaying) return undefined;
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs, replaying]);
  return tape ?? now;
}
