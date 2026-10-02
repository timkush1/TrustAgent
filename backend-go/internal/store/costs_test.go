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
