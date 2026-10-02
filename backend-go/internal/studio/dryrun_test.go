package studio

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
)

const tested = "my_check@4"

// outcome is a sample where the tested evaluator ended with status and level.
func outcome(status, level string, risk float64) *Outcome {
	e := decision.EvaluatorResult{Ref: tested, Status: status}
	if level != "" {
		e.Level, e.RiskRaw = ptr(level), ptr(risk)
	}
	return &Outcome{
		Decision: &decision.Core{Evaluators: decision.EvaluatorResults{e}}, CostUSD: 0.001,
		Usage: &Usage{Snapshot: ptr("jev-1.13.0")},
	}
}

func TestResultOf(t *testing.T) {
	r := ResultOf(outcome(decision.EvalAssessed, decision.LevelCritical, 0.9), tested)
	if r.Status != decision.EvalAssessed || r.Fires == nil || !*r.Fires || *r.RiskRaw != 0.9 {
		t.Errorf("critical: %+v", r)
	}
	r = ResultOf(outcome(decision.EvalAssessed, decision.LevelOK, 0.1), tested)
	if r.Fires == nil || *r.Fires {
		t.Errorf("ok: %+v", r)
	}
	r = ResultOf(outcome(decision.EvalNotAssessed, "", 0), tested)
	if r.Fires != nil {
		t.Errorf("not assessed fires: %+v", r)
	}
	if r := ResultOf(outcome(decision.EvalAssessed, decision.LevelOK, 0.1), "other@1"); r.Status != decision.EvalNotAssessed || *r.Reason != ReasonNotRun {
		t.Errorf("missing ref: %+v", r)
	}
	if r := ResultOf(&Outcome{Skipped: true}, tested); r.Status != decision.EvalNotAssessed || *r.Reason != ReasonOverBudget {
		t.Errorf("skipped: %+v", r)
	}
	for stage, want := range map[string]string{StageCompile: "error:compile", StageDetectors: "error:internal", StageDecide: "error:internal"} {
		o := (&Outcome{}).fail(stage, errors.New("boom"))
		if r := ResultOf(o, tested); r.Status != decision.EvalError || *r.Reason != want {
			t.Errorf("%s: %+v", stage, r)
		}
	}
	tooLarge := (&Outcome{}).fail(StageCompile, fmt.Errorf("pack: %w", decision.ErrRequestTooLarge))
	if r := ResultOf(tooLarge, tested); *r.Reason != "error:too_large" {
		t.Errorf("too large: %s", *r.Reason)
	}
}

