import { describe, expect, it } from 'vitest';
import { canonicalize, canonicalizeJSON, CanonicalJsonError, formatNumber, sha256Hex } from './jcs';
import { sha256Hex as sha256HexOfText } from './sha256';

const LS = String.fromCharCode(0x2028);
const PS = String.fromCharCode(0x2029);
const LONE_HIGH = String.fromCharCode(0xd800);
const LONE_LOW = String.fromCharCode(0xdc00);
const EMOJI = String.fromCodePoint(0x1f600);

describe('canonicalize (RFC 8785 + SPEC §2.1)', () => {
  it('sorts keys by UTF-16 code units, recursively, with no whitespace', () => {
    expect(canonicalize({ b: [1, { z: true, a: null }], a: 'x' })).toBe('{"a":"x","b":[1,{"a":null,"z":true}]}');
    // U+1F600 is the surrogate pair D83D DE00, which sorts before U+FF61 in UTF-16 (but after it by code point).
    const halfwidth = String.fromCharCode(0xff61);
    expect(canonicalize({ [halfwidth]: 1, [EMOJI]: 2 })).toBe(`{"${EMOJI}":2,"${halfwidth}":1}`);
  });

  it('formats numbers like ES6 Number.prototype.toString', () => {
    expect(canonicalize([1.0, 0.1, 1e-7, 1e21, 1e20, -0, 123456789012345680000, 5e-324])).toBe(
      '[1,0.1,1e-7,1e+21,100000000000000000000,0,123456789012345680000,5e-324]',
    );
    expect(formatNumber(-0)).toBe('0');
    expect(() => formatNumber(Number.NaN)).toThrow(CanonicalJsonError);
    expect(() => canonicalize({ x: Number.POSITIVE_INFINITY })).toThrow(/not a JSON number/);
  });

  it('escapes like JSON.stringify plus U+2028/U+2029, without HTML escaping', () => {
    expect(canonicalize(`<a href="x">&\\\b\f\n\r\t${String.fromCharCode(1)}${String.fromCharCode(0x1f)}${LS}${PS}`)).toBe(
      '"<a href=\\"x\\">&\\\\\\b\\f\\n\\r\\t\\u0001\\u001f\\u2028\\u2029"',
    );
    expect(canonicalize(`caf\u00e9 ${EMOJI}`)).toBe(`"caf\u00e9 ${EMOJI}"`);
  });

  it('replaces lone surrogates with U+FFFD in values and keys', () => {
    expect(canonicalize(`a${LONE_HIGH}b${LONE_LOW}`)).toBe('"a\uFFFDb\uFFFD"');
    expect(canonicalizeJSON('{"\\ud800":1}')).toBe('{"\uFFFD":1}');
    expect(canonicalize(`${EMOJI}`)).toBe(`"${EMOJI}"`);
  });

  it('skips undefined members, honours toJSON and rejects non-JSON values', () => {
    expect(canonicalize({ a: undefined, b: 1 })).toBe('{"b":1}');
    expect(canonicalize({ when: { toJSON: () => '2026-01-01' } })).toBe('{"when":"2026-01-01"}');
    expect(canonicalize(Object.assign(Object.create(null) as object, { k: 'v' }))).toBe('{"k":"v"}');
    expect(() => canonicalize([undefined])).toThrow(CanonicalJsonError);
    expect(() => canonicalize(new Map([['a', 1]]))).toThrow(/Map/);
    expect(() => canonicalize(() => 1)).toThrow(/function/);
    expect(() => canonicalize(1n)).toThrow(/bigint/);
  });

  it('hashes the UTF-8 bytes of the canonical text', () => {
    expect(sha256Hex({ b: 2, a: 1 })).toBe(sha256HexOfText('{"a":1,"b":2}'));
  });
});

describe('sha256', () => {
  it.each([
    ['', 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'],
    ['abc', 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'],
    ['abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq', '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1'],
    ['a'.repeat(55), '9f4390f8d30c2dd92ec9f095b65e2b9ae9b0a925a5258e241c9f1e910f734318'],
    ['a'.repeat(56), 'b35439a4ac6f0948b6d6f9e3c6af0f5f590ce20f1bde7090ef7970686ec6738a'],
    ['a'.repeat(64), 'ffe054fe7ae0cb6dc65c3af9b61d5209f439851db43d0ba5997337df154668eb'],
    [`h\u00e9llo ${EMOJI} ${LS}`, '842a9d49ed28c8fc57ef00683a821c51a268d0d7d62dc2c4a772c47acb01b0ff'],
  ])('hashes %j', (text, digest) => {
    expect(sha256HexOfText(text)).toBe(digest);
  });

  it('encodes a lone surrogate as U+FFFD', () => {
    expect(sha256HexOfText(LONE_HIGH)).toBe('83d544ccc223c057d2bf80d3f2a32982c32c3c0db8e2674820da5064783fb097');
  });
});
