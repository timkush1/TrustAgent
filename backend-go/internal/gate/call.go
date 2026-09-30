package gate

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// callOutcome is the outcome of one compiled request.
type callOutcome struct {
	resp *jev.Response
	err  error
}

// spent is what earlier rounds of one exchange cost: a too_large resend counts the refused round's
// calls, attempts, cost and tokens in the decision's usage, and its time in the latency.
type spent struct {
	calls, attempts int
	hedged          bool
	costUSD         *float64
	inputTokens     *int
	ms              int64
	costSources     []string
}

// callRound is the decision-model calls of one compile: the outcome of each request, what earlier
// rounds spent, and the collector of the answers that arrive after the deadline.
type callRound struct {
	compiled *decision.Compiled
	outs     []callOutcome
	base     spent
	started  time.Time
	late     *lateGate
}

// deadline is how long the gate waits for the decision model: the route deadline (sync) or the
// async deadline (observe).
func (g *Gate) deadline(x *Exchange) time.Duration {
	d := time.Duration(x.Route.DeadlineMs) * time.Millisecond
	if x.Trace.Mode == decision.ModeObserve {
		d = g.cfg.AsyncDeadline
	}
	if d <= 0 {
		d = g.cfg.AttemptTimeout
	}
	return d
}

// consult asks the decision model about a compiled exchange and waits until the route deadline
// (sync) or the async deadline (observe). Each attempt runs detached inside the client with its
// own TRUTHTABLE_JEV_ATTEMPT_TIMEOUT, so an attempt still running at the deadline may finish as a
// late decision, and a hanging vendor still counts against the breaker (SPEC §2.9 late
// decisions).

func (g *Gate) consult(ctx context.Context, x *Exchange, model string, reqRules map[string]string, c *decision.Compiled) *callRound {
	ctx, cancel := context.WithTimeout(ctx, g.deadline(x))
	defer cancel()
	return g.ask(ctx, x, c, spent{})
}

// ask sends every request of a compile concurrently and waits for all of them (each returns at
// ctx's deadline at the latest).
func (g *Gate) ask(ctx context.Context, x *Exchange, c *decision.Compiled, base spent) *callRound {
	r := &callRound{compiled: c, outs: make([]callOutcome, len(c.Requests)), base: base, started: g.cfg.Now(),
		late: &lateGate{g: g, traceID: x.Trace.TraceID, arrived: map[int]callOutcome{}}}
	opt := jev.CallOptions{
		Class: jev.ClassSync, Scenario: x.Trace.Scenario,
		HedgeAfter: time.Duration(x.Route.HedgeAfterMs) * time.Millisecond,
		Enforcing:  x.Trace.Mode == decision.ModeBlock || x.Trace.Mode == decision.ModeFallback,
		TraceID:    x.Trace.TraceID, Route: x.Route.Name, KeyLabel: x.Trace.KeyLabel,
	}
	if x.Trace.Mode == decision.ModeObserve {
		opt.Class = jev.ClassAsync
	}
	var wg sync.WaitGroup
	for k := range c.Requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := opt
			o.OnLate = r.late.receive(k)
			resp, err := g.cfg.Jev.Decide(ctx, jev.NewRequest(&c.Requests[k], x.Trace.TraceID), o)
			r.outs[k] = callOutcome{resp, err}
		}()
	}
	wg.Wait()
	for k, o := range r.outs {
		if o.err != nil {
			g.log.Warn("gate: decision-model call failed", "trace_id", x.Trace.TraceID, "request", k, "kind", jev.ErrorKind(o.err), "err", o.err)
		}
	}
	return r
}

// tooLarge reports whether a request of the round was refused as too large.
func (r *callRound) tooLarge() bool {
	return slices.ContainsFunc(r.outs, func(o callOutcome) bool { return o.err != nil && jev.ErrorKind(o.err) == jev.KindTooLarge })
}

