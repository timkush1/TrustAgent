package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/upstream"
)

// sseUpstream is an OpenAI upstream that answers a stream:true request with an event stream of
// pieces, one chunk each, then the finish reason, the usage chunk and [DONE]. After each chunk but
// the last it waits until the gateway relayed it (step), so a gateway that read the whole stream
// before relaying anything would stall it (stalled). Any other request gets jsonAnswer.
type sseUpstream struct {
	*httptest.Server
	pieces     []string
	jsonAnswer string
	// tail, when set, replaces the finish, usage and [DONE] events.
	tail string
	step chan struct{}

	mu      sync.Mutex
	bodies  [][]byte
	auth    []string
	stalled bool
}

func newSSEUpstream(t *testing.T, pieces []string, jsonAnswer string) *sseUpstream {
	t.Helper()
	u := &sseUpstream{pieces: pieces, jsonAnswer: jsonAnswer, step: make(chan struct{}, len(pieces)+1)}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.bodies = append(u.bodies, b)
		u.auth = append(u.auth, r.Header.Get("Authorization"))
		tail := u.tail
		u.mu.Unlock()
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(b, &req)
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, u.jsonAnswer)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i, p := range u.pieces {
			content, _ := json.Marshal(p)
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-s\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", content)
			fl.Flush()
			if i == len(u.pieces)-1 {
				break
			}
			select {
			case <-u.step:
			case <-time.After(3 * time.Second):
				u.mu.Lock()
				u.stalled = true
				u.mu.Unlock()
			case <-r.Context().Done():
				return
			}
		}
		if tail != "" {
			_, _ = io.WriteString(w, tail)
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-s\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-s\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5,\"total_tokens\":17}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *sseUpstream) calls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.bodies)
}

// streamedRun runs in through e's Playground with onToken and OnDecision recorders: the tokens,
// and the tokens and upstream calls seen when the decision arrived.
type streamedRun struct {
	tokens          []string
	decision        *gate.Decision
	tokensAtDecided int
	callsAtDecided  int
}

func runStreamed(t *testing.T, e *env, up *sseUpstream, in PlaygroundInput) (*streamedRun, *PlaygroundResult, error) {
	t.Helper()
	run := &streamedRun{}
	in.OnDecision = func(d *gate.Decision) {
		if run.decision != nil {
			t.Error("OnDecision was called twice")
		}
		run.decision, run.tokensAtDecided, run.callsAtDecided = d, len(run.tokens), up.calls()
	}
	res, err := e.handler.Playground(context.Background(), playgroundContext(), in, func(text string) {
		run.tokens = append(run.tokens, text)
		up.step <- struct{}{}
	})
	return run, res, err
}

