package proxy

// The /v1 handler's own time (D120), in process: no network, no store, the mock decision model
// answering at once. BenchmarkHandlerOverhead sends a demo scenario per mode with its scripted
// answer (test mode, so no upstream is called and every nanosecond is the gateway's);
// BenchmarkSSERelayPerMB relays a 1 MiB passthrough stream from an upstream. The k6 load test
// (scripts/load) measures the same paths over HTTP under load.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/upstream"
	"github.com/truthtable/contracts"
)

// benchRouter is the /v1 handler on policy yaml ("" = config/policies.yaml) with decision model
// cl, upstream up, and a sink that records nothing.
func benchRouter(b *testing.B, yaml string, cl jev.Client, up *fakeUpstream) (*gin.Engine, *Handler) {
	b.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		b.Fatal(err)
	}
	pol := loadPolicy(b, reg, yaml, policy.Env{Enforcing: true})
	quiet := slog.New(slog.DiscardHandler)
	g, err := gate.New(gate.Config{Jev: cl, Evaluators: reg, UniformAuditsPerDay: 50, Rand: func() float64 { return 0.5 }, Logger: quiet})
	if err != nil {
		b.Fatal(err)
	}
	auth := authz.New([]authz.Key{{Label: "trafficgen", Secret: ingestKey, Scopes: authz.APIKeyScopes}}, "")
	client, err := upstream.New(upstream.Config{BaseURL: up.URL, Timeout: 30 * time.Second, HeaderTimeout: 30 * time.Second}, auth.IsProxyKey)
	if err != nil {
		b.Fatal(err)
	}
	set := &upstream.Set{OpenAI: client, Anthropic: client}
	h := New(Options{
		Upstreams: func() *upstream.Set { return set }, Policy: func() *policy.Policy { return pol }, Gate: g, Sink: gate.NopSink{},
		TestMode: true, Logger: quiet,
	})
	h.ProxyKeys(auth.IsProxyKey)
	r := gin.New()
	h.Mount(r.Group("/v1", h.TraceIDs(), auth.Ingest(Deny)), func(c *gin.Context) { c.Next() })
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = h.Drain(ctx)
	})
	return r, h
}

// benchScenario is a demo scenario's request with its scripted answer, and its expected action.
type benchScenario struct {
	id, action string
	stream     bool
	headers    map[string]string
	body       []byte
}

func loadBenchScenario(b *testing.B, id string) benchScenario {
	b.Helper()
	raw, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		b.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID       string            `json:"id"`
			Stream   bool              `json:"stream"`
			Headers  map[string]string `json:"headers"`
			Request  json.RawMessage   `json:"request"`
			Response string            `json:"response"`
			Expect   struct {
				Action string `json:"action"`
			} `json:"expect"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &pack); err != nil {
		b.Fatal(err)
	}
	for _, s := range pack.Scenarios {
		if s.ID != id {
			continue
		}
		var req map[string]any
		if err := json.Unmarshal(s.Request, &req); err != nil {
			b.Fatal(err)
		}
		req["test_response"] = s.Response
		if s.Stream {
			req["stream"] = true
		}
		body, _ := json.Marshal(req)
		headers := map[string]string{"X-TrustAgent-Scenario": s.ID}
		for k, v := range s.Headers {
			headers[k] = v
		}
		return benchScenario{id: s.ID, action: s.Expect.Action, stream: s.Stream, headers: headers, body: body}
	}
	b.Fatalf("no demo scenario %s", id)
	return benchScenario{}
}

// serve sends one request through r and returns the recorder.
func serve(r http.Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TrustAgent-Key", ingestKey)
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := newRecorder()
	r.ServeHTTP(w, req)
	return w.ResponseRecorder
}

// BenchmarkHandlerOverhead is one gated answer per mode, on the demo routes of
// config/policies.yaml: observe (faq-bot, decided after the response), flag (support-bot), block
// (help-center, a JSON answer it blocks) and buffer (help-center, a stream it buffers and blocks).
func BenchmarkHandlerOverhead(b *testing.B) {
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario, Strict: true}})
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct{ name, scenario string }{
		{"observe", "observe-would-block"},
		{"flag", "grounded-pass"},
		{"block", "help-center-link-block-json"},
		{"buffer", "help-center-link-block"},
	} {
		b.Run(c.name, func(b *testing.B) {
			r, _ := benchRouter(b, "", cl, newFakeUpstream(b))
			sc := loadBenchScenario(b, c.scenario)
			// The answer is the scenario's: the route decided, as the demo scenario says.
			w := serve(r, sc.body, sc.headers)
			if got := w.Header().Get(HeaderAction); w.Code != http.StatusOK || got != sc.action && !sc.stream {
				b.Fatalf("%s: HTTP %d action %q, want 200 and %s", sc.id, w.Code, got, sc.action)
			}
			b.ReportAllocs()
			for b.Loop() {
				if w := serve(r, sc.body, sc.headers); w.Code != http.StatusOK {
					b.Fatalf("HTTP %d", w.Code)
				}
			}
		})
	}
}

// relayPolicy has one passthrough flag route that asks nothing, so the relay itself is timed:
// the stream is read, sent on event by event, and its text kept for the gate after the end.
const relayPolicy = `version: 1
defaults: {mode: flag, require_calibration: false, streaming: passthrough, evaluators: [], detectors: {flag_on: [], block_on: []}, deep_audit: {enabled: false}, store_bodies: none}
`

// sseMiB is about 1 MiB of OpenAI chat completion chunks ending with [DONE].
func sseMiB() string {
	content := strings.Repeat("Annual plans can be refunded within 30 days of purchase. ", 4)
	chunk, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-relay", "object": "chat.completion.chunk", "created": 1, "model": "gpt-4o-mini",
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": nil}},
	})
	event := "data: " + string(chunk) + "\n\n"
	var b strings.Builder
	for b.Len() < 1<<20 {
		b.WriteString(event)
	}
	b.WriteString(`data: {"id":"chatcmpl-relay","object":"chat.completion.chunk","created":1,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// BenchmarkSSERelayPerMB relays a 1 MiB passthrough stream from the upstream (a local HTTP server)
// to the client; MB/s is the relay's throughput, the upstream's own serving included.
func BenchmarkSSERelayPerMB(b *testing.B) {
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockHash}})
	if err != nil {
		b.Fatal(err)
	}
	up := newFakeUpstream(b)
	stream := sseMiB()
	up.ctype, up.body = "text/event-stream", stream
	r, _ := benchRouter(b, relayPolicy, cl, up)
	body := []byte(`{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"How long is the refund window?"}]}`)
	w := serve(r, body, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "data: [DONE]") {
		b.Fatalf("HTTP %d, %d bytes relayed", w.Code, w.Body.Len())
	}
	if relayed := w.Body.Len(); relayed < len(stream) {
		b.Fatalf("%d of %d bytes relayed", relayed, len(stream))
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	for b.Loop() {
		if w := serve(r, body, nil); w.Code != http.StatusOK {
			b.Fatalf("HTTP %d", w.Code)
		}
	}
}
