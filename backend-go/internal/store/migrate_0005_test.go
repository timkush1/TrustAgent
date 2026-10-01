package store

import (
	"context"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/storetest"
)

// Migration 0005 (P4-PLAN D44, D45, D55) is a contract file. These tests pin
// what it adds: the cost ledger link and flag columns, traces.response_chars,
// the two partial cost indexes, the calibrations file-entry unique index and
// the append-only trigger on admin_audit_log. They also upgrade a database
// that 0004 left with live rows.

const migration0005 = "0005_p4.up.sql"

// appendOnlyMessage is what the admin_audit_log trigger raises (D44).
const appendOnlyMessage = "admin_audit_log rows are append-only"

func TestMigration0005Schema(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	requireColumns(t, ctx, pool, "cost_events", []string{
		"id bigint not null default nextval('cost_events_id_seq'::regclass)",
		"occurred_at timestamp with time zone not null default now()",
		"trace_id text",
		"route text",
		"key_label text",
		"component text not null",
		"kind text not null",
		"provider text",
		"model text",
		"attempt smallint not null default 0",
		"input_tokens integer",
		"output_tokens integer",
		"cost_usd numeric(14,10)",
		"cost_source text not null",
		"price_table_version text",
		"latency_ms integer",
		"status text",
		"questions integer",
		"selection text",
		"audit_id text",
		"hedge boolean not null default false",
		"late boolean not null default false",
		"estimated boolean not null default false",
	})
	traceColumns := columnContract(t, ctx, pool, "traces")
	if got := traceColumns[len(traceColumns)-1]; got != "response_chars integer" {
		t.Errorf("last traces column = %q, want %q", got, "response_chars integer")
	}

	requireIndexes(t, ctx, pool, []string{"cost_events", "calibrations"}, []string{
		"calibrations_file_entry: CREATE UNIQUE INDEX calibrations_file_entry ON calibrations USING btree (file_sha256, target_key) WHERE (origin = 'file'::text)",
		"calibrations_pkey: CREATE UNIQUE INDEX calibrations_pkey ON calibrations USING btree (id)",
		"cost_events_pkey: CREATE UNIQUE INDEX cost_events_pkey ON cost_events USING btree (id)",
		"idx_cost_events_audit: CREATE INDEX idx_cost_events_audit ON cost_events USING btree (audit_id) WHERE (audit_id IS NOT NULL)",
		"idx_cost_events_time: CREATE INDEX idx_cost_events_time ON cost_events USING btree (occurred_at DESC, component)",
		"idx_cost_events_trace: CREATE INDEX idx_cost_events_trace ON cost_events USING btree (trace_id) WHERE (trace_id IS NOT NULL)",
		"one_active_calibration: CREATE UNIQUE INDEX one_active_calibration ON calibrations USING btree (snapshot, target_kind, target_key) WHERE active",
	})

	const calibration = `INSERT INTO calibrations (created_by, origin, backend, snapshot, family, target_kind,
		target_key, quality, label_source, entry, file_sha256, active) VALUES `
	runStatementCases(t, ctx, pool, []statementCase{
		// cost_events: the link and flag columns, with false defaults.
		{"LLM cost row linked to its audit", `INSERT INTO cost_events (trace_id, audit_id, component, kind, cost_source,
			cost_usd, hedge, late, estimated) VALUES ('t1', 'a1', 'deep_audit_verify', 'llm', 'estimated', 0.0012, false, true, true)`, sqlOK},
		{"hedge attempt row", `INSERT INTO cost_events (trace_id, component, kind, cost_source, attempt, hedge)
			VALUES ('t1', 'gate', 'jev', 'reported', 1, true)`, sqlOK},
		{"hedge cannot be null", `INSERT INTO cost_events (component, kind, cost_source, hedge)
			VALUES ('gate', 'jev', 'reported', NULL)`, sqlNotNullViolation},

		// traces.response_chars is a plain nullable INT.
		{"trace with response_chars", `INSERT INTO traces (trace_id, source, api_format, path, route, mode,
			gate_status, audit_status, response_chars) VALUES ('0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a91', 'proxy',
			'openai_chat', '/v1/chat/completions', 'support-bot', 'observe', 'ok', 'none', 1234)`, sqlOK},

		// calibrations: one mirrored row per (file, target) among file rows only (D55).
		{"file entry", calibration + `('file', 'file', 'typesafe', 'jev-1.13', 'jev', 'evaluator',
			'groundedness@1#0123456789abcdef#-', 'measured', '{}', '{}', 'sha-a', false)`, sqlOK},
		{"same file entry twice", calibration + `('file', 'file', 'typesafe', 'jev-1.13', 'jev', 'evaluator',
			'groundedness@1#0123456789abcdef#-', 'measured', '{}', '{}', 'sha-a', false)`, sqlUniqueViolation},
		{"same target from another file", calibration + `('file', 'file', 'typesafe', 'jev-1.13', 'jev', 'evaluator',
			'groundedness@1#0123456789abcdef#-', 'measured', '{}', '{}', 'sha-b', false)`, sqlOK},
		{"import row with the same file and target", calibration + `('admin', 'import', 'typesafe', 'jev-1.13', 'jev',
			'evaluator', 'groundedness@1#0123456789abcdef#-', 'measured', '{}', '{}', 'sha-a', false)`, sqlOK},

		// admin_audit_log is append-only (D44).
		{"admin audit entry", `INSERT INTO admin_audit_log (actor, action, target, diff)
			VALUES ('setup-admin', 'settings.update', 'judge', '{"provider":"openrouter"}')`, sqlOK},
		{"admin audit update", `UPDATE admin_audit_log SET actor = 'someone-else'`, sqlRaiseException},
		{"admin audit delete", `DELETE FROM admin_audit_log`, sqlRaiseException},
	})

	_, err := pool.Exec(ctx, `UPDATE admin_audit_log SET target = NULL`)
	if err == nil || !strings.Contains(err.Error(), appendOnlyMessage) {
		t.Errorf("UPDATE admin_audit_log error = %v, want %q", err, appendOnlyMessage)
	}
	var entries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_log WHERE actor = 'setup-admin'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("admin_audit_log has %d setup-admin rows after the refused UPDATE and DELETE, want 1", entries)
	}

	var hedge, late, estimated bool
	if err := pool.QueryRow(ctx, `SELECT hedge, late, estimated FROM cost_events WHERE audit_id IS NULL LIMIT 1`).
		Scan(&hedge, &late, &estimated); err != nil {
		t.Fatal(err)
	}
	if !hedge || late || estimated {
		t.Errorf("hedge row flags = hedge %v late %v estimated %v, want true false false", hedge, late, estimated)
	}
}

