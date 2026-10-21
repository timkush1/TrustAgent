package gate

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// lateJev answers every call after the caller's deadline: the call times out, its attempt goes on,
// and the answer (or lateErr) arrives through OnLate. With before, the late answer arrives before
// Decide returns, so before the gate has built its result.
type lateJev struct {
	jev.Client
	before  bool
	lateErr error
	calls   sync.WaitGroup
}

func (l *lateJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	resp, err := l.Client.Decide(context.Background(), req, opt)
	if err != nil {
		return nil, err
	}
	resp.Late, resp.Snapshot = true, "jev-late-snapshot"
	deliver := func() {
		if l.lateErr != nil {
			opt.OnLate(nil, l.lateErr)
			return
		}
		opt.OnLate(resp, nil)
	}
	if l.before {
		deliver()
	} else {
		l.calls.Add(1)
		go func() {
			defer l.calls.Done()
			<-ctx.Done()
			time.Sleep(5 * time.Millisecond)
			deliver()
		}()
	}
	<-ctx.Done()
	return nil, &jev.Error{Kind: jev.KindTimeout, Attempts: 1, Continued: true, Err: ctx.Err()}
}

// lateGateUnder builds a gate on cl whose late decisions land in the returned channel.
func lateGateUnder(t *testing.T, cl jev.Client) (*Gate, chan *Result) {
	t.Helper()
	got := make(chan *Result, 4)
	g, err := New(Config{Jev: cl, Evaluators: registry(t), Budget: decision.DefaultBudget(), Rand: seq(),
		Logger: slog.New(slog.DiscardHandler), OnLate: func(_ context.Context, r *Result) { got <- r }})
	if err != nil {
		t.Fatal(err)
	}
	return g, got
}

// lateExchange is the refund-contradiction scenario on the flag route, with a 50 ms deadline.
func lateExchange(t *testing.T) *Exchange {
	t.Helper()
	x := scenarioExchange(t, repoPolicy(t), "refund-contradiction-flag")
	x.Route.DeadlineMs = 50
	return x
}

func waitLate(t *testing.T, got chan *Result) *Result {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no late decision")
		return nil
	}
}

func TestLateAnswerIsDecidedAgain(t *testing.T) {
	for _, before := range []bool{false, true} {
		cl := &lateJev{Client: strictMock(t, 0).Client, before: before}
		g, got := lateGateUnder(t, cl)
		x := lateExchange(t)
		r := g.Evaluate(context.Background(), x)
		d := r.Decision
		// Fail open at the deadline: the answer passes, unchecked.
		if r.Status != decision.StatusTimeout || d.Action != decision.ActionPass || d.Late || !r.LateExpected {
			t.Fatalf("before=%v: status %s action %s late %v expected %v", before, r.Status, d.Action, d.Late, r.LateExpected)
		}
		l := waitLate(t, got)
		ld := l.Decision
		if !ld.Late || ld.Enforced || ld.Attempt != 0 || ld.TraceID != x.Trace.TraceID || l.Status != decision.StatusOK {
			t.Errorf("before=%v: late decision late %v enforced %v attempt %d status %s", before, ld.Late, ld.Enforced, ld.Attempt, l.Status)
		}
		// The late answers say what the gate would have done: flag the contradiction.
		if ld.Action != decision.ActionFlag || ld.WouldAction != decision.ActionBlock || !slices.Contains(ld.Reasons, "contradiction:critical") {
			t.Errorf("before=%v: late action %s would %s reasons %v", before, ld.Action, ld.WouldAction, ld.Reasons)
		}
		if ld.DecisionModel == nil || ld.DecisionModel.Snapshot == nil || *ld.DecisionModel.Snapshot != "jev-late-snapshot" ||
			ld.DecisionModel.Attempts != 1 || ld.DecisionModel.Calls != 1 {
			t.Errorf("before=%v: late usage %+v", before, ld.DecisionModel)
		}
		if !slices.Contains(l.Flags, FlagLateDecision) || slices.Contains(r.Flags, FlagLateDecision) {
			t.Errorf("before=%v: flags late %v original %v", before, l.Flags, r.Flags)
		}
		if l.Audit != (AuditPlan{}) || l.Body.Store || l.ResponseText != "" {
			t.Errorf("before=%v: a late decision is neither audited nor stored again: %+v %+v", before, l.Audit, l.Body)
		}
		// The original result is the handler's to change; the late one does not share its decision.
		if d.Late || d.Action != decision.ActionPass || ld.Detectors == nil || ld.Compile == nil || ld.PolicyVersion == nil {
			t.Errorf("before=%v: original %+v late compile %v", before, d.Core, ld.Compile)
		}
		contractstest.Validate(t, "decision.v1.schema.json", ld)
		cl.calls.Wait()
	}
}

