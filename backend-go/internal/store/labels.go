package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Human labels (SPEC §2.3 labels; §2.4 rows 39-41; D59, D60). A label is one admin's
// judgement of one target of a trace:
//
//   - "<id>@<version>": should_fire or should_not_fire, the calibration labels of that evaluator;
//   - "claim:<position>": the verdict a claim of the trace's deep audit should have had;
//   - "response": hallucinated or grounded (reserved).
//
// Each admin has at most one label per target of a trace (primary key trace_id, target,
// labeled_by). The label freezes what was judged: the trace's body (sample, nulled by retention
// at sample_expires_at), and the evaluator's raw risk and the decision-model snapshot of the
// trace's attempt-0 decision, so a fit never depends on rows that retention deletes.

// Label values (labels.label).
const (
	LabelShouldFire    = "should_fire"
	LabelShouldNotFire = "should_not_fire"
	LabelHallucinated  = "hallucinated"
	LabelGrounded      = "grounded"
)

// LabelRecord is one labels row as PUT /api/labels writes it.
type LabelRecord struct {
	TraceID   string
	Target    string
	Label     string
	LabeledBy string
	Note      *string
	// Sample is the frozen body (LabelSample), SampleExpiresAt when retention nulls it.
	Sample          json.RawMessage
	SampleExpiresAt time.Time
	// RiskRaw and Snapshot come from the trace's attempt-0 decision (LabelContext).
	RiskRaw  *float64
	Snapshot *string
}

// LabelSample is labels.sample: the inputs the label judged, frozen from the trace body.
type LabelSample struct {
	Messages           json.RawMessage `json:"messages"`
	UserRequest        *string         `json:"user_request"`
	SystemInstructions *string         `json:"system_instructions"`
	ResponseText       *string         `json:"response_text"`
	FinalResponseText  *string         `json:"final_response_text"`
	Evidence           json.RawMessage `json:"evidence"`
	Truncated          bool            `json:"truncated"`
}

// SampleOf freezes a trace body into a label sample.
func SampleOf(b *BodyRecord) LabelSample {
	return LabelSample{
		Messages: jsonOr(b.Messages, "[]"), UserRequest: b.UserRequest, SystemInstructions: b.SystemInstructions,
		ResponseText: b.ResponseText, FinalResponseText: b.FinalResponseText, Evidence: jsonOr(b.Evidence, "[]"),
		Truncated: b.Truncated,
	}
}

// LabelContext is what a trace's attempt-0 decision says about a label target.
type LabelContext struct {
	// Decided is false when the trace has no attempt-0 decision.
	Decided bool
	// Snapshot is the decision-model snapshot of the decision (nil when it reported none).
	Snapshot *string
	// Asked reports whether the decision has a result for the evaluator ref (labels of an
	// "<id>@<version>" target); RiskRaw is that result's raw risk, nil when it has none.
	Asked   bool
	RiskRaw *float64
}

// LabelContextOf reads the attempt-0 decision of a trace for a label of ref ("" for a target that
// is not an evaluator).
func (s *PostgresStore) LabelContextOf(ctx context.Context, traceID, ref string) (LabelContext, error) {
	var lc LabelContext
	var risk *float64
	err := s.pool.QueryRow(ctx, `
		SELECT gd.decision_snapshot, `+decisionEvaluatorsSQL+` ? $2,
			CASE WHEN jsonb_typeof(`+decisionEvaluatorsSQL+` -> $2 -> 'risk_raw') = 'number'
				THEN (`+decisionEvaluatorsSQL+` -> $2 ->> 'risk_raw')::float8 END
		FROM gate_decisions gd WHERE gd.trace_id = $1 AND gd.attempt = 0`, traceID, ref).Scan(&lc.Snapshot, &lc.Asked, &risk)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return LabelContext{}, nil
	case err != nil:
		return LabelContext{}, fmt.Errorf("label context: %w", err)
	}
	lc.Decided, lc.RiskRaw = true, risk
	return lc, nil
}

