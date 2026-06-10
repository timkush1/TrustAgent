package store

import (
	"context"
	"embed"
	"encoding/json"
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

// PostgresStore implements Store on top of a pgx connection pool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects, verifies the connection, and applies pending
// migrations. Migrations are embedded *.up.sql files applied in filename
// order and tracked in schema_migrations — a deliberate minimal runner (the
// schema history is linear; see docs/DECISIONS.md).
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

	s := &PostgresStore{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations failed: %w", err)
	}
	return s, nil
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return err
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`, name,
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}

		sql, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("applying %s: %w", name, err)
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			return err
		}
		log.Printf("Applied migration %s", name)
	}
	return nil
}

func (s *PostgresStore) SaveAudit(ctx context.Context, record *AuditRecord) error {
	timings, err := json.Marshal(record.StepTimings)
	if err != nil {
		return fmt.Errorf("marshal step timings: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	_, err = tx.Exec(ctx, `
		INSERT INTO audits (
			audit_id, request_id, user_query, llm_response, model,
			faithfulness_score, grade, hallucination_detected,
			reasoning_trace, processing_time_ms, step_timings, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (audit_id) DO NOTHING`,
		record.AuditID, record.RequestID, record.UserQuery, record.LLMResponse,
		record.Model, record.FaithfulnessScore, record.Grade,
		record.HallucinationDetected, record.ReasoningTrace,
		record.ProcessingTimeMs, timings, record.CreatedAt,
	)
	if err != nil {
		return err
	}

	for i, claim := range record.Claims {
		evidence, err := json.Marshal(claim.Evidence)
		if err != nil {
			return fmt.Errorf("marshal evidence: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_claims (audit_id, position, claim, status, confidence, evidence)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			record.AuditID, i, claim.Claim, claim.Status, claim.Confidence, evidence,
		); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

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
		SELECT audit_id, request_id, user_query, llm_response, model,
		       faithfulness_score, grade, hallucination_detected,
		       reasoning_trace, processing_time_ms, step_timings, created_at
		FROM audits
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, whereClause, len(args)-1, len(args))

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
	row, err := s.pool.Query(ctx, `
		SELECT audit_id, request_id, user_query, llm_response, model,
		       faithfulness_score, grade, hallucination_detected,
		       reasoning_trace, processing_time_ms, step_timings, created_at
		FROM audits WHERE audit_id = $1`, auditID)
	if err != nil {
		return nil, err
	}
	defer row.Close()

	if !row.Next() {
		return nil, pgx.ErrNoRows
	}
	record, err := scanAudit(row)
	if err != nil {
		return nil, err
	}
	row.Close()

	claimRows, err := s.pool.Query(ctx, `
		SELECT claim, status, confidence, evidence
		FROM audit_claims WHERE audit_id = $1 ORDER BY position`, auditID)
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

func scanAudit(row pgx.Rows) (*AuditRecord, error) {
	var record AuditRecord
	var timings []byte
	if err := row.Scan(
		&record.AuditID, &record.RequestID, &record.UserQuery, &record.LLMResponse,
		&record.Model, &record.FaithfulnessScore, &record.Grade,
		&record.HallucinationDetected, &record.ReasoningTrace,
		&record.ProcessingTimeMs, &timings, &record.CreatedAt,
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