func TestDryRunTallyMatchesWilsonAndExtrapolates(t *testing.T) {
	tally := NewDryRunTally(tested)
	warn := ptr(decision.LevelWarn)
	hall, grounded := ptr(auditHallucinated), ptr(auditGrounded)
	// 3 fire (one audited hallucinated, one audited grounded), 5 stay ok (one audited grounded),
	// 1 cannot assess, 1 errors.
	for i := range 3 {
		tr := DryRunTrace{TraceID: fmt.Sprintf("f%d", i), InclusionProb: 1, Levels: map[string]*string{"groundedness@1": warn, "contradiction@1": nil}}
		switch i {
		case 0:
			tr.AuditVerdict = hall
		case 1:
			tr.AuditVerdict = grounded
		}
		tally.Add(tr, outcome(decision.EvalAssessed, decision.LevelWarn, 0.5+float64(i)/10))
	}
	for i := range 5 {
		tr := DryRunTrace{TraceID: fmt.Sprintf("q%d", i), InclusionProb: 1, Levels: map[string]*string{"groundedness@1": nil}}
		if i == 0 {
			tr.AuditVerdict = grounded
		}
		tally.Add(tr, outcome(decision.EvalAssessed, decision.LevelOK, 0.1))
	}
	tally.Add(DryRunTrace{TraceID: "n", InclusionProb: 1}, outcome(decision.EvalNotAssessed, "", 0))
	tally.Add(DryRunTrace{TraceID: "e", InclusionProb: 1}, (&Outcome{}).fail(StageCompile, errors.New("x")))

	evaluated, fired, cost := tally.Progress()
	if evaluated != 10 || fired != 3 || math.Abs(cost-0.009) > 1e-12 {
		t.Errorf("progress %d %d %v", evaluated, fired, cost)
	}
	s := tally.Stats(900)
	if s.Sampled != 10 || s.Assessed != 8 || s.NotAssessed != 1 || s.Errors != 1 || s.Fired != 3 {
		t.Fatalf("counts %+v", s)
	}
	lo, hi := decision.WilsonInterval(3, 9, decision.WilsonZ95)
	if s.FireRate != 3.0/9 || s.FireRateCI95 != [2]float64{lo, hi} {
		t.Errorf("rate %v ci %v, want %v [%v %v]", s.FireRate, s.FireRateCI95, 3.0/9, lo, hi)
	}
	if s.ExtrapolatedFires != 300 {
		t.Errorf("extrapolated = %d, want 300 (every body kept)", s.ExtrapolatedFires)
	}
	if len(s.Overlap) != 2 || s.Overlap[0] != (Overlap{"groundedness@1", 3}) || s.Overlap[1] != (Overlap{"contradiction@1", 0}) {
		t.Errorf("overlap = %+v", s.Overlap)
	}
	if s.AgreementWithAudit == nil || *s.AgreementWithAudit != (AuditAgreement{N: 3, Agree: 2}) {
		t.Errorf("agreement = %+v", s.AgreementWithAudit)
	}
	if len(s.Examples) != 3 || s.Examples[0].TraceID != "f2" || s.Examples[0].Risk != 0.7 || s.Examples[0].Level != decision.LevelWarn {
		t.Errorf("examples = %+v", s.Examples)
	}
	if s.Snapshot == nil || *s.Snapshot != "jev-1.13.0" {
		t.Errorf("snapshot = %v", s.Snapshot)
	}
}

func TestDryRunTallyWeightsByInclusionProbability(t *testing.T) {
	// One answer fired (its body was always kept); three passing answers were kept with
	// probability 0.25, so each stands for four.
	tally := NewDryRunTally(tested)
	tally.Add(DryRunTrace{TraceID: "f", InclusionProb: 1}, outcome(decision.EvalAssessed, decision.LevelCritical, 0.9))
	for i := range 3 {
		tally.Add(DryRunTrace{TraceID: fmt.Sprintf("p%d", i), InclusionProb: 0.25}, outcome(decision.EvalAssessed, decision.LevelOK, 0.1))
	}
	s := tally.Stats(1300)
	if s.FireRate != 0.25 {
		t.Errorf("sample rate = %v", s.FireRate)
	}
	// Weighted: 1 / (1 + 3*4) of 1300 = 100, not the unweighted 325.
	if s.ExtrapolatedFires != 100 {
		t.Errorf("extrapolated = %d, want 100", s.ExtrapolatedFires)
	}
}

func TestDryRunTallyEdges(t *testing.T) {
	empty := NewDryRunTally(tested).Stats(100)
	if empty.FireRate != 0 || empty.FireRateCI95 != [2]float64{0, 0} || empty.ExtrapolatedFires != 0 ||
		empty.AgreementWithAudit != nil || empty.Examples == nil || empty.Overlap == nil {
		t.Errorf("empty = %+v", empty)
	}
	tally := NewDryRunTally(tested)
	for i := range MaxDryRunExamples + 5 {
		tr := DryRunTrace{TraceID: fmt.Sprintf("t%02d", i), Levels: map[string]*string{tested: ptr(decision.LevelWarn)}}
		tally.Add(tr, outcome(decision.EvalAssessed, decision.LevelWarn, float64(i)/100))
	}
	s := tally.Stats(15)
	if len(s.Examples) != MaxDryRunExamples || s.Examples[0].TraceID != "t14" {
		t.Errorf("examples = %+v", s.Examples)
	}
	if len(s.Overlap) != 0 {
		t.Errorf("the tested evaluator overlaps itself: %+v", s.Overlap)
	}
	if s.ExtrapolatedFires != 15 {
		t.Errorf("extrapolated = %d", s.ExtrapolatedFires)
	}
}
