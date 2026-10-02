/**
 * Immutable edits to one field of an evaluator spec, for Studio's Advanced tab: every other member
 * (and its order) stays as it was, and `undefined` removes an optional member.
 */
import type { EvaluatorSpec } from '@/types/api';

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);

function setIn(target: unknown, path: readonly string[], value: unknown): unknown {
  const [key, ...rest] = path;
  const base: Obj = isObj(target) ? { ...target } : {};
  if (rest.length === 0) {
    if (value === undefined) delete base[key];
    else base[key] = value;
    return base;
  }
  base[key] = setIn(base[key], rest, value);
  return base;
}

/** `spec` with the field at `path` (object keys) set to `value`, or removed when `value` is undefined. */
export function withField(spec: EvaluatorSpec, path: readonly string[], value: unknown): EvaluatorSpec {
  if (path.length === 0) throw new Error('withField needs a path.');
  return setIn(spec, path, value) as EvaluatorSpec;
}

/** The value at `path`, or undefined. */
export function fieldAt(spec: unknown, path: readonly string[]): unknown {
  let cur = spec;
  for (const key of path) {
    if (!isObj(cur)) return undefined;
    cur = cur[key];
  }
  return cur;
}

/** "a, b , c" → ["a", "b", "c"] (empty items dropped, duplicates kept once). */
export function parseList(text: string): string[] {
  return [...new Set(text.split(',').map((s) => s.trim()).filter(Boolean))];
}

/** A threshold typed by the viewer: a number in [0, 1], null for empty, or undefined when invalid. */
export function parseThreshold(text: string): number | null | undefined {
  const t = text.trim();
  if (t === '') return null;
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 && n <= 1 ? n : undefined;
}

/** A spec as the JSON tab and the save diff show it: two-space indents and a final newline. */
export function specJson(spec: unknown): string {
  return `${JSON.stringify(spec, null, 2)}\n`;
}

/**
 * Whether a document carries what Studio's steps read from a spec: a string id and name, the
 * question, the evidence, the state fields, and the outcomes with a numeric `warn_at`. The JSON tab
 * hands only such a document to the other steps; anything else is still checked and listed there,
 * while the steps keep the last document that had them.
 */
export function isWorkableSpec(doc: unknown): doc is EvaluatorSpec {
  if (!isObj(doc) || typeof doc.id !== 'string' || typeof doc.name !== 'string') return false;
  if (!isObj(doc.question) || !isObj(doc.evidence) || !Array.isArray(doc.state_fields) || !isObj(doc.outcomes)) return false;
  return typeof doc.outcomes.warn_at === 'number' && Number.isFinite(doc.outcomes.warn_at);
}

/** Whether two specs say the same apart from their version number (member order counts). */
export function sameSpec(a: EvaluatorSpec, b: EvaluatorSpec): boolean {
  return JSON.stringify({ ...a, version: 0 }) === JSON.stringify({ ...b, version: 0 });
}
