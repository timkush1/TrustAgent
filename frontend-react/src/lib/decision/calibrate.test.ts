// @vitest-environment node
/**
 * The quick calibration's honesty text, ported from backend-go/internal/decision/calibrate_test.go
 * (TestQuickCalibrate, TestQuickCalibrateUnstableWithoutLOOMove). The golden vectors
 * (contracts/vectors/calibrate_fit) pin the numbers to the Go reference but not the message, so
 * these keep the two languages' words in step.
 */
import { describe, expect, it } from 'vitest';
import { quickCalibrate, type LabeledScore } from './calibrate';

/** `[risk, fires, does not fire]` groups, as the Go test's labels() builds them. */
function labels(...groups: [number, number, number][]): LabeledScore[] {
  return groups.flatMap(([risk, fire, quiet]) => [
    ...Array.from({ length: fire }, () => ({ risk_raw: risk, label: true })),
    ...Array.from({ length: quiet }, () => ({ risk_raw: risk, label: false })),
  ]);
}

describe('quickCalibrate message', () => {
  it('names the leave-one-out range when that range makes the suggestion unstable', () => {
    const q = quickCalibrate({ labeled: labels([0.1, 0, 3], [0.3, 1, 2], [0.6, 2, 1], [0.9, 3, 0]) });
    expect(q.threshold).toBe(0.6);
    expect(q.loo_range).toEqual([0.3, 0.6]);
    expect(q.unstable).toBe(true);
    expect(q.message).toContain('Suggested threshold from 12 labels: 0.6.');
    expect(q.message).toContain('; the suggestion is unstable (leaving out one label moves it between 0.3 and 0.6). Keep it watching');
    expect(q.message.toLowerCase()).not.toContain('calibrated');
  });

  it('says the 95% ranges are wide, and names no range of one value, when every leave-one-out fit agrees', () => {
    const q = quickCalibrate({ labeled: labels([0.08, 0, 4], [0.92, 4, 0]) });
    expect(q.threshold).toBe(0.92);
    expect(q.loo_range).toEqual([0.92, 0.92]);
    expect(q.unstable).toBe(true);
    expect(q.message).not.toContain('leaving out one label');
    expect(q.message).toContain('; the suggestion is unstable (too few labels: the 95% ranges are wide). Keep it watching and label more traffic.');
  });
});