// A database 0004 left with live rows upgrades in place: existing cost rows get
// false flags and no link, existing traces keep a NULL response_chars, and the
// file runs again over the upgraded rows without changing anything.
func TestMigration0005UpgradesLiveRows(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	migrateThrough(t, ctx, pool, migration0004)
	seedConfigRows(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO traces (trace_id, source, api_format, path, route, mode, gate_status, audit_status)
		VALUES ('0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a92', 'proxy', 'openai_chat', '/v1/chat/completions', 'support-bot',
		        'observe', 'ok', 'none')`); err != nil {
		t.Fatalf("seed trace: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := appliedNames(t, ctx, pool); got[len(got)-1] != migration0005 {
		t.Fatalf("schema_migrations = %v, want %s last", got, migration0005)
	}

	var flagged, linked, withChars int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM cost_events WHERE hedge OR late OR estimated),
		       (SELECT count(*) FROM cost_events WHERE audit_id IS NOT NULL),
		       (SELECT count(*) FROM traces WHERE response_chars IS NOT NULL)`).Scan(&flagged, &linked, &withChars); err != nil {
		t.Fatal(err)
	}
	if flagged != 0 || linked != 0 || withChars != 0 {
		t.Errorf("upgrade backfilled values: %d flagged cost rows, %d linked cost rows, %d traces with response_chars",
			flagged, linked, withChars)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	before := snapshot(t, ctx, tx)
	m := migrationByName(t, migration0005)
	for round := 1; round <= 2; round++ {
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			t.Fatalf("%s, run %d over live rows: %v", m.name, round, err)
		}
	}
	if after := snapshot(t, ctx, tx); after != before {
		t.Errorf("re-running 0005 changed the database\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	runStatementCases(t, ctx, pool, []statementCase{
		{"append-only trigger still installed", `DELETE FROM admin_audit_log`, sqlRaiseException},
	})
}
