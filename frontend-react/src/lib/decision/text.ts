/**
 * String helpers that reproduce the Go reference's text semantics in JavaScript.
 *
 * Go strings are UTF-8 and the Go reference counts code points ("runes"); JavaScript strings are
 * UTF-16, so every "n characters" rule here works on code points, and offsets that leave the
 * library are UTF-16 code units (SPEC §2.1). Go's JSON decoder turns a lone surrogate into U+FFFD;
 * toWellFormed does the same for strings that arrive through JSON.parse.
 */

const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g;

/** Replaces every lone surrogate with U+FFFD (String.prototype.toWellFormed, ES2024). */
export function toWellFormed(s: string): string {
  LONE_SURROGATE.lastIndex = 0;
  return LONE_SURROGATE.test(s) ? s.replace(LONE_SURROGATE, '\uFFFD') : s;
}

/** Compares two strings by UTF-16 code units: the key order of RFC 8785 and of Array.prototype.sort. */
export function compareUTF16(a: string, b: string): number {
  if (a < b) return -1;
  if (a > b) return 1;
  return 0;
}

/** Returns a sorted copy of keys in UTF-16 code unit order. */
export function sortedUTF16(keys: Iterable<string>): string[] {
  return [...keys].sort(compareUTF16);
}

/** Number of code points in s. */
export function codePointLength(s: string): number {
  let n = 0;
  for (const _ of s) n++;
  return n;
}

/** The first n code points of s. */
export function headCodePoints(s: string, n: number): string {
  if (n <= 0) return '';
  let units = 0;
  let count = 0;
  for (const ch of s) {
    if (count === n) break;
    units += ch.length;
    count++;
  }
  return s.slice(0, units);
}

/** The last n code points of s. */
export function tailCodePoints(s: string, n: number): string {
  if (n <= 0) return '';
  const cps = Array.from(s);
  return cps.slice(Math.max(0, cps.length - n)).join('');
}

// unicode.IsSpace in Go: the Latin-1 spaces plus the White_Space property.
const GO_SPACE = '\t\n\v\f\r \u0085\u00A0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200A\u2028\u2029\u202F\u205F\u3000';

/**
 * strings.TrimSpace: trims Go's unicode.IsSpace characters. It differs from String.prototype.trim,
 * which keeps U+0085 and removes U+FEFF.
 */
export function trimSpace(s: string): string {
  let start = 0;
  let end = s.length;
  while (start < end && GO_SPACE.includes(s[start])) start++;
  while (end > start && GO_SPACE.includes(s[end - 1])) end--;
  return s.slice(start, end);
}

/** strings.Trim(s, cutset): trims every leading and trailing code point found in cutset. */
export function trimChars(s: string, cutset: string): string {
  const cps = Array.from(s);
  const cut = new Set(Array.from(cutset));
  let start = 0;
  let end = cps.length;
  while (start < end && cut.has(cps[start])) start++;
  while (end > start && cut.has(cps[end - 1])) end--;
  return cps.slice(start, end).join('');
}

/** strings.TrimRight(s, cutset). */
export function trimRightChars(s: string, cutset: string): string {
  const cps = Array.from(s);
  const cut = new Set(Array.from(cutset));
  let end = cps.length;
  while (end > 0 && cut.has(cps[end - 1])) end--;
  return cps.slice(0, end).join('');
}

/**
 * strings.ToLower: lowercases code point by code point with the simple case mapping. Per-code-point
 * mapping avoids the context rules of String.prototype.toLowerCase (final sigma); U+0130 maps to
 * "i" as in Go, not to "i" plus a combining dot.
 */
export function goToLower(s: string): string {
  // eslint-disable-next-line no-control-regex
  if (/^[\x00-\x7F]*$/.test(s)) return s.toLowerCase();
  let out = '';
  for (const ch of s) {
    if (ch === '\u0130') {
      out += 'i';
      continue;
    }
    const lower = ch.toLowerCase();
    out += Array.from(lower).length === 1 ? lower : ch;
  }
  return out;
}

/** Lowercases ASCII A-Z only. */
export function asciiLower(s: string): string {
  return s.replace(/[A-Z]+/g, (m) => m.toLowerCase());
}

/** Own-property lookup that never reaches Object.prototype ("constructor", "__proto__"). */
export function own<T>(record: Readonly<Record<string, T>> | null | undefined, key: string): T | undefined {
  return record != null && Object.hasOwn(record, key) ? record[key] : undefined;
}

/** Whether record has key as an own property. */
export function has(record: object | null | undefined, key: string): boolean {
  return record != null && Object.hasOwn(record, key);
}

/** Builds a record from entries with data properties only (a "__proto__" key stays a key). */
export function record<T>(entries: Iterable<readonly [string, T]>): Record<string, T> {
  return Object.fromEntries(entries) as Record<string, T>;
}
