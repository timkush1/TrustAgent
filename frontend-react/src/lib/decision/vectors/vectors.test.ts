// @vitest-environment node
/**
 * Runs every golden vector in contracts/vectors against the TypeScript port (SPEC §7.6): Go is the
 * reference and TS conforms. Kinds the port implements must pass in full, draft and frozen alike;
 * kinds without a TS runner are reported as skipped with their reason; an unknown kind fails.
 *
 * Files are read as text and parsed with JSON.parse, the parser the vector README names for
 * JavaScript runners, so -0, lone surrogates and number spellings reach the runners as JSON.parse
 * produces them.
 */
import { afterAll, describe, expect, it } from 'vitest';
import { compareVector, DEFAULT_TOLERANCE } from './compare';
import { checkVector, KINDS, runVectorKind, type Vector } from './kinds';

const files = import.meta.glob<string>('@contracts/vectors/*/*.json', { query: '?raw', import: 'default', eager: true });

interface Loaded {
  path: string;
  dir: string;
  fileName: string;
  vector: Vector;
}

const loaded: Loaded[] = Object.entries(files)
  .map(([path, text]) => {
    const parts = path.split('/');
    return { path, dir: parts[parts.length - 2], fileName: parts[parts.length - 1], vector: JSON.parse(text) as Vector };
  })
  .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));

const byKind = new Map<string, Loaded[]>();
for (const l of loaded) byKind.set(l.dir, [...(byKind.get(l.dir) ?? []), l]);

const report = new Map<string, { total: number; frozen: number; passed: number; skipped: string | null }>();

describe('golden vectors (contracts/vectors)', () => {
  it('finds the vector files', () => {
    expect(loaded.length).toBeGreaterThan(0);
  });

  it('has a runner or a skip reason for every kind directory', () => {
    const unknown = [...byKind.keys()].filter((k) => !Object.hasOwn(KINDS, k));
    expect(unknown, `unknown vector kinds: ${unknown.join(', ')}`).toEqual([]);
  });

  it.each(loaded.map((l) => [l.path.replace(/^.*vectors\//, ''), l] as const))('%s is a well-formed vector file', (_name, l) => {
    expect(checkVector(l.vector, l.dir, l.fileName)).toEqual([]);
  });
});

for (const [kind, list] of [...byKind].sort(([a], [b]) => (a < b ? -1 : 1))) {
  const k = Object.hasOwn(KINDS, kind) ? KINDS[kind] : undefined;
  const frozen = list.filter((l) => l.vector.status === 'frozen').length;
  const entry = { total: list.length, frozen, passed: 0, skipped: k?.skip ?? null };
  report.set(kind, entry);

  describe(`${kind} vectors`, () => {
    if (k === undefined) {
      it('is a known kind', () => {
        expect.fail(`unknown vector kind ${kind} (${list.length} files)`);
      });
      return;
    }
    if (k.run === undefined) {
      it.skip(`${list.length} vectors not run: ${k.skip ?? ''}`, () => {});
      return;
    }
    it.each(list.map((l) => [l.vector.name, l] as const))('%s', (_name, l) => {
      const v = l.vector;
      expect(v.expected, `${l.path}: expected is null; run make vectors to generate it`).not.toBeNull();
      const got = runVectorKind(v.kind, v.input);
      const diffs = compareVector(v.expected, got, v.tolerance ?? DEFAULT_TOLERANCE);
      expect(diffs, `${l.path} (${v.status})`).toEqual([]);
      entry.passed++;
    });
  });
}

afterAll(() => {
  const rows = [...report].map(([kind, r]) => {
    const go = Object.hasOwn(KINDS, kind) ? KINDS[kind].go : 'unknown kind';
    const ts = r.skipped === null ? `${r.passed}/${r.total} passed` : `skipped: ${r.skipped}`;
    return `  ${kind.padEnd(18)} ${String(r.total).padStart(3)} vectors (${r.frozen} frozen)  TS ${ts}  | Go: ${go}`;
  });
  // Kinds without a vector directory yet are reported too, so every skip reason is visible.
  for (const [kind, k] of Object.entries(KINDS).sort(([a], [b]) => (a < b ? -1 : 1))) {
    if (report.has(kind)) continue;
    const ts = k.run === undefined ? `skipped: ${k.skip ?? ''}` : 'runner ready, no vectors yet';
    rows.push(`  ${kind.padEnd(18)}   0 vectors  TS ${ts}  | Go: ${k.go}`);
  }
  console.info(`golden vectors, TypeScript port:\n${rows.join('\n')}`);
});
