package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
)

// The stream tests serve a scripted upstream stream through serveStream the way Serve does for a
// 2xx text/event-stream answer, on a real HTTP server, and read what a client gets: events,
// headers and trailers. Helper names carry the strm prefix.

// strmPolicy is a policy whose defaults route (every request) has the given settings and the two
// evidence checks the demo scenarios script answers for.
func strmPolicy(settings string) string {
	return "version: 1\ndefaults: {" + settings + ", require_calibration: false, " +
		"evaluators: [{ref: groundedness@1}, {ref: contradiction@1}]}\n"
}

// strmScenario is a demo scenario's request and scripted answer (contracts/demo/scenarios.v1.json).
type strmScenario struct {
	ID       string
	Request  map[string]json.RawMessage
	Response string
}

func loadStrmScenario(t *testing.T, id string) strmScenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID       string                     `json:"id"`
			Request  map[string]json.RawMessage `json:"request"`
			Response string                     `json:"response"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	for _, s := range pack.Scenarios {
		if s.ID == id {
			return strmScenario{ID: s.ID, Request: s.Request, Response: s.Response}
		}
	}
	t.Fatalf("no scenario %s", id)
	return strmScenario{}
}

// body is the scenario's request as a streaming request of format (the OpenAI chat request as it
// is; for Anthropic its system message moves to system; for completions its question is the
// prompt and its document the trustagent.context), with extra members merged in.
func (s strmScenario) body(t *testing.T, format string, extra map[string]any) string {
	t.Helper()
	var msgs []struct{ Role, Content string }
	if err := json.Unmarshal(s.Request["messages"], &msgs); err != nil {
		t.Fatal(err)
	}
	var system, user string
	for _, m := range msgs {
		switch m.Role {
		case "system":
			system = m.Content
		case "user":
			user = m.Content
		}
	}
	m := map[string]any{"model": "gpt-4o-mini", "stream": true}
	switch format {
	case llmapi.FormatAnthropicMessages:
		m["model"], m["system"], m["max_tokens"] = "claude-sonnet-4", system, 256
		m["messages"] = []any{map[string]any{"role": "user", "content": user}}
	case llmapi.FormatOpenAICompletions:
		m["model"], m["prompt"] = "gpt-3.5-turbo-instruct", user
		m["trustagent"] = map[string]any{"context": []any{map[string]any{"id": "refund-policy.md", "text": system}}}
	default:
		m["messages"] = s.Request["messages"]
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// strmSink records what the handler hands the sink, in order.
type strmSink struct {
	gate.NopSink
	mu       sync.Mutex
	decided  chan *gate.Result
	attempts []*gate.Result
	calls    []string
}

func newStrmSink() *strmSink { return &strmSink{decided: make(chan *gate.Result, 16)} }

func (s *strmSink) GateDecided(_ context.Context, r *gate.Result) string {
	s.mu.Lock()
	s.calls = append(s.calls, "decided")
	s.mu.Unlock()
	s.decided <- r
	if r.Audit.Selection != "" {
		return gate.AuditQueued
	}
	return r.Audit.Status
}

func (s *strmSink) AttemptDecided(_ context.Context, r *gate.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "attempt")
	s.attempts = append(s.attempts, r)
}

func (s *strmSink) result(t *testing.T) *gate.Result {
	t.Helper()
	select {
	case r := <-s.decided:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no gate decision reached the sink")
		return nil
	}
}

func (s *strmSink) none(t *testing.T) {
	t.Helper()
	select {
	case r := <-s.decided:
		t.Fatalf("a second decision reached the sink: %+v", r.Decision)
	case <-time.After(50 * time.Millisecond):
	}
}

// strmUpstream answers every request with a scripted event stream.
type strmUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	events []string
	// delay comes before the first event, gap between events; step, when set, holds each event
	// after the first until the test sends on it.
	delay, gap time.Duration
	step       chan struct{}
	// stall keeps the stream open after the events until the request goes away (gone).
	stall bool
	gone  chan struct{}
}

func newStrmUpstream(t *testing.T, events ...string) *strmUpstream {
	t.Helper()
	u := &strmUpstream{events: events, gone: make(chan struct{}, 4)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		u.mu.Lock()
		events, delay, gap, step, stall := u.events, u.delay, u.gap, u.step, u.stall
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		wait := func(d time.Duration) bool {
			select {
			case <-time.After(d):
				return true
			case <-r.Context().Done():
				u.gone <- struct{}{}
				return false
			}
		}
		if !wait(delay) {
			return
		}
		for i, ev := range events {
			if i > 0 {
				if step != nil {
					select {
					case <-step:
					case <-r.Context().Done():
						u.gone <- struct{}{}
						return
					}
				}
				if !wait(gap) {
					return
				}
			}
			_, _ = io.WriteString(w, ev)
			w.(http.Flusher).Flush()
		}
		if stall {
			<-r.Context().Done()
			u.gone <- struct{}{}
		}
	}))
	t.Cleanup(u.Close)
	return u
}

// strmChunk is one OpenAI chat chunk of the upstream answer.
func strmChunk(delta string, finish string) string {
	d := map[string]any{}
	if delta != "" {
		d["content"] = delta
	}
	var fr any
	if finish != "" {
		fr = finish
	}
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-up", "object": "chat.completion.chunk", "created": 1760000000, "model": "gpt-4o-mini",
		"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": fr}},
	})
	return "data: " + string(b) + "\n\n"
}

// strmOpenAIStream is an OpenAI chat stream answering text in two deltas, with a usage chunk.
func strmOpenAIStream(text string) []string {
	half := strings.LastIndex(text[:len(text)/2+1], " ") + 1
	return []string{
		strmChunk(text[:half], ""),
		strmChunk(text[half:], ""),
		strmChunk("", "stop"),
		`data: {"id":"chatcmpl-up","object":"chat.completion.chunk","created":1760000000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":17,"total_tokens":59}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

// strmAnthropicStream is an Anthropic Messages stream answering text in two text deltas.
func strmAnthropicStream(text string) []string {
	half := len(text) / 2
	delta := func(s string) string {
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": s}})
		return "event: content_block_delta\ndata: " + string(b) + "\n\n"
	}
	return []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_up\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":31,\"output_tokens\":1}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: ping\ndata: {\"type\":\"ping\"}\n\n",
		delta(text[:half]),
		delta(text[half:]),
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":19}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
}

type strmOpts struct {
	policyYAML string
	jevLatency time.Duration
	maxBytes   int64
	maxGates   int
	// idle and total are the upstream client's timeouts (default 5 s and 10 s; negative: none).
	idle, total time.Duration
	// writeTimeout is the gateway server's WriteTimeout (0: none).
	writeTimeout time.Duration
	// synth, when set, answers every request with a synthesized test_response stream of it.
	synth string
}

// strmEnv is a gateway handler with a scripted decision model, serving streams on a real server.
type strmEnv struct {
	h    *Handler
	sink *strmSink
	pol  *policy.Policy
	up   *upstream.Client
	srv  *httptest.Server
	// flows are the exchanges served, by trace id.
	mu    sync.Mutex
	flows map[string]*flow
}

func newStrmEnv(t *testing.T, u *strmUpstream, o strmOpts) *strmEnv {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	res := policy.Check([]byte(o.policyYAML), policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg, Enforcing: true})
	if res.Policy == nil {
		t.Fatalf("policy: %v", res.Err())
	}
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true, Latency: o.jevLatency}})
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.DiscardHandler)
	g, err := gate.New(gate.Config{Jev: cl, Evaluators: reg, UniformAuditsPerDay: 50, Rand: func() float64 { return 0.5 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	idle, total := o.idle, o.total
	if idle == 0 {
		idle = 5 * time.Second
	}
	if total == 0 {
		total = 10 * time.Second
	}
	base := "http://upstream.invalid"
	if u != nil {
		base = u.URL
	}
	up, err := upstream.New(upstream.Config{BaseURL: base, IdleTimeout: max(idle, 0), Timeout: max(total, 0)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &strmEnv{sink: newStrmSink(), pol: res.Policy, up: up, flows: map[string]*flow{}}
	e.h = New(Options{
		Upstreams: &upstream.Set{OpenAI: up, Anthropic: up}, Policy: func() *policy.Policy { return e.pol }, Gate: g,
		Sink: e.sink, TestMode: true, MaxUpstreamResponseBytes: o.maxBytes, GateMaxConcurrency: o.maxGates, Logger: quiet,
	})
	r := gin.New()
	r.POST("/v1/*path", func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		f := e.newFlow(t, c, raw)
		if o.synth != "" {
			e.h.serveStream(f, nil, e.h.synthStream(f, o.synth))
			return
		}
		resp, cancel, err := e.up.Do(c.Request.Context(), http.MethodPost, c.Request.URL.Path, "", c.Request.Header, raw)
		if err != nil {
			t.Errorf("upstream call: %v", err)
			c.Status(http.StatusBadGateway)
			return
		}
		e.h.serveStream(f, resp, e.up.StreamBody(resp, cancel))
	})
	e.srv = httptest.NewUnstartedServer(r)
	e.srv.Config.WriteTimeout = o.writeTimeout
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.h.Drain(ctx)
	})
	return e
}

// newFlow builds the gated exchange of a request as the handler does: format from the path, the
// route and mode from the policy, evidence from the request, the scenario from its header.
func (e *strmEnv) newFlow(t *testing.T, c *gin.Context, raw []byte) *flow {
	format := llmapi.FormatForPath(c.Request.URL.Path)
	req, err := llmapi.ParseRequest(format, raw)
	if err != nil {
		t.Errorf("request: %v", err)
	}
	route := e.pol.Match(&policy.Request{Path: c.Request.URL.Path, Model: req.Model, Headers: c.Request.Header})
	mode, _ := e.pol.Mode(route, "")
	traceID := store.NewID()
	c.Header(HeaderTraceID, traceID)
	c.Header(HeaderRequestID, traceID)
	start := time.Now()
	ts := &gate.TraceStart{
		TraceID: traceID, CreatedAt: start.UTC(), Source: gate.SourceTrafficgen, APIFormat: format, Path: c.Request.URL.Path,
		Route: route.Name, PolicyVersion: e.pol.Version, Mode: mode, Model: req.Model, Stream: true,
		Scenario: c.GetHeader(HeaderScenario), AnswerSource: gate.AnswerLive, Flags: []string{},
	}
	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(req, &settings)
	if err != nil {
		t.Errorf("evidence: %v", err)
	}
	f := &flow{
		c: c, format: format, req: req, raw: raw, route: route, up: e.up, start: start,
		x:      &gate.Exchange{Trace: ts, Policy: e.pol, Route: route, Request: req, Evidence: ev},
		record: context.WithoutCancel(c.Request.Context()),
	}
	e.mu.Lock()
	e.flows[traceID] = f
	e.mu.Unlock()
	return f
}

// call sends a streaming request and reads the whole answer (then resp.Trailer is filled in).
func (e *strmEnv) call(t *testing.T, path, body, scenario string) (*http.Response, []byte) {
	t.Helper()
	resp := e.open(t, context.Background(), path, body, scenario)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v (got %q)", err, b)
	}
	return resp, b
}

