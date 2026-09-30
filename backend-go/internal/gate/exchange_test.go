package gate

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// pendingPolicy loads config/policies.pending.yaml, whose block and fallback routes the /v1
// handler enforces from P3 (payments-faq, help-center) and whose handbook route reads the
// knowledge add-on.
func pendingPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policies.pending.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res := policy.Check(b, policy.Env{Seeds: policy.DefaultSeeds(), Specs: registry(t), Enforcing: true, Knowledge: true})
	if res.Policy == nil {
		t.Fatal(res.Err())
	}
	return res.Policy
}

// scenarioByID returns a demo scenario.
func scenarioByID(t *testing.T, id string) scenario {
	t.Helper()
	for _, s := range loadScenarios(t) {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no scenario %s", id)
	return scenario{}
}

// scenarioExchange is the exchange of a demo scenario on the policy's route for its headers.
func scenarioExchange(t *testing.T, p *policy.Policy, id string) *Exchange {
	t.Helper()
	sc := scenarioByID(t, id)
	return exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID)
}

func TestExchangeStreamSetsTheDecisionStream(t *testing.T) {
	p := pendingPolicy(t)
	for _, tc := range []struct {
		stream, mode string
		downgraded   bool
	}{
		{"", decision.ModeFallback, false},
		{StreamNone, decision.ModeFallback, false},
		{StreamBuffer, decision.ModeFallback, false},
		// decide() cannot enforce a stream the client already has: fallback acts as flag.
		{StreamPassthrough, decision.ModeFlag, true},
	} {
		x := scenarioExchange(t, p, "payments-fallback")
		if x.Route.Name != "payments-faq" {
			t.Fatalf("route %s", x.Route.Name)
		}
		x.Stream = tc.stream
		d := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x).Decision
		if d.EffectiveMode != tc.mode || slices.Contains(d.Reasons, decision.ReasonStreamNotBuffered) != tc.downgraded {
			t.Errorf("stream %q: effective mode %s reasons %v", tc.stream, d.EffectiveMode, d.Reasons)
		}
		if tc.downgraded && (d.Action != decision.ActionFlag || d.WouldAction != decision.ActionBlock) {
			t.Errorf("passthrough: action %s would %s, want flag/block", d.Action, d.WouldAction)
		}
		contractstest.Validate(t, "decision.v1.schema.json", d)
	}
}

