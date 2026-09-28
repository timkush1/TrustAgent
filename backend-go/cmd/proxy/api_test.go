package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
)

// Schema definitions of contracts/schema/api.v1.schema.json. JSON-pointer fragments are
// percent-encoded, so Page<T> is Page%3CT%3E.
const (
	schemaTracePage  = "api.v1.schema.json#/$defs/Page%3CTraceSummary%3E"
	schemaTrace      = "api.v1.schema.json#/$defs/TraceDetail"
	schemaAuditPage  = "api.v1.schema.json#/$defs/Page%3CAuditSummary%3E"
	schemaAudit      = "api.v1.schema.json#/$defs/AuditDetail"
	schemaHealth     = "api.v1.schema.json#/$defs/HealthStatus"
	schemaStatus     = "api.v1.schema.json#/$defs/SystemStatus"
	schemaError      = "api.v1.schema.json#/$defs/ApiErrorBody"
	schemaDecisionV1 = "decision.v1.schema.json"
)

// TestS0ScenariosThroughTheAPI sends the three S0 scenarios through the real gateway (mock
// decision model, fake engine answering from the scenario judge fixtures) and reads them back over
// REST; every response is validated against the contract schema.
func TestS0ScenariosThroughTheAPI(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	scenarios := loadDemoScenarios(t)
	ids := map[string]string{}
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed"} {
		s := scenarios[id]
		resp := env.send(s)
		if got := resp.Header.Get("X-TrustAgent-Action"); got != s.Expect.Action {
			t.Errorf("%s: action %q, want %q", id, got, s.Expect.Action)
		}
		if got := resp.Header.Get("X-TrustAgent-Audit"); got != s.Expect.Audit {
			t.Errorf("%s: audit %q, want %q", id, got, s.Expect.Audit)
		}
		ids[id] = resp.Header.Get("X-TrustAgent-Trace-Id")
	}

	for id, traceID := range ids {
		s := scenarios[id]
		d := env.settledTrace(traceID)
		code, body := env.get("/api/traces/"+traceID, testKey)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d", id, code)
		}
		contractstest.ValidateJSON(t, schemaTrace, body)
		for _, dec := range d.Decisions {
			contractstest.ValidateJSON(t, schemaDecisionV1, dec)
		}
		if got := store.Outcome(store.OutcomeFields{GateStatus: d.GateStatus, Action: deref(d.Action), WouldAction: deref(d.WouldAction)}); got != s.Expect.Outcome {
			t.Errorf("%s: outcome %q, want %q", id, got, s.Expect.Outcome)
		}
		if d.Source != "trafficgen" || d.Route != "support-bot" || d.AnswerSource == nil || *d.AnswerSource != "scripted" {
			t.Errorf("%s: trace %+v", id, d.TraceSummary)
		}
		if !d.Body.Available || d.Body.ResponseText == nil || *d.Body.ResponseText != s.Response || len(d.Decisions) != 1 {
			t.Errorf("%s: body %+v, %d decisions", id, d.Body, len(d.Decisions))
		}
		want := "(none)"
		if s.Expect.AuditVerdict != nil {
			want = *s.Expect.AuditVerdict
		}
		got := "(none)"
		if d.Audit.Verdict != nil {
			got = *d.Audit.Verdict
		}
		if got != want {
			t.Errorf("%s: audit verdict %s, want %s", id, got, want)
		}
		if s.Judge != nil {
			if d.AuditDetail == nil || len(d.AuditDetail.Claims) != len(s.Judge.Claims) {
				t.Fatalf("%s: audit detail %+v", id, d.AuditDetail)
			}
			for i, c := range s.Judge.Claims {
				if d.AuditDetail.Claims[i].Status != c.Verdict {
					t.Errorf("%s: claim %d is %s, want %s", id, i, d.AuditDetail.Claims[i].Status, c.Verdict)
				}
			}
			code, body := env.get("/api/audits/"+d.AuditDetail.AuditID, testKey)
			if code != http.StatusOK {
				t.Fatalf("%s: audit HTTP %d", id, code)
			}
			contractstest.ValidateJSON(t, schemaAudit, body)
		}
	}

	// The engine got the M5 request: trace id, the last user turn, the evidence, the gate
	// decision and the scenario for the mock judge.
	var refund bool
	for _, req := range env.engine.Requests() {
		if req.GetOptions().GetMockScenario() != "refund-contradiction-flag" {
			continue
		}
		refund = true
		if req.GetTraceId() != ids["refund-contradiction-flag"] || req.GetRequestId() != req.GetTraceId() ||
			req.GetQuery() != "Can I still get a refund on my annual plan? I bought it 45 days ago." ||
			req.GetResponse() != scenarios["refund-contradiction-flag"].Response {
			t.Errorf("audit request %v", req)
		}
		if len(req.GetContext()) != 1 || req.GetContext()[0].GetId() != "refund-policy.md" || req.GetContext()[0].GetSource() != "request_system" {
			t.Errorf("audit context %v", req.GetContext())
		}
		if g := req.GetGate(); g.GetAction() != "flag" || g.GetWouldAction() != "block" || len(g.GetDecisionJson()) == 0 || g.GetDelivered() != "original" {
			t.Errorf("audit gate %v", g)
		}
		if req.GetPriority() != "high" || req.GetSource() != "trafficgen" || req.GetRoute() != "support-bot" {
			t.Errorf("audit routing %v", req)
		}
	}
	if !refund {
		t.Error("the flagged scenario was not audited")
	}
	if n := len(env.engine.Requests()); n != 2 {
		t.Errorf("%d audits; the no-evidence trace must not be audited", n)
	}

	code, body := env.get("/api/traces", testKey)
	if code != http.StatusOK {
		t.Fatalf("list HTTP %d", code)
	}
	contractstest.ValidateJSON(t, schemaTracePage, body)
	var p page[store.TraceSummary]
	_ = json.Unmarshal(body, &p)
	if len(p.Items) != 3 || p.NextCursor != nil {
		t.Errorf("list %d items, next %v", len(p.Items), p.NextCursor)
	}

	code, body = env.get("/api/traces?route=support-bot&outcome=flagged&limit=10", testKey)
	_ = json.Unmarshal(body, &p)
	if code != http.StatusOK || len(p.Items) != 1 || p.Items[0].TraceID != ids["refund-contradiction-flag"] {
		t.Errorf("outcome filter: HTTP %d %s", code, body)
	}
	code, body = env.get("/api/traces?limit=2", testKey)
	_ = json.Unmarshal(body, &p)
	if code != http.StatusOK || len(p.Items) != 2 || p.NextCursor == nil {
		t.Fatalf("first page: HTTP %d %s", code, body)
	}
	code, body = env.get("/api/traces?limit=2&cursor="+url.QueryEscape(*p.NextCursor), testKey)
	contractstest.ValidateJSON(t, schemaTracePage, body)
	_ = json.Unmarshal(body, &p)
	if code != http.StatusOK || len(p.Items) != 1 || p.NextCursor != nil {
		t.Errorf("second page: HTTP %d %s", code, body)
	}

	code, body = env.get("/api/audits", testKey)
	if code != http.StatusOK {
		t.Fatalf("audits HTTP %d", code)
	}
	contractstest.ValidateJSON(t, schemaAuditPage, body)
}

