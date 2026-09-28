package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

const testKey = "tg-key"

// stubProxy answers the scenarios as a gateway would, from each scenario's expect: the gate
// headers on /v1/chat/completions and a trace on /api/traces/:id whose audit is queued for the
// first polls, then settles.
type stubProxy struct {
	t         *testing.T
	scenarios map[string]*scenario
	// mutate lets a test change the headers of one scenario.
	mutate func(id string, h http.Header)
	// verdict and action override the settled audit verdict and the stored action of a scenario.
	verdict map[string]string
	action  map[string]string

	mu       sync.Mutex
	polls    map[string]int
	requests []*http.Request
	bodies   [][]byte
}

func newStub(t *testing.T) (*stubProxy, *httptest.Server) {
	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	s := &stubProxy{t: t, scenarios: map[string]*scenario{}, polls: map[string]int{}, verdict: map[string]string{},
		action: map[string]string{}}
	for i := range all {
		s.scenarios[all[i].ID] = &all[i]
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *stubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		sc, ok := s.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
		if !ok || r.Header.Get("X-TrustAgent-Key") != testKey {
			http.Error(w, "unknown scenario or key", http.StatusBadRequest)
			return
		}
		e := expectation(sc.Expect)
		h := w.Header()
		h.Set(headerTraceID, "trace-"+sc.ID)
		h.Set(headerRoute, sc.Route+"@v1")
		h.Set(headerMode, e.str("mode"))
		h.Set(headerAction, e.str("action"))
		if would := e.str("would_action"); would != "" && would != e.str("action") {
			h.Set(headerWouldAction, would)
		}
		h.Set(headerStatus, e.str("status"))
		if _, null := e.has("verdict"); !null {
			h.Set(headerVerdict, e.str("verdict"))
		}
		if _, null := e.has("max_risk"); !null {
			var risk float64
			_ = json.Unmarshal(e["max_risk"], &risk)
			h.Set(headerRisk, decision.FormatRisk(risk))
		}
		var reasons []string
		_ = json.Unmarshal(e["reasons"], &reasons)
		if len(reasons) > 0 {
			h.Set(headerReasons, strings.Join(reasons, ","))
		}
		h.Set(headerCheckedAgainst, e.str("checked_against"))
		h.Set(headerAudit, e.str("audit"))
		if s.mutate != nil {
			s.mutate(sc.ID, h)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[]}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/traces/"):
		if r.Header.Get("X-TrustAgent-Key") != testKey {
			http.Error(w, `{"error":{"code":"unauthorized","message":"no"}}`, http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/traces/"), "trace-")
		sc, ok := s.scenarios[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.polls[id]++
		n := s.polls[id]
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(s.trace(sc, n))
	default:
		http.NotFound(w, r)
	}
}

// trace is the TraceDetail of a scenario at its n-th poll.
func (s *stubProxy) trace(sc *scenario, n int) map[string]any {
	e := expectation(sc.Expect)
	gateStatus := e.str("status")
	audit := map[string]any{"status": e.str("audit"), "verdict": nil}
	var detail any
	switch {
	case e.str("audit") == "queued" && n < 3:
	case e.str("audit") == "queued":
		verdict := e.str("audit_verdict")
		if v, ok := s.verdict[sc.ID]; ok {
			verdict = v
		}
		audit = map[string]any{"status": "completed", "verdict": verdict}
		var claims []map[string]any
		for _, c := range sc.Judge.Claims {
			claims = append(claims, map[string]any{"status": c.Verdict})
		}
		detail = map[string]any{"status": "completed", "verdict": verdict, "claims": claims}
	case e.str("audit") == "not_assessed":
		audit["verdict"] = "NOT_ASSESSED"
	}
	would := any(nil)
	if w := e.str("would_action"); w != "" {
		would = w
	}
	action := e.str("action")
	if a, ok := s.action[sc.ID]; ok {
		action = a
	}
	return map[string]any{"trace_id": "trace-" + sc.ID, "gate_status": gateStatus, "action": action,
		"would_action": would, "audit": audit, "audit_detail": detail}
}

// snapshot returns the requests, bodies and poll counts seen so far.
func (s *stubProxy) snapshot() ([]*http.Request, [][]byte, map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	polls := map[string]int{}
	for k, v := range s.polls {
		polls[k] = v
	}
	return append([]*http.Request(nil), s.requests...), append([][]byte(nil), s.bodies...), polls
}

func runTG(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := map[string]string{keyEnv: testKey}
	code := run(context.Background(), args, &out, &errOut, func(k string) string { return env[k] })
	return code, out.String() + errOut.String()
}

func TestOnceExpectPasses(t *testing.T) {
	stub, srv := newStub(t)
	code, out := runTG(t, "--once", "--expect", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed"} {
		if !strings.Contains(out, "ok   "+id+" (trace trace-"+id) {
			t.Errorf("no ok line for %s:\n%s", id, out)
		}
	}
	requests, bodies, polls := stub.snapshot()
	if len(requests) != 3 {
		t.Fatalf("%d requests", len(requests))
	}
	r, body := requests[1], bodies[1]
	if r.Header.Get("X-TrustAgent-Route") != "support-bot" || r.Header.Get("X-TrustAgent-Scenario") != "refund-contradiction-flag" ||
		!strings.HasPrefix(r.Header.Get("X-Request-ID"), "trafficgen:refund-contradiction-flag:") {
		t.Errorf("headers %v", r.Header)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["test_response"]) != `"You can get a full refund within 60 days of purchase."` || sent["messages"] == nil || string(sent["model"]) != `"gpt-4o-mini"` {
		t.Errorf("body %s", body)
	}
	if polls["refund-contradiction-flag"] < 3 || polls["no-evidence-not-assessed"] != 1 {
		t.Errorf("polls %v", polls)
	}
}

func TestOnceExpectReportsADiff(t *testing.T) {
	stub, srv := newStub(t)
	stub.mutate = func(id string, h http.Header) {
		if id == "refund-contradiction-flag" {
			h.Set(headerAction, "pass")
			h.Del(headerWouldAction)
			h.Set(headerReasons, "groundedness:critical")
			h.Set(headerRisk, "0.97")
		}
		if id == "grounded-pass" {
			h.Set(headerWouldAction, "pass") // sent although it equals the action
		}
	}
	stub.verdict["refund-contradiction-flag"] = "GROUNDED"
	stub.action["refund-contradiction-flag"] = "pass"
	code, out := runTG(t, "--once", "--expect", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitMismatch {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"FAIL refund-contradiction-flag (trace trace-refund-contradiction-flag)",
		`X-TrustAgent-Action: want "flag", got "pass"`,
		`X-TrustAgent-Would-Action: want "block", got (absent)`,
		`X-TrustAgent-Risk: want "0.98", got "0.97"`,
		`X-TrustAgent-Reasons: want to include "contradiction:critical", got "groundedness:critical"`,
		"outcome: want flagged, got passed",
		"audit verdict: want HALLUCINATED, got GROUNDED",
		"FAIL grounded-pass",
		`X-TrustAgent-Would-Action: want (absent) (equals the action), got "pass"`,
		"ok   no-evidence-not-assessed",
		"some scenarios do not match their expectations",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAuditThatNeverSettles(t *testing.T) {
	_, srv := newStub(t)
	code, out := runTG(t, "--once", "--expect", "--scenarios", "refund-contradiction-flag", "--base-url", srv.URL, "--audit-timeout", "150ms")
	if code != exitMismatch || !strings.Contains(out, "did not settle within --audit-timeout (gate ok, audit queued)") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWithoutExpectOnlyTransportFailuresCount(t *testing.T) {
	stub, srv := newStub(t)
	stub.mutate = func(string, http.Header) {}
	code, out := runTG(t, "--scenarios", "demo", "--base-url", srv.URL)
	if _, _, polls := stub.snapshot(); code != exitOK || len(polls) != 0 {
		t.Fatalf("exit %d, polls %v:\n%s", code, polls, out)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"test_response is honoured only in test mode","code":"test_mode_disabled"}}`, http.StatusBadRequest)
	}))
	defer failing.Close()
	code, out = runTG(t, "--once", "--scenarios", "grounded-pass", "--base-url", failing.URL)
	if code != exitMismatch || !strings.Contains(out, "HTTP 400") || !strings.Contains(out, "test_mode_disabled") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestLoopRunsUntilCancelled(t *testing.T) {
	stub, srv := newStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	var out bytes.Buffer
	go func() {
		done <- run(ctx, []string{"--loop", "--interval", "10ms", "--scenarios", "s0", "--base-url", srv.URL}, &out, io.Discard,
			func(string) string { return testKey })
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		requests, _, _ := stub.snapshot()
		if n := len(requests); n >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests after 5s", len(requests))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != exitOK {
		t.Fatalf("exit %d", code)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--once", "--loop"},
		{"--base-url", "localhost:8080"},
		{"--scenarios", "no-such-set"},
		{"--interval", "0s", "--loop"},
		{"extra"},
		{"--file", "/does/not/exist.json"},
	} {
		if code, out := runTG(t, args...); code != exitUsage {
			t.Errorf("%v: exit %d:\n%s", args, code, out)
		}
	}
}

func TestSelectScenarios(t *testing.T) {
	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	for sel, want := range map[string]int{"s0": 3, "demo,e2e": 3, "all": len(all), "grounded-pass": 1, "grounded-pass, s0": 3} {
		got, err := selectScenarios(all, sel)
		if err != nil || len(got) != want {
			t.Errorf("%q: %d scenarios (%v), want %d", sel, len(got), err, want)
		}
	}
}

func TestCheckHeadersNullsAndSubsets(t *testing.T) {
	s := &scenario{Route: "support-bot", Expect: map[string]json.RawMessage{
		"action": json.RawMessage(`"pass"`), "verdict": json.RawMessage(`null`), "max_risk": json.RawMessage(`null`),
		"would_action": json.RawMessage(`null`), "reasons": json.RawMessage(`[]`),
	}}
	h := http.Header{}
	h.Set(headerAction, "pass")
	h.Set(headerRoute, "support-bot@v7")
	h.Set(headerReasons, "anything")
	if m := checkHeaders(s, h); len(m) != 0 {
		t.Errorf("mismatches %v", m)
	}
	h.Set(headerVerdict, "supported")
	h.Set(headerRisk, "0.10")
	h.Set(headerRoute, "support-bot-2@v1")
	got := fmt.Sprint(checkHeaders(s, h))
	for _, want := range []string{"X-TrustAgent-Verdict: want (absent)", "X-TrustAgent-Risk: want (absent)", `X-TrustAgent-Route: want "support-bot@v<version>"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from %s", want, got)
		}
	}
}
