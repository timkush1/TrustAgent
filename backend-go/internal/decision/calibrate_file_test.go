package decision_test

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
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

// validFileJSON is a schema-valid calibration file with one Platt entry that recommends a warn_at.
func validFileJSON(t *testing.T) []byte {
	t.Helper()
	g := template(t, "groundedness@1")
	target, err := decision.EvaluatorTarget(g, nil)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := decision.SoloQuestionSetSHA256(g)
	fit := fitFor(t, 240)
	if fit.Recommended.WarnAt == nil {
		t.Fatal("the base fit recommends no warn_at")
	}
	snapshot := "typesafe/jev-1.13-20260917"
	e, err := decision.NewCalibrationEntry(target, decision.FitContext{StateFields: g.StateFields, QuestionSetSHA256: qs}, fit,
		decision.LabelSourceCounts{PublicDataset: 240}, []string{"ragtruth:qa@abc"}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f := decision.NewCalibrationFile("openrouter-decisions", snapshot, "cmd/calibrate@abc1234", time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	f.Upsert(*e)
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, "calibration.v1.schema.json", b)
	if _, err := decision.ParseCalibrationFile(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestParseCalibrationFileRejectsWhatTheSchemaRejects mutates a valid file one member at a time.
// The schema rejects every mutation, and so must ParseCalibrationFile: the gateway has no schema
// validator at run time, so it is the only check an imported file gets.
func TestParseCalibrationFileRejectsWhatTheSchemaRejects(t *testing.T) {
	valid := validFileJSON(t)
	schema := contractstest.Schema(t, "calibration.v1.schema.json")
	type obj = map[string]any
	entry := func(m obj) obj { return m["entries"].([]any)[0].(obj) }
	member := func(m obj, path ...string) obj {
		o := entry(m)
		for _, p := range path {
			o = o[p].(obj)
		}
		return o
	}
	for name, mutate := range map[string]func(obj){
		"created_at missing":    func(m obj) { delete(m, "created_at") },
		"generated_by empty":    func(m obj) { m["generated_by"] = "" },
		"generated_by too long": func(m obj) { m["generated_by"] = strings.Repeat("x", 201) },
		"unknown backend":       func(m obj) { m["decision_model"].(obj)["backend"] = "gpt-4o" },
		"entries missing":       func(m obj) { delete(m, "entries") },
		"warnings missing":      func(m obj) { delete(m, "warnings") },
		"target kind":           func(m obj) { member(m, "target")["kind"] = "kb" },
		"evaluator id":          func(m obj) { member(m, "target")["evaluator_id"] = "Groundedness" },
		"evaluator version":     func(m obj) { member(m, "target")["evaluator_version"] = 0 },
		"calibration key":       func(m obj) { member(m, "target")["calibration_key"] = "abc" },
		"params sha256":         func(m obj) { member(m, "target")["params_sha256"] = strings.Repeat("G", 64) },
		"unknown state field":   func(m obj) { member(m, "fit_context")["state_fields"] = []any{"answer"} },
		"state field twice":     func(m obj) { member(m, "fit_context")["state_fields"] = []any{"evidence", "evidence"} },
		"state fields missing":  func(m obj) { delete(member(m, "fit_context"), "state_fields") },
		"partition":             func(m obj) { member(m, "fit_context")["partition"] = "per_item" },
		"question set sha256":   func(m obj) { member(m, "fit_context")["question_set_sha256"] = "" },
		"negative n_pos":        func(m obj) { e := entry(m); e["n_pos"], e["n_neg"] = -1, e["n"] },
		"negative label source": func(m obj) { member(m, "label_source")["human"], member(m, "label_source")["public_dataset"] = -1, 241 },
		"datasets missing":      func(m obj) { delete(entry(m), "datasets") },
		"empty dataset":         func(m obj) { entry(m)["datasets"] = []any{""} },
		"auroc above 1":         func(m obj) { member(m, "metrics")["auroc"] = 1.5 },
		"reliability missing":   func(m obj) { delete(entry(m), "reliability") },
		"reliability share":     func(m obj) { entry(m)["reliability"].([]any)[0].(obj)["frac_pos"] = 2 },
		"recommended space":     func(m obj) { member(m, "recommended")["space"] = "raw" },
		"warn_at precision":     func(m obj) { member(m, "recommended", "warn_at")["precision"] = 1.2 },
		"critical_at bound": func(m obj) {
			member(m, "recommended")["critical_at"] = obj{"value": 0.99, "precision_lb95": 1.5, "predicted_pos": 60}
		},
		"critical_at count": func(m obj) {
			member(m, "recommended")["critical_at"] = obj{"value": 0.99, "precision_lb95": 0.96, "predicted_pos": -1}
		},
		"empty critical_null_reason": func(m obj) { member(m, "recommended")["critical_null_reason"] = "" },
	} {
		var m obj
		if err := json.Unmarshal(valid, &m); err != nil {
			t.Fatal(err)
		}
		mutate(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if schema.Validate(inst) == nil {
			t.Errorf("%s: the schema accepts the mutation, so it tests nothing", name)
		}
		if _, err := decision.ParseCalibrationFile(b); err == nil {
			t.Errorf("%s: ParseCalibrationFile accepted a file the schema rejects", name)
		}
	}

	// Beyond the schema: a recommended warn_at above critical_at cannot be adopted (SPEC §2.8).
	var m obj
	if err := json.Unmarshal(valid, &m); err != nil {
		t.Fatal(err)
	}
	rec := member(m, "recommended")
	rec["critical_at"], rec["critical_null_reason"] = obj{"value": 0.01, "precision_lb95": 0.96, "predicted_pos": 60}, nil
	b, _ := json.Marshal(m)
	if _, err := decision.ParseCalibrationFile(b); err == nil || !strings.Contains(err.Error(), "is above critical_at") {
		t.Errorf("warn_at above critical_at: %v", err)
	}
}

// TestFittedFilesPassCheck: every entry the fitter produces is one Check accepts, whatever the
// labels and method.
func TestFittedFilesPassCheck(t *testing.T) {
	g := template(t, "groundedness@1")
	target, _ := decision.EvaluatorTarget(g, nil)
	qs, _ := decision.SoloQuestionSetSHA256(g)
	r := rand.New(rand.NewPCG(5, 6))
	for i := range 40 {
		n := 8 + r.IntN(700)
		s := make([]decision.LabeledScore, n)
		for j := range s {
			x := float64(r.IntN(101)) / 100
			s[j] = decision.LabeledScore{Risk: x, Fire: r.Float64() < x*x}
		}
		s[0].Fire, s[1].Fire, s[2].Fire, s[3].Fire, s[4].Fire, s[5].Fire = true, true, true, false, false, false
		method := []string{decision.MethodAuto, decision.MethodIsotonic, decision.MethodPlatt}[i%3]
		fit, err := decision.FitCalibration(s, decision.FitOptions{Method: method})
		if err != nil {
			t.Fatal(err)
		}
		src := decision.LabelSourceCounts{Human: n / 2, PublicDataset: n - n/2}
		e, err := decision.NewCalibrationEntry(target, decision.FitContext{QuestionSetSHA256: qs}, fit, src, nil, "typesafe/jev-1.13-20260917")
		if err != nil {
			t.Fatal(err)
		}
		f := decision.NewCalibrationFile("typesafe", "typesafe/jev-1.13-20260917", "test", time.Now())
		f.Upsert(*e)
		if err := f.Check(); err != nil {
			t.Fatalf("fit %d (%s, n=%d): %v", i, method, n, err)
		}
		b, _ := json.Marshal(f)
		contractstest.ValidateJSON(t, "calibration.v1.schema.json", b)
	}
}
