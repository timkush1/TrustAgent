package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	AuditsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trustagent_audits_total",
			Help: "Total number of audits processed",
		},
		[]string{"status"},
	)

	HallucinationsDetected = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "trustagent_hallucinations_detected_total",
			Help: "Total number of hallucinations detected",
		},
	)

	AuditDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "trustagent_audit_duration_seconds",
			Help:    "Audit processing duration in seconds",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60},
		},
	)

	FaithfulnessScore = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "trustagent_faithfulness_score",
			Help:    "Distribution of faithfulness scores",
			Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
	)

	ActiveAudits = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "trustagent_active_audits",
			Help: "Number of currently processing audits",
		},
	)

	WebSocketClients = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "trustagent_websocket_clients",
			Help: "Number of connected WebSocket clients",
		},
	)

	ClaimsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "trustagent_claims_total",
			Help: "Total claims by verification status",
		},
		[]string{"status"},
	)
)
