package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/jev"
)

const (
	schemaDecisionModelTest = "api.v1.schema.json#/$defs/DecisionModelTestResult"
	dmTestAdminKey          = "dm-test-admin-key-0001"
	// dmUnmatchedSecret matches no redaction pattern (P4-PLAN §0 rule 8).
	dmUnmatchedSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
)

// postDecisionModelTest calls POST /api/decision-model/test with key and returns the response.
func postDecisionModelTest(t *testing.T, env *testEnv, key string) (*http.Response, []byte) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, env.http.URL+"/api/decision-model/test", nil)
	if key != "" {
		r.Header.Set("X-TrustAgent-Key", key)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp, b.Bytes()
}

func decodeDecisionModelTest(t *testing.T, body []byte) decisionModelTestResult {
	t.Helper()
	contractstest.ValidateJSON(t, schemaDecisionModelTest, body)
	var res decisionModelTestResult
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatal(err)
	}
	// The probe's answer is never shown (SPEC §2.4 row 60): exactly the result's fields.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	for k := range raw {
		switch k {
		case "ok", "backend", "endpoint_host", "model_requested", "snapshot", "latency_ms", "input_tokens", "cost_usd", "cost_source", "error":
		default:
			t.Errorf("unexpected field %q in the test result", k)
		}
	}
	if bytes.Contains(body, []byte("answers")) || bytes.Contains(body, []byte(`"probe"`)) {
		t.Errorf("the result echoes the probe: %s", body)
	}
	return res
}

// mockUpstreamJev points the decision model at an in-process mock-upstream (OpenRouter) with key.
func mockUpstreamJev(t *testing.T, key string) (*mockupstream.Server, func(*config.Config)) {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(mu)
	t.Cleanup(hs.Close)
	return mu, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.OpenRouterKey, cfg.Jev.OpenRouterBaseURL = jev.BackendOpenRouterDecisions, key, hs.URL
		cfg.Jev.KeepAlive = 0
	}
}

func TestDecisionModelTestOK(t *testing.T) {
	mu, mutate := mockUpstreamJev(t, "sk-or-v1-e2e-good-0123456789abcdef")
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, mutate: mutate})
	before := len(mu.Requests())
	resp, body := postDecisionModelTest(t, env, dmTestAdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	res := decodeDecisionModelTest(t, body)
	if !res.OK || res.Error != nil || res.Backend != jev.BackendOpenRouterDecisions {
		t.Fatalf("result = %s, want ok on openrouter-decisions", body)
	}
	if res.EndpointHost == nil || *res.EndpointHost != "127.0.0.1" || res.ModelRequested == nil || *res.ModelRequested != jev.DefaultOpenRouterModel {
		t.Errorf("endpoint/model = %v %v", res.EndpointHost, res.ModelRequested)
	}
	if res.Snapshot == nil || *res.Snapshot == "" || res.InputTokens == nil || res.LatencyMs == nil {
		t.Errorf("probe details missing: %s", body)
	}
	if res.CostUSD == nil || *res.CostUSD <= 0 || res.CostSource == nil || *res.CostSource != "reported" {
		t.Errorf("cost = %v %v, want the reported probe cost", res.CostUSD, res.CostSource)
	}
	reqs := mu.Requests()[before:]
	if len(reqs) != 1 || reqs[0].Path != "/api/alpha/decisions" {
		t.Errorf("mock-upstream saw %d requests, want one decision call", len(reqs))
	}
}

func TestDecisionModelTestSimulated(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey})
	resp, body := postDecisionModelTest(t, env, dmTestAdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	res := decodeDecisionModelTest(t, body)
	if !res.OK || res.Backend != jev.BackendMock || res.EndpointHost != nil {
		t.Fatalf("mock backend: %s", body)
	}
	if res.CostSource == nil || *res.CostSource != "simulated" {
		t.Errorf("a mock probe's cost must be labelled simulated: %s", body)
	}
}

