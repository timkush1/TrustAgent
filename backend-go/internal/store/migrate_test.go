package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truthtable/backend-go/internal/storetest"
)

func mustLoadMigrations(t *testing.T) []migration {
	t.Helper()
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 2 || migrations[0].name != "0001_create_audits.up.sql" ||
		migrations[1].name != "0002_traces.up.sql" {
		t.Fatalf("unexpected embedded migrations: %v", migrationNames(migrations))
	}
	return migrations
}

func migrationNames(migrations []migration) []string {
	names := make([]string, len(migrations))
	for i, m := range migrations {
		names[i] = m.name
	}
	return names
}

func appliedNames(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT filename FROM schema_migrations ORDER BY filename`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrateEmptyDatabase(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	migrations := mustLoadMigrations(t)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if got, want := appliedNames(t, ctx, pool), migrationNames(migrations); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("schema_migrations = %v, want %v", got, want)
	}
	for _, table := range []string{"audits", "audit_claims", "traces", "trace_bodies", "gate_decisions"} {
		if !tableExists(t, ctx, pool, table) {
			t.Errorf("table %s missing after migrate", table)
		}
	}
	// 0003/0004 tables belong to later contract PRs.
	for _, table := range []string{"policy_versions", "evaluators", "settings", "credentials", "cost_events"} {
		if tableExists(t, ctx, pool, table) {
			t.Errorf("table %s must not exist after 0002", table)
		}
	}

	// Dependent tables deliberately carry no foreign keys (SPEC §2.3).
	var fks int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE contype = 'f' AND connamespace = to_regnamespace(current_schema())
		  AND conrelid::regclass::text IN ('traces', 'trace_bodies', 'gate_decisions')`).Scan(&fks); err != nil {
		t.Fatal(err)
	}
	if fks != 0 {
		t.Errorf("expected no foreign keys on trace tables, found %d", fks)
	}

	// Spot-check the 0002 column contract.
	nullable := map[string]string{
		"audits.faithfulness_score":   "YES",
		"audits.grade":                "YES",
		"audits.user_query":           "YES",
		"audits.llm_response":         "YES",
		"audits.trace_id":             "YES",
		"audits.client_request_id":    "YES",
		"audits.status":               "NO",
		"audit_claims.claim":          "YES",
		"traces.client_request_id":    "YES",
		"traces.updated_at":           "NO",
		"traces.gate_status":          "NO",
		"traces.audit_status":         "NO",
		"traces.action":               "YES",
		"gate_decisions.updated_at":   "NO",
		"trace_bodies.expires_at":     "NO",
		"traces.decision_cost_usd":    "YES",
		"gate_decisions.cost_usd":     "YES",
		"audits.cost_usd":             "YES",
		"audit_claims.evidence_spans": "YES",
	}
	for column, want := range nullable {
		table, name, _ := strings.Cut(column, ".")
		var got string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			table, name).Scan(&got); err != nil {
			t.Errorf("column %s: %v", column, err)
			continue
		}
		if got != want {
			t.Errorf("column %s is_nullable = %s, want %s", column, got, want)
		}
	}
	for _, column := range []string{"traces.decision_cost_usd", "gate_decisions.cost_usd", "audits.cost_usd"} {
		table, name, _ := strings.Cut(column, ".")
		var precision, scale int
		if err := pool.QueryRow(ctx, `
			SELECT numeric_precision, numeric_scale FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`,
			table, name).Scan(&precision, &scale); err != nil {
			t.Fatal(err)
		}
		if precision != 14 || scale != 10 {
			t.Errorf("%s is NUMERIC(%d,%d), want NUMERIC(14,10)", column, precision, scale)
		}
	}
}