// spentBy is what the round and the rounds before it spent, for the round sent after it.
func (g *Gate) spentBy(r *callRound) spent {
	s := r.base
	s.costSources = slices.Clone(s.costSources)
	s.calls += len(r.compiled.Requests)
	for _, o := range r.outs {
		if o.err != nil {
			s.attempts += attemptsOf(o.err)
			continue
		}
		s.attempts += max(o.resp.Attempts, 1)
		s.hedged = s.hedged || o.resp.Hedged
		if o.resp.CostUSD != nil {
			s.costUSD = addFloat(s.costUSD, *o.resp.CostUSD)
		}
		if o.resp.InputTokens != nil {
			s.inputTokens = addInt(s.inputTokens, *o.resp.InputTokens)
		}
		s.costSources = append(s.costSources, o.resp.CostSource)
	}
	s.ms += g.cfg.Now().Sub(r.started).Milliseconds()
	return s
}

// collect turns a round's outcomes into the decision's usage, answers and request statuses.
//
// When every request of a first round was refused before reaching the vendor (breaker, rate
// limit, budget, missing credentials), skip is that refusal (SPEC §2.9 caller rules); a partial
// refusal, or one after a too_large round, is a request status error:<kind> instead.
func (g *Gate) collect(r *callRound, model string) (usage *DecisionModelUsage, answers map[string]*decision.RawAnswer, status map[string]string, skip string) {
	b := r.base
	usage = &DecisionModelUsage{
		Backend: g.cfg.Jev.Info().Backend, ModelRequested: &model, Calls: b.calls + len(r.compiled.Requests),
		Attempts: b.attempts, Hedged: b.hedged, CostUSD: b.costUSD, InputTokens: b.inputTokens, CostSource: jev.CostUnknown,
	}
	answers = map[string]*decision.RawAnswer{}
	status = map[string]string{}
	costSources := slices.Clone(b.costSources)
	var skips []string
	for k, o := range r.outs {
		key := strconv.Itoa(k)
		if o.err != nil {
			usage.Attempts += attemptsOf(o.err)
			kind := jev.ErrorKind(o.err)
			if reason, ok := jev.SkipReason(o.err); ok {
				skips = append(skips, reason)
			}
			if kind == jev.KindTimeout {
				status[key] = decision.RequestTimeout
			} else {
				status[key] = "error:" + kind
			}
			continue
		}
		status[key] = decision.RequestOK
		resp := o.resp
		for qid := range r.compiled.Requests[k].Questions {
			if a, ok := resp.Answers[qid]; ok {
				answers[qid] = a
			}
		}
		usage.Attempts += max(resp.Attempts, 1)
		usage.Hedged = usage.Hedged || resp.Hedged
		if resp.Snapshot != "" {
			snap := resp.Snapshot
			usage.Snapshot = &snap
		}
		lat := resp.LatencyMs + b.ms
		if usage.LatencyMs == nil || lat > *usage.LatencyMs {
			usage.LatencyMs = &lat
		}
		if resp.ConnectMs != nil {
			usage.ConnectMs = resp.ConnectMs
		}
		if resp.InputTokens != nil {
			usage.InputTokens = addInt(usage.InputTokens, *resp.InputTokens)
		}
		if resp.CostUSD != nil {
			usage.CostUSD = addFloat(usage.CostUSD, *resp.CostUSD)
		}
		costSources = append(costSources, resp.CostSource)
	}
	if len(costSources) > 0 && !slices.ContainsFunc(costSources, func(s string) bool { return s != costSources[0] }) {
		usage.CostSource = costSources[0]
	}
	if b.calls == 0 && len(skips) == len(r.outs) {
		// Nothing reached the vendor: the gate was skipped, not failed (decide() reads skip_reason).
		return usage, answers, map[string]string{}, skips[0]
	}
	return usage, answers, status, ""
}

// attemptsOf is the number of vendor requests a failed call made (0 when it was refused before
// any, such as by the breaker or the budget).
func attemptsOf(err error) int {
	var e *jev.Error
	if errors.As(err, &e) {
		return e.Attempts
	}
	return 1
}

// continued is the requests of a round whose attempts went on after the deadline: their outcome
// arrives later through CallOptions.OnLate.
func continued(outs []callOutcome) []int {
	var out []int
	for k, o := range outs {
		var e *jev.Error
		if o.err != nil && errors.As(o.err, &e) && e.Continued {
			out = append(out, k)
		}
	}
	return out
}
