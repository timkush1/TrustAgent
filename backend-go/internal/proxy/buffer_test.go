package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
)

const strmBlockMessage = "I can't confirm that from our policy documents."

// strmBufferPolicy is a closed buffer-streaming route in mode with extra settings; its block
// sends strmBlockMessage in style (message when empty).
func strmBufferPolicy(mode, style, extra string) string {
	block := "block: {message: \"" + strmBlockMessage + "\""
	if style != "" {
		block += ", style: " + style
	}
	s := "mode: " + mode + ", fail: closed, streaming: buffer, " + block + "}"
	if extra != "" {
		s += ", " + extra
	}
	return strmPolicy(s)
}

// strmData decodes the JSON data of an event.
func strmData(t *testing.T, ev llmapi.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Data, &m); err != nil {
		t.Fatalf("event data %q: %v", ev.Data, err)
	}
	return m
}

// TestBufferedPassDeliversHeadersAndTheUpstreamEvents: an answer decided before its headers went
// out carries the decision in the headers (no trailers, no comment) and reaches the client as the
// upstream sent it (D9).
func TestBufferedPassDeliversHeadersAndTheUpstreamEvents(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	events := strmOpenAIStream(sc.Response)
	e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmBufferPolicy("block", "", "")})
	resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if string(body) != strings.Join(events, "") {
		t.Errorf("the upstream events, as they were:\n%s", body)
	}
	for name, want := range map[string]string{
		HeaderAction: "pass", HeaderMode: "block", HeaderStatus: "ok", HeaderVerdict: "supported", "X-Upstream": "yes",
		HeaderSource: gate.SourceTrafficgen, "Content-Type": "text/event-stream; charset=utf-8",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if len(resp.Trailer) != 0 {
		t.Errorf("no trailers while the headers carry the decision: %v", resp.Trailer)
	}
	r := e.sink.result(t)
	if r.Decision.Action != decision.ActionPass || !r.Decision.Enforced || r.Final != nil || r.ResponseText != sc.Response {
		t.Errorf("action %s enforced %v final %+v", r.Decision.Action, r.Decision.Enforced, r.Final)
	}
}

// TestBufferedKeepaliveCommitsTheHeaders: a slow answer gets a keepalive comment, which commits
// the headers; the decision then goes out as the comment before [DONE] and as trailers.
func TestBufferedKeepaliveCommitsTheHeaders(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	events := strmOpenAIStream(sc.Response)
	u := newStrmUpstream(t, events...)
	u.delay = 1300 * time.Millisecond
	e := newStrmEnv(t, u, strmOpts{policyYAML: strmBufferPolicy("block", "", "buffer_keepalive_s: 1")})
	resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if !strings.HasPrefix(string(body), ": keepalive\n\n") {
		t.Fatalf("the keepalive comes first:\n%s", body)
	}
	evs := strmEvents(t, body)
	a, at := strmAnnotation(t, evs)
	if at != len(evs)-2 || a.Action != "pass" || !llmapi.IsTerminal(llmapi.FormatOpenAIChat, evs[len(evs)-1]) {
		t.Errorf("annotation %+v at %d of %d", a, at, len(evs))
	}
	if resp.Header.Get(HeaderAction) != "" || resp.Header.Get(HeaderRoute) == "" {
		t.Errorf("after the commit the decision is a trailer: %v", resp.Header)
	}
	if resp.Trailer.Get(HeaderAction) != "pass" || resp.Trailer.Get(HeaderMode) != "block" || resp.Trailer.Get(HeaderStatus) != "ok" {
		t.Errorf("trailers %v", resp.Trailer)
	}
	e.sink.result(t)
}

