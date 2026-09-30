package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Retention (SPEC §2.3 retention, D17): an hourly job deletes, or nulls, what has outlived its
// retention, in adaptive batches. Each batch runs in its own transaction under
// SET LOCAL statement_timeout; a batch that times out is tried again at half the size, down to
// minRetentionBatch, below which the rule waits for the next run.
//
//	traces (with any body left), gate_decisions, audits    deleted after RETENTION_DAYS (claims go with audits)
//	trace_bodies                                           deleted at expires_at; the trace is flagged body_expired
//	audit text (query, answer, context, claim texts)       nulled at text_expires_at (text_redacted)
//	labels.sample                                          nulled at sample_expires_at
//	cost_events                                            deleted after COST_RETENTION_DAYS
//	studio_results                                         deleted after 30 days
//
// Policy and evaluator versions, calibrations, label rows and the admin audit log are kept.

// Retention defaults.
const (
	DefaultRetentionBatch   = 5000
	DefaultRetentionTimeout = 5 * time.Second
	StudioRetentionDays     = 30
	minRetentionBatch       = 50
)

// Retention rules (RetentionReport keys).
const (
	RetainTraces        = "traces"
	RetainDecisions     = "gate_decisions"
	RetainAudits        = "audits"
	RetainBodies        = "trace_bodies"
	RetainAuditText     = "audit_text"
	RetainLabelSamples  = "label_samples"
	RetainCostEvents    = "cost_events"
	RetainStudioResults = "studio_results"
)

// RetentionConfig sets one retention run.
type RetentionConfig struct {
	// Days is TRUTHTABLE_RETENTION_DAYS (traces, gate decisions, audits); CostDays is
	// TRUTHTABLE_COST_RETENTION_DAYS; StudioDays keeps Studio results. 0 uses the default (30, 400
	// and 30).
	Days, CostDays, StudioDays int
	// Batch is the first batch size (DefaultRetentionBatch); Timeout bounds each batch
	// (DefaultRetentionTimeout).
	Batch   int
	Timeout time.Duration
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

// RetentionReport is what a run did: the rows each rule deleted or nulled, and how far the job is
// behind afterwards (the age of the oldest row past its retention that is still stored).
type RetentionReport struct {
	Rows map[string]int64
	Lag  time.Duration
}

// retentionRule is one rule: a batch statement over ($1 cutoff, $2 batch size) that returns how
// many rows it handled, and a statement returning the due time of the oldest row it has left.
type retentionRule struct {
	name, batch, oldest string
	cutoff              time.Time
	// age says whether oldest returns a creation time (due at cutoff) or a due time itself.
	age bool
}

func (c RetentionConfig) rules(now time.Time) []retentionRule {
	days := func(n, def int) time.Time {
		if n <= 0 {
			n = def
		}
		return now.AddDate(0, 0, -n)
	}
	keep, cost, studio := days(c.Days, 30), days(c.CostDays, 400), days(c.StudioDays, StudioRetentionDays)
	return []retentionRule{
		{name: RetainBodies, cutoff: now, batch: `
			WITH gone AS (
				DELETE FROM trace_bodies WHERE trace_id IN (
					SELECT trace_id FROM trace_bodies WHERE expires_at <= $1 ORDER BY expires_at LIMIT $2)
				RETURNING trace_id),
			flagged AS (
				UPDATE traces t SET flags = array_append(t.flags, 'body_expired') FROM gone
				WHERE t.trace_id = gone.trace_id AND NOT ('body_expired' = ANY(t.flags)) RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(expires_at) FROM trace_bodies WHERE expires_at <= $1`},
		{name: RetainAuditText, cutoff: now, batch: `
			WITH redacted AS (
				UPDATE audits SET user_query = NULL, llm_response = NULL, context_used = NULL, reasoning_trace = '',
					text_redacted = true
				WHERE audit_id IN (
					SELECT audit_id FROM audits WHERE text_expires_at <= $1 AND NOT text_redacted LIMIT $2)
				RETURNING audit_id),
			claims AS (
				UPDATE audit_claims c SET claim = NULL, quote = NULL, reasoning = NULL, evidence = NULL, evidence_spans = NULL
				FROM redacted WHERE c.audit_id = redacted.audit_id RETURNING 1)
			SELECT count(*) FROM redacted`,
			oldest: `SELECT min(text_expires_at) FROM audits WHERE text_expires_at <= $1 AND NOT text_redacted`},
		{name: RetainLabelSamples, cutoff: now, batch: `
			WITH nulled AS (
				UPDATE labels SET sample = NULL WHERE (trace_id, target, labeled_by) IN (
					SELECT trace_id, target, labeled_by FROM labels
					WHERE sample IS NOT NULL AND sample_expires_at <= $1 LIMIT $2)
				RETURNING 1)
			SELECT count(*) FROM nulled`,
			oldest: `SELECT min(sample_expires_at) FROM labels WHERE sample IS NOT NULL AND sample_expires_at <= $1`},
		{name: RetainDecisions, cutoff: keep, age: true, batch: `
			WITH gone AS (
				DELETE FROM gate_decisions WHERE id IN (
					SELECT id FROM gate_decisions WHERE created_at < $1 ORDER BY created_at LIMIT $2)
				RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(created_at) FROM gate_decisions WHERE created_at < $1`},
		{name: RetainAudits, cutoff: keep, age: true, batch: `
			WITH gone AS (
				DELETE FROM audits WHERE audit_id IN (
					SELECT audit_id FROM audits WHERE created_at < $1 ORDER BY created_at LIMIT $2)
				RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(created_at) FROM audits WHERE created_at < $1`},
		{name: RetainTraces, cutoff: keep, age: true, batch: `
			WITH gone AS (
				DELETE FROM traces WHERE trace_id IN (
					SELECT trace_id FROM traces WHERE created_at < $1 ORDER BY created_at LIMIT $2)
				RETURNING trace_id),
			bodies AS (
				DELETE FROM trace_bodies b USING gone WHERE b.trace_id = gone.trace_id RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(created_at) FROM traces WHERE created_at < $1`},
		{name: RetainCostEvents, cutoff: cost, age: true, batch: `
			WITH gone AS (
				DELETE FROM cost_events WHERE id IN (
					SELECT id FROM cost_events WHERE occurred_at < $1 ORDER BY occurred_at LIMIT $2)
				RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(occurred_at) FROM cost_events WHERE occurred_at < $1`},
		{name: RetainStudioResults, cutoff: studio, age: true, batch: `
			WITH gone AS (
				DELETE FROM studio_results WHERE (run_id, sample_id, evaluator_ref) IN (
					SELECT run_id, sample_id, evaluator_ref FROM studio_results WHERE created_at < $1 LIMIT $2)
				RETURNING 1)
			SELECT count(*) FROM gone`,
			oldest: `SELECT min(created_at) FROM studio_results WHERE created_at < $1`},
	}
}

// RunRetention runs every retention rule once and reports what it did and the lag left. A rule
// that fails is logged in the returned error and the others still run; the lag then shows what is
// left behind. ctx ending stops the run.
func (s *PostgresStore) RunRetention(ctx context.Context, c RetentionConfig) (RetentionReport, error) {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Batch <= 0 {
		c.Batch = DefaultRetentionBatch
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultRetentionTimeout
	}
	now := c.Now().UTC()
	rep := RetentionReport{Rows: map[string]int64{}}
	var errs []error
	for _, r := range c.rules(now) {
		n, err := s.retain(ctx, r, c.Batch, c.Timeout)
		rep.Rows[r.name] = n
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("retention %s: %w", r.name, err))
		}
	}
	lag, err := s.retentionLag(ctx, c.rules(now), now)
	if err != nil {
		errs = append(errs, fmt.Errorf("retention lag: %w", err))
	}
	rep.Lag = lag
	return rep, errors.Join(errs...)
}

