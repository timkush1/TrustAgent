package decision

import (
	"math"
	"testing"
)

func identityTable() []float64 {
	t := make([]float64, CalibrationTableSize)
	for i := range t {
		t[i] = float64(i) / 100
	}
	return t
}

func TestCheckCalibrationTable(t *testing.T) {
	if err := CheckCalibrationTable(identityTable()); err != nil {
		t.Fatal(err)
	}
	short := identityTable()[:100]
	decreasing := identityTable()
	decreasing[50] = 0.1
	outOfRange := identityTable()
	outOfRange[100] = 1.2
	nan := identityTable()
	nan[3] = math.NaN()
	for name, table := range map[string][]float64{"short": short, "decreasing": decreasing, "out of range": outOfRange, "nan": nan} {
		if err := CheckCalibrationTable(table); err == nil {
			t.Errorf("%s: CheckCalibrationTable succeeded, want an error", name)
		}
	}
}

func TestApplyCalibration(t *testing.T) {
	table := identityTable()
	for _, c := range []struct{ x, want float64 }{{0, 0}, {0.42, 0.42}, {0.425, 0.425}, {1, 1}, {1.5, 1}, {-1, 0}} {
		if got := ApplyCalibration(table, c.x); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("ApplyCalibration(identity, %v) = %v, want %v", c.x, got, c.want)
		}
	}
}
