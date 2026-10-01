package decision

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Quick calibration (SPEC §7.3 step 4; POST /api/studio/calibrate, mirrored in TypeScript): a
// suggested raw-space threshold from a handful of labels. It never says "calibrated" and never
// emits critical_at.
const (
	// QuickPlattMinLabels is the fewest labels for which a Platt mapping is shown.
	QuickPlattMinLabels = 30
	// QuickIsotonicMinLabels and QuickIsotonicMinPerClass are the fewest labels (in total and of each
	// class) for which the isotonic mapping replaces Platt.
	QuickIsotonicMinLabels   = 50
	QuickIsotonicMinPerClass = 15
	// QuickMaxIntervalWidth and QuickMaxLOORange mark a suggestion unstable: a precision or recall
	// 95% interval wider than 0.4, or a leave-one-out threshold range wider than 0.15.
	QuickMaxIntervalWidth = 0.4
	QuickMaxLOORange      = 0.15
	// QuickDefaultTargetPrecision is the target of min_threshold_at_precision when none is given.
	QuickDefaultTargetPrecision = 0.8
)

// QuickCalibrationRequest is api.ts QuickCalibrationRequest.
type QuickCalibrationRequest struct {
	Labeled []LabeledScore `json:"labeled"`
	// Objective is max_f1 (the default) or min_threshold_at_precision.
	Objective string `json:"objective,omitempty"`
	// TargetPrecision is the precision min_threshold_at_precision must reach (default 0.8).
	TargetPrecision *float64 `json:"target_precision,omitempty"`
	// Current is the threshold in use; its row is added to the grid.
	Current *float64 `json:"current,omitempty"`
}

// PointCI is a proportion with its Wilson 95% interval.
type PointCI struct {
	Point float64    `json:"point"`
	CI95  [2]float64 `json:"ci95"`
}

// QuickMapping is QuickCalibration.mapping: the calibration a fit of these labels would give, shown
// for information only.
type QuickMapping struct {
	Method *string      `json:"method"`
	Table  []float64    `json:"table"`
	Platt  *PlattParams `json:"platt"`
}

// QuickCalibration is api.ts QuickCalibration.
type QuickCalibration struct {
	N         int      `json:"n"`
	NPos      int      `json:"n_pos"`
	NNeg      int      `json:"n_neg"`
	Enough    bool     `json:"enough"`
	Message   string   `json:"message"`
	Threshold *float64 `json:"threshold"`
	Precision *PointCI `json:"precision"`
	Recall    *PointCI `json:"recall"`
	// LOORange is the smallest and largest threshold the objective picks with one label left out.
	LOORange *[2]float64  `json:"loo_range"`
	Unstable bool         `json:"unstable"`
	Mapping  QuickMapping `json:"mapping"`
	// Grid is the confusion matrix at every candidate threshold (and at Current), ascending.
	Grid []Confusion `json:"grid"`
}

