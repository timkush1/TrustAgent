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
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/metrics"
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
	// stall keeps the stream open after the events until the request goes away (gone); abort
	// drops the connection after them instead of ending the stream (a read error).
	stall, abort bool
	gone         chan struct{}
	// status is the response status (0: 200); header is set on the response after the event-stream
	// Content-Type (a Content-Type in it replaces that one).
	status int
	header http.Header
	// paths and bodies are the requests received, in order.
	paths  []string
	bodies [][]byte
}

func newStrmUpstream(t *testing.T, events ...string) *strmUpstream {
	t.Helper()
	u := &strmUpstream{events: events, gone: make(chan struct{}, 4)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.paths, u.bodies = append(u.paths, r.URL.Path), append(u.bodies, body)
		events, delay, gap, step, stall, abort, status := u.events, u.delay, u.gap, u.step, u.stall, u.abort, u.status
		for k, v := range u.header {
			w.Header()[k] = v
		}
		u.mu.Unlock()
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		}
		w.Header().Set("X-Upstream", "yes")
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
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
		if abort {
			panic(http.ErrAbortHandler)
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
	// noFlowUpstream leaves the flow without an upstream (a test_response flow can have none).
	noFlowUpstream bool
	// serve routes requests through the handler's own /v1 router (TraceIDs, Mount, Serve), as
	// the gateway does, instead of handing a hand-built flow to serveStream.
	serve bool
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
	set := &upstream.Set{OpenAI: up, Anthropic: up}
	e.h = New(Options{
		Upstreams: func() *upstream.Set { return set }, Policy: func() *policy.Policy { return e.pol }, Gate: g,
		Sink: e.sink, TestMode: true, MaxUpstreamResponseBytes: o.maxBytes, GateMaxConcurrency: o.maxGates, Logger: quiet,
	})
	r := gin.New()
	serve := func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		f := e.newFlow(t, c, raw)
		if o.noFlowUpstream {
			f.up = nil
		}
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
	}
	if o.serve {
		e.h.Mount(r.Group("/v1", e.h.TraceIDs()), nil)
	} else {
		r.POST("/v1/*path", serve)
	}
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

// TestStreamOversizeRestEndsAsTheUpstreamDid: after an event past the size limit the rest goes
// out as it is, however it ends. A rest cut off inside an event gets no annotation (it would join
// the open line; the trailers still carry the decision), and an upstream that stalls during the
// rest is an upstream error, not a client that left.
func TestStreamOversizeRestEndsAsTheUpstreamDid(t *testing.T) {
	sc := loadStrmScenario(t, "grounded-pass")
	big := strmChunk(strings.Repeat("x", 3000), "")
	t.Run("unterminated", func(t *testing.T) {
		events := []string{strmChunk("Hello ", ""), big, `data: {"choices":[{"index":0,"delta":{"content":"tr`}
		e := newStrmEnv(t, newStrmUpstream(t, events...), strmOpts{policyYAML: strmPolicy("mode: flag"), maxBytes: 2048})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if string(body) != strings.Join(events, "") {
			t.Errorf("the client gets the upstream's bytes and nothing after the open event:\n%q", body[max(len(body)-120, 0):])
		}
		if resp.Trailer.Get(HeaderStatus) != "skipped:upstream_error" {
			t.Errorf("trailers %v", resp.Trailer)
		}
		if r := e.sink.result(t); r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError {
			t.Errorf("skip %v", r.Decision.SkipReason)
		}
	})
	t.Run("stalled", func(t *testing.T) {
		u := newStrmUpstream(t, strmChunk("Hello ", ""), big)
		u.stall = true
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag"), maxBytes: 2048, idle: 200 * time.Millisecond})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if !strings.HasPrefix(string(body), strmChunk("Hello ", "")+big) {
			t.Errorf("the rest before the stall reaches the client:\n%q", body)
		}
		if resp.Trailer.Get(HeaderStatus) != "skipped:upstream_error" || resp.Trailer.Get(HeaderAction) == "" {
			t.Errorf("the client is still there and gets the decision: %v", resp.Trailer)
		}
		r := e.sink.result(t)
		if r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipUpstreamError || slices.Contains(r.Flags, gate.FlagClientDisconnected) {
			t.Errorf("skip %v flags %v", r.Decision.SkipReason, r.Flags)
		}
	})
}

