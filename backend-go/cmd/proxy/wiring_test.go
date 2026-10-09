package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// TestSavedPolicyReachesTheGate: a policy saved through PUT /api/policies (admin, If-Match) is the
// policy the /v1 gate applies from the next request on, without a restart, and /api/status
// reports the new version.
func TestSavedPolicyReachesTheGate(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	const admin = "wiring-admin-key"
	env := newTestEnv(t, envOpts{store: pg, adminKey: admin})
	scenario := loadDemoScenarios(t)["grounded-pass"]

	if got := env.send(scenario).Header.Get(proxy.HeaderMode); got != "flag" {
		t.Fatalf("support-bot starts in flag mode, got %q", got)
	}

	code, body := env.get("/api/policies", admin)
	if code != http.StatusOK {
		t.Fatalf("GET /api/policies: HTTP %d %s", code, body)
	}
	var st struct {
		ID   int64  `json:"id"`
		ETag string `json:"etag"`
		YAML string `json:"yaml"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(st.YAML, "    mode: flag\n", "    mode: observe\n", 1)
	if edited == st.YAML {
		t.Fatal("support-bot's mode line was not found in the policy")
	}
	save, _ := json.Marshal(map[string]string{"yaml": edited, "message": "observe support-bot"})
	put := func(key string) (int, []byte) {
		r, _ := http.NewRequest(http.MethodPut, env.http.URL+"/api/policies", bytes.NewReader(save))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-TrustAgent-Key", key)
		r.Header.Set("If-Match", st.ETag)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, body := put(testKey); code != http.StatusForbidden {
		t.Errorf("an ingest/read key cannot save a policy: HTTP %d %s", code, body)
	}
	code, body = put(admin)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/policies: HTTP %d %s", code, body)
	}
	var saved struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(body, &saved)
	if saved.ID <= st.ID {
		t.Fatalf("the save made no new version: %s", body)
	}

	resp := env.send(scenario)
	if got := resp.Header.Get(proxy.HeaderMode); got != "observe" {
		t.Errorf("the next request runs on the saved policy: mode %q", got)
	}
	d := env.settledTrace(resp.Header.Get(proxy.HeaderTraceID))
	if d.PolicyVersion == nil || *d.PolicyVersion != saved.ID {
		t.Errorf("trace policy version %v, want %d", d.PolicyVersion, saved.ID)
	}
	_, body = env.get("/api/status", admin)
	var status struct {
		Policy statusPolicy `json:"policy"`
	}
	_ = json.Unmarshal(body, &status)
	if status.Policy.Version == nil || *status.Policy.Version != saved.ID || status.Policy.Source != "dashboard" || !status.Policy.Persisted {
		t.Errorf("status policy %+v", status.Policy)
	}
}

// TestKnowledgeEndpointsWhileTheAddOnIsOff: the knowledge-base endpoints answer 409
// knowledge_disabled (SPEC §2.4 rows 62-65) when the engine runs without the knowledge add-on,
// and the aggregates answer 503 persistence_disabled without a database.
func TestKnowledgeEndpointsWhileTheAddOnIsOff(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	for _, path := range []string{"/api/kb/stats", "/api/kb/claims", "/api/kb/conflicts"} {
		code, body := env.get(path, testKey)
		if code != http.StatusConflict || !strings.Contains(string(body), `"code":"knowledge_disabled"`) {
			t.Errorf("%s: HTTP %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/api/overview", "/api/roi/summary", "/api/evidence/coverage"} {
		code, body := env.get(path, testKey)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), `"code":"persistence_disabled"`) {
			t.Errorf("%s without a database: HTTP %d %s", path, code, body)
		}
	}
}

// TestWriteDeadlinePerRoute: with TRUTHTABLE_WRITE_TIMEOUT=0 each route family gets its own write
// deadline (SPEC M12): /v1 the upstream timeout + 30 s, the knowledge upload the ingest call's
// bound + 30 s, /ws none, and everything else 60 s.
func TestWriteDeadlinePerRoute(t *testing.T) {
	cfg := &config.Config{UpstreamTimeout: 5 * time.Minute}
	ingest := 8 * time.Minute
	for _, tc := range []struct {
		path   string
		ingest time.Duration
		want   time.Duration
	}{
		{"/v1/chat/completions", ingest, 5*time.Minute + 30*time.Second},
		{"/v1", ingest, 5*time.Minute + 30*time.Second},
		{"/v1beta", ingest, apiWriteDeadline},
		{"/api/kb/documents", ingest, 8*time.Minute + 30*time.Second},
		{"/api/kb/documents", 0, apiWriteDeadline}, // no engine: the upload answers 503 at once
		{"/api/upload", ingest, apiWriteDeadline},  // removed at the end of P3: an unknown path
		{"/api/policies/dry-run", ingest, apiWriteDeadline},
		{"/health", ingest, apiWriteDeadline},
		{"/ws", ingest, 0},
	} {
		if got := writeDeadline(cfg, tc.path, tc.ingest); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.path, got, tc.want)
		}
	}
	if got := writeDeadline(&config.Config{}, "/v1/chat/completions", ingest); got != 0 {
		t.Errorf("an upstream timeout of 0 sets no /v1 deadline, got %s", got)
	}
}

// The owner seams change nothing on the gated path: a gated request flows as before, every
// seam's route is served by its owner's handler, and the harness options the newer tests use work
// (a custom engine, a config mutation, the redacting log, WS replay frames).
func TestOwnerSeamsKeepTodaysBehaviour(t *testing.T) {
	const secret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm" // matches no redaction pattern
	var logs bytes.Buffer
	eng := scenarioEngine(t, false)
	env := newTestEnv(t, envOpts{publicRead: true, engine: eng, logs: &logs, mutate: func(c *config.Config) {
		c.Jev.APIKey = secret // a loaded secret joins the redactor
	}})
	if env.engine != eng || env.srv.rt == nil || env.srv.rt.manager.Current().Version != 0 || env.srv.apply == nil || env.srv.eval == nil {
		t.Fatalf("seams not wired: rt %v apply %v eval %v", env.srv.rt, env.srv.apply, env.srv.eval)
	}
	if got := env.srv.redact("key " + secret); strings.Contains(got, secret) {
		t.Errorf("server.redact kept a loaded secret: %q", got)
	}
	if h := env.srv.costs.headroom(true); h <= 0 {
		t.Errorf("without a ledger the decision headroom is unlimited, got %v", h)
	}
	settle, err := env.srv.costs.reserve(context.Background(), 1, "test")
	if err != nil {
		t.Fatalf("without a ledger nothing is reserved: %v", err)
	}
	settle(0.5)

	resp := env.send(loadDemoScenarios(t)["refund-contradiction-flag"])
	traceID := resp.Header.Get(proxy.HeaderTraceID)
	d := env.settledTrace(traceID)
	if d.Labels == nil || len(d.Labels) != 0 {
		t.Errorf("labels hook default: %v", d.Labels)
	}
	frames := env.wsFrames(t, 50)
	var sawTrace bool
	for _, f := range frames {
		if strings.Contains(string(f), `"trace_started"`) && strings.Contains(string(f), traceID) {
			sawTrace = true
		}
	}
	if !sawTrace {
		t.Errorf("wsFrames did not replay trace_started of %s (%d frames)", traceID, len(frames))
	}
	// Every seam has its owner's handler: /api/settings (SEC-5) and /api/credentials
	// (CN-7) since CP-SEC2+CP-SEC3, /api/labels/queue (EV-3) since CP-EVAL, /api/roi/timeseries
	// (COST-9) and /api/studio/samples (ST-4) since I-1. None answers the unmounted 404.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/settings"}, {http.MethodGet, "/api/credentials"}, {http.MethodGet, "/api/labels/queue"},
		{http.MethodGet, "/api/roi/timeseries"}, {http.MethodPost, "/api/studio/samples"},
	} {
		if code, body := env.do(route.method, route.path, testKey, []byte(`{}`)); code == http.StatusNotFound && strings.Contains(string(body), "No endpoint here") {
			t.Errorf("%s %s = the unknown-route 404: its owner's handler is not mounted", route.method, route.path)
		}
	}
	if !strings.Contains(env.logText(), "level=") || strings.Contains(env.logText(), secret) {
		t.Errorf("the redacting test log is empty or leaks the secret")
	}
}

// TestStatusWarnsAboutExcludedEvaluators: a file version that breaks a hard rule is left out of
// the registry and named in /api/status warnings (D54), while the other versions stay.
func TestStatusWarnsAboutExcludedEvaluators(t *testing.T) {
	const spec = `{"spec_version":"evaluator/v1","id":"tone","version":%d,"name":"Tone","category":"quality",
  "state_fields":["assistant_response"],"question":{"type":"noul","instructions":"%s","criteria":{"true":"Rude","false":"Polite"}},
  "evidence":{"requires":"none"},"signal":{"invert":false},"outcomes":{"warn_at":0.7},"changelog":"first"}`
	dir := t.TempDir()
	for name, body := range map[string]string{
		"tone-1.json": fmt.Sprintf(spec, 1, "Is it rude?"),
		"tone-2.json": fmt.Sprintf(spec, 2, "Is it rude about `evidence`?"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := newTestEnv(t, envOpts{mutate: func(c *config.Config) { c.EvaluatorsDir = dir }})
	if _, ok := env.srv.policies.Registry.Get("tone@1"); !ok {
		t.Fatal("tone@1 is clean and must load")
	}
	code, body := env.get("/api/status", testKey)
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var st struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	var found int
	for _, w := range st.Warnings {
		if strings.HasPrefix(w, "Evaluators: ") && strings.Contains(w, "tone@2 breaks the hard rule unresolved_state_reference") {
			found++
		}
	}
	if found != 1 {
		t.Errorf("want one evaluator warning for tone@2, got %q", st.Warnings)
	}
}
