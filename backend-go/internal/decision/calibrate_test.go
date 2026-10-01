package decision

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// labels builds scores from (risk, positives, negatives) triples, positives first.
func labels(groups ...[3]float64) []LabeledScore {
	var out []LabeledScore
	for _, g := range groups {
		for range int(g[1]) {
			out = append(out, LabeledScore{Risk: g[0], Fire: true})
		}
		for range int(g[2]) {
			out = append(out, LabeledScore{Risk: g[0], Fire: false})
		}
	}
	return out
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestR6(t *testing.T) {
	for _, c := range []struct{ x, want float64 }{
		{0.1234564, 0.123456}, {0.1234565, 0.123457}, {0.9999996, 1}, {-1.2345675, -1.234567}, {0, 0}, {1, 1},
	} {
		if got := R6(c.x); got != c.want {
			t.Errorf("R6(%v) = %v, want %v", c.x, got, c.want)
		}
	}
}

func TestWilsonInterval(t *testing.T) {
	// Reference values: the Wilson score interval at z = 1.959963984540054.
	for _, c := range []struct {
		k, n   int
		lo, hi float64
	}{
		{8, 10, 0.49016247153664183, 0.9433178485456247},
		{0, 10, 0, 0.2775327998628892},
		{10, 10, 0.7224672001371107, 1},
		{1, 2, 0.09453120573423074, 0.9054687942657693},
		{80, 80, 0.9541818704644728, 1},
		{100, 101, 0.9460328420055449, 0.9982500887510318},
	} {
		lo, hi := WilsonInterval(c.k, c.n, WilsonZ95)
		if !near(lo, c.lo, 1e-12) || !near(hi, c.hi, 1e-12) {
			t.Errorf("Wilson(%d/%d) = [%v, %v], want [%v, %v]", c.k, c.n, lo, hi, c.lo, c.hi)
		}
	}
	if lo, hi := WilsonInterval(0, 0, WilsonZ95); lo != 0 || hi != 1 {
		t.Errorf("Wilson(0/0) = [%v, %v], want [0, 1]", lo, hi)
	}
	// Property: the interval holds the point estimate and narrows as n grows at a fixed share.
	prev := 1.0
	for n := 10; n <= 10000; n *= 10 {
		k := n * 3 / 10
		lo, hi := WilsonInterval(k, n, WilsonZ95)
		p := float64(k) / float64(n)
		if lo > p || hi < p || lo < 0 || hi > 1 {
			t.Errorf("Wilson(%d/%d) = [%v, %v] does not hold %v", k, n, lo, hi, p)
		}
		if hi-lo >= prev {
			t.Errorf("Wilson(%d/%d) width %v did not shrink below %v", k, n, hi-lo, prev)
		}
		prev = hi - lo
	}
}

func TestThresholdGridAndConfusionAt(t *testing.T) {
	s := labels([3]float64{0.1, 0, 2}, [3]float64{0.5, 1, 1}, [3]float64{0.5 + 1e-10, 1, 0}, [3]float64{0.9, 2, 0})
	grid := ThresholdGrid(s)
	want := []Confusion{
		{Threshold: 0.1, TP: 4, FP: 3, TN: 0, FN: 0},
		{Threshold: 0.5, TP: 4, FP: 1, TN: 2, FN: 0}, // 0.5 and 0.5+1e-10 are one candidate
		{Threshold: 0.9, TP: 2, FP: 0, TN: 3, FN: 2},
	}
	if !slices.Equal(grid, want) {
		t.Fatalf("grid = %+v, want %+v", grid, want)
	}
	for _, c := range grid {
		if got := ConfusionAt(s, c.Threshold); got != c {
			t.Errorf("ConfusionAt(%v) = %+v, grid says %+v", c.Threshold, got, c)
		}
	}
	if got := ThresholdGrid(nil); got == nil || len(got) != 0 {
		t.Errorf("ThresholdGrid(nil) = %#v, want an empty slice", got)
	}
}

func TestMaxF1TieBreakAndPrecisionFloor(t *testing.T) {
	// At 0.3: TP 4, FP 4, FN 0 -> F1 8/12. At 0.7: TP 2, FP 0, FN 2 -> F1 4/6. Equal: the higher
	// threshold wins.
	s := labels([3]float64{0.3, 2, 4}, [3]float64{0.7, 2, 0})
	c, ok := MaxF1Threshold(s, 0)
	if !ok || c.Threshold != 0.7 || c.F1() != 4.0/6 {
		t.Fatalf("MaxF1Threshold = %+v %v, want the tie to go to 0.7", c, ok)
	}
	// A precision floor rules out low-precision candidates.
	s2 := labels([3]float64{0.2, 3, 1}, [3]float64{0.6, 1, 1}, [3]float64{0.8, 2, 0})
	if c, _ := MaxF1Threshold(s2, 0); c.Threshold != 0.2 {
		t.Errorf("without a floor: %+v, want 0.2 (F1 12/14)", c)
	}
	if c, _ := MaxF1Threshold(s2, 0.9); c.Threshold != 0.8 {
		t.Errorf("with floor 0.9: %+v, want 0.8", c)
	}
	if _, ok := MaxF1Threshold(labels([3]float64{0.5, 0, 3}), 0); ok {
		t.Error("MaxF1Threshold found a threshold without positives")
	}
	if c, ok := MinThresholdAtPrecision(s2, 0.75); !ok || c.Threshold != 0.2 {
		t.Errorf("MinThresholdAtPrecision(0.75) = %+v %v, want 0.2", c, ok)
	}
	if c, ok := MinThresholdAtPrecision(s2, 0.8); !ok || c.Threshold != 0.8 {
		t.Errorf("MinThresholdAtPrecision(0.8) = %+v %v, want 0.8 (precision 3/4 at 0.2 and 0.6)", c, ok)
	}
}

func TestMaxF1MatchesBruteForce(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for trial := 0; trial < 200; trial++ {
		s := randomLabels(r, 5+r.IntN(40), 0.4)
		got, ok := MaxF1Threshold(s, 0)
		var best Confusion
		found := false
		risks := make([]float64, len(s))
		for i := range s {
			risks[i] = s[i].Risk
		}
		slices.Sort(risks)
		for _, x := range slices.Compact(risks) {
			c := ConfusionAt(s, x)
			if c.TP == 0 {
				continue
			}
			if !found || c.F1() > best.F1()+1e-15 || (math.Abs(c.F1()-best.F1()) <= 1e-15 && x > best.Threshold) {
				best, found = c, true
			}
		}
		if ok != found || (ok && got != best) {
			t.Fatalf("trial %d: MaxF1Threshold = %+v %v, brute force %+v %v", trial, got, ok, best, found)
		}
	}
}

func TestWilsonCriticalAt(t *testing.T) {
	// 80 positives at 0.9 give LB(80/80) = 0.9542; adding 0.8 (20 pos, 1 neg) drops LB(100/101)
	// below 0.95; 0.7 (200 pos) lifts LB(300/301) back above it. The rule needs every judgeable
	// threshold above to pass, so critical_at stays 0.9.
	s := labels([3]float64{0.1, 0, 300}, [3]float64{0.7, 200, 0}, [3]float64{0.8, 20, 1}, [3]float64{0.9, 80, 0})
	c := WilsonCriticalAt(s, CriticalMinPredictedPos, CriticalMinPrecisionLB)
	if c == nil || c.Value != 0.9 || c.PredictedPos != 80 || !near(c.PrecisionLB95, 0.9541818704644728, 1e-12) {
		t.Fatalf("critical_at = %+v, want 0.9 with 80 predicted positives", c)
	}
	// Too few predicted positives anywhere above the negatives: null.
	if c := WilsonCriticalAt(labels([3]float64{0.1, 0, 10}, [3]float64{0.9, 40, 0}), 50, 0.95); c != nil {
		t.Errorf("critical_at = %+v with 40 predicted positives, want nil", c)
	}
	// The highest judgeable threshold fails: null.
	if c := WilsonCriticalAt(labels([3]float64{0.9, 60, 1}), 50, 0.95); c != nil {
		t.Errorf("critical_at = %+v at LB(60/61), want nil", c)
	}
	// Extends down while every threshold passes.
	s2 := labels([3]float64{0.2, 0, 50}, [3]float64{0.6, 100, 0}, [3]float64{0.9, 80, 0})
	if c := WilsonCriticalAt(s2, 50, 0.95); c == nil || c.Value != 0.6 || c.PredictedPos != 180 {
		t.Errorf("critical_at = %+v, want 0.6 with 180 predicted positives", c)
	}
}

func TestMetrics(t *testing.T) {
	s := labels([3]float64{0.2, 1, 3}, [3]float64{0.8, 3, 1})
	preds := make([]float64, len(s))
	for i := range s {
		preds[i] = s[i].Risk
	}
	bins, ece := Reliability(s, preds)
	want := []ReliabilityBin{{Lo: 0.2, Hi: 0.3, N: 4, MeanPred: 0.2, FracPos: 0.25}, {Lo: 0.8, Hi: 0.9, N: 4, MeanPred: 0.8, FracPos: 0.75}}
	if len(bins) != 2 {
		t.Fatalf("bins = %+v", bins)
	}
	for i := range want {
		b := bins[i]
		if !near(b.Lo, want[i].Lo, 1e-15) || !near(b.Hi, want[i].Hi, 1e-15) || b.N != want[i].N ||
			!near(b.MeanPred, want[i].MeanPred, 1e-15) || !near(b.FracPos, want[i].FracPos, 1e-15) {
			t.Errorf("bin %d = %+v, want %+v", i, b, want[i])
		}
	}
	if ece == nil || !near(*ece, 0.05, 1e-15) {
		t.Errorf("ECE = %v, want 0.05", ece)
	}
	// Brier: 1 pos at 0.2 (0.64) + 3 neg at 0.2 (0.04 each) + 3 pos at 0.8 (0.04) + 1 neg at 0.8 (0.64).
	if b := Brier(s, preds); b == nil || !near(*b, (0.64*2+0.04*6)/8, 1e-15) {
		t.Errorf("Brier = %v", b)
	}
	// AUROC: positives above negatives 3*3 + ties (1*3 + 3*1)/2 + 1 pos at 0.2 vs 1 neg at 0.8: 0.
	if a := AUROC(s); a == nil || !near(*a, (9+3)/16.0, 1e-15) {
		t.Errorf("AUROC = %v, want 0.75", a)
	}
	if AUROC(labels([3]float64{0.5, 2, 0})) != nil || Brier(nil, nil) != nil {
		t.Error("undefined metrics must be nil")
	}
	if _, ece := Reliability(nil, nil); ece != nil {
		t.Error("ECE of no samples must be nil")
	}
	// The last bin includes 1.
	bins, _ = Reliability(labels([3]float64{1, 1, 0}), []float64{1})
	if len(bins) != 1 || bins[0].Lo != 0.9 || bins[0].Hi != 1 {
		t.Errorf("bin of 1 = %+v", bins)
	}
}

// isotonicReference is the min-max formula of isotonic regression on grouped points:
// f(i) = max over j <= i of min over k >= i of mean(y[j..k]).
func isotonicReference(groups []riskGroup) []float64 {
	out := make([]float64, len(groups))
	for i := range groups {
		best := math.Inf(-1)
		for j := 0; j <= i; j++ {
			low := math.Inf(1)
			for k := i; k < len(groups); k++ {
				pos, cnt := 0, 0
				for m := j; m <= k; m++ {
					pos += groups[m].Pos
					cnt += groups[m].Pos + groups[m].Neg
				}
				low = math.Min(low, float64(pos)/float64(cnt))
			}
			best = math.Max(best, low)
		}
		out[i] = best
	}
	return out
}

func TestFitIsotonic(t *testing.T) {
	// 0.1: 1 pos; 0.2: 1 neg -> violators pooled to 0.5; 0.3: 1 pos, 1 neg -> 0.5 pooled too.
	blocks := FitIsotonic(labels([3]float64{0.1, 1, 0}, [3]float64{0.2, 0, 1}, [3]float64{0.3, 1, 1}, [3]float64{0.6, 2, 0}))
	want := []IsotonicBlock{{Lo: 0.1, Hi: 0.3, Value: 0.5, N: 4}, {Lo: 0.6, Hi: 0.6, Value: 1, N: 2}}
	if !slices.Equal(blocks, want) {
		t.Fatalf("blocks = %+v, want %+v", blocks, want)
	}
	for _, c := range []struct{ x, want float64 }{{0, 0.5}, {0.1, 0.5}, {0.3, 0.5}, {0.45, 0.75}, {0.6, 1}, {1, 1}} {
		if got := IsotonicAt(blocks, c.x); !near(got, c.want, 1e-12) {
			t.Errorf("IsotonicAt(%v) = %v, want %v", c.x, got, c.want)
		}
	}
	table := IsotonicTable(blocks)
	if err := CheckCalibrationTable(table); err != nil {
		t.Fatal(err)
	}
	if table[45] != 0.75 || table[10] != 0.5 || table[100] != 1 {
		t.Errorf("table[10,45,100] = %v %v %v", table[10], table[45], table[100])
	}
}

func TestIsotonicProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 300; trial++ {
		s := randomLabels(r, 1+r.IntN(30), 0.2)
		blocks := FitIsotonic(s)
		groups := groupRisks(s)
		ref := isotonicReference(groups)
		// Every grouped point's fitted value equals the min-max reference.
		for i, g := range groups {
			if got := IsotonicAt(blocks, g.Lo); !near(got, ref[i], 1e-12) {
				t.Fatalf("trial %d: fitted(%v) = %v, reference %v (blocks %+v)", trial, g.Lo, got, ref[i], blocks)
			}
		}
		n := 0
		for i, b := range blocks {
			n += b.N
			if i > 0 && b.Value <= blocks[i-1].Value {
				t.Fatalf("trial %d: blocks not strictly increasing: %+v", trial, blocks)
			}
		}
		if n != len(s) {
			t.Fatalf("trial %d: blocks hold %d labels, want %d", trial, n, len(s))
		}
		if err := CheckCalibrationTable(IsotonicTable(blocks)); err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
	}
}

