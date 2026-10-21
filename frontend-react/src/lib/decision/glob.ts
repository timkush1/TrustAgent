/**
 * Shell-style name patterns of the policy (request paths, model names, untrusted tool names; SPEC
 * §2.7, §2.10). Port of backend-go/internal/glob: '*' matches any run of characters (including '/'
 * and the empty run), '?' exactly one character; there are no classes and no escapes. Matching works
 * on code points.
 */

/** Whether name matches pattern as a whole. */
export function globMatch(pattern: string, name: string): boolean {
  const p = Array.from(pattern);
  const n = Array.from(name);
  // Iterative matching with single-star backtracking: when a later literal fails, the most recent
  // '*' absorbs one more code point of name and matching resumes after it.
  let pi = 0;
  let ni = 0;
  let starP = -1;
  let starN = 0;
  while (ni < n.length) {
    if (pi < p.length) {
      if (p[pi] === '*') {
        starP = pi;
        starN = ni;
        pi++;
        continue;
      }
      if (p[pi] === '?' || p[pi] === n[ni]) {
        pi++;
        ni++;
        continue;
      }
    }
    if (starP < 0) return false;
    starN++;
    pi = starP + 1;
    ni = starN;
  }
  while (pi < p.length && p[pi] === '*') pi++;
  return pi === p.length;
}

/** Whether name matches at least one of patterns. */
export function globAny(patterns: readonly string[], name: string): boolean {
  return patterns.some((p) => globMatch(p, name));
}
