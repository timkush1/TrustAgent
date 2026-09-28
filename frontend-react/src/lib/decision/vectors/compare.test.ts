// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { compareVector } from './compare';
import { checkVector, KINDS, runVectorKind, type Vector } from './kinds';

const actual = JSON.parse('{"a": 1, "b": {"c": [1, {"d": null, "e": "x"}], "f": true}, "g": null, "h": 0.30000000000000004}') as unknown;

describe('compareVector', () => {
  it.each(['{}', '{"a": 1.0000000001}', '{"b": {"c": [1, {}]}}', '{"b": {"c": [1, {"d": null}], "f": true}, "g": null, "h": 0.3}'])(
    'accepts the subset %s',
    (want) => {
      expect(compareVector(JSON.parse(want), actual, 1e-9)).toEqual([]);
    },
  );

  it.each([
    ['{"a": 2}', '$.a'],
    ['{"z": null}', '$.z: missing'],
    ['{"b": {"c": [1]}}', '$.b.c: want 1 elements'],
    ['{"b": {"c": [1, {"e": "y"}]}}', '$.b.c[1].e: want "y"'],
    ['{"b": {"f": false}}', '$.b.f'],
    ['{"a": "1"}', '$.a'],
    ['{"a": {}}', '$.a: want an object'],
    ['{"a": []}', '$.a: want an array'],
    ['{"g": 0}', '$.g'],
  ])('reports %s at %s', (want, where) => {
    const diffs = compareVector(JSON.parse(want), actual, 1e-9);
    expect(diffs).toHaveLength(1);
    expect(diffs[0]).toContain(where);
  });
});

describe('vector kinds', () => {
  it('fails an unknown kind and a skipped kind', () => {
    expect(() => runVectorKind('nonsense', {})).toThrow(/unknown vector kind/);
    expect(() => runVectorKind('cost', {})).toThrow(/no TS runner/);
  });

  it('has a runner or a skip reason for every kind of vector.v1', () => {
    for (const [kind, k] of Object.entries(KINDS)) expect([kind, (k.run === undefined) !== (k.skip === undefined)]).toEqual([kind, true]);
  });

  it('rejects unknown input members like the Go runner', () => {
    expect(runVectorKind('tokens', { value: 'abc', cpt: 4 })).toEqual({ narrow: 5, wide: 0, est: 2 });
    expect(() => runVectorKind('tokens', { value: 'abc', cpt: 4, extra: 1 })).toThrow(/unknown input member/);
  });

  it('detects a wrong expected value', () => {
    const got = runVectorKind('calibration_apply', { table: Array.from({ length: 101 }, (_, i) => i / 100), x: 0.5 });
    expect(compareVector({ y: 0.5 }, got)).toEqual([]);
    expect(compareVector({ y: 0.51 }, got)).toHaveLength(1);
  });

  it('checks the file-level fields', () => {
    const v: Vector = { vector: '2', kind: 'canonical', name: 'other', status: 'done', input: {}, expected: {} };
    const problems = checkVector(v, 'tokens', 'misnamed.json');
    expect(problems.join('\n')).toMatch(/vector is "2"[\s\S]*kind "canonical"[\s\S]*name "other"[\s\S]*status "done"/);
    expect(checkVector({ ...v, vector: '1', kind: 'tokens', name: 'ok', status: 'frozen' }, 'tokens', 'ok.json')).toEqual([]);
  });
});
