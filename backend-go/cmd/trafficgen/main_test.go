package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
)

const testKey = "tg-key"

// stubProxy answers the scenarios as a gateway would, from each scenario's expect: the delivered
// text in the scenario's format (JSON, or an event stream when it streams), the decision as
// headers (a passthrough stream: only Trace-Id and Route as headers, the decision as the
// trustagent comment and the Mode and Action trailers; an observe route: Status deferred), and a
// trace on /api/traces/:id whose audit is queued for the first polls, then settles.
type stubProxy struct {
	t         *testing.T
	scenarios map[string]*scenario
	// mutate lets a test change the headers of one scenario.
	mutate func(id string, h http.Header)
	// text overrides the delivered text; noComment and noTrailers drop the stream decision.
	text       map[string]string
	noComment  map[string]bool
	noTrailers map[string]bool
	// verdict and action override the settled audit verdict and the stored action of a scenario;
	// would overrides the stored would_action.
	verdict map[string]string
	action  map[string]string
	would   map[string]string
	// status is the GET /api/status body; nil answers 404. neverSettle keeps a scenario's audit
	// queued.
	status      map[string]any
	neverSettle map[string]bool

	mu          sync.Mutex
	statusReads int
	polls       map[string]int
	requests    []*http.Request
	bodies      [][]byte
}

