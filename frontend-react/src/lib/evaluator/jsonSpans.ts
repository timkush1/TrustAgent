/**
 * Where each value of a JSON text is (RFC 8259): the character range of every value and of every
 * object key, by JSON pointer, so Studio's JSON tab can underline the exact field a schema error or
 * lint names. JSON.parse gives no positions and words its syntax errors differently per engine, so
 * this small scanner also reports the first syntax error with its offset.
 *
 * Offsets are UTF-16 code units into the text, half-open (CLAUDE.md "text spans"), which is what
 * CodeMirror uses.
 */

export interface JsonSpan {
  /** The value's range. */
  from: number;
  to: number;
  /** The range of the key (with its quotes) when the value is an object member. */
  keyFrom?: number;
  keyTo?: number;
}

export type JsonScan = { ok: true; spans: Map<string, JsonSpan> } | { ok: false; message: string; offset: number };

/** Deeper than this is refused as a syntax problem (Go refuses evaluator documents past 32 levels). */
const MAX_DEPTH = 256;

class ScanError extends Error {
  readonly offset: number;
  constructor(message: string, offset: number) {
    super(message);
    this.offset = offset;
  }
}

/** A JSON pointer ("/question/criteria/no_blame") from its segments; "" is the document. */
export function toPointer(segments: readonly (string | number)[]): string {
  return segments.map((s) => `/${String(s).replace(/~/g, '~0').replace(/\//g, '~1')}`).join('');
}

/** The segments of a JSON pointer (array indexes stay strings). */
export function fromPointer(pointer: string): string[] {
  if (pointer === '') return [];
  return pointer
    .slice(1)
    .split('/')
    .map((s) => s.replace(/~1/g, '/').replace(/~0/g, '~'));
}

/** 1-based line and column of an offset. */
export function lineCol(text: string, offset: number): { line: number; col: number } {
  let line = 1;
  let start = 0;
  const end = Math.min(Math.max(offset, 0), text.length);
  for (let i = 0; i < end; i++) {
    if (text.charCodeAt(i) === 10) {
      line += 1;
      start = i + 1;
    }
  }
  return { line, col: end - start + 1 };
}

const NUMBER = /-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/y;

export function scanJson(text: string): JsonScan {
  const spans = new Map<string, JsonSpan>();
  let i = 0;

  const fail = (message: string, at = i): never => {
    throw new ScanError(message, at);
  };
  const ws = () => {
    while (i < text.length) {
      const c = text.charCodeAt(i);
      if (c === 32 || c === 9 || c === 10 || c === 13) i += 1;
      else break;
    }
  };
  const describe = (at: number) => (at >= text.length ? 'the end of the text' : `“${text[at]}”`);

  const string = (): string => {
    const start = i;
    i += 1; // the opening quote
    while (i < text.length) {
      const c = text.charCodeAt(i);
      if (c === 34) {
        i += 1;
        return JSON.parse(text.slice(start, i)) as string;
      }
      if (c === 92) {
        const e = text[i + 1];
        if (e === 'u') {
          if (!/^[0-9a-fA-F]{4}$/.test(text.slice(i + 2, i + 6))) fail('A \\u escape needs four hex digits.', i);
          i += 6;
        } else if (e !== undefined && '"\\/bfnrt'.includes(e)) {
          i += 2;
        } else {
          fail('Unknown escape in a string.', i);
        }
        continue;
      }
      if (c < 32) fail('A string cannot contain a raw line break or control character; write \\n instead.', i);
      i += 1;
    }
    return fail('A string is not closed (missing ").', start);
  };

  const value = (path: (string | number)[], depth: number, key?: { from: number; to: number }) => {
    if (depth > MAX_DEPTH) fail('The document is nested too deeply.');
    ws();
    const from = i;
    const c = text[i];
    if (c === '{') {
      i += 1;
      ws();
      if (text[i] === '}') {
        i += 1;
      } else {
        for (;;) {
          ws();
          if (text[i] !== '"') fail(`Expected a key in quotes, found ${describe(i)}.`);
          const keyFrom = i;
          const name = string();
          const keyTo = i;
          ws();
          if (text[i] !== ':') fail(`Expected “:” after the key, found ${describe(i)}.`);
          i += 1;
          value([...path, name], depth + 1, { from: keyFrom, to: keyTo });
          ws();
          if (text[i] === ',') {
            i += 1;
            continue;
          }
          if (text[i] === '}') {
            i += 1;
            break;
          }
          fail(`Expected “,” or “}”, found ${describe(i)}.`);
        }
      }
    } else if (c === '[') {
      i += 1;
      ws();
      if (text[i] === ']') {
        i += 1;
      } else {
        for (let n = 0; ; n++) {
          value([...path, n], depth + 1);
          ws();
          if (text[i] === ',') {
            i += 1;
            continue;
          }
          if (text[i] === ']') {
            i += 1;
            break;
          }
          fail(`Expected “,” or “]”, found ${describe(i)}.`);
        }
      }
    } else if (c === '"') {
      string();
    } else if (text.startsWith('true', i) || text.startsWith('null', i)) {
      i += 4;
    } else if (text.startsWith('false', i)) {
      i += 5;
    } else {
      NUMBER.lastIndex = i;
      const m = NUMBER.exec(text);
      if (!m || m[0] === '' || m[0] === '-') fail(`Expected a value, found ${describe(i)}.`);
      i += (m as RegExpExecArray)[0].length;
    }
    const span: JsonSpan = { from, to: i };
    if (key) {
      span.keyFrom = key.from;
      span.keyTo = key.to;
    }
    spans.set(toPointer(path), span);
  };

  try {
    value([], 0);
    ws();
    if (i < text.length) fail(`Unexpected ${describe(i)} after the end of the document.`);
    return { ok: true, spans };
  } catch (err) {
    if (err instanceof ScanError) return { ok: false, message: err.message, offset: err.offset };
    throw err;
  }
}

/**
 * The range to underline for a problem at `pointer`: the value itself, the key of an unknown or
 * misnamed member (`atKey`), or the nearest enclosing value that exists (a missing member is
 * reported on its object).
 */
export function spanAt(spans: ReadonlyMap<string, JsonSpan>, pointer: string, atKey = false): { from: number; to: number } {
  let segments = fromPointer(pointer);
  for (;;) {
    const span = spans.get(toPointer(segments));
    if (span) {
      if (atKey && span.keyFrom !== undefined && span.keyTo !== undefined) return { from: span.keyFrom, to: span.keyTo };
      return { from: span.from, to: span.to };
    }
    if (segments.length === 0) return { from: 0, to: 0 };
    segments = segments.slice(0, -1);
  }
}
