package llmapi

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "streams", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assemble reads src as a stream of format and returns the assembler and the kind of every event.
func assemble(t *testing.T, format string, src []byte) (*StreamAssembler, []EventKind) {
	t.Helper()
	asm, err := NewStreamAssembler(format)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	for _, ev := range readAll(t, NewReader(bytes.NewReader(src))) {
		kinds = append(kinds, asm.Add(ev))
	}
	return asm, kinds
}

func TestStreamAssemblerFixtures(t *testing.T) {
	type choice struct {
		Index        int
		Text         string
		FinishReason string
		ToolCalls    bool
	}
	cases := []struct {
		file, format string
		id, model    string
		created      int64
		choices      []choice
		usage        *Usage
		kinds        []EventKind
		finished     bool
		terminated   bool
		errType      string
	}{
		{
			file: "openai_chat_basic.sse", format: FormatOpenAIChat, id: "chatcmpl-B9x1", model: "gpt-4o-mini-2024-07-18", created: 1760000000,
			choices:  []choice{{0, "Refunds are available within 30 days \u2014 <b>no</b> exceptions.", "stop", false}},
			usage:    &Usage{PromptTokens: 41, CompletionTokens: 9},
			kinds:    []EventKind{EventDelta, EventDelta, EventDelta, EventDelta, EventFinish, EventDelta, EventTerminal},
			finished: true, terminated: true,
		},
		{
			// Reasoning deltas and tool-call arguments never reach the gated text.
			file: "openai_chat_tools_reasoning.sse", format: FormatOpenAIChat, id: "gen-17", model: "deepseek/deepseek-r1", created: 1760000100,
			choices:  []choice{{0, "Let me check order 4411.", "tool_calls", true}},
			usage:    &Usage{PromptTokens: 120, CompletionTokens: 33},
			kinds:    []EventKind{EventOther, EventOther, EventDelta, EventDelta, EventDelta, EventDelta, EventDelta, EventFinish, EventTerminal},
			finished: true, terminated: true,
		},
		{
			// n=2: choice 1 finishing first does not finish the gated choice.
			file: "openai_chat_n2.sse", format: FormatOpenAIChat, id: "chatcmpl-n2", model: "gpt-4o-mini", created: 1760000200,
			choices:  []choice{{0, "First answer.", "length", false}, {1, "Second answer.", "stop", false}},
			kinds:    []EventKind{EventDelta, EventDelta, EventDelta, EventDelta, EventFinish, EventTerminal},
			finished: true, terminated: true,
		},
		{
			file: "openai_completions.sse", format: FormatOpenAICompletions, id: "cmpl-7", model: "gpt-3.5-turbo-instruct", created: 1760000300,
			choices:  []choice{{0, "Paris is the capital.", "stop", false}},
			usage:    &Usage{PromptTokens: 6, CompletionTokens: 5},
			kinds:    []EventKind{EventDelta, EventDelta, EventFinish, EventDelta, EventTerminal},
			finished: true, terminated: true,
		},
		{
			file: "anthropic_basic.sse", format: FormatAnthropicMessages, id: "msg_01XFDUDYJgAACzvnptvVoYEL", model: "claude-sonnet-4-5",
			choices:  []choice{{0, "Refunds are available within 30 days.", "end_turn", false}},
			usage:    &Usage{PromptTokens: 472, CompletionTokens: 15},
			kinds:    []EventKind{EventDelta, EventDelta, EventOther, EventDelta, EventDelta, EventOther, EventFinish, EventTerminal},
			finished: true, terminated: true,
		},
		{
			// Thinking and tool_use input never reach the gated text.
			file: "anthropic_thinking_tools.sse", format: FormatAnthropicMessages, id: "msg_01Think", model: "claude-opus-4-1",
			choices: []choice{{0, "Checking order 4411 now.", "tool_use", true}},
			usage:   &Usage{PromptTokens: 900, CompletionTokens: 89},
			kinds: []EventKind{EventDelta, EventDelta, EventDelta, EventDelta, EventOther, EventDelta, EventDelta, EventOther,
				EventDelta, EventDelta, EventDelta, EventOther, EventFinish, EventTerminal},
			finished: true, terminated: true,
		},
		{
			// Raw multi-byte UTF-8 (Cyrillic, CJK, an emoji): the chunking tests split it anywhere.
			file: "anthropic_utf8.sse", format: FormatAnthropicMessages, id: "msg_01Utf8", model: "claude-haiku-4-5",
			choices:  []choice{{0, "Возврат — 30 дней. 退款 😀 ok", "end_turn", false}},
			usage:    &Usage{PromptTokens: 12, CompletionTokens: 14},
			kinds:    []EventKind{EventDelta, EventDelta, EventDelta, EventDelta, EventOther, EventFinish, EventTerminal},
			finished: true, terminated: true,
		},
		{
			file: "anthropic_error.sse", format: FormatAnthropicMessages, id: "msg_01Err", model: "claude-haiku-4-5",
			choices: []choice{{0, "Partial", "", false}},
			usage:   &Usage{PromptTokens: 10, CompletionTokens: 1},
			kinds:   []EventKind{EventDelta, EventDelta, EventDelta, EventError},
			errType: "overloaded_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			for i, src := range lineEndings(fixture(t, tc.file)) {
				asm, kinds := assemble(t, tc.format, src)
				if !slices.Equal(kinds, tc.kinds) {
					t.Errorf("variant %d: kinds %v, want %v", i, kinds, tc.kinds)
				}
				r := asm.Response()
				var got []choice
				for _, c := range r.Choices {
					got = append(got, choice{c.Index, c.Text, c.FinishReason, c.ToolCalls})
				}
				if !reflect.DeepEqual(got, tc.choices) {
					t.Errorf("variant %d: choices %+v, want %+v", i, got, tc.choices)
				}
				if r.ID != tc.id || r.Model != tc.model || r.Created != tc.created || r.Format != tc.format {
					t.Errorf("variant %d: id %q model %q created %d format %q", i, r.ID, r.Model, r.Created, r.Format)
				}
				if !reflect.DeepEqual(r.Usage, tc.usage) {
					t.Errorf("variant %d: usage %+v, want %+v", i, r.Usage, tc.usage)
				}
				if asm.Finished() != tc.finished || asm.Terminated() != tc.terminated {
					t.Errorf("variant %d: finished %v terminated %v", i, asm.Finished(), asm.Terminated())
				}
				if e := asm.Err(); (e == nil) != (tc.errType == "") || (e != nil && e.Type != tc.errType) {
					t.Errorf("variant %d: err %v, want type %q", i, e, tc.errType)
				}
				if asm.Malformed() != 0 {
					t.Errorf("variant %d: %d malformed events", i, asm.Malformed())
				}
			}
		})
	}
}

