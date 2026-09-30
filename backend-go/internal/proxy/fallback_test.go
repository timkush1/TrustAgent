package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// fallbackText is a grounded answer to refund-contradiction-flag's request (its 30 is in the
// document), which the fallback upstream answers in these tests.
const fallbackText = "Annual plans can be refunded within 30 days of purchase."

// fallbackPolicy is a fallback route whose evidence checks decide without calibration, with the
// fallback settings given (the members of a YAML flow mapping) and extra route settings.
func fallbackPolicy(fallback, extra string) string {
	return blockPolicy(decision.ModeFallback, strings.TrimPrefix(extra+", fallback: {"+fallback+"}", ", "))
}

// newMockJev is the mock decision model answering from the scenario pack, strictly.
func newMockJev(t *testing.T) jev.Client {
	t.Helper()
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true}})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

// Re-gate outcomes regateJev scripts.
const (
	regatePass    = "pass"
	regateBlock   = "would block"
	regateTimeout = "timeout"
)

// regateJev scripts the decision model's answer to the re-gate of text: it passes (answered as
// grounded-pass), would block (answered as the request's own scenario) or times out. Every other
// call goes to the mock as it is.
type regateJev struct {
	jev.Client
	text, outcome string
}

func (r regateJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	if got, _ := req.State[decision.FieldAssistantResponse].(string); got == r.text {
		switch r.outcome {
		case regatePass:
			opt.Scenario = "grounded-pass"
		case regateTimeout:
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return r.Client.Decide(ctx, req, opt)
}

// withMembers sets members of a JSON request body.
func withMembers(t *testing.T, body string, members map[string]any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range members {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fallbackCount is fallback_total for outcome.
func fallbackCount(t *testing.T, outcome string) float64 {
	t.Helper()
	return metricSample(t, "trustagent_gateway_fallback_total", "outcome", outcome)
}

// countsOnce checks that fallback_total{outcome} went up by one since before.
func countsOnce(t *testing.T, outcome string, before float64) {
	t.Helper()
	if got := fallbackCount(t, outcome) - before; got != 1 {
		t.Errorf("fallback_total{outcome=%q} went up by %v, want 1", outcome, got)
	}
}

// checkFinal checks the delivered outcome the trace records.
func checkFinal(t *testing.T, r *gate.Result, action, delivered, text string, flags, reasons []string) {
	t.Helper()
	f := r.Final
	if f == nil || f.Action != action || f.Delivered != delivered || f.Text != text ||
		!slices.Equal(f.Flags, flags) || !slices.Equal(f.Reasons, reasons) {
		t.Errorf("final %+v, want action %s, delivered %s, text %q, flags %v, reasons %v", f, action, delivered, text, flags, reasons)
	}
}

// checkReasonsHeader checks that X-TrustAgent-Reasons carries the decision's reasons, then the
// delivery's reason the trace records (D19), as the stream comment does.
func checkReasonsHeader(t *testing.T, h http.Header, r0 *gate.Result, reason string) {
	t.Helper()
	want := strings.Join(append(slices.Clone(r0.Decision.Reasons), reason), ",")
	if got := h.Get(HeaderReasons); got != want || len(r0.Decision.Reasons) == 0 {
		t.Errorf("reasons header %q, want %q", got, want)
	}
}

// checkAttempts checks what the sink recorded: the re-gate as attempt 1 with no audit, then the
// trace's decision (attempt 0) once, with the audit on the original answer; or, without a
// re-gate, the trace's decision alone.
func checkAttempts(t *testing.T, e *env, r0 *gate.Result, original, regated string) *gate.Result {
	t.Helper()
	attempts, calls := e.sink.log()
	if r0.Decision.Attempt != 0 || r0.ResponseText != original {
		t.Errorf("the trace's decision is attempt %d on %q", r0.Decision.Attempt, r0.ResponseText)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r0.Decision)
	if regated == "" {
		if !slices.Equal(calls, []string{"decided"}) {
			t.Errorf("sink calls %v: no re-gate, one decision", calls)
		}
		return nil
	}
	if !slices.Equal(calls, []string{"attempt", "decided"}) || len(attempts) != 1 {
		t.Fatalf("sink calls %v: attempt 1, then the trace's decision once", calls)
	}
	a := attempts[0]
	if a.Decision.Attempt != 1 || a.ResponseText != regated || a.Audit != (gate.AuditPlan{}) || a.Decision.TraceID != r0.Decision.TraceID {
		t.Errorf("attempt 1: attempt %d on %q, audit %+v, trace %s", a.Decision.Attempt, a.ResponseText, a.Audit, a.Decision.TraceID)
	}
	contractstest.Validate(t, "decision.v1.schema.json", a.Decision)
	return a
}

// TestFallbackCallsTheUpstream: the fallback request goes once to the route's upstream (same):
// the fallback prompt with the trusted evidence as CONTEXT in front, the route's fallback model,
// stream false, none of the gateway's members, the client's own credential and query (M2) and no
// TrustAgent header. A re-gate that passes delivers the fallback answer as it came, with Action fallback; the
// trace records attempt 1, then attempt 0 once, audited on the original answer.
func TestFallbackCallsTheUpstream(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass},
		policyYAML: fallbackPolicy("upstream: same, model: gpt-4o", "")})
	e.up.body = chatAnswer(fallbackText)
	before := fallbackCount(t, fallbackDelivered)
	req, answer, hdr := contradicted(t)
	body := withMembers(t, withTestResponse(t, req, answer), map[string]any{
		"stream_options": map[string]any{"include_usage": true},
		"trustagent":     map[string]any{"rules": map[string]any{}},
	})
	w := e.do(t, "/v1/chat/completions?api-version=2024-10-21", body, hdr)
	r0 := e.sink.result(t)

	if w.Code != http.StatusOK || w.Body.String() != e.up.body || w.Header().Get(HeaderAction) != decision.ActionFallback ||
		w.Header().Get(HeaderWouldAction) != decision.ActionBlock || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%d action %q would %q: %s", w.Code, w.Header().Get(HeaderAction), w.Header().Get(HeaderWouldAction), w.Body.String())
	}
	if e.up.calls() != 1 {
		t.Fatalf("%d upstream calls: the scripted answer makes none, the fallback one", e.up.calls())
	}
	up, sent := e.up.last(t)
	if up.URL.Path != llmapi.PathChatCompletions || up.URL.RawQuery != "api-version=2024-10-21" ||
		up.Header.Get("Authorization") != "Bearer "+upstreamKey || up.Header.Get("X-TrustAgent-Key") != "" {
		t.Errorf("fallback call %s?%s with headers %v", up.URL.Path, up.URL.RawQuery, up.Header)
	}
	var fr map[string]json.RawMessage
	if err := json.Unmarshal(sent, &fr); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{"trustagent", "test_response", "stream_options"} {
		if fr[member] != nil {
			t.Errorf("the fallback request carries %s: %s", member, sent)
		}
	}
	var msgs []struct{ Role, Content string }
	_ = json.Unmarshal(fr["messages"], &msgs)
	wantPrompt := llmapi.DefaultFallbackSystemPrompt + "\n\nCONTEXT:\n<context>\n[refund-policy.md] " +
		"Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.\n</context>"
	if string(fr["model"]) != `"gpt-4o"` || string(fr["stream"]) != "false" || len(msgs) != 3 || msgs[0].Role != "system" ||
		msgs[0].Content != wantPrompt || msgs[1].Role != "system" || msgs[2].Role != "user" {
		t.Errorf("fallback request %s", sent)
	}

	a := checkAttempts(t, e, r0, answer, fallbackText)
	if a != nil && (a.Decision.Action != decision.ActionPass || a.Status != "ok") {
		t.Errorf("attempt 1 %s %s", a.Decision.Action, a.Status)
	}
	if r0.Decision.Action != decision.ActionFallback || r0.Audit.Selection == "" {
		t.Errorf("attempt 0 %s, audit %+v: the original answer is audited", r0.Decision.Action, r0.Audit)
	}
	checkFinal(t, r0, decision.ActionFallback, gate.DeliveredFallback, fallbackText, []string{gate.FlagFallbackUsed}, nil)
	countsOnce(t, fallbackDelivered, before)
}

