package decision

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// Calibration fitting (SPEC §2.11, §7.3): Wilson intervals, the threshold grid, the max-F1 and
// minimum-precision threshold searches, the Wilson-bound critical_at and the fit metrics. PAV
// isotonic regression is in calibrate_isotonic.go, Platt scaling in calibrate_platt.go, the fit
// itself in calibrate_fit.go, Studio's quick calibration in calibrate_quick.go and quality
// derivation and the calibration.v1 file in calibrate_file.go.
//
// Like the rest of the package the fitter is pure and deterministic, so the TypeScript port can
// reproduce it: labels are counted as integers and compared by cross-multiplication where the
// result decides a branch, sums run in input order, thresholds compare with EPS, published tables
// are rounded to six decimals (R6), and ties are broken by fixed rules.

// LabeledScore is one labelled risk (QuickCalibrationRequest.labeled). Risk is the evaluator's risk
// in [0,1]; Fire is the label: true when the evaluator should fire on that answer (the problem is
// present), false when it should not.
type LabeledScore struct {
	Risk float64 `json:"risk_raw"`
	Fire bool    `json:"label"`
}

// Fitting limits (SPEC §2.11, §7.3, §0 row 13).
const (
	// WilsonZ95 is the two-sided 95% standard normal quantile of every Wilson interval.
	WilsonZ95 = 1.959963984540054

	// MinFitLabels and MinFitPerClass are the fewest labels any fit accepts (§7.3: n >= 8 with at
	// least 3 of each class).
	MinFitLabels   = 8
	MinFitPerClass = 3

	// WarnMinPrecision is the precision floor of the recommended warn_at (§2.11).
	WarnMinPrecision = 0.8
	// CriticalMinPrecisionLB and CriticalMinPredictedPos define the recommended critical_at: the
	// Wilson 95% lower bound of precision must reach 0.95 with at least 50 predicted positives.
	CriticalMinPrecisionLB  = 0.95
	CriticalMinPredictedPos = 50

	// ReliabilityBins is the number of equal-width bins of the reliability table and of ECE.
	ReliabilityBins = 10
)

// CriticalNullReason is recommended.critical_null_reason when no critical_at qualifies (§2.11).
const CriticalNullReason = "insufficient evidence to block"

// ErrNotEnoughLabels is returned when a fit has fewer labels than MinFitLabels or fewer than
// MinFitPerClass of either class.
var ErrNotEnoughLabels = errors.New("calibrate: not enough labels")

// R6 rounds x to six decimals as floor(x*1e6 + 0.5) / 1e6, the rounding of published calibration
// tables and Platt parameters. Like R2 it never uses a language round().
func R6(x float64) float64 {
	return math.Floor(x*1e6+0.5) / 1e6
}

// WilsonInterval returns the Wilson score interval of k successes in n trials at the normal
// quantile z: center ± half, with center = (p + z²/2n) / (1 + z²/n) and
// half = z·sqrt(p(1-p)/n + z²/4n²) / (1 + z²/n). The bounds are clamped to [0,1] and are exact at
// the ends (lo = 0 when k = 0, hi = 1 when k = n). With n = 0 the interval is [0,1].
func WilsonInterval(k, n int, z float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	k = min(max(k, 0), n)
	nf := float64(n)
	p := float64(k) / nf
	z2 := z * z
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	half := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	lo, hi = clamp01(center-half), clamp01(center+half)
	if k == 0 {
		lo = 0
	}
	if k == n {
		hi = 1
	}
	return lo, hi
}

