package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/upstream"
)

// serveBuffered is buffer streaming (SPEC §2.12, route streaming: buffer, GS-6): the whole
// upstream answer is read before the client sees any of it, so the gate decides with
// meta.stream buffer and every action is enforced. While the answer is read, a keepalive comment
// goes out every buffer_keepalive_s; the first one commits the headers.
//
// The decision reaches the client in the headers while they are not committed; after that, as
// the ": trustagent {...}" comment and the trailers (D9), and a block is then always message
// style. Pass and flag deliver the upstream events as they were read; block delivers the format's
// block stream (or, uncommitted with style error, the error status and body); fallback delivers
// the fallback's answer re-emitted as a stream, or what on_fail picked.
//
// An answer the gate cannot check (past TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES, undecodable, cut
// short by a read error) is a skip: a closed block or fallback route blocks it (D4). An answer
// past the size limit is never relayed ungated on a block or fallback route, whatever its fail
// mode: fail open lets an unchecked answer through, not an unread one (D4, as the JSON path).
// Anything else delivers what was read and then the rest of the stream, ungated. An error the
// upstream reported inside the stream, or an answer without text, passes through unenforced (D4,
// D5). A stream that fails, stalls or ends before its first event, while no keepalive committed
// the headers, is answered as a failed upstream call (streamFailed, D21). A client that went away gets
// nothing: the decision is recorded unenforced. The request counts once in requests_total.
func (h *Handler) serveBuffered(f *flow, resp *http.Response, body io.Reader) {
	c, x := f.c, f.x
	x.Stream = gate.StreamBuffer
	s := h.newStreamState(f, resp, body)
	if s == nil {
		h.relayStreamUngated(f, resp, body)
		return
	}
	hdr := c.Writer.Header()
	base := hdr.Clone()
	if resp != nil {
		upstream.CopyResponseHeaders(hdr, resp.Header)
	}
	setStreamLeadHeaders(hdr, x.Trace, f.route, x.Policy.Label(f.route))
	// Declared for the case a keepalive commits the headers; cleared when it does not.
	s.sw.DeclareTrailers(streamTrailerNames(f.route)...)

	col := llmapi.Collect(s.rd, s.asm, llmapi.CollectOptions{
		MaxBytes: h.maxBytes, Keepalive: time.Duration(f.route.BufferKeepaliveS) * time.Second, Writer: s.sw,
	})
	readEnd := h.now()
	gone := col.ClientGone(c.Request.Context())
	// Past the size limit the rest of the stream may still be relayed, so the upstream stays open
	// until the answer is delivered.
	tooLarge := col.TooLarge || errors.Is(col.UpstreamErr, llmapi.ErrEventTooLarge)
	if !tooLarge || gone {
		s.closeUpstream(!gone && col.UpstreamErr == nil)
	}
	if !gone && !tooLarge && s.failedEarly(col.UpstreamErr, false, col.Events) {
		h.streamFailed(f, s, base, col.UpstreamErr, readEnd)
		return
	}
	out := s.classify(gone, col.UpstreamErr)
	up := s.upstreamRecord(readEnd)

	// The gate bounds itself by the route deadline; the record context lets it finish when the
	// client leaves meanwhile.
	g := h.gate.Evaluate(f.record, x)
	if !gone {
		gone = s.leftDuringGate(g, &out)
	}
	var d bufferedAnswer
	if !gone {
		d = h.bufferedDelivery(f, s, g, col, tooLarge)
		if d.result1 != nil {
			h.sink.AttemptDecided(f.record, d.result1)
		}
		gone = s.leftDuringGate(g, &out) // during the fallback call
	}
	if gone {
		// Nothing reaches a client that went away, whatever the decision or the fallback call
		// would have delivered: the decision is unenforced (classify, leftDuringGate) and the
		// delivered action is the one an unenforced decision delivers, as for a JSON call the
		// client left (upstreamFailed).
		d = bufferedAnswer{fin: &gate.Final{Action: unenforcedAction(g.Decision.Action), Delivered: gate.DeliveredOriginal}}
	}
	addStreamFlags(g, out)
	end := h.now()
	audit := h.decided(f, g, up, end, d.fin)
	h.countRequest(f, deliveredAction(g), up, end)
	if !gone {
		h.writeBuffered(f, s, g, audit, d)
	}
	if tooLarge {
		s.closeUpstream(false)
	}
}

// bufferedFallback calls a route's fallback for a buffered answer (h.fallback); tests replace it
// with a scripted outcome.
var bufferedFallback = func(h *Handler, ctx context.Context, f *flow, res0 *gate.Result) *fallbackOut {
	return h.fallback(ctx, f, res0)
}

// bufferedAnswer is what a decided buffered answer delivers.
type bufferedAnswer struct {
	// events are written as the stream; rest, when set, follows them as it is (an answer past
	// the size limit, ungated).
	events []llmapi.Event
	rest   io.Reader
	// errorStatus and errorBody replace the stream with an error-style block while the headers are
	// not committed.
	errorStatus int
	errorBody   []byte
	// fin is the delivered outcome (nil: the decision's action and the original events).
	fin *gate.Final
	// result1 is the fallback's attempt-1 re-gate, recorded before the trace's decision.
	result1 *gate.Result
}