func TestFitPlatt(t *testing.T) {
	s := labels([3]float64{0.1, 1, 9}, [3]float64{0.5, 5, 5}, [3]float64{0.9, 9, 1})
	p, warnings := FitPlatt(s)
	if len(warnings) != 0 || p.A <= 0 {
		t.Fatalf("FitPlatt = %+v %v, want a positive slope and no warnings", p, warnings)
	}
	// Symmetric data: the mapping passes through 0.5 at x = 0.5 (B = -A/2).
	if !near(p.At(0.5), 0.5, 1e-5) {
		t.Errorf("calibrated(0.5) = %v, want 0.5", p.At(0.5))
	}
	checkPlattOptimal(t, s, p)

	// Anti-correlated labels: the slope is clamped to 0 and the mapping is flat at the smoothed
	// base rate.
	anti := labels([3]float64{0.1, 8, 2}, [3]float64{0.9, 2, 8})
	p, warnings = FitPlatt(anti)
	if p.A != 0 || !slices.Contains(warnings, WarnPlattAntiCorrelated) || !near(p.At(0.3), 0.5, 1e-6) {
		t.Errorf("anti-correlated FitPlatt = %+v %v", p, warnings)
	}
	// One risk for every label: flat.
	p, warnings = FitPlatt(labels([3]float64{0.4, 3, 1}))
	if p.A != 0 || !slices.Equal(warnings, []string{WarnPlattFlatScores}) {
		t.Errorf("flat FitPlatt = %+v %v", p, warnings)
	}
	// Separable labels stay finite thanks to the smoothed targets.
	p, _ = FitPlatt(labels([3]float64{0.1, 0, 20}, [3]float64{0.9, 20, 0}))
	if math.IsInf(p.A, 0) || math.IsNaN(p.A) || p.At(0.9) >= 1 || p.At(0.1) <= 0 {
		t.Errorf("separable FitPlatt = %+v", p)
	}
}