func TestAttemptOneTakesNoAuditAndNoUniformDraw(t *testing.T) {
	p := repoPolicy(t)
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	g, err := New(Config{Jev: cl, Evaluators: registry(t), UniformAuditsPerDay: 1,
		Rand: func() float64 { return 0 }, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	x := exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	x.Attempt, x.NoAudit = 1, true
	r := g.Evaluate(context.Background(), x)
	if r.Decision.Attempt != 1 || r.Audit != (AuditPlan{}) || slices.Contains(r.Flags, FlagUniformSample) {
		t.Errorf("attempt %d audit %+v flags %v", r.Decision.Attempt, r.Audit, r.Flags)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)

	// The re-gate spent no uniform draw: the next original answer still gets the day's sample.
	x = exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	if r := g.Evaluate(context.Background(), x); r.Decision.Attempt != 0 || r.Audit.Selection != SelectionUniform {
		t.Errorf("attempt %d audit %+v after the re-gate", r.Decision.Attempt, r.Audit)
	}
}

func TestUnenforcedRecordsEnforcedFalse(t *testing.T) {
	p := repoPolicy(t)
	x := scenarioExchange(t, p, "refund-contradiction-flag")
	r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	if !r.Decision.Enforced || r.Decision.Action != decision.ActionFlag {
		t.Fatalf("a flag route enforces: %s enforced %v", r.Decision.Action, r.Decision.Enforced)
	}
	x = scenarioExchange(t, p, "refund-contradiction-flag")
	x.Unenforced = true
	r = newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	d := r.Decision
	// The decision itself is unchanged: only the record says the answer went out regardless.
	if d.Enforced || d.Action != decision.ActionFlag || d.WouldAction != decision.ActionBlock || r.Audit.Selection != SelectionHigh {
		t.Errorf("unenforced: action %s would %s enforced %v audit %+v", d.Action, d.WouldAction, d.Enforced, r.Audit)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

func TestAuditSelectionForcesTheAudit(t *testing.T) {
	// A route whose deep audits are off still audits a Playground run, in the high lane.
	p := flagPolicy(t, "  deep_audit: {enabled: false}\n")
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	g := newGate(t, cl, func() float64 { return 0 })
	x := exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	if r := g.Evaluate(context.Background(), x); r.Audit.Status != AuditDisabled || r.Audit.Selection != "" {
		t.Fatalf("deep audit off: %+v", r.Audit)
	}
	x.AuditSelection = SelectionPlayground
	r := g.Evaluate(context.Background(), x)
	if r.Audit.Selection != SelectionPlayground || r.Audit.Lane != LaneHigh || r.Audit.InclusionProb != 1 || r.Audit.UniformSample ||
		slices.Contains(r.Flags, FlagUniformSample) {
		t.Errorf("playground audit %+v flags %v", r.Audit, r.Flags)
	}

	// On a route that samples uniformly, the forced selection takes no uniform draw.
	p = repoPolicy(t)
	g, err := New(Config{Jev: cl, Evaluators: registry(t), UniformAuditsPerDay: 1,
		Rand: func() float64 { return 0 }, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	x = exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	x.AuditSelection = SelectionPlayground
	if r := g.Evaluate(context.Background(), x); r.Audit.Selection != SelectionPlayground || r.Audit.UniformSample {
		t.Errorf("uniform route: %+v", r.Audit)
	}
	x.AuditSelection = ""
	if r := g.Evaluate(context.Background(), x); r.Audit.Selection != SelectionUniform {
		t.Errorf("the day's uniform sample is still there: %+v", r.Audit)
	}

	// Nothing to audit without an answer; the re-gate plans no audit at all.
	x = exchange(t, p, nil, []byte(defaultRouteBody), "", "")
	x.SkipReason, x.AuditSelection = SkipNoText, SelectionPlayground
	if r := g.Evaluate(context.Background(), x); r.Audit.Selection != "" || r.Audit.Status != AuditNotAssessed {
		t.Errorf("no text: %+v", r.Audit)
	}
	x = exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	x.AuditSelection, x.NoAudit = SelectionPlayground, true
	if r := g.Evaluate(context.Background(), x); r.Audit != (AuditPlan{}) {
		t.Errorf("NoAudit wins: %+v", r.Audit)
	}
}

func TestKnowledgeOnlyCapsGrounding(t *testing.T) {
	p := repoPolicy(t)
	x := scenarioExchange(t, p, "refund-contradiction-flag")
	r := newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	g, _ := r.Decision.Evaluators.Get("groundedness@1")
	if g == nil || *g.Level != decision.LevelCritical || g.Capped != nil {
		t.Fatalf("groundedness %+v", g)
	}

	x = scenarioExchange(t, p, "refund-contradiction-flag")
	x.KnowledgeOnly = true
	r = newGate(t, strictMock(t, 0), seq()).Evaluate(context.Background(), x)
	g, _ = r.Decision.Evaluators.Get("groundedness@1")
	if g == nil || *g.Level != decision.LevelWarn || g.Capped == nil || *g.Capped != decision.CappedKnowledgeOnly {
		t.Errorf("knowledge-only groundedness %+v", g)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
}

func TestPlaygroundRunIsNeverSampledOut(t *testing.T) {
	// D27: an observe route that samples half its traffic still checks every Playground run.
	doc := []byte("version: 1\ndefaults:\n  sample_rate: 0.5\n")
	p, err := policy.Parse(doc, policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	cl := strictMock(t, 0)
	x := exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	x.AuditSelection = SelectionPlayground
	r := newGate(t, &countingJev{Client: jevHash(t)}, seq(0.99, 0.99)).Evaluate(context.Background(), x)
	if r.Status == "skipped:sampled_out" || r.Decision.SkipReason != nil {
		t.Errorf("a Playground run was sampled out: status %s", r.Status)
	}
	if r.Audit.Selection != SelectionPlayground {
		t.Errorf("audit %+v", r.Audit)
	}
	// The same draw samples an ordinary request out.
	x = exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", "")
	if r := newGate(t, cl, seq(0.99, 0.99)).Evaluate(context.Background(), x); r.Status != "skipped:sampled_out" {
		t.Errorf("an ordinary request: status %s", r.Status)
	}
}

// jevHash is the mock decision model answering every question from a hash of the request.
func jevHash(t *testing.T) jev.Client {
	t.Helper()
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}