func (e *strmEnv) open(t *testing.T, ctx context.Context, path, body, scenario string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if scenario != "" {
		req.Header.Set(HeaderScenario, scenario)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// strmEvents parses a client's stream.
func strmEvents(t *testing.T, body []byte) []llmapi.Event {
	t.Helper()
	rd := llmapi.NewReader(bytes.NewReader(body))
	var out []llmapi.Event
	for {
		ev, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("the client's stream does not parse: %v\n%s", err, body)
		}
		out = append(out, ev)
	}
}

// strmAnnotation finds the ": trustagent {...}" comment of a stream and its position.
func strmAnnotation(t *testing.T, evs []llmapi.Event) (llmapi.Annotation, int) {
	t.Helper()
	for i, ev := range evs {
		for _, c := range ev.Comments {
			if rest, ok := strings.CutPrefix(c, llmapi.AnnotationPrefix); ok {
				var a llmapi.Annotation
				if err := json.Unmarshal([]byte(rest), &a); err != nil {
					t.Fatalf("annotation %q: %v", c, err)
				}
				return a, i
			}
		}
	}
	return llmapi.Annotation{}, -1
}

// strmText is the gated text of a client's stream.
func strmText(t *testing.T, format string, body []byte) (string, *llmapi.StreamAssembler) {
	t.Helper()
	asm, err := llmapi.NewStreamAssembler(format)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range strmEvents(t, body) {
		asm.Add(ev)
	}
	c, _ := asm.Response().Gated()
	return c.Text, asm
}

// TestStreamRelayFlushesEventsAndHoldsTheTerminalEvent (GS-5): each event reaches the client as
// the upstream sends it; the terminal event waits for the decision, which goes out first as the
// ": trustagent {...}" comment and after the body as trailers a Go http.Client reads. Route,
// Trace-Id and Request-ID come up front, the rest is declared in Trailer (D9).
func TestStreamRelayFlushesEventsAndHoldsTheTerminalEvent(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	events := strmOpenAIStream(sc.Response)
	u := newStrmUpstream(t, events...)
	u.step = make(chan struct{})
	e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag"), jevLatency: 150 * time.Millisecond})

	resp := e.open(t, context.Background(), "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	defer resp.Body.Close()
	rd := llmapi.NewReader(resp.Body)
	first, err := rd.Next()
	if err != nil || string(first.Raw) != events[0] {
		t.Fatalf("the first event before the upstream sent the second: %q %v", first.Raw, err)
	}
	var got []llmapi.Event
	var finishedAt, doneAt time.Time
	for i := 1; ; i++ {
		if i < len(events) {
			u.step <- struct{}{}
		}
		ev, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
		switch {
		case strings.Contains(string(ev.Data), `"finish_reason":"stop"`):
			finishedAt = time.Now()
		case llmapi.IsTerminal(llmapi.FormatOpenAIChat, ev):
			doneAt = time.Now()
		}
	}
	all := append([]llmapi.Event{first}, got...)
	n := len(all)
	if n != len(events)+1 || !llmapi.IsTerminal(llmapi.FormatOpenAIChat, all[n-1]) {
		t.Fatalf("%d events, want the %d upstream events and the annotation", n, len(events))
	}
	for i := 0; i < len(events)-1; i++ {
		if string(all[i].Raw) != events[i] {
			t.Errorf("event %d changed:\n%q\nwant\n%q", i, all[i].Raw, events[i])
		}
	}
	a, at := strmAnnotation(t, all)
	if at != n-2 {
		t.Fatalf("the annotation is event %d, want %d (just before [DONE])", at, n-2)
	}
	if d := doneAt.Sub(finishedAt); d < 100*time.Millisecond {
		t.Errorf("[DONE] came %s after the finish reason: it did not wait for the decision (150 ms)", d)
	}
	traceID := resp.Header.Get(HeaderTraceID)
	if a.TraceID != traceID || a.Action != decision.ActionFlag || a.WouldAction != decision.ActionBlock || a.Status != "ok" ||
		a.Verdict == nil || *a.Verdict != "contradicted" || !slices.Contains(a.Reasons, "contradiction:critical") {
		t.Errorf("annotation %+v", a)
	}

	for name, want := range map[string]string{
		HeaderTraceID: traceID, HeaderRequestID: traceID, HeaderRoute: e.pol.Label(e.pol.Match(&policy.Request{Path: "/v1/chat/completions"})),
		"X-Upstream": "yes", "Content-Type": "text/event-stream; charset=utf-8",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{HeaderAction, HeaderMode, HeaderStatus} {
		if resp.Header.Get(name) != "" {
			t.Errorf("%s is a trailer, not a header", name)
		}
	}
	if resp.Header.Get(HeaderSource) != gate.SourceTrafficgen {
		t.Errorf("Source up front: %q", resp.Header.Get(HeaderSource))
	}
	for name, want := range map[string]string{
		HeaderAction: "flag", HeaderMode: "flag", HeaderWouldAction: "block", HeaderStatus: "ok", HeaderVerdict: "contradicted",
		HeaderCheckedAgainst: "request_system:1", HeaderAudit: gate.AuditQueued,
	} {
		if got := resp.Trailer.Get(name); got != want {
			t.Errorf("trailer %s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{HeaderRisk, HeaderReasons, HeaderGateMs, HeaderOverheadMs, HeaderDecisionModel, HeaderCalibrated} {
		if resp.Trailer.Get(name) == "" {
			t.Errorf("trailer %s missing: %v", name, resp.Trailer)
		}
	}

	r := e.sink.result(t)
	if r.Decision.EffectiveMode != decision.ModeFlag || r.Decision.Action != decision.ActionFlag || !r.Decision.Enforced ||
		r.ResponseText != sc.Response || r.Final != nil {
		t.Errorf("decision %s/%s enforced %v text %q final %+v", r.Decision.EffectiveMode, r.Decision.Action, r.Decision.Enforced, r.ResponseText, r.Final)
	}
	up := r.Upstream
	if up.Status != http.StatusOK || up.Ms == nil || up.TTFTMs == nil || *up.TTFTMs > *up.Ms ||
		up.PromptTokens == nil || *up.PromptTokens != 42 || up.CompletionTokens == nil || *up.CompletionTokens != 17 {
		t.Errorf("upstream record %+v", up)
	}
	if r.TotalMs == nil || r.OverheadMs == nil || *r.OverheadMs < 100 {
		t.Errorf("the held terminal event is gateway overhead: total %v overhead %v", r.TotalMs, r.OverheadMs)
	}
}

// TestStreamOnABlockRouteIsAnEffectiveFlag: a passthrough stream cannot be taken back, so block
// and fallback routes flag it instead (stream_not_buffered).
func TestStreamOnABlockRouteIsAnEffectiveFlag(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	for _, mode := range []string{"block", "fallback"} {
		u := newStrmUpstream(t, strmOpenAIStream(sc.Response)...)
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: " + mode + ", fail: closed, streaming: passthrough")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if text, _ := strmText(t, llmapi.FormatOpenAIChat, body); text != sc.Response {
			t.Errorf("%s: the client got %q", mode, text)
		}
		r := e.sink.result(t)
		if r.Decision.EffectiveMode != decision.ModeFlag || r.Decision.Action != decision.ActionFlag ||
			r.Decision.WouldAction != decision.ActionBlock || !slices.Contains(r.Decision.Reasons, decision.ReasonStreamNotBuffered) {
			t.Errorf("%s: %s/%s would %s reasons %v", mode, r.Decision.EffectiveMode, r.Decision.Action, r.Decision.WouldAction, r.Decision.Reasons)
		}
		if resp.Trailer.Get(HeaderMode) != "flag" || resp.Trailer.Get(HeaderAction) != "flag" ||
			!strings.Contains(resp.Trailer.Get(HeaderReasons), decision.ReasonStreamNotBuffered) {
			t.Errorf("%s: trailers %v", mode, resp.Trailer)
		}
	}
}

// TestStreamObserveAnnotationsNoneAndActionOnly: observe routes send the deferred headers and
// hold nothing (the body is the upstream's, byte for byte) and are gated afterwards;
// stream_annotations: none writes no comment; headers: action_only declares Action alone.
func TestStreamObserveAnnotationsNoneAndActionOnly(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	events := strmOpenAIStream(sc.Response)
	upstreamBody := strings.Join(events, "")

	t.Run("observe", func(t *testing.T) {
		e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: observe, streaming: buffer")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if string(body) != upstreamBody {
			t.Errorf("observe changed the stream:\n%s", body)
		}
		if resp.Header.Get(HeaderAction) != decision.ActionObserve || resp.Header.Get(HeaderStatus) != StatusDeferred ||
			len(resp.Trailer) != 0 {
			t.Errorf("observe headers %v trailers %v", resp.Header, resp.Trailer)
		}
		r := e.sink.result(t)
		if r.Decision.Action != decision.ActionObserve || r.Decision.WouldAction != decision.ActionBlock || r.ResponseText != sc.Response {
			t.Errorf("the gate ran afterwards: %s would %s text %q", r.Decision.Action, r.Decision.WouldAction, r.ResponseText)
		}
	})
	t.Run("observe unterminated", func(t *testing.T) {
		// An upstream that stops in the middle of an event: the client gets that tail too.
		cut := append(slices.Clone(events[:3]), `data: {"choices":[{"index":0,"delta":{"content":"tr`)
		e := newStrmEnv(t, newStrmUpstream(t, cut...), strmOpts{policyYAML: strmPolicy("mode: observe")})
		_, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if string(body) != strings.Join(cut, "") {
			t.Errorf("observe dropped the tail:\n%s", body)
		}
		e.sink.result(t)
	})
	t.Run("annotations none", func(t *testing.T) {
		e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: flag, stream_annotations: none")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if string(body) != upstreamBody {
			t.Errorf("no annotation, so the stream is the upstream's:\n%s", body)
		}
		if resp.Trailer.Get(HeaderAction) != "flag" {
			t.Errorf("the trailers still carry the decision: %v", resp.Trailer)
		}
		e.sink.result(t)
	})
	t.Run("action only", func(t *testing.T) {
		e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: flag, headers: action_only")})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		// A Go client moves the declared names from the Trailer header into resp.Trailer.
		if len(resp.Trailer) != 1 || resp.Trailer.Get(HeaderAction) != "flag" {
			t.Errorf("action_only trailers %v", resp.Trailer)
		}
		if resp.Header.Get(HeaderRoute) != "" || resp.Header.Get(HeaderSource) != "" || resp.Header.Get(HeaderTraceID) == "" {
			t.Errorf("action_only up front: %v", resp.Header)
		}
		if _, at := strmAnnotation(t, strmEvents(t, body)); at < 0 {
			t.Error("the annotation does not depend on the headers setting")
		}
		e.sink.result(t)
	})
}

// TestStreamOversizeEventAndAnswer: an event past the size limit sends the rest of the stream as
// it is; an answer past it is relayed whole with the terminal event still held; neither is gated
// (skipped:upstream_error).
func TestStreamOversizeEventAndAnswer(t *testing.T) {
	big := strmChunk(strings.Repeat("x", 3000), "")
	many := []string{}
	for range 40 {
		many = append(many, strmChunk("Refunds take thirty days. ", ""))
	}
	for name, events := range map[string][]string{
		"event":  {strmChunk("Hello ", ""), big, strmChunk("", "stop"), "data: [DONE]\n\n"},
		"answer": append(append(many, strmChunk("", "stop")), "data: [DONE]\n\n"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: flag"), maxBytes: 2048})
			sc := loadStrmScenario(t, "grounded-pass")
			resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			upstreamBody := strings.Join(events, "")
			evs := strmEvents(t, body)
			a, at := strmAnnotation(t, evs)
			if at < 0 || a.Status != "skipped:upstream_error" {
				t.Fatalf("annotation %+v at %d", a, at)
			}
			// The client gets every upstream byte; the annotation comes before [DONE] when the
			// terminal event could still be held, else last.
			without := bytes.Replace(body, evs[at].Raw, nil, 1)
			if at == len(evs)-1 {
				without = bytes.TrimSuffix(body, evs[at].Raw)
			}
			if string(without) != upstreamBody {
				t.Errorf("the stream changed")
			}
			if name == "answer" && at != len(evs)-2 {
				t.Errorf("an answer past the limit still holds [DONE]: annotation at %d of %d", at, len(evs))
			}
			if resp.Trailer.Get(HeaderStatus) != "skipped:upstream_error" {
				t.Errorf("trailers %v", resp.Trailer)
			}
			r := e.sink.result(t)
			if r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError || r.ResponseText != "" {
				t.Errorf("skip %v text %q", r.Decision.SkipReason, r.ResponseText)
			}
		})
	}
}

// TestStreamClientDisconnectAfterVersusBeforeFinish (D7): a client that leaves after the finish
// reason still gets its answer gated, unenforced and audited with the client_disconnected flag; one
// that leaves before it is skipped as client_disconnected.
func TestStreamClientDisconnectAfterVersusBeforeFinish(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	events := strmOpenAIStream(sc.Response)
	for _, c := range []struct {
		name   string
		events []string
		finish bool
	}{
		{"after the finish reason", events[:3], true},
		{"before the finish reason", events[:1], false},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newStrmUpstream(t, c.events...)
			u.stall = true
			e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag")})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp := e.open(t, ctx, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			rd := llmapi.NewReader(resp.Body)
			for range c.events {
				if _, err := rd.Next(); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			_ = resp.Body.Close()
			select {
			case <-u.gone:
			case <-time.After(5 * time.Second):
				t.Fatal("the upstream call was not cancelled")
			}
			r := e.sink.result(t)
			d := r.Decision
			if c.finish {
				if d.Enforced || !slices.Contains(r.Flags, gate.FlagClientDisconnected) || r.Audit.Selection == "" ||
					d.SkipReason != nil || r.ResponseText != sc.Response {
					t.Errorf("enforced %v flags %v audit %+v skip %v text %q", d.Enforced, r.Flags, r.Audit, d.SkipReason, r.ResponseText)
				}
				return
			}
			if d.SkipReason == nil || *d.SkipReason != gate.SkipClientDisconnected || slices.Contains(r.Flags, gate.FlagClientDisconnected) {
				t.Errorf("skip %v flags %v", d.SkipReason, r.Flags)
			}
		})
	}
}

