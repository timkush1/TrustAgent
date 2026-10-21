/**
 * Which buckets of a time series get an axis label (OutcomeTimeSeries): one round step of the
 * reader's clock all along, so the axis never switches from "18:00" to "23:15" half way.
 */

/** Round steps for axis labels, in minutes: 5 min up to a week. */
const TICK_STEPS_MIN = [5, 10, 15, 30, 60, 120, 180, 240, 360, 720, 1440, 2880, 10080];

/** Minutes since the epoch on the reader's clock (the labels are local times). */
function localMinutes(t: number): number {
  return Math.round(t / 60_000) - new Date(t).getTimezoneOffset();
}

/**
 * The buckets that get an axis label: those on one round step of the reader's clock (every fourth
 * whole hour across a day of 15-minute buckets), at most `max` (six fit a phone), so the axis never
 * switches from "18:00" to "23:15" half way. With more buckets than labels, the last bucket gets
 * none: its label would be cut at the chart's right edge. Buckets on no round time (a clock offset
 * by 30 minutes, daily buckets at UTC midnight) get evenly spaced labels from the first instead.
 */
export function axisTicks(times: readonly string[], max = 6): string[] {
  const ms = times.map((t) => Date.parse(t));
  // Few bars are wide enough to label each one.
  if (times.length <= max || ms.some(Number.isNaN)) return [...times];
  const candidates = times.slice(0, -1);
  const bucketMin = Math.max(1, (ms[1] - ms[0]) / 60_000);
  for (const step of TICK_STEPS_MIN) {
    if (step < bucketMin) continue;
    const ticks = candidates.filter((_, i) => localMinutes(ms[i]) % step === 0);
    if (ticks.length >= 2 && ticks.length <= max) return ticks;
    if (ticks.length < 2 && step > bucketMin * candidates.length) break;
  }
  const every = Math.ceil(candidates.length / max);
  return candidates.filter((_, i) => i % every === 0);
}