// TestBufferedBlockBeforeAndAfterCommit: before the headers went out an error-style block sends
// its status and error body (OpenAI and Anthropic shapes); after a keepalive committed them the
// block is a message-style stream with the comment and trailers.
func TestBufferedBlockBeforeAndAfterCommit(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	t.Run("uncommitted openai", func(t *testing.T) {
		e := newStrmEnv(t, newStrmUpstream(t, strmOpenAIStream(sc.Response)...),
			strmOpts{policyYAML: strmBufferPolicy("block", "error, status: 451", "")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		var got struct {
			Error struct{ Message, Type, Code string }
		}
		if err := json.Unmarshal(body, &got); err != nil || resp.StatusCode != http.StatusUnavailableForLegalReasons ||
			resp.Header.Get("Content-Type") != "application/json" || got.Error.Type != llmapi.BlockErrorType ||
			got.Error.Code != llmapi.BlockErrorCode || got.Error.Message != strmBlockMessage {
			t.Fatalf("%d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		if resp.Header.Get(HeaderAction) != "block" || resp.Header.Get(HeaderStatus) != "ok" || len(resp.Trailer) != 0 {
			t.Errorf("headers %v trailers %v", resp.Header, resp.Trailer)
		}
		r := e.sink.result(t)
		want := &gate.Final{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: strmBlockMessage}
		if !reflect.DeepEqual(r.Final, want) || !r.Decision.Enforced || r.ResponseText != sc.Response {
			t.Errorf("final %+v enforced %v text %q", r.Final, r.Decision.Enforced, r.ResponseText)
		}
	})
	t.Run("uncommitted anthropic", func(t *testing.T) {
		e := newStrmEnv(t, newStrmUpstream(t, strmAnthropicStream(sc.Response)...),
			strmOpts{policyYAML: strmBufferPolicy("block", "error", "")})
		resp, body := e.call(t, "/v1/messages", sc.body(t, llmapi.FormatAnthropicMessages, nil), sc.ID)
		var got struct {
			Type  string
			Error struct{ Type, Message string }
		}
		if err := json.Unmarshal(body, &got); err != nil || resp.StatusCode != http.StatusBadRequest || got.Type != "error" ||
			got.Error.Type != "invalid_request_error" || got.Error.Message != strmBlockMessage {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		e.sink.result(t)
	})
	t.Run("committed", func(t *testing.T) {
		u := newStrmUpstream(t, strmOpenAIStream(sc.Response)...)
		u.delay = 1300 * time.Millisecond
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmBufferPolicy("block", "error, status: 451", "buffer_keepalive_s: 1")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		text, asm := strmText(t, llmapi.FormatOpenAIChat, body)
		c, _ := asm.Response().Gated()
		if resp.StatusCode != http.StatusOK || text != strmBlockMessage || c.FinishReason != llmapi.FinishReasonContentFilter {
			t.Fatalf("a committed block is message style: %d %q %s\n%s", resp.StatusCode, text, c.FinishReason, body)
		}
		evs := strmEvents(t, body)
		if a, at := strmAnnotation(t, evs); at != len(evs)-2 || a.Action != "block" {
			t.Errorf("annotation %+v at %d of %d", a, at, len(evs))
		}
		if resp.Trailer.Get(HeaderAction) != "block" {
			t.Errorf("trailers %v", resp.Trailer)
		}
		e.sink.result(t)
	})
}

// TestBufferedOpenAIBlockChunks (SPEC §2.12): a blocked chat stream is a chunk with the role and
// the message, a chunk with an empty delta and finish_reason content_filter, the usage chunk only
// under stream_options.include_usage, then [DONE]; ids, created and model are the upstream's.
func TestBufferedOpenAIBlockChunks(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	for _, includeUsage := range []bool{false, true} {
		e := newStrmEnv(t, newStrmUpstream(t, strmOpenAIStream(sc.Response)...), strmOpts{policyYAML: strmBufferPolicy("block", "", "")})
		extra := map[string]any{"stream_options": map[string]any{"include_usage": includeUsage}}
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, extra), sc.ID)
		evs := strmEvents(t, body)
		want := 3
		if includeUsage {
			want = 4
		}
		if resp.StatusCode != http.StatusOK || len(evs) != want || !llmapi.IsTerminal(llmapi.FormatOpenAIChat, evs[len(evs)-1]) {
			t.Fatalf("include_usage %v: %d events:\n%s", includeUsage, len(evs), body)
		}
		first, second := strmData(t, evs[0]), strmData(t, evs[1])
		for _, m := range []map[string]any{first, second} {
			if m["id"] != "chatcmpl-up" || m["object"] != "chat.completion.chunk" || m["created"] != float64(1760000000) || m["model"] != "gpt-4o-mini" {
				t.Errorf("chunk %v", m)
			}
		}
		c0 := first["choices"].([]any)[0].(map[string]any)
		delta := c0["delta"].(map[string]any)
		if delta["role"] != "assistant" || delta["content"] != strmBlockMessage || c0["finish_reason"] != nil {
			t.Errorf("first chunk %v", first)
		}
		c1 := second["choices"].([]any)[0].(map[string]any)
		if len(c1["delta"].(map[string]any)) != 0 || c1["finish_reason"] != llmapi.FinishReasonContentFilter {
			t.Errorf("second chunk %v", second)
		}
		if includeUsage {
			u := strmData(t, evs[2])
			if len(u["choices"].([]any)) != 0 || u["usage"].(map[string]any)["prompt_tokens"] != float64(42) {
				t.Errorf("usage chunk %v", u)
			}
		}
		if strings.Contains(string(body), "45 days") {
			t.Error("the blocked answer leaked")
		}
		e.sink.result(t)
	}
}

// TestBufferedAnthropicBlockEvents (SPEC §2.12): a blocked Messages stream is message_start,
// content_block_start, one text_delta with the message, content_block_stop, message_delta with
// the route's stop reason (refusal by default), message_stop.
func TestBufferedAnthropicBlockEvents(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	e := newStrmEnv(t, newStrmUpstream(t, strmAnthropicStream(sc.Response)...), strmOpts{policyYAML: strmBufferPolicy("block", "", "")})
	resp, body := e.call(t, "/v1/messages", sc.body(t, llmapi.FormatAnthropicMessages, nil), sc.ID)
	evs := strmEvents(t, body)
	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type())
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if resp.StatusCode != http.StatusOK || !slices.Equal(types, want) {
		t.Fatalf("%d %v:\n%s", resp.StatusCode, types, body)
	}
	text, asm := strmText(t, llmapi.FormatAnthropicMessages, body)
	c, _ := asm.Response().Gated()
	start := strmData(t, evs[0])["message"].(map[string]any)
	if text != strmBlockMessage || c.FinishReason != llmapi.StopReasonRefusal ||
		start["id"] != llmapi.SyntheticIDFor(llmapi.FormatAnthropicMessages, resp.Header.Get(HeaderTraceID)) {
		t.Errorf("text %q stop %q start %v", text, c.FinishReason, start)
	}
	e.sink.result(t)
}

// TestBufferedFallbackFromAHandBuiltOutcome: a fallback route delivers what the fallback picked:
// the fallback's answer re-emitted as a stream (finish_reason stop), the original events
// (on_fail: original) or the block. The attempt-1 re-gate is recorded before the trace's decision,
// once, and the delivered outcome is the trace's Final.
func TestBufferedFallbackFromAHandBuiltOutcome(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	events := strmOpenAIStream(sc.Response)
	const fb = "Annual plans can be refunded within 30 days of purchase; day 40 is past that window."
	result1 := &gate.Result{Decision: &gate.Decision{Attempt: 1}}
	cases := []struct {
		name string
		out  fallbackOut
		// text is what the client reads.
		text string
	}{
		{"delivered", fallbackOut{
			Action: decision.ActionFallback, Delivered: gate.DeliveredFallback, Text: fb, Result1: result1,
			Response: &llmapi.ChatResponse{Format: llmapi.FormatOpenAIChat, ID: "chatcmpl-fb", Model: "gpt-4o",
				Choices: []llmapi.ChatChoice{{Index: 0, Text: fb, FinishReason: "length"}}},
			Flags: []string{gate.FlagFallbackUsed},
		}, fb},
		{"original", fallbackOut{Action: decision.ActionFlag, Delivered: gate.DeliveredOriginal, Text: sc.Response, Result1: result1}, sc.Response},
		{"blocked", fallbackOut{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: strmBlockMessage,
			Reasons: []string{reasonFallbackFailed}}, strmBlockMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls int
			restore := bufferedFallback
			bufferedFallback = func(_ *Handler, _ context.Context, f *flow, res0 *gate.Result) *fallbackOut {
				calls++
				if res0.Decision.Action != decision.ActionFallback || f.x.Stream != gate.StreamBuffer {
					t.Errorf("fallback for %s (%s)", res0.Decision.Action, f.x.Stream)
				}
				out := c.out
				return &out
			}
			t.Cleanup(func() { bufferedFallback = restore })
			e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmBufferPolicy("fallback", "",
				"fallback: {upstream: same, on_fail: block}")})
			resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			text, asm := strmText(t, llmapi.FormatOpenAIChat, body)
			if resp.StatusCode != http.StatusOK || text != c.text || !asm.Terminated() || calls != 1 {
				t.Fatalf("%d %q (%d calls):\n%s", resp.StatusCode, text, calls, body)
			}
			ch, _ := asm.Response().Gated()
			switch c.name {
			case "delivered":
				if ch.FinishReason != llmapi.FinishReasonStop || asm.Response().ID != "chatcmpl-fb" || resp.Header.Get(HeaderAction) != "fallback" {
					t.Errorf("finish %q id %q action %q", ch.FinishReason, asm.Response().ID, resp.Header.Get(HeaderAction))
				}
			case "original":
				if string(body) != strings.Join(events, "") {
					t.Errorf("the original events:\n%s", body)
				}
			case "blocked":
				if ch.FinishReason != llmapi.FinishReasonContentFilter {
					t.Errorf("finish %q", ch.FinishReason)
				}
			}
			r := e.sink.result(t)
			want := &gate.Final{Action: c.out.Action, Delivered: c.out.Delivered, Text: c.out.Text, Flags: c.out.Flags, Reasons: c.out.Reasons}
			if !reflect.DeepEqual(r.Final, want) || r.ResponseText != sc.Response {
				t.Errorf("final %+v, want %+v; audited text %q", r.Final, want, r.ResponseText)
			}
			e.sink.mu.Lock()
			sinkCalls, attempts := slices.Clone(e.sink.calls), slices.Clone(e.sink.attempts)
			e.sink.mu.Unlock()
			wantCalls := []string{"decided"}
			if c.out.Result1 != nil {
				wantCalls = []string{"attempt", "decided"}
			}
			if !slices.Equal(sinkCalls, wantCalls) || (c.out.Result1 != nil && attempts[0] != result1) {
				t.Errorf("sink calls %v", sinkCalls)
			}
		})
	}
}

