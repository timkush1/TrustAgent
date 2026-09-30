package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/decision"
)

var update = flag.Bool("update", false, "rewrite the golden request files in testdata/")

// harness is a mock-upstream server with a cost-event recorder.
type harness struct {
	t    *testing.T
	mock *mockupstream.Server
	srv  *httptest.Server

	mu    sync.Mutex
	costs []CostEvent
}

func newHarness(t *testing.T, mc mockupstream.Config) *harness {
	t.Helper()
	m, err := mockupstream.New(mc)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, mock: m, srv: httptest.NewServer(m)}
	t.Cleanup(h.srv.Close)
	return h
}

const (
	testOpenRouterKey = "sk-or-v1-test-0123456789"
	testTypeSafeKey   = "ts-test-key-0123"
	testGatewayKey    = "gw-test-key-0123"
)

// client builds an HTTP backend against the mock. base, when set, replaces the server URL (a
// /fault/<spec> prefix).
func (h *harness) client(backend string, mut func(*Config)) *httpClient {
	h.t.Helper()
	hc := DefaultHTTPConfig()
	hc.KeepAlive, hc.RetryAfterCap, hc.AttemptTimeout = 0, 20*time.Millisecond, 5*time.Second
	cfg := Config{Backend: backend, HTTP: &hc, Costs: CostFunc(h.record)}
	switch backend {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
		cfg.OpenRouterKey, cfg.OpenRouterBaseURL = testOpenRouterKey, h.srv.URL
	case BackendTypeSafe:
		cfg.TypeSafeKey, cfg.BaseURL = testTypeSafeKey, h.srv.URL
	case BackendCompatible:
		cfg.JevAPIKey, cfg.BaseURL, cfg.Model = testGatewayKey, h.srv.URL, "typesafe-ai/jev"
	}
	if mut != nil {
		mut(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	hcl, ok := c.(*httpClient)
	if !ok {
		h.t.Fatalf("backend %s resolved to %+v", backend, c.Info())
	}
	h.t.Cleanup(func() { _ = hcl.Close() })
	return hcl
}

// withFault points a client's base URL at /fault/<spec>.
func (h *harness) withFault(spec string) func(*Config) {
	return func(c *Config) {
		base := h.srv.URL + "/fault/" + url.PathEscape(spec)
		if c.OpenRouterBaseURL != "" {
			c.OpenRouterBaseURL = base
		} else {
			c.BaseURL = base
		}
	}
}

func (h *harness) record(e CostEvent) {
	h.mu.Lock()
	h.costs = append(h.costs, e)
	h.mu.Unlock()
}

func (h *harness) events() []CostEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]CostEvent(nil), h.costs...)
}

