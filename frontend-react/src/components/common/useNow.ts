import { useEffect, useState } from 'react';

/**
 * The current time, refreshed every `intervalMs`, so relative times ("12 s ago") stay true
 * without every row keeping a timer of its own.
 */
export function useNow(intervalMs = 15_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}
