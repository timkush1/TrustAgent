package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// seedDecision records a decided trace whose decision document has the given evaluator levels
// ("" is an evaluator that was not assessed) and disagreement flag.
func seedDecision(t *testing.T, s *PostgresStore, route, action, status string, levels map[string]string, disagreement bool) string {
	t.Helper()
	ctx := context.Background()
	tr := pendingTrace(route, time.Now().UTC())
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	evaluators := map[string]any{}
	for ref, level := range levels {
		e := map[string]any{"ref": ref, "level": nil, "risk_raw": nil}
		if level != "" {
			e["level"], e["risk_raw"] = level, 0.5
		}
		evaluators[ref] = e
	}
	doc, _ := json.Marshal(map[string]any{"spec": "decision/v1", "trace_id": tr.TraceID, "evaluators": evaluators,
		"disagreement": disagreement})
	d, rec := decided(tr, action, "block", "contradicted", 0.9, []string{"contradiction:critical"})
	d.GateStatus, rec.Status, rec.Decision = status, status, doc
	snapshot, ms, cost := "mock/jev-heuristic-v1", int64(12), 0.0000257
	rec.DecisionSnapshot, rec.DecisionMs, rec.CostUSD = &snapshot, &ms, &cost
	if err := s.RecordDecision(ctx, d, rec, nil); err != nil {
		t.Fatal(err)
	}
	return tr.TraceID
}

func ids(rows []DecisionRow) string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.TraceID
	}
	return strings.Join(out, ",")
}

