package gate

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/calibration"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// mockCalibration is the committed config/calibration as the zero-key demo loads it.
func mockCalibration(t *testing.T, reg *evaluators.Registry) *calibration.Store {
	t.Helper()
	s := calibration.New(calibration.Config{
		Dir: filepath.Join("..", "..", "..", "config", "calibration"), AllowMock: true,
		Specs: func() policy.Resolver { return reg }, Logger: slog.New(slog.DiscardHandler),
	})
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	return s
}

func calibratedGate(t *testing.T, cl jev.Client, cal policy.CalibrationLookup) *Gate {
	t.Helper()
	g, err := New(Config{Jev: cl, Evaluators: registry(t), Budget: decision.DefaultBudget(), UniformAuditsPerDay: 50, Rand: seq(),
		Calibrations: cal, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestP3ScenariosWithTheMockCalibration runs the gate side of the P3 block and fallback scenarios
// on config/policies.yaml (payments-faq, help-center) with the demo's mock calibration.
func TestP3ScenariosWithTheMockCalibration(t *testing.T) {
	p := repoPolicy(t)
	cal := mockCalibration(t, registry(t))
	for _, id := range []string{"payments-fallback", "help-center-link-block", "help-center-uncalibrated-flag"} {
		t.Run(id, func(t *testing.T) {
			sc := scenarioByID(t, id)
			r := calibratedGate(t, strictMock(t, 0).Client, cal).Evaluate(context.Background(),
				exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID))
			d, exp := r.Decision, sc.Expect
			if d.EffectiveMode != exp.Mode || d.Action != exp.Action || d.WouldAction != exp.WouldAction || r.Status != exp.Status {
				t.Errorf("mode/action/would/status %s/%s/%s/%s, want %s/%s/%s/%s",
					d.EffectiveMode, d.Action, d.WouldAction, r.Status, exp.Mode, exp.Action, exp.WouldAction, exp.Status)
			}
			if d.Verdict == nil || *d.Verdict != exp.Verdict || d.DeepAudit != exp.DeepAudit || r.CheckedAgainst != exp.CheckedAgainst {
				t.Errorf("verdict %v deep_audit %s checked %s", d.Verdict, d.DeepAudit, r.CheckedAgainst)
			}
			for _, reason := range exp.Reasons {
				if !slices.Contains(d.Reasons, reason) {
					t.Errorf("reasons %v, missing %s", d.Reasons, reason)
				}
			}
			contractstest.Validate(t, "decision.v1.schema.json", d)
		})
	}
}

func TestPaymentsFallbackNeedsTheCalibration(t *testing.T) {
	p := repoPolicy(t)
	reg := registry(t)
	sc := scenarioByID(t, "payments-fallback")

	// With the mock calibration (the mock backend counts it): fallback, both checks calibrated.
	r := calibratedGate(t, strictMock(t, 0).Client, mockCalibration(t, reg)).Evaluate(context.Background(),
		exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID))
	d := r.Decision
	if d.Action != decision.ActionFallback || d.Calibration != decision.CalibrationAll || d.Degraded != nil ||
		slices.Contains(d.Reasons, decision.ReasonUncalibratedSnapshot) {
		t.Errorf("calibrated: action %s calibration %s degraded %v reasons %v", d.Action, d.Calibration, d.Degraded, d.Reasons)
	}
	for _, ref := range []string{"groundedness@1", "contradiction@1"} {
		e, _ := d.Evaluators.Get(ref)
		if e == nil || !e.Calibrated || e.CalibrationState != policy.CalibrationMock || e.ThresholdSpace != decision.SpaceCalibrated {
			t.Errorf("%s: %+v", ref, e)
		}
	}

	// Without it: capped to flag, uncalibrated_snapshot.
	for name, cal := range map[string]policy.CalibrationLookup{"nil": nil, "none": policy.NoCalibration{}} {
		r := calibratedGate(t, strictMock(t, 0).Client, cal).Evaluate(context.Background(),
			exchange(t, p, sc.Headers, sc.Request, sc.Response, sc.ID))
		d := r.Decision
		if d.Action != decision.ActionFlag || d.WouldAction != decision.ActionBlock || !slices.Contains(d.Reasons, decision.ReasonUncalibratedSnapshot) ||
			d.Degraded == nil || *d.Degraded != decision.DegradedUncalibrated || d.Calibration != decision.CalibrationNone {
			t.Errorf("%s: action %s reasons %v degraded %v calibration %s", name, d.Action, d.Reasons, d.Degraded, d.Calibration)
		}
		contractstest.Validate(t, "decision.v1.schema.json", d)
	}

	// The fallback answer's re-gate (attempt 1): it passes, and plans no audit.
	x := exchange(t, p, sc.Headers, sc.Request, "Approved refunds go back to your original payment method within 5 to 10 business days.", sc.ID)
	x.Attempt, x.NoAudit = 1, true
	r = calibratedGate(t, strictMock(t, 0).Client, mockCalibration(t, reg)).Evaluate(context.Background(), x)
	d = r.Decision
	if d.Attempt != 1 || d.Action != decision.ActionPass || d.WouldAction != decision.ActionPass || *d.Verdict != "supported" ||
		decision.FormatRisk(*d.MaxRisk) != decision.FormatRisk(0.04) || len(d.Reasons) != 0 || r.Audit != (AuditPlan{}) {
		t.Errorf("re-gate: attempt %d action %s would %s verdict %v risk %v reasons %v audit %+v",
			d.Attempt, d.Action, d.WouldAction, d.Verdict, d.MaxRisk, d.Reasons, r.Audit)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
}

// snapshotJev answers as the wrapped client but reports another snapshot.
type snapshotJev struct {
	jev.Client
	snapshot string
}

func (s snapshotJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	resp, err := s.Client.Decide(ctx, req, opt)
	if resp != nil {
		resp.Snapshot = s.snapshot
	}
	return resp, err
}

// lookups records the lookups made and answers each with a calibrated identity table.
type lookups struct {
	mu   sync.Mutex
	seen []string
	args map[string]map[string]string
}

func (l *lookups) Lookup(ref string, params map[string]string, snapshot string) policy.CalibrationStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, ref+" "+snapshot)
	if l.args == nil {
		l.args = map[string]map[string]string{}
	}
	l.args[ref] = params
	table := make([]float64, 101)
	for i := range table {
		table[i] = float64(i) / 100
	}
	return policy.CalibrationStatus{State: policy.CalibrationMeasured, Quality: policy.CalibrationMeasured, Table: table}
}