func TestAPIAuthAndRedaction(t *testing.T) {
	scenarios := loadDemoScenarios(t)

	env := newTestEnv(t, envOpts{})
	traceID := env.send(scenarios["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")
	env.settledTrace(traceID)
	for _, key := range []string{"", "wrong-key"} {
		code, body := env.get("/api/traces", key)
		if code != http.StatusUnauthorized {
			t.Errorf("key %q: HTTP %d", key, code)
		}
		contractstest.ValidateJSON(t, schemaError, body)
	}
	// Legacy key headers still work on /api.
	r, _ := http.NewRequest(http.MethodGet, env.http.URL+"/api/traces/"+traceID, nil)
	r.Header.Set("Authorization", "Bearer "+testKey)
	if resp, err := http.DefaultClient.Do(r); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("Bearer key: %v %v", resp, err)
	} else {
		_ = resp.Body.Close()
	}

	// Public read without demo mode: metadata only.
	public := newTestEnv(t, envOpts{publicRead: true})
	traceID = public.send(scenarios["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")
	public.settledTrace(traceID)
	code, body := public.get("/api/traces/"+traceID, "")
	if code != http.StatusOK {
		t.Fatalf("public read: HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaTrace, body)
	var d store.TraceDetail
	_ = json.Unmarshal(body, &d)
	if !d.Redacted || d.QuestionPreview != nil || d.ResponsePreview != nil || d.Body.Available ||
		d.Body.UnavailableReason == nil || *d.Body.UnavailableReason != store.BodyScope {
		t.Errorf("redacted trace %+v", d.TraceSummary)
	}
	if d.AuditDetail == nil || d.AuditDetail.Claims[0].Claim != nil || d.AuditDetail.Claims[0].Status != "CONTRADICTED" || d.AuditDetail.UserQuery != nil {
		t.Errorf("redacted audit %+v", d.AuditDetail)
	}
	if strings.Contains(string(body), "60 days") {
		t.Errorf("a redacted trace leaks the answer: %s", body)
	}
	code, body = public.get("/api/traces", "")
	contractstest.ValidateJSON(t, schemaTracePage, body)
	if code != http.StatusOK || strings.Contains(string(body), "60 days") || strings.Contains(string(body), "bought it") {
		t.Errorf("redacted list: HTTP %d %s", code, body)
	}
	// Reads are public; ingest is not.
	code, body = public.do(http.MethodPost, "/api/audit", "", []byte(`{"query":"q","response":"r"}`))
	if code != http.StatusUnauthorized {
		t.Errorf("public ingest: HTTP %d %s", code, body)
	}

	// Demo mode: the scripted demo data is readable in full without a key.
	demo := newTestEnv(t, envOpts{publicRead: true, demo: true})
	traceID = demo.send(scenarios["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")
	demo.settledTrace(traceID)
	_, body = demo.get("/api/traces/"+traceID, "")
	_ = json.Unmarshal(body, &d)
	if d.Redacted || !d.Body.Available || d.ResponsePreview == nil {
		t.Errorf("demo public read %+v", d.TraceSummary)
	}
}

func TestAPIValidationAndErrors(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	since := url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	old := url.QueryEscape(time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339))
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/traces?limit=0", 400},
		{"/api/traces?limit=201", 400},
		{"/api/traces?since=yesterday", 400},
		{"/api/traces?outcome=exploded", 400},
		{"/api/traces?action=pending", 400},
		{"/api/traces?cursor=bogus", 400},
		{"/api/traces?q=refund", 400},
		{"/api/traces?q=refund&since=" + old, 400},
		{"/api/traces?q=refund&since=" + since, 200},
		{"/api/traces/does-not-exist", 404},
		{"/api/audits?verdict=grounded", 400},
		{"/api/audits/does-not-exist", 404},
	} {
		code, body := env.get(tc.path, testKey)
		if code != tc.code {
			t.Errorf("%s: HTTP %d, want %d: %s", tc.path, code, tc.code, body)
		}
		if code != http.StatusOK {
			contractstest.ValidateJSON(t, schemaError, body)
		}
	}

	noDB := newTestEnv(t, envOpts{noStore: true})
	for _, path := range []string{"/api/traces", "/api/traces/x", "/api/audits", "/api/audits/x"} {
		code, body := noDB.get(path, testKey)
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), codePersistenceDisabled) {
			t.Errorf("%s without a store: HTTP %d %s", path, code, body)
		}
		contractstest.ValidateJSON(t, schemaError, body)
	}
	// Without a store the gateway still gates and broadcasts.
	resp := noDB.send(loadDemoScenarios(t)["grounded-pass"])
	if resp.Header.Get("X-TrustAgent-Action") != "pass" {
		t.Errorf("gating without a store: %v", resp.Header)
	}
}

