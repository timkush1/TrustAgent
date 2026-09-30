package main

import (
	"context"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
)

// Status-event components (api.ts WsEvent status).
const (
	componentEngine        = "engine"
	componentJudge         = "judge"
	componentKnowledge     = "knowledge"
	componentDecisionModel = "decision_model"
	componentBudget        = "budget"
)

// Decision budget states of the budget component.
const (
	budgetOK          = "ok"
	budgetReserveOnly = "reserve_only"
	budgetExhausted   = "exhausted"
)

// statusWatchInterval is how often startStatusWatch reads the component states.
const statusWatchInterval = 5 * time.Second

// publishEngineStatus logs an engine health transition (the pinger calls it on every change of
// the engine's state) and broadcasts the WS status of the engine, its judge and the knowledge
// add-on, each only when it changed.
func (s *server) publishEngineStatus(h grpc.EngineHealth) {
	if h.Reachable() {
		s.log.Info("audit engine health", "state", h.State, "detail", h.Detail)
	} else {
		s.log.Warn("audit engine health", "state", h.State, "detail", h.Detail)
	}
	s.publishEngineComponents(h)
}

// publishEngineComponents broadcasts the engine, judge and knowledge states of h that changed.
// While a configured engine has not answered (not checked yet, or down), nothing is known about
// its knowledge store: that is unknown, not disabled, so the stream neither announces the add-on
// off before the first health check nor turns it off when the engine stops answering.
func (s *server) publishEngineComponents(h grpc.EngineHealth) {
	s.hub.PublishStatus(componentEngine, h.State, h.Detail)
	judge := judgeState(h)
	s.hub.PublishStatus(componentJudge, judge, judgeDetail(judge))
	k := knowledgeStatus(h)
	if h.Response == nil && h.State != stateDisabled {
		k.State, k.Detail = stateUnknown, ""
	}
	s.hub.PublishStatus(componentKnowledge, k.State, k.Detail)
}

// judgeDetail explains a judge state that is not up.
func judgeDetail(state string) string {
	switch state {
	case stateDown:
		return "the audit engine reports its judge unhealthy"
	case stateDegraded:
		return "the configured judge is unavailable; the engine uses its fallback judge"
	case stateDisabled:
		return "no deep-audit judge is available: audits come back not assessed"
	}
	return ""
}

// startStatusWatch starts the loop on wg that broadcasts, until ctx ends, the status transitions
// nothing else reports: every statusWatchInterval it reads the decision model's state (breaker,
// credential and budget states included) and its daily budget, and the cached engine health,
// whose judge and knowledge states can change while the engine's own state does not.
func (s *server) startStatusWatch(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(statusWatchInterval)
		defer t.Stop()
		for {
			s.watchStatus()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// watchStatus broadcasts the component states that changed since the last broadcast.
func (s *server) watchStatus() {
	if s.status.engine != nil { // an engine is configured
		s.publishEngineComponents(s.status.engineHealth())
	}
	st := s.status.jevStatus()
	s.hub.PublishStatus(componentDecisionModel, s.status.decisionModel().State, decisionModelDetail(st))
	if state, detail, ok := budgetState(st.Spend); ok {
		s.hub.PublishStatus(componentBudget, state, detail)
	}
}

// decisionModelDetail is the last error's message while the decision model is not working.
func decisionModelDetail(st jev.Status) string {
	switch st.State {
	case "ready", "mock", "off", "checking", "":
		return ""
	}
	if st.LastError != nil {
		return st.LastError.Message
	}
	return ""
}

// budgetCapEps absorbs float rounding in the cap comparisons (jev's budgetEps).
const budgetCapEps = 1e-8

// budgetState is the decision model's daily budget as a status: exhausted once the day's spend
// reaches the cap, reserve_only once observe and flag calls stop at (1 − reserve)·cap while
// enforcing routes still spend the reserve, ok otherwise. ok is false when the client keeps no
// budget.
//
// jev.SpendStatus.Exhausted is already true at that soft cap (it answers "may a non-enforcing
// call spend?"), so only the spend against CapUSD says whether enforcing routes are still checked.
func budgetState(sp *jev.SpendStatus) (state, detail string, ok bool) {
	const (
		exhausted   = "the decision model's daily budget is spent: checks are skipped until tomorrow (UTC)"
		reserveOnly = "the decision budget's share for observe and flag routes is spent: only enforcing routes are checked"
	)
	switch {
	case sp == nil:
		return "", "", false
	case sp.CapUSD > 0 && sp.SpentUSD >= sp.CapUSD-budgetCapEps, sp.CapUSD <= 0 && sp.Exhausted:
		return budgetExhausted, exhausted, true
	case sp.CapUSD > 0 && sp.SoftCapUSD < sp.CapUSD-budgetCapEps &&
		(sp.Exhausted || sp.SpentUSD+sp.ReservedUSD >= sp.SoftCapUSD-budgetCapEps):
		return budgetReserveOnly, reserveOnly, true
	}
	return budgetOK, "", true
}
