package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Decision-model (Jev) metrics, SPEC Appendix B: trustagent_gateway_jev_*. Labels are bounded:
// backend is one of the six backend ids, outcome an attempt status or error kind, component a
// cost-ledger component, class sync|async, kind an anomaly kind. Snapshot ids are few.
var (
	// JevRequests counts decision attempts by backend and outcome: ok, hedge_lost, cancelled or
	// an error kind (auth, credits, retryable, timeout, bad_content_type, …).
	JevRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "jev_requests_total",
		Help: "Decision-model attempts by backend and outcome.",
	}, []string{"backend", "outcome"})

	// JevSeconds is the latency of decision attempts that got a response.
	JevSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "jev_seconds",
		Help:    "Decision-model attempt latency.",
		Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10},
	}, []string{"backend"})

	// JevConnectSeconds is the time to obtain a connection (0 for a reused, warm one).
	JevConnectSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Name: "jev_connect_seconds",
		Help:    "Time to obtain a decision-model connection (DNS, TCP and TLS; 0 when reused).",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	})

	// JevCostUSD is the decision-model spend by backend and component.
	JevCostUSD = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "jev_cost_usd_total",
		Help: "Decision-model spend in USD by backend and component (hedge losers included).",
	}, []string{"backend", "component"})

	// JevInputTokens counts reported (or estimated) input tokens.
	JevInputTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "jev_input_tokens_total",
		Help: "Decision-model input tokens by backend.",
	}, []string{"backend"})

	// JevBreakerState is the breaker state per backend: 0 closed, 1 half-open, 2 open.
	JevBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "jev_breaker_state",
		Help: "Decision-model circuit breaker: 0 closed, 1 half-open, 2 open.",
	}, []string{"backend"})

	// JevRateLimited counts calls the gateway's limiter refused, by call class.
	JevRateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "jev_rate_limited_total",
		Help: "Decision calls refused by the gateway limiter (rate or in-flight cap), by class.",
	}, []string{"class"})

	// JevSnapshotInfo is 1 for each snapshot currently answering a requested model.
	JevSnapshotInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "jev_snapshot_info",
		Help: "Decision-model snapshots currently answering (value 1).",
	}, []string{"snapshot"})

	// JevAnomalies counts response anomalies: snapshot_changed, missing_answer,
	// unexpected_answer, type_mismatch, probability_sum.
	JevAnomalies = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "jev_anomalies_total",
		Help: "Decision-model response anomalies by kind.",
	}, []string{"kind"})
)
