package worker

import (
	"encoding/json"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

// audit_failed kinds (api.ts WsEvent audit_failed).
const (
	KindEngineUnavailable = "engine_unavailable"
	KindTimeout           = "timeout"
	KindJudgeUnavailable  = "judge_unavailable"
	KindDecomposerFailed  = "decomposer_failed"
	KindInternal          = "internal"
)

var auditStatusNames = map[pb.AuditStatus]string{
	pb.AuditStatus_AUDIT_STATUS_COMPLETED: store.AuditCompleted,
	pb.AuditStatus_AUDIT_STATUS_DEGRADED:  store.AuditDegraded,
	pb.AuditStatus_AUDIT_STATUS_FAILED:    store.AuditFailed,
}

var verdictNames = map[pb.AuditVerdict]string{
	pb.AuditVerdict_AUDIT_VERDICT_GROUNDED:     store.VerdictGrounded,
	pb.AuditVerdict_AUDIT_VERDICT_HALLUCINATED: store.VerdictHallucinated,
	pb.AuditVerdict_AUDIT_VERDICT_UNVERIFIABLE: store.VerdictUnverifiable,
	pb.AuditVerdict_AUDIT_VERDICT_NO_CLAIMS:    store.VerdictNoClaims,
	pb.AuditVerdict_AUDIT_VERDICT_NOT_ASSESSED: store.VerdictNotAssessed,
}

// claimStatusNames maps VerificationStatus onto ClaimVerdict. UNSPECIFIED (a judge or parse
// failure on an older engine) reads as UNKNOWN; CONTRADICTED stays distinct from UNSUPPORTED.
var claimStatusNames = map[pb.VerificationStatus]string{
	pb.VerificationStatus_VERIFICATION_STATUS_SUPPORTED:           "SUPPORTED",
	pb.VerificationStatus_VERIFICATION_STATUS_PARTIALLY_SUPPORTED: "PARTIALLY_SUPPORTED",
	pb.VerificationStatus_VERIFICATION_STATUS_CONTRADICTED:        "CONTRADICTED",
	pb.VerificationStatus_VERIFICATION_STATUS_UNSUPPORTED:         "UNSUPPORTED",
	pb.VerificationStatus_VERIFICATION_STATUS_UNKNOWN:             "UNKNOWN",
	pb.VerificationStatus_VERIFICATION_STATUS_NOT_CHECKED:         "NOT_CHECKED",
}

var gradeNames = map[pb.TrustGrade]string{
	pb.TrustGrade_TRUST_GRADE_A: "A", pb.TrustGrade_TRUST_GRADE_B: "B", pb.TrustGrade_TRUST_GRADE_C: "C",
	pb.TrustGrade_TRUST_GRADE_D: "D", pb.TrustGrade_TRUST_GRADE_F: "F",
}

// baseRecord is the part of a stored audit that comes from the job, which a worker took at
// started and finished at finished.
func baseRecord(job *Job, lane string, started, finished time.Time, retention time.Duration) *store.AuditRecord {
	req := job.Request
	query, response := req.GetQuery(), req.GetResponse()
	lanePtr, selection := lane, job.Selection
	incl := job.InclusionProb
	queueWait := max(started.Sub(job.enqueued).Milliseconds(), 0)
	completed := finished.UTC()
	rec := &store.AuditRecord{
		AuditID: req.GetAuditId(), TraceID: req.GetTraceId(), RequestID: req.GetTraceId(),
		ClientRequestID: job.ClientRequestID, UserQuery: &query, LLMResponse: &response, Model: req.GetModel(),
		CreatedAt: job.enqueued.UTC(), Source: req.GetSource(), Route: optional(req.GetRoute()),
		APIFormat: optional(req.GetProvider()), Priority: &lanePtr, Selection: optional(selection),
		Gate: job.Gate, EvidenceSummary: job.EvidenceSummary, ContextUsed: job.ContextUsed,
		QueueWaitMs: &queueWait, CompletedAt: &completed, StepTimings: map[string]int64{}, DegradedReasons: []string{},
	}
	if job.Selection != "" {
		rec.InclusionProb = &incl
	}
	if retention > 0 {
		expires := finished.Add(retention).UTC()
		rec.TextExpiresAt = &expires
	}
	return rec
}

// resultRecord maps an Evaluate result onto the stored audit. The wire grade, verdict and status
// are persisted as they are (SPEC §2.2).
func resultRecord(job *Job, lane string, res *pb.AuditResult, started, finished time.Time, retention time.Duration) *store.AuditRecord {
	rec := baseRecord(job, lane, started, finished, retention)
	rec.Status = auditStatusNames[res.GetStatus()]
	if rec.Status == "" {
		rec.Status = store.AuditFailed
		rec.ErrorKind, rec.Error = optional(KindInternal), optional("The audit engine returned no audit status.")
	}
	if v, ok := verdictNames[res.GetVerdict()]; ok {
		rec.Verdict = &v
	}
	if g, ok := gradeNames[res.GetGrade()]; ok {
		rec.Grade = &g
	}
	rec.HallucinationDetected = res.GetHallucinationDetected()
	rec.ReasoningTrace = res.GetReasoningTrace()
	rec.ProcessingTimeMs = res.GetProcessingTimeMs()
	for k, v := range res.GetStepTimings() {
		rec.StepTimings[k] = v
	}
	rec.NotAssessedReason = optional(res.GetNotAssessedReason())
	if rec.Error == nil {
		rec.Error, rec.ErrorKind = optional(res.GetError()), optional(res.GetErrorKind())
	}
	if rec.Status == store.AuditFailed && rec.ErrorKind == nil {
		rec.ErrorKind = optional(KindInternal)
	}
	rec.DegradedReasons = append(rec.DegradedReasons, res.GetDegradedReasons()...)
	if s := res.GetScores(); s != nil && rec.Status != store.AuditFailed && rec.Verdict != nil && *rec.Verdict != store.VerdictNotAssessed {
		rec.Scores = &store.AuditScores{
			TrustScore: f64(s.TrustScore), Faithfulness: f64(s.Faithfulness), ContradictionRate: f64(s.ContradictionRate),
			UnverifiedRate: f64(s.UnverifiedRate), Coverage: f64(s.Coverage), AccuracyAddressed: f64(s.AccuracyAddressed),
			MaxContradiction: f64(s.MaxContradiction), PHallucinated: f64(s.PHallucinated),
			ClaimsTotal: int(s.GetClaimsTotal()), ClaimsDecided: int(s.GetClaimsDecided()), ClaimsUnknown: int(s.GetClaimsUnknown()),
		}
		rec.FaithfulnessScore = rec.Scores.TrustScore
	}
	if j := res.GetJudge(); j != nil {
		rec.Judge = judgeJSON(j)
	}
	if p := res.GetUnsupportedPolicy(); p == "hallucination" || p == "unverifiable" {
		rec.UnsupportedPolicy = &p
	}
	rec.ScoringVersion = res.GetScoringVersion()
	if rec.ScoringVersion == "" {
		rec.ScoringVersion = "v2"
	}
	var cost float64
	for _, c := range res.GetCosts() {
		cost += c.GetCostUsd()
		rec.Costs = append(rec.Costs, store.CostRecord{
			Component: c.GetComponent(), Kind: c.GetKind(), Provider: c.GetProvider(), Model: c.GetModel(),
			InputTokens: c.GetInputTokens(), OutputTokens: c.GetOutputTokens(), CostUSD: c.GetCostUsd(),
			CostSource: c.GetCostSource(), LatencyMs: c.GetLatencyMs(), Status: c.GetStatus(), Questions: int(c.GetQuestions()),
		})
	}
	if len(res.GetCosts()) > 0 {
		rec.CostUSD = &cost
	}
	for _, c := range res.GetClaims() {
		rec.Claims = append(rec.Claims, claimRecord(c))
	}
	return rec
}

func claimRecord(c *pb.ClaimVerification) store.ClaimRecord {
	text := c.GetClaim()
	st, ok := claimStatusNames[c.GetStatus()]
	if !ok {
		st = "UNKNOWN"
	}
	rec := store.ClaimRecord{
		Claim: &text, Status: st, Confidence: store.Float64From32(c.GetConfidence()), Evidence: c.GetEvidence(),
		Reasoning: optional(c.GetReasoning()), DecidedBy: optional(c.GetDecidedBy()),
		ProbabilitySource: optional(c.GetProbabilitySource()), Quote: optional(c.GetQuote()),
		SpanMethod: optional(c.GetSpanMethod()),
	}
	if rec.Evidence == nil {
		rec.Evidence = []string{}
	}
	if probs := c.GetVerdictProbabilities(); len(probs) > 0 {
		rec.VerdictProbabilities = make(map[string]float64, len(probs))
		for k, v := range probs {
			rec.VerdictProbabilities[k] = store.Float64From32(v)
		}
	}
	// -1 marks an unaligned span; so does a proto default (0,0) without a span method.
	if start, end := int(c.GetSpanStart()), int(c.GetSpanEnd()); start >= 0 && end > start {
		rec.SpanStart, rec.SpanEnd = &start, &end
	}
	for _, s := range c.GetEvidenceSpans() {
		kind := s.GetKind()
		if !slices.Contains([]string{"quote_verified", "quote_unverified", "retrieved"}, kind) {
			kind = "retrieved"
		}
		rec.EvidenceSpans = append(rec.EvidenceSpans, store.EvidenceSpan{
			DocIndex: int(s.GetDocIndex()), DocID: s.GetDocId(), Start: int(s.GetStart()), End: int(s.GetEnd()),
			Text: s.GetText(), Kind: kind,
		})
	}
	return rec
}

// failedRecord is the stored audit of an engine call that did not return a result.
func failedRecord(job *Job, lane, kind, message string, started, finished time.Time, retention time.Duration) *store.AuditRecord {
	rec := baseRecord(job, lane, started, finished, retention)
	rec.Status, rec.ErrorKind, rec.Error = store.AuditFailed, &kind, &message
	rec.ProcessingTimeMs = finished.Sub(started).Milliseconds()
	rec.ScoringVersion = "v2"
	rec.ReasoningTrace = "Audit failed (" + kind + "): " + message
	return rec
}

// traceAuditStatus is the trace's audit status for a stored audit.
func traceAuditStatus(rec *store.AuditRecord) string {
	switch {
	case rec.Status == store.AuditFailed:
		return store.AuditFailed
	case rec.Verdict != nil && *rec.Verdict == store.VerdictNotAssessed:
		return store.TraceAuditNotAssessed
	case rec.Status == store.AuditDegraded:
		return store.AuditDegraded
	default:
		return store.AuditCompleted
	}
}

// rpcFailureKind classifies an Evaluate error: the engine is down, too slow, or failed.
func rpcFailureKind(err error) string {
	switch status.Code(err) {
	case codes.Unavailable:
		return KindEngineUnavailable
	case codes.DeadlineExceeded:
		return KindTimeout
	default:
		return KindInternal
	}
}

// failedKind maps an audit's error_kind onto the audit_failed kinds.
func failedKind(errorKind string) string {
	switch errorKind {
	case KindEngineUnavailable, KindTimeout, KindJudgeUnavailable, KindDecomposerFailed:
		return errorKind
	case "no_judge":
		return KindJudgeUnavailable
	default:
		return KindInternal
	}
}

// CompletedEvent is the audit_completed event of a stored audit: the full audit, the redacted
// variant, and the ring variant without context_used, plus the v1 audit_result frame.
func CompletedEvent(rec *store.AuditRecord) (websocket.Event, error) {
	detail := rec.Detail()
	type payload struct {
		Audit store.AuditDetail `json:"audit"`
	}
	ev, err := websocket.NewEvent("audit_completed", rec.TraceID, payload{detail}, payload{detail.Redact()})
	if err != nil {
		return ev, err
	}
	if ev.Ring, err = json.Marshal(payload{detail.WithoutContext()}); err != nil {
		return ev, err
	}
	ev.V1Type = "audit_result"
	ev.V1, err = json.Marshal(v1Audit(rec))
	return ev, err
}

func failedEvent(rec *store.AuditRecord) (websocket.Event, error) {
	kind := failedKind(derefString(rec.ErrorKind))
	retryable := kind == KindEngineUnavailable || kind == KindTimeout || kind == KindJudgeUnavailable
	data := map[string]any{"audit_id": rec.AuditID, "kind": kind, "message": derefString(rec.Error), "retryable": retryable}
	redacted := map[string]any{"audit_id": rec.AuditID, "kind": kind, "message": "", "retryable": retryable}
	return websocket.NewEvent("audit_failed", rec.TraceID, data, redacted)
}

// queuedEvent is the audit_queued event of an audit Submit put in lane behind depth others.
func queuedEvent(traceID, auditID, lane string, depth int) (websocket.Event, error) {
	return websocket.NewEvent("audit_queued", traceID, map[string]any{"audit_id": auditID, "lane": lane, "queue_depth": depth}, nil)
}

// startedEvent is the audit_started event of an audit a worker took after waitMs in its lane.
func startedEvent(traceID, auditID string, waitMs int64) (websocket.Event, error) {
	return websocket.NewEvent("audit_started", traceID, map[string]any{"audit_id": auditID, "queue_wait_ms": waitMs}, nil)
}

// DroppedEvent is the audit_dropped event of an audit that was not run.
func DroppedEvent(traceID string, auditID *string, reason string) (websocket.Event, error) {
	return websocket.NewEvent("audit_dropped", traceID, map[string]any{"audit_id": auditID, "reason": reason}, nil)
}

// v1Audit is the audit in the v1 audit_result frame (the pre-v2 dashboard format).
func v1Audit(rec *store.AuditRecord) any {
	type claim struct {
		Claim      string   `json:"claim"`
		Status     string   `json:"status"`
		Confidence float64  `json:"confidence"`
		Evidence   []string `json:"evidence"`
	}
	claims := make([]claim, 0, len(rec.Claims))
	for _, c := range rec.Claims {
		claims = append(claims, claim{Claim: derefString(c.Claim), Status: c.Status, Confidence: c.Confidence, Evidence: c.Evidence})
	}
	var score float64
	if rec.FaithfulnessScore != nil {
		score = *rec.FaithfulnessScore
	}
	return map[string]any{
		"audit_id": rec.AuditID, "request_id": rec.TraceID, "user_query": derefString(rec.UserQuery),
		"llm_response": derefString(rec.LLMResponse), "faithfulness_score": score, "overall_score": score,
		"hallucination_detected": rec.HallucinationDetected, "claims": claims, "reasoning_trace": rec.ReasoningTrace,
		"processing_time_ms": rec.ProcessingTimeMs, "timestamp": rec.CreatedAt.UTC().Format(time.RFC3339),
		"provider": "proxy", "model": rec.Model, "step_timings": rec.StepTimings,
	}
}

// judgeJSON is api.ts JudgeInfo for a wire JudgeInfo.
func judgeJSON(j *pb.JudgeInfo) json.RawMessage {
	source := j.GetCredentialSource()
	if source != "env" && source != "dashboard" {
		source = "none"
	}
	b, err := json.Marshal(map[string]any{
		"configured_provider": j.GetConfiguredProvider(), "effective_provider": j.GetEffectiveProvider(),
		"model": j.GetModel(), "fallback_active": j.GetFallbackActive(), "fallback_reason": optional(j.GetFallbackReason()),
		"structured_outputs": j.GetStructuredOutputs(), "healthy": j.GetHealthy(), "credential_source": source,
	})
	if err != nil {
		return nil
	}
	return b
}

func f64(p *float32) *float64 {
	if p == nil {
		return nil
	}
	v := store.Float64From32(*p)
	return &v
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
