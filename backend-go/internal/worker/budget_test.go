package worker

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/store"
)

// capBudget is an LLM cap with exact bookkeeping: a hold of usd is refused when the day's spend
// and holds would pass the cap (a hold of 0 only when the cap is spent), and every settle books
// the actual cost once.
type capBudget struct {
	mu                      sync.Mutex
	cap, est                float64
	spent, reserved         float64
	holds                   int
	settled                 []float64
	refused                 int
	versionCalls            int
	versionProvider, vModel string
}

var errCap = errors.New("cap reached")

func (b *capBudget) EstimateAuditUSD() float64 { return b.est }

func (b *capBudget) Reserve(usd float64, _ string) (func(float64), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent+b.reserved+usd > b.cap+1e-12 || b.spent >= b.cap {
		b.refused++
		return nil, errCap
	}
	b.reserved += usd
	b.holds++
	var once sync.Once
	return func(actual float64) {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.reserved -= usd
			b.spent += actual
			b.holds--
			b.settled = append(b.settled, actual)
		})
	}, nil
}

func (b *capBudget) HeadroomUSD() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cap - b.spent - b.reserved
}

func (b *capBudget) PriceTableVersion(provider, model string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.versionCalls++
	b.versionProvider, b.vModel = provider, model
	if provider == "anthropic" {
		return "override"
	}
	return "2026-09-27"
}

func (b *capBudget) state() (spent, reserved float64, holds int, settled []float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent, b.reserved, b.holds, append([]float64(nil), b.settled...)
}

