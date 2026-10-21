package decision

import (
	"math"
	"testing"
)

// Most normalize behaviour is pinned by contracts/vectors/normalize; these cover inputs JSON cannot
// carry (NaN, infinities) and the result shape.
func TestNormalizeNonFinite(t *testing.T) {
	specs := loadTemplates(t)
	nan := math.NaN()
	if n := Normalize(&RawAnswer{Type: QuestionNoul, Noul: &nan}, specs["groundedness@1"]); n.Error != ErrMissingAnswer {
		t.Errorf("NaN noul: %+v", n)
	}
	for _, p := range []float64{math.NaN(), math.Inf(1)} {
		a := &RawAnswer{Type: QuestionChoice, Probabilities: map[string]float64{"answers_request": 0.5, "declines_or_deflects": p}}
		if n := Normalize(a, specs["refusal@1"]); n.Error != ErrInvalidDistribution {
			t.Errorf("probability %v: %+v", p, n)
		}
	}
}

func TestNormalizeChoiceResult(t *testing.T) {
	specs := loadTemplates(t)
	conf := 0.7
	n := Normalize(&RawAnswer{
		Type:          QuestionChoice,
		Choice:        "declines_or_deflects",
		Confidence:    &conf,
		Probabilities: map[string]float64{"declines_or_deflects": 0.7, "answers_request": 0.3},
	}, specs["refusal@1"])
	if n.Error != "" || n.Top != "declines_or_deflects" || math.Abs(n.RiskRaw-0.7) > EPS ||
		n.ConfidenceSource != ConfidenceVendor || *n.VendorConfidence != 0.7 || len(n.Probabilities) != 4 || n.Anomalies != nil {
		t.Errorf("Normalize = %+v", n)
	}
	if n := Normalize(nil, specs["refusal@1"]); n.Error != ErrMissingAnswer {
		t.Errorf("nil answer: %+v", n)
	}
}
