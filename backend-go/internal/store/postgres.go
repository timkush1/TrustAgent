package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the pg_advisory_lock key that serializes migration runs
// across every process sharing the database (SPEC §2.3).
const migrationLockID int64 = 7212026

// PostgresStore implements Store on top of a pgx connection pool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects, verifies the connection, and applies pending
// migrations (see Migrate).
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid database URL: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres unreachable: %w", err)
	}

	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations failed: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// migration is one embedded *.up.sql file.
type migration struct {
	name string
	sql  string
}

// loadMigrations returns the embedded *.up.sql files in filename order. The
// schema history is linear and forward-only (see docs/DECISIONS.md), so the
// runner is deliberately minimal.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	migrations := make([]migration, 0, len(names))
	for _, name := range names {
		sql, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{name: name, sql: string(sql)})
	}
	return migrations, nil
}

// Migrate applies every pending embedded migration, tracked by filename in
// schema_migrations. It is safe to call concurrently from several processes:
//
//  1. pg_advisory_lock(7212026) is taken on a dedicated connection before
//     schema_migrations is created or read, so runners are fully serialized;
//  2. each file runs in one transaction together with its schema_migrations
//     insert, so a failed file leaves neither partial DDL nor a tracking row;
//  3. the lock is released (or its session closed) before returning.
//
// Re-running is a no-op. Migration files must therefore be transaction-safe
// (no CREATE INDEX CONCURRENTLY and the like). Tables are created in the first
// schema on the connection's search_path, which is how storetest isolates tests.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	return applyMigrations(ctx, pool, migrations)
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, migrations []migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		// The lock may or may not have been granted before the failure;
		// closing the session is the only way to be sure it is not kept.
		discardConn(conn)
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer releaseMigrationLock(conn)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.name] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
		log.Printf("Applied migration %s", m.name)
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT filename FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := make(map[string]bool, len(names))
	for _, name := range names {
		applied[name] = true
	}
	return applied, nil
}

// applyMigration runs one file and records it in a single transaction.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("applying %s: begin: %w", m.name, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// No arguments, so pgx uses the simple protocol, which accepts a
	// multi-statement file.
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("applying %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (filename) VALUES ($1)`, m.name); err != nil {
		return fmt.Errorf("recording %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("applying %s: commit: %w", m.name, err)
	}
	return nil
}

// releaseMigrationLock unlocks and returns conn to the pool. It uses its own
// context so a cancelled migration still releases the lock.
func releaseMigrationLock(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var released bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID).Scan(&released)
	if err != nil || !released {
		// A pooled connection that might still hold the lock would block every
		// later migrator forever; ending the session releases it server-side.
		log.Printf("Releasing migration lock failed (released=%v, err=%v); closing connection", released, err)
		discardConn(conn)
		return
	}
	conn.Release()
}

// discardConn removes conn from the pool and closes its session.
func discardConn(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}

