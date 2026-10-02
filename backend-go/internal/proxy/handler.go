// Package proxy is the gateway's /v1 HTTP surface (SPEC §2.12). Each request is identified
// (TraceIDs), forwarded to the upstream of its API family, gated through internal/gate, and
// answered with what the decision delivers and the X-TrustAgent-* headers.
//
// Routes (Mount): POST /v1/chat/completions, /v1/completions and /v1/messages share one gated
// handler, Serve. /v1/messages/count_tokens (on the Anthropic upstream), /v1/models (on the
// upstream of the client's SDK family, D67) and /v1/embeddings pass through ungated, and so does
// the OpenAI Responses API
// (skipped:unsupported_api). Every error TrustAgent writes on /v1 takes the error shape of the
// route's API: Anthropic on /v1/messages and its sub-paths, OpenAI elsewhere.
//
// The route and its mode come from the policy in effect, read once per request. The answer takes
// the branch its response calls for:
//
//   - an event stream (a text/event-stream 2xx, or a stream:true test_response) is relayed and
//     gated by stream.go. Passthrough routes relay the events as they arrive and hold the
//     terminal event while a flag, block or fallback route decides; the decision then travels as
//     trailers and a ": trustagent" comment. A route with streaming: buffer (observe routes
//     always relay) reads the whole answer first and enforces every action (buffer.go);
//   - anything else is a JSON answer. Observe routes send it at once and gate it afterwards; every
//     other route gates it first and enforces the decision (enforce.go): block sends the route's
//     block body, fallback calls the route's fallback and re-gates its answer (fallback.go), pass
//     and flag send the upstream bytes.
//
// An answer the gate cannot check fails the way the route says (fail open or closed), except two
// kinds that are never enforced: an upstream error (a non-2xx passes through as it came, a failed
// call becomes a 502 or 504) and an answer without text (tool calls only). A JSON or buffered
// answer past TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES is relayed unread only on observe and flag
// routes; block and fallback routes block it. In test mode (TRUTHTABLE_TEST_MODE) a test_response
// answers in place of the upstream, and a scripted scenario's fallback answers from the scenario
// pack (testfallback.go), so the zero-key demo needs no LLM upstream.
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
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
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
	// Upstreams returns the upstreams in effect, by API family plus the optional fallback
	// upstream. It is called once per request, so a rebuilt set (dashboard upstream settings and
	// credentials, P4) applies from the next request on; nil, or a nil result, serves no upstream.
	Upstreams func() *upstream.Set
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
	// Knowledge retrieves knowledge-base context for gated answers on routes that read the
	// knowledge source (P4-PLAN D65, knowledge.go); nil means the add-on is off.
	Knowledge Retriever
	// Redact masks secrets in text the handler relays: in inject mode, the body of an upstream
	// error answer before it reaches the client (P4-PLAN D42, redact.go); nil is the identity.
	Redact func(string) string
}

// Retriever fetches knowledge-base documents for an answer (P4-PLAN D65): the handler starts it
// together with the upstream call and joins it before the gate within the knowledge deadline. An
// error or no documents means no knowledge, never a failed request; ErrKnowledgeDisabled reports
// the add-on off. response may be "" when the retrieval starts before the answer is known (the
// handler always starts it so: the query only). ctx carries the route's limits
// (KnowledgeOptionsFrom) and the deadline.
type Retriever interface {
	Retrieve(ctx context.Context, query, response string) ([]evidence.Item, error)
}

// Handler serves /v1.
type Handler struct {
	upsFn    func() *upstream.Set
	knowl    Retriever
	redact   func(string) string
	policy   func() *policy.Policy
	gate     *gate.Gate
	sink     gate.Sink
	testMode bool
	maxBytes int64
	log      *slog.Logger
	now      func() time.Time
	// rps are the passthrough reverse proxies of rpsSet, one per upstream client; they are rebuilt
	// when the upstream set changes.
	rpsMu  sync.Mutex
	rpsSet *upstream.Set
	rps    map[*upstream.Client]http.Handler
	// isProxyKey reports TrustAgent keys (ProxyKeys); /v1/models reads it to tell an Anthropic
	// client's own x-api-key from a proxy key.
	isProxyKey func(string) bool

	sem   chan struct{}
	async sync.WaitGroup
}