func TestStreamAssemblerKeepsTheUpstreamUsageObject(t *testing.T) {
	asm, _ := assemble(t, FormatOpenAIChat, fixture(t, "openai_chat_tools_reasoning.sse"))
	if got := string(asm.Response().RawUsage); got != `{"prompt_tokens":120,"completion_tokens":33,"total_tokens":153,"cost":0.000291}` {
		t.Errorf("raw usage %s", got)
	}
}

func TestStreamAndJSONAgree(t *testing.T) {
	// The text the gate sees must not depend on whether the client asked for a stream.
	cases := []struct {
		format, file, json string
	}{
		{FormatOpenAIChat, "openai_chat_basic.sse", `{"id":"chatcmpl-B9x1","object":"chat.completion","created":1760000000,"model":"gpt-4o-mini-2024-07-18",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Refunds are available within 30 days \u2014 <b>no</b> exceptions.","refusal":null},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":41,"completion_tokens":9,"total_tokens":50,"prompt_tokens_details":{"cached_tokens":0}}}`},
		{FormatOpenAIChat, "openai_chat_tools_reasoning.sse", `{"id":"gen-17","object":"chat.completion","created":1760000100,"model":"deepseek/deepseek-r1",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Let me check order 4411.","reasoning":"The user asks about order 4411. I should call the tool.",
			"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_order","arguments":"{\"order_id\": \"4411\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":120,"completion_tokens":33,"total_tokens":153,"cost":0.000291}}`},
		{FormatOpenAICompletions, "openai_completions.sse", `{"id":"cmpl-7","object":"text_completion","created":1760000300,"model":"gpt-3.5-turbo-instruct",
			"choices":[{"text":"Paris is the capital.","index":0,"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":5,"total_tokens":11}}`},
		{FormatAnthropicMessages, "anthropic_basic.sse", `{"id":"msg_01XFDUDYJgAACzvnptvVoYEL","type":"message","role":"assistant","model":"claude-sonnet-4-5",
			"content":[{"type":"text","text":"Refunds are available within 30 days."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":472,"output_tokens":15}}`},
		{FormatAnthropicMessages, "anthropic_thinking_tools.sse", `{"id":"msg_01Think","type":"message","role":"assistant","model":"claude-opus-4-1",
			"content":[{"type":"thinking","thinking":"The order id is 4411; I need the tool.","signature":"EqQBCgIYAhIM"},{"type":"text","text":"Checking order 4411 now."},
			{"type":"tool_use","id":"toolu_01","name":"get_order","input":{"order_id":"4411"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":900,"output_tokens":89}}`},
	}
	for _, tc := range cases {
		asm, _ := assemble(t, tc.format, fixture(t, tc.file))
		streamed := asm.Response()
		parsed, err := ParseResponse(tc.format, []byte(tc.json))
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		s, _ := streamed.Gated()
		p, _ := parsed.Gated()
		if s != p {
			t.Errorf("%s: streamed %+v, parsed %+v", tc.file, s, p)
		}
		if !reflect.DeepEqual(streamed.Usage, parsed.Usage) || streamed.ID != parsed.ID || streamed.Model != parsed.Model {
			t.Errorf("%s: streamed %+v, parsed %+v", tc.file, streamed, parsed)
		}
	}
}

