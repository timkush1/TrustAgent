// Package store persists completed audits. The single writer is the worker
// pool (it holds the complete result before broadcasting); readers are the
// /api/audits endpoints.
package store

import (
	"context"
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

// AuditRecord is one persisted audit with its claim verifications.
//
// Columns that migration 0002 made nullable (score, grade and the texts that
// retention or store_bodies=none may null out) are pointers: nil round-trips as
// SQL NULL and JSON null, so an audit that was never scored is not reported as
// a zero score.
type AuditRecord struct {
	// AuditID is the server-generated UUIDv7 primary key. Legacy rows keep
	// their original id.
	AuditID string `json:"audit_id"`
	// TraceID links the audit to its gateway trace. SaveAudit defaults it to
	// AuditID, the same rule 0002 applies to legacy rows.
	TraceID string `json:"trace_id"`
	// RequestID is the legacy request identifier. For new audits it equals
	// TraceID (the gRPC request_id); SaveAudit defaults it that way.
	RequestID string `json:"request_id"`
	// ClientRequestID is the validated, non-unique client X-Request-ID, if any.
	ClientRequestID       *string          `json:"client_request_id"`
	UserQuery             *string          `json:"user_query"`
	LLMResponse           *string          `json:"llm_response"`
	Model                 string           `json:"model,omitempty"`
	FaithfulnessScore     *float64         `json:"faithfulness_score"`
	Grade                 *string          `json:"grade"`
	HallucinationDetected bool             `json:"hallucination_detected"`
	ReasoningTrace        string           `json:"reasoning_trace,omitempty"`
	ProcessingTimeMs      int64            `json:"processing_time_ms"`
	StepTimings           map[string]int64 `json:"step_timings,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	Claims                []ClaimRecord    `json:"claims"`
}

// ClaimRecord is one verified claim belonging to an audit. Claim is nil when
// the text was not stored (store_bodies=none keeps verdicts, not text).
type ClaimRecord struct {
	Claim      *string  `json:"claim"`
	Status     string   `json:"status"`
	Confidence float64  `json:"confidence"`
	Evidence   []string `json:"evidence"`
}

// ListFilter narrows and pages ListAudits results.
type ListFilter struct {
	Limit   int
	Offset  int
	Grade   string // "" = any
	Flagged *bool  // nil = any
}

// Store is the persistence interface; PostgresStore is the implementation.
type Store interface {
	// SaveAudit inserts a new audit and its claims atomically. It returns
	// ErrDuplicate, without writing anything, when record.AuditID exists.
	SaveAudit(ctx context.Context, record *AuditRecord) error
	ListAudits(ctx context.Context, filter ListFilter) ([]AuditRecord, int, error)
	// GetAudit returns pgx.ErrNoRows when no audit has the given id.
	GetAudit(ctx context.Context, auditID string) (*AuditRecord, error)
	Close()
}

// GradeForScore maps a faithfulness score onto the A-F scale used across the
// dashboard and the Python engine.
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