func TestListDecisions(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	a := seedDecision(t, s, "support-bot", "flag", "ok", map[string]string{"groundedness@1": "critical", "contradiction@1": "warn"}, true)
	b := seedDecision(t, s, "support-bot", "pass", "ok", map[string]string{"groundedness@1": "ok", "contradiction@1": ""}, false)
	c := seedDecision(t, s, "faq", "pass", "not_assessed", map[string]string{}, false)
	// A fallback re-gate of c (attempt 1) is not a row of its own.
	_, retry := decided(pendingTrace("faq", time.Now().UTC()), "pass", "pass", "supported", 0.1, nil)
	retry.TraceID, retry.Attempt, retry.Decision = c, 1, json.RawMessage(`{"spec":"decision/v1","evaluators":{"groundedness@1":{"level":"ok"}}}`)
	trC, _ := s.GetTrace(ctx, c)
	if err := s.RecordDecision(ctx, trC, retry, nil); err != nil {
		t.Fatal(err)
	}
	// a was audited: the row reports the audit verdict through the trace.
	auditID := NewID()
	if err := s.SetTraceAudit(ctx, a, TraceAuditUpdate{AuditID: &auditID, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	audit := sampleAudit(auditID, 0.2, true)
	audit.TraceID, audit.Status, audit.Verdict, audit.ScoringVersion = a, AuditCompleted, strp(VerdictHallucinated), "v2"
	if err := s.SaveAuditResult(ctx, audit, AuditCompleted); err != nil {
		t.Fatal(err)
	}

	// Keyset paging, newest first, one row per trace.
	first, next, err := s.ListDecisions(ctx, DecisionFilter{Limit: 2})
	if err != nil || ids(first) != c+","+b || next == "" {
		t.Fatalf("page 1: %s next %q err %v (want %s,%s)", ids(first), next, err, c, b)
	}
	second, next2, err := s.ListDecisions(ctx, DecisionFilter{Limit: 2, Cursor: next})
	if err != nil || ids(second) != a || next2 != "" {
		t.Fatalf("page 2: %s next %q err %v", ids(second), next2, err)
	}
	if _, _, err := s.ListDecisions(ctx, DecisionFilter{Cursor: "not-a-cursor"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}

	row := second[0]
	if row.Route != "support-bot" || row.Mode != "flag" || row.EffectiveMode != "flag" || row.Action != "flag" ||
		row.WouldAction == nil || *row.WouldAction != "block" || row.Status != "ok" || row.Verdict == nil ||
		*row.Verdict != "contradicted" || row.MaxRisk == nil || *row.MaxRisk < 0.89 || row.Late ||
		row.DecisionSnapshot == nil || *row.DecisionSnapshot != "mock/jev-heuristic-v1" || row.DecisionMs == nil ||
		*row.DecisionMs != 12 || row.CostUSD == nil || *row.CostUSD != 0.0000257 || len(row.Reasons) != 1 ||
		row.AuditVerdict == nil || *row.AuditVerdict != VerdictHallucinated || row.CreatedAt.Location() != time.UTC {
		t.Errorf("row a %+v", row)
	}
	if g, co := row.EvaluatorLevels["groundedness@1"], row.EvaluatorLevels["contradiction@1"]; len(row.EvaluatorLevels) != 2 ||
		g == nil || *g != "critical" || co == nil || *co != "warn" {
		t.Errorf("levels of a %v", row.EvaluatorLevels)
	}
	if lv := first[1].EvaluatorLevels; len(lv) != 2 || lv["groundedness@1"] == nil || *lv["groundedness@1"] != "ok" || lv["contradiction@1"] != nil {
		t.Errorf("levels of b %v", lv)
	}
	if first[0].EvaluatorLevels == nil || len(first[0].EvaluatorLevels) != 0 || first[0].AuditVerdict != nil || first[0].Reasons == nil {
		t.Errorf("row c %+v", first[0])
	}
	if b, _ := json.Marshal(first[1]); !strings.Contains(string(b), `"contradiction@1":null`) {
		t.Errorf("an unassessed evaluator is null: %s", b)
	}

	yes, no := true, false
	late := func(f DecisionFilter) DecisionFilter { return f }
	for name, tc := range map[string]struct {
		f    DecisionFilter
		want string
	}{
		"route":                {DecisionFilter{Route: "faq"}, c},
		"action":               {DecisionFilter{Action: "flag"}, a},
		"status":               {DecisionFilter{Status: "not_assessed"}, c},
		"ref and level":        {DecisionFilter{Evaluator: "groundedness@1", Level: "critical"}, a},
		"bare id and level":    {DecisionFilter{Evaluator: "groundedness", Level: "ok"}, b},
		"ref alone":            {DecisionFilter{Evaluator: "contradiction@1"}, b + "," + a},
		"other version":        {DecisionFilter{Evaluator: "groundedness@2"}, ""},
		"level alone":          {DecisionFilter{Level: "warn"}, a},
		"disagreement":         {DecisionFilter{Disagreement: &yes}, a},
		"agreement":            {DecisionFilter{Disagreement: &no}, c + "," + b},
		"not late":             {late(DecisionFilter{Late: &no}), c + "," + b + "," + a},
		"late before upserts":  {DecisionFilter{Late: &yes}, ""},
		"route and evaluator":  {DecisionFilter{Route: "faq", Evaluator: "groundedness"}, ""},
		"everything, default":  {DecisionFilter{}, c + "," + b + "," + a},
		"limit above the page": {DecisionFilter{Limit: 500}, c + "," + b + "," + a},
	} {
		rows, _, err := s.ListDecisions(ctx, tc.f)
		if err != nil || ids(rows) != tc.want {
			t.Errorf("%s: %s err %v, want %s", name, ids(rows), err, tc.want)
		}
	}

	// A late decision upserts b's row: it shows through late and updated_since.
	mark := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	trB, _ := s.GetTrace(ctx, b)
	_, lateRec := decided(trB, "pass", "pass", "supported", 0.1, nil)
	lateRec.Late, lateRec.Decision = true, json.RawMessage(`{"spec":"decision/v1","late":true,"evaluators":{"groundedness@1":{"level":"ok"}}}`)
	if err := s.RecordDecision(ctx, trB, lateRec, nil); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := s.ListDecisions(ctx, DecisionFilter{Late: &yes}); err != nil || ids(rows) != b || !rows[0].Late {
		t.Errorf("late: %s %v", ids(rows), err)
	}
	if rows, _, err := s.ListDecisions(ctx, DecisionFilter{UpdatedSince: &mark}); err != nil || ids(rows) != b {
		t.Errorf("updated_since: %s %v", ids(rows), err)
	}
	// since filters on the decision's creation time.
	if _, err := s.pool.Exec(ctx, `UPDATE gate_decisions SET created_at = now() - interval '2 days' WHERE trace_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC().Add(-time.Hour)
	if rows, _, err := s.ListDecisions(ctx, DecisionFilter{Since: &since}); err != nil || ids(rows) != c+","+b {
		t.Errorf("since: %s %v", ids(rows), err)
	}
}
