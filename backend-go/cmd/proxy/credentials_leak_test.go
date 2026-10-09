package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/jev"
)

// The credential leak test (SEC-4, SEC-8; D42): a secret that matches no redaction
// pattern (noPatternSecret, so only the list of loaded secrets can mask it) must never reach a
// log line, a REST body or header, or a WS frame, even when a vendor echoes it back. The test
// grows with each endpoint: TestCredentialLeakWithoutDatabase needs no database, and
// TestCredentialLeakWithPostgres covers the credential store, Connect OpenRouter and the admin
// audit log.

// leakAdminKey is the admin env key of the leak tests; leakSecondAdminKey is another admin (the
// decision-model test allows one call per caller every 10 s).
const (
	leakAdminKey       = "leak-admin-env-key"
	leakSecondAdminKey = "leak-admin-env-key-2"
)

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
	case leakSecondAdminKey:
		return "second admin"
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
	t.Setenv("OPENROUTER_API_KEY", leakOpenRouterKey) // an env row that nothing calls
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
	waitDecisionRefusal(t, env)

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
	// The credential endpoints without a database (D29, D31, D80): the env key is checked again
	// against the refusing vendor (one check per name every 10 s), a bind of an env key is
	// env_managed, a name no source holds is not found, and a write that would store a key is
	// persistence_disabled. No answer carries a secret.
	for _, tc := range []struct {
		method, path, body string
		want               int
		code               string // the error code, or the schema of a 200 body
	}{
		{http.MethodGet, "/api/credentials", "", http.StatusOK, schemaCredentialList},
		{http.MethodPost, "/api/credentials/compatible/verify", `{}`, http.StatusOK, schemaCredentialWriteResult},
		{http.MethodPost, "/api/credentials/openrouter/verify", `{"bind":["judge"]}`, http.StatusConflict, codeEnvManaged},
		{http.MethodPost, "/api/credentials/typesafe/verify", `{}`, http.StatusNotFound, "not_found"},
		{http.MethodPut, "/api/credentials/compatible", `{"provider":"generic","secret":"sk-test-leak-0123456789","base_url":"https://llm.example.com/v1"}`,
			http.StatusConflict, codeEnvManaged},
		{http.MethodPut, "/api/credentials/upstream-fallback", `{"provider":"generic","secret":"sk-test-leak-0123456789","base_url":"https://llm.example.com/v1"}`,
			http.StatusServiceUnavailable, codePersistenceDisabled},
		{http.MethodDelete, "/api/credentials/upstream-fallback", "", http.StatusServiceUnavailable, codePersistenceDisabled},
	} {
		var body []byte
		if tc.body != "" {
			body = []byte(tc.body)
		}
		code, out := sink.call(env, tc.method, tc.path, leakAdminKey, body)
		if code != tc.want {
			t.Errorf("%s %s %s: HTTP %d, want %d", tc.method, tc.path, tc.body, code, tc.want)
			continue
		}
		if code != http.StatusOK {
			if got := errorCodeOf(t, out); got != tc.code {
				t.Errorf("%s %s %s: code %s, want %s", tc.method, tc.path, tc.body, got, tc.code)
			}
			continue
		}
		contractstest.ValidateJSON(t, tc.code, out)
		var res struct {
			Verification *struct {
				Status string `json:"status"`
			} `json:"verification"`
		}
		if tc.method == http.MethodPost && (json.Unmarshal(out, &res) != nil || res.Verification == nil || res.Verification.Status != "unauthorized") {
			t.Error("the env key's check did not hear the vendor's refusal, so its echo never reached the answer")
		}
	}
	refusal := false
	for i, f := range env.wsFrames(t, 500) {
		sink.add(fmt.Sprintf("WS frame %d", i), f)
		refusal = refusal || isDecisionRefusalFrame(f)
	}
	if !refusal {
		t.Fatal("no WS status frame carried the decision model's refusal, so the echo never reached the stream")
	}
	sink.add("the log", []byte(env.logText()))
	sink.assertAbsent(noPatternSecret)
	sink.assertAbsent(leakOpenRouterKey)
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

