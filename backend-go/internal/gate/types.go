package gate

import (
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// Trace sources (TraceSource).
const (
	SourceProxy      = "proxy"
	SourceDirect     = "direct"
	SourcePlayground = "playground"
	SourceTest       = "test"
	SourceTrafficgen = "trafficgen"
)

// Answer sources (TraceSummary.answer_source).
const (
	AnswerScripted = "scripted"
	AnswerLive     = "live"
)

// Trace flags (traces.flags) the gateway sets in S0.
const (
	FlagTestMode      = "test_mode"
	FlagMockDecision  = "mock_decision"
	FlagUniformSample = "uniform_sample"
)

// TraceStart is a gated request as it arrives, before the upstream call: the traces row with
// gate_status pending (SPEC §2.3).
type TraceStart struct {
	TraceID string `json:"trace_id"`
	// ClientRequestID is the client's X-Request-ID when it passed the B4 rule, else nil.
	ClientRequestID *string   `json:"client_request_id"`
	CreatedAt       time.Time `json:"created_at"`
	Source          string    `json:"source"`
	APIFormat       string    `json:"api_format"`
	Path            string    `json:"path"`
	Route           string    `json:"route"`
	PolicyVersion   int64     `json:"policy_version"`
	// Mode is the configured mode after the client override (X-TrustAgent-Mode).
	Mode     string `json:"mode"`
	KeyLabel string `json:"key_label,omitempty"`
	Model    string `json:"model,omitempty"`
	Stream   bool   `json:"stream"`
	// Scenario is the X-TrustAgent-Scenario id (test mode only).
	Scenario     string   `json:"scenario,omitempty"`
	AnswerSource string   `json:"answer_source,omitempty"`
	Flags        []string `json:"flags"`
	// UserRequest is the extracted user request, for the question preview.
	UserRequest string `json:"-"`
}

// Decision is one gate decision as stored and served: decision/v1
// (contracts/schema/decision.v1.schema.json, api.ts GateDecisionView).
type Decision struct {
	Spec          string    `json:"spec"`
	TraceID       string    `json:"trace_id"`
	Attempt       int       `json:"attempt"`
	CreatedAt     time.Time `json:"created_at"`
	Route         string    `json:"route"`
	PolicyVersion *int64    `json:"policy_version"`
	Mode          string    `json:"mode"`
	Fail          string    `json:"fail"`
	// Core carries effective_mode, status, skip_reason, action, would_action, enforced, verdict,
	// max_risk, calibration, degraded, disagreement, deep_audit, reasons and evaluators.
	decision.Core
	Detectors       []decision.DetectorResult `json:"detectors"`
	Excluded        []decision.Exclusion      `json:"excluded"`
	DecisionModel   *DecisionModelUsage       `json:"decision_model"`
	Compile         *CompileInfo              `json:"compile"`
	EvidenceSummary *evidence.Summary         `json:"evidence_summary"`
	Late            bool                      `json:"late"`
	GateMs          *int64                    `json:"gate_ms"`
}

// DecisionSpec is the decision/v1 spec marker.
const DecisionSpec = "decision/v1"

// DecisionModelUsage is how the decision model was called (decision/v1 DecisionModelUsage).
type DecisionModelUsage struct {
	Backend        string   `json:"backend"`
	ModelRequested *string  `json:"model_requested"`
	Snapshot       *string  `json:"snapshot"`
	Calls          int      `json:"calls"`
	Attempts       int      `json:"attempts"`
	Hedged         bool     `json:"hedged"`
	LatencyMs      *int64   `json:"latency_ms"`
	ConnectMs      *int64   `json:"connect_ms"`
	InputTokens    *int     `json:"input_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	CostSource     string   `json:"cost_source"`
}

// CompileInfo records what compile() sent (decision/v1 CompileInfo).
type CompileInfo struct {
	CPT               float64  `json:"cpt"`
	Requests          int      `json:"requests"`
	QuestionSetSHA256 string   `json:"question_set_sha256"`
	RequestSHA256     []string `json:"request_sha256"`
	PackedEvidenceIDs []string `json:"packed_evidence_ids"`
	TruncatedIDs      []string `json:"truncated_ids"`
	EstTokens         []int    `json:"est_tokens"`
}

// Audit selections (AuditSelection) the gate makes, and lanes.
const (
	SelectionHigh        = "high"
	SelectionSample      = "sample"
	SelectionErrorSample = "error_sample"
	SelectionUniform     = "uniform"

	LaneHigh = "high"
	LaneLow  = "low"
)

// Trace audit statuses (TraceAuditStatus) the gate or the sink report in X-TrustAgent-Audit.
const (
	AuditQueued      = "queued"
	AuditSampledOut  = "sampled_out"
	AuditDropped     = "dropped"
	AuditDisabled    = "disabled"
	AuditNotAssessed = "not_assessed"
)

// AuditPlan is the deep-audit decision for a trace (SPEC §2.9 caller rules).
type AuditPlan struct {
	// Selection is set when the trace should get a deep audit: high, sample, error_sample or
	// uniform. The sink then submits it and reports queued or dropped.
	Selection string
	// Lane is the requested lane (high or low); the worker may demote high to low.
	Lane string
	// InclusionProb is the probability that the trace was selected (1 for high).
	InclusionProb float64
	// Status is the trace audit status when Selection is empty: sampled_out, disabled or
	// not_assessed.
	Status string
	// Verifier is the route deep_audit.verifier; uniform samples use llm.
	Verifier string
	// UniformSample marks the pre-gate uniform draw (AuditOptions.uniform_sample).
	UniformSample bool
	// MockScenario is the scenario id for the mock judge (AuditOptions.mock_scenario, test mode).
	MockScenario string
}

// BodyPlan says whether the trace body is kept (route store_bodies, SPEC §2.3 trace_bodies).
type BodyPlan struct {
	Store         bool
	InclusionProb float64
}

// ChatMessage is a request message as trace bodies store it (api.ts ChatMessage).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// Messages converts parsed request messages for trace bodies.
func Messages(msgs []llmapi.Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, ChatMessage{Role: m.Role, Content: m.Text(), Name: m.Name})
	}
	return out
}

// Upstream describes the upstream exchange of a gated request.
type Upstream struct {
	// Status is the upstream HTTP status (200 for a test-mode answer); 0 when there was none.
	Status           int
	Ms               *int64
	PromptTokens     *int
	CompletionTokens *int
}

// Result is the outcome of gating one exchange: everything the sink persists and broadcasts and
// the handler turns into response headers.
type Result struct {
	Trace    *TraceStart
	Decision *Decision
	// Status is the X-TrustAgent-Status value: ok, partial, not_assessed, skipped:<reason>,
	// timeout or error:<kind>.
	Status string
	// RouteLabel is the X-TrustAgent-Route value, <route>@v<policy_version>.
	RouteLabel string
	// CheckedAgainst is the X-TrustAgent-Checked-Against value.
	CheckedAgainst string
	// Evidence is the full, pre-packing evidence (untrusted items included and marked); Truncated
	// marks the items packing cut short.
	Evidence           []evidence.Item
	UserRequest        string
	SystemInstructions string
	// ResponseText is the gated upstream text (the original answer, never block or fallback text).
	ResponseText string
	// Rules are the resolved rules params of the route's evaluators.
	Rules    map[string]string
	Messages []ChatMessage
	Audit    AuditPlan
	Body     BodyPlan
	Flags    []string
	// Upstream, TotalMs and OverheadMs are filled by the handler before the sink sees the result.
	Upstream   Upstream
	TotalMs    *int64
	OverheadMs *int64
	// Headers is the route headers setting (full or action_only).
	Headers string
}

// Snapshot returns the decision-model snapshot that answered, or "".
func (r *Result) Snapshot() string {
	if r.Decision != nil && r.Decision.DecisionModel != nil && r.Decision.DecisionModel.Snapshot != nil {
		return *r.Decision.DecisionModel.Snapshot
	}
	return ""
}

// statusValue renders the X-TrustAgent-Status value of a decision.
func statusValue(core *decision.Core, requestStatus map[string]string) string {
	switch core.Status {
	case decision.StatusSkipped:
		if core.SkipReason != nil {
			return "skipped:" + *core.SkipReason
		}
		return "skipped"
	case decision.StatusError:
		for k := 0; k < len(requestStatus); k++ {
			if st := requestStatus[itoa(k)]; strings.HasPrefix(st, "error:") {
				return st
			}
		}
		return "error"
	}
	return core.Status
}
