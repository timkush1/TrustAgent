package gate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
)

type scenario struct {
	ID       string            `json:"id"`
	Sets     []string          `json:"sets"`
	Headers  map[string]string `json:"headers"`
	Request  json.RawMessage   `json:"request"`
	Response string            `json:"response"`
	Expect   struct {
		Mode           string   `json:"mode"`
		Action         string   `json:"action"`
		WouldAction    string   `json:"would_action"`
		Status         string   `json:"status"`
		Verdict        string   `json:"verdict"`
		MaxRisk        *float64 `json:"max_risk"`
		Reasons        []string `json:"reasons"`
		CheckedAgainst string   `json:"checked_against"`
		DeepAudit      string   `json:"deep_audit"`
		Audit          string   `json:"audit"`
	} `json:"expect"`
}

func loadScenarios(t *testing.T) []scenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	return pack.Scenarios
}

func registry(t *testing.T) *evaluators.Registry {
	t.Helper()
	r, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func repoPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, err := policy.LoadFile(filepath.Join("..", "..", "..", "config", "policies.yaml"), policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// countingJev counts calls to the wrapped client.
type countingJev struct {
	jev.Client
	calls atomic.Int32
}

func (c *countingJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	c.calls.Add(1)
	return c.Client.Decide(ctx, req, opt)
}

func strictMock(t *testing.T, latency time.Duration) *countingJev {
	t.Helper()
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true, Latency: latency}})
	if err != nil {
		t.Fatal(err)
	}
	return &countingJev{Client: cl}
}

// seq returns a Rand that yields the given values in turn, then 0.5.
func seq(vals ...float64) func() float64 {
	i := 0
	return func() float64 {
		if i < len(vals) {
			i++
			return vals[i-1]
		}
		return 0.5
	}
}