func TestDecisionModelTestNoCredits(t *testing.T) {
	_, mutate := mockUpstreamJev(t, "sk-or-v1-e2e-nocredits-0123456789")
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, mutate: mutate})
	resp, body := postDecisionModelTest(t, env, dmTestAdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	res := decodeDecisionModelTest(t, body)
	if res.OK || res.Error == nil || res.Error.Kind != jev.KindCredits || res.Error.Status == nil || *res.Error.Status != http.StatusPaymentRequired {
		t.Fatalf("result = %s, want a 402 credits error", body)
	}
	if res.Error.Message == "" || res.Error.Hint == "" || res.CostUSD != nil || res.Snapshot != nil || res.LatencyMs == nil {
		t.Errorf("error details: %s", body)
	}
	// The startup probe already met the 402 and opened the breaker; the admin's test still
	// reached the vendor (it closes the breaker first), and the failure opened it again.
	if st := env.srv.gateway.client.(jev.StatusReporter).Status(); st.Breaker != jev.BreakerOpen {
		t.Errorf("breaker after a failed test = %s, want open", st.Breaker)
	}
	if strings.Contains(string(body), "nocredits-0123456789") {
		t.Error("the result carries the key")
	}
}

// TestDecisionModelTestKeepsAFaultBreakerOpen: a breaker that vendor faults opened stays open. The
// test answers breaker_open without calling the vendor, so an admin's check never sends live
// traffic back to a failing vendor (a key or credits pause is lifted: TestDecisionModelTestNoCredits).
func TestDecisionModelTestKeepsAFaultBreakerOpen(t *testing.T) {
	var calls atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 500, "message": "upstream down"}})
	}))
	defer vendor.Close()
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, mutate: func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.OpenRouterKey, cfg.Jev.OpenRouterBaseURL = jev.BackendOpenRouterDecisions, "sk-or-v1-e2e-good-0123456789", vendor.URL
		cfg.Jev.KeepAlive, cfg.Jev.MaxRetries, cfg.Jev.StartupProbe = 0, 0, false
		cfg.Jev.BreakerFailures, cfg.Jev.BreakerCooldown = 1, time.Hour
	}})
	client := env.srv.gateway.client
	if _, err := jev.Probe(context.Background(), client); err == nil {
		t.Fatal("a probe against a failing vendor succeeded")
	}
	if st := client.(jev.StatusReporter).Status(); st.Breaker != jev.BreakerOpen || st.State != "breaker_open" {
		t.Fatalf("after a vendor fault: breaker %s, state %s; want open, breaker_open", st.Breaker, st.State)
	}
	before := calls.Load()
	resp, body := postDecisionModelTest(t, env, dmTestAdminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	res := decodeDecisionModelTest(t, body)
	if res.OK || res.Error == nil || res.Error.Kind != jev.KindBreakerOpen || res.LatencyMs != nil {
		t.Fatalf("result = %s, want breaker_open with no latency (nothing was sent)", body)
	}
	if calls.Load() != before {
		t.Error("the test called a vendor the breaker had paused")
	}
	if st := client.(jev.StatusReporter).Status(); st.Breaker != jev.BreakerOpen {
		t.Errorf("breaker after the test = %s, want still open", st.Breaker)
	}
}

func TestDecisionModelTestOff(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, mutate: func(cfg *config.Config) { cfg.Jev.Backend = jev.BackendOff }})
	_, body := postDecisionModelTest(t, env, dmTestAdminKey)
	res := decodeDecisionModelTest(t, body)
	if res.OK || res.Backend != jev.BackendOff || res.Error == nil || res.Error.Kind != jev.KindOff || res.LatencyMs != nil {
		t.Fatalf("off backend: %s, want ok:false, kind off and no latency (nothing was sent)", body)
	}
}

// TestDecisionModelTestRedactsErrorText: a vendor that echoes a secret the process knows (here
// the admin key, which matches no pattern) gets it masked in the result.
func TestDecisionModelTestRedactsErrorText(t *testing.T) {
	if got := config.NewRedactor().Redact(dmUnmatchedSecret); got != dmUnmatchedSecret {
		t.Fatalf("the redaction patterns mask the unmatched secret (%q)", got)
	}
	// The vendor echoes the secret only once the process knows it, as it would for a key that
	// joined the redactor when it was received or decrypted (D42).
	var echo atomic.Bool
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		msg := "bad request"
		if echo.Load() {
			msg += " near " + dmUnmatchedSecret + " " + strings.Repeat("x", 600)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "message": msg}})
	}))
	defer vendor.Close()
	var logs bytes.Buffer
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, logs: &logs, mutate: func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.OpenRouterKey, cfg.Jev.OpenRouterBaseURL = jev.BackendOpenRouterDecisions, "sk-or-v1-e2e-good-0123456789", vendor.URL
		cfg.Jev.KeepAlive, cfg.Jev.MaxRetries = 0, 0
	}})
	env.srv.redactor.Add(dmUnmatchedSecret)
	echo.Store(true)
	_, body := postDecisionModelTest(t, env, dmTestAdminKey)
	res := decodeDecisionModelTest(t, body)
	if res.OK || res.Error == nil {
		t.Fatalf("result = %s, want an error", body)
	}
	if strings.Contains(res.Error.Message, dmUnmatchedSecret) || !strings.Contains(res.Error.Message, config.Masked) {
		t.Errorf("message not redacted: %q", res.Error.Message)
	}
	if n := len([]rune(res.Error.Message)); n > maxTestMessage {
		t.Errorf("message has %d characters, want at most %d", n, maxTestMessage)
	}
	if strings.Contains(env.logText(), dmUnmatchedSecret) {
		t.Error("the logs carry the secret")
	}
}

