package llmapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseChatRequestContentParts(t *testing.T) {
	body := []byte(`{
	  "model": "gpt-4o-mini",
	  "stream": false,
	  "messages": [
	    {"role": "developer", "content": [{"type": "text", "text": "<context>Premium support is 24/7.</context>"}]},
	    {"role": "user", "content": [
	      {"type": "text", "text": "Order #4411 shipped."},
	      {"type": "image_url", "image_url": {"url": "https://example.com/r.png"}},
	      {"type": "input_text", "text": "Where is my order?"}
	    ]},
	    {"role": "assistant", "content": null, "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "get_order", "arguments": "{}"}}]},
	    {"role": "tool", "tool_call_id": "call_1", "content": "Shipped on 2026-03-02."},
	    {"role": "function", "name": "get_weather", "content": "Sunny."},
	    "not a message",
	    {"role": "user", "content": 42}
	  ]
	}`)
	r, err := ParseChatRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "gpt-4o-mini" || r.Stream {
		t.Errorf("model %q stream %v", r.Model, r.Stream)
	}
	if len(r.Messages) != 6 {
		t.Fatalf("got %d messages, want 6 (the string entry is skipped)", len(r.Messages))
	}
	if got := r.Messages[0]; got.Role != "developer" || got.Text() != "<context>Premium support is 24/7.</context>" {
		t.Errorf("developer message %+v", got)
	}
	if got := r.Messages[1].Parts; !reflect.DeepEqual(got, []string{"Order #4411 shipped.", "Where is my order?"}) {
		t.Errorf("user parts %q: the image part must be ignored", got)
	}
	if got := r.Messages[2]; got.Parts != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0] != (ToolCall{ID: "call_1", Name: "get_order"}) {
		t.Errorf("assistant tool call message %+v", got)
	}
	if got := r.Messages[3]; got.ToolCallID != "call_1" || got.Text() != "Shipped on 2026-03-02." {
		t.Errorf("tool message %+v", got)
	}
	if got := r.Messages[4]; got.Name != "get_weather" {
		t.Errorf("function message %+v", got)
	}
	if got := r.Messages[5]; got.Parts != nil {
		t.Errorf("a numeric content has no text, got %q", got.Parts)
	}
}