// Confusion is a confusion matrix at one threshold: an answer fires when its risk reaches the
// threshold (risk >= t - EPS, as in decide()).
type Confusion struct {
	Threshold float64 `json:"threshold"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	TN        int     `json:"tn"`
	FN        int     `json:"fn"`
}

// Precision is TP/(TP+FP), 0 when nothing fires.
func (c Confusion) Precision() float64 {
	if c.TP+c.FP == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FP)
}

// Recall is TP/(TP+FN), 0 when there are no positives.
func (c Confusion) Recall() float64 {
	if c.TP+c.FN == 0 {
		return 0
	}
	return float64(c.TP) / float64(c.TP+c.FN)
}

// F1 is 2TP/(2TP+FP+FN), 0 when TP = 0.
func (c Confusion) F1() float64 {
	if c.TP == 0 {
		return 0
	}
	return float64(2*c.TP) / float64(2*c.TP+c.FP+c.FN)
}

// f1Greater reports whether a has a strictly higher F1 than b, compared exactly by
// cross-multiplying 2TP/(2TP+FP+FN).
func f1Greater(a, b Confusion) bool {
	an, ad := int64(2*a.TP), int64(2*a.TP+a.FP+a.FN)
	bn, bd := int64(2*b.TP), int64(2*b.TP+b.FP+b.FN)
	if ad == 0 || bd == 0 {
		return an > 0 && bn == 0
	}
	return an*bd > bn*ad
}

// riskGroup is a run of sorted risks within EPS of their neighbours: they reach every threshold
// together. Lo and Hi are the smallest and largest risk of the run.
type riskGroup struct {
	Lo, Hi   float64
	Pos, Neg int
}

// groupRisks sorts the scores by risk and merges runs whose consecutive risks differ by at most
// EPS. It returns the groups in ascending order.
func groupRisks(samples []LabeledScore) []riskGroup {
	sorted := slices.Clone(samples)
	slices.SortStableFunc(sorted, func(a, b LabeledScore) int {
		switch {
		case a.Risk < b.Risk:
			return -1
		case a.Risk > b.Risk:
			return 1
		}
		return 0
	})
	var out []riskGroup
	for _, s := range sorted {
		if n := len(out); n > 0 && s.Risk-out[n-1].Hi <= EPS {
			out[n-1].Hi = s.Risk
			if s.Fire {
				out[n-1].Pos++
			} else {
				out[n-1].Neg++
			}
			continue
		}
		g := riskGroup{Lo: s.Risk, Hi: s.Risk}
		if s.Fire {
			g.Pos = 1
		} else {
			g.Neg = 1
		}
		out = append(out, g)
	}
	return out
}

// ThresholdGrid returns the confusion matrix at every candidate threshold, ascending. The
// candidates are the distinct risks (runs within EPS count once, represented by their smallest
// risk), so every possible split of the labels appears exactly once.
func ThresholdGrid(samples []LabeledScore) []Confusion {
	groups := groupRisks(samples)
	pos, neg := countLabels(samples)
	out := make([]Confusion, len(groups))
	tp, fp := pos, neg // everything fires at the lowest candidate
	for i, g := range groups {
		out[i] = Confusion{Threshold: g.Lo, TP: tp, FP: fp, TN: neg - fp, FN: pos - tp}
		tp -= g.Pos
		fp -= g.Neg
	}
	return out
}

// ConfusionAt returns the confusion matrix at threshold t.
func ConfusionAt(samples []LabeledScore, t float64) Confusion {
	c := Confusion{Threshold: t}
	for _, s := range samples {
		fires := s.Risk >= t-EPS
		switch {
		case fires && s.Fire:
			c.TP++
		case fires:
			c.FP++
		case s.Fire:
			c.FN++
		default:
			c.TN++
		}
	}
	return c
}

// Threshold objectives (QuickCalibrationRequest.objective).
const (
	ObjectiveMaxF1                   = "max_f1"
	ObjectiveMinThresholdAtPrecision = "min_threshold_at_precision"
)

// MaxF1Threshold returns the candidate threshold with the highest F1 among those whose precision
// is at least minPrecision (minus EPS; 0 disables the floor) and that catch at least one positive.
// Ties in F1 (compared exactly) go to the higher threshold, which fires less. ok is false when no
// candidate qualifies.
func MaxF1Threshold(samples []LabeledScore, minPrecision float64) (best Confusion, ok bool) {
	for _, c := range ThresholdGrid(samples) {
		if c.TP == 0 || c.Precision() < minPrecision-EPS {
			continue
		}
		if !ok || !f1Greater(best, c) { // ascending: an equal F1 moves to the higher threshold
			best, ok = c, true
		}
	}
	return best, ok
}

// MinThresholdAtPrecision returns the smallest candidate threshold whose precision is at least
// target (minus EPS) and that catches at least one positive: the highest recall at that precision.
func MinThresholdAtPrecision(samples []LabeledScore, target float64) (Confusion, bool) {
	for _, c := range ThresholdGrid(samples) {
		if c.TP > 0 && c.Precision() >= target-EPS {
			return c, true
		}
	}
	return Confusion{}, false
}

// CriticalAt is recommended.critical_at: the threshold, the Wilson 95% lower bound of precision at
// it, and the number of answers that reach it.
type CriticalAt struct {
	Value         float64 `json:"value"`
	PrecisionLB95 float64 `json:"precision_lb95"`
	PredictedPos  int     `json:"predicted_pos"`
}

// WilsonCriticalAt returns the smallest candidate threshold t such that, for every candidate
// t' >= t with at least minPredictedPos predicted positives, the Wilson 95% lower bound of
// precision is at least minLB (minus EPS) (SPEC §2.11). Candidates above the highest one with
// minPredictedPos predicted positives have too few positives to judge and are not considered.
// It returns nil when the highest judgeable candidate already fails or none has enough predicted
// positives: there is not enough evidence to block.
func WilsonCriticalAt(samples []LabeledScore, minPredictedPos int, minLB float64) *CriticalAt {
	grid := ThresholdGrid(samples)
	var best *CriticalAt
	for i := len(grid) - 1; i >= 0; i-- {
		c := grid[i]
		pp := c.TP + c.FP
		if pp < minPredictedPos {
			continue
		}
		lo, _ := WilsonInterval(c.TP, pp, WilsonZ95)
		if lo < minLB-EPS {
			break
		}
		best = &CriticalAt{Value: c.Threshold, PrecisionLB95: lo, PredictedPos: pp}
	}
	return best
}

// countLabels returns the number of positive and negative labels.
func countLabels(samples []LabeledScore) (pos, neg int) {
	for _, s := range samples {
		if s.Fire {
			pos++
		} else {
			neg++
		}
	}
	return pos, neg
}

// checkSamples rejects risks outside [0,1] (NaN included).
func checkSamples(samples []LabeledScore) error {
	for i, s := range samples {
		if math.IsNaN(s.Risk) || s.Risk < 0 || s.Risk > 1 {
			return fmt.Errorf("calibrate: label %d has risk %v, want a value in [0,1]", i, s.Risk)
		}
	}
	return nil
}

// enoughLabels reports whether the labels meet the §7.3 minimum.
func enoughLabels(pos, neg int) bool {
	return pos+neg >= MinFitLabels && pos >= MinFitPerClass && neg >= MinFitPerClass
}

// ReliabilityBin is one row of the reliability table: the predictions in [Lo, Hi) (the last bin
// includes 1), their mean and the share of positive labels among them.
type ReliabilityBin struct {
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	N        int     `json:"n"`
	MeanPred float64 `json:"mean_pred"`
	FracPos  float64 `json:"frac_pos"`
}

// Reliability bins predictions into ReliabilityBins equal-width bins (bin min(floor(10p), 9)) and
// returns the non-empty ones in ascending order, with ECE = sum over bins of
// (n_b/n)·|mean_pred_b - frac_pos_b|. preds[i] is the prediction for samples[i]; sums run in input
// order. ECE is nil when there are no samples.
func Reliability(samples []LabeledScore, preds []float64) ([]ReliabilityBin, *float64) {
	type acc struct {
		n, pos int
		sum    float64
	}
	var bins [ReliabilityBins]acc
	for i, s := range samples {
		p := clamp01(preds[i])
		k := min(int(math.Floor(p*ReliabilityBins)), ReliabilityBins-1)
		bins[k].n++
		bins[k].sum += p
		if s.Fire {
			bins[k].pos++
		}
	}
	out := []ReliabilityBin{}
	if len(samples) == 0 {
		return out, nil
	}
	ece := 0.0
	for k, b := range bins {
		if b.n == 0 {
			continue
		}
		mean, frac := b.sum/float64(b.n), float64(b.pos)/float64(b.n)
		out = append(out, ReliabilityBin{
			Lo: float64(k) / ReliabilityBins, Hi: float64(k+1) / ReliabilityBins,
			N: b.n, MeanPred: mean, FracPos: frac,
		})
		ece += float64(b.n) / float64(len(samples)) * math.Abs(mean-frac)
	}
	return out, &ece
}

// Brier is the mean squared difference between the predictions and the labels (1 = should fire),
// summed in input order; nil when there are no samples.
func Brier(samples []LabeledScore, preds []float64) *float64 {
	if len(samples) == 0 {
		return nil
	}
	sum := 0.0
	for i, s := range samples {
		d := preds[i]
		if s.Fire {
			d = 1 - preds[i]
		}
		sum += d * d
	}
	b := sum / float64(len(samples))
	return &b
}

// AUROC is the probability that a random positive has a higher risk than a random negative, ties
// (risks within EPS) counting one half. It is computed exactly from integer pair counts and is nil
// unless both classes are present.
func AUROC(samples []LabeledScore) *float64 {
	pos, neg := countLabels(samples)
	if pos == 0 || neg == 0 {
		return nil
	}
	var twice int64 // 2 * (pairs ordered correctly) + ties
	negBelow := 0
	for _, g := range groupRisks(samples) {
		twice += int64(g.Pos) * int64(2*negBelow+g.Neg)
		negBelow += g.Neg
	}
	a := float64(twice) / float64(2*int64(pos)*int64(neg))
	return &a
}
