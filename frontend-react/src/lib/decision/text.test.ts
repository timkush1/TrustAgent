import { describe, expect, it } from 'vitest';
import { globAny, globMatch } from './glob';
import { compileRE2, findAll, translateRE2 } from './re2';
import {
  asciiLower,
  codePointLength,
  goToLower,
  headCodePoints,
  own,
  record,
  tailCodePoints,
  toWellFormed,
  trimChars,
  trimRightChars,
  trimSpace,
} from './text';
import { checkCPT, countTokens, est } from './tokens';

const EMOJI = String.fromCodePoint(0x1f600);
const NEL = String.fromCharCode(0x85);
const BOM = String.fromCharCode(0xfeff);
const LS = String.fromCharCode(0x2028);
const NBSP = String.fromCharCode(0xa0);

describe('text helpers (Go string semantics)', () => {
  it('replaces lone surrogates only', () => {
    expect(toWellFormed(`a${String.fromCharCode(0xd83d)}`)).toBe('a\uFFFD');
    expect(toWellFormed(`${String.fromCharCode(0xde00)}b`)).toBe('\uFFFDb');
    expect(toWellFormed(`x${EMOJI}y`)).toBe(`x${EMOJI}y`);
  });

  it('counts and cuts by code points', () => {
    expect(codePointLength(`a${EMOJI}b`)).toBe(3);
    expect(headCodePoints(`${EMOJI}${EMOJI}abc`, 2)).toBe(`${EMOJI}${EMOJI}`);
    expect(headCodePoints('abc', 10)).toBe('abc');
    expect(headCodePoints('abc', 0)).toBe('');
    expect(tailCodePoints(`abc${EMOJI}`, 2)).toBe(`c${EMOJI}`);
    expect(tailCodePoints('abc', 0)).toBe('');
  });

  it('trims like strings.TrimSpace: NEL is space, BOM is not', () => {
    expect(trimSpace(`${NEL} \t x y \n${LS}`)).toBe('x y');
    expect(trimSpace(`${BOM}x`)).toBe(`${BOM}x`);
    expect(trimChars('..hi!?', '.,;:!?')).toBe('hi');
    expect(trimChars(`\u2026quote\u2026`, '.,\u2026')).toBe('quote');
    expect(trimRightChars('https://a.example/x/.', './')).toBe('https://a.example/x');
  });

  it('lowercases per code point like strings.ToLower', () => {
    expect(goToLower('HeLLo')).toBe('hello');
    expect(goToLower('\u0130STANBUL')).toBe('istanbul');
    expect(goToLower('\u039F\u03A3')).toBe('\u03BF\u03C3'); // no final-sigma context rule
    expect(asciiLower('\u212A\u00C9A')).toBe('\u212A\u00C9a');
  });

  it('never reads inherited properties', () => {
    const r = record([
      ['__proto__', 1],
      ['a', 2],
    ]);
    expect(Object.keys(r)).toEqual(['__proto__', 'a']);
    expect(own(r, '__proto__')).toBe(1);
    expect(own({}, 'constructor')).toBeUndefined();
    expect(own(null, 'a')).toBeUndefined();
  });
});

describe('RE2 patterns in JavaScript', () => {
  it('keeps RE2 line semantics for ^ and $ with the m flag', () => {
    const re = compileRE2('^[0-9]+[.)] ', 'm');
    // RE2 breaks lines at \n only: "\r2. " and a line separator do not start a line.
    expect(findAll(re, `1. a\n2. b\r3. c${LS}4. d`).map((m) => m.text)).toEqual(['1. ', '2. ']);
    expect(findAll(compileRE2('x$', 'm'), 'x\nx\rx').map((m) => m.start)).toEqual([0, 4]);
  });

  it('keeps RE2 dot and \\s semantics', () => {
    expect(translateRE2('a.b', '')).toBe('a[^\\n]b');
    expect(translateRE2('a.b', 's')).toBe('a.b');
    expect(findAll(compileRE2('a.b'), 'a\rb a\nb').map((m) => m.start)).toEqual([0]);
    expect(findAll(compileRE2('\\s+'), `a${NBSP}b c`).map((m) => m.text)).toEqual([' ']);
    expect(translateRE2('[\\s,]\\S', '')).toBe('[\\t\\n\\f\\r ,][^\\t\\n\\f\\r ]');
  });

  it('translates RE2-only spellings', () => {
    expect(translateRE2('[]a]', '')).toBe('[\\]a]');
    expect(translateRE2('[^]a]', '')).toBe('[^\\]a]');
    expect(translateRE2('(?P<n>x)', '')).toBe('(?<n>x)');
    expect(findAll(compileRE2('\\Aab\\z'), 'ab').length).toBe(1);
    expect(findAll(compileRE2('\\Aab\\z'), 'ab\n').length).toBe(0);
    expect(translateRE2('\\^\\$\\.', 'm')).toBe('\\^\\$\\.');
  });

  it('reports group spans and honours a match limit', () => {
    const [m] = findAll(compileRE2('<doc([ ][^>]*)?>(.*?)</doc>', 'is'), 'x<DOC>a\nb</doc>');
    expect(m.start).toBe(1);
    expect(m.groups[1]).toBeUndefined();
    expect(m.groups[2]).toEqual([6, 9]);
    expect(findAll(compileRE2('[a-z]+'), 'ab cd ef', 2).map((x) => x.text)).toEqual(['ab', 'cd']);
    expect(findAll(compileRE2('[a-z]+'), 'ab cd', 0)).toEqual([]);
  });

  it('rejects flags outside ims', () => {
    expect(() => compileRE2('a', 'u')).toThrow(/subset of "ims"/);
  });
});

describe('glob', () => {
  it.each([
    ['web_*', 'web_search', true],
    ['web_*', 'get_web', false],
    ['*', '', true],
    ['a?c', 'abc', true],
    ['a?c', `a${EMOJI}c`, true],
    ['a*b*c', 'aXbYbZc', true],
    ['a*b', 'ab/cb', true],
    ['*.md', 'refund-policy.md', true],
    ['a', 'ab', false],
    ['ab*', 'a', false],
  ])('%s matches %s: %s', (pattern, name, want) => {
    expect(globMatch(pattern, name)).toBe(want);
  });

  it('matches any of several patterns', () => {
    expect(globAny(['x', 'web_*'], 'web_fetch')).toBe(true);
    expect(globAny([], 'web_fetch')).toBe(false);
  });
});

describe('token estimates', () => {
  it('counts narrow and wide code points of the canonical JSON', () => {
    expect(countTokens('abc', 4)).toEqual({ narrow: 5, wide: 0, est: 2 });
    expect(countTokens('\u6F22\u5B57', 4)).toEqual({ narrow: 2, wide: 2, est: 3 });
    expect(countTokens({ a: EMOJI }, 3)).toEqual({ narrow: 8, wide: 1, est: 4 });
    expect(est([], 2.5)).toBe(1);
  });

  it('rejects a cpt that is not positive and finite', () => {
    for (const cpt of [0, -1, Number.NaN, Number.POSITIVE_INFINITY]) expect(() => checkCPT(cpt)).toThrow(RangeError);
    expect(() => est('x', 0)).toThrow(/cpt/);
  });
});
