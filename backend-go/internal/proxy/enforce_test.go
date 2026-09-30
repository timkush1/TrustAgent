package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// demoScenario returns a scenario of contracts/demo/scenarios.v1.json by id.
func demoScenario(t *testing.T, id string) scenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	for _, s := range pack.Scenarios {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no scenario %s", id)
	return scenario{}
}

const heldBack = "Held back: that answer could not be verified."

// blockPolicy is a route in mode whose evidence checks decide without calibration, with extra
// route settings (a YAML flow mapping's members) and, unless extra sets block, the block message
// heldBack.
func blockPolicy(mode, extra string) string {
	if !strings.Contains(extra, "block:") {
		extra = strings.TrimPrefix(extra+", block: {message: "+strconv.Quote(heldBack)+"}", ", ")
	}
	return fmt.Sprintf("version: 1\ndefaults: {mode: %s, require_calibration: false, evaluators: [{ref: groundedness@1}, {ref: contradiction@1}], %s}\n",
		mode, extra)
}

// contradicted is refund-contradiction-flag: the mock decision model finds the answer critical
// on both checks, so the decision would block.
func contradicted(t *testing.T) (req, answer string, headers map[string]string) {
	t.Helper()
	sc := demoScenario(t, "refund-contradiction-flag")
	return string(sc.Request), sc.Response, map[string]string{"X-TrustAgent-Scenario": sc.ID}
}

// chatAnswer is an upstream chat completion answering text.
func chatAnswer(text string) string {
	b, _ := json.Marshal(text)
	return `{"id":"chatcmpl-up","object":"chat.completion","created":7,"model":"gpt-4o-mini","system_fingerprint":"fp_7",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + string(b) + `},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":20,"completion_tokens":9,"total_tokens":29}}`
}

// checkBlocked checks a decided exchange whose answer was blocked.
func checkBlocked(t *testing.T, w *httptest.ResponseRecorder, r *gate.Result) {
	t.Helper()
	if got := w.Header().Get(HeaderAction); got != decision.ActionBlock {
		t.Errorf("action header %q, want block", got)
	}
	if r.Final == nil || r.Final.Action != decision.ActionBlock || r.Final.Delivered != gate.DeliveredBlocked || r.Final.Text != heldBack {
		t.Errorf("final %+v", r.Final)
	}
	if !r.Decision.Enforced || r.ResponseText == heldBack {
		t.Errorf("enforced %v response text %q: the decision is enforced and the trace keeps the original", r.Decision.Enforced, r.ResponseText)
	}
	contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
}

