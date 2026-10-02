package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
)

// The credential leak test (P4-PLAN SEC-4, SEC-8; D42): a secret that matches no redaction
// pattern (noPatternSecret, so only the list of loaded secrets can mask it) must never reach a
// log line, a REST body or header, or a WS frame, even when a vendor echoes it back. The test
// grows with each endpoint; this file holds the variant that needs no database.

// leakAdminKey is the admin env key of the leak tests.
const leakAdminKey = "leak-admin-env-key"

// leakSink collects everything the server sends out, keyed by where it came from, and asserts that
// a secret appears in none of it. Failure messages name the place, never the secret or the text.
type leakSink struct {
	t       *testing.T
	sources []string
	texts   []string
}

func (l *leakSink) add(source string, text []byte) {
	l.sources = append(l.sources, source)
	l.texts = append(l.texts, string(text))
}

// addResponse records a response's status line, headers and body.
func (l *leakSink) addResponse(source string, resp *http.Response, body []byte) {
	var b bytes.Buffer
	_ = resp.Header.Write(&b)
	b.Write(body)
	l.add(source, b.Bytes())
}

// assertAbsent fails for every recorded text that contains secret.
func (l *leakSink) assertAbsent(secret string) {
	l.t.Helper()
	if len(l.texts) == 0 {
		l.t.Fatal("nothing was collected")
	}
	for i, text := range l.texts {
		if strings.Contains(text, secret) {
			l.t.Errorf("the secret leaks in %s", l.sources[i])
		}
	}
}

