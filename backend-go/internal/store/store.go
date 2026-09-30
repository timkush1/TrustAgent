// Package store persists traces, gate decisions, trace bodies and audits in
// Postgres (SPEC §2.3). Go is the only writer: the gate sink writes traces and
// decisions, the worker pool writes audits before it broadcasts them. The
// readers back the /api/traces and /api/audits endpoints and build their wire
// views (views.go).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// ErrDuplicate is returned by SaveAudit when an audit with the same audit_id
// already exists. Nothing is written in that case: in particular no claims are
// appended to the existing audit (B4).
var ErrDuplicate = errors.New("store: duplicate audit_id")

// clientRequestIDPattern is the only shape of client-supplied X-Request-ID that
// is persisted (SPEC §2.1 IDs).
var clientRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// NewID returns a server-generated identifier for trace_id and audit_id: a
// UUIDv7 in its canonical lowercase, hyphenated form. UUIDv7 ids sort by
// creation time, which keeps keyset pagination and index locality cheap.
func NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}

// ClientRequestID applies the B4 rule to a client-supplied X-Request-ID: it is
// storable (as the non-unique client_request_id, never as a key) only when it
// matches ^[A-Za-z0-9._:-]{1,128}$. It returns the id and true when it may be
// stored, or "" and false when it must be dropped; callers log a warning for a
// non-empty id that is dropped.
func ClientRequestID(raw string) (string, bool) {
	if !clientRequestIDPattern.MatchString(raw) {
		return "", false
	}
	return raw, true
}

// AuditRecord is one audits row with its claim verifications.
//
// Columns that migration 0002 made nullable (score, grade and the texts that
// retention or store_bodies=none may null out) are pointers: nil round-trips as
// SQL NULL, so an audit that was never scored is not reported as a zero score.
// Legacy (v1) rows read back with the 0002 column defaults: source proxy,
// status completed, scoring_version v1 and no verdict.
type AuditRecord struct {
	// AuditID is the server-generated UUIDv7 primary key. Legacy rows keep
	// their original id.
	AuditID string
	// TraceID links the audit to its gateway trace. SaveAudit defaults it to
	// AuditID, the same rule 0002 applies to legacy rows.
	TraceID string
	// RequestID is the legacy request identifier. For new audits it equals
	// TraceID (the gRPC request_id); SaveAudit defaults it that way.
	RequestID string
	// ClientRequestID is the validated, non-unique client X-Request-ID, if any.
	ClientRequestID       *string
	UserQuery             *string
	LLMResponse           *string
	Model                 string
	FaithfulnessScore     *float64
	Grade                 *string
	HallucinationDetected bool
	ReasoningTrace        string
	ProcessingTimeMs      int64
	StepTimings           map[string]int64
	CreatedAt             time.Time
	Claims                []ClaimRecord

	// Source is the TraceSource (default proxy).
	Source    string
	Route     *string
	APIFormat *string
	// Priority is the lane the audit ran in (high or low).
	Priority      *string
	Selection     *string
	InclusionProb *float64
	// Status is the AuditStatus (default completed).
	Status            string
	Verdict           *string
	NotAssessedReason *string
	Error             *string
	ErrorKind         *string
	Scores            *AuditScores
	DegradedReasons   []string
	// Judge is the JudgeInfo JSON, or nil.
	Judge json.RawMessage
	Gate  *AuditGate
	// EvidenceSummary is the EvidenceSummary JSON of the request, or nil.
	EvidenceSummary json.RawMessage
	// ContextUsed is the EvidenceItem[] JSON the audit checked against, or nil.
	ContextUsed       json.RawMessage
	UnsupportedPolicy *string
	// ScoringVersion is v1 for legacy rows and v2 for Evaluate results.
	ScoringVersion string
	CostUSD        *float64
	// Costs are the audit's model calls (AuditResult.costs); they are stored in cost_events.
	Costs       []CostRecord
	QueueWaitMs *int64
	CompletedAt *time.Time
	// TextExpiresAt is when retention nulls the prompt, answer and context.
	TextExpiresAt *time.Time

	// Counts are the claim verdict counts; the readers fill them.
	Counts VerdictCounts
}

// ClaimRecord is one verified claim belonging to an audit. Claim is nil when
// the text was not stored (store_bodies=none keeps verdicts, not text).
type ClaimRecord struct {
	Claim                *string
	Status               string
	Confidence           float64
	Evidence             []string
	Reasoning            *string
	DecidedBy            *string
	VerdictProbabilities map[string]float64
	ProbabilitySource    *string
	// SpanStart and SpanEnd locate the claim in the answer (UTF-16, half-open); nil when
	// unaligned.
	SpanStart     *int
	SpanEnd       *int
	SpanMethod    *string
	Quote         *string
	EvidenceSpans []EvidenceSpan
}

// AuditFilter selects and pages GET /api/audits. Empty strings and nil times match everything.
type AuditFilter struct {
	Limit          int
	Cursor         string
	Since          *time.Time
	CompletedSince *time.Time
	Route          string
	Source         string
	Status         string
	Verdict        string
	Selection      string
}

// Store is the persistence interface; PostgresStore is the implementation.
type Store interface {
	// SaveAudit inserts a new audit and its claims atomically. It returns
	// ErrDuplicate, without writing anything, when record.AuditID exists.
	SaveAudit(ctx context.Context, record *AuditRecord) error
	// SaveAuditResult saves an audit (as SaveAudit) and sets its trace's audit
	// status in the same transaction.
	SaveAuditResult(ctx context.Context, record *AuditRecord, traceAuditStatus string) error
	// ListAudits returns a keyset page of audits, newest first, and the next cursor.
	ListAudits(ctx context.Context, filter AuditFilter) ([]AuditRecord, string, error)
	// GetAudit returns ErrNotFound when no audit has the given id.
	GetAudit(ctx context.Context, auditID string) (*AuditRecord, error)
	// LatestAuditForTrace returns the newest audit of a trace, or ErrNotFound.
	LatestAuditForTrace(ctx context.Context, traceID string) (*AuditRecord, error)

	StartTrace(ctx context.Context, t *TraceRecord) error
	RecordDecision(ctx context.Context, t *TraceRecord, d *DecisionRecord, b *BodyRecord) error
	// RecordAttempt writes a later attempt's gate_decisions row (the fallback re-gate) only.
	RecordAttempt(ctx context.Context, d *DecisionRecord) error
	SetTraceAudit(ctx context.Context, traceID string, u TraceAuditUpdate) error
	ListTraces(ctx context.Context, f TraceFilter) ([]TraceRecord, string, error)
	// GetTrace, GetTraceBody: ErrNotFound when absent.
	GetTrace(ctx context.Context, traceID string) (*TraceRecord, error)
	GetTraceBody(ctx context.Context, traceID string) (*BodyRecord, error)
	TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error)

	// Ping checks the connection; Migration names the newest applied migration.
	Ping(ctx context.Context) error
	Migration(ctx context.Context) (string, error)
	Close()
}

// GradeForScore maps a faithfulness score onto the A-F scale of v1 audits. v2
// audits persist the engine's wire grade instead (SPEC §2.2).
func GradeForScore(score float64) string {
	switch {
	case score >= 0.9:
		return "A"
	case score >= 0.7:
		return "B"
	case score >= 0.5:
		return "C"
	case score >= 0.3:
		return "D"
	default:
		return "F"
	}
}