// waitEvents waits until n cost events arrived.
func (h *harness) waitEvents(n int) []CostEvent {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev := h.events()
		if len(ev) >= n {
			return ev
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("got %d cost events, want %d: %+v", len(ev), n, ev)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) decisionRequests() []mockupstream.Recorded {
	var out []mockupstream.Recorded
	for _, r := range h.mock.Requests() {
		if r.Method == http.MethodPost && (strings.HasSuffix(r.Path, "/decisions") || strings.HasSuffix(r.Path, "/systemone")) {
			out = append(out, r)
		}
	}
	return out
}

func testQuestions() map[string]any {
	return map[string]any{
		"groundedness__v1": map[string]any{"type": "noul", "instructions": "Is every factual statement in `assistant_response` supported by `evidence`?",
			"criteria": map[string]any{"true": "A statement is missing from `evidence` or disagrees with it", "false": "Every statement is in `evidence`"}},
		"refusal__v1": map[string]any{"type": "choice", "instructions": "How does `assistant_response` treat `user_request`?",
			"criteria": map[string]any{"answers_request": map[string]any{"what": "Answers it"}, "declines_or_deflects": map[string]any{"what": "Declines"}, "nothing_to_answer": nil}},
		"quality__v1": map[string]any{"type": "score", "instructions": "How clear is `assistant_response`?", "criteria": []any{"unclear", "mixed", "clear"}},
	}
}

func testRequest(session string) *Request {
	return &Request{SessionID: session, State: map[string]any{
		"user_request": "How long is the refund window for annual plans?",
		"evidence": []any{map[string]any{"id": "refund-policy.md", "source": "request_system",
			"text": "Annual plans: refund within 30 days. <b>Monthly</b> plans & add-ons are non-refundable — no exceptions."}},
		"assistant_response": "You can get a full refund within 60 days.",
	}, Questions: testQuestions()}
}

func decide(t *testing.T, c Client, req *Request, opt CallOptions) (*Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Decide(ctx, req, opt)
}

func TestBackendRequestShapes(t *testing.T) {
	cases := []struct {
		backend, path string
		openRouter    bool
	}{
		{BackendOpenRouterDecisions, "/api/alpha/decisions", true},
		{BackendOpenRouterSystemOne, "/api/v1/systemone", true},
		{BackendTypeSafe, "/v1/systemone", false},
		{BackendCompatible, "/v1/systemone", false},
	}
	for _, c := range cases {
		t.Run(c.backend, func(t *testing.T) {
			h := newHarness(t, mockupstream.Config{})
			cl := h.client(c.backend, func(cfg *Config) {
				cfg.HTTP.ZDR, cfg.HTTP.PublicURL = true, "https://trust.example.com"
			})
			resp, err := decide(t, cl, testRequest("0192f7c2-trace"), CallOptions{Scenario: "grounded-pass"})
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Answers) != 3 {
				t.Errorf("answers %+v", resp.Answers)
			}
			reqs := h.decisionRequests()
			if len(reqs) != 1 || reqs[0].Path != c.path {
				t.Fatalf("requests %+v, want one POST %s", reqs, c.path)
			}
			r := reqs[0]
			golden := filepath.Join("testdata", "request_"+c.backend+".golden.json")
			if *update {
				if err := os.WriteFile(golden, []byte(r.Body+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -update to create it)", err)
			}
			if r.Body != strings.TrimRight(string(want), "\n") {
				t.Errorf("request body\n%s\nwant (%s)\n%s", r.Body, golden, want)
			}
			hd := r.Header
			if hd.Get("Authorization") != "[redacted]" || hd.Get("Content-Type") != "application/json" || hd.Get("Accept") != "application/json" ||
				!strings.HasPrefix(hd.Get("User-Agent"), "TrustAgent/") {
				t.Errorf("headers %v", hd)
			}
			if hd.Get("X-TrustAgent-Scenario") != "" {
				t.Error("the scenario is forwarded only with ForwardScenario")
			}
			if c.openRouter {
				if hd.Get("HTTP-Referer") != "https://trust.example.com" || hd.Get("X-OpenRouter-Title") != "TrustAgent" || hd.Get("X-Title") != "TrustAgent" {
					t.Errorf("attribution headers %v", hd)
				}
			} else if hd.Get("HTTP-Referer") != "" || hd.Get("X-OpenRouter-Title") != "" {
				t.Errorf("attribution headers go to OpenRouter only: %v", hd)
			}
		})
	}
}

func TestResponseAccounting(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, nil)
	resp, err := decide(t, cl, testRequest("trace-acct"), CallOptions{TraceID: "trace-acct", Route: "support-bot", KeyLabel: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Snapshot != mockupstream.DefaultSnapshot || resp.Backend != BackendOpenRouterDecisions || resp.Attempts != 1 || resp.Hedged || resp.Late ||
		resp.ID == "" || resp.InputTokens == nil || resp.CostSource != CostReported || resp.CostUSD == nil || resp.ConnectMs == nil {
		t.Fatalf("response %+v", resp)
	}
	if want := float64(*resp.InputTokens) * 0.042 / 1e6; math.Abs(*resp.CostUSD-want) > 1e-15 {
		t.Errorf("reported cost %v, want %v", *resp.CostUSD, want)
	}
	ev := h.waitEvents(1)[0]
	if ev.TraceID != "trace-acct" || ev.Route != "support-bot" || ev.KeyLabel != "app" || ev.Component != ComponentGate || ev.Kind != CostKindDecision ||
		ev.Provider != BackendOpenRouterDecisions || ev.Model != mockupstream.DefaultSnapshot || ev.Attempt != 0 || ev.Status != AttemptOK ||
		ev.CostSource != CostReported || ev.CostUSD != *resp.CostUSD || ev.Questions != 3 || ev.Estimated || ev.Late || *ev.InputTokens != *resp.InputTokens {
		t.Errorf("cost event %+v", ev)
	}
	st := cl.Status()
	if st.State != "ready" || st.Snapshot != mockupstream.DefaultSnapshot || st.Calls != 1 || st.Attempts != 1 || st.LastOK == nil || st.Breaker != BreakerClosed ||
		st.RPMUsed != 1 || st.InFlight != 0 || st.Spend == nil || st.Spend.SpentUSD != ev.CostUSD {
		t.Errorf("status %+v", st)
	}

	// TypeSafe reports no cost: it is computed from input_tokens and the price table.
	ts := h.client(BackendTypeSafe, nil)
	resp, err = decide(t, ts, testRequest(""), CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CostSource != CostComputed || math.Abs(*resp.CostUSD-float64(*resp.InputTokens)*0.042/1e6) > 1e-15 || resp.Snapshot != "jev-1.13.0" {
		t.Errorf("typesafe response %+v cost %v", resp, *resp.CostUSD)
	}
}

func TestFaultClassification(t *testing.T) {
	cases := []struct {
		fault     string
		kind      string // "" = success
		retryable bool
		attempts  int
		breaker   string
		hint      string
	}{
		{"401", KindAuth, false, 1, BreakerOpen, "Key rejected"},
		{"403", KindAuth, false, 1, BreakerOpen, "Key rejected"},
		{"402_credits", KindCredits, false, 1, BreakerOpen, "openrouter.ai/settings/credits"},
		{"402_inflight", KindCredits, true, 2, BreakerClosed, "in-flight budget"},
		{"402_inflight@1", "", false, 2, BreakerClosed, ""},
		{"429@1", "", false, 2, BreakerClosed, ""},
		{"429", KindRetryable, true, 2, BreakerClosed, "busy"},
		{"529", KindRetryable, true, 2, BreakerClosed, "busy"},
		{"500", KindRetryable, true, 2, BreakerClosed, ""},
		{"502", KindRetryable, true, 2, BreakerClosed, ""},
		{"503@1", "", false, 2, BreakerClosed, ""},
		{"524", KindRetryable, true, 2, BreakerClosed, ""},
		{"413", KindTooLarge, false, 1, BreakerClosed, "TRUTHTABLE_JEV_TOKEN_BUDGET"},
		{"400_context", KindTooLarge, false, 1, BreakerClosed, ""},
		{"400", KindBadRequest, false, 1, BreakerClosed, ""},
		{"404", KindNotFound, false, 1, BreakerClosed, "TRUTHTABLE_JEV_MODEL"},
		{"404_data_policy", KindDataPolicy, false, 1, BreakerClosed, "check openrouter.ai/settings/privacy or unset TRUTHTABLE_JEV_ZDR"},
		{"html200", KindBadContentType, false, 1, BreakerOpen, "OpenRouter URLs end in /api"},
		{"bad_json", KindBadResponse, false, 1, BreakerClosed, ""},
		{"no_answers", KindBadResponse, false, 1, BreakerClosed, ""},
	}
	for _, c := range cases {
		t.Run(c.fault, func(t *testing.T) {
			h := newHarness(t, mockupstream.Config{})
			cl := h.client(BackendOpenRouterDecisions, h.withFault(c.fault))
			resp, err := decide(t, cl, testRequest("trace-"+c.fault), CallOptions{})
			if c.kind == "" {
				if err != nil || resp.Attempts != c.attempts {
					t.Fatalf("want success after %d attempts: %+v %v", c.attempts, resp, err)
				}
			} else {
				var e *Error
				if !errors.As(err, &e) {
					t.Fatalf("err %v, want kind %s", err, c.kind)
				}
				if e.Kind != c.kind || e.Retryable != c.retryable || e.Attempts != c.attempts || !strings.Contains(e.Hint, c.hint) {
					t.Errorf("error %+v, want kind %s retryable %v attempts %d hint ~%q", e, c.kind, c.retryable, c.attempts, c.hint)
				}
				if e.Status == 0 {
					t.Error("an HTTP error keeps its status")
				}
			}
			if n := len(h.decisionRequests()); n != c.attempts {
				t.Errorf("%d vendor requests, want %d", n, c.attempts)
			}
			events := h.waitEvents(c.attempts)
			for i, ev := range events {
				if ev.Attempt != i || (ev.Status == AttemptOK) != (c.kind == "" && i == c.attempts-1) {
					t.Errorf("cost event %d: %+v", i, ev)
				}
				if ev.Status != AttemptOK && ev.CostUSD != 0 {
					t.Errorf("an HTTP error is not billed: %+v", ev)
				}
			}
			if st := cl.Status(); st.Breaker != c.breaker {
				t.Errorf("breaker %s, want %s", st.Breaker, c.breaker)
			}
		})
	}
}

func TestTypeSafeAndVercelEnvelopes(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendTypeSafe, h.withFault("422"))
	_, err := decide(t, cl, testRequest(""), CallOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindBadRequest || e.Status != 422 || e.Message != "body.questions.q1.criteria: field required" {
		t.Errorf("422: %+v", err)
	}
	cl = h.client(BackendTypeSafe, func(c *Config) { c.TypeSafeKey = "ts-invalid-key" })
	if _, err = decide(t, cl, testRequest(""), CallOptions{}); ErrorKind(err) != KindAuth || !strings.Contains(err.Error(), "User not found.") {
		t.Errorf("typesafe auth: %v", err)
	}
	// A real validation failure of the TypeSafe-shaped endpoint.
	cl = h.client(BackendCompatible, nil)
	bad := testRequest("")
	bad.Questions = map[string]any{"q": map[string]any{"type": "score", "instructions": "i", "criteria": []any{"only one"}}}
	if _, err = decide(t, cl, bad, CallOptions{}); !errors.As(err, &e) || e.Kind != KindBadRequest || !strings.Contains(e.Message, "questions.q.criteria") {
		t.Errorf("validation: %v", err)
	}
	// Vercel's {"message","error_type"} envelope.
	vercel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Model typesafe-ai/jev is not available","error_type":"model_not_found"}`))
	}))
	defer vercel.Close()
	cl = h.client(BackendCompatible, func(c *Config) { c.BaseURL = vercel.URL + "/typesafe" })
	if _, err = decide(t, cl, testRequest(""), CallOptions{}); !errors.As(err, &e) || e.Kind != KindBadRequest || e.Message != "Model typesafe-ai/jev is not available" {
		t.Errorf("vercel envelope: %+v", err)
	}
}

