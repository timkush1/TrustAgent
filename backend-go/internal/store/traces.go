package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Traces, gate decisions and trace bodies (SPEC §2.3, migration 0002). Go is the only writer.
//
// The write methods run inline, each in its own transaction. With TRUTHTABLE_TRACE_FLUSH=async the
// ordered, batched writer (internal/store/tracewriter) fronts them: it sends its batches through
// WriteTraceBatch, which runs the same statements, and falls back to these methods row by row.

// ErrNotFound is returned by the readers when the row does not exist.
var ErrNotFound = pgx.ErrNoRows

// TraceRecord is one traces row. Nullable columns are pointers. Audit carries the audit fields
// that readers join from audits and audit_claims; writers ignore it.
type TraceRecord struct {
	TraceID          string
	ClientRequestID  *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Source           string
	APIFormat        string
	Path             string
	Route            string
	PolicyVersion    *int64
	Mode             string
	EffectiveMode    *string
	KeyLabel         *string
	Model            *string
	Stream           bool
	Scenario         *string
	AnswerSource     *string
	UpstreamStatus   *int
	UpstreamMs       *int64
	TTFTMs           *int64
	TotalMs          *int64
	OverheadMs       *int64
	GateMs           *int64
	PromptTokens     *int
	CompletionTokens *int
	Action           *string
	WouldAction      *string
	// GateStatus is pending until the decision is recorded.
	GateStatus       string
	SkipReason       *string
	Verdict          *string
	MaxRisk          *float64
	Reasons          []string
	EvidenceSummary  json.RawMessage
	DecisionBackend  *string
	DecisionSnapshot *string
	DecisionCostUSD  *float64
	AuditID          *string
	// AuditStatus is a TraceAuditStatus; none until an audit is planned.
	AuditStatus     string
	AuditLane       *string
	AuditSelection  *string
	QuestionPreview *string
	ResponsePreview *string
	Flags           []string
	// ResponseChars is the rune count of the gated answer text (0005; NULL for traces written
	// before migration 0005 and for traces without an answer).
	ResponseChars *int

	Audit AuditJoin
}

// AuditJoin is the deep-audit outcome joined into a trace summary. Counts is nil when the trace
// has no audit row.
type AuditJoin struct {
	Verdict    *string
	TrustScore *float64
	Grade      *string
	Counts     VerdictCounts
}

// Summary returns the trace as the API shows it. A trace whose audit is not_assessed (no
// evidence, so the audit would make no LLM call) reports the NOT_ASSESSED audit verdict.
func (r *TraceRecord) Summary() TraceSummary {
	s := TraceSummary{
		TraceID: r.TraceID, ClientRequestID: r.ClientRequestID, CreatedAt: utc(r.CreatedAt), UpdatedAt: utc(r.UpdatedAt),
		Source: r.Source, APIFormat: r.APIFormat, Route: r.Route, PolicyVersion: r.PolicyVersion, Mode: r.Mode,
		EffectiveMode: r.EffectiveMode, Model: r.Model, Stream: r.Stream, UpstreamStatus: r.UpstreamStatus,
		AnswerSource: r.AnswerSource, Action: r.Action, WouldAction: r.WouldAction, GateStatus: r.GateStatus,
		SkipReason: r.SkipReason, Verdict: r.Verdict, MaxRisk: r.MaxRisk, Reasons: nonNil(r.Reasons),
		CheckedAgainst: jsonOrNull(r.EvidenceSummary),
		Timings:        Timings{UpstreamMs: r.UpstreamMs, TTFTMs: r.TTFTMs, GateMs: r.GateMs, OverheadMs: r.OverheadMs, TotalMs: r.TotalMs},
		Audit: TraceAudit{
			AuditID: r.AuditID, Status: r.AuditStatus, Lane: r.AuditLane, Selection: r.AuditSelection,
			Verdict: r.Audit.Verdict, TrustScore: r.Audit.TrustScore, Grade: r.Audit.Grade, VerdictCounts: r.Audit.Counts,
		},
		QuestionPreview: r.QuestionPreview, ResponsePreview: r.ResponsePreview, Flags: nonNil(r.Flags),
	}
	if s.GateStatus == "" {
		s.GateStatus = GateStatusPending
	}
	if s.Audit.Status == "" {
		s.Audit.Status = TraceAuditNone
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = s.CreatedAt
	}
	if r.DecisionBackend != nil {
		s.DecisionModel = &TraceDecisionModel{Backend: *r.DecisionBackend, Snapshot: r.DecisionSnapshot, CostUSD: r.DecisionCostUSD}
	}
	if s.Audit.Verdict == nil && s.Audit.Status == TraceAuditNotAssessed {
		v := VerdictNotAssessed
		s.Audit.Verdict = &v
	}
	return s
}

