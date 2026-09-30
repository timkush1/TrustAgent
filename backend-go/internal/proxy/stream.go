package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/upstream"
)

// streamAnnotationsNone is the route stream_annotations value that writes no ": trustagent {...}"
// comment.
const streamAnnotationsNone = "none"

// streamTrailerHeaders are the decision headers a stream sends as HTTP trailers once its headers
// went out (D9): the decision arrives after the body. Trace-Id, Request-ID, Route and Source are
// known up front and go in the headers.
var streamTrailerHeaders = []string{
	HeaderMode, HeaderAction, HeaderWouldAction, HeaderStatus, HeaderVerdict, HeaderRisk, HeaderCalibrated,
	HeaderDegraded, HeaderReasons, HeaderCheckedAgainst, HeaderDecisionModel, HeaderGateMs, HeaderOverheadMs,
	HeaderAudit,
}

// streamTrailerNames are the trailers a stream of route declares: with headers: action_only only
// Action.
func streamTrailerNames(route *policy.Route) []string {
	if route.Headers == policy.HeadersActionOnly {
		return []string{HeaderAction}
	}
	return streamTrailerHeaders
}

// streamDeadlineMargin is added to the upstream's idle (or total) timeout to give the write
// deadline a stream keeps (llmapi.StreamWriter.KeepWriteDeadline); /v1 starts with the upstream
// timeout plus the same 30 s.
const streamDeadlineMargin = 30 * time.Second

// streamDrainBytes bounds what is read after a stream's terminal event, so the upstream
// connection ends cleanly and can be reused.
const streamDrainBytes = 64 << 10

// serveStream relays an event stream to the client and gates it (SPEC §2.12 streaming). resp is
// the upstream response (a 2xx text/event-stream), nil for a synthesized test_response stream;
// body is the stream to relay (upstream.Client.StreamBody, or synthStream). A route with
// streaming: buffer reads the whole answer before the client sees any of it (serveBuffered);
// every other route, and every observe route (nothing is enforced there), relays the events as
// they arrive (relayStream). serveStream closes body when it is an io.Closer.
func (h *Handler) serveStream(f *flow, resp *http.Response, body io.Reader) {
	if f.route.Streaming == policy.StreamingBuffer && f.x.Trace.Mode != decision.ModeObserve {
		h.serveBuffered(f, resp, body)
		return
	}
	h.relayStream(f, resp, body)
}

// synthStream is the event stream of a stream:true test_response in f's format: the text in one
// delta, then the finish reason, the usage chunk under stream_options.include_usage and the
// terminal event, exactly as SynthesizeStream builds it (GS-4).
func (h *Handler) synthStream(f *flow, text string) io.Reader {
	s := llmapi.SynthStream{TraceID: f.x.Trace.TraceID, Created: f.start.Unix(), Model: f.x.Trace.Model, Text: text}
	if f.req != nil {
		s.Model, s.IncludeUsage = f.req.Model, f.req.IncludeUsage
	}
	return bytes.NewReader(llmapi.EncodeEvents(llmapi.SynthesizeStream(f.format, s)))
}

// streamState is one stream being relayed or buffered.
type streamState struct {
	f    *flow
	resp *http.Response
	body io.Reader
	rd   *llmapi.Reader
	asm  *llmapi.StreamAssembler
	sw   *llmapi.StreamWriter
	// upStart is when the upstream call started (the request start for a synthesized stream);
	// first is when its first bytes arrived.
	upStart time.Time
	first   *streamFirstRead
}

// newStreamState prepares the relay of body: the reader, whose events and the assembler, whose
// answer, are bounded by TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES (an answer past it cannot be
// gated), and the writer, which keeps the connection's write deadline ahead of a stream that
// outlasts the route's deadline. It returns nil when f's format cannot be assembled.
func (h *Handler) newStreamState(f *flow, resp *http.Response, body io.Reader) *streamState {
	asm, err := llmapi.NewStreamAssembler(f.format)
	if err != nil {
		return nil
	}
	asm.SetMaxBytes(h.maxBytes)
	s := &streamState{f: f, resp: resp, body: body, asm: asm, sw: llmapi.NewStreamWriter(f.c.Writer), upStart: f.start}
	if resp != nil {
		if t := upstream.StartedAt(resp); !t.IsZero() {
			s.upStart = t
		}
	}
	s.first = &streamFirstRead{r: body, now: h.now}
	s.rd = llmapi.NewReader(s.first)
	if h.maxBytes < llmapi.DefaultMaxEventBytes {
		s.rd.SetMaxEventBytes(int(h.maxBytes))
	}
	s.sw.KeepWriteDeadline(streamDeadlineWindow(f.up))
	return s
}

