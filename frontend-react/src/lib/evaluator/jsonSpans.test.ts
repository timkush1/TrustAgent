// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { fromPointer, lineCol, scanJson, spanAt, toPointer } from './jsonSpans';

describe('scanJson', () => {
  it('records the range of every value and key by pointer', () => {
    const text = '{"a": [1, {"b~/c": "x\\"y"}], "e": null, "f": -1.5e3, "g": true}';
    const scan = scanJson(text);
    if (!scan.ok) throw new Error(scan.message);
    const at = (p: string) => {
      const s = scan.spans.get(p);
      return s ? text.slice(s.from, s.to) : undefined;
    };
    expect(at('')).toBe(text);
    expect(at('/a')).toBe('[1, {"b~/c": "x\\"y"}]');
    expect(at('/a/0')).toBe('1');
    expect(at('/a/1/b~0~1c')).toBe('"x\\"y"');
    expect(at('/f')).toBe('-1.5e3');
    const key = scan.spans.get('/e');
    expect(text.slice(key?.keyFrom, key?.keyTo)).toBe('"e"');
  });

  it('agrees with JSON.parse on what is valid', () => {
    const valid = ['{}', '[]', '"\\u00e9"', '0', '-0.5', '1E+2', ' {"a" : [ ] } ', '"a\\/b"'];
    for (const t of valid) {
      expect(scanJson(t).ok, t).toBe(true);
      expect(() => JSON.parse(t)).not.toThrow();
    }
    const invalid = ['', '{', '{"a":1,}', '[1,]', '{a:1}', "'x'", '01', '1.', '"\\x"', '"a\nb"', 'tru', '{} {}', '"\\u12"', '-'];
    for (const t of invalid) {
      expect(scanJson(t).ok, t).toBe(false);
      expect(() => JSON.parse(t), t).toThrow();
    }
  });

  it('reports where the first syntax error is', () => {
    expect(scanJson('{"a": 1 "b": 2}')).toEqual({ ok: false, message: 'Expected “,” or “}”, found “"”.', offset: 8 });
    expect(scanJson('{"a": "open')).toEqual({ ok: false, message: 'A string is not closed (missing ").', offset: 6 });
    expect(scanJson('[1')).toEqual({ ok: false, message: 'Expected “,” or “]”, found the end of the text.', offset: 2 });
  });
});

describe('pointers and positions', () => {
  it('escapes and unescapes pointer segments', () => {
    expect(toPointer(['a/b', 'c~d', 0])).toBe('/a~1b/c~0d/0');
    expect(fromPointer('/a~1b/c~0d/0')).toEqual(['a/b', 'c~d', '0']);
    expect(fromPointer('')).toEqual([]);
  });

  it('counts lines and columns from 1', () => {
    expect(lineCol('ab\ncd', 0)).toEqual({ line: 1, col: 1 });
    expect(lineCol('ab\ncd', 4)).toEqual({ line: 2, col: 2 });
  });

  it('falls back to the nearest value that exists, or the key when asked', () => {
    const text = '{"a": {"b": 1}}';
    const scan = scanJson(text);
    if (!scan.ok) throw new Error(scan.message);
    const range = spanAt(scan.spans, '/a/missing');
    expect(text.slice(range.from, range.to)).toBe('{"b": 1}');
    const key = spanAt(scan.spans, '/a/b', true);
    expect(text.slice(key.from, key.to)).toBe('"b"');
  });
});