// jobsTotal reads audit_jobs_total for lane, selection and outcome (0 when absent).
func jobsTotal(t *testing.T, lane, selection, outcome string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"lane": lane, "selection": selection, "outcome": outcome}
	for _, f := range families {
		if f.GetName() != "trustagent_gateway_audit_jobs_total" {
			continue
		}
	metric:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if want[l.GetName()] != l.GetValue() {
					continue metric
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

func unstartedPool(t *testing.T, cfg Config) *Pool {
	t.Helper()
	eng := enginetest.Start(t, &enginetest.Engine{})
	c, err := grpc.NewAuditClient(eng.Target, time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	cfg.Engine, cfg.Logger = c, slog.New(slog.DiscardHandler)
	return New(cfg)
}

// Past the cap every lane and every selection is refused (D48: no admin bypass, uniform samples
// included), with ErrLLMBudget, the audit_dropped reason llm_budget and the audit_jobs_total
// outcome llm_budget; nothing is queued and nothing stays held.
func TestEveryLaneIsRefusedPastTheLLMCap(t *testing.T) {
	b := &capBudget{cap: 1, est: 0.01, spent: 1}
	p := unstartedPool(t, Config{HighPerMin: 30})
	p.SetBudget(b)
	for _, c := range []struct{ lane, selection string }{
		{LaneHigh, "high"}, {LaneLow, "sample"}, {LaneLow, "sample_error"}, {LaneLow, "uniform"},
		{LaneLow, "direct"}, {LaneLow, "playground"},
	} {
		before := jobsTotal(t, c.lane, c.selection, DropLLMBudget)
		lane, err := p.Submit(job(c.lane, c.selection))
		if !errors.Is(err, ErrLLMBudget) || lane != "" || DropReason(err) != "llm_budget" {
			t.Errorf("%s/%s past the cap: lane %q err %v", c.lane, c.selection, lane, err)
		}
		if got := jobsTotal(t, c.lane, c.selection, DropLLMBudget) - before; got != 1 {
			t.Errorf("%s/%s: audit_jobs_total{outcome=llm_budget} rose by %v", c.lane, c.selection, got)
		}
	}
	if p.QueueDepth(LaneHigh)+p.QueueDepth(LaneLow) != 0 {
		t.Errorf("refused audits were queued: %d high, %d low", p.QueueDepth(LaneHigh), p.QueueDepth(LaneLow))
	}
	if _, reserved, holds, _ := b.state(); reserved != 0 || holds != 0 {
		t.Errorf("refused audits hold %v in %d holds", reserved, holds)
	}
	// A mock or local judge's estimate is $0: it still stops once the cap is spent.
	free := &capBudget{cap: 1, est: 0, spent: 1}
	p.SetBudget(free)
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrLLMBudget) {
		t.Errorf("a $0 estimate past the cap: %v", err)
	}
}

// A queued audit holds one estimate; a full queue releases the hold of the audit it refuses.
func TestFullQueueReleasesItsHold(t *testing.T) {
	b := &capBudget{cap: 1, est: 0.25}
	p := unstartedPool(t, Config{LowQueue: 1, HighQueue: 1})
	p.SetBudget(b)
	if _, err := p.Submit(job(LaneLow, "sample")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second audit: %v", err)
	}
	spent, reserved, holds, settled := b.state()
	if holds != 1 || math.Abs(reserved-0.25) > 1e-12 || spent != 0 || len(settled) != 1 || settled[0] != 0 {
		t.Errorf("after a full queue: holds %d reserved %v spent %v settled %v", holds, reserved, spent, settled)
	}
}

// The engine is told to keep an audit lean once the cap cannot take one more after it
// (AuditOptions.llm_budget_exhausted), and the audit after that is refused.
func TestNearCapFlagIsSet(t *testing.T) {
	b := &capBudget{cap: 0.025, est: 0.01}
	p := unstartedPool(t, Config{})
	p.SetBudget(b)
	first, second := job(LaneLow, "sample"), job(LaneLow, "sample")
	first.Request.Options = &pb.AuditOptions{Verifier: "llm"}
	if _, err := p.Submit(first); err != nil {
		t.Fatal(err)
	}
	if first.Request.GetOptions().GetLlmBudgetExhausted() || first.Request.GetOptions().GetVerifier() != "llm" {
		t.Errorf("with room for another audit: options %v", first.Request.GetOptions())
	}
	if _, err := p.Submit(second); err != nil {
		t.Fatal(err)
	}
	if !second.Request.GetOptions().GetLlmBudgetExhausted() {
		t.Error("the last audit the cap can take is not flagged llm_budget_exhausted")
	}
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrLLMBudget) {
		t.Errorf("the third audit: %v", err)
	}
}

// costed answers an audit with a real cost of usd (plus a simulated call that counts $0) and a
// call no price covers.
func costed(usd float64) func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
	return func(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
		res, _ := hallucinated(ctx, req)
		res.Costs = []*pb.CostRecord{
			{Component: "deep_audit_decompose", Kind: "llm", Provider: "mock", Model: "mock", InputTokens: 180,
				OutputTokens: 40, CostUsd: 0.5, CostSource: "simulated", Status: "ok"},
			{Component: "deep_audit_verify", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5", InputTokens: 400,
				OutputTokens: 60, CostUsd: usd, CostSource: "computed", LatencyMs: 900, Status: "ok", Questions: 3},
			{Component: "deep_audit_verify", Kind: "llm", Provider: "acme", Model: "secret-model", InputTokens: 10,
				OutputTokens: 5, CostSource: "unknown", Status: "ok"},
		}
		return res, nil
	}
}

// Holds settle on the engine's cost records: an audit that cost less than its estimate gives the
// rest back, one that cost more books its real cost; simulated and unpriced calls count $0 (D46).
func TestHoldsReconcileWithTheRealCost(t *testing.T) {
	for _, c := range []struct {
		name      string
		est, real float64
	}{{"over-estimate", 0.01, 0.0007}, {"under-estimate", 0.01, 0.05}} {
		t.Run(c.name, func(t *testing.T) {
			eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: costed(c.real)})
			b := &capBudget{cap: 1, est: c.est}
			p, _, pub := startPool(t, eng, Config{Workers: 1})
			p.SetBudget(b)
			if _, err := p.Submit(job(LaneLow, "sample")); err != nil {
				t.Fatal(err)
			}
			if ev := pub.outcome(t); ev.Type != "audit_completed" {
				t.Fatalf("outcome %s", ev.Type)
			}
			spent, reserved, holds, settled := b.state()
			if math.Abs(spent-c.real) > 1e-12 || reserved != 0 || holds != 0 || len(settled) != 1 {
				t.Errorf("spent %v reserved %v holds %d settled %v, want %v booked", spent, reserved, holds, settled, c.real)
			}
		})
	}
}

