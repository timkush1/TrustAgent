package proxy

import (
	"context"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// Trace reasons a fallback adds through gate.Final.Reasons (never into decision/v1).
const (
	reasonFallbackFailed         = "fallback_failed"
	reasonFallbackFormatMismatch = "fallback_format_mismatch"
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

// fallback calls the route's fallback, re-gates its answer as attempt 1 and picks what is
// delivered (GH-6). For now it answers block with reason fallback_failed: nothing calls it while
// fallback routes are refused.
func (h *Handler) fallback(ctx context.Context, f *flow, res0 *gate.Result) *fallbackOut {
	return &fallbackOut{
		Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked, Text: f.route.Block.Message,
		Reasons: []string{reasonFallbackFailed},
	}
}
