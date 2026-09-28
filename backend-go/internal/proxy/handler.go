// Package proxy is the gateway's /v1 HTTP surface (SPEC §2.12): it identifies each request,
// forwards it upstream, gates the answer through internal/gate and writes the response with the
// X-TrustAgent-* headers.
//
// S0 gates non-streaming POST /v1/chat/completions on observe and flag routes. Streaming chat
// requests, /v1/completions and /v1/responses pass through ungated; /v1/models and /v1/embeddings
// are plain passthrough. Streaming gating, block and fallback bodies, /v1/messages and gated
// completions land in P3.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
)

// Error codes the /v1 surface writes (in the OpenAI error shape).
const (
	CodeTestModeDisabled = "test_mode_disabled"
	CodeRequestTooLarge  = "request_too_large"
	CodeBadRequest       = "bad_request"
	CodeUpstreamError    = "upstream_error"
	CodeUpstreamTimeout  = "upstream_timeout"
)

// TrafficgenKeyLabel is the ingest key label whose traffic is recorded with source trafficgen.
const TrafficgenKeyLabel = "trafficgen"

// Options configures a Handler.
type Options struct {
	Upstream *upstream.Client
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
	up       *upstream.Client
	policy   func() *policy.Policy
	gate     *gate.Gate
	sink     gate.Sink
	testMode bool
	maxBytes int64
	log      *slog.Logger
	now      func() time.Time
	rp       http.Handler

	sem   chan struct{}
	async sync.WaitGroup
}

