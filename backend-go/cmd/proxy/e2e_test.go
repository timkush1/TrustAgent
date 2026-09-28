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
