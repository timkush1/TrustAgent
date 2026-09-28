package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/truthtable/backend-go/internal/storetest"
)

// Migrations 0003 (policy versions, evaluators, calibrations, labels, Studio
// results) and 0004 (settings, credentials, API keys, admin audit log, cost
// ledger) are contract files: their SQL is SPEC §2.3 verbatim. These tests pin
// that contract (columns, keys, indexes, CHECKs, the immutability trigger), run
// both files again over live rows, and upgrade a seeded 0002-era database.

const (
	migration0003 = "0003_policy_evaluators.up.sql"
	migration0004 = "0004_security_costs.up.sql"
)

var (
	tables0003 = []string{"policy_versions", "evaluators", "evaluator_versions", "calibrations", "labels", "studio_results"}
	tables0004 = []string{"settings", "credentials", "api_keys", "admin_audit_log", "cost_events"}
)

// SQLSTATEs the constraint cases expect (PostgreSQL Appendix A).
const (
	sqlOK               = ""
	sqlNotNullViolation = "23502"
	sqlForeignKey       = "23503"
	sqlUniqueViolation  = "23505"
	sqlCheckViolation   = "23514"
	sqlRaiseException   = "P0001"
)

// immutableMessage is what the evaluator_versions trigger raises (SPEC §2.3).
const immutableMessage = "evaluator_versions rows are immutable"

// migrationByName returns the embedded migration called name.
func migrationByName(t *testing.T, name string) migration {
	t.Helper()
	for _, m := range mustLoadMigrations(t) {
		if m.name == name {
			return m
		}
	}
	t.Fatalf("migration %s is not embedded", name)
	return migration{}
}

// columnContract renders the columns of table in ordinal order as
// "name type[ not null][ default expr]", with types as format_type prints them.
func columnContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
		       || CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
		       || coalesce(' default ' || pg_get_expr(d.adbin, d.adrelid), '')
		FROM pg_attribute a
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, table)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return columns
}