func TestMigrateRerunIsNoOp(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)

	appliedAt := func() []time.Time {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT applied_at FROM schema_migrations ORDER BY filename`)
		if err != nil {
			t.Fatal(err)
		}
		times, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
		if err != nil {
			t.Fatal(err)
		}
		return times
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	before := appliedAt()
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("re-run %d: %v", i+1, err)
		}
	}
	after := appliedAt()
	if len(after) != len(before) {
		t.Fatalf("re-run changed schema_migrations: %d rows -> %d", len(before), len(after))
	}
	for i := range before {
		if !after[i].Equal(before[i]) {
			t.Errorf("re-run re-applied a migration (applied_at %v -> %v)", before[i], after[i])
		}
	}
}

// Every file must be safe to execute twice (critique L4): the runner would
// hide a non-idempotent file, so the SQL is executed directly, twice, inside
// one transaction, and the second execution must change neither the catalog
// nor any row. Legacy rows are seeded between 0001 and 0002 so 0002's data
// statements have work to do.
func TestMigrateEachFileTwiceIsNoOp(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)

	for _, m := range mustLoadMigrations(t) {
		if m.name == "0002_traces.up.sql" {
			seedLegacyAudits(t, ctx, pool)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			t.Fatalf("%s first execution: %v", m.name, err)
		}
		first := snapshot(t, ctx, tx)
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			t.Fatalf("%s second execution: %v", m.name, err)
		}
		second := snapshot(t, ctx, tx)
		if first != second {
			t.Errorf("%s: second execution changed the database\n--- after first ---\n%s\n--- after second ---\n%s",
				m.name, first, second)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot renders the schema's catalog (columns, indexes, constraints) and a
// digest of every table's rows.
func snapshot(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	var b strings.Builder
	collect := func(label, sql string) {
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatalf("snapshot %s: %v", label, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("snapshot %s: %v", label, err)
		}
		fmt.Fprintf(&b, "[%s]\n%s\n", label, strings.Join(lines, "\n"))
	}
	collect("columns", `
		SELECT concat_ws(' ', table_name, column_name, data_type, is_nullable, column_default)
		FROM information_schema.columns WHERE table_schema = current_schema()
		ORDER BY table_name, column_name`)
	collect("indexes", `
		SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() ORDER BY indexname`)
	collect("constraints", `
		SELECT concat_ws(' ', conrelid::regclass::text, conname, pg_get_constraintdef(oid))
		FROM pg_constraint WHERE connamespace = to_regnamespace(current_schema())
		ORDER BY conrelid::regclass::text, conname`)

	rows, err := tx.Query(ctx, `
		SELECT table_name::text FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		var digest string
		if err := tx.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*), coalesce(md5(string_agg(r::text, E'\n' ORDER BY r::text)), '') FROM %s r`,
			pgx.Identifier{table}.Sanitize())).Scan(&count, &digest); err != nil {
			t.Fatalf("snapshot rows of %s: %v", table, err)
		}
		fmt.Fprintf(&b, "[rows %s] %d %s\n", table, count, digest)
	}
	return b.String()
}

// legacyCreatedAt is the created_at of every seeded 0001-era audit.
var legacyCreatedAt = time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)

// seedLegacyAudits writes 0001-era data, including what B4 produced: audit
// "dup-req" had its claims appended twice by a repeated X-Request-ID, and
// "legacy-3" reuses request_id "dup-req" under its own audit_id. Claim
// statuses are mixed-case and include UNSPECIFIED.
func seedLegacyAudits(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	longQuery, longResponse := strings.Repeat("q", 250), strings.Repeat("r", 400)
	audits := []struct {
		id, requestID, query, response, model string
		score                                 float64
	}{
		{"legacy-1", "legacy-1", "Q1", "R1", "llama3.2", 0.95},
		{"dup-req", "dup-req", "Q2", "R2", "claude-haiku-4-5", 0.4},
		{"legacy-3", "dup-req", "Q3", "R3", "llama3.2", 0.7},
		{"legacy-4", "legacy-4", longQuery, longResponse, "", 0.1},
	}
	for _, a := range audits {
		if _, err := pool.Exec(ctx, `
			INSERT INTO audits (audit_id, request_id, user_query, llm_response, model,
			                    faithfulness_score, grade, hallucination_detected, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			a.id, a.requestID, a.query, a.response, a.model, a.score, GradeForScore(a.score),
			a.score < 0.5, legacyCreatedAt); err != nil {
			t.Fatalf("seed audit %s: %v", a.id, err)
		}
	}
	claims := []struct {
		auditID  string
		position int
		claim    string
		status   string
	}{
		{"legacy-1", 0, "c1", "supported"},
		{"legacy-1", 1, "c2", "Contradicted"},
		{"legacy-1", 2, "c3", "UNSPECIFIED"},
		{"dup-req", 0, "first-0", "SUPPORTED"},
		{"dup-req", 1, "first-1", "unsupported"},
		{"dup-req", 0, "second-0", "CONTRADICTED"}, // B4 duplicates, appended later
		{"dup-req", 1, "second-1", "SUPPORTED"},
		{"legacy-3", 0, "c4", "partially_supported"},
		{"legacy-4", 0, "c5", "UNKNOWN"},
	}
	for _, c := range claims {
		if _, err := pool.Exec(ctx, `
			INSERT INTO audit_claims (audit_id, position, claim, status, confidence, evidence)
			VALUES ($1, $2, $3, $4, 0.5, '[]')`, c.auditID, c.position, c.claim, c.status); err != nil {
			t.Fatalf("seed claim %s/%d: %v", c.auditID, c.position, err)
		}
	}
}