func newGate(t *testing.T, cl jev.Client, rnd func() float64) *Gate {
	t.Helper()
	g, err := New(Config{Jev: cl, Evaluators: registry(t), Budget: decision.DefaultBudget(), UniformAuditsPerDay: 50, Rand: rnd,
		Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// exchange builds the exchange of a chat request body on the route its headers match.
func exchange(t *testing.T, p *policy.Policy, headers map[string]string, body []byte, response, scenario string) *Exchange {
	t.Helper()
	req, err := llmapi.ParseChatRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	route := p.Match(&policy.Request{Path: "/v1/chat/completions", Model: req.Model, Headers: h, BodyRoute: req.Extension.Route})
	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(req, &settings)
	if err != nil {
		t.Fatal(err)
	}
	trace := &TraceStart{
		TraceID: "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90", CreatedAt: time.Now(), Source: SourceTrafficgen,
		APIFormat: llmapi.FormatOpenAIChat, Path: "/v1/chat/completions", Route: route.Name, PolicyVersion: p.Version,
		Mode: route.Mode, Model: req.Model, Scenario: scenario, AnswerSource: AnswerScripted, Flags: []string{FlagTestMode},
		UserRequest: ev.UserRequest,
	}
	return &Exchange{Trace: trace, Policy: p, Route: route, Request: req, Evidence: ev, Response: response}
}

func TestS0Scenarios(t *testing.T) {
	p := repoPolicy(t)
	seen := 0
	for _, sc := range loadScenarios(t) {
		if !slices.Contains(sc.Sets, "s0") {
			continue
		}
		seen++
		t.Run(sc.ID, func(t *testing.T) {
			cl := strictMock(t, 0)
			g := newGate(t, cl, seq())
			x := exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID)
			r := g.Evaluate(context.Background(), x)
			d, exp := r.Decision, sc.Expect

			if d.EffectiveMode != exp.Mode || d.Action != exp.Action || d.WouldAction != exp.WouldAction || r.Status != exp.Status {
				t.Errorf("mode/action/would/status %s/%s/%s/%s, want %s/%s/%s/%s",
					d.EffectiveMode, d.Action, d.WouldAction, r.Status, exp.Mode, exp.Action, exp.WouldAction, exp.Status)
			}
			if d.Verdict == nil || *d.Verdict != exp.Verdict {
				t.Errorf("verdict %v, want %s", d.Verdict, exp.Verdict)
			}
			switch {
			case exp.MaxRisk == nil && d.MaxRisk != nil,
				exp.MaxRisk != nil && (d.MaxRisk == nil || decision.FormatRisk(*d.MaxRisk) != decision.FormatRisk(*exp.MaxRisk)):
				t.Errorf("max_risk %v, want %v", d.MaxRisk, exp.MaxRisk)
			}
			if !slices.Equal(d.Reasons, exp.Reasons) {
				t.Errorf("reasons %v, want %v", d.Reasons, exp.Reasons)
			}
			if r.CheckedAgainst != exp.CheckedAgainst || d.DeepAudit != exp.DeepAudit {
				t.Errorf("checked %q deep_audit %s, want %q %s", r.CheckedAgainst, d.DeepAudit, exp.CheckedAgainst, exp.DeepAudit)
			}
			if r.RouteLabel != "support-bot@v1" || d.Route != "support-bot" || *d.PolicyVersion != 1 || d.Mode != decision.ModeFlag {
				t.Errorf("route %s decision route %s v%d mode %s", r.RouteLabel, d.Route, *d.PolicyVersion, d.Mode)
			}
			// The mock sink reports queued for a selected audit.
			audit := r.Audit.Status
			if r.Audit.Selection != "" {
				audit = AuditQueued
			}
			if audit != exp.Audit {
				t.Errorf("audit %s (plan %+v), want %s", audit, r.Audit, exp.Audit)
			}
			if r.Audit.MockScenario != sc.ID {
				t.Errorf("mock scenario %q", r.Audit.MockScenario)
			}
			wantCalls := int32(1)
			if exp.Status == decision.StatusNotAssessed {
				wantCalls = 0
				if d.DecisionModel != nil || slices.Contains(r.Flags, FlagMockDecision) {
					t.Errorf("nothing is sent without evidence: %+v %v", d.DecisionModel, r.Flags)
				}
			} else {
				if d.DecisionModel == nil || *d.DecisionModel.Snapshot != jev.DefaultMockModel || d.DecisionModel.Calls != 1 ||
					d.DecisionModel.CostSource != jev.CostSimulated || !slices.Contains(r.Flags, FlagMockDecision) {
					t.Errorf("decision model %+v flags %v", d.DecisionModel, r.Flags)
				}
				if d.Compile == nil || d.Compile.Requests != 1 || !slices.Equal(d.Compile.PackedEvidenceIDs, []string{"refund-policy.md"}) {
					t.Errorf("compile %+v", d.Compile)
				}
			}
			if got := cl.calls.Load(); got != wantCalls {
				t.Errorf("decision-model calls %d, want %d", got, wantCalls)
			}
			if !r.Body.Store || r.Body.InclusionProb != 1 {
				t.Errorf("support-bot stores every body: %+v", r.Body)
			}
			contractstest.Validate(t, "decision.v1.schema.json", d)
			if testing.Verbose() {
				b, _ := json.MarshalIndent(d, "", " ")
				t.Logf("decision:\n%s", b)
			}
		})
	}
	if seen != 3 {
		t.Fatalf("ran %d s0 scenarios, want 3", seen)
	}
}

func TestRefundScenarioDetectorsAndEvidence(t *testing.T) {
	p := repoPolicy(t)
	var sc scenario
	for _, s := range loadScenarios(t) {
		if s.ID == "refund-contradiction-flag" {
			sc = s
		}
	}
	r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID))
	var numbers decision.DetectorResult
	for _, d := range r.Decision.Detectors {
		if d.ID == decision.DetNumbers {
			numbers = d
		}
	}
	if !numbers.Fired || len(numbers.Findings) != 1 || numbers.Findings[0] != (decision.DetectorFinding{Text: "60", Start: 33, End: 35}) {
		t.Errorf("numbers detector %+v", numbers)
	}
	if len(r.Decision.Detectors) != len(decision.DetectorIDs) {
		t.Errorf("every detector is recorded: %d", len(r.Decision.Detectors))
	}
	if r.UserRequest != "Can I still get a refund on my annual plan? I bought it 45 days ago." || len(r.Evidence) != 1 || r.Evidence[0].ID != "refund-policy.md" {
		t.Errorf("exchange texts %q %+v", r.UserRequest, r.Evidence)
	}
	g := r.Decision.Evaluators
	if len(g) != 2 || g[0].Ref != "groundedness@1" || *g[0].Level != decision.LevelCritical || *g[1].Corroborated != true {
		t.Errorf("evaluators %+v", g)
	}
	sum := r.Decision.EvidenceSummary
	if sum == nil || sum.CheckedAgainst() != "request_system:1" || sum.Knowledge != evidence.KnowledgeNotRequested {
		t.Errorf("evidence summary %+v", sum)
	}
	if r.Audit.Selection != SelectionHigh || r.Audit.Lane != LaneHigh || r.Audit.InclusionProb != 1 {
		t.Errorf("audit %+v", r.Audit)
	}
}