// call sends a request with the X-TrustAgent-Key key and records the response.
func (l *leakSink) call(env *testEnv, method, path, key string, body []byte, headers ...string) (int, []byte) {
	l.t.Helper()
	req, _ := http.NewRequest(method, env.http.URL+path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("X-TrustAgent-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	l.addResponse(method+" "+path+" as "+keyRole(key), resp, b)
	return resp.StatusCode, b
}

func keyRole(key string) string {
	switch key {
	case leakAdminKey:
		return "admin"
	case testKey:
		return "reader"
	case "":
		return "anonymous"
	}
	return "other"
}

// echoVendor is a decision-model vendor that refuses every call and echoes the key it was sent
// in its body and its headers, the way some gateways do in their error pages.
func echoVendor(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
		w.Header().Set("X-Echo-Key", key)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", key="`+key+`"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"invalid api key %s","code":401,"key":%q}}`, key, key)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

// TestCredentialLeakWithoutDatabase: an env decision-model key (TRUTHTABLE_JEV_API_KEY) that a
// vendor echoes in its 401 body and headers stays out of the log, every REST body and header
// (status, settings, traces, sessions), the /v1 responses and the WS frames (D42).
func TestCredentialLeakWithoutDatabase(t *testing.T) {
	if config.NewRedactor().Redact(noPatternSecret) != noPatternSecret {
		t.Fatal("the leak secret must match no redaction pattern, so only the loaded-secret list can mask it")
	}
	vendor, calls := echoVendor(t)
	t.Setenv("TRUTHTABLE_JEV_API_KEY", noPatternSecret)
	var logs bytes.Buffer
	env := newTestEnv(t, envOpts{logs: &logs, adminKey: leakAdminKey, mutate: func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Model = jev.BackendCompatible, vendor.URL, "jev-leak-test"
	}})
	defer installRedaction(env.srv.redactor)()
	sink := &leakSink{t: t}

	// Gated traffic: every answer asks the decision model, which the vendor refuses.
	scenarios := loadDemoScenarios(t)
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag"} {
		s := scenarios[id]
		if s == nil {
			t.Fatalf("scenario %s is missing", id)
		}
		var req map[string]json.RawMessage
		if err := json.Unmarshal(s.Request, &req); err != nil {
			t.Fatal(err)
		}
		req["test_response"], _ = json.Marshal(s.Response)
		body, _ := json.Marshal(req)
		code, _ := sink.call(env, http.MethodPost, "/v1/chat/completions", testKey, body)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", id, code)
		}
	}
	if calls.Load() == 0 {
		t.Fatal("the vendor was never called, so the test proves nothing")
	}
	// The refusal reaches the decision model's status: its last error is what the vendor said,
	// with the echoed key masked. This proves the echo travelled the path the test watches.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var st struct {
			DecisionModel struct {
				State     string `json:"state"`
				LastError *struct {
					Message string `json:"message"`
				} `json:"last_error"`
			} `json:"decision_model"`
		}
		_, body := env.get("/api/status", leakAdminKey)
		_ = json.Unmarshal(body, &st)
		if e := st.DecisionModel.LastError; st.DecisionModel.State == "unauthorized" && e != nil &&
			strings.Contains(e.Message, "invalid api key "+config.Masked) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the decision model never reported the refusal (state %q)", st.DecisionModel.State)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, key := range []string{leakAdminKey, testKey, ""} {
		for _, path := range []string{"/health", "/health/ready", "/api/setup/status", "/api/session", "/api/status",
			"/api/settings", "/api/traces?limit=200", "/api/audits?limit=200", "/api/gate/decisions?limit=200",
			"/api/policies", "/api/evaluators", "/api/pricing", "/api/keys"} {
			sink.call(env, http.MethodGet, path, key, nil)
		}
	}
	_, body := sink.call(env, http.MethodGet, "/api/traces?limit=200", leakAdminKey, nil)
	var traces struct {
		Items []struct {
			TraceID string `json:"trace_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &traces); err != nil || len(traces.Items) == 0 {
		t.Fatalf("no traces were recorded (%v)", err)
	}
	for _, tr := range traces.Items {
		sink.call(env, http.MethodGet, "/api/traces/"+tr.TraceID, leakAdminKey, nil)
	}
	// A settings write without a database is refused; its error carries no secret either.
	sink.call(env, http.MethodPatch, "/api/settings", leakAdminKey, []byte(`{"budgets":{"llm_usd_day":1}}`), "If-Match", `"s0"`)
	for i, f := range env.wsFrames(t, 500) {
		sink.add(fmt.Sprintf("WS frame %d", i), f)
	}
	sink.add("the log", []byte(env.logText()))
	sink.assertAbsent(noPatternSecret)
}

// TestRESTErrorsPassThroughTheProcessRedactor: abortAPI masks the loaded secrets of the installed
// Redactor and, before one is installed, the credential patterns (D42).
func TestRESTErrorsPassThroughTheProcessRedactor(t *testing.T) {
	if got := apiRedact("upstream said: Bearer sk-test-0123456789abcdef"); strings.Contains(got, "sk-test-0123456789abcdef") {
		t.Errorf("the patterns apply before a Redactor is installed: %q", got)
	}
	if got := apiRedact("echo " + noPatternSecret); !strings.Contains(got, noPatternSecret) {
		t.Fatal("without the Redactor the no-pattern secret is not known (the next check would prove nothing)")
	}
	restore := installRedaction(config.NewRedactor(noPatternSecret))
	defer restore()
	if got := apiRedact("echo " + noPatternSecret); strings.Contains(got, noPatternSecret) {
		t.Error("the installed Redactor masks a loaded secret")
	}
	s := &server{redactor: config.NewRedactor(noPatternSecret)}
	if got := s.redactErr(fmt.Errorf("engine: %s refused", noPatternSecret)); strings.Contains(got, noPatternSecret) {
		t.Error("redactErr masks a secret in an error")
	}
	grpcErr := status.Error(codes.FailedPrecondition, "no_judge: the key "+noPatternSecret+" was refused")
	if got := s.redactErr(grpcErr); got != "no_judge: the key "+config.Masked+" was refused" {
		t.Errorf("a gRPC error gives its status message, redacted: %q", strings.ReplaceAll(got, noPatternSecret, "<secret>"))
	}
	if s.redactErr(nil) != "" {
		t.Error("redactErr(nil) is empty")
	}
}
