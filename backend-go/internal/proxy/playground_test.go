package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/upstream"
)

const injectedKey = "sk-playground-injected"

// playgroundContext is the gin context of a dashboard's POST /api/playground/run: a browser
// request with a session cookie, the dashboard's CSRF header and an admin key.
func playgroundContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	r := httptest.NewRequest(http.MethodPost, "/api/playground/run", strings.NewReader("{}"))
	r.Header.Set("Cookie", "ta_session=dashboard-session")
	r.Header.Set("X-TrustAgent-CSRF", "1")
	r.Header.Set("X-TrustAgent-Key", "ta-admin-key")
	r.Header.Set("Origin", "http://localhost:5173")
	c.Request = r
	return c
}

// refundRun is the refund-contradiction-flag conversation as a Playground run: the mock decision
// model finds the answer critical on both checks (in test mode, with the scenario).
func refundRun(t *testing.T, mode string) PlaygroundInput {
	t.Helper()
	sc := demoScenario(t, "refund-contradiction-flag")
	var req struct {
		Model    string              `json:"model"`
		Messages []PlaygroundMessage `json:"messages"`
	}
	if err := json.Unmarshal(sc.Request, &req); err != nil {
		t.Fatal(err)
	}
	in := PlaygroundInput{Mode: mode, Model: req.Model, Messages: req.Messages, Scenario: sc.ID}
	if mode == PlaygroundAudit {
		in.Response = sc.Response
	}
	return in
}

