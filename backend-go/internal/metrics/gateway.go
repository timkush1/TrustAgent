package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Gateway request, gate, fallback, retention and spend metrics (SPEC Appendix B). Labels are
// bounded: route by RouteLabel (at most maxRouteLabels names, then "other"), format an API format
// id, mode a policy mode, action a gate action, status a decision status, stage a gate stage,
// evaluator an evaluator ref, level a risk level, detector a detector id, outcome a fallback
// outcome and kind a spend or error kind. There is no model label.
var (
	// Requests counts /v1 requests by route, API format and delivered action.
	Requests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "requests_total",
		Help: "Gateway /v1 requests by route, API format and delivered action.",
	}, []string{"route", "format", "action"})

	// Overhead is the latency the gateway adds to a /v1 request (total minus the upstream time).
	Overhead = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "overhead_seconds",
		Help:    "Latency the gateway adds to a /v1 request (total minus the upstream time), by route and configured mode.",
		Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route", "mode"})

	// UpstreamLatency is the upstream LLM API's answer time by API format.
	UpstreamLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "upstream_seconds",
		Help:    "Upstream LLM API answer time by API format.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120},
	}, []string{"format"})

	// GateDecisions counts gate decisions by route, configured mode, action and decision status.
	GateDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "gate_decisions_total",
		Help: "Gate decisions by route, configured mode, action and decision status.",
	}, []string{"route", "mode", "action", "status"})

	// GateLatency is the time of one gate stage: detectors, compile, jev or decide.
	GateLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "gate_seconds",
		Help:    "Gate time per stage (detectors, compile, jev, decide).",
		Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"stage"})

	// GateErrors counts gate failures by kind (an error kind, compile or internal).
	GateErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "gate_errors_total",
		Help: "Gate failures by kind.",
	}, []string{"kind"})

	// EvaluatorLevels counts evaluator outcomes by evaluator ref, risk level and stage.
	EvaluatorLevels = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "evaluator_levels_total",
		Help: "Evaluator outcomes by evaluator ref, risk level and stage.",
	}, []string{"evaluator", "level", "stage"})

	// DetectorFirings counts Tier-0 detector firings by detector.
	DetectorFirings = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "detector_fired_total",
		Help: "Tier-0 detector firings by detector.",
	}, []string{"detector"})

	// FallbackOutcomes counts fallback attempts by outcome: delivered, on_fail_block,
	// on_fail_flag, on_fail_original, failed or format_mismatch.
	FallbackOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "fallback_total",
		Help: "Fallback attempts by outcome.",
	}, []string{"outcome"})

	// UniformSamplesToday is the number of uniform deep-audit samples taken today (UTC).
	UniformSamplesToday = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "uniform_samples_today",
		Help: "Uniform deep-audit samples taken today (UTC day).",
	})

	// RetentionLag is how far the retention job is behind: the age of the oldest row past its
	// retention that is still stored.
	RetentionLag = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "retention_lag_seconds",
		Help: "Age of the oldest row past its retention that is still stored.",
	})

	// SpendToday is today's spend in USD by kind (decision or judge).
	SpendToday = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "spend_usd_today",
		Help: "Spend in USD today (UTC day) by kind.",
	}, []string{"kind"})

	// SpendReserved is the spend in USD reserved for calls in flight, by kind.
	SpendReserved = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "spend_reserved_usd",
		Help: "Spend in USD reserved for calls in flight, by kind.",
	}, []string{"kind"})

	// CostWritesDropped counts cost_events rows the ledger dropped because its buffer was full
	// or the write failed (P4-PLAN D51, SPEC Appendix B erratum).
	CostWritesDropped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Name: "cost_write_dropped_total",
		Help: "Cost ledger rows dropped (buffer full or write failed).",
	})
)

// maxRouteLabels is how many distinct route names the route label takes before "other".
const maxRouteLabels = 64

// routeLabels bounds the route label: the first max distinct names keep their own value, every
// later one is "other".
type routeLabels struct {
	mu   sync.Mutex
	seen map[string]struct{}
	max  int
}

func (l *routeLabels) label(route string) string {
	if route == "" {
		return "none"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.seen[route]; ok {
		return route
	}
	if len(l.seen) >= l.max {
		return "other"
	}
	if l.seen == nil {
		l.seen = make(map[string]struct{}, l.max)
	}
	l.seen[route] = struct{}{}
	return route
}

var routes = &routeLabels{max: maxRouteLabels}

// RouteLabel is the route label value of a route name: the name for the first 64 distinct
// routes the process sees, then "other"; "none" when there is no route.
func RouteLabel(route string) string { return routes.label(route) }

// orNone is a label value that is "none" when empty.
func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

// ObserveRequest counts one /v1 request (requests_total) with its delivered action ("" for an
// ungated request is recorded as none) and, when overhead is not negative, observes the latency
// the gateway added (overhead_seconds) under the configured mode.
func ObserveRequest(route, format, mode, action string, overhead time.Duration) {
	r := RouteLabel(route)
	Requests.WithLabelValues(r, orNone(format), orNone(action)).Inc()
	if overhead >= 0 {
		Overhead.WithLabelValues(r, orNone(mode)).Observe(overhead.Seconds())
	}
}

// ObserveUpstream observes one upstream answer time (upstream_seconds).
func ObserveUpstream(format string, d time.Duration) {
	UpstreamLatency.WithLabelValues(orNone(format)).Observe(d.Seconds())
}

// ObserveGate counts one gate decision (gate_decisions_total).
func ObserveGate(route, mode, action, status string) {
	GateDecisions.WithLabelValues(RouteLabel(route), orNone(mode), orNone(action), orNone(status)).Inc()
}

// ObserveGateStage observes the time of one gate stage (gate_seconds).
func ObserveGateStage(stage string, d time.Duration) {
	GateLatency.WithLabelValues(stage).Observe(d.Seconds())
}

// GateError counts one gate failure (gate_errors_total).
func GateError(kind string) { GateErrors.WithLabelValues(orNone(kind)).Inc() }

// EvaluatorLevel counts one evaluator outcome (evaluator_levels_total).
func EvaluatorLevel(evaluator, level, stage string) {
	EvaluatorLevels.WithLabelValues(evaluator, orNone(level), orNone(stage)).Inc()
}

// DetectorFired counts one detector firing (detector_fired_total).
func DetectorFired(detector string) { DetectorFirings.WithLabelValues(detector).Inc() }

// Fallback counts one fallback attempt by outcome (fallback_total).
func Fallback(outcome string) { FallbackOutcomes.WithLabelValues(outcome).Inc() }

// SetUniformSamplesToday sets today's uniform deep-audit sample count (uniform_samples_today).
func SetUniformSamplesToday(n int) { UniformSamplesToday.Set(float64(n)) }

// SetRetentionLag sets how far the retention job is behind (retention_lag_seconds).
func SetRetentionLag(d time.Duration) { RetentionLag.Set(max(d, 0).Seconds()) }

// CostWriteDropped counts one dropped cost ledger row (cost_write_dropped_total).
func CostWriteDropped() { CostWritesDropped.Inc() }

// SetSpend sets today's spend and the reserved spend of a kind (spend_usd_today,
// spend_reserved_usd).
func SetSpend(kind string, todayUSD, reservedUSD float64) {
	SpendToday.WithLabelValues(kind).Set(todayUSD)
	SpendReserved.WithLabelValues(kind).Set(reservedUSD)
}
