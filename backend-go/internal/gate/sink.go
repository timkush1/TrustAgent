package gate

import "context"

// Sink receives a gated request's lifecycle. The handler calls TraceStarted once the route is
// resolved and before the upstream call, then GateDecided exactly once with the final result:
// before the response is written in sync modes (flag), after it in async observe. Implementations
// persist the trace and decision, broadcast trace_started and gate_decision over WS v2, and submit
// the deep audit the result's AuditPlan selects.
//
// GateDecided returns the trace audit status for X-TrustAgent-Audit: queued or dropped for a
// selected audit (disabled when no audit engine is available), otherwise r.Audit.Status. Both
// methods must be safe for concurrent use and must not retain r's slices beyond the call unless
// they copy them.
type Sink interface {
	TraceStarted(ctx context.Context, t *TraceStart)
	GateDecided(ctx context.Context, r *Result) string
}

// NopSink records nothing and submits no audits: every selected audit reports disabled.
type NopSink struct{}

// TraceStarted implements Sink.
func (NopSink) TraceStarted(context.Context, *TraceStart) {}

// GateDecided implements Sink.
func (NopSink) GateDecided(_ context.Context, r *Result) string {
	if r.Audit.Selection != "" {
		return AuditDisabled
	}
	return r.Audit.Status
}
