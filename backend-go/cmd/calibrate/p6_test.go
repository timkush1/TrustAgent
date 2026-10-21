package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/decision"
)

// The P6 calibration flow (D206): fit on RAGTruth-QA train lines from one warm gate-eval process,
// merge both evaluators into one file, then measure ECE and Brier on held-out test labels. Risks
// from a stand-in host are refused (D203).

const trainDataset = "ragtruth:qa-train@c103204"

// calibLines imitates `make calib-pass` output: gate-eval lines with both labels (groundedness@1 =
// hallucinated, contradiction@1 = has a conflict span), from host. The raw risk overstates the
// chance of firing (fire with probability x²), so a calibration has something to correct.
func calibLines(n int, seed uint64, host string) string {
	r := rand.New(rand.NewPCG(seed, 99))
	var b strings.Builder
	for i := range n {
		x := float64(r.IntN(1000)) / 1000
		hallucinated := r.Float64() < x*x
		conflict := hallucinated && r.Float64() < 0.4
		c := 0.05
		if conflict {
			c = 0.5 + x/2
		}
		hostJSON := "null"
		if host != "" {
			hostJSON = fmt.Sprintf("%q", host)
		}
		fmt.Fprintf(&b, `{"id":"c%d","error":null,"skipped":null,"simulated":false,"endpoint_host":%s,"connection":"warm",`+
			`"decision":{"status":"ok","evaluators":{"groundedness@1":{"status":"assessed","risk_raw":%v},"contradiction@1":{"status":"assessed","risk_raw":%v}}},`+
			`"decision_model":{"backend":"openrouter-decisions","snapshot":%q},"compile":{"state_fields":["assistant_response","evidence"],"question_set_sha256":%q},`+
			`"labels":{"groundedness@1":%v,"contradiction@1":%v},"label_hallucinated":%v,"label_source":"public_dataset"}`+"\n",
			i, hostJSON, x, c, snapshot, strings.Repeat("a", 64), hallucinated, conflict, hallucinated)
	}
	return b.String()
}

