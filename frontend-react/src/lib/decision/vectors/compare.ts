/**
 * The comparison rules of contracts/vectors (README "Comparing expected"): objects in expected are
 * subsets at every depth, arrays match in length and order, numbers match as parsed values within
 * the tolerance, and strings, booleans and null match exactly (a null in expected needs a present
 * null). Port of backend-go/internal/decision/vectors/compare.go.
 */

/** The tolerance when a vector does not set one. */
export const DEFAULT_TOLERANCE = 1e-9;

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Short JSON rendering for diff lines. */
function show(v: unknown): string {
  let s: string;
  try {
    s = JSON.stringify(v) ?? String(v);
  } catch {
    s = String(v);
  }
  return s.length > 200 ? `${s.slice(0, 200)}...` : s;
}

/** Checks actual against expected; returns one line per mismatch, prefixed with its JSON path. */
export function compareVector(expected: unknown, actual: unknown, tol = DEFAULT_TOLERANCE): string[] {
  const diffs: string[] = [];
  compare('$', expected, actual, tol, diffs);
  return diffs;
}

function compare(at: string, want: unknown, got: unknown, tol: number, diffs: string[]): void {
  if (isObj(want)) {
    if (!isObj(got)) {
      diffs.push(`${at}: want an object, got ${show(got)}`);
      return;
    }
    for (const k of Object.keys(want).sort()) {
      if (!Object.hasOwn(got, k) || got[k] === undefined) {
        diffs.push(`${at}.${k}: missing (want ${show(want[k])})`);
        continue;
      }
      compare(`${at}.${k}`, want[k], got[k], tol, diffs);
    }
    return;
  }
  if (Array.isArray(want)) {
    if (!Array.isArray(got)) {
      diffs.push(`${at}: want an array, got ${show(got)}`);
      return;
    }
    if (got.length !== want.length) {
      diffs.push(`${at}: want ${want.length} elements ${show(want)}, got ${got.length} ${show(got)}`);
      return;
    }
    want.forEach((w, i) => compare(`${at}[${i}]`, w, got[i], tol, diffs));
    return;
  }
  if (typeof want === 'number') {
    if (typeof got !== 'number' || Number.isNaN(got) || Math.abs(got - want) > tol) diffs.push(`${at}: want ${show(want)}, got ${show(got)}`);
    return;
  }
  // string, boolean, null
  if (want !== got) diffs.push(`${at}: want ${show(want)}, got ${show(got)}`);
}