// TestRateLimitedErrorShapes: a refused request is 429 rate_limited with Retry-After, as an
// ApiErrorBody on /api and in the OpenAI error shape on /v1 (SPEC §2.4, §2.12).
func TestRateLimitedErrorShapes(t *testing.T) {
	env := newTestEnv(t, envOpts{rateLimit: 1})
	env.get("/api/traces", testKey)
	code, body := env.get("/api/traces", testKey)
	if code != http.StatusTooManyRequests || !strings.Contains(string(body), `"code":"rate_limited"`) {
		t.Fatalf("/api over the limit: HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaError, body)

	s := loadDemoScenarios(t)["grounded-pass"]
	env.send(s)
	req, _ := http.NewRequest(http.MethodPost, env.http.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("X-TrustAgent-Key", testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var openAI struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&openAI); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" ||
		openAI.Error.Type != "trustagent_error" || openAI.Error.Code != "rate_limited" || resp.Header.Get("X-TrustAgent-Trace-Id") == "" {
		t.Errorf("/v1 over the limit: HTTP %d %+v %v", resp.StatusCode, openAI, resp.Header)
	}
}

func TestHealthAndStatus(t *testing.T) {
	env := newTestEnv(t, envOpts{demo: true})
	code, body := env.get("/health", "")
	if code != http.StatusOK {
		t.Fatalf("health HTTP %d", code)
	}
	contractstest.ValidateJSON(t, schemaHealth, body)
	var h healthStatus
	_ = json.Unmarshal(body, &h)
	if h.Status != "ok" || !h.AuditEngine || !h.Demo {
		t.Errorf("health %+v", h)
	}

	code, body = env.get("/api/status", testKey)
	if code != http.StatusOK {
		t.Fatalf("status HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStatus, body)
	var st systemStatus
	_ = json.Unmarshal(body, &st)
	if st.Engine.State != stateUp || st.DecisionModel.State != "mock" || st.Persistence.State != stateUp ||
		st.Persistence.Migration == nil || st.Policy.FileStatus != "ok" || strings.Join(st.Auth.CallerScopes, ",") != "ingest,read,content" {
		t.Errorf("status %+v", st)
	}
	if code, _ := env.get("/api/status", ""); code != http.StatusUnauthorized {
		t.Errorf("status without a key: HTTP %d", code)
	}

	// An engine that does not answer its health check.
	down := newTestEnv(t, envOpts{noStore: true, engineDown: true})
	_, body = down.get("/health", "")
	contractstest.ValidateJSON(t, schemaHealth, body)
	if !strings.Contains(string(body), `"audit_engine":false`) {
		t.Errorf("health with the engine down: %s", body)
	}
	_, body = down.get("/api/status", testKey)
	contractstest.ValidateJSON(t, schemaStatus, body)
	if !strings.Contains(string(body), `"persistence":{"state":"disabled"`) {
		t.Errorf("status without a store: %s", body)
	}
}

func TestDirectAudit(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	code, body := env.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"How long is the refund window?",
		"response":"60 days.","context":[{"id":"refund-policy.md","text":"30 days."}]}`))
	if code != http.StatusAccepted {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var acc directAuditAccepted
	_ = json.Unmarshal(body, &acc)
	if acc.Status != "queued" || acc.Lane != "low" || acc.TraceID == "" || acc.AuditID == "" {
		t.Fatalf("accepted %+v", acc)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body = env.get("/api/audits/"+acc.AuditID, testKey)
		if code == http.StatusOK || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code != http.StatusOK {
		t.Fatalf("audit HTTP %d", code)
	}
	contractstest.ValidateJSON(t, schemaAudit, body)
	var a store.AuditDetail
	_ = json.Unmarshal(body, &a)
	// The fake engine has no fixture without a scenario: a failed audit, reported as such.
	if a.Source != "direct" || a.Selection == nil || *a.Selection != "direct" || a.Status != store.AuditFailed || a.Error == nil {
		t.Errorf("direct audit %+v", a.AuditSummary)
	}
	req := env.engine.Requests()[0]
	if len(req.GetContext()) != 1 || req.GetContext()[0].GetSource() != "extension" || req.GetSource() != "direct" {
		t.Errorf("engine request %v", req)
	}

	for _, bad := range []string{`not json`, `{"query":"","response":"r"}`, `{"query":"q","response":"r","route":"Bad Route"}`} {
		code, body := env.do(http.MethodPost, "/api/audit", testKey, []byte(bad))
		if code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
			t.Errorf("%s: HTTP %d", bad, code)
		}
		contractstest.ValidateJSON(t, schemaError, body)
	}
	ro := newTestEnv(t, envOpts{readonly: true})
	if code, _ := ro.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"q","response":"r"}`)); code != http.StatusForbidden {
		t.Errorf("read-only: HTTP %d", code)
	}
}

// On the default route (observe mode) the response goes out first and the decision is recorded
// afterwards: the trace starts pending and settles with action observe.
func TestObserveRouteRecordsTheDeferredDecision(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	s := *loadDemoScenarios(t)["grounded-pass"]
	s.Headers = nil // no X-TrustAgent-Route: the default route
	resp := env.send(&s)
	if resp.Header.Get("X-TrustAgent-Action") != "observe" || resp.Header.Get("X-TrustAgent-Status") != "deferred" ||
		!strings.HasPrefix(resp.Header.Get("X-TrustAgent-Route"), "default@v") {
		t.Fatalf("headers %v", resp.Header)
	}
	d := env.settledTrace(resp.Header.Get("X-TrustAgent-Trace-Id"))
	if d.Mode != "observe" || d.Action == nil || *d.Action != "observe" || d.GateStatus == store.GateStatusPending || len(d.Decisions) != 1 {
		t.Errorf("observed trace %+v", d.TraceSummary)
	}
}
