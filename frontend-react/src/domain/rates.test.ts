import { describe, expect, it } from 'vitest';
import { formatInterval, formatPercent, rateReadout, share } from './rates';

describe('formatPercent', () => {
  it.each([
    [null, '—'],
    [0, '0%'],
    [0.0004, '<0.1%'],
    [0.041, '4.1%'],
    [0.0995, '10%'],
    [0.85, '85%'],
    [1, '100%'],
    [0.125, '13%'],
  ])('%s → %s', (input, out) => {
    expect(formatPercent(input)).toBe(out);
  });
});

describe('rates', () => {
  it('formats an interval with one percent sign', () => {
    expect(formatInterval([0.029, 0.056])).toBe('2.9–5.6%');
    expect(formatInterval(null)).toBeNull();
  });

  it('shares need a whole', () => {
    expect(share(1, 4)).toBe(0.25);
    expect(share(1, 0)).toBeNull();
  });

  it('asks for more samples until n reaches min_n', () => {
    const r = rateReadout({ value: null, label: 'measured', basis: '', n: 12, ci95: null, min_n: 30 });
    expect(r).toMatchObject({ enough: false, value: null, needs: 'Needs 30 unbiased samples (have 12)' });
    expect(r.progress).toBeCloseTo(0.4);
  });

  it('never shows a value below min_n, even when the server sent one', () => {
    const r = rateReadout({ value: 0.2, label: 'measured', basis: '', n: 29, ci95: [0.1, 0.3], min_n: 30 });
    expect(r.enough).toBe(false);
    expect(r.value).toBeNull();
  });

  it('shows the value and interval once enough', () => {
    const r = rateReadout({ value: 0.041, label: 'measured', basis: '', n: 212, ci95: [0.029, 0.056], min_n: 30 });
    expect(r).toMatchObject({ enough: true, value: '4.1%', interval: '95% CI 2.9–5.6%', needs: null, progress: 1 });
  });

  it('says "have 0" for a fresh install', () => {
    expect(rateReadout({ value: null, label: 'measured', basis: '', n: 0, ci95: null, min_n: 30 }).needs).toBe(
      'Needs 30 unbiased samples (have 0)',
    );
  });
  it('words the missing-samples sentence with a caller template', () => {
    const r = rateReadout({ value: null, label: 'measured', basis: '', n: 7, ci95: null, min_n: 30 }, 'Needs {min_n} audited answers (have {n})');
    expect(r.needs).toBe('Needs 30 audited answers (have 7)');
  });
});
