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
	"testing"
	"time"

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
		action: map[string]string{}, would: map[string]string{}, text: map[string]string{}, noComment: map[string]bool{},
		noTrailers: map[string]bool{}}
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
	case e.str("audit") == "queued" && n < 3:
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
	code, out := runTG(t, "--scenarios", "demo", "--base-url", srv.URL)
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

// TestUpstreamAnswers: --upstream generated leaves test_response out of generated scenarios only,
// --upstream all out of every scenario; a generated answer from the upstream is not checked.
func TestUpstreamAnswers(t *testing.T) {
	for mode, wantScripted := range map[string]bool{upstreamNone: true, upstreamGenerated: true, upstreamAll: false} {
		stub, srv := newStub(t)
		code, out := runTG(t, "--once", "--expect", "--scenarios", "grounded-pass,generated", "--upstream", mode, "--base-url", srv.URL, "--audit-timeout", "10s")
		if code != exitOK {
			t.Fatalf("%s: exit %d:\n%s", mode, code, out)
		}
		requests, bodies, _ := stub.snapshot()
		for i, r := range requests {
			sc := stub.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
			var sent map[string]json.RawMessage
			_ = json.Unmarshal(bodies[i], &sent)
			scripted := wantScripted && !(mode == upstreamGenerated && sc.AnswerSource == answerGenerated)
			if (sent["test_response"] != nil) != scripted {
				t.Errorf("--upstream %s, %s: test_response sent %v", mode, sc.ID, sent["test_response"] != nil)
			}
			unchecked := strings.Contains(out, "sent "+sc.ID+" (")
			if unchecked != (!scripted && sc.AnswerSource == answerGenerated) {
				t.Errorf("--upstream %s, %s: unchecked %v:\n%s", mode, sc.ID, unchecked, out)
			}
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
		{"--scenarios", "p3"}, // every p3 scenario needs a later phase than the default
		{"--phase", "p9"},
		{"--upstream", "sometimes"},
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

func TestPhases(t *testing.T) {
	for sets, want := range map[string]string{"s0,demo": phaseS0, "demo": phaseS0, "p3,demo": "p3", "p2,p3": "p3", "p5": "p5"} {
		if got := scenarioPhase(strings.Split(sets, ",")); got != want {
			t.Errorf("%s: phase %s, want %s", sets, got, want)
		}
	}
	if !phaseAllows("p3", "p3") || !phaseAllows("p3", phaseS0) || phaseAllows(defaultPhase, "p3") {
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
