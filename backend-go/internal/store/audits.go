package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Audit defaults of migration 0002.
const (
	defaultAuditSource    = "proxy"
	scoringVersionLegacy  = "v1"
	decidedByLLM          = "llm"
	decidedByNone         = "none"
	probabilityFromLLM    = "llm_confidence"
	unalignedSpan         = -1
	unsupportedPolicyHall = "hallucination"
	unsupportedPolicyUnv  = "unverifiable"
)

// SaveAudit inserts the audit and its claims in one transaction. An existing
// audit_id is never touched: the insert is ON CONFLICT DO NOTHING and, when it
// affects no row, ErrDuplicate is returned before any claim is written (B4).
//
// Defaults applied to the stored row: trace_id = audit_id when TraceID is
// empty, request_id = trace_id when RequestID is empty, created_at = now when
// CreatedAt is zero, and the 0002 column defaults for an empty Source, Status
// and ScoringVersion. ClientRequestID is stored only when it passes
// ClientRequestID; anything else is dropped with a warning.
func (s *PostgresStore) SaveAudit(ctx context.Context, record *AuditRecord) error {
	return s.saveAudit(ctx, record, "", nil)
}

// SaveAuditResult saves an audit like SaveAudit and, in the same transaction, sets the audit
// status of its trace (a TraceAuditStatus). A trace that was never recorded is not an error.
// Audits are persisted before audit_completed is broadcast (SPEC §2.3).
func (s *PostgresStore) SaveAuditResult(ctx context.Context, record *AuditRecord, traceAuditStatus string) error {
	if traceAuditStatus == "" {
		return errors.New("store: a trace audit status is required")
	}
	return s.saveAudit(ctx, record, traceAuditStatus, nil)
}

// SaveAuditResultWithCosts is SaveAuditResult with the audit's cost_events rows as the caller
// built them (worker.LinkedCostStore): linked by audit_id and stamped at completion, with their
// key label and price table version (D45). They are written in the audit's transaction,
// so the audit and its cost appear together; record.Costs is then only what the event carries.
// nil rows fall back to the rows SaveAuditResult derives from record.Costs.
func (s *PostgresStore) SaveAuditResultWithCosts(ctx context.Context, record *AuditRecord, traceAuditStatus string, costs []CostEvent) error {
	if traceAuditStatus == "" {
		return errors.New("store: a trace audit status is required")
	}
	if costs == nil {
		costs = []CostEvent{}
	}
	return s.saveAudit(ctx, record, traceAuditStatus, costs)
}