const defaultRouteBody = `{"model":"gpt-4o-mini","messages":[
  {"role":"system","content":"<document id=\"sla\">Priority tickets get a first response within 4 hours.</document>"},
  {"role":"user","content":"How fast do you answer priority tickets?"}]}`

func TestObserveDefaultRoute(t *testing.T) {
	p := repoPolicy(t)
	x := exchange(t, p, nil, []byte(defaultRouteBody), "We answer priority tickets within 4 hours.", "")
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	// Draws: uniform (no), pass sample (no), body sample (no).
	r := newGate(t, cl, seq(0.99, 0.99, 0.99)).Evaluate(context.Background(), x)
	d := r.Decision
	if x.Route.Name != policy.DefaultRouteName || d.Action != decision.ActionObserve || d.EffectiveMode != decision.ModeObserve || d.Enforced {
		t.Errorf("default route decision %s/%s enforced %v", d.Action, d.EffectiveMode, d.Enforced)
	}
	if len(d.Evaluators) != 4 || d.DecisionModel == nil || d.DecisionModel.Calls != 1 {
		t.Errorf("gateway-default evaluators %d, calls %+v", len(d.Evaluators), d.DecisionModel)
	}
	if r.RouteLabel != "default@v1" {
		t.Errorf("route label %s", r.RouteLabel)
	}
	if d.DeepAudit == decision.DeepAuditSample && r.Audit.Status != AuditSampledOut {
		t.Errorf("a 2%% pass sample with a 0.99 draw is sampled out: %+v", r.Audit)
	}
	if d.WouldAction == decision.ActionPass && (r.Body.Store || r.Body.InclusionProb != 0.2) {
		t.Errorf("a pass with a 0.99 draw keeps no body: %+v", r.Body)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestUniformDrawAndDailyCap(t *testing.T) {
	p := repoPolicy(t)
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	g, err := New(Config{Jev: cl, Evaluators: registry(t), UniformAuditsPerDay: 1, Rand: func() float64 { return 0 },
		Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	x := exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	r := g.Evaluate(context.Background(), x)
	if r.Audit.Selection != SelectionUniform || !r.Audit.UniformSample || r.Audit.Verifier != "llm" || r.Audit.InclusionProb != 0.01 ||
		!slices.Contains(r.Flags, FlagUniformSample) {
		t.Errorf("uniform draw %+v flags %v", r.Audit, r.Flags)
	}
	if r = g.Evaluate(context.Background(), x); r.Audit.Selection == SelectionUniform {
		t.Error("the daily cap of 1 is spent")
	}
	c := &dailyCounter{limit: 1}
	day := time.Date(2026, 9, 28, 23, 59, 0, 0, time.UTC)
	if !c.take(day) || c.take(day) || !c.take(day.Add(2*time.Minute)) {
		t.Error("the cap restarts at UTC midnight")
	}
	if (&dailyCounter{}).take(day) {
		t.Error("a zero cap allows nothing")
	}
}

func TestSampledOutObserve(t *testing.T) {
	doc := []byte("version: 1\ndefaults:\n  sample_rate: 0.5\n")
	p, err := policy.Parse(doc, policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	cl := strictMock(t, 0)
	x := exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	r := newGate(t, cl, seq(0.99, 0.7)).Evaluate(context.Background(), x)
	if r.Status != "skipped:sampled_out" || r.Decision.Degraded != nil || cl.calls.Load() != 0 {
		t.Errorf("status %s degraded %v calls %d", r.Status, r.Decision.Degraded, cl.calls.Load())
	}
	if !r.Decision.Detectors[0].Ran {
		t.Error("detectors always run, sampled or not")
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
}

func TestOffBackendSkips(t *testing.T) {
	p := repoPolicy(t)
	cl, _ := jev.New(jev.Config{})
	x := exchange(t, p, map[string]string{"X-TrustAgent-Route": "support-bot"}, []byte(defaultRouteBody), "Within 4 hours.", "")
	r := newGate(t, cl, seq()).Evaluate(context.Background(), x)
	d := r.Decision
	if r.Status != "skipped:decision_model_off" || d.Action != decision.ActionPass || d.Degraded == nil || *d.Degraded != decision.DegradedOff {
		t.Errorf("status %s action %s degraded %v", r.Status, d.Action, d.Degraded)
	}
	if d.DecisionModel != nil || d.Compile == nil {
		t.Errorf("off: nothing called but compile recorded: %+v %+v", d.DecisionModel, d.Compile)
	}
	// deep_audit sample_error; support-bot's error_sample_rate 0.1 with a 0.5 draw is sampled out.
	if d.DeepAudit != decision.DeepAuditSampleError || r.Audit.Status != AuditSampledOut {
		t.Errorf("deep audit %s plan %+v", d.DeepAudit, r.Audit)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestStrictMissIsErrorStatus(t *testing.T) {
	p := repoPolicy(t)
	x := exchange(t, p, map[string]string{"X-TrustAgent-Route": "support-bot"}, []byte(defaultRouteBody), "Within 4 hours.", "unknown-scenario")
	r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	d := r.Decision
	if r.Status != "error:mock_fixture_missing" || d.Status != decision.StatusError || d.Action != decision.ActionPass {
		t.Errorf("status %s/%s action %s", r.Status, d.Status, d.Action)
	}
	if d.DecisionModel == nil || d.DecisionModel.Snapshot != nil || d.DecisionModel.Calls != 1 {
		t.Errorf("a failed call is still recorded: %+v", d.DecisionModel)
	}
	for _, e := range d.Evaluators {
		if e.Status != decision.EvalError || *e.Reason != "error:mock_fixture_missing" {
			t.Errorf("evaluator %s: %s %v", e.Ref, e.Status, e.Reason)
		}
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestDeadlineIsTimeout(t *testing.T) {
	doc := []byte("version: 1\ndefaults:\n  mode: flag\n  deadline_ms: 100\n")
	p, err := policy.Parse(doc, policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash, Latency: 2 * time.Second}})
	start := time.Now()
	r := newGate(t, cl, seq()).Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	if el := time.Since(start); el > time.Second {
		t.Errorf("the gate waited %v past a 100 ms deadline", el)
	}
	d := r.Decision
	if r.Status != decision.StatusTimeout || d.Action != decision.ActionPass || d.Degraded == nil || *d.Degraded != decision.DegradedTimeout {
		t.Errorf("status %s action %s degraded %v", r.Status, d.Action, d.Degraded)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestNoTextSkips(t *testing.T) {
	p := repoPolicy(t)
	for _, skip := range []string{SkipUpstreamError, SkipNoText} {
		x := exchange(t, p, map[string]string{"X-TrustAgent-Route": "support-bot"}, []byte(defaultRouteBody), "", "")
		x.SkipReason = skip
		r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
		d := r.Decision
		if r.Status != "skipped:"+skip || d.Compile != nil || d.Detectors[0].Ran || r.Audit.Status != AuditNotAssessed {
			t.Errorf("%s: status %s compile %+v audit %+v", skip, r.Status, d.Compile, r.Audit)
		}
		contractstest.Validate(t, "decision.v1.schema.json", d)
	}
	x := &Exchange{
		Trace:  &TraceStart{TraceID: "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90", Mode: decision.ModeObserve, Route: "default"},
		Policy: p, Route: p.Default, SkipReason: SkipUnparseableRequest,
	}
	r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	if r.Status != "skipped:unparseable_request" || r.Decision.EvidenceSummary != nil || r.CheckedAgainst != "none" || r.Decision.Action != decision.ActionObserve {
		t.Errorf("unparseable: %s %+v %s", r.Status, r.Decision.EvidenceSummary, r.CheckedAgainst)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
}

func TestCompileFailureIsErrorStatus(t *testing.T) {
	p := repoPolicy(t)
	route := *p.Match(&policy.Request{Headers: http.Header{"X-Trustagent-Route": {"support-bot"}}})
	// The loader rejects unknown refs; a registry that lost one makes compile() fail at runtime.
	route.Evaluators = append(slices.Clone(route.Evaluators), policy.EvaluatorRef{Ref: "retired@1"})
	x := exchange(t, p, map[string]string{"X-TrustAgent-Route": "support-bot"}, []byte(defaultRouteBody), "Within 4 hours.", "")
	x.Route = &route
	cl := strictMock(t, 0)
	r := newGate(t, cl, seq()).Evaluate(context.Background(), x)
	d := r.Decision
	if r.Status != "error:compile" || d.Status != decision.StatusError || d.Action != decision.ActionPass || cl.calls.Load() != 0 {
		t.Errorf("status %s/%s action %s calls %d", r.Status, d.Status, d.Action, cl.calls.Load())
	}
	if d.Compile != nil || d.DeepAudit != decision.DeepAuditSampleError || len(d.Evaluators) != 2 {
		t.Errorf("compile %+v deep %s evaluators %d", d.Compile, d.DeepAudit, len(d.Evaluators))
	}
	for _, e := range d.Evaluators {
		if e.Status != decision.EvalError || *e.Reason != "error:compile" {
			t.Errorf("%s: %s %v", e.Ref, e.Status, e.Reason)
		}
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestResolveRulesAndClientRules(t *testing.T) {
	doc := []byte(`version: 1
overrides: {client_rules: true}
defaults:
  evaluators:
    - {ref: commitments@1, params: {allowed_commitments: "Refunds within 30 days."}}
    - {ref: off_brand_tone@1}
    - {ref: refusal@1, enabled: false}
`)
	p, err := policy.Parse(doc, policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	g := newGate(t, strictMock(t, 0), seq())
	got := g.resolveRules(p.Default, true, map[string]string{"brand_voice": "Terse.", "unknown": "x"})
	want := map[string]string{"allowed_commitments": "Refunds within 30 days.", "brand_voice": "Terse."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules %v, want %v", got, want)
	}
	if got := g.resolveRules(p.Default, false, map[string]string{"brand_voice": "Terse."}); got["brand_voice"] == "Terse." {
		t.Error("client rules are ignored without overrides.client_rules")
	}
}

func TestNopSink(t *testing.T) {
	var s Sink = NopSink{}
	s.TraceStarted(context.Background(), &TraceStart{})
	if got := s.GateDecided(context.Background(), &Result{Audit: AuditPlan{Selection: SelectionHigh}}); got != AuditDisabled {
		t.Errorf("selected audit without an engine: %s", got)
	}
	if got := s.GateDecided(context.Background(), &Result{Audit: AuditPlan{Status: AuditNotAssessed}}); got != AuditNotAssessed {
		t.Errorf("planned status passes through: %s", got)
	}
}

func TestMessages(t *testing.T) {
	got := Messages([]llmapi.Message{{Role: "user", Parts: []string{"a", "b"}}, {Role: "function", Name: "f", Parts: []string{"x"}}})
	want := []ChatMessage{{Role: "user", Content: "a\nb"}, {Role: "function", Content: "x", Name: "f"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("messages %+v", got)
	}
}