// TestBufferedSlowFallbackStillDelivers: a fallback call that outlasts the route's write deadline
// does not cut the answer off: the deadline is pushed before the call and before the write.
func TestBufferedSlowFallbackStillDelivers(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	restore := bufferedFallback
	bufferedFallback = func(*Handler, context.Context, *flow, *gate.Result) *fallbackOut {
		time.Sleep(500 * time.Millisecond)
		return &fallbackOut{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: strmBlockMessage,
			Reasons: []string{reasonFallbackFailed}}
	}
	t.Cleanup(func() { bufferedFallback = restore })
	e := newStrmEnv(t, newStrmUpstream(t, strmOpenAIStream(sc.Response)...), strmOpts{
		policyYAML:   strmBufferPolicy("fallback", "error", "fallback: {upstream: same, on_fail: block}"),
		writeTimeout: 250 * time.Millisecond,
	})
	resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if resp.StatusCode != llmapi.DefaultBlockStatus || !strings.Contains(string(body), llmapi.BlockErrorCode) {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	e.sink.result(t)
}

// TestBufferedFallbackWithoutAnUpstream: a synthesized (test_response) stream on a fallback route
// whose flow has no upstream still reaches the fallback and delivers what it picked.
func TestBufferedFallbackWithoutAnUpstream(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	restore := bufferedFallback
	bufferedFallback = func(*Handler, context.Context, *flow, *gate.Result) *fallbackOut {
		return &fallbackOut{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: strmBlockMessage,
			Reasons: []string{reasonFallbackFailed}}
	}
	t.Cleanup(func() { bufferedFallback = restore })
	e := newStrmEnv(t, nil, strmOpts{
		policyYAML: strmBufferPolicy("fallback", "", "fallback: {upstream: same, on_fail: block}"), synth: sc.Response, noFlowUpstream: true,
	})
	resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if text, _ := strmText(t, llmapi.FormatOpenAIChat, body); resp.StatusCode != http.StatusOK || text != strmBlockMessage {
		t.Errorf("%d %q", resp.StatusCode, text)
	}
	if r := e.sink.result(t); r.Final == nil || r.Final.Delivered != gate.DeliveredBlocked {
		t.Errorf("final %+v", r.Final)
	}
}

// TestBufferedTooLargeTail: an answer past the size limit is a skip: an open route delivers what
// was read and then the rest of the stream, ungated; a closed block route blocks it (D4).
func TestBufferedTooLargeTail(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	var events []string
	for range 40 {
		events = append(events, strmChunk("Refunds take thirty days. ", ""))
	}
	events = append(events, strmChunk("", "stop"), "data: [DONE]\n\n")

	e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{
		policyYAML: strmPolicy("mode: flag, fail: open, streaming: buffer"), maxBytes: 2048,
	})
	resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if string(body) != strings.Join(events, "") || resp.Header.Get(HeaderStatus) != "skipped:upstream_error" {
		t.Errorf("open route: status %q, %d bytes of %d", resp.Header.Get(HeaderStatus), len(body), len(strings.Join(events, "")))
	}
	r := e.sink.result(t)
	if r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError || r.Final != nil {
		t.Errorf("skip %v final %+v", r.Decision.SkipReason, r.Final)
	}

	e = newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmBufferPolicy("block", "", ""), maxBytes: 2048})
	resp, body = e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if text, _ := strmText(t, llmapi.FormatOpenAIChat, body); text != strmBlockMessage || resp.Header.Get(HeaderAction) != "block" {
		t.Errorf("closed route blocks: %q %q", text, resp.Header.Get(HeaderAction))
	}
	if r := e.sink.result(t); r.Final == nil || r.Final.Delivered != gate.DeliveredBlocked {
		t.Errorf("final %+v", r.Final)
	}

	// Fail open lets an unchecked answer through, not an unread one: a block route that fails open
	// still blocks an answer past the limit rather than relaying its rest ungated (D4).
	e = newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{
		policyYAML: strmPolicy(`mode: block, fail: open, streaming: buffer, block: {message: "` + strmBlockMessage + `"}`), maxBytes: 2048,
	})
	_, body = e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if text, asm := strmText(t, llmapi.FormatOpenAIChat, body); text != strmBlockMessage || !asm.Terminated() {
		t.Errorf("a fail-open block route blocks the unread answer: %q", text)
	}
	r = e.sink.result(t)
	if r.Decision.Action != decision.ActionPass || r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError ||
		r.Final == nil || r.Final.Action != decision.ActionBlock || r.Final.Delivered != gate.DeliveredBlocked {
		t.Errorf("fail open decides pass, the delivery blocks: action %s skip %v final %+v", r.Decision.Action, r.Decision.SkipReason, r.Final)
	}
}