// retain runs one rule's batches until one handles fewer rows than its size. A batch that times
// out is tried again at half the size; errStillBehind ends the rule when even minRetentionBatch
// times out.
func (s *PostgresStore) retain(ctx context.Context, r retentionRule, batch int, timeout time.Duration) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.retentionBatch(ctx, r.batch, r.cutoff, batch, timeout)
		switch {
		case isStatementTimeout(err) && batch > minRetentionBatch:
			batch = max(batch/2, minRetentionBatch)
			continue
		case isStatementTimeout(err):
			return total, errStillBehind
		case err != nil:
			return total, err
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
	}
}

// errStillBehind ends a rule whose smallest batch still timed out; the next run tries again.
var errStillBehind = errors.New("the smallest batch timed out; the rest waits for the next run")

// retentionBatch runs one batch statement in its own transaction under statement_timeout.
func (s *PostgresStore) retentionBatch(ctx context.Context, sql string, cutoff time.Time, n int, timeout time.Duration) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
		return 0, err
	}
	var handled int64
	if err := tx.QueryRow(ctx, sql, cutoff, n).Scan(&handled); err != nil {
		return 0, err
	}
	return handled, tx.Commit(ctx)
}

// retentionLag is the age of the oldest row past its retention still stored, over every rule.
func (s *PostgresStore) retentionLag(ctx context.Context, rules []retentionRule, now time.Time) (time.Duration, error) {
	var lag time.Duration
	for _, r := range rules {
		var oldest *time.Time
		if err := s.pool.QueryRow(ctx, r.oldest, r.cutoff).Scan(&oldest); err != nil {
			return lag, fmt.Errorf("%s: %w", r.name, err)
		}
		if oldest == nil {
			continue
		}
		due := now.Sub(*oldest)
		if r.age {
			due = r.cutoff.Sub(*oldest)
		}
		lag = max(lag, due)
	}
	return lag, nil
}

// isStatementTimeout reports a statement cancelled by statement_timeout (SQLSTATE 57014).
func isStatementTimeout(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "57014"
}