// saveAudit writes the audit and its costs: the rows given, or (nil) the rows derived from
// record.Costs.
func (s *PostgresStore) saveAudit(ctx context.Context, record *AuditRecord, traceAuditStatus string, costs []CostEvent) error {
	if record.AuditID == "" {
		return errors.New("store: audit_id is required")
	}
	traceID := record.TraceID
	if traceID == "" {
		traceID = record.AuditID
	}
	requestID := record.RequestID
	if requestID == "" {
		requestID = traceID
	}
	var clientRequestID *string
	if record.ClientRequestID != nil && *record.ClientRequestID != "" {
		if id, ok := ClientRequestID(*record.ClientRequestID); ok {
			clientRequestID = &id
		} else {
			log.Printf("[%s] warn: dropping client request id %.64q (%d bytes): must match %s",
				record.AuditID, *record.ClientRequestID, len(*record.ClientRequestID), clientRequestIDPattern)
		}
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	source := defaultString(record.Source, defaultAuditSource)
	status := defaultString(record.Status, AuditCompleted)
	scoring := defaultString(record.ScoringVersion, scoringVersionLegacy)
	timings, err := json.Marshal(record.StepTimings)
	if err != nil {
		return fmt.Errorf("marshal step timings: %w", err)
	}
	scores, err := marshalOrNil(record.Scores)
	if err != nil {
		return fmt.Errorf("marshal scores: %w", err)
	}
	gate, err := marshalOrNil(record.Gate)
	if err != nil {
		return fmt.Errorf("marshal gate: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	tag, err := tx.Exec(ctx, `
		INSERT INTO audits (
			audit_id, trace_id, request_id, client_request_id,
			user_query, llm_response, model,
			faithfulness_score, grade, hallucination_detected,
			reasoning_trace, processing_time_ms, step_timings, created_at,
			source, route, api_format, priority, selection, inclusion_prob, status, verdict,
			not_assessed_reason, error, error_kind, scores, degraded_reasons, judge, gate,
			evidence_summary, context_used, unsupported_policy, scoring_version, cost_usd,
			queue_wait_ms, completed_at, text_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,
			$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37)
		ON CONFLICT (audit_id) DO NOTHING`,
		record.AuditID, traceID, requestID, clientRequestID,
		record.UserQuery, record.LLMResponse, record.Model,
		record.FaithfulnessScore, record.Grade, record.HallucinationDetected,
		record.ReasoningTrace, record.ProcessingTimeMs, timings, createdAt,
		source, record.Route, record.APIFormat, record.Priority, record.Selection, record.InclusionProb, status,
		record.Verdict, record.NotAssessedReason, record.Error, record.ErrorKind, scores,
		nonNil(record.DegradedReasons), jsonOrNil(record.Judge), gate, jsonOrNil(record.EvidenceSummary),
		jsonOrNil(record.ContextUsed), record.UnsupportedPolicy, scoring, record.CostUSD, record.QueueWaitMs,
		record.CompletedAt, record.TextExpiresAt,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDuplicate
	}

	if len(record.Claims) > 0 {
		batch := &pgx.Batch{}
		for i, claim := range record.Claims {
			evidence, err := json.Marshal(nonNil(claim.Evidence))
			if err != nil {
				return fmt.Errorf("marshal evidence: %w", err)
			}
			probs, err := marshalOrNil(claim.VerdictProbabilities)
			if err != nil {
				return fmt.Errorf("marshal verdict probabilities: %w", err)
			}
			spans, err := marshalOrNil(claim.EvidenceSpans)
			if err != nil {
				return fmt.Errorf("marshal evidence spans: %w", err)
			}
			batch.Queue(`
				INSERT INTO audit_claims (audit_id, position, claim, status, confidence, evidence, reasoning,
					decided_by, verdict_probabilities, probability_source, span_start, span_end, span_method, quote,
					evidence_spans)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
				record.AuditID, i, claim.Claim, claim.Status, claim.Confidence, evidence, claim.Reasoning,
				claim.DecidedBy, probs, claim.ProbabilitySource, claim.SpanStart, claim.SpanEnd, claim.SpanMethod,
				claim.Quote, spans,
			)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert claims: %w", err)
		}
	}

	if costs == nil {
		costs = auditCostEvents(record, traceID, createdAt)
	}
	if len(costs) > 0 {
		batch := &pgx.Batch{}
		for i := range costs {
			queueCostEvent(batch, &costs[i])
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert cost events: %w", err)
		}
	}

	if traceAuditStatus != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE traces SET audit_id = $2, audit_status = $3, updated_at = now() WHERE trace_id = $1`,
			traceID, record.AuditID, traceAuditStatus); err != nil {
			return fmt.Errorf("update trace audit status: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// auditColumns is the SELECT list scanned by scanAudit (audits aliased a). trace_id can only be
// NULL for a row inserted after 0002 by a pre-0002 writer (an old proxy during a rolling
// upgrade); the legacy rule trace_id = audit_id covers it.
const auditColumns = `
	a.audit_id, COALESCE(a.trace_id, a.audit_id), a.request_id, a.client_request_id,
	a.user_query, a.llm_response, a.model,
	a.faithfulness_score, a.grade, a.hallucination_detected,
	a.reasoning_trace, a.processing_time_ms, a.step_timings, a.created_at,
	a.source, a.route, a.api_format, a.priority, a.selection, a.inclusion_prob, a.status, a.verdict,
	a.not_assessed_reason, a.error, a.error_kind, a.scores, a.degraded_reasons, a.judge, a.gate,
	a.evidence_summary, a.context_used, a.unsupported_policy, a.scoring_version, a.cost_usd::float8,
	a.queue_wait_ms, a.completed_at, a.text_expires_at, vc.counts`

// auditFrom joins every audit with its claim verdict counts.
const auditFrom = `
	FROM audits a
	LEFT JOIN LATERAL (
		SELECT jsonb_object_agg(s.status, s.n) AS counts
		FROM (SELECT c.status, count(*) AS n FROM audit_claims c WHERE c.audit_id = a.audit_id GROUP BY c.status) s
	) vc ON TRUE`

// scanAudit scans one auditColumns row; it accepts both pgx.Row and pgx.Rows.
func scanAudit(row pgx.Row) (*AuditRecord, error) {
	var (
		r        AuditRecord
		timings  []byte
		scores   []byte
		gate     []byte
		incl     *float32
		queueMs  *int
		counts   map[string]int
		degraded []string
	)
	if err := row.Scan(
		&r.AuditID, &r.TraceID, &r.RequestID, &r.ClientRequestID,
		&r.UserQuery, &r.LLMResponse, &r.Model,
		&r.FaithfulnessScore, &r.Grade, &r.HallucinationDetected,
		&r.ReasoningTrace, &r.ProcessingTimeMs, &timings, &r.CreatedAt,
		&r.Source, &r.Route, &r.APIFormat, &r.Priority, &r.Selection, &incl, &r.Status, &r.Verdict,
		&r.NotAssessedReason, &r.Error, &r.ErrorKind, &scores, &degraded, &r.Judge, &gate,
		&r.EvidenceSummary, &r.ContextUsed, &r.UnsupportedPolicy, &r.ScoringVersion, &r.CostUSD,
		&queueMs, &r.CompletedAt, &r.TextExpiresAt, &counts,
	); err != nil {
		return nil, err
	}
	r.InclusionProb = f32ptr(incl)
	r.DegradedReasons = nonNil(degraded)
	if queueMs != nil {
		v := int64(*queueMs)
		r.QueueWaitMs = &v
	}
	if len(timings) > 0 {
		if err := json.Unmarshal(timings, &r.StepTimings); err != nil {
			return nil, fmt.Errorf("step timings: %w", err)
		}
	}
	if len(scores) > 0 && string(scores) != "null" {
		r.Scores = &AuditScores{}
		if err := json.Unmarshal(scores, r.Scores); err != nil {
			return nil, fmt.Errorf("scores: %w", err)
		}
	}
	if len(gate) > 0 && string(gate) != "null" {
		r.Gate = &AuditGate{}
		if err := json.Unmarshal(gate, r.Gate); err != nil {
			return nil, fmt.Errorf("gate: %w", err)
		}
	}
	r.Counts = NewVerdictCounts()
	for k, n := range counts {
		if _, ok := r.Counts[k]; ok {
			r.Counts[k] = n
		}
	}
	r.Claims = []ClaimRecord{}
	return &r, nil
}

// ListAudits returns a keyset page of audits, newest first, without their claims, and the cursor
// of the next page ("" at the end).
func (s *PostgresStore) ListAudits(ctx context.Context, f AuditFilter) ([]AuditRecord, string, error) {
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
		where = append(where, fmt.Sprintf("(a.created_at, a.audit_id) < (%s, %s)", arg(at), arg(id)))
	}
	if f.Since != nil {
		where = append(where, "a.created_at >= "+arg(*f.Since))
	}
	if f.CompletedSince != nil {
		where = append(where, "a.completed_at >= "+arg(*f.CompletedSince))
	}
	eq("a.route", f.Route)
	eq("a.source", f.Source)
	eq("a.status", f.Status)
	eq("a.verdict", f.Verdict)
	eq("a.selection", f.Selection)
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := "SELECT " + auditColumns + auditFrom
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY a.created_at DESC, a.audit_id DESC LIMIT " + arg(limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []AuditRecord{}
	for rows.Next() {
		r, err := scanAudit(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.AuditID)
	}
	return out, next, nil
}

// GetAudit returns one audit with its claims, or ErrNotFound.
func (s *PostgresStore) GetAudit(ctx context.Context, auditID string) (*AuditRecord, error) {
	return s.getAudit(ctx, "a.audit_id = $1", auditID)
}

// LatestAuditForTrace returns the newest audit of a trace with its claims, or ErrNotFound.
func (s *PostgresStore) LatestAuditForTrace(ctx context.Context, traceID string) (*AuditRecord, error) {
	return s.getAudit(ctx, "a.trace_id = $1", traceID)
}

func (s *PostgresStore) getAudit(ctx context.Context, where, id string) (*AuditRecord, error) {
	record, err := scanAudit(s.pool.QueryRow(ctx,
		"SELECT "+auditColumns+auditFrom+" WHERE "+where+" ORDER BY a.created_at DESC, a.audit_id DESC LIMIT 1", id))
	if err != nil {
		return nil, err // ErrNotFound when absent
	}
	rows, err := s.pool.Query(ctx, `
		SELECT claim, status, confidence, evidence, reasoning, decided_by, verdict_probabilities,
			probability_source, span_start, span_end, span_method, quote, evidence_spans
		FROM audit_claims WHERE audit_id = $1 ORDER BY position, id`, record.AuditID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			c                      ClaimRecord
			evidence, probs, spans []byte
			spanStart, spanEnd     *int32
		)
		if err := rows.Scan(&c.Claim, &c.Status, &c.Confidence, &evidence, &c.Reasoning, &c.DecidedBy, &probs,
			&c.ProbabilitySource, &spanStart, &spanEnd, &c.SpanMethod, &c.Quote, &spans); err != nil {
			return nil, err
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &c.Evidence); err != nil {
				return nil, fmt.Errorf("claim evidence: %w", err)
			}
		}
		c.Evidence = nonNil(c.Evidence)
		if len(probs) > 0 {
			if err := json.Unmarshal(probs, &c.VerdictProbabilities); err != nil {
				return nil, fmt.Errorf("claim probabilities: %w", err)
			}
		}
		if len(spans) > 0 {
			if err := json.Unmarshal(spans, &c.EvidenceSpans); err != nil {
				return nil, fmt.Errorf("claim evidence spans: %w", err)
			}
		}
		c.SpanStart, c.SpanEnd = intPtr(spanStart), intPtr(spanEnd)
		record.Claims = append(record.Claims, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if record.Costs, err = s.auditCosts(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// auditCostEvents are the cost_events rows of record.Costs (one per model call), linked to the
// audit by audit_id and stamped when it completed (D45; created_at for a record without a
// completion time). A call no price covers keeps a NULL cost.
func auditCostEvents(record *AuditRecord, traceID string, createdAt time.Time) []CostEvent {
	occurred := createdAt
	if record.CompletedAt != nil && !record.CompletedAt.IsZero() {
		occurred = *record.CompletedAt
	}
	out := make([]CostEvent, 0, len(record.Costs))
	for _, c := range record.Costs {
		in, outTokens, latency := c.InputTokens, c.OutputTokens, c.LatencyMs
		e := CostEvent{
			OccurredAt: occurred, TraceID: traceID, AuditID: record.AuditID, Route: derefOr(record.Route, ""),
			Component: c.Component, Kind: defaultString(c.Kind, CostKindLLM), Provider: c.Provider, Model: c.Model,
			InputTokens: &in, OutputTokens: &outTokens, CostSource: defaultString(c.CostSource, "unknown"),
			LatencyMs: &latency, Status: c.Status, Selection: derefOr(record.Selection, ""),
		}
		if e.CostSource != "unknown" {
			usd := c.CostUSD
			e.CostUSD = &usd
		}
		if c.Questions > 0 {
			q := c.Questions
			e.Questions = &q
		}
		out = append(out, e)
	}
	return out
}

// auditCosts reads the audit's model calls back from cost_events: the rows linked to it by
// audit_id (D45) and, for an audit stored before 0005, its trace's deep-audit rows stamped with
// the audit's created_at (the pre-0005 rule, which had no audit id).
func (s *PostgresStore) auditCosts(ctx context.Context, record *AuditRecord) ([]CostRecord, error) {
	const cols = `id, component, kind, COALESCE(provider, ''), COALESCE(model, ''), COALESCE(input_tokens, 0),
		COALESCE(output_tokens, 0), COALESCE(cost_usd, 0)::float8, cost_source, COALESCE(latency_ms, 0),
		COALESCE(status, ''), COALESCE(questions, 0)`
	rows, err := s.pool.Query(ctx, `
		SELECT `+cols+` FROM cost_events WHERE audit_id = $1
		UNION ALL
		SELECT `+cols+` FROM cost_events
		WHERE audit_id IS NULL AND trace_id = $2 AND occurred_at = $3 AND component LIKE 'deep\_audit%'
		ORDER BY 1`, record.AuditID, record.TraceID, record.CreatedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CostRecord
	for rows.Next() {
		var (
			id                            int64
			c                             CostRecord
			input, output, latency, quest int32
		)
		if err := rows.Scan(&id, &c.Component, &c.Kind, &c.Provider, &c.Model, &input, &output, &c.CostUSD, &c.CostSource,
			&latency, &c.Status, &quest); err != nil {
			return nil, err
		}
		c.InputTokens, c.OutputTokens, c.LatencyMs, c.Questions = int64(input), int64(output), int64(latency), int(quest)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DropTraceAudit records that a trace's audit was not queued: its audit status becomes dropped
// and, when flag is not empty, the flag joins the trace's flags once (llm_budget for an audit the
// day's LLM cap refused, D48). A trace that was never recorded is not an error.
func (s *PostgresStore) DropTraceAudit(ctx context.Context, traceID, flag string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE traces SET audit_status = $2,
			flags = CASE WHEN $3 = '' OR $3 = ANY(flags) THEN flags ELSE array_append(flags, $3::TEXT) END,
			updated_at = now()
		WHERE trace_id = $1`, traceID, TraceAuditDropped, flag)
	return err
}

