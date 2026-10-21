package proxy

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/policy"
)

// Request headers the gateway reads (SPEC §2.12).
const (
	HeaderClientMode = "X-TrustAgent-Mode"
	HeaderScenario   = "X-TrustAgent-Scenario"
)

// Response headers (SPEC §2.12). Every one is listed in middleware.ExposedHeaders.
const (
	HeaderTraceID        = "X-TrustAgent-Trace-Id"
	HeaderRequestID      = "X-Request-ID"
	HeaderRoute          = "X-TrustAgent-Route"
	HeaderMode           = "X-TrustAgent-Mode"
	HeaderAction         = "X-TrustAgent-Action"
	HeaderWouldAction    = "X-TrustAgent-Would-Action"
	HeaderStatus         = "X-TrustAgent-Status"
	HeaderVerdict        = "X-TrustAgent-Verdict"
	HeaderRisk           = "X-TrustAgent-Risk"
	HeaderCalibrated     = "X-TrustAgent-Calibrated"
	HeaderDegraded       = "X-TrustAgent-Degraded"
	HeaderReasons        = "X-TrustAgent-Reasons"
	HeaderCheckedAgainst = "X-TrustAgent-Checked-Against"
	HeaderDecisionModel  = "X-TrustAgent-Decision-Model"
	HeaderGateMs         = "X-TrustAgent-Gate-Ms"
	HeaderOverheadMs     = "X-TrustAgent-Overhead-Ms"
	HeaderAudit          = "X-TrustAgent-Audit"
	HeaderSource         = "X-TrustAgent-Source"
)

// ResponseHeaders lists every response header the gateway sets.
var ResponseHeaders = []string{
	HeaderTraceID, HeaderRequestID, HeaderRoute, HeaderMode, HeaderAction, HeaderWouldAction, HeaderStatus,
	HeaderVerdict, HeaderRisk, HeaderCalibrated, HeaderDegraded, HeaderReasons, HeaderCheckedAgainst,
	HeaderDecisionModel, HeaderGateMs, HeaderOverheadMs, HeaderAudit, HeaderSource,
}

// Status values the handler sets without a decision.
const (
	StatusDeferred = "deferred"
	// StatusSkippedUnsupportedAPI marks an endpoint that is proxied but not gated (the OpenAI
	// Responses API, SPEC §2.4 row 73).
	StatusSkippedUnsupportedAPI = "skipped:unsupported_api"
)

// maxReasons caps X-TrustAgent-Reasons (SPEC §2.12: at most 8).
const maxReasons = 8

// setDecisionHeaders writes the headers of a decided exchange. Action is the delivered action
// (r.Final.Action once decided has set it, else the decision's); Would-Action comes from the
// decision and is sent when it differs from Action. With headers: action_only only Action joins
// Trace-Id and Request-ID.
func setDecisionHeaders(h http.Header, r *gate.Result, audit string) {
	d := r.Decision
	action := deliveredAction(r)
	h.Set(HeaderAction, action)
	if r.Headers == policy.HeadersActionOnly {
		return
	}
	h.Set(HeaderRoute, r.RouteLabel)
	h.Set(HeaderMode, d.EffectiveMode)
	h.Set(HeaderStatus, r.Status)
	if d.WouldAction != "" && d.WouldAction != action {
		h.Set(HeaderWouldAction, d.WouldAction)
	}
	if d.Verdict != nil {
		h.Set(HeaderVerdict, *d.Verdict)
	}
	if d.MaxRisk != nil {
		h.Set(HeaderRisk, decision.FormatRisk(*d.MaxRisk))
	}
	h.Set(HeaderCalibrated, d.Calibration)
	if d.Degraded != nil {
		h.Set(HeaderDegraded, *d.Degraded)
	}
	if reasons := headerReasons(r); len(reasons) > 0 {
		h.Set(HeaderReasons, strings.Join(reasons, ","))
	}
	h.Set(HeaderCheckedAgainst, r.CheckedAgainst)
	if snap := r.Snapshot(); snap != "" {
		h.Set(HeaderDecisionModel, snap)
	}
	if d.GateMs != nil {
		h.Set(HeaderGateMs, strconv.FormatInt(*d.GateMs, 10))
	}
	if r.OverheadMs != nil {
		h.Set(HeaderOverheadMs, strconv.FormatInt(*r.OverheadMs, 10))
	}
	if audit != "" {
		h.Set(HeaderAudit, audit)
	}
	setSource(h, r.Trace.Source)
}

// setDeferredHeaders writes the headers of an observe-mode response whose gate runs after it
// (SPEC §2.12: Trace-Id, Route, Mode, Action: observe, Status: deferred and Audit).
func setDeferredHeaders(h http.Header, t *gate.TraceStart, route *policy.Route, routeLabel string) {
	h.Set(HeaderAction, decision.ActionObserve)
	if route.Headers == policy.HeadersActionOnly {
		return
	}
	h.Set(HeaderRoute, routeLabel)
	h.Set(HeaderMode, t.Mode)
	h.Set(HeaderStatus, StatusDeferred)
	// The audit is chosen from the decision, which does not exist yet; only a route without deep
	// audits knows its answer up front.
	if !route.DeepAudit.Enabled {
		h.Set(HeaderAudit, gate.AuditDisabled)
	}
	setSource(h, t.Source)
}

// headerReasons is the X-TrustAgent-Reasons list of a decided exchange, at most maxReasons: the
// decision's reasons, then the delivery's that the decision does not have (a fallback that failed
// or could not be sent, D19), as the trace records them and the stream comment carries them. When
// the cap cuts, the delivery's reasons stay: they say why the delivered action is what it is.
func headerReasons(r *gate.Result) []string {
	reasons := r.Decision.Reasons
	var delivery []string
	if r.Final != nil {
		for _, reason := range r.Final.Reasons {
			if !slices.Contains(reasons, reason) && !slices.Contains(delivery, reason) {
				delivery = append(delivery, reason)
			}
		}
	}
	delivery = delivery[:min(len(delivery), maxReasons)]
	return append(slices.Clone(reasons[:min(len(reasons), maxReasons-len(delivery))]), delivery...)
}

// deliveredAction is the action a decided exchange delivered: the Final's, else the decision's.
func deliveredAction(r *gate.Result) string {
	if r.Final != nil && r.Final.Action != "" {
		return r.Final.Action
	}
	return r.Decision.Action
}

func setSource(h http.Header, source string) {
	if source != "" && source != gate.SourceProxy {
		h.Set(HeaderSource, source)
	}
}
