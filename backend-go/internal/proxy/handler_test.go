package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/upstream"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	ingestKey   = "ta-test-ingest-key"
	upstreamKey = "sk-upstream-test-key"
)

// recSink records the lifecycle calls; a selected audit reports queued.
type recSink struct {
	mu       sync.Mutex
	started  []*gate.TraceStart
	decided  chan *gate.Result
	attempts []*gate.Result // AttemptDecided calls
	late     []*gate.Result // LateDecided calls
}

func newRecSink() *recSink { return &recSink{decided: make(chan *gate.Result, 16)} }

func (s *recSink) TraceStarted(_ context.Context, t *gate.TraceStart) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, t)
}

func (s *recSink) GateDecided(_ context.Context, r *gate.Result) string {
	s.decided <- r
	if r.Audit.Selection != "" {
		return gate.AuditQueued
	}
	return r.Audit.Status
}

func (s *recSink) AttemptDecided(_ context.Context, r *gate.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r)
}

func (s *recSink) LateDecided(_ context.Context, r *gate.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.late = append(s.late, r)
}

func (s *recSink) result(t *testing.T) *gate.Result {
	t.Helper()
	select {
	case r := <-s.decided:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no gate decision reached the sink")
		return nil
	}
}

func (s *recSink) traces() []*gate.TraceStart {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.started)
}

// fakeUpstream is an OpenAI-compatible upstream that records what it receives.
type fakeUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	status   int
	body     string
	ctype    string
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{status: http.StatusOK, ctype: "application/json",
		body: `{"id":"chatcmpl-up","object":"chat.completion","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"Priority tickets get a first response within 4 hours."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":9,"total_tokens":29}}`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.bodies = append(f.bodies, b)
		status, body, ctype := f.status, f.body, f.ctype
		f.mu.Unlock()
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("X-TrustAgent-Action", "spoofed")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeUpstream) last(t *testing.T) (*http.Request, []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the upstream was not called")
	}
	return f.requests[len(f.requests)-1], f.bodies[len(f.bodies)-1]
}

func (f *fakeUpstream) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// recorder is an httptest.ResponseRecorder that, like a real server's writer, implements
// http.CloseNotifier, which gin's writer (and so ReverseProxy) relies on.
type recorder struct{ *httptest.ResponseRecorder }

func (recorder) CloseNotify() <-chan bool { return make(chan bool) }

func newRecorder() recorder { return recorder{httptest.NewRecorder()} }

type env struct {
	router *gin.Engine
	sink   *recSink
	// up is the OpenAI upstream and anth the Anthropic one.
	up, anth *fakeUpstream
	handler  *Handler
}

type envOpts struct {
	testMode   bool
	policyYAML string // "" = config/policies.yaml
	// enforcing loads the policy as the enforcing gateway does (block and fallback accepted).
	enforcing  bool
	ingest     string
	jevLatency time.Duration
	jevBackend string
	// jev replaces the decision-model client (the mock of jevBackend).
	jev      jev.Client
	maxGates int
	// maxBytes is MAX_UPSTREAM_RESPONSE_BYTES (0: the default).
	maxBytes int64
	// rateLimit, when set, limits /v1 to that many requests a minute (the v1 limiter).
	rateLimit int
}

// anthropicBody is the Anthropic upstream's default answer.
const anthropicBody = `{"id":"msg_up","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"Priority tickets get a first response within 4 hours."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":20,"output_tokens":9}}`

// loadPolicy loads config/policies.yaml, or yaml when set; enforcing accepts block and fallback.
func loadPolicy(t *testing.T, reg *evaluators.Registry, yaml string, enforcing bool) *policy.Policy {
	t.Helper()
	src := []byte(yaml)
	if yaml == "" {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policies.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		src = b
	}
	res := policy.Check(src, policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg, Enforcing: enforcing})
	if res.Policy == nil {
		t.Fatal(res.Err())
	}
	return res.Policy
}

