package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
)

const snapshot = "typesafe/jev-1.13-20260917"

func testEnv() env {
	return env{
		now:    func() time.Time { return time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC) },
		getenv: func(string) string { return "" },
		rev:    func() string { return "abc123def456" },
	}
}

func runCLI(t *testing.T, stdin string, e env, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb, e)
	return code, out.String(), errb.String()
}

// flatLabels returns n labelled risks whose chance of firing grows with the risk.
func flatLabels(n int, seed uint64, extra string) string {
	r := rand.New(rand.NewPCG(seed, 7))
	var b strings.Builder
	for i := range n {
		x := float64(r.IntN(1000)) / 1000
		fire := r.Float64() < 0.1+0.8*x
		fmt.Fprintf(&b, `{"id":"s%d","risk_raw":%v,"label":%v%s}`+"\n", i, x, fire, extra)
	}
	return b.String()
}

func readFile(t *testing.T, path string) *decision.CalibrationFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, "calibration.v1.schema.json", b)
	f, err := decision.ParseCalibrationFile(b)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := runCLI(t, flatLabels(240, 1, ""), testEnv(), "--evaluator", "groundedness@1", "--label-source", "public_dataset",
		"--snapshot", snapshot, "--backend", "openrouter-decisions", "--out", dir, "--dataset", "ragtruth:qa@abc")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"Calibration for groundedness@1 on typesafe/jev-1.13-20260917 (backend openrouter-decisions)",
		"Labels: 240 used", "sources: public_dataset 240", "Datasets: ragtruth:qa@abc",
		"Quality: measured", "Method: platt: calibrated(x) = 1/(1 + exp(-(", "not ground truth",
		"Recommended thresholds (calibrated space)", "critical_at: none (insufficient evidence to block)",
		"does not change live decisions", "Dry run: nothing written. Pass --write to write " + filepath.Join(dir, "typesafe__jev-1.13-20260917.json"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("dry run wrote %v", entries)
	}
}

func TestWriteMergeAndReplace(t *testing.T) {
	dir := t.TempDir()
	args := func(ref string) []string {
		return []string{"--evaluator", ref, "--label-source", "human", "--snapshot", snapshot, "--backend", "openrouter-decisions", "--out", dir, "--write"}
	}
	path := filepath.Join(dir, "typesafe__jev-1.13-20260917.json")
	if code, out, stderr := runCLI(t, flatLabels(120, 2, ""), testEnv(), args("groundedness@1")...); code != exitOK || !strings.Contains(out, "Added groundedness@1#") {
		t.Fatalf("exit %d: %s%s", code, out, stderr)
	}
	f := readFile(t, path)
	e := f.Entries[0]
	if f.GeneratedBy != "cmd/calibrate@abc123def456" || f.DecisionModel.Family != "typesafe/jev-1.13" || e.Quality != decision.QualityProvisional ||
		e.N != 120 || e.LabelSource.Human != 120 || e.Method != decision.MethodPlatt || e.FitContext.Partition != "single" ||
		strings.Join(e.FitContext.StateFields, ",") != "assistant_response,evidence" || !f.CreatedAt.Equal(testEnv().now()) {
		t.Errorf("file = %+v\nentry = %+v", f, e)
	}
	g, _ := decision.EvaluatorTarget(mustSpec(t, "groundedness@1"), nil)
	qs, _ := decision.SoloQuestionSetSHA256(mustSpec(t, "groundedness@1"))
	if e.Target != g || e.FitContext.QuestionSetSHA256 != qs {
		t.Errorf("target %+v, question set %s", e.Target, e.FitContext.QuestionSetSHA256)
	}

	// A second evaluator joins the file; the first is kept.
	if code, _, stderr := runCLI(t, flatLabels(120, 3, ""), testEnv(), args("contradiction@1")...); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	// Refitting the first replaces its entry.
	code, out, stderr := runCLI(t, flatLabels(300, 4, ""), testEnv(), args("groundedness@1")...)
	if code != exitOK || !strings.Contains(out, "Replaced groundedness@1#") {
		t.Fatalf("exit %d: %s%s", code, out, stderr)
	}
	f = readFile(t, path)
	if len(f.Entries) != 2 || f.Entries[0].Target.EvaluatorID != "groundedness" || f.Entries[0].N != 300 ||
		f.Entries[0].Quality != decision.QualityMeasured || f.Entries[1].Target.EvaluatorID != "contradiction" {
		t.Errorf("entries = %+v", f.Entries)
	}

	// A file for the snapshot that names another backend is not overwritten.
	code, _, stderr = runCLI(t, flatLabels(120, 5, ""), testEnv(), "--evaluator", "groundedness@1", "--label-source", "human",
		"--snapshot", snapshot, "--backend", "typesafe", "--out", dir, "--write")
	if code != exitFailed || !strings.Contains(stderr, "holds typesafe/jev-1.13-20260917 on openrouter-decisions") {
		t.Errorf("backend clash: exit %d: %s", code, stderr)
	}
}