// TestPaymentsFallbackScenario: the demo's payments-fallback scenario on a fallback route. Scripted
// (test mode, test_response) its fallback call is answered from the scenario pack with no upstream
// call (D1); through the upstream, attempt 0 and the fallback call both reach it. Either way the
// re-gate passes on the scenario's fallback answers and the client gets its delivered text.
func TestPaymentsFallbackScenario(t *testing.T) {
	sc := demoScenario(t, "payments-fallback")
	if sc.Fallback == nil || sc.Expect.DeliveredText == nil || *sc.Expect.DeliveredText != sc.Fallback.Response {
		t.Fatalf("payments-fallback delivers its fallback answer: %+v", sc.Expect)
	}
	fb := sc.Fallback.Response
	for _, scripted := range []bool{true, false} {
		name := map[bool]string{true: "scripted", false: "upstream"}[scripted]
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true, enforcing: true,
				policyYAML: fallbackPolicy("upstream: same, model: gpt-4o, on_fail: block", "fail: closed")})
			e.up.respond = func(n int, _ []byte) (int, string, string) {
				if n == 0 {
					return http.StatusOK, "application/json", chatAnswer(sc.Response)
				}
				return http.StatusOK, "application/json", chatAnswer(fb)
			}
			body := string(sc.Request)
			if scripted {
				body = withTestResponse(t, body, sc.Response)
			}
			hdr := map[string]string{"X-TrustAgent-Scenario": sc.ID}
			for k, v := range sc.Headers {
				hdr[k] = v
			}
			w := e.do(t, "/v1/chat/completions", body, hdr)
			r0 := e.sink.result(t)
			h := w.Header()
			if got := gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()); w.Code != http.StatusOK || got != fb {
				t.Errorf("%d delivered %q", w.Code, got)
			}
			if h.Get(HeaderAction) != sc.Expect.Action || h.Get(HeaderWouldAction) != sc.Expect.WouldAction ||
				h.Get(HeaderStatus) != sc.Expect.Status || h.Get(HeaderVerdict) != sc.Expect.Verdict ||
				h.Get(HeaderReasons) != strings.Join(sc.Expect.Reasons, ",") {
				t.Errorf("headers %v, want %+v", h, sc.Expect)
			}
			wantCalls := 2
			if scripted {
				wantCalls = 0
			}
			if e.up.calls() != wantCalls {
				t.Errorf("%d upstream calls, want %d", e.up.calls(), wantCalls)
			}
			if !scripted {
				_, sent := e.up.last(t)
				if !strings.Contains(string(sent), `CONTEXT:\n<context>\n[refund-timing.md] Approved refunds are returned`) {
					t.Errorf("fallback request %s", sent)
				}
			}
			a := checkAttempts(t, e, r0, sc.Response, fb)
			if a != nil && (a.Decision.WouldAction != decision.ActionPass || a.Decision.MaxRisk == nil || *a.Decision.MaxRisk != 0.04) {
				t.Errorf("attempt 1 would %s risk %v", a.Decision.WouldAction, a.Decision.MaxRisk)
			}
			checkFinal(t, r0, decision.ActionFallback, gate.DeliveredFallback, fb, []string{gate.FlagFallbackUsed}, nil)
		})
	}
}