// New builds a Handler.
func New(o Options) *Handler {
	h := &Handler{
		upsFn: o.Upstreams, policy: o.Policy, gate: o.Gate, sink: o.Sink, testMode: o.TestMode,
		maxBytes: o.MaxUpstreamResponseBytes, log: o.Logger, now: o.Now, knowl: o.Knowledge, redact: o.Redact,
		isProxyKey: authz.IsMintedKey,
	}
	if h.redact == nil {
		h.redact = func(s string) string { return s }
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
	h.reverseProxies(h.upstreams()) // build the passthrough proxies of the initial set
	return h
}

// ProxyKeys sets how the handler recognises TrustAgent keys (authz.Authenticator.IsProxyKey):
// /v1/models picks its upstream family by the client's credential (D67). Call it before the
// handler serves; until then only minted keys (ta_…) count as proxy keys.
func (h *Handler) ProxyKeys(isProxyKey func(string) bool) {
	if isProxyKey != nil {
		h.isProxyKey = isProxyKey
	}
}

// noUpstreams is the set served when Options.Upstreams is nil or returns nil.
var noUpstreams = &upstream.Set{}

// upstreams is the upstream set in effect for one request (Options.Upstreams).
func (h *Handler) upstreams() *upstream.Set {
	if h.upsFn != nil {
		if s := h.upsFn(); s != nil {
			return s
		}
	}
	return noUpstreams
}

// reverseProxies returns the passthrough reverse proxies of set, building them once per set.
func (h *Handler) reverseProxies(set *upstream.Set) map[*upstream.Client]http.Handler {
	h.rpsMu.Lock()
	defer h.rpsMu.Unlock()
	if h.rpsSet == set && h.rps != nil {
		return h.rps
	}
	rps := map[*upstream.Client]http.Handler{}
	for _, c := range []*upstream.Client{set.OpenAI, set.Anthropic} {
		if _, ok := rps[c]; c != nil && !ok {
			rps[c] = h.redactingProxy(c)
		}
	}
	h.rpsSet, h.rps = set, rps
	return rps
}

// Mount registers the LLM API routes on v1, the /v1 group (SPEC §2.4 rows 71-73): the gated
// POST /v1/chat/completions, /v1/completions and /v1/messages; /v1/messages/count_tokens (on the
// Anthropic upstream), /v1/models (on the client's family, modelsFamily) and /v1/embeddings
// passed through; the Responses API passed
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

// Passthrough forwards a request ungated to the upstream of its API family: the Anthropic upstream
// for /v1/messages/count_tokens, the family modelsFamily picks for /v1/models (D67), the OpenAI
// upstream otherwise. A JSON body loses the gateway's members first, and a test_response outside
// test mode is refused (M7); status, when set, is sent as X-TrustAgent-Status. Errors take the
// family's error shape.
func (h *Handler) Passthrough(status string) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		family := llmapi.FamilyForPath(path)
		if isModelsPath(path) {
			family = h.modelsFamily(c.Request.Header)
		}
		format := llmapi.FormatOpenAIChat
		if family == llmapi.FamilyAnthropic {
			format = llmapi.FormatAnthropicMessages
		}
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
		set := h.upstreams()
		r := c.Request.WithContext(context.WithValue(c.Request.Context(), errorFormatKey{}, format))
		rp, ok := h.reverseProxies(set)[set.For(family)]
		if !ok {
			h.passthroughFailed(c.Writer, r, errNoUpstream)
			return
		}
		rp.ServeHTTP(c.Writer, r)
		// Ungated traffic counts in requests_total with no route, format or action, and adds no
		// overhead sample.
		metrics.ObserveRequest("", "", "", "", -1)
	}
}

// isModelsPath reports the model-list routes, /v1/models and /v1/models/{model}.
func isModelsPath(path string) bool {
	p := strings.TrimSuffix(path, "/")
	return p == llmapi.PathModels || strings.HasPrefix(p, llmapi.PathModels+"/")
}

