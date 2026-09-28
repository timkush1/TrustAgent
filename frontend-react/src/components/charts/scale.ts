/** Small scale helpers shared by the custom (non-Recharts) chart primitives. */

export function clamp01(x: number): number {
  return Math.min(1, Math.max(0, x));
}

/** A value in [0, 1] as a CSS percentage, clamped. */
export function pct01(x: number): string {
  return `${clamp01(x) * 100}%`;
}
