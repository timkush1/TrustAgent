package proxy

import (
	"time"

	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/llmapi"
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
