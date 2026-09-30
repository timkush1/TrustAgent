package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// withTestResponse adds test_response to a JSON request body.
func withTestResponse(t *testing.T, body, text string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m["test_response"] = text
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// gatedText reads the gated text (choice 0, or the Anthropic text blocks) of a JSON answer.
func gatedText(t *testing.T, format string, body []byte) string {
	t.Helper()
	r, err := llmapi.ParseResponse(format, body)
	if err != nil {
		t.Fatalf("not a %s answer: %s", format, body)
	}
	c, _ := r.Gated()
	return c.Text
}

const slaText = "Priority tickets get a first response within 4 hours."

// TestGatedFormats: /v1/completions and /v1/messages are gated like /v1/chat/completions, for a
// scripted test_response and for an upstream answer; the trace records each API format.
func TestGatedFormats(t *testing.T) {
	cases := []struct {
		name, path, format, request, checked string
		// upstream is the fake upstream of the format's family; answer is its JSON answer.
		upstream     func(*env) *fakeUpstream
		answer       string
		idPrefix     string
		promptTokens int
	}{
		{
			name: "chat", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat,
			request: defaultRouteBody, checked: "request_system:1",
			upstream: func(e *env) *fakeUpstream { return e.up }, idPrefix: llmapi.SyntheticIDPrefix, promptTokens: 20,
		},
		{
			name: "completions", path: "/v1/completions", format: llmapi.FormatOpenAICompletions,
			request: `{"model":"gpt-3.5-turbo-instruct","prompt":"How fast do you answer priority tickets?","max_tokens":64,` +
				`"trustagent":{"context":[{"id":"sla","text":"` + slaText + `"}]}}`,
			checked: "extension:1", upstream: func(e *env) *fakeUpstream { return e.up },
			answer: `{"id":"cmpl-up","object":"text_completion","created":1,"model":"gpt-3.5-turbo-instruct",` +
				`"choices":[{"index":0,"text":"` + slaText + `","logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":9}}`,
			idPrefix: llmapi.SyntheticCompletionIDPrefix, promptTokens: 12,
		},
		{
			name: "messages", path: "/v1/messages", format: llmapi.FormatAnthropicMessages,
			request: `{"model":"claude-haiku-4-5","max_tokens":256,"system":[{"type":"text","text":"Answer from the documents."},` +
				`{"type":"text","text":"<document id=\"sla\">` + slaText + `</document>"}],` +
				`"messages":[{"role":"user","content":"How fast do you answer priority tickets?"}],"trustagent":{"route":"x"}}`,
			checked: "request_system:1", upstream: func(e *env) *fakeUpstream { return e.anth },
			answer: anthropicBody, idPrefix: llmapi.SyntheticMessageIDPrefix, promptTokens: 20,
		},
	}
	scenario := map[string]string{"X-TrustAgent-Scenario": "grounded-pass"}
	for _, tc := range cases {
		checkGated := func(t *testing.T, w *httptest.ResponseRecorder, r *gate.Result, answer string) {
			t.Helper()
			h := w.Header()
			if w.Code != http.StatusOK || h.Get(HeaderStatus) != "ok" || h.Get(HeaderAction) != decision.ActionPass ||
				h.Get(HeaderCheckedAgainst) != tc.checked || h.Get(HeaderMode) != decision.ModeFlag {
				t.Errorf("%d status %q action %q checked %q: %s", w.Code, h.Get(HeaderStatus), h.Get(HeaderAction),
					h.Get(HeaderCheckedAgainst), w.Body.String())
			}
			if got := gatedText(t, tc.format, w.Body.Bytes()); got != answer {
				t.Errorf("answer %q, want %q", got, answer)
			}
			if r.Trace.APIFormat != tc.format || r.Trace.Path != tc.path || r.ResponseText != answer {
				t.Errorf("trace format %q path %q response %q", r.Trace.APIFormat, r.Trace.Path, r.ResponseText)
			}
			if r.Final == nil || r.Final.Action != decision.ActionPass || r.Final.Delivered != gate.DeliveredOriginal || r.Final.Text != answer {
				t.Errorf("final %+v", r.Final)
			}
			contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
		}

		t.Run(tc.name+"/scripted", func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true, policyYAML: twoChecks})
			const scripted = "Priority tickets are answered within 4 hours."
			w := e.do(t, tc.path, withTestResponse(t, tc.request, scripted), scenario)
			r := e.sink.result(t)
			checkGated(t, w, r, scripted)
			var id struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(w.Body.Bytes(), &id) != nil || !strings.HasPrefix(id.ID, tc.idPrefix) {
				t.Errorf("synthetic id %q, want prefix %s", id.ID, tc.idPrefix)
			}
			if w.Header().Get("Content-Type") != "application/json" || r.Trace.AnswerSource != gate.AnswerScripted {
				t.Errorf("content type %q answer source %q", w.Header().Get("Content-Type"), r.Trace.AnswerSource)
			}
			if e.up.calls()+e.anth.calls() != 0 {
				t.Error("a scripted answer calls no upstream")
			}
		})

		t.Run(tc.name+"/upstream", func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true, policyYAML: twoChecks})
			up := tc.upstream(e)
			if tc.answer != "" {
				up.body = tc.answer
			}
			w := e.do(t, tc.path, tc.request, scenario)
			r := e.sink.result(t)
			checkGated(t, w, r, slaText)
			if w.Body.String() != up.body {
				t.Errorf("a passing answer is the upstream's bytes: %s", w.Body.String())
			}
			got, fwd := up.last(t)
			if got.URL.Path != tc.path || strings.Contains(string(fwd), "trustagent") {
				t.Errorf("upstream got %s %s", got.URL.Path, fwd)
			}
			if e.up.calls()+e.anth.calls() != 1 {
				t.Errorf("%d upstream calls: only the %s family's upstream is called", e.up.calls()+e.anth.calls(), llmapi.Family(tc.format))
			}
			if r.Upstream.Status != http.StatusOK || r.Upstream.PromptTokens == nil || *r.Upstream.PromptTokens != tc.promptTokens {
				t.Errorf("upstream record %+v", r.Upstream)
			}
		})
	}
}

