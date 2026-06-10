// Package store persists completed audits. The single writer is the worker
// pool (it holds the complete result before broadcasting); readers are the
// /api/audits endpoints.
package store

import (
	"context"
	"time"
)

// AuditRecord is one persisted audit with its claim verifications.
type AuditRecord struct {
	AuditID               string           `json:"audit_id"`
	RequestID             string           `json:"request_id"`
	UserQuery             string           `json:"user_query"`
	LLMResponse           string           `json:"llm_response"`
	Model                 string           `json:"model,omitempty"`
	FaithfulnessScore     float64          `json:"faithfulness_score"`
	Grade                 string           `json:"grade"`
	HallucinationDetected bool             `json:"hallucination_detected"`
	ReasoningTrace        string           `json:"reasoning_trace,omitempty"`
	ProcessingTimeMs      int64            `json:"processing_time_ms"`
	StepTimings           map[string]int64 `json:"step_timings,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	Claims                []ClaimRecord    `json:"claims"`
}

// ClaimRecord is one verified claim belonging to an audit.
type ClaimRecord struct {
	Claim      string   `json:"claim"`
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
	SaveAudit(ctx context.Context, record *AuditRecord) error
	ListAudits(ctx context.Context, filter ListFilter) ([]AuditRecord, int, error)
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