// modelsFamily is the upstream family of a /v1/models request (D67): both SDK families list
// models there, so the client's headers decide. An Anthropic client sends anthropic-version (the
// SDKs always do) or its own key on x-api-key; anything else, a proxy key on x-api-key included,
// goes to the OpenAI upstream.
func (h *Handler) modelsFamily(hdr http.Header) string {
	if hdr.Get("Anthropic-Version") != "" {
		return llmapi.FamilyAnthropic
	}
	if k := strings.TrimSpace(hdr.Get("X-Api-Key")); k != "" && !h.isProxyKey(k) {
		return llmapi.FamilyAnthropic
	}
	return llmapi.FamilyOpenAI
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
	// Knowledge runs next to the upstream call (or the scripted answer) and is joined before the
	// gate (D65).
	h.startKnowledge(f)

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
	if f.up.Injecting() {
		// The headers the client receives may quote the gateway's own key (redact.go, D42).
		h.redactHeaders(resp.Header)
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
	// The gate waits on the record context: only its own deadline stops it (SPEC §2.9 late
	// decisions), so a client that hangs up mid-check leaves the real decision on record, not a
	// cancelled check (a timeout, and a fail-closed block on a closed route).
	res := h.evaluate(f.record, f)
	ctx := f.c.Request.Context()
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
		res := h.evaluate(f.record, f)
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
		res := h.evaluate(gctx, f)
		bounded := *f
		bounded.record = gctx
		h.decided(&bounded, res, ans.upstreamRecord(), written, nil)
	}()
}

// upstreamFailed answers a request whose upstream call failed outright (502, or 504 on a
// timeout, in the route's error shape) and records the trace as skipped:upstream_error, or
// skipped:undecodable for a 2xx whose body cannot be decoded (D24). The failure is never enforced
// (D4): the decision is recorded with enforced false and the delivered action is pass. A call
// that ended because the client went away is no upstream failure: it is recorded as
// skipped:client_disconnected, unenforced, and nothing is written.
func (h *Handler) upstreamFailed(f *flow, err error) {
	c, x := f.c, f.x
	var up gate.Upstream
	if !f.upStart.IsZero() {
		ms := h.now().Sub(f.upStart).Milliseconds()
		up.Ms = &ms
	}
	var answered *answerError
	if errors.As(err, &answered) {
		up.Status = answered.status
	}
	gone := clientGone(c, err)
	x.SkipReason, x.Unenforced = gate.SkipUpstreamError, true
	switch {
	case gone:
		h.log.Debug("the client went away before the upstream answered", "trace_id", x.Trace.TraceID)
		x.SkipReason = gate.SkipClientDisconnected
	case errors.Is(err, upstream.ErrUndecodable) && up.Status >= 200 && up.Status <= 299:
		// Nothing readable to gate or to deliver: a 502 as for a failed call.
		h.log.Warn("upstream answer cannot be decoded", "trace_id", x.Trace.TraceID, "err", err)
		x.SkipReason = gate.SkipUndecodable
	default:
		h.log.Warn("upstream call failed", "trace_id", x.Trace.TraceID, "err", err)
	}
	res := h.evaluate(f.record, f)
	fin := &gate.Final{Action: unenforcedAction(res.Decision.Action), Delivered: gate.DeliveredOriginal}
	end := h.now()
	audit := h.decided(f, res, up, end, fin)
	h.countRequest(f, fin.Action, up, end)
	if gone {
		return
	}
	status, code, msg := http.StatusBadGateway, CodeUpstreamError, "The upstream LLM API could not be reached."
	if upstream.IsTimeout(err) {
		status, code, msg = http.StatusGatewayTimeout, CodeUpstreamTimeout, "The upstream LLM API did not answer in time."
	}
	setDecisionHeaders(c.Writer.Header(), res, audit)
	writeError(c.Writer, f.format, status, code, msg)
}

// clientGone reports whether err ended an upstream call because the client went away (the
// request context the call runs on was cancelled), not because the upstream failed or timed out.
func clientGone(c *gin.Context, err error) bool {
	return errors.Is(err, context.Canceled) && c.Request.Context().Err() != nil
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