func mustSpec(t *testing.T, ref string) *decision.Spec {
	t.Helper()
	specs, err := loadSpecs(nil)
	if err != nil {
		t.Fatal(err)
	}
	return specs[ref]
}

// gateEvalLines imitates cmd/gate-eval output for groundedness@1 and contradiction@1.
func gateEvalLines(n int) string {
	r := rand.New(rand.NewPCG(11, 12))
	var b strings.Builder
	for i := range n {
		x := float64(r.IntN(1000)) / 1000
		hallucinated := r.Float64() < 0.1+0.8*x
		fields := `["assistant_response","evidence"]`
		qs := strings.Repeat("a", 64)
		if i%10 == 0 {
			fields, qs = `["assistant_response","evidence","user_request"]`, strings.Repeat("b", 64)
		}
		fmt.Fprintf(&b, `{"id":"g%d","error":null,"skipped":null,"simulated":false,`+
			`"decision":{"status":"ok","evaluators":{"groundedness@1":{"status":"assessed","risk_raw":%v},"contradiction@1":{"status":"assessed","risk_raw":0.1}}},`+
			`"decision_model":{"backend":"openrouter-decisions","snapshot":%q},"endpoint_host":"openrouter.ai","compile":{"state_fields":%s,"question_set_sha256":%q},`+
			`"labels":{"groundedness@1":%v},"label_hallucinated":%v,"label_source":"public_dataset"}`+"\n",
			i, x, snapshot, fields, qs, hallucinated, hallucinated)
	}
	b.WriteString(`{"id":"err","error":"compile: boom","skipped":null,"decision":null}` + "\n")
	b.WriteString(`{"id":"over","error":null,"skipped":"over_budget","decision":null}` + "\n")
	b.WriteString(`{"id":"na","error":null,"skipped":null,"decision":{"evaluators":{"groundedness@1":{"status":"not_assessed","risk_raw":null}}}}` + "\n")
	b.WriteString(`{"id":"nolabel","error":null,"skipped":null,"decision":{"evaluators":{"groundedness@1":{"status":"assessed","risk_raw":0.5}}},"decision_model":{"backend":"openrouter-decisions","snapshot":"` + snapshot + `"},"labels":{}}` + "\n")
	return b.String()
}

func TestGateEvalLines(t *testing.T) {
	dir := t.TempDir()
	code, out, stderr := runCLI(t, gateEvalLines(250), testEnv(), "--evaluator", "groundedness@1", "--out", dir, "--write")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(out, "not used: 1 gate-eval error, 1 gate-eval over_budget, 1 not assessed, 1 unlabelled") ||
		!strings.Contains(out, "Quality: measured") || !strings.Contains(out, "different state compositions") {
		t.Errorf("summary:\n%s", out)
	}
	f := readFile(t, filepath.Join(dir, "typesafe__jev-1.13-20260917.json"))
	e := f.Entries[0]
	if f.DecisionModel.Backend != "openrouter-decisions" || e.N != 250 ||
		strings.Join(e.FitContext.StateFields, ",") != "assistant_response,evidence" || e.FitContext.QuestionSetSHA256 != strings.Repeat("a", 64) {
		t.Errorf("file %+v, entry context %+v", f.DecisionModel, e.FitContext)
	}
	if len(f.Warnings) != 2 || !strings.HasPrefix(f.Warnings[0], "groundedness@1: ") {
		t.Errorf("warnings = %q", f.Warnings)
	}

	// --label-key reads another member (contradiction@1 lines carry no labels[] entry). Its risk is
	// constant, so the mapping is flat and nothing is recommended.
	code, out, stderr = runCLI(t, gateEvalLines(250), testEnv(), "--evaluator", "contradiction@1", "--label-key", "label_hallucinated", "--out", dir)
	if code != exitOK || !strings.Contains(out, "Labels: 250 used") || !strings.Contains(out, "every label has the same risk") ||
		!strings.Contains(out, "warn_at: none") {
		t.Errorf("contradiction with a constant risk: exit %d %s %s", code, out, stderr)
	}
}

