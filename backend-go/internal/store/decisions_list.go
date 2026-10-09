package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GET /api/gate/decisions (SPEC §2.4 endpoint 16, D10): one row per trace, its attempt-0 gate
// decision. A late decision is an upsert of that row, so it shows through late and updated_at;
// fallback re-gates (attempt 1) appear only in TraceDetail.decisions.

// DecisionFilter selects and pages the attempt-0 gate decisions, newest first. Empty strings and
// nil pointers match everything.
type DecisionFilter struct {
	Limit        int
	Cursor       string
	Since        *time.Time
	UpdatedSince *time.Time
	Route        string
	Action       string
	Status       string
	// Evaluator is an evaluator ref ("<id>@<version>") or a bare id (every version); Level is an
	// EvaluatorLevel. Together they select decisions where that evaluator reached that level;
	// Evaluator alone selects decisions that ran it, Level alone decisions where any evaluator
	// reached it.
	Evaluator    string
	Level        string
	Disagreement *bool
	Late         *bool
}

// DecisionRow is api.ts GateDecisionRow. It carries no text, so it needs no redaction.
type DecisionRow struct {
	TraceID          string    `json:"trace_id"`
	CreatedAt        time.Time `json:"created_at"`
	Route            string    `json:"route"`
	Mode             string    `json:"mode"`
	EffectiveMode    string    `json:"effective_mode"`
	Action           string    `json:"action"`
	WouldAction      *string   `json:"would_action"`
	Status           string    `json:"status"`
	Verdict          *string   `json:"verdict"`
	MaxRisk          *float64  `json:"max_risk"`
	Reasons          []string  `json:"reasons"`
	Late             bool      `json:"late"`
	DecisionSnapshot *string   `json:"decision_snapshot"`
	DecisionMs       *int64    `json:"decision_ms"`
	CostUSD          *float64  `json:"cost_usd"`
	AuditVerdict     *string   `json:"audit_verdict"`
	// EvaluatorLevels maps each evaluator ref of the decision to its level (null when it was not
	// assessed).
	EvaluatorLevels map[string]*string `json:"evaluator_levels"`
}

// decisionEvaluatorsSQL is the decision's evaluators object (gd aliases gate_decisions), or an
// empty object when the document has none, so jsonb_each never fails on a malformed row.
const decisionEvaluatorsSQL = `(CASE WHEN jsonb_typeof(gd.decision->'evaluators') = 'object'
	THEN gd.decision->'evaluators' ELSE '{}'::jsonb END)`

// disagreementSQL holds when the decision gd recorded that its evaluators disagreed (the
// decisions list's disagreement filter and the Studio's disagreement samples).
const disagreementSQL = `COALESCE(gd.decision->'disagreement' = 'true'::jsonb, false)`

// decisionRowSelect reads attempt-0 gate_decisions rows (alias gd) with the trace's deep-audit
// verdict (through traces.audit_id, as a trace summary reports it) and the evaluator levels.
const decisionRowSelect = `
	SELECT gd.trace_id, gd.created_at, gd.route, gd.mode, gd.effective_mode, gd.action, gd.would_action,
		gd.status, gd.verdict, gd.max_risk, gd.reasons, gd.late, gd.decision_snapshot, gd.decision_ms,
		gd.cost_usd::float8, ` + auditVerdictSQL + `,
		COALESCE((SELECT jsonb_object_agg(e.key, e.value->'level') FROM jsonb_each(` + decisionEvaluatorsSQL + `) e), '{}'::jsonb)
	FROM gate_decisions gd
	LEFT JOIN traces t ON t.trace_id = gd.trace_id
	LEFT JOIN audits a ON a.audit_id = t.audit_id
	WHERE gd.attempt = 0`

// ListDecisions returns a keyset page of attempt-0 gate decisions, newest first, and the cursor of
// the next page ("" at the end). It returns ErrBadCursor for a cursor this store did not issue.
func (s *PostgresStore) ListDecisions(ctx context.Context, f DecisionFilter) ([]DecisionRow, string, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	eq := func(col, v string) {
		if v != "" {
			where = append(where, col+" = "+arg(v))
		}
	}
	if f.Cursor != "" {
		at, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, fmt.Sprintf("(gd.created_at, gd.trace_id) < (%s, %s)", arg(at), arg(id)))
	}
	if f.Since != nil {
		where = append(where, "gd.created_at >= "+arg(*f.Since))
	}
	if f.UpdatedSince != nil {
		where = append(where, "gd.updated_at >= "+arg(*f.UpdatedSince))
	}
	eq("gd.route", f.Route)
	eq("gd.action", f.Action)
	eq("gd.status", f.Status)
	if f.Evaluator != "" || f.Level != "" {
		conds := []string{"TRUE"}
		if f.Evaluator != "" {
			// A ref matches its key; a bare id matches every version of it (keys are "<id>@<v>").
			p := arg(f.Evaluator)
			conds = append(conds, fmt.Sprintf("(e.key = %s OR split_part(e.key, '@', 1) = %s)", p, p))
		}
		if f.Level != "" {
			conds = append(conds, "e.value->>'level' = "+arg(f.Level))
		}
		where = append(where, "EXISTS (SELECT 1 FROM jsonb_each("+decisionEvaluatorsSQL+") e WHERE "+strings.Join(conds, " AND ")+")")
	}
	if f.Disagreement != nil {
		where = append(where, disagreementSQL+" = "+arg(*f.Disagreement))
	}
	if f.Late != nil {
		where = append(where, "gd.late = "+arg(*f.Late))
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := decisionRowSelect
	for _, w := range where {
		query += " AND " + w
	}
	query += " ORDER BY gd.created_at DESC, gd.trace_id DESC LIMIT " + arg(limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []DecisionRow{}
	for rows.Next() {
		var (
			r       DecisionRow
			maxRisk *float32
			levels  []byte
		)
		if err := rows.Scan(&r.TraceID, &r.CreatedAt, &r.Route, &r.Mode, &r.EffectiveMode, &r.Action, &r.WouldAction,
			&r.Status, &r.Verdict, &maxRisk, &r.Reasons, &r.Late, &r.DecisionSnapshot, &r.DecisionMs, &r.CostUSD,
			&r.AuditVerdict, &levels); err != nil {
			return nil, "", err
		}
		r.CreatedAt = utc(r.CreatedAt)
		r.MaxRisk = f32ptr(maxRisk)
		r.Reasons = nonNil(r.Reasons)
		if err := json.Unmarshal(levels, &r.EvaluatorLevels); err != nil {
			return nil, "", fmt.Errorf("decision %s: evaluator levels: %w", r.TraceID, err)
		}
		if r.EvaluatorLevels == nil {
			r.EvaluatorLevels = map[string]*string{}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.TraceID)
	}
	return out, next, nil
}