// SaveAudit inserts the audit and its claims in one transaction. An existing
// audit_id is never touched: the insert is ON CONFLICT DO NOTHING and, when it
// affects no row, ErrDuplicate is returned before any claim is written (B4).
//
// Defaults applied to the stored row: trace_id = audit_id when TraceID is
// empty, request_id = trace_id when RequestID is empty, and created_at = now
// when CreatedAt is zero. ClientRequestID is stored only when it passes
// ClientRequestID; anything else is dropped with a warning.
func (s *PostgresStore) SaveAudit(ctx context.Context, record *AuditRecord) error {
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
	timings, err := json.Marshal(record.StepTimings)
	if err != nil {
		return fmt.Errorf("marshal step timings: %w", err)
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
			reasoning_trace, processing_time_ms, step_timings, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (audit_id) DO NOTHING`,
		record.AuditID, traceID, requestID, clientRequestID,
		record.UserQuery, record.LLMResponse, record.Model,
		record.FaithfulnessScore, record.Grade, record.HallucinationDetected,
		record.ReasoningTrace, record.ProcessingTimeMs, timings, createdAt,
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
			evidence, err := json.Marshal(claim.Evidence)
			if err != nil {
				return fmt.Errorf("marshal evidence: %w", err)
			}
			batch.Queue(`
				INSERT INTO audit_claims (audit_id, position, claim, status, confidence, evidence)
				VALUES ($1,$2,$3,$4,$5,$6)`,
				record.AuditID, i, claim.Claim, claim.Status, claim.Confidence, evidence,
			)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert claims: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// auditColumns is the SELECT list scanned by scanAudit. trace_id can only be
// NULL for a row inserted after 0002 by a pre-0002 writer (an old proxy during
// a rolling upgrade); the legacy rule trace_id = audit_id covers it.
const auditColumns = `
	audit_id, COALESCE(trace_id, audit_id), request_id, client_request_id,
	user_query, llm_response, model,
	faithfulness_score, grade, hallucination_detected,
	reasoning_trace, processing_time_ms, step_timings, created_at`

func (s *PostgresStore) ListAudits(ctx context.Context, filter ListFilter) ([]AuditRecord, int, error) {
	where := []string{"TRUE"}
	args := []any{}

	if filter.Grade != "" {
		args = append(args, filter.Grade)
		where = append(where, fmt.Sprintf("grade = $%d", len(args)))
	}
	if filter.Flagged != nil {
		args = append(args, *filter.Flagged)
		where = append(where, fmt.Sprintf("hallucination_detected = $%d", len(args)))
	}
	whereClause := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM audits WHERE "+whereClause, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit, filter.Offset)
	query := fmt.Sprintf(`
		SELECT %s
		FROM audits
		WHERE %s
		ORDER BY created_at DESC, audit_id DESC
		LIMIT $%d OFFSET $%d`, auditColumns, whereClause, len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	records := []AuditRecord{}
	for rows.Next() {
		record, err := scanAudit(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, *record)
	}
	return records, total, rows.Err()
}

func (s *PostgresStore) GetAudit(ctx context.Context, auditID string) (*AuditRecord, error) {
	record, err := scanAudit(s.pool.QueryRow(ctx,
		"SELECT "+auditColumns+" FROM audits WHERE audit_id = $1", auditID))
	if err != nil {
		return nil, err // pgx.ErrNoRows when absent
	}

	claimRows, err := s.pool.Query(ctx, `
		SELECT claim, status, confidence, evidence
		FROM audit_claims WHERE audit_id = $1 ORDER BY position, id`, auditID)
	if err != nil {
		return nil, err
	}
	defer claimRows.Close()

	for claimRows.Next() {
		var claim ClaimRecord
		var evidence []byte
		if err := claimRows.Scan(&claim.Claim, &claim.Status, &claim.Confidence, &evidence); err != nil {
			return nil, err
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &claim.Evidence); err != nil {
				return nil, err
			}
		}
		if claim.Evidence == nil {
			claim.Evidence = []string{}
		}
		record.Claims = append(record.Claims, claim)
	}
	return record, claimRows.Err()
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

// scanAudit scans one auditColumns row; it accepts both pgx.Row and pgx.Rows.
func scanAudit(row pgx.Row) (*AuditRecord, error) {
	var record AuditRecord
	var timings []byte
	if err := row.Scan(
		&record.AuditID, &record.TraceID, &record.RequestID, &record.ClientRequestID,
		&record.UserQuery, &record.LLMResponse, &record.Model,
		&record.FaithfulnessScore, &record.Grade, &record.HallucinationDetected,
		&record.ReasoningTrace, &record.ProcessingTimeMs, &timings, &record.CreatedAt,
	); err != nil {
		return nil, err
	}
	if len(timings) > 0 {
		if err := json.Unmarshal(timings, &record.StepTimings); err != nil {
			return nil, err
		}
	}
	record.Claims = []ClaimRecord{}
	return &record, nil
}