// checkPlattOptimal checks the first-order conditions of the smoothed-target likelihood at p, up
// to the R6 rounding of the parameters.
func checkPlattOptimal(t *testing.T, s []LabeledScore, p PlattParams) {
	t.Helper()
	targets := plattTargets(s)
	var ga, gb float64
	for i, x := range s {
		d := p.At(x.Risk) - targets[i]
		ga += d * x.Risk
		gb += d
	}
	if tol := 1e-5 * float64(len(s)); math.Abs(ga) > tol || math.Abs(gb) > tol {
		t.Errorf("Platt %+v is not optimal: gradient (%v, %v)", p, ga, gb)
	}
}

func TestPlattProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for trial := 0; trial < 200; trial++ {
		s := randomLabels(r, 2+r.IntN(60), 0.3)
		p, warnings := FitPlatt(s)
		if p.A < 0 || math.IsNaN(p.A) || math.IsNaN(p.B) || slices.Contains(warnings, WarnPlattNotConverged) {
			t.Fatalf("trial %d: FitPlatt = %+v %v", trial, p, warnings)
		}
		for _, x := range []float64{0, 0.25, 0.5, 0.75, 1} {
			if y := p.At(x); !(y > 0 && y < 1) {
				t.Fatalf("trial %d: calibrated(%v) = %v, want a probability in (0,1)", trial, x, y)
			}
		}
		if err := CheckCalibrationTable(p.Table()); err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		if p.A > 0 {
			checkPlattOptimal(t, s, p)
		}
	}
}