func TestBreakerOpensAndSkips(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		h.withFault("503")(c)
		c.HTTP.MaxRetries, c.HTTP.BreakerFailures = 0, 3
	})
	for i := range 3 {
		if _, err := decide(t, cl, testRequest("t"), CallOptions{}); ErrorKind(err) != KindRetryable {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err := decide(t, cl, testRequest("t"), CallOptions{})
	if reason, ok := SkipReason(err); !ok || reason != "breaker_open" || !strings.Contains(err.Error(), "retryable") {
		t.Fatalf("after 3 faults: %v", err)
	}
	if n := len(h.decisionRequests()); n != 3 {
		t.Errorf("an open breaker must not call the vendor: %d requests", n)
	}
	st := cl.Status()
	if st.Breaker != BreakerOpen || st.State != "breaker_open" || st.BreakerUntil == nil || st.LastError == nil || st.LastError.Kind != KindRetryable {
		t.Errorf("status %+v", st)
	}
	if metricValue("trustagent_gateway_jev_breaker_state", "backend", BackendOpenRouterDecisions) != 2 {
		t.Error("breaker gauge")
	}
}

func TestBreakerIgnoresOwnCancellations(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		h.withFault("hang")(c)
		c.HTTP.MaxRetries, c.HTTP.BreakerFailures, c.HTTP.AttemptTimeout = 0, 2, 10*time.Second
	})
	for i := range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		_, err := cl.Decide(ctx, testRequest("t"), CallOptions{})
		cancel()
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindTimeout || e.Retryable || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	for _, ev := range h.waitEvents(4) {
		if ev.Status != AttemptCancelled || !ev.Late || !ev.Estimated || ev.CostUSD <= 0 {
			t.Errorf("a cancelled attempt that was sent is booked at its estimate: %+v", ev)
		}
	}
	if st := cl.Status(); st.Breaker != BreakerClosed || cl.breaker.view().consecutive != 0 || st.LastError != nil {
		t.Fatalf("our own deadline cancellations are not vendor faults: %+v", st)
	}

	// The attempt timeout is a vendor fault.
	slow := h.client(BackendOpenRouterDecisions, func(c *Config) {
		h.withFault("hang")(c)
		c.HTTP.MaxRetries, c.HTTP.BreakerFailures, c.HTTP.AttemptTimeout = 0, 2, 60*time.Millisecond
	})
	for i := range 2 {
		_, err := decide(t, slow, testRequest("t"), CallOptions{})
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindTimeout || !e.Retryable || !errors.Is(err, errAttemptTimeout) {
			t.Fatalf("attempt timeout %d: %v", i, err)
		}
	}
	if _, err := decide(t, slow, testRequest("t"), CallOptions{}); ErrorKind(err) != KindBreakerOpen {
		t.Fatalf("two attempt timeouts open the breaker: %v", err)
	}
}