// TestStreamClientDisconnectAfterVersusBeforeFinish (D7): a client that leaves after the finish
// reason still gets its answer gated, unenforced and audited with the client_disconnected flag; one
// that leaves before it is skipped as client_disconnected, unenforced too (nothing more reaches it).
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
			if d.SkipReason == nil || *d.SkipReason != gate.SkipClientDisconnected || slices.Contains(r.Flags, gate.FlagClientDisconnected) ||
				d.Enforced {
				t.Errorf("skip %v flags %v enforced %v", d.SkipReason, r.Flags, d.Enforced)
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

// strmCompletionsStream is an OpenAI completions stream answering text in two chunks.
func strmCompletionsStream(text string) []string {
	chunk := func(s, finish string) string {
		c := map[string]any{"index": 0, "text": s, "logprobs": nil, "finish_reason": nil}
		if finish != "" {
			c["finish_reason"] = finish
		}
		b, _ := json.Marshal(map[string]any{
			"id": "cmpl-up", "object": "text_completion", "created": 1760000000, "model": "gpt-3.5-turbo-instruct",
			"choices": []any{c},
		})
		return "data: " + string(b) + "\n\n"
	}
	half := len(text) / 2
	return []string{chunk(text[:half], ""), chunk(text[half:], ""), chunk("", "stop"), "data: [DONE]\n\n"}
}

// strmMetric reads one series of the default registry, matched by all its labels: a counter's
// value or a histogram's sample count (0 when the series does not exist yet).
func strmMetric(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	series:
		for _, m := range f.GetMetric() {
			if len(m.GetLabel()) != len(labels) {
				continue
			}
			for _, l := range m.GetLabel() {
				if v, ok := labels[l.GetName()]; !ok || v != l.GetValue() {
					continue series
				}
			}
			return m.GetCounter().GetValue() + float64(m.GetHistogram().GetSampleCount())
		}
	}
	return 0
}

// strmRequestsTotal is the sum of requests_total over every series.
func strmRequestsTotal(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, f := range families {
		if f.GetName() == "trustagent_gateway_requests_total" {
			for _, m := range f.GetMetric() {
				sum += m.GetCounter().GetValue()
			}
		}
	}
	return sum
}

// strmCountedOnce runs call and checks that it counted exactly one /v1 request: requests_total
// once, with route, format and the delivered action, and overhead_seconds once, with the route's
// configured mode.
func strmCountedOnce(t *testing.T, route, format, mode, action string, call func()) {
	t.Helper()
	label := metrics.RouteLabel(route)
	counter := map[string]string{"route": label, "format": format, "action": action}
	overhead := map[string]string{"route": label, "mode": mode}
	requests, observed, all := strmMetric(t, "trustagent_gateway_requests_total", counter),
		strmMetric(t, "trustagent_gateway_overhead_seconds", overhead), strmRequestsTotal(t)
	call()
	if got := strmMetric(t, "trustagent_gateway_requests_total", counter) - requests; got != 1 {
		t.Errorf("requests_total%v grew by %v, want 1", counter, got)
	}
	if got := strmRequestsTotal(t) - all; got != 1 {
		t.Errorf("requests_total grew by %v in all, want 1", got)
	}
	if got := strmMetric(t, "trustagent_gateway_overhead_seconds", overhead) - observed; got != 1 {
		t.Errorf("overhead_seconds%v observed %v samples, want 1", overhead, got)
	}
}

