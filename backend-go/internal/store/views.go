package store

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// The wire views of the REST API and the WS v2 events (SPEC §2.6, frontend-react/src/types/api.ts,
// contracts/schema/api.v1.schema.json). Field names and JSON tags are identical to api.ts; the
// handler tests validate every view against the schema.

// Claim verdicts (ClaimVerdict), in the order api.ts lists them.
var ClaimVerdicts = []string{"SUPPORTED", "PARTIALLY_SUPPORTED", "CONTRADICTED", "UNSUPPORTED", "UNKNOWN", "NOT_CHECKED"}

// Audit verdicts (AuditVerdict).
const (
	VerdictGrounded     = "GROUNDED"
	VerdictHallucinated = "HALLUCINATED"
	VerdictUnverifiable = "UNVERIFIABLE"
	VerdictNoClaims     = "NO_CLAIMS"
	VerdictNotAssessed  = "NOT_ASSESSED"
)

// Audit statuses (AuditStatus, the audits.status column).
const (
	AuditQueued    = "queued"
	AuditRunning   = "running"
	AuditCompleted = "completed"
	AuditDegraded  = "degraded"
	AuditFailed    = "failed"
)

// Trace audit statuses (TraceAuditStatus, the traces.audit_status column) beyond the audit statuses.
const (
	TraceAuditNone        = "none"
	TraceAuditSampledOut  = "sampled_out"
	TraceAuditDropped     = "dropped"
	TraceAuditDisabled    = "disabled"
	TraceAuditNotAssessed = "not_assessed"
)

// GateStatusPending is the gate status of a trace whose decision has not been recorded yet.
const GateStatusPending = "pending"

// Preview lengths (SPEC §2.3 traces.question_preview and response_preview), in runes.
const (
	QuestionPreviewRunes = 200
	ResponsePreviewRunes = 300
)

// VerdictCounts is Record<ClaimVerdict, number>: every verdict is present.
type VerdictCounts map[string]int

// NewVerdictCounts returns counts with every verdict at zero.
func NewVerdictCounts() VerdictCounts {
	c := make(VerdictCounts, len(ClaimVerdicts))
	for _, v := range ClaimVerdicts {
		c[v] = 0
	}
	return c
}

// Timings are a trace's latencies in milliseconds.
type Timings struct {
	UpstreamMs *int64 `json:"upstream_ms"`
	TTFTMs     *int64 `json:"ttft_ms"`
	GateMs     *int64 `json:"gate_ms"`
	OverheadMs *int64 `json:"overhead_ms"`
	TotalMs    *int64 `json:"total_ms"`
}

// TraceDecisionModel is TraceSummary.decision_model.
type TraceDecisionModel struct {
	Backend  string   `json:"backend"`
	Snapshot *string  `json:"snapshot"`
	CostUSD  *float64 `json:"cost_usd"`
}

// TraceAudit is TraceSummary.audit.
type TraceAudit struct {
	AuditID       *string       `json:"audit_id"`
	Status        string        `json:"status"`
	Lane          *string       `json:"lane"`
	Selection     *string       `json:"selection"`
	Verdict       *string       `json:"verdict"`
	TrustScore    *float64      `json:"trust_score"`
	Grade         *string       `json:"grade"`
	VerdictCounts VerdictCounts `json:"verdict_counts"`
}

