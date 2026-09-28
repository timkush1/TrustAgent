package decision

// String enums of decide-input.v1 and decision.v1 (contracts/schema/common.v1.schema.json).

// escalation.on_error values.
const (
	OnErrorEscalate = "escalate"
	OnErrorIgnore   = "ignore"
)

// Policy modes (PolicyMode) and actions (GateAction, WouldAction).
const (
	ModeObserve  = "observe"
	ModeFlag     = "flag"
	ModeBlock    = "block"
	ModeFallback = "fallback"

	ActionObserve  = "observe"
	ActionPass     = "pass"
	ActionFlag     = "flag"
	ActionBlock    = "block"
	ActionFallback = "fallback"
)

// Fail modes (FailMode).
const (
	FailOpen   = "open"
	FailClosed = "closed"
)

// on_no_evidence values.
const (
	OnNoEvidenceIgnore = "ignore"
	OnNoEvidenceFlag   = "flag"
	OnNoEvidenceBlock  = "block"
)

// Evaluator stages (EvaluatorStage).
const (
	StageActive = "active"
	StageShadow = "shadow"
)

// Threshold spaces (ThresholdSpace).
const (
	SpaceRaw        = "raw"
	SpaceCalibrated = "calibrated"
)

// Calibration qualities (CalibrationQuality) and the "none" state (CalibrationState).
const (
	QualityMeasured = "measured"
	QualityMock     = "mock"
	CalibrationNone = "none"
)

// Decision backends that decide() distinguishes (DecisionBackend).
const BackendMock = "mock"

// Stream handling of the gated answer (decide-input meta.stream).
const StreamPassthrough = "passthrough"

// Gate statuses (GateStatus).
const (
	StatusOK          = "ok"
	StatusPartial     = "partial"
	StatusNotAssessed = "not_assessed"
	StatusSkipped     = "skipped"
	StatusTimeout     = "timeout"
	StatusError       = "error"
)

// Request statuses (decide-input meta.request_status); failures are "timeout" or "error:<kind>".
const (
	RequestOK      = "ok"
	RequestTimeout = "timeout"
)

// Evaluator result statuses (EvaluatorResultStatus).
const (
	EvalAssessed    = "assessed"
	EvalNotAssessed = "not_assessed"
	EvalError       = "error"
)

// Evaluator levels (EvaluatorLevel).
const (
	LevelOK       = "ok"
	LevelWarn     = "warn"
	LevelCritical = "critical"
)

// Gate verdicts (GateVerdict).
const (
	VerdictSupported    = "supported"
	VerdictUnsupported  = "unsupported"
	VerdictContradicted = "contradicted"
	VerdictNotAssessed  = "not_assessed"
	VerdictUnknown      = "unknown"
)

// Escalation reasons (EvaluatorResult.escalate).
const (
	EscalateUncertain     = "uncertain"
	EscalateNumericOrDate = "numeric_or_date_unverified"
	EscalateError         = "error"
)

// Level caps (EvaluatorResult.capped).
const (
	CappedKnowledgeOnly     = "knowledge_only"
	CappedEvidenceTruncated = "evidence_truncated"
)

// Confidence sources (EvaluatorResult.confidence_source; display only).
const (
	ConfidenceVendor          = "vendor"
	ConfidenceFallbackTopProb = "fallback_top_prob"
	ConfidenceChoiceOnly      = "choice_only"
)

// Normalization errors (EvaluatorResult.reason) and anomalies.
const (
	ErrMissingAnswer        = "missing_answer"
	ErrUnexpectedOption     = "unexpected_option"
	ErrInvalidDistribution  = "invalid_distribution"
	ErrProbabilitiesMissing = "probabilities_missing"
	AnomalySumOutOfRange    = "sum_out_of_range"
)

// Skip reasons that decide() treats specially (SkipReason).
const SkipSampledOut = "sampled_out"

// Calibration summaries (decision/v1 calibration).
const (
	CalibrationAll     = "all"
	CalibrationPartial = "partial"
)

// Fixed reasons (decision/v1 reasons), besides "<id>:<level>" and "det:<id>".
const (
	ReasonDisagreement         = "disagreement"
	ReasonNeedsSecondSignal    = "needs_second_signal"
	ReasonNoEvidence           = "no_evidence"
	ReasonStreamNotBuffered    = "stream_not_buffered"
	ReasonFailClosed           = "fail_closed"
	ReasonUncalibratedSnapshot = "uncalibrated_snapshot"
)

// Exclusion reasons (ExclusionReason): why compile() did not ask an evaluator.
const (
	ExcludedTooLong            = "too_long"
	ExcludedNoEvidence         = "no_evidence"
	ExcludedEvidenceTooShort   = "evidence_too_short"
	ExcludedEvidencePackedAway = "evidence_packed_away"
)

// Deep-audit classes (DeepAuditClass).
const (
	DeepAuditHigh        = "high"
	DeepAuditSample      = "sample"
	DeepAuditSampleError = "sample_error"
	DeepAuditNone        = "none"
)

// Degradations (Degraded).
const (
	DegradedUncalibrated = "uncalibrated"
	DegradedBudget       = "budget"
	DegradedBreaker      = "breaker"
	DegradedRateLimited  = "rate_limited"
	DegradedTimeout      = "timeout"
	DegradedError        = "error"
	DegradedOverload     = "overload"
	DegradedOff          = "off"
)

// Detector ids (DetectorId).
const (
	DetNumbers    = "numbers_not_in_evidence"
	DetDates      = "dates_not_in_evidence"
	DetURLs       = "urls_not_in_evidence"
	DetQuotes     = "quotes_not_in_evidence"
	DetCitation   = "citation_out_of_range"
	DetSecret     = "secret_pattern"
	DetPromptLeak = "system_prompt_leak"
)

// groundingDetectors are the detectors whose firing is a grounding trigger (GROUNDING_DETS).
var groundingDetectors = map[string]bool{
	DetNumbers: true, DetDates: true, DetURLs: true, DetQuotes: true, DetCitation: true,
}

// FactualClaimsID is the internal evaluator compile() adds when on_no_evidence is set and a
// required evaluator had no evidence (SPEC §2.9 compile step 3).
const (
	FactualClaimsID  = "factual_claims"
	FactualClaimsRef = "factual_claims@1"
)
