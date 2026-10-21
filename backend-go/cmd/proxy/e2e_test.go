package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

var (
	trafficgenOnce sync.Once
	trafficgenBin  string
	trafficgenErr  string
)

// buildTrafficgen builds cmd/trafficgen once per test run, so the e2e tests run the real binary
// exactly as the S0 exit gate does.
func buildTrafficgen(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go tool is not on PATH; skipping the trafficgen e2e test")
	}
	trafficgenOnce.Do(func() {
		dir, err := os.MkdirTemp("", "trafficgen-e2e-")
		if err != nil {
			trafficgenErr = err.Error()
			return
		}
		trafficgenBin = filepath.Join(dir, "trafficgen")
		out, err := exec.Command(goBin, "build", "-o", trafficgenBin, "../trafficgen").CombinedOutput()
		if err != nil {
			trafficgenErr = err.Error() + ": " + string(out)
		}
	})
	if trafficgenErr != "" {
		t.Fatalf("build trafficgen: %s", trafficgenErr)
	}
	return trafficgenBin
}

func TestMain(m *testing.M) {
	code := m.Run()
	if trafficgenBin != "" {
		_ = os.RemoveAll(filepath.Dir(trafficgenBin))
	}
	os.Exit(code)
}

// runTrafficgen runs `trafficgen --once --expect --scenarios s0` against env and fails the test
// with trafficgen's diff when a scenario does not match.
func runTrafficgen(t *testing.T, env *testEnv) string {
	t.Helper()
	cmd := exec.Command(buildTrafficgen(t), "--once", "--expect", "--scenarios", "s0", "--base-url", env.http.URL,
		"--audit-timeout", "30s")
	cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trafficgen --once --expect --scenarios s0: %v\n%s", err, out)
	}
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed"} {
		if !strings.Contains(string(out), "ok   "+id) {
			t.Errorf("trafficgen did not pass %s:\n%s", id, out)
		}
	}
	return string(out)
}

// TestTrafficgenExpectAgainstTheGateway is the S0 exit check in process: the real gateway (mock
// decision model, fake engine answering from the scenario fixtures) and the real trafficgen.
func TestTrafficgenExpectAgainstTheGateway(t *testing.T) {
	runTrafficgen(t, newTestEnv(t, envOpts{}))
}

// TestTrafficgenExpectAgainstPostgres runs the same check with the Postgres store, then reads
// every trace back and validates it against the contract schema.
func TestTrafficgenExpectAgainstPostgres(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	env := newTestEnv(t, envOpts{store: pg})
	runTrafficgen(t, env)

	code, body := env.get("/api/traces?route=support-bot", testKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaTracePage, body)
	var p page[store.TraceSummary]
	_ = json.Unmarshal(body, &p)
	if len(p.Items) != 3 {
		t.Fatalf("%d traces", len(p.Items))
	}
	for _, it := range p.Items {
		code, body := env.get("/api/traces/"+it.TraceID, testKey)
		if code != http.StatusOK {
			t.Fatalf("HTTP %d", code)
		}
		contractstest.ValidateJSON(t, schemaTrace, body)
	}
	for _, outcome := range []string{"passed", "flagged", "not_checked"} {
		_, body := env.get("/api/traces?outcome="+outcome, testKey)
		_ = json.Unmarshal(body, &p)
		if len(p.Items) != 1 {
			t.Errorf("outcome %s: %d traces", outcome, len(p.Items))
		}
	}
	code, body = env.get("/api/audits?verdict=HALLUCINATED", testKey)
	contractstest.ValidateJSON(t, schemaAuditPage, body)
	var audits page[store.AuditSummary]
	_ = json.Unmarshal(body, &audits)
	if code != http.StatusOK || len(audits.Items) != 1 || audits.Items[0].Selection == nil || *audits.Items[0].Selection != "high" {
		t.Errorf("audits HTTP %d %s", code, body)
	}
	checkAggregates(t, env)
	_, body = env.get("/api/status", testKey)
	contractstest.ValidateJSON(t, schemaStatus, body)
	if want := newestMigration(t); !strings.Contains(string(body), `"migration":"`+want+`"`) {
		t.Errorf("status names the newest migration %s: %s", want, body)
	}
}

