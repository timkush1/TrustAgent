package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/studio"
)

// Skip reasons of an output line.
const (
	skippedOverBudget = "over_budget"
)

// evaluator runs the gate pipeline of SPEC §3.1 on examples (internal/studio.Runner: detectors,
// compile, the decision-model call, and decide(), which normalizes the answers) and writes each
// outcome as an output line.
type evaluator struct {
	*studio.Runner
}

// compileInfo is what compile() produced for an example.
type compileInfo struct {
	Requests          int      `json:"requests"`
	EstTokens         []int    `json:"est_tokens"`
	CPT               float64  `json:"cpt"`
	QuestionSetSHA256 string   `json:"question_set_sha256"`
	RequestSHA256     []string `json:"request_sha256"`
	// StateFields are the state members sent (the calibration fit_context composition).
	StateFields  []string `json:"state_fields"`
	PackedIDs    []string `json:"packed_ids"`
	TruncatedIDs []string `json:"truncated_ids"`
	Truncated    bool     `json:"truncated"`
}

// usage is decision/v1 DecisionModelUsage for an example's calls.
type usage = studio.Usage

// result is one output line.
type result struct {
	ID string `json:"id"`
	// Line is the 1-based input line.
	Line int `json:"line"`
	// Error is set when the example could not be evaluated (invalid input, compile failure).
	Error *string `json:"error"`
	// Skipped is over_budget when the cost cap stopped the run before this example.
	Skipped *string `json:"skipped"`
	// Simulated is true with the mock backend: the probabilities are not model output.
	Simulated     bool                      `json:"simulated"`
	Route         string                    `json:"route"`
	Mode          string                    `json:"mode"`
	Decision      *decision.Core            `json:"decision"`
	Detectors     []decision.DetectorResult `json:"detectors"`
	Excluded      []decision.Exclusion      `json:"excluded"`
	Compile       *compileInfo              `json:"compile"`
	DecisionModel *usage                    `json:"decision_model"`
	// CostUSD is the example's decision-model cost (0 when nothing was sent).
	CostUSD float64 `json:"cost_usd"`
	// LatencyMs is the wall time of the example's pipeline, calls included.
	LatencyMs int64 `json:"latency_ms"`
	// Passed through from the input.
	Labels            json.RawMessage `json:"labels,omitempty"`
	LabelHallucinated *bool           `json:"label_hallucinated,omitempty"`
	LabelSource       string          `json:"label_source,omitempty"`
	Domain            string          `json:"domain,omitempty"`
	Meta              json.RawMessage `json:"meta,omitempty"`
}

func (e *evaluator) newResult(line int, ex *example) *result {
	r := &result{
		Line: line, Route: e.Route.Name, Mode: e.Mode, Simulated: e.Client.Info().Backend == jev.BackendMock,
		Detectors: []decision.DetectorResult{}, Excluded: []decision.Exclusion{},
	}
	if ex != nil {
		l := ex.line
		r.ID, r.Labels, r.LabelHallucinated, r.LabelSource, r.Domain, r.Meta = l.ID, l.Labels, l.LabelHallucinated, l.LabelSource, l.Domain, l.Meta
	}
	return r
}

func fail(r *result, format string, args ...any) *result {
	msg := fmt.Sprintf(format, args...)
	r.Error = &msg
	return r
}

// evaluate gates one example. It never returns nil; problems are recorded on the line.
func (e *evaluator) evaluate(ctx context.Context, line int, ex *example) *result {
	r := e.newResult(line, ex)
	o := e.Evaluate(ctx, &studio.Sample{
		ID: ex.line.ID, UserRequest: ex.UserRequest, Response: ex.Response, SystemInstructions: ex.SystemInstructions,
		Evidence: ex.Evidence, Rules: ex.line.Rules, MaxMarker: ex.line.MaxMarker, Scenario: ex.line.Scenario,
	})
	r.LatencyMs = o.LatencyMs
	if o.Skipped {
		s := skippedOverBudget
		r.Skipped = &s
		return r
	}
	r.Detectors, r.Excluded = o.Detectors, o.Excluded
	if c := o.Compiled; c != nil {
		r.Compile = &compileInfo{
			Requests: len(c.Requests), EstTokens: nonNilInts(c.EstTokens), CPT: c.CPT,
			QuestionSetSHA256: c.QuestionSetSHA256, RequestSHA256: nonNilStrings(c.RequestSHA256),
			StateFields: studio.StateFields(c), PackedIDs: nonNilStrings(c.PackedIDs),
			TruncatedIDs: nonNilStrings(c.TruncatedIDs), Truncated: c.Truncated,
		}
	}
	r.DecisionModel, r.CostUSD, r.Decision = o.Usage, o.CostUSD, o.Decision
	if o.Err != nil {
		return fail(r, "%v", o.Err)
	}
	return r
}

func nonNilInts(xs []int) []int {
	if xs == nil {
		return []int{}
	}
	return xs
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// errNoDecisionModel is returned when the resolved backend is off.
var errNoDecisionModel = errors.New("the decision model is off")