// The secrets of the Postgres leak test. The no-pattern ones match no redaction pattern, so only
// the list of loaded secrets can mask them: the dashboard key pasted through #55, the key Connect
// OpenRouter exchanges, and the gateway's own env upstream key. The sk-or-v1-e2e key is the shape
// a real OpenRouter key has.
const (
	leakPKCESecret     = "pkc0e2eHt5Jk8Lm2Np4Qr6Sv"
	leakUpstreamSecret = "ups0e2eWx3Yz5Ab7Cd9Ef1Gh"
	leakOpenRouterKey  = "sk-or-v1-e2e-leak-5d7c9e1f3a2b4c6d"
)

// leakVendor is an OpenRouter double: mock-upstream, except that the PKCE exchange hands out
// exchanged (a key that matches no pattern) and, while refusing, every call that carries a key
// is answered 401 with the key echoed in the body and the headers. It remembers which keys it
// received and which it echoed, compared by value and never printed.
type leakVendor struct {
	srv    *httptest.Server
	refuse atomic.Bool
	mu     sync.Mutex
	seen   map[string]bool
	echoed map[string]bool
}

func newLeakVendor(t *testing.T, exchanged string) *leakVendor {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	v := &leakVendor{seen: map[string]bool{}, echoed: map[string]bool{}}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
		if key == "" {
			key = r.Header.Get("X-Api-Key")
		}
		if key == "" {
			key = r.Header.Get("Api-Key")
		}
		if key != "" {
			v.note(v.seen, key)
		}
		if key != "" && v.refuse.Load() {
			v.note(v.echoed, key)
			w.Header().Set("X-Echo-Key", key)
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", key="`+key+`"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error":{"message":"invalid api key %s","code":401,"key":%q}}`, key, key)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/keys" {
			mu.ServeHTTP(w, r)
			return
		}
		// The exchange: mock-upstream checks the code and the verifier; the key it hands out is
		// replaced with the no-pattern one.
		rec := httptest.NewRecorder()
		mu.ServeHTTP(rec, r)
		var out map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		out["key"] = exchanged
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func (v *leakVendor) note(set map[string]bool, key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	set[key] = true
}

// got reports whether the vendor received key, and echoed it back.
func (v *leakVendor) got(key string) (received, echoed bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.seen[key], v.echoed[key]
}

// TestCredentialLeakWithPostgres (SEC-8, D42): with the credential store on Postgres, a
// pasted key (#55), a key from Connect OpenRouter (#58, #59) and the gateway's env upstream key,
// each echoed back by the vendor that refuses it, never reach the log, a body or header of #8,
// #11, #52-#61 (with the Playground's SSE error event), #67-#70, /metrics, a WS frame or a row of
// admin_audit_log or cost_events. The credential checks book their probes as the component probe
// (D82).
func TestCredentialLeakWithPostgres(t *testing.T) {
	secrets := []string{noPatternSecret, leakPKCESecret, leakUpstreamSecret, leakOpenRouterKey}
	patterns := config.NewRedactor()
	for _, s := range secrets[:3] {
		if patterns.Redact(s) != s {
			t.Fatal("a no-pattern leak secret matches a redaction pattern, so a leak of it could be hidden")
		}
	}
	vendor := newLeakVendor(t, leakPKCESecret)
	t.Setenv("TRUTHTABLE_UPSTREAM_API_KEY", leakUpstreamSecret) // an env key, as an operator sets it
	var logs bytes.Buffer
	env := newSettingsEnv(t, envOpts{logs: &logs, adminKey: leakAdminKey, mutate: func(c *config.Config) {
		c.Jev.OpenRouterBaseURL, c.Jev.KeepAlive = vendor.srv.URL, 0
		c.Jev.OpenRouterKey, c.Jev.TypeSafeKey, c.Jev.APIKey = "", "", ""
		// Chat-mode Playground runs go to the vendor with the gateway's own key.
		c.UpstreamURL, c.UpstreamAPIKey = vendor.srv.URL, leakUpstreamSecret
		c.AllowedOrigins = append(slices.Clone(c.AllowedOrigins), connectOrigin)
		c.Auth.AdminKeys = append(c.Auth.AdminKeys, config.APIKey{Label: "admin-2", Key: leakSecondAdminKey})
	}})
	defer installRedaction(env.srv.redactor)()
	sink := &leakSink{t: t}
	mustCallAs := func(key string, want int, method, path string, body any, headers ...string) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		code, b := sink.call(env.testEnv, method, path, key, raw, headers...)
		if code != want {
			t.Fatalf("%s %s: HTTP %d, want %d (%s)", method, path, code, want, errorCodeOf(t, b))
		}
		return b
	}
	mustCall := func(want int, method, path string, body any, headers ...string) []byte {
		t.Helper()
		return mustCallAs(leakAdminKey, want, method, path, body, headers...)
	}

	// patchSettings is #53 on the current version: it reads the ETag first.
	patchSettings := func(want int, patch map[string]any) {
		t.Helper()
		var view struct {
			ETag string `json:"etag"`
		}
		if err := json.Unmarshal(mustCall(http.StatusOK, http.MethodGet, "/api/settings", nil), &view); err != nil {
			t.Fatal(err)
		}
		mustCall(want, http.MethodPatch, "/api/settings", patch, "If-Match", view.ETag)
	}

	// #55: a compatible key pasted without a check, then #57 checks it, with the model the settings
	// name (#53), against a vendor that refuses and echoes it.
	mustCall(http.StatusOK, http.MethodPut, "/api/credentials/compatible",
		map[string]any{"provider": "generic", "secret": noPatternSecret, "base_url": vendor.srv.URL + "/v1", "verify": false})
	patchSettings(http.StatusOK, map[string]any{"decision_model": map[string]any{"model": "jev-leak-test"}})
	vendor.refuse.Store(true)
	mustCall(http.StatusOK, http.MethodPost, "/api/credentials/compatible/verify", map[string]any{})
	vendor.refuse.Store(false)
	patchSettings(http.StatusOK, map[string]any{"decision_model": map[string]any{"model": nil}})
	// An OpenRouter key, checked and bound to the fast check and the judge; #60 tests it against
	// the refusing vendor.
	vendor.refuse.Store(false)
	mustCall(http.StatusOK, http.MethodPut, "/api/credentials/openrouter",
		map[string]any{"provider": "openrouter", "secret": leakOpenRouterKey, "bind": []string{"decision_model", "judge"}})
	vendor.refuse.Store(true)
	mustCall(http.StatusOK, http.MethodPost, "/api/decision-model/test", nil)
	vendor.refuse.Store(false)

	// #58, #59: Connect OpenRouter replaces it with the exchanged key.
	var start connectStartResult
	if err := json.Unmarshal(mustCall(http.StatusOK, http.MethodPost, "/api/connect/openrouter/start", map[string]any{}, "Origin", connectOrigin), &start); err != nil {
		t.Fatal(err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(start.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cb, err := resp.Location()
	if err != nil || cb.Query().Get("code") == "" {
		t.Fatalf("the sign-in did not return to the callback with a code (%d, %v)", resp.StatusCode, err)
	}
	var done struct {
		Stored       bool `json:"stored"`
		Verification *struct {
			OK bool `json:"ok"`
		} `json:"verification"`
	}
	b := mustCall(http.StatusOK, http.MethodPost, "/api/connect/openrouter/complete",
		map[string]any{"code": cb.Query().Get("code"), "nonce": start.Nonce}, "Origin", connectOrigin)
	if err := json.Unmarshal(b, &done); err != nil || !done.Stored || done.Verification == nil || !done.Verification.OK {
		t.Fatalf("Connect OpenRouter did not store a checked key (%v)", err)
	}

	// The vendor now refuses the exchanged key: gated traffic, #60 and #57 all hear it echoed.
	vendor.refuse.Store(true)
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag"} {
		s := loadDemoScenarios(t)[id]
		var req map[string]json.RawMessage
		if err := json.Unmarshal(s.Request, &req); err != nil {
			t.Fatal(err)
		}
		req["test_response"], _ = json.Marshal(s.Response)
		body, _ := json.Marshal(req)
		if code, _ := sink.call(env.testEnv, http.MethodPost, "/v1/chat/completions", testKey, body); code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", id, code)
		}
	}
	waitDecisionRefusal(t, env.testEnv)
	mustCallAs(leakSecondAdminKey, http.StatusOK, http.MethodPost, "/api/decision-model/test", nil)
	mustCall(http.StatusOK, http.MethodPost, "/api/credentials/openrouter/verify", map[string]any{})
	// #61: a streamed chat-mode run whose upstream refuses the gateway's key ends with an error event.
	run := playRun(t, "refund-contradiction-flag", "chat")
	run["stream"] = true
	if b := mustCall(http.StatusOK, http.MethodPost, "/api/playground/run", run); !bytes.Contains(b, []byte("event: error")) {
		t.Fatal("the refused Playground run did not end with an SSE error event")
	}
	// The /v1 relay (inject mode): the upstream refuses the gateway's own key and echoes it in its
	// body and headers; the client gets the upstream's 401 with both masked (GW-6, D42).
	if code, _ := sink.call(env.testEnv, http.MethodPost, "/v1/chat/completions", testKey,
		[]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)); code != http.StatusUnauthorized {
		t.Fatalf("the refused /v1 call: HTTP %d, want the upstream's 401", code)
	}
	vendor.refuse.Store(false)
	for i, s := range secrets {
		if received, echoed := vendor.got(s); !received || (s != leakOpenRouterKey && !echoed) {
			t.Fatalf("leak secret #%d did not travel the path the test watches (received %v, echoed %v)", i, received, echoed)
		}
	}
	if _, echoed := vendor.got(leakOpenRouterKey); !echoed {
		t.Fatal("the OpenRouter key was never echoed back")
	}

	// #52, #53: the settings view for every caller; a write, and a refused one whose URL carries a
	// secret in its userinfo.
	for _, key := range []string{leakAdminKey, testKey, ""} {
		sink.call(env.testEnv, http.MethodGet, "/api/settings", key, nil)
	}
	patchSettings(http.StatusOK, map[string]any{"budgets": map[string]any{"llm_usd_day": 2}})
	patchSettings(http.StatusUnprocessableEntity,
		map[string]any{"upstream": map[string]any{"default": map[string]any{"url": "https://ops:" + noPatternSecret + "@llm.example.com/v1"}}})

	// #54, #56, #67-#69, and a sign-in.
	mustCall(http.StatusOK, http.MethodGet, "/api/credentials", nil)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(mustCall(http.StatusCreated, http.MethodPost, "/api/keys",
		map[string]any{"label": "leak-ingest", "scopes": []string{"ingest"}}), &created); err != nil || created.ID == "" {
		t.Fatalf("POST /api/keys: %v", err)
	}
	mustCall(http.StatusOK, http.MethodGet, "/api/keys", nil)
	mustCall(http.StatusNoContent, http.MethodDelete, "/api/keys/"+created.ID, nil)
	mustCall(http.StatusNoContent, http.MethodDelete, "/api/credentials/compatible", nil)
	mustCall(http.StatusOK, http.MethodPost, "/api/session", map[string]any{"key": leakAdminKey}, "Origin", connectOrigin)

	// #8 and #11 for every caller, the lists, and #70.
	for _, key := range []string{leakAdminKey, testKey, ""} {
		for _, path := range []string{"/api/status", "/api/session", "/api/traces?limit=200", "/api/audits?limit=200",
			"/api/gate/decisions?limit=200", "/api/credentials", "/api/admin/audit-log?limit=200"} {
			sink.call(env.testEnv, http.MethodGet, path, key, nil)
		}
	}
	var traces struct {
		Items []struct {
			TraceID string `json:"trace_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(mustCall(http.StatusOK, http.MethodGet, "/api/traces?limit=200", nil), &traces); err != nil || len(traces.Items) < 3 {
		t.Fatalf("the gated and Playground traces were not recorded (%d, %v)", len(traces.Items), err)
	}
	for _, tr := range traces.Items {
		for _, key := range []string{leakAdminKey, testKey} {
			sink.call(env.testEnv, http.MethodGet, "/api/traces/"+tr.TraceID, key, nil)
		}
	}

	// admin_audit_log, row by row as the database holds it.
	rows, err := env.schema.Pool(t).Query(context.Background(), `SELECT row_to_json(a)::text FROM admin_audit_log a ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var actions int
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		actions++
		sink.add(fmt.Sprintf("admin_audit_log row %d", actions), []byte(row))
	}
	rows.Close()
	if actions < 6 {
		t.Fatalf("admin_audit_log holds %d rows; the writes above make more", actions)
	}

	// cost_events, row by row: the credential checks and #60 booked their probes as the component
	// probe, outside any trace (D82), and no row holds a secret.
	if err := env.srv.stopCosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err = env.schema.Pool(t).Query(context.Background(),
		`SELECT component, trace_id IS NULL, row_to_json(c)::text FROM cost_events c ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var costRows, probes int
	for rows.Next() {
		var (
			component string
			untraced  bool
			row       string
		)
		if err := rows.Scan(&component, &untraced, &row); err != nil {
			t.Fatal(err)
		}
		costRows++
		sink.add(fmt.Sprintf("cost_events row %d", costRows), []byte(row))
		if component == jev.ComponentProbe {
			probes++
			if !untraced {
				t.Errorf("cost_events row %d: a probe is booked against a trace", costRows)
			}
		}
	}
	rows.Close()
	// At least the two checks that passed (#55's OpenRouter key and #59's exchanged key) booked a
	// probe each; the refused ones of #57 and #60 may book theirs too.
	if probes < 2 {
		t.Fatalf("cost_events holds %d probe rows of %d; the credential checks book theirs as probe (D82)", probes, costRows)
	}

	// /metrics, the WS frames and the log.
	m := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), "trustagent_gateway_") {
		t.Fatal("/metrics carries no gateway series")
	}
	sink.add("/metrics", m.Body.Bytes())
	frames := env.wsFrames(t, 1000)
	if len(frames) == 0 {
		t.Fatal("no WS frame was published")
	}
	refusal := false
	for i, f := range frames {
		sink.add(fmt.Sprintf("WS frame %d", i), f)
		refusal = refusal || isDecisionRefusalFrame(f)
	}
	if !refusal {
		t.Fatal("no WS status frame carried the decision model's refusal, so the echo never reached the stream")
	}
	sink.add("the log", []byte(env.logText()))
	for _, s := range secrets {
		sink.assertAbsent(s)
	}
}

// waitDecisionRefusal waits until /api/status reports the decision model refused with the vendor's
// echo masked: the echo travelled the path the test watches. It then runs one pass of the status
// watch (startStatusWatch runs it every 5 s), which broadcasts the refusal as a WS status frame.
func waitDecisionRefusal(t *testing.T, env *testEnv) {
	t.Helper()
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
			env.srv.watchStatus()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the decision model never reported the refusal (state %q)", st.DecisionModel.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isDecisionRefusalFrame reports whether a WS frame is the decision model's unauthorized status,
// whose detail is the vendor's message with the echoed key masked.
func isDecisionRefusalFrame(frame []byte) bool {
	var f struct {
		Type string `json:"type"`
		Data struct {
			Component string `json:"component"`
			State     string `json:"state"`
			Detail    string `json:"detail"`
		} `json:"data"`
	}
	return json.Unmarshal(frame, &f) == nil && f.Type == "status" && f.Data.Component == componentDecisionModel &&
		f.Data.State == "unauthorized" && strings.Contains(f.Data.Detail, "invalid api key "+config.Masked)
}