func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	pol := loadPolicy(t, reg, o.policyYAML, o.enforcing)
	cl := o.jev
	if cl == nil {
		backend := o.jevBackend
		if backend == "" {
			backend = jev.BackendMock
		}
		if cl, err = jev.New(jev.Config{Backend: backend, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true, Latency: o.jevLatency}}); err != nil {
			t.Fatal(err)
		}
	}
	quiet := slog.New(slog.DiscardHandler)
	g, err := gate.New(gate.Config{Jev: cl, Evaluators: reg, UniformAuditsPerDay: 50, Rand: func() float64 { return 0.5 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	auth := authz.New([]authz.Key{{Label: "trafficgen", Secret: ingestKey, Scopes: authz.APIKeyScopes}}, o.ingest)
	fu, fa := newFakeUpstream(t), newFakeUpstream(t)
	fa.body = anthropicBody
	up, err := upstream.New(upstream.Config{BaseURL: fu.URL, Timeout: 5 * time.Second}, auth.IsProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	anth, err := upstream.New(upstream.Config{BaseURL: fa.URL, Timeout: 5 * time.Second}, auth.IsProxyKey)
	if err != nil {
		t.Fatal(err)
	}
	sink := newRecSink()
	h := New(Options{
		Upstreams: &upstream.Set{OpenAI: up, Anthropic: anth}, Policy: func() *policy.Policy { return pol }, Gate: g, Sink: sink,
		TestMode: o.testMode, GateMaxConcurrency: o.maxGates, MaxUpstreamResponseBytes: o.maxBytes, Logger: quiet,
	})
	r := gin.New()
	r.Use(middleware.CORS([]string{"http://localhost:5173"}))
	guards := []gin.HandlerFunc{h.TraceIDs(), auth.Ingest(Deny)}
	if o.rateLimit > 0 {
		guards = append(guards, middleware.RateLimitBy(middleware.NewMemoryLimiter(), o.rateLimit, time.Minute, Deny, nil))
	}
	h.Mount(r.Group("/v1", guards...), middleware.BodyLimit(1<<20))
	// As cmd/proxy/server.go: unknown /v1 paths answer in their API's error shape.
	r.NoRoute(func(c *gin.Context) {
		if p := c.Request.URL.Path; strings.HasPrefix(p, "/v1/") {
			Deny(c, http.StatusNotFound, CodeNotFound, "TrustAgent does not serve "+c.Request.Method+" "+p+".")
			c.Abort()
		}
	})
	return &env{router: r, sink: sink, up: fu, anth: fa, handler: h}
}

func (e *env) do(t *testing.T, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return e.send(t, http.MethodPost, path, body, headers)
}

// send sends an ingest-authenticated request; a header set to "" is removed.
func (e *env) send(t *testing.T, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TrustAgent-Key", ingestKey)
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	w := newRecorder()
	e.router.ServeHTTP(w, req)
	return w.ResponseRecorder
}

type scenario struct {
	ID       string            `json:"id"`
	Sets     []string          `json:"sets"`
	Headers  map[string]string `json:"headers"`
	Request  json.RawMessage   `json:"request"`
	Response string            `json:"response"`
	Expect   struct {
		Mode           string   `json:"mode"`
		Action         string   `json:"action"`
		WouldAction    string   `json:"would_action"`
		Status         string   `json:"status"`
		Verdict        string   `json:"verdict"`
		MaxRisk        *float64 `json:"max_risk"`
		Reasons        []string `json:"reasons"`
		CheckedAgainst string   `json:"checked_against"`
		Audit          string   `json:"audit"`
	} `json:"expect"`
}

func s0Scenarios(t *testing.T) []scenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	var out []scenario
	for _, s := range pack.Scenarios {
		if slices.Contains(s.Sets, "s0") {
			out = append(out, s)
		}
	}
	if len(out) != 3 {
		t.Fatalf("%d s0 scenarios, want 3", len(out))
	}
	return out
}

// trafficgenBody is the scenario request with the scripted answer as test_response.
func trafficgenBody(t *testing.T, sc scenario) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(sc.Request, &m); err != nil {
		t.Fatal(err)
	}
	m["test_response"] = sc.Response
	b, _ := json.Marshal(m)
	return string(b)
}

// TestS0ScenariosOverHTTP sends each S0 scenario the way trafficgen does and checks the response
// headers against the scenario's expect block (contracts/demo/scenarios.v1.json).
func TestS0ScenariosOverHTTP(t *testing.T) {
	for _, sc := range s0Scenarios(t) {
		t.Run(sc.ID, func(t *testing.T) {
			e := newEnv(t, envOpts{testMode: true})
			hdr := map[string]string{"X-TrustAgent-Scenario": sc.ID}
			for k, v := range sc.Headers {
				hdr[k] = v
			}
			w := e.do(t, "/v1/chat/completions", trafficgenBody(t, sc), hdr)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d body %s", w.Code, w.Body.String())
			}
			h, exp := w.Header(), sc.Expect
			check := func(name, want string) {
				t.Helper()
				if got := h.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			check(HeaderMode, exp.Mode)
			check(HeaderAction, exp.Action)
			check(HeaderStatus, exp.Status)
			check(HeaderVerdict, exp.Verdict)
			check(HeaderCheckedAgainst, exp.CheckedAgainst)
			check(HeaderAudit, exp.Audit)
			check(HeaderSource, gate.SourceTrafficgen)
			if exp.WouldAction != exp.Action {
				check(HeaderWouldAction, exp.WouldAction)
			} else {
				check(HeaderWouldAction, "")
			}
			if exp.MaxRisk != nil {
				check(HeaderRisk, decision.FormatRisk(*exp.MaxRisk))
			} else {
				check(HeaderRisk, "")
			}
			reasons := h.Get(HeaderReasons)
			if len(exp.Reasons) == 0 && reasons != "" {
				t.Errorf("reasons header %q, want none", reasons)
			}
			for _, r := range exp.Reasons {
				if !slices.Contains(strings.Split(reasons, ","), r) {
					t.Errorf("reasons %q lack %s", reasons, r)
				}
			}
			if !strings.HasPrefix(h.Get(HeaderRoute), "support-bot@v") {
				t.Errorf("route %q", h.Get(HeaderRoute))
			}
			if id, err := uuid.Parse(h.Get(HeaderTraceID)); err != nil || id.Version() != 7 || h.Get(HeaderRequestID) != id.String() {
				t.Errorf("trace id %q request id %q", h.Get(HeaderTraceID), h.Get(HeaderRequestID))
			}
			if exp.Status != decision.StatusNotAssessed {
				check(HeaderDecisionModel, jev.DefaultMockModel)
			} else {
				check(HeaderDecisionModel, "")
			}
			check(HeaderCalibrated, decision.CalibrationNone)
			if h.Get(HeaderGateMs) == "" || h.Get(HeaderOverheadMs) == "" {
				t.Error("Gate-Ms and Overhead-Ms are sent on gated routes")
			}
			// Test mode serves the scripted answer: no upstream call.
			if e.up.calls() != 0 {
				t.Errorf("the upstream was called %d times in test mode", e.up.calls())
			}
			var body struct {
				ID      string `json:"id"`
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Choices[0].Message.Content != sc.Response ||
				!strings.HasPrefix(body.ID, "chatcmpl-trustagent-") {
				t.Errorf("body %s", w.Body.String())
			}

			r := e.sink.result(t)
			traces := e.sink.traces()
			if len(traces) != 1 || traces[0].TraceID != h.Get(HeaderTraceID) || traces[0].Scenario != sc.ID ||
				traces[0].AnswerSource != gate.AnswerScripted || !slices.Contains(traces[0].Flags, gate.FlagTestMode) {
				t.Errorf("trace %+v", traces)
			}
			if r.Trace != traces[0] || r.Decision.TraceID != traces[0].TraceID || r.Upstream.Status != http.StatusOK || r.TotalMs == nil {
				t.Errorf("result %+v", r)
			}
			contractstest.Validate(t, "decision.v1.schema.json", r.Decision)
		})
	}
}

const defaultRouteBody = `{"model":"gpt-4o-mini","messages":[
  {"role":"system","content":"<document id=\"sla\">Priority tickets get a first response within 4 hours.</document>"},
  {"role":"user","content":"How fast do you answer priority tickets?"}]}`

func TestObserveServesUpstreamBytesThenGates(t *testing.T) {
	e := newEnv(t, envOpts{})
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Code != http.StatusOK || w.Body.String() != e.up.body {
		t.Fatalf("status %d body %q: observe returns the upstream body byte for byte", w.Code, w.Body.String())
	}
	h := w.Header()
	for name, want := range map[string]string{
		HeaderRoute: "default@v1", HeaderMode: decision.ModeObserve, HeaderAction: decision.ActionObserve,
		HeaderStatus: StatusDeferred, "X-Upstream": "yes", "Content-Type": "application/json",
		HeaderSource: gate.SourceTrafficgen, // the ingest key is labelled trafficgen
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{HeaderVerdict, HeaderRisk, HeaderWouldAction, HeaderAudit, HeaderGateMs, HeaderCheckedAgainst} {
		if h.Get(name) != "" {
			t.Errorf("%s = %q: observe mode sends no decision headers", name, h.Get(name))
		}
	}
	if got := h.Values(HeaderAction); len(got) != 1 {
		t.Errorf("the upstream's own X-TrustAgent-Action must not reach the client: %v", got)
	}
	r := e.sink.result(t)
	d := r.Decision
	// The strict mock has no scenario for plain traffic, so the asynchronous gate reports the miss.
	if d.Action != decision.ActionObserve || r.Status != "error:mock_fixture_missing" || r.Trace.Mode != decision.ModeObserve {
		t.Errorf("decision %s %s mode %s", d.Action, r.Status, r.Trace.Mode)
	}
	if r.Upstream.Status != http.StatusOK || r.Upstream.PromptTokens == nil || *r.Upstream.PromptTokens != 20 || r.Trace.Source != gate.SourceTrafficgen {
		t.Errorf("upstream %+v source %s (the trafficgen key label marks the source)", r.Upstream, r.Trace.Source)
	}
	if r.Trace.AnswerSource != gate.AnswerLive || r.UserRequest != "How fast do you answer priority tickets?" {
		t.Errorf("trace %+v user request %q", r.Trace, r.UserRequest)
	}
	if err := e.handler.Drain(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestObserveOverloadIsRecordedSynchronously(t *testing.T) {
	e := newEnv(t, envOpts{maxGates: 1})
	e.handler.sem <- struct{}{} // every async slot is taken
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Code != http.StatusOK || w.Header().Get(HeaderStatus) != StatusDeferred {
		t.Fatalf("status %d %q", w.Code, w.Header().Get(HeaderStatus))
	}
	r := e.sink.result(t)
	if r.Status != "skipped:overload" || !r.Decision.Detectors[0].Ran {
		t.Errorf("status %s: an overflowing gate is skipped with detectors only", r.Status)
	}
}

func TestForwardingStripsHeadersAndNeverForwardsTheProxyKey(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	w := e.do(t, "/v1/chat/completions?api-version=1", defaultRouteBody, map[string]string{
		"Cookie": "ta_session=secret", "Accept-Encoding": "br", "X-TrustAgent-Route": "x",
		"X-TrustAgent-Mode": "observe", "X-TrustAgent-Scenario": "grounded-pass", "X-Request-ID": "client-req-42",
		"OpenAI-Organization": "org-7",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	got, _ := e.up.last(t)
	for name := range got.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-trustagent-") {
			t.Errorf("forwarded %s", name)
		}
	}
	if got.Header.Get("Cookie") != "" || strings.Contains(got.Header.Get("Accept-Encoding"), "br") {
		t.Errorf("forwarded Cookie %q / client Accept-Encoding %q", got.Header.Get("Cookie"), got.Header.Get("Accept-Encoding"))
	}
	// B1 passthrough: the client's upstream credential goes up untouched; the ingest key never does.
	if got.Header.Get("Authorization") != "Bearer "+upstreamKey || strings.Contains(strings.Join(flatten(got.Header), " "), ingestKey) {
		t.Errorf("upstream headers %v", got.Header)
	}
	if got.Header.Get("Openai-Organization") != "org-7" || got.URL.Path != "/v1/chat/completions" || got.URL.RawQuery != "api-version=1" {
		t.Errorf("upstream request %s?%s %v", got.URL.Path, got.URL.RawQuery, got.Header)
	}
	if w.Header().Get(HeaderRequestID) != "client-req-42" {
		t.Errorf("a valid client X-Request-ID is echoed, got %q", w.Header().Get(HeaderRequestID))
	}
	r := e.sink.result(t)
	if r.Trace.ClientRequestID == nil || *r.Trace.ClientRequestID != "client-req-42" || r.Trace.Scenario != "" {
		t.Errorf("trace %+v: the scenario header is ignored outside test mode", r.Trace)
	}
	// Outside test mode the scenario is not passed to the strict mock, which then fails loudly.
	if w.Header().Get(HeaderStatus) != "error:mock_fixture_missing" || w.Header().Get(HeaderMode) != decision.ModeFlag {
		t.Errorf("status %q mode %q", w.Header().Get(HeaderStatus), w.Header().Get(HeaderMode))
	}

	// A client that puts the proxy key in Authorization does not leak it upstream either.
	e.do(t, "/v1/chat/completions", defaultRouteBody, map[string]string{"Authorization": "Bearer " + ingestKey})
	got, _ = e.up.last(t)
	if got.Header.Get("Authorization") != "" {
		t.Errorf("the proxy key was forwarded: %q", got.Header.Get("Authorization"))
	}
	e.sink.result(t)
}

func flatten(h http.Header) []string {
	var out []string
	for _, v := range h {
		out = append(out, v...)
	}
	return out
}

func TestExtensionStrippedByteForByte(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	body := "{\"model\":\"gpt-4o-mini\",\n \"messages\":[{\"role\":\"user\",\"content\":\"Is <b>bold</b> & fine?\"}],\n" +
		" \"trustagent\":{\"route\":\"x\",\"context\":[{\"id\":\"doc-1\",\"text\":\"Bold text is fine in answers.\"}]},\n \"temperature\":0.20}"
	w := e.do(t, "/v1/chat/completions", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	_, forwarded := e.up.last(t)
	want := "{\"model\":\"gpt-4o-mini\",\"messages\":[{\"role\":\"user\",\"content\":\"Is <b>bold</b> & fine?\"}],\"temperature\":0.20}"
	if string(forwarded) != want {
		t.Errorf("forwarded\n%s\nwant\n%s", forwarded, want)
	}
	r := e.sink.result(t)
	if len(r.Evidence) != 1 || r.Evidence[0].Source != "extension" || r.CheckedAgainst != "extension:1" {
		t.Errorf("evidence %+v checked %q", r.Evidence, r.CheckedAgainst)
	}
}

// twoChecks is a flag route with the two evidence checks the S0 scenarios script answers for.
const twoChecks = "version: 1\ndefaults: {mode: flag, evaluators: [{ref: groundedness@1}, {ref: contradiction@1}]}\n"

func TestContentPartsAreTolerated(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: twoChecks})
	body := `{"model":"gpt-4o-mini","test_response":"Annual plans can be refunded within 30 days of purchase.","messages":[
	  {"role":"developer","content":[{"type":"text","text":"<document id=\"refund-policy.md\">Annual plans can be refunded within 30 days of purchase.</document>"}]},
	  {"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"input_text","text":"How long is the refund window?"}]}]}`
	w := e.do(t, "/v1/chat/completions", body, map[string]string{"X-TrustAgent-Scenario": "grounded-pass"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get(HeaderCheckedAgainst) != "request_system:1" || w.Header().Get(HeaderStatus) != "ok" {
		t.Errorf("checked %q status %q", w.Header().Get(HeaderCheckedAgainst), w.Header().Get(HeaderStatus))
	}
	r := e.sink.result(t)
	if r.UserRequest != "How long is the refund window?" || r.Messages[0].Role != "developer" {
		t.Errorf("user request %q messages %+v", r.UserRequest, r.Messages)
	}
}

func TestUnparseableRequestIsForwardedAndSkipped(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	e.up.status, e.up.body = http.StatusBadRequest, `{"error":{"message":"bad json"}}`
	w := e.do(t, "/v1/chat/completions", `{"model": `, nil)
	if w.Code != http.StatusBadRequest || w.Body.String() != e.up.body {
		t.Errorf("status %d body %q: the upstream's answer passes through", w.Code, w.Body.String())
	}
	if _, fwd := e.up.last(t); string(fwd) != `{"model": ` {
		t.Errorf("an unparseable body is forwarded unchanged, got %q", fwd)
	}
	if got := w.Header().Get(HeaderStatus); got != "skipped:unparseable_request" {
		t.Errorf("status header %q", got)
	}
	e.sink.result(t)
}

func TestTestModeDisabled(t *testing.T) {
	e := newEnv(t, envOpts{})
	for _, path := range []string{"/v1/chat/completions", "/v1/completions"} {
		w := e.do(t, path, `{"model":"m","messages":[],"prompt":"x","test_response":"scripted"}`, nil)
		var body struct {
			Error struct {
				Code, Type string
			} `json:"error"`
		}
		if w.Code != http.StatusBadRequest || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
			body.Error.Code != CodeTestModeDisabled || body.Error.Type != "trustagent_error" {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get(HeaderTraceID) == "" {
			t.Errorf("%s: errors carry the trace id too", path)
		}
	}
	if e.up.calls() != 0 || len(e.sink.traces()) != 0 {
		t.Error("a refused test_response reaches neither the upstream nor the sink")
	}
}

func TestIngestAuthErrorsUseTheOpenAIShape(t *testing.T) {
	e := newEnv(t, envOpts{})
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, map[string]string{"X-TrustAgent-Key": ""})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) || w.Header().Get(HeaderTraceID) == "" {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if e.up.calls() != 0 {
		t.Error("an unauthenticated request must not reach the upstream")
	}
	open := newEnv(t, envOpts{ingest: authz.IngestOpen, policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	if w := open.do(t, "/v1/chat/completions", defaultRouteBody, map[string]string{"X-TrustAgent-Key": ""}); w.Code != http.StatusOK {
		t.Errorf("open ingest: %d", w.Code)
	}
	open.sink.result(t)
}

func TestInvalidRequestIDIsReplacedByTheTraceID(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, map[string]string{"X-Request-ID": "has spaces/and slashes"})
	if w.Header().Get(HeaderRequestID) != w.Header().Get(HeaderTraceID) {
		t.Errorf("request id %q trace id %q", w.Header().Get(HeaderRequestID), w.Header().Get(HeaderTraceID))
	}
	if r := e.sink.result(t); r.Trace.ClientRequestID != nil {
		t.Errorf("an invalid client id is not recorded: %q", *r.Trace.ClientRequestID)
	}
}

// TestEventStreamTakesTheStreamBranch: the branch follows the response, not the request (M3). An
// upstream event stream goes to the stream relay (stream.go) with its events intact; a JSON answer
// to a stream:true request, such as an upstream error, takes the JSON path.
func TestEventStreamTakesTheStreamBranch(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	e.up.ctype = "text/event-stream; charset=utf-8"
	e.up.body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n"
	w := e.do(t, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"trustagent":{"route":"x"}}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"Hi"`) || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Errorf("%d %q", w.Code, w.Body.String())
	}
	if _, fwd := e.up.last(t); strings.Contains(string(fwd), "trustagent") {
		t.Errorf("the extension was forwarded: %s", fwd)
	}
	traces := e.sink.traces()
	if len(traces) != 1 || !traces[0].Stream || traces[0].APIFormat != llmapi.FormatOpenAIChat {
		t.Errorf("a streamed request is traced with stream true: %+v", traces)
	}

	// A stream:true request whose upstream answers with a JSON error is not a stream.
	e.up.ctype, e.up.status, e.up.body = "application/json", http.StatusBadRequest, `{"error":{"message":"bad model"}}`
	w = e.do(t, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != http.StatusBadRequest || w.Body.String() != e.up.body || w.Header().Get(HeaderStatus) != "skipped:upstream_error" {
		t.Errorf("%d %q %q", w.Code, w.Body.String(), w.Header().Get(HeaderStatus))
	}
	e.sink.result(t)
}

func TestUpstreamErrorPassesThrough(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	e.up.status, e.up.body = http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Code != http.StatusTooManyRequests || w.Body.String() != e.up.body || w.Header().Get(HeaderStatus) != "skipped:upstream_error" {
		t.Errorf("%d %q %q", w.Code, w.Body.String(), w.Header().Get(HeaderStatus))
	}
	r := e.sink.result(t)
	if r.Upstream.Status != http.StatusTooManyRequests || r.Audit.Status != gate.AuditNotAssessed {
		t.Errorf("upstream %+v audit %+v", r.Upstream, r.Audit)
	}
}

func TestUpstreamDownIs502AndRecorded(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	e.up.Close()
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"upstream_error"`) || w.Header().Get(HeaderStatus) != "skipped:upstream_error" {
		t.Errorf("%d %s %q", w.Code, w.Body.String(), w.Header().Get(HeaderStatus))
	}
	if r := e.sink.result(t); r.Upstream.Status != 0 {
		t.Errorf("no upstream status: %+v", r.Upstream)
	}
}

func TestNoTextIsSkipped(t *testing.T) {
	e := newEnv(t, envOpts{policyYAML: "version: 1\ndefaults: {mode: flag}\n"})
	e.up.body = `{"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	w := e.do(t, "/v1/chat/completions", defaultRouteBody, nil)
	if w.Body.String() != e.up.body || w.Header().Get(HeaderStatus) != "skipped:no_text" {
		t.Errorf("%q %q", w.Body.String(), w.Header().Get(HeaderStatus))
	}
	e.sink.result(t)
}

func TestActionOnlyHeaders(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: "version: 1\ndefaults: {mode: flag, headers: action_only}\n"})
	w := e.do(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}],"test_response":"Hello!"}`, nil)
	for _, name := range ResponseHeaders {
		want := name == HeaderTraceID || name == HeaderRequestID || name == HeaderAction
		if got := w.Header().Get(name) != ""; got != want {
			t.Errorf("%s present=%v, want %v", name, got, want)
		}
	}
	e.sink.result(t)
}

func TestClientModeOverride(t *testing.T) {
	e := newEnv(t, envOpts{testMode: true, policyYAML: "version: 1\noverrides: {client_mode: stricter_only}\n"})
	w := e.do(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}],"test_response":"Hello!"}`,
		map[string]string{"X-TrustAgent-Mode": "flag"})
	if w.Header().Get(HeaderMode) != decision.ModeFlag || w.Header().Get(HeaderStatus) == StatusDeferred {
		t.Errorf("mode %q status %q", w.Header().Get(HeaderMode), w.Header().Get(HeaderStatus))
	}
	if r := e.sink.result(t); r.Trace.Mode != decision.ModeFlag {
		t.Errorf("trace mode %s", r.Trace.Mode)
	}
}

func TestPassthroughEndpoints(t *testing.T) {
	e := newEnv(t, envOpts{})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-TrustAgent-Key", ingestKey)
	w := newRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get(HeaderStatus) != "" || w.Header().Get(HeaderTraceID) == "" {
		t.Errorf("models: %d status %q", w.Code, w.Header().Get(HeaderStatus))
	}
	rw := e.do(t, "/v1/responses", `{"model":"m","input":"hi","trustagent":{"route":"x"}}`, nil)
	if rw.Header().Get(HeaderStatus) != StatusSkippedUnsupportedAPI {
		t.Errorf("responses status %q", rw.Header().Get(HeaderStatus))
	}
	if _, fwd := e.up.last(t); string(fwd) != `{"model":"m","input":"hi"}` {
		t.Errorf("responses forwarded %s", fwd)
	}
}

func TestBodyTooLarge(t *testing.T) {
	e := newEnv(t, envOpts{})
	w := e.do(t, "/v1/chat/completions", `{"x":"`+strings.Repeat("a", 1<<20)+`"}`, nil)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), CodeRequestTooLarge) {
		t.Errorf("%d %s", w.Code, w.Body.String()[:min(200, w.Body.Len())])
	}
}

func TestEveryResponseHeaderIsExposedToBrowsers(t *testing.T) {
	for _, h := range ResponseHeaders {
		if !slices.Contains(middleware.ExposedHeaders, h) {
			t.Errorf("%s is set by the gateway but not in Access-Control-Expose-Headers", h)
		}
	}
	e := newEnv(t, envOpts{})
	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", bytes.NewReader(nil))
	req.Header.Set("Origin", "http://localhost:5173")
	w := newRecorder()
	e.router.ServeHTTP(w, req)
	if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), HeaderTraceID) {
		t.Errorf("preflight exposes %q", w.Header().Get("Access-Control-Expose-Headers"))
	}
}