// TraceHasClaim reports whether the deep audit of a trace has a claim at position.
func (s *PostgresStore) TraceHasClaim(ctx context.Context, traceID string, position int) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM traces t JOIN audit_claims c ON c.audit_id = t.audit_id
		WHERE t.trace_id = $1 AND c.position = $2)`, traceID, position).Scan(&ok)
	return ok, err
}

// UpsertLabel stores an admin's label of a trace target. Saving the same label and note again
// changes nothing; a changed label or note replaces the row's judgement (and its time), and
// re-freezes the sample.
func (s *PostgresStore) UpsertLabel(ctx context.Context, l LabelRecord) error {
	var risk *float32
	if l.RiskRaw != nil {
		r := float32(*l.RiskRaw)
		risk = &r
	}
	var sample any
	if len(l.Sample) > 0 {
		sample = l.Sample
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO labels (trace_id, target, label, labeled_by, note, label_source, sample, sample_expires_at, risk_raw, snapshot)
		VALUES ($1, $2, $3, $4, $5, 'human', $6, $7, $8, $9)
		ON CONFLICT (trace_id, target, labeled_by) DO UPDATE SET
			label = EXCLUDED.label, note = EXCLUDED.note, created_at = now(), sample = EXCLUDED.sample,
			sample_expires_at = EXCLUDED.sample_expires_at, risk_raw = EXCLUDED.risk_raw, snapshot = EXCLUDED.snapshot
		WHERE labels.label IS DISTINCT FROM EXCLUDED.label OR labels.note IS DISTINCT FROM EXCLUDED.note`,
		l.TraceID, l.Target, l.Label, l.LabeledBy, l.Note, sample, l.SampleExpiresAt, risk, l.Snapshot)
	if err != nil {
		return fmt.Errorf("upsert label: %w", err)
	}
	return nil
}