// DecisionRecord is one gate_decisions row. Decision is the decision/v1 document as served.
type DecisionRecord struct {
	TraceID                string
	Attempt                int
	PolicyVersion          *int64
	Route                  string
	Mode                   string
	EffectiveMode          string
	Status                 string
	Action                 string
	WouldAction            *string
	Verdict                *string
	MaxRisk                *float64
	Calibration            string
	Degraded               *string
	Reasons                []string
	Decision               json.RawMessage
	Detectors              json.RawMessage
	Excluded               json.RawMessage
	EvidenceSummary        json.RawMessage
	QuestionSetSHA256      *string
	RequestSHA256          []string
	CPT                    *float64
	PackedEvidenceIDs      []string
	TruncatedIDs           []string
	DecisionBackend        *string
	DecisionModelRequested *string
	DecisionSnapshot       *string
	DecisionMs             *int64
	DecisionCalls          *int
	DecisionAttempts       *int
	DecisionHedged         *bool
	ConnectMs              *int64
	InputTokens            *int
	CostUSD                *float64
	ErrorKind              *string
	ErrorMessage           *string
	Late                   bool
	GateMs                 *int64
}

// BodyRecord is one trace_bodies row. Messages and Evidence are JSON arrays.
type BodyRecord struct {
	TraceID            string
	Messages           json.RawMessage
	UserRequest        *string
	SystemInstructions *string
	ResponseText       *string
	FinalResponseText  *string
	Evidence           json.RawMessage
	Rules              map[string]string
	Bytes              int
	Truncated          bool
	InclusionProb      float64
	ExpiresAt          time.Time
}

// View returns the body as TraceDetail.body shows it; an expired body is unavailable.
func (b *BodyRecord) View(now time.Time) TraceBodyView {
	if !now.Before(b.ExpiresAt) {
		return UnavailableBody(BodyExpired)
	}
	rules := b.Rules
	if rules == nil {
		rules = map[string]string{}
	}
	return TraceBodyView{
		Available: true, Messages: jsonOr(b.Messages, "[]"), UserRequest: b.UserRequest,
		SystemInstructions: b.SystemInstructions, ResponseText: b.ResponseText, FinalResponseText: b.FinalResponseText,
		Evidence: jsonOr(b.Evidence, "[]"), Rules: rules, Truncated: b.Truncated,
	}
}

// TraceWriteKind names a trace-scoped write: one of the write methods below.
type TraceWriteKind int

// The trace-scoped writes (SPEC §2.3 writers).
const (
	WriteStart TraceWriteKind = iota + 1
	WriteDecision
	WriteAttempt
	WriteLateDecision
)

// TraceWrite is one trace-scoped write with the arguments of its method: StartTrace (Trace),
// RecordDecision (Trace, Decision and an optional Body), RecordAttempt or RecordLateDecision
// (Decision). The batched writer (internal/store/tracewriter) queues these and sends them through
// WriteTraceBatch.
type TraceWrite struct {
	Kind     TraceWriteKind
	Trace    *TraceRecord
	Decision *DecisionRecord
	Body     *BodyRecord
}

// TraceID is the trace the write belongs to ("" for a write Check refuses).
func (w *TraceWrite) TraceID() string {
	switch {
	case w.Trace != nil:
		return w.Trace.TraceID
	case w.Decision != nil:
		return w.Decision.TraceID
	}
	return ""
}

// Check refuses a write its method would refuse, before anything is sent.
func (w *TraceWrite) Check() error {
	switch w.Kind {
	case WriteStart:
		if w.Trace == nil || w.Trace.TraceID == "" {
			return errors.New("store: trace_id is required")
		}
	case WriteDecision:
		if w.Trace == nil || w.Trace.TraceID == "" || w.Decision == nil {
			return errors.New("store: a trace id and a decision are required")
		}
	case WriteAttempt:
		if w.Decision == nil || w.Decision.TraceID == "" {
			return errors.New("store: a decision with a trace id is required")
		}
		if w.Decision.Attempt < 1 {
			return fmt.Errorf("store: attempt %d is recorded with its trace (RecordDecision), not as a later attempt", w.Decision.Attempt)
		}
	case WriteLateDecision:
		if w.Decision == nil || w.Decision.TraceID == "" {
			return errors.New("store: a decision with a trace id is required")
		}
		if !w.Decision.Late {
			return errors.New("store: a late decision must be marked late")
		}
	default:
		return fmt.Errorf("store: unknown trace write kind %d", w.Kind)
	}
	return nil
}

// statement is one SQL statement of a write; what names it in an error.
type statement struct {
	what string
	sql  string
	args []any
}

