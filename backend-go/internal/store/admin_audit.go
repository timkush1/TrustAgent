package store

import (
	"context"
	"encoding/json"
	"time"
)

// AdminAuditRecord is one admin_audit_log row as GET /api/admin/audit-log (#70) shows it (api.ts
// AdminAuditEntry). Diff never holds a secret: InsertAdminAudit scrubs it on the way in (D44).
type AdminAuditRecord struct {
	ID     int64           `json:"id"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target *string         `json:"target"`
	Diff   json.RawMessage `json:"diff"`
}

// ListAdminAudit returns a keyset page of the admin audit log, newest first, and the cursor of
// the next page ("" at the end). limit is clamped to [1, 200] (default 50); a cursor this store
// did not issue is ErrBadCursor.
func (s *PostgresStore) ListAdminAudit(ctx context.Context, limit int, cursor string) ([]AdminAuditRecord, string, error) {
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	before := int64(0)
	if cursor != "" {
		var err error
		if before, err = decodeIDCursor(cursor); err != nil {
			return nil, "", err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, at, actor, action, target, diff FROM admin_audit_log
		WHERE ($1 = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []AdminAuditRecord{}
	for rows.Next() {
		var r AdminAuditRecord
		var diff []byte
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.Target, &diff); err != nil {
			return nil, "", err
		}
		r.At = r.At.UTC()
		if len(diff) > 0 {
			r.Diff = diff
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeIDCursor(out[limit-1].ID)
	}
	return out, next, nil
}
