package decision

import (
	"fmt"
	"slices"
)

// Calibration methods (calibration.v1 method; FitOptions.Method also takes auto).
const (
	MethodAuto     = "auto"
	MethodIsotonic = "isotonic"
	MethodPlatt    = "platt"
)

// Method selection (SPEC §2.11): isotonic needs at least IsotonicMinLabels labels, and auto then
// picks the method with the lower CVFolds-fold cross-validated Brier score.
const (
	IsotonicMinLabels = 500
	CVFolds           = 5
)

// Fit warnings.
const (
	WarnIsotonicFewLabels = "isotonic regression with fewer than 500 labels tends to overfit; auto would use Platt scaling"
	WarnNoWarnAt          = "no threshold reaches precision 0.8, so there is no recommended warn_at"
	WarnWarnAtCapped      = "the max-F1 warn_at was above critical_at, so warn_at is lowered to critical_at (warn_at must not exceed critical_at)"
)

// FitOptions configure Fit.
type FitOptions struct {
	// Method is auto (the default), isotonic or platt.
	Method string
}

// FitMetrics are calibration.v1 metrics, measured in-sample on the fitted labels. AUROC is the raw
// risk's (a monotone mapping cannot improve it). Every field is nil when it is undefined.
type FitMetrics struct {
	ECEBefore   *float64 `json:"ece_before"`
	ECEAfter    *float64 `json:"ece_after"`
	BrierBefore *float64 `json:"brier_before"`
	BrierAfter  *float64 `json:"brier_after"`
	AUROC       *float64 `json:"auroc"`
}