// TestStreamTTFT: the time to the first event counts from the upstream call.
func TestStreamTTFT(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	u := newStrmUpstream(t, strmOpenAIStream(sc.Response)...)
	u.delay, u.gap = 150*time.Millisecond, 30*time.Millisecond
	e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag")})
	e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
	up := e.sink.result(t).Upstream
	if up.TTFTMs == nil || *up.TTFTMs < 150 || up.Ms == nil || *up.Ms < *up.TTFTMs+90 {
		t.Errorf("ttft %v ms %v", up.TTFTMs, up.Ms)
	}
}

// TestStreamAnthropicParity: an Anthropic stream is relayed and gated the same way: message_stop is
// held, the annotation precedes it, the trailers follow, and the text and usage are the stream's.
func TestStreamAnthropicParity(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	events := strmAnthropicStream(sc.Response)
	e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: flag")})
	resp, body := e.call(t, "/v1/messages", sc.body(t, llmapi.FormatAnthropicMessages, nil), sc.ID)
	evs := strmEvents(t, body)
	n := len(evs)
	a, at := strmAnnotation(t, evs)
	if n != len(events)+1 || at != n-2 || evs[n-1].Type() != "message_stop" || a.Action != "flag" || a.WouldAction != "block" {
		t.Fatalf("%d events, annotation %+v at %d:\n%s", n, a, at, body)
	}
	if !bytes.HasPrefix(body, []byte(strings.Join(events[:len(events)-1], ""))) {
		t.Error("the events before message_stop are the upstream's")
	}
	if resp.Trailer.Get(HeaderAction) != "flag" || resp.Trailer.Get(HeaderVerdict) != "contradicted" {
		t.Errorf("trailers %v", resp.Trailer)
	}
	r := e.sink.result(t)
	if r.ResponseText != sc.Response || r.Upstream.PromptTokens == nil || *r.Upstream.PromptTokens != 31 ||
		r.Upstream.CompletionTokens == nil || *r.Upstream.CompletionTokens != 19 || r.Decision.WouldAction != decision.ActionBlock {
		t.Errorf("text %q usage %v/%v would %s", r.ResponseText, r.Upstream.PromptTokens, r.Upstream.CompletionTokens, r.Decision.WouldAction)
	}
}

