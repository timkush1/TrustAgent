// @vitest-environment node
/**
 * The decision core runs in the browser (the static demo decides with the same code as the gateway)
 * and must stay deterministic: no Node APIs, no clocks, no randomness, no I/O (SPEC §2.9, §3.3).
 */
import { describe, expect, it } from 'vitest';

const sources = import.meta.glob<string>(['./**/*.ts', '!./**/*.test.ts'], { query: '?raw', import: 'default', eager: true });

const FORBIDDEN: [string, RegExp][] = [
  ['a Node built-in import', /from\s+['"](node:|fs|path|crypto|os|child_process)['"/]/],
  ['require()', /\brequire\s*\(/],
  ['process', /\bprocess\./],
  ['Buffer', /\bBuffer\b/],
  ['a clock', /\bDate\.now\b|\bnew Date\b|\bperformance\.now\b/],
  ['randomness', /\bMath\.random\b|\bcrypto\.getRandomValues\b/],
  ['I/O', /\bfetch\s*\(|\bXMLHttpRequest\b|\blocalStorage\b/],
];

describe('decision core purity', () => {
  it('has sources to check', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(10);
  });

  it.each(Object.entries(sources))('%s uses no Node APIs, clocks, randomness or I/O', (_path, text) => {
    const code = text.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '');
    for (const [what, re] of FORBIDDEN) expect(re.test(code), what).toBe(false);
  });
});
