package llmapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFormatForPath(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":       FormatOpenAIChat,
		"/v1/chat/completions/":      FormatOpenAIChat,
		"/v1/completions":            FormatOpenAICompletions,
		"/v1/messages":               FormatAnthropicMessages,
		"/v1/messages/count_tokens":  "",
		"/v1/messages/batches":       "",
		"/v1/responses":              "",
		"/v1/embeddings":             "",
		"/v1/chat/completions/x":     "",
		"/api/v1/chat/completions":   "",
		"":                           "",
		"/v1/chat/completions?x=1":   "",
		"/V1/chat/completions":       "",
		"/v1/chat/completions//":     "",
		"/v1/completions/chat":       "",
		"/v1/messages?beta=true":     "",
		"/v1/responses/resp_1/input": "",
	}
	for path, want := range cases {
		if got := FormatForPath(path); got != want {
			t.Errorf("%q: %q, want %q", path, got, want)
		}
	}
	for path, want := range map[string]bool{
		"/v1/responses": true, "/v1/responses/resp_1": true, "/v1/responses/resp_1/input_items": true,
		"/v1/responsesx": false, "/v1/chat/completions": false,
	} {
		if got := IsResponsesAPI(path); got != want {
			t.Errorf("IsResponsesAPI(%q) = %v", path, got)
		}
	}
	for path, want := range map[string]string{
		"/v1/messages": FamilyAnthropic, "/v1/messages/": FamilyAnthropic, "/v1/messages/count_tokens": FamilyAnthropic,
		"/v1/messages/batches/b_1": FamilyAnthropic, "/v1/messagesx": FamilyOpenAI, "/v1/chat/completions": FamilyOpenAI,
		"/v1/completions": FamilyOpenAI, "/v1/models": FamilyOpenAI, "/v1/responses/resp_1": FamilyOpenAI, "": FamilyOpenAI,
	} {
		if got := FamilyForPath(path); got != want {
			t.Errorf("FamilyForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFormatFamilies(t *testing.T) {
	cases := []struct {
		format, family string
		ok             bool
	}{
		{FormatOpenAIChat, "openai", true},
		{FormatOpenAICompletions, "", true}, // the TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT default is openai
		{FormatOpenAIChat, "anthropic", false},
		{FormatAnthropicMessages, "anthropic", true},
		{FormatAnthropicMessages, "openai", false},
		{FormatAnthropicMessages, "", false},
		{"direct", "openai", false},
	}
	for _, tc := range cases {
		if got := FallbackFormatCompatible(tc.format, tc.family); got != tc.ok {
			t.Errorf("%s/%q: %v", tc.format, tc.family, got)
		}
	}
	if Family("direct") != "" || Family(FormatAnthropicMessages) != FamilyAnthropic {
		t.Error("Family")
	}
}

func TestParseRequestCompletions(t *testing.T) {
	r, err := ParseRequest(FormatOpenAICompletions, []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"<context>Paris is the capital.</context> Capital of France?",
	  "stream":true,"stream_options":{"include_usage":true},"n":2,"trustagent":{"route":"r"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatOpenAICompletions || r.Model != "gpt-3.5-turbo-instruct" || !r.Stream || !r.IncludeUsage || r.N != 2 || r.Extension.Route != "r" {
		t.Errorf("request %+v", r)
	}
	want := []Message{{Role: "user", Parts: []string{"<context>Paris is the capital.</context> Capital of France?"}, Origin: OriginPrompt}}
	if !reflect.DeepEqual(r.Messages, want) || r.LastUserText() != want[0].Parts[0] {
		t.Errorf("messages %+v", r.Messages)
	}

	// A prompt array: every string is kept, only the first one (choice 0) is the user message.
	r, _ = ParseRequest(FormatOpenAICompletions, []byte(`{"prompt":["first",[1,2],"second",3]}`))
	if !reflect.DeepEqual(r.Prompts, []string{"first", "second"}) || len(r.Messages) != 1 || r.Messages[0].Text() != "first" {
		t.Errorf("prompts %q messages %+v", r.Prompts, r.Messages)
	}
	// Token prompts carry no text.
	for _, body := range []string{`{"prompt":[1,2,3]}`, `{"prompt":[[1,2]]}`, `{"prompt":7}`, `{}`} {
		r, err := ParseRequest(FormatOpenAICompletions, []byte(body))
		if err != nil || r.Prompts != nil || r.Messages != nil {
			t.Errorf("%s: %+v %v", body, r, err)
		}
	}
}

func TestParseRequestAnthropic(t *testing.T) {
	body := []byte(`{
	  "model": "claude-sonnet-4-5", "max_tokens": 1024, "stream": true,
	  "system": [{"type": "text", "text": "You are a support bot."}, {"type": "text", "text": "<document id=\"refunds\">30 days.</document>", "cache_control": {"type": "ephemeral"}}],
	  "messages": [
	    {"role": "user", "content": "Where is order 4411?"},
	    {"role": "assistant", "content": [
	      {"type": "thinking", "thinking": "I need the tool.", "signature": "x"},
	      {"type": "text", "text": "Let me check."},
	      {"type": "tool_use", "id": "toolu_1", "name": "get_order", "input": {"id": "4411"}}
	    ]},
	    {"role": "user", "content": [
	      {"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": "Shipped on 2026-03-02."}, {"type": "image", "source": {}}]}
	    ]},
	    {"role": "assistant", "content": "It shipped."},
	    {"role": "user", "content": [
	      {"type": "tool_result", "tool_use_id": "toolu_2", "content": "Carrier: UPS.", "is_error": false},
	      {"type": "document", "source": {"type": "text", "media_type": "text/plain", "data": "ignored"}},
	      {"type": "text", "text": "<context>Delivery takes 3 days.</context>"},
	      {"type": "text", "text": "When will it arrive?"}
	    ]},
	    "junk",
	    {"role": "user", "content": [{"type": "image", "source": {}}]}
	  ],
	  "trustagent": {"context": [{"text": "ext"}]}
	}`)
	r, err := ParseRequest(FormatAnthropicMessages, body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Format != FormatAnthropicMessages || r.Model != "claude-sonnet-4-5" || !r.Stream || len(r.Extension.Context) != 1 {
		t.Errorf("request %+v", r)
	}
	want := []Message{
		{Role: "system", Parts: []string{"You are a support bot.", `<document id="refunds">30 days.</document>`}, Origin: OriginAnthropicSystem},
		{Role: "user", Parts: []string{"Where is order 4411?"}},
		{Role: "assistant", Parts: []string{"Let me check."}, ToolCalls: []ToolCall{{ID: "toolu_1", Name: "get_order"}}},
		{Role: "tool", ToolCallID: "toolu_1", Parts: []string{"Shipped on 2026-03-02."}, Origin: OriginAnthropicToolResult},
		{Role: "assistant", Parts: []string{"It shipped."}},
		{Role: "tool", ToolCallID: "toolu_2", Parts: []string{"Carrier: UPS."}, Origin: OriginAnthropicToolResult},
		{Role: "user", Parts: []string{"<context>Delivery takes 3 days.</context>", "When will it arrive?"}},
		{Role: "user"}, // an image-only turn is still a user turn
	}
	if !reflect.DeepEqual(r.Messages, want) {
		got, _ := json.MarshalIndent(r.Messages, "", " ")
		t.Errorf("messages\n%s", got)
	}
	if r.SystemText() != "You are a support bot.\n"+`<document id="refunds">30 days.</document>` {
		t.Errorf("system text %q", r.SystemText())
	}
	if !r.Messages[0].IsSystem() || r.Messages[1].IsSystem() || !(&Message{Role: "developer"}).IsSystem() {
		t.Error("IsSystem")
	}

	// A string system; no system; a malformed system.
	for body, wantSystem := range map[string]string{
		`{"system":"Be brief.","messages":[]}`:         "Be brief.",
		`{"messages":[{"role":"user","content":"x"}]}`: "",
		`{"system":42,"messages":[]}`:                  "",
		`{"system":"","messages":[]}`:                  "",
	} {
		r, err := ParseRequest(FormatAnthropicMessages, []byte(body))
		if err != nil || r.SystemText() != wantSystem {
			t.Errorf("%s: system %q err %v", body, r.SystemText(), err)
		}
	}
}

func TestParseRequestErrors(t *testing.T) {
	if _, err := ParseRequest("responses", []byte(`{}`)); err != ErrUnknownFormat {
		t.Errorf("err %v", err)
	}
	for _, f := range []string{FormatOpenAIChat, FormatOpenAICompletions, FormatAnthropicMessages} {
		if _, err := ParseRequest(f, []byte(`[]`)); err != ErrUnparseable {
			t.Errorf("%s: err %v", f, err)
		}
	}
	// The developer role is read like any other.
	r, _ := ParseChatRequest([]byte(`{"messages":[{"role":"developer","content":"Rules."},{"role":"user","content":"Q"}]}`))
	if r.Format != FormatOpenAIChat || r.SystemText() != "Rules." || r.LastUserText() != "Q" {
		t.Errorf("request %+v", r)
	}
	if (&ChatRequest{}).LastUserText() != "" {
		t.Error("no user message")
	}
}

func TestParseResponseFormats(t *testing.T) {
	r, err := ParseResponse(FormatOpenAICompletions, []byte(`{"id":"cmpl-1","object":"text_completion","created":5,"model":"m",
	  "choices":[{"text":" Paris.","index":0,"logprobs":null,"finish_reason":"stop"},{"text":"Lyon","index":1,"finish_reason":"length"}],
	  "usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Gated()
	if c.Text != " Paris." || c.FinishReason != "stop" || !r.MultiChoice() || r.Created != 5 || *r.Usage != (Usage{3, 2}) {
		t.Errorf("completions %+v %+v", r, c)
	}

	r, err = ParseResponse(FormatAnthropicMessages, []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude",
	  "content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"Refunds take "},{"type":"text","text":"30 days.","citations":[]},
	    {"type":"tool_use","id":"t","name":"n","input":{"q":"args are not gated"}}, 7],
	  "stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":4,"cache_read_input_tokens":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, _ = r.Gated()
	if c.Text != "Refunds take 30 days." || c.FinishReason != "tool_use" || !c.ToolCalls || r.MultiChoice() ||
		*r.Usage != (Usage{11, 4}) || r.ID != "msg_1" || r.Model != "claude" || r.Format != FormatAnthropicMessages {
		t.Errorf("anthropic %+v %+v", r, c)
	}

	for _, bad := range []string{`{"type":"error","error":{"type":"overloaded_error","message":"x"}}`, `{"content":"x"}`, `[]`,
		`{"type":"completion","content":[]}`} {
		if _, err := ParseResponse(FormatAnthropicMessages, []byte(bad)); err != ErrNotChatResponse {
			t.Errorf("%s: err %v", bad, err)
		}
	}
	if _, err := ParseResponse("responses", []byte(`{}`)); err != ErrUnknownFormat {
		t.Errorf("err %v", err)
	}
	// choices: null parses (as in S0) but leaves nothing to gate.
	if r, err := ParseResponse(FormatOpenAICompletions, []byte(`{"choices":null}`)); err != nil || len(r.Choices) != 0 {
		t.Errorf("null choices: %+v %v", r, err)
	}
}

func TestErrorBodies(t *testing.T) {
	if got := string(OpenAIError("upstream_error", "The upstream <LLM> failed.")); got !=
		`{"error":{"message":"The upstream <LLM> failed.","type":"trustagent_error","param":null,"code":"upstream_error"}}` {
		t.Errorf("openai %s", got)
	}
	if got := string(AnthropicError(AnthropicRateLimitError, "Slow down.")); got !=
		`{"type":"error","error":{"type":"rate_limit_error","message":"Slow down."}}` {
		t.Errorf("anthropic %s", got)
	}
	for status, want := range map[int]string{
		401: AnthropicAuthenticationError, 400: AnthropicInvalidRequestError, 403: AnthropicInvalidRequestError,
		413: AnthropicInvalidRequestError, 429: AnthropicRateLimitError, 500: AnthropicAPIError, 502: AnthropicAPIError,
		503: AnthropicOverloadedError, 504: AnthropicAPIError, 529: AnthropicOverloadedError,
	} {
		if got := AnthropicErrorType(status); got != want {
			t.Errorf("%d: %s, want %s", status, got, want)
		}
	}
	if got := string(ErrorBody(FormatAnthropicMessages, 401, "unauthorized", "Bad key.")); got !=
		`{"type":"error","error":{"type":"authentication_error","message":"Bad key."}}` {
		t.Errorf("anthropic route %s", got)
	}
	if got := string(ErrorBody(FormatOpenAICompletions, 413, "request_too_large", "Too big.")); got !=
		`{"error":{"message":"Too big.","type":"trustagent_error","param":null,"code":"request_too_large"}}` {
		t.Errorf("openai route %s", got)
	}
	trace := "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90"
	for format, want := range map[string]string{
		FormatOpenAIChat:        "chatcmpl-trustagent-0192f3a07c1e7b2a9d115b6c0e7f8a90",
		FormatOpenAICompletions: "cmpl-trustagent-0192f3a07c1e7b2a9d115b6c0e7f8a90",
		FormatAnthropicMessages: "msg_trustagent_0192f3a07c1e7b2a9d115b6c0e7f8a90",
	} {
		if got := SyntheticIDFor(format, trace); got != want {
			t.Errorf("%s: %s", format, got)
		}
	}
}

func TestSyntheticResponse(t *testing.T) {
	for _, format := range []string{FormatOpenAIChat, FormatOpenAICompletions, FormatAnthropicMessages} {
		body := SyntheticResponse(format, testTrace, "m", testCreated, "Scripted <answer> & more.")
		r, err := ParseResponse(format, body)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		c, _ := r.Gated()
		wantFinish := FinishReasonStop
		if IsAnthropic(format) {
			wantFinish = StopReasonEndTurn
		}
		if c.Text != "Scripted <answer> & more." || c.FinishReason != wantFinish || r.ID != SyntheticIDFor(format, testTrace) || r.Model != "m" {
			t.Errorf("%s: %s", format, body)
		}
	}
	if string(SyntheticResponse(FormatOpenAIChat, "t", "m", 1, "x")) != string(SyntheticChatCompletion("t", "m", 1, "x")) {
		t.Error("the chat answer is the S0 one")
	}
	if got := string(SyntheticResponse(FormatAnthropicMessages, testTrace, "claude", 1, "Hi")); got !=
		`{"id":"msg_trustagent_`+testTrace32+`","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}` {
		t.Errorf("anthropic %s", got)
	}
}
