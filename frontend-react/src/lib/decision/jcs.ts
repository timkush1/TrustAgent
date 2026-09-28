/**
 * Canonical JSON (SPEC §2.1): RFC 8785 (JCS) with object keys sorted by UTF-16 code units, ES6
 * number formatting, no insignificant whitespace and no HTML escaping, plus two TrustAgent rules:
 * U+2028 and U+2029 are written as \u2028 and \u2029, and lone surrogates become U+FFFD.
 * Port of backend-go/internal/decision/jcs.go; pinned by contracts/vectors/canonical.
 */
import { sha256Hex as sha256HexOfText } from './sha256';
import { compareUTF16, toWellFormed } from './text';

/** A value canonicalize() accepts: JSON values, with undefined object members skipped. */
export type JsonValue = null | boolean | number | string | readonly JsonValue[] | { readonly [key: string]: JsonValue | undefined };

/** Raised for a value that has no canonical JSON form (NaN, Infinity, a function, a Map...). */
export class CanonicalJsonError extends Error {
  constructor(message: string) {
    super(`canonical json: ${message}`);
    this.name = 'CanonicalJsonError';
  }
}

/** Returns the canonical JSON text of value. */
export function canonicalize(value: unknown): string {
  const out: string[] = [];
  write(value, out);
  return out.join('');
}

/** The lowercase hex SHA-256 of the UTF-8 canonical form of value. */
export function sha256Hex(value: unknown): string {
  return sha256HexOfText(canonicalize(value));
}

/** Parses a JSON document and returns its canonical form. */
export function canonicalizeJSON(doc: string): string {
  return canonicalize(JSON.parse(doc));
}

function isPlainObject(v: object): boolean {
  const proto: unknown = Object.getPrototypeOf(v);
  return proto === Object.prototype || proto === null;
}

function write(v: unknown, out: string[]): void {
  switch (typeof v) {
    case 'string':
      out.push(quote(v));
      return;
    case 'number':
      out.push(formatNumber(v));
      return;
    case 'boolean':
      out.push(v ? 'true' : 'false');
      return;
    case 'object':
      break;
    default:
      throw new CanonicalJsonError(`${typeof v} is not a JSON value`);
  }
  if (v === null) {
    out.push('null');
    return;
  }
  if (Array.isArray(v)) {
    out.push('[');
    v.forEach((e: unknown, i) => {
      if (i > 0) out.push(',');
      if (e === undefined) throw new CanonicalJsonError('an array element is undefined');
      write(e, out);
    });
    out.push(']');
    return;
  }
  const toJSON = (v as { toJSON?: unknown }).toJSON;
  if (typeof toJSON === 'function') {
    write(toJSON.call(v), out);
    return;
  }
  if (!isPlainObject(v)) throw new CanonicalJsonError(`${Object.prototype.toString.call(v)} is not a JSON value`);
  // Keys are made well-formed before sorting, as Go's decoder does; a later duplicate wins.
  const members = new Map<string, unknown>();
  for (const [k, e] of Object.entries(v)) {
    if (e !== undefined) members.set(toWellFormed(k), e);
  }
  const keys = [...members.keys()].sort(compareUTF16);
  out.push('{');
  keys.forEach((k, i) => {
    if (i > 0) out.push(',');
    out.push(quote(k), ':');
    write(members.get(k), out);
  });
  out.push('}');
}

/**
 * ES6 Number.prototype.toString (RFC 8785 §3.2.2.3): shortest round-trip digits, plain notation for
 * 1e-6 <= |x| < 1e21, exponent notation otherwise ("1e+21", "1e-7"); -0 is written as 0.
 */
export function formatNumber(x: number): string {
  if (!Number.isFinite(x)) throw new CanonicalJsonError(`${x} is not a JSON number`);
  return Object.is(x, -0) ? '0' : String(x);
}

const SEPARATORS = /[\u2028\u2029]/g;

/**
 * A JSON string as ES6 JSON.stringify writes it (\" \\ \b \f \n \r \t, other controls as \u00xx),
 * plus the U+2028/U+2029 escapes, with lone surrogates replaced by U+FFFD.
 */
function quote(s: string): string {
  return JSON.stringify(toWellFormed(s)).replace(SEPARATORS, (c) => (c === '\u2028' ? '\\u2028' : '\\u2029'));
}
