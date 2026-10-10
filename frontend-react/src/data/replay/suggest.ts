/**
 * Evidence suggestions in the browser (SPEC §7.1 "Coverage and suggestions", D64): the gateway's
 * scan (backend-go internal/evidence/suggest.go) over the replayed answers that were checked
 * against no evidence. It looks for document shapes the doc-blocks rules do not know (an unknown
 * <tag>, a markdown heading, a "Title:" header line); each shape becomes a candidate route
 * evidence.tag_patterns entry, and would_gain counts the scanned requests that would have carried
 * evidence with it, by running the real extraction (lib/decision/evidence, the TS twin of the
 * gateway's). The trustagent.context extension is always offered too.
 *
 * Kept in step with suggest.go: the same shapes, limits, ranking and labels; block sizes count
 * UTF-8 bytes and patterns compare byte-wise, as Go does. Loaded only when the Overview's
 * "Why not?" panel asks for suggestions.
 */
import type { EvidenceSettings } from '@/lib/contracts/types.gen';
import { extractEvidence } from '@/lib/decision/evidence';
import { compileRE2 } from '@/lib/decision/re2';
import { DEFAULT_ROUTE_SOURCES, mergeSettings, resolveRoute } from '@/lib/gate/route';
import type { PolicyInput } from '@/lib/gate/types';
import type { EvidenceSuggestion } from '@/types/api';

/** The most bodies one scan reads. */
export const MAX_SUGGEST_SCAN = 500;
/** The most suggestions returned, the extension included. */
export const MAX_SUGGESTIONS = 10;
/** The longest pattern suggested, in characters (runes). */
export const MAX_SUGGEST_PATTERN = 256;
/** The flags a suggested pattern may use. */
const SUGGEST_FLAGS = 'ims';
/** How many shapes are tried against every body. */
const MAX_CANDIDATES = 20;
/** The least text (UTF-8 bytes, trimmed) a block needs to count as a document. */
export const MIN_BLOCK_BYTES = 40;

export const LABEL_EXTENSION_SUGGESTION = 'Send documents in the trustagent.context extension';

/** One stored request message (TraceDetail.body.messages). */
export interface ScanMessage {
  role: string;
  content: string;
  name?: string;
}

/** One answer the scan reads: its trace, route and request messages. */
export interface ScanBody {
  traceId: string;
  route: string;
  messages: readonly ScanMessage[];
}

type ShapeKind = 'tag_pattern' | 'line_header';

/** One document shape found in the bodies, as a candidate pattern. */
interface Shape {
  kind: ShapeKind;
  pattern: { pattern: string; flags: string };
  label: string;
  /** The bodies the shape appears in. */
  bodies: number;
  /** Those where it is first seen in a system or developer message. */
  inSystem: number;
}

