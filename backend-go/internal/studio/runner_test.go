package studio

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

const refundDoc = "Refund policy: annual plans can be refunded within 30 days of purchase. Monthly plans are not refundable."

func testRoute() *policy.Route {
	r := &policy.Route{Name: "support-bot"}
	r.Mode, r.Fail, r.OnNoEvidence = decision.ModeObserve, "open", decision.OnNoEvidenceIgnore
	r.Evidence = policy.EvidenceSettings{Sources: evidence.DefaultSources, MaxTokens: 8000}
	r.Decision.MaxResponseTokens = 6000
	r.Evaluators = []policy.EvaluatorRef{{Ref: "groundedness@1"}, {Ref: "contradiction@1"}}
	return r
}

func testRunner(t *testing.T, client jev.Client) *Runner {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	route := testRoute()
	b := decision.DefaultBudget()
	b.MaxResponseTokens, b.EvidenceMaxTokens = route.Decision.MaxResponseTokens, route.Evidence.MaxTokens
	return &Runner{
		Client: client, Route: route, Mode: route.Mode, Model: client.Info().Model, Specs: reg.Specs(), Budget: b,
		PricePerMTok: 10, Call: jev.CallOptions{Class: jev.ClassAsync, Component: jev.ComponentStudioTest},
	}
}

func refundSample(id, response string) *Sample {
	return &Sample{
		ID: id, UserRequest: "How long is the refund window?", Response: response,
		Evidence: []decision.EvidenceItem{{ID: "doc-1", Source: evidence.SourceRequestContext, Text: refundDoc}},
	}
}

func TestHashMockIsDeterministic(t *testing.T) {
	newMock := func() jev.Client {
		c, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash, Seed: 42}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	s := refundSample("a", "Annual plans can be refunded within 60 days.")
	first := testRunner(t, newMock()).Evaluate(context.Background(), s)
	second := testRunner(t, newMock()).Evaluate(context.Background(), s)
	if first.Err != nil || first.Decision == nil {
		t.Fatalf("outcome = %+v", first)
	}
	a, _ := json.Marshal(first.Decision)
	b, _ := json.Marshal(second.Decision)
	if string(a) != string(b) {
		t.Fatalf("the hash mock answered differently:\n%s\n%s", a, b)
	}
	g, ok := first.Decision.Evaluators.Get("groundedness@1")
	if !ok || g.Status != decision.EvalAssessed || g.RiskRaw == nil {
		t.Fatalf("groundedness = %+v", g)
	}
	if first.Usage == nil || first.Usage.Backend != jev.BackendMock || first.Usage.CostSource != jev.CostSimulated {
		t.Errorf("usage = %+v", first.Usage)
	}
	if first.Compiled == nil || len(first.Compiled.RequestSHA256) != 1 || first.Compiled.RequestSHA256[0] != second.Compiled.RequestSHA256[0] {
		t.Errorf("compile differs: %+v", first.Compiled)
	}
	if first.RequestStatus["0"] != decision.RequestOK {
		t.Errorf("request status = %v", first.RequestStatus)
	}
}

// recorder answers every question with a fixed noul and records the call options.
type recorder struct {
	mu    sync.Mutex
	opts  []jev.CallOptions
	fail  error
	cost  float64
	model string
}

func (r *recorder) Info() jev.Info {
	return jev.Info{Backend: jev.BackendOpenRouterDecisions, Model: "typesafe/jev-1.13"}
}

func (r *recorder) Decide(_ context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	r.mu.Lock()
	r.opts = append(r.opts, opt)
	r.model = req.Model
	r.mu.Unlock()
	if r.fail != nil {
		return nil, r.fail
	}
	answers := map[string]*decision.RawAnswer{}
	for qid := range req.Questions {
		p := 0.9
		answers[qid] = &decision.RawAnswer{Noul: &p}
	}
	cost := r.cost
	return &jev.Response{Answers: answers, CostUSD: &cost, CostSource: jev.CostReported, Snapshot: "jev-1.13.0", Attempts: 1}, nil
}