// TraceSummary is one row of the Traffic list (api.ts TraceSummary).
type TraceSummary struct {
	TraceID         string              `json:"trace_id"`
	ClientRequestID *string             `json:"client_request_id"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Source          string              `json:"source"`
	APIFormat       string              `json:"api_format"`
	Route           string              `json:"route"`
	PolicyVersion   *int64              `json:"policy_version"`
	Mode            string              `json:"mode"`
	EffectiveMode   *string             `json:"effective_mode"`
	Model           *string             `json:"model"`
	Stream          bool                `json:"stream"`
	UpstreamStatus  *int                `json:"upstream_status"`
	AnswerSource    *string             `json:"answer_source"`
	Action          *string             `json:"action"`
	WouldAction     *string             `json:"would_action"`
	GateStatus      string              `json:"gate_status"`
	SkipReason      *string             `json:"skip_reason"`
	Verdict         *string             `json:"verdict"`
	MaxRisk         *float64            `json:"max_risk"`
	Reasons         []string            `json:"reasons"`
	CheckedAgainst  json.RawMessage     `json:"checked_against"`
	Timings         Timings             `json:"timings"`
	DecisionModel   *TraceDecisionModel `json:"decision_model"`
	Audit           TraceAudit          `json:"audit"`
	QuestionPreview *string             `json:"question_preview"`
	ResponsePreview *string             `json:"response_preview"`
	Redacted        bool                `json:"redacted"`
	Flags           []string            `json:"flags"`
}

// Redact returns the summary as a caller without the content scope sees it: previews are null.
func (s TraceSummary) Redact() TraceSummary {
	s.QuestionPreview, s.ResponsePreview, s.Redacted = nil, nil, true
	return s
}

// Body unavailable reasons (TraceDetail.body.unavailable_reason).
const (
	BodyExpired   = "expired"
	BodyNotStored = "not_stored"
	BodyScope     = "scope"
)

// TraceBodyView is TraceDetail.body.
type TraceBodyView struct {
	Available          bool              `json:"available"`
	UnavailableReason  *string           `json:"unavailable_reason"`
	Messages           json.RawMessage   `json:"messages"`
	UserRequest        *string           `json:"user_request"`
	SystemInstructions *string           `json:"system_instructions"`
	ResponseText       *string           `json:"response_text"`
	FinalResponseText  *string           `json:"final_response_text"`
	Evidence           json.RawMessage   `json:"evidence"`
	Rules              map[string]string `json:"rules"`
	Truncated          bool              `json:"truncated"`
}

// UnavailableBody is a body that cannot be shown, for reason.
func UnavailableBody(reason string) TraceBodyView {
	return TraceBodyView{
		UnavailableReason: &reason, Messages: json.RawMessage("[]"), Evidence: json.RawMessage("[]"),
		Rules: map[string]string{},
	}
}

// LabelView is one human label on a trace (labels arrive with migration 0003).
type LabelView struct {
	Target    string    `json:"target"`
	Label     string    `json:"label"`
	LabeledBy string    `json:"labeled_by"`
	CreatedAt time.Time `json:"created_at"`
	Note      *string   `json:"note"`
}

// TraceDetail is GET /api/traces/:id (api.ts TraceDetail).
type TraceDetail struct {
	TraceSummary
	Body TraceBodyView `json:"body"`
	// Decisions are the decision/v1 documents (GateDecisionView) by attempt, as recorded.
	Decisions   []json.RawMessage `json:"decisions"`
	AuditDetail *AuditDetail      `json:"audit_detail"`
	Labels      []LabelView       `json:"labels"`
}

// AuditScores is api.ts AuditScores.
type AuditScores struct {
	TrustScore        *float64 `json:"trust_score"`
	Faithfulness      *float64 `json:"faithfulness"`
	ContradictionRate *float64 `json:"contradiction_rate"`
	UnverifiedRate    *float64 `json:"unverified_rate"`
	Coverage          *float64 `json:"coverage"`
	AccuracyAddressed *float64 `json:"accuracy_addressed"`
	MaxContradiction  *float64 `json:"max_contradiction"`
	PHallucinated     *float64 `json:"p_hallucinated"`
	ClaimsTotal       int      `json:"claims_total"`
	ClaimsDecided     int      `json:"claims_decided"`
	ClaimsUnknown     int      `json:"claims_unknown"`
}

// AuditGate is AuditDetail.gate: the gate decision the audit ran behind.
type AuditGate struct {
	Action      string   `json:"action"`
	WouldAction *string  `json:"would_action"`
	Verdict     *string  `json:"verdict"`
	MaxRisk     *float64 `json:"max_risk"`
	Delivered   string   `json:"delivered"`
}

// AuditError is AuditDetail.error.
type AuditError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// ClaimSpan is ClaimView.span: where the claim sits in the answer (UTF-16, half-open).
type ClaimSpan struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Method string `json:"method"`
}

// EvidenceSpan is where a claim's support sits in an evidence document.
type EvidenceSpan struct {
	DocIndex int    `json:"doc_index"`
	DocID    string `json:"doc_id"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Text     string `json:"text"`
	Kind     string `json:"kind"`
}

