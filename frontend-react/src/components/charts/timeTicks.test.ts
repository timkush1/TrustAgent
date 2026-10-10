import { describe, expect, it } from 'vitest';
import { axisTicks } from './timeTicks';

/** ISO times of `n` buckets of `minutes` from a local clock time (the axis labels local times). */
function series(start: Date, minutes: number, n: number): string[] {
  return Array.from({ length: n }, (_, i) => new Date(start.getTime() + i * minutes * 60_000).toISOString());
}

const localClock = (iso: string) => {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
};

describe('axisTicks', () => {
  it('labels a day of 15-minute buckets on one round step, never a :15 after whole hours', () => {
    // 24 h from 17:45 local: the old axis read 18:00 … 22:00 and then 23:15, 00:15 ….
    const times = series(new Date(2026, 9, 9, 17, 45), 15, 96);
    const labels = axisTicks(times).map(localClock);
    expect(labels.length).toBeGreaterThanOrEqual(2);
    expect(labels.length).toBeLessThanOrEqual(6);
    expect(labels.every((l) => l.endsWith(':00'))).toBe(true);
    const hours = labels.map((l) => Number(l.slice(0, 2)));
    const steps = new Set(hours.slice(1).map((h, i) => (h - hours[i] + 24) % 24));
    expect(steps.size).toBe(1);
    // The last bucket, at the right edge, gets no label that would be cut off.
    expect(axisTicks(times)).not.toContain(times[times.length - 1]);
  });

  it('labels every bar when there are few', () => {
    const times = series(new Date(2026, 9, 9, 10, 0), 60, 3);
    expect(axisTicks(times)).toEqual(times);
  });

  it('spaces labels evenly when no bucket falls on a round time', () => {
    const times = series(new Date(2026, 9, 9, 10, 7), 60 * 24 * 7, 20);
    const ticks = axisTicks(times);
    expect(ticks[0]).toBe(times[0]);
    expect(ticks.length).toBeLessThanOrEqual(6);
    const at = ticks.map((t) => times.indexOf(t));
    expect(new Set(at.slice(1).map((v, i) => v - at[i])).size).toBe(1);
  });
});