// sqlState is the SQLSTATE of err, "" for nil, or "?" for a non-Postgres error.
func sqlState(err error) string {
	if err == nil {
		return sqlOK
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "?"
}

// statementCase is one statement run on its own (autocommit), in order, with
// the SQLSTATE it must end with (sqlOK for success).
type statementCase struct {
	name string
	sql  string
	code string
}

func runStatementCases(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cases []statementCase) {
	t.Helper()
	for _, c := range cases {
		_, err := pool.Exec(ctx, c.sql)
		if got := sqlState(err); got != c.code {
			t.Errorf("%s: SQLSTATE %q (err %v), want %q", c.name, got, err, c.code)
		}
	}
}

func TestMigration0003Schema(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	want := map[string][]string{
		"policy_versions": {
			"id bigint not null default nextval('policy_versions_id_seq'::regclass)",
			"created_at timestamp with time zone not null default now()",
			"source text not null",
			"author text not null",
			"message text not null default ''::text",
			"doc jsonb not null",
			"yaml text not null",
			"sha256 text not null",
			"file_sha256 text",
			"parent_id bigint",
		},
		"evaluators": {
			"id text not null",
			"name text not null",
			"category text not null",
			"origin text not null",
			"latest_version integer not null",
			"archived boolean not null default false",
			"created_at timestamp with time zone not null default now()",
			"updated_at timestamp with time zone not null default now()",
		},
		"evaluator_versions": {
			"evaluator_id text not null",
			"version integer not null",
			"spec jsonb not null",
			"spec_sha256 text not null",
			"question_sha256 text not null",
			"calibration_key text not null",
			"created_by text not null",
			"created_at timestamp with time zone not null default now()",
			"changelog text not null default ''::text",
		},
		"calibrations": {
			"id bigint not null default nextval('calibrations_id_seq'::regclass)",
			"created_at timestamp with time zone not null default now()",
			"created_by text not null",
			"origin text not null",
			"backend text not null",
			"snapshot text not null",
			"family text not null",
			"target_kind text not null",
			"target_key text not null",
			"quality text not null",
			"label_source jsonb not null",
			"entry jsonb not null",
			"file_sha256 text",
			"active boolean not null default true",
		},
		"labels": {
			"trace_id text not null",
			"target text not null",
			"label text not null",
			"labeled_by text not null",
			"created_at timestamp with time zone not null default now()",
			"note text",
			"label_source text not null default 'human'::text",
			"sample jsonb",
			"sample_expires_at timestamp with time zone",
			"risk_raw real",
			"snapshot text",
		},
		"studio_results": {
			"run_id text not null",
			"kind text not null",
			"sample_id text not null",
			"evaluator_ref text not null",
			"answer jsonb",
			"risk_raw real",
			"risk real",
			"level text",
			"status text",
			"escalate text",
			"snapshot text",
			"latency_ms integer",
			"cost_usd numeric(14,10)",
			"created_at timestamp with time zone not null default now()",
		},
	}
	for _, table := range tables0003 {
		requireColumns(t, ctx, pool, table, want[table])
	}
	requireIndexes(t, ctx, pool, tables0003, []string{
		"calibrations_pkey: CREATE UNIQUE INDEX calibrations_pkey ON calibrations USING btree (id)",
		"evaluator_versions_pkey: CREATE UNIQUE INDEX evaluator_versions_pkey ON evaluator_versions USING btree (evaluator_id, version)",
		"evaluators_pkey: CREATE UNIQUE INDEX evaluators_pkey ON evaluators USING btree (id)",
		"labels_pkey: CREATE UNIQUE INDEX labels_pkey ON labels USING btree (trace_id, target, labeled_by)",
		"one_active_calibration: CREATE UNIQUE INDEX one_active_calibration ON calibrations USING btree (snapshot, target_kind, target_key) WHERE active",
		"policy_versions_pkey: CREATE UNIQUE INDEX policy_versions_pkey ON policy_versions USING btree (id)",
		"studio_results_pkey: CREATE UNIQUE INDEX studio_results_pkey ON studio_results USING btree (run_id, sample_id, evaluator_ref)",
	})

	const calibration = `INSERT INTO calibrations (created_by, origin, backend, snapshot, family, target_kind, target_key,
		quality, label_source, entry, active) `
	runStatementCases(t, ctx, pool, []statementCase{
		// policy_versions: source enum, self-referencing parent.
		{"bootstrap policy version", `INSERT INTO policy_versions (source, author, doc, yaml, sha256)
			VALUES ('bootstrap', 'system', '{"version":1}', 'version: 1', 'a1')`, sqlOK},
		{"policy source outside the enum", `INSERT INTO policy_versions (source, author, doc, yaml, sha256)
			VALUES ('import', 'admin', '{"version":1}', 'version: 1', 'a2')`, sqlCheckViolation},
		{"policy parent must exist", `INSERT INTO policy_versions (source, author, doc, yaml, sha256, parent_id)
			VALUES ('dashboard', 'admin', '{"version":1}', 'version: 1', 'a3', 999999)`, sqlForeignKey},
		{"dashboard version on its parent", `INSERT INTO policy_versions (source, author, doc, yaml, sha256, parent_id)
			SELECT 'dashboard', 'admin', '{"version":1}', 'version: 1', 'a4', min(id) FROM policy_versions`, sqlOK},
		{"every source value", `INSERT INTO policy_versions (source, author, doc, yaml, sha256, file_sha256)
			SELECT s, 'system', '{"version":1}', 'version: 1', 'a5', 'f5'
			FROM unnest(ARRAY['bootstrap','file','dashboard','rollback','adopt_file']) s`, sqlOK},

		// evaluators: id pattern ^[a-z][a-z0-9_]{1,47}$ and origin enum.
		{"built-in evaluator", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('groundedness', 'Groundedness', 'grounding', 'builtin', 1)`, sqlOK},
		{"48-character id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('` + "a" + strings.Repeat("b", 47) + `', 'Long', 'custom', 'dashboard', 1)`, sqlOK},
		{"49-character id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('` + "a" + strings.Repeat("b", 48) + `', 'Too long', 'custom', 'dashboard', 1)`, sqlCheckViolation},
		{"one-character id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('g', 'G', 'custom', 'dashboard', 1)`, sqlCheckViolation},
		{"upper-case id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('Refusal', 'Refusal', 'quality', 'file', 1)`, sqlCheckViolation},
		{"hyphenated id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('off-topic', 'Off-topic', 'quality', 'file', 1)`, sqlCheckViolation},
		{"leading digit id", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('1refusal', 'Refusal', 'quality', 'file', 1)`, sqlCheckViolation},
		{"evaluator origin outside the enum", `INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ('refusal', 'Refusal', 'quality', 'studio', 1)`, sqlCheckViolation},

		// evaluator_versions: FK, version >= 1, composite key, immutable rows.
		{"first version", `INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256,
			calibration_key, created_by) VALUES ('groundedness', 1, '{"id":"groundedness"}', 's1', 'q1', 'k1', 'bootstrap')`, sqlOK},
		{"version zero", `INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256,
			calibration_key, created_by) VALUES ('groundedness', 0, '{}', 's0', 'q0', 'k0', 'admin')`, sqlCheckViolation},
		{"version of an unknown evaluator", `INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256,
			question_sha256, calibration_key, created_by) VALUES ('no_such_check', 1, '{}', 's', 'q', 'k', 'admin')`, sqlForeignKey},
		{"same version twice", `INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256,
			calibration_key, created_by) VALUES ('groundedness', 1, '{}', 's2', 'q2', 'k2', 'admin')`, sqlUniqueViolation},
		{"versions are immutable", `UPDATE evaluator_versions SET changelog = 'edited in place'`, sqlRaiseException},
		{"specs are immutable", `UPDATE evaluator_versions SET spec = '{}' WHERE evaluator_id = 'groundedness'`, sqlRaiseException},
		{"next version", `INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256,
			calibration_key, created_by, changelog) VALUES ('groundedness', 2, '{}', 's3', 'q3', 'k3', 'admin', 'Reworded')`, sqlOK},

		// calibrations: enums and at most one active row per (snapshot, target_kind, target_key).
		{"active calibration", calibration + `VALUES ('cli', 'file', 'openrouter-decisions', 'typesafe/jev-1.13-20260917', 'jev-1.13',
			'evaluator', 'groundedness@1#0123456789abcdef#-', 'measured', '{"human":240}', '{}', true)`, sqlOK},
		{"second active calibration for the same target", calibration + `VALUES ('admin', 'fit', 'openrouter-decisions',
			'typesafe/jev-1.13-20260917', 'jev-1.13', 'evaluator', 'groundedness@1#0123456789abcdef#-', 'provisional',
			'{"human":40}', '{}', true)`, sqlUniqueViolation},
		{"inactive calibration for the same target", calibration + `VALUES ('admin', 'fit', 'openrouter-decisions',
			'typesafe/jev-1.13-20260917', 'jev-1.13', 'evaluator', 'groundedness@1#0123456789abcdef#-', 'provisional',
			'{"human":40}', '{}', false)`, sqlOK},
		{"same target on another snapshot", calibration + `VALUES ('cli', 'import', 'typesafe', 'jev-1.13.0', 'jev-1.13',
			'evaluator', 'groundedness@1#0123456789abcdef#-', 'measured', '{"human":240}', '{}', true)`, sqlOK},
		{"mock quality", calibration + `VALUES ('demo', 'file', 'mock', 'mock/jev-heuristic-v1', 'mock', 'evaluator',
			'contradiction@1#fedcba9876543210#-', 'mock', '{"mock":true}', '{}', true)`, sqlOK},
		{"every target kind", calibration + `SELECT 'cli', 'file', 'mock', 'mock/jev-heuristic-v1', 'mock', k, k || ':x',
			'proxy', '{}', '{}', true FROM unnest(ARRAY['evaluator','cascade_option','llm_confidence','kb']) k`, sqlOK},
		{"quality outside the enum", calibration + `VALUES ('cli', 'file', 'mock', 'mock/jev-heuristic-v1', 'mock', 'evaluator',
			'refusal@1#0000000000000000#-', 'estimate', '{}', '{}', true)`, sqlCheckViolation},
		{"origin outside the enum", calibration + `VALUES ('admin', 'studio', 'mock', 'mock/jev-heuristic-v1', 'mock', 'evaluator',
			'refusal@1#0000000000000000#-', 'provisional', '{}', '{}', true)`, sqlCheckViolation},
		{"target kind outside the enum", calibration + `VALUES ('cli', 'file', 'mock', 'mock/jev-heuristic-v1', 'mock', 'route',
			'support-bot', 'provisional', '{}', '{}', true)`, sqlCheckViolation},
		{"deactivate the active calibration", `UPDATE calibrations SET active = false
			WHERE snapshot = 'typesafe/jev-1.13-20260917' AND target_key = 'groundedness@1#0123456789abcdef#-'`, sqlOK},
		{"activate a replacement", calibration + `VALUES ('admin', 'fit', 'openrouter-decisions', 'typesafe/jev-1.13-20260917',
			'jev-1.13', 'evaluator', 'groundedness@1#0123456789abcdef#-', 'measured', '{"human":260}', '{}', true)`, sqlOK},

		// labels: one label per (trace, target, labeler); label_source enum.
		{"human label", `INSERT INTO labels (trace_id, target, label, labeled_by, sample, risk_raw, snapshot)
			VALUES ('t1', 'groundedness@1', 'should_fire', 'admin', '{"assistant_response":"60 days"}', 0.96,
			        'typesafe/jev-1.13-20260917')`, sqlOK},
		{"relabel by the same labeler", `INSERT INTO labels (trace_id, target, label, labeled_by)
			VALUES ('t1', 'groundedness@1', 'should_not_fire', 'admin')`, sqlUniqueViolation},
		{"same target by another labeler", `INSERT INTO labels (trace_id, target, label, labeled_by)
			VALUES ('t1', 'groundedness@1', 'should_fire', 'reviewer')`, sqlOK},
		{"claim and response targets", `INSERT INTO labels (trace_id, target, label, labeled_by, label_source)
			VALUES ('t1', 'claim:0', 'CONTRADICTED', 'ragtruth', 'public_dataset'),
			       ('t1', 'response', 'hallucinated', 'ragtruth', 'public_dataset')`, sqlOK},
		{"label source outside the enum", `INSERT INTO labels (trace_id, target, label, labeled_by, label_source)
			VALUES ('t2', 'response', 'grounded', 'judge', 'deep_audit')`, sqlCheckViolation},

		// studio_results: kind enum and one row per (run, sample, evaluator).
		{"test result", `INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref, answer, risk_raw, risk,
			level, status, snapshot, latency_ms, cost_usd) VALUES ('run-1', 'test', 's1', 'draft:0123456789abcdef',
			'{"noul":0.91}', 0.91, 0.91, 'critical', 'ok', 'typesafe/jev-1.13-20260917', 212, 0.0000123456)`, sqlOK},
		{"dry-run result", `INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref)
			VALUES ('run-2', 'dry_run', 's1', 'groundedness@1')`, sqlOK},
		{"same sample twice in a run", `INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref)
			VALUES ('run-1', 'test', 's1', 'draft:0123456789abcdef')`, sqlUniqueViolation},
		{"kind outside the enum", `INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref)
			VALUES ('run-3', 'shadow', 's1', 'groundedness@1')`, sqlCheckViolation},
	})

	// The trigger raises the SPEC message and leaves the row as it was.
	_, err := pool.Exec(ctx, `UPDATE evaluator_versions SET created_by = 'someone' WHERE version = 1`)
	if err == nil || !strings.Contains(err.Error(), immutableMessage) {
		t.Errorf("UPDATE of an evaluator version: err %v, want %q", err, immutableMessage)
	}
	var createdBy, changelog string
	if err := pool.QueryRow(ctx, `SELECT created_by, changelog FROM evaluator_versions
		WHERE evaluator_id = 'groundedness' AND version = 1`).Scan(&createdBy, &changelog); err != nil {
		t.Fatal(err)
	}
	if createdBy != "bootstrap" || changelog != "" {
		t.Errorf("evaluator version changed despite the trigger: created_by=%q changelog=%q", createdBy, changelog)
	}

	// Defaults the writers rely on.
	var message, source string
	var archived, active bool
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT message FROM policy_versions ORDER BY id LIMIT 1),
		       (SELECT archived FROM evaluators WHERE id = 'groundedness'),
		       (SELECT active FROM calibrations WHERE quality = 'mock' AND target_kind = 'evaluator'),
		       (SELECT label_source FROM labels WHERE labeled_by = 'admin')`).Scan(&message, &archived, &active, &source); err != nil {
		t.Fatal(err)
	}
	if message != "" || archived || !active || source != "human" {
		t.Errorf("defaults: message=%q archived=%v active=%v label_source=%q", message, archived, active, source)
	}
	var activeRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM calibrations
		WHERE active AND snapshot = 'typesafe/jev-1.13-20260917' AND target_key = 'groundedness@1#0123456789abcdef#-'`).Scan(&activeRows); err != nil {
		t.Fatal(err)
	}
	if activeRows != 1 {
		t.Errorf("%d active calibrations for one target and snapshot, want 1", activeRows)
	}
	var cost string
	if err := pool.QueryRow(ctx, `SELECT cost_usd::text FROM studio_results WHERE run_id = 'run-1'`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != "0.0000123456" {
		t.Errorf("studio_results.cost_usd = %s, want 0.0000123456 kept exactly", cost)
	}
}

func TestMigration0004Schema(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	want := map[string][]string{
		"settings": {
			"key text not null",
			"value jsonb not null",
			"version bigint not null default 1",
			"updated_by text not null",
			"updated_at timestamp with time zone not null default now()",
		},
		"credentials": {
			"name text not null",
			"provider text not null",
			"ciphertext bytea not null",
			"nonce bytea not null",
			"key_id text not null",
			"masked text not null",
			"fingerprint text not null",
			"allowed_hosts text[] not null",
			"created_at timestamp with time zone not null default now()",
			"rotated_at timestamp with time zone",
			"last_verified_at timestamp with time zone",
			"last_verify_status text",
			"last_used_at timestamp with time zone",
		},
		"api_keys": {
			"id text not null",
			"label text not null",
			"scopes text[] not null",
			"sha256 text not null",
			"prefix text not null",
			"created_by text not null",
			"created_at timestamp with time zone not null default now()",
			"last_used_at timestamp with time zone",
			"revoked_at timestamp with time zone",
		},
		"admin_audit_log": {
			"id bigint not null default nextval('admin_audit_log_id_seq'::regclass)",
			"at timestamp with time zone not null default now()",
			"actor text not null",
			"action text not null",
			"target text",
			"diff jsonb",
		},
		"cost_events": {
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
		},
	}
	for _, table := range tables0004 {
		requireColumns(t, ctx, pool, table, want[table])
	}
	requireIndexes(t, ctx, pool, tables0004, []string{
		"admin_audit_log_pkey: CREATE UNIQUE INDEX admin_audit_log_pkey ON admin_audit_log USING btree (id)",
		"api_keys_pkey: CREATE UNIQUE INDEX api_keys_pkey ON api_keys USING btree (id)",
		"api_keys_sha256_key: CREATE UNIQUE INDEX api_keys_sha256_key ON api_keys USING btree (sha256)",
		"cost_events_pkey: CREATE UNIQUE INDEX cost_events_pkey ON cost_events USING btree (id)",
		"credentials_pkey: CREATE UNIQUE INDEX credentials_pkey ON credentials USING btree (name)",
		"idx_cost_events_time: CREATE INDEX idx_cost_events_time ON cost_events USING btree (occurred_at DESC, component)",
		"settings_pkey: CREATE UNIQUE INDEX settings_pkey ON settings USING btree (key)",
	})

	const credential = `INSERT INTO credentials (name, provider, ciphertext, nonce, key_id, masked, fingerprint, allowed_hosts) `
	const apiKey = `INSERT INTO api_keys (id, label, scopes, sha256, prefix, created_by) VALUES `
	runStatementCases(t, ctx, pool, []statementCase{
		{"setting", `INSERT INTO settings (key, value, updated_by) VALUES ('budgets', '{"decision_usd_day":2}', 'admin')`, sqlOK},
		{"setting without a value", `INSERT INTO settings (key, value, updated_by) VALUES ('judge', NULL, 'admin')`, sqlNotNullViolation},

		// credentials: the name enum, and never without its host binding.
		{"every credential name", credential + `SELECT n, 'openrouter', '\x00'::bytea, '\x01'::bytea, '0123456789abcdef',
			'sk-or-...abcd', 'fp', ARRAY['openrouter.ai'] FROM unnest(ARRAY['openrouter','typesafe','anthropic','openai',
			'compatible','upstream-default','upstream-anthropic','upstream-fallback']) n`, sqlOK},
		{"credential name outside the enum", credential + `VALUES ('github', 'github', '\x00'::bytea, '\x01'::bytea,
			'0123456789abcdef', 'ghp_...abcd', 'fp', ARRAY['api.github.com'])`, sqlCheckViolation},
		{"credential without allowed hosts", `INSERT INTO credentials (name, provider, ciphertext, nonce, key_id, masked,
			fingerprint) VALUES ('openai', 'openai', '\x00'::bytea, '\x01'::bytea, 'k', 'm', 'f')`, sqlNotNullViolation},

		// api_keys: scopes within {ingest, read, content, admin}; unique key hashes.
		{"ingest and read key", apiKey + `('k1', 'app', ARRAY['ingest','read','content'], 'h1', 'ta_1234', 'env')`, sqlOK},
		{"admin key", apiKey + `('k2', 'setup-admin', ARRAY['admin'], 'h2', 'ta_5678', 'setup')`, sqlOK},
		{"key without scopes", apiKey + `('k3', 'none', ARRAY[]::text[], 'h3', 'ta_9abc', 'admin')`, sqlOK},
		{"unknown scope", apiKey + `('k4', 'bad', ARRAY['write'], 'h4', 'ta_def0', 'admin')`, sqlCheckViolation},
		{"unknown scope among known ones", apiKey + `('k5', 'bad', ARRAY['read','superuser'], 'h5', 'ta_def1', 'admin')`, sqlCheckViolation},
		{"same key hash twice", apiKey + `('k6', 'copy', ARRAY['read'], 'h1', 'ta_1234', 'admin')`, sqlUniqueViolation},

		{"admin audit entry", `INSERT INTO admin_audit_log (actor, action, target, diff)
			VALUES ('setup-admin', 'credential.create', 'openrouter', '{"masked":"sk-or-...abcd"}')`, sqlOK},
		{"admin audit entry without an actor", `INSERT INTO admin_audit_log (action) VALUES ('policy.save')`, sqlNotNullViolation},

		{"cost event", `INSERT INTO cost_events (trace_id, route, component, kind, provider, model, input_tokens,
			output_tokens, cost_usd, cost_source, price_table_version, latency_ms, status, questions)
			VALUES ('t1', 'support-bot', 'gate', 'jev', 'openrouter', 'typesafe/jev-1.13', 812, 0, 0.0000341040,
			        'reported', '1', 212, 'ok', 4)`, sqlOK},
		{"cost event without a component", `INSERT INTO cost_events (kind, cost_source) VALUES ('llm', 'computed')`, sqlNotNullViolation},
	})

	var version int64
	var attempt int16
	var cost string
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT version FROM settings WHERE key = 'budgets'),
		       (SELECT attempt FROM cost_events LIMIT 1),
		       (SELECT cost_usd::text FROM cost_events LIMIT 1)`).Scan(&version, &attempt, &cost); err != nil {
		t.Fatal(err)
	}
	if version != 1 || attempt != 0 || cost != "0.0000341040" {
		t.Errorf("defaults: settings.version=%d cost_events.attempt=%d cost_usd=%s", version, attempt, cost)
	}
}

func requireColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, want []string) {
	t.Helper()
	if got := columnContract(t, ctx, pool, table); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s columns:\n%s\nwant (SPEC §2.3):\n%s", table, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// requireIndexes compares every index on tables, as "name: definition" with the
// schema qualifier removed, with want.
func requireIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tables, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT indexname || ': ' || replace(indexdef, current_schema() || '.', '')
		FROM pg_indexes WHERE schemaname = current_schema() AND tablename = ANY($1)
		ORDER BY indexname`, tables)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("indexes:\n%s\nwant (SPEC §2.3):\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// seedConfigRows writes one row into every 0003 and 0004 table.
func seedConfigRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, sql := range []string{
		`INSERT INTO policy_versions (source, author, doc, yaml, sha256, file_sha256)
		 VALUES ('file', 'system', '{"version":1}', 'version: 1', 'a1', 'f1')`,
		`INSERT INTO evaluators (id, name, category, origin, latest_version)
		 VALUES ('groundedness', 'Groundedness', 'grounding', 'builtin', 1)`,
		`INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256, calibration_key, created_by)
		 VALUES ('groundedness', 1, '{"id":"groundedness"}', 's1', 'q1', 'k1', 'bootstrap')`,
		`INSERT INTO calibrations (created_by, origin, backend, snapshot, family, target_kind, target_key, quality,
		                           label_source, entry)
		 VALUES ('cli', 'file', 'mock', 'mock/jev-heuristic-v1', 'mock', 'evaluator', 'groundedness@1#0123456789abcdef#-',
		         'mock', '{"mock":true}', '{}')`,
		`INSERT INTO labels (trace_id, target, label, labeled_by) VALUES ('t1', 'groundedness@1', 'should_fire', 'admin')`,
		`INSERT INTO studio_results (run_id, kind, sample_id, evaluator_ref, cost_usd)
		 VALUES ('run-1', 'test', 's1', 'groundedness@1', 0.0000123456)`,
		`INSERT INTO settings (key, value, updated_by) VALUES ('epoch', '"01J"', 'system')`,
		`INSERT INTO credentials (name, provider, ciphertext, nonce, key_id, masked, fingerprint, allowed_hosts)
		 VALUES ('openrouter', 'openrouter', '\x00'::bytea, '\x01'::bytea, 'k', 'sk-or-...abcd', 'fp', ARRAY['openrouter.ai'])`,
		`INSERT INTO api_keys (id, label, scopes, sha256, prefix, created_by)
		 VALUES ('k1', 'app', ARRAY['ingest','read'], 'h1', 'ta_1234', 'env')`,
		`INSERT INTO admin_audit_log (actor, action, target) VALUES ('setup-admin', 'setup.claim', 'setup')`,
		`INSERT INTO cost_events (component, kind, cost_source, cost_usd) VALUES ('gate', 'jev', 'computed', 0.0000341040)`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
}

// Both files run again (twice each) over live rows: nothing in the catalog or
// in any row changes, and the trigger and the one-active index still hold.
func TestMigration0003And0004RerunKeepsRows(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := schema.Pool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedConfigRows(t, ctx, pool)
	files := []migration{migrationByName(t, migration0003), migrationByName(t, migration0004)}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	before := snapshot(t, ctx, tx)
	for round := 1; round <= 2; round++ {
		for _, m := range files {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				t.Fatalf("%s, run %d over live rows: %v", m.name, round, err)
			}
		}
	}
	if after := snapshot(t, ctx, tx); after != before {
		t.Errorf("re-running 0003 and 0004 changed the database\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	runStatementCases(t, ctx, pool, []statementCase{
		{"trigger still installed", `UPDATE evaluator_versions SET changelog = 'x'`, sqlRaiseException},
		{"one active calibration still enforced", `INSERT INTO calibrations (created_by, origin, backend, snapshot,
			family, target_kind, target_key, quality, label_source, entry) VALUES ('cli', 'file', 'mock',
			'mock/jev-heuristic-v1', 'mock', 'evaluator', 'groundedness@1#0123456789abcdef#-', 'mock', '{}', '{}')`, sqlUniqueViolation},
		{"sequences continue after the rows", `INSERT INTO cost_events (component, kind, cost_source)
			VALUES ('deep_audit_verify', 'llm', 'reported')`, sqlOK},
	})
}

// rowDigests returns "count md5" of every row of each table.
func rowDigests(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		var count int
		var digest string
		if err := pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*), coalesce(md5(string_agg(r::text, E'\n' ORDER BY r::text)), '') FROM %s r`,
			pgx.Identifier{table}.Sanitize())).Scan(&count, &digest); err != nil {
			t.Fatalf("digest %s: %v", table, err)
		}
		out[table] = fmt.Sprintf("%d %s", count, digest)
	}
	return out
}