// DeleteLabel removes an admin's label of a trace target; it reports whether there was one.
func (s *PostgresStore) DeleteLabel(ctx context.Context, traceID, target, labeledBy string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM labels WHERE trace_id = $1 AND target = $2 AND labeled_by = $3`, traceID, target, labeledBy)
	if err != nil {
		return false, fmt.Errorf("delete label: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// TraceLabels returns the labels of a trace (TraceDetail.labels), by target, then labeller.
func (s *PostgresStore) TraceLabels(ctx context.Context, traceID string) ([]LabelView, error) {
	rows, err := s.pool.Query(ctx, `SELECT target, label, labeled_by, created_at, note FROM labels
		WHERE trace_id = $1 ORDER BY target, labeled_by`, traceID)
	if err != nil {
		return nil, fmt.Errorf("trace labels: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LabelView, error) {
		var l LabelView
		err := row.Scan(&l.Target, &l.Label, &l.LabeledBy, &l.CreatedAt, &l.Note)
		l.CreatedAt = l.CreatedAt.UTC()
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("trace labels: %w", err)
	}
	if out == nil {
		out = []LabelView{}
	}
	return out, nil
}

// LabelCountFilter selects the labels a calibration fit of one target uses.
type LabelCountFilter struct {
	// Target is an evaluator ref ("<id>@<version>").
	Target string
	// Snapshot keeps one decision-model snapshot; "" keeps every snapshot.
	Snapshot string
	// From and To bound the label's time (the decision's for uniform audits).
	From, To time.Time
	// IncludeMock counts uniform audits of simulated traffic (as ReliabilityFilter.IncludeMock).
	IncludeMock bool
}

// LabelCounts are the labels of one calibration target.
type LabelCounts struct {
	// N, Pos and Neg count the human and public-dataset labels with a raw risk: should_fire
	// labels are positives, should_not_fire negatives (SPEC §2.3 labels).
	N   int
	Pos int
	Neg int
	// Proxy counts the uniform-sample deep audits that label the target (a proxy, never measured).
	Proxy int
}

// CalibrationLabelCounts counts the labels a fit of f.Target would use: exactly the samples
// ReliabilitySamples returns for the same filter, by source.
func (s *PostgresStore) CalibrationLabelCounts(ctx context.Context, f LabelCountFilter) (LabelCounts, error) {
	var c LabelCounts
	err := s.pool.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE l.label = 'should_fire'), count(*) FILTER (WHERE l.label = 'should_not_fire')
		FROM labels l
		WHERE l.target = $1 AND l.label IN ('should_fire', 'should_not_fire') AND l.risk_raw IS NOT NULL
			AND l.created_at >= $2 AND l.created_at < $3 AND ($4 = '' OR l.snapshot = $4)`,
		f.Target, f.From, f.To, f.Snapshot).Scan(&c.N, &c.Pos, &c.Neg)
	if err != nil {
		return c, fmt.Errorf("label counts: %w", err)
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*)
		FROM gate_decisions gd
		JOIN traces t ON t.trace_id = gd.trace_id
		JOIN audits a ON a.audit_id = t.audit_id
		WHERE gd.attempt = 0 AND jsonb_typeof(`+decisionEvaluatorsSQL+` -> $1 -> 'risk_raw') = 'number'
			AND a.selection = 'uniform' AND a.verdict IN ('HALLUCINATED', 'GROUNDED')
			AND gd.created_at >= $2 AND gd.created_at < $3 AND ($4 = '' OR gd.decision_snapshot = $4)
			AND ($5::BOOLEAN OR NOT `+mockTraceSQL+`)`, f.Target, f.From, f.To, f.Snapshot, f.IncludeMock).Scan(&c.Proxy)
	if err != nil {
		return c, fmt.Errorf("proxy label counts: %w", err)
	}
	return c, nil
}

// LabelQueueFilter selects the label queue of one admin (GET /api/labels/queue, D60).
type LabelQueueFilter struct {
	// LabeledBy is the caller: decisions it already labelled for that evaluator are left out.
	LabeledBy string
	// Evaluator keeps one evaluator ref; "" keeps every evaluator of each decision.
	Evaluator string
	From, To  time.Time
	Limit     int
	// WarnAt is the warn threshold of each evaluator ref on each route ({route: {ref: warn_at}}),
	// in the route's threshold space; DefaultWarnAt the spec's, by ref, for a route the policy no
	// longer has. A ref in neither orders as if its warn_at were 0.5.
	WarnAt        map[string]map[string]float64
	DefaultWarnAt map[string]float64
	// Now decides which bodies are still live.
	Now time.Time
}

// LabelQueueRow is one item of the label queue: an evaluator's result on an attempt-0 decision.
type LabelQueueRow struct {
	TraceID      string
	CreatedAt    time.Time
	Route        string
	EvaluatorRef string
	RiskRaw      float64
	Level        *string
	UserRequest  *string
	ResponseText *string
	// Evidence is the body's evidence items (JSON array).
	Evidence json.RawMessage
}

// LabelQueue lists the label queue (D60): the evaluator results of attempt-0 decisions in the
// window that have a numeric raw risk and a live body and that the caller has not labelled,
// nearest the flag line first (|risk - warn_at| in the route's threshold space, the stored risk
// or else the raw risk), then newest first.
func (s *PostgresStore) LabelQueue(ctx context.Context, f LabelQueueFilter) ([]LabelQueueRow, error) {
	warn, err := json.Marshal(f.WarnAt)
	if err != nil {
		return nil, err
	}
	defaults, err := json.Marshal(f.DefaultWarnAt)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT gd.trace_id, gd.created_at, gd.route, e.key, (e.value->>'risk_raw')::float8,
			CASE WHEN jsonb_typeof(e.value->'level') = 'string' THEN e.value->>'level' END,
			b.user_request, b.response_text, b.evidence
		FROM gate_decisions gd
		JOIN trace_bodies b ON b.trace_id = gd.trace_id AND b.expires_at > $5
		CROSS JOIN LATERAL jsonb_each(`+decisionEvaluatorsSQL+`) e
		WHERE gd.attempt = 0 AND gd.created_at >= $1 AND gd.created_at < $2
			AND jsonb_typeof(e.value->'risk_raw') = 'number' AND ($3 = '' OR e.key = $3)
			AND NOT EXISTS (SELECT 1 FROM labels l WHERE l.trace_id = gd.trace_id AND l.target = e.key AND l.labeled_by = $4)
		ORDER BY abs(
				CASE WHEN jsonb_typeof(e.value->'risk') = 'number' THEN (e.value->>'risk')::float8 ELSE (e.value->>'risk_raw')::float8 END
				- COALESCE(($6::jsonb -> gd.route ->> e.key)::float8, ($7::jsonb ->> e.key)::float8, 0.5)),
			gd.created_at DESC, gd.trace_id, e.key
		LIMIT $8`, f.From, f.To, f.Evaluator, f.LabeledBy, f.Now, string(warn), string(defaults), f.Limit)
	if err != nil {
		return nil, fmt.Errorf("label queue: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (LabelQueueRow, error) {
		var r LabelQueueRow
		err := row.Scan(&r.TraceID, &r.CreatedAt, &r.Route, &r.EvaluatorRef, &r.RiskRaw, &r.Level,
			&r.UserRequest, &r.ResponseText, &r.Evidence)
		r.CreatedAt = r.CreatedAt.UTC()
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("label queue: %w", err)
	}
	if out == nil {
		out = []LabelQueueRow{}
	}
	return out, nil
}
