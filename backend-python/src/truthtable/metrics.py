"""
Prometheus metrics of the TrustAgent audit engine (docs/v2/SPEC.md Appendix B).

Every name starts with ``trustagent_engine_`` (M11): the gateway's own metrics
are ``trustagent_gateway_*``, so the two services never share a name, and
Grafana tells them apart by ``job`` as well. Label values are bounded: stages,
RPC methods, gate actions, verdicts and KB statuses come from fixed sets, and
providers from the provider registry.
"""

from typing import Final

from prometheus_client import Counter, Gauge, Histogram

PREFIX: Final = "trustagent_engine_"

# ---------- Audits ----------

AUDITS_TOTAL = Counter(
    "trustagent_engine_audits_total",
    "Deep audits by outcome: success, degraded (some claims undecided) or error",
    ["status"],
)

AUDIT_DURATION = Histogram(
    "trustagent_engine_audit_duration_seconds",
    "Deep audit duration in seconds",
    buckets=[0.5, 1, 2, 5, 10, 30, 60],
)

FAITHFULNESS_SCORE = Histogram(
    "trustagent_engine_faithfulness_score",
    "Trust scores of the audits that have one",
    buckets=[0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0],
)

HALLUCINATIONS_DETECTED = Counter(
    "trustagent_engine_hallucinations_detected_total",
    "Audits with the verdict HALLUCINATED",
)

ACTIVE_AUDITS = Gauge(
    "trustagent_engine_active_audits",
    "Deep audits running now",
)

CLAIMS_TOTAL = Counter(
    "trustagent_engine_claims_total",
    "Checked claims by verdict",
    ["verdict"],
)

# The audit graph's stages, from the step timings its nodes record (`<stage>_ms`).
STAGES: Final = frozenset({"decompose", "retrieve", "verify", "score"})

STAGE_DURATION = Histogram(
    "trustagent_engine_stage_duration_seconds",
    "Time spent in each audit stage (decompose, retrieve, verify, score)",
    ["stage"],
    buckets=[0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60],
)

# ---------- Judge (the deep-audit LLM) ----------

JUDGE_REQUESTS = Counter(
    "trustagent_engine_judge_requests_total",
    "Judge calls by provider and outcome (ok or error)",
    ["provider", "outcome"],
)

JUDGE_TOKENS = Counter(
    "trustagent_engine_judge_tokens_total",
    "Judge tokens by provider and direction (input or output)",
    ["provider", "direction"],
)

JUDGE_COST_USD = Counter(
    "trustagent_engine_judge_cost_usd_total",
    "Judge spend in USD by provider (calls with a known cost only)",
    ["provider"],
)

JUDGE_FALLBACK_ACTIVE = Gauge(
    "trustagent_engine_judge_fallback_active",
    "1 when the engine runs a fallback judge instead of the configured one",
)

# ---------- RPCs, the gate and the knowledge base ----------

RPC_INFLIGHT = Gauge(
    "trustagent_engine_rpc_inflight",
    "gRPC calls being served, by method",
    ["method"],
)

# What the proxy's fast check decided (GateDecision.action) against the deep
# audit's verdict: the raw material of calibration and of the ROI figures.
GATE_ACTIONS: Final = frozenset({"pass", "flag", "block", "fallback"})

GATE_VS_AUDIT = Counter(
    "trustagent_engine_gate_vs_audit_total",
    "Audited answers by the gate's action and the audit's verdict",
    ["gate_action", "audit_verdict"],
)

KB_STATUSES: Final = frozenset({"accepted", "quarantined"})

KB_INGEST_CLAIMS = Counter(
    "trustagent_engine_kb_ingest_claims_total",
    "Knowledge-base claims ingested, by status (accepted or quarantined)",
    ["status"],
)