// injecting points e's OpenAI upstream at base with the gateway's own key (inject mode).
func (e *env) injecting(t *testing.T, base string) {
	t.Helper()
	up, err := upstream.New(upstream.Config{BaseURL: base, APIKey: injectedKey, Timeout: 5 * time.Second, HeaderTimeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.handler.ups = &upstream.Set{OpenAI: up}
}

func playgroundErr(t *testing.T, err error) *PlaygroundError {
	t.Helper()
	var pe *PlaygroundError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v, want a PlaygroundError", err)
	}
	return pe
}

// TestPlaygroundAuditModeOverrideBlocks: an audit-mode run answers through the test_response path
// (no upstream call, upstream null), and the admin's block override delivers the route's block
// text; the trace is the Playground's, and the run gets a playground deep audit.
func TestPlaygroundAuditModeOverrideBlocks(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, policyYAML: blockPolicy("flag", "")})
	in := refundRun(t, PlaygroundAudit)
	in.ModeOverride, in.KeyLabel = decision.ModeBlock, "root"
	res, err := e.handler.Playground(context.Background(), playgroundContext(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := e.sink.result(t)
	if res.DeliveredText != heldBack || res.ResponseText != in.Response || res.Upstream != nil {
		t.Errorf("delivered %q response %q upstream %+v", res.DeliveredText, res.ResponseText, res.Upstream)
	}
	if res.Headers[HeaderAction] != decision.ActionBlock || res.Headers[HeaderMode] != decision.ModeBlock ||
		res.Headers[HeaderTraceID] != res.TraceID || res.Headers[HeaderSource] != gate.SourcePlayground {
		t.Errorf("headers %v", res.Headers)
	}
	d := res.Decision
	if d.Mode != decision.ModeBlock || d.Action != decision.ActionBlock || !d.Enforced || d.Attempt != 0 || d.TraceID != res.TraceID {
		t.Errorf("decision mode %s action %s enforced %v attempt %d", d.Mode, d.Action, d.Enforced, d.Attempt)
	}
	contractstest.Validate(t, "decision.v1.schema.json", d)
	if res.AuditStatus != gate.AuditQueued || r.Audit.Selection != gate.SelectionPlayground || r.Audit.Lane != gate.LaneHigh {
		t.Errorf("audit %s: plan %+v, want a playground audit in the high lane", res.AuditStatus, r.Audit)
	}
	tr := r.Trace
	if tr.Source != gate.SourcePlayground || tr.AnswerSource != gate.AnswerScripted || tr.KeyLabel != "root" ||
		tr.Scenario != in.Scenario || tr.APIFormat != "openai_chat" || tr.Path != "/v1/chat/completions" {
		t.Errorf("trace %+v", tr)
	}
	if slices.Contains(r.Flags, gate.FlagTestMode) {
		t.Errorf("flags %v: a Playground run is no test-mode traffic", r.Flags)
	}
	if r.Final == nil || r.Final.Delivered != gate.DeliveredBlocked {
		t.Errorf("final %+v", r.Final)
	}
	if n := len(e.sink.traces()); n != 1 {
		t.Errorf("%d traces started, want 1", n)
	}
	if e.up.calls() != 0 {
		t.Error("an audit-mode run called the upstream")
	}
}

// TestPlaygroundOverrideBypassesClientMode: overrides.client_mode none keeps clients to the
// route's mode, but not the admin's override; a mode the gateway does not enforce stays refused.
func TestPlaygroundOverrideBypassesClientMode(t *testing.T) {
	yaml := strings.Replace(blockPolicy("observe", ""), "defaults:", "overrides: {client_mode: none}\ndefaults:", 1)
	e := newEnv(t, envOpts{testMode: true, policyYAML: yaml})
	in := refundRun(t, PlaygroundAudit)
	in.ModeOverride = decision.ModeFlag
	res, err := e.handler.Playground(context.Background(), playgroundContext(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	if res.Decision.Mode != decision.ModeFlag || res.Decision.Action != decision.ActionFlag || res.DeliveredText != in.Response {
		t.Errorf("mode %s action %s delivered %q", res.Decision.Mode, res.Decision.Action, res.DeliveredText)
	}

	// Without an override the run is checked in the route's mode, synchronously even in observe.
	in.ModeOverride = ""
	if res, err = e.handler.Playground(context.Background(), playgroundContext(), in, nil); err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	if res.Decision.Mode != decision.ModeObserve || res.Decision.Action != decision.ActionObserve || res.Headers[HeaderStatus] != "ok" {
		t.Errorf("mode %s action %s headers %v", res.Decision.Mode, res.Decision.Action, res.Headers)
	}

	// This policy is not enforcing: block is refused like any client's, and nothing is recorded.
	in.ModeOverride = decision.ModeBlock
	_, err = e.handler.Playground(context.Background(), playgroundContext(), in, nil)
	if pe := playgroundErr(t, err); pe.Status != http.StatusUnprocessableEntity || pe.Field != "mode_override" || pe.TraceID != "" {
		t.Errorf("block override without enforcement: %+v", pe)
	}
	if n := len(e.sink.traces()); n != 2 {
		t.Errorf("%d traces started, want 2 (the refused run records none)", n)
	}
}

// TestPlaygroundNamedRoute: a named route is used instead of the one the policy matches, and
// the evidence is read with its settings; an unknown name is a 422 that records nothing.
func TestPlaygroundNamedRoute(t *testing.T) {
	yaml := blockPolicy("flag", "") + `routes:
  - name: context-only
    match: {headers: {X-TrustAgent-Route: context-only}}
    evidence: {sources: [extension]}
`
	e := newEnv(t, envOpts{testMode: true, policyYAML: yaml})
	in := refundRun(t, PlaygroundAudit)
	in.Route = "context-only"
	in.Evidence = []PlaygroundDocument{{ID: "faq-7", Text: "Annual plans can be refunded within 30 days.", Source: "help center"}}
	res, err := e.handler.Playground(context.Background(), playgroundContext(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := e.sink.result(t)
	if r.Trace.Route != "context-only" || res.Decision.Route != "context-only" || !strings.HasPrefix(res.Headers[HeaderRoute], "context-only@v") {
		t.Errorf("trace route %s decision route %s header %s", r.Trace.Route, res.Decision.Route, res.Headers[HeaderRoute])
	}
	// Only the run's own document counts on this route, not the system prompt's.
	if len(r.Evidence) != 1 || r.Evidence[0].ID != "faq-7" || r.Evidence[0].Source != "extension" || r.Evidence[0].Label != "help center" {
		t.Errorf("evidence %+v", r.Evidence)
	}

	in.Route = "no-such-route"
	_, err = e.handler.Playground(context.Background(), playgroundContext(), in, nil)
	if pe := playgroundErr(t, err); pe.Status != http.StatusUnprocessableEntity || pe.Code != CodeValidationFailed || pe.Field != "route" {
		t.Errorf("unknown route: %+v", pe)
	}
	if n := len(e.sink.traces()); n != 1 {
		t.Errorf("%d traces started, want 1", n)
	}
}

// TestPlaygroundScenarioOnlyInTestMode: the scenario id reaches the trace (and so the mock
// decision model and judge) only in test mode.
func TestPlaygroundScenarioOnlyInTestMode(t *testing.T) {
	for _, testMode := range []bool{true, false} {
		e := newEnv(t, envOpts{testMode: testMode, policyYAML: blockPolicy("flag", "")})
		in := refundRun(t, PlaygroundAudit)
		if _, err := e.handler.Playground(context.Background(), playgroundContext(), in, nil); err != nil {
			t.Fatal(err)
		}
		r := e.sink.result(t)
		want := ""
		if testMode {
			want = in.Scenario
		}
		if r.Trace.Scenario != want || r.Audit.MockScenario != want {
			t.Errorf("test mode %v: trace scenario %q, audit scenario %q", testMode, r.Trace.Scenario, r.Audit.MockScenario)
		}
	}
}

// TestPlaygroundChatModeNeedsTheGatewaysKey: without an injected upstream credential chat mode
// is refused with 503 credentials_unavailable before anything is recorded or sent.
func TestPlaygroundChatModeNeedsTheGatewaysKey(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true})
	_, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundChat), nil)
	if pe := playgroundErr(t, err); pe.Status != http.StatusServiceUnavailable || pe.Code != CodeCredentialsUnavailable || pe.TraceID != "" {
		t.Errorf("%+v", pe)
	}
	if len(e.sink.traces()) != 0 || e.up.calls() != 0 {
		t.Errorf("a refused run recorded %d traces and made %d upstream calls", len(e.sink.traces()), e.up.calls())
	}
}

// TestPlaygroundChatMode: chat mode asks the upstream with the gateway's own key and
// stream:false, without the gateway's members or the dashboard's headers, and streams the answer
// to onToken before the gate decides.
func TestPlaygroundChatMode(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
	e.injecting(t, e.up.URL)
	in := refundRun(t, PlaygroundChat)
	answer := "You can get a full refund within 60 days of purchase."
	e.up.body = chatAnswer(answer)
	in.Evidence = []PlaygroundDocument{{Text: "Monthly plans are non-refundable."}}
	var tokens []string
	onToken := func(text string) {
		if len(e.sink.decided) != 0 {
			t.Error("a token arrived after the decision")
		}
		tokens = append(tokens, text)
	}
	res, err := e.handler.Playground(context.Background(), playgroundContext(), in, onToken)
	if err != nil {
		t.Fatal(err)
	}
	r := e.sink.result(t)
	if strings.Join(tokens, "") != answer || len(tokens) < 2 {
		t.Errorf("tokens %q", tokens)
	}
	if res.Upstream == nil || res.Upstream.Status != http.StatusOK || res.ResponseText != answer || res.DeliveredText != answer {
		t.Errorf("result %+v", res)
	}
	if res.Decision.Action != decision.ActionFlag || r.Trace.AnswerSource != gate.AnswerLive || r.Trace.Source != gate.SourcePlayground {
		t.Errorf("action %s trace %+v", res.Decision.Action, r.Trace)
	}

	req, body := e.up.last(t)
	if req.URL.Path != "/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer "+injectedKey {
		t.Errorf("upstream call %s with Authorization %q", req.URL.Path, req.Header.Get("Authorization"))
	}
	for _, h := range []string{"Cookie", "X-Trustagent-Key", "X-Trustagent-Csrf", "Origin"} {
		if req.Header.Get(h) != "" {
			t.Errorf("the dashboard's %s reached the upstream", h)
		}
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["stream"]) != "false" || string(sent["model"]) != `"gpt-4o-mini"` || sent["trustagent"] != nil || sent["test_response"] != nil {
		t.Errorf("upstream body %s", body)
	}
}

// TestPlaygroundUpstreamFailures: a chat-mode run whose upstream fails records its trace as
// skipped:upstream_error (never enforced) and returns a 502 that names the trace; an upstream's
// error status is reported without its body.
func TestPlaygroundUpstreamFailures(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
	e.injecting(t, e.up.URL)
	e.up.status, e.up.body = http.StatusUnauthorized, `{"error":{"message":"Incorrect API key provided: sk-proj-****abcd","code":"invalid_api_key"}}`
	_, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundChat), nil)
	pe := playgroundErr(t, err)
	r := e.sink.result(t)
	if pe.Status != http.StatusBadGateway || pe.Code != CodeUpstreamError || pe.UpstreamStatus != http.StatusUnauthorized ||
		pe.TraceID != r.Trace.TraceID || strings.Contains(pe.Message, "sk-proj") {
		t.Errorf("%+v", pe)
	}
	if r.Status != "skipped:upstream_error" || r.Decision.Enforced || r.Upstream.Status != http.StatusUnauthorized {
		t.Errorf("status %s enforced %v upstream %d", r.Status, r.Decision.Enforced, r.Upstream.Status)
	}

	// Nothing listens: the call fails outright.
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	e.injecting(t, closed.URL)
	_, err = e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundChat), nil)
	pe = playgroundErr(t, err)
	r = e.sink.result(t)
	if pe.Status != http.StatusBadGateway || pe.Code != CodeUpstreamError || pe.UpstreamStatus != 0 || pe.TraceID != r.Trace.TraceID {
		t.Errorf("%+v", pe)
	}
	if r.Status != "skipped:upstream_error" || r.Decision.Enforced || r.Final == nil || r.Final.Action != decision.ActionPass {
		t.Errorf("status %s enforced %v final %+v", r.Status, r.Decision.Enforced, r.Final)
	}
}

