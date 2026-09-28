package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIKeyRecord is one api_keys row (migration 0004). The key itself is never stored: SHA256 is the
// lowercase hex SHA-256 of the key and Prefix its first characters, for display.
type APIKeyRecord struct {
	ID         string
	Label      string
	Scopes     []string
	SHA256     string
	Prefix     string
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Active reports whether the key is not revoked.
func (k *APIKeyRecord) Active() bool { return k.RevokedAt == nil }

// AdminAuditEntry is one admin_audit_log row. Diff never contains secrets.
type AdminAuditEntry struct {
	Actor  string
	Action string
	Target string
	Diff   any
}

const apiKeyColumns = `id, label, scopes, sha256, prefix, created_by, created_at, last_used_at, revoked_at`

func scanAPIKey(row pgx.Row) (*APIKeyRecord, error) {
	var k APIKeyRecord
	if err := row.Scan(&k.ID, &k.Label, &k.Scopes, &k.SHA256, &k.Prefix, &k.CreatedBy, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	k.CreatedAt = k.CreatedAt.UTC()
	if k.LastUsedAt != nil {
		t := k.LastUsedAt.UTC()
		k.LastUsedAt = &t
	}
	if k.RevokedAt != nil {
		t := k.RevokedAt.UTC()
		k.RevokedAt = &t
	}
	return &k, nil
}

// insertAPIKey inserts k and fills CreatedAt.
func insertAPIKey(ctx context.Context, q pgx.Tx, k *APIKeyRecord) error {
	if k.ID == "" || k.Label == "" || k.SHA256 == "" || len(k.Scopes) == 0 {
		return errors.New("store: an api key needs an id, a label, a sha256 and at least one scope")
	}
	return q.QueryRow(ctx, `
		INSERT INTO api_keys (id, label, scopes, sha256, prefix, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		k.ID, k.Label, k.Scopes, k.SHA256, k.Prefix, k.CreatedBy).Scan(&k.CreatedAt)
}

// insertAdminAudit appends one admin_audit_log row.
func insertAdminAudit(ctx context.Context, q pgx.Tx, e AdminAuditEntry) error {
	var diff []byte
	if e.Diff != nil {
		b, err := json.Marshal(e.Diff)
		if err != nil {
			return fmt.Errorf("store: admin audit diff: %w", err)
		}
		diff = b
	}
	var target *string
	if e.Target != "" {
		target = &e.Target
	}
	_, err := q.Exec(ctx, `INSERT INTO admin_audit_log (actor, action, target, diff) VALUES ($1, $2, $3, $4)`,
		e.Actor, e.Action, target, diff)
	return err
}

// CreateAPIKey inserts a key and its admin_audit_log row (action api_key.create) in one
// transaction, and fills k.CreatedAt.
func (s *PostgresStore) CreateAPIKey(ctx context.Context, k *APIKeyRecord, actor string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertAPIKey(ctx, tx, k); err != nil {
			return fmt.Errorf("store: create api key: %w", err)
		}
		return insertAdminAudit(ctx, tx, AdminAuditEntry{
			Actor: actor, Action: "api_key.create", Target: k.ID,
			Diff: map[string]any{"label": k.Label, "scopes": k.Scopes, "prefix": k.Prefix},
		})
	})
}

// APIKeyBySHA256 returns the key whose hash is sha (revoked keys included), or ErrNotFound.
func (s *PostgresStore) APIKeyBySHA256(ctx context.Context, sha string) (*APIKeyRecord, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE sha256 = $1`, sha))
}

// APIKeyByID returns a key (revoked keys included), or ErrNotFound.
func (s *PostgresStore) APIKeyByID(ctx context.Context, id string) (*APIKeyRecord, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE id = $1`, id))
}

// ListAPIKeys returns every key, newest first.
func (s *PostgresStore) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_keys ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()
	out := []APIKeyRecord{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list api keys: %w", err)
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// RevokeAPIKey sets revoked_at on an active key and logs api_key.revoke, in one transaction. It
// returns the row and whether this call revoked it (false for a key that was already revoked), or
// ErrNotFound.
func (s *PostgresStore) RevokeAPIKey(ctx context.Context, id, actor string) (*APIKeyRecord, bool, error) {
	var (
		out     *APIKeyRecord
		revoked bool
	)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		k, err := scanAPIKey(tx.QueryRow(ctx, `
			UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL
			RETURNING `+apiKeyColumns, id))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			k, err = scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_keys WHERE id = $1`, id))
			if err != nil {
				return err
			}
			out = k
			return nil
		case err != nil:
			return err
		}
		out, revoked = k, true
		return insertAdminAudit(ctx, tx, AdminAuditEntry{
			Actor: actor, Action: "api_key.revoke", Target: id, Diff: map[string]any{"label": k.Label},
		})
	})
	if err != nil {
		return nil, false, err
	}
	return out, revoked, nil
}

// TouchAPIKey records that a key was used at at.
func (s *PostgresStore) TouchAPIKey(ctx context.Context, id string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2)`, id, at)
	return err
}

// AdminKeyExists reports whether an active api_keys row holds the admin scope.
func (s *PostgresStore) AdminKeyExists(ctx context.Context) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM api_keys WHERE 'admin' = ANY (scopes) AND revoked_at IS NULL)`).Scan(&ok)
	return ok, err
}

// AppendAdminAudit appends one admin_audit_log row.
func (s *PostgresStore) AppendAdminAudit(ctx context.Context, e AdminAuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return insertAdminAudit(ctx, tx, e) })
}