func TestCalibrationIsLookedUpForTheSnapshotThatAnswered(t *testing.T) {
	doc := []byte(`version: 1
defaults:
  mode: flag
  evaluators:
    - {ref: groundedness@1, thresholds: {space: calibrated}}
    - {ref: off_brand_tone@1, params: {brand_voice: "Formal."}}
    - {ref: refusal@1, enabled: false}
`)
	p, err := policy.Parse(doc, policy.DefaultSeeds(), registry(t))
	if err != nil {
		t.Fatal(err)
	}
	cl, _ := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	l := &lookups{}
	g := calibratedGate(t, snapshotJev{Client: cl, snapshot: "typesafe/jev-1.13-20260917"}, l)
	r := g.Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	slices.Sort(l.seen)
	if want := []string{"groundedness@1 typesafe/jev-1.13-20260917", "off_brand_tone@1 typesafe/jev-1.13-20260917"}; !slices.Equal(l.seen, want) {
		t.Errorf("lookups %v, want %v", l.seen, want)
	}
	if l.args["off_brand_tone@1"]["brand_voice"] != "Formal." || l.args["groundedness@1"] != nil {
		t.Errorf("params %v", l.args)
	}
	g1, _ := r.Decision.Evaluators.Get("groundedness@1")
	tone, _ := r.Decision.Evaluators.Get("off_brand_tone@1")
	// Calibrated threshold space and a measured calibration: calibrated. Raw space: the state is
	// shown, the risk stays raw.
	if !g1.Calibrated || g1.CalibrationState != policy.CalibrationMeasured || tone.Calibrated || tone.CalibrationState != policy.CalibrationMeasured {
		t.Errorf("groundedness %+v tone %+v", g1, tone)
	}
	if r.Decision.Calibration != decision.CalibrationPartial {
		t.Errorf("calibration %s", r.Decision.Calibration)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)

	// Nothing answered, nothing looked up: a skipped exchange and a refused call.
	l = &lookups{}
	g = calibratedGate(t, cl, l)
	x := exchange(t, p, nil, []byte(defaultRouteBody), "", "")
	x.SkipReason = SkipNoText
	g.Evaluate(context.Background(), x)
	g = calibratedGate(t, &scriptedJev{Client: cl, err: &jev.Error{Kind: jev.KindBreakerOpen}}, l)
	g.Evaluate(context.Background(), exchange(t, p, nil, []byte(defaultRouteBody), "Within 4 hours.", ""))
	if len(l.seen) != 0 {
		t.Errorf("lookups without an answer: %v", l.seen)
	}
}