// statements are the SQL statements of a checked write, in order. They run in one transaction.
func (w *TraceWrite) statements() ([]statement, error) {
	switch w.Kind {
	case WriteStart:
		return []statement{{"insert trace", `
		INSERT INTO traces (` + traceInsertColumns + `)
		VALUES (` + placeholders(traceInsertCount) + `)
		ON CONFLICT (trace_id) DO NOTHING`, traceInsertArgs(w.Trace)}}, nil
	case WriteDecision:
		out := []statement{
			{"upsert trace", `
		INSERT INTO traces (` + traceInsertColumns + `)
		VALUES (` + placeholders(traceInsertCount) + `)
		ON CONFLICT (trace_id) DO UPDATE SET ` + traceDecisionUpdate, traceInsertArgs(w.Trace)},
			decisionStatement(w.Decision),
		}
		if b := w.Body; b != nil {
			rules, err := json.Marshal(nonNilMap(b.Rules))
			if err != nil {
				return nil, fmt.Errorf("marshal rules: %w", err)
			}
			out = append(out, statement{"insert trace body", `
			INSERT INTO trace_bodies (trace_id, messages, user_request, system_instructions, response_text,
				final_response_text, evidence, rules, bytes, truncated, inclusion_prob, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (trace_id) DO UPDATE SET final_response_text = EXCLUDED.final_response_text`, []any{
				w.Trace.TraceID, jsonOr(b.Messages, "[]"), b.UserRequest, b.SystemInstructions, b.ResponseText,
				b.FinalResponseText, jsonOr(b.Evidence, "[]"), json.RawMessage(rules), b.Bytes, b.Truncated,
				b.InclusionProb, b.ExpiresAt,
			}})
		}
		return out, nil
	case WriteAttempt:
		return []statement{decisionStatement(w.Decision)}, nil
	case WriteLateDecision:
		return []statement{
			{"upsert late decision", `
		INSERT INTO gate_decisions (` + decisionColumns + `)
		VALUES (` + placeholders(decisionColumnCount) + `)
		ON CONFLICT (trace_id, attempt) DO UPDATE SET ` + lateDecisionUpdate, decisionArgs(w.Decision)},
			{"flag late decision", `
		UPDATE traces SET flags = array_append(flags, 'late_decision'), updated_at = now()
		WHERE trace_id = $1 AND NOT ('late_decision' = ANY(flags))`, []any{w.Decision.TraceID}},
		}, nil
	}
	return nil, w.Check()
}

