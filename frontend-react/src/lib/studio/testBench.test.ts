// @vitest-environment node
/**
 * The test bench's free arithmetic: the threshold what-if over stored risks, matches against the
 * viewer's current labels, and the quick-calibration gate (n ≥ 8 with ≥ 3 of each label; SPEC §7.3).
 */
import { describe, expect, it } from 'vitest';
import testFixture from '@contracts/fixtures/api/StudioTestResponse.six.json';
import type { StudioTestResponse } from '@/types/api';
import { benchAt, firesAt, quickCalibrationOf, runKeys, type Expected, type TestRow } from './testBench';
import { blankSpec } from './templates';

const results = (testFixture as StudioTestResponse).results;

function row(id: string, risk: number | null, expected: Expected | null): TestRow {
  return { ...results[0], sample_id: id, risk_raw: risk, risk, fires: risk === null ? null : risk >= 0.6, expected, agrees: null, status: risk === null ? 'not_assessed' : 'assessed' };
}

describe('benchAt', () => {
  it("keeps the server's own outcome without a threshold", () => {
    const s = benchAt(results, new Map());
    expect(s.rows.map((r) => r.fires)).toEqual(results.map((r) => r.fires));
    expect(s.labelled).toBe(results.filter((r) => r.fires !== null && r.expected !== null).length);
    expect(s.matched).toBe(results.filter((r) => r.agrees === true).length);
  });

  it('replays the stored risks at another threshold, for free', () => {
    const rows = [row('a', 0.2, 'no_fire'), row('b', 0.55, 'fire'), row('c', 0.9, 'fire'), row('d', null, 'fire')];
    expect(benchAt(rows, new Map(), 0.6)).toMatchObject({ assessed: 3, fired: 1, labelled: 3, matched: 2 });
    const lower = benchAt(rows, new Map(), 0.5);
    expect(lower).toMatchObject({ fired: 2, matched: 3 });
    expect(lower.rows.map((r) => r.fires)).toEqual([false, true, true, null]);
    expect(lower.rows[3].agrees).toBeNull();
  });

  it("uses the viewer's current labels over the ones sent with the run", () => {
    const rows = [row('a', 0.9, 'fire'), row('b', 0.1, null)];
    const s = benchAt(rows, new Map<string, Expected | undefined>([['a', 'no_fire'], ['b', 'no_fire']]), 0.6);
    expect(s.rows.map((r) => r.agrees)).toEqual([false, true]);
    const cleared = benchAt(rows, new Map<string, Expected | undefined>([['a', undefined]]), 0.6);
    expect(cleared.rows[0].agrees).toBeNull();
  });

  it('fires at the threshold itself, with the gate tolerance', () => {
    expect(firesAt(0.6, 0.6)).toBe(true);
    expect(firesAt(0.6 - 1e-12, 0.6)).toBe(true);
    expect(firesAt(0.59, 0.6)).toBe(false);
    expect(firesAt(null, 0.6)).toBeNull();
  });
});

describe('quickCalibrationOf', () => {
  it('needs 8 labels with at least 3 of each, and says how many are missing', () => {
    const q = quickCalibrationOf(results, new Map(), 0.6);
    expect(q.result.n).toBe(6);
    expect(q.result.enough).toBe(false);
    expect(q.result.threshold).toBeNull();
    expect(q.needed.total).toBe(2);
    expect(q.result.message).toMatch(/^Label at least 8 answers, with at least 3 where the check should fire and 3 where it should not/);
    expect(q.result.message).not.toMatch(/calibrated/i);
  });

  it('suggests a threshold once there are enough labels, and never says calibrated or sets critical_at', () => {
    const rows = [0.05, 0.1, 0.2, 0.3, 0.35, 0.7, 0.8, 0.85, 0.9, 0.95].map((r, i) => row(`s${i}`, r, r > 0.5 ? 'fire' : 'no_fire'));
    const q = quickCalibrationOf(rows, new Map(), 0.6);
    expect(q.result.enough).toBe(true);
    expect(q.result.threshold).toBe(0.7);
    expect(q.needed).toEqual({ total: 0, fire: 0, noFire: 0 });
    expect(q.result.message).toMatch(/^Suggested threshold from 10 labels: 0.7\./);
    expect(q.result.message).not.toMatch(/calibrated/i);
    expect(q.result).not.toHaveProperty('critical_at');
  });

  it('skips unassessed and unlabelled rows', () => {
    const rows = [row('a', null, 'fire'), row('b', 0.4, null), row('c', 0.9, 'fire')];
    const q = quickCalibrationOf(rows, new Map(), 0.6);
    expect([q.result.n, q.result.n_pos, q.result.n_neg]).toEqual([1, 1, 0]);
    expect(q.needed).toEqual({ total: 7, fire: 2, noFire: 3 });
  });
});

describe('runKeys', () => {
  const spec = blankSpec();
  const samples = [{ id: 'p1', assistant_response: 'Sorry, that is your fault.', expected: 'fire' as const }];

  it('stays the same for a new threshold or new labels, which re-score the stored risks for free', () => {
    const keys = runKeys(spec, samples);
    expect(runKeys({ ...spec, outcomes: { ...spec.outcomes, warn_at: 0.35 } }, samples)).toEqual(keys);
    expect(runKeys(spec, [{ ...samples[0], expected: 'no_fire' }])).toEqual(keys);
  });

  it('changes with anything else the answers depend on', () => {
    const keys = runKeys(spec, samples);
    expect(runKeys({ ...spec, question: { ...spec.question, instructions: 'Something else?' } }, samples).specKey).not.toBe(keys.specKey);
    expect(runKeys({ ...spec, outcomes: { ...spec.outcomes, critical_at: 0.9 } }, samples).specKey).not.toBe(keys.specKey);
    expect(runKeys(spec, [{ ...samples[0], assistant_response: 'Happy to help.' }]).samplesKey).not.toBe(keys.samplesKey);
  });
});
