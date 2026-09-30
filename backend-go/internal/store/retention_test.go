package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// exec runs a statement against the test schema.
func exec(t *testing.T, s *PostgresStore, sql string, args ...any) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// count runs a count query against the test schema.
func count(t *testing.T, s *PostgresStore, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestRetentionRemovesWhatExpired(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -40)

	// A trace past RETENTION_DAYS, with its decision, body and audit; three recent traces whose
	// body expired or did not, and one of them audited with expired text.
	var ids []string
	for _, at := range []time.Time{old, old, now, now, now} {
		start := pendingTrace("support-bot", at)
		tr, dec := decided(start, "flag", "block", "contradicted", 0.9, []string{"contradiction:critical"})
		expires := now.Add(-time.Hour)
		if len(ids) == 4 {
			expires = now.Add(time.Hour)
		}
		if err := s.RecordDecision(ctx, tr, dec, body(start.TraceID, expires)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, start.TraceID)
	}
	exec(t, s, `UPDATE gate_decisions SET created_at = $2 WHERE trace_id = ANY($1)`, ids[:2], old)

	oldAudit := sampleAudit(ids[0], 0.3, true)
	oldAudit.TraceID, oldAudit.CreatedAt = ids[0], old
	expiredText := sampleAudit(ids[2], 0.3, true)
	past := now.Add(-time.Minute)
	expiredText.TraceID, expiredText.TextExpiresAt = ids[2], &past
	keptText := sampleAudit(ids[3], 0.9, false)
	future := now.Add(time.Hour)
	keptText.TraceID, keptText.TextExpiresAt = ids[3], &future
	for _, a := range []*AuditRecord{oldAudit, expiredText, keptText} {
		if err := s.SaveAudit(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, s, `INSERT INTO labels (trace_id, target, label, labeled_by, sample, sample_expires_at) VALUES
		($1, 'groundedness@1', 'should_fire', 'ops', '{"q":"x"}', $2), ($1, 'contradiction@1', 'should_fire', 'ops', '{"q":"y"}', $3)`,
		ids[2], past, future)
	exec(t, s, `INSERT INTO cost_events (occurred_at, component, kind, cost_source) VALUES
		($1, 'gate', 'decision', 'reported'), ($2, 'gate', 'decision', 'reported')`, now.AddDate(0, 0, -401), now)
	exec(t, s, `INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref, created_at) VALUES
		('r1', 'test', 's1', 'groundedness@1', $1), ('r2', 'test', 's1', 'groundedness@1', $2)`, now.AddDate(0, 0, -31), now)

	lag, err := s.retentionLag(ctx, RetentionConfig{}.rules(now), now)
	if err != nil || lag < 10*24*time.Hour {
		t.Fatalf("lag before the run %v (%v), want the 10 days the old traces are past 30", lag, err)
	}

	// Batches of 2 take several rounds.
	rep, err := s.RunRetention(ctx, RetentionConfig{Batch: 2, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{RetainTraces: 2, RetainDecisions: 2, RetainAudits: 1, RetainBodies: 4, RetainAuditText: 1,
		RetainLabelSamples: 1, RetainCostEvents: 1, RetainStudioResults: 1}
	for k, v := range want {
		if rep.Rows[k] != v {
			t.Errorf("%s: %d rows, want %d (report %v)", k, rep.Rows[k], v, rep.Rows)
		}
	}
	if rep.Lag != 0 {
		t.Errorf("lag after a full run %v", rep.Lag)
	}

	if n := count(t, s, `SELECT count(*) FROM traces WHERE trace_id = ANY($1)`, ids[:2]); n != 0 {
		t.Errorf("%d old traces left", n)
	}
	if n := count(t, s, `SELECT count(*) FROM gate_decisions WHERE trace_id = ANY($1)`, ids); n != 3 {
		t.Errorf("%d decisions left, want the 3 recent ones", n)
	}
	if n := countClaims(t, s, oldAudit.AuditID); n != 0 {
		t.Errorf("%d claims of the old audit left", n)
	}
	// Expired bodies are gone and their traces say so; the trace reads its body as expired.
	if n := count(t, s, `SELECT count(*) FROM trace_bodies`); n != 1 {
		t.Errorf("%d bodies left, want the unexpired one", n)
	}
	tr, err := s.GetTrace(ctx, ids[2])
	if err != nil || !containsString(tr.Flags, "body_expired") {
		t.Errorf("flags %v (%v)", tr.Flags, err)
	}
	b, err := s.GetTraceBody(ctx, ids[2])
	if err != nil || b.View(now).Available || *b.View(now).UnavailableReason != BodyExpired {
		t.Errorf("an expired body reads %+v (%v)", b, err)
	}
	if _, err := s.GetTraceBody(ctx, "no-such-trace"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown trace body: %v", err)
	}
	if tr, _ := s.GetTrace(ctx, ids[4]); containsString(tr.Flags, "body_expired") {
		t.Error("a live body's trace was flagged")
	}
	// Expired audit text is nulled, claims included; the verdicts stay.
	var query, answer, claim *string
	var redacted bool
	var status string
	if err := s.pool.QueryRow(ctx, `SELECT a.user_query, a.llm_response, a.text_redacted, c.claim, c.status
		FROM audits a JOIN audit_claims c USING (audit_id) WHERE a.audit_id = $1 LIMIT 1`, expiredText.AuditID).Scan(
		&query, &answer, &redacted, &claim, &status); err != nil {
		t.Fatal(err)
	}
	if query != nil || answer != nil || !redacted || claim != nil || status != "SUPPORTED" {
		t.Errorf("expired text: query %v answer %v redacted %v claim %v status %s", query, answer, redacted, claim, status)
	}
	if n := count(t, s, `SELECT count(*) FROM audits WHERE audit_id = $1 AND user_query IS NOT NULL AND NOT text_redacted`, keptText.AuditID); n != 1 {
		t.Error("unexpired audit text was nulled")
	}
	if n := count(t, s, `SELECT count(*) FROM labels WHERE sample IS NOT NULL`); n != 1 {
		t.Errorf("%d label samples left, want 1", n)
	}
	if n := count(t, s, `SELECT count(*) FROM labels`); n != 2 {
		t.Errorf("label rows are kept: %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM cost_events`) + count(t, s, `SELECT count(*) FROM studio_results`); n != 2 {
		t.Errorf("%d cost events and Studio results left, want the 2 recent ones", n)
	}

	// Nothing is left to do.
	rep, err = s.RunRetention(ctx, RetentionConfig{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range rep.Rows {
		if v != 0 {
			t.Errorf("second run: %s %d rows", k, v)
		}
	}
}

func TestRetentionHalvesATimedOutBatch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	// A batch above 100 rows outlasts the 100 ms statement timeout; one of 100 handles 99 rows and
	// ends the rule.
	r := retentionRule{name: "test", cutoff: time.Now(), batch: `
		SELECT CASE WHEN $2::int > 100 THEN (SELECT count(*) FROM pg_sleep(1)) ELSE ($2::int - 1)::bigint END
		WHERE $1::timestamptz IS NOT NULL`}
	n, err := s.retain(ctx, r, 400, 100*time.Millisecond)
	if err != nil || n != 99 {
		t.Errorf("rows %d (%v), want 99 from a batch of 100", n, err)
	}
	// When even the smallest batch times out, the rule waits for the next run.
	r.batch = `SELECT (SELECT count(*) FROM pg_sleep(1)) WHERE $1::timestamptz IS NOT NULL AND $2::int > 0`
	if _, err := s.retain(ctx, r, minRetentionBatch*2, 100*time.Millisecond); !errors.Is(err, errStillBehind) {
		t.Errorf("err %v, want errStillBehind", err)
	}
	// A cancelled run stops.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.RunRetention(cctx, RetentionConfig{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled run: %v", err)
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
