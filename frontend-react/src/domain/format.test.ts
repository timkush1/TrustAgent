import { describe, expect, it } from 'vitest';
import { formatCount, formatDurationMs, formatRelativeTime, formatRisk, formatUsd, r2 } from './format';

describe('format', () => {
  it('rounds with r2 (SPEC §2.1), not Math.round', () => {
    expect(r2(0.125)).toBe(0.13);
    expect(r2(0.98)).toBe(0.98);
    expect(formatRisk(0.055)).toBe('0.06');
    expect(formatRisk(null)).toBe('—');
  });

  it('formats durations', () => {
    expect(formatDurationMs(212)).toBe('212 ms');
    expect(formatDurationMs(1400)).toBe('1.4 s');
    expect(formatDurationMs(12_000)).toBe('12 s');
    expect(formatDurationMs(125_000)).toBe('2 min 5 s');
    expect(formatDurationMs(null)).toBe('—');
  });

  it('formats money down to fractions of a cent', () => {
    expect(formatUsd(0)).toBe('$0');
    expect(formatUsd(1234.5)).toBe('$1,234.50');
    expect(formatUsd(0.5)).toBe('$0.50');
    expect(formatUsd(0.00001)).toBe('$0.00001');
    expect(formatUsd(0.000234)).toBe('$0.00023');
    expect(formatUsd(undefined)).toBe('—');
  });

  it('formats counts and relative times', () => {
    expect(formatCount(12345)).toBe('12,345');
    const now = Date.parse('2026-09-27T12:00:00Z');
    expect(formatRelativeTime('2026-09-27T12:00:02Z', now)).toBe('just now');
    expect(formatRelativeTime('2026-09-27T11:59:30Z', now)).toBe('30 sec. ago');
    expect(formatRelativeTime('2026-09-27T11:55:00Z', now)).toBe('5 min. ago');
    expect(formatRelativeTime('bad', now)).toBe('—');
  });
});