// TestPlaygroundStreamsTheUpstream: a streamed chat-mode run asks the upstream for a stream and
// relays each piece as a token as soon as it arrives (the upstream waits for the relay of one
// piece before it sends the next); the decision comes after the last token, the run records the
// stream's usage, and is checked as the JSON answer an app would have read.
func TestPlaygroundStreamsTheUpstream(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
	pieces := []string{"You can get ", "a full refund ", "within 60 days ", "of purchase."}
	up := newSSEUpstream(t, pieces, "")
	e.injecting(t, up.URL)

	run, res, err := runStreamed(t, e, up, refundRun(t, PlaygroundChat))
	if err != nil {
		t.Fatal(err)
	}
	r := e.sink.result(t)
	answer := strings.Join(pieces, "")
	if strings.Join(run.tokens, "") != answer || len(run.tokens) != len(pieces) {
		t.Errorf("tokens %q, want one per upstream chunk: %q", run.tokens, pieces)
	}
	if up.stalled {
		t.Error("the upstream stalled: a piece was not relayed until the stream ended")
	}
	if run.decision == nil || run.tokensAtDecided != len(pieces) || run.decision.TraceID != res.TraceID || run.decision != res.Decision {
		t.Errorf("decision %v after %d tokens, want the run's decision after every token", run.decision, run.tokensAtDecided)
	}
	if res.ResponseText != answer || res.DeliveredText != answer || res.Upstream == nil || res.Upstream.Status != http.StatusOK {
		t.Errorf("result %+v", res)
	}
	if p, c := r.Upstream.PromptTokens, r.Upstream.CompletionTokens; p == nil || c == nil || *p != 12 || *c != 5 {
		t.Errorf("upstream usage %v %v, want the stream's 12 and 5", p, c)
	}
	if r.Trace.Stream || slices.Contains(r.Decision.Reasons, "stream_not_buffered") {
		t.Errorf("trace stream %v, reasons %v: the run is checked as a JSON answer", r.Trace.Stream, r.Decision.Reasons)
	}
	if r.Decision.Action != decision.ActionFlag || r.Trace.Source != gate.SourcePlayground {
		t.Errorf("action %s source %s", r.Decision.Action, r.Trace.Source)
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(up.bodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent["stream"]) != "true" || string(sent["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("upstream body %s", up.bodies[0])
	}
	if up.auth[0] != "Bearer "+injectedKey {
		t.Errorf("upstream Authorization %q", up.auth[0])
	}
}

// TestPlaygroundDecisionArrivesBeforeTheFallback: on a fallback route the decision reaches the
// caller as soon as the gate decided, before the fallback call; the result then delivers the
// fallback answer.
func TestPlaygroundDecisionArrivesBeforeTheFallback(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: regateJev{newMockJev(t), fallbackText, regatePass}, policyYAML: fallbackPolicy("", "")})
	sc := demoScenario(t, "refund-contradiction-flag")
	up := newSSEUpstream(t, []string{"You can get a full refund ", "within 60 days of purchase."}, chatAnswer(fallbackText))
	e.injecting(t, up.URL)

	run, res, err := runStreamed(t, e, up, refundRun(t, PlaygroundChat))
	if err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	if run.decision == nil || run.decision.Action != decision.ActionFallback || run.callsAtDecided != 1 {
		t.Fatalf("decision %v with %d upstream calls made, want fallback before the fallback call", run.decision, run.callsAtDecided)
	}
	if up.calls() != 2 || res.ResponseText != sc.Response || res.DeliveredText != fallbackText {
		t.Errorf("%d upstream calls, result %+v; want the fallback answer delivered", up.calls(), res)
	}
}

// TestPlaygroundBrokenStreams: a stream that reports an error inside it, or ends before its answer
// does, records the run as skipped:upstream_error (never enforced) and fails it with a 502 that
// names the trace, without quoting the upstream's message.
func TestPlaygroundBrokenStreams(t *testing.T) {
	cases := []struct{ name, tail, want string }{
		{name: "error event", tail: "data: {\"error\":{\"message\":\"key sk-proj-leakedkey123456 is over quota\",\"type\":\"server_error\",\"code\":\"overloaded\"}}\n\n",
			want: "reported an error (overloaded)"},
		{name: "cut short", tail: "", want: "ended before its answer did"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
			up := newSSEUpstream(t, []string{"You can get ", "a full refund."}, "")
			up.tail = tc.tail
			if tc.tail == "" {
				up.tail = "\n" // no finish reason, no [DONE]: the connection just ends
			}
			e.injecting(t, up.URL)
			run, _, err := runStreamed(t, e, up, refundRun(t, PlaygroundChat))
			pe := playgroundErr(t, err)
			r := e.sink.result(t)
			if pe.Status != http.StatusBadGateway || pe.Code != CodeUpstreamError || pe.TraceID != r.Trace.TraceID ||
				!strings.Contains(pe.Message, tc.want) || strings.Contains(pe.Message, "sk-proj") {
				t.Errorf("%+v", pe)
			}
			if r.Status != "skipped:upstream_error" || run.decision != nil {
				t.Errorf("status %s, decision event %v", r.Status, run.decision)
			}
		})
	}
}