func TestStreamAssemblerJoinsCharactersSplitAcrossDeltas(t *testing.T) {
	chat := func(content string) string {
		return `data: {"id":"c","choices":[{"index":0,"delta":{"content":"` + content + `"},"finish_reason":null}]}` + "\n\n"
	}
	cases := []struct {
		name, format, stream, want string
	}{
		{"raw utf-8 bytes", FormatOpenAIChat, chat("Caf\xc3") + chat("\xa9 \xf0\x9f") + chat("\x98\x80!"), "Café 😀!"},
		{"surrogate pair halves", FormatOpenAIChat, chat(`\ud83d`) + chat(`\ude00 ok`), "😀 ok"},
		{"escapes", FormatOpenAIChat, chat(`a\nb \"q\" \\ \u00e9`), "a\nb \"q\" \\ é"},
		{"lone half stays replaced", FormatOpenAIChat, chat(`x\ud83d`), "x\uFFFD"},
		{"anthropic", FormatAnthropicMessages,
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\ud83d\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\ude00\"}}\n\n",
			"😀"},
	}
	for _, tc := range cases {
		// One byte at a time, so the network split falls inside every character too.
		asm, err := NewStreamAssembler(tc.format)
		if err != nil {
			t.Fatal(err)
		}
		r := NewReader(&chunked{src: []byte(tc.stream), sizes: []int{1}})
		for _, ev := range readAll(t, r) {
			asm.Add(ev)
		}
		if c, _ := asm.Response().Gated(); c.Text != tc.want {
			t.Errorf("%s: text %q, want %q", tc.name, c.Text, tc.want)
		}
		if got := asm.Text(); got != tc.want {
			t.Errorf("%s: Text %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStreamAssemblerTolerance(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"x","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":`,             // truncated JSON
		`data: not json`,           // not an object
		`data: {"choices":"nope"}`, // wrong shapes are ignored
		`data: {"choices":[7,{"delta":{"content":[{"type":"text","text":" world"},{"type":"image_url"}]}}]}`,
		`data: {"choices":[{"index":0,"delta":{"refusal":"I can't."},"finish_reason":null}]}`,
		`data:`,       // empty data
		`event: ping`, // no data at all
		`data: {"error":{"message":"Internal error","type":"server_error","code":502},"choices":[{"index":0,"delta":{},"finish_reason":"error"}]}`,
		`data: [DONE]`,
	}, "\n\n") // the [DONE] line has no blank line after it
	asm, kinds := assemble(t, FormatOpenAIChat, []byte(stream))
	want := []EventKind{EventDelta, EventMalformed, EventMalformed, EventOther, EventDelta, EventDelta, EventOther, EventOther, EventError, EventTerminal}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds %v, want %v", kinds, want)
	}
	if asm.Malformed() != 2 || !asm.Terminated() || !asm.Finished() {
		t.Errorf("malformed %d terminated %v finished %v", asm.Malformed(), asm.Terminated(), asm.Finished())
	}
	c, _ := asm.Response().Gated()
	if c.Text != "Hello world" || c.Refusal != "I can't." || c.FinishReason != "error" {
		t.Errorf("choice %+v", c)
	}
	if e := asm.Err(); e == nil || e.Code != "502" || e.Type != "server_error" || e.Message != "Internal error" ||
		e.Error() != "llmapi: upstream stream error server_error 502: Internal error" {
		t.Errorf("err %+v", e)
	}
}

