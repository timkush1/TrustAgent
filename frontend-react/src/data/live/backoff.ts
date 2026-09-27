/**
 * Reconnect delay (SPEC §2.5): full jitter `random(0, min(30000, 500·2^n))` with a 250 ms floor.
 * The client never gives up; the cap keeps the worst case at 30 s.
 */
export const BACKOFF_BASE_MS = 500;
export const BACKOFF_CAP_MS = 30_000;
export const BACKOFF_FLOOR_MS = 250;

export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const n = Math.max(0, Math.min(attempt, 16)); // 500·2^16 is far past the cap; avoids overflow
  const ceiling = Math.min(BACKOFF_CAP_MS, BACKOFF_BASE_MS * 2 ** n);
  return Math.max(BACKOFF_FLOOR_MS, Math.floor(random() * ceiling));
}