// TestStreamSynthesizedTestResponse (GS-4): a stream:true test_response is a valid event stream of
// the request's format (the text in one delta, the finish reason, the usage chunk under
// include_usage, the terminal event), gated like an upstream stream.
func TestStreamSynthesizedTestResponse(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	for _, c := range []struct{ path, format string }{
		{"/v1/chat/completions", llmapi.FormatOpenAIChat},
		{"/v1/completions", llmapi.FormatOpenAICompletions},
		{"/v1/messages", llmapi.FormatAnthropicMessages},
	} {
		t.Run(c.format, func(t *testing.T) {
			e := newStrmEnv(t, nil, strmOpts{policyYAML: strmPolicy("mode: flag"), synth: sc.Response})
			extra := map[string]any{"stream_options": map[string]any{"include_usage": true}}
			resp, body := e.call(t, c.path, sc.body(t, c.format, extra), sc.ID)
			if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			text, asm := strmText(t, c.format, body)
			evs := strmEvents(t, body)
			last := evs[len(evs)-1]
			if text != sc.Response || !asm.Finished() || !asm.Terminated() || !llmapi.IsTerminal(c.format, last) {
				t.Errorf("text %q finished %v terminated %v:\n%s", text, asm.Finished(), asm.Terminated(), body)
			}
			if _, at := strmAnnotation(t, evs); at != len(evs)-2 {
				t.Errorf("annotation at %d of %d", at, len(evs))
			}
			if c.format == llmapi.FormatOpenAIChat && !strings.Contains(string(body), `"choices":[],"usage"`) {
				t.Errorf("include_usage adds the usage chunk:\n%s", body)
			}
			r := e.sink.result(t)
			if r.ResponseText != sc.Response || r.Upstream.Status != http.StatusOK || *r.Upstream.Ms != 0 || *r.Upstream.TTFTMs != 0 ||
				r.Decision.Action != decision.ActionPass {
				t.Errorf("text %q upstream %+v action %s", r.ResponseText, r.Upstream, r.Decision.Action)
			}
		})
	}
}

