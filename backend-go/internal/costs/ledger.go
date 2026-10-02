package costs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
)

// DefaultResyncEvery is how often the ledger reconciles its in-memory spend with the stored rows.
const DefaultResyncEvery = 60 * time.Second

// ErrLLMCapReached refuses an LLM call (a deep audit, a Studio draft, a knowledge ingest) once
// the day's LLM cap cannot take it (P4-PLAN D48).
var ErrLLMCapReached = errors.New("daily LLM spend cap reached")

// LedgerStore is the storage of the ledger (store.PostgresStore).
type LedgerStore interface {
	RowStore
	SpendToday(ctx context.Context, dayStart time.Time) (store.SpendTotals, error)
}

// LedgerConfig configures a Ledger.
type LedgerConfig struct {
	// DecisionCapUSD, EnforcingReserve and PerKeyCapUSD configure the decision budget
	// (TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY, _SPEND_ENFORCING_RESERVE, _KEY_DECISION_USD_DAY);
	// LLMCapUSD the LLM budget (TRUTHTABLE_SPEND_CAP_LLM_USD_DAY), which has no reserve and no
	// per-key cap: it is hard for every caller.
	DecisionCapUSD   float64
	EnforcingReserve float64
	PerKeyCapUSD     float64
	LLMCapUSD        float64
	// Prices is the effective price table; required.
	Prices *PriceBook
	// Store persists rows and seeds the spend; nil without a database (rows are then not kept).
	Store LedgerStore
	// Judge reports the engine's judge, for the audit estimate; nil when not known.
	Judge JudgeFunc
	// Writer configures the row writer; ResyncEvery the reconciliation (DefaultResyncEvery).
	Writer      WriterConfig
	ResyncEvery time.Duration
	Now         func() time.Time
	Logger      *slog.Logger
}

// Ledger is the cost ledger (SPEC §7.4, P4-PLAN COST-3, COST-4): the daily decision and LLM caps,
// held in memory and seeded from cost_events, and the writer of cost_events rows. It is the
// decision client's budget, spend reporter and cost sink (jev.Budget, jev.SpendReporter,
// jev.CostSink); LLM returns the LLM cap the audit pool, Studio and the knowledge upload hold
// against.
//
// Spend and rows travel apart: a hold's Settle books the cost into the day's spend at once, and
// the row is queued for the database. A row that cannot be written is dropped and counted (D51),
// and the caps still count its cost. Simulated (mock) costs never count toward a cap (D46).
type Ledger struct {
	decision, llm *Budget
	prices        *PriceBook
	audits        *AuditEstimator
	st            LedgerStore
	w             *Writer // nil without a store
	resyncEvery   time.Duration
	now           func() time.Time
	log           *slog.Logger

	// writerG is the writer's goroutine, which Run waits for.
	writerG sync.WaitGroup
}

var (
	_ jev.Budget        = (*Ledger)(nil)
	_ jev.SpendReporter = (*Ledger)(nil)
	_ jev.CostSink      = (*Ledger)(nil)
)

