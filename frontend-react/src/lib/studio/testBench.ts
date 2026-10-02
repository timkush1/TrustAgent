/**
 * The test bench's arithmetic (SPEC §7.3 steps 3 and 4), free and in the browser:
 *
 * - the threshold what-if replays the risks a test already returned against another `warn_at`
 *   ("Threshold what-if replays stored answers for free");
 * - the summary counts matches against the viewer's current labels ("5 of 6 match · $0.001");
 * - quick calibration runs the TS mirror of `POST /api/studio/calibrate` (lib/decision
 *   quickCalibrate, pinned by the same vectors as Go) on the labelled risks. It suggests a
 *   threshold and never says "calibrated" or emits `critical_at`.
 *
 * Thresholds here are raw-space, as an unsaved evaluator's are (no calibration applies to it).
 */
import { EPS, quickCalibrate, type QuickCalibration } from '@/lib/decision';
import { MIN_FIT_LABELS, MIN_FIT_PER_CLASS } from '@/lib/decision/calibrate';
import type { EvaluatorSpec, StudioSample, StudioTestResponse } from '@/types/api';

export type TestRow = StudioTestResponse['results'][number];
export type Expected = 'fire' | 'no_fire';

export interface RowOutcome {
  sample_id: string;
  /** Whether the check fires at the threshold; null when the sample was not assessed. */
  fires: boolean | null;
  /** Whether that matches the viewer's label; null without a label or an answer. */
  agrees: boolean | null;
}

export interface BenchSummary {
  rows: RowOutcome[];
  /** Samples with a risk (assessed). */
  assessed: number;
  fired: number;
  /** Assessed samples that carry a label. */
  labelled: number;
  matched: number;
}

/** Fires when the raw risk reaches the threshold (the gate's `>=`, with its float tolerance). */
export function firesAt(riskRaw: number | null, threshold: number): boolean | null {
  if (riskRaw === null || !Number.isFinite(riskRaw)) return null;
  return riskRaw >= threshold - EPS;
}

/**
 * The bench at `threshold`: each row's outcome against the labels the viewer has now (a label
 * changed after the run counts). `threshold` undefined keeps the server's own `fires`.
 */
export function benchAt(results: readonly TestRow[], labels: ReadonlyMap<string, Expected | undefined>, threshold?: number): BenchSummary {
  let assessed = 0;
  let fired = 0;
  let labelled = 0;
  let matched = 0;
  const rows = results.map((r): RowOutcome => {
    const fires = threshold === undefined ? r.fires : firesAt(r.risk_raw, threshold);
    const expected = labels.has(r.sample_id) ? labels.get(r.sample_id) : (r.expected ?? undefined);
    const agrees = fires === null || expected === undefined ? null : fires === (expected === 'fire');
    if (fires !== null) {
      assessed += 1;
      if (fires) fired += 1;
      if (agrees !== null) {
        labelled += 1;
        if (agrees) matched += 1;
      }
    }
    return { sample_id: r.sample_id, fires, agrees };
  });
  return { rows, assessed, fired, labelled, matched };
}

export interface QuickCalibrationView {
  result: QuickCalibration;
  /** Labels still needed before a threshold can be suggested (0 when there are enough). */
  needed: { total: number; fire: number; noFire: number };
}

/** Quick calibration of the labelled, assessed rows at the current threshold. */
export function quickCalibrationOf(results: readonly TestRow[], labels: ReadonlyMap<string, Expected | undefined>, current: number): QuickCalibrationView {
  const labeled = results.flatMap((r) => {
    const expected = labels.has(r.sample_id) ? labels.get(r.sample_id) : (r.expected ?? undefined);
    if (r.risk_raw === null || !(r.risk_raw >= 0 && r.risk_raw <= 1) || expected === undefined) return [];
    return [{ risk_raw: r.risk_raw, label: expected === 'fire' }];
  });
  const result = quickCalibrate(current >= 0 && current <= 1 ? { labeled, current } : { labeled });
  return {
    result,
    needed: {
      total: Math.max(0, MIN_FIT_LABELS - result.n),
      fire: Math.max(0, MIN_FIT_PER_CLASS - result.n_pos),
      noFire: Math.max(0, MIN_FIT_PER_CLASS - result.n_neg),
    },
  };
}

/**
 * What a test ran on, to tell later whether the check or the samples changed since. Labels are left
 * out: relabelling a sample re-scores the run without a new call.
 */
export function runKeys(spec: EvaluatorSpec, samples: readonly StudioSample[]): { specKey: string; samplesKey: string } {
  return {
    specKey: JSON.stringify(spec),
    samplesKey: JSON.stringify(samples.map(({ expected: _label, ...rest }) => rest)),
  };
}

export { MIN_FIT_LABELS, MIN_FIT_PER_CLASS };