func newStub(t *testing.T) (*stubProxy, *httptest.Server) {
	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	s := &stubProxy{t: t, scenarios: map[string]*scenario{}, polls: map[string]int{}, verdict: map[string]string{},
		action: map[string]string{}, would: map[string]string{}, text: map[string]string{}, noComment: map[string]bool{},
		noTrailers: map[string]bool{}, neverSettle: map[string]bool{}}
	for i := range all {
		s.scenarios[all[i].ID] = &all[i]
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

// delivered is the text the stub answers a scenario with.
func (s *stubProxy) delivered(sc *scenario) string {
	if v, ok := s.text[sc.ID]; ok {
		return v
	}
	e := expectation(sc.Expect)
	if present, null := e.has("delivered_text"); present && !null {
		return e.str("delivered_text")
	}
	return *sc.Response
}

func (s *stubProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && slices.Contains([]string{"/v1/chat/completions", "/v1/completions", "/v1/messages"}, r.URL.Path):
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		sc, ok := s.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
		if !ok || r.Header.Get("X-TrustAgent-Key") != testKey || apiPaths[sc.APIFormat] != r.URL.Path {
			http.Error(w, "unknown scenario, key or path", http.StatusBadRequest)
			return
		}
		s.answer(w, sc)
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		if r.Header.Get("X-TrustAgent-Key") != testKey {
			http.Error(w, `{"error":{"code":"unauthorized","message":"no"}}`, http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		s.statusReads++
		st := s.status
		s.mu.Unlock()
		if st == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(st)
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

// decisionHeaders are the X-TrustAgent-* values of a scenario's expect.
func decisionHeaders(sc *scenario) http.Header {
	e := expectation(sc.Expect)
	h := http.Header{}
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
	return h
}

func (s *stubProxy) answer(w http.ResponseWriter, sc *scenario) {
	e := expectation(sc.Expect)
	h := w.Header()
	h.Set(headerTraceID, "trace-"+sc.ID)
	h.Set(headerRoute, sc.Route+"@v1")
	decided := decisionHeaders(sc)
	var sd *streamDecision
	if present, null := e.has("stream_decision"); present && !null {
		sd = &streamDecision{}
		_ = json.Unmarshal(e["stream_decision"], sd)
	}
	switch {
	case e.str("status") == statusDeferred:
		for _, k := range []string{headerMode, headerAction, headerStatus} {
			h.Set(k, decided.Get(k))
		}
	case sd != nil:
		if !s.noTrailers[sc.ID] {
			h.Set("Trailer", strings.Join(sd.Trailers, ", "))
		}
	default:
		for k, v := range decided {
			h[k] = v
		}
	}
	if s.mutate != nil {
		s.mutate(sc.ID, h)
	}
	status := http.StatusOK
	if present, null := e.has("http_status"); present && !null {
		_ = json.Unmarshal(e["http_status"], &status)
	}
	text := s.delivered(sc)
	if !sc.Stream {
		h.Set("Content-Type", "application/json")
		w.WriteHeader(status)
		var body any
		switch sc.APIFormat {
		case "anthropic_messages":
			body = map[string]any{"type": "message", "content": []any{map[string]any{"type": "text", "text": text}}}
		case "openai_completions":
			body = map[string]any{"object": "text_completion", "choices": []any{map[string]any{"index": 0, "text": text}}}
		default:
			body = map[string]any{"object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}}}}
		}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	h.Set("Content-Type", "text/event-stream")
	w.WriteHeader(status)
	for _, word := range strings.SplitAfter(text, " ") {
		var ev string
		switch sc.APIFormat {
		case "anthropic_messages":
			b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": word}})
			ev = "event: content_block_delta\ndata: " + string(b) + "\n\n"
		default:
			b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": word}}}})
			ev = "data: " + string(b) + "\n\n"
		}
		_, _ = io.WriteString(w, ev)
	}
	if sd != nil && sd.Comment && !s.noComment[sc.ID] {
		var reasons []string
		_ = json.Unmarshal(e["reasons"], &reasons)
		ann := map[string]any{"trace_id": "trace-" + sc.ID, "action": e.str("action"), "would_action": json.RawMessage(e["would_action"]),
			"status": e.str("status"), "verdict": json.RawMessage(e["verdict"]), "max_risk": json.RawMessage(e["max_risk"]), "reasons": reasons}
		b, _ := json.Marshal(ann)
		_, _ = io.WriteString(w, annotationPrefix+string(b)+"\n\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if sd != nil && !s.noTrailers[sc.ID] {
		for _, name := range sd.Trailers {
			h.Set(name, decided.Get(name))
		}
	}
}

// trace is the TraceDetail of a scenario at its n-th poll.
func (s *stubProxy) trace(sc *scenario, n int) map[string]any {
	e := expectation(sc.Expect)
	gateStatus := e.str("status")
	if gateStatus == statusDeferred {
		gateStatus = "ok"
	}
	audit := map[string]any{"status": e.str("audit"), "verdict": nil}
	var detail any
	switch {
	case e.str("audit") == "queued" && (n < 3 || s.neverSettle[sc.ID]):
	case e.str("audit") == "queued":
		verdict := e.str("audit_verdict")
		if v, ok := s.verdict[sc.ID]; ok {
			verdict = v
		}
		audit = map[string]any{"status": "completed", "verdict": verdict}
		var claims []map[string]any
		if sc.Judge != nil {
			for _, c := range sc.Judge.Claims {
				claims = append(claims, map[string]any{"status": c.Verdict})
			}
		}
		detail = map[string]any{"status": "completed", "verdict": verdict, "claims": claims}
	case e.str("audit") == "not_assessed":
		audit["verdict"] = "NOT_ASSESSED"
	}
	would := any(nil)
	if w := e.str("would_action"); w != "" {
		would = w
	}
	if w, ok := s.would[sc.ID]; ok {
		would = w
	}
	action := e.str("action")
	if a, ok := s.action[sc.ID]; ok {
		action = a
	}
	var reasons []string
	_ = json.Unmarshal(e["reasons"], &reasons)
	var used []map[string]any
	if c := e.str("checked_against"); c != "none" {
		for _, part := range strings.Split(c, ",") {
			src, items, _ := strings.Cut(part, ":")
			var k int
			_, _ = fmt.Sscan(items, &k)
			used = append(used, map[string]any{"source": src, "items": k})
		}
	}
	return map[string]any{"trace_id": "trace-" + sc.ID, "gate_status": gateStatus, "action": action,
		"would_action": would, "verdict": json.RawMessage(e["verdict"]), "max_risk": json.RawMessage(e["max_risk"]), "reasons": reasons,
		"checked_against": map[string]any{"used": used}, "audit": audit, "audit_detail": detail}
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

// TestEveryScenarioPassesAgainstItsExpectations sends the whole pack (--phase p3) to a stub that
// answers each scenario exactly as its expect says, in every format: every check must hold.
func TestEveryScenarioPassesAgainstItsExpectations(t *testing.T) {
	stub, srv := newStub(t)
	code, out := runTG(t, "--once", "--expect", "--scenarios", "all", "--phase", "p3", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	requests, bodies, _ := stub.snapshot()
	if len(requests) != len(stub.scenarios) {
		t.Fatalf("%d requests for %d scenarios", len(requests), len(stub.scenarios))
	}
	paths := map[string]bool{}
	for i, r := range requests {
		paths[r.URL.Path] = true
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(bodies[i], &sent); err != nil {
			t.Fatal(err)
		}
		sc := stub.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
		if (string(sent["stream"]) == "true") != sc.Stream || sent["test_response"] == nil {
			t.Errorf("%s: body %s", sc.ID, bodies[i])
		}
		for k, v := range sc.Headers {
			if r.Header.Get(k) != v {
				t.Errorf("%s: header %s %q, want %q", sc.ID, k, r.Header.Get(k), v)
			}
		}
	}
	if len(paths) != 3 {
		t.Errorf("paths %v: every api_format is exercised", paths)
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
	stub.text["no-evidence-not-assessed"] = "Something else."
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
		"FAIL no-evidence-not-assessed",
		`delivered text: want "Annual plans can be refunded within 30 days of purchase, so a plan bought 45 days ago is past the refund window.", got "Something else."`,
		"some scenarios do not match their expectations",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestStreamDecisionIsChecked: a passthrough stream must carry the decision comment and the Mode
// and Action trailers; the decision is read from them.
func TestStreamDecisionIsChecked(t *testing.T) {
	stub, srv := newStub(t)
	code, out := runTG(t, "--once", "--expect", "--scenarios", "stream-passthrough-flag", "--phase", "p3", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	stub.noComment["stream-passthrough-flag"] = true
	stub.noTrailers["stream-passthrough-flag"] = true
	code, out = runTG(t, "--once", "--expect", "--scenarios", "stream-passthrough-flag", "--phase", "p3", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitMismatch {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"stream decision comment: want a `: trustagent {...}` comment, got (absent)",
		"X-TrustAgent-Mode: want an HTTP trailer, got (absent)",
		"X-TrustAgent-Action: want an HTTP trailer, got (absent)",
		`X-TrustAgent-Action: want "flag", got (absent)`,
		`X-TrustAgent-Reasons: want to include "contradiction:critical", got (absent)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestDeferredDecisionIsCheckedOnTheTrace: an observe route answers Status: deferred, so its
// decision fields are checked on the stored trace.
func TestDeferredDecisionIsCheckedOnTheTrace(t *testing.T) {
	stub, srv := newStub(t)
	code, out := runTG(t, "--once", "--expect", "--scenarios", "observe-would-block", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	stub.would["observe-would-block"] = "flag"
	code, out = runTG(t, "--once", "--expect", "--scenarios", "observe-would-block", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitMismatch || !strings.Contains(out, "stored would_action: want block, got flag") || !strings.Contains(out, "outcome: want would_block, got would_flag") {
		t.Fatalf("exit %d:\n%s", code, out)
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
	// Without --expect a phase skip is only a note (with it, see TestExpectFailsOnAPhaseSkip).
	code, out := runTG(t, "--scenarios", "demo", "--phase", "p2", "--base-url", srv.URL)
	if _, _, polls := stub.snapshot(); code != exitOK || len(polls) != 0 {
		t.Fatalf("exit %d, polls %v:\n%s", code, polls, out)
	}
	if !strings.Contains(out, "skipping payments-fallback: it needs gateway phase p3 (this run: --phase p2)") {
		t.Errorf("phase-tagged scenarios are skipped with a note:\n%s", out)
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

// TestExpectFailsOnAPhaseSkip: with --expect, a selected scenario skipped by phase fails the check
// although every scenario sent passes (D18), unless --allow-skip accepts the skip; this build's
// default phase sends the whole demo set.
func TestExpectFailsOnAPhaseSkip(t *testing.T) {
	_, srv := newStub(t)
	args := []string{"--once", "--expect", "--scenarios", "s0,payments-fallback", "--phase", "p2", "--base-url", srv.URL, "--audit-timeout", "10s"}
	code, out := runTG(t, args...)
	if code != exitMismatch {
		t.Fatalf("exit %d, want %d:\n%s", code, exitMismatch, out)
	}
	for _, want := range []string{
		"skipping payments-fallback: it needs gateway phase p3 (this run: --phase p2)",
		"--expect skips 1 selected scenario(s) that need a later gateway phase than p2, so the check fails; pass --allow-skip",
		"ok   grounded-pass ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if code, out := runTG(t, append(args, "--allow-skip")...); code != exitOK || strings.Contains(out, "so the check fails") {
		t.Fatalf("--allow-skip: exit %d:\n%s", code, out)
	}
	code, out = runTG(t, "--once", "--expect", "--scenarios", "demo", "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK || strings.Contains(out, "skipping") || !strings.Contains(out, "ok   payments-fallback ") {
		t.Fatalf("the default phase (%s) sends the whole demo set: exit %d:\n%s", defaultPhase, code, out)
	}
}

// TestUpstreamAnswers: --upstream generated leaves test_response out of generated scenarios only,
// --upstream all out of every scenario. A generated answer from a real upstream (--upstream
// generated) is not checked, but its trace is awaited until it settles (D121); under --upstream
// all mock-upstream answers with the stand-in response, so the generated scenarios are checked
// like the others.
func TestUpstreamAnswers(t *testing.T) {
	for mode, wantScripted := range map[string]bool{upstreamNone: true, upstreamGenerated: true, upstreamAll: false} {
		stub, srv := newStub(t)
		code, out := runTG(t, "--once", "--expect", "--scenarios", "grounded-pass,generated", "--upstream", mode, "--base-url", srv.URL, "--audit-timeout", "10s")
		if code != exitOK {
			t.Fatalf("%s: exit %d:\n%s", mode, code, out)
		}
		requests, bodies, polls := stub.snapshot()
		if len(requests) != 3 {
			t.Fatalf("%s: %d requests, want grounded-pass and the generated pair", mode, len(requests))
		}
		for i, r := range requests {
			sc := stub.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
			var sent map[string]json.RawMessage
			_ = json.Unmarshal(bodies[i], &sent)
			scripted := wantScripted && !(mode == upstreamGenerated && sc.AnswerSource == answerGenerated)
			if (sent["test_response"] != nil) != scripted {
				t.Errorf("--upstream %s, %s: test_response sent %v", mode, sc.ID, sent["test_response"] != nil)
			}
			wantUnchecked := mode == upstreamGenerated && sc.AnswerSource == answerGenerated
			unchecked := strings.Contains(out, "sent "+sc.ID+" (trace trace-"+sc.ID)
			checked := strings.Contains(out, "ok   "+sc.ID+" (trace trace-"+sc.ID)
			if unchecked != wantUnchecked || checked == wantUnchecked {
				t.Errorf("--upstream %s, %s: unchecked %v, checked %v:\n%s", mode, sc.ID, unchecked, checked, out)
			}
			// Every trace is awaited until its audit settles (the stub settles at the third poll).
			if polls[sc.ID] < 3 {
				t.Errorf("--upstream %s, %s: %d trace polls, want it awaited until settled", mode, sc.ID, polls[sc.ID])
			}
		}
	}
}

// TestUncheckedTraceMustSettle: a generated upstream answer is not checked, but a trace that does
// not settle within --audit-timeout still fails the run.
func TestUncheckedTraceMustSettle(t *testing.T) {
	stub, srv := newStub(t)
	stub.neverSettle["generated-weekend-shipping"] = true
	code, out := runTG(t, "--once", "--expect", "--scenarios", "generated", "--upstream", upstreamGenerated, "--base-url", srv.URL,
		"--audit-timeout", "1s")
	if code != exitMismatch || !strings.Contains(out, "FAIL generated-weekend-shipping: trace trace-generated-weekend-shipping did not settle") ||
		!strings.Contains(out, "sent generated-monthly-refund (trace trace-generated-monthly-refund") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// TestTraceIDsOut: --trace-ids-out writes the trace id of every answered request, one per line in
// send order, also when some scenarios fail; a request that got no answer has no line. A file that
// cannot be written fails the run.
func TestTraceIDsOut(t *testing.T) {
	stub, srv := newStub(t)
	stub.mutate = func(id string, h http.Header) {
		if id == "refund-contradiction-flag" {
			h.Set(headerAction, "pass")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := runTG(t, "--once", "--expect", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "10s", "--trace-ids-out", path)
	if code != exitMismatch {
		t.Fatalf("a mismatch still fails the run: exit %d:\n%s", code, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "trace-grounded-pass\ntrace-refund-contradiction-flag\ntrace-no-evidence-not-assessed\n"
	if string(b) != want {
		t.Errorf("ids file %q, want %q", b, want)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("a temporary file was left behind: %v", entries)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer failing.Close()
	if code, out := runTG(t, "--once", "--scenarios", "s0", "--base-url", failing.URL, "--trace-ids-out", path); code != exitMismatch {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if b, _ := os.ReadFile(path); len(b) != 0 {
		t.Errorf("no request was answered with a trace, so the file is empty: %q", b)
	}
	if code, out := runTG(t, "--once", "--scenarios", "s0", "--base-url", srv.URL, "--trace-ids-out", filepath.Join(dir, "missing", "ids.txt")); code != exitMismatch ||
		!strings.Contains(out, "--trace-ids-out") {
		t.Errorf("an unwritable file fails the run: exit %d:\n%s", code, out)
	}
}

// recordableStatus is a GET /api/status body a recording run accepts: test mode, a real decision
// backend that is ready, and a judge that is up on its configured provider.
func recordableStatus() map[string]any {
	return map[string]any{
		"test_mode":      true,
		"decision_model": map[string]any{"backend": "openrouter-decisions", "state": "ready", "endpoint_host": "openrouter.ai"},
		"judge":          map[string]any{"state": "up", "provider": "openrouter", "fallback_active": false, "fallback_reason": nil},
	}
}

// TestRecord: --record checks the gateway first, then runs like --expect with every trace awaited,
// reporting a mismatch as drift without failing; a failed request still fails the run.
func TestRecord(t *testing.T) {
	stub, srv := newStub(t)
	stub.status = recordableStatus()
	stub.verdict["generated-monthly-refund"] = "HALLUCINATED" // the stand-in expects GROUNDED
	stub.mutate = func(id string, h http.Header) {
		if id == "grounded-pass" {
			h.Set(headerAction, "flag")
		}
	}
	path := filepath.Join(t.TempDir(), "ids.txt")
	code, out := runTG(t, "--record", "--scenarios", "s0,generated", "--upstream", upstreamNone, "--base-url", srv.URL,
		"--audit-timeout", "10s", "--trace-ids-out", path)
	if code != exitOK {
		t.Fatalf("drift does not fail a recording: exit %d:\n%s", code, out)
	}
	for _, want := range []string{
		"drift grounded-pass (trace trace-grounded-pass",
		`X-TrustAgent-Action: want "pass", got "flag"`,
		"drift generated-monthly-refund (trace trace-generated-monthly-refund",
		"audit verdict: want GROUNDED, got HALLUCINATED",
		"ok   refund-contradiction-flag (trace",
		"trafficgen: 5 scenario(s) recorded: every trace settled; 2 drift from their expectations (reported, not failing)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if b, _ := os.ReadFile(path); strings.Count(string(b), "\n") != 5 {
		t.Errorf("ids file %q", b)
	}
	if stub.statusReads != 1 {
		t.Errorf("%d status reads, want the one preflight", stub.statusReads)
	}

	// Under --upstream generated the generated answers are a real model's: awaited, not checked.
	code, out = runTG(t, "--record", "--scenarios", "generated", "--upstream", upstreamGenerated, "--base-url", srv.URL, "--audit-timeout", "10s")
	if code != exitOK || strings.Contains(out, "drift generated") || !strings.Contains(out, "sent generated-monthly-refund (trace") ||
		!strings.Contains(out, "0 drift from their expectations") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	stub.neverSettle["refund-contradiction-flag"] = true
	code, out = runTG(t, "--record", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "1s")
	if code != exitMismatch || !strings.Contains(out, "FAIL refund-contradiction-flag: trace trace-refund-contradiction-flag did not settle") ||
		!strings.Contains(out, "some requests failed or did not settle") {
		t.Fatalf("a trace that does not settle fails a recording: exit %d:\n%s", code, out)
	}
}

// TestRecordRefusesASimulatedGateway: --record refuses, before sending anything, a gateway whose
// traffic could only be labelled simulated or would not be exportable as recorded.
func TestRecordRefusesASimulatedGateway(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(st map[string]any)
		want string
	}{
		"not test mode":       {func(st map[string]any) { st["test_mode"] = false }, "not in test mode"},
		"mock decision model": {func(st map[string]any) { st["decision_model"] = map[string]any{"backend": "mock", "state": "mock"} }, "the decision model is mock"},
		"decision model off":  {func(st map[string]any) { st["decision_model"] = map[string]any{"backend": "off", "state": "off"} }, "the decision model is off"},
		"mock-upstream": {func(st map[string]any) {
			st["decision_model"] = map[string]any{"backend": "compatible", "state": "ready", "endpoint_host": "mock-upstream"}
		}, "answers from the local endpoint mock-upstream, which gets each scenario id in test mode"},
		"localhost": {func(st map[string]any) {
			st["decision_model"] = map[string]any{"backend": "compatible", "state": "ready", "endpoint_host": "localhost"}
		}, "answers from the local endpoint localhost"},
		"loopback": {func(st map[string]any) {
			st["decision_model"] = map[string]any{"backend": "typesafe", "state": "ready", "endpoint_host": "127.0.0.1"}
		}, "answers from the local endpoint 127.0.0.1"},
		"rejected key": {func(st map[string]any) { st["decision_model"].(map[string]any)["state"] = "unauthorized" }, "the decision model is unauthorized, so it cannot answer"},
		"breaker open": {func(st map[string]any) { st["decision_model"].(map[string]any)["state"] = "breaker_open" }, "the decision model is breaker_open"},
		"no credits":   {func(st map[string]any) { st["decision_model"].(map[string]any)["state"] = "no_credits" }, "the decision model is no_credits"},
		"mock judge":   {func(st map[string]any) { st["judge"].(map[string]any)["provider"] = "mock" }, "the judge is the mock"},
		"no judge":     {func(st map[string]any) { st["judge"].(map[string]any)["provider"] = nil }, "the deep audit has no judge"},
		"fallback judge": {func(st map[string]any) {
			j := st["judge"].(map[string]any)
			j["state"], j["fallback_active"], j["fallback_reason"] = "degraded", true, "key refused"
		}, "the judge runs on its fallback provider (key refused)"},
		"judge down":    {func(st map[string]any) { st["judge"].(map[string]any)["state"] = "down" }, "the judge is down, not up"},
		"not a status":  {func(st map[string]any) { delete(st, "test_mode") }, "did not answer a gateway status"},
		"no state":      {func(st map[string]any) { delete(st["decision_model"].(map[string]any), "state") }, "did not answer a gateway status"},
		"several kinds": {func(st map[string]any) { st["test_mode"] = false; st["judge"].(map[string]any)["provider"] = "mock" }, "not in test mode (TRUTHTABLE_TEST_MODE=true sends scripted answers as test_response); the judge is the mock"},
	} {
		t.Run(name, func(t *testing.T) {
			stub, srv := newStub(t)
			stub.status = recordableStatus()
			c.edit(stub.status)
			code, out := runTG(t, "--record", "--scenarios", "s0", "--base-url", srv.URL)
			if requests, _, _ := stub.snapshot(); code != exitUsage || len(requests) != 0 || !strings.Contains(out, "--record refused: ") || !strings.Contains(out, c.want) {
				t.Fatalf("exit %d, %d requests sent:\n%s", code, len(requests), out)
			}
		})
	}
	stub, srv := newStub(t)
	if code, out := runTG(t, "--record", "--scenarios", "s0", "--base-url", srv.URL); code != exitUsage || !strings.Contains(out, "GET /api/status: HTTP 404") {
		t.Errorf("a gateway without a status: exit %d:\n%s", code, out)
	}
	if requests, _, _ := stub.snapshot(); len(requests) != 0 {
		t.Errorf("%d requests sent after a refusal", len(requests))
	}

	// A degraded decision model still answers, and one whose first check is running will: both
	// record.
	for _, state := range []string{"degraded", "checking"} {
		stub, srv := newStub(t)
		stub.status = recordableStatus()
		stub.status["decision_model"].(map[string]any)["state"] = state
		if code, out := runTG(t, "--record", "--scenarios", "grounded-pass", "--base-url", srv.URL, "--audit-timeout", "10s"); code != exitOK ||
			!strings.Contains(out, "ok   grounded-pass (trace") {
			t.Errorf("decision model %s: exit %d:\n%s", state, code, out)
		}
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
		{"--scenarios", "s0", "--exclude", "s0"},
		{"--scenarios", "p3", "--phase", "p2"}, // every p3 scenario needs a later phase than p2
		{"--phase", "p9"},
		{"--upstream", "sometimes"},
		{"--interval", "0s", "--loop"},
		{"extra"},
		{"--file", "/does/not/exist.json"},
		{"--pack", "bogus"},
		{"--pack", "faults", "--file", "pack.json"}, // a file replaces the embedded pack
		{"--record", "--loop"},
		{"--record", "--upstream", "all"}, // mock-upstream's stand-ins are no recording
		{"--loop", "--trace-ids-out", "ids.txt"},
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
	count := func(match func(*scenario) bool) int {
		n := 0
		for i := range all {
			if match(&all[i]) {
				n++
			}
		}
		return n
	}
	in := func(sets ...string) func(*scenario) bool {
		return func(s *scenario) bool {
			return slices.ContainsFunc(sets, func(x string) bool { return slices.Contains(s.Sets, x) })
		}
	}
	for _, c := range []struct {
		sel, exclude string
		want         int
	}{
		{"s0", "", 3},
		{"demo,e2e", "", count(in("demo", "e2e"))},
		{"all", "", len(all)},
		{"grounded-pass", "", 1},
		{"grounded-pass, s0", "", 3},
		{"untagged", "", 3},
		{"demo", "untagged,generated", count(func(s *scenario) bool { return in("demo")(s) && !in("untagged", "generated")(s) })},
		{"s0", "grounded-pass", 2},
	} {
		got, err := selectScenarios(all, c.sel, c.exclude)
		if err != nil || len(got) != c.want {
			t.Errorf("%q minus %q: %d scenarios (%v), want %d", c.sel, c.exclude, len(got), err, c.want)
		}
	}
	if count(in("generated")) < 2 || count(in("p3")) < 7 || len(all) < 14+3 {
		t.Errorf("the pack shrank: %d scenarios", len(all))
	}
}

// TestPacks: --pack faults reads the embedded vendor-fault pack (D119), the main pack stays the
// default, and the two never share a scenario: the main pack's sets never select a fault
// scenario, and every faults-pack scenario is in set faults with a mock_faults to send.
func TestPacks(t *testing.T) {
	main, err := loadPack(packMain, "")
	if err != nil {
		t.Fatal(err)
	}
	if def, err := loadScenarios(""); err != nil || len(def) != len(main) {
		t.Fatalf("the default pack is the main pack: %d scenarios, %v", len(def), err)
	}
	faults, err := loadPack(packFaults, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := contracts.FS.ReadFile("demo/faults.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Scenarios []struct {
			ID         string          `json:"id"`
			MockFaults json.RawMessage `json:"mock_faults"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if len(faults) != len(raw.Scenarios) {
		t.Fatalf("--pack faults read %d scenarios, the file holds %d", len(faults), len(raw.Scenarios))
	}
	ids := map[string]bool{}
	for _, s := range main {
		ids[s.ID] = true
		if slices.Contains(s.Sets, "faults") {
			t.Errorf("%s: the main pack never holds a fault scenario", s.ID)
		}
	}
	for i, s := range faults {
		if ids[s.ID] || !slices.Contains(s.Sets, "faults") || len(raw.Scenarios[i].MockFaults) == 0 {
			t.Errorf("%s: a faults-pack scenario has an id of its own, set faults and mock_faults", s.ID)
		}
	}
	if _, err := loadPack("bogus", ""); err == nil {
		t.Error("an unknown pack is refused")
	}
}

func TestPhases(t *testing.T) {
	for sets, want := range map[string]string{"s0,demo": phaseS0, "demo": phaseS0, "p3,demo": "p3", "p2,p3": "p3", "p5": "p5"} {
		if got := scenarioPhase(strings.Split(sets, ",")); got != want {
			t.Errorf("%s: phase %s, want %s", sets, got, want)
		}
	}
	if !phaseAllows("p3", "p3") || !phaseAllows("p3", phaseS0) || phaseAllows("p2", "p3") ||
		!phaseAllows(defaultPhase, "p3") || !phaseAllows(defaultPhase, "p4") || phaseAllows(defaultPhase, "p5") {
		t.Error("phaseAllows")
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

func TestReadAnswer(t *testing.T) {
	jsonHeader := http.Header{"Content-Type": {"application/json"}}
	sseHeader := http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}
	for _, c := range []struct {
		name, format string
		header       http.Header
		body         string
		want         string
		ok, comment  bool
	}{
		{"chat", "openai_chat", jsonHeader, `{"choices":[{"index":0,"message":{"role":"assistant","content":"Hi there."}}]}`, "Hi there.", true, false},
		{"chat parts", "openai_chat", jsonHeader, `{"choices":[{"message":{"content":[{"type":"text","text":"Hi "},{"type":"text","text":"there."}]}}]}`, "Hi there.", true, false},
		{"chat without choices", "openai_chat", jsonHeader, `{"choices":[]}`, "", false, false},
		{"completions", "openai_completions", jsonHeader, `{"choices":[{"index":0,"text":"Done."}]}`, "Done.", true, false},
		{"anthropic", "anthropic_messages", jsonHeader, `{"content":[{"type":"text","text":"A"},{"type":"tool_use","id":"t"},{"type":"text","text":"B"}]}`, "AB", true, false},
		{"chat stream", "openai_chat", sseHeader,
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi \"}}]}\n\n" +
				"data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"other choice\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"there.\"},\"finish_reason\":\"stop\"}]}\n\n" +
				": trustagent {\"trace_id\":\"t\",\"action\":\"flag\",\"would_action\":\"block\",\"status\":\"ok\",\"verdict\":\"contradicted\",\"max_risk\":0.97,\"reasons\":[\"a\",\"b\"]}\n\ndata: [DONE]\n\n",
			"Hi there.", true, true},
		{"completions stream", "openai_completions", sseHeader, "data: {\"choices\":[{\"index\":0,\"text\":\"Do\"}]}\n\ndata: {\"choices\":[{\"index\":0,\"text\":\"ne.\"}]}\n\ndata: [DONE]\n\n", "Done.", true, false},
		{"anthropic stream", "anthropic_messages", sseHeader,
			"event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi \"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"there.\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			"Hi there.", true, false},
	} {
		a := readAnswer(c.format, http.StatusOK, c.header, http.Header{}, []byte(c.body))
		if a.text != c.want || a.ok != c.ok || (a.comment != nil) != c.comment {
			t.Errorf("%s: text %q ok %v comment %v", c.name, a.text, a.ok, a.comment != nil)
		}
		if c.comment {
			v := newDecisionView(a)
			for name, want := range map[string]string{headerAction: "flag", headerWouldAction: "block", headerStatus: "ok", headerVerdict: "contradicted", headerRisk: "0.97", headerReasons: "a,b"} {
				if got, _ := v.get(name); got != want {
					t.Errorf("%s: %s %q, want %q", c.name, name, got, want)
				}
			}
		}
	}
	if a := readAnswer("openai_chat", http.StatusBadGateway, jsonHeader, nil, []byte(`{"error":{}}`)); a.ok {
		t.Error("an error body has no assistant text")
	}
	// A trailer overrides a header; a comment's would_action equal to its action reads as absent.
	a := &answer{header: http.Header{headerAction: {"pass"}, headerMode: {"flag"}}, trailer: http.Header{headerAction: {"flag"}},
		comment: &annotation{Action: "flag", WouldAction: ptrTo("flag"), Status: "ok"}}
	v := newDecisionView(a)
	if got, _ := v.get(headerAction); got != "flag" {
		t.Errorf("action %q", got)
	}
	if _, ok := v.get(headerWouldAction); ok {
		t.Error("would_action equal to the action reads as absent")
	}
}

func ptrTo[T any](v T) *T { return &v }