func TestCallsCarryTheTemplateOptions(t *testing.T) {
	rec := &recorder{cost: 0.0001}
	r := testRunner(t, rec)
	r.Call.Enforcing, r.Call.KeyLabel = false, "admin"
	s := refundSample("a", "Annual plans can be refunded within 30 days.")
	s.Scenario = "grounded-pass"
	o := r.Evaluate(context.Background(), s)
	if o.Err != nil || o.Decision == nil {
		t.Fatalf("outcome = %+v", o)
	}
	if len(rec.opts) != 1 {
		t.Fatalf("calls = %d", len(rec.opts))
	}
	got := rec.opts[0]
	if got.Enforcing || got.Component != jev.ComponentStudioTest || got.KeyLabel != "admin" || got.Scenario != "grounded-pass" || got.Class != jev.ClassAsync {
		t.Errorf("options = %+v", got)
	}
	if o.CostUSD != 0.0001 || o.Usage.CostSource != jev.CostReported || *o.Usage.Snapshot != "jev-1.13.0" {
		t.Errorf("cost %v usage %+v", o.CostUSD, o.Usage)
	}
	if rec.model != "typesafe/jev-1.13" {
		t.Errorf("model = %q", rec.model)
	}
}

func TestFailedCallIsChargedAtTheEstimateAndReported(t *testing.T) {
	rec := &recorder{fail: &jev.Error{Kind: jev.KindBudget}}
	r := testRunner(t, rec)
	o := r.Evaluate(context.Background(), refundSample("a", "Annual plans can be refunded within 30 days."))
	if o.Err != nil || o.Decision == nil {
		t.Fatalf("outcome = %+v", o)
	}
	if !o.Failed(jev.KindBudget) || o.Failed(jev.KindTimeout) {
		t.Errorf("request status = %v", o.RequestStatus)
	}
	want := r.EstimateTokensUSD(o.Compiled)
	if o.CostUSD != want || want <= 0 {
		t.Errorf("cost = %v, want the estimate %v", o.CostUSD, want)
	}
}

func TestSpendCapSkipsAndStops(t *testing.T) {
	rec := &recorder{cost: 0.0001}
	r := testRunner(t, rec)
	r.Spend = NewSpend(1e-12)
	o := r.Evaluate(context.Background(), refundSample("a", "Annual plans can be refunded within 30 days."))
	if !o.Skipped || o.Decision != nil || o.Compiled != nil || len(o.Detectors) != 0 {
		t.Fatalf("outcome = %+v", o)
	}
	if !r.Spend.Stopped() || len(rec.opts) != 0 {
		t.Errorf("stopped %v, calls %d", r.Spend.Stopped(), len(rec.opts))
	}
	if o := r.Evaluate(context.Background(), refundSample("b", "x")); !o.Skipped {
		t.Error("a stopped run evaluated another sample")
	}
}

func TestOffBackendSkipsTheCall(t *testing.T) {
	off, err := jev.New(jev.Config{Backend: jev.BackendOff})
	if err != nil {
		t.Fatal(err)
	}
	r := testRunner(t, off)
	r.Model = "unset"
	o := r.Evaluate(context.Background(), refundSample("a", "Annual plans can be refunded within 30 days."))
	if o.Err != nil || o.Decision == nil || o.Usage != nil {
		t.Fatalf("outcome = %+v", o)
	}
	if o.Decision.SkipReason == nil || *o.Decision.SkipReason != "decision_model_off" {
		t.Errorf("skip reason = %v", o.Decision.SkipReason)
	}
}

func TestCompileErrorKeepsTheDetectors(t *testing.T) {
	r := testRunner(t, &recorder{})
	r.Model = "" // compile refuses an unset model
	o := r.Evaluate(context.Background(), refundSample("a", "Annual plans can be refunded within 30 days."))
	if o.Err == nil || o.Decision != nil || o.Compiled != nil {
		t.Fatalf("outcome = %+v", o)
	}
	if got := o.Err.Error(); len(got) < 8 || got[:8] != "compile:" {
		t.Errorf("error = %q", got)
	}
	if len(o.Detectors) == 0 {
		t.Error("the detectors' results were dropped")
	}
	if errors.Unwrap(o.Err) == nil {
		t.Error("the stage error does not wrap its cause")
	}
}

func TestResolveRulesAndStateFields(t *testing.T) {
	r := testRunner(t, &recorder{})
	if got := r.ResolveRules(nil); len(got) != 0 {
		t.Errorf("rules without params = %v", got)
	}
	o := r.Evaluate(context.Background(), refundSample("a", "Annual plans can be refunded within 30 days."))
	fields := StateFields(o.Compiled)
	if len(fields) == 0 || fields[0] > fields[len(fields)-1] {
		t.Errorf("state fields = %v", fields)
	}
	if got := StateFields(&decision.Compiled{}); len(got) != 0 {
		t.Errorf("no requests: %v", got)
	}
}
