package studio

import (
	"errors"

	"github.com/truthtable/backend-go/internal/decision"
)

// Reasons of a Studio result that decide() does not give.
const (
	// ReasonOverBudget: the run's spend cap stopped before this sample.
	ReasonOverBudget = "over_budget"
	// ReasonNotRun: the evaluator was not part of the decision (a bug guard).
	ReasonNotRun = "not_run"
)

// Result is one evaluator's outcome on one sample as the Studio reports it (api.ts
// StudioTestResponse.results[] without the sample's bookkeeping).
type Result struct {
	Status        string
	Reason        *string
	Answer        *decision.RawAnswer
	Probabilities map[string]float64
	RiskRaw       *float64
	Risk          *float64
	Level         *string
	Escalate      *string
	// Fires is whether the evaluator reached warn or critical; nil when it was not assessed.
	Fires *bool
}

// ResultOf extracts evaluator ref's result from a sample's outcome. A stage failure is an error
// result with reason error:compile (error:too_large for a request too large to send) or
// error:internal; a skipped sample is not assessed with reason over_budget.
func ResultOf(o *Outcome, ref string) Result {
	switch {
	case o.Skipped:
		return Result{Status: decision.EvalNotAssessed, Reason: ptr(ReasonOverBudget)}
	case o.Err != nil:
		reason := "error:internal"
		if o.Stage == StageCompile {
			reason = "error:compile"
			if errors.Is(o.Err, decision.ErrRequestTooLarge) {
				reason = "error:too_large"
			}
		}
		return Result{Status: decision.EvalError, Reason: &reason}
	}
	e, ok := o.Decision.Evaluators.Get(ref)
	if !ok {
		return Result{Status: decision.EvalNotAssessed, Reason: ptr(ReasonNotRun)}
	}
	r := Result{
		Status: e.Status, Reason: e.Reason, Answer: e.Answer, Probabilities: e.Probabilities,
		RiskRaw: e.RiskRaw, Risk: e.Risk, Level: e.Level, Escalate: e.Escalate,
	}
	if e.Status == decision.EvalAssessed && e.Level != nil {
		fires := *e.Level == decision.LevelWarn || *e.Level == decision.LevelCritical
		r.Fires = &fires
	}
	return r
}

func ptr[T any](v T) *T { return &v }