// streamDeadlineWindow is how far ahead a stream keeps its write deadline: the upstream's idle
// timeout (else its total timeout) plus a margin; 0 (none) when the upstream has neither.
func streamDeadlineWindow(up *upstream.Client) time.Duration {
	if up == nil {
		return 0
	}
	switch {
	case up.IdleTimeout() > 0:
		return up.IdleTimeout() + streamDeadlineMargin
	case up.Timeout() > 0:
		return up.Timeout() + streamDeadlineMargin
	}
	return 0
}

// streamFirstRead records when the first bytes of a stream arrived (the time to the first event). A
// read may still be in flight when a buffered read ends on a failed keepalive, so the time is
// kept atomically.
type streamFirstRead struct {
	r   io.Reader
	now func() time.Time
	at  atomic.Pointer[time.Time]
}

func (fr *streamFirstRead) Read(p []byte) (int, error) {
	n, err := fr.r.Read(p)
	if n > 0 && fr.at.Load() == nil {
		t := fr.now()
		fr.at.CompareAndSwap(nil, &t)
	}
	return n, err
}

// upstreamRecord is the upstream exchange of a stream as the trace records it: status, the time
// until the relay or the buffer read ended, the time to the first event (nil when none arrived)
// and the token usage the stream reported. A synthesized stream answers in 0 ms with status 200.
func (s *streamState) upstreamRecord(end time.Time) gate.Upstream {
	var ms, ttft int64
	u := gate.Upstream{Status: http.StatusOK, Ms: &ms, TTFTMs: &ttft}
	if s.resp != nil {
		u.Status = s.resp.StatusCode
		ms = max(end.Sub(s.upStart).Milliseconds(), 0)
		if at := s.first.at.Load(); at != nil {
			ttft = max(at.Sub(s.upStart).Milliseconds(), 0)
		} else {
			u.TTFTMs = nil
		}
	}
	if usage := s.asm.Response().Usage; usage != nil {
		pt, ct := usage.PromptTokens, usage.CompletionTokens
		u.PromptTokens, u.CompletionTokens = &pt, &ct
	}
	return u
}

// closeUpstream ends the upstream body once the gateway stopped reading it. After a clean end the
// bytes that may follow the terminal event are drained in the background (bounded, and by the
// idle timeout), so the connection can be reused; after anything else it is closed at once, which
// cancels the upstream call.
func (s *streamState) closeUpstream(drain bool) {
	c, ok := s.body.(io.Closer)
	if !ok {
		return
	}
	if !drain {
		_ = c.Close()
		return
	}
	go func() {
		_, _ = io.CopyN(io.Discard, s.body, streamDrainBytes)
		_ = c.Close()
	}()
}

// streamOutcome is how a relayed or buffered answer is gated.
type streamOutcome struct {
	// gone: the client went away; disconnected: after the finish reason, so the answer is still
	// gated (unenforced) and audited with the client_disconnected flag (D7).
	gone, disconnected bool
	// multi: the answer has several choices; only choice 0 is gated (D6).
	multi bool
}

// classify sets the exchange's gated text, or its skip reason, from how the stream ended:
//
//   - the client went away (D7): after the finish reason the answer is gated unenforced and
//     audited with flag client_disconnected, before it skipped as client_disconnected;
//   - an undecodable body: skipped:undecodable; a read error, an event or an answer past the size
//     limit, or a stream that ended before its finish reason: skipped:upstream_error;
//   - an error the upstream reported inside the stream, or an answer without text (tool calls
//     only): skipped, and unenforced: the client gets the upstream's answer as it is (D4, D5);
//   - otherwise choice 0's text is gated.
func (s *streamState) classify(clientGone bool, upstreamErr error) streamOutcome {
	x := s.f.x
	var out streamOutcome
	resp := s.asm.Response()
	switch {
	case clientGone:
		out.gone = true
		if s.asm.Finished() && !s.asm.TooLarge() {
			out.disconnected = true
			x.Unenforced = true
			s.gatedText(resp)
		} else {
			x.SkipReason = gate.SkipClientDisconnected
		}
	case errors.Is(upstreamErr, upstream.ErrUndecodable):
		x.SkipReason = gate.SkipUndecodable
	case upstreamErr != nil, s.asm.TooLarge():
		x.SkipReason = gate.SkipUpstreamError
	case s.asm.Err() != nil:
		x.SkipReason, x.Unenforced = gate.SkipUpstreamError, true
	case !s.asm.Terminated() && !s.asm.Finished():
		x.SkipReason = gate.SkipUpstreamError // the stream ended before its answer did
	default:
		s.gatedText(resp)
	}
	out.multi = resp.MultiChoice()
	return out
}