// TestFallbackReGateOutcomes: the attempt-1 action picks what is delivered (SPEC §2.12 fallback
// steps 4-5, D3): pass or flag delivers the fallback answer; a re-gate that would block, or that
// times out on a closed route, applies on_fail: block sends the block body, flag the fallback
// answer with Action flag, original the original answer with Action flag. On an open route a
// re-gate that times out passes, so the fallback answer is delivered.
func TestFallbackReGateOutcomes(t *testing.T) {
	type want struct {
		action, delivered string
		fallback          bool // the fallback answer is delivered (else the block body or the original)
		metric            string
	}
	_, original, _ := contradicted(t)
	onFail := map[string]want{
		"block":    {decision.ActionBlock, gate.DeliveredBlocked, false, fallbackOnFailBlock},
		"flag":     {decision.ActionFlag, gate.DeliveredFallback, true, fallbackOnFailFlag},
		"original": {decision.ActionFlag, gate.DeliveredOriginal, false, fallbackOnFailOriginal},
	}
	delivered := want{decision.ActionFallback, gate.DeliveredFallback, true, fallbackDelivered}
	type check struct {
		outcome, onFail, fail string
		want                  want
		// attempt1 is the re-gate's action and status.
		attempt1, status1 string
	}
	var cases []check
	for _, of := range []string{"block", "flag", "original"} {
		cases = append(cases,
			check{regatePass, of, decision.FailClosed, delivered, decision.ActionPass, "ok"},
			check{regateBlock, of, decision.FailClosed, onFail[of], decision.ActionFallback, "ok"},
			check{regateTimeout, of, decision.FailClosed, onFail[of], decision.ActionBlock, decision.StatusTimeout})
	}
	cases = append(cases, check{regateTimeout, "block", decision.FailOpen, delivered, decision.ActionPass, decision.StatusTimeout})
	for _, c := range cases {
		t.Run(c.outcome+"/on_fail "+c.onFail+"/"+c.fail, func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, c.outcome},
				policyYAML: fallbackPolicy("upstream: same, on_fail: "+c.onFail, "fail: "+c.fail+", deadline_ms: 200")})
			e.up.body = chatAnswer(fallbackText)
			before := fallbackCount(t, c.want.metric)
			req, answer, hdr := contradicted(t)
			w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
			r0 := e.sink.result(t)

			text := original
			switch {
			case c.want.fallback:
				text = fallbackText
			case c.want.delivered == gate.DeliveredBlocked:
				text = heldBack
			}
			if got := gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()); w.Code != http.StatusOK || got != text {
				t.Errorf("%d delivered %q, want %q", w.Code, got, text)
			}
			wouldHeader := decision.ActionBlock
			if c.want.action == decision.ActionBlock {
				wouldHeader = "" // would block is the delivered action
			}
			if w.Header().Get(HeaderAction) != c.want.action || w.Header().Get(HeaderWouldAction) != wouldHeader {
				t.Errorf("action %q would %q", w.Header().Get(HeaderAction), w.Header().Get(HeaderWouldAction))
			}
			var flags []string
			if c.want.fallback {
				flags = []string{gate.FlagFallbackUsed}
			}
			checkFinal(t, r0, c.want.action, c.want.delivered, text, flags, nil)
			a := checkAttempts(t, e, r0, answer, fallbackText)
			if a != nil && (a.Decision.Action != c.attempt1 || a.Status != c.status1) {
				t.Errorf("attempt 1 %s %s, want %s %s", a.Decision.Action, a.Status, c.attempt1, c.status1)
			}
			countsOnce(t, c.want.metric, before)
		})
	}
}

