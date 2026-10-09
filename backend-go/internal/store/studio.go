package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Evaluator Studio's storage (SPEC §2.4 rows 12, 35, 36 and 38): the outcomes of
// test-bench and dry-run samples (studio_results), the traffic a test bench or a dry-run samples
// from (attempt-0 decisions with a live trace body), and the recorded compile of a decision (the
// decision-request recompile, D63).

// Studio run kinds (studio_results.kind).
const (
	StudioKindTest   = "test"
	StudioKindDryRun = "dry_run"
)

// StudioResult is one studio_results row: one evaluator's outcome on one sample of a run. Text is
// never stored: the sample id names a pasted sample or a trace.
type StudioResult struct {
	RunID        string
	Kind         string
	SampleID     string
	EvaluatorRef string
	// Answer is the decision model's raw answer (JSON), nil when there was none.
	Answer   json.RawMessage
	RiskRaw  *float64
	Risk     *float64
	Level    *string
	Status   *string
	Escalate *string
	Snapshot *string
	// LatencyMs and CostUSD are the sample's (all of its calls).
	LatencyMs *int64
	CostUSD   *float64
	CreatedAt time.Time
}

// InsertStudioResults writes a run's rows in one transaction: all of them or, on an error, none.
// A row already stored (same run, sample and evaluator) is kept as it is.
func (s *PostgresStore) InsertStudioResults(ctx context.Context, rows []StudioResult) error {
	if len(rows) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for i := range rows {
			r := &rows[i]
			if r.RunID == "" || r.SampleID == "" || r.EvaluatorRef == "" {
				return errors.New("store: a studio result needs a run id, a sample id and an evaluator ref")
			}
			if r.Kind != StudioKindTest && r.Kind != StudioKindDryRun {
				return fmt.Errorf("store: unknown studio run kind %q", r.Kind)
			}
			var answer any
			if len(r.Answer) > 0 {
				answer = string(r.Answer)
			}
			batch.Queue(`
				INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref, answer, risk_raw, risk, level,
					status, escalate, snapshot, latency_ms, cost_usd)
				VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11,$12,$13)
				ON CONFLICT (run_id, sample_id, evaluator_ref) DO NOTHING`,
				r.RunID, r.Kind, r.SampleID, r.EvaluatorRef, answer, f32(r.RiskRaw), f32(r.Risk), r.Level,
				r.Status, r.Escalate, r.Snapshot, clampInt32Ptr(r.LatencyMs), storedCost(r.CostUSD))
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert studio results: %w", err)
		}
		return nil
	})
}

// StudioRun returns the rows of a run, by sample id.
func (s *PostgresStore) StudioRun(ctx context.Context, runID string) ([]StudioResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, kind, sample_id, evaluator_ref, answer, risk_raw, risk, level, status, escalate, snapshot,
			latency_ms, cost_usd::float8, created_at
		FROM studio_results WHERE run_id = $1 ORDER BY sample_id, evaluator_ref`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StudioResult{}
	for rows.Next() {
		var (
			r             StudioResult
			riskRaw, risk *float32
			latency       *int32
		)
		if err := rows.Scan(&r.RunID, &r.Kind, &r.SampleID, &r.EvaluatorRef, &r.Answer, &riskRaw, &risk, &r.Level,
			&r.Status, &r.Escalate, &r.Snapshot, &latency, &r.CostUSD, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.RiskRaw, r.Risk, r.CreatedAt = f32ptr(riskRaw), f32ptr(risk), utc(r.CreatedAt)
		if latency != nil {
			ms := int64(*latency)
			r.LatencyMs = &ms
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// f32 is a REAL parameter: nil stays NULL.
func f32(p *float64) *float32 {
	if p == nil {
		return nil
	}
	v := float32(*p)
	return &v
}

// Studio sample filters (StudioSamplesRequest.filter).
const (
	SampleFlagged      = "flagged"
	SampleRandom       = "random"
	SampleDisagreement = "disagreement"
)

// flaggedSQL holds when the attempt-0 decision gd flagged, blocked or fell back, or would have
// (an observe route records the action it would have taken).
const flaggedSQL = `(gd.action IN ('flag','block','fallback') OR gd.would_action IN ('flag','block','fallback'))`

// liveBodySQL holds when trace t's body (alias b) is still kept and has an answer.
const liveBodySQL = `b.expires_at > $1 AND b.response_text IS NOT NULL AND b.response_text <> ''`

// StudioSampleFilter selects the traffic a Studio test bench samples (POST /api/studio/samples):
// attempt-0 decisions since Since on Route ("" = every route) whose trace body is still kept,
// flagged (newest first), at random, or where the evaluators disagreed (newest first).
type StudioSampleFilter struct {
	Route  string
	Since  time.Time
	Filter string
	Limit  int
	// Now is the body-expiry reference.
	Now time.Time
}

// StudioSampleTraces returns the trace ids f selects, at most f.Limit (1-50).
func (s *PostgresStore) StudioSampleTraces(ctx context.Context, f StudioSampleFilter) ([]string, error) {
	var pred, order string
	switch f.Filter {
	case SampleFlagged:
		pred, order = flaggedSQL, "gd.created_at DESC, gd.trace_id DESC"
	case SampleDisagreement:
		pred, order = disagreementSQL, "gd.created_at DESC, gd.trace_id DESC"
	case SampleRandom:
		pred, order = "TRUE", "random()"
	default:
		return nil, fmt.Errorf("store: unknown sample filter %q", f.Filter)
	}
	limit := min(max(f.Limit, 1), 50)
	rows, err := s.pool.Query(ctx, `
		SELECT gd.trace_id FROM gate_decisions gd
		JOIN trace_bodies b ON b.trace_id = gd.trace_id
		WHERE gd.attempt = 0 AND gd.created_at >= $2 AND ($3 = '' OR gd.route = $3) AND `+liveBodySQL+`
			AND `+pred+`
		ORDER BY `+order+` LIMIT $4`,
		f.Now.UTC(), f.Since.UTC(), f.Route, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DryRunFilter selects a route's past traffic for a Studio dry-run: attempt-0 decisions since
// Since on Route.
type DryRunFilter struct {
	Route string
	Since time.Time
	// Now is the body-expiry reference.
	Now time.Time
}

// DryRunPopulation counts the attempt-0 decisions of f (the population a dry-run extrapolates to)
// and those whose body is still kept with an answer (what it can sample).
func (s *PostgresStore) DryRunPopulation(ctx context.Context, f DryRunFilter) (population, withBody int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE b.trace_id IS NOT NULL AND `+liveBodySQL+`)
		FROM gate_decisions gd
		LEFT JOIN trace_bodies b ON b.trace_id = gd.trace_id
		WHERE gd.attempt = 0 AND gd.created_at >= $2 AND gd.route = $3`,
		f.Now.UTC(), f.Since.UTC(), f.Route).Scan(&population, &withBody)
	return population, withBody, err
}

