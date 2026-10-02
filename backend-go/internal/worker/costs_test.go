package worker

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
)

// recorded collects what a CostRecorder receives.
type recorded struct {
	mu   sync.Mutex
	got  []AuditCosts
	seen chan struct{}
}

func newRecorded() *recorded { return &recorded{seen: make(chan struct{}, 8)} }

func (r *recorded) record(a AuditCosts) {
	r.mu.Lock()
	r.got = append(r.got, a)
	r.mu.Unlock()
	r.seen <- struct{}{}
}

func (r *recorded) all() []AuditCosts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AuditCosts(nil), r.got...)
}

// An answered audit's cost records reach the cost recorder after the result is stored, with the
// audit's trace, id, route and selection.
func TestCostRecorderReceivesAnsweredAudits(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: hallucinated})
	p, st, pub := startPool(t, eng, Config{Workers: 1})
	rec := newRecorded()
	p.SetCostRecorder(rec.record)
	j := job(LaneLow, "sample")
	if _, err := p.Submit(j); err != nil {
		t.Fatal(err)
	}
	pub.outcome(t)
	select {
	case <-rec.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorder was not called")
	}
	got := rec.all()
	if len(got) != 1 {
		t.Fatalf("recorded %d audits", len(got))
	}
	a := got[0]
	if a.TraceID != j.Request.GetTraceId() || a.AuditID != j.Request.GetAuditId() || a.Route != "support-bot" || a.Selection != "sample" {
		t.Errorf("link = %+v", a)
	}
	if len(a.Records) != 2 || a.Records[1].GetCostUsd() != 0.0007 {
		t.Errorf("records = %v", a.Records)
	}
	if st.traceStatus(j.Request.GetTraceId()) == "" {
		t.Error("the recorder ran before the result was stored")
	}
}

// A failed engine call has no cost records to learn from: the recorder is not called. Removing
// the recorder stops the calls.
func TestCostRecorderSkipsFailedCalls(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
		return nil, status.Error(codes.DeadlineExceeded, "slow judge")
	}})
	p, _, pub := startPool(t, eng, Config{Workers: 1})
	rec := newRecorded()
	p.SetCostRecorder(rec.record)
	if _, err := p.Submit(job(LaneLow, "sample")); err != nil {
		t.Fatal(err)
	}
	pub.outcome(t)
	if got := rec.all(); len(got) != 0 {
		t.Errorf("a failed call was recorded: %+v", got)
	}

	ok := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: hallucinated})
	p2, _, pub2 := startPool(t, ok, Config{Workers: 1})
	p2.SetCostRecorder(rec.record)
	p2.SetCostRecorder(nil)
	if _, err := p2.Submit(job(LaneLow, "sample")); err != nil {
		t.Fatal(err)
	}
	pub2.outcome(t)
	if got := rec.all(); len(got) != 0 {
		t.Errorf("a removed recorder was called: %+v", got)
	}
}

// fixedBudget is a Budget with a fixed estimate.
type fixedBudget struct{ est float64 }

func (b fixedBudget) EstimateAuditUSD() float64 { return b.est }
func (b fixedBudget) Reserve(float64, string) (func(float64), error) {
	return func(float64) {}, nil
}
func (b fixedBudget) HeadroomUSD() float64                 { return math.Inf(1) }
func (b fixedBudget) PriceTableVersion(_, _ string) string { return "2026-09-27" }
func (b fixedBudget) CapCostUSD(records []*pb.CostRecord) float64 {
	return costs.RealCostUSD(records)
}

// SetBudget is safe before Start and from other goroutines while audits run.
func TestBudgetSetBeforeStartAndConcurrently(t *testing.T) {
	p := New(Config{Workers: 1})
	p.SetBudget(fixedBudget{est: 0.0355})
	if b := p.budget.Load(); b == nil || (*b).EstimateAuditUSD() != 0.0355 {
		t.Fatalf("budget = %v", b)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				p.SetBudget(fixedBudget{est: float64(i)})
			} else {
				p.SetBudget(nil)
			}
		}()
	}
	wg.Wait()
	p.SetBudget(nil)
	if p.budget.Load() != nil {
		t.Error("SetBudget(nil) kept a budget")
	}
}
