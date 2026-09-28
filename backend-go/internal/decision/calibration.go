package decision

import (
	"fmt"
	"math"
)

// CalibrationTableSize is the number of points in a calibration table: t[i] = calibrated(i/100).
const CalibrationTableSize = 101

// CheckCalibrationTable reports whether table is a valid calibration table: exactly 101
// non-decreasing values in [0,1] (common.v1 CalibrationTable).
func CheckCalibrationTable(table []float64) error {
	if len(table) != CalibrationTableSize {
		return fmt.Errorf("calibration table has %d points, want %d", len(table), CalibrationTableSize)
	}
	for i, t := range table {
		if math.IsNaN(t) || t < 0 || t > 1 {
			return fmt.Errorf("calibration table point %d is %v, want a value in [0,1]", i, t)
		}
		if i > 0 && t < table[i-1] {
			return fmt.Errorf("calibration table decreases at point %d", i)
		}
	}
	return nil
}

// ApplyCalibration maps a raw risk through a 101-point table by linear interpolation
// (SPEC §2.9): u = 100*clamp(x), i = min(floor(u), 99), f = t[i] + (t[i+1]-t[i])*(u-i).
// The table must pass CheckCalibrationTable.
func ApplyCalibration(table []float64, x float64) float64 {
	u := 100 * clamp01(x)
	i := math.Min(math.Floor(u), 99)
	k := int(i)
	return table[k] + (table[k+1]-table[k])*(u-i)
}