func TestStreamAssemblerAnthropicTolerance(t *testing.T) {
	// No event lines (the type comes from the data), a delta without its block start, an unknown
	// event type, a malformed event and a comment.
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":5}}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Oh, "}}`,
		`event: future_event` + "\n" + `data: {"type":"future_event"}`,
		`event: content_block_delta` + "\n" + `data: {"type":`,
		`: keepalive`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"input_tokens":5,"output_tokens":3}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	asm, kinds := assemble(t, FormatAnthropicMessages, []byte(stream))
	want := []EventKind{EventDelta, EventDelta, EventDelta, EventOther, EventMalformed, EventOther, EventFinish, EventTerminal}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds %v, want %v", kinds, want)
	}
	r := asm.Response()
	c, _ := r.Gated()
	if c.Text != "Oh, Hi" || c.FinishReason != "max_tokens" || r.ID != "msg_1" || *r.Usage != (Usage{5, 3}) {
		t.Errorf("response %+v choice %+v usage %+v", r, c, r.Usage)
	}

	// Nothing read: no choice to gate.
	empty, _ := NewStreamAssembler(FormatAnthropicMessages)
	if _, ok := empty.Response().Gated(); ok {
		t.Error("an empty stream has nothing to gate")
	}
}

func TestIsTerminal(t *testing.T) {
	cases := []struct {
		format string
		ev     Event
		want   bool
	}{
		{FormatOpenAIChat, DataEvent("", []byte("[DONE]")), true},
		{FormatOpenAICompletions, DataEvent("", []byte(" [DONE] ")), true},
		{FormatOpenAIChat, DataEvent("", []byte(`{"choices":[]}`)), false},
		{FormatOpenAIChat, CommentEvent("[DONE]"), false},
		{FormatAnthropicMessages, DataEvent("message_stop", []byte(`{"type":"message_stop"}`)), true},
		{FormatAnthropicMessages, DataEvent("", []byte(`{"type":"message_stop"}`)), true},
		{FormatAnthropicMessages, DataEvent("message_delta", []byte(`{}`)), false},
		{FormatAnthropicMessages, DataEvent("", []byte("[DONE]")), false},
	}
	for _, tc := range cases {
		if got := IsTerminal(tc.format, tc.ev); got != tc.want {
			t.Errorf("%s %q: %v", tc.format, tc.ev.Encode(), got)
		}
	}
	if _, err := NewStreamAssembler("responses"); err != ErrUnknownFormat {
		t.Errorf("err %v", err)
	}
	if EventTerminal.String() != "terminal" || EventKind(42).String() != "EventKind(42)" {
		t.Error("EventKind names")
	}
}

// TestStreamAssemblerTextSoFar: Text is the gated text at every point of the stream, a split
// character showing as U+FFFD until its rest arrives, and nothing once the stream is too large.
func TestStreamAssemblerTextSoFar(t *testing.T) {
	chat := func(index int, content string) Event {
		data := `{"id":"c","choices":[{"index":` + strconv.Itoa(index) + `,"delta":{"content":"` + content + `"},"finish_reason":null}]}`
		return Event{Data: []byte(data), HasData: true}
	}
	asm, err := NewStreamAssembler(FormatOpenAIChat)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		ev   Event
		want string
	}{
		{chat(0, "Hello"), "Hello"},
		{chat(1, "other choice"), "Hello"},
		{chat(0, ` \ud83d`), "Hello �"},
		{chat(0, `\ude00!`), "Hello 😀!"},
	}
	for i, st := range steps {
		asm.Add(st.ev)
		if got := asm.Text(); got != st.want {
			t.Errorf("step %d: Text %q, want %q", i, got, st.want)
		}
	}
	asm.SetMaxBytes(1)
	asm.Add(chat(0, "more"))
	if !asm.TooLarge() || asm.Text() != "" {
		t.Errorf("too large: %v, Text %q", asm.TooLarge(), asm.Text())
	}

	anth, err := NewStreamAssembler(FormatAnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct{ name, data string }{
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`},
	} {
		anth.Add(Event{Name: ev.name, Data: []byte(ev.data), HasData: true})
	}
	if got := anth.Text(); got != "Hello world" {
		t.Errorf("anthropic Text %q, want the text blocks in index order", got)
	}
}