// TestServeStreamsEveryFormat (GS-8): a streaming request of each API format goes through the
// handler's /v1 router (TraceIDs, Mount, Serve) to the upstream without the gateway's members and
// comes back as the upstream's event stream, gated: the annotation just before the held terminal
// event, the decision in the trailers, the trace recorded with its format, stream true and the
// time to the first event, and the request counted once in requests_total and overhead_seconds. A
// stream:true test_response answers with an event stream of the request's format (GS-4), with no
// upstream call, and counts the same way.
func TestServeStreamsEveryFormat(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	for _, c := range []struct {
		path, format string
		events       []string
	}{
		{"/v1/chat/completions", llmapi.FormatOpenAIChat, strmOpenAIStream(sc.Response)},
		{"/v1/completions", llmapi.FormatOpenAICompletions, strmCompletionsStream(sc.Response)},
		{"/v1/messages", llmapi.FormatAnthropicMessages, strmAnthropicStream(sc.Response)},
	} {
		for _, scripted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/scripted=%v", c.format, scripted), func(t *testing.T) {
				u := newStrmUpstream(t, c.events...)
				e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag"), serve: true})
				// The gateway's members are never forwarded (the completions request carries its
				// document in trustagent.context already).
				extra := map[string]any{}
				if c.format != llmapi.FormatOpenAICompletions {
					extra["trustagent"] = map[string]any{"route": "ignored-by-this-policy"}
				}
				if scripted {
					extra["test_response"] = sc.Response
				}
				route := e.pol.Match(&policy.Request{Path: c.path}).Name
				var resp *http.Response
				var body []byte
				strmCountedOnce(t, route, c.format, decision.ModeFlag, decision.ActionFlag, func() {
					resp, body = e.call(t, c.path, sc.body(t, c.format, extra), sc.ID)
				})
				if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
					t.Fatalf("%d %s:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
				}
				traceID := resp.Header.Get(HeaderTraceID)
				if traceID == "" || resp.Header.Get(HeaderRequestID) != traceID || resp.Header.Get(HeaderRoute) == "" {
					t.Errorf("headers %v", resp.Header)
				}
				text, asm := strmText(t, c.format, body)
				evs := strmEvents(t, body)
				if text != sc.Response || !asm.Terminated() || !llmapi.IsTerminal(c.format, evs[len(evs)-1]) {
					t.Errorf("text %q terminated %v:\n%s", text, asm.Terminated(), body)
				}
				a, at := strmAnnotation(t, evs)
				if at != len(evs)-2 || a.TraceID != traceID || a.Action != decision.ActionFlag || a.WouldAction != decision.ActionBlock {
					t.Errorf("annotation %+v at %d of %d", a, at, len(evs))
				}
				if resp.Trailer.Get(HeaderAction) != decision.ActionFlag || resp.Trailer.Get(HeaderStatus) != "ok" {
					t.Errorf("trailers %v", resp.Trailer)
				}

				r := e.sink.result(t)
				tr := r.Trace
				if tr.TraceID != traceID || tr.APIFormat != c.format || tr.Path != c.path || !tr.Stream || tr.Mode != decision.ModeFlag ||
					r.Decision.Action != decision.ActionFlag || r.ResponseText != sc.Response || r.Upstream.TTFTMs == nil {
					t.Errorf("trace %+v action %s text %q upstream %+v", tr, r.Decision.Action, r.ResponseText, r.Upstream)
				}
				u.mu.Lock()
				paths, bodies := slices.Clone(u.paths), slices.Clone(u.bodies)
				u.mu.Unlock()
				if scripted {
					if tr.AnswerSource != gate.AnswerScripted || len(paths) != 0 {
						t.Errorf("a test_response answers without the upstream: answer source %q, upstream calls %v", tr.AnswerSource, paths)
					}
					return
				}
				if len(paths) != 1 || paths[0] != c.path || bytes.Contains(bodies[0], []byte("trustagent")) || !bytes.Contains(bodies[0], []byte(`"stream":true`)) {
					t.Errorf("upstream calls %v, body %s", paths, bodies)
				}
			})
		}
	}
}

// TestServeStreamRequestsCountOnce (GS-8): every stream path counts its request once, with the
// action it delivered: an observe relay as observe when it is written (the gate runs after), a
// buffered answer with its decision's delivered action (a block here).
func TestServeStreamRequestsCountOnce(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	for _, c := range []struct {
		name, policy, mode, action string
	}{
		{"observe relay", strmPolicy("mode: observe"), decision.ModeObserve, decision.ActionObserve},
		{"buffered block", strmBufferPolicy("block", "", ""), decision.ModeBlock, decision.ActionBlock},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newStrmEnv(t, newStrmUpstream(t, strmOpenAIStream(sc.Response)...), strmOpts{policyYAML: c.policy, serve: true})
			route := e.pol.Match(&policy.Request{Path: "/v1/chat/completions"}).Name
			var resp *http.Response
			strmCountedOnce(t, route, llmapi.FormatOpenAIChat, c.mode, c.action, func() {
				resp, _ = e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			})
			if resp.Header.Get(HeaderAction) != c.action {
				t.Errorf("action %q", resp.Header.Get(HeaderAction))
			}
			if r := e.sink.result(t); deliveredAction(r) != c.action {
				t.Errorf("recorded action %q", deliveredAction(r))
			}
		})
	}
}