// TestPassthroughUpstreamPerPath: count_tokens goes to the Anthropic upstream and the Responses
// API to the OpenAI one, ungated and without the gateway's members.
func TestPassthroughUpstreamPerPath(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.anth.body = `{"input_tokens":42}`
	w := e.do(t, "/v1/messages/count_tokens", `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}],"trustagent":{"route":"x"}}`,
		map[string]string{"anthropic-version": "2023-06-01", "Authorization": "", "X-Api-Key": "sk-ant-client"})
	if w.Code != http.StatusOK || w.Body.String() != e.anth.body || w.Header().Get(HeaderStatus) != "" || w.Header().Get(HeaderTraceID) == "" {
		t.Errorf("count_tokens: %d %q status %q", w.Code, w.Body.String(), w.Header().Get(HeaderStatus))
	}
	got, fwd := e.anth.last(t)
	if got.URL.Path != "/v1/messages/count_tokens" || string(fwd) != `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hi"}]}` ||
		got.Header.Get("Anthropic-Version") != "2023-06-01" || got.Header.Get("X-Api-Key") != "sk-ant-client" {
		t.Errorf("anthropic upstream got %s %s %v", got.URL.Path, fwd, got.Header)
	}

	w = e.send(t, http.MethodGet, "/v1/responses/resp_1/input_items", "", nil)
	if w.Header().Get(HeaderStatus) != StatusSkippedUnsupportedAPI {
		t.Errorf("responses: status %q", w.Header().Get(HeaderStatus))
	}
	if got, _ := e.up.last(t); got.URL.Path != "/v1/responses/resp_1/input_items" {
		t.Errorf("openai upstream got %s", got.URL.Path)
	}
	if e.up.calls() != 1 || e.anth.calls() != 1 || len(e.sink.traces()) != 0 {
		t.Errorf("calls openai %d anthropic %d traces %d: passthrough is not traced", e.up.calls(), e.anth.calls(), len(e.sink.traces()))
	}

	// Outside test mode, test_response is refused on the passthrough paths too, in their shape.
	w = e.do(t, "/v1/messages/count_tokens", `{"model":"m","messages":[],"test_response":"x"}`, nil)
	checkAnthropicError(t, w, http.StatusBadRequest, llmapi.AnthropicInvalidRequestError)
}

// checkAnthropicError checks a TrustAgent error in the Anthropic shape.
func checkAnthropicError(t *testing.T, w *httptest.ResponseRecorder, status int, errType string) {
	t.Helper()
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Type != "error" || body.Error.Type != errType ||
		body.Error.Message == "" || strings.Contains(w.Body.String(), `"code"`) || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("want %d %s in the Anthropic error shape, got %d %s", status, errType, w.Code, w.Body.String())
	}
}

