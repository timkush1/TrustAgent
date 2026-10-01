package decision

// IsotonicBlock is one constant piece of a PAV isotonic fit: the labels whose risks lie in
// [Lo, Hi] are all mapped to Value, the share of positives among their N labels.
type IsotonicBlock struct {
	Lo    float64 `json:"x_lo"`
	Hi    float64 `json:"x_hi"`
	Value float64 `json:"value"`
	N     int     `json:"n"`
}

// FitIsotonic fits a non-decreasing step function to the labels with the pool-adjacent-violators
// algorithm (PAV). Risks within EPS of each other are pooled first (they reach every threshold
// together, so they must map to one value). Adjacent blocks are merged while the earlier one has a
// share of positives at least as high as the later one; the comparison is exact (integer
// cross-multiplication), and merging equal shares makes the blocks maximal, so the result is
// canonical. Blocks are returned in ascending order; there are none without samples.
func FitIsotonic(samples []LabeledScore) []IsotonicBlock {
	type block struct {
		lo, hi   float64
		pos, cnt int
	}
	var stack []block
	for _, g := range groupRisks(samples) {
		stack = append(stack, block{lo: g.Lo, hi: g.Hi, pos: g.Pos, cnt: g.Pos + g.Neg})
		for len(stack) > 1 {
			a, b := stack[len(stack)-2], stack[len(stack)-1]
			// a.pos/a.cnt >= b.pos/b.cnt: pool (equal shares too, for maximal blocks)
			if int64(a.pos)*int64(b.cnt) < int64(b.pos)*int64(a.cnt) {
				break
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, block{lo: a.lo, hi: b.hi, pos: a.pos + b.pos, cnt: a.cnt + b.cnt})
		}
	}
	out := make([]IsotonicBlock, len(stack))
	for i, b := range stack {
		out[i] = IsotonicBlock{Lo: b.lo, Hi: b.hi, Value: float64(b.pos) / float64(b.cnt), N: b.cnt}
	}
	return out
}

// IsotonicAt evaluates an isotonic fit at x: the block value inside a block, linear interpolation
// between the end of one block and the start of the next, and the first or last value outside the
// fitted range (as scikit-learn's IsotonicRegression with out_of_bounds="clip"). blocks must be
// non-empty and ascending.
func IsotonicAt(blocks []IsotonicBlock, x float64) float64 {
	if x <= blocks[0].Lo {
		return blocks[0].Value
	}
	for i, b := range blocks {
		if x <= b.Hi {
			if x >= b.Lo {
				return b.Value
			}
			prev := blocks[i-1] // x is between prev.Hi and b.Lo; i > 0 because x > blocks[0].Lo
			return prev.Value + (b.Value-prev.Value)*(x-prev.Hi)/(b.Lo-prev.Hi)
		}
	}
	return blocks[len(blocks)-1].Value
}

// IsotonicTable is the 101-point calibration table of an isotonic fit: t[i] = R6(IsotonicAt(i/100)).
func IsotonicTable(blocks []IsotonicBlock) []float64 {
	t := make([]float64, CalibrationTableSize)
	for i := range t {
		t[i] = R6(IsotonicAt(blocks, float64(i)/100))
	}
	return t
}
