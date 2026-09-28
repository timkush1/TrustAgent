package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Policy metrics (SPEC Appendix B "Policy and storage").
var (
	// PolicyVersion is the active policy version id.
	PolicyVersion = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "policy_version",
		Help: "Active policy version id.",
	})

	// PolicyReloadErrors counts policies that failed to load, by source (file or database).
	PolicyReloadErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "policy_reload_errors_total",
		Help: "Policy loads that failed validation, by source (file or database).",
	}, []string{"source"})
)