// checkOpenAIError checks a TrustAgent error in the OpenAI shape.
func checkOpenAIError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error struct {
			Message, Type, Code string
			Param               *string
		} `json:"error"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Type != llmapi.OpenAIErrorType ||
		body.Error.Code != code || body.Error.Message == "" || !strings.Contains(w.Body.String(), `"param":null`) {
		t.Errorf("want %d %s in the OpenAI error shape, got %d %s", status, code, w.Code, w.Body.String())
	}
}

const messagesBody = `{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`

// TestErrorsTakeTheRoutesShape: every error TrustAgent writes on /v1/messages is in the Anthropic
// shape (ingest 401, the v1 limiter's 429 with Retry-After, unknown paths, test_mode_disabled, a
// body too large, an unreachable upstream); the OpenAI routes keep the OpenAI shape.
func TestErrorsTakeTheRoutesShape(t *testing.T) {
	e := newEnv(t, envOpts{})
	w := e.do(t, "/v1/messages", messagesBody, map[string]string{"X-TrustAgent-Key": ""})
	checkAnthropicError(t, w, http.StatusUnauthorized, llmapi.AnthropicAuthenticationError)
	if w.Header().Get(HeaderTraceID) == "" {
		t.Error("errors carry the trace id")
	}
	checkOpenAIError(t, e.do(t, "/v1/chat/completions", defaultRouteBody, map[string]string{"X-TrustAgent-Key": ""}), http.StatusUnauthorized, "unauthorized")

	w = e.do(t, "/v1/messages", withTestResponse(t, messagesBody, "scripted"), nil)
	checkAnthropicError(t, w, http.StatusBadRequest, llmapi.AnthropicInvalidRequestError)
	checkOpenAIError(t, e.do(t, "/v1/completions", `{"model":"m","prompt":"x","test_response":"y"}`, nil), http.StatusBadRequest, CodeTestModeDisabled)

	for _, path := range []string{"/v1/messages", "/v1/messages/batches"} {
		checkAnthropicError(t, e.send(t, http.MethodGet, path, "", nil), http.StatusNotFound, llmapi.AnthropicInvalidRequestError)
	}
	checkOpenAIError(t, e.send(t, http.MethodGet, "/v1/audio/speech", "", nil), http.StatusNotFound, CodeNotFound)

	w = e.do(t, "/v1/messages", `{"x":"`+strings.Repeat("a", 1<<20)+`"}`, nil)
	checkAnthropicError(t, w, http.StatusRequestEntityTooLarge, llmapi.AnthropicInvalidRequestError)

	if e.up.calls()+e.anth.calls() != 0 || len(e.sink.traces()) != 0 {
		t.Error("refused requests reach neither an upstream nor the sink")
	}

	// An unreachable Anthropic upstream: 502 api_error on the gated route and on count_tokens.
	e.anth.Close()
	w = e.do(t, "/v1/messages", messagesBody, nil)
	checkAnthropicError(t, w, http.StatusBadGateway, llmapi.AnthropicAPIError)
	if w.Header().Get(HeaderStatus) != "skipped:upstream_error" {
		t.Errorf("status %q", w.Header().Get(HeaderStatus))
	}
	e.sink.result(t)
	checkAnthropicError(t, e.do(t, "/v1/messages/count_tokens", messagesBody, nil), http.StatusBadGateway, llmapi.AnthropicAPIError)
}

// TestRateLimitTakesTheRoutesShape: the v1 limiter answers 429 with Retry-After in the route's shape.
func TestRateLimitTakesTheRoutesShape(t *testing.T) {
	e := newEnv(t, envOpts{rateLimit: 1})
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		body := messagesBody
		if path == "/v1/chat/completions" {
			body = defaultRouteBody
		}
		// Refused for test_response, the first request still counts against the limit.
		e.do(t, path, withTestResponse(t, body, "x"), nil)
		w := e.do(t, path, body, nil)
		if w.Header().Get("Retry-After") == "" || w.Header().Get(HeaderTraceID) == "" {
			t.Errorf("%s: Retry-After %q", path, w.Header().Get("Retry-After"))
		}
		if path == "/v1/messages" {
			checkAnthropicError(t, w, http.StatusTooManyRequests, llmapi.AnthropicRateLimitError)
		} else {
			checkOpenAIError(t, w, http.StatusTooManyRequests, "rate_limited")
		}
	}
}