// TestPlaygroundStreamCallerGoesAway: a caller that leaves mid-stream cancels the upstream call;
// the run is recorded as skipped:client_disconnected.
func TestPlaygroundStreamCallerGoesAway(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
	up := newSSEUpstream(t, []string{"You can get ", "a full refund."}, "")
	e.injecting(t, up.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := e.handler.Playground(ctx, playgroundContext(), refundRun(t, PlaygroundChat), func(string) { cancel() })
	playgroundErr(t, err)
	r := e.sink.result(t)
	if r.Decision.SkipReason == nil || *r.Decision.SkipReason != gate.SkipClientDisconnected || r.Decision.Enforced {
		t.Errorf("skip %v enforced %v", r.Decision.SkipReason, r.Decision.Enforced)
	}
}

// TestPlaygroundChatWithADashboardCredential: chat mode needs no environment key once the upstream
// set in effect injects a credential (one connected in the dashboard rebuilds the set): the set is
// read per run, so the run that follows the swap is served.
func TestPlaygroundChatWithADashboardCredential(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: blockPolicy("flag", "")})
	answer := "Annual plans can be refunded within 30 days."
	e.up.body = chatAnswer(answer)
	newSet := func(key string) *upstream.Set {
		c, err := upstream.New(upstream.Config{BaseURL: e.up.URL, APIKey: key, Timeout: 5 * time.Second, HeaderTimeout: 5 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &upstream.Set{OpenAI: c}
	}
	var current atomic.Pointer[upstream.Set]
	current.Store(newSet(""))
	e.handler.upsFn = current.Load

	_, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundChat), nil)
	if pe := playgroundErr(t, err); pe.Status != http.StatusServiceUnavailable || pe.Code != CodeCredentialsUnavailable ||
		!strings.Contains(pe.Message, "Settings → Connections") {
		t.Fatalf("without a credential: %+v", pe)
	}

	const dashboardKey = "sk-test-dashboard-credential-3f2a"
	current.Store(newSet(dashboardKey))
	res, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundChat), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	req, _ := e.up.last(t)
	if res.ResponseText != answer || req.Header.Get("Authorization") != "Bearer "+dashboardKey {
		t.Errorf("result %+v, upstream Authorization %q", res, req.Header.Get("Authorization"))
	}
}

// enforcingJev records the Enforcing option of every decision-model call.
type enforcingJev struct {
	jev.Client
	mu  sync.Mutex
	got []bool
}

func (j *enforcingJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	j.mu.Lock()
	j.got = append(j.got, opt.Enforcing)
	j.mu.Unlock()
	return j.Client.Decide(ctx, req, opt)
}

func (j *enforcingJev) calls() []bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]bool(nil), j.got...)
}

// TestPlaygroundDecisionCallsAreAdvisory: a Playground run on a block route never spends the
// enforcing reserve (Enforcing false, D82), while the same route's /v1 traffic does.
func TestPlaygroundDecisionCallsAreAdvisory(t *testing.T) {
	rec := &enforcingJev{Client: newMockJev(t)}
	e := newEnv(t, envOpts{testMode: true, enforcing: true, jev: rec, policyYAML: blockPolicy(decision.ModeBlock, "")})
	if _, err := e.handler.Playground(context.Background(), playgroundContext(), refundRun(t, PlaygroundAudit), nil); err != nil {
		t.Fatal(err)
	}
	e.sink.result(t)
	played := rec.calls()
	if len(played) == 0 || played[0] {
		t.Fatalf("playground decision calls %v, want advisory (Enforcing false)", played)
	}

	req, answer, headers := contradicted(t)
	e.do(t, "/v1/chat/completions", withTestResponse(t, req, answer), headers)
	e.sink.result(t)
	all := rec.calls()
	if len(all) <= len(played) || !all[len(played)] {
		t.Errorf("/v1 decision calls %v, want enforcing on a block route", all[len(played):])
	}
}
