package vectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/truthtable/backend-go/internal/decision"
)

// calibrate_fit vectors pin the calibration fitter (SPEC §2.11, §7.3). The input names an op; the
// input and output of each op are documented in contracts/vectors/README.md.

// labelSet is the labels of a calibrate_fit input: samples as written, or groups expanded in order
// (each group's positives, then its negatives, at its risk). Give exactly one.
type labelSet struct {
	Samples []decision.LabeledScore `json:"samples,omitempty"`
	Groups  []struct {
		Risk float64 `json:"risk_raw"`
		Pos  int     `json:"pos"`
		Neg  int     `json:"neg"`
	} `json:"groups,omitempty"`
}

func (l labelSet) labels() ([]decision.LabeledScore, error) {
	switch {
	case l.Samples != nil && l.Groups != nil:
		return nil, errors.New("give either samples or groups, not both")
	case l.Samples != nil:
		return l.Samples, nil
	case l.Groups == nil:
		return nil, errors.New("missing samples or groups")
	}
	var out []decision.LabeledScore
	for i, g := range l.Groups {
		if g.Pos < 0 || g.Neg < 0 {
			return nil, fmt.Errorf("groups[%d]: negative count", i)
		}
		for range g.Pos {
			out = append(out, decision.LabeledScore{Risk: g.Risk, Fire: true})
		}
		for range g.Neg {
			out = append(out, decision.LabeledScore{Risk: g.Risk, Fire: false})
		}
	}
	return out, nil
}

// calibrate_fit: {op, ...} -> the op's output
func runCalibrateFit(in json.RawMessage, _ fs.FS) (any, error) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(in, &head); err != nil {
		return nil, err
	}
	switch head.Op {
	case "wilson":
		return fitWilson(in)
	case "isotonic":
		return fitIsotonic(in)
	case "platt":
		return fitPlatt(in)
	case "threshold":
		return fitThreshold(in)
	case "critical_at":
		return fitCriticalAt(in)
	case "quality":
		return fitQuality(in)
	case "quick":
		return fitQuick(in)
	case "fit":
		return fitFull(in)
	}
	return nil, fmt.Errorf("unknown calibrate_fit op %q", head.Op)
}

// wilson: {k, n} -> {lo, hi} at z = 1.959963984540054
func fitWilson(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		K  int    `json:"k"`
		N  int    `json:"n"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	lo, hi := decision.WilsonInterval(x.K, x.N, decision.WilsonZ95)
	return map[string]any{"lo": lo, "hi": hi}, nil
}

// isotonic: {samples|groups} -> {blocks, table}
func fitIsotonic(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		labelSet
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	s, err := x.labels()
	if err != nil {
		return nil, err
	}
	if len(s) == 0 {
		return nil, errors.New("isotonic needs at least one label")
	}
	blocks := decision.FitIsotonic(s)
	return map[string]any{"blocks": blocks, "table": decision.IsotonicTable(blocks)}, nil
}

// platt: {samples|groups} -> {platt, table, warnings}
func fitPlatt(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		labelSet
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	s, err := x.labels()
	if err != nil {
		return nil, err
	}
	if len(s) == 0 {
		return nil, errors.New("platt needs at least one label")
	}
	p, warnings := decision.FitPlatt(s)
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{"platt": p, "table": p.Table(), "warnings": warnings}, nil
}

// threshold: {samples|groups, objective, min_precision?} -> {result: null | {threshold, tp, fp, tn,
// fn, precision, recall, f1}}
func fitThreshold(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		labelSet
		Objective    string   `json:"objective"`
		MinPrecision *float64 `json:"min_precision,omitempty"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	s, err := x.labels()
	if err != nil {
		return nil, err
	}
	var c decision.Confusion
	var ok bool
	switch x.Objective {
	case decision.ObjectiveMaxF1:
		floor := 0.0
		if x.MinPrecision != nil {
			floor = *x.MinPrecision
		}
		c, ok = decision.MaxF1Threshold(s, floor)
	case decision.ObjectiveMinThresholdAtPrecision:
		target := decision.QuickDefaultTargetPrecision
		if x.MinPrecision != nil {
			target = *x.MinPrecision
		}
		c, ok = decision.MinThresholdAtPrecision(s, target)
	default:
		return nil, fmt.Errorf("unknown objective %q", x.Objective)
	}
	if !ok {
		return map[string]any{"result": nil}, nil
	}
	return map[string]any{"result": map[string]any{
		"threshold": c.Threshold, "tp": c.TP, "fp": c.FP, "tn": c.TN, "fn": c.FN,
		"precision": c.Precision(), "recall": c.Recall(), "f1": c.F1(),
	}}, nil
}

// critical_at: {samples|groups, min_predicted_pos?, min_precision_lb?} -> {critical_at,
// critical_null_reason}
func fitCriticalAt(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		labelSet
		MinPredictedPos *int     `json:"min_predicted_pos,omitempty"`
		MinPrecisionLB  *float64 `json:"min_precision_lb,omitempty"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	s, err := x.labels()
	if err != nil {
		return nil, err
	}
	minPos, minLB := decision.CriticalMinPredictedPos, decision.CriticalMinPrecisionLB
	if x.MinPredictedPos != nil {
		minPos = *x.MinPredictedPos
	}
	if x.MinPrecisionLB != nil {
		minLB = *x.MinPrecisionLB
	}
	c := decision.WilsonCriticalAt(s, minPos, minLB)
	if c == nil {
		return map[string]any{"critical_at": nil, "critical_null_reason": decision.CriticalNullReason}, nil
	}
	return map[string]any{"critical_at": c, "critical_null_reason": nil}, nil
}

// quality: {snapshot, label_source, n_pos, n_neg, family?} -> {quality}
func fitQuality(in json.RawMessage) (any, error) {
	var x struct {
		Op          string                     `json:"op"`
		Snapshot    string                     `json:"snapshot"`
		LabelSource decision.LabelSourceCounts `json:"label_source"`
		NPos        int                        `json:"n_pos"`
		NNeg        int                        `json:"n_neg"`
		Family      bool                       `json:"family,omitempty"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	return map[string]any{"quality": decision.DeriveQuality(x.Snapshot, x.LabelSource, x.NPos, x.NNeg, x.Family)}, nil
}

// quick: QuickCalibrationRequest -> QuickCalibration without message (UI copy is not pinned)
func fitQuick(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		decision.QuickCalibrationRequest
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	q, err := decision.QuickCalibrate(&x.QuickCalibrationRequest)
	if err != nil {
		return nil, err
	}
	out, err := asObject(q)
	if err != nil {
		return nil, err
	}
	delete(out, "message")
	return out, nil
}

// fit: {samples|groups, method?} -> Fit
func fitFull(in json.RawMessage) (any, error) {
	var x struct {
		Op string `json:"op"`
		labelSet
		Method string `json:"method,omitempty"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	s, err := x.labels()
	if err != nil {
		return nil, err
	}
	return decision.FitCalibration(s, decision.FitOptions{Method: x.Method})
}

func asObject(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(b, &out)
	return out, err
}