// clampInt32 fits a count into an INT column.
func clampInt32(v int64) int32 {
	return int32(min(max(v, 0), math.MaxInt32)) // #nosec G115 -- clamped to the int32 range first
}

// Summary returns the audit as the audit list shows it.
func (r *AuditRecord) Summary() AuditSummary {
	counts := r.Counts
	if counts == nil {
		counts = NewVerdictCounts()
		for _, c := range r.Claims {
			if _, ok := counts[c.Status]; ok {
				counts[c.Status]++
			}
		}
	}
	var gateAction *string
	if r.Gate != nil && r.Gate.Action != "" {
		a := r.Gate.Action
		gateAction = &a
	}
	var query *string
	if r.UserQuery != nil {
		query = Preview(*r.UserQuery, QuestionPreviewRunes)
	}
	return AuditSummary{
		AuditID: r.AuditID, TraceID: r.TraceID, CreatedAt: utc(r.CreatedAt), Route: r.Route,
		Source: defaultString(r.Source, defaultAuditSource), Model: nilIfEmpty(r.Model),
		Status: defaultString(r.Status, AuditCompleted), Verdict: r.Verdict, NotAssessedReason: r.NotAssessedReason,
		TrustScore: r.FaithfulnessScore, Grade: r.Grade, HallucinationDetected: r.HallucinationDetected,
		VerdictCounts: counts, ProcessingMs: r.ProcessingTimeMs, Judge: jsonOrNull(r.Judge), GateAction: gateAction,
		Selection: r.Selection, InclusionProb: r.InclusionProb, QueryPreview: query,
	}
}

