// Package metrics holds the proxy's Prometheus metrics, namespace trustagent_gateway_* (SPEC
// Appendix B). Audit outcomes (scores, verdicts, claims) are the engine's metrics
// (trustagent_engine_*); the gateway counts only its own work: audit jobs, the stream and trace
// writes. The remaining Appendix B families land with their components.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const namespace = "trustagent_gateway"

var (
	// AuditJobs counts deep-audit jobs by lane, selection and outcome: completed, degraded,
	// failed, not_assessed, dropped (not queued) or stopped (abandoned at shutdown).
	AuditJobs = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "audit_jobs_total",
		Help: "Deep-audit jobs by lane, selection and outcome.",
	}, []string{"lane", "selection", "outcome"})

	// AuditQueueDepth is the number of queued deep-audit jobs per lane.
	AuditQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "audit_queue_depth",
		Help: "Queued deep-audit jobs per lane.",
	}, []string{"lane"})

	// AuditRoundtrip is the time from queueing a deep audit to its persisted result.
	AuditRoundtrip = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Name: "audit_roundtrip_seconds",
		Help:    "Time from queueing a deep audit to its persisted result.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120},
	})

	// WSClients is the number of connected WebSocket clients.
	WSClients = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "ws_clients",
		Help: "Connected WebSocket clients.",
	})

	// WSDropped counts events a client missed: producer overflow and full client buffers.
	WSDropped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Name: "ws_dropped_total",
		Help: "WebSocket events dropped (hub overflow or a slow client).",
	})

	// WSGaps counts gap events sent, by reason.
	WSGaps = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "ws_gaps_total",
		Help: "WebSocket gap events sent, by reason.",
	}, []string{"reason"})

	// TraceWriteDropped counts trace writes that did not reach the database.
	TraceWriteDropped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Name: "trace_write_dropped_total",
		Help: "Trace writes (start, decision, body, audit status) that were dropped.",
	})
)
