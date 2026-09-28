package decision

import (
	"math"
	"slices"
	"strconv"
)

// RawAnswer is one decision-model answer as received (common.v1 RawAnswer). It is deliberately
// lenient: Normalize owns range and consistency checks.
type RawAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`
}

// Normalized is the result of Normalize. When Error is set, the other fields are zero.
type Normalized struct {
	// Error is "" or one of missing_answer, unexpected_option, invalid_distribution,
	// probabilities_missing.
	Error string
	// Probabilities are the renormalized option (choice) or level (score) probabilities, with
	// every option present. nil for noul.
	Probabilities map[string]float64
	// RiskRaw is the answer's risk in [0,1] before calibration (high = risky).
	RiskRaw float64
	// Top is the most likely option or level ("" for noul).
	Top string
	// VendorConfidence is the answer's own confidence, for display only.
	VendorConfidence *float64
	// ConfidenceSource says where the displayed confidence comes from ("" for noul without a
	// vendor confidence).
	ConfidenceSource string
	// Anomalies are non-fatal observations (sum_out_of_range), for metrics.
	Anomalies []string
}

// Normalize turns a raw answer into a risk signal (SPEC §2.9 normalize). A nil answer is
// missing_answer. The fractional score and the answer's type field are never used.
func Normalize(a *RawAnswer, s *Spec) Normalized {
	if a == nil {
		return Normalized{Error: ErrMissingAnswer}
	}
	switch s.Question.Type {
	case QuestionNoul:
		return normalizeNoul(a, s)
	case QuestionChoice:
		return normalizeChoice(a, s)
	case QuestionScore:
		return normalizeScore(a, s)
	}
	return Normalized{Error: ErrMissingAnswer}
}

func normalizeNoul(a *RawAnswer, s *Spec) Normalized {
	if a.Noul == nil || math.IsNaN(*a.Noul) {
		return Normalized{Error: ErrMissingAnswer}
	}
	p := clamp01(*a.Noul)
	n := Normalized{RiskRaw: p, VendorConfidence: a.Confidence}
	if s.Signal.Invert {
		n.RiskRaw = 1 - p
	}
	if a.Confidence != nil {
		n.ConfidenceSource = ConfidenceVendor
	}
	return n
}

func normalizeChoice(a *RawAnswer, s *Spec) Normalized {
	options := s.Options()
	probs := a.Probabilities
	source := ""
	if probs == nil {
		if a.Choice == "" {
			return Normalized{Error: ErrMissingAnswer}
		}
		// A choice-only answer; a choice outside the options fails as unexpected_option below.
		probs = map[string]float64{a.Choice: 1}
		source = ConfidenceChoiceOnly
	}
	n := distribution(probs, options, a)
	if n.Error != "" {
		return n
	}
	if source != "" {
		n.ConfidenceSource = source
	}
	n.RiskRaw = weightedRisk(n.Probabilities, s.Signal.RiskWeights)
	return n
}

func normalizeScore(a *RawAnswer, s *Spec) Normalized {
	levels := s.Options()
	if !hasAnyKey(a.Probabilities, levels) {
		return Normalized{Error: ErrProbabilitiesMissing}
	}
	n := distribution(a.Probabilities, levels, a)
	if n.Error != "" {
		return n
	}
	var risk float64
	for _, l := range sortedLevels(s.Signal.RiskLevels) {
		risk += n.Probabilities[strconv.Itoa(l)]
	}
	n.RiskRaw = clamp01(risk)
	return n
}

// distribution applies the shared choice/score rules: foreign keys, missing options, clipping,
// the sum check, renormalization and the top option.
func distribution(probs map[string]float64, options []string, a *RawAnswer) Normalized {
	isOption := make(map[string]bool, len(options))
	for _, o := range options {
		isOption[o] = true
	}
	for k, p := range probs {
		if math.IsNaN(p) || math.IsInf(p, 0) {
			return Normalized{Error: ErrInvalidDistribution}
		}
		if !isOption[k] && p > 0.01+EPS {
			return Normalized{Error: ErrUnexpectedOption}
		}
	}
	out := make(map[string]float64, len(options))
	var sum float64
	for _, o := range options { // options are in sorted key order
		p := math.Max(0, probs[o])
		out[o] = p
		sum += p
	}
	tol := math.Max(0.10, 0.005*float64(len(options)))
	if sum == 0 || math.Abs(sum-1) > tol+EPS {
		return Normalized{Error: ErrInvalidDistribution}
	}
	n := Normalized{Probabilities: out, VendorConfidence: a.Confidence}
	for _, o := range options {
		out[o] /= sum
	}
	if math.Abs(sum-1) > 0.02+EPS {
		n.Anomalies = []string{AnomalySumOutOfRange}
	}
	n.Top = topOption(out, options, a.Choice)
	if a.Confidence != nil {
		n.ConfidenceSource = ConfidenceVendor
	} else {
		n.ConfidenceSource = ConfidenceFallbackTopProb
	}
	return n
}

// topOption is the argmax; on a tie (within EPS) the vendor's choice wins if it is tied,
// otherwise the smallest key (options are sorted, so the first tied one).
func topOption(probs map[string]float64, options []string, vendorChoice string) string {
	best := math.Inf(-1)
	for _, o := range options {
		best = math.Max(best, probs[o])
	}
	first := ""
	for _, o := range options {
		if probs[o] >= best-EPS {
			if o == vendorChoice {
				return o
			}
			if first == "" {
				first = o
			}
		}
	}
	return first
}

// weightedRisk is clamp(sum over sorted weight keys of w_o * P(o)).
func weightedRisk(probs map[string]float64, weights map[string]float64) float64 {
	keys := make([]string, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	SortUTF16(keys)
	var risk float64
	for _, k := range keys {
		risk += weights[k] * probs[k]
	}
	return clamp01(risk)
}

// sortedLevels returns the distinct risk levels in ascending order.
func sortedLevels(levels []int) []int {
	out := slices.Clone(levels)
	slices.Sort(out)
	return slices.Compact(out)
}

func hasAnyKey(m map[string]float64, keys []string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}