// newestMigration is the last embedded migration file, which /api/status reports once applied.
func newestMigration(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "store", "migrations", "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	return filepath.Base(files[len(files)-1])
}

// checkAggregates reads the overview, ROI and evidence-coverage aggregates of the s0 traffic: all
// of it runs on the mock decision model, so it is counted (and labelled simulated) only with
// include_mock, and left out by default outside demo mode.
func checkAggregates(t *testing.T, env *testEnv) {
	t.Helper()
	strict := func(body []byte, dst any) {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			t.Fatalf("decode %T: %v\n%s", dst, err, body)
		}
	}
	code, body := env.get("/api/overview?window=1h&include_mock=true", testKey)
	if code != http.StatusOK {
		t.Fatalf("overview HTTP %d %s", code, body)
	}
	var o overviewStats
	strict(body, &o)
	if o.Totals.Requests != 3 || o.Totals.Flagged != 1 || !o.IncludeMock || o.Bucket != "1m" || len(o.OutcomesSeries) < 60 {
		t.Errorf("overview totals %+v bucket %s points %d", o.Totals, o.Bucket, len(o.OutcomesSeries))
	}
	if o.EvidenceCoverage.WithEvidence != 2 || o.EvidenceCoverage.GroundednessAssessed != 2 || o.EvidenceCoverage.Rate == nil ||
		o.EvidenceCoverage.BySource["request_system"] != 2 {
		t.Errorf("overview coverage %+v", o.EvidenceCoverage)
	}
	if o.HallucinationRate.Label != labelSimulated || o.HallucinationRate.Value != nil || o.HallucinationRate.MinN != minRateN {
		t.Errorf("hallucination rate %+v", o.HallucinationRate)
	}
	if len(o.ClaimVerdictsAmongAudited) != 6 || len(o.StageLatencyMs) != 9 || len(o.LatencyMs) != 4 {
		t.Errorf("verdicts %v stages %v latency %v", o.ClaimVerdictsAmongAudited, o.StageLatencyMs, o.LatencyMs)
	}
	code, body = env.get("/api/overview?window=1h", testKey)
	strict(body, &o)
	if code != http.StatusOK || o.Totals.Requests != 0 || o.IncludeMock {
		t.Errorf("without include_mock the simulated traffic is left out: HTTP %d %+v", code, o.Totals)
	}
	if code, _ := env.get("/api/overview?window=2d", testKey); code != http.StatusBadRequest {
		t.Errorf("an unknown window: HTTP %d", code)
	}

	code, body = env.get("/api/evidence/coverage?window=1h&include_mock=true", testKey)
	var cov evidenceCoverage
	strict(body, &cov)
	if code != http.StatusOK || cov.Requests != 3 || cov.WithEvidence != 2 || len(cov.TopReasons) != 1 || cov.TopReasons[0].Reason != "no_evidence" {
		t.Errorf("coverage HTTP %d %+v", code, cov)
	}

	code, body = env.get("/api/roi/summary?window=1h&include_mock=true", testKey)
	var r roiSummary
	strict(body, &r)
	if code != http.StatusOK || !r.Simulated || r.DecisionCost.Label != labelSimulated || r.Outcomes.Label != labelSimulated ||
		r.Outcomes.Flagged != 1 || len(r.Counterfactual) != 2 {
		t.Errorf("roi HTTP %d %+v", code, r)
	}
	// Over simulated traffic every counterfactual is simulated except the published full-audit
	// assumption, which stays an estimate (its basis says the answers are simulated).
	for _, cf := range r.Counterfactual {
		want := labelSimulated
		if cf.Method == "static_assumptions" {
			want = labelEstimate
		}
		if cf.Label != want {
			t.Errorf("counterfactual %s (%s) over simulated traffic is labelled %s, want %s", cf.Kind, cf.Method, cf.Label, want)
		}
	}
}