// TestServeStreamBranchFollowsTheResponse (M3): Serve picks the stream branch from the upstream's
// response, not the request. A 2xx that is not an event stream answers a stream:true request on the
// JSON path (gated, the decision in headers, no time to the first event); an event stream with a
// non-2xx status passes through as it is, never enforced, even on a closed block route (D4).
func TestServeStreamBranchFollowsTheResponse(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	t.Run("json 2xx", func(t *testing.T) {
		answer, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-up", "object": "chat.completion", "created": 1760000000, "model": "gpt-4o-mini",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": sc.Response}, "finish_reason": "stop"}},
		})
		u := newStrmUpstream(t, string(answer))
		u.header = http.Header{"Content-Type": {"application/json"}}
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag"), serve: true})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" || string(body) != string(answer) {
			t.Errorf("%d %s:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		if resp.Header.Get(HeaderAction) != decision.ActionFlag || resp.Header.Get("Trailer") != "" || len(resp.Trailer) != 0 {
			t.Errorf("the decision goes in headers: action %q, trailers %q %v", resp.Header.Get(HeaderAction), resp.Header.Get("Trailer"), resp.Trailer)
		}
		r := e.sink.result(t)
		if !r.Trace.Stream || r.Upstream.TTFTMs != nil || r.ResponseText != sc.Response || r.Decision.Action != decision.ActionFlag {
			t.Errorf("trace stream %v ttft %v text %q action %s", r.Trace.Stream, r.Upstream.TTFTMs, r.ResponseText, r.Decision.Action)
		}
	})
	t.Run("non-2xx event stream", func(t *testing.T) {
		const failure = "data: {\"error\":{\"message\":\"overloaded\",\"type\":\"server_error\"}}\n\n"
		u := newStrmUpstream(t, failure)
		u.status = http.StatusServiceUnavailable
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: block, fail: closed"), serve: true})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if resp.StatusCode != http.StatusServiceUnavailable || string(body) != failure ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("X-Upstream") != "yes" {
			t.Errorf("%d %s:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
		if resp.Header.Get(HeaderStatus) != "skipped:upstream_error" || resp.Header.Get(HeaderAction) != decision.ActionPass {
			t.Errorf("status %q action %q", resp.Header.Get(HeaderStatus), resp.Header.Get(HeaderAction))
		}
		if r := e.sink.result(t); r.Decision.Enforced || r.Upstream.Status != http.StatusServiceUnavailable {
			t.Errorf("enforced %v upstream %+v", r.Decision.Enforced, r.Upstream)
		}
	})
}

