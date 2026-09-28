package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Recorded gate decisions for the policy dry-run ("Preview impact", SPEC §2.4 row 26, §0 row 47):
// a pure replay of stored answers through decide(), so it reads decision/v1 documents, never the
// decision model.

// ReplayRow is one recorded first-attempt gate decision with what a replay needs.
type ReplayRow struct {
	TraceID   string
	CreatedAt time.Time
	Route     string
	// PolicyVersion is the policy version the trace was gated under, when known.
	PolicyVersion *int64
	// Stream reports whether the client asked for a streamed answer.
	Stream bool
	Source string
	// Late marks a decision that arrived after the route deadline and was never enforced.
	Late bool
	// Decision is the decision/v1 document as recorded, raw answers included.
	Decision         json.RawMessage
	DecisionSnapshot *string
	// AuditVerdict is the newest deep-audit verdict of the trace, if any.
	AuditVerdict *string
	// Body is the trace body when ReplayFilter.WithBodies is set and one was kept (it may have
	// expired: check ExpiresAt).
	Body *BodyRecord
}

// ReplayFilter selects the decisions to replay.
type ReplayFilter struct {
	// Since bounds the window (created_at >= Since).
	Since time.Time
	// Limit caps the rows read, newest first (1-100000; default 10000).
	Limit int
	// Routes, when not empty, keeps only decisions recorded on these routes.
	Routes []string
	// WithBodies joins the trace bodies (for detectors that must be re-run).
	WithBodies bool
}

// MaxReplayRows is the largest dry-run window in rows (SPEC §2.4 row 26: limit <= 100000).
const MaxReplayRows = 100000

// ForEachReplayDecision streams the first-attempt decisions of the window to fn, newest first
// (created_at, then trace_id, descending). fn's error stops the scan and is returned.
func (s *PostgresStore) ForEachReplayDecision(ctx context.Context, f ReplayFilter, fn func(*ReplayRow) error) error {
	limit := f.Limit
	if limit <= 0 {
		limit = 10000
	}
	limit = min(limit, MaxReplayRows)
	args := []any{f.Since}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var q strings.Builder
	q.WriteString(`
		SELECT d.trace_id, d.created_at, d.route, d.policy_version, COALESCE(t.stream, false), COALESCE(t.source, ''),
			d.late, d.decision, d.decision_snapshot, a.verdict`)
	if f.WithBodies {
		q.WriteString(`, b.trace_id IS NOT NULL, b.user_request, b.system_instructions, b.response_text, b.evidence,
			b.rules, b.expires_at`)
	}
	q.WriteString(`
		FROM gate_decisions d
		LEFT JOIN traces t ON t.trace_id = d.trace_id
		LEFT JOIN LATERAL (SELECT verdict FROM audits WHERE trace_id = d.trace_id
			ORDER BY created_at DESC LIMIT 1) a ON true`)
	if f.WithBodies {
		q.WriteString(` LEFT JOIN trace_bodies b ON b.trace_id = d.trace_id`)
	}
	q.WriteString(` WHERE d.attempt = 0 AND d.created_at >= $1`)
	if len(f.Routes) > 0 {
		q.WriteString(` AND d.route = ANY(` + arg(f.Routes) + `)`)
	}
	q.WriteString(` ORDER BY d.created_at DESC, d.trace_id DESC LIMIT ` + arg(limit))

	rows, err := s.pool.Query(ctx, q.String(), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r ReplayRow
		dest := []any{&r.TraceID, &r.CreatedAt, &r.Route, &r.PolicyVersion, &r.Stream, &r.Source, &r.Late,
			&r.Decision, &r.DecisionSnapshot, &r.AuditVerdict}
		var (
			hasBody bool
			body    BodyRecord
			rules   []byte
			expires *time.Time
		)
		if f.WithBodies {
			dest = append(dest, &hasBody, &body.UserRequest, &body.SystemInstructions, &body.ResponseText, &body.Evidence,
				&rules, &expires)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		r.CreatedAt = r.CreatedAt.UTC()
		if hasBody {
			body.TraceID = r.TraceID
			if expires != nil {
				body.ExpiresAt = *expires
			}
			if len(rules) > 0 {
				if err := json.Unmarshal(rules, &body.Rules); err != nil {
					return fmt.Errorf("trace body rules: %w", err)
				}
			}
			r.Body = &body
		}
		if err := fn(&r); err != nil {
			return err
		}
	}
	return rows.Err()
}
