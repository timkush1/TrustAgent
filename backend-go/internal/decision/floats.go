package decision

import (
	"math"
	"strconv"
)

// EPS is the comparison tolerance of SPEC §2.1 and §2.9: a threshold t is reached when
// x >= t - EPS, and a tolerance tol is exceeded when x > tol + EPS.
const EPS = 1e-9

// clamp01 limits x to [0, 1].
func clamp01(x float64) float64 {
	switch {
	case x < 0:
		return 0
	case x > 1:
		return 1
	}
	return x
}

// R2 rounds x to two decimals as SPEC §2.1 prescribes: floor(x*100 + 0.5) / 100. It never uses a
// language round(), whose tie handling differs between Go and JavaScript.
func R2(x float64) float64 {
	return math.Floor(x*100+0.5) / 100
}

// FormatRisk formats a risk for the X-TrustAgent-Risk header: R2 with exactly two decimals.
func FormatRisk(x float64) string {
	return strconv.FormatFloat(R2(x), 'f', 2, 64)
}
