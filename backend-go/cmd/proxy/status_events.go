package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/websocket"
)

// Status-event components (api.ts WsEvent status).
const (
	componentEngine        = "engine"
	componentJudge         = "judge"
	componentKnowledge     = "knowledge"
	componentDecisionModel = "decision_model"
	componentBudget        = "budget"
)

// Budget states of the budget component (the worse of the two caps, D49).
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
	// The engine's own words (an error from a judge or a vector store) may echo a key; WS status
	// details leave through the process Redactor (D42).
	s.hub.PublishStatus(componentEngine, h.State, s.redact(h.Detail))
	judge := judgeState(h)
	s.hub.PublishStatus(componentJudge, judge, judgeDetail(judge))
	k := knowledgeStatus(h)
	if h.Response == nil && h.State != stateDisabled {
		k.State, k.Detail = stateUnknown, ""
	}
	s.hub.PublishStatus(componentKnowledge, k.State, s.redact(k.Detail))
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
	s.hub.PublishStatus(componentDecisionModel, s.status.decisionModel().State, s.redact(decisionModelDetail(st)))
	if state, detail, ok := s.budgetStatus(st.Spend); ok {
		s.publishBudget(state, detail)
	}
}

// lastBudgetDetail remembers, per hub, the detail of the last budget status broadcast. The hub
// deduplicates status events by state alone, and D49 needs a change of the caps named at the same
// state (the LLM cap exhausted, then the decision cap too) to reach the web, which refetches
// /api/status on every budget event.
var lastBudgetDetail sync.Map // *websocket.Hub → string

// publishBudget broadcasts the budget status when its state or the caps it names changed.
func (s *server) publishBudget(state, detail string) {
	prev, seen := lastBudgetDetail.Load(s.hub)
	if s.hub.PublishStatus(componentBudget, state, detail) {
		lastBudgetDetail.Store(s.hub, detail)
		return
	}
	if !seen || prev == detail {
		return
	}
	data := map[string]string{"component": componentBudget, "state": state}
	if detail != "" {
		data["detail"] = detail
	}
	ev, err := websocket.NewEvent("status", "", data, nil)
	if err != nil {
		s.log.Error("ws: event does not marshal", "type", "status", "component", componentBudget, "err", err)
		return
	}
	lastBudgetDetail.Store(s.hub, detail)
	s.hub.Publish(ev)
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

// The caps a budget status names (D49).
const (
	capDecision = "decision"
	capLLM      = "llm"
)

// budgetRank orders the budget states: a worse state outranks a better one.
var budgetRank = map[string]int{budgetOK: 0, budgetReserveOnly: 1, budgetExhausted: 2}

// budgetStatus is the WS status of the daily caps (P4-PLAN D49): the worse state of the decision
// cap and the LLM cap (ok < reserve_only < exhausted), with detail naming the caps in that state
// ("decision", "llm" or "decision,llm"; "" when both are ok). The cost ledger reports both caps;
// without one the decision client's own budget reports the decision cap. ok is false when no cap
// is kept at all. The web refetches /api/status on any budget event.
func (s *server) budgetStatus(sp *jev.SpendStatus) (state, detail string, ok bool) {
	type capState struct{ name, state string }
	var caps []capState
	if l := s.ledger(); l != nil {
		v := l.Budgets()
		caps = append(caps, capState{capDecision, ledgerBudgetState(v.Decision)}, capState{capLLM, ledgerBudgetState(v.LLM)})
	} else if st, ok := budgetState(sp); ok {
		caps = append(caps, capState{capDecision, st})
	}
	if len(caps) == 0 {
		return "", "", false
	}
	state = budgetOK
	for _, c := range caps {
		if budgetRank[c.state] > budgetRank[state] {
			state = c.state
		}
	}
	if state == budgetOK {
		return state, "", true
	}
	var names []string
	for _, c := range caps {
		if c.state == state {
			names = append(names, c.name)
		}
	}
	return state, strings.Join(names, ","), true
}

// ledgerBudgetState is a ledger cap's status: exhausted once the day's spend reaches the cap,
// reserve_only once only enforcing calls still spend (the decision cap's observe line), else ok.
func ledgerBudgetState(b costs.BudgetState) string {
	switch {
	case b.Exhausted:
		return budgetExhausted
	case b.ReserveOnly:
		return budgetReserveOnly
	}
	return budgetOK
}

// budgetCapEps absorbs float rounding in the cap comparisons (jev's budgetEps).
const budgetCapEps = 1e-8

// budgetState is the decision client's own daily budget as a status: exhausted once the day's
// spend reaches the cap, reserve_only once observe and flag calls stop at (1 − reserve)·cap while
// enforcing routes still spend the reserve, ok otherwise. ok is false when the client keeps no
// budget.
//
// jev.SpendStatus.Exhausted is already true at that soft cap (it answers "may a non-enforcing
// call spend?"), so only the spend against CapUSD says whether enforcing routes are still checked.
func budgetState(sp *jev.SpendStatus) (state string, ok bool) {
	switch {
	case sp == nil:
		return "", false
	case sp.CapUSD > 0 && sp.SpentUSD >= sp.CapUSD-budgetCapEps, sp.CapUSD <= 0 && sp.Exhausted:
		return budgetExhausted, true
	case sp.CapUSD > 0 && sp.SoftCapUSD < sp.CapUSD-budgetCapEps &&
		(sp.Exhausted || sp.SpentUSD+sp.ReservedUSD >= sp.SoftCapUSD-budgetCapEps):
		return budgetReserveOnly, true
	}
	return budgetOK, true
}
