package llmapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

// golden compares got with testdata/golden/name (or rewrites it with -update).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run %s -update to create it)", err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file\n got: %s\nwant: %s", name, got, want)
	}
}

const (
	testTrace   = "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90"
	testTrace32 = "0192f3a07c1e7b2a9d115b6c0e7f8a90"
	testCreated = 1760000000
)

// spec fills the <placeholders> of a body copied from SPEC §2.12.
func spec(template string, values map[string]string) string {
	for k, v := range values {
		template = strings.ReplaceAll(template, "<"+k+">", v)
	}
	return template
}

// upstreamChat is an upstream chat completion with everything a block must remove or keep: two
// choices, tool calls, a refusal, logprobs, usage and vendor members.
const upstreamChat = `{"id":"chatcmpl-B9x1","object":"chat.completion","created":1760000000,"model":"gpt-4o-2024-08-06",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"Refunds within 90 days.","refusal":null,` +
	`"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
	`"logprobs":{"content":[{"token":"Ref","logprob":-0.1}]},"finish_reason":"stop"},` +
	`{"index":1,"message":{"role":"assistant","content":null,"refusal":"No."},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":41,"completion_tokens":9,"total_tokens":50},"system_fingerprint":"fp_1",` +
	`"service_tier":"default","note":"<a&b>"}`

const upstreamCompletion = `{"id":"cmpl-7","object":"text_completion","created":1760000300,"model":"gpt-3.5-turbo-instruct",` +
	`"choices":[{"text":"Refunds within 90 days.","index":0,"logprobs":{"tokens":["Ref"]},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":6,"completion_tokens":5,"total_tokens":11}}`

const upstreamMessage = `{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5",` +
	`"content":[{"type":"text","text":"Refunds within 90 days."}],"stop_reason":"end_turn","stop_sequence":null,` +
	`"usage":{"input_tokens":472,"output_tokens":15}}`

func TestBlockJSONMessageStyle(t *testing.T) {
	msg := "I can't confirm that from our policy documents. A person will follow up."
	b := Block{Message: msg}

	status, body := BlockJSON(FormatOpenAIChat, b, BlockTarget{TraceID: testTrace, Upstream: []byte(upstreamChat)})
	if status != http.StatusOK {
		t.Errorf("status %d", status)
	}
	golden(t, "block_chat_message.json", body)
	// Every member but choices keeps its bytes; every choice is replaced (n>1 included).
	want := strings.Replace(upstreamChat, upstreamChat[strings.Index(upstreamChat, `"choices":`):strings.Index(upstreamChat, `,"usage"`)],
		`"choices":[{"index":0,"message":{"role":"assistant","content":"`+msg+`"},"logprobs":null,"finish_reason":"content_filter"},`+
			`{"index":1,"message":{"role":"assistant","content":"`+msg+`"},"finish_reason":"content_filter"}]`, 1)
	if string(body) != want {
		t.Errorf("patched chat\n got %s\nwant %s", body, want)
	}
	r, err := ParseChatResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Choices {
		if c.Text != msg || c.FinishReason != FinishReasonContentFilter || c.ToolCalls || c.Refusal != "" {
			t.Errorf("choice %+v", c)
		}
	}

	status, body = BlockJSON(FormatOpenAICompletions, b, BlockTarget{TraceID: testTrace, Upstream: []byte(upstreamCompletion)})
	golden(t, "block_completions_message.json", body)
	if status != http.StatusOK || !strings.Contains(string(body), `"choices":[{"text":"`+msg+`","index":0,"logprobs":null,"finish_reason":"content_filter"}]`) {
		t.Errorf("completions %d %s", status, body)
	}

	// Anthropic, exactly as SPEC §2.12 writes it.
	status, body = BlockJSON(FormatAnthropicMessages, b, BlockTarget{TraceID: testTrace, Upstream: []byte(upstreamMessage)})
	golden(t, "block_anthropic_message.json", body)
	wantAnthropic := spec(`{"id":"msg_trustagent_<trace32>","type":"message","role":"assistant","model":"<model>","content":[{"type":"text","text":"<msg>"}],"stop_reason":"<refusal|end_turn>","stop_sequence":null,"usage":{"input_tokens":<n|0>,"output_tokens":0}}`,
		map[string]string{"trace32": testTrace32, "model": "claude-sonnet-4-5", "msg": msg, "refusal|end_turn": "refusal", "n|0": "472"})
	if status != http.StatusOK || string(body) != wantAnthropic {
		t.Errorf("anthropic %d\n got %s\nwant %s", status, body, wantAnthropic)
	}
	// anthropic_stop_reason end_turn; no upstream answer (model from the request, 0 input tokens).
	_, body = BlockJSON(FormatAnthropicMessages, Block{AnthropicStopReason: "end_turn"}, BlockTarget{TraceID: testTrace, Model: "claude-haiku-4-5"})
	wantAnthropic = spec(`{"id":"msg_trustagent_<trace32>","type":"message","role":"assistant","model":"<model>","content":[{"type":"text","text":"<msg>"}],"stop_reason":"<refusal|end_turn>","stop_sequence":null,"usage":{"input_tokens":<n|0>,"output_tokens":0}}`,
		map[string]string{"trace32": testTrace32, "model": "claude-haiku-4-5", "msg": DefaultBlockMessage, "refusal|end_turn": "end_turn", "n|0": "0"})
	if string(body) != wantAnthropic {
		t.Errorf("anthropic end_turn\n got %s\nwant %s", body, wantAnthropic)
	}
}

func TestBlockJSONBuildsWhenTheUpstreamAnswerIsUnusable(t *testing.T) {
	for _, upstream := range []string{"", "not json", `{"error":{"message":"x"}}`, `{"choices":null}`, `[1]`} {
		status, body := BlockJSON(FormatOpenAIChat, Block{}, BlockTarget{TraceID: testTrace, Model: "gpt-4o-mini", Created: testCreated, Upstream: []byte(upstream)})
		want := `{"id":"chatcmpl-trustagent-` + testTrace32 + `","object":"chat.completion","created":1760000000,"model":"gpt-4o-mini",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"` + DefaultBlockMessage + `"},"finish_reason":"content_filter"}]}`
		if status != http.StatusOK || string(body) != want {
			t.Errorf("%q: %d %s", upstream, status, body)
		}
	}
	_, body := BlockJSON(FormatOpenAICompletions, Block{}, BlockTarget{TraceID: testTrace, Model: "m", Created: testCreated})
	golden(t, "block_completions_built.json", body)

	// An empty choices array gets the one choice; a choice that is not an object is replaced.
	_, body = BlockJSON(FormatOpenAIChat, Block{Message: "x"}, BlockTarget{Upstream: []byte(`{"id":"a","choices":[]}`)})
	if string(body) != `{"id":"a","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"content_filter"}]}` {
		t.Errorf("empty choices %s", body)
	}
	_, body = BlockJSON(FormatOpenAIChat, Block{Message: "x"}, BlockTarget{Upstream: []byte(`{"choices":[null, {"index":1,"finish_reason":"stop","message":{}}], "choices_note": 1}`)})
	if string(body) != `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"content_filter"},{"index":1,"finish_reason":"content_filter","message":{"role":"assistant","content":"x"}}],"choices_note": 1}` {
		t.Errorf("odd choices %s", body)
	}
}

func TestBlockJSONErrorStyle(t *testing.T) {
	msg := "Blocked by policy."
	for _, tc := range []struct{ configured, want int }{
		{0, 422}, {400, 400}, {403, 403}, {409, 409}, {422, 422}, {451, 451}, {429, 422}, {500, 422}, {503, 422}, {200, 422}, {404, 422},
	} {
		status, body := BlockJSON(FormatOpenAIChat, Block{Style: BlockStyleError, Message: msg, Status: tc.configured}, BlockTarget{TraceID: testTrace})
		want := spec(`{"error":{"message":"<msg>","type":"trustagent_policy_block","param":null,"code":"trustagent_blocked"}}`, map[string]string{"msg": msg})
		if status != tc.want || string(body) != want {
			t.Errorf("status %d: got %d %s", tc.configured, status, body)
		}
	}
	status, body := BlockJSON(FormatOpenAICompletions, Block{Style: BlockStyleError, Status: 451}, BlockTarget{})
	golden(t, "block_openai_error.json", body)
	if status != 451 {
		t.Errorf("completions status %d", status)
	}
	// Anthropic error style is always a 400 invalid_request_error (SPEC §2.12).
	status, body = BlockJSON(FormatAnthropicMessages, Block{Style: BlockStyleError, Message: msg, Status: 403}, BlockTarget{})
	golden(t, "block_anthropic_error.json", body)
	want := spec(`{"type":"error","error":{"type":"invalid_request_error","message":"<msg>"}}`, map[string]string{"msg": msg})
	if status != http.StatusBadRequest || string(body) != want {
		t.Errorf("anthropic error %d %s", status, body)
	}
	if status, _ := BlockJSON("direct", Block{}, BlockTarget{}); status != http.StatusInternalServerError {
		t.Errorf("unknown format status %d", status)
	}
}

// TestBlockText: the text a block delivers (the trace's delivered text) is the route's message, or
// the default when the route sets none, in either style.
func TestBlockText(t *testing.T) {
	for _, tc := range []struct {
		b    Block
		want string
	}{
		{Block{}, DefaultBlockMessage},
		{Block{Style: BlockStyleError, Status: 451}, DefaultBlockMessage},
		{Block{Message: "Held back."}, "Held back."},
		{Block{Style: BlockStyleError, Message: "Held back."}, "Held back."},
	} {
		if got := tc.b.Text(); got != tc.want {
			t.Errorf("%+v: Text() = %q, want %q", tc.b, got, tc.want)
		}
	}
}

func TestBlockStream(t *testing.T) {
	msg := "I can't confirm that."
	b := Block{Message: msg, Style: BlockStyleError} // streams are always message style
	usage := &Usage{PromptTokens: 41, CompletionTokens: 9}

	// OpenAI chat, as SPEC §2.12 writes it: the upstream id, then the usage chunk only with
	// stream_options.include_usage, then [DONE].
	events := BlockStream(FormatOpenAIChat, b, BlockTarget{
		TraceID: testTrace, Model: "gpt-4o-mini", Created: 1,
		Response:     &ChatResponse{ID: "chatcmpl-up", Created: testCreated, Model: "gpt-4o-mini-2024-07-18", Usage: usage, RawUsage: json.RawMessage(`{"prompt_tokens": 41, "completion_tokens": 9, "total_tokens": 50, "cost": 0.0001}`)},
		IncludeUsage: true,
	})
	got := EncodeEvents(events)
	golden(t, "block_chat_stream.sse", got)
	values := map[string]string{"id": "chatcmpl-up", "unix": "1760000000", "model": "gpt-4o-mini-2024-07-18", "msg": msg}
	want := spec(`data: {"id":"<id>","object":"chat.completion.chunk","created":<unix>,"model":"<model>","choices":[{"index":0,"delta":{"role":"assistant","content":"<msg>"},"finish_reason":null}]}`+"\n\n"+
		`data: {"id":"<id>","object":"chat.completion.chunk","created":<unix>,"model":"<model>","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`+"\n\n"+
		`data: {"id":"<id>","object":"chat.completion.chunk","created":<unix>,"model":"<model>","choices":[],"usage":{"prompt_tokens":41,"completion_tokens":9,"total_tokens":50,"cost":0.0001}}`+"\n\n"+
		"data: [DONE]\n\n", values)
	if string(got) != want {
		t.Errorf("chat block stream\n got %s\nwant %s", got, want)
	}

	// Without include_usage, and with no upstream id: chatcmpl-trustagent-<trace32>.
	got = EncodeEvents(BlockStream(FormatOpenAIChat, Block{Message: msg}, BlockTarget{TraceID: testTrace, Model: "gpt-4o-mini", Created: testCreated}))
	values["id"], values["model"] = "chatcmpl-trustagent-"+testTrace32, "gpt-4o-mini"
	want = spec(`data: {"id":"<id>","object":"chat.completion.chunk","created":<unix>,"model":"<model>","choices":[{"index":0,"delta":{"role":"assistant","content":"<msg>"},"finish_reason":null}]}`+"\n\n"+
		`data: {"id":"<id>","object":"chat.completion.chunk","created":<unix>,"model":"<model>","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`+"\n\n"+
		"data: [DONE]\n\n", values)
	if string(got) != want {
		t.Errorf("chat block stream without usage\n got %s\nwant %s", got, want)
	}

	got = EncodeEvents(BlockStream(FormatOpenAICompletions, Block{Message: msg}, BlockTarget{
		TraceID: testTrace, Model: "gpt-3.5-turbo-instruct", Created: testCreated, IncludeUsage: true, Response: &ChatResponse{Usage: &Usage{6, 5}},
	}))
	golden(t, "block_completions_stream.sse", got)

	// Anthropic, as SPEC §2.12 lists it: message_start → content_block_start → content_block_delta
	// → content_block_stop → message_delta → message_stop.
	events = BlockStream(FormatAnthropicMessages, Block{Message: msg}, BlockTarget{
		TraceID: testTrace, Model: "claude-sonnet-4-5", Response: &ChatResponse{ID: "msg_up", Usage: &Usage{472, 15}},
	})
	got = EncodeEvents(events)
	golden(t, "block_anthropic_stream.sse", got)
	var types []string
	for _, ev := range events {
		types = append(types, ev.Name)
	}
	if strings.Join(types, " ") != "message_start content_block_start content_block_delta content_block_stop message_delta message_stop" {
		t.Errorf("event order %v", types)
	}
	checks := map[int]string{
		0: `{"type":"message_start","message":{"id":"msg_trustagent_` + testTrace32 + `","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":472,"output_tokens":0}}}`,
		1: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		2: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + msg + `"}}`,
		3: `{"type":"content_block_stop","index":0}`,
		4: `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":0}}`,
		5: `{"type":"message_stop"}`,
	}
	for i, want := range checks {
		if string(events[i].Data) != want {
			t.Errorf("anthropic event %d\n got %s\nwant %s", i, events[i].Data, want)
		}
	}
}

func TestBuiltStreamsParseBack(t *testing.T) {
	// Every stream the gateway writes reads back, with the same reader and assembler, as the text
	// and finish reason it was built from.
	text := "Line one.\nLine \"two\" <b>&</b> caf\u00e9 \U0001F600 \u2028."
	for _, format := range []string{FormatOpenAIChat, FormatOpenAICompletions, FormatAnthropicMessages} {
		stop, blocked := FinishReasonStop, FinishReasonContentFilter
		if IsAnthropic(format) {
			stop, blocked = StopReasonEndTurn, StopReasonRefusal
		}
		for _, tc := range []struct {
			events []Event
			finish string
		}{
			{SynthesizeStream(format, SynthStream{TraceID: testTrace, Model: "m", Text: text, IncludeUsage: true, Usage: &Usage{3, 4}}), stop},
			{BlockStream(format, Block{Message: text}, BlockTarget{TraceID: testTrace, Model: "m"}), blocked},
			{SynthesizeStream(format, SynthStream{TraceID: testTrace, Text: "", FinishReason: "length"}), "length"},
		} {
			for _, variant := range lineEndings(EncodeEvents(tc.events)) {
				asm, kinds := assemble(t, format, variant)
				c, ok := asm.Response().Gated()
				want := text
				if tc.finish == "length" {
					want = ""
				}
				if !ok || c.Text != want || c.FinishReason != tc.finish || !asm.Terminated() || !asm.Finished() || asm.Malformed() != 0 {
					t.Errorf("%s: %+v terminated %v kinds %v", format, c, asm.Terminated(), kinds)
				}
			}
		}
	}
	// The usage chunk carries the usage object.
	asm, _ := assemble(t, FormatOpenAIChat, EncodeEvents(SynthesizeStream(FormatOpenAIChat, SynthStream{IncludeUsage: true, Usage: &Usage{3, 4}})))
	if r := asm.Response(); *r.Usage != (Usage{3, 4}) || string(r.RawUsage) != `{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}` {
		t.Errorf("usage %+v %s", r.Usage, r.RawUsage)
	}
	asm, _ = assemble(t, FormatAnthropicMessages, EncodeEvents(SynthesizeStream(FormatAnthropicMessages, SynthStream{Usage: &Usage{3, 4}})))
	if r := asm.Response(); *r.Usage != (Usage{3, 4}) {
		t.Errorf("anthropic usage %+v", r.Usage)
	}
}

func TestSynthesizedFallbackStreams(t *testing.T) {
	// A fallback answer delivered to a buffered stream: finish_reason stop / stop_reason end_turn.
	resp, err := ParseChatResponse([]byte(`{"id":"chatcmpl-fb","object":"chat.completion","created":1760000500,"model":"gpt-4o",
	  "choices":[{"index":0,"message":{"role":"assistant","content":"I don't have that information."},"finish_reason":"stop"}],
	  "usage":{"prompt_tokens":300,"completion_tokens":7,"total_tokens":307}}`))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := resp.Gated()
	built := EncodeEvents(SynthesizeStream(FormatOpenAIChat, SynthStream{
		ID: resp.ID, Created: resp.Created, Model: resp.Model, Text: c.Text, Usage: resp.Usage, RawUsage: resp.RawUsage, IncludeUsage: true,
	}))
	golden(t, "synth_chat_stream.sse", built)
	if got := EncodeEvents(StreamFromResponse(FormatOpenAIChat, resp, testTrace, 1, true)); string(got) != string(built) {
		t.Errorf("StreamFromResponse\n got %s\nwant %s", got, built)
	}
	anthropic, err := ParseResponse(FormatAnthropicMessages, []byte(upstreamMessage))
	if err != nil {
		t.Fatal(err)
	}
	evs := StreamFromResponse(FormatAnthropicMessages, anthropic, testTrace, testCreated, false)
	asm, _ := assemble(t, FormatAnthropicMessages, EncodeEvents(evs))
	if r := asm.Response(); r.ID != "msg_01" || r.Choices[0].Text != "Refunds within 90 days." || r.Choices[0].FinishReason != StopReasonEndTurn || *r.Usage != (Usage{472, 15}) {
		t.Errorf("anthropic re-emitted %+v", r)
	}
	// Nothing delivered: an empty stream with the synthetic id.
	evs = StreamFromResponse(FormatOpenAICompletions, nil, testTrace, testCreated, false)
	if len(evs) != 3 || !strings.Contains(string(evs[0].Data), `"id":"cmpl-trustagent-`+testTrace32+`","object":"text_completion","created":1760000000`) {
		t.Errorf("empty re-emit %s", EncodeEvents(evs))
	}
	golden(t, "synth_anthropic_stream.sse", EncodeEvents(SynthesizeStream(FormatAnthropicMessages, SynthStream{
		TraceID: testTrace, Model: "claude-sonnet-4-5", Text: "I don't have that information.", Usage: &Usage{300, 7},
	})))
	golden(t, "synth_completions_stream.sse", EncodeEvents(SynthesizeStream(FormatOpenAICompletions, SynthStream{
		TraceID: testTrace, Created: testCreated, Model: "gpt-3.5-turbo-instruct", Text: "I don't have that information.",
	})))
	// A raw usage object that is not valid JSON is not trusted.
	evs = SynthesizeStream(FormatOpenAIChat, SynthStream{IncludeUsage: true, RawUsage: json.RawMessage(`{"x":`)})
	if !strings.Contains(string(evs[2].Data), `"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`) {
		t.Errorf("usage chunk %s", evs[2].Data)
	}
}