func TestQualityIsDerivedFromTheLabels(t *testing.T) {
	dir := t.TempDir()
	lines := flatLabels(260, 6, `,"label_source":"public_dataset"`) + flatLabels(1, 7, `,"label_source":"judge"`)
	code, out, stderr := runCLI(t, lines, testEnv(), "--evaluator", "groundedness@1", "--snapshot", snapshot, "--backend", "openrouter-decisions", "--out", dir)
	if code != exitOK || !strings.Contains(out, "Quality: proxy: 1 labels came from uniform-sample audits or a judge") {
		t.Errorf("exit %d:\n%s%s", code, out, stderr)
	}
	code, out, stderr = runCLI(t, flatLabels(60, 8, ""), testEnv(), "--evaluator", "groundedness@1", "--label-source", "human", "--snapshot", "mock/jev-heuristic-v1", "--out", dir, "--write")
	if code != exitOK || !strings.Contains(out, "Quality: mock") || !strings.Contains(out, "(backend mock)") {
		t.Fatalf("exit %d:\n%s%s", code, out, stderr)
	}
	if f := readFile(t, filepath.Join(dir, "mock__jev-heuristic-v1.json")); f.Entries[0].Quality != decision.QualityMock {
		t.Errorf("mock quality = %s", f.Entries[0].Quality)
	}
}

func TestInputErrors(t *testing.T) {
	for _, c := range []struct {
		name, stdin string
		args        []string
		code        int
		want        string
	}{
		{"no evaluator", "", nil, exitUsage, "usage"},
		{"unknown evaluator", "", []string{"--evaluator", "nope@1"}, exitUsage, "unknown evaluator"},
		{"no label source", flatLabels(20, 1, ""), []string{"--evaluator", "groundedness@1", "--snapshot", snapshot}, exitFailed, "--label-source"},
		{"no snapshot", flatLabels(20, 1, ""), []string{"--evaluator", "groundedness@1", "--label-source", "human"}, exitUsage, "--snapshot"},
		{"no backend", flatLabels(20, 1, ""), []string{"--evaluator", "groundedness@1", "--label-source", "human", "--snapshot", snapshot}, exitUsage, "--backend"},
		{"mixed snapshots", `{"risk_raw":0.1,"label":true,"snapshot":"a/b"}` + "\n" + `{"risk_raw":0.2,"label":false,"snapshot":"a/c"}`, []string{"--evaluator", "groundedness@1", "--label-source", "human"}, exitFailed, "one snapshot"},
		{"bad label", `{"risk_raw":0.1,"label":"yes"}`, []string{"--evaluator", "groundedness@1", "--label-source", "human"}, exitFailed, "line 1: label"},
		{"bad risk", `{"risk_raw":1.5,"label":true}`, []string{"--evaluator", "groundedness@1", "--label-source", "human"}, exitFailed, "outside [0,1]"},
		{"unknown member", `{"risk_raw":0.5,"label":true,"score":1}`, []string{"--evaluator", "groundedness@1"}, exitFailed, "unknown field"},
		{"unknown source", `{"risk_raw":0.5,"label":true,"label_source":"crowd"}`, []string{"--evaluator", "groundedness@1"}, exitFailed, "unknown label source"},
		{"too few labels", flatLabels(5, 1, ""), []string{"--evaluator", "groundedness@1", "--label-source", "human", "--snapshot", snapshot, "--backend", "typesafe"}, exitFailed, "not enough labels"},
		{"bad param", flatLabels(20, 1, ""), []string{"--evaluator", "groundedness@1", "--label-source", "human", "--snapshot", snapshot, "--backend", "typesafe", "--param", "x"}, exitUsage, "name=value"},
		{"spec redefines a template", "", []string{"--evaluator", "groundedness@1", "--spec", builtinSpecFile(t, "groundedness@1")}, exitUsage, "already defined"},
	} {
		code, _, stderr := runCLI(t, c.stdin, testEnv(), c.args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, want %d; stderr %q lacks %q", c.name, code, c.code, stderr, c.want)
		}
	}
	// Lines for another target are skipped, not rejected.
	in := flatLabels(20, 9, `,"target":"groundedness@1"`) + `{"risk_raw":0.5,"label":true,"target":"refusal@1"}` + "\n"
	code, out, stderr := runCLI(t, in, testEnv(), "--evaluator", "groundedness@1", "--label-source", "human", "--snapshot", snapshot, "--backend", "typesafe", "--out", t.TempDir())
	if code != exitOK || !strings.Contains(out, "1 other target") {
		t.Errorf("exit %d:\n%s%s", code, out, stderr)
	}
}

func TestDefaultOutDirIsTheRepositoryConfig(t *testing.T) {
	got := outDir("")
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "config", "calibration") {
		t.Errorf("outDir(\"\") = %s, want %s/config/calibration", got, root)
	}
	if outDir("x/y") != "x/y" {
		t.Error("an explicit --out was rewritten")
	}
}

func TestParamsHash(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runCLI(t, flatLabels(40, 10, ""), testEnv(), "--evaluator", "commitments@1", "--label-source", "human",
		"--snapshot", snapshot, "--backend", "typesafe", "--param", "allowed_commitments=Refunds within 30 days.", "--out", dir, "--write")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	f := readFile(t, filepath.Join(dir, "typesafe__jev-1.13-20260917.json"))
	want, _ := decision.SHA256Hex(map[string]string{"allowed_commitments": "Refunds within 30 days."})
	if p := f.Entries[0].Target.ParamsSHA256; p == nil || *p != want {
		t.Errorf("params_sha256 = %v, want %s", p, want)
	}
}