// Detail returns the audit with its claims as the API shows it.
func (r *AuditRecord) Detail() AuditDetail {
	d := AuditDetail{
		AuditSummary: r.Summary(),
		UserQuery:    r.UserQuery, LLMResponse: r.LLMResponse, Claims: make([]ClaimView, len(r.Claims)),
		Scores: r.Scores, DegradedReasons: nonNil(r.DegradedReasons), ReasoningTrace: r.ReasoningTrace,
		StepTimings: r.StepTimings, Gate: r.Gate, EvidenceSummary: jsonOrNull(r.EvidenceSummary),
		ContextUsed: jsonOr(r.ContextUsed, "[]"), ScoringVersion: defaultString(r.ScoringVersion, scoringVersionLegacy),
		Costs: r.Costs,
	}
	if d.Costs == nil {
		d.Costs = []CostRecord{}
	}
	if d.StepTimings == nil {
		d.StepTimings = map[string]int64{}
	}
	if r.ErrorKind != nil || r.Error != nil {
		d.Error = &AuditError{Kind: derefOr(r.ErrorKind, "internal"), Message: derefOr(r.Error, "")}
	}
	if r.UnsupportedPolicy != nil && (*r.UnsupportedPolicy == unsupportedPolicyHall || *r.UnsupportedPolicy == unsupportedPolicyUnv) {
		d.UnsupportedPolicy = r.UnsupportedPolicy
	}
	for i, c := range r.Claims {
		d.Claims[i] = c.view(i)
	}
	return d
}