// A database created by the 0001-era runner (schema_migrations lists only
// 0001) is upgraded in place and backfilled.
func TestBackfillSeeded0001Database(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	migrations := mustLoadMigrations(t)

	if err := applyMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	seedLegacyAudits(t, ctx, pool)

	s, err := NewPostgresStore(ctx, schema.URL) // runs the remaining migrations
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	t.Cleanup(s.Close)

	if got := appliedNames(t, ctx, pool); strings.Join(got, ",") != strings.Join(migrationNames(migrations), ",") {
		t.Errorf("schema_migrations = %v", got)
	}

	// audits: trace_id = audit_id (never request_id, which was not unique),
	// v2 columns take their documented defaults.
	rows, err := pool.Query(ctx, `
		SELECT audit_id, trace_id, source, status, scoring_version,
		       cardinality(degraded_reasons), text_redacted, client_request_id IS NULL
		FROM audits ORDER BY audit_id`)
	if err != nil {
		t.Fatal(err)
	}
	type auditRow struct {
		AuditID, TraceID, Source, Status, ScoringVersion string
		Degraded                                         int
		Redacted, NoClientID                             bool
	}
	auditRows, err := pgx.CollectRows(rows, pgx.RowToStructByPos[auditRow])
	if err != nil {
		t.Fatal(err)
	}
	if len(auditRows) != 4 {
		t.Fatalf("expected 4 audits, got %d", len(auditRows))
	}
	for _, a := range auditRows {
		if a.TraceID != a.AuditID || a.Source != "proxy" || a.Status != "completed" ||
			a.ScoringVersion != "v1" || a.Degraded != 0 || a.Redacted || !a.NoClientID {
			t.Errorf("legacy audit not upgraded as specified: %+v", a)
		}
	}

	// traces: one skipped/legacy trace per audit, keyed by audit_id.
	rows, err = pool.Query(ctx, `
		SELECT trace_id, audit_id, created_at, updated_at, source, api_format, path, route, mode,
		       action IS NULL, gate_status, skip_reason, audit_status, model,
		       length(question_preview), length(response_preview),
		       stream, cardinality(reasons), cardinality(flags), client_request_id IS NULL
		FROM traces ORDER BY trace_id`)
	if err != nil {
		t.Fatal(err)
	}
	type traceRow struct {
		TraceID, AuditID                     string
		CreatedAt, UpdatedAt                 time.Time
		Source, APIFormat, Path, Route, Mode string
		NoAction                             bool
		GateStatus, SkipReason, AuditStatus  string
		Model                                *string
		QuestionLen, ResponseLen             int
		Stream                               bool
		Reasons, Flags                       int
		NoClientID                           bool
	}
	traces, err := pgx.CollectRows(rows, pgx.RowToStructByPos[traceRow])
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 4 {
		t.Fatalf("expected 4 backfilled traces, got %d", len(traces))
	}
	for _, tr := range traces {
		if tr.TraceID != tr.AuditID || !tr.CreatedAt.Equal(legacyCreatedAt) || !tr.UpdatedAt.Equal(legacyCreatedAt) ||
			tr.Source != "proxy" || tr.APIFormat != "openai_chat" || tr.Path != "/v1/chat/completions" ||
			tr.Route != "default" || tr.Mode != "observe" || !tr.NoAction || tr.GateStatus != "skipped" ||
			tr.SkipReason != "legacy" || tr.AuditStatus != "completed" || tr.Stream ||
			tr.Reasons != 0 || tr.Flags != 0 || !tr.NoClientID {
			t.Errorf("backfilled trace not as specified: %+v", tr)
		}
		switch tr.TraceID {
		case "legacy-4":
			if tr.Model != nil {
				t.Errorf("empty model should backfill as NULL, got %q", *tr.Model)
			}
			if tr.QuestionLen != 200 || tr.ResponseLen != 300 {
				t.Errorf("previews not truncated to 200/300: %d/%d", tr.QuestionLen, tr.ResponseLen)
			}
		case "legacy-1":
			if tr.Model == nil || *tr.Model != "llama3.2" || tr.QuestionLen != 2 || tr.ResponseLen != 2 {
				t.Errorf("legacy-1 trace mismatch: %+v", tr)
			}
		}
	}

	// audit_claims: statuses upper-cased, UNSPECIFIED -> UNKNOWN, B4 duplicates
	// removed keeping the first-written row per position.
	rows, err = pool.Query(ctx, `
		SELECT audit_id || '/' || position || '/' || claim || '/' || status
		FROM audit_claims ORDER BY audit_id, position`)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"dup-req/0/first-0/SUPPORTED",
		"dup-req/1/first-1/UNSUPPORTED",
		"legacy-1/0/c1/SUPPORTED",
		"legacy-1/1/c2/CONTRADICTED",
		"legacy-1/2/c3/UNKNOWN",
		"legacy-3/0/c4/PARTIALLY_SUPPORTED",
		"legacy-4/0/c5/UNKNOWN",
	}
	if strings.Join(claims, "\n") != strings.Join(want, "\n") {
		t.Errorf("claims after 0002:\n%s\nwant:\n%s", strings.Join(claims, "\n"), strings.Join(want, "\n"))
	}

	// The store reads legacy rows through the new columns.
	got, err := s.GetAudit(ctx, "dup-req")
	if err != nil {
		t.Fatalf("GetAudit legacy: %v", err)
	}
	if got.TraceID != "dup-req" || got.RequestID != "dup-req" || got.FaithfulnessScore == nil ||
		*got.FaithfulnessScore != 0.4 || got.Grade == nil || *got.Grade != "D" || len(got.Claims) != 2 {
		t.Errorf("legacy audit read back wrong: %+v", got)
	}
	list, total, err := s.ListAudits(ctx, ListFilter{Limit: 10})
	if err != nil || total != 4 || len(list) != 4 {
		t.Errorf("ListAudits on legacy data: total=%d len=%d err=%v", total, len(list), err)
	}

	// A new audit next to legacy ones, then a no-op re-run of the runner.
	if err := s.SaveAudit(ctx, sampleAudit(NewID(), 0.9, false)); err != nil {
		t.Fatalf("SaveAudit after upgrade: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("re-run after upgrade: %v", err)
	}
	var traceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM traces`).Scan(&traceCount); err != nil {
		t.Fatal(err)
	}
	if traceCount != 4 {
		t.Errorf("re-run must not backfill again: %d traces", traceCount)
	}
}

// A file and its schema_migrations row commit or roll back together, whether
// the file itself fails or recording it fails, and the lock is released so the
// next run proceeds.
func TestMigrateFailedFileRollsBack(t *testing.T) {
	const name = "0001_zz_broken.up.sql"
	cases := map[string]string{
		"file fails": "CREATE TABLE must_not_exist (id INT); SELECT 1/0;",
		// The file succeeds but the runner's tracking insert then violates
		// the primary key, so only a shared transaction undoes the DDL.
		"recording fails": "CREATE TABLE must_not_exist (id INT); " +
			"INSERT INTO schema_migrations (filename) VALUES ('" + name + "');",
	}
	for label, sql := range cases {
		t.Run(label, func(t *testing.T) {
			ctx := context.Background()
			schema := storetest.New(t)
			pool := schema.Pool(t)
			migrations := mustLoadMigrations(t)

			err := applyMigrations(ctx, pool, []migration{migrations[0], {name: name, sql: sql}})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected an error naming %s, got %v", name, err)
			}
			if tableExists(t, ctx, pool, "must_not_exist") {
				t.Error("DDL of the failed file was not rolled back")
			}
			if got := appliedNames(t, ctx, pool); strings.Join(got, ",") != migrations[0].name {
				t.Errorf("schema_migrations = %v, want only %s", got, migrations[0].name)
			}

			done := make(chan error, 1)
			go func() { done <- Migrate(ctx, pool) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Migrate after failure: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Migrate blocked: the advisory lock was not released after a failed run")
			}
		})
	}
}

// The advisory lock is taken before schema_migrations is created or read: a
// runner started while another session holds the lock must not touch the
// schema until the lock is released.
func TestMigrateWaitsForAdvisoryLock(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)

	// Opened first so it is closed last: Close waits for the runner's
	// connection, which is only freed once the lock below is released.
	runnerPool, err := pgxpool.New(ctx, schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer runnerPool.Close()

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		holder.Release()
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID); err != nil {
			t.Errorf("unlock: %v", err)
		}
		holder.Release()
	}
	defer release()

	done := make(chan error, 1)
	go func() { done <- Migrate(ctx, runnerPool) }()

	// Wait until some session queues on the migration lock, then give the
	// runner time it would need to create schema_migrations were it unguarded.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND classid = 0 AND objid::bigint = $1 AND objsubid = 1 AND NOT granted`,
			migrationLockID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runner never waited on the migration lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	select {
	case err := <-done:
		t.Fatalf("Migrate finished while the lock was held (err=%v)", err)
	default:
	}
	if tableExists(t, ctx, pool, "schema_migrations") {
		t.Fatal("schema_migrations was created before the advisory lock was granted")
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Migrate after release: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Migrate did not finish after the lock was released")
	}
	if !tableExists(t, ctx, pool, "traces") {
		t.Error("migrations not applied after the lock was released")
	}
}

// Concurrent runners (separate pools, as separate processes would be) are
// serialized: all succeed and every file is applied exactly once.
func TestMigrateConcurrentRunnersSerialized(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	migrations := mustLoadMigrations(t)

	const runners = 8
	pools := make([]*pgxpool.Pool, runners)
	for i := range pools {
		p, err := pgxpool.New(ctx, schema.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		pools[i] = p
	}

	errs := make([]error, runners)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Migrate(ctx, pools[i])
		}()
	}
	close(start)
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("concurrent Migrate failed: %v", err)
	}
	var total, distinct int
	if err := pools[0].QueryRow(ctx,
		`SELECT count(*), count(DISTINCT filename) FROM schema_migrations`).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if total != len(migrations) || distinct != len(migrations) {
		t.Errorf("schema_migrations has %d rows (%d distinct), want %d", total, distinct, len(migrations))
	}
}