// QuickCalibrate computes a quick calibration (SPEC §7.3):
//
//   - It needs n >= 8 with at least 3 of each class; otherwise only the counts, the grid and a
//     message are returned.
//   - The threshold is raw-space: max-F1 (ties to the higher threshold) or, with
//     min_threshold_at_precision, the smallest threshold reaching the target precision.
//   - Precision and recall carry Wilson 95% intervals; loo_range is the range of thresholds the same
//     objective picks with each label left out in turn.
//   - Unstable when either interval is wider than 0.4 or the leave-one-out range exceeds 0.15.
//   - The mapping is Platt from 30 labels and isotonic from 50 labels with 15 of each class.
func QuickCalibrate(req *QuickCalibrationRequest) (*QuickCalibration, error) {
	if err := checkSamples(req.Labeled); err != nil {
		return nil, err
	}
	objective := req.Objective
	if objective == "" {
		objective = ObjectiveMaxF1
	}
	target := QuickDefaultTargetPrecision
	switch objective {
	case ObjectiveMaxF1:
	case ObjectiveMinThresholdAtPrecision:
		if req.TargetPrecision != nil {
			target = *req.TargetPrecision
		}
		if math.IsNaN(target) || target <= 0 || target > 1 {
			return nil, fmt.Errorf("calibrate: target_precision %v, want a value in (0,1]", target)
		}
	default:
		return nil, fmt.Errorf("calibrate: unknown objective %q (want %s or %s)", objective, ObjectiveMaxF1, ObjectiveMinThresholdAtPrecision)
	}
	if c := req.Current; c != nil && (math.IsNaN(*c) || *c < 0 || *c > 1) {
		return nil, fmt.Errorf("calibrate: current threshold %v, want a value in [0,1]", *c)
	}

	pick := func(grid []Confusion) (Confusion, bool) {
		if objective == ObjectiveMinThresholdAtPrecision {
			return minThresholdOf(grid, target)
		}
		return maxF1Of(grid, 0)
	}

	pos, neg := countLabels(req.Labeled)
	q := &QuickCalibration{N: pos + neg, NPos: pos, NNeg: neg, Enough: enoughLabels(pos, neg)}
	q.Grid = quickGrid(req.Labeled, req.Current)
	if !q.Enough {
		q.Message = fmt.Sprintf("Label at least %d answers, with at least %d where the check should fire and %d where it should not (you have %d and %d).",
			MinFitLabels, MinFitPerClass, MinFitPerClass, pos, neg)
		return q, nil
	}

	c, ok := pick(ThresholdGrid(req.Labeled))
	if !ok {
		q.Message = fmt.Sprintf("With %d labels, no threshold reaches precision %s; keep it watching and label more traffic.", q.N, percent(target))
		q.Mapping = quickMapping(req.Labeled, pos, neg)
		return q, nil
	}
	t := c.Threshold
	q.Threshold = &t
	plo, phi := WilsonInterval(c.TP, c.TP+c.FP, WilsonZ95)
	rlo, rhi := WilsonInterval(c.TP, c.TP+c.FN, WilsonZ95)
	q.Precision = &PointCI{Point: c.Precision(), CI95: [2]float64{plo, phi}}
	q.Recall = &PointCI{Point: c.Recall(), CI95: [2]float64{rlo, rhi}}

	// Leave-one-out: each label left out in turn, regrouped and re-picked. The risk order does not
	// depend on which label is left out, so one sort serves every pass (O(n) each).
	loLOO, hiLOO, found := 0.0, 0.0, false
	sorted := sortByRisk(req.Labeled)
	var groups []riskGroup
	var grid []Confusion
	for i := range sorted {
		groups = groupSorted(sorted, i, groups)
		grid = gridOf(groups, grid)
		li, ok := pick(grid)
		if !ok {
			continue
		}
		if !found {
			loLOO, hiLOO, found = li.Threshold, li.Threshold, true
			continue
		}
		loLOO, hiLOO = math.Min(loLOO, li.Threshold), math.Max(hiLOO, li.Threshold)
	}
	if found {
		q.LOORange = &[2]float64{loLOO, hiLOO}
	}
	q.Unstable = phi-plo > QuickMaxIntervalWidth+EPS || rhi-rlo > QuickMaxIntervalWidth+EPS ||
		(q.LOORange != nil && q.LOORange[1]-q.LOORange[0] > QuickMaxLOORange+EPS)
	q.Mapping = quickMapping(req.Labeled, pos, neg)
	q.Message = quickMessage(q)
	return q, nil
}

// quickGrid is the threshold grid, with a row for current inserted in order when it is not
// already a candidate (within EPS).
func quickGrid(samples []LabeledScore, current *float64) []Confusion {
	grid := ThresholdGrid(samples)
	if current == nil {
		return grid
	}
	at := slices.IndexFunc(grid, func(c Confusion) bool { return c.Threshold >= *current-EPS })
	if at >= 0 && math.Abs(grid[at].Threshold-*current) <= EPS {
		return grid
	}
	row := ConfusionAt(samples, *current)
	if at < 0 {
		return append(grid, row)
	}
	return slices.Insert(grid, at, row)
}

// quickMapping is the informational mapping: none below 30 labels, Platt from 30, isotonic from 50
// with 15 of each class.
func quickMapping(samples []LabeledScore, pos, neg int) QuickMapping {
	n := pos + neg
	switch {
	case n >= QuickIsotonicMinLabels && pos >= QuickIsotonicMinPerClass && neg >= QuickIsotonicMinPerClass:
		m := MethodIsotonic
		return QuickMapping{Method: &m, Table: IsotonicTable(FitIsotonic(samples))}
	case n >= QuickPlattMinLabels:
		m := MethodPlatt
		p, _ := FitPlatt(samples)
		return QuickMapping{Method: &m, Table: p.Table(), Platt: &p}
	}
	return QuickMapping{}
}

// quickMessage is the honesty text of §7.3. It says "suggested threshold", never "calibrated".
func quickMessage(q *QuickCalibration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Suggested threshold from %d labels: %s. ", q.N, strconv.FormatFloat(*q.Threshold, 'f', -1, 64))
	fmt.Fprintf(&b, "With %d labels, recall is %s (95%% range %s–%s) and precision is %s (95%% range %s–%s)",
		q.N, percent(q.Recall.Point), percent(q.Recall.CI95[0]), percent(q.Recall.CI95[1]),
		percent(q.Precision.Point), percent(q.Precision.CI95[0]), percent(q.Precision.CI95[1]))
	if q.Unstable {
		b.WriteString("; the suggestion is unstable")
		if q.LOORange != nil {
			fmt.Fprintf(&b, " (leaving out one label moves it between %s and %s)",
				strconv.FormatFloat(q.LOORange[0], 'f', -1, 64), strconv.FormatFloat(q.LOORange[1], 'f', -1, 64))
		}
	}
	b.WriteString(". Keep it watching and label more traffic.")
	return b.String()
}

// percent formats a proportion as a whole percentage, rounding half up (floor(100p + 0.5)).
func percent(p float64) string {
	return strconv.Itoa(int(math.Floor(p*100+0.5))) + "%"
}
