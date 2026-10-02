package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/worker"
)

// CostLink ties LLM cost records to what they were spent on (P4-PLAN D45): the trace, the audit
// (LLM rows link by audit_id and are stamped at completion), the route, the caller's key label,
// the audit selection and the attempt.
type CostLink struct {
	TraceID   string
	AuditID   string
	Route     string
	KeyLabel  string
	Selection string
	Attempt   int
}

// costsBootTimeout bounds the reads initCosts makes before the server serves: today's spend,
// today's uniform samples and the last decision-model snapshot.
const costsBootTimeout = 10 * time.Second

// initCosts builds the cost ledger (SPEC §7.4, P4-PLAN COST-3..5) and fills s.costs:
//
//   - the price book: the dashboard overrides, then TRUTHTABLE_PRICE_TABLE_FILE, then the
//     embedded contracts table (D53);
//   - the ledger: the daily decision and LLM caps, seeded from today's stored rows before the
//     server serves, and the writer of cost_events rows (with a database);
//   - the runtime settings' caps and pricing overrides, applied now and on every new snapshot
//     (the env already won there, D33; costs never imports runtimecfg, D90);
//   - the decision client's budget, cost sink and prices; the LLM cap of the audit pool, Studio
//     and the knowledge upload; the status budgets.
//
// How LLM callers use the hooks (D48): reserve the estimate with s.costs.reserve, call the engine,
// then settle with what its records count against the cap (s.costs.capCost, D91: simulated ones
// count $0, unpriced ones the reference judge's price) and record the records with
// s.costs.record. The settle books the spend; recording writes the rows.
func (s *server) initCosts(d serverDeps) {
	cfg := s.cfg
	book, err := costs.LoadPriceBook(cfg.PriceTableFile)
	if err != nil {
		s.log.Error("cost ledger: TRUTHTABLE_PRICE_TABLE_FILE is not usable; costs are priced from the embedded table", "err", err)
		if book, err = costs.LoadPriceBook(""); err != nil {
			s.log.Error("cost ledger: the embedded price table does not load; costs keep their defaults", "err", err)
			return
		}
	}
	lc := costs.LedgerConfig{
		DecisionCapUSD: cfg.SpendCapDecisionUSDDay, EnforcingReserve: cfg.Jev.SpendEnforcingReserve,
		PerKeyCapUSD: cfg.Jev.KeyUSDDay, LLMCapUSD: cfg.SpendCapLLMUSDDay,
		Prices: book, Judge: s.auditJudge(), Logger: s.log,
	}
	if d.pg != nil {
		lc.Store = d.pg
	}
	ledger, err := costs.NewLedger(lc)
	if err != nil {
		s.log.Error("cost ledger: not built; costs keep their defaults", "err", err)
		return
	}
	if s.rt != nil && s.rt.manager != nil {
		apply := func(snap *runtimecfg.Snapshot) {
			b := snap.Settings.Budgets
			ledger.ApplySettings(costs.Caps{DecisionUSDDay: b.DecisionUSDDay, LLMUSDDay: b.LLMUSDDay}, snap.Settings.PricingOverrides)
		}
		apply(s.rt.manager.Current())
		s.rt.manager.Subscribe(apply)
	}

	llm := ledger.LLM()
	s.costs = costHooks{
		decision: ledger, budget: ledger, prices: book.JevPrices,
		reserveLLM: func(_ context.Context, usd float64, component string) (func(float64), error) {
			return llm.Reserve(usd, component)
		},
		recordLLM: func(_ context.Context, records []*pb.CostRecord, link CostLink) {
			ledger.RecordLLM(records, costs.Link(link))
		},
		effectivePriceJSON: book.EffectiveJSON,
		decisionHeadroom:   ledger.Headroom,
		capCostLLM:         ledger.CapCostUSD,
	}
	if d.pg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), costsBootTimeout)
		defer cancel()
		if err := ledger.Seed(ctx); err != nil {
			s.log.Warn("cost ledger: today's spend could not be read; the caps start from $0 until the next resync", "err", err)
		}
		now := time.Now()
		if n, _, err := d.pg.UniformToday(ctx, now, cfg.UniformAuditsPerDay); err != nil {
			s.log.Warn("cost ledger: today's uniform samples could not be read; the daily count starts at 0", "err", err)
		} else {
			s.costs.uniformSeed = int(n)
		}
		if backend, model := decisionModelRef(cfg); backend != "" {
			snap, err := d.pg.LastDecisionSnapshot(ctx, backend, model)
			if err != nil {
				s.log.Warn("cost ledger: the last decision-model snapshot could not be read", "err", err)
			}
			s.costs.knownSnapshot = snap
		}
	}
	if d.pg != nil && s.status != nil {
		day := &decisionDayCache{read: d.pg.Status24h, now: time.Now, log: s.log}
		ctx, cancel := context.WithTimeout(context.Background(), costsBootTimeout)
		day.refresh(ctx)
		cancel()
		s.costs.refreshDecisionDay = day.refresh
		s.status.decisionDay = day.get
	}
	s.pool.SetBudget(llm)
	s.pool.SetCostRecorder(func(a worker.AuditCosts) { llm.ObserveAudit(a.Records) })
	s.status.spend = func() *spendView { return budgetsView(ledger.Budgets()) }
}