// randomLabels draws n labels whose chance of firing grows with the risk; risks are rounded to
// two decimals with probability tie, so ties occur.
func randomLabels(r *rand.Rand, n int, tie float64) []LabeledScore {
	out := make([]LabeledScore, n)
	for i := range out {
		x := r.Float64()
		if r.Float64() < tie {
			x = math.Floor(x*100) / 100
		}
		out[i] = LabeledScore{Risk: x, Fire: r.Float64() < 0.2+0.6*x}
	}
	return out
}

func TestFitCalibration(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	small := randomLabels(r, 120, 0)
	fit, err := FitCalibration(small, FitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fit.Method != MethodPlatt || fit.Platt == nil || fit.CVBrier != nil || fit.N != 120 || fit.NPos+fit.NNeg != 120 {
		t.Errorf("auto below 500 labels: method %s, platt %v, cv %v", fit.Method, fit.Platt, fit.CVBrier)
	}
	if err := CheckCalibrationTable(fit.Table); err != nil {
		t.Fatal(err)
	}
	if fit.Recommended.Space != SpaceCalibrated || fit.Recommended.CriticalAt != nil ||
		fit.Recommended.CriticalNullReason == nil || *fit.Recommended.CriticalNullReason != CriticalNullReason {
		t.Errorf("recommended = %+v", fit.Recommended)
	}
	if fit.Metrics.ECEBefore == nil || fit.Metrics.ECEAfter == nil || fit.Metrics.BrierAfter == nil || fit.Metrics.AUROC == nil {
		t.Errorf("metrics = %+v", fit.Metrics)
	}
	if *fit.Metrics.BrierAfter > *fit.Metrics.BrierBefore+1e-9 {
		t.Errorf("Platt made in-sample Brier worse: %v -> %v", *fit.Metrics.BrierBefore, *fit.Metrics.BrierAfter)
	}

	large := randomLabels(r, 600, 0.5)
	fit, err = FitCalibration(large, FitOptions{Method: MethodAuto})
	if err != nil {
		t.Fatal(err)
	}
	if fit.CVBrier == nil || (fit.Method == MethodIsotonic) != (fit.CVBrier.Isotonic <= fit.CVBrier.Platt+EPS) {
		t.Errorf("auto at 600 labels: method %s, cv %+v", fit.Method, fit.CVBrier)
	}
	iso, err := FitCalibration(small, FitOptions{Method: MethodIsotonic})
	if err != nil || iso.Method != MethodIsotonic || iso.Platt != nil || !slices.Contains(iso.Warnings, WarnIsotonicFewLabels) {
		t.Errorf("explicit isotonic: %+v %v", iso, err)
	}
	if _, err := FitCalibration(small, FitOptions{Method: "spline"}); err == nil {
		t.Error("unknown method accepted")
	}
	if _, err := FitCalibration(labels([3]float64{0.5, 2, 10}), FitOptions{}); !errors.Is(err, ErrNotEnoughLabels) {
		t.Errorf("2 positives: err = %v, want ErrNotEnoughLabels", err)
	}
	if _, err := FitCalibration([]LabeledScore{{Risk: math.NaN()}}, FitOptions{}); err == nil {
		t.Error("NaN risk accepted")
	}
}

func TestFitCalibrationRecommendsCriticalAt(t *testing.T) {
	// 550 labels: 300 clean negatives low, 5/50 in the middle, 200 positives high. Isotonic maps
	// the top block to 1, and 200 predicted positives with no false alarm clear the Wilson bound.
	s := labels([3]float64{0.05, 0, 150}, [3]float64{0.15, 0, 150}, [3]float64{0.55, 5, 45}, [3]float64{0.95, 200, 0})
	fit, err := FitCalibration(s, FitOptions{Method: MethodIsotonic})
	if err != nil {
		t.Fatal(err)
	}
	c := fit.Recommended.CriticalAt
	if c == nil || c.PredictedPos != 200 || c.Value != 1 || fit.Recommended.CriticalNullReason != nil {
		t.Fatalf("critical_at = %+v (reason %v)", c, fit.Recommended.CriticalNullReason)
	}
	w := fit.Recommended.WarnAt
	if w == nil || w.Value != 1 || w.Precision != 1 || !near(w.Recall, 200.0/205, 1e-12) {
		t.Errorf("warn_at = %+v", w)
	}
}

func TestFitCalibrationKeepsWarnAtBelowCriticalAt(t *testing.T) {
	// Max-F1 prefers the top block alone (F1 2000/2005 against 2010/2016 with the middle block),
	// while the Wilson bound still holds with the middle block (1005/1011), so critical_at is the
	// middle block's calibrated value and warn_at comes down to it.
	s := labels([3]float64{0.1, 0, 300}, [3]float64{0.5, 5, 6}, [3]float64{0.9, 1000, 0})
	fit, err := FitCalibration(s, FitOptions{Method: MethodIsotonic})
	if err != nil {
		t.Fatal(err)
	}
	c, w := fit.Recommended.CriticalAt, fit.Recommended.WarnAt
	if c == nil || w == nil || !near(c.Value, 5.0/11, 1e-6) || c.PredictedPos != 1011 {
		t.Fatalf("critical_at %+v, warn_at %+v", c, w)
	}
	if w.Value != c.Value || !near(w.Precision, 1005.0/1011, 1e-12) || w.Recall != 1 || !slices.Contains(fit.Warnings, WarnWarnAtCapped) {
		t.Errorf("warn_at = %+v, warnings %v", w, fit.Warnings)
	}
	if uncapped, ok := MaxF1Threshold(calibratedLabels(fit.Table, s), WarnMinPrecision); !ok || uncapped.Threshold != 1 {
		t.Errorf("the case no longer crosses: max-F1 warn_at %+v", uncapped)
	}
}

func calibratedLabels(table []float64, s []LabeledScore) []LabeledScore {
	out := make([]LabeledScore, len(s))
	for i, x := range s {
		out[i] = LabeledScore{Risk: ApplyCalibration(table, x.Risk), Fire: x.Fire}
	}
	return out
}

func TestQuickCalibrate(t *testing.T) {
	few := &QuickCalibrationRequest{Labeled: labels([3]float64{0.2, 1, 3}, [3]float64{0.8, 2, 1})}
	q, err := QuickCalibrate(few)
	if err != nil {
		t.Fatal(err)
	}
	if q.Enough || q.Threshold != nil || q.Precision != nil || q.LOORange != nil || q.Mapping.Method != nil ||
		len(q.Grid) != 2 || !strings.Contains(q.Message, "at least 8") {
		t.Errorf("7 labels: %+v", q)
	}

	req := &QuickCalibrationRequest{Labeled: labels(
		[3]float64{0.1, 0, 3}, [3]float64{0.3, 1, 2}, [3]float64{0.6, 2, 1}, [3]float64{0.9, 3, 0})}
	q, err = QuickCalibrate(req)
	if err != nil {
		t.Fatal(err)
	}
	// Grid: 0.1 (6/6 TP, 6 FP) F1 12/18; 0.3 TP 6 FP 3 F1 12/15; 0.6 TP 5 FP 1 F1 10/12; 0.9 TP 3 F1 6/9.
	if !q.Enough || q.Threshold == nil || *q.Threshold != 0.6 {
		t.Fatalf("threshold = %v, want 0.6", q.Threshold)
	}
	if q.Precision.Point != 5.0/6 || q.Recall.Point != 5.0/6 {
		t.Errorf("precision %v recall %v", q.Precision, q.Recall)
	}
	if q.LOORange == nil || q.LOORange[0] > 0.6 || q.LOORange[1] < 0.6 {
		t.Errorf("loo_range = %v", q.LOORange)
	}
	if !q.Unstable || q.Mapping.Method != nil {
		t.Errorf("12 labels: unstable %v, mapping %+v", q.Unstable, q.Mapping)
	}
	for _, word := range []string{"Suggested threshold from 12 labels", "recall is 83%", "95% range", "Keep it watching and label more traffic", "unstable"} {
		if !strings.Contains(q.Message, word) {
			t.Errorf("message %q lacks %q", q.Message, word)
		}
	}
	if strings.Contains(strings.ToLower(q.Message), "calibrated") {
		t.Errorf("quick calibration message says calibrated: %q", q.Message)
	}

	cur := 0.45
	req.Current = &cur
	req.Objective = ObjectiveMinThresholdAtPrecision
	tp := 0.99
	req.TargetPrecision = &tp
	q, err = QuickCalibrate(req)
	if err != nil {
		t.Fatal(err)
	}
	if q.Threshold == nil || *q.Threshold != 0.9 {
		t.Errorf("min threshold at precision 0.99 = %v, want 0.9", q.Threshold)
	}
	if len(q.Grid) != 5 || q.Grid[2].Threshold != 0.45 || q.Grid[2].TP != 5 {
		t.Errorf("grid with current 0.45 = %+v", q.Grid)
	}

	for _, bad := range []*QuickCalibrationRequest{
		{Labeled: req.Labeled, Objective: "recall"},
		{Labeled: req.Labeled, Objective: ObjectiveMinThresholdAtPrecision, TargetPrecision: new(float64)},
		{Labeled: []LabeledScore{{Risk: 2}}},
		{Labeled: req.Labeled, Current: func() *float64 { v := -0.1; return &v }()},
	} {
		if _, err := QuickCalibrate(bad); err == nil {
			t.Errorf("QuickCalibrate(%+v) accepted", bad)
		}
	}
}

// TestQuickLeaveOneOutMatchesTheDefinition checks the single-sort leave-one-out against the
// definition: refit on every subset with one label removed. Chains of risks within EPS, which a
// removal can split, are included.
func TestQuickLeaveOneOutMatchesTheDefinition(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 22))
	for trial := 0; trial < 150; trial++ {
		s := randomLabels(r, 8+r.IntN(40), 0.4)
		if trial%3 == 0 {
			s = append(s, LabeledScore{Risk: 0.5, Fire: true}, LabeledScore{Risk: 0.5 + 0.8e-9}, LabeledScore{Risk: 0.5 + 1.6e-9, Fire: true})
		}
		for _, objective := range []string{ObjectiveMaxF1, ObjectiveMinThresholdAtPrecision} {
			q, err := QuickCalibrate(&QuickCalibrationRequest{Labeled: s, Objective: objective})
			if err != nil {
				t.Fatal(err)
			}
			if !q.Enough || q.Threshold == nil {
				continue
			}
			var lo, hi float64
			found := false
			for i := range s {
				rest := append(slices.Clone(s[:i]), s[i+1:]...)
				var c Confusion
				var ok bool
				if objective == ObjectiveMaxF1 {
					c, ok = MaxF1Threshold(rest, 0)
				} else {
					c, ok = MinThresholdAtPrecision(rest, QuickDefaultTargetPrecision)
				}
				if !ok {
					continue
				}
				if !found {
					lo, hi, found = c.Threshold, c.Threshold, true
				}
				lo, hi = math.Min(lo, c.Threshold), math.Max(hi, c.Threshold)
			}
			if (q.LOORange != nil) != found || (found && (q.LOORange[0] != lo || q.LOORange[1] != hi)) {
				t.Fatalf("trial %d %s: loo_range %v, definition [%v, %v] (found %v)", trial, objective, q.LOORange, lo, hi, found)
			}
		}
	}
}

