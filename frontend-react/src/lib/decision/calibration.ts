/**
 * Calibration apply (SPEC §2.9, §2.11). Port of backend-go/internal/decision/calibration.go;
 * pinned by contracts/vectors/calibration_apply.
 */
import { clamp01 } from './floats';

/** Points in a calibration table: t[i] = calibrated(i/100). */
export const CALIBRATION_TABLE_SIZE = 101;

/** Throws unless table has exactly 101 non-decreasing values in [0,1] (common.v1 CalibrationTable). */
export function checkCalibrationTable(table: readonly number[]): void {
  if (!Array.isArray(table) || table.length !== CALIBRATION_TABLE_SIZE) {
    throw new RangeError(`calibration table has ${Array.isArray(table) ? table.length : 0} points, want ${CALIBRATION_TABLE_SIZE}`);
  }
  table.forEach((t, i) => {
    if (typeof t !== 'number' || Number.isNaN(t) || t < 0 || t > 1) {
      throw new RangeError(`calibration table point ${i} is ${String(t)}, want a value in [0,1]`);
    }
    if (i > 0 && t < table[i - 1]) throw new RangeError(`calibration table decreases at point ${i}`);
  });
}

/**
 * Maps a raw risk through a 101-point table by linear interpolation:
 * u = 100*clamp(x), i = min(floor(u), 99), f = t[i] + (t[i+1]-t[i])*(u-i).
 * The table must pass checkCalibrationTable.
 */
export function applyCalibration(table: readonly number[], x: number): number {
  const u = 100 * clamp01(x);
  const i = Math.min(Math.floor(u), 99);
  return table[i] + (table[i + 1] - table[i]) * (u - i);
}