// An engine call that returns no result settles at the estimate when the judge may have been
// called (a deadline, an internal error) and at $0 when the engine refused it first (unavailable).
func TestFailedCallsSettleByWhatTheEngineMayHaveSpent(t *testing.T) {
	for _, c := range []struct {
		code codes.Code
		want float64
	}{{codes.DeadlineExceeded, 0.02}, {codes.Internal, 0.02}, {codes.Unavailable, 0}, {codes.ResourceExhausted, 0}} {
		t.Run(c.code.String(), func(t *testing.T) {
			eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
				return nil, status.Error(c.code, "no result")
			}})
			b := &capBudget{cap: 1, est: 0.02}
			p, _, pub := startPool(t, eng, Config{Workers: 1})
			p.SetBudget(b)
			if _, err := p.Submit(job(LaneLow, "sample")); err != nil {
				t.Fatal(err)
			}
			pub.outcome(t)
			spent, reserved, holds, _ := b.state()
			if spent != c.want || reserved != 0 || holds != 0 {
				t.Errorf("%s: spent %v reserved %v holds %d, want %v", c.code, spent, reserved, holds, c.want)
			}
		})
	}
}

// linkedStore is a fakeStore that also takes the audit's cost rows (worker.LinkedCostStore).
type linkedStore struct {
	*fakeStore
	rows map[string][]store.CostEvent
}

func (s *linkedStore) SaveAuditResultWithCosts(ctx context.Context, r *store.AuditRecord, status string, rows []store.CostEvent) error {
	s.mu.Lock()
	if s.rows == nil {
		s.rows = map[string][]store.CostEvent{}
	}
	s.rows[r.AuditID] = rows
	s.mu.Unlock()
	return s.SaveAuditResult(ctx, r, status)
}

// An answered audit's cost rows are linked by audit_id and stamped when it completed (D45), with
// the job's trace, route, key label and selection, attempt 0 and each call's price table version;
// an unpriced call keeps a NULL cost.
func TestAuditCostRowsAreLinkedToTheAudit(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: costed(0.0007)})
	b := &capBudget{cap: 1, est: 0.01}
	st, pub := &linkedStore{fakeStore: &fakeStore{}}, newPub()
	c, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	completed := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	p := New(Config{Workers: 1, Engine: c, Store: st, Publisher: pub, Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return completed }})
	p.SetBudget(b)
	p.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Drain(ctx)
	})
	j := job(LaneLow, "uniform")
	j.KeyLabel = "support-app"
	if _, err := p.Submit(j); err != nil {
		t.Fatal(err)
	}
	pub.outcome(t)
	st.mu.Lock()
	rows := st.rows[j.Request.GetAuditId()]
	saved := st.saved
	st.mu.Unlock()
	if len(rows) != 3 || len(saved) != 1 || len(saved[0].Costs) != 3 {
		t.Fatalf("rows %d, saved %d", len(rows), len(saved))
	}
	for i, r := range rows {
		if r.AuditID != j.Request.GetAuditId() || r.TraceID != j.Request.GetTraceId() || r.Route != "support-bot" ||
			r.KeyLabel != "support-app" || r.Selection != "uniform" || r.Attempt != 0 || !r.OccurredAt.Equal(completed) ||
			r.Kind != store.CostKindLLM || r.Hedge || r.Late || r.Estimated {
			t.Errorf("row %d link: %+v", i, r)
		}
	}
	if rows[1].PriceTableVersion != "override" || rows[0].PriceTableVersion != "2026-09-27" {
		t.Errorf("versions %q %q", rows[0].PriceTableVersion, rows[1].PriceTableVersion)
	}
	if rows[1].CostUSD == nil || *rows[1].CostUSD != 0.0007 || *rows[1].Questions != 3 || *rows[1].LatencyMs != 900 ||
		rows[0].CostSource != "simulated" || rows[0].CostUSD == nil {
		t.Errorf("priced rows %+v %+v", rows[0], rows[1])
	}
	if rows[2].CostUSD != nil || rows[2].CostSource != "unknown" {
		t.Errorf("an unpriced call has cost %v (%s)", rows[2].CostUSD, rows[2].CostSource)
	}
	if !strings.HasPrefix(rows[1].Component, "deep_audit") {
		t.Errorf("component %q", rows[1].Component)
	}
}