// WarnAt is recommended.warn_at: the threshold with its precision and recall.
type WarnAt struct {
	Value     float64 `json:"value"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// Recommended is calibration.v1 recommended: thresholds in calibrated space.
type Recommended struct {
	Space              string      `json:"space"`
	WarnAt             *WarnAt     `json:"warn_at"`
	CriticalAt         *CriticalAt `json:"critical_at"`
	CriticalNullReason *string     `json:"critical_null_reason"`
}

// CVBrier are the cross-validated Brier scores auto compared.
type CVBrier struct {
	Isotonic float64 `json:"isotonic"`
	Platt    float64 `json:"platt"`
}

// Fit is a fitted calibration: the calibration.v1 entry fields that depend only on the labels.
type Fit struct {
	Method      string           `json:"method"`
	Table       []float64        `json:"table"`
	Platt       *PlattParams     `json:"platt"`
	N           int              `json:"n"`
	NPos        int              `json:"n_pos"`
	NNeg        int              `json:"n_neg"`
	Metrics     FitMetrics       `json:"metrics"`
	Reliability []ReliabilityBin `json:"reliability"`
	Recommended Recommended      `json:"recommended"`
	// CVBrier is set when auto compared the methods by cross-validation.
	CVBrier  *CVBrier `json:"cv_brier"`
	Warnings []string `json:"warnings"`
}

// FitCalibration fits a calibration to labelled raw risks (SPEC §2.11):
//
//   - Method: isotonic (PAV) or Platt; auto uses Platt below 500 labels and otherwise the method with
//     the lower 5-fold cross-validated Brier score (isotonic on a tie).
//   - The 101-point table is authoritative (R6-rounded); the calibrated risk of every label is the
//     table applied with ApplyCalibration, exactly as decide() will apply it.
//   - Recommended thresholds are in calibrated space: warn_at maximizes F1 with precision >= 0.8,
//     critical_at is the Wilson-bound threshold of WilsonCriticalAt (null with "insufficient
//     evidence to block" otherwise). A warn_at above critical_at is lowered to it (the SPEC §2.8
//     hard rule warn_at <= critical_at), with a warning.
//   - Metrics and the reliability table are in-sample: ECE and Brier before (raw risk as a
//     probability) and after (calibrated), AUROC of the raw risk.
//
// It needs at least 8 labels with 3 of each class (ErrNotEnoughLabels). Quality is not part of the
// fit: it depends on where the labels came from (DeriveQuality).
func FitCalibration(samples []LabeledScore, opt FitOptions) (*Fit, error) {
	if err := checkSamples(samples); err != nil {
		return nil, err
	}
	pos, neg := countLabels(samples)
	if !enoughLabels(pos, neg) {
		return nil, fmt.Errorf("%w: have %d (%d should fire, %d should not), need %d with at least %d of each",
			ErrNotEnoughLabels, pos+neg, pos, neg, MinFitLabels, MinFitPerClass)
	}
	method := opt.Method
	if method == "" {
		method = MethodAuto
	}
	fit := &Fit{N: pos + neg, NPos: pos, NNeg: neg, Warnings: []string{}}
	switch method {
	case MethodAuto:
		method = MethodPlatt
		if fit.N >= IsotonicMinLabels {
			cv := &CVBrier{Isotonic: cvBrier(samples, MethodIsotonic), Platt: cvBrier(samples, MethodPlatt)}
			fit.CVBrier = cv
			if cv.Isotonic <= cv.Platt+EPS {
				method = MethodIsotonic
			}
		}
	case MethodIsotonic:
		if fit.N < IsotonicMinLabels {
			fit.Warnings = append(fit.Warnings, WarnIsotonicFewLabels)
		}
	case MethodPlatt:
	default:
		return nil, fmt.Errorf("calibrate: unknown method %q (want auto, isotonic or platt)", opt.Method)
	}
	fit.Method = method
	if method == MethodIsotonic {
		fit.Table = IsotonicTable(FitIsotonic(samples))
	} else {
		p, warnings := FitPlatt(samples)
		fit.Platt = &p
		fit.Table = p.Table()
		fit.Warnings = append(fit.Warnings, warnings...)
	}

	raw := make([]float64, len(samples))
	cal := make([]float64, len(samples))
	calibrated := make([]LabeledScore, len(samples))
	for i, s := range samples {
		raw[i] = s.Risk
		cal[i] = ApplyCalibration(fit.Table, s.Risk)
		calibrated[i] = LabeledScore{Risk: cal[i], Fire: s.Fire}
	}
	_, fit.Metrics.ECEBefore = Reliability(samples, raw)
	fit.Reliability, fit.Metrics.ECEAfter = Reliability(samples, cal)
	fit.Metrics.BrierBefore = Brier(samples, raw)
	fit.Metrics.BrierAfter = Brier(samples, cal)
	fit.Metrics.AUROC = AUROC(samples)

	fit.Recommended = Recommended{Space: SpaceCalibrated}
	if c, ok := MaxF1Threshold(calibrated, WarnMinPrecision); ok {
		fit.Recommended.WarnAt = &WarnAt{Value: c.Threshold, Precision: c.Precision(), Recall: c.Recall()}
	} else {
		fit.Warnings = append(fit.Warnings, WarnNoWarnAt)
	}
	fit.Recommended.CriticalAt = WilsonCriticalAt(calibrated, CriticalMinPredictedPos, CriticalMinPrecisionLB)
	if fit.Recommended.CriticalAt == nil {
		reason := CriticalNullReason
		fit.Recommended.CriticalNullReason = &reason
	}
	// SPEC §2.8 hard rule warn_at <= critical_at: a threshold safe enough to block is safe enough
	// to warn, so a higher max-F1 warn_at comes down to critical_at.
	if w, c := fit.Recommended.WarnAt, fit.Recommended.CriticalAt; w != nil && c != nil && w.Value > c.Value {
		at := ConfusionAt(calibrated, c.Value)
		fit.Recommended.WarnAt = &WarnAt{Value: c.Value, Precision: at.Precision(), Recall: at.Recall()}
		fit.Warnings = append(fit.Warnings, WarnWarnAtCapped)
	}
	return fit, nil
}

// cvBrier is the CVFolds-fold cross-validated Brier score of a method. Folds are assigned after a
// stable sort by (risk, label): the j-th label in that order goes to fold j mod CVFolds, which
// spreads every risk range over all folds. Held-out labels are scored through the fold's table;
// squared errors are summed in that same order.
func cvBrier(samples []LabeledScore, method string) float64 {
	order := slices.Clone(samples)
	slices.SortStableFunc(order, func(a, b LabeledScore) int {
		switch {
		case a.Risk < b.Risk:
			return -1
		case a.Risk > b.Risk:
			return 1
		case a.Fire == b.Fire:
			return 0
		case !a.Fire:
			return -1
		}
		return 1
	})
	sum := 0.0
	for k := 0; k < CVFolds; k++ {
		var train, test []LabeledScore
		for j, s := range order {
			if j%CVFolds == k {
				test = append(test, s)
			} else {
				train = append(train, s)
			}
		}
		if len(test) == 0 {
			continue
		}
		var table []float64
		if method == MethodIsotonic {
			table = IsotonicTable(FitIsotonic(train))
		} else {
			p, _ := FitPlatt(train)
			table = p.Table()
		}
		for _, s := range test {
			d := ApplyCalibration(table, s.Risk)
			if s.Fire {
				d = 1 - d
			}
			sum += d * d
		}
	}
	return sum / float64(len(samples))
}