// holdoutLines imitates `bench export-labels`: flat labelled risks of the bench's test sample.
func holdoutLines(n int, seed uint64, dataset, host string) string {
	r := rand.New(rand.NewPCG(seed, 98))
	var b strings.Builder
	for i := range n {
		x := float64(r.IntN(1000)) / 1000
		fire := r.Float64() < x*x
		fmt.Fprintf(&b, `{"id":"h%d","target":"groundedness@1","risk_raw":%v,"label":%v,"label_source":"public_dataset","snapshot":%q,"backend":"openrouter-decisions","dataset":%q,"endpoint_host":%q}`+"\n",
			i, x, fire, snapshot, dataset, host)
	}
	return b.String()
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCalibrateEvaluatorMergeFromOneCalibPass(t *testing.T) {
	dir := t.TempDir()
	gate := writeTemp(t, "gate.jsonl", calibLines(800, 1, "openrouter.ai"))
	fit := func(ref string) string {
		code, out, stderr := runCLI(t, "", testEnv(), "--evaluator", ref, "--in", gate, "--label-source", "public_dataset",
			"--dataset", trainDataset, "--out", dir, "--write")
		if code != exitOK {
			t.Fatalf("%s: exit %d: %s%s", ref, code, out, stderr)
		}
		return out
	}
	if out := fit("groundedness@1"); !strings.Contains(out, "Quality: measured") || !strings.Contains(out, "Added groundedness@1#") {
		t.Errorf("groundedness@1 from 800 public-dataset labels:\n%s", out)
	}
	if out := fit("contradiction@1"); !strings.Contains(out, "Added contradiction@1#") {
		t.Errorf("contradiction@1:\n%s", out)
	}
	f := readFile(t, filepath.Join(dir, "typesafe__jev-1.13-20260917.json"))
	if len(f.Entries) != 2 || f.Entries[0].Target.EvaluatorID != "groundedness" || f.Entries[1].Target.EvaluatorID != "contradiction" {
		t.Fatalf("entries %+v", f.Entries)
	}
	for _, e := range f.Entries {
		if e.N != 800 || e.LabelSource.PublicDataset != 800 || strings.Join(e.Datasets, ",") != trainDataset {
			t.Errorf("%s: n %d, sources %+v, datasets %v", e.Target.EvaluatorID, e.N, e.LabelSource, e.Datasets)
		}
	}
	// The contradiction labels are the conflict spans only, far fewer positives than groundedness.
	if g, c := f.Entries[0], f.Entries[1]; c.NPos >= g.NPos || c.NPos == 0 {
		t.Errorf("contradiction positives %d, groundedness %d", c.NPos, g.NPos)
	}
}

// ece10 is an independent ECE over ten equal-width bins (bin min(floor(10p), 9)).
func ece10(risks []float64, fire []bool) float64 {
	var n, pos [10]float64
	var sum [10]float64
	for i, p := range risks {
		k := min(int(math.Floor(p*10)), 9)
		n[k]++
		sum[k] += p
		if fire[i] {
			pos[k]++
		}
	}
	e := 0.0
	for k := range 10 {
		if n[k] > 0 {
			e += n[k] / float64(len(risks)) * math.Abs(sum[k]/n[k]-pos[k]/n[k])
		}
	}
	return e
}

func TestCalibrateHoldoutMeasuresTheHeldOutSample(t *testing.T) {
	dir := t.TempDir()
	gate := writeTemp(t, "gate.jsonl", calibLines(800, 2, "openrouter.ai"))
	if code, out, stderr := runCLI(t, "", testEnv(), "--evaluator", "groundedness@1", "--in", gate, "--dataset", trainDataset, "--out", dir, "--write"); code != exitOK {
		t.Fatalf("fit: exit %d: %s%s", code, out, stderr)
	}
	calPath := filepath.Join(dir, "typesafe__jev-1.13-20260917.json")
	held := holdoutLines(200, 3, "ragtruth:qa-test@c103204", "openrouter.ai")
	report := filepath.Join(t.TempDir(), "holdout.json")

	code, out, stderr := runCLI(t, held, testEnv(), "--evaluator", "groundedness@1", "--holdout", "-", "--calibration", calPath, "--holdout-out", report)
	if code != exitOK {
		t.Fatalf("holdout: exit %d: %s%s", code, out, stderr)
	}
	for _, want := range []string{"Held-out check of the groundedness@1 calibration", "Held out: 200 labels", "Held out, not in-sample: ECE"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	var rep holdoutReport
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(calPath)
	sum := sha256.Sum256(raw)
	if rep.Schema != holdoutSchema || rep.Snapshot != snapshot || rep.CalibrationSHA256 != hex.EncodeToString(sum[:]) ||
		rep.CalibrationQuality != decision.QualityMeasured || strings.Join(rep.CalibrationDatasets, ",") != trainDataset ||
		rep.Holdout.N != 200 || rep.Holdout.NPos+rep.Holdout.NNeg != 200 || len(rep.NonVendorHosts) != 0 {
		t.Errorf("report %+v", rep)
	}

	// The numbers match an independent computation through the file's table.
	f := readFile(t, calPath)
	var risks, after []float64
	var fire []bool
	for _, line := range strings.Split(strings.TrimSpace(held), "\n") {
		var l struct {
			Risk  float64 `json:"risk_raw"`
			Label bool    `json:"label"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatal(err)
		}
		risks, fire = append(risks, l.Risk), append(fire, l.Label)
		after = append(after, decision.ApplyCalibration(f.Entries[0].Table, l.Risk))
	}
	if rep.Before.ECE == nil || math.Abs(*rep.Before.ECE-ece10(risks, fire)) > 1e-12 ||
		rep.After.ECE == nil || math.Abs(*rep.After.ECE-ece10(after, fire)) > 1e-12 {
		t.Errorf("ECE before %v after %v; independent %v, %v", rep.Before.ECE, rep.After.ECE, ece10(risks, fire), ece10(after, fire))
	}
	// The raw risk overstates the chance of firing; the calibration fitted on train corrects it.
	if *rep.After.ECE >= *rep.Before.ECE || *rep.After.Brier >= *rep.Before.Brier {
		t.Errorf("calibration did not help on the holdout: ECE %v → %v, Brier %v → %v", *rep.Before.ECE, *rep.After.ECE, *rep.Before.Brier, *rep.After.Brier)
	}
}

func TestCalibrateHoldoutRefusals(t *testing.T) {
	dir := t.TempDir()
	gate := writeTemp(t, "gate.jsonl", calibLines(400, 4, "openrouter.ai"))
	if code, _, stderr := runCLI(t, "", testEnv(), "--evaluator", "groundedness@1", "--in", gate, "--dataset", trainDataset, "--out", dir, "--write"); code != exitOK {
		t.Fatalf("fit: %s", stderr)
	}
	calPath := filepath.Join(dir, "typesafe__jev-1.13-20260917.json")
	cases := []struct {
		name, stdin string
		args        []string
		code        int
		want        string
	}{
		{"in-sample", holdoutLines(50, 5, trainDataset, "openrouter.ai"), nil, exitFailed, "would be in-sample"},
		{"other snapshot", strings.ReplaceAll(holdoutLines(50, 6, "ragtruth:qa-test@c", "openrouter.ai"), snapshot, "typesafe/jev-1.13-20261101"), nil, exitFailed, "one snapshot across calibration and bench"},
		{"no entry", holdoutLines(50, 7, "ragtruth:qa-test@c", "openrouter.ai"), []string{"--evaluator", "contradiction@1"}, exitUsage, "has no calibration for contradiction@1"},
		{"with --write", holdoutLines(5, 8, "ragtruth:qa-test@c", "openrouter.ai"), []string{"--write"}, exitUsage, "fits and writes nothing"},
		{"no labels", "", nil, exitUsage, "has no labelled groundedness@1 risks"},
	}
	for _, c := range cases {
		args := append([]string{"--evaluator", "groundedness@1", "--holdout", "-", "--calibration", calPath}, c.args...)
		code, _, stderr := runCLI(t, c.stdin, testEnv(), args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d (want %d): %s", c.name, code, c.code, stderr)
		}
	}

	// A holdout from a stand-in host is computed, and says it is simulated.
	code, out, stderr := runCLI(t, holdoutLines(60, 9, "ragtruth:qa-test@c", "127.0.0.1"), testEnv(), "--evaluator", "groundedness@1", "--holdout", "-", "--calibration", calPath)
	if code != exitOK || !strings.Contains(out, "came from 127.0.0.1, not a vendor host") {
		t.Errorf("stand-in holdout: exit %d: %s%s", code, out, stderr)
	}
}

func TestCalibrateRefusesRisksFromAStandInHost(t *testing.T) {
	dir := t.TempDir()
	gate := writeTemp(t, "gate.jsonl", calibLines(300, 10, "127.0.0.1"))
	args := []string{"--evaluator", "groundedness@1", "--in", gate, "--dataset", trainDataset, "--out", dir}

	code, out, stderr := runCLI(t, "", testEnv(), args...)
	if code != exitOK || !strings.Contains(out, "came from 127.0.0.1, not a vendor host") || !strings.Contains(out, "--write refuses them (D203)") {
		t.Errorf("dry run: exit %d: %s%s", code, out, stderr)
	}
	code, _, stderr = runCLI(t, "", testEnv(), append(args, "--write")...)
	if code != exitFailed || !strings.Contains(stderr, "refusing to write") || !strings.Contains(stderr, "openrouter.ai, api.typesafe.ai, api.anthropic.com") {
		t.Errorf("--write: exit %d: %s", code, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a stand-in calibration was written: %v", entries)
	}

	// A non-mock line that names no host (a gate-eval from before P6, or a host the stamp dropped)
	// is no proof that a vendor answered: refused as well (D203).
	hostless := calibLines(300, 11, "")
	code, out, stderr = runCLI(t, hostless, testEnv(), "--evaluator", "groundedness@1", "--out", dir, "--write")
	if code != exitFailed || !strings.Contains(stderr, "came from (no endpoint_host), not a vendor host") {
		t.Errorf("no host: exit %d: %s%s", code, out, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a calibration without a host was written: %v", entries)
	}

	// The mock backend has no host: its mock/* snapshot is written with quality mock, as before.
	mock := strings.NewReplacer(`"endpoint_host":"127.0.0.1"`, `"endpoint_host":null`, `"backend":"openrouter-decisions"`, `"backend":"mock"`,
		snapshot, "mock/jev-heuristic-v1").Replace(calibLines(300, 11, "127.0.0.1"))
	code, out, stderr = runCLI(t, mock, testEnv(), "--evaluator", "groundedness@1", "--out", dir, "--write")
	if code != exitOK || !strings.Contains(out, "Quality: mock") {
		t.Errorf("mock: exit %d: %s%s", code, out, stderr)
	}
}
