package decision

import "math"

// PlattParams are a Platt scaling (calibration.v1 platt): calibrated(x) = 1 / (1 + exp(-(A·x + B)))
// for a raw risk x. A >= 0, so the mapping never decreases. (Platt's paper writes
// 1 / (1 + exp(A'·f + B')); here A = -A' and B = -B'.)
type PlattParams struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

// Platt fitting limits: Newton's method with a backtracking line search (Lin, Lin and Weng, "A note
// on Platt's probabilistic outputs for support vector machines", 2007), run to convergence so the
// result does not depend on the path taken.
const (
	plattMaxIter  = 100
	plattMinStep  = 1e-10
	plattSigma    = 1e-12 // ridge added to the Hessian diagonal
	plattGradTol  = 1e-12 // per label: stop when |gradient| < plattGradTol·n
	plattStepTol  = 1e-12 // relative: stop after a Newton step this small
	plattArmijo   = 1e-4
	plattFlatSpan = 1e-9 // risk spans below this cannot fit a slope
)

// Platt warnings.
const (
	WarnPlattFlatScores     = "every label has the same risk, so the mapping is flat"
	WarnPlattAntiCorrelated = "risk falls as the labels rise, so the Platt slope is clamped to 0 and the mapping is flat"
	WarnPlattNotConverged   = "the Platt fit did not converge; its parameters are the last Newton iterate"
)

// sigmoid is 1/(1+exp(-z)), computed without overflow.
func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}

// softplus is log(1+exp(z)), computed without overflow.
func softplus(z float64) float64 {
	if z > 0 {
		return z + math.Log1p(math.Exp(-z))
	}
	return math.Log1p(math.Exp(z))
}

// logit is log(p/(1-p)).
func logit(p float64) float64 { return math.Log(p / (1 - p)) }

// plattTargets returns Platt's smoothed targets: (N+ + 1)/(N+ + 2) for positives and 1/(N- + 2)
// for negatives.
func plattTargets(samples []LabeledScore) []float64 {
	pos, neg := countLabels(samples)
	hi, lo := (float64(pos)+1)/(float64(pos)+2), 1/(float64(neg)+2)
	t := make([]float64, len(samples))
	for i, s := range samples {
		t[i] = lo
		if s.Fire {
			t[i] = hi
		}
	}
	return t
}

// flatPlatt is the best constant mapping for the smoothed targets: A = 0 and B = logit(mean
// target), with the mean summed in input order.
func flatPlatt(targets []float64) PlattParams {
	sum := 0.0
	for _, t := range targets {
		sum += t
	}
	return PlattParams{A: 0, B: logit(sum / float64(len(targets)))}
}

// FitPlatt fits a Platt scaling to the labels by Newton's method on the negative log-likelihood of
// the smoothed targets, starting from A = 0, B = log((N+ + 1)/(N- + 1)). It returns the parameters
// rounded to six decimals (R6) and any warnings. Degenerate inputs give a flat mapping (A = 0):
// risks that all lie within 1e-9 of each other, or a fitted slope below zero (a decreasing mapping
// cannot be a calibration table). samples must be non-empty.
func FitPlatt(samples []LabeledScore) (PlattParams, []string) {
	targets := plattTargets(samples)
	lo, hi := samples[0].Risk, samples[0].Risk
	for _, s := range samples {
		lo, hi = math.Min(lo, s.Risk), math.Max(hi, s.Risk)
	}
	if hi-lo < plattFlatSpan {
		p := flatPlatt(targets)
		return PlattParams{A: 0, B: R6(p.B)}, []string{WarnPlattFlatScores}
	}

	pos, neg := countLabels(samples)
	a, b := 0.0, math.Log((float64(pos)+1)/(float64(neg)+1))
	loss := func(a, b float64) float64 {
		sum := 0.0
		for i, s := range samples {
			z := a*s.Risk + b
			sum += softplus(z) - targets[i]*z
		}
		return sum
	}
	var warnings []string
	tol := plattGradTol * float64(len(samples))
	f := loss(a, b)
	converged := false
	for iter := 0; iter < plattMaxIter; iter++ {
		var ga, gb, h11, h12, h22 float64
		for i, s := range samples {
			p := sigmoid(a*s.Risk + b)
			d := p - targets[i]
			w := p * (1 - p)
			ga += d * s.Risk
			gb += d
			h11 += w * s.Risk * s.Risk
			h12 += w * s.Risk
			h22 += w
		}
		if math.Abs(ga) < tol && math.Abs(gb) < tol {
			converged = true
			break
		}
		h11 += plattSigma
		h22 += plattSigma
		det := h11*h22 - h12*h12
		da := -(h22*ga - h12*gb) / det
		db := -(-h12*ga + h11*gb) / det
		if math.Max(math.Abs(da), math.Abs(db)) <= plattStepTol*(1+math.Max(math.Abs(a), math.Abs(b))) {
			// A negligible full Newton step: take it and stop (the loss can no longer resolve it).
			a, b = a+da, b+db
			converged = true
			break
		}
		slope := ga*da + gb*db // directional derivative, negative for a descent direction
		step := 1.0
		accepted := false
		for step >= plattMinStep {
			na, nb := a+step*da, b+step*db
			if nf := loss(na, nb); nf < f+plattArmijo*step*slope {
				a, b, f = na, nb, nf
				accepted = true
				break
			}
			step /= 2
		}
		if !accepted { // no further decrease is representable: this is the numerical optimum
			converged = true
			break
		}
	}
	if !converged {
		warnings = append(warnings, WarnPlattNotConverged)
	}
	if a < 0 {
		p := flatPlatt(targets)
		return PlattParams{A: 0, B: R6(p.B)}, append(warnings, WarnPlattAntiCorrelated)
	}
	return PlattParams{A: R6(a), B: R6(b)}, warnings
}

// At evaluates the mapping at a raw risk x.
func (p PlattParams) At(x float64) float64 { return sigmoid(p.A*x + p.B) }

// Table is the 101-point calibration table of the mapping: t[i] = R6(At(i/100)).
func (p PlattParams) Table() []float64 {
	t := make([]float64, CalibrationTableSize)
	for i := range t {
		t[i] = R6(p.At(float64(i) / 100))
	}
	return t
}