// New builds a Handler.
func New(o Options) *Handler {
	h := &Handler{
		up: o.Upstream, policy: o.Policy, gate: o.Gate, sink: o.Sink, testMode: o.TestMode,
		maxBytes: o.MaxUpstreamResponseBytes, log: o.Logger, now: o.Now,
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
	h.rp = h.up.ReverseProxy(func(w http.ResponseWriter, r *http.Request, err error) {
		h.log.Warn("upstream passthrough failed", "path", r.URL.Path, "err", err)
		status, code := http.StatusBadGateway, CodeUpstreamError
		if upstream.IsTimeout(err) {
			status, code = http.StatusGatewayTimeout, CodeUpstreamTimeout
		}
		writeOpenAIError(w, status, code, "The upstream LLM API could not be reached.")
	})
	return h
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

// Deny writes an authentication failure in the OpenAI error shape (the authz.Deny of /v1).
func Deny(c *gin.Context, status int, code, message string) {
	writeOpenAIError(c.Writer, status, code, message)
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

// Passthrough forwards a request upstream ungated. A JSON body loses the gateway's members first;
// status, when set, is sent as X-TrustAgent-Status (endpoints that will be gated later).
func (h *Handler) Passthrough(status string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			body, err := io.ReadAll(c.Request.Body)
			if err != nil {
				readError(c, err)
				return
			}
			if req, err := llmapi.ParseChatRequest(body); err == nil {
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
		h.rp.ServeHTTP(c.Writer, c.Request)
	}
}

// ChatCompletions gates POST /v1/chat/completions.
func (h *Handler) ChatCompletions(c *gin.Context) {
	start := h.now()
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		readError(c, err)
		return
	}
	req, parseErr := llmapi.ParseChatRequest(body)
	if req != nil && req.TestResponse != nil && !h.testMode {
		testModeDisabled(c)
		return
	}
	forward := body
	if req != nil {
		if fwd, err := llmapi.ForwardBody(body); err == nil {
			forward = fwd
		}
	}
	if req != nil && req.Stream && req.TestResponse == nil {
		// S0 does not gate streams: forward the stripped request and relay the stream as is.
		c.Header(HeaderStatus, StatusSkippedStream)
		setBody(c.Request, forward)
		h.rp.ServeHTTP(c.Writer, c.Request)
		return
	}

	x, route := h.newExchange(c, req, start)
	if parseErr != nil {
		x.SkipReason = gate.SkipUnparseableRequest
	}
	// The record outlives the client: a client that goes away still gets its trace written.
	record := context.WithoutCancel(c.Request.Context())
	h.sink.TraceStarted(record, x.Trace)

	ans, err := h.answer(c, req, x, forward, start)
	if err != nil {
		h.upstreamFailed(c, x, err, start)
		return
	}
	defer ans.close()
	x.Response = ans.text
	if x.SkipReason == "" {
		x.SkipReason = ans.skip
	}
	if x.Trace.Mode == decision.ModeObserve && x.SkipReason == "" {
		h.respondThenGate(c, x, route, ans, start)
		return
	}
	res := h.gate.Evaluate(c.Request.Context(), x)
	h.finish(res, ans, start, h.now())
	audit := h.sink.GateDecided(record, res)
	upstream.CopyResponseHeaders(c.Writer.Header(), ans.header)
	setDecisionHeaders(c.Writer.Header(), res, audit)
	ans.write(c.Writer, h.log)
}

// newExchange resolves the route and mode and builds the trace of a gated request.
func (h *Handler) newExchange(c *gin.Context, req *llmapi.ChatRequest, start time.Time) (*gate.Exchange, *policy.Route) {
	var model, bodyRoute string
	if req != nil {
		model, bodyRoute = req.Model, req.Extension.Route
	}
	keyLabel := authz.KeyLabel(c)
	// One policy per request: a reload mid-request cannot mix two versions.
	pol := h.policy()
	route := pol.Match(&policy.Request{
		Path: c.Request.URL.Path, Model: model, Headers: c.Request.Header, KeyLabel: keyLabel, BodyRoute: bodyRoute,
	})
	mode, _ := pol.Mode(route, c.GetHeader(HeaderClientMode))

	t := &gate.TraceStart{
		TraceID: c.GetString(traceIDKey), CreatedAt: start.UTC(), Source: gate.SourceProxy,
		APIFormat: llmapi.FormatOpenAIChat, Path: c.Request.URL.Path, Route: route.Name,
		PolicyVersion: pol.Version, Mode: mode, KeyLabel: keyLabel, Model: model,
		AnswerSource: gate.AnswerLive, Flags: []string{},
	}
	if id := c.GetString(clientRequestIDKey); id != "" {
		t.ClientRequestID = &id
	}
	if h.testMode {
		if s := c.GetHeader(HeaderScenario); scenarioID.MatchString(s) {
			t.Scenario = s
		}
	}
	if req != nil && req.TestResponse != nil {
		t.Source, t.AnswerSource = gate.SourceTest, gate.AnswerScripted
		t.Flags = append(t.Flags, gate.FlagTestMode)
	}
	if keyLabel == TrafficgenKeyLabel || t.Scenario != "" {
		t.Source = gate.SourceTrafficgen
	}

	x := &gate.Exchange{Trace: t, Policy: pol, Route: route, Request: req}
	if req != nil {
		settings := route.Evidence.Extraction()
		ev, err := evidence.Extract(req, &settings)
		if err != nil {
			h.log.Error("evidence extraction failed", "trace_id", t.TraceID, "route", route.Name, "err", err)
		} else {
			x.Evidence = ev
			t.UserRequest = ev.UserRequest
		}
	}
	return x, route
}

// answer is the upstream answer to a gated request.
type answer struct {
	status int
	header http.Header
	// body is what was read; rest, when set, is the unread remainder of an oversized body.
	body []byte
	rest io.Reader
	// text is the gated text; skip is set when the answer cannot be gated.
	text  string
	skip  string
	ms    *int64
	usage *llmapi.Usage
	close func()
}

// answer gets the upstream answer: the scripted test_response in test mode, else the upstream
// call. An error means there is no upstream response at all.
func (h *Handler) answer(c *gin.Context, req *llmapi.ChatRequest, x *gate.Exchange, forward []byte, start time.Time) (*answer, error) {
	if req != nil && req.TestResponse != nil {
		zero := int64(0)
		a := &answer{
			status: http.StatusOK, header: http.Header{"Content-Type": {"application/json"}},
			body: llmapi.SyntheticChatCompletion(x.Trace.TraceID, req.Model, start.Unix(), *req.TestResponse),
			text: *req.TestResponse, ms: &zero, close: func() {},
		}
		if a.text == "" {
			a.skip = gate.SkipNoText
		}
		return a, nil
	}
	upStart := h.now()
	resp, cancel, err := h.up.Do(c.Request.Context(), c.Request.Method, c.Request.URL.Path, c.Request.URL.RawQuery, c.Request.Header, forward)
	if err != nil {
		return nil, err
	}
	a := &answer{status: resp.StatusCode, header: resp.Header, close: func() { _ = resp.Body.Close(); cancel() }}
	body, tooLarge, err := upstream.ReadBody(resp, h.maxBytes)
	ms := h.now().Sub(upStart).Milliseconds()
	a.ms = &ms
	if err != nil {
		a.close()
		return nil, err
	}
	a.body = body
	switch {
	case tooLarge:
		a.rest, a.skip = resp.Body, gate.SkipUpstreamError
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		a.skip = gate.SkipUpstreamError
	default:
		parsed, err := llmapi.ParseChatResponse(body)
		if err != nil {
			a.skip = gate.SkipUpstreamError
			break
		}
		a.usage = parsed.Usage
		if choice, ok := parsed.Gated(); ok && choice.Text != "" {
			a.text = choice.Text
		} else {
			a.skip = gate.SkipNoText
		}
	}
	return a, nil
}

// write sends the answer: status, then the body as the upstream sent it.
func (a *answer) write(w http.ResponseWriter, log *slog.Logger) {
	w.WriteHeader(a.status)
	if _, err := w.Write(a.body); err != nil {
		log.Debug("client went away while the response was written", "err", err)
		return
	}
	if a.rest != nil {
		if _, err := io.Copy(w, a.rest); err != nil {
			log.Debug("oversized upstream body relay stopped", "err", err)
		}
	}
}

// respondThenGate serves an observe-mode answer at once and gates it afterwards (SPEC §2.12: the
// body is byte-identical and the decision is recorded asynchronously). When too many gates are
// already running, this one is recorded as skipped:overload with detectors only.
func (h *Handler) respondThenGate(c *gin.Context, x *gate.Exchange, route *policy.Route, ans *answer, start time.Time) {
	upstream.CopyResponseHeaders(c.Writer.Header(), ans.header)
	setDeferredHeaders(c.Writer.Header(), x.Trace, route, x.Policy.Label(route))
	ans.write(c.Writer, h.log)
	written := h.now()

	ctx := context.WithoutCancel(c.Request.Context())
	select {
	case h.sem <- struct{}{}:
	default:
		x.SkipReason = gate.SkipOverload
		res := h.gate.Evaluate(ctx, x)
		h.finish(res, ans, start, written)
		h.sink.GateDecided(ctx, res)
		return
	}
	h.async.Add(1)
	go func() {
		defer h.async.Done()
		defer func() { <-h.sem }()
		// The gate bounds its decision-model call; this bounds the rest (and a stuck sink).
		gctx, cancel := context.WithTimeout(ctx, 2*h.gate.AsyncDeadline())
		defer cancel()
		res := h.gate.Evaluate(gctx, x)
		h.finish(res, ans, start, written)
		h.sink.GateDecided(gctx, res)
	}()
}

// finish fills the timings of a result: total runs to when the response was (or is about to be)
// written, and overhead is total minus the upstream time.
func (h *Handler) finish(res *gate.Result, ans *answer, start, end time.Time) {
	total := end.Sub(start).Milliseconds()
	res.TotalMs = &total
	res.Upstream = gate.Upstream{Status: ans.status, Ms: ans.ms}
	overhead := total
	if ans.ms != nil {
		overhead = max(total-*ans.ms, 0)
	}
	res.OverheadMs = &overhead
	if ans.usage != nil {
		pt, ct := ans.usage.PromptTokens, ans.usage.CompletionTokens
		res.Upstream.PromptTokens, res.Upstream.CompletionTokens = &pt, &ct
	}
}

// upstreamFailed answers a request whose upstream call failed outright (502, or 504 on a
// timeout) and records the trace as skipped:upstream_error.
func (h *Handler) upstreamFailed(c *gin.Context, x *gate.Exchange, err error, start time.Time) {
	h.log.Warn("upstream call failed", "trace_id", x.Trace.TraceID, "err", err)
	status, code, msg := http.StatusBadGateway, CodeUpstreamError, "The upstream LLM API could not be reached."
	if upstream.IsTimeout(err) {
		status, code, msg = http.StatusGatewayTimeout, CodeUpstreamTimeout, "The upstream LLM API did not answer in time."
	}
	x.SkipReason = gate.SkipUpstreamError
	ctx := context.WithoutCancel(c.Request.Context())
	res := h.gate.Evaluate(ctx, x)
	h.finish(res, &answer{}, start, h.now())
	audit := h.sink.GateDecided(ctx, res)
	setDecisionHeaders(c.Writer.Header(), res, audit)
	writeOpenAIError(c.Writer, status, code, msg)
}

func readError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeOpenAIError(c.Writer, http.StatusRequestEntityTooLarge, CodeRequestTooLarge, "The request body is too large.")
		return
	}
	writeOpenAIError(c.Writer, http.StatusBadRequest, CodeBadRequest, "The request body could not be read.")
}

func testModeDisabled(c *gin.Context) {
	writeOpenAIError(c.Writer, http.StatusBadRequest, CodeTestModeDisabled,
		"test_response is honoured only when the gateway runs with TRUTHTABLE_TEST_MODE=true.")
}

// writeOpenAIError writes a TrustAgent error in the OpenAI error shape (SPEC §2.12).
func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(llmapi.OpenAIError(code, message))
}

// setBody replaces a request body (passthrough after the gateway's members are removed).
func setBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	r.Header.Del("Content-Length")
}