func TestLateFailureRecordsNothing(t *testing.T) {
	cl := &lateJev{Client: strictMock(t, 0).Client, lateErr: &jev.Error{Kind: jev.KindRetryable, Status: 503, Attempts: 1}}
	g, got := lateGateUnder(t, cl)
	r := g.Evaluate(context.Background(), lateExchange(t))
	if r.Status != decision.StatusTimeout || !r.LateExpected {
		t.Fatalf("status %s expected %v", r.Status, r.LateExpected)
	}
	cl.calls.Wait()
	select {
	case l := <-got:
		t.Errorf("a late failure changed nothing, yet a late decision came: %+v", l.Decision.Core)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestNoLateDecisionWithoutContinuation(t *testing.T) {
	// A timeout whose attempts were cancelled (in flight past half the cap) never answers late.
	cl := &scriptedJev{Client: strictMock(t, 0).Client, err: &jev.Error{Kind: jev.KindTimeout, Attempts: 1}}
	g, got := lateGateUnder(t, cl)
	r := g.Evaluate(context.Background(), lateExchange(t))
	if r.Status != decision.StatusTimeout || r.LateExpected {
		t.Errorf("status %s expected %v", r.Status, r.LateExpected)
	}
	// An answer in time: nothing goes on.
	g, got = lateGateUnder(t, strictMock(t, 0))
	if r := g.Evaluate(context.Background(), scenarioExchange(t, repoPolicy(t), "refund-contradiction-flag")); r.LateExpected || r.Status != decision.StatusOK {
		t.Errorf("answered in time: status %s expected %v", r.Status, r.LateExpected)
	}
	select {
	case <-got:
		t.Error("a late decision without a late answer")
	case <-time.After(20 * time.Millisecond):
	}
	// Without Config.OnLate the late answer is only logged.
	cl2 := &lateJev{Client: strictMock(t, 0).Client}
	if r := newGate(t, cl2, seq()).Evaluate(context.Background(), lateExchange(t)); r.LateExpected {
		t.Error("LateExpected without an OnLate")
	}
	cl2.calls.Wait()
}

// partlyLateJev answers the request holding question qid in time and every other request late.
type partlyLateJev struct {
	lateJev
	inTime string
}

func (p *partlyLateJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	if _, ok := req.Questions[p.inTime]; ok {
		return p.lateJev.Client.Decide(ctx, req, opt)
	}
	return p.lateJev.Decide(ctx, req, opt)
}

func TestLateAnswersMergeWithTheOnesInTime(t *testing.T) {
	cl := &partlyLateJev{lateJev: lateJev{Client: strictMock(t, 0).Client}, inTime: "groundedness__v1"}
	g, got := lateGateUnder(t, cl)
	// One question per request: groundedness answers in time, contradiction late.
	g.cfg.Budget.MaxQuestions = 1
	x := lateExchange(t)
	r := g.Evaluate(context.Background(), x)
	if r.Status != decision.StatusPartial || !r.LateExpected || r.Decision.Compile.Requests != 2 {
		t.Fatalf("status %s expected %v compile %+v", r.Status, r.LateExpected, r.Decision.Compile)
	}
	l := waitLate(t, got)
	if l.Status != decision.StatusOK || l.Decision.DecisionModel.Calls != 2 || l.Decision.Action != decision.ActionFlag {
		t.Errorf("late status %s usage %+v action %s", l.Status, l.Decision.DecisionModel, l.Decision.Action)
	}
	for _, e := range l.Decision.Evaluators {
		if e.Status != decision.EvalAssessed {
			t.Errorf("late evaluator %s status %s", e.Ref, e.Status)
		}
	}
	cl.calls.Wait()
}

func TestContinuedMarksOnlyContinuingTimeouts(t *testing.T) {
	outs := []callOutcome{
		{err: &jev.Error{Kind: jev.KindTimeout, Continued: true}},
		{err: &jev.Error{Kind: jev.KindTimeout}},
		{err: errors.New("plain")},
		{resp: &jev.Response{}},
	}
	if got := continued(outs); !slices.Equal(got, []int{0}) {
		t.Errorf("continued %v", got)
	}
}