// TestBlockOpenAIMessageStyle: a message-style block patches every choice of the upstream answer
// (content_filter, no tool_calls, logprobs nulled) and keeps its other members.
func TestBlockOpenAIMessageStyle(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("block", "")})
	req, answer, hdr := contradicted(t)
	a, _ := json.Marshal(answer)
	e.up.body = `{"id":"chatcmpl-up","object":"chat.completion","created":7,"model":"gpt-4o-mini","system_fingerprint":"fp_7","choices":[` +
		`{"index":0,"message":{"role":"assistant","content":` + string(a) + `,"refusal":null},"logprobs":{"content":[{"token":"60"}]},"finish_reason":"stop"},` +
		`{"index":1,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"refund","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":20,"completion_tokens":9,"total_tokens":29}}`
	w := e.do(t, "/v1/chat/completions", req, hdr)
	r := e.sink.result(t)
	checkBlocked(t, w, r)
	var body struct {
		Choices []map[string]json.RawMessage `json:"choices"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Choices) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for i, c := range body.Choices {
		var msg map[string]json.RawMessage
		_ = json.Unmarshal(c["message"], &msg)
		if string(c["finish_reason"]) != `"content_filter"` || string(msg["content"]) != `"`+heldBack+`"` || msg["tool_calls"] != nil ||
			msg["refusal"] != nil || (c["logprobs"] != nil && string(c["logprobs"]) != "null") {
			t.Errorf("choice %d: %s", i, c["message"])
		}
	}
	for _, kept := range []string{`"id":"chatcmpl-up"`, `"system_fingerprint":"fp_7"`, `"created":7`, `"total_tokens":29`} {
		if !strings.Contains(w.Body.String(), kept) {
			t.Errorf("the block keeps %s: %s", kept, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "60 days") || strings.Contains(w.Body.String(), "refund\"") {
		t.Errorf("the blocked answer leaks: %s", w.Body.String())
	}
	h := w.Header()
	if h.Get(HeaderMode) != decision.ModeBlock || h.Get(HeaderStatus) != "ok" || h.Get(HeaderWouldAction) != "" ||
		h.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", h)
	}
	if !slices.Contains(r.Flags, gate.FlagMultiChoice) {
		t.Errorf("flags %v: an answer with two choices is flagged multi_choice", r.Flags)
	}
}

// TestBlockErrorStyle: an error-style block answers with the route's status and the
// trustagent_policy_block error (OpenAI) or a 400 invalid_request_error (Anthropic).
func TestBlockErrorStyle(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("block", "block: {style: error, status: 451, message: "+strconv.Quote(heldBack)+"}")})
	req, answer, hdr := contradicted(t)
	w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
	checkBlocked(t, w, e.sink.result(t))
	if w.Code != http.StatusUnavailableForLegalReasons || w.Body.String() !=
		`{"error":{"message":"`+heldBack+`","type":"trustagent_policy_block","param":null,"code":"trustagent_blocked"}}` {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}

	w = e.do(t, "/v1/messages", anthropicContradicted(t, answer), hdr)
	checkBlocked(t, w, e.sink.result(t))
	checkAnthropicError(t, w, http.StatusBadRequest, llmapi.AnthropicInvalidRequestError)
	if !strings.Contains(w.Body.String(), heldBack) {
		t.Errorf("the block message is the error message: %s", w.Body.String())
	}
}

// anthropicContradicted is the contradicted scenario as an Anthropic request answered by text.
func anthropicContradicted(t *testing.T, text string) string {
	t.Helper()
	sc := demoScenario(t, "refund-contradiction-flag")
	var chat struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if err := json.Unmarshal(sc.Request, &chat); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{
		"model": "claude-haiku-4-5", "max_tokens": 256, "system": chat.Messages[0].Content,
		"messages": []any{map[string]any{"role": "user", "content": chat.Messages[1].Content}},
	}
	if text != "" {
		req["test_response"] = text
	}
	b, _ := json.Marshal(req)
	return string(b)
}

// TestBlockAnthropicAndCompletions: the message-style block in the other formats: an Anthropic
// msg_trustagent_ message with the route's stop reason, and completions choices with the text.
func TestBlockAnthropicAndCompletions(t *testing.T) {
	_, answer, hdr := contradicted(t)
	for _, stop := range []string{"", llmapi.StopReasonEndTurn} {
		extra := ""
		if stop != "" {
			extra = "block: {anthropic_stop_reason: " + stop + ", message: " + strconv.Quote(heldBack) + "}"
		}
		e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("block", extra)})
		a, _ := json.Marshal(answer)
		e.anth.body = `{"id":"msg_up","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":` + string(a) +
			`}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":31,"output_tokens":12}}`
		w := e.do(t, "/v1/messages", anthropicContradicted(t, ""), hdr)
		r := e.sink.result(t)
		checkBlocked(t, w, r)
		want := stop
		if want == "" {
			want = llmapi.StopReasonRefusal
		}
		trace32 := strings.ReplaceAll(r.Trace.TraceID, "-", "")
		if w.Code != http.StatusOK || w.Body.String() != `{"id":"msg_trustagent_`+trace32+`","type":"message","role":"assistant","model":"claude-haiku-4-5",`+
			`"content":[{"type":"text","text":"`+heldBack+`"}],"stop_reason":"`+want+`","stop_sequence":null,"usage":{"input_tokens":31,"output_tokens":0}}` {
			t.Errorf("stop %q: %d %s", stop, w.Code, w.Body.String())
		}
	}

	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("block", "")})
	a, _ := json.Marshal(answer)
	e.up.body = `{"id":"cmpl-up","object":"text_completion","created":7,"model":"gpt-3.5-turbo-instruct","choices":[` +
		`{"index":0,"text":` + string(a) + `,"logprobs":null,"finish_reason":"stop"},{"index":1,"text":"also leaked","logprobs":null,"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":12,"completion_tokens":9}}`
	w := e.do(t, "/v1/completions", `{"model":"gpt-3.5-turbo-instruct","prompt":"How long is the refund window?","n":2,`+
		`"trustagent":{"context":[{"id":"refund-policy.md","text":"Annual plans can be refunded within 30 days of purchase."}]}}`, hdr)
	checkBlocked(t, w, e.sink.result(t))
	var body struct {
		ID      string `json:"id"`
		Choices []struct {
			Text         string `json:"text"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.ID != "cmpl-up" || len(body.Choices) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for i, c := range body.Choices {
		if c.Text != heldBack || c.FinishReason != llmapi.FinishReasonContentFilter {
			t.Errorf("choice %d: %+v", i, c)
		}
	}
}

// TestBlockActionOnlyHeaders: with headers: action_only a block sends only Action beside the ids.
func TestBlockActionOnlyHeaders(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("block", "headers: action_only")})
	req, answer, hdr := contradicted(t)
	w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
	for _, name := range ResponseHeaders {
		want := name == HeaderTraceID || name == HeaderRequestID || name == HeaderAction
		if got := w.Header().Get(name) != ""; got != want {
			t.Errorf("%s present=%v, want %v", name, got, want)
		}
	}
	checkBlocked(t, w, e.sink.result(t))
}

// TestFallbackDecisionDeliversWhatFallbackChose: on a fallback route only a fallback decision
// (would block) runs the route's fallback, whose choice is delivered and recorded, attempt 1
// first (fallback_test.go covers the fallback itself); an answer that passes goes out as it came,
// with no fallback call.
func TestFallbackDecisionDeliversWhatFallbackChose(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass},
		policyYAML: blockPolicy("fallback", "")})
	e.up.body = chatAnswer(fallbackText)
	req, answer, hdr := contradicted(t)
	w := e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), hdr)
	r := e.sink.result(t)
	if r.Decision.Action != decision.ActionFallback || w.Header().Get(HeaderAction) != decision.ActionFallback ||
		w.Header().Get(HeaderWouldAction) != decision.ActionBlock || w.Body.String() != e.up.body || e.up.calls() != 1 {
		t.Errorf("decision %s, headers %v, %d upstream calls: %s", r.Decision.Action, w.Header(), e.up.calls(), w.Body.String())
	}
	checkFinal(t, r, decision.ActionFallback, gate.DeliveredFallback, fallbackText, []string{gate.FlagFallbackUsed}, nil)
	checkAttempts(t, e, r, answer, fallbackText)

	p := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("fallback", "")})
	sc := demoScenario(t, "grounded-pass")
	w = p.do(t, "/v1/chat/completions", withTestResponse(t, string(sc.Request), sc.Response), map[string]string{"X-TrustAgent-Scenario": sc.ID})
	r = p.sink.result(t)
	if r.Decision.Action != decision.ActionPass || w.Header().Get(HeaderAction) != decision.ActionPass || p.up.calls() != 0 ||
		gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()) != sc.Response {
		t.Errorf("decision %s, %d upstream calls: %s", r.Decision.Action, p.up.calls(), w.Body.String())
	}
	checkAttempts(t, p, r, sc.Response, "")
}

// refusingJev refuses every decision-model call before any vendor request with kind.
type refusingJev struct {
	jev.Client
	kind string
}

func (r refusingJev) Decide(context.Context, *jev.Request, jev.CallOptions) (*jev.Response, error) {
	return nil, &jev.Error{Kind: r.kind, Message: "refused in the test"}
}

// TestFailMatrixOnClosedRoutes: on closed block and fallback routes an answer that cannot be
// checked is blocked (an unparseable request, a 2xx that is not JSON, an oversized answer, an
// overloaded or absent decision model), while an upstream non-2xx, a transport error and an
// answer without text pass through unenforced (D4, D5).
func TestFailMatrixOnClosedRoutes(t *testing.T) {
	mock, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true}})
	if err != nil {
		t.Fatal(err)
	}
	type check struct {
		name  string
		opts  envOpts
		setup func(*env)
		body  string
		// blocked answers are replaced by the block body; the others pass unenforced with status.
		blocked bool
		status  int
		skip    string
	}
	cases := []check{
		{name: "unparseable_request", body: `{"model": `, blocked: true, skip: gate.SkipUnparseableRequest},
		{name: "non-JSON 2xx", setup: func(e *env) { e.up.ctype, e.up.body = "text/html", "<html>"+slaText+"</html>" },
			blocked: true, skip: gate.SkipUpstreamError},
		{name: "oversized", opts: envOpts{maxBytes: 64}, blocked: true, skip: gate.SkipUpstreamError},
		{name: "overload", opts: envOpts{jev: refusingJev{mock, jev.KindOverload}}, blocked: true, skip: gate.SkipOverload},
		{name: "decision_model_off", opts: envOpts{jevBackend: jev.BackendOff}, blocked: true, skip: gate.SkipDecisionModelOff},
		{name: "non-2xx", setup: func(e *env) { e.up.status, e.up.body = http.StatusTooManyRequests, `{"error":{"message":"slow down"}}` },
			status: http.StatusTooManyRequests, skip: gate.SkipUpstreamError},
		{name: "transport error", setup: func(e *env) { e.up.Close() }, status: http.StatusBadGateway, skip: gate.SkipUpstreamError},
		{name: "no_text", setup: func(e *env) {
			e.up.body = `{"id":"chatcmpl-up","object":"chat.completion","created":7,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":null,` +
				`"tool_calls":[{"id":"c1","type":"function","function":{"name":"refund","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		}, status: http.StatusOK, skip: gate.SkipNoText},
	}
	for _, mode := range []string{decision.ModeBlock, decision.ModeFallback} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				o := tc.opts
				o.enforcing, o.policyYAML = true, blockPolicy(mode, "fail: closed")
				e := newEnv(t, o)
				if tc.setup != nil {
					tc.setup(e)
				}
				body := tc.body
				if body == "" {
					body = defaultRouteBody
				}
				upstreamBody := e.up.body
				w := e.do(t, "/v1/chat/completions", body, nil)
				r := e.sink.result(t)
				h := w.Header()
				if h.Get(HeaderStatus) != "skipped:"+tc.skip || r.Decision.EffectiveMode != mode {
					t.Errorf("status %q mode %s", h.Get(HeaderStatus), r.Decision.EffectiveMode)
				}
				if tc.blocked {
					checkBlocked(t, w, r)
					if w.Code != http.StatusOK || gatedText(t, llmapi.FormatOpenAIChat, w.Body.Bytes()) != heldBack ||
						strings.Contains(w.Body.String(), slaText) || h.Get("Content-Type") != "application/json" {
						t.Errorf("%d %s", w.Code, w.Body.String())
					}
					if !slices.Contains(r.Decision.Reasons, decision.ReasonFailClosed) || r.Decision.Degraded == nil ||
						h.Get(HeaderWouldAction) != decision.ActionPass {
						t.Errorf("reasons %v degraded %v would %q", r.Decision.Reasons, r.Decision.Degraded, h.Get(HeaderWouldAction))
					}
					return
				}
				if w.Code != tc.status || h.Get(HeaderAction) != decision.ActionPass || h.Get(HeaderWouldAction) != "" {
					t.Errorf("%d action %q would %q: %s", w.Code, h.Get(HeaderAction), h.Get(HeaderWouldAction), w.Body.String())
				}
				if tc.status != http.StatusBadGateway && w.Body.String() != upstreamBody {
					t.Errorf("the answer passes through as it came: %s", w.Body.String())
				}
				if r.Decision.Enforced || r.Final == nil || r.Final.Action != decision.ActionPass || r.Final.Delivered != gate.DeliveredOriginal {
					t.Errorf("enforced %v final %+v", r.Decision.Enforced, r.Final)
				}
				contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
			})
		}
	}
}

// TestOversizedAnswerOnAnOpenRoute: fail open lets an unchecked answer through, but an enforcing
// route never relays an oversized answer it could not read; a flag route relays all of it.
func TestOversizedAnswerOnAnOpenRoute(t *testing.T) {
	e := newEnv(t, envOpts{enforcing: true, maxBytes: 64, policyYAML: blockPolicy("block", "fail: open")})
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	r := e.sink.result(t)
	if r.Decision.Action != decision.ActionPass || r.Final == nil || r.Final.Action != decision.ActionBlock ||
		strings.Contains(w.Body.String(), slaText) || w.Header().Get(HeaderAction) != decision.ActionBlock {
		t.Errorf("decision %s final %+v body %s", r.Decision.Action, r.Final, w.Body.String())
	}

	f := newEnv(t, envOpts{maxBytes: 64, policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	w = f.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Body.String() != f.up.body || w.Header().Get(HeaderStatus) != "skipped:upstream_error" || w.Header().Get(HeaderAction) != decision.ActionPass {
		t.Errorf("a flag route relays the whole oversized answer: %q %v", w.Body.String(), w.Header())
	}
	f.sink.result(t)
}

// TestSampledOutIsExempt: an answer sampled out of the decision model is not a failure: fail
// closed does not apply and nothing is degraded (sampling only happens on observe routes).
func TestSampledOutIsExempt(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: observe, fail: closed, sample_rate: 0}\n"})
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Code != http.StatusOK || w.Body.String() != e.up.body {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	r := e.sink.result(t)
	if r.Status != "skipped:sampled_out" || r.Decision.Action != decision.ActionObserve || r.Decision.Degraded != nil ||
		slices.Contains(r.Decision.Reasons, decision.ReasonFailClosed) {
		t.Errorf("status %s action %s degraded %v reasons %v", r.Status, r.Decision.Action, r.Decision.Degraded, r.Decision.Reasons)
	}
	if err := e.handler.Drain(context.Background()); err != nil {
		t.Error(err)
	}
}

// TestMultiChoiceGatesChoiceZero: with n>1 only choice 0 is gated and the trace is flagged
// multi_choice (D6); a passing answer is delivered as it came.
func TestMultiChoiceGatesChoiceZero(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: twoChecks})
	e.up.body = `{"id":"chatcmpl-up","object":"chat.completion","created":7,"model":"gpt-4o-mini","choices":[` +
		`{"index":1,"message":{"role":"assistant","content":"Priority tickets wait a week."},"finish_reason":"stop"},` +
		`{"index":0,"message":{"role":"assistant","content":"` + slaText + `"},"finish_reason":"stop"}]}`
	w := e.do(t, "/v1/chat/completions", strings.Replace(defaultRouteBody, `"model":"gpt-4o-mini",`, `"model":"gpt-4o-mini","n":2,`, 1),
		map[string]string{"X-TrustAgent-Scenario": "grounded-pass"})
	r := e.sink.result(t)
	if w.Body.String() != e.up.body || r.ResponseText != slaText || !slices.Contains(r.Flags, gate.FlagMultiChoice) ||
		!slices.Contains(r.Trace.Flags, gate.FlagMultiChoice) || w.Header().Get(HeaderAction) != decision.ActionPass {
		t.Errorf("response text %q flags %v action %q", r.ResponseText, r.Flags, w.Header().Get(HeaderAction))
	}
}
