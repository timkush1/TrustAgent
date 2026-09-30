// Package proxy is the gateway's /v1 HTTP surface (SPEC §2.12): it identifies each request,
// forwards it to the upstream of its API family, gates the answer through internal/gate, enforces
// the decision and writes the response with the X-TrustAgent-* headers.
//
// POST /v1/chat/completions, /v1/completions and /v1/messages share one gated handler, Serve. The
// answer takes the branch its response calls for: an event stream (a text/event-stream 2xx, or a
// stream:true test_response) is relayed and gated by stream.go; anything else is a JSON answer,
// gated before it is written and enforced per route (enforce.go): block routes send the route's
// block body, fallback routes the fallback answer (fallback.go), pass, flag and observe the
// upstream bytes. Observe routes answer first and gate afterwards. An upstream error or an answer
// without text is never enforced. /v1/messages/count_tokens, /v1/models and /v1/embeddings pass
// through ungated, and so does the Responses API (skipped:unsupported_api). Every error TrustAgent
// writes on /v1 takes the error shape of the route's API: Anthropic on /v1/messages, OpenAI
// elsewhere.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
)

// Error codes the /v1 surface writes (the code member of the OpenAI error shape; the Anthropic
// shape carries a type derived from the status instead).
const (
	CodeTestModeDisabled = "test_mode_disabled"
	CodeRequestTooLarge  = "request_too_large"
	CodeBadRequest       = "bad_request"
	CodeNotFound         = "not_found"
	CodeUpstreamError    = "upstream_error"
	CodeUpstreamTimeout  = "upstream_timeout"
)

// TrafficgenKeyLabel is the ingest key label whose traffic is recorded with source trafficgen.
const TrafficgenKeyLabel = "trafficgen"

// Options configures a Handler.
type Options struct {
	// Upstreams are the upstreams by API family, plus the optional fallback upstream.
	Upstreams *upstream.Set
	// Policy returns the policy in effect; it is called once per request, so a saved, rolled back
	// or hot-reloaded version applies from the next request on (policy.Engine.Current).
	Policy func() *policy.Policy
	Gate   *gate.Gate
	// Sink records traces and decisions and submits audits; nil means gate.NopSink.
	Sink gate.Sink
	// TestMode honours test_response and X-TrustAgent-Scenario (TRUTHTABLE_TEST_MODE, M7).
	TestMode bool
	// MaxUpstreamResponseBytes is the largest upstream body that is gated.
	MaxUpstreamResponseBytes int64
	// GateMaxConcurrency bounds the observe-mode gates running after their response.
	GateMaxConcurrency int
	Logger             *slog.Logger
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

// Handler serves /v1.
type Handler struct {
	ups      *upstream.Set
	policy   func() *policy.Policy
	gate     *gate.Gate
	sink     gate.Sink
	testMode bool
	maxBytes int64
	log      *slog.Logger
	now      func() time.Time
	// rps are the passthrough reverse proxies, one per upstream client.
	rps map[*upstream.Client]http.Handler

	sem   chan struct{}
	async sync.WaitGroup
}

// New builds a Handler.
func New(o Options) *Handler {
	h := &Handler{
		ups: o.Upstreams, policy: o.Policy, gate: o.Gate, sink: o.Sink, testMode: o.TestMode,
		maxBytes: o.MaxUpstreamResponseBytes, log: o.Logger, now: o.Now,
	}
	if h.ups == nil {
		h.ups = &upstream.Set{}
	}
	if h.sink == nil {
		h.sink = gate.NopSink{}
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.now == nil {
		h.now = time.Now
	}
	if h.maxBytes <= 0 {
		h.maxBytes = 8 << 20
	}
	n := o.GateMaxConcurrency
	if n <= 0 {
		n = 256
	}
	h.sem = make(chan struct{}, n)
	h.rps = map[*upstream.Client]http.Handler{}
	for _, c := range []*upstream.Client{h.ups.OpenAI, h.ups.Anthropic} {
		if _, ok := h.rps[c]; c != nil && !ok {
			h.rps[c] = c.ReverseProxy(h.passthroughFailed)
		}
	}
	return h
}

// Mount registers the LLM API routes on v1, the /v1 group (SPEC §2.4 rows 71-73): the gated
// POST /v1/chat/completions, /v1/completions and /v1/messages; /v1/messages/count_tokens (on the
// Anthropic upstream), /v1/models and /v1/embeddings passed through; the Responses API passed
// through ungated with X-TrustAgent-Status skipped:unsupported_api. bodyLimit bounds the request
// body of every route, the passthrough ones included (Passthrough reads the whole body to strip
// the gateway's members); nil sets no bound.
func (h *Handler) Mount(v1 gin.IRoutes, bodyLimit gin.HandlerFunc) {
	if bodyLimit == nil {
		bodyLimit = func(c *gin.Context) { c.Next() }
	}
	v1.POST("/chat/completions", bodyLimit, h.Serve)
	v1.POST("/completions", bodyLimit, h.Serve)
	v1.POST("/messages", bodyLimit, h.Serve)
	v1.POST("/messages/count_tokens", bodyLimit, h.Passthrough(""))
	v1.Any("/responses", bodyLimit, h.Passthrough(StatusSkippedUnsupportedAPI))
	v1.Any("/responses/*rest", bodyLimit, h.Passthrough(StatusSkippedUnsupportedAPI))
	v1.Any("/models", bodyLimit, h.Passthrough(""))
	v1.Any("/models/*model", bodyLimit, h.Passthrough(""))
	v1.Any("/embeddings", bodyLimit, h.Passthrough(""))
}

// gin context keys set by TraceIDs.
const (
	traceIDKey         = "trustagent.trace_id"
	clientRequestIDKey = "trustagent.client_request_id"
)

// scenarioID is the shape of a scenario id (contracts/schema/scenarios.v1.schema.json).
var scenarioID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// TraceIDs assigns every /v1 request its UUIDv7 trace id and answers X-TrustAgent-Trace-Id and
// X-Request-ID on every response, errors included (SPEC §2.1, §2.12). A client X-Request-ID that
// passes the B4 rule is echoed and recorded as client_request_id; any other is dropped with a
// warning and X-Request-ID carries the trace id.
func (h *Handler) TraceIDs() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := store.NewID()
		requestID := traceID
		if raw := c.GetHeader(HeaderRequestID); raw != "" {
			if id, ok := store.ClientRequestID(raw); ok {
				requestID = id
				c.Set(clientRequestIDKey, id)
			} else {
				h.log.Warn("dropping an invalid X-Request-ID", "trace_id", traceID, "length", len(raw))
			}
		}
		c.Set(traceIDKey, traceID)
		c.Header(HeaderTraceID, traceID)
		c.Header(HeaderRequestID, requestID)
		c.Next()
	}
}