// gatedText sets choice 0's text as the gated answer; an answer without text is skipped
// (no_text) and unenforced (D5).
func (s *streamState) gatedText(resp *llmapi.ChatResponse) {
	x := s.f.x
	if c, ok := resp.Gated(); ok && c.Text != "" {
		x.Response = c.Text
		return
	}
	if x.SkipReason == "" {
		x.SkipReason = gate.SkipNoText
	}
	x.Unenforced = true
}

// leftDuringGate reports whether the client went away while the answer was being decided: then
// nothing reaches it, and an answer that was complete is recorded unenforced with the
// client_disconnected flag (D7).
func (s *streamState) leftDuringGate(g *gate.Result, out *streamOutcome) bool {
	if s.f.c.Request.Context().Err() == nil {
		return false
	}
	out.gone = true
	if s.asm.Finished() && g.Decision != nil {
		g.Decision.Enforced = false
		out.disconnected = true
	}
	return true
}

// addStreamFlags adds the trace flags a stream outcome carries to a result.
func addStreamFlags(res *gate.Result, out streamOutcome) {
	if out.disconnected {
		res.Flags = appendStreamFlag(res.Flags, gate.FlagClientDisconnected)
	}
	if out.multi {
		res.Flags = appendStreamFlag(res.Flags, gate.FlagMultiChoice)
	}
}

// appendStreamFlag appends flag unless it is already there.
func appendStreamFlag(flags []string, flag string) []string {
	if slices.Contains(flags, flag) {
		return flags
	}
	return append(flags, flag)
}

// relayStream is passthrough streaming (SPEC §2.12, GS-5): every event reaches the client as it
// arrives.
//
// On flag, block and fallback routes the terminal event (data: [DONE] or message_stop) is held
// while the gate decides synchronously with meta.stream passthrough (block and fallback become
// an effective flag, stream_not_buffered); then the ": trustagent {...}" comment (unless
// stream_annotations: none), the terminal event and the decision trailers go out (D9: Route,
// Source, Trace-Id and Request-ID up front, the rest declared as trailers; Action alone with
// headers: action_only). On observe routes the headers are the deferred ones, nothing is held and
// the gate runs afterwards, bounded by the handler's semaphore.
//
// An event past the size limit is forwarded with the rest of the stream as it is; an answer past
// TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES is relayed whole but not gated (skipped:upstream_error).
func (h *Handler) relayStream(f *flow, resp *http.Response, body io.Reader) {
	c, x := f.c, f.x
	x.Stream = gate.StreamPassthrough
	s := h.newStreamState(f, resp, body)
	if s == nil {
		h.relayStreamUngated(f, resp, body)
		return
	}
	hdr := c.Writer.Header()
	if resp != nil {
		upstream.CopyResponseHeaders(hdr, resp.Header)
	}
	observe := x.Trace.Mode == decision.ModeObserve
	label := x.Policy.Label(f.route)
	if observe {
		setDeferredHeaders(hdr, x.Trace, f.route, label)
	} else {
		setStreamLeadHeaders(hdr, x.Trace, f.route, label)
		s.sw.DeclareTrailers(streamTrailerNames(f.route)...)
	}

	res := llmapi.Relay(s.rd, s.asm, s.sw)
	if errors.Is(res.UpstreamErr, llmapi.ErrEventTooLarge) && res.ClientErr == nil {
		// Too large to read event by event: the rest reaches the client whole and ungated. Only a
		// failed write means the client left; a failed read is the upstream's (the answer is
		// skipped as upstream_error either way).
		if _, err := io.Copy(s.sw, s.rd.Rest()); err != nil && s.sw.Err() != nil {
			res.ClientErr = err
		}
	}
	relayEnd := h.now()
	gone := res.ClientGone(c.Request.Context())
	s.closeUpstream(!gone && res.UpstreamErr == nil)
	out := s.classify(gone, res.UpstreamErr)
	up := s.upstreamRecord(relayEnd)

	if observe {
		// Nothing waits for the decision: the terminal event and any unterminated tail go out
		// as the upstream sent them.
		if !gone {
			if err := s.sw.Finish(nil, nil); err != nil {
				h.log.Debug("client went away before the end of the stream", "trace_id", x.Trace.TraceID, "err", err)
			}
		}
		h.gateStreamLater(f, out, up, relayEnd)
		return
	}
	// The gate bounds itself by the route deadline; the record context lets it finish when the
	// client leaves meanwhile.
	g := h.gate.Evaluate(f.record, x)
	if !gone && s.leftDuringGate(g, &out) {
		gone = true
	}
	addStreamFlags(g, out)
	audit := h.decided(f, g, up, h.now(), nil)
	if gone {
		return
	}
	if err := s.sw.Finish(streamAnnotation(f.route, g), streamTrailers(f.route, g, audit)); err != nil {
		h.log.Debug("client went away before the end of the stream", "trace_id", x.Trace.TraceID, "err", err)
	}
}