// TestServeStreamFailsBeforeItsFirstEvent (D21): an upstream stream that breaks off, stalls or
// ends before its first event, while nothing was written, is answered as a failed upstream call: 502,
// or 504 after the idle timeout, in the route's error shape, without the upstream's headers. The
// trace is skipped:upstream_error (skipped:undecodable for a body the gateway cannot decode), the
// decision unenforced and the delivered action pass, and the request counts once. A buffered
// route answers the same while no keepalive committed its headers, even when closed. Once an event
// went out, the stream stays a 200 and the failure goes into the trailers.
func TestServeStreamFailsBeforeItsFirstEvent(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	for _, c := range []struct {
		name, path, format, policy, mode string
		// events are sent before the upstream breaks off or stalls (or, with ends, ends the stream
		// cleanly).
		events      []string
		stall, ends bool
		header      http.Header
		status      int
		code        string
		skip        string
	}{
		{name: "relay breaks off", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUpstreamError},
		{name: "relay stalls", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, stall: true, status: http.StatusGatewayTimeout, code: CodeUpstreamTimeout, skip: gate.SkipUpstreamError},
		{name: "relay sends half an event", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, events: []string{"data: {\"choi"}, status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUpstreamError},
		{name: "relay ends without an event", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, ends: true, status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUpstreamError},
		{name: "buffer ends without an event", path: "/v1/messages", format: llmapi.FormatAnthropicMessages,
			policy: strmBufferPolicy("block", "", ""), mode: decision.ModeBlock, ends: true, status: http.StatusBadGateway, skip: gate.SkipUpstreamError},
		{name: "anthropic breaks off", path: "/v1/messages", format: llmapi.FormatAnthropicMessages, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, status: http.StatusBadGateway, skip: gate.SkipUpstreamError},
		{name: "observe breaks off", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: observe"),
			mode: decision.ModeObserve, status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUpstreamError},
		{name: "buffer breaks off on a closed block route", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat,
			policy: strmBufferPolicy("block", "error", ""), mode: decision.ModeBlock, status: http.StatusBadGateway, code: CodeUpstreamError,
			skip: gate.SkipUpstreamError},
		// An upstream's own keepalive comment is no event (a client dispatches nothing for it) and
		// a buffered route has not written it: the call still failed before its first event.
		{name: "buffer reads only an upstream keepalive", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat,
			policy: strmBufferPolicy("block", "error", ""), mode: decision.ModeBlock, events: []string{": OPENROUTER PROCESSING\n\n"},
			status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUpstreamError},
		{name: "buffer ends after an upstream keepalive", path: "/v1/messages", format: llmapi.FormatAnthropicMessages,
			policy: strmBufferPolicy("block", "", ""), mode: decision.ModeBlock, events: []string{": keepalive\n\n", "\n"}, ends: true,
			status: http.StatusBadGateway, skip: gate.SkipUpstreamError},
		{name: "undecodable", path: "/v1/chat/completions", format: llmapi.FormatOpenAIChat, policy: strmPolicy("mode: flag"),
			mode: decision.ModeFlag, events: strmOpenAIStream(sc.Response), header: http.Header{"Content-Encoding": {"br"}},
			status: http.StatusBadGateway, code: CodeUpstreamError, skip: gate.SkipUndecodable},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newStrmUpstream(t, c.events...)
			u.stall, u.abort, u.header = c.stall, !c.stall && !c.ends, c.header
			e := newStrmEnv(t, u, strmOpts{policyYAML: c.policy, idle: 150 * time.Millisecond, serve: true})
			route := e.pol.Match(&policy.Request{Path: c.path}).Name
			// Pass is what an unenforced decision delivers; observe stays observe.
			action := decision.ActionPass
			if c.mode == decision.ModeObserve {
				action = decision.ActionObserve
			}
			var resp *http.Response
			var body []byte
			strmCountedOnce(t, route, c.format, c.mode, action, func() {
				resp, body = e.call(t, c.path, sc.body(t, c.format, nil), sc.ID)
			})
			if resp.StatusCode != c.status || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("%d %s:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
			var shape struct {
				Type  string `json:"type"`
				Error struct {
					Type, Code, Message string
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &shape); err != nil || shape.Error.Message == "" {
				t.Fatalf("error body %s: %v", body, err)
			}
			if llmapi.IsAnthropic(c.format) {
				if shape.Type != "error" || shape.Error.Type == "" {
					t.Errorf("an Anthropic error: %s", body)
				}
			} else if shape.Error.Code != c.code || shape.Error.Type != "trustagent_error" {
				t.Errorf("an OpenAI error with code %s: %s", c.code, body)
			}
			if resp.Header.Get("X-Upstream") != "" || resp.Header.Get("Trailer") != "" || len(resp.Trailer) != 0 {
				t.Errorf("the upstream's headers or a trailer went out: %v %v", resp.Header, resp.Trailer)
			}
			traceID := resp.Header.Get(HeaderTraceID)
			if traceID == "" || resp.Header.Get(HeaderRequestID) != traceID || resp.Header.Get(HeaderAction) != action ||
				resp.Header.Get(HeaderStatus) != "skipped:"+c.skip || resp.Header.Get(HeaderRoute) == "" {
				t.Errorf("headers %v", resp.Header)
			}
			r := e.sink.result(t)
			d := r.Decision
			want := &gate.Final{Action: action, Delivered: gate.DeliveredOriginal}
			if d.Enforced || d.SkipReason == nil || *d.SkipReason != c.skip || !reflect.DeepEqual(r.Final, want) ||
				r.Upstream.Status != http.StatusOK || r.Trace.TraceID != traceID {
				t.Errorf("enforced %v skip %v final %+v upstream %+v", d.Enforced, d.SkipReason, r.Final, r.Upstream)
			}
			e.sink.none(t)
		})
	}

	t.Run("after the first event", func(t *testing.T) {
		u := newStrmUpstream(t, strmOpenAIStream(sc.Response)[0])
		u.abort = true
		e := newStrmEnv(t, u, strmOpts{policyYAML: strmPolicy("mode: flag"), serve: true})
		resp, body := e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
		if resp.StatusCode != http.StatusOK || resp.Trailer.Get(HeaderStatus) != "skipped:upstream_error" ||
			!strings.HasPrefix(string(body), strmOpenAIStream(sc.Response)[0]) {
			t.Errorf("%d trailers %v:\n%s", resp.StatusCode, resp.Trailer, body)
		}
		e.sink.result(t)
	})
}