func TestSourceAPI(t *testing.T) {
	fit, err := decision.FitCalibration([]decision.LabeledScore{
		{Risk: 0.1}, {Risk: 0.2}, {Risk: 0.3}, {Risk: 0.4, Fire: true}, {Risk: 0.6, Fire: true}, {Risk: 0.7}, {Risk: 0.8, Fire: true}, {Risk: 0.9, Fire: true},
	}, decision.FitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := mustSpec(t, "groundedness@1")
	target, _ := decision.EvaluatorTarget(spec, nil)
	qs, _ := decision.SoloQuestionSetSHA256(spec)
	entry, err := decision.NewCalibrationEntry(target, decision.FitContext{StateFields: spec.StateFields, QuestionSetSHA256: qs}, fit,
		decision.LabelSourceCounts{Human: 8}, nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var got fitRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/calibration/fit" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		// The request is what the gateway's POST /api/calibration/fit accepts (api.v1).
		contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/CalibrationFitRequest", b)
		if got.Target == "refusal@1" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":{"code":"validation_failed","message":"Not enough labels to calibrate refusal@1: 5 (2 should fire, 3 should not); a fit needs 8 with at least 3 of each."}}`))
			return
		}
		// The answer has the gateway's full CalibrationFitResponse shape.
		resp, _ := json.Marshal(map[string]any{
			"record": calibrationRecord{
				ID: 7, CreatedAt: time.Date(2026, 9, 28, 10, 15, 22, 0, time.UTC), Origin: "fit", Backend: "openrouter-decisions",
				Snapshot: snapshot, Family: "typesafe/jev-1.13", TargetKind: "evaluator", TargetKey: target.Key(), Quality: entry.Quality, Entry: *entry,
			},
			"reliability": map[string]any{"evaluator": "groundedness@1", "snapshot": snapshot, "window": "30d",
				"n_by_source": map[string]int{"human": 8, "uniform_audit": 0}, "bins": fit.Reliability, "ece": fit.Metrics.ECEBefore, "n": 8},
			"metrics": fit.Metrics, "recommended": fit.Recommended,
		})
		contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/CalibrationFitResponse", resp)
		_, _ = w.Write(resp)
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("admin-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := testEnv()
	e.client = srv.Client()
	code, out, stderr := runCLI(t, "", e, "--source", "api", "--api", srv.URL, "--api-key-file", keyFile, "--evaluator", "groundedness@1",
		"--route", "support-bot", "--sources", "labels,uniform_audit", "--out", dir, "--write")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if auth != "Bearer admin-secret" || got.Target != "groundedness@1" || got.Route != "support-bot" ||
		strings.Join(got.Sources, ",") != "labels,uniform_audit" || got.Method != "auto" || got.Activate {
		t.Errorf("request = %+v, auth %q", got, auth)
	}
	if !strings.Contains(out, "Quality: provisional") || strings.Contains(out, "95% range") {
		t.Errorf("summary:\n%s", out)
	}
	f := readFile(t, filepath.Join(dir, "typesafe__jev-1.13-20260917.json"))
	if f.GeneratedBy != "cmd/calibrate@abc123def456 via api/calibration/fit" || f.Entries[0].Target != target {
		t.Errorf("file = %+v", f)
	}

	code, _, stderr = runCLI(t, "", e, "--source", "api", "--api", srv.URL, "--evaluator", "refusal@1", "--out", dir)
	if code != exitFailed || !strings.Contains(stderr, "422 validation_failed: Not enough labels to calibrate refusal@1") {
		t.Errorf("api error: exit %d %s", code, stderr)
	}
	code, _, stderr = runCLI(t, "", e, "--source", "api", "--api", "ftp://x", "--evaluator", "groundedness@1", "--out", dir)
	if code != exitFailed || !strings.Contains(stderr, "not an http(s) URL") {
		t.Errorf("bad url: exit %d %s", code, stderr)
	}
	code, _, stderr = runCLI(t, "", e, "--source", "api", "--api", srv.URL, "--evaluator", "contradiction@1", "--out", dir)
	if code != exitFailed || !strings.Contains(stderr, "fitted groundedness@1, not contradiction@1") {
		t.Errorf("wrong target: exit %d %s", code, stderr)
	}
}

// builtinSpecFile writes a built-in template to a spec file.
func builtinSpecFile(t *testing.T, ref string) string {
	t.Helper()
	b, err := json.Marshal(mustSpec(t, ref))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