// bufferedDelivery picks what a decided buffered answer delivers.
func (h *Handler) bufferedDelivery(f *flow, s *streamState, g *gate.Result, col llmapi.CollectResult, tooLarge bool) bufferedAnswer {
	original := func() bufferedAnswer {
		a := bufferedAnswer{events: col.Events}
		if tooLarge {
			a.rest = s.rd.Rest()
		}
		return a
	}
	if f.x.Unenforced {
		// The upstream's own error, or an answer without text: it passes through as it is.
		a := original()
		a.fin = &gate.Final{Action: decision.ActionPass, Delivered: gate.DeliveredOriginal, Text: f.x.Response}
		return a
	}
	switch g.Decision.Action {
	case decision.ActionBlock:
		return h.bufferedBlock(f, s)
	case decision.ActionFallback:
		// The fallback call can take as long as an upstream call; the answer is written after it.
		// A flow without an upstream (a test_response) has no timeout to size that by.
		if f.up != nil && f.up.Timeout() > 0 {
			_ = s.sw.ExtendWriteDeadline(f.up.Timeout() + streamDeadlineMargin)
		}
		fo := bufferedFallback(h, f.c.Request.Context(), f, g)
		var a bufferedAnswer
		switch fo.Delivered {
		case gate.DeliveredFallback:
			a.events = h.bufferedFallbackEvents(f, fo)
		case gate.DeliveredOriginal:
			a = original()
		default:
			a = h.bufferedBlock(f, s)
		}
		a.fin = &gate.Final{Action: fo.Action, Delivered: fo.Delivered, Text: fo.Text, Flags: fo.Flags, Reasons: fo.Reasons}
		a.result1 = fo.Result1
		return a
	}
	if tooLarge && enforcing(g.Decision.EffectiveMode) {
		// The ungated relay of an answer past the size limit stays on observe and flag routes (D4).
		return h.bufferedBlock(f, s)
	}
	return original()
}

// bufferedFallbackEvents re-emits a delivered fallback answer as a stream of f's format, with finish
// reason stop / stop_reason end_turn (SPEC §2.12 fallback step 6).
func (h *Handler) bufferedFallbackEvents(f *flow, fo *fallbackOut) []llmapi.Event {
	includeUsage := f.req != nil && f.req.IncludeUsage
	if fo.Response != nil {
		return llmapi.StreamFromResponse(f.format, fo.Response, f.x.Trace.TraceID, f.start.Unix(), includeUsage)
	}
	return llmapi.SynthesizeStream(f.format, llmapi.SynthStream{
		TraceID: f.x.Trace.TraceID, Created: f.start.Unix(), Model: f.x.Trace.Model, Text: fo.Text, IncludeUsage: includeUsage,
	})
}

// bufferedBlock is the block of a buffered answer: the route's error-style status and body while
// the headers are not committed, else the format's block stream (message style).
func (h *Handler) bufferedBlock(f *flow, s *streamState) bufferedAnswer {
	b := blockOf(f.route)
	target := llmapi.BlockTarget{
		TraceID: f.x.Trace.TraceID, Model: f.x.Trace.Model, Created: f.start.Unix(), Response: s.asm.Response(),
		IncludeUsage: f.req != nil && f.req.IncludeUsage,
	}
	text := b.Message
	if text == "" {
		text = llmapi.DefaultBlockMessage
	}
	a := bufferedAnswer{fin: &gate.Final{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: text}}
	if b.Style == llmapi.BlockStyleError && !s.sw.Committed() {
		a.errorStatus, a.errorBody = llmapi.BlockJSON(f.format, b, target)
		return a
	}
	a.events = llmapi.BlockStream(f.format, b, target)
	return a
}

// writeBuffered writes a decided buffered answer: the decision headers while they are not
// committed, else the annotation before the terminal event and the trailers (D9).
func (h *Handler) writeBuffered(f *flow, s *streamState, g *gate.Result, audit string, a bufferedAnswer) {
	if a.errorBody != nil {
		// Written past the stream writer: the answer may have taken longer than the route's
		// write deadline to read and decide.
		if window := streamDeadlineWindow(f.up); window > 0 {
			_ = s.sw.ExtendWriteDeadline(window)
		}
		w := f.c.Writer
		hdr := w.Header()
		setDecisionHeaders(hdr, g, audit)
		hdr.Del("Content-Length")
		hdr.Del("Content-Encoding")
		hdr.Set("Content-Type", "application/json")
		w.WriteHeader(a.errorStatus)
		if _, err := w.Write(a.errorBody); err != nil {
			h.log.Debug("client went away before the block was written", "trace_id", f.x.Trace.TraceID, "err", err)
		}
		return
	}
	var ann *llmapi.Annotation
	var trailers map[string]string
	if s.sw.Committed() {
		ann, trailers = streamAnnotation(f.route, g), streamTrailers(f.route, g, audit)
	} else {
		s.sw.ClearTrailers()
		setDecisionHeaders(f.c.Writer.Header(), g, audit)
	}
	var err error
	if a.rest == nil {
		err = s.sw.WriteEvents(f.format, a.events, ann)
		ann = nil
	} else {
		// The terminal event is somewhere in the rest: the annotation goes last (and only when the
		// rest ended between two events).
		err = s.sw.WriteEvents(f.format, a.events, nil)
		if err == nil {
			_, err = io.Copy(s.sw, a.rest)
		}
	}
	if s.sw.Err() == nil {
		// The client is still there (a failed read of the rest is the upstream's): the decision
		// goes out as well.
		err = errors.Join(err, s.sw.Finish(ann, trailers))
	}
	if err != nil {
		h.log.Debug("the stream ended early", "trace_id", f.x.Trace.TraceID, "err", err)
	}
}
