package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/upstream"
)

// Trace reasons a fallback adds through gate.Final.Reasons (never into decision/v1).
const (
	reasonFallbackFailed         = "fallback_failed"
	reasonFallbackFormatMismatch = "fallback_format_mismatch"
)

// The outcomes fallback_total counts (metrics.Fallback).
const (
	fallbackDelivered      = "delivered"
	fallbackOnFailBlock    = "on_fail_block"
	fallbackOnFailFlag     = "on_fail_flag"
	fallbackOnFailOriginal = "on_fail_original"
	fallbackFailed         = "failed"
	fallbackFormatMismatch = "format_mismatch"
)

// Values of a route's fallback settings (policy/v1 RouteSettings.fallback): upstream is same or
// fallback, on_fail is block, flag or original.
const (
	fallbackUpstreamFallback = "fallback"
	onFailFlag               = "flag"
	onFailOriginal           = "original"
)

// fallbackDeadlineMargin is how far past the fallback call's own timeout the response's write
// deadline moves: the re-gate (at most the route's deadline_ms, 5 s) and the write come after it.
const fallbackDeadlineMargin = 30 * time.Second

// Why a fallback call gave no answer to re-gate (D2), besides a transport error.
var (
	errFallbackStatus   = errors.New("proxy: the fallback upstream answered with an error status")
	errFallbackTooLarge = errors.New("proxy: the fallback answer is larger than TRUTHTABLE_MAX_UPSTREAM_RESPONSE_BYTES")
	errFallbackNoText   = errors.New("proxy: the fallback answer has no text")
)

// fallbackOut is how a fallback attempt ends (SPEC §2.12 fallback): the action and body the
// client gets, and what the trace records.
type fallbackOut struct {
	// Action is the delivered action (fallback, block, or flag for on_fail flag and original);
	// Delivered is fallback, blocked or original (gate.Delivered*); Text is the delivered text.
	Action, Delivered, Text string
	// Body is the response body to send; Response is the parsed fallback answer, nil unless the
	// fallback was delivered.
	Body     []byte
	Response *llmapi.ChatResponse
	// Result1 is the attempt-1 re-gate, nil when the fallback call did not answer.
	Result1 *gate.Result
	// Flags and Reasons are added to the trace (fallback_used; fallback_failed,
	// fallback_format_mismatch).
	Flags, Reasons []string
}

// fallback runs a route's fallback for an answer whose decision is fallback (res0, attempt 0;
// SPEC §2.12 fallback, D1-D3) and picks what is delivered:
//
//  1. the fallback request is the client's request in the same format, with the route's fallback
//     model, and the fallback prompt with the request's trusted evidence (at most 8k tokens) in
//     front, never streamed (llmapi.BuildFallbackRequest);
//  2. it goes once to the upstream fallback.upstream names: same (f.up, with the client's
//     headers) or the fallback upstream, whose format family must match the request's (a mismatch
//     blocks, reason fallback_format_mismatch). The call runs on ctx, the client's context (M1),
//     after the response's write deadline has moved past it. In test mode a scripted answer
//     (test_response) whose scenario has a fallback answer takes that answer instead, with no call
//     (D1, testfallback.go);
//  3. the answer is re-gated as attempt 1, with no audit and a fresh route deadline, on f.record
//     like attempt 0;
//  4. an attempt-1 action pass or flag delivers the fallback answer (Action fallback, flag
//     fallback_used); anything else (would block, or a fail-closed block) applies on_fail: block
//     sends the route's block body, flag the fallback answer with Action flag, original the
//     original answer with Action flag (D3).
//
// A call that gives no answer (a transport error, a non-2xx, a body that is not JSON, too large or
// without text) is not re-gated: on_fail block blocks, flag and original deliver the original with
// Action flag, reason fallback_failed (D2).
//
// fallback records nothing on the sink: the caller records Result1 with sink.AttemptDecided, and
// then attempt 0 once with decided (D22). It counts fallback_total by outcome. Its signature is
// fixed (the buffer path and the Playground call it).
func (h *Handler) fallback(ctx context.Context, f *flow, res0 *gate.Result) *fallbackOut {
	settings := f.route.Fallback
	up := f.up
	if settings.Upstream == fallbackUpstreamFallback {
		set := f.ups
		if set == nil {
			set = h.upstreams()
		}
		up = set.Fallback
		if up != nil && !llmapi.FallbackFormatCompatible(f.format, set.FallbackFormat) {
			h.log.Warn("the fallback upstream cannot answer this API format: blocking", "trace_id", f.x.Trace.TraceID,
				"route", f.route.Name, "format", f.format, "fallback_format", set.FallbackFormat)
			metrics.Fallback(fallbackFormatMismatch)
			return h.fallbackBlocked(f, reasonFallbackFormatMismatch)
		}
	}

	text, resp, body, err := h.fallbackAnswer(ctx, f, up)
	if err != nil {
		metrics.Fallback(fallbackFailed)
		return h.fallbackFailed(ctx, f, res0, err)
	}

	x := f.x
	res1 := h.gate.Evaluate(f.record, &gate.Exchange{
		Trace: x.Trace, Policy: x.Policy, Route: x.Route, Request: x.Request, Evidence: x.Evidence,
		Response: text, Stream: x.Stream, Attempt: 1, NoAudit: true, KnowledgeOnly: x.KnowledgeOnly, Advisory: x.Advisory,
	})
	delivered := func(action string) *fallbackOut {
		return &fallbackOut{
			Action: action, Delivered: gate.DeliveredFallback, Text: text, Body: body, Response: resp, Result1: res1,
			Flags: []string{gate.FlagFallbackUsed},
		}
	}
	switch action := res1.Decision.Action; {
	case action == decision.ActionPass || action == decision.ActionFlag:
		metrics.Fallback(fallbackDelivered)
		return delivered(decision.ActionFallback)
	case settings.OnFail == onFailFlag:
		metrics.Fallback(fallbackOnFailFlag)
		return delivered(decision.ActionFlag)
	case settings.OnFail == onFailOriginal:
		metrics.Fallback(fallbackOnFailOriginal)
		return &fallbackOut{Action: decision.ActionFlag, Delivered: gate.DeliveredOriginal, Text: x.Response, Result1: res1}
	}
	metrics.Fallback(fallbackOnFailBlock)
	out := h.fallbackBlocked(f)
	out.Result1 = res1
	return out
}