// Deny writes a TrustAgent error in the error shape of the request path's API (the authz.Deny of
// /v1, also used by its rate limiter and unknown paths): Anthropic on /v1/messages and its
// sub-paths, OpenAI elsewhere.
func Deny(c *gin.Context, status int, code, message string) {
	writeError(c.Writer, familyFormat(c.Request.URL.Path), status, code, message)
}

// Drain waits for the observe-mode gates still running, or for ctx.
func (h *Handler) Drain(ctx context.Context) error {
	done := make(chan struct{})
	go func() { h.async.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// errorFormatKey carries a passthrough request's error format to the reverse proxy's error hook,
// which sees only the outgoing request (whose path may carry the upstream's base path).
type errorFormatKey struct{}

// Passthrough forwards a request ungated to the upstream of its path's API family (the Anthropic
// upstream for /v1/messages/count_tokens, the OpenAI upstream otherwise). A JSON body loses the
// gateway's members first, and a test_response outside test mode is refused (M7); status, when
// set, is sent as X-TrustAgent-Status.
func (h *Handler) Passthrough(status string) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		format := familyFormat(path)
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				readError(c, err)
				return
			}
			if req, err := llmapi.ParseRequest(format, body); err == nil {
				if req.TestResponse != nil && !h.testMode {
					testModeDisabled(c)
					return
				}
				if fwd, err := llmapi.ForwardBody(body); err == nil {
					body = fwd
				}
			}
			setBody(c.Request, body)
		}
		if status != "" {
			c.Header(HeaderStatus, status)
		}
		rp, ok := h.rps[h.ups.For(llmapi.FamilyForPath(path))]
		if !ok {
			h.passthroughFailed(c.Writer, c.Request, errNoUpstream)
			return
		}
		r := c.Request.WithContext(context.WithValue(c.Request.Context(), errorFormatKey{}, format))
		rp.ServeHTTP(c.Writer, r)
		// Ungated traffic counts in requests_total with no route, format or action, and adds no
		// overhead sample.
		metrics.ObserveRequest("", "", "", "", -1)
	}
}

// passthroughFailed answers a passthrough request whose upstream call failed: 502, or 504 on a
// timeout, in the error shape of the request's API.
func (h *Handler) passthroughFailed(w http.ResponseWriter, r *http.Request, err error) {
	format, _ := r.Context().Value(errorFormatKey{}).(string)
	if format == "" {
		format = familyFormat(r.URL.Path)
	}
	h.log.Warn("upstream passthrough failed", "path", r.URL.Path, "err", err)
	status, code := http.StatusBadGateway, CodeUpstreamError
	if upstream.IsTimeout(err) {
		status, code = http.StatusGatewayTimeout, CodeUpstreamTimeout
	}
	writeError(w, format, status, code, "The upstream LLM API could not be reached.")
}