// view returns the claim as the API shows it. Legacy claims have no decided_by or probability
// source; they were decided by the LLM judge unless the judge could not decide.
func (c *ClaimRecord) view(index int) ClaimView {
	decided := c.Status != "UNKNOWN" && c.Status != "NOT_CHECKED"
	decidedBy := decidedByNone
	if decided {
		decidedBy = decidedByLLM
	}
	if c.DecidedBy != nil && slices.Contains([]string{"llm", "rule", "none", "jev", "jev>llm", "jev>llm_failed"}, *c.DecidedBy) {
		decidedBy = *c.DecidedBy
	}
	source := probabilityFromLLM
	if c.ProbabilitySource != nil && slices.Contains([]string{"llm_confidence", "llm_calibrated", "rule", "jev", "jev_calibrated"}, *c.ProbabilitySource) {
		source = *c.ProbabilitySource
	}
	probs := map[string]float64{}
	for k, v := range c.VerdictProbabilities {
		if slices.Contains([]string{"supported", "partially_supported", "contradicted", "unsupported"}, k) {
			probs[k] = v
		}
	}
	v := ClaimView{
		Index: index, Claim: c.Claim, Status: c.Status, Confidence: c.Confidence, Evidence: nonNil(c.Evidence),
		Reasoning: c.Reasoning, DecidedBy: decidedBy, VerdictProbabilities: probs, ProbabilitySource: source,
		Quote: c.Quote, EvidenceSpans: c.EvidenceSpans,
	}
	if v.EvidenceSpans == nil {
		v.EvidenceSpans = []EvidenceSpan{}
	}
	if c.SpanStart != nil && c.SpanEnd != nil && *c.SpanStart != unalignedSpan && *c.SpanEnd != unalignedSpan &&
		*c.SpanStart <= *c.SpanEnd {
		method := derefOr(c.SpanMethod, "exact")
		if !slices.Contains([]string{"exact", "normalized", "fuzzy"}, method) {
			method = "exact"
		}
		v.Span = &ClaimSpan{Start: *c.SpanStart, End: *c.SpanEnd, Method: method}
	}
	return v
}

// Ping checks the database connection.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migration returns the newest applied migration file name.
func (s *PostgresStore) Migration(ctx context.Context) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT filename FROM schema_migrations ORDER BY filename DESC LIMIT 1`).Scan(&name)
	return name, err
}

// Close closes the connection pool.
func (s *PostgresStore) Close() { s.pool.Close() }

func marshalOrNil(v any) (json.RawMessage, error) {
	switch x := v.(type) {
	case *AuditScores:
		if x == nil {
			return nil, nil
		}
	case *AuditGate:
		if x == nil {
			return nil, nil
		}
	case map[string]float64:
		if x == nil {
			return nil, nil
		}
	case []EvidenceSpan:
		if x == nil {
			return nil, nil
		}
	}
	return json.Marshal(v)
}

func defaultString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}
