package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/upstream"
)

// The helpers a gated exchange is built from, shared with the stream relay (stream.go, buffer.go)
// and the Playground (playground.go). Their signatures are fixed; the flow, answer, jsonOut and
// fallbackOut structs are append-only. A JSON exchange runs them in this order (serveJSON):
//
//  1. f := h.newFlow(c, format, path, raw, req, start) resolves the route and mode, builds the
//     trace and its evidence, and picks the upstream; the caller then records
//     sink.TraceStarted(f.record, f.x.Trace);
//  2. ans := h.scriptedAnswer(f, text), or h.upstreamAnswer(f, resp, cancel) after setting
//     f.upStart before the call (an error there goes to upstreamFailed, which records and answers
//     it); then f.takeAnswer(ans), and ans.close once the answer is written;
//  3. res := h.evaluate(f.record, f): the record context outlives the client, and the gate
//     bounds itself by the route's deadline. evaluate joins the knowledge retrieval that
//     h.startKnowledge(f) started next to the upstream call (knowledge.go), then gates f.x;
//  4. out := h.enforceJSON(ctx, f, res, ans) picks what is delivered. ctx is the client's context:
//     a fallback decision calls h.fallback on it and records the fallback's attempt 1;
//  5. audit := h.decided(f, res, ans.upstreamRecord(), end, out.final) records the trace, exactly
//     once, and returns the X-TrustAgent-Audit value;
//  6. h.countRequest(f, out.final.Action, up, end), then h.writeJSON(f, res, audit, ans.header, out)
//     writes the headers (setDecisionHeaders) and out.
//
// blockOf is a route's block body. h.fallback runs a route's fallback and returns what it
// delivers, recording nothing: a caller that does not go through enforceJSON records
// fallbackOut.Result1 with sink.AttemptDecided before it calls decided (D22), as buffer.go does.

// flow is one gated /v1 exchange. Fields are append-only.
type flow struct {
	c      *gin.Context
	format string              // llmapi format id
	req    *llmapi.ChatRequest // nil when unparseable
	raw    []byte              // client body
	x      *gate.Exchange
	route  *policy.Route
	up     *upstream.Client
	start  time.Time
	record context.Context // context.WithoutCancel(request ctx)
	// forward is the body sent upstream: raw without the gateway's members (trustagent,
	// test_response), or raw as it came when it does not parse.
	forward []byte
	// upStart is when the upstream call started; zero for a scripted (test_response) answer.
	upStart time.Time
	// ups is the upstream set in effect when the flow started; the fallback upstream comes from
	// the same set.
	ups *upstream.Set
	// knowledge is the knowledge retrieval in flight (knowledge.go), nil when none runs or once it
	// was joined.
	knowledge *knowledgeFetch
}

// newFlow builds the flow of a request of format on path: the route and mode the policy in effect
// resolves for it, the trace (source, scenario and flags included), the exchange with its evidence
// (skip reason unparseable_request when req is nil), the body to forward and the upstream of the
// format's family. The trace id is the one TraceIDs assigned; a caller outside /v1 (the
// Playground) that has none gets a new one. newFlow records nothing: the caller hands f.x.Trace to
// sink.TraceStarted once it has accepted the request.
//
// newFlow is shared with the Playground (playground.go): its signature is fixed.
func (h *Handler) newFlow(c *gin.Context, format, path string, raw []byte, req *llmapi.ChatRequest, start time.Time) *flow {
	ups := h.upstreams()
	f := &flow{
		c: c, format: format, req: req, raw: raw, start: start, forward: raw,
		up: ups.For(llmapi.Family(format)), ups: ups,
		// The record outlives the client: a client that goes away still gets its trace written.
		record: context.WithoutCancel(c.Request.Context()),
	}
	if req != nil {
		if fwd, err := llmapi.ForwardBody(raw); err == nil {
			f.forward = fwd
		}
	}
	f.x, f.route = h.newExchange(c, format, path, req, start)
	if req == nil {
		f.x.SkipReason = gate.SkipUnparseableRequest
	}
	return f
}