const TAG_OPEN = /<([A-Za-z][A-Za-z0-9_.:-]{0,47})(?:[\t\n\f\r ][^<>]*)?>/g;
const TAG_CLOSE = /<\/([A-Za-z][A-Za-z0-9_.:-]{0,47})>/g;
// RE2 (?m) ^ and $ break lines at \n only; the u flag counts runes, as Go's {0,58} does.
const HEADING = /(?<![^\n])#{1,6}[\t\f\r ]+([^\n#][^\n]{0,58}?)[\t\f\r ]*(?![^\n])/gu;
const COLON_HEADER = /(?<![^\n])([A-Z][A-Za-z0-9 '&/-]{2,40}):[\t\f\r ]*(?![^\n])/g;

const INSTRUCTION_LIKE: ReadonlySet<string> = new Set([
  'instruction', 'instructions', 'rule', 'rules', 'guideline', 'guidelines', 'persona', 'role', 'tone', 'style',
  'format', 'output_format', 'output format', 'output', 'example', 'examples', 'task', 'constraint', 'constraints',
  'system', 'thinking', 'scratchpad', 'answer', 'response', 'question', 'query', 'user', 'assistant', 'goal', 'goals',
]);

const encoder = new TextEncoder();

/** Go's unicode.IsSpace set (strings.TrimSpace): ASCII space, NEL, NBSP and the Unicode White_Space runes. */
const GO_SPACE = '\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000';
const GO_TRIM = new RegExp(`^[${GO_SPACE}]+|[${GO_SPACE}]+$`, 'g');

/** Go's strings.TrimSpace (JavaScript's trim also drops U+FEFF and keeps U+0085). */
export function goTrimSpace(s: string): string {
  return s.replace(GO_TRIM, '');
}

/** Go's regexp.QuoteMeta: a backslash before each of \.+*?()|[]{}^$. */
export function quoteMeta(s: string): string {
  return s.replace(/[\\.+*?()|[\]{}^$]/g, '\\$&');
}

/** Go's strconv.Quote for the label text (control characters escaped; printable text as is). */
export function goQuote(s: string): string {
  const named: Record<string, string> = { '\x07': '\\a', '\b': '\\b', '\f': '\\f', '\n': '\\n', '\r': '\\r', '\t': '\\t', '\v': '\\v', '"': '\\"', '\\': '\\\\' };
  let out = '"';
  for (const ch of s) {
    const code = ch.codePointAt(0) ?? 0;
    if (named[ch] !== undefined) out += named[ch];
    else if (code < 0x20 || code === 0x7f) out += `\\x${code.toString(16).padStart(2, '0')}`;
    else if (code >= 0x80 && code <= 0x9f) out += `\\u${code.toString(16).padStart(4, '0')}`;
    else out += ch;
  }
  return `${out}"`;
}

/** Byte-wise order of two strings' UTF-8 encodings (Go's string comparison). */
function compareBytes(a: string, b: string): number {
  if (a === b) return 0;
  const x = encoder.encode(a);
  const y = encoder.encode(b);
  const n = Math.min(x.length, y.length);
  for (let i = 0; i < n; i++) if (x[i] !== y[i]) return x[i] - y[i];
  return x.length - y.length;
}

/**
 * A text's UTF-8 byte offsets, and a constant-time answer to whether a byte span holds a block:
 * at least MIN_BLOCK_BYTES bytes once the ASCII white space around it is trimmed (suggest.go
 * textIndex).
 */
export class TextIndex {
  /** UTF-16 index → UTF-8 byte offset (length + 1 entries). */
  private readonly byteOf: Int32Array;
  /** Byte offset → the first byte at or after it that is not ASCII white space. */
  private readonly next: Int32Array;
  readonly bytes: number;

  constructor(text: string) {
    this.byteOf = new Int32Array(text.length + 1);
    let b = 0;
    for (let i = 0; i < text.length; i++) {
      this.byteOf[i] = b;
      const c = text.charCodeAt(i);
      if (c < 0x80) b += 1;
      else if (c < 0x800) b += 2;
      else if (c >= 0xd800 && c <= 0xdbff && i + 1 < text.length && (text.charCodeAt(i + 1) & 0xfc00) === 0xdc00) {
        // A surrogate pair is one 4-byte rune; its low half sits inside it.
        this.byteOf[i + 1] = b + 2;
        b += 4;
        i++;
      } else b += 3; // the rest of the BMP, and a lone surrogate (U+FFFD in UTF-8)
    }
    this.byteOf[text.length] = b;
    this.bytes = b;
    const raw = encoder.encode(text);
    this.next = new Int32Array(raw.length + 1);
    this.next[raw.length] = raw.length;
    for (let i = raw.length - 1; i >= 0; i--) {
      const c = raw[i];
      this.next[i] = c === 0x20 || c === 0x09 || c === 0x0a || c === 0x0b || c === 0x0c || c === 0x0d ? this.next[i + 1] : i;
    }
  }

  /** The UTF-8 byte offset of a UTF-16 index. */
  at(index: number): number {
    return this.byteOf[index];
  }

  /** Whether bytes [from, to) hold a block. */
  block(from: number, to: number): boolean {
    if (from < 0 || from > this.bytes) return false;
    const f = this.next[from];
    const last = f + MIN_BLOCK_BYTES - 1;
    return f < to && last < to && last <= this.bytes && this.next[last] < to;
  }
}

/** Byte offsets of every occurrence of sub (two characters or more, ASCII) in text, overlapping ones included. */
export function occurrences(text: string, sub: string, ix: TextIndex): number[] {
  const out: number[] = [];
  for (let from = 0; ; ) {
    const i = text.indexOf(sub, from);
    if (i < 0) return out;
    out.push(ix.at(i));
    from = i + 1;
  }
}

/** The first of sorted that is at least from, or -1. */
export function firstAtOrAfter(sorted: readonly number[], from: number): number {
  let lo = 0;
  let hi = sorted.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (sorted[mid] < from) lo = mid + 1;
    else hi = mid;
  }
  return lo < sorted.length ? sorted[lo] : -1;
}

/** Where the section that starts at byte `from` ends: at the first stop after it, else at `end`. */
export function sectionEnd(stops: readonly number[], from: number, end: number): number {
  const at = firstAtOrAfter(stops, from + 1);
  return at >= 0 ? at : end;
}

/** The closing tags of text: per lower-cased name, the byte offsets where they start, ascending. */
export function closingTags(text: string, ix: TextIndex): Map<string, number[]> {
  const out = new Map<string, number[]>();
  for (const m of text.matchAll(TAG_CLOSE)) {
    const key = m[1].toLowerCase();
    const list = out.get(key) ?? [];
    list.push(ix.at(m.index));
    out.set(key, list);
  }
  return out;
}

/**
 * The document shapes of one message text: unknown tags wrapping a block, markdown headings and
 * "Title:" header lines followed by one. Instruction-like names are left out. Each shape once.
 */
export function shapesOf(text: string): Shape[] {
  const out: Shape[] = [];
  const seen = new Set<string>();
  const add = (kind: ShapeKind, pattern: string, flags: string, label: string) => {
    if (seen.has(pattern)) return;
    seen.add(pattern);
    out.push({ kind, pattern: { pattern, flags }, label, bodies: 0, inSystem: 0 });
  };
  const ix = new TextIndex(text);
  const closes = closingTags(text, ix);
  for (const m of text.matchAll(TAG_OPEN)) {
    const name = m[1];
    const key = name.toLowerCase(); // tag names are ASCII
    if (INSTRUCTION_LIKE.has(key)) continue;
    const openEnd = ix.at(m.index + m[0].length);
    const at = firstAtOrAfter(closes.get(key) ?? [], openEnd);
    if (at < 0 || !ix.block(openEnd, at)) continue;
    const q = quoteMeta(name);
    add('tag_pattern', `<${q}(?:[\\t\\n\\f\\r ][^>]*)?>(.*?)</${q}>`, 'is', `<${name}> blocks`);
  }
  const headingEnds = occurrences(text, '\n#', ix);
  for (const m of text.matchAll(HEADING)) {
    const title = goTrimSpace(m[1]);
    const end = ix.at(m.index + m[0].length);
    if (title === '' || INSTRUCTION_LIKE.has(title.toLowerCase()) || !ix.block(end, sectionEnd(headingEnds, end, ix.bytes))) continue;
    add('line_header', `^#{1,6}[\\t\\f\\r ]*${quoteMeta(title)}[\\t\\f\\r ]*\\n(.*?)(?:\\n#{1,6}[\\t\\f\\r ]|\\z)`, 'ims', `${goQuote(`## ${title}`)} sections`);
  }
  const paragraphEnds = occurrences(text, '\n\n', ix);
  for (const m of text.matchAll(COLON_HEADER)) {
    const title = m[1];
    const end = ix.at(m.index + m[0].length);
    if (INSTRUCTION_LIKE.has(title.toLowerCase()) || !ix.block(end, sectionEnd(paragraphEnds, end, ix.bytes))) continue;
    add('line_header', `^${quoteMeta(title)}:[\\t\\f\\r ]*\\n(.*?)(?:\\n[\\t\\f\\r ]*\\n|\\z)`, 'ims', `${goQuote(`${title}:`)} sections`);
  }
  return out;
}

/** Whether a pattern may be suggested (D64): RE2, at most 256 characters, flags within "ims". */
export function validSuggestion(p: { pattern: string; flags?: string }): boolean {
  if (p.pattern === '' || [...p.pattern].length > MAX_SUGGEST_PATTERN) return false;
  const flags = p.flags ?? '';
  if ([...flags].some((f) => !SUGGEST_FLAGS.includes(f))) return false;
  try {
    compileRE2(p.pattern, flags);
    return true;
  } catch {
    return false;
  }
}

/** Whether a body's request carries trusted evidence under settings (null: it cannot be read). */
function trusted(body: ScanBody, settings: EvidenceSettings): boolean | null {
  try {
    const request = { messages: body.messages.map((m) => ({ role: m.role, content: m.content, ...(m.name ? { name: m.name } : {}) })) };
    return extractEvidence('openai_chat', request, settings).items.some((it) => !it.untrusted);
  } catch {
    return null;
  }
}

/**
 * Scans bodies (most recent first; at most MAX_SUGGEST_SCAN are read) and returns at most
 * MAX_SUGGESTIONS suggestions, the most useful first, the extension last. settingsOf gives a
 * route's evidence settings. Nothing to scan gives no suggestion.
 */
export function suggestEvidence(all: readonly ScanBody[], settingsOf: (route: string) => EvidenceSettings): EvidenceSuggestion[] {
  const bodies = all.slice(0, MAX_SUGGEST_SCAN);
  if (bodies.length === 0) return [];

  const shapes = new Map<string, Shape>();
  for (const b of bodies) {
    const seen = new Set<string>();
    for (const m of b.messages) {
      if (m.role === 'assistant') continue;
      for (const sh of shapesOf(m.content.replace(/\r\n?/g, '\n'))) {
        const key = sh.pattern.pattern;
        if (seen.has(key)) continue;
        seen.add(key);
        const got = shapes.get(key) ?? sh;
        shapes.set(key, got);
        got.bodies += 1;
        if (m.role === 'system' || m.role === 'developer') got.inSystem += 1;
      }
    }
  }
  const minBodies = Math.min(2, bodies.length);
  const ranked = [...shapes.values()]
    .filter((sh) => sh.bodies >= minBodies)
    .sort((a, b) => b.bodies - a.bodies || compareBytes(a.pattern.pattern, b.pattern.pattern))
    .slice(0, MAX_CANDIDATES);

  // What each body yields today, per route settings; unreadable counts as having evidence, so a
  // suggestion is never credited with it.
  const had = bodies.map((b) => trusted(b, settingsOf(b.route)) ?? true);
  const out: EvidenceSuggestion[] = [];
  for (const sh of ranked) {
    if (!validSuggestion(sh.pattern)) continue;
    const s: EvidenceSuggestion = {
      kind: sh.kind,
      pattern: sh.pattern,
      label: `${sh.label}${sh.inSystem * 2 >= sh.bodies ? ' in the system prompt' : ' in user messages'}`,
      would_gain: 0,
      sample: bodies.length,
      example_trace_id: null,
    };
    const withShape = new Map<string, EvidenceSettings>();
    bodies.forEach((b, i) => {
      if (had[i]) return;
      let settings = withShape.get(b.route);
      if (settings === undefined) {
        const base = settingsOf(b.route);
        settings = { ...base, tag_patterns: [...(base.tag_patterns ?? []), sh.pattern] };
        withShape.set(b.route, settings);
      }
      if (trusted(b, settings) !== true) return;
      s.would_gain += 1;
      s.example_trace_id ??= b.traceId;
    });
    if (s.would_gain > 0) out.push(s);
  }
  // Stable: equal gains keep the ranking's order.
  out.sort((a, b) => b.would_gain - a.would_gain);
  return [
    ...out.slice(0, MAX_SUGGESTIONS - 1),
    { kind: 'extension', pattern: null, label: LABEL_EXTENSION_SUGGESTION, would_gain: 0, sample: bodies.length, example_trace_id: null },
  ];
}

/**
 * A route's evidence settings in a policy (the gateway's evidenceSettings): the route merged over
 * the policy's defaults, or the defaults for a route the policy does not have.
 */
export function policyEvidenceSettings(policy: PolicyInput): (route: string) => EvidenceSettings {
  const cache = new Map<string, EvidenceSettings>();
  return (route) => {
    let settings = cache.get(route);
    if (settings === undefined) {
      try {
        settings = resolveRoute(policy, route).evidence;
      } catch {
        settings = mergeSettings({ sources: DEFAULT_ROUTE_SOURCES }, policy.defaults?.evidence) as EvidenceSettings;
      }
      cache.set(route, settings);
    }
    return settings;
  };
}