// ledger is the cost ledger initCosts built, nil when it built none.
func (s *server) ledger() *costs.Ledger {
	l, _ := s.costs.decision.(*costs.Ledger)
	return l
}

// auditJudge reports the judge the engine last said it uses (EngineInfo.judge), for the audit
// estimate; nil without an engine.
func (s *server) auditJudge() costs.JudgeFunc {
	if s.pinger == nil {
		return nil
	}
	p := s.pinger
	return func() (string, string) {
		j := p.Health().Info.GetJudge()
		return j.GetEffectiveProvider(), j.GetModel()
	}
}

// decisionModelRef is the backend and default model the decision client resolves to from the
// env ("" for off and mock, which have no snapshot to remember).
func decisionModelRef(cfg *config.Config) (backend, model string) {
	info := jev.Resolve(jev.Config{
		Backend: cfg.Jev.Backend, Model: cfg.Jev.Model, DemoMode: cfg.DemoMode,
		OpenRouterKey: cfg.Jev.OpenRouterKey, TypeSafeKey: cfg.Jev.TypeSafeKey, JevAPIKey: cfg.Jev.APIKey,
		BaseURL: cfg.Jev.BaseURL, Path: cfg.Jev.Path, OpenRouterBaseURL: cfg.Jev.OpenRouterBaseURL,
	})
	switch info.Backend {
	case jev.BackendOff, jev.BackendMock, "":
		return "", ""
	}
	return info.Backend, info.Model
}

// budgetsView is SystemStatus.budgets from the ledger: real spend only (D46). The decision cap is
// exhausted at the cap and reserve_only from the observe line, where only enforcing routes still
// spend; the LLM cap is exhausted once its spend reaches it.
func budgetsView(v costs.BudgetsView) *spendView {
	return &spendView{
		DecisionUSDDay: v.Decision.CapUSD, DecisionSpentTodayUSD: v.Decision.SpentUSD,
		LLMUSDDay: v.LLM.CapUSD, LLMSpentTodayUSD: v.LLM.SpentUSD,
		DecisionExhausted: v.Decision.Exhausted, DecisionReserveOnly: v.Decision.ReserveOnly, LLMExhausted: v.LLM.Exhausted,
	}
}

// mountCostAPI mounts the ROI time series (SPEC §2.4 row 48, roi_api.go; read scope).
func (s *server) mountCostAPI(api *gin.RouterGroup, g apiGuards) {
	api.GET("/roi/timeseries", g.read, g.rateLimit, s.handleRoiTimeseries)
}

// startCostLoops runs the ledger until ctx ends: the cost_events writer (every 250 ms or 200
// rows) and the 60 s reconciliation of the caps with the stored rows. Without a database there is
// nothing to run.
func (s *server) startCostLoops(ctx context.Context, wg *sync.WaitGroup) {
	l := s.ledger()
	if l == nil {
		return
	}
	wg.Add(1)
	go func() { defer wg.Done(); l.Run(ctx) }()
	if refresh := s.costs.refreshDecisionDay; refresh != nil {
		wg.Add(1)
		go func() { defer wg.Done(); everyMinute(ctx, refresh) }()
	}
}

// everyMinute calls f every costs.DefaultResyncEvery until ctx ends, each call bounded by
// costsBootTimeout.
func everyMinute(ctx context.Context, f func(context.Context)) {
	t := time.NewTicker(costs.DefaultResyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, costsBootTimeout)
			f(rctx)
			cancel()
		}
	}
}

// decisionDayCache is the decision model's calls and cost over the last 24 hours, read from
// cost_events (DecisionModelStatus.calls_24h and cost_24h_usd, D92): it survives a restart, unlike
// the decision client's own counters, which also restart on a hot swap. The cost loop refreshes it
// every minute, so /api/status never queries for it; until the first read succeeds, get reports
// nothing and the status keeps the client's counters.
type decisionDayCache struct {
	read func(ctx context.Context, now time.Time) (store.DecisionDay, error)
	now  func() time.Time
	log  *slog.Logger
	v    atomic.Pointer[store.DecisionDay]
}

// refresh re-reads the last 24 hours; a failed read keeps the previous value.
func (c *decisionDayCache) refresh(ctx context.Context) {
	day, err := c.read(ctx, c.now())
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("cost ledger: the decision model's last 24 hours could not be read; /api/status keeps the previous figures", "err", err)
		}
		return
	}
	c.v.Store(&day)
}

// get is the last value read (the statusReporter decisionDay hook).
func (c *decisionDayCache) get() (store.DecisionDay, bool) {
	if d := c.v.Load(); d != nil {
		return *d, true
	}
	return store.DecisionDay{}, false
}

// stopCosts writes the ledger's buffered rows at shutdown, after the audits drained. Rows booked
// later (a late decision attempt) are written by the writer's final flush when the loops stop.
func (s *server) stopCosts(ctx context.Context) error {
	l := s.ledger()
	if l == nil {
		return nil
	}
	return l.Flush(ctx)
}