// NewLedger builds the ledger. Call Seed before serving and Run to write rows.
func NewLedger(c LedgerConfig) (*Ledger, error) {
	if c.Prices == nil {
		return nil, errors.New("cost ledger: no price book")
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	log := c.Logger
	if log == nil {
		log = slog.Default()
	}
	audits, err := NewAuditEstimator(c.Prices, c.Judge)
	if err != nil {
		return nil, fmt.Errorf("cost ledger: %w", err)
	}
	l := &Ledger{
		decision: NewBudget(KindDecision, BudgetConfig{CapUSD: c.DecisionCapUSD, EnforcingReserve: c.EnforcingReserve, PerKeyCapUSD: c.PerKeyCapUSD, Now: now}),
		llm:      NewBudget(KindLLM, BudgetConfig{CapUSD: c.LLMCapUSD, Now: now}),
		prices:   c.Prices, audits: audits, st: c.Store, resyncEvery: c.ResyncEvery, now: now, log: log,
	}
	if l.resyncEvery <= 0 {
		l.resyncEvery = DefaultResyncEvery
	}
	if c.Store != nil {
		wc := c.Writer
		if wc.Logger == nil {
			wc.Logger = log
		}
		l.w = NewWriter(c.Store, wc)
	}
	return l, nil
}

// Prices is the ledger's price book.
func (l *Ledger) Prices() *PriceBook { return l.prices }

// Decision is the decision budget.
func (l *Ledger) Decision() *Budget { return l.decision }

// Budget is the budget of kind (KindDecision or KindLLM); nil for another kind.
func (l *Ledger) Budget(kind string) *Budget {
	switch kind {
	case KindDecision:
		return l.decision
	case KindLLM:
		return l.llm
	}
	return nil
}

// Caps are the daily caps of the runtime settings (settings.budgets), USD per UTC day.
type Caps struct {
	DecisionUSDDay float64
	LLMUSDDay      float64
}

// ApplySettings applies the runtime settings in effect: the daily caps and the pricing
// overrides. The server adapts the runtime configuration's snapshots into calls of it (P4-PLAN
// D90: costs never imports runtimecfg), and the env already won over the stored values there.
// Overrides that ValidateOverrides would refuse are left out and logged.
func (l *Ledger) ApplySettings(caps Caps, overrides []decision.PriceEntry) {
	l.decision.SetCap(caps.DecisionUSDDay)
	l.llm.SetCap(caps.LLMUSDDay)
	if dropped := l.prices.SetOverrides(overrides); dropped > 0 {
		l.log.Warn("cost ledger: pricing overrides left out because they are not valid", "count", dropped)
	}
}

// Headroom is the decision spend still allowed today: up to the observe line when enforcing is
// false, up to the cap when true (P4-PLAN D82); +Inf without a cap.
func (l *Ledger) Headroom(enforcing bool) float64 { return l.decision.Headroom(enforcing) }

// CapHitAt is when today's cap of kind was first hit (nil when it was not, or for another kind).
func (l *Ledger) CapHitAt(kind string) *time.Time {
	if b := l.Budget(kind); b != nil {
		return b.CapHitAt()
	}
	return nil
}

// Reserve holds a decision attempt's estimated cost (jev.Budget): enforcing calls may spend the
// enforcing reserve, the others stop at the observe line; a key's calls stop at the per-key cap.
func (l *Ledger) Reserve(r jev.SpendRequest) (jev.Reservation, error) {
	h, err := l.decision.Reserve(r.USD, r.Enforcing, r.KeyLabel)
	if err != nil {
		return nil, jev.ErrBudgetExhausted
	}
	return h, nil
}

// Spend reports the day's decision spend (jev.SpendReporter). As jev's own budget does, Exhausted
// is true from the observe line on: it answers whether a non-enforcing call may still spend.
func (l *Ledger) Spend() jev.SpendStatus {
	st := l.decision.State()
	s := jev.SpendStatus{Day: st.Day, SpentUSD: st.SpentUSD, ReservedUSD: st.ReservedUSD, CapUSD: st.CapUSD}
	if st.CapUSD > 0 {
		s.SoftCapUSD = st.SoftCapUSD
		s.Exhausted = st.SpentUSD >= st.SoftCapUSD-budgetEps
	}
	return s
}

// RecordCost queues the row of one decision attempt (jev.CostSink). Its spend was booked when
// the attempt's hold settled.
func (l *Ledger) RecordCost(e jev.CostEvent) {
	if l.w == nil {
		return
	}
	cost := e.CostUSD
	row := store.CostEvent{
		OccurredAt: e.OccurredAt, TraceID: e.TraceID, Route: e.Route, KeyLabel: e.KeyLabel,
		Component: e.Component, Kind: defaultKind(e.Kind, store.CostKindDecision), Provider: e.Provider, Model: e.Model,
		Attempt: e.Attempt, InputTokens: int64Ptr(e.InputTokens), OutputTokens: int64Ptr(e.OutputTokens), CostUSD: &cost,
		CostSource: e.CostSource, PriceTableVersion: e.PriceTableVersion, LatencyMs: &e.LatencyMs, Status: e.Status,
		Hedge: e.Hedge, Late: e.Late, Estimated: e.Estimated,
	}
	if e.Questions > 0 {
		q := e.Questions
		row.Questions = &q
	}
	if row.OccurredAt.IsZero() {
		row.OccurredAt = l.now()
	}
	l.w.Add(row)
}

// Link ties LLM cost records to what they were spent on (cmd/proxy CostLink, P4-PLAN D45).
type Link struct {
	TraceID   string
	AuditID   string
	Route     string
	KeyLabel  string
	Selection string
	Attempt   int
}

// RecordLLM queues the rows of the engine's LLM cost records (a Studio draft, a knowledge
// ingest), stamped now and priced under the price table version in force. It writes rows only:
// the spend is booked by the Settle of the call's LLM hold.
func (l *Ledger) RecordLLM(records []*pb.CostRecord, link Link) {
	if l.w == nil {
		return
	}
	now := l.now()
	for _, r := range records {
		if r == nil {
			continue
		}
		in, out, latency := r.GetInputTokens(), r.GetOutputTokens(), r.GetLatencyMs()
		row := store.CostEvent{
			OccurredAt: now, TraceID: link.TraceID, AuditID: link.AuditID, Route: link.Route, KeyLabel: link.KeyLabel,
			Component: r.GetComponent(), Kind: defaultKind(r.GetKind(), store.CostKindLLM), Provider: r.GetProvider(),
			Model: r.GetModel(), Attempt: link.Attempt, InputTokens: &in, OutputTokens: &out,
			CostSource: r.GetCostSource(), PriceTableVersion: l.priceVersion(r.GetProvider(), r.GetModel()),
			LatencyMs: &latency, Status: r.GetStatus(), Selection: link.Selection,
		}
		if row.CostSource != decision.CostUnknown {
			c := r.GetCostUsd()
			row.CostUSD = &c
		}
		if q := int(r.GetQuestions()); q > 0 {
			row.Questions = &q
		}
		l.w.Add(row)
	}
}

// priceVersion is the price_table_version a cost of model on provider records: "override" when a
// dashboard override prices it, otherwise the version of the table in force.
func (l *Ledger) priceVersion(provider, model string) string {
	_, v, _ := l.prices.Lookup(provider, model)
	return v
}

// RealCostUSD is the cost of records that counts toward the LLM cap: simulated (mock) records
// and records without a known cost count $0 (D46).
func RealCostUSD(records []*pb.CostRecord) float64 {
	var usd float64
	for _, r := range records {
		switch r.GetCostSource() {
		case decision.CostSimulated, decision.CostUnknown:
			continue
		}
		usd += nonNegative(r.GetCostUsd())
	}
	return usd
}

// LLM is the LLM cap as its callers use it.
func (l *Ledger) LLM() *LLMBudget { return &LLMBudget{l: l} }

// LLMBudget is the daily LLM cap (P4-PLAN D48): hard in every lane and for every caller, with no
// reserve, no per-key cap and no admin bypass.
type LLMBudget struct{ l *Ledger }

// EstimateAuditUSD is what one deep audit is expected to cost (the AuditEstimator).
func (b *LLMBudget) EstimateAuditUSD() float64 { return b.l.audits.Estimate() }

// Reserve holds usd of today's LLM cap for a call of component and returns its settle function,
// which books the call's real cost (RealCostUSD of its records; 0 releases the hold). It fails
// with ErrLLMCapReached when the cap cannot take usd, or is spent: a hold of 0 asks only whether
// the cap is spent (a knowledge ingest, booked afterwards).
func (b *LLMBudget) Reserve(usd float64, component string) (settle func(actualUSD float64), err error) {
	h, err := b.l.llm.Reserve(usd, true, "")
	if err != nil {
		b.l.log.Debug("cost ledger: LLM call refused by the daily cap", "component", component, "usd", usd)
		return nil, ErrLLMCapReached
	}
	return h.Settle, nil
}

// HeadroomUSD is the LLM spend still allowed today (+Inf without a cap).
func (b *LLMBudget) HeadroomUSD() float64 { return b.l.llm.Headroom(true) }

// PriceTableVersion is the price_table_version an LLM cost of model on provider records:
// "override" when a dashboard override prices it, else the version of the table in force.
func (b *LLMBudget) PriceTableVersion(provider, model string) string {
	return b.l.priceVersion(provider, model)
}

// ObserveAudit folds the real cost of an audit the engine answered into the audit estimate. An
// audit whose cost is not known is no observation: one without cost records (an engine that does
// not report them) or with a record no price covers (cost_source unknown). Counting those as $0
// would wear the estimate down to nothing for a judge the price table does not know, where D48
// holds the reference price instead.
func (b *LLMBudget) ObserveAudit(records []*pb.CostRecord) {
	if !costKnown(records) {
		return
	}
	b.l.audits.Observe(RealCostUSD(records))
}

// costKnown reports whether records carry a known cost: at least one record, and none whose cost
// no price covers. Simulated records are known ($0).
func costKnown(records []*pb.CostRecord) bool {
	n := 0
	for _, r := range records {
		if r == nil {
			continue
		}
		if r.GetCostSource() == decision.CostUnknown {
			return false
		}
		n++
	}
	return n > 0
}

// State is the LLM budget's day.
func (b *LLMBudget) State() BudgetState { return b.l.llm.State() }

// BudgetsView is both caps' day: SystemStatus.budgets (real spend only, D46).
type BudgetsView struct {
	Decision BudgetState
	LLM      BudgetState
}

// Budgets reports both caps.
func (l *Ledger) Budgets() BudgetsView {
	return BudgetsView{Decision: l.decision.State(), LLM: l.llm.State()}
}

// Seed raises today's in-memory spend to the stored real spend (max(memory, db)), per cap and per
// key: after a restart, and from rows other replicas wrote. Without a store it does nothing.
func (l *Ledger) Seed(ctx context.Context) error {
	if l.st == nil {
		return nil
	}
	now := l.now().UTC()
	day, start := now.Format(time.DateOnly), now.Truncate(24*time.Hour)
	totals, err := l.st.SpendToday(ctx, start)
	if err != nil {
		return fmt.Errorf("cost ledger: seed: %w", err)
	}
	l.decision.seed(day, totals.DecisionUSD, totals.DecisionByKey)
	l.llm.seed(day, totals.LLMUSD, nil)
	return nil
}

// Run writes rows and reconciles the spend with the stored rows every ResyncEvery until ctx
// ends; the writer then writes what is left. Without a store there is nothing to run.
func (l *Ledger) Run(ctx context.Context) {
	if l.st == nil {
		return
	}
	l.writerG.Add(1)
	go func() { defer l.writerG.Done(); l.w.Run(ctx) }()
	defer l.writerG.Wait()
	t := time.NewTicker(l.resyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := l.Seed(rctx); err != nil && ctx.Err() == nil {
				l.log.Warn("cost ledger: reading today's spend failed; the caps keep the in-memory spend", "err", err)
			}
			cancel()
		}
	}
}

// Flush writes every buffered row now: the drain at shutdown, after the audits finished and
// before the loops stop. Rows that fail stay for the writer's final flush.
func (l *Ledger) Flush(ctx context.Context) error {
	if l.w == nil {
		return nil
	}
	return l.w.Flush(ctx)
}

// PendingRows is how many rows wait to be written.
func (l *Ledger) PendingRows() int {
	if l.w == nil {
		return 0
	}
	return l.w.Pending()
}

func defaultKind(kind, def string) string {
	if kind == "" {
		return def
	}
	return kind
}

func int64Ptr(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}
