package llmapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

var testEvidence = []EvidenceLine{
	{ID: "refund-policy.md", Text: "Annual plans: refund within 30 days of purchase."},
	{ID: "tool1", Text: "  Order 4411 shipped on 2026-03-02.\n"},
}

const wantEvidencePrompt = DefaultFallbackSystemPrompt + "\n\nCONTEXT:\n<context>\n" +
	"[refund-policy.md] Annual plans: refund within 30 days of purchase.\n" +
	"[tool1] Order 4411 shipped on 2026-03-02." +
	"\n</context>"

func TestFallbackPrompt(t *testing.T) {
	prompt, ids, truncated := FallbackPrompt(Fallback{Evidence: testEvidence})
	if prompt != wantEvidencePrompt || !reflect.DeepEqual(ids, []string{"refund-policy.md", "tool1"}) || truncated {
		t.Errorf("prompt %q ids %v truncated %v", prompt, ids, truncated)
	}
	// Without evidence the prompt asks for caution instead (SPEC §2.12).
	prompt, ids, _ = FallbackPrompt(Fallback{SystemPrompt: "Answer from CONTEXT only.", Evidence: []EvidenceLine{{ID: "x", Text: "  "}}})
	if prompt != "Answer from CONTEXT only. If you are not certain, say so; do not include specific numbers, dates, URLs or quotations." || ids != nil {
		t.Errorf("no-evidence prompt %q ids %v", prompt, ids)
	}
	// Items without an id are numbered by position.
	prompt, ids, _ = FallbackPrompt(Fallback{Evidence: []EvidenceLine{{Text: "a"}, {ID: "b", Text: "b"}, {Text: "c"}}})
	if !strings.Contains(prompt, "<context>\n[1] a\n[b] b\n[3] c\n</context>") || !reflect.DeepEqual(ids, []string{"1", "b", "3"}) {
		t.Errorf("numbered %q %v", prompt, ids)
	}
}

func TestFallbackEvidenceBudget(t *testing.T) {
	long := strings.Repeat("word ", 100) // 500 characters
	f := Fallback{
		Evidence:  []EvidenceLine{{ID: "a", Text: "short fact"}, {ID: "b", Text: long}, {ID: "c", Text: "never packed"}},
		MaxTokens: 50,
	}
	prompt, ids, truncated := FallbackPrompt(f)
	if !truncated || !reflect.DeepEqual(ids, []string{"a", "b"}) || strings.Contains(prompt, "never packed") {
		t.Fatalf("ids %v truncated %v", ids, truncated)
	}
	body := prompt[strings.Index(prompt, "<context>\n")+len("<context>\n") : strings.Index(prompt, "\n</context>")]
	used := 0
	for _, line := range strings.Split(body, "\n") {
		used += estimateTokens(line)
	}
	if used > 50 || used < 45 {
		t.Errorf("packed %d tokens of a 50-token budget:\n%s", used, body)
	}

	// Wide characters count one token each; a cut never splits a character.
	wide := Fallback{Evidence: []EvidenceLine{{ID: "zh", Text: strings.Repeat("退款期限为三十天。", 20)}}, MaxTokens: 30}
	prompt, _, truncated = FallbackPrompt(wide)
	if !truncated || !utf8.ValidString(prompt) {
		t.Errorf("wide cut %q", prompt)
	}
	if n := estimateTokens(strings.Split(strings.Split(prompt, "<context>\n")[1], "\n</context>")[0]); n > 30 {
		t.Errorf("wide line uses %d tokens", n)
	}

	// A budget too small for any text packs nothing (and says so).
	prompt, ids, truncated = FallbackPrompt(Fallback{Evidence: testEvidence, MaxTokens: 1})
	if ids != nil || !truncated || !strings.HasSuffix(prompt, FallbackNoEvidenceSuffix) {
		t.Errorf("tiny budget %q %v %v", prompt, ids, truncated)
	}

	// A custom estimate (the gate's runtime cpt) is used as given.
	calls := 0
	_, ids, _ = FallbackPrompt(Fallback{Evidence: testEvidence, CountTokens: func(s string) int { calls++; return 1 }, MaxTokens: 2})
	if calls == 0 || len(ids) != 2 {
		t.Errorf("custom counter: %d calls, ids %v", calls, ids)
	}
	if estimateTokens("abcd") != 1 || estimateTokens("abcde") != 2 || estimateTokens("退款") != 2 || estimateTokens("") != 0 {
		t.Error("estimateTokens")
	}
}