// TestDecisionModelTestDropsAKeyTheVendorMessageCut (D40, D42): the decision client cuts a vendor
// message to 300 characters before it masks the key it sent, so a key the vendor echoes across the
// cut reaches the handler as a bare prefix that no exact-secret redaction recognises. The result
// drops the word the cut left, whatever secret it belonged to.
func TestDecisionModelTestDropsAKeyTheVendorMessageCut(t *testing.T) {
	if got := config.NewRedactor().Redact(dmUnmatchedSecret); got != dmUnmatchedSecret {
		t.Fatalf("the redaction patterns mask the unmatched secret (%q)", got)
	}
	// 290 characters and a space, then the key: the 300-character cut keeps its first 9.
	padding := strings.Repeat("x", 290)
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400,
			"message": padding + " " + dmUnmatchedSecret + " is not a valid key"}})
	}))
	defer vendor.Close()
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, mutate: func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, vendor.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = dmUnmatchedSecret, "typesafe/jev-1.13"
		cfg.Jev.KeepAlive, cfg.Jev.MaxRetries, cfg.Jev.StartupProbe = 0, 0, false
	}})
	_, body := postDecisionModelTest(t, env, dmTestAdminKey)
	res := decodeDecisionModelTest(t, body)
	if res.OK || res.Error == nil {
		t.Fatalf("result = %s, want an error", body)
	}
	if strings.Contains(res.Error.Message, dmUnmatchedSecret[:4]) {
		t.Error("the message carries a prefix of the key")
	}
	if want := padding + "…"; res.Error.Message != want {
		t.Errorf("message has %d characters, want the vendor's text up to the cut word (%d)",
			len([]rune(res.Error.Message)), len([]rune(want)))
	}
}

func TestDropCutWord(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"bad key", "bad key"},              // not cut: kept whole
		{"bad key cmp0e2eZq…", "bad key…"},  // the cut word goes
		{"bad key\tcmp0e2eZq…", "bad key…"}, // after any space
		{"bad key  [redacted] near cmp0e…", "bad key  [redacted] near…"},
		{"cmp0e2eZq…", "…"}, // a message of one cut word keeps nothing of it
		{"…", "…"},
	} {
		if got := dropCutWord(c.in); got != c.want {
			t.Errorf("dropCutWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecisionModelTestGuards(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: dmTestAdminKey})
	if resp, _ := postDecisionModelTest(t, env, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no key: %d, want 401", resp.StatusCode)
	}
	if resp, body := postDecisionModelTest(t, env, testKey); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin key: %d %s, want 403", resp.StatusCode, body)
	}
	// One probe per caller every 10 s.
	if resp, body := postDecisionModelTest(t, env, dmTestAdminKey); resp.StatusCode != http.StatusOK {
		t.Fatalf("first test: %d %s", resp.StatusCode, body)
	}
	resp, body := postDecisionModelTest(t, env, dmTestAdminKey)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("second test within 10 s: %d (Retry-After %q) %s, want 429 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}

	ro := newTestEnv(t, envOpts{adminKey: dmTestAdminKey, readonly: true})
	if resp, body := postDecisionModelTest(t, ro, dmTestAdminKey); resp.StatusCode != http.StatusForbidden || errorCodeOf(t, body) != "readonly" {
		t.Errorf("read-only mode: %d %s, want 403 readonly", resp.StatusCode, body)
	}
}
