package llmapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testAnnotation = Annotation{
	TraceID: "0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90", Action: "flag", WouldAction: "flag", Status: "ok",
	Verdict: ptr("unsupported"), MaxRisk: ptr(0.91), Reasons: []string{"groundedness", "det:urls_not_in_evidence"},
}

func ptr[T any](v T) *T { return &v }

func TestAnnotationComment(t *testing.T) {
	want := `trustagent {"trace_id":"0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90","action":"flag","would_action":"flag","status":"ok",` +
		`"verdict":"unsupported","max_risk":0.91,"reasons":["groundedness","det:urls_not_in_evidence"]}`
	if got := testAnnotation.Comment(); got != want {
		t.Errorf("comment\n%s\nwant\n%s", got, want)
	}
	bare := Annotation{TraceID: "t", Action: "pass", WouldAction: "pass", Status: "not_assessed"}
	if got := bare.Comment(); got != `trustagent {"trace_id":"t","action":"pass","would_action":"pass","status":"not_assessed","verdict":null,"max_risk":null,"reasons":[]}` {
		t.Errorf("bare comment %s", got)
	}
}

func TestStreamWriterHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	h := rec.Header()
	// Upstream headers, copied with multi-value Add before the writer takes over.
	h.Add("Content-Type", "text/event-stream; charset=utf-8")
	h.Add("Content-Length", "1234")
	h.Add("Content-Encoding", "gzip")
	h.Add("X-Upstream", "a")
	h.Add("X-Upstream", "b")
	sw := NewStreamWriter(rec)
	sw.DeclareTrailers("x-trustagent-action", "X-TrustAgent-Mode", "X-TrustAgent-Action")
	if sw.Committed() {
		t.Fatal("nothing written yet")
	}
	if err := sw.WriteEvent(DataEvent("", []byte("x"))); err != nil {
		t.Fatal(err)
	}
	sw.DeclareTrailers("X-Too-Late") // ignored once committed
	res := rec.Result()
	for name, want := range map[string]string{
		"Content-Type": "text/event-stream; charset=utf-8", "Content-Length": "", "Content-Encoding": "",
		"Cache-Control": "no-cache", "X-Accel-Buffering": "no", "Trailer": "X-Trustagent-Action, X-Trustagent-Mode",
	} {
		if got := res.Header.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if got := res.Header.Values("X-Upstream"); len(got) != 2 {
		t.Errorf("multi-value header %q", got)
	}
	if !sw.Committed() || res.StatusCode != http.StatusOK {
		t.Errorf("committed %v status %d", sw.Committed(), res.StatusCode)
	}

	// A JSON content type (a buffered stream whose headers came from elsewhere) becomes SSE, and
	// SetStatus applies before the first write only.
	rec = httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	sw = NewStreamWriter(rec)
	sw.SetStatus(http.StatusAccepted)
	if err := sw.Commit(); err != nil {
		t.Fatal(err)
	}
	sw.SetStatus(http.StatusTeapot)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("status %d content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestRelayHoldsTheTerminalEventAndAnnotates(t *testing.T) {
	cases := []struct {
		format, file, terminal string
	}{
		{FormatOpenAIChat, "openai_chat_basic.sse", "data: [DONE]\n\n"},
		{FormatOpenAIChat, "openai_chat_tools_reasoning.sse", "data: [DONE]\n\n"},
		{FormatOpenAICompletions, "openai_completions.sse", "data: [DONE]\n\n"},
		{FormatAnthropicMessages, "anthropic_basic.sse", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{FormatAnthropicMessages, "anthropic_thinking_tools.sse", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	}
	for _, tc := range cases {
		src := fixture(t, tc.file)
		rec := httptest.NewRecorder()
		sw := NewStreamWriter(rec)
		asm, _ := NewStreamAssembler(tc.format)
		res := Relay(NewReader(&chunked{src: src, sizes: []int{5, 17, 1}}), asm, sw)
		if !res.Held || res.ClientErr != nil || res.UpstreamErr != nil || !sw.Holding() {
			t.Fatalf("%s: %+v", tc.file, res)
		}
		if res.Bytes != int64(len(src)) {
			t.Errorf("%s: %d bytes read, want %d", tc.file, res.Bytes, len(src))
		}
		// Everything but the terminal event went out byte for byte.
		before := strings.TrimSuffix(string(src), tc.terminal)
		if rec.Body.String() != before {
			t.Fatalf("%s: relayed\n%q\nwant\n%q", tc.file, rec.Body.String(), before)
		}
		if err := sw.Finish(&testAnnotation, nil); err != nil {
			t.Fatal(err)
		}
		want := before + ": " + testAnnotation.Comment() + "\n\n" + tc.terminal
		if rec.Body.String() != want {
			t.Errorf("%s: finished stream ends %q", tc.file, tail(rec.Body.String(), 200))
		}
		// The client's view is unchanged apart from the comment.
		clientAsm, _ := assemble(t, tc.format, rec.Body.Bytes())
		c1, _ := clientAsm.Response().Gated()
		c2, _ := asm.Response().Gated()
		if c1 != c2 || !clientAsm.Terminated() {
			t.Errorf("%s: client sees %+v, gate saw %+v", tc.file, c1, c2)
		}
		if err := sw.WriteEvent(DataEvent("", []byte("late"))); !errors.Is(err, ErrStreamClosed) {
			t.Errorf("write after Finish: %v", err)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func TestRelayEndsWithoutTerminalEvent(t *testing.T) {
	// The upstream ends (or fails) without its terminal event: nothing is held and Finish still
	// annotates the stream.
	src := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":\"stop\"}]}\n\n"
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(rec)
	asm, _ := NewStreamAssembler(FormatOpenAIChat)
	res := Relay(NewReader(strings.NewReader(src)), asm, sw)
	if res.Held || res.UpstreamErr != nil || res.ClientErr != nil || res.Events != 1 || !asm.Finished() {
		t.Fatalf("%+v", res)
	}
	if err := sw.Finish(&testAnnotation, nil); err != nil {
		t.Fatal(err)
	}
	if want := src + ": " + testAnnotation.Comment() + "\n\n"; rec.Body.String() != want {
		t.Errorf("body %q", rec.Body.String())
	}

	boom := errors.New("reset")
	asm, _ = NewStreamAssembler(FormatOpenAIChat)
	res = Relay(NewReader(&failAfter{data: []byte(src + "data: {\"trunc"), err: boom}), asm, NewStreamWriter(httptest.NewRecorder()))
	if !errors.Is(res.UpstreamErr, boom) || res.Events != 2 || asm.Malformed() != 1 {
		t.Errorf("%+v malformed %d", res, asm.Malformed())
	}

	// An empty stream: Finish commits the headers and writes nothing but the comment (or nothing).
	rec = httptest.NewRecorder()
	sw = NewStreamWriter(rec)
	asm, _ = NewStreamAssembler(FormatAnthropicMessages)
	if res := Relay(NewReader(strings.NewReader("")), asm, sw); res.Events != 0 || res.Held {
		t.Errorf("%+v", res)
	}
	if err := sw.Finish(nil, nil); err != nil || rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("err %v body %q", err, rec.Body.String())
	}
}

// brokenWriter is a ResponseWriter whose client goes away at the n-th Write.
type brokenWriter struct {
	header http.Header
	writes int
	failAt int
	body   bytes.Buffer
}

var errClientGone = errors.New("write: broken pipe")

func (b *brokenWriter) Header() http.Header { return b.header }
func (b *brokenWriter) WriteHeader(int)     {}
func (b *brokenWriter) Write(p []byte) (int, error) {
	b.writes++
	if b.writes >= b.failAt {
		return 0, errClientGone
	}
	return b.body.Write(p)
}

func TestRelayClientDisconnect(t *testing.T) {
	// openai_chat_basic.sse: role, 3 content chunks, the finish chunk (5th), usage, [DONE].
	cases := []struct {
		failAt   int
		finished bool
	}{
		{1, false}, // gone before anything: the answer is skipped
		{4, false}, // gone mid-answer: skipped too
		{5, true},  // gone while the finish chunk is written: the gateway read it, the text is complete
		{6, true},  // gone after the finish chunk: gated unenforced, audited as client_disconnected
	}
	for _, tc := range cases {
		w := &brokenWriter{header: http.Header{}, failAt: tc.failAt}
		sw := NewStreamWriter(w)
		asm, _ := NewStreamAssembler(FormatOpenAIChat)
		res := Relay(NewReader(bytes.NewReader(fixture(t, "openai_chat_basic.sse"))), asm, sw)
		if !errors.Is(res.ClientErr, errClientGone) || res.Held {
			t.Fatalf("failAt %d: %+v", tc.failAt, res)
		}
		// The finish reason counts once it was read from the upstream: the gated text is complete.
		if asm.Finished() != tc.finished {
			t.Errorf("failAt %d: finished %v", tc.failAt, asm.Finished())
		}
		// The error is sticky.
		if err := sw.WriteComment("x"); !errors.Is(err, errClientGone) || !errors.Is(sw.Err(), errClientGone) {
			t.Errorf("sticky error: %v", err)
		}
		if err := sw.Finish(&testAnnotation, map[string]string{"X-TrustAgent-Action": "flag"}); !errors.Is(err, errClientGone) {
			t.Errorf("finish: %v", err)
		}
	}
}

func TestTrailersReachTheClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := NewStreamWriter(w)
		sw.DeclareTrailers("X-TrustAgent-Action", "X-TrustAgent-Mode")
		asm, _ := NewStreamAssembler(FormatOpenAIChat)
		res := Relay(NewReader(bytes.NewReader(fixture(t, "openai_chat_basic.sse"))), asm, sw)
		if !res.Held {
			t.Errorf("relay %+v", res)
		}
		if err := sw.ExtendWriteDeadline(time.Minute); err != nil {
			t.Errorf("a net/http connection can extend its write deadline: %v", err)
		}
		// X-TrustAgent-Status was not declared: it goes out with the trailer prefix.
		if err := sw.Finish(&testAnnotation, map[string]string{
			"X-TrustAgent-Action": "flag", "x-trustagent-mode": "flag", "X-TrustAgent-Status": "ok",
		}); err != nil {
			t.Errorf("finish: %v", err)
		}
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type %q", resp.Header.Get("Content-Type"))
	}
	if _, ok := resp.Trailer["X-Trustagent-Action"]; !ok {
		t.Errorf("declared trailers %v", resp.Trailer)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"X-TrustAgent-Action": "flag", "X-TrustAgent-Mode": "flag", "X-TrustAgent-Status": "ok"} {
		if got := resp.Trailer.Get(name); got != want {
			t.Errorf("trailer %s = %q, want %q", name, got, want)
		}
	}
	// The comment arrives just before [DONE], and SDK-style parsing ignores it.
	evs := readAll(t, NewReader(bytes.NewReader(body)))
	n := len(evs)
	if n < 2 || !IsTerminal(FormatOpenAIChat, evs[n-1]) || len(evs[n-2].Comments) != 1 ||
		evs[n-2].Comments[0] != testAnnotation.Comment() {
		t.Fatalf("stream tail %q", tail(string(body), 300))
	}

	if err := NewStreamWriter(httptest.NewRecorder()).ExtendWriteDeadline(time.Second); !errors.Is(err, http.ErrNotSupported) {
		t.Errorf("recorder deadline: %v", err)
	}
}

func TestWriteEventsPlacesTheAnnotation(t *testing.T) {
	events := SynthesizeStream(FormatOpenAIChat, SynthStream{TraceID: "t", Model: "m", Text: "Hi"})
	cases := []struct {
		name   string
		format string
		events []Event
		ann    *Annotation
		want   func(evs []Event) bool
	}{
		{"before [DONE]", FormatOpenAIChat, events, &testAnnotation, func(evs []Event) bool {
			return len(evs) == 4 && len(evs[2].Comments) == 1 && IsTerminal(FormatOpenAIChat, evs[3])
		}},
		{"no terminal: at the end", FormatOpenAIChat, events[:2], &testAnnotation, func(evs []Event) bool {
			return len(evs) == 3 && len(evs[2].Comments) == 1
		}},
		{"no annotation", FormatOpenAIChat, events, nil, func(evs []Event) bool { return len(evs) == 3 }},
		{"anthropic", FormatAnthropicMessages, SynthesizeStream(FormatAnthropicMessages, SynthStream{TraceID: "t", Text: "Hi"}), &testAnnotation,
			func(evs []Event) bool {
				return len(evs) == 7 && len(evs[5].Comments) == 1 && IsTerminal(FormatAnthropicMessages, evs[6])
			}},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		if err := NewStreamWriter(rec).WriteEvents(tc.format, tc.events, tc.ann); err != nil {
			t.Fatal(err)
		}
		if evs := readAll(t, NewReader(rec.Body)); !tc.want(evs) {
			t.Errorf("%s: %q", tc.name, rec.Body.String())
		}
	}
	w := &brokenWriter{header: http.Header{}, failAt: 1}
	if err := NewStreamWriter(w).WriteEvents(FormatOpenAIChat, events, &testAnnotation); !errors.Is(err, errClientGone) {
		t.Errorf("err %v", err)
	}
}

func TestStreamWriterHoldAndRelease(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(rec)
	if err := sw.Release(); err != nil || sw.Holding() {
		t.Fatal("nothing held")
	}
	sw.Hold(DataEvent("", []byte("first")))
	sw.Hold(DataEvent("", []byte("[DONE]")))
	if err := sw.WriteKeepalive(); err != nil {
		t.Fatal(err)
	}
	if err := sw.Release(); err != nil || sw.Holding() {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != ": keepalive\n\ndata: [DONE]\n\n" {
		t.Errorf("body %q", got)
	}
}

// slowStream writes parts to a pipe with pauses, then closes it.
func slowStream(parts []string, pause time.Duration) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for _, p := range parts {
			time.Sleep(pause)
			if _, err := pw.Write([]byte(p)); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	return pr
}

func TestCollect(t *testing.T) {
	src := fixture(t, "anthropic_basic.sse")
	asm, _ := NewStreamAssembler(FormatAnthropicMessages)
	res := Collect(NewReader(bytes.NewReader(src)), asm, CollectOptions{})
	if !res.Terminated || res.TooLarge || res.UpstreamErr != nil || len(res.Events) != 8 || res.Bytes != int64(len(src)) {
		t.Fatalf("%+v", res)
	}
	// Delivered as is after the decision: the upstream bytes plus the comment before message_stop.
	rec := httptest.NewRecorder()
	if err := NewStreamWriter(rec).WriteEvents(FormatAnthropicMessages, res.Events, &testAnnotation); err != nil {
		t.Fatal(err)
	}
	terminal := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if want := strings.TrimSuffix(string(src), terminal) + ": " + testAnnotation.Comment() + "\n\n" + terminal; rec.Body.String() != want {
		t.Errorf("replayed %q", tail(rec.Body.String(), 300))
	}

	// Over the byte limit: collection stops and the Reader goes on where it left off.
	r := NewReader(bytes.NewReader(src))
	asm, _ = NewStreamAssembler(FormatAnthropicMessages)
	res = Collect(r, asm, CollectOptions{MaxBytes: 300})
	if !res.TooLarge || res.Terminated || res.Bytes <= 300 {
		t.Fatalf("%+v", res)
	}
	rest := Relay(r, asm, NewStreamWriter(httptest.NewRecorder()))
	if res.Bytes+rest.Bytes != int64(len(src)) || !rest.Held {
		t.Errorf("collected %d + relayed %d of %d", res.Bytes, rest.Bytes, len(src))
	}

	boom := errors.New("reset")
	asm, _ = NewStreamAssembler(FormatAnthropicMessages)
	res = Collect(NewReader(&failAfter{data: src[:200], err: boom}), asm, CollectOptions{})
	if !errors.Is(res.UpstreamErr, boom) || res.Terminated {
		t.Errorf("%+v", res)
	}
}

func TestCollectSendsKeepalives(t *testing.T) {
	src := string(fixture(t, "openai_chat_basic.sse"))
	half := strings.Index(src, "data: {\"id\":\"chatcmpl-B9x1\",\"object\":\"chat.completion.chunk\",\"created\":1760000000,\"model\":\"gpt-4o-mini-2024-07-18\",\"system_fingerprint\":\"fp_1\",\"choices\":[{\"index\":0,\"delta\":{},")
	rec := httptest.NewRecorder()
	sw := NewStreamWriter(rec)
	asm, _ := NewStreamAssembler(FormatOpenAIChat)
	res := Collect(NewReader(slowStream([]string{src[:half], src[half:]}, 60*time.Millisecond)), asm,
		CollectOptions{Keepalive: 10 * time.Millisecond, Writer: sw})
	if !res.Terminated || res.Keepalives == 0 || res.ClientErr != nil {
		t.Fatalf("%+v", res)
	}
	if !sw.Committed() || !strings.HasPrefix(rec.Body.String(), ": keepalive\n\n") {
		t.Errorf("the first keepalive commits the headers: %q", rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "data:") != 0 {
		t.Error("nothing of the answer goes out while it is collected")
	}
	if c, _ := asm.Response().Gated(); c.Text != "Refunds are available within 30 days — <b>no</b> exceptions." {
		t.Errorf("text %q", c.Text)
	}
}

func TestCollectStopsWhenTheClientLeaves(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		_, _ = pw.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\n\n"))
	}()
	asm, _ := NewStreamAssembler(FormatOpenAIChat)
	w := &brokenWriter{header: http.Header{}, failAt: 1}
	done := make(chan CollectResult, 1)
	go func() {
		done <- Collect(NewReader(pr), asm, CollectOptions{Keepalive: 5 * time.Millisecond, Writer: NewStreamWriter(w)})
	}()
	select {
	case res := <-done:
		if !errors.Is(res.ClientErr, errClientGone) || res.Terminated {
			t.Errorf("%+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Collect did not return after the client left")
	}
	// Closing the upstream ends the read still in flight.
	_ = pr.CloseWithError(io.ErrClosedPipe)
}

// clientEvents parses a relayed body the way an SSE client does: events that never got their
// blank line are dropped.
func clientEvents(t *testing.T, body []byte) []Event {
	t.Helper()
	var out []Event
	for _, ev := range readAll(t, NewReader(bytes.NewReader(body))) {
		if !ev.Incomplete {
			out = append(out, ev)
		}
	}
	return out
}

func TestNothingFollowsAnUnterminatedTail(t *testing.T) {
	complete := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"
	comment := ": " + testAnnotation.Comment() + "\n\n"
	cases := []struct {
		name, tail string
		held       bool
	}{
		// The upstream stopped inside a data line: the comment must not become the rest of it.
		{"open data line", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"}", false},
		// A whole line but no blank line: the comment's blank line must not dispatch it.
		{"no blank line", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"}}]}\r\n", false},
		{"cr-terminated line", "data: {\"choices\":[]}\r", false},
		// [DONE] without its blank line is still the terminal event, held and released last.
		{"unterminated [DONE]", "data: [DONE]\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := complete + tc.tail
			rec := httptest.NewRecorder()
			sw := NewStreamWriter(rec)
			asm, _ := NewStreamAssembler(FormatOpenAIChat)
			res := Relay(NewReader(&chunked{src: []byte(src), sizes: []int{7, 3}}), asm, sw)
			if res.Held != tc.held || res.UpstreamErr != nil || res.ClientErr != nil || res.Bytes != int64(len(src)) {
				t.Fatalf("%+v", res)
			}
			if rec.Body.String() != complete {
				t.Fatalf("the tail went out before the decision: %q", rec.Body.String())
			}
			if err := sw.Finish(&testAnnotation, nil); err != nil {
				t.Fatal(err)
			}
			if want := complete + comment + tc.tail; rec.Body.String() != want {
				t.Errorf("body %q\nwant %q", rec.Body.String(), want)
			}
			// The client reads the complete events and the comment, nothing malformed.
			evs := clientEvents(t, rec.Body.Bytes())
			if len(evs) != 2 || len(evs[1].Comments) != 1 || evs[1].Comments[0] != testAnnotation.Comment() || evs[1].HasData {
				t.Errorf("client events %+v", evs)
			}
		})
	}
}

func TestWriteEventsKeepsAnUnterminatedTailLast(t *testing.T) {
	src := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"choi"
	collect := func() []Event {
		asm, _ := NewStreamAssembler(FormatOpenAIChat)
		res := Collect(NewReader(strings.NewReader(src)), asm, CollectOptions{})
		if len(res.Events) != 2 || !res.Events[1].Incomplete {
			t.Fatalf("%+v", res)
		}
		return res.Events
	}
	want := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\n: " + testAnnotation.Comment() + "\n\ndata: {\"choi"

	// Delivered after the decision with the annotation: the tail goes out after it.
	rec := httptest.NewRecorder()
	if err := NewStreamWriter(rec).WriteEvents(FormatOpenAIChat, collect(), &testAnnotation); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != want {
		t.Errorf("body %q", rec.Body.String())
	}

	// Without an annotation the tail waits for Finish, which writes the annotation first.
	rec = httptest.NewRecorder()
	sw := NewStreamWriter(rec)
	if err := sw.WriteEvents(FormatOpenAIChat, collect(), nil); err != nil {
		t.Fatal(err)
	}
	if err := sw.Finish(&testAnnotation, nil); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != want {
		t.Errorf("body %q", rec.Body.String())
	}
	if err := sw.WriteEvent(Event{Raw: []byte("data: x"), Incomplete: true}); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("a tail after Finish: %v", err)
	}

	// A client that is gone gets the sticky error, not a silent keep-back.
	w := &brokenWriter{header: http.Header{}, failAt: 1}
	sw = NewStreamWriter(w)
	_ = sw.WriteKeepalive()
	if err := sw.WriteEvent(Event{Raw: []byte("data: x"), Incomplete: true}); !errors.Is(err, errClientGone) {
		t.Errorf("a tail after a failed write: %v", err)
	}
}