func TestParseChatRequestTolerance(t *testing.T) {
	for _, body := range []string{`[]`, `"x"`, `null`, `{`, ``, `nope`} {
		if _, err := ParseChatRequest([]byte(body)); err != ErrUnparseable {
			t.Errorf("%q: err %v, want ErrUnparseable", body, err)
		}
	}
	// Malformed members are ignored, never fatal.
	r, err := ParseChatRequest([]byte(`{"model": 5, "stream": "yes", "messages": {"role": "user"}, "trustagent": "x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "" || r.Stream || r.Messages != nil || !r.Extension.Present || r.Extension.Context != nil {
		t.Errorf("tolerant parse %+v", r)
	}
}

func TestParseChatRequestExtensionAndTestResponse(t *testing.T) {
	r, err := ParseChatRequest([]byte(`{"messages": [], "test_response": "Scripted.",
	  "trustagent": {"route": "support-bot", "rules": {"brand_voice": "Calm.", "n": 3},
	    "context": [{"id": "sla", "text": "4 hours.", "source": "crm"}, {"text": "No id."}, 7]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.TestResponse == nil || *r.TestResponse != "Scripted." {
		t.Errorf("test_response %v", r.TestResponse)
	}
	want := Extension{
		Present: true, Route: "support-bot",
		Context: []ContextItem{{ID: "sla", Text: "4 hours.", Source: "crm"}, {Text: "No id."}},
		Rules:   map[string]string{"brand_voice": "Calm."},
	}
	if !reflect.DeepEqual(r.Extension, want) {
		t.Errorf("extension %+v, want %+v", r.Extension, want)
	}
	r, _ = ParseChatRequest([]byte(`{"test_response": null}`))
	if r.TestResponse != nil {
		t.Error("a null test_response counts as absent")
	}
}

func TestForwardBodyKeepsOtherMembersByteForByte(t *testing.T) {
	// Key order, spacing inside values, escapes and number spellings must survive: only the
	// gateway's members disappear.
	body := []byte("{\"model\" : \"gpt-4o-mini\",\n  \"messages\":[{\"role\":\"system\",\"content\":\"<document id=\\\"a\\\">x &amp; y</document>\"}],\n" +
		"  \"trustagent\": {\"context\": [{\"text\": \"t\"}]},\n  \"temperature\": 0.20,\n  \"test_response\": \"z\", \"n\":1e0 }")
	got, err := ForwardBody(body)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"model\" : \"gpt-4o-mini\",\"messages\":[{\"role\":\"system\",\"content\":\"<document id=\\\"a\\\">x &amp; y</document>\"}]," +
		"\"temperature\": 0.20,\"n\":1e0}"
	if string(got) != want {
		t.Errorf("forwarded\n%s\nwant\n%s", got, want)
	}
	var v map[string]any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("forwarded body is not valid JSON: %v", err)
	}
}

func TestForwardBodyWithoutGatewayMembersIsUnchanged(t *testing.T) {
	body := []byte("  {\"model\":\"m\", \"messages\":[]}\n")
	got, err := ForwardBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("got %q, want the original bytes", got)
	}
}

func TestStripMembersDuplicatesAndErrors(t *testing.T) {
	got, err := StripMembers([]byte(`{"trustagent":1,"a":2,"trustagent":{"x":[1,2]}}`), "trustagent")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":2}` {
		t.Errorf("got %s", got)
	}
	if got, _ := StripMembers([]byte(`{"trustagent":1}`), "trustagent"); string(got) != `{}` {
		t.Errorf("got %s, want {}", got)
	}
	for _, bad := range []string{`[1]`, `{"a":`, `{"a" 1}`} {
		if _, err := StripMembers([]byte(bad), "a"); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}

func TestParseChatResponse(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o-mini-2024",
	  "choices":[
	    {"index":1,"message":{"role":"assistant","content":"second"},"finish_reason":"stop"},
	    {"index":0,"message":{"role":"assistant","content":"Paris.","reasoning_content":"hidden"},"finish_reason":"stop"}
	  ],
	  "usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`)
	r, err := ParseChatResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := r.Gated()
	if !ok || c.Text != "Paris." || c.Index != 0 {
		t.Errorf("gated choice %+v: want choice index 0 without reasoning", c)
	}
	if r.Model != "gpt-4o-mini-2024" || r.Usage == nil || r.Usage.PromptTokens != 12 || r.Usage.CompletionTokens != 3 {
		t.Errorf("response %+v usage %+v", r, r.Usage)
	}

	r, err = ParseChatResponse([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c"}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := r.Gated(); c.Text != "" || !c.ToolCalls {
		t.Errorf("tool-call-only choice %+v", c)
	}
	for _, bad := range []string{`{}`, `{"choices":{}}`, `[]`, `x`} {
		if _, err := ParseChatResponse([]byte(bad)); err != ErrNotChatResponse {
			t.Errorf("%s: err %v", bad, err)
		}
	}
	if _, ok := (&ChatResponse{}).Gated(); ok {
		t.Error("no choices: nothing to gate")
	}
}

func TestOpenAIError(t *testing.T) {
	var v map[string]map[string]any
	if err := json.Unmarshal(OpenAIError("test_mode_disabled", "Test mode is off."), &v); err != nil {
		t.Fatal(err)
	}
	e := v["error"]
	if e["code"] != "test_mode_disabled" || e["type"] != "trustagent_error" || e["message"] != "Test mode is off." {
		t.Errorf("error body %v", e)
	}
	if p, ok := e["param"]; !ok || p != nil {
		t.Errorf("param must be present and null, got %v", p)
	}
}

func TestSyntheticChatCompletion(t *testing.T) {
	trace := "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90"
	body := SyntheticChatCompletion(trace, "gpt-4o-mini", 1700000000, "Scripted <answer> & more.")
	r, err := ParseChatResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Gated()
	if c.Text != "Scripted <answer> & more." || c.FinishReason != "stop" {
		t.Errorf("choice %+v", c)
	}
	if r.ID != "chatcmpl-trustagent-0192f3a07c1e7b2a9d115b6c0e7f8a90" || !strings.HasPrefix(r.ID, SyntheticIDPrefix) {
		t.Errorf("id %q", r.ID)
	}
}

func TestGeneratedBodiesAreNotHTMLEscaped(t *testing.T) {
	body := string(SyntheticChatCompletion("t", "m", 1, "a <b> & c"))
	if !strings.Contains(body, `"a <b> & c"`) || strings.HasSuffix(body, "\n") {
		t.Errorf("body %q", body)
	}
	if e := string(OpenAIError("c", "<x>")); !strings.Contains(e, `"<x>"`) {
		t.Errorf("error body %q", e)
	}
}