// errNoUpstream is the failure of a request whose API family has no upstream configured.
var errNoUpstream = errors.New("proxy: no upstream is configured for this API")

// Serve gates POST /v1/chat/completions, /v1/completions and /v1/messages (SPEC §2.4 row 71). The
// body is parsed tolerantly in the path's API format and forwarded without the gateway's members
// to the upstream of the format's family; in test mode a test_response answers instead of the
// upstream. The answer takes the branch its response calls for: an event stream (the upstream's
// text/event-stream 2xx, or a stream:true test_response) goes to serveStream; anything else is a
// JSON answer (serveJSON), non-2xx responses and non-JSON bodies included (M3).
func (h *Handler) Serve(c *gin.Context) {
	start := h.now()
	path := c.Request.URL.Path
	format := llmapi.FormatForPath(path)
	if format == "" {
		format = llmapi.FormatForPath(c.FullPath())
	}
	if format == "" {
		// Mount serves only the gated paths here; any other is not an LLM API TrustAgent gates.
		writeError(c.Writer, familyFormat(path), http.StatusNotFound, CodeNotFound, "TrustAgent does not gate this path.")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		readError(c, err)
		return
	}
	req, err := llmapi.ParseRequest(format, body)
	if err != nil {
		req = nil
	}
	if req != nil && req.TestResponse != nil && !h.testMode {
		testModeDisabled(c)
		return
	}
	f := h.newFlow(c, format, path, body, req, start)
	h.sink.TraceStarted(f.record, f.x.Trace)

	if req != nil && req.TestResponse != nil {
		if req.Stream {
			h.serveStream(f, nil, h.synthStream(f, *req.TestResponse))
			return
		}
		h.serveJSON(f, h.scriptedAnswer(f, *req.TestResponse))
		return
	}
	if f.up == nil {
		h.upstreamFailed(f, errNoUpstream)
		return
	}
	f.upStart = h.now()
	resp, cancel, err := f.up.Do(c.Request.Context(), c.Request.Method, path, c.Request.URL.RawQuery, c.Request.Header, f.forward)
	if err != nil {
		h.upstreamFailed(f, err)
		return
	}
	if isEventStream(resp) {
		stream := f.up.StreamBody(resp, cancel)
		defer func() { _ = stream.Close(); cancel() }()
		h.serveStream(f, resp, stream)
		return
	}
	ans, err := h.upstreamAnswer(f, resp, cancel)
	if err != nil {
		h.upstreamFailed(f, err)
		return
	}
	h.serveJSON(f, ans)
}

// isEventStream reports whether an upstream response is a successful event stream: a 2xx whose
// Content-Type is text/event-stream. Anything else takes the JSON path (M3).
func isEventStream(resp *http.Response) bool {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}

// serveJSON gates a JSON answer and writes what the decision delivers (enforceJSON). An observe
// route writes the answer first and gates it afterwards (respondThenGate), unless the answer
// cannot be gated.
func (h *Handler) serveJSON(f *flow, ans *answer) {
	defer ans.close()
	f.takeAnswer(ans)
	x := f.x
	if x.Trace.Mode == decision.ModeObserve && x.SkipReason == "" {
		h.respondThenGate(f, ans)
		return
	}
	ctx := f.c.Request.Context()
	res := h.gate.Evaluate(ctx, x)
	out := h.enforceJSON(ctx, f, res, ans)
	up, end := ans.upstreamRecord(), h.now()
	audit := h.decided(f, res, up, end, out.final)
	h.countRequest(f, out.final.Action, up, end)
	h.writeJSON(f, res, audit, ans.header, out)
}

// writeJSON writes a decided JSON exchange: the upstream's response headers, the decision
// headers, then out.
func (h *Handler) writeJSON(f *flow, res *gate.Result, audit string, upHeader http.Header, out *jsonOut) {
	hdr := f.c.Writer.Header()
	upstream.CopyResponseHeaders(hdr, upHeader)
	if out.built {
		hdr.Set("Content-Type", "application/json")
	}
	setDecisionHeaders(hdr, res, audit)
	writeBody(f.c.Writer, out.status, out.body, out.rest, h.log)
}