func TestQuickMappingThresholds(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	balanced := func(n, pos int) []LabeledScore {
		out := make([]LabeledScore, n)
		for i := range out {
			out[i] = LabeledScore{Risk: r.Float64(), Fire: i < pos}
		}
		return out
	}
	for _, c := range []struct {
		n, pos int
		want   string
	}{{29, 10, ""}, {30, 10, MethodPlatt}, {50, 15, MethodIsotonic}, {50, 14, MethodPlatt}, {60, 45, MethodIsotonic}, {60, 46, MethodPlatt}} {
		q, err := QuickCalibrate(&QuickCalibrationRequest{Labeled: balanced(c.n, c.pos)})
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if q.Mapping.Method != nil {
			got = *q.Mapping.Method
			if err := CheckCalibrationTable(q.Mapping.Table); err != nil {
				t.Errorf("n=%d: %v", c.n, err)
			}
		}
		if got != c.want || (got == MethodPlatt) != (q.Mapping.Platt != nil) {
			t.Errorf("n=%d pos=%d: mapping %q (platt %v), want %q", c.n, c.pos, got, q.Mapping.Platt, c.want)
		}
	}
}

func TestDeriveQuality(t *testing.T) {
	public := LabelSourceCounts{PublicDataset: 200}
	for _, c := range []struct {
		name     string
		snapshot string
		src      LabelSourceCounts
		pos, neg int
		family   bool
		want     string
	}{
		{"measured", "typesafe/jev-1.13-20260917", public, 170, 30, false, QualityMeasured},
		{"human and public", "typesafe/jev-1.13-20260917", LabelSourceCounts{Human: 100, PublicDataset: 100}, 100, 100, false, QualityMeasured},
		{"too few negatives", "typesafe/jev-1.13-20260917", public, 171, 29, false, QualityProvisional},
		{"too few labels", "typesafe/jev-1.13-20260917", LabelSourceCounts{Human: 199}, 100, 99, false, QualityProvisional},
		{"one judge label", "typesafe/jev-1.13-20260917", LabelSourceCounts{PublicDataset: 300, Judge: 1}, 150, 151, false, QualityProxy},
		{"uniform audit", "typesafe/jev-1.13-20260917", LabelSourceCounts{Human: 300, UniformAudit: 5}, 150, 155, false, QualityProxy},
		{"family fallback", "typesafe/jev-1.13-20260917", public, 100, 100, true, QualityProvisional},
		{"mock snapshot", "mock/jev-heuristic-v1", LabelSourceCounts{Judge: 12}, 6, 6, false, QualityMock},
	} {
		if got := DeriveQuality(c.snapshot, c.src, c.pos, c.neg, c.family); got != c.want {
			t.Errorf("%s: quality %q, want %q", c.name, got, c.want)
		}
	}
	var src LabelSourceCounts
	for _, s := range []string{LabelSourceHuman, LabelSourcePublicDataset, LabelSourceUniformAudit, LabelSourceJudge, LabelSourceJudge} {
		if err := src.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	if src != (LabelSourceCounts{1, 1, 1, 2}) || src.Total() != 5 || src.Add("crowd") == nil {
		t.Errorf("label source counts = %+v", src)
	}
}
