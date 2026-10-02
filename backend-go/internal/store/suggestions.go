package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// GET /api/evidence/suggestions (SPEC §2.4 row 51, §7.1; P4-PLAN D64): the evidence suggestions
// scan the most recent answers that were checked against no evidence.

// MaxScanBodies is the most bodies one suggestion scan reads (D64).
const MaxScanBodies = 500

// ScanFilter selects the bodies a suggestion scan reads: traces since Since on Route ("" = every
// route) whose answer used no evidence source, with a body still kept; the most recent first, at
// most Limit (1-500).
type ScanFilter struct {
	Route string
	Since time.Time
	Limit int
	// Now is the body-expiry reference.
	Now time.Time
}

// ScanBody is one body a suggestion scan reads: its trace, route and request messages (a JSON
// array of {role, content, name?}).
type ScanBody struct {
	TraceID  string
	Route    string
	Messages json.RawMessage
}

// NoEvidenceBodies returns the bodies f selects. Direct audits (POST /api/audit) carry no request
// messages and are left out.
func (s *PostgresStore) NoEvidenceBodies(ctx context.Context, f ScanFilter) ([]ScanBody, error) {
	limit := min(max(f.Limit, 1), MaxScanBodies)
	rows, err := s.pool.Query(ctx, `
		SELECT t.trace_id, t.route, b.messages
		FROM traces t JOIN trace_bodies b ON b.trace_id = t.trace_id
		WHERE t.created_at >= $2 AND ($3 = '' OR t.route = $3) AND t.source <> 'direct'
			AND b.expires_at > $1 AND `+evidenceUsedSQL+` = 0
		ORDER BY t.created_at DESC, t.trace_id DESC LIMIT $4`,
		f.Now.UTC(), f.Since.UTC(), f.Route, limit)
	if err != nil {
		return nil, fmt.Errorf("no-evidence bodies: %w", err)
	}
	defer rows.Close()
	out := []ScanBody{}
	for rows.Next() {
		var b ScanBody
		if err := rows.Scan(&b.TraceID, &b.Route, &b.Messages); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
