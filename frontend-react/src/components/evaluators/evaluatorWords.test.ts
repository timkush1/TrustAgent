import { describe, expect, it } from 'vitest';
import evaluatorFiles from '@contracts/evaluators/off_topic.v1.json';
import { copy } from '@/domain/presentation';
import { calibrationProgress } from '@/test/control/fixtures';
import { calibrateReason, evaluatorDescription } from './evaluatorWords';

const of = (id: string, origin: 'builtin' | 'file' | 'dashboard', description?: string) => ({ id, origin, latest: { spec: { description } } });

describe('evaluatorDescription', () => {
  it('prefers the spec’s own line, then the built-in copy, and invents none for a saved check', () => {
    expect(evaluatorDescription(of('groundedness', 'builtin', 'Flags answers that state facts the evidence does not contain.'))).toBe(
      'Flags answers that state facts the evidence does not contain.',
    );
    // off_topic's template has no description of its own: the contract copy gives it one.
    expect((evaluatorFiles as { description?: string }).description).toBeUndefined();
    expect(evaluatorDescription(of('off_topic', 'builtin'))).toBe(copy.evaluators.builtin_descriptions.off_topic);
    expect(evaluatorDescription(of('off_topic', 'builtin', '  '))).toBe(copy.evaluators.builtin_descriptions.off_topic);
    expect(evaluatorDescription(of('off_topic', 'dashboard'))).toBeNull();
    expect(evaluatorDescription(of('blames_the_customer', 'builtin'))).toBeNull();
  });

  it('has a line for every built-in check whose template has none', () => {
    const templates = import.meta.glob<{ id: string; description?: string }>('../../../../contracts/evaluators/*.v1.json', { eager: true, import: 'default' });
    const missing = Object.values(templates).filter((t) => !t.description).map((t) => t.id);
    expect(missing.length).toBeGreaterThan(0);
    for (const id of missing) expect(evaluatorDescription(of(id, 'builtin')), id).toMatch(/^Flags answers /);
  });
});

describe('calibrateReason', () => {
  it('counts only the uniform-sample deep audits as labels, and says how many there are', () => {
    // The demo: every answer deep-audited, none of them from the uniform sample, so none counts.
    const demo = calibrationProgress('groundedness@1', { human_labels: { n: 0, pos: 0, neg: 0 }, proxy_labels: 0, can_fit_provisional: false, can_fit_measured: false });
    expect(calibrateReason(demo)?.text).toBe(
      'Calibrate now needs 8 labels: your own, plus deep audits from the uniform sample (0 so far). Label 8 more answers in the queue.',
    );
    const some = calibrationProgress('groundedness@1', { human_labels: { n: 2, pos: 1, neg: 1 }, proxy_labels: 5, can_fit_provisional: false, can_fit_measured: false });
    expect(calibrateReason(some)?.text).toBe(
      'Calibrate now needs 8 labels: your own, plus deep audits from the uniform sample (5 so far). Label 1 more answer in the queue.',
    );
    expect(calibrateReason({ ...some, proxy_labels: 6 })).toBeNull();
    expect(calibrateReason({ ...demo, can_fit_provisional: true })).toBeNull();
  });
});