// TestPlaygroundPieces: the token pieces join to the answer, a word and its space at a time, and
// never cut a rune.
func TestPlaygroundPieces(t *testing.T) {
	for _, text := range []string{
		"", "one", "Refunds take 5 to 10 business days.", "  leading and trailing  ",
		"line one\nline two", strings.Repeat("长", 70) + " 退款 😀 ok", strings.Repeat("x", 100),
	} {
		pieces := playgroundPieces(text)
		if strings.Join(pieces, "") != text {
			t.Errorf("%q: pieces %q", text, pieces)
		}
		for _, p := range pieces {
			if p == "" || !utf8.ValidString(p) || utf8.RuneCountInString(p) > maxPieceRunes {
				t.Errorf("%q: piece %q", text, p)
			}
		}
	}
	if got := playgroundPieces("Refunds take 5 days."); !slices.Equal(got, []string{"Refunds ", "take ", "5 ", "days."}) {
		t.Errorf("pieces %q", got)
	}
}

// TestPlaygroundRouteMatchedLikeAChatCompletion: without a route name, the run is matched as a
// POST /v1/chat/completions of its model.
func TestPlaygroundRouteMatchedLikeAChatCompletion(t *testing.T) {
	yaml := blockPolicy("flag", "") + `routes:
  - name: mini
    match: {models: ["gpt-4o-mini"]}
    mode: observe
`
	e := newEnv(t, envOpts{testMode: true, policyYAML: yaml})
	res, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundAudit), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	if res.Decision.Route != "mini" || res.Decision.Mode != decision.ModeObserve {
		t.Errorf("route %s mode %s", res.Decision.Route, res.Decision.Mode)
	}
}
