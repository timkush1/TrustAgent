/** Small scale helpers shared by the custom (non-Recharts) chart primitives. */

export function clamp01(x: number): number {
  return Math.min(1, Math.max(0, x));
}

/** A value in [0, 1] as a CSS percentage, clamped. */
export function pct01(x: number): string {
  return `${clamp01(x) * 100}%`;
}

/**
 * Evenly spaced round ticks from 0 that cover `max`: a step of 1, 2 or 5 × 10ⁿ, whichever gives
 * the number of intervals closest to `count` (the larger step on a tie). A non-positive or
 * non-finite max gives [0, 1].
 */
export function niceTicks(max: number, count = 4): number[] {
  if (!(max > 0) || !Number.isFinite(max)) return [0, 1];
  const pow = 10 ** Math.floor(Math.log10(max / Math.max(1, count)));
  let step = pow;
  let best = Number.POSITIVE_INFINITY;
  for (const m of [1, 2, 5, 10]) {
    const s = m * pow;
    const miss = Math.abs(Math.ceil(max / s) - count);
    if (miss <= best) {
      best = miss;
      step = s;
    }
  }
  const n = Math.ceil(max / step);
  return Array.from({ length: n + 1 }, (_, i) => Math.round(i * step * 1e6) / 1e6);
}