// ClaimView is one verified claim (api.ts ClaimView).
type ClaimView struct {
	Index                int                `json:"index"`
	Claim                *string            `json:"claim"`
	Status               string             `json:"status"`
	Confidence           float64            `json:"confidence"`
	Evidence             []string           `json:"evidence"`
	Reasoning            *string            `json:"reasoning"`
	DecidedBy            string             `json:"decided_by"`
	VerdictProbabilities map[string]float64 `json:"verdict_probabilities"`
	ProbabilitySource    string             `json:"probability_source"`
	Span                 *ClaimSpan         `json:"span"`
	Quote                *string            `json:"quote"`
	EvidenceSpans        []EvidenceSpan     `json:"evidence_spans"`
}

// AuditSummary is one row of the audit list (api.ts AuditSummary).
type AuditSummary struct {
	AuditID               string          `json:"audit_id"`
	TraceID               string          `json:"trace_id"`
	CreatedAt             time.Time       `json:"created_at"`
	Route                 *string         `json:"route"`
	Source                string          `json:"source"`
	Model                 *string         `json:"model"`
	Status                string          `json:"status"`
	Verdict               *string         `json:"verdict"`
	NotAssessedReason     *string         `json:"not_assessed_reason"`
	TrustScore            *float64        `json:"trust_score"`
	Grade                 *string         `json:"grade"`
	HallucinationDetected bool            `json:"hallucination_detected"`
	VerdictCounts         VerdictCounts   `json:"verdict_counts"`
	ProcessingMs          int64           `json:"processing_ms"`
	Judge                 json.RawMessage `json:"judge"`
	GateAction            *string         `json:"gate_action"`
	Selection             *string         `json:"selection"`
	InclusionProb         *float64        `json:"inclusion_prob"`
	QueryPreview          *string         `json:"query_preview"`
	Redacted              bool            `json:"redacted"`
}

// AuditDetail is one audit with its claims (api.ts AuditDetail).
type AuditDetail struct {
	AuditSummary
	UserQuery         *string          `json:"user_query"`
	LLMResponse       *string          `json:"llm_response"`
	Claims            []ClaimView      `json:"claims"`
	Scores            *AuditScores     `json:"scores"`
	DegradedReasons   []string         `json:"degraded_reasons"`
	Error             *AuditError      `json:"error"`
	ReasoningTrace    string           `json:"reasoning_trace"`
	StepTimings       map[string]int64 `json:"step_timings"`
	Gate              *AuditGate       `json:"gate"`
	EvidenceSummary   json.RawMessage  `json:"evidence_summary"`
	ContextUsed       json.RawMessage  `json:"context_used"`
	UnsupportedPolicy *string          `json:"unsupported_policy"`
	ScoringVersion    string           `json:"scoring_version"`
	Costs             []CostRecord     `json:"costs"`
}