// write runs one write's statements in a transaction (one statement runs on its own).
func (s *PostgresStore) write(ctx context.Context, w *TraceWrite) error {
	if err := w.Check(); err != nil {
		return err
	}
	stmts, err := w.statements()
	if err != nil {
		return err
	}
	if len(stmts) == 1 {
		return execStatement(ctx, s.pool, stmts[0])
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	for _, st := range stmts {
		if err := execStatement(ctx, tx, st); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// execStatement runs a statement; its error names the statement.
func execStatement(ctx context.Context, q execer, st statement) error {
	if _, err := q.Exec(ctx, st.sql, st.args...); err != nil {
		return fmt.Errorf("%s: %w", st.what, err)
	}
	return nil
}

// WriteTraceBatch runs the statements of several writes, in order, in one transaction and one
// round trip (pgx.Batch). Any failure rolls the whole batch back and is returned: the batched
// writer then retries its writes one by one through the methods below.
func (s *PostgresStore) WriteTraceBatch(ctx context.Context, writes []TraceWrite) error {
	b := &pgx.Batch{}
	var queued []statement
	for i := range writes {
		w := &writes[i]
		if err := w.Check(); err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
		stmts, err := w.statements()
		if err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
		for _, st := range stmts {
			b.Queue(st.sql, st.args...)
		}
		queued = append(queued, stmts...)
	}
	if len(queued) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	br := tx.SendBatch(ctx, b)
	for _, st := range queued {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("%s: %w", st.what, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// StartTrace inserts a pending trace. A trace that already exists is left as it is.
func (s *PostgresStore) StartTrace(ctx context.Context, t *TraceRecord) error {
	return s.write(ctx, &TraceWrite{Kind: WriteStart, Trace: t})
}

// RecordDecision writes a decided trace in one transaction: the traces row (inserted, or updated
// with the decision's columns; columns the decision leaves NULL keep their stored value), the
// gate_decisions row for its attempt, and the body when one is kept.
func (s *PostgresStore) RecordDecision(ctx context.Context, t *TraceRecord, d *DecisionRecord, b *BodyRecord) error {
	return s.write(ctx, &TraceWrite{Kind: WriteDecision, Trace: t, Decision: d, Body: b})
}

// RecordAttempt writes the gate_decisions row of a later attempt of a trace (attempt 1, the
// fallback re-gate) and nothing else: the trace keeps its fields and its audit stays on the
// original answer. Writing an attempt again replaces its decision.
func (s *PostgresStore) RecordAttempt(ctx context.Context, d *DecisionRecord) error {
	return s.write(ctx, &TraceWrite{Kind: WriteAttempt, Decision: d})
}

// RecordLateDecision records a decision recomputed after a late decision-model answer (d.Late,
// SPEC §2.9 late decisions): it replaces the gate_decisions row of d's attempt whole, usage and
// compile columns included, and adds the flag late_decision to the trace. The trace keeps every
// other field: its action and gate outcome are what was enforced.
func (s *PostgresStore) RecordLateDecision(ctx context.Context, d *DecisionRecord) error {
	return s.write(ctx, &TraceWrite{Kind: WriteLateDecision, Decision: d})
}

// lateDecisionUpdate replaces a gate_decisions row whole with a late decision.
const lateDecisionUpdate = `
	policy_version = EXCLUDED.policy_version, route = EXCLUDED.route, mode = EXCLUDED.mode,
	effective_mode = EXCLUDED.effective_mode, status = EXCLUDED.status, action = EXCLUDED.action,
	would_action = EXCLUDED.would_action, verdict = EXCLUDED.verdict, max_risk = EXCLUDED.max_risk,
	calibration = EXCLUDED.calibration, degraded = EXCLUDED.degraded, reasons = EXCLUDED.reasons,
	decision = EXCLUDED.decision, detectors = EXCLUDED.detectors, excluded = EXCLUDED.excluded,
	evidence_summary = EXCLUDED.evidence_summary, question_set_sha256 = EXCLUDED.question_set_sha256,
	request_sha256 = EXCLUDED.request_sha256, cpt = EXCLUDED.cpt,
	packed_evidence_ids = EXCLUDED.packed_evidence_ids, truncated_ids = EXCLUDED.truncated_ids,
	decision_backend = EXCLUDED.decision_backend, decision_model_requested = EXCLUDED.decision_model_requested,
	decision_snapshot = EXCLUDED.decision_snapshot, decision_ms = EXCLUDED.decision_ms,
	decision_calls = EXCLUDED.decision_calls, decision_attempts = EXCLUDED.decision_attempts,
	decision_hedged = EXCLUDED.decision_hedged, connect_ms = EXCLUDED.connect_ms,
	input_tokens = EXCLUDED.input_tokens, cost_usd = EXCLUDED.cost_usd, error_kind = EXCLUDED.error_kind,
	error_message = EXCLUDED.error_message, late = true, gate_ms = EXCLUDED.gate_ms, updated_at = now()`

// execer runs a statement on the pool or in a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// decisionColumns are the gate_decisions columns a decision write carries, in decisionArgs order.
const decisionColumns = `
	trace_id, attempt, policy_version, route, mode, effective_mode, status, action, would_action, verdict,
	max_risk, calibration, degraded, reasons, decision, detectors, excluded, evidence_summary,
	question_set_sha256, request_sha256, cpt, packed_evidence_ids, truncated_ids, decision_backend,
	decision_model_requested, decision_snapshot, decision_ms, decision_calls, decision_attempts,
	decision_hedged, connect_ms, input_tokens, cost_usd, error_kind, error_message, late, gate_ms`

const decisionColumnCount = 37

func decisionArgs(d *DecisionRecord) []any {
	return []any{
		d.TraceID, d.Attempt, d.PolicyVersion, d.Route, d.Mode, d.EffectiveMode, d.Status, d.Action, d.WouldAction,
		d.Verdict, d.MaxRisk, d.Calibration, d.Degraded, nonNil(d.Reasons), jsonOr(d.Decision, "{}"),
		jsonOr(d.Detectors, "[]"), jsonOr(d.Excluded, "[]"), jsonOrNil(d.EvidenceSummary), d.QuestionSetSHA256,
		d.RequestSHA256, d.CPT, nonNil(d.PackedEvidenceIDs), nonNil(d.TruncatedIDs), d.DecisionBackend,
		d.DecisionModelRequested, d.DecisionSnapshot, d.DecisionMs, d.DecisionCalls, d.DecisionAttempts,
		d.DecisionHedged, d.ConnectMs, d.InputTokens, d.CostUSD, d.ErrorKind, d.ErrorMessage, d.Late, d.GateMs,
	}
}

// decisionStatement upserts the gate_decisions row of d's attempt. A late decision already
// recorded for the attempt stays: it is the newest word on it (RecordLateDecision).
func decisionStatement(d *DecisionRecord) statement {
	return statement{"insert gate decision", `
		INSERT INTO gate_decisions (` + decisionColumns + `)
		VALUES (` + placeholders(decisionColumnCount) + `)
		ON CONFLICT (trace_id, attempt) DO UPDATE SET
			status = EXCLUDED.status, action = EXCLUDED.action, would_action = EXCLUDED.would_action,
			verdict = EXCLUDED.verdict, max_risk = EXCLUDED.max_risk, calibration = EXCLUDED.calibration,
			degraded = EXCLUDED.degraded, reasons = EXCLUDED.reasons, decision = EXCLUDED.decision,
			detectors = EXCLUDED.detectors, excluded = EXCLUDED.excluded, late = EXCLUDED.late,
			gate_ms = EXCLUDED.gate_ms, updated_at = now()
		WHERE NOT gate_decisions.late`, decisionArgs(d)}
}

// TraceAuditUpdate changes a trace's audit fields; nil and empty fields keep their value.
type TraceAuditUpdate struct {
	AuditID *string
	// Status is a TraceAuditStatus.
	Status string
	Lane   *string
}

// SetTraceAudit updates the audit fields of a trace.
func (s *PostgresStore) SetTraceAudit(ctx context.Context, traceID string, u TraceAuditUpdate) error {
	var status *string
	if u.Status != "" {
		status = &u.Status
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE traces SET audit_id = COALESCE($2, audit_id), audit_status = COALESCE($3, audit_status),
			audit_lane = COALESCE($4, audit_lane), updated_at = now()
		WHERE trace_id = $1`, traceID, u.AuditID, status, u.Lane)
	return err
}

// traceInsertColumns are the columns every trace write carries.
const traceInsertColumns = `
	trace_id, client_request_id, created_at, source, api_format, path, route, policy_version, mode,
	effective_mode, key_label, model, stream, scenario, answer_source, upstream_status, upstream_ms, ttft_ms,
	total_ms, overhead_ms, gate_ms, prompt_tokens, completion_tokens, action, would_action, gate_status,
	skip_reason, verdict, max_risk, reasons, evidence_summary, decision_backend, decision_snapshot,
	decision_cost_usd, audit_id, audit_status, audit_lane, audit_selection, question_preview, response_preview,
	flags, response_chars`

const traceInsertCount = 42

// traceDecisionUpdate updates a stored trace with a decided one. The gate outcome, the reasons,
// the audit status and the flags are the decision's; everything else keeps a stored value the
// decision does not have. A late_decision flag already stored stays (RecordLateDecision): the
// late decision of the fallback's attempt 1 can be recorded before the trace write of attempt 0.
const traceDecisionUpdate = `
	effective_mode = COALESCE(EXCLUDED.effective_mode, traces.effective_mode),
	model = COALESCE(EXCLUDED.model, traces.model),
	answer_source = COALESCE(EXCLUDED.answer_source, traces.answer_source),
	upstream_status = COALESCE(EXCLUDED.upstream_status, traces.upstream_status),
	upstream_ms = COALESCE(EXCLUDED.upstream_ms, traces.upstream_ms),
	ttft_ms = COALESCE(EXCLUDED.ttft_ms, traces.ttft_ms),
	total_ms = COALESCE(EXCLUDED.total_ms, traces.total_ms),
	overhead_ms = COALESCE(EXCLUDED.overhead_ms, traces.overhead_ms),
	gate_ms = COALESCE(EXCLUDED.gate_ms, traces.gate_ms),
	prompt_tokens = COALESCE(EXCLUDED.prompt_tokens, traces.prompt_tokens),
	completion_tokens = COALESCE(EXCLUDED.completion_tokens, traces.completion_tokens),
	action = EXCLUDED.action,
	would_action = EXCLUDED.would_action,
	gate_status = EXCLUDED.gate_status,
	skip_reason = EXCLUDED.skip_reason,
	verdict = EXCLUDED.verdict,
	max_risk = EXCLUDED.max_risk,
	reasons = EXCLUDED.reasons,
	evidence_summary = COALESCE(EXCLUDED.evidence_summary, traces.evidence_summary),
	decision_backend = COALESCE(EXCLUDED.decision_backend, traces.decision_backend),
	decision_snapshot = COALESCE(EXCLUDED.decision_snapshot, traces.decision_snapshot),
	decision_cost_usd = COALESCE(EXCLUDED.decision_cost_usd, traces.decision_cost_usd),
	audit_id = COALESCE(EXCLUDED.audit_id, traces.audit_id),
	audit_status = EXCLUDED.audit_status,
	audit_lane = COALESCE(EXCLUDED.audit_lane, traces.audit_lane),
	audit_selection = COALESCE(EXCLUDED.audit_selection, traces.audit_selection),
	question_preview = COALESCE(EXCLUDED.question_preview, traces.question_preview),
	response_preview = COALESCE(EXCLUDED.response_preview, traces.response_preview),
	response_chars = COALESCE(EXCLUDED.response_chars, traces.response_chars),
	flags = CASE WHEN 'late_decision' = ANY(traces.flags) AND NOT ('late_decision' = ANY(EXCLUDED.flags))
		THEN array_append(EXCLUDED.flags, 'late_decision') ELSE EXCLUDED.flags END,
	updated_at = now()`

// Stored returns t as a trace write stores it and GetTrace reads it back (traceInsertArgs and
// scanTrace): created_at defaults to now and is UTC, the gate status defaults to pending, the audit
// status to none, max_risk keeps a REAL's precision, an empty evidence summary is NULL, reasons and
// flags are never nil, and updated_at is now. The batched writer's memory copy of a trace is built
// with it, so a read from memory answers as the database would.
func (t *TraceRecord) Stored(now time.Time) TraceRecord {
	out := *t
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.CreatedAt, out.UpdatedAt = out.CreatedAt.UTC(), now.UTC()
	if out.GateStatus == "" {
		out.GateStatus = GateStatusPending
	}
	if out.AuditStatus == "" {
		out.AuditStatus = TraceAuditNone
	}
	if out.MaxRisk != nil {
		v := Float64From32(float32(*out.MaxRisk))
		out.MaxRisk = &v
	}
	out.EvidenceSummary = jsonOrNil(out.EvidenceSummary)
	out.Reasons, out.Flags = append([]string{}, out.Reasons...), append([]string{}, out.Flags...)
	out.Audit = AuditJoin{}
	return out
}

// MergeDecided is the Go form of RecordDecision's upsert of a stored trace (traceDecisionUpdate):
// the gate outcome, reasons, audit status and flags are the decided trace's (a late_decision flag
// already stored stays); the other decision columns keep a stored value the decided trace leaves
// unset; identity columns (client request id, created_at, source, format, path, route, policy
// version, mode, key label, stream, scenario) and the joined audit keep their stored values. Both
// records are as Stored returns them.
func MergeDecided(stored, decided *TraceRecord) TraceRecord {
	out := *stored
	coalesce := func(dst **string, v *string) {
		if v != nil {
			*dst = v
		}
	}
	coalesceInt64 := func(dst **int64, v *int64) {
		if v != nil {
			*dst = v
		}
	}
	coalesceInt := func(dst **int, v *int) {
		if v != nil {
			*dst = v
		}
	}
	coalesce(&out.EffectiveMode, decided.EffectiveMode)
	coalesce(&out.Model, decided.Model)
	coalesce(&out.AnswerSource, decided.AnswerSource)
	coalesceInt(&out.UpstreamStatus, decided.UpstreamStatus)
	coalesceInt64(&out.UpstreamMs, decided.UpstreamMs)
	coalesceInt64(&out.TTFTMs, decided.TTFTMs)
	coalesceInt64(&out.TotalMs, decided.TotalMs)
	coalesceInt64(&out.OverheadMs, decided.OverheadMs)
	coalesceInt64(&out.GateMs, decided.GateMs)
	coalesceInt(&out.PromptTokens, decided.PromptTokens)
	coalesceInt(&out.CompletionTokens, decided.CompletionTokens)
	out.Action, out.WouldAction, out.GateStatus = decided.Action, decided.WouldAction, decided.GateStatus
	out.SkipReason, out.Verdict, out.MaxRisk, out.Reasons = decided.SkipReason, decided.Verdict, decided.MaxRisk, decided.Reasons
	if decided.EvidenceSummary != nil {
		out.EvidenceSummary = decided.EvidenceSummary
	}
	coalesce(&out.DecisionBackend, decided.DecisionBackend)
	coalesce(&out.DecisionSnapshot, decided.DecisionSnapshot)
	if decided.DecisionCostUSD != nil {
		out.DecisionCostUSD = decided.DecisionCostUSD
	}
	coalesce(&out.AuditID, decided.AuditID)
	out.AuditStatus = decided.AuditStatus
	coalesce(&out.AuditLane, decided.AuditLane)
	coalesce(&out.AuditSelection, decided.AuditSelection)
	coalesce(&out.QuestionPreview, decided.QuestionPreview)
	coalesce(&out.ResponsePreview, decided.ResponsePreview)
	coalesceInt(&out.ResponseChars, decided.ResponseChars)
	out.Flags = append([]string{}, decided.Flags...)
	if slices.Contains(stored.Flags, FlagLateDecision) && !slices.Contains(out.Flags, FlagLateDecision) {
		out.Flags = append(out.Flags, FlagLateDecision)
	}
	out.UpdatedAt = decided.UpdatedAt
	return out
}

// FlagLateDecision is the trace flag RecordLateDecision adds.
const FlagLateDecision = "late_decision"

func traceInsertArgs(t *TraceRecord) []any {
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	gateStatus := t.GateStatus
	if gateStatus == "" {
		gateStatus = GateStatusPending
	}
	auditStatus := t.AuditStatus
	if auditStatus == "" {
		auditStatus = TraceAuditNone
	}
	return []any{
		t.TraceID, t.ClientRequestID, created.UTC(), t.Source, t.APIFormat, t.Path, t.Route, t.PolicyVersion, t.Mode,
		t.EffectiveMode, t.KeyLabel, t.Model, t.Stream, t.Scenario, t.AnswerSource, t.UpstreamStatus, t.UpstreamMs,
		t.TTFTMs, t.TotalMs, t.OverheadMs, t.GateMs, t.PromptTokens, t.CompletionTokens, t.Action, t.WouldAction,
		gateStatus, t.SkipReason, t.Verdict, t.MaxRisk, nonNil(t.Reasons), jsonOrNil(t.EvidenceSummary),
		t.DecisionBackend, t.DecisionSnapshot, t.DecisionCostUSD, t.AuditID, auditStatus, t.AuditLane,
		t.AuditSelection, t.QuestionPreview, t.ResponsePreview, nonNil(t.Flags), t.ResponseChars,
	}
}

// traceSelect reads traces rows (alias t) with their audit outcome.
const traceSelect = `
	SELECT t.trace_id, t.client_request_id, t.created_at, t.updated_at, t.source, t.api_format, t.path, t.route,
		t.policy_version, t.mode, t.effective_mode, t.key_label, t.model, t.stream, t.scenario, t.answer_source,
		t.upstream_status, t.upstream_ms, t.ttft_ms, t.total_ms, t.overhead_ms, t.gate_ms, t.prompt_tokens,
		t.completion_tokens, t.action, t.would_action, t.gate_status, t.skip_reason, t.verdict, t.max_risk,
		t.reasons, t.evidence_summary, t.decision_backend, t.decision_snapshot, t.decision_cost_usd::float8,
		t.audit_id, t.audit_status, t.audit_lane, t.audit_selection, t.question_preview, t.response_preview,
		t.flags, t.response_chars, a.audit_id IS NOT NULL, a.verdict, a.faithfulness_score, a.grade, vc.counts
	FROM traces t
	LEFT JOIN audits a ON a.audit_id = t.audit_id
	LEFT JOIN LATERAL (
		SELECT jsonb_object_agg(s.status, s.n) AS counts
		FROM (SELECT c.status, count(*) AS n FROM audit_claims c WHERE c.audit_id = a.audit_id GROUP BY c.status) s
	) vc ON TRUE`

// auditVerdictSQL is the audit verdict a trace summary reports (see TraceRecord.Summary).
const auditVerdictSQL = `COALESCE(a.verdict, CASE WHEN t.audit_status = 'not_assessed' THEN 'NOT_ASSESSED' END)`

func scanTrace(row pgx.Row) (*TraceRecord, error) {
	var (
		t        TraceRecord
		maxRisk  *float32
		hasAudit bool
		counts   map[string]int
	)
	err := row.Scan(
		&t.TraceID, &t.ClientRequestID, &t.CreatedAt, &t.UpdatedAt, &t.Source, &t.APIFormat, &t.Path, &t.Route,
		&t.PolicyVersion, &t.Mode, &t.EffectiveMode, &t.KeyLabel, &t.Model, &t.Stream, &t.Scenario, &t.AnswerSource,
		&t.UpstreamStatus, &t.UpstreamMs, &t.TTFTMs, &t.TotalMs, &t.OverheadMs, &t.GateMs, &t.PromptTokens,
		&t.CompletionTokens, &t.Action, &t.WouldAction, &t.GateStatus, &t.SkipReason, &t.Verdict, &maxRisk,
		&t.Reasons, &t.EvidenceSummary, &t.DecisionBackend, &t.DecisionSnapshot, &t.DecisionCostUSD,
		&t.AuditID, &t.AuditStatus, &t.AuditLane, &t.AuditSelection, &t.QuestionPreview, &t.ResponsePreview,
		&t.Flags, &t.ResponseChars, &hasAudit, &t.Audit.Verdict, &t.Audit.TrustScore, &t.Audit.Grade, &counts,
	)
	if err != nil {
		return nil, err
	}
	t.MaxRisk = f32ptr(maxRisk)
	if hasAudit {
		t.Audit.Counts = NewVerdictCounts()
		for k, n := range counts {
			if _, ok := t.Audit.Counts[k]; ok {
				t.Audit.Counts[k] = n
			}
		}
	}
	return &t, nil
}

// TraceFilter selects and pages GET /api/traces. Empty strings and nil times match everything.
type TraceFilter struct {
	Limit        int
	Cursor       string
	Since        *time.Time
	Until        *time.Time
	UpdatedSince *time.Time
	Route        string
	Model        string
	Action       string
	GateStatus   string
	Verdict      string
	AuditStatus  string
	AuditVerdict string
	Source       string
	Outcome      string
	// Q matches the question and answer previews (case-insensitive substring).
	Q string
	// Reason matches the gate reasons (D68): a whole code ("det:secret_pattern") matches
	// that reason, a prefix that ends in a colon ("det:") matches every reason it starts. The API
	// asks for Since with it, which bounds the scan.
	Reason string
}

// ReasonPrefix reports whether a reason filter is a prefix ("det:") rather than a whole code.
func ReasonPrefix(reason string) bool { return strings.HasSuffix(reason, ":") }

// ErrBadCursor is returned for a cursor this store did not issue.
var ErrBadCursor = errors.New("store: invalid cursor")

// ListTraces returns a keyset page of traces, newest first, and the cursor of the next page ("" at
// the end).
func (s *PostgresStore) ListTraces(ctx context.Context, f TraceFilter) ([]TraceRecord, string, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	eq := func(col, v string) {
		if v != "" {
			where = append(where, col+" = "+arg(v))
		}
	}
	if f.Cursor != "" {
		at, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, fmt.Sprintf("(t.created_at, t.trace_id) < (%s, %s)", arg(at), arg(id)))
	}
	if f.Since != nil {
		where = append(where, "t.created_at >= "+arg(*f.Since))
	}
	if f.Until != nil {
		where = append(where, "t.created_at < "+arg(*f.Until))
	}
	if f.UpdatedSince != nil {
		where = append(where, "t.updated_at >= "+arg(*f.UpdatedSince))
	}
	eq("t.route", f.Route)
	eq("t.model", f.Model)
	eq("t.action", f.Action)
	eq("t.gate_status", f.GateStatus)
	eq("t.verdict", f.Verdict)
	eq("t.audit_status", f.AuditStatus)
	eq(auditVerdictSQL, f.AuditVerdict)
	eq("t.source", f.Source)
	if f.Outcome != "" {
		expr, err := outcomeSQL()
		if err != nil {
			return nil, "", err
		}
		eq(expr, f.Outcome)
	}
	switch {
	case f.Reason == "":
	case ReasonPrefix(f.Reason):
		where = append(where, "EXISTS (SELECT 1 FROM unnest(t.reasons) AS r(code) WHERE r.code LIKE "+arg(escapeLike(f.Reason)+"%")+")")
	default:
		where = append(where, "t.reasons @> ARRAY["+arg(f.Reason)+"]::text[]")
	}
	if f.Q != "" {
		p := arg("%" + escapeLike(f.Q) + "%")
		where = append(where, fmt.Sprintf("(t.question_preview ILIKE %s OR t.response_preview ILIKE %s)", p, p))
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := traceSelect
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY t.created_at DESC, t.trace_id DESC LIMIT " + arg(limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []TraceRecord{}
	for rows.Next() {
		t, err := scanTrace(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.TraceID)
	}
	return out, next, nil
}

// GetTrace returns one trace with its audit outcome, or ErrNotFound.
func (s *PostgresStore) GetTrace(ctx context.Context, traceID string) (*TraceRecord, error) {
	return scanTrace(s.pool.QueryRow(ctx, traceSelect+" WHERE t.trace_id = $1", traceID))
}

// GetTraceBody returns the stored body of a trace, or ErrNotFound when none was kept. A body the
// retention job deleted comes back as an expired record without content.
func (s *PostgresStore) GetTraceBody(ctx context.Context, traceID string) (*BodyRecord, error) {
	var (
		b     BodyRecord
		rules []byte
		prob  float32
	)
	err := s.pool.QueryRow(ctx, `
		SELECT trace_id, messages, user_request, system_instructions, response_text, final_response_text,
			evidence, rules, bytes, truncated, inclusion_prob, expires_at
		FROM trace_bodies WHERE trace_id = $1`, traceID).Scan(
		&b.TraceID, &b.Messages, &b.UserRequest, &b.SystemInstructions, &b.ResponseText, &b.FinalResponseText,
		&b.Evidence, &rules, &b.Bytes, &b.Truncated, &prob, &b.ExpiresAt)
	if errors.Is(err, ErrNotFound) {
		return s.expiredBody(ctx, traceID)
	}
	if err != nil {
		return nil, err
	}
	b.InclusionProb = Float64From32(prob)
	if len(rules) > 0 {
		if err := json.Unmarshal(rules, &b.Rules); err != nil {
			return nil, fmt.Errorf("trace body rules: %w", err)
		}
	}
	return &b, nil
}

// expiredBody is the body of a trace whose body retention deleted (flag body_expired): a record
// without content that expired at the zero time, so View reports it expired rather than never
// stored. Any other trace has no body: ErrNotFound.
func (s *PostgresStore) expiredBody(ctx context.Context, traceID string) (*BodyRecord, error) {
	var expired bool
	err := s.pool.QueryRow(ctx, `SELECT 'body_expired' = ANY(flags) FROM traces WHERE trace_id = $1`, traceID).Scan(&expired)
	switch {
	case err != nil:
		return nil, err
	case !expired:
		return nil, ErrNotFound
	}
	return &BodyRecord{TraceID: traceID, Messages: json.RawMessage("[]"), Evidence: json.RawMessage("[]"), Rules: map[string]string{}}, nil
}

// TraceDecisions returns the decision/v1 documents of a trace, by attempt.
func (s *PostgresStore) TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT decision FROM gate_decisions WHERE trace_id = $1 ORDER BY attempt`, traceID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []json.RawMessage{}
	}
	return out, nil
}

// cursor encodes a keyset position: created_at (unix microseconds, the column's precision) and
// the id that breaks ties.
func encodeCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrBadCursor
	}
	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrBadCursor
	}
	return time.UnixMicro(us).UTC(), id, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func placeholders(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		b.WriteString("$" + strconv.Itoa(i))
	}
	return b.String()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// jsonOr returns raw, or def when raw is empty or JSON null (for NOT NULL columns and views).
func jsonOr(raw json.RawMessage, def string) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(def)
	}
	return raw
}

// jsonOrNil returns raw, or nil (SQL NULL) when raw is empty or JSON null.
func jsonOrNil(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// jsonOrNull returns raw, or JSON null when raw is empty.
func jsonOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