// TestServeStreamEarlyFailureNeedsUncommittedHeaders (D21 boundaries): a stream counts as failed
// before its first event only while nothing went out and nothing is held. A relayed upstream
// keepalive comment commits the headers, so a failure after it stays a 200 stream with the failure
// in the trailers; a stream whose only event is the terminal one holds it and is an answer without
// text; a buffered answer whose keepalive committed the headers can no longer become an error
// status, so a closed block route sends its block as a message-style stream.
func TestServeStreamEarlyFailureNeedsUncommittedHeaders(t *testing.T) {
	sc := loadStrmScenario(t, "stream-passthrough-flag")
	const comment = ": OPENROUTER PROCESSING\n\n"
	for _, c := range []struct {
		name, policy, action, status string
		events                       []string
		delay                        time.Duration
		abort                        bool
		// prefix starts the client's body; text, when set, is the answer text it carries.
		prefix, text string
	}{
		{name: "relay after an upstream keepalive", policy: strmPolicy("mode: flag"), events: []string{comment}, abort: true,
			action: decision.ActionPass, status: "skipped:upstream_error", prefix: comment},
		{name: "relay of only the terminal event", policy: strmPolicy("mode: flag"), events: []string{"data: [DONE]\n\n"},
			action: decision.ActionPass, status: "skipped:no_text", prefix: ": " + llmapi.AnnotationPrefix},
		{name: "buffer after its keepalive", policy: strmBufferPolicy("block", "error", "buffer_keepalive_s: 1"),
			delay: 1300 * time.Millisecond, abort: true, action: decision.ActionBlock, status: "skipped:upstream_error",
			prefix: ": keepalive\n\n", text: strmBlockMessage},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := newStrmUpstream(t, c.events...)
			u.delay, u.abort = c.delay, c.abort
			e := newStrmEnv(t, u, strmOpts{policyYAML: c.policy, serve: true})
			route := e.pol.Match(&policy.Request{Path: "/v1/chat/completions"})
			mode, _ := e.pol.Mode(route, "")
			var resp *http.Response
			var body []byte
			strmCountedOnce(t, route.Name, llmapi.FormatOpenAIChat, mode, c.action, func() {
				resp, body = e.call(t, "/v1/chat/completions", sc.body(t, llmapi.FormatOpenAIChat, nil), sc.ID)
			})
			if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
				!strings.HasPrefix(string(body), c.prefix) {
				t.Fatalf("%d %s:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
			if resp.Trailer.Get(HeaderAction) != c.action || resp.Trailer.Get(HeaderStatus) != c.status {
				t.Errorf("trailers %v", resp.Trailer)
			}
			if text, _ := strmText(t, llmapi.FormatOpenAIChat, body); text != c.text {
				t.Errorf("text %q, want %q", text, c.text)
			}
			if _, at := strmAnnotation(t, strmEvents(t, body)); at < 0 {
				t.Errorf("no annotation:\n%s", body)
			}
			if r := e.sink.result(t); deliveredAction(r) != c.action || r.Status != c.status {
				t.Errorf("recorded action %q status %q", deliveredAction(r), r.Status)
			}
			e.sink.none(t)
		})
	}
}
