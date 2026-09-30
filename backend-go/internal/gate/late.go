package gate

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/calibration"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// lateWriteTimeout bounds what Config.OnLate may spend recording one late decision.
const lateWriteTimeout = 10 * time.Second

// lateGate collects the answers of one round's requests that arrive after the gate stopped
// waiting (SPEC §2.9 late decisions). Once Evaluate has built its result and every request that
// went on has resolved, with at least one answer among them, the exchange is decided again with
// every answer: the late decision of the same attempt, with late=true and enforced=false, handed
// to Config.OnLate. It is never enforced and never audited. Without Config.OnLate the late
// answers are only logged.
type lateGate struct {
	g       *Gate
	traceID string

	mu      sync.Mutex
	arrived map[int]callOutcome
	// eval and pending are set by arm; done once the late decision was handed over, or when
	// there is none to make.
	eval    *lateEval
	pending []int
	done    bool
}

// lateEval is what deciding again needs, captured when Evaluate built the result: the handler may
// change the result (its flags, its decision's enforced) once Evaluate returns.
type lateEval struct {
	round    *callRound
	in       decision.Input
	model    string
	route    *policy.Route
	start    time.Time
	decision Decision
	result   Result
}

// receive is the CallOptions.OnLate of request k: the client calls it at most once, from its own
// goroutine, possibly before Evaluate returns.
func (l *lateGate) receive(k int) func(*jev.Response, error) {
	return func(resp *jev.Response, err error) {
		if err != nil {
			l.g.log.Info("gate: late decision-model call failed", "trace_id", l.traceID, "request", k, "kind", jev.ErrorKind(err))
		} else {
			l.g.log.Info("gate: late decision arrived (recorded only, never enforced)", "trace_id", l.traceID, "request", k,
				"latency_ms", resp.LatencyMs, "snapshot", resp.Snapshot)
		}
		l.mu.Lock()
		l.arrived[k] = callOutcome{resp, err}
		ev := l.ready()
		l.mu.Unlock()
		if ev != nil {
			l.decide(ev)
		}
	}
}

// arm hands over what deciding again needs once Evaluate built the result. It reports whether a
// late decision may follow: some request of the round went on after the deadline.
func (l *lateGate) arm(ev *lateEval) bool {
	pending := continued(ev.round.outs)
	l.mu.Lock()
	if len(pending) == 0 || l.g.cfg.OnLate == nil {
		l.done = true
		l.mu.Unlock()
		return false
	}
	l.eval, l.pending = ev, pending
	ready := l.ready()
	l.mu.Unlock()
	if ready != nil {
		// Every late answer is in already; Evaluate does not wait for the late decision.
		go l.decide(ready)
	}
	return true
}

// ready returns the evaluation to decide again, once, when the gate is armed and every request
// that went on has resolved. The caller holds mu.
func (l *lateGate) ready() *lateEval {
	if l.done || l.eval == nil {
		return nil
	}
	for _, k := range l.pending {
		if _, ok := l.arrived[k]; !ok {
			return nil
		}
	}
	l.done = true
	return l.eval
}

// decide decides the exchange again with the late answers merged in (their requests now ok, or the
// error they ended with) and hands the late decision to Config.OnLate. When no late request
// answered, nothing changed and nothing is recorded.
func (l *lateGate) decide(ev *lateEval) {
	g := l.g
	outs := slices.Clone(ev.round.outs)
	answered := false
	l.mu.Lock()
	for k, o := range l.arrived {
		if k < len(outs) {
			outs[k] = o
			answered = answered || o.err == nil
		}
	}
	l.mu.Unlock()
	if !answered {
		return
	}
	round := *ev.round
	round.outs = outs
	usage, answers, status, _ := g.collect(&round, ev.model)
	in := ev.in
	in.Answers, in.Meta.RequestStatus, in.Meta.SkipReason = answers, status, nil
	in.Calibration = map[string]decision.CalibrationEntry{}
	if usage.Snapshot != nil {
		// The calibration of the snapshot that answered late (SPEC §2.11).
		in.Calibration = calibration.Entries(g.cfg.Calibrations, in.Evaluators, routeParams(ev.route), *usage.Snapshot)
	}
	core, err := decision.Decide(&in)
	if err != nil {
		g.log.Error("gate: the late decision failed", "trace_id", l.traceID, "err", err)
		return
	}
	core.Enforced = false
	gateMs := g.cfg.Now().Sub(ev.start).Milliseconds()
	d := ev.decision
	d.Core, d.DecisionModel, d.Late, d.GateMs = *core, usage, true, &gateMs
	res := ev.result
	res.Decision, res.Status = &d, statusValue(core, status)
	res.Flags = appendFlag(slices.Clone(res.Flags), FlagLateDecision)
	ctx, cancel := context.WithTimeout(context.Background(), lateWriteTimeout)
	defer cancel()
	g.cfg.OnLate(ctx, &res)
}

// lateResult is the part of a result a late decision keeps: what identifies the trace and the
// route, never the texts, the audit plan or the body plan (a late decision is not audited, and
// the trace body was stored with the original decision).
func lateResult(res *Result) Result {
	return Result{
		Trace: res.Trace, RouteLabel: res.RouteLabel, CheckedAgainst: res.CheckedAgainst, Headers: res.Headers,
		Flags: slices.Clone(res.Flags),
	}
}
