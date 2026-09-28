package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
		{"/api/upload", ingest, 8*time.Minute + 30*time.Second},
		{"/api/upload", 0, apiWriteDeadline}, // no engine: the upload answers 503 at once
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