// relayStreamUngated copies a stream of a format the gateway cannot assemble as it is, recorded as
// skipped:unsupported_api.
func (h *Handler) relayStreamUngated(f *flow, resp *http.Response, body io.Reader) {
	hdr := f.c.Writer.Header()
	if resp != nil {
		upstream.CopyResponseHeaders(hdr, resp.Header)
	}
	f.x.SkipReason = gate.SkipUnsupportedAPI
	g := h.gate.Evaluate(f.record, f.x)
	audit := h.decided(f, g, gate.Upstream{Status: http.StatusOK}, h.now(), nil)
	setDecisionHeaders(hdr, g, audit)
	sw := llmapi.NewStreamWriter(f.c.Writer)
	if _, err := io.Copy(sw, body); err != nil {
		h.log.Debug("stream relay stopped", "trace_id", f.x.Trace.TraceID, "err", err)
	}
	if cl, ok := body.(io.Closer); ok {
		_ = cl.Close()
	}
}

// gateStreamLater gates an observe-mode stream after it was relayed (SPEC §2.12: the body is
// byte-identical and the decision is recorded asynchronously). A skipped answer is recorded at
// once (no decision-model call); when too many gates already run, the answer is recorded as
// skipped:overload with detectors only.
func (h *Handler) gateStreamLater(f *flow, out streamOutcome, up gate.Upstream, written time.Time) {
	x := f.x
	if x.SkipReason != "" {
		g := h.gate.Evaluate(f.record, x)
		addStreamFlags(g, out)
		h.decided(f, g, up, written, nil)
		return
	}
	select {
	case h.sem <- struct{}{}:
	default:
		x.SkipReason = gate.SkipOverload
		g := h.gate.Evaluate(f.record, x)
		addStreamFlags(g, out)
		h.decided(f, g, up, written, nil)
		return
	}
	h.async.Add(1)
	go func() {
		defer h.async.Done()
		defer func() { <-h.sem }()
		// The gate bounds its decision-model call; this bounds the rest (and a stuck sink).
		gctx, cancel := context.WithTimeout(f.record, 2*h.gate.AsyncDeadline())
		defer cancel()
		g := h.gate.Evaluate(gctx, x)
		addStreamFlags(g, out)
		bounded := *f
		bounded.record = gctx
		h.decided(&bounded, g, up, written, nil)
	}()
}

// setStreamLeadHeaders writes the decision headers a stream knows before its decision (D9): Route
// and Source (Trace-Id and Request-ID are always set). With headers: action_only there are none.
func setStreamLeadHeaders(h http.Header, t *gate.TraceStart, route *policy.Route, routeLabel string) {
	if route.Headers == policy.HeadersActionOnly {
		return
	}
	h.Set(HeaderRoute, routeLabel)
	if t.Source != "" && t.Source != gate.SourceProxy {
		h.Set(HeaderSource, t.Source)
	}
}

// streamTrailers are the decision headers of a result as the trailers route declares (GS-7): the
// values setDecisionHeaders writes, collected in a scratch header.
func streamTrailers(route *policy.Route, res *gate.Result, audit string) map[string]string {
	scratch := http.Header{}
	setDecisionHeaders(scratch, res, audit)
	out := map[string]string{}
	for _, name := range streamTrailerNames(route) {
		if v := scratch.Get(name); v != "" {
			out[name] = v
		}
	}
	return out
}

// streamDeliveredAction is the action the client got: the delivered outcome's, else the decision's.
func streamDeliveredAction(res *gate.Result) string {
	if res.Final != nil && res.Final.Action != "" {
		return res.Final.Action
	}
	return res.Decision.Action
}

// streamAnnotation is the ": trustagent {...}" comment of a decided stream, or nil with
// stream_annotations: none. Its reasons are the decision's, then the delivery's (a failed
// fallback).
func streamAnnotation(route *policy.Route, res *gate.Result) *llmapi.Annotation {
	if route.StreamAnnotations == streamAnnotationsNone {
		return nil
	}
	d := res.Decision
	a := &llmapi.Annotation{
		TraceID: res.Trace.TraceID, Action: streamDeliveredAction(res), WouldAction: d.WouldAction, Status: res.Status,
		Verdict: d.Verdict, MaxRisk: d.MaxRisk, Reasons: slices.Clone(d.Reasons),
	}
	if a.WouldAction == "" {
		a.WouldAction = d.Action
	}
	if res.Final != nil {
		for _, r := range res.Final.Reasons {
			if !slices.Contains(a.Reasons, r) {
				a.Reasons = append(a.Reasons, r)
			}
		}
	}
	return a
}
