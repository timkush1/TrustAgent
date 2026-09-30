package proxy

import (
	"context"
	"io"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/policy"
)

// decided records a gated exchange once the handler knows how it ends. It fills the result's
// timings (total runs from f.start to end, when the response was or is about to be written, and
// overhead is total minus the upstream time), the upstream exchange up and the delivered outcome
// fin (nil: the decision's action and the original text), then hands the result to the sink's
// GateDecided on f.record. Call it exactly once per exchange; it returns the X-TrustAgent-Audit
// value.
func (h *Handler) decided(f *flow, res *gate.Result, up gate.Upstream, end time.Time, fin *gate.Final) string {
	total := end.Sub(f.start).Milliseconds()
	overhead := total
	if up.Ms != nil {
		overhead = max(total-*up.Ms, 0)
	}
	res.TotalMs, res.OverheadMs = &total, &overhead
	res.Upstream = up
	res.Final = fin
	return h.sink.GateDecided(f.record, res)
}

// blockOf is the block body a route sends (route settings block → llmapi.Block).
func blockOf(r *policy.Route) llmapi.Block {
	b := r.Block
	return llmapi.Block{Style: b.Style, Message: b.Message, Status: b.Status, AnthropicStopReason: b.AnthropicStopReason}
}

// jsonOut is what a gated JSON exchange sends, and how it ended. Fields are append-only.
type jsonOut struct {
	// status and body are the response; rest, when set, is the unread remainder of an oversized
	// upstream body, relayed after body.
	status int
	body   []byte
	rest   io.Reader
	// built marks a body the gateway wrote (a block or the fallback answer): it goes out as
	// application/json whatever the upstream's Content-Type was.
	built bool
	// final is the delivered outcome the trace records (gate.Result.Final).
	final *gate.Final
}

// enforceJSON applies a synchronous decision to a JSON answer (SPEC §2.12; D4, D5):
//   - an unenforced answer (an upstream non-2xx, an answer without text) is delivered as it came;
//     where the decision would block, the delivered action is pass;
//   - block delivers the route's block body (llmapi.BlockJSON: message style patches every
//     choice, error style answers with the route's status);
//   - fallback runs the route's fallback (h.fallback) and delivers what it chose: the fallback
//     answer, the original, or the block body;
//   - pass, flag and observe deliver the upstream bytes, except that a block or fallback route
//     never relays an oversized answer it could not check: it blocks it.
//
// enforceJSON writes nothing, and records nothing but a fallback's attempt 1
// (sink.AttemptDecided, D22): the caller then records the trace once with decided(…, out.final)
// and writes out (writeJSON). ctx is the client's context, which the fallback call runs on (M1).
// It is shared with the Playground (playground.go): its signature is fixed.
func (h *Handler) enforceJSON(ctx context.Context, f *flow, res *gate.Result, ans *answer) *jsonOut {
	d := res.Decision
	out := &jsonOut{
		status: ans.status, body: ans.body, rest: ans.rest,
		final: &gate.Final{Action: d.Action, Delivered: gate.DeliveredOriginal, Text: ans.text},
	}
	switch {
	case f.x.Unenforced:
		out.final.Action = unenforcedAction(d.Action)
	case d.Action == decision.ActionBlock:
		return h.blockJSON(f, ans)
	case d.Action == decision.ActionFallback:
		return h.fallbackJSON(ctx, f, res, ans)
	case ans.rest != nil && enforcing(d.EffectiveMode):
		// Fail open lets an unchecked answer through, but not one the gateway could not even
		// read: an oversized answer on an enforcing route is blocked (D4).
		return h.blockJSON(f, ans)
	}
	return out
}

// blockJSON is the route's block body in f's format for the answer ans.
func (h *Handler) blockJSON(f *flow, ans *answer) *jsonOut {
	b := blockOf(f.route)
	model := ""
	if f.req != nil {
		model = f.req.Model
	}
	status, body := llmapi.BlockJSON(f.format, b, llmapi.BlockTarget{
		TraceID: f.x.Trace.TraceID, Model: model, Created: f.start.Unix(), Upstream: ans.body, Response: ans.resp,
	})
	return &jsonOut{
		status: status, body: body, built: true,
		final: &gate.Final{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: b.Text()},
	}
}

// fallbackJSON runs the route's fallback for a JSON answer and returns what it delivers. The
// fallback's attempt 1, when there was one, is recorded here, before the caller records the
// trace's decision (D22).
func (h *Handler) fallbackJSON(ctx context.Context, f *flow, res *gate.Result, ans *answer) *jsonOut {
	fo := h.fallback(ctx, f, res)
	if fo.Result1 != nil {
		h.sink.AttemptDecided(f.record, fo.Result1)
	}
	fin := &gate.Final{Action: fo.Action, Delivered: fo.Delivered, Text: fo.Text, Flags: fo.Flags, Reasons: fo.Reasons}
	switch fo.Delivered {
	case gate.DeliveredFallback:
		body := fo.Body
		if len(body) == 0 {
			model := ""
			if f.req != nil {
				model = f.req.Model
			}
			body = llmapi.SyntheticResponse(f.format, f.x.Trace.TraceID, model, f.start.Unix(), fo.Text)
		}
		return &jsonOut{status: 200, body: body, built: true, final: fin}
	case gate.DeliveredOriginal:
		fin.Text = ans.text
		return &jsonOut{status: ans.status, body: ans.body, rest: ans.rest, final: fin}
	}
	out := h.blockJSON(f, ans)
	fin.Delivered, fin.Text = gate.DeliveredBlocked, out.final.Text
	out.final = fin
	return out
}

// enforcing reports whether a mode enforces its decisions on the answer (block and fallback).
func enforcing(mode string) bool {
	return mode == decision.ModeBlock || mode == decision.ModeFallback
}

// unenforcedAction is the delivered action of an answer delivered whatever the decision says: a
// block (fail closed) becomes pass; observe, pass and flag stay.
func unenforcedAction(action string) string {
	if action == decision.ActionBlock || action == decision.ActionFallback {
		return decision.ActionPass
	}
	return action
}

// countRequest counts one gated /v1 request (requests_total) with the action it delivered, and
// observes the latency the gateway added (overhead_seconds): end minus f.start, minus the
// upstream time up.Ms. Call it once per gated request, when its response is written; the JSON
// path does (serveJSON, respondThenGate, upstreamFailed), the stream paths call it themselves.
func (h *Handler) countRequest(f *flow, action string, up gate.Upstream, end time.Time) {
	overhead := end.Sub(f.start)
	if up.Ms != nil {
		overhead -= time.Duration(*up.Ms) * time.Millisecond
	}
	metrics.ObserveRequest(f.route.Name, f.format, f.x.Trace.Mode, action, max(overhead, 0))
}