// TestBufferedOversizeRestCutOffStillFinishes: after a keepalive committed the headers, an answer
// past the size limit goes out with the rest of the stream; an upstream that drops the connection
// during the rest still leaves the client the decision trailers, and the trace an upstream error.
func TestBufferedOversizeRestCutOffStillFinishes(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	hello, big := strmChunk("Hello ", ""), strmChunk(strings.Repeat("x", 3000), "")
	u := newStrmUpstream(t, hello, big)
	u.delay, u.abort = 1300*time.Millisecond, true
	e := newStrmEnv(t, u, strmOpts{
		policyYAML: strmPolicy("mode: flag, fail: open, streaming: buffer, buffer_keepalive_s: 1"), maxBytes: 2048,
	})
	resp := e.open(t, context.Background(), "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || !strings.HasPrefix(string(body), ": keepalive\n\n"+hello+big) {
		t.Fatalf("the keepalive, then what the upstream sent (%v):\n%q", err, body[:min(len(body), 200)])
	}
	if resp.Trailer.Get(HeaderStatus) != "skipped:upstream_error" || resp.Trailer.Get(HeaderAction) != decision.ActionPass {
		t.Errorf("the client is still there and gets the decision: %v", resp.Trailer)
	}
	r := e.sink.result(t)
	if r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError || slices.Contains(r.Flags, gate.FlagClientDisconnected) {
		t.Errorf("skip %v flags %v", r.Decision.SkipReason, r.Flags)
	}
}