func TestHedgeCancelsTheLoser(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, h.withFault("hang@1")) // the first request of a session hangs
	lostBefore := metricValue("trustagent_gateway_jev_requests_total", "backend", BackendOpenRouterDecisions, "outcome", AttemptHedgeLost)
	start := time.Now()
	resp, err := decide(t, cl, testRequest("trace-hedge"), CallOptions{HedgeAfter: 50 * time.Millisecond, TraceID: "trace-hedge"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Hedged || resp.Attempts != 2 || len(resp.Answers) != 3 {
		t.Fatalf("response %+v", resp)
	}
	if el := time.Since(start); el < 50*time.Millisecond || el > 3*time.Second {
		t.Errorf("the hedge answered after %s", el)
	}
	// The loser hangs until cancelled; its cost event proves the cancellation reached it.
	events := h.waitEvents(2)
	byAttempt := map[int]CostEvent{}
	for _, ev := range events {
		byAttempt[ev.Attempt] = ev
	}
	win, lost := byAttempt[1], byAttempt[0]
	if win.Status != AttemptOK || !win.Hedge || win.CostSource != CostReported {
		t.Errorf("winner %+v", win)
	}
	if lost.Status != AttemptHedgeLost || lost.Hedge || !lost.Estimated || lost.CostUSD <= 0 || lost.TraceID != "trace-hedge" {
		t.Errorf("loser %+v", lost)
	}
	if got := metricValue("trustagent_gateway_jev_requests_total", "backend", BackendOpenRouterDecisions, "outcome", AttemptHedgeLost) - lostBefore; got != 1 {
		t.Errorf("hedge_lost outcomes %v", got)
	}
	if v := cl.breaker.view(); v.state != BreakerClosed || v.consecutive != 0 {
		t.Errorf("a hedge loser is not a vendor fault: %+v", v)
	}
	if n := len(h.decisionRequests()); n != 2 {
		t.Errorf("%d vendor requests", n)
	}

	// No hedge when the first attempt answers in time.
	h2 := newHarness(t, mockupstream.Config{})
	resp, err = decide(t, h2.client(BackendOpenRouterDecisions, nil), testRequest("t"), CallOptions{HedgeAfter: 2 * time.Second})
	if err != nil || resp.Hedged || resp.Attempts != 1 {
		t.Errorf("unhedged %+v %v", resp, err)
	}
}

func TestLateDecisions(t *testing.T) {
	h := newHarness(t, mockupstream.Config{JevLatency: 200 * time.Millisecond})
	cl := h.client(BackendOpenRouterDecisions, nil)
	type late struct {
		resp *Response
		err  error
	}
	got := make(chan late, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := cl.Decide(ctx, testRequest("trace-late"), CallOptions{OnLate: func(r *Response, err error) { got <- late{r, err} }})
	var je *Error
	if ErrorKind(err) != KindTimeout || !errors.As(err, &je) || !je.Continued {
		t.Fatalf("the waiter stops at its deadline, and the attempt continues: %v", err)
	}
	select {
	case l := <-got:
		if l.err != nil || !l.resp.Late || len(l.resp.Answers) != 3 || l.resp.Snapshot != mockupstream.DefaultSnapshot {
			t.Fatalf("late decision %+v %v", l.resp, l.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no late decision")
	}
	if ev := h.waitEvents(1)[0]; ev.Status != AttemptOK || !ev.Late || ev.CostSource != CostReported {
		t.Errorf("late cost event %+v", ev)
	}

	// Without OnLate the attempt is cancelled.
	h2 := newHarness(t, mockupstream.Config{JevLatency: 200 * time.Millisecond})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel2()
	_, err = h2.client(BackendOpenRouterDecisions, nil).Decide(ctx2, testRequest("t"), CallOptions{})
	if !errors.As(err, &je) || je.Continued {
		t.Errorf("a cancelled attempt does not continue: %v", err)
	}
	if ev := h2.waitEvents(1)[0]; ev.Status != AttemptCancelled || !ev.Late {
		t.Errorf("cancelled cost event %+v", ev)
	}

	// Continuation needs in-flight below half the cap: with a cap of 2, one call is already half.
	h3 := newHarness(t, mockupstream.Config{JevLatency: 200 * time.Millisecond})
	busy := h3.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.MaxInFlight = 2 })
	var called atomic.Bool
	ctx3, cancel3 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel3()
	_, err = busy.Decide(ctx3, testRequest("t"), CallOptions{OnLate: func(*Response, error) { called.Store(true) }})
	if !errors.As(err, &je) || je.Continued {
		t.Errorf("an attempt cancelled past 50%% in flight does not continue: %v", err)
	}
	if ev := h3.waitEvents(1)[0]; ev.Status != AttemptCancelled {
		t.Errorf("continuation past 50%% in flight: %+v", ev)
	}
	time.Sleep(50 * time.Millisecond)
	if called.Load() {
		t.Error("OnLate called for a cancelled attempt")
	}
}

func TestSnapshotChangeHook(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	var mu sync.Mutex
	var changes []SnapshotChange
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.OnSnapshotChange = func(s SnapshotChange) { mu.Lock(); changes = append(changes, s); mu.Unlock() }
	})
	seen := func() []SnapshotChange {
		mu.Lock()
		defer mu.Unlock()
		return append([]SnapshotChange(nil), changes...)
	}
	changedBefore := metricValue("trustagent_gateway_jev_anomalies_total", "kind", AnomalySnapshotChanged)

	for range 2 {
		if _, err := decide(t, cl, testRequest("t"), CallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if c := seen(); len(c) != 1 || c[0] != (SnapshotChange{Backend: BackendOpenRouterDecisions, Model: DefaultOpenRouterModel, Snapshot: mockupstream.DefaultSnapshot}) {
		t.Fatalf("first sighting %+v", c)
	}
	const next = "typesafe/jev-1.13-20261015"
	h.mock.SetSnapshot(next)
	if _, err := decide(t, cl, testRequest("t"), CallOptions{}); err != nil {
		t.Fatal(err)
	}
	c := seen()
	if len(c) != 2 || c[1].Previous != mockupstream.DefaultSnapshot || c[1].Snapshot != next {
		t.Fatalf("change %+v", c)
	}
	if cl.Status().Snapshot != next {
		t.Errorf("status snapshot %q", cl.Status().Snapshot)
	}
	if metricValue("trustagent_gateway_jev_anomalies_total", "kind", AnomalySnapshotChanged)-changedBefore != 1 {
		t.Error("snapshot_changed anomaly")
	}
	if metricValue("trustagent_gateway_jev_snapshot_info", "snapshot", next) != 1 {
		t.Error("snapshot info gauge")
	}
	// A route pinning another model is tracked separately.
	pinned := testRequest("t")
	pinned.Model = "~typesafe/jev-latest"
	if _, err := decide(t, cl, pinned, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if c := seen(); len(c) != 3 || c[2].Model != "~typesafe/jev-latest" || c[2].Previous != "" || cl.Status().Snapshot != next {
		t.Errorf("pinned model %+v", c)
	}

	// A known snapshot from before a restart is not a change.
	h.mock.SetSnapshot("")
	var fired atomic.Bool
	known := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.KnownSnapshot, c.OnSnapshotChange = mockupstream.DefaultSnapshot, func(SnapshotChange) { fired.Store(true) }
	})
	if _, err := decide(t, known, testRequest("t"), CallOptions{}); err != nil || fired.Load() {
		t.Errorf("known snapshot: fired %v, err %v", fired.Load(), err)
	}
}

func TestCPTSelfCalibration(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.CharsPerToken = 3 })
	if cl.CPT() != 3 {
		t.Fatalf("initial cpt %v", cl.CPT())
	}
	req := testRequest("t")
	resp, err := decide(t, cl, req, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	count, err := decision.CountTokens(map[string]any{"model": DefaultOpenRouterModel, "state": req.State, "questions": req.Questions}, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := decision.UpdateCPT(3, count.Narrow, *resp.InputTokens-count.Wide)
	if got := cl.CPT(); got != want || got == 3 {
		t.Errorf("cpt %v, want %v", got, want)
	}
	if cl.Status().CPT != cl.CPT() {
		t.Error("status cpt")
	}
}

func TestBudgetCapsSpend(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	req := testRequest("")
	count, _ := decision.CountTokens(map[string]any{"model": DefaultTypeSafeModel, "state": req.State, "questions": req.Questions}, decision.DefaultCPT)
	estUSD := float64(decision.Overhead+count.Est) * 1000 / 1e6
	cl := h.client(BackendTypeSafe, func(c *Config) {
		c.HTTP.PricePerMTok, c.HTTP.SpendCapUSDDay, c.HTTP.EnforcingReserve = 1000, 2.2*estUSD, 0.5
	})
	if _, err := decide(t, cl, req, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err := decide(t, cl, req, CallOptions{})
	if reason, ok := SkipReason(err); !ok || reason != "budget" {
		t.Fatalf("observe/flag stop at the soft cap: %v", err)
	}
	if _, err := decide(t, cl, req, CallOptions{Enforcing: true}); err != nil {
		t.Fatalf("enforcing routes may spend the reserve: %v", err)
	}
	if n := len(h.decisionRequests()); n != 2 {
		t.Errorf("a budget refusal makes no request: %d requests", n)
	}
	st := cl.Status()
	if st.Spend == nil || st.Spend.SpentUSD <= estUSD || st.State != "budget_exhausted" {
		t.Errorf("status %+v spend %+v", st, st.Spend)
	}
}

func TestLimiterThroughClient(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	clk := newFakeClock()
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.HTTP.RPM, c.HTTP.SyncReserve, c.HTTP.Now = 60, 0.5, clk.Now
	})
	before := metricValue("trustagent_gateway_jev_rate_limited_total", "class", "async")
	for i := range 5 {
		if _, err := decide(t, cl, testRequest("t"), CallOptions{Class: ClassAsync}); err != nil {
			t.Fatalf("async %d: %v", i, err)
		}
	}
	_, err := decide(t, cl, testRequest("t"), CallOptions{Class: ClassAsync})
	if reason, ok := SkipReason(err); !ok || reason != "rate_limited" {
		t.Fatalf("async beyond the reserve: %v", err)
	}
	if _, err := decide(t, cl, testRequest("t"), CallOptions{Class: ClassSync}); err != nil {
		t.Fatalf("sync uses the reserve: %v", err)
	}
	if metricValue("trustagent_gateway_jev_rate_limited_total", "class", "async")-before != 1 {
		t.Error("rate-limited metric")
	}
	if n := len(h.decisionRequests()); n != 6 {
		t.Errorf("%d requests", n)
	}
}