// A database the P1-core runner left at 0002, holding legacy audits and v2
// traffic, upgrades in place: 0003 and 0004 are applied once, in order, under
// the lock, and no existing row changes.
func TestMigrateUpgradesSeeded0002Database(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	pool := taggedPool(t, schema)
	migrations := mustLoadMigrations(t)
	if len(migrations) < 4 || migrations[2].name != migration0003 || migrations[3].name != migration0004 {
		t.Fatalf("embedded migrations = %v, want 0003 and 0004 after 0001 and 0002", migrationNames(migrations))
	}

	if err := applyMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	seedLegacyAudits(t, ctx, pool)
	if err := applyMigrations(ctx, pool, migrations[:2]); err != nil {
		t.Fatalf("apply 0002: %v", err)
	}
	for _, sql := range []string{
		`INSERT INTO traces (trace_id, source, api_format, path, route, mode, action, gate_status, audit_status, reasons, flags)
		 VALUES ('0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90', 'proxy', 'openai_chat', '/v1/chat/completions', 'support-bot',
		         'flag', 'flag', 'ok', 'completed', '{contradiction:critical}', '{test_mode}')`,
		`INSERT INTO gate_decisions (trace_id, route, mode, effective_mode, status, action, decision, cost_usd)
		 VALUES ('0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90', 'support-bot', 'flag', 'flag', 'ok', 'flag',
		         '{"spec":"decision/v1"}', 0.0000341040)`,
		`INSERT INTO trace_bodies (trace_id, messages, bytes, expires_at)
		 VALUES ('0192f3a0-7c1e-7b2a-9d11-5b6c0e7f8a90', '[]', 2, now() + interval '7 days')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("seed 0002 row: %v", err)
		}
	}
	for _, table := range append(append([]string{}, tables0003...), tables0004...) {
		if tableExists(t, ctx, pool, table) {
			t.Fatalf("table %s exists before 0003/0004 ran", table)
		}
	}
	existing := []string{"audits", "audit_claims", "traces", "gate_decisions", "trace_bodies"}
	before := rowDigests(t, ctx, pool, existing)

	s, err := NewPostgresStore(ctx, schema.URL) // applies the pending 0003 and 0004
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	t.Cleanup(s.Close)

	if got := appliedNames(t, ctx, pool); strings.Join(got, ",") != strings.Join(migrationNames(migrations), ",") {
		t.Errorf("schema_migrations = %v, want %v", got, migrationNames(migrations))
	}
	if after := rowDigests(t, ctx, pool, existing); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("upgrade changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	for _, table := range append(append([]string{}, tables0003...), tables0004...) {
		if !tableExists(t, ctx, pool, table) {
			t.Errorf("table %s missing after the upgrade", table)
			continue
		}
		var n int
		if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, pgx.Identifier{table}.Sanitize())).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("table %s has %d rows after the upgrade; 0003 and 0004 backfill nothing", table, n)
		}
	}
	if _, _, err := s.ListAudits(ctx, AuditFilter{Limit: 10}); err != nil {
		t.Errorf("ListAudits after the upgrade: %v", err)
	}

	// The upgrade released the lock, and the database is complete: a later
	// run finishes promptly and applies nothing.
	requireMigrateSoon(t, pool)
	requireLockReleased(t, ctx, pool, schema.Name)
	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != len(migrations) {
		t.Errorf("schema_migrations has %d rows after a re-run, want %d", total, len(migrations))
	}
}