// fallbackAnswer gets the fallback's answer to f: the scripted one in test mode (D1), else the
// answer of one call to up. It returns the gated text (choice 0), the parsed answer and its body
// (both nil for a scripted answer: the caller builds the body from the text). An error means there
// is no answer to re-gate (D2).
func (h *Handler) fallbackAnswer(ctx context.Context, f *flow, up *upstream.Client) (string, *llmapi.ChatResponse, []byte, error) {
	if text, ok := h.scriptedFallback(f); ok {
		return text, nil, nil, nil
	}
	if up == nil {
		return "", nil, nil, errNoUpstream
	}
	settings := f.route.Fallback
	fr, err := llmapi.BuildFallbackRequest(f.format, f.raw, llmapi.Fallback{
		Model: settings.Model, SystemPrompt: settings.SystemPrompt, Evidence: trustedEvidence(f),
		MaxTokens: llmapi.DefaultFallbackEvidenceTokens,
		// CountTokens stays the default estimate (four characters a token): the fallback prompt goes
		// to the LLM upstream, whose tokenizer is not the decision model's.
	})
	if err != nil {
		return "", nil, nil, err
	}
	// The call, its re-gate and the write come after the upstream call the route's write deadline
	// was sized for (SPEC §2.12, M12).
	if d := up.Timeout(); d > 0 {
		_ = http.NewResponseController(f.c.Writer).SetWriteDeadline(time.Now().Add(d + fallbackDeadlineMargin))
	}
	query := ""
	if llmapi.FormatForPath(f.c.Request.URL.Path) != "" {
		query = f.c.Request.URL.RawQuery // the client's own query on a /v1 call (M2), such as an api-version
	}
	resp, cancel, err := up.Do(ctx, http.MethodPost, formatPath(f.format), query, f.c.Request.Header, fr.Body)
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { _ = resp.Body.Close(); cancel() }()
	body, tooLarge, err := upstream.ReadBody(resp, h.maxBytes)
	switch {
	case err != nil:
		return "", nil, nil, err
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return "", nil, nil, fmt.Errorf("%w: %d", errFallbackStatus, resp.StatusCode)
	case tooLarge:
		return "", nil, nil, errFallbackTooLarge
	}
	parsed, err := llmapi.ParseResponse(f.format, body)
	if err != nil {
		return "", nil, nil, err
	}
	if choice, ok := parsed.Gated(); ok && choice.Text != "" {
		return choice.Text, parsed, body, nil
	}
	return "", nil, nil, errFallbackNoText
}

// fallbackFailed is the outcome of a fallback call that gave no answer (D2): on_fail block blocks,
// flag and original deliver the original answer with Action flag, reason fallback_failed. A call
// that ended because the client went away is flagged client_disconnected: nothing reaches the
// client.
func (h *Handler) fallbackFailed(ctx context.Context, f *flow, res0 *gate.Result, err error) *fallbackOut {
	var out *fallbackOut
	if f.route.Fallback.OnFail == onFailFlag || f.route.Fallback.OnFail == onFailOriginal {
		out = &fallbackOut{
			Action: decision.ActionFlag, Delivered: gate.DeliveredOriginal, Text: f.x.Response,
			Reasons: []string{reasonFallbackFailed},
		}
	} else {
		out = h.fallbackBlocked(f, reasonFallbackFailed)
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		h.log.Debug("the client went away during the fallback call", "trace_id", res0.Trace.TraceID)
		out.Flags = append(out.Flags, gate.FlagClientDisconnected)
		return out
	}
	h.log.Warn("fallback call failed", "trace_id", res0.Trace.TraceID, "route", f.route.Name, "on_fail", f.route.Fallback.OnFail, "err", err)
	return out
}

// fallbackBlocked is a fallback that ends in the route's block body, with the trace reasons given.
func (h *Handler) fallbackBlocked(f *flow, reasons ...string) *fallbackOut {
	return &fallbackOut{
		Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: blockOf(f.route).Text(), Reasons: reasons,
	}
}

// trustedEvidence is the evidence the fallback prompt may carry: f's items in priority order,
// without the untrusted ones (SPEC §2.12 fallback step 1).
func trustedEvidence(f *flow) []llmapi.EvidenceLine {
	if f.x.Evidence == nil {
		return nil
	}
	var lines []llmapi.EvidenceLine
	for _, it := range f.x.Evidence.Items {
		if !it.Untrusted {
			lines = append(lines, llmapi.EvidenceLine{ID: it.ID, Text: it.Text})
		}
	}
	return lines
}

// formatPath is the API path of a gated format: where its fallback request goes.
func formatPath(format string) string {
	switch format {
	case llmapi.FormatOpenAICompletions:
		return llmapi.PathCompletions
	case llmapi.FormatAnthropicMessages:
		return llmapi.PathMessages
	}
	return llmapi.PathChatCompletions
}