func TestInFlightCapThroughClient(t *testing.T) {
	h := newHarness(t, mockupstream.Config{JevLatency: time.Second})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.MaxInFlight, c.HTTP.SyncReserve = 2, 0.5 })
	done := make(chan error, 1)
	go func() {
		_, err := decide(t, cl, testRequest("a"), CallOptions{Class: ClassAsync})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for cl.Status().InFlight != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first call never went in flight")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := decide(t, cl, testRequest("b"), CallOptions{Class: ClassAsync}); ErrorKind(err) != KindOverload {
		t.Errorf("async past its in-flight share: %v", err)
	}
	if _, err := decide(t, cl, testRequest("c"), CallOptions{Class: ClassSync}); err != nil {
		t.Errorf("sync keeps its reserved slot: %v", err)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
}

func TestKeepWarm(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.KeepAlive = 30 * time.Millisecond })
	keyChecks := func() int {
		n := 0
		for _, r := range h.mock.Requests() {
			if r.Method == http.MethodGet && r.Path == "/api/v1/key" {
				n++
			}
		}
		return n
	}
	if keyChecks() != 0 {
		t.Fatal("keep-warm runs only while a sync route exists")
	}
	cl.SetKeepWarm(true)
	cl.SetKeepWarm(true) // idempotent
	if !cl.Status().KeepWarm {
		t.Error("status keep_warm")
	}
	deadline := time.Now().Add(5 * time.Second)
	for keyChecks() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d keep-warm requests", keyChecks())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cl.SetKeepWarm(false)
	n := keyChecks()
	time.Sleep(120 * time.Millisecond)
	if keyChecks() != n {
		t.Error("keep-warm continued after SetKeepWarm(false)")
	}
	for _, r := range h.mock.Requests() {
		if r.Path == "/api/v1/key" && (r.Header.Get("Authorization") != "[redacted]" || r.Header.Get("X-OpenRouter-Title") != "TrustAgent") {
			t.Fatalf("keep-warm headers %v", r.Header)
		}
	}
	_ = cl.Close()
	cl.SetKeepWarm(true)
	if cl.Status().KeepWarm {
		t.Error("keep-warm after Close")
	}

	// Elsewhere keep-warm is a HEAD of the endpoint.
	ts := h.client(BackendTypeSafe, func(c *Config) { c.HTTP.KeepAlive = time.Hour })
	ts.SetKeepWarm(true)
	deadline = time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, r := range h.mock.Requests() {
			found = found || r.Method == http.MethodHead && r.Path == "/v1/systemone"
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no HEAD keep-warm")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ts.SetKeepWarm(false)
}