// newExchange resolves the route and mode and builds the trace of a gated request.
func (h *Handler) newExchange(c *gin.Context, format, path string, req *llmapi.ChatRequest, start time.Time) (*gate.Exchange, *policy.Route) {
	var model, bodyRoute string
	if req != nil {
		model, bodyRoute = req.Model, req.Extension.Route
	}
	keyLabel := authz.KeyLabel(c)
	// One policy per request: a reload mid-request cannot mix two versions.
	pol := h.policy()
	route := pol.Match(&policy.Request{
		Path: path, Model: model, Headers: c.Request.Header, KeyLabel: keyLabel, BodyRoute: bodyRoute,
	})
	mode, _ := pol.Mode(route, c.GetHeader(HeaderClientMode))

	traceID := c.GetString(traceIDKey)
	if traceID == "" {
		traceID = store.NewID()
		c.Set(traceIDKey, traceID)
	}
	t := &gate.TraceStart{
		TraceID: traceID, CreatedAt: start.UTC(), Source: gate.SourceProxy,
		APIFormat: format, Path: path, Route: route.Name,
		PolicyVersion: pol.Version, Mode: mode, KeyLabel: keyLabel, Model: model,
		Stream: req != nil && req.Stream, AnswerSource: gate.AnswerLive, Flags: []string{},
	}
	if id := c.GetString(clientRequestIDKey); id != "" {
		t.ClientRequestID = &id
	}
	// In test mode X-TrustAgent-Scenario names a pack scenario, which the trace keeps, or carries a
	// forward-only fault:<spec> for the decision model, which it never keeps (D119).
	var faultSpec string
	if h.testMode {
		switch s := c.GetHeader(HeaderScenario); {
		case scenarioID.MatchString(s):
			t.Scenario = s
		case faultScenario.MatchString(s):
			faultSpec = s
		}
	}
	switch {
	case req != nil && req.TestResponse != nil:
		t.Source, t.AnswerSource = gate.SourceTest, gate.AnswerScripted
		t.Flags = append(t.Flags, gate.FlagTestMode)
	case h.testMode && generatedScenarios()[t.Scenario]:
		// A recording run sends a generated scenario without test_response, so the upstream
		// writes the answer (D121); the recording's label then says "generated answers".
		t.AnswerSource = gate.AnswerGenerated
	}
	if keyLabel == TrafficgenKeyLabel || t.Scenario != "" {
		t.Source = gate.SourceTrafficgen
	}

	x := &gate.Exchange{Trace: t, Policy: pol, Route: route, Request: req, FaultScenario: faultSpec}
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

// answer is a JSON answer to a gated request: the upstream's (upstreamAnswer), or the scripted
// test_response (scriptedAnswer). Fields are append-only.
type answer struct {
	status int
	header http.Header
	// body is what was read; rest, when set, is the unread remainder of an oversized body.
	body []byte
	rest io.Reader
	// resp is the parsed answer; nil when the body did not parse (or was not parsed).
	resp *llmapi.ChatResponse
	// text is the gated text (choice 0); skip is set when the answer cannot be gated.
	text string
	skip string
	// unenforced marks an answer that is delivered whatever the decision says: an upstream
	// non-2xx and an answer without text (tool calls only) pass through (SPEC §2.10, D4, D5).
	unenforced bool
	// multiChoice marks an answer with more than one choice: only choice 0 is gated (D6).
	multiChoice bool
	ms          *int64
	usage       *llmapi.Usage
	close       func()
	// broken says why a streamed 2xx answer has no answer to show (an error inside the stream, a
	// stream cut short); "" otherwise. Only the Playground reads it (streamedAnswer).
	broken string
}

// takeAnswer hands a JSON answer to the exchange before it is gated: the gated text, the skip
// reason when the exchange has none yet, whether it is enforced, and the multi_choice flag (only
// choice 0 is gated, D6; a block still replaces every choice).
func (f *flow) takeAnswer(ans *answer) {
	x := f.x
	x.Response = ans.text
	if x.SkipReason == "" {
		x.SkipReason = ans.skip
	}
	x.Unenforced = x.Unenforced || ans.unenforced
	if ans.multiChoice && !slices.Contains(x.Trace.Flags, gate.FlagMultiChoice) {
		x.Trace.Flags = append(x.Trace.Flags, gate.FlagMultiChoice)
	}
}

// scriptedAnswer is the test-mode answer to f: text as a JSON answer of f's format, with no
// upstream call.
func (h *Handler) scriptedAnswer(f *flow, text string) *answer {
	zero := int64(0)
	model := ""
	if f.req != nil {
		model = f.req.Model
	}
	a := &answer{
		status: http.StatusOK, header: http.Header{"Content-Type": {"application/json"}},
		body: llmapi.SyntheticResponse(f.format, f.x.Trace.TraceID, model, f.start.Unix(), text),
		text: text, ms: &zero, close: func() {},
	}
	if text == "" {
		a.skip, a.unenforced = gate.SkipNoText, true
	}
	return a
}

// upstreamAnswer reads a JSON (not event-stream) upstream response to f; the caller sets
// f.upStart before the call, which the upstream time counts from. An error means the body could
// not be read at all, which counts as a failed upstream call (upstreamFailed); it is an
// *answerError that carries the status the upstream answered with.
func (h *Handler) upstreamAnswer(f *flow, resp *http.Response, cancel context.CancelFunc) (*answer, error) {
	a := &answer{status: resp.StatusCode, header: resp.Header, close: func() { _ = resp.Body.Close(); cancel() }}
	body, tooLarge, err := upstream.ReadBody(resp, h.maxBytes)
	ms := h.now().Sub(f.upStart).Milliseconds()
	a.ms = &ms
	if err != nil {
		a.close()
		return nil, &answerError{status: resp.StatusCode, err: err}
	}
	a.body = body
	if tooLarge {
		a.rest = resp.Body
	}
	switch {
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// Passed through as it is, oversized or not: an upstream error is never enforced (D4). In
		// inject mode it may quote the gateway's own key: it is masked first (redact.go, D42).
		a.skip, a.unenforced = gate.SkipUpstreamError, true
		if f.up != nil && f.up.Injecting() {
			a.body, a.rest = h.redactErrorBody(body), nil
		}
	case tooLarge:
		a.skip = gate.SkipUpstreamError
	default:
		parsed, err := llmapi.ParseResponse(f.format, body)
		if err != nil {
			a.skip = gate.SkipUpstreamError
			break
		}
		a.resp, a.usage, a.multiChoice = parsed, parsed.Usage, parsed.MultiChoice()
		if choice, ok := parsed.Gated(); ok && choice.Text != "" {
			a.text = choice.Text
		} else {
			a.skip, a.unenforced = gate.SkipNoText, true
		}
	}
	return a, nil
}

// answerError is an upstream answer whose body could not be read: the upstream answered with
// status, then reading the body failed with err (a timeout, a reset connection, a body that
// cannot be decoded).
type answerError struct {
	status int
	err    error
}

func (e *answerError) Error() string { return e.err.Error() }
func (e *answerError) Unwrap() error { return e.err }

// upstreamRecord is the upstream exchange as the trace records it: status, time and token usage.
func (a *answer) upstreamRecord() gate.Upstream {
	u := gate.Upstream{Status: a.status, Ms: a.ms}
	if a.usage != nil {
		pt, ct := a.usage.PromptTokens, a.usage.CompletionTokens
		u.PromptTokens, u.CompletionTokens = &pt, &ct
	}
	return u
}

// write sends the answer as the upstream sent it: status, then the body, then the unread rest of
// an oversized body.
func (a *answer) write(w http.ResponseWriter, log *slog.Logger) {
	writeBody(w, a.status, a.body, a.rest, log)
}

// writeBody writes status, body and, when set, the rest of an oversized body.
func writeBody(w http.ResponseWriter, status int, body []byte, rest io.Reader, log *slog.Logger) {
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		log.Debug("client went away while the response was written", "err", err)
		return
	}
	if rest != nil {
		if _, err := io.Copy(w, rest); err != nil {
			log.Debug("oversized upstream body relay stopped", "err", err)
		}
	}
}