// TestFallbackCallFailures: a fallback call that gives no answer is not re-gated (D2): on_fail
// block sends the block body; flag and original send the original answer with Action flag. The
// trace gets reason fallback_failed and fallback_total counts failed.
func TestFallbackCallFailures(t *testing.T) {
	failures := []struct {
		name  string
		opts  envOpts
		setup func(*fakeUpstream)
	}{
		{name: "transport error", setup: func(u *fakeUpstream) { u.Close() }},
		{name: "non-2xx", setup: func(u *fakeUpstream) {
			u.status, u.body = http.StatusInternalServerError, `{"error":{"message":"down"}}`
		}},
		{name: "not JSON", setup: func(u *fakeUpstream) { u.ctype, u.body = "text/html", "<html>"+fallbackText+"</html>" }},
		{name: "no text", setup: func(u *fakeUpstream) {
			u.body = `{"id":"chatcmpl-fb","object":"chat.completion","created":7,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,` +
				`"tool_calls":[{"id":"c1","type":"function","function":{"name":"refund","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		}},
		{name: "too large", opts: envOpts{maxBytes: 64}, setup: func(u *fakeUpstream) { u.body = chatAnswer(fallbackText) }},
		{name: "undecodable", setup: func(u *fakeUpstream) { u.body, u.encoding = chatAnswer(fallbackText), "br" }},
	}
	for _, of := range []string{"block", "flag", "original"} {
		for _, fc := range failures {
			t.Run(of+"/"+fc.name, func(t *testing.T) {
				o := fc.opts
				o.testMode, o.enforcing = true, true
				o.policyYAML = fallbackPolicy("upstream: same, on_fail: "+of, "")
				e := newEnv(t, o)
				fc.setup(e.up)
				before := fallbackCount(t, fallbackFailed)
				req, answer, hdr := contradicted(t)
				w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
				r0 := e.sink.result(t)
				action, delivered, text := decision.ActionFlag, gate.DeliveredOriginal, answer
				if of == "block" {
					action, delivered, text = decision.ActionBlock, gate.DeliveredBlocked, heldBack
				}
				if got := gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()); w.Code != http.StatusOK || got != text ||
					w.Header().Get(HeaderAction) != action {
					t.Errorf("%d action %q delivered %q", w.Code, w.Header().Get(HeaderAction), got)
				}
				checkReasonsHeader(t, w.Header(), r0, reasonFallbackFailed)
				checkFinal(t, r0, action, delivered, text, nil, []string{reasonFallbackFailed})
				checkAttempts(t, e, r0, answer, "")
				countsOnce(t, fallbackFailed, before)
			})
		}
	}
}

// TestFallbackUpstream: fallback.upstream: fallback sends the fallback request to the fallback
// upstream when its format family matches the request's, and blocks with reason
// fallback_format_mismatch, calling nothing, when it does not. An Anthropic request's fallback
// goes to /v1/messages with the prompt in front of its system.
func TestFallbackUpstream(t *testing.T) {
	t.Run("compatible", func(t *testing.T) {
		e := newEnv(t, envOpts{testMode: true, enforcing: true, fallbackFormat: llmapi.FamilyOpenAI,
			jev: regateJev{newMockJev(t), fallbackText, regatePass}, policyYAML: fallbackPolicy("upstream: fallback", "")})
		e.fb.body = chatAnswer(fallbackText)
		req, answer, hdr := contradicted(t)
		w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
		r0 := e.sink.result(t)
		if w.Body.String() != e.fb.body || e.fb.calls() != 1 || e.up.calls() != 0 {
			t.Errorf("fallback upstream %d calls, primary %d: %s", e.fb.calls(), e.up.calls(), w.Body.String())
		}
		if up, _ := e.fb.last(t); up.URL.Path != llmapi.PathChatCompletions {
			t.Errorf("fallback call to %s", up.URL.Path)
		}
		checkFinal(t, r0, decision.ActionFallback, gate.DeliveredFallback, fallbackText, []string{gate.FlagFallbackUsed}, nil)
	})
	t.Run("format mismatch", func(t *testing.T) {
		e := newEnv(t, envOpts{testMode: true, enforcing: true, fallbackFormat: llmapi.FamilyAnthropic,
			policyYAML: fallbackPolicy("upstream: fallback, on_fail: original", "")})
		before := fallbackCount(t, fallbackFormatMismatch)
		req, answer, hdr := contradicted(t)
		w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
		r0 := e.sink.result(t)
		if gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()) != heldBack || w.Header().Get(HeaderAction) != decision.ActionBlock ||
			e.fb.calls() != 0 || e.up.calls() != 0 {
			t.Errorf("action %q, %d fallback calls: %s", w.Header().Get(HeaderAction), e.fb.calls(), w.Body.String())
		}
		checkReasonsHeader(t, w.Header(), r0, reasonFallbackFormatMismatch)
		checkFinal(t, r0, decision.ActionBlock, gate.DeliveredBlocked, heldBack, nil, []string{reasonFallbackFormatMismatch})
		checkAttempts(t, e, r0, answer, "")
		countsOnce(t, fallbackFormatMismatch, before)
	})
	t.Run("anthropic", func(t *testing.T) {
		e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass},
			policyYAML: fallbackPolicy("upstream: same", "")})
		e.anth.body = strings.Replace(anthropicBody, slaText, fallbackText, 1)
		_, answer, hdr := contradicted(t)
		w := e.do(t, "/v1/messages", anthropicContradicted(t, answer), hdr)
		r0 := e.sink.result(t)
		if w.Body.String() != e.anth.body || w.Header().Get(HeaderAction) != decision.ActionFallback || e.anth.calls() != 1 {
			t.Errorf("action %q, %d calls: %s", w.Header().Get(HeaderAction), e.anth.calls(), w.Body.String())
		}
		up, sent := e.anth.last(t)
		var fr struct {
			System string          `json:"system"`
			Stream json.RawMessage `json:"stream"`
		}
		if err := json.Unmarshal(sent, &fr); err != nil {
			t.Fatal(err)
		}
		if up.URL.Path != llmapi.PathMessages || !strings.HasPrefix(fr.System, llmapi.DefaultFallbackSystemPrompt+"\n\nCONTEXT:\n<context>\n[refund-policy.md] ") ||
			!strings.HasSuffix(fr.System, "</context>\n\nYou are the Acme support assistant. Answer only from the documents below. If they do not cover the question, say so.\n\n"+
				`<document id="refund-policy.md">Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.</document>`) ||
			string(fr.Stream) != "false" {
			t.Errorf("fallback call to %s: %s", up.URL.Path, sent)
		}
		checkFinal(t, r0, decision.ActionFallback, gate.DeliveredFallback, fallbackText, []string{gate.FlagFallbackUsed}, nil)
	})
}

// TestFallbackExtendsTheWriteDeadline: a fallback call that outlasts the response's write deadline
// does not cut the answer off: the deadline moves past the call before it starts (SPEC §2.12).
func TestFallbackExtendsTheWriteDeadline(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass},
		policyYAML: fallbackPolicy("upstream: same", "")})
	e.up.body, e.up.delay = chatAnswer(fallbackText), 600*time.Millisecond
	srv := httptest.NewUnstartedServer(e.router)
	srv.Config.WriteTimeout = 250 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	req, answer, hdr := contradicted(t)
	r, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(withTestResponse(t, req, answer)))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-TrustAgent-Key", ingestKey)
	r.Header.Set("Authorization", "Bearer "+upstreamKey)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatalf("the write deadline cut the fallback answer off: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || gatedText(t, llmapi.FormatOpenAIChat, body) != fallbackText {
		t.Errorf("%d %s (%v)", resp.StatusCode, body, err)
	}
	e.sink.result(t)
}

// TestFallbackClientGoneDuringTheCall: the fallback call runs on the client's context (M1), so a
// client that leaves cancels it. Nothing is re-gated; the trace records the on_fail outcome with
// reason fallback_failed and flag client_disconnected.
func TestFallbackClientGoneDuringTheCall(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: fallbackPolicy("upstream: same", "")})
	e.up.body, e.up.delay = chatAnswer(fallbackText), 10*time.Second
	req, answer, hdr := contradicted(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(withTestResponse(t, req, answer))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-TrustAgent-Key", ingestKey)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	go func() {
		for deadline := time.Now().Add(5 * time.Second); e.up.calls() == 0 && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	e.router.ServeHTTP(newRecorder(), r)
	r0 := e.sink.result(t)
	if time.Since(start) > 5*time.Second {
		t.Errorf("the fallback call outlived its client")
	}
	checkFinal(t, r0, decision.ActionBlock, gate.DeliveredBlocked, heldBack, []string{gate.FlagClientDisconnected}, []string{reasonFallbackFailed})
	checkAttempts(t, e, r0, answer, "")
}

// TestBufferedFallbackCallsTheUpstream: on a buffer-streaming route the fallback call is the same
// JSON call; the client gets the fallback answer re-emitted as a stream that finishes with stop,
// and the trace records attempt 1 before attempt 0.
func TestBufferedFallbackCallsTheUpstream(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass},
		policyYAML: fallbackPolicy("upstream: same", "streaming: buffer")})
	req, answer, hdr := contradicted(t)
	chunk := func(delta, finish string) string {
		f := "null"
		if finish != "" {
			f = `"` + finish + `"`
		}
		return `data: {"id":"chatcmpl-up","object":"chat.completion.chunk","created":7,"model":"gpt-4o-mini","choices":[{"index":0,"delta":` +
			delta + `,"finish_reason":` + f + "}]}\n\n"
	}
	a, _ := json.Marshal(answer)
	events := chunk(`{"role":"assistant","content":`+string(a)+`}`, "") + chunk(`{}`, "stop") + "data: [DONE]\n\n"
	e.up.respond = func(n int, _ []byte) (int, string, string) {
		if n == 0 {
			return http.StatusOK, "text/event-stream", events
		}
		return http.StatusOK, "application/json", chatAnswer(fallbackText)
	}
	w := e.do(t, "/v1/chat/completions", withMembers(t, req, map[string]any{"stream": true}), hdr)
	r0 := e.sink.result(t)
	fb, _ := json.Marshal(fallbackText)
	out := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(out, string(fb)) || strings.Contains(out, string(a)) ||
		!strings.Contains(out, `"finish_reason":"stop"`) || !strings.HasSuffix(out, "data: [DONE]\n\n") ||
		w.Header().Get(HeaderAction) != decision.ActionFallback {
		t.Errorf("%d action %q:\n%s", w.Code, w.Header().Get(HeaderAction), out)
	}
	if e.up.calls() != 2 {
		t.Errorf("%d upstream calls", e.up.calls())
	}
	checkAttempts(t, e, r0, answer, fallbackText)
	checkFinal(t, r0, decision.ActionFallback, gate.DeliveredFallback, fallbackText, []string{gate.FlagFallbackUsed}, nil)
}

// TestScriptedFallbacks: the scripted fallback answers come from the scenario pack, and apply only
// to a scripted answer in test mode whose scenario has one (D1).
func TestScriptedFallbacks(t *testing.T) {
	sc := demoScenario(t, "payments-fallback")
	if got := scriptedFallbacks()[sc.ID]; got != sc.Fallback.Response {
		t.Errorf("payments-fallback scripts %q", got)
	}
	if _, ok := scriptedFallbacks()["grounded-pass"]; ok {
		t.Error("grounded-pass has no fallback")
	}
	flowOf := func(answerSource, scenario string) *flow {
		return &flow{x: &gate.Exchange{Trace: &gate.TraceStart{AnswerSource: answerSource, Scenario: scenario}}}
	}
	h := &Handler{testMode: true}
	if text, ok := h.scriptedFallback(flowOf(gate.AnswerScripted, sc.ID)); !ok || text != sc.Fallback.Response {
		t.Errorf("scripted answer in test mode: %q %v", text, ok)
	}
	for name, f := range map[string]*flow{
		"live answer":      flowOf(gate.AnswerLive, sc.ID),
		"no scenario":      flowOf(gate.AnswerScripted, ""),
		"without fallback": flowOf(gate.AnswerScripted, "grounded-pass"),
	} {
		if _, ok := h.scriptedFallback(f); ok {
			t.Errorf("%s: scripted fallback", name)
		}
	}
	if _, ok := (&Handler{}).scriptedFallback(flowOf(gate.AnswerScripted, sc.ID)); ok {
		t.Error("outside test mode: scripted fallback")
	}
}

// TestHeaderReasonsCarryTheDelivery: X-TrustAgent-Reasons lists the decision's reasons, then the
// delivery's the decision does not have (D19), at most 8; when the cap cuts, the delivery's stay.
func TestHeaderReasonsCarryTheDelivery(t *testing.T) {
	var many []string
	for i := range 9 {
		many = append(many, "reason"+string(rune('a'+i)))
	}
	result := func(reasons []string, fin *gate.Final) *gate.Result {
		return &gate.Result{Decision: &gate.Decision{Core: decision.Core{Reasons: reasons}}, Final: fin}
	}
	failed := &gate.Final{Reasons: []string{reasonFallbackFailed}}
	for name, c := range map[string]struct {
		res  *gate.Result
		want []string
	}{
		"no delivery":      {result([]string{"contradiction:critical"}, nil), []string{"contradiction:critical"}},
		"delivery after":   {result([]string{"contradiction:critical"}, failed), []string{"contradiction:critical", reasonFallbackFailed}},
		"delivery only":    {result(nil, failed), []string{reasonFallbackFailed}},
		"no repeat":        {result([]string{reasonFallbackFailed}, failed), []string{reasonFallbackFailed}},
		"capped":           {result(many, nil), many[:maxReasons]},
		"capped, delivery": {result(many, failed), append(slices.Clone(many[:maxReasons-1]), reasonFallbackFailed)},
		"neither":          {result(nil, &gate.Final{}), nil},
	} {
		if got := headerReasons(c.res); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
