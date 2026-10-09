package costs

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func newTestLedger(t *testing.T, c LedgerConfig) *Ledger {
	t.Helper()
	if c.Prices == nil {
		book, err := LoadPriceBook("")
		if err != nil {
			t.Fatal(err)
		}
		c.Prices = book
	}
	if c.Logger == nil {
		c.Logger = quietLog()
	}
	l, err := NewLedger(c)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The ledger is the decision client's budget: observe calls stop at the observe line with jev's
// budget error, enforcing calls spend the reserve, and Spend reports exhausted from the line on
// (as jev's own budget does).
func TestBudgetLedgerIsTheDecisionBudget(t *testing.T) {
	clk := newClock(day1)
	l := newTestLedger(t, LedgerConfig{DecisionCapUSD: 1, EnforcingReserve: 0.25, Now: clk.Now})
	r, err := l.Reserve(jev.SpendRequest{USD: 0.75, KeyLabel: "app"})
	if err != nil {
		t.Fatal(err)
	}
	r.Settle(0.75)
	if _, err := l.Reserve(jev.SpendRequest{USD: 0.01}); !errors.Is(err, jev.ErrBudgetExhausted) {
		t.Fatalf("observe past the line = %v, want jev.ErrBudgetExhausted", err)
	}
	sp := l.Spend()
	if !sp.Exhausted || sp.SpentUSD != 0.75 || sp.CapUSD != 1 || sp.SoftCapUSD != 0.75 || sp.Day != "2026-10-02" {
		t.Errorf("Spend = %+v", sp)
	}
	if got := l.Headroom(false); got != 0 {
		t.Errorf("Headroom(false) at the line = %v", got)
	}
	if got := l.Headroom(true); !near(got, 0.25) {
		t.Errorf("Headroom(true) at the line = %v", got)
	}
	r, err = l.Reserve(jev.SpendRequest{USD: 0.2, Enforcing: true})
	if err != nil {
		t.Fatalf("an enforcing call below the cap: %v", err)
	}
	r.Settle(0.2)
	if at := l.CapHitAt(KindDecision); at == nil || !at.Equal(day1) {
		t.Errorf("CapHitAt(decision) = %v, want the refused observe call's time", at)
	}
	if l.CapHitAt(KindLLM) != nil || l.CapHitAt("other") != nil {
		t.Error("the LLM cap was not hit")
	}
	v := l.Budgets()
	if !near(v.Decision.SpentUSD, 0.95) || v.Decision.Exhausted || !v.Decision.ReserveOnly || v.LLM.SpentUSD != 0 {
		t.Errorf("Budgets = %+v", v)
	}
}

// The LLM cap is hard for every caller: a hold past it is refused with ErrLLMCapReached, the
// settle books the real cost, and a hold of 0 asks only whether the cap is spent.
func TestBudgetLedgerLLMCap(t *testing.T) {
	clk := newClock(day1)
	l := newTestLedger(t, LedgerConfig{LLMCapUSD: 0.1, Now: clk.Now})
	llm := l.LLM()
	settle, err := llm.Reserve(0.06, "studio_draft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Reserve(0.06, "deep_audit"); !errors.Is(err, ErrLLMCapReached) {
		t.Fatalf("a hold past the LLM cap = %v", err)
	}
	if got := llm.HeadroomUSD(); !near(got, 0.04) {
		t.Errorf("HeadroomUSD = %v", got)
	}
	settle(0.1)
	if _, err := llm.Reserve(0, "kb_ingest"); !errors.Is(err, ErrLLMCapReached) {
		t.Errorf("a $0 hold once the cap is spent = %v", err)
	}
	if st := llm.State(); !st.Exhausted || st.SpentUSD != 0.1 || st.ReservedUSD != 0 {
		t.Errorf("State = %+v", st)
	}
	if l.CapHitAt(KindLLM) == nil {
		t.Error("CapHitAt(llm) not recorded")
	}
	l.ApplySettings(Caps{DecisionUSDDay: 3, LLMUSDDay: 0.5}, nil)
	if _, err := llm.Reserve(0.3, "deep_audit"); err != nil {
		t.Errorf("a raised LLM cap still refuses: %v", err)
	}
	if st := l.Budgets(); st.Decision.CapUSD != 3 || st.LLM.CapUSD != 0.5 {
		t.Errorf("caps after ApplySettings = %+v", st)
	}
}

// Simulated and unknown costs never count toward the LLM cap (D46).
func TestCostRealCostExcludesSimulated(t *testing.T) {
	records := []*pb.CostRecord{
		{Component: "deep_audit_verify", CostUsd: 0.01, CostSource: decision.CostReported},
		{Component: "deep_audit_verify", CostUsd: 0.02, CostSource: decision.CostComputed},
		{Component: "deep_audit_verify", CostUsd: 5, CostSource: decision.CostSimulated},
		{Component: "deep_audit_verify", CostUsd: 7, CostSource: decision.CostUnknown},
		{Component: "deep_audit_verify", CostUsd: -1, CostSource: decision.CostReported},
		nil,
	}
	if got := RealCostUSD(records); !near(got, 0.03) {
		t.Errorf("RealCostUSD = %v, want 0.03", got)
	}
}

// The LLM cap counts a record no price covers at the reference judge's price for its tokens, with
// dashboard overrides of that price applied, and simulated records at $0 (D91); RealCostUSD, which
// ROI and Savings use, still counts the unpriced record at $0.
func TestBudgetCapCostPricesUnknownAtTheReference(t *testing.T) {
	l := newTestLedger(t, LedgerConfig{LLMCapUSD: 1})
	records := []*pb.CostRecord{
		{Component: "deep_audit_verify", CostUsd: 0.01, CostSource: decision.CostReported},
		{Component: "deep_audit_verify", CostUsd: 5, CostSource: decision.CostSimulated},
		{Component: "deep_audit_verify", Provider: "openai", Model: "gpt-4o-mini", InputTokens: 2000, OutputTokens: 400,
			CostUsd: 7, CostSource: decision.CostUnknown},
		{Component: "deep_audit_verify", CostUsd: math.NaN(), CostSource: decision.CostComputed},
		nil,
	}
	// 2,000 × $1 + 400 × $5 per million tokens = $0.004, plus the $0.01 reported.
	if got := l.CapCostUSD(records); !near(got, 0.014) {
		t.Errorf("CapCostUSD = %v, want 0.014", got)
	}
	if got := l.LLM().CapCostUSD(records); !near(got, 0.014) {
		t.Errorf("LLMBudget.CapCostUSD = %v, want 0.014", got)
	}
	if got := RealCostUSD(records); !near(got, 0.01) {
		t.Errorf("RealCostUSD = %v, want 0.01 (the unpriced record is not spend)", got)
	}
	l.ApplySettings(Caps{LLMUSDDay: 1}, []decision.PriceEntry{{Provider: "anthropic", Match: "claude-haiku-4-5*",
		InputPerMTok: 2, OutputPerMTok: 10, Label: decision.PriceLabelOverride, AsOf: "2026-10-01"}})
	if got := l.CapCostUSD(records); !near(got, 0.018) {
		t.Errorf("CapCostUSD with the reference overridden = %v, want 0.018", got)
	}
}

// ApplySettings swaps the pricing overrides and logs the invalid ones out.
func TestPricingLedgerApplySettingsOverrides(t *testing.T) {
	l := newTestLedger(t, LedgerConfig{})
	l.ApplySettings(Caps{}, []decision.PriceEntry{
		{Provider: "openrouter", Match: "typesafe/jev-1.13*", InputPerMTok: 0.05, Label: decision.PriceLabelOverride, AsOf: "2026-10-01"},
		{Provider: "openrouter", Match: "x/*", InputPerMTok: -1, Label: decision.PriceLabelOverride, AsOf: "2026-10-01"},
	})
	e, v, ok := l.Prices().Lookup("openrouter", "typesafe/jev-1.13")
	if !ok || e.InputPerMTok != 0.05 || v != decision.PriceVersionOverride {
		t.Errorf("Lookup after the override = %+v %s %v", e, v, ok)
	}
	if got := len(l.Prices().Effective().Entries); got != 6 {
		t.Errorf("effective entries = %d, want the 5 contracts entries and 1 valid override", got)
	}
	l.ApplySettings(Caps{}, nil)
	if _, v, _ := l.Prices().Lookup("openrouter", "typesafe/jev-1.13"); v == decision.PriceVersionOverride {
		t.Error("removing the override kept it")
	}
}

// A decision attempt's cost event becomes one row with every column carried.
func TestCostLedgerRecordsDecisionRows(t *testing.T) {
	sink := newRowSink()
	clk := newClock(day1)
	l := newTestLedger(t, LedgerConfig{Store: sink, Now: clk.Now})
	in, out := 1200, 3
	l.RecordCost(jev.CostEvent{
		OccurredAt: day1, TraceID: "tr-1", Route: "support", KeyLabel: "app", Component: "gate", Kind: jev.CostKindDecision,
		Provider: "openrouter-decisions", Model: "typesafe/jev-1.13-20260917", Attempt: 1, Hedge: true,
		InputTokens: &in, OutputTokens: &out, CostUSD: 5.04e-05, CostSource: decision.CostComputed, Estimated: true,
		PriceTableVersion: "2026-09-27", LatencyMs: 420, Status: jev.AttemptHedgeLost, Questions: 4, Late: true,
	})
	l.RecordCost(jev.CostEvent{TraceID: "tr-2", Component: "probe", CostSource: decision.CostSimulated, Provider: "mock"})
	if l.PendingRows() != 2 {
		t.Fatalf("pending = %d", l.PendingRows())
	}
	if err := l.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := sink.rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.TraceID != "tr-1" || r.Route != "support" || r.KeyLabel != "app" || r.Component != "gate" || r.Kind != "decision" ||
		r.Attempt != 1 || !r.Hedge || !r.Late || !r.Estimated || *r.InputTokens != 1200 || *r.OutputTokens != 3 ||
		*r.CostUSD != 5.04e-05 || r.CostSource != "computed" || r.PriceTableVersion != "2026-09-27" || *r.LatencyMs != 420 ||
		r.Status != "hedge_lost" || *r.Questions != 4 || !r.OccurredAt.Equal(day1) || r.AuditID != "" {
		t.Errorf("decision row = %+v", r)
	}
	if r := rows[1]; r.Kind != "decision" || r.OccurredAt.IsZero() || r.Questions != nil || r.InputTokens != nil {
		t.Errorf("a sparse event's row = %+v", r)
	}
	// Recording a row books no spend: the attempt's hold did.
	if st := l.Spend(); st.SpentUSD != 0 {
		t.Errorf("recording spent %v", st.SpentUSD)
	}
}

// LLM cost records become rows linked to their trace and audit, stamped when recorded and priced
// under the version in force ("override" when an override prices them).
func TestCostLedgerRecordsLLMRows(t *testing.T) {
	sink := newRowSink()
	clk := newClock(day1)
	l := newTestLedger(t, LedgerConfig{Store: sink, Now: clk.Now})
	l.ApplySettings(Caps{}, []decision.PriceEntry{{Provider: "anthropic", Match: "claude-haiku-4-5*", InputPerMTok: 0.8,
		OutputPerMTok: 4, Label: decision.PriceLabelOverride, AsOf: "2026-10-01"}})
	l.RecordLLM([]*pb.CostRecord{
		{Component: "studio_draft", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5", InputTokens: 900,
			OutputTokens: 400, CostUsd: 0.00232, CostSource: "computed", LatencyMs: 3000, Status: "ok"},
		{Component: "kb_ingest", Provider: "some-vendor", Model: "embed-x", CostSource: decision.CostUnknown, CostUsd: 0},
		nil,
	}, Link{TraceID: "tr-9", AuditID: "au-9", Route: "support", KeyLabel: "admin", Selection: "manual"})
	if err := l.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := sink.rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.AuditID != "au-9" || r.TraceID != "tr-9" || r.Kind != "llm" || r.Component != "studio_draft" || *r.CostUSD != 0.00232 ||
		r.PriceTableVersion != decision.PriceVersionOverride || !r.OccurredAt.Equal(day1) || r.Selection != "manual" || r.KeyLabel != "admin" {
		t.Errorf("llm row = %+v", r)
	}
	if r := rows[1]; r.Kind != "llm" || r.CostUSD != nil || r.CostSource != "unknown" || r.PriceTableVersion != "2026-09-27" {
		t.Errorf("an unpriced llm row = %+v", r)
	}
	if st := l.LLM().State(); st.SpentUSD != 0 {
		t.Errorf("recording LLM rows spent %v (the hold's settle books it)", st.SpentUSD)
	}
}

// Without a database the ledger keeps no rows, seeds nothing and runs no loop, but still enforces
// the caps.
func TestBudgetLedgerWithoutAStore(t *testing.T) {
	l := newTestLedger(t, LedgerConfig{DecisionCapUSD: 1})
	l.RecordCost(jev.CostEvent{TraceID: "t"})
	l.RecordLLM([]*pb.CostRecord{{Component: "kb_ingest"}}, Link{})
	if err := l.Seed(context.Background()); err != nil || l.PendingRows() != 0 || l.Flush(context.Background()) != nil {
		t.Errorf("seed %v pending %d", err, l.PendingRows())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l.Run(ctx) // returns at once
	if _, err := NewLedger(LedgerConfig{}); err == nil {
		t.Error("a ledger without a price book was built")
	}
}

// Seed and the periodic resync raise the in-memory spend to the stored real spend
// (max(memory, db)); the writer stops with Run.
func TestSeedLedgerResyncsFromTheStore(t *testing.T) {
	sink := newRowSink()
	sink.totals = store.SpendTotals{DecisionUSD: 0.4, LLMUSD: 2, DecisionByKey: map[string]float64{"app": 0.4}}
	clk := newClock(day1)
	l := newTestLedger(t, LedgerConfig{DecisionCapUSD: 1, EnforcingReserve: 0.25, PerKeyCapUSD: 0.4, LLMCapUSD: 5,
		Store: sink, Now: clk.Now, ResyncEvery: 5 * time.Millisecond})
	if err := l.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := l.Budgets(); v.Decision.SpentUSD != 0.4 || v.LLM.SpentUSD != 2 {
		t.Fatalf("seeded = %+v", v)
	}
	if got := sink.totalsAt[0]; !got.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("SpendToday read from %v, want the UTC day's start", got)
	}
	if _, err := l.Reserve(jev.SpendRequest{USD: 0.01, KeyLabel: "app"}); err == nil {
		t.Error("the seeded key spend did not reach the key cap")
	}
	settle, err := l.LLM().Reserve(1, "deep_audit")
	if err != nil {
		t.Fatal(err)
	}
	settle(1) // memory 3 > db 2

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	sink.mu.Lock()
	sink.totals.DecisionUSD = 0.6 // another replica spent
	sink.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for l.Budgets().Decision.SpentUSD != 0.6 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if v := l.Budgets(); v.Decision.SpentUSD != 0.6 || v.LLM.SpentUSD != 3 {
		t.Errorf("after a resync = %+v (decision from the db, llm from memory)", v)
	}
	l.RecordCost(jev.CostEvent{TraceID: "at-stop", Component: "gate"})
	cancel()
	<-done
	if rows := sink.rows(); len(rows) != 1 || rows[0].TraceID != "at-stop" {
		t.Errorf("the writer's final flush = %+v", rows)
	}
}

// Seeding after a restart, against Postgres: today's real rows count, per kind and per key;
// simulated rows, rows without a cost and yesterday's rows do not (D46).
func TestSeedLedgerAfterARestart(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	now := time.Now().UTC()
	clk := newClock(now)
	first := newTestLedger(t, LedgerConfig{DecisionCapUSD: 10, EnforcingReserve: 0.25, PerKeyCapUSD: 0.5, LLMCapUSD: 10,
		Store: pg, Now: clk.Now})
	// One real decision attempt per key, one simulated, one LLM call.
	for _, key := range []string{"app-a", "app-b"} {
		r, err := first.Reserve(jev.SpendRequest{USD: 0.3, KeyLabel: key})
		if err != nil {
			t.Fatal(err)
		}
		r.Settle(0.25)
		first.RecordCost(jev.CostEvent{OccurredAt: now, TraceID: "tr-" + key, KeyLabel: key, Component: "gate",
			Kind: jev.CostKindDecision, CostUSD: 0.25, CostSource: decision.CostReported})
	}
	first.RecordCost(jev.CostEvent{OccurredAt: now, TraceID: "tr-mock", KeyLabel: "app-a", Component: "gate",
		Kind: jev.CostKindDecision, Provider: "mock", CostUSD: 3, CostSource: decision.CostSimulated})
	settle, err := first.LLM().Reserve(0.05, "studio_draft")
	if err != nil {
		t.Fatal(err)
	}
	records := []*pb.CostRecord{{Component: "studio_draft", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5",
		CostUsd: 0.04, CostSource: decision.CostComputed}, {Component: "studio_draft", Kind: "llm", Provider: "openai",
		Model: "gpt-4o-mini", InputTokens: 1000, OutputTokens: 200, CostSource: decision.CostUnknown}}
	// D91: the unpriced record counts at the reference judge's price ($1/$5 per MTok): $0.002.
	if got := first.CapCostUSD(records); !near(got, 0.042) {
		t.Fatalf("CapCostUSD = %v, want 0.042", got)
	}
	settle(first.CapCostUSD(records))
	first.RecordLLM(records, Link{TraceID: "tr-draft"})
	if err := first.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Yesterday's spend never counts today.
	usd := 4.0
	if err := pg.InsertCostEvents(context.Background(), []store.CostEvent{{OccurredAt: now.Add(-25 * time.Hour),
		Component: "gate", Kind: "decision", CostUSD: &usd, CostSource: decision.CostReported, KeyLabel: "app-a"}}); err != nil {
		t.Fatal(err)
	}

	restarted := newTestLedger(t, LedgerConfig{DecisionCapUSD: 10, EnforcingReserve: 0.25, PerKeyCapUSD: 0.5, LLMCapUSD: 10,
		Store: pg, Now: clk.Now})
	if v := restarted.Budgets(); v.Decision.SpentUSD != 0 {
		t.Fatalf("a new ledger starts at %v", v.Decision.SpentUSD)
	}
	if err := restarted.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := restarted.Budgets()
	if !near(v.Decision.SpentUSD, 0.5) || !near(v.LLM.SpentUSD, 0.042) {
		t.Errorf("after the restart: decision %v (want 0.5), llm %v (want 0.042: the unpriced row at the reference price, D91)",
			v.Decision.SpentUSD, v.LLM.SpentUSD)
	}
	if _, err := restarted.Reserve(jev.SpendRequest{USD: 0.3, KeyLabel: "app-a"}); err == nil {
		t.Error("app-a's seeded spend (0.25 of 0.5) left room for 0.3")
	}
	if r, err := restarted.Reserve(jev.SpendRequest{USD: 0.2, KeyLabel: "app-a"}); err != nil {
		t.Errorf("app-a has 0.25 left: %v", err)
	} else {
		r.Settle(0)
	}
	if math.IsInf(restarted.Headroom(false), 0) {
		t.Error("a capped ledger reported unlimited headroom")
	}
}

// Only audits whose cost is known teach the audit estimate (D48): an audit without cost records,
// or with a record no price covers, leaves the estimate where it was; a simulated record is a
// known $0.
func TestCostLedgerObservesOnlyKnownAuditCosts(t *testing.T) {
	l := newTestLedger(t, LedgerConfig{Judge: func() (string, string) { return "anthropic", "claude-haiku-4-5" }})
	llm := l.LLM()
	seed := llm.EstimateAuditUSD()
	if !near(seed, 0.0355) {
		t.Fatalf("seed = %v", seed)
	}
	llm.ObserveAudit(nil)
	llm.ObserveAudit([]*pb.CostRecord{nil})
	llm.ObserveAudit([]*pb.CostRecord{
		{Component: "deep_audit_decompose", CostUsd: 0.01, CostSource: decision.CostComputed},
		{Component: "deep_audit_verify", CostSource: decision.CostUnknown},
	})
	if got := llm.EstimateAuditUSD(); got != seed {
		t.Errorf("audits of unknown cost moved the estimate to %v", got)
	}
	llm.ObserveAudit([]*pb.CostRecord{
		{Component: "deep_audit_decompose", CostUsd: 0.01, CostSource: decision.CostReported},
		{Component: "deep_audit_verify", CostUsd: 0.0055, CostSource: decision.CostComputed},
	})
	want := 0.2*0.0155 + 0.8*seed
	if got := llm.EstimateAuditUSD(); !near(got, want) {
		t.Errorf("after a known audit = %v, want %v", got, want)
	}
	llm.ObserveAudit([]*pb.CostRecord{{Component: "deep_audit_verify", CostUsd: 5, CostSource: decision.CostSimulated}})
	if got := llm.EstimateAuditUSD(); !near(got, 0.8*want) {
		t.Errorf("after a simulated audit = %v, want %v", got, 0.8*want)
	}
}
