package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// jevCostTotal reads jev_cost_usd_total{backend,component}.
func jevCostTotal(t *testing.T, backend, component string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "trustagent_gateway_jev_cost_usd_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["backend"] == backend && labels["component"] == component {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// newPGStore opens a Postgres store in a fresh schema (skipped without TEST_DATABASE_URL), and a
// pool on the same schema for the test's own reads.
func newPGStore(t *testing.T) (*store.PostgresStore, *pgxpool.Pool) {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg, schema.Pool(t)
}

// costRow is one stored cost_events row as the wiring test reads it.
type costRow struct {
	attempt          int
	hedge, late, est bool
	status, source   string
	usd              float64
}

func traceCostRows(t *testing.T, db *pgxpool.Pool, traceID string) []costRow {
	t.Helper()
	rows, err := db.Query(context.Background(), `SELECT attempt, hedge, late, estimated, COALESCE(status, ''), cost_source,
		COALESCE(cost_usd, 0)::FLOAT8 FROM cost_events WHERE trace_id = $1 ORDER BY attempt`, traceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []costRow
	for rows.Next() {
		var r costRow
		if err := rows.Scan(&r.attempt, &r.hedge, &r.late, &r.est, &r.status, &r.source, &r.usd); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// waitCostRows flushes the ledger until traceID has n rows.
func waitCostRows(t *testing.T, env *testEnv, db *pgxpool.Pool, traceID string, n int) []costRow {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := env.srv.stopCosts(context.Background()); err != nil {
			t.Fatal(err)
		}
		rows := traceCostRows(t, db, traceID)
		if len(rows) >= n || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func decisionRequest() *jev.Request {
	return &jev.Request{Model: "typesafe/jev-1.13", State: map[string]any{"assistant_response": "Refunds within 30 days."},
		Questions: map[string]any{"groundedness__v1": map[string]any{"type": "noul", "instructions": "Is it grounded?",
			"criteria": map[string]any{"true": "yes", "false": "no"}}}}
}

// The decision client books every attempt through the ledger: a hedged call writes one row per
// attempt (the cancelled loser included, at its estimate), a call the caller stopped waiting for
// writes its attempt as late, the rows' sum equals the increase of jev_cost_usd_total, and
// GET /api/status reports the same spend.
func TestCostLedgerWiringBooksEveryAttempt(t *testing.T) {
	pg, db := newPGStore(t)
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Requests 1 and 3 are slow: the first is beaten by its hedge, the third outlives its caller.
	var calls atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/systemone" {
			if n := calls.Add(1); n == 1 || n == 3 {
				time.Sleep(400 * time.Millisecond)
			}
		}
		mu.ServeHTTP(w, r)
	}))
	t.Cleanup(vendor.Close)
	env := newTestEnv(t, envOpts{pg: pg, adminKey: "sk-test-admin-costs", mutate: func(c *config.Config) {
		c.Jev.Backend, c.Jev.BaseURL, c.Jev.Path = jev.BackendCompatible, vendor.URL, "/v1/systemone"
		c.Jev.APIKey, c.Jev.Model, c.Jev.MaxRetries = "sk-test-wiring-key", "typesafe/jev-1.13", 0
		c.Jev.StartupProbe = false
	}})
	client := env.srv.gateway.client
	before := jevCostTotal(t, jev.BackendCompatible, jev.ComponentGate)

	resp, err := client.Decide(context.Background(), decisionRequest(), jev.CallOptions{HedgeAfter: 100 * time.Millisecond,
		TraceID: "tr-hedged", Route: "support", KeyLabel: "trafficgen", Enforcing: true})
	if err != nil {
		t.Fatalf("hedged call: %v", err)
	}
	if !resp.Hedged || resp.Attempts != 2 {
		t.Fatalf("the call was not hedged: %+v", resp)
	}
	hedged := waitCostRows(t, env, db, "tr-hedged", 2)
	if len(hedged) != 2 || hedged[0].attempt != 0 || hedged[1].attempt != 1 || !hedged[1].hedge || hedged[0].hedge {
		t.Fatalf("hedged rows = %+v", hedged)
	}
	if hedged[0].status != jev.AttemptHedgeLost || !hedged[0].est || hedged[1].status != jev.AttemptOK || hedged[1].est {
		t.Errorf("loser %+v, winner %+v", hedged[0], hedged[1])
	}

	late := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = client.Decide(ctx, decisionRequest(), jev.CallOptions{TraceID: "tr-late", Route: "support",
		OnLate: func(*jev.Response, error) { close(late) }})
	var jerr *jev.Error
	if !errors.As(err, &jerr) || jerr.Kind != jev.KindTimeout || !jerr.Continued {
		t.Fatalf("the late call = %v", err)
	}
	select {
	case <-late:
	case <-time.After(5 * time.Second):
		t.Fatal("the late decision never arrived")
	}
	lateRows := waitCostRows(t, env, db, "tr-late", 1)
	if len(lateRows) != 1 || !lateRows[0].late || lateRows[0].status != jev.AttemptOK || lateRows[0].usd <= 0 {
		t.Fatalf("late rows = %+v", lateRows)
	}

	var sum float64
	for _, r := range append(hedged, lateRows...) {
		sum += r.usd
	}
	if delta := jevCostTotal(t, jev.BackendCompatible, jev.ComponentGate) - before; math.Abs(delta-sum) > 1e-12 {
		t.Errorf("rows sum to %v, jev_cost_usd_total grew by %v", sum, delta)
	}
	code, body := env.get("/api/status", "sk-test-admin-costs")
	var st struct {
		Budgets statusBudgets `json:"budgets"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		t.Fatalf("status %d: %s", code, body)
	}
	if math.Abs(st.Budgets.DecisionSpentTodayUSD-sum) > 1e-9 || st.Budgets.DecisionUSDDay != 2 || st.Budgets.LLMUSDDay != 5 {
		t.Errorf("status budgets = %+v, want decision spend %v", st.Budgets, sum)
	}
}

// The runtime settings reach the ledger: a budgets row changes both caps and a pricing override
// reprices decisions and the engine's table, without a restart; a restart seeds today's spend
// from the rows.
func TestCostLedgerFollowsSettingsAndSeedsOnRestart(t *testing.T) {
	pg, db := newPGStore(t)
	env := newTestEnv(t, envOpts{pg: pg})
	ctx := context.Background()
	if h := env.srv.costs.headroom(false); math.Abs(h-1.5) > 1e-9 {
		t.Fatalf("observe headroom with the $2 default cap = %v, want 1.5", h)
	}
	_, v, err := pg.RuntimeEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	override := `[{"provider":"openrouter","match":"typesafe/jev-1.13*","input_per_mtok":0.05,"output_per_mtok":0,"label":"override","as_of":"2026-10-01"}]`
	if _, err := pg.PatchSettings(ctx, "", v, map[string]json.RawMessage{
		"budgets":           json.RawMessage(`{"decision_usd_day":0.4,"llm_usd_day":0.02}`),
		"pricing_overrides": json.RawMessage(override),
	}, "admin:test", nil); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.rt.manager.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if h := env.srv.costs.headroom(true); math.Abs(h-0.4) > 1e-9 {
		t.Errorf("enforcing headroom after the budgets row = %v, want 0.4", h)
	}
	if p, ver, ok := env.srv.costs.prices(jev.BackendOpenRouterDecisions, "typesafe/jev-1.13"); !ok || p != 0.05 || ver != decision.PriceVersionOverride {
		t.Errorf("decision price after the override = %v %s %v", p, ver, ok)
	}
	var table decision.PriceTable
	if err := json.Unmarshal(env.srv.costs.priceJSON(nil), &table); err != nil || table.Entries[0].Label == decision.PriceLabelOverride {
		t.Errorf("priceJSON(nil) is a pure function of its overrides: %v %+v", err, table.Entries[0])
	}

	// The LLM cap is hard: a Studio draft estimate past it is refused, one inside it books its
	// real cost and its rows link to the trace.
	if _, err := env.srv.costs.reserve(ctx, 0.05, "studio_draft"); !errors.Is(err, costs.ErrLLMCapReached) {
		t.Fatalf("a hold past the LLM cap = %v", err)
	}
	settle, err := env.srv.costs.reserve(ctx, 0.01, "studio_draft")
	if err != nil {
		t.Fatal(err)
	}
	records := []*pb.CostRecord{{Component: "studio_draft", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5",
		InputTokens: 2000, OutputTokens: 400, CostUsd: 0.004, CostSource: decision.CostComputed, Status: "ok"}}
	settle(costs.RealCostUSD(records))
	env.srv.costs.record(ctx, records, CostLink{TraceID: "tr-draft", KeyLabel: "admin"})
	if rows := waitCostRows(t, env, db, "tr-draft", 1); len(rows) != 1 || rows[0].usd != 0.004 {
		t.Fatalf("draft rows = %+v", rows)
	}
	if v := env.srv.ledger().Budgets(); v.LLM.SpentUSD != 0.004 || v.LLM.CapUSD != 0.02 || v.Decision.CapUSD != 0.4 {
		t.Errorf("budgets = %+v", v)
	}

	restarted := newTestEnv(t, envOpts{pg: pg})
	if v := restarted.srv.ledger().Budgets(); v.LLM.SpentUSD != 0.004 || v.LLM.CapUSD != 0.02 {
		t.Errorf("after a restart = %+v: today's spend and the stored caps", v)
	}
}

// /api/status reads the decision model's last 24 hours from the cost ledger (D92): after a restart
// the calls and cost of stored decision rows show (first attempts only; simulated rows cost $0),
// where the restarted client's own counters would read 0.
func TestCostStatusDecisionDayFromTheLedger(t *testing.T) {
	pg, _ := newPGStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	usd, hedge, mock := 0.0002, 0.0001, 0.5
	if err := pg.InsertCostEvents(ctx, []store.CostEvent{
		{OccurredAt: now.Add(-time.Hour), TraceID: "tr-1", Component: "gate", Kind: "decision", CostUSD: &usd, CostSource: decision.CostReported},
		{OccurredAt: now.Add(-time.Hour), TraceID: "tr-1", Component: "gate", Kind: "decision", Attempt: 1, Hedge: true, CostUSD: &hedge, CostSource: decision.CostReported},
		{OccurredAt: now.Add(-2 * time.Hour), TraceID: "tr-2", Component: "gate", Kind: "decision", Provider: "mock", CostUSD: &mock, CostSource: decision.CostSimulated},
		{OccurredAt: now.Add(-25 * time.Hour), TraceID: "tr-old", Component: "gate", Kind: "decision", CostUSD: &mock, CostSource: decision.CostReported},
	}); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, envOpts{pg: pg})
	code, body := env.get("/api/status", testKey)
	if code != http.StatusOK {
		t.Fatalf("GET /api/status = %d", code)
	}
	var st struct {
		DecisionModel struct {
			Calls24h   int     `json:"calls_24h"`
			Cost24hUSD float64 `json:"cost_24h_usd"`
		} `json:"decision_model"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.DecisionModel.Calls24h != 2 || math.Abs(st.DecisionModel.Cost24hUSD-0.0003) > 1e-12 {
		t.Errorf("decision_model calls_24h %d cost_24h_usd %v, want 2 calls and $0.0003 from the stored rows",
			st.DecisionModel.Calls24h, st.DecisionModel.Cost24hUSD)
	}
}

// The decision-day cache reports nothing until a read succeeds, and a failed refresh keeps the
// last value (D92).
func TestCostDecisionDayCacheKeepsTheLastRead(t *testing.T) {
	var fail atomic.Bool
	calls := int64(0)
	c := &decisionDayCache{now: time.Now, log: slog.New(slog.DiscardHandler), read: func(context.Context, time.Time) (store.DecisionDay, error) {
		if fail.Load() {
			return store.DecisionDay{}, errors.New("database down")
		}
		calls++
		return store.DecisionDay{Calls: calls, CostUSD: float64(calls) / 100}, nil
	}}
	fail.Store(true)
	c.refresh(context.Background())
	if _, ok := c.get(); ok {
		t.Fatal("a cache that never read reports a value")
	}
	fail.Store(false)
	c.refresh(context.Background())
	if d, ok := c.get(); !ok || d.Calls != 1 || d.CostUSD != 0.01 {
		t.Fatalf("after a read: %+v %v", d, ok)
	}
	fail.Store(true)
	c.refresh(context.Background())
	if d, ok := c.get(); !ok || d.Calls != 1 {
		t.Errorf("a failed refresh changed the value: %+v %v", d, ok)
	}
}

// Without a database the ledger still enforces the env caps and reports them; nothing is stored.
func TestCostLedgerWithoutADatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{publicRead: true, mutate: func(c *config.Config) {
		c.SpendCapLLMUSDDay = 0.03
	}})
	if env.srv.ledger() == nil {
		t.Fatal("no ledger without a database")
	}
	ctx := context.Background()
	settle, err := env.srv.costs.reserve(ctx, 0.03, "deep_audit")
	if err != nil {
		t.Fatal(err)
	}
	settle(0.03)
	if _, err := env.srv.costs.reserve(ctx, 0, "kb_ingest"); !errors.Is(err, costs.ErrLLMCapReached) {
		t.Errorf("a $0 hold once the LLM cap is spent = %v", err)
	}
	code, body := env.get("/api/status", "")
	var st struct {
		Budgets statusBudgets `json:"budgets"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		t.Fatalf("status %d: %s", code, body)
	}
	if !st.Budgets.LLMExhausted || st.Budgets.LLMSpentTodayUSD != 0.03 || st.Budgets.LLMUSDDay != 0.03 || st.Budgets.DecisionExhausted {
		t.Errorf("status budgets = %+v", st.Budgets)
	}
	if env.srv.ledger().PendingRows() != 0 {
		t.Error("rows were buffered without a database")
	}
}
