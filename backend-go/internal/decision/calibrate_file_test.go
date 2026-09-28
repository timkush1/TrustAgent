package decision_test

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/decision/vectors"
)

func template(t *testing.T, ref string) *decision.Spec {
	t.Helper()
	s, err := vectors.Template(contracts.FS, ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshotFamilyAndFileName(t *testing.T) {
	for snapshot, family := range map[string]string{
		"typesafe/jev-1.13-20260917": "typesafe/jev-1.13",
		"mock/jev-heuristic-v1":      "mock/jev-heuristic-v1",
		"jev-1.13.0":                 "jev-1.13.0",
		"x/model-2026091":            "x/model-2026091", // seven digits: not a date suffix
	} {
		if got := decision.SnapshotFamily(snapshot); got != family {
			t.Errorf("SnapshotFamily(%q) = %q, want %q", snapshot, got, family)
		}
	}
	for snapshot, want := range map[string]string{
		"mock/jev-heuristic-v1":      "mock__jev-heuristic-v1.json",
		"typesafe/jev-1.13-20260917": "typesafe__jev-1.13-20260917.json",
	} {
		if got, err := decision.CalibrationFileName(snapshot); err != nil || got != want {
			t.Errorf("CalibrationFileName(%q) = %q, %v; want %q", snapshot, got, err, want)
		}
	}
	for _, bad := range []string{"", "../etc/passwd", "a b", "/abs", "x/..", strings.Repeat("a", 201)} {
		if got, err := decision.CalibrationFileName(bad); err == nil {
			t.Errorf("CalibrationFileName(%q) = %q, want an error", bad, got)
		}
	}
}

func TestEvaluatorTargetAndKey(t *testing.T) {
	g := template(t, "groundedness@1")
	target, err := decision.EvaluatorTarget(g, map[string]string{"ignored": "x"})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := g.Hashes()
	if target.ParamsSHA256 != nil || target.CalibrationKey != h.CalibrationKey || target.Kind != "evaluator" {
		t.Fatalf("groundedness target = %+v", target)
	}
	if want := "groundedness@1#" + h.CalibrationKey[:16] + "#-"; target.Key() != want {
		t.Errorf("Key() = %q, want %q", target.Key(), want)
	}

	c := template(t, "commitments@1")
	def, err := decision.EvaluatorTarget(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := decision.EvaluatorTarget(c, map[string]string{"allowed_commitments": "Refunds within 30 days."})
	if err != nil {
		t.Fatal(err)
	}
	if def.ParamsSHA256 == nil || custom.ParamsSHA256 == nil || *def.ParamsSHA256 == *custom.ParamsSHA256 {
		t.Fatalf("params hashes: default %v, custom %v", def.ParamsSHA256, custom.ParamsSHA256)
	}
	resolved := decision.ResolvedParams(c, nil)
	want, _ := decision.SHA256Hex(resolved)
	if *def.ParamsSHA256 != want || !strings.HasSuffix(def.Key(), "#"+want[:16]) {
		t.Errorf("default params hash %s, key %s; want sha256(JCS(%v)) = %s", *def.ParamsSHA256, def.Key(), resolved, want)
	}
	if decision.ResolvedParams(g, map[string]string{"x": "y"}) != nil {
		t.Error("an evaluator without params resolved some")
	}

	solo, err := decision.SoloQuestionSetSHA256(g)
	if err != nil {
		t.Fatal(err)
	}
	// compile() reports the same question set hash for a route with only this evaluator.
	compiled, err := decision.Compile(&decision.CompileRoute{
		Evaluators: []decision.RouteEvaluator{{Ref: "groundedness@1"}}, OnNoEvidence: "ignore",
		EvidenceSources: []string{"request_context"}, Model: "m",
	}, map[string]*decision.Spec{"groundedness@1": g}, &decision.CompileInputs{
		AssistantResponse: "Refunds take 30 days.",
		Evidence:          []decision.EvidenceItem{{ID: "doc", Source: "request_context", Text: strings.Repeat("Refunds are processed within 30 days. ", 3)}},
	}, decision.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if compiled.QuestionSetSHA256 != solo {
		t.Errorf("SoloQuestionSetSHA256 = %s, compile says %s", solo, compiled.QuestionSetSHA256)
	}
}

func fitFor(t *testing.T, n int) *decision.Fit {
	t.Helper()
	r := rand.New(rand.NewPCG(uint64(n), 42))
	s := make([]decision.LabeledScore, n)
	for i := range s {
		x := r.Float64()
		s[i] = decision.LabeledScore{Risk: x, Fire: r.Float64() < x}
	}
	fit, err := decision.FitCalibration(s, decision.FitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return fit
}

func TestCalibrationFileRoundTrip(t *testing.T) {
	g := template(t, "groundedness@1")
	target, err := decision.EvaluatorTarget(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := decision.SoloQuestionSetSHA256(g)
	ctx := decision.FitContext{StateFields: []string{"evidence", "assistant_response", "evidence"}, QuestionSetSHA256: qs}
	fit := fitFor(t, 240)
	src := decision.LabelSourceCounts{PublicDataset: 240}
	snapshot := "typesafe/jev-1.13-20260917"
	e, err := decision.NewCalibrationEntry(target, ctx, fit, src, []string{"ragtruth:qa@abc"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if e.Quality != decision.DeriveQuality(snapshot, src, fit.NPos, fit.NNeg, false) ||
		strings.Join(e.FitContext.StateFields, ",") != "assistant_response,evidence" || e.FitContext.Partition != "single" {
		t.Fatalf("entry = %+v", e)
	}
	if _, err := decision.NewCalibrationEntry(target, ctx, fit, decision.LabelSourceCounts{Human: 1}, nil, snapshot); err == nil {
		t.Error("NewCalibrationEntry accepted label sources that do not add up to n")
	}

	f := decision.NewCalibrationFile("openrouter-decisions", snapshot, "cmd/calibrate@abc1234", time.Date(2026, 10, 5, 10, 0, 0, 0, time.FixedZone("x", 3600)))
	if f.Upsert(*e) {
		t.Error("Upsert into an empty file replaced an entry")
	}
	if !f.Upsert(*e) || len(f.Entries) != 1 {
		t.Error("Upsert of the same target did not replace it")
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, "calibration.v1.schema.json", b)
	if !strings.Contains(string(b), `"created_at": "2026-10-05T09:00:00Z"`) || !strings.Contains(string(b), `"family": "typesafe/jev-1.13"`) {
		t.Errorf("file:\n%s", b)
	}
	back, err := decision.ParseCalibrationFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Entries[0].Target.Key() != target.Key() || len(back.Entries[0].Table) != decision.CalibrationTableSize {
		t.Errorf("round trip = %+v", back.Entries[0])
	}

	// Check catches what the schema cannot.
	bad := *f
	bad.Entries = []decision.CalibrationFileEntry{*e, *e}
	bad.Entries[0].Quality = decision.QualityMeasured
	bad.Entries[0].N = 239
	bad.Entries[1].Method = decision.MethodIsotonic
	bad.Entries[1].Table = append([]float64{0.5}, bad.Entries[1].Table[1:]...)
	bad.DecisionModel.Family = "typesafe/jev"
	err = bad.Check()
	for _, want := range []string{"family", "duplicate target", "isotonic entry has platt", "decreases at point", "n = 239"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check() = %v, want it to mention %q", err, want)
		}
	}
	claims := *f
	claims.Entries = []decision.CalibrationFileEntry{*e}
	claims.Entries[0].LabelSource = decision.LabelSourceCounts{PublicDataset: 239, Judge: 1}
	if err := claims.Check(); err == nil || !strings.Contains(err.Error(), `its labels support "proxy"`) {
		t.Errorf("Check() of an over-claimed quality = %v", err)
	}
	if _, err := decision.ParseCalibrationFile([]byte(`{"schema":"trustagent.calibration/v1","extra":1}`)); err == nil {
		t.Error("ParseCalibrationFile accepted an unknown member")
	}
	if _, err := decision.ParseCalibrationFile(append(b, '{', '}')); err == nil {
		t.Error("ParseCalibrationFile accepted trailing data")
	}
}