// CostRecord is api.ts CostRecord: one model call and what it cost (SPEC §7.4). cost_source says
// how the figure was obtained: reported by the vendor, computed from the price table, free_local,
// simulated (mock backends) or unknown.
type CostRecord struct {
	Component    string  `json:"component"`
	Kind         string  `json:"kind"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	CostSource   string  `json:"cost_source"`
	LatencyMs    int64   `json:"latency_ms"`
	Status       string  `json:"status"`
	Questions    int     `json:"questions"`
}

// Redact returns the summary without prompt text.
func (s AuditSummary) Redact() AuditSummary {
	s.QueryPreview, s.Redacted = nil, true
	return s
}

// Redact returns the audit as a caller without the content scope sees it: prompt, answer,
// claim text, quotes, evidence and reasoning are null or empty.
func (d AuditDetail) Redact() AuditDetail {
	d.AuditSummary = d.AuditSummary.Redact()
	d.UserQuery, d.LLMResponse = nil, nil
	d.ReasoningTrace = ""
	d.ContextUsed = json.RawMessage("[]")
	claims := make([]ClaimView, len(d.Claims))
	for i, c := range d.Claims {
		c.Claim, c.Reasoning, c.Quote = nil, nil, nil
		c.Evidence, c.EvidenceSpans = []string{}, []EvidenceSpan{}
		claims[i] = c
	}
	d.Claims = claims
	if d.Error != nil {
		d.Error = &AuditError{Kind: d.Error.Kind, Message: ""}
	}
	return d
}

// WithoutContext returns the audit without context_used (the WS replay ring keeps summaries).
func (d AuditDetail) WithoutContext() AuditDetail {
	d.ContextUsed = json.RawMessage("[]")
	return d
}

// Redact returns the detail as a caller without the content scope sees it (SPEC §2.4): text
// fields are null, the body is unavailable with reason scope, and detector findings lose their
// text.
func (d TraceDetail) Redact() TraceDetail {
	d.TraceSummary = d.TraceSummary.Redact()
	d.Body = UnavailableBody(BodyScope)
	decisions := make([]json.RawMessage, len(d.Decisions))
	for i, raw := range d.Decisions {
		decisions[i] = RedactDecision(raw)
	}
	d.Decisions = decisions
	if d.AuditDetail != nil {
		a := d.AuditDetail.Redact()
		d.AuditDetail = &a
	}
	if d.Labels != nil {
		// A label note is free text that can quote the answer (SPEC §2.1: text needs content).
		labels := make([]LabelView, len(d.Labels))
		for i, l := range d.Labels {
			l.Note = nil
			labels[i] = l
		}
		d.Labels = labels
	}
	return d
}

// RedactDecision blanks the finding texts of a decision/v1 document: they quote the answer
// (numbers, URLs, quotations, secrets). A document that does not parse is returned unchanged.
func RedactDecision(raw json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	var detectors []map[string]json.RawMessage
	if err := json.Unmarshal(doc["detectors"], &detectors); err != nil {
		return raw
	}
	for _, det := range detectors {
		var findings []map[string]json.RawMessage
		if err := json.Unmarshal(det["findings"], &findings); err != nil {
			continue
		}
		for _, f := range findings {
			f["text"] = json.RawMessage(`""`)
		}
		if b, err := json.Marshal(findings); err == nil {
			det["findings"] = b
		}
	}
	b, err := json.Marshal(detectors)
	if err != nil {
		return raw
	}
	doc["detectors"] = b
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// Preview returns the first n runes of s, or nil for an empty s.
func Preview(s string, n int) *string {
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > n {
		i, count := 0, 0
		for i = range s {
			if count == n {
				break
			}
			count++
		}
		s = s[:i]
	}
	return &s
}

// Float64From32 converts a float32 (a REAL column or a proto float) to the float64 with the same
// shortest decimal form, so 0.06 stays 0.06 instead of 0.05999999865889549.
func Float64From32(f float32) float64 {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return float64(f)
	}
	v, err := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
	if err != nil {
		return float64(f)
	}
	return v
}

func f32ptr(p *float32) *float64 {
	if p == nil {
		return nil
	}
	v := Float64From32(*p)
	return &v
}

func utc(t time.Time) time.Time { return t.UTC() }
