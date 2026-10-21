/**
 * Float rules of SPEC §2.1 and §2.9 (backend-go/internal/decision/floats.go).
 */

/** Comparison tolerance: a threshold t is reached when x >= t - EPS; a tolerance tol is exceeded when x > tol + EPS. */
export const EPS = 1e-9;

/** Limits x to [0, 1]. */
export function clamp01(x: number): number {
  if (x < 0) return 0;
  if (x > 1) return 1;
  return x;
}

/**
 * Rounds x to two decimals as SPEC §2.1 prescribes: floor(x*100 + 0.5) / 100. Never Math.round,
 * whose tie handling differs from other languages' round().
 */
export function r2(x: number): number {
  return Math.floor(x * 100 + 0.5) / 100;
}

/** Formats a risk for the X-TrustAgent-Risk header: r2 with exactly two decimals. */
export function formatRisk(x: number): string {
  return r2(x).toFixed(2);
}
