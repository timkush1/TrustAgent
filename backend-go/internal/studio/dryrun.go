package studio

import (
	"cmp"
	"math"
	"slices"
	"sync"

	"github.com/truthtable/backend-go/internal/decision"
)

// MaxDryRunExamples caps DryRunResult.examples.
const MaxDryRunExamples = 10

// Deep-audit verdicts a dry-run compares with (store.VerdictHallucinated, VerdictGrounded).
const (
	auditHallucinated = "HALLUCINATED"
	auditGrounded     = "GROUNDED"
)

// DryRunTrace is what a dry-run knows about a sampled trace besides the new evaluator's result.
type DryRunTrace struct {
	TraceID string
	// InclusionProb is the probability its body was kept (trace_bodies.inclusion_prob, in (0,1]).
	InclusionProb float64
	// Levels are the recorded levels of the evaluators the trace was gated with (nil: not
	// assessed).
	Levels map[string]*string
	// AuditVerdict is the trace's deep-audit verdict, nil without one.
	AuditVerdict *string
}

// Overlap is DryRunResult.overlap[]: how often a recorded evaluator fired on a sample where the
// tested one fired.
type Overlap struct {
	EvaluatorRef string `json:"evaluator_ref"`
	BothFired    int    `json:"both_fired"`
}

// AuditAgreement is DryRunResult.agreement_with_audit: of the sampled answers the tested evaluator
// assessed and a deep audit judged GROUNDED or HALLUCINATED, how many it agreed with (fired on a
// hallucination, stayed quiet on a grounded answer).
type AuditAgreement struct {
	N     int `json:"n"`
	Agree int `json:"agree"`
}

// Example is DryRunResult.examples[]: a sampled trace the tested evaluator fired on.
type Example struct {
	TraceID string  `json:"trace_id"`
	Risk    float64 `json:"risk"`
	Level   string  `json:"level"`
}

// DryRunStats are the statistics of DryRunResult.
type DryRunStats struct {
	Sampled     int `json:"sampled"`
	Assessed    int `json:"assessed"`
	NotAssessed int `json:"not_assessed"`
	Errors      int `json:"errors"`
	Fired       int `json:"fired"`
	// FireRate is fired / (assessed + not_assessed): the share of the sampled answers it would
	// have fired on (an evaluator that cannot assess an answer never fires on it); errors are
	// left out. FireRateCI95 is its Wilson 95% interval.
	FireRate     float64    `json:"fire_rate"`
	FireRateCI95 [2]float64 `json:"fire_rate_ci95"`
	// ExtrapolatedFires is an estimate: population times the fire rate weighted by 1/inclusion
	// probability (a Hajek estimate), so answers whose bodies were kept less often count more.
	ExtrapolatedFires  int             `json:"extrapolated_fires"`
	Overlap            []Overlap       `json:"overlap"`
	AgreementWithAudit *AuditAgreement `json:"agreement_with_audit"`
	Examples           []Example       `json:"examples"`
	CostUSD            float64         `json:"cost_usd"`
	Snapshot           *string         `json:"snapshot"`
}

// DryRunTally accumulates a dry-run's samples. It is safe for concurrent use.
type DryRunTally struct {
	ref string

	mu              sync.Mutex
	s               DryRunStats
	weightJudged    float64
	weightFired     float64
	overlap         map[string]int
	agreeN, agreeOK int
}

// NewDryRunTally tallies the results of the evaluator ref (the ref the runner compiled).
func NewDryRunTally(ref string) *DryRunTally {
	return &DryRunTally{ref: ref, overlap: map[string]int{}}
}

// Add counts one sample: the trace and the runner's outcome on it. It returns the sample's result.
func (t *DryRunTally) Add(tr DryRunTrace, o *Outcome) Result {
	res := ResultOf(o, t.ref)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.s
	s.Sampled++
	s.CostUSD += o.CostUSD
	if o.Usage != nil && o.Usage.Snapshot != nil {
		snap := *o.Usage.Snapshot
		s.Snapshot = &snap
	}
	w := 1.0
	if tr.InclusionProb > 0 && tr.InclusionProb < 1 {
		w = 1 / tr.InclusionProb
	}
	switch res.Status {
	case decision.EvalError:
		s.Errors++
		return res
	case decision.EvalAssessed:
		s.Assessed++
	default:
		s.NotAssessed++
	}
	t.weightJudged += w
	fired := res.Fires != nil && *res.Fires
	for ref, level := range tr.Levels {
		if _, seen := t.overlap[ref]; !seen {
			t.overlap[ref] = 0
		}
		if fired && level != nil && (*level == decision.LevelWarn || *level == decision.LevelCritical) {
			t.overlap[ref]++
		}
	}
	if res.Status == decision.EvalAssessed && tr.AuditVerdict != nil {
		switch *tr.AuditVerdict {
		case auditHallucinated:
			t.agreeN++
			if fired {
				t.agreeOK++
			}
		case auditGrounded:
			t.agreeN++
			if !fired {
				t.agreeOK++
			}
		}
	}
	if fired {
		s.Fired++
		t.weightFired += w
		risk := res.Risk
		if risk == nil {
			risk = res.RiskRaw
		}
		ex := Example{TraceID: tr.TraceID, Level: *res.Level}
		if risk != nil {
			ex.Risk = *risk
		}
		s.Examples = append(s.Examples, ex)
	}
	return res
}

// Progress is the running count: samples evaluated, fired, and the cost so far.
func (t *DryRunTally) Progress() (evaluated, fired int, costUSD float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.Sampled, t.s.Fired, t.s.CostUSD
}

// Stats returns the statistics, extrapolated to population traces.
func (t *DryRunTally) Stats(population int64) DryRunStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.s
	judged := s.Assessed + s.NotAssessed
	if judged > 0 {
		s.FireRate = float64(s.Fired) / float64(judged)
	}
	lo, hi := decision.WilsonInterval(s.Fired, judged, decision.WilsonZ95)
	if judged == 0 {
		lo, hi = 0, 0
	}
	s.FireRateCI95 = [2]float64{lo, hi}
	if t.weightJudged > 0 {
		s.ExtrapolatedFires = int(math.Round(float64(population) * t.weightFired / t.weightJudged))
	}
	s.Overlap = make([]Overlap, 0, len(t.overlap))
	for ref, n := range t.overlap {
		if ref != t.ref {
			s.Overlap = append(s.Overlap, Overlap{EvaluatorRef: ref, BothFired: n})
		}
	}
	slices.SortFunc(s.Overlap, func(a, b Overlap) int {
		return cmp.Or(cmp.Compare(b.BothFired, a.BothFired), cmp.Compare(a.EvaluatorRef, b.EvaluatorRef))
	})
	if t.agreeN > 0 {
		s.AgreementWithAudit = &AuditAgreement{N: t.agreeN, Agree: t.agreeOK}
	}
	s.Examples = slices.Clone(s.Examples)
	slices.SortStableFunc(s.Examples, func(a, b Example) int {
		return cmp.Or(cmp.Compare(b.Risk, a.Risk), cmp.Compare(a.TraceID, b.TraceID))
	})
	if len(s.Examples) > MaxDryRunExamples {
		s.Examples = s.Examples[:MaxDryRunExamples]
	}
	if s.Examples == nil {
		s.Examples = []Example{}
	}
	return s
}