// TestStreamKeepsTheWriteDeadlineAhead (GS-7): a stream that outlasts the server's write deadline
// is not cut off while it keeps sending (the deadline is pushed ahead as events go out); without
// an upstream timeout to size that push, the deadline cuts it.
func TestStreamKeepsTheWriteDeadlineAhead(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	events := strmOpenAIStream(sc.Response)
	for _, keep := range []bool{true, false} {
		t.Run(fmt.Sprintf("keep=%v", keep), func(t *testing.T) {
			u := newStrmUpstream(t, events...)
			u.gap = 120 * time.Millisecond
			o := strmOpts{policyYAML: strmPolicy("mode: flag"), writeTimeout: 250 * time.Millisecond}
			if !keep {
				o.idle, o.total = -1, -1
			}
			e := newStrmEnv(t, u, o)
			resp := e.open(t, context.Background(), "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			complete := err == nil && bytes.HasSuffix(body, []byte("data: [DONE]\n\n"))
			if complete != keep {
				t.Errorf("complete %v (err %v), want %v:\n%s", complete, err, keep, body)
			}
			e.sink.result(t)
		})
	}
}

// TestStreamTrailersFollowTheHeadersSetting: the trailer helper sends what setDecisionHeaders
// writes, restricted to the declared names.
func TestStreamTrailersFollowTheHeadersSetting(t *testing.T) {
	verdict, risk := "contradicted", 0.97
	res := &gate.Result{
		Trace: &gate.TraceStart{TraceID: "t1", Source: gate.SourceTest}, RouteLabel: "r@v1", Status: "ok", CheckedAgainst: "none",
		Decision: &gate.Decision{Core: decision.Core{
			EffectiveMode: "flag", Action: "flag", WouldAction: "block", Verdict: &verdict, MaxRisk: &risk, Calibration: "none",
			Reasons: []string{"contradiction:critical"},
		}},
	}
	full := streamTrailers(&policy.Route{}, res, gate.AuditQueued)
	for name, want := range map[string]string{
		HeaderAction: "flag", HeaderMode: "flag", HeaderWouldAction: "block", HeaderStatus: "ok", HeaderVerdict: verdict,
		HeaderRisk: "0.97", HeaderReasons: "contradiction:critical", HeaderAudit: gate.AuditQueued, HeaderCalibrated: "none",
	} {
		if full[name] != want {
			t.Errorf("%s = %q, want %q", name, full[name], want)
		}
	}
	if _, ok := full[HeaderRoute]; ok {
		t.Error("Route goes up front, not in the trailers")
	}
	if _, ok := full[HeaderSource]; ok {
		t.Error("Source goes up front, not in the trailers")
	}
	only := streamTrailers(&policy.Route{Settings: policy.Settings{Headers: policy.HeadersActionOnly}}, res, gate.AuditQueued)
	if len(only) != 1 || only[HeaderAction] != "flag" {
		t.Errorf("action_only trailers %v", only)
	}
}