func TestBuildFallbackRequestChat(t *testing.T) {
	original := `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"<document id=\"refund-policy.md\">Annual plans: refund within 30 days of purchase.</document>"},` +
		`{"role":"user","content":"Can I get a refund after 60 days?"}],"stream":true,"stream_options":{"include_usage":true},` +
		`"temperature":0.20,"trustagent":{"route":"payments-faq"},"n":1}`
	req, err := BuildFallbackRequest(FormatOpenAIChat, []byte(original), Fallback{Model: "gpt-4o", Evidence: testEvidence[:1]})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "fallback_chat_request.json", req.Body)
	prompt := DefaultFallbackSystemPrompt + "\n\nCONTEXT:\n<context>\n[refund-policy.md] Annual plans: refund within 30 days of purchase.\n</context>"
	want := `{"model":"gpt-4o","messages":[` +
		`{"role":"system","content":` + string(marshal(prompt)) + `},` +
		`{"role":"system","content":"<document id=\"refund-policy.md\">Annual plans: refund within 30 days of purchase.</document>"},` +
		`{"role":"user","content":"Can I get a refund after 60 days?"}],"stream":false,"temperature":0.20,"n":1}`
	if string(req.Body) != want {
		t.Errorf("fallback request\n got %s\nwant %s", req.Body, want)
	}
	if req.Model != "gpt-4o" || req.Prompt != prompt || !reflect.DeepEqual(req.PackedIDs, []string{"refund-policy.md"}) || req.Truncated {
		t.Errorf("request %+v", req)
	}
	parsed, err := ParseRequest(FormatOpenAIChat, req.Body)
	if err != nil || parsed.Stream || parsed.IncludeUsage || parsed.Extension.Present || parsed.Messages[0].Text() != prompt {
		t.Errorf("parsed back %+v %v", parsed, err)
	}

	// No fallback.model keeps the original; a body without stream gets stream:false.
	req, err = BuildFallbackRequest(FormatOpenAIChat, []byte(`{"messages":[],"model":"m","test_response":"x"}`), Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := DefaultFallbackSystemPrompt + FallbackNoEvidenceSuffix
	if string(req.Body) != `{"messages":[{"role":"system","content":`+string(marshal(wantPrompt))+`}],"model":"m","stream":false}` || req.Model != "m" {
		t.Errorf("no-evidence request %s model %q", req.Body, req.Model)
	}
}

func TestBuildFallbackRequestAnthropic(t *testing.T) {
	prompt := wantEvidencePrompt
	cases := []struct {
		name, system, want string
	}{
		{"no system", ``, string(marshal(prompt))},
		{"null system", `"system":null,`, string(marshal(prompt))},
		{"empty system", `"system":"",`, string(marshal(prompt))},
		{"string system", `"system":"You are a support bot.",`, string(marshal(prompt + "\n\nYou are a support bot."))},
		{"block system", `"system":[{"type":"text","text":"You are a support bot.","cache_control":{"type":"ephemeral"}}],`,
			`[{"type":"text","text":` + string(marshal(prompt)) + `},{"type":"text","text":"You are a support bot.","cache_control":{"type":"ephemeral"}}]`},
	}
	for _, tc := range cases {
		original := `{"model":"claude-sonnet-4-5","max_tokens":1024,` + tc.system + `"messages":[{"role":"user","content":"Refund after 60 days?"}],"stream":true}`
		req, err := BuildFallbackRequest(FormatAnthropicMessages, []byte(original), Fallback{Evidence: testEvidence})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(body["system"]) != tc.want || string(body["stream"]) != "false" || req.Model != "claude-sonnet-4-5" ||
			string(body["messages"]) != `[{"role":"user","content":"Refund after 60 days?"}]` || string(body["max_tokens"]) != "1024" {
			t.Errorf("%s: %s", tc.name, req.Body)
		}
		if tc.name == "string system" {
			golden(t, "fallback_anthropic_request.json", req.Body)
		}
		parsed, _ := ParseRequest(FormatAnthropicMessages, req.Body)
		if !strings.HasPrefix(parsed.SystemText(), prompt) {
			t.Errorf("%s: system %q", tc.name, parsed.SystemText())
		}
	}
	if _, err := BuildFallbackRequest(FormatAnthropicMessages, []byte(`{"system":42}`), Fallback{}); !errors.Is(err, ErrFallbackRequest) {
		t.Errorf("err %v", err)
	}
}

func TestBuildFallbackRequestCompletions(t *testing.T) {
	prompt := DefaultFallbackSystemPrompt + FallbackNoEvidenceSuffix
	req, err := BuildFallbackRequest(FormatOpenAICompletions, []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"Capital of France?","stream":true,"max_tokens":20}`), Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "fallback_completions_request.json", req.Body)
	if string(req.Body) != `{"model":"gpt-3.5-turbo-instruct","prompt":`+string(marshal(prompt+"\n\nCapital of France?"))+`,"stream":false,"max_tokens":20}` {
		t.Errorf("completions %s", req.Body)
	}
	req, err = BuildFallbackRequest(FormatOpenAICompletions, []byte(`{"prompt":["a","b"]}`), Fallback{})
	if err != nil {
		t.Fatal(err)
	}
	if string(req.Body) != `{"prompt":[`+string(marshal(prompt+"\n\na"))+`,`+string(marshal(prompt+"\n\nb"))+`],"stream":false}` {
		t.Errorf("prompt array %s", req.Body)
	}
	for _, bad := range []string{`{"prompt":[1,2]}`, `{"prompt":[["a"]]}`, `{"prompt":[]}`, `{"prompt":5}`, `{}`, `{"prompt":["a",1]}`} {
		if _, err := BuildFallbackRequest(FormatOpenAICompletions, []byte(bad), Fallback{}); !errors.Is(err, ErrFallbackRequest) {
			t.Errorf("%s: err %v", bad, err)
		}
	}
}

func TestBuildFallbackRequestErrors(t *testing.T) {
	for _, bad := range []string{`[]`, `nope`, ``, `{"a":`} {
		if _, err := BuildFallbackRequest(FormatOpenAIChat, []byte(bad), Fallback{}); !errors.Is(err, ErrUnparseable) {
			t.Errorf("%q: err %v", bad, err)
		}
	}
	for _, bad := range []string{`{}`, `{"messages":{}}`, `{"messages":null}`} {
		if _, err := BuildFallbackRequest(FormatOpenAIChat, []byte(bad), Fallback{}); !errors.Is(err, ErrFallbackRequest) {
			t.Errorf("%s: err %v", bad, err)
		}
	}
	if _, err := BuildFallbackRequest("direct", []byte(`{}`), Fallback{}); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("err %v", err)
	}
}
