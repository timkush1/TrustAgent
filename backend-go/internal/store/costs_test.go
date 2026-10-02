package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// Every column of a cost row round-trips; empty strings and nil pointers are NULL, and a cost
// past what NUMERIC(14,10) holds is stored as that maximum instead of failing the batch.
func TestCostEventsInsert(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	huge := 1e9
	rows := []CostEvent{
		{OccurredAt: at, TraceID: "tr-1", Route: "support", KeyLabel: "app", Component: "gate", Kind: CostKindDecision,
			Provider: "openrouter-decisions", Model: "typesafe/jev-1.13-20260917", Attempt: 1, InputTokens: ptr(int64(1200)),
			OutputTokens: ptr(int64(3)), CostUSD: ptr(5.04e-05), CostSource: "computed", PriceTableVersion: "2026-09-27",
			LatencyMs: ptr(int64(420)), Status: "hedge_lost", Questions: ptr(4), Hedge: true, Late: true, Estimated: true},
		{OccurredAt: at, TraceID: "tr-1", AuditID: "au-1", Component: "deep_audit_verify", Kind: CostKindLLM,
			CostUSD: &huge, CostSource: "reported", Selection: "uniform"},
		{Component: "kb_ingest", Kind: CostKindLLM},
	}
	if err := s.InsertCostEvents(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCostEvents(ctx, nil); err != nil {
		t.Fatalf("an empty batch: %v", err)
	}
	type stored struct {
		at                                                  time.Time
		trace, audit, route, key, provider, model, ptv, sel *string
		component, kind, source                             string
		attempt                                             int16
		in, out, latency, questions                         *int32
		cost                                                *float64
		status                                              *string
		hedge, late, estimated                              bool
	}
	q, err := s.pool.Query(ctx, `SELECT occurred_at, trace_id, audit_id, route, key_label, provider, model, price_table_version,
		selection, component, kind, cost_source, attempt, input_tokens, output_tokens, latency_ms, questions,
		cost_usd::FLOAT8, status, hedge, late, estimated FROM cost_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var got []stored
	for q.Next() {
		var r stored
		if err := q.Scan(&r.at, &r.trace, &r.audit, &r.route, &r.key, &r.provider, &r.model, &r.ptv, &r.sel, &r.component,
			&r.kind, &r.source, &r.attempt, &r.in, &r.out, &r.latency, &r.questions, &r.cost, &r.status, &r.hedge, &r.late,
			&r.estimated); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d", len(got))
	}
	r := got[0]
	if !r.at.Equal(at) || *r.trace != "tr-1" || r.audit != nil || *r.route != "support" || *r.key != "app" ||
		*r.provider != "openrouter-decisions" || *r.ptv != "2026-09-27" || r.sel != nil || r.attempt != 1 || *r.in != 1200 ||
		*r.out != 3 || *r.latency != 420 || *r.questions != 4 || *r.cost != 5.04e-05 || *r.status != "hedge_lost" ||
		!r.hedge || !r.late || !r.estimated {
		t.Errorf("decision row = %+v", r)
	}
	if r := got[1]; *r.audit != "au-1" || *r.cost != maxCostUSD || *r.sel != "uniform" || r.hedge {
		t.Errorf("llm row = %+v", r)
	}
	if r := got[2]; r.cost != nil || r.source != "unknown" || r.trace != nil || r.at.IsZero() || r.in != nil {
		t.Errorf("sparse row = %+v", r)
	}
}

// A cost that is not a finite number is stored as NULL, like an unknown one, so it never reads
// back as spend: an infinite cost clamped to the column's maximum would exhaust a cap at the next
// seed. A negative cost is stored as $0.
func TestCostEventsStoreNoInfiniteCost(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	at := start.Add(9 * time.Hour)
	if err := s.InsertCostEvents(ctx, []CostEvent{
		{OccurredAt: at, Component: "gate", Kind: CostKindDecision, CostUSD: ptr(math.Inf(1)), CostSource: "reported"},
		{OccurredAt: at, Component: "deep_audit_verify", Kind: CostKindLLM, CostUSD: ptr(math.NaN()), CostSource: "computed"},
		{OccurredAt: at, Component: "deep_audit_verify", Kind: CostKindLLM, CostUSD: ptr(math.Inf(-1)), CostSource: "reported"},
		{OccurredAt: at, Component: "deep_audit_score", Kind: CostKindLLM, CostUSD: ptr(-0.5), CostSource: "reported"},
	}); err != nil {
		t.Fatal(err)
	}
	var nulls, zeros int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE cost_usd IS NULL), count(*) FILTER (WHERE cost_usd = 0)
		FROM cost_events`).Scan(&nulls, &zeros); err != nil {
		t.Fatal(err)
	}
	if nulls != 3 || zeros != 1 {
		t.Errorf("%d NULL and %d $0 costs, want 3 and 1", nulls, zeros)
	}
	got, err := s.SpendToday(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if got.DecisionUSD != 0 || got.LLMUSD != 0 {
		t.Errorf("spend today = %+v, want none", got)
	}
}

// SpendToday counts today's real spend by kind and per key: simulated rows, rows without a cost
// and rows before the day's start never count (D46); every component counts (D50).
func TestSpendTodayCountsRealRowsOnly(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	at := start.Add(9 * time.Hour)
	if got, err := s.SpendToday(ctx, start); err != nil || got.DecisionUSD != 0 || got.LLMUSD != 0 || got.DecisionByKey == nil {
		t.Fatalf("an empty ledger = %+v, %v", got, err)
	}
	if err := s.InsertCostEvents(ctx, []CostEvent{
		{OccurredAt: at, Component: "gate", Kind: CostKindDecision, KeyLabel: "app-a", CostUSD: ptr(0.25), CostSource: "reported"},
		{OccurredAt: at, Component: "probe", Kind: CostKindDecision, CostUSD: ptr(0.125), CostSource: "computed"},
		{OccurredAt: at, Component: "studio_test", Kind: CostKindDecision, KeyLabel: "app-b", CostUSD: ptr(0.5), CostSource: "computed"},
		{OccurredAt: at, Component: "gate", Kind: CostKindDecision, KeyLabel: "app-a", CostUSD: ptr(3.0), CostSource: "simulated"},
		{OccurredAt: at, Component: "gate", Kind: CostKindDecision, KeyLabel: "app-a", CostSource: "unknown"},
		{OccurredAt: start.Add(-time.Minute), Component: "gate", Kind: CostKindDecision, KeyLabel: "app-a", CostUSD: ptr(9.0), CostSource: "reported"},
		{OccurredAt: at, Component: "deep_audit_verify", Kind: CostKindLLM, CostUSD: ptr(0.0355), CostSource: "computed"},
		{OccurredAt: at, Component: "studio_draft", Kind: CostKindLLM, CostUSD: ptr(0.01), CostSource: "free_local"},
		{OccurredAt: at, Component: "deep_audit_verify", Kind: CostKindLLM, CostUSD: ptr(1.0), CostSource: "simulated"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SpendToday(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got.DecisionUSD-0.875) > 1e-9 || math.Abs(got.LLMUSD-0.0455) > 1e-9 {
		t.Errorf("spend today = %+v, want decision 0.875 and llm 0.0455", got)
	}
	if len(got.DecisionByKey) != 2 || got.DecisionByKey["app-a"] != 0.25 || got.DecisionByKey["app-b"] != 0.5 {
		t.Errorf("by key = %v", got.DecisionByKey)
	}
}

// Status24h counts the decision model's calls (first attempts, mock ones included) and its real
// cost over the 24 hours before now.
func TestCostStatus24h(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if err := s.InsertCostEvents(ctx, []CostEvent{
		{OccurredAt: now.Add(-time.Hour), Component: "gate", Kind: CostKindDecision, Attempt: 0, CostUSD: ptr(0.001), CostSource: "reported"},
		{OccurredAt: now.Add(-time.Hour), Component: "gate", Kind: CostKindDecision, Attempt: 1, Hedge: true, CostUSD: ptr(0.001), CostSource: "computed"},
		{OccurredAt: now.Add(-2 * time.Hour), Component: "probe", Kind: CostKindDecision, Attempt: 0, CostUSD: ptr(0.0002), CostSource: "simulated"},
		{OccurredAt: now.Add(-25 * time.Hour), Component: "gate", Kind: CostKindDecision, Attempt: 0, CostUSD: ptr(5.0), CostSource: "reported"},
		{OccurredAt: now.Add(-time.Hour), Component: "deep_audit_verify", Kind: CostKindLLM, CostUSD: ptr(0.03), CostSource: "computed"},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.Status24h(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Calls != 2 || math.Abs(d.CostUSD-0.002) > 1e-12 {
		t.Errorf("24h = %+v, want 2 calls and $0.002", d)
	}
}

// The decision client starts from the snapshot that last answered its default model on its
// backend; another model or backend does not count.
func TestCostKnownSnapshot(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if snap, err := s.LastDecisionSnapshot(ctx, "openrouter-decisions", "typesafe/jev-1.13"); err != nil || snap != "" {
		t.Fatalf("an empty table = %q, %v", snap, err)
	}
	insert := func(trace, backend, model, snap string, at time.Time) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO gate_decisions (trace_id, route, mode, effective_mode, status, action, decision,
			decision_backend, decision_model_requested, decision_snapshot, created_at)
			VALUES ($1, 'support', 'observe', 'observe', 'ok', 'pass', '{}', $2, $3, NULLIF($4, ''), $5)`,
			trace, backend, model, snap, at); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	insert("t1", "openrouter-decisions", "typesafe/jev-1.13", "typesafe/jev-1.13-20260917", base)
	insert("t2", "openrouter-decisions", "typesafe/jev-1.13", "typesafe/jev-1.13-20261001", base.Add(time.Hour))
	insert("t3", "openrouter-decisions", "typesafe/jev-1.13", "", base.Add(2*time.Hour))
	insert("t4", "openrouter-decisions", "typesafe/jev-2", "typesafe/jev-2-20261002", base.Add(3*time.Hour))
	insert("t5", "typesafe", "typesafe/jev-1.13", "jev-1.13-x", base.Add(4*time.Hour))
	snap, err := s.LastDecisionSnapshot(ctx, "openrouter-decisions", "typesafe/jev-1.13")
	if err != nil || snap != "typesafe/jev-1.13-20261001" {
		t.Errorf("last snapshot = %q, %v", snap, err)
	}
}

// An audit's cost rows are linked by audit_id and stamped at completion (D45): SaveAuditResult
// derives them from the record, SaveAuditResultWithCosts writes the caller's rows (key label,
// price table version) in the audit's transaction, and the audit reads back only its own rows
// even when another audit of the same trace completed at the same moment.
func TestAuditCostRowsLinkByAuditID(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	tr := pendingTrace("support-bot", time.Now().UTC())
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	completed := created.Add(4 * time.Second)
	calls := []CostRecord{
		{Component: "deep_audit_decompose", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5", InputTokens: 900,
			OutputTokens: 250, CostUSD: 0.00215, CostSource: "computed", LatencyMs: 700, Status: "ok"},
		{Component: "deep_audit_verify", Kind: "llm", Provider: "acme", Model: "private", InputTokens: 10, OutputTokens: 2,
			CostSource: "unknown", Status: "ok"},
	}
	derived := sampleAudit(NewID(), 0.2, true)
	derived.TraceID, derived.Costs, derived.CreatedAt, derived.CompletedAt = tr.TraceID, calls, created, &completed
	derived.Route, derived.Selection = strp("support-bot"), strp("high")
	if err := s.SaveAuditResult(ctx, derived, AuditCompleted); err != nil {
		t.Fatal(err)
	}
	in, out, latency, q := int64(400), int64(60), int64(900), 3
	usd := 0.0007
	given := sampleAudit(NewID(), 0.9, false)
	given.TraceID, given.Costs, given.CreatedAt, given.CompletedAt = tr.TraceID, calls[:1], created, &completed
	rows := []CostEvent{{OccurredAt: completed, TraceID: tr.TraceID, AuditID: given.AuditID, Route: "support-bot",
		KeyLabel: "support-app", Component: "deep_audit_verify", Kind: CostKindLLM, Provider: "anthropic",
		Model: "claude-haiku-4-5", InputTokens: &in, OutputTokens: &out, CostUSD: &usd, CostSource: "computed",
		PriceTableVersion: "override", LatencyMs: &latency, Status: "ok", Questions: &q, Selection: "uniform"}}
	if err := s.SaveAuditResultWithCosts(ctx, given, AuditCompleted, rows); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAudit(ctx, derived.AuditID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Costs) != 2 || got.Costs[0] != calls[0] || got.Costs[1].CostSource != "unknown" || got.Costs[1].CostUSD != 0 {
		t.Errorf("derived costs %+v", got.Costs)
	}
	got, _ = s.GetAudit(ctx, given.AuditID)
	if len(got.Costs) != 1 || got.Costs[0].Model != "claude-haiku-4-5" || got.Costs[0].CostUSD != 0.0007 || got.Costs[0].Questions != 3 {
		t.Errorf("given costs %+v", got.Costs)
	}

	type stored struct {
		audit, key, version, selection string
		attempt                        int
		occurred                       time.Time
		nullCost                       bool
	}
	r, err := s.pool.Query(ctx, `SELECT audit_id, COALESCE(key_label, ''), COALESCE(price_table_version, ''),
		COALESCE(selection, ''), attempt, occurred_at, cost_usd IS NULL FROM cost_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var all []stored
	for r.Next() {
		var x stored
		if err := r.Scan(&x.audit, &x.key, &x.version, &x.selection, &x.attempt, &x.occurred, &x.nullCost); err != nil {
			t.Fatal(err)
		}
		all = append(all, x)
	}
	r.Close()
	if len(all) != 3 {
		t.Fatalf("rows %+v", all)
	}
	for i, x := range all {
		if !x.occurred.Equal(completed) || x.attempt != 0 {
			t.Errorf("row %d is not stamped at completion: %+v", i, x)
		}
	}
	if all[0].audit != derived.AuditID || all[0].selection != "high" || all[0].nullCost || !all[1].nullCost {
		t.Errorf("derived rows %+v", all[:2])
	}
	if all[2].audit != given.AuditID || all[2].key != "support-app" || all[2].version != "override" || all[2].selection != "uniform" {
		t.Errorf("given row %+v", all[2])
	}
}

// Rows written before 0005 have no audit_id: the audit still finds them by the old rule (its
// trace's deep-audit rows stamped with its created_at).
func TestAuditCostsReadPre0005Rows(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	a := sampleAudit(NewID(), 0.5, false)
	a.TraceID, a.CreatedAt = NewID(), time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	if err := s.SaveAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"deep_audit_verify", "gate"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO cost_events (occurred_at, trace_id, component, kind, cost_usd, cost_source)
			VALUES ($1, $2, $3, 'llm', 0.001, 'computed')`, a.CreatedAt, a.TraceID, component); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetAudit(ctx, a.AuditID)
	if err != nil || len(got.Costs) != 1 || got.Costs[0].Component != "deep_audit_verify" {
		t.Errorf("legacy costs %+v %v", got, err)
	}
}

// DropTraceAudit drops the trace's audit and adds the flag once; an empty flag adds none.
func TestDropTraceAuditFlagsOnce(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	tr := pendingTrace("support-bot", time.Now().UTC())
	tr.Flags = []string{"uniform_sample"}
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.DropTraceAudit(ctx, tr.TraceID, "llm_budget"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetTrace(ctx, tr.TraceID)
	if err != nil || got.AuditStatus != TraceAuditDropped || len(got.Flags) != 2 || got.Flags[1] != "llm_budget" {
		t.Fatalf("trace %+v %v", got, err)
	}
	other := pendingTrace("support-bot", time.Now().UTC())
	if err := s.StartTrace(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.DropTraceAudit(ctx, other.TraceID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetTrace(ctx, other.TraceID); got.AuditStatus != TraceAuditDropped || len(got.Flags) != len(other.Flags) {
		t.Errorf("no flag: %+v", got.Flags)
	}
	if err := s.DropTraceAudit(ctx, NewID(), "llm_budget"); err != nil {
		t.Errorf("an unknown trace: %v", err)
	}
}