// respondThenGate serves an observe-mode answer at once and gates it afterwards (SPEC §2.12: the
// body is byte-identical and the decision is recorded asynchronously). When too many gates are
// already running, this one is recorded as skipped:overload with detectors only.
func (h *Handler) respondThenGate(f *flow, ans *answer) {
	c, x := f.c, f.x
	upstream.CopyResponseHeaders(c.Writer.Header(), ans.header)
	setDeferredHeaders(c.Writer.Header(), x.Trace, f.route, x.Policy.Label(f.route))
	ans.write(c.Writer, h.log)
	written := h.now()
	h.countRequest(f, decision.ActionObserve, ans.upstreamRecord(), written)

	select {
	case h.sem <- struct{}{}:
	default:
		x.SkipReason = gate.SkipOverload
		res := h.gate.Evaluate(f.record, x)
		h.decided(f, res, ans.upstreamRecord(), written, nil)
		return
	}
	h.async.Add(1)
	go func() {
		defer h.async.Done()
		defer func() { <-h.sem }()
		// The gate bounds its decision-model call; this bounds the rest (and a stuck sink).
		gctx, cancel := context.WithTimeout(f.record, 2*h.gate.AsyncDeadline())
		defer cancel()
		res := h.gate.Evaluate(gctx, x)
		bounded := *f
		bounded.record = gctx
		h.decided(&bounded, res, ans.upstreamRecord(), written, nil)
	}()
}

// upstreamFailed answers a request whose upstream call failed outright (502, or 504 on a
// timeout, in the route's error shape) and records the trace as skipped:upstream_error. The
// failure is never enforced (D4): the decision is recorded with enforced false and the
// delivered action is pass.
func (h *Handler) upstreamFailed(f *flow, err error) {
	c, x := f.c, f.x
	var up gate.Upstream
	if !f.upStart.IsZero() {
		ms := h.now().Sub(f.upStart).Milliseconds()
		up.Ms = &ms
	}
	h.log.Warn("upstream call failed", "trace_id", x.Trace.TraceID, "err", err)
	status, code, msg := http.StatusBadGateway, CodeUpstreamError, "The upstream LLM API could not be reached."
	if upstream.IsTimeout(err) {
		status, code, msg = http.StatusGatewayTimeout, CodeUpstreamTimeout, "The upstream LLM API did not answer in time."
	}
	x.SkipReason, x.Unenforced = gate.SkipUpstreamError, true
	res := h.gate.Evaluate(f.record, x)
	fin := &gate.Final{Action: unenforcedAction(res.Decision.Action), Delivered: gate.DeliveredOriginal}
	end := h.now()
	audit := h.decided(f, res, up, end, fin)
	h.countRequest(f, fin.Action, up, end)
	setDecisionHeaders(c.Writer.Header(), res, audit)
	writeError(c.Writer, f.format, status, code, msg)
}

func readError(c *gin.Context, err error) {
	format := familyFormat(c.Request.URL.Path)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(c.Writer, format, http.StatusRequestEntityTooLarge, CodeRequestTooLarge, "The request body is too large.")
		return
	}
	writeError(c.Writer, format, http.StatusBadRequest, CodeBadRequest, "The request body could not be read.")
}

// testModeDisabled refuses a test_response outside test mode: 400 test_mode_disabled (M7, D8).
func testModeDisabled(c *gin.Context) {
	writeError(c.Writer, familyFormat(c.Request.URL.Path), http.StatusBadRequest, CodeTestModeDisabled,
		"test_response is honoured only when the gateway runs with TRUTHTABLE_TEST_MODE=true.")
}

// familyFormat is the API format of a /v1 path's family, which sets its error shape and how a
// passthrough body is read: anthropic_messages on /v1/messages and its sub-paths, openai_chat
// everywhere else.
func familyFormat(path string) string {
	if llmapi.FamilyForPath(path) == llmapi.FamilyAnthropic {
		return llmapi.FormatAnthropicMessages
	}
	return llmapi.FormatOpenAIChat
}

// writeError writes a TrustAgent error on a /v1 route in the error shape of format's API (SPEC
// §2.12): {"error":{"message","type":"trustagent_error","param":null,"code"}} on the OpenAI
// formats, {"type":"error","error":{"type","message"}} on anthropic_messages.
func writeError(w http.ResponseWriter, format string, status int, code, message string) {
	// The format, which follows the request path, only picks the shape: the body is built from the
	// gateway's own status, code and message (llmapi.ErrorBody, spelled out).
	body := llmapi.OpenAIError(code, message)
	if llmapi.IsAnthropic(format) {
		body = llmapi.AnthropicError(llmapi.AnthropicErrorType(status), message)
	}
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// setBody replaces a request body (passthrough after the gateway's members are removed).
func setBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	r.Header.Del("Content-Length")
}