// TestBufferedToolCallsPassThroughUnenforced (D5): an answer with tool calls and no text cannot be
// gated; even a closed block route delivers it as it is, unenforced.
func TestBufferedToolCallsPassThroughUnenforced(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	events := []string{
		`data: {"id":"chatcmpl-up","object":"chat.completion.chunk","created":1760000000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup_order","arguments":"{\"id\":7}"}}]},"finish_reason":null}]}` + "\n\n",
		strmChunk("", "tool_calls"),
		"data: [DONE]\n\n",
	}
	e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmBufferPolicy("block", "", "")})
	_, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	if string(body) != strings.Join(events, "") {
		t.Errorf("the tool calls pass as they are:\n%s", body)
	}
	r := e.sink.result(t)
	want := &gate.Final{Action: decision.ActionPass, Delivered: gate.DeliveredOriginal}
	if r.Decision.Enforced || r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipNoText || !reflect.DeepEqual(r.Final, want) {
		t.Errorf("enforced %v skip %v final %+v", r.Decision.Enforced, r.Decision.SkipReason, r.Final)
	}

}

// TestBufferedClientLeavesDuringTheFallback: a client that goes away while the fallback is called
// gets nothing, so nothing is recorded as delivered: the decision is unenforced and the trace's
// delivered action is pass with the original (as for a JSON call the client left), flagged
// client_disconnected (the answer was complete, D7). The fallback's attempt 1 stays on record,
// before the trace's decision.
func TestBufferedClientLeavesDuringTheFallback(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	result1 := &gate.Result{Decision: &gate.Decision{Attempt: 1}}
	called := make(chan struct{})
	restore := bufferedFallback
	bufferedFallback = func(_ *Handler, ctx context.Context, _ *flow, _ *gate.Result) *fallbackOut {
		close(called)
		<-ctx.Done() // the client leaves while the fallback upstream answers
		return &fallbackOut{Action: decision.ActionFallback, Delivered: gate.DeliveredFallback, Text: "A fallback answer.",
			Result1: result1, Flags: []string{gate.FlagFallbackUsed}}
	}
	t.Cleanup(func() { bufferedFallback = restore })
	e := newStrmEnv(t, newStrmUpstream(t, strmOpenAIStream(sc.Response)...), strmOpts{
		policyYAML: strmBufferPolicy("fallback", "", "fallback: {upstream: same, on_fail: block}"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+"/v1/chat/completions",
		strings.NewReader(sc.body(t, llmapi.FormatOpenAIChat, nil)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderScenario, sc.ID)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := e.srv.Client().Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the fallback was not called")
	}
	cancel()
	<-done

	r := e.sink.result(t)
	want := &gate.Final{Action: decision.ActionPass, Delivered: gate.DeliveredOriginal}
	if !reflect.DeepEqual(r.Final, want) || r.Decision.Enforced || r.Decision.Action != decision.ActionFallback ||
		!slices.Contains(r.Flags, gate.FlagClientDisconnected) || slices.Contains(r.Flags, gate.FlagFallbackUsed) {
		t.Errorf("final %+v enforced %v action %s flags %v", r.Final, r.Decision.Enforced, r.Decision.Action, r.Flags)
	}
	e.sink.mu.Lock()
	calls, attempts := slices.Clone(e.sink.calls), slices.Clone(e.sink.attempts)
	e.sink.mu.Unlock()
	if !slices.Equal(calls, []string{"attempt", "decided"}) || attempts[0] != result1 {
		t.Errorf("sink calls %v", calls)
	}
}
