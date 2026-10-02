package costs

import (
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
)

// judgeVar is a settable JudgeFunc.
type judgeVar struct {
	mu              sync.Mutex
	provider, model string
}

func (j *judgeVar) get() (string, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.provider, j.model
}

func (j *judgeVar) set(provider, model string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.provider, j.model = provider, model
}

// The published assumptions load: one audit is about 1,000 + 8 × 3,525 input tokens and
// 300 + 8 × 120 output tokens, at the Claude Haiku 4.5 reference judge.
func TestCostAssumptionsLoad(t *testing.T) {
	a, err := ContractsAssumptions()
	if err != nil {
		t.Fatal(err)
	}
	in, out := a.Settings.AuditTokens()
	if in.RatString() != "29200" || out.RatString() != "1260" {
		t.Errorf("audit tokens = %s in, %s out", in.RatString(), out.RatString())
	}
	if a.Judge.Provider != "anthropic" || a.Judge.Model != "claude-haiku-4-5" || a.DecisionModel.Provider != "openrouter" {
		t.Errorf("reference models = %+v / %+v", a.Judge, a.DecisionModel)
	}
	if _, err := parseAssumptions([]byte(`{"prices":{},"inputs":{}}`)); err == nil {
		t.Error("assumptions without reference prices parsed")
	}
	if _, err := parseAssumptions([]byte(`{"inputs":{"claims_per_answer":{"value":-1}}}`)); err == nil {
		t.Error("a negative input parsed")
	}
}

// The estimate starts from the assumptions at the judge's price: the Haiku reference ($0.0355)
// for an unknown or unpriced judge, $0 for a mock or local judge, the judge's own price
// otherwise (P4-PLAN D48).
func TestCostAuditEstimateSeed(t *testing.T) {
	book, err := LoadPriceBook("")
	if err != nil {
		t.Fatal(err)
	}
	j := &judgeVar{}
	e, err := NewAuditEstimator(book, j.get)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		provider, model string
		want            float64
	}{
		{"", "", 0.0355},
		{"anthropic", "claude-haiku-4-5-20251001", 0.0355},
		{"some-vendor", "unpriced-model", 0.0355},
		{"mock", "mock-judge", 0},
		{"ollama", "llama3.2", 0},
	}
	for _, c := range cases {
		j.set(c.provider, c.model)
		if got := e.Estimate(); !near(got, c.want) {
			t.Errorf("estimate for %s/%s = %v, want %v", c.provider, c.model, got, c.want)
		}
	}
	// A dashboard override prices the judge: $2/$10 doubles the audit.
	book.SetOverrides([]decision.PriceEntry{{Provider: "anthropic", Match: "claude-haiku-4-5*", InputPerMTok: 2, OutputPerMTok: 10,
		Label: decision.PriceLabelOverride, AsOf: "2026-10-01"}})
	j.set("anthropic", "claude-haiku-4-5")
	if got := e.Estimate(); !near(got, 0.071) {
		t.Errorf("estimate under an override = %v, want 0.071", got)
	}
	// Without a judge function the reference applies.
	plain, err := NewAuditEstimator(book, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Estimate(); !near(got, 0.071) {
		t.Errorf("estimate without a judge = %v", got)
	}
}

// Real audit costs move the estimate (an EWMA from the seed); a change of judge starts over.
func TestCostAuditEstimateLearnsAndResetsOnJudgeChange(t *testing.T) {
	book, err := LoadPriceBook("")
	if err != nil {
		t.Fatal(err)
	}
	j := &judgeVar{provider: "anthropic", model: "claude-haiku-4-5"}
	e, err := NewAuditEstimator(book, j.get)
	if err != nil {
		t.Fatal(err)
	}
	e.Observe(0.0155)
	if got := e.Estimate(); !near(got, 0.2*0.0155+0.8*0.0355) {
		t.Errorf("after one audit = %v", got)
	}
	for range 60 {
		e.Observe(0.01)
	}
	if got := e.Estimate(); got < 0.01 || got > 0.0101 {
		t.Errorf("after 60 audits at $0.01 = %v", got)
	}
	e.Observe(-5) // not a cost: counts as $0
	if got := e.Estimate(); got > 0.0081 {
		t.Errorf("a negative cost raised the estimate: %v", got)
	}
	j.set("mock", "mock-judge")
	if got := e.Estimate(); got != 0 {
		t.Errorf("a mock judge after a real one = %v, want 0", got)
	}
	j.set("anthropic", "claude-haiku-4-5")
	if got := e.Estimate(); !near(got, 0.0355) {
		t.Errorf("back to the real judge: the average did not start over: %v", got)
	}
}