func TestCheckKey(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	ctx := context.Background()
	ki, err := h.client(BackendOpenRouterDecisions, nil).CheckKey(ctx)
	if err != nil || ki.Label == "" || ki.UsageUSD != 0.0123 || ki.LimitUSD != nil || ki.RateLimit == nil || ki.RateLimit.Requests != 1000 {
		t.Fatalf("key info %+v %v", ki, err)
	}
	ki, err = h.client(BackendOpenRouterDecisions, func(c *Config) { c.OpenRouterKey = "sk-or-nocredits-1" }).CheckKey(ctx)
	if err != nil || ki.LimitRemainingUSD == nil || *ki.LimitRemainingUSD != 0 {
		t.Fatalf("exhausted key %+v %v", ki, err)
	}
	bad := h.client(BackendOpenRouterDecisions, func(c *Config) { c.OpenRouterKey = "sk-or-invalid-1" })
	if _, err := bad.CheckKey(ctx); ErrorKind(err) != KindAuth {
		t.Fatalf("invalid key: %v", err)
	}
	if st := bad.Status(); st.State != "unauthorized" || st.Breaker != BreakerClosed {
		t.Errorf("a failed key check shows in status but never opens the breaker: %+v", st)
	}
	if _, err := h.client(BackendTypeSafe, nil).CheckKey(ctx); !errors.Is(err, ErrKeyCheckUnsupported) {
		t.Errorf("typesafe key check: %v", err)
	}
	if _, err := h.client(BackendOpenRouterDecisions, h.withFault("html200")).CheckKey(ctx); ErrorKind(err) != KindBadContentType {
		t.Errorf("key check behind a wrong base URL: %v", err)
	}
}

func TestProbe(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	resp, err := Probe(context.Background(), h.client(BackendOpenRouterSystemOne, nil))
	if err != nil || resp.Answers[ProbeQuestionID] == nil {
		t.Fatalf("probe %+v %v", resp, err)
	}
	if ev := h.waitEvents(1)[0]; ev.Component != ComponentProbe || ev.Questions != 1 {
		t.Errorf("probe cost event %+v", ev)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(h.decisionRequests()[0].Body), &body)
	if st, _ := body["state"].(map[string]any); st["note"] != "TrustAgent connectivity probe" {
		t.Errorf("probe body %v", body)
	}
	mock, _ := New(Config{Backend: BackendMock, Mock: MockConfig{Mode: MockHash}})
	if resp, err := Probe(context.Background(), mock); err != nil || resp.Answers[ProbeQuestionID] == nil {
		t.Errorf("mock probe %+v %v", resp, err)
	}
	h2 := newHarness(t, mockupstream.Config{})
	if _, err := Probe(context.Background(), h2.client(BackendOpenRouterDecisions, h2.withFault("401"))); ErrorKind(err) != KindAuth {
		t.Errorf("probe error %v", err)
	}
}

func TestForwardScenario(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	req := &Request{SessionID: "t", State: map[string]any{"assistant_response": "x"}, Questions: map[string]any{
		"groundedness__v1":  testQuestions()["groundedness__v1"],
		"contradiction__v1": testQuestions()["groundedness__v1"],
	}}
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.ForwardScenario = true })
	resp, err := decide(t, cl, req, CallOptions{Scenario: "refund-contradiction-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if *resp.Answers["groundedness__v1"].Noul != 0.96 || *resp.Answers["contradiction__v1"].Noul != 0.98 {
		t.Errorf("scenario answers %+v", resp.Answers)
	}
	if got := h.decisionRequests()[0].Header.Get("X-TrustAgent-Scenario"); got != "refund-contradiction-flag" {
		t.Errorf("scenario header %q", got)
	}
}

func TestProbabilitiesPassThroughToNormalize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenarios.json")
	pack := `{"scenarios":[{"id":"rounded","mock_answers":{
	  "refusal__v1":{"type":"choice","choice":"answers_request","probabilities":{"answers_request":0.33,"declines_or_deflects":0.33,"nothing_to_answer":0.33}},
	  "tone__v1":{"type":"choice","choice":"a","probabilities":{"a":0.5,"b":0.45}}}}]}`
	if err := os.WriteFile(path, []byte(pack), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, mockupstream.Config{ScenariosPath: path})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.ForwardScenario = true })
	req := &Request{SessionID: "t", State: map[string]any{"assistant_response": "x"}, Questions: map[string]any{
		"refusal__v1": testQuestions()["refusal__v1"],
		"tone__v1":    map[string]any{"type": "choice", "instructions": "i", "criteria": map[string]any{"a": nil, "b": nil}},
	}}
	sumBefore := metricValue("trustagent_gateway_jev_anomalies_total", "kind", AnomalyProbabilitySum)
	resp, err := decide(t, cl, req, CallOptions{Scenario: "rounded"})
	if err != nil {
		t.Fatal(err)
	}
	raw := resp.Answers["refusal__v1"]
	if raw.Probabilities["answers_request"] != 0.33 || len(raw.Probabilities) != 3 {
		t.Fatalf("the client must not touch probabilities: %+v", raw)
	}
	if got := metricValue("trustagent_gateway_jev_anomalies_total", "kind", AnomalyProbabilitySum) - sumBefore; got != 1 {
		t.Errorf("probability_sum anomalies %v, want 1 (0.95 counts, 0.99 does not)", got)
	}
	spec, err := decision.ParseSpec([]byte(`{"spec_version":"evaluator/v1","id":"refusal","version":1,"name":"R","category":"quality","state_fields":["assistant_response"],
	  "question":{"type":"choice","instructions":"i","criteria":{"answers_request":{"what":"a"},"declines_or_deflects":{"what":"b"},"nothing_to_answer":null}},
	  "evidence":{"requires":"none"},"signal":{"risk_weights":{"declines_or_deflects":1}},"outcomes":{"warn_at":0.7}}`))
	if err != nil {
		t.Fatal(err)
	}
	n := decision.Normalize(raw, spec)
	var sum float64
	for _, p := range n.Probabilities {
		sum += p
	}
	if n.Error != "" || math.Abs(sum-1) > 1e-12 || math.Abs(n.RiskRaw-1.0/3) > 1e-12 || len(n.Anomalies) != 0 {
		t.Errorf("normalized %+v (sum %v)", n, sum)
	}
}

