"""
Prometheus metrics for the TrustAgent audit engine.
"""

from prometheus_client import Counter, Histogram, Gauge

AUDITS_TOTAL = Counter(
    "trustagent_audits_total",
    "Total number of audits processed",
    ["status"],
)

AUDIT_DURATION = Histogram(
    "trustagent_audit_duration_seconds",
    "Audit processing duration in seconds",
    buckets=[0.5, 1, 2, 5, 10, 30, 60],
)

FAITHFULNESS_SCORE = Histogram(
    "trustagent_faithfulness_score",
    "Distribution of faithfulness scores",
    buckets=[0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0],
)

HALLUCINATIONS_DETECTED = Counter(
    "trustagent_hallucinations_detected_total",
    "Total number of hallucinations detected",
)

ACTIVE_AUDITS = Gauge(
    "trustagent_active_audits",
    "Number of currently processing audits",
)

CLAIMS_TOTAL = Counter(
    "trustagent_claims_total",
    "Total claims by verification status",
    ["status"],
)