// DryRunCandidate is one trace a dry-run evaluates: its body, its recorded evaluator levels and
// its deep-audit verdict.
type DryRunCandidate struct {
	TraceID string
	Body    BodyRecord
	// Levels maps each evaluator ref of the recorded decision to its level (nil: not assessed).
	Levels       map[string]*string
	AuditVerdict *string
}

// DryRunSample draws up to limit traces of f with a live body, uniformly at random.
func (s *PostgresStore) DryRunSample(ctx context.Context, f DryRunFilter, limit int) ([]DryRunCandidate, error) {
	if limit <= 0 {
		return []DryRunCandidate{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT gd.trace_id, b.messages, b.user_request, b.system_instructions, b.response_text, b.final_response_text,
			b.evidence, b.rules, b.bytes, b.truncated, b.inclusion_prob, b.expires_at, `+auditVerdictSQL+`,
			COALESCE((SELECT jsonb_object_agg(e.key, e.value->'level') FROM jsonb_each(`+decisionEvaluatorsSQL+`) e), '{}'::jsonb)
		FROM gate_decisions gd
		JOIN trace_bodies b ON b.trace_id = gd.trace_id
		LEFT JOIN traces t ON t.trace_id = gd.trace_id
		LEFT JOIN audits a ON a.audit_id = t.audit_id
		WHERE gd.attempt = 0 AND gd.created_at >= $2 AND gd.route = $3 AND `+liveBodySQL+`
		ORDER BY random() LIMIT $4`,
		f.Now.UTC(), f.Since.UTC(), f.Route, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DryRunCandidate{}
	for rows.Next() {
		var (
			c      DryRunCandidate
			rules  []byte
			prob   float32
			levels []byte
		)
		b := &c.Body
		if err := rows.Scan(&c.TraceID, &b.Messages, &b.UserRequest, &b.SystemInstructions, &b.ResponseText,
			&b.FinalResponseText, &b.Evidence, &rules, &b.Bytes, &b.Truncated, &prob, &b.ExpiresAt, &c.AuditVerdict,
			&levels); err != nil {
			return nil, err
		}
		b.TraceID, b.InclusionProb = c.TraceID, Float64From32(prob)
		if len(rules) > 0 {
			if err := json.Unmarshal(rules, &b.Rules); err != nil {
				return nil, fmt.Errorf("trace %s: body rules: %w", c.TraceID, err)
			}
		}
		if err := json.Unmarshal(levels, &c.Levels); err != nil {
			return nil, fmt.Errorf("trace %s: evaluator levels: %w", c.TraceID, err)
		}
		if c.Levels == nil {
			c.Levels = map[string]*string{}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordedCompile is what a gate decision recorded about its compile (D63): enough to recompile
// it from the trace body and compare the request hashes.
type RecordedCompile struct {
	TraceID string
	Attempt int
	Route   string
	// PolicyVersion is the policy version the trace was gated under, nil when unknown.
	PolicyVersion *int64
	// ModelRequested is the decision model asked (nil when none was asked).
	ModelRequested *string
	// CPT is the chars-per-token the compile used, nil when it did not compile.
	CPT *float64
	// RequestSHA256 are the recorded request hashes, in request order.
	RequestSHA256 []string
}

// GetRecordedCompile returns the recorded compile of a trace's decision attempt, or ErrNotFound.
// cpt comes from the decision document, which keeps the float64 the compile used (the cpt column
// is a REAL).
func (s *PostgresStore) GetRecordedCompile(ctx context.Context, traceID string, attempt int) (*RecordedCompile, error) {
	r := RecordedCompile{TraceID: traceID, Attempt: attempt}
	err := s.pool.QueryRow(ctx, `
		SELECT route, policy_version, decision_model_requested,
			CASE WHEN jsonb_typeof(decision->'compile'->'cpt') = 'number' THEN (decision->'compile'->>'cpt')::float8 END,
			COALESCE(request_sha256, '{}')
		FROM gate_decisions WHERE trace_id = $1 AND attempt = $2`, traceID, attempt).Scan(
		&r.Route, &r.PolicyVersion, &r.ModelRequested, &r.CPT, &r.RequestSHA256)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