func TestKeyRotationResetsTheBreaker(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	var key atomic.Value
	key.Store("sk-or-invalid-9")
	var hosts []string
	var mu sync.Mutex
	cl := h.client(BackendAuto, func(c *Config) {
		c.OpenRouterBaseURL = h.srv.URL
		c.Binding = &Binding{Backend: BackendOpenRouterDecisions, Name: "openrouter", Key: func(_ context.Context, host string) (string, error) {
			mu.Lock()
			hosts = append(hosts, host)
			mu.Unlock()
			return key.Load().(string), nil
		}}
	})
	if cl.Info().CredentialSource != "dashboard" {
		t.Errorf("credential source %q", cl.Info().CredentialSource)
	}
	if _, err := decide(t, cl, testRequest("t"), CallOptions{}); ErrorKind(err) != KindAuth {
		t.Fatalf("invalid key: %v", err)
	}
	if _, err := decide(t, cl, testRequest("t"), CallOptions{}); ErrorKind(err) != KindBreakerOpen {
		t.Fatalf("auth opens the breaker: %v", err)
	}
	if st := cl.Status(); st.State != "unauthorized" {
		t.Errorf("state %q", st.State)
	}
	key.Store(testOpenRouterKey)
	if _, err := decide(t, cl, testRequest("t"), CallOptions{}); err != nil {
		t.Fatalf("a new key resets the breaker: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) == 0 || hosts[0] != "127.0.0.1" {
		t.Errorf("KeyFunc hosts %v", hosts)
	}
}

func TestCredentialsUnavailable(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendAuto, func(c *Config) {
		c.OpenRouterBaseURL = h.srv.URL
		c.Binding = &Binding{Backend: BackendOpenRouterDecisions, Key: func(context.Context, string) (string, error) {
			return "", errors.New("store unavailable: master key unreadable")
		}}
	})
	_, err := decide(t, cl, testRequest("t"), CallOptions{})
	if reason, ok := SkipReason(err); !ok || reason != "credentials_unavailable" {
		t.Fatalf("err %v", err)
	}
	if len(h.decisionRequests()) != 0 {
		t.Error("no request without a credential")
	}
}

func TestDecideEdgeCases(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cl.Decide(ctx, testRequest("t"), CallOptions{}); ErrorKind(err) != KindTimeout {
		t.Errorf("a finished context: %v", err)
	}
	if _, err := cl.Decide(context.Background(), nil, CallOptions{}); ErrorKind(err) != KindBadRequest {
		t.Errorf("nil request: %v", err)
	}
	bad := testRequest("t")
	bad.State = map[string]any{"x": math.NaN()}
	if _, err := cl.Decide(context.Background(), bad, CallOptions{}); ErrorKind(err) != KindBadRequest {
		t.Errorf("unencodable request: %v", err)
	}
	if len(h.decisionRequests()) != 0 {
		t.Error("no request was valid")
	}
	if st := cl.Status(); st.State != "checking" || st.Calls != 3 {
		t.Errorf("status %+v", st)
	}
	// A long session id is cut to OpenRouter's 256 characters.
	long := testRequest(strings.Repeat("é", 300))
	if _, err := decide(t, cl, long, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(h.decisionRequests()[0].Body), &body)
	if len([]rune(body.SessionID)) != 256 {
		t.Errorf("session id of %d runes", len([]rune(body.SessionID)))
	}
	// SendSessionID=false omits it.
	h2 := newHarness(t, mockupstream.Config{})
	if _, err := decide(t, h2.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.SendSessionID = false }), testRequest("t"), CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h2.decisionRequests()[0].Body, "session_id") {
		t.Error("session_id sent although disabled")
	}
}

func TestNetworkErrorsAndRedirects(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	cl, err := New(Config{Backend: BackendOpenRouterDecisions, OpenRouterKey: testOpenRouterKey, OpenRouterBaseURL: deadURL,
		HTTP: &HTTPConfig{MaxRetries: 1, KeepAlive: 0}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = decide(t, cl, testRequest("t"), CallOptions{})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindRetryable || !e.Retryable || e.Attempts != 2 || !strings.Contains(e.Message, "network error") {
		t.Fatalf("connection refused: %+v", err)
	}
	if st := cl.(StatusReporter).Status(); st.State != "unreachable" {
		t.Errorf("state %q", st.State)
	}

	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHit.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/login?next=x", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	cl, _ = New(Config{Backend: BackendOpenRouterDecisions, OpenRouterKey: testOpenRouterKey, OpenRouterBaseURL: redirect.URL})
	_, err = decide(t, cl, testRequest("t"), CallOptions{})
	if !errors.As(err, &e) || e.Kind != KindNotFound || !strings.Contains(e.Message, "redirects to "+target.URL+"/login") || strings.Contains(e.Message, "next=x") {
		t.Errorf("redirect: %+v", err)
	}
	if targetHit.Load() {
		t.Error("a redirect was followed with the key")
	}
}

func TestLogPayloadsNeverLogsTheKey(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	var buf bytes.Buffer
	var mu sync.Mutex
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.HTTP.LogPayloads = true
		c.Logger = newTestLogger(&buf, &mu)
	})
	if _, err := decide(t, cl, testRequest("t"), CallOptions{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "jev request") || !strings.Contains(out, "jev response") || strings.Contains(out, testOpenRouterKey) {
		t.Errorf("payload log:\n%s", out)
	}
}

// lockedWriter serializes writes of concurrent log calls.
type lockedWriter struct {
	buf *bytes.Buffer
	mu  *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func newTestLogger(buf *bytes.Buffer, mu *sync.Mutex) *slog.Logger {
	return slog.New(slog.NewTextHandler(lockedWriter{buf, mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// metricValue reads a counter or gauge from the default registry by name and label pairs
// (prometheus testutil would add a module dependency).
func metricValue(name string, labelPairs ...string) float64 {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		panic(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			for i := 0; i+1 < len(labelPairs); i += 2 {
				found := false
				for _, l := range m.GetLabel() {
					found = found || l.GetName() == labelPairs[i] && l.GetValue() == labelPairs[i+1]
				}
				if !found {
					continue metrics
				}
			}
			return m.GetCounter().GetValue() + m.GetGauge().GetValue()
		}
	}
	return 0
}

func TestKeyCheckStaysOnTheDecisionOrigin(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		keyURL string
	}{
		{"default", Config{Backend: BackendOpenRouterDecisions, OpenRouterKey: "k"}, "https://openrouter.ai/api/v1/key"},
		{"OpenRouter base URL", Config{OpenRouterKey: "k", OpenRouterBaseURL: "http://mock-upstream:8090"}, "http://mock-upstream:8090/api/v1/key"},
		{"explicit .../api base", Config{Backend: BackendOpenRouterDecisions, JevAPIKey: "k", BaseURL: "https://or-proxy.example/api/"},
			"https://or-proxy.example/api/v1/key"},
		{"explicit base on the OpenRouter origin", Config{Backend: BackendOpenRouterSystemOne, OpenRouterKey: "k", BaseURL: "https://openrouter.ai"},
			"https://openrouter.ai/api/v1/key"},
		{"explicit gateway base", Config{Backend: BackendOpenRouterDecisions, JevAPIKey: "corp-secret", BaseURL: "https://gw.corp.example/openrouter"}, ""},
		{"typesafe", Config{TypeSafeKey: "k"}, ""},
		{"compatible", Config{BaseURL: "https://ai-gateway.vercel.sh/typesafe", JevAPIKey: "k", Model: "typesafe-ai/jev"}, ""},
	}
	for _, c := range cases {
		if r := resolve(c.cfg); r.ep.keyURL != c.keyURL || r.backend == BackendOff {
			t.Errorf("%s: backend %s key URL %q, want %q", c.name, r.backend, r.ep.keyURL, c.keyURL)
		}
	}

	// End to end: a gateway credential for an OpenRouter-shaped endpoint never reaches the
	// OpenRouter base (here the mock), neither through the key check nor through keep-warm.
	h := newHarness(t, mockupstream.Config{})
	var mu sync.Mutex
	var seen []string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"d","model":"typesafe/jev-1.13-20260917","answers":{},"usage":{"input_tokens":300}}`)
	}))
	defer gw.Close()
	const secret = "corp-gateway-secret-0123"
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) {
		c.OpenRouterKey, c.JevAPIKey, c.BaseURL = "", secret, gw.URL+"/openrouter"
		c.HTTP.KeepAlive = 20 * time.Millisecond
	})
	if _, err := cl.CheckKey(context.Background()); !errors.Is(err, ErrKeyCheckUnsupported) {
		t.Fatalf("key check off the decision origin: %v", err)
	}
	cl.SetKeepWarm(true)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no keep-warm request reached the gateway")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cl.SetKeepWarm(false)
	mu.Lock()
	if seen[0] != "HEAD /openrouter/alpha/decisions auth=" {
		t.Errorf("keep-warm %q, want an unauthenticated HEAD of the endpoint", seen[0])
	}
	mu.Unlock()
	if reqs := h.mock.Requests(); len(reqs) != 0 {
		t.Errorf("the OpenRouter base got %d requests: %+v", len(reqs), reqs[0])
	}
}

func TestInFlightBudgetIsNotNoCredits(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	busy := h.client(BackendOpenRouterDecisions, h.withFault("402_inflight"))
	if _, err := decide(t, busy, testRequest("t"), CallOptions{}); ErrorKind(err) != KindCredits {
		t.Fatalf("in-flight 402: %v", err)
	}
	if st := busy.Status(); st.State != "degraded" || st.LastError == nil || st.LastError.Kind != KindCredits || st.Breaker != BreakerClosed {
		t.Errorf("a transient in-flight 402 is busy, not out of credits: %+v", st)
	}
	broke := h.client(BackendOpenRouterDecisions, h.withFault("402_credits"))
	if _, err := decide(t, broke, testRequest("t2"), CallOptions{}); ErrorKind(err) != KindCredits {
		t.Fatalf("credits 402: %v", err)
	}
	if st := broke.Status(); st.State != "no_credits" {
		t.Errorf("an empty account shows no_credits: %+v", st)
	}
}

func TestEchoedCredentialIsMasked(t *testing.T) {
	const key = "ts-live-ABCDEFGH12345678"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"error_type":"auth","message":"Unknown API key `+key+`"}}`)
	}))
	defer srv.Close()
	cl, err := New(Config{Backend: BackendTypeSafe, TypeSafeKey: key, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = decide(t, cl, testRequest(""), CallOptions{})
	if ErrorKind(err) != KindAuth || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "Unknown API key [redacted]") {
		t.Errorf("error %v", err)
	}
	if st := cl.(StatusReporter).Status(); st.LastError == nil || strings.Contains(st.LastError.Message, key) {
		t.Errorf("status %+v", st.LastError)
	}
}

func TestCloseStopsKeepWarmForGood(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	cl := h.client(BackendOpenRouterDecisions, func(c *Config) { c.HTTP.KeepAlive = time.Hour })
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl.SetKeepWarm(i%2 == 0)
		}()
	}
	_ = cl.Close()
	wg.Wait()
	if cl.Status().KeepWarm {
		t.Error("keep-warm runs after Close")
	}
}

func TestBudgetRefusalsTakeNoRateTokens(t *testing.T) {
	h := newHarness(t, mockupstream.Config{})
	req := testRequest("")
	count, _ := decision.CountTokens(map[string]any{"model": DefaultTypeSafeModel, "state": req.State, "questions": req.Questions}, decision.DefaultCPT)
	estUSD := float64(decision.Overhead+count.Est) * 1000 / 1e6
	clk := newFakeClock()
	cl := h.client(BackendTypeSafe, func(c *Config) {
		c.HTTP.PricePerMTok, c.HTTP.SpendCapUSDDay, c.HTTP.EnforcingReserve = 1000, 2.2*estUSD, 0.5
		c.HTTP.RPM, c.HTTP.Now = 60, clk.Now // a 10-token bucket that never refills
	})
	if _, err := decide(t, cl, req, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for i := range 15 { // past the soft cap: flag traffic keeps coming
		if reason, _ := SkipReason(func() error { _, err := decide(t, cl, req, CallOptions{}); return err }()); reason != "budget" {
			t.Fatalf("call %d: skip %q, want budget", i, reason)
		}
	}
	if _, err := decide(t, cl, req, CallOptions{Enforcing: true}); err != nil {
		t.Fatalf("an enforcing route still gets its reserve and a rate token: %v", err)
	}
	if st := cl.Status(); st.RPMUsed != 2 {
		t.Errorf("rpm_used %d, want the 2 calls that went out", st.RPMUsed)
	}
}
