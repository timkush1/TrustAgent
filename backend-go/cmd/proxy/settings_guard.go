package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// Changes that would block every answer on fail-closed routes (P4-PLAN D77). An enforcing route
// is a route of the policy in effect with fail: closed and mode block or fallback: when the fast
// check cannot answer, it blocks (or falls back on) every answer. Turning the decision model off,
// unbinding its credential or lowering its daily budget below today's spend therefore needs an
// explicit ?confirm=true, and while such a route has no usable decision model the status and the
// settings carry the fail_closed_unprotected warning.

// enforcingRoutes lists the enforcing fail-closed routes of the policy in effect, in policy order
// (the default route last).
func (s *server) enforcingRoutes() []string {
	if s.policies == nil || s.policies.Engine == nil {
		return nil
	}
	p := s.policies.Engine.Current()
	if p == nil {
		return nil
	}
	var out []string
	for _, r := range p.AllRoutes() {
		if r == nil {
			continue
		}
		if r.Fail == decision.FailClosed && (r.Mode == decision.ModeBlock || r.Mode == decision.ModeFallback) {
			out = append(out, r.Name)
		}
	}
	return out
}

// decisionSpentToday is today's real decision spend in USD: the cost ledger's figure when it is
// wired (D46), else the decision client's in-memory budget.
func (s *server) decisionSpentToday() float64 {
	if s.status == nil {
		return 0
	}
	if s.status.spend != nil {
		if v := s.status.spend(); v != nil {
			return v.DecisionSpentTodayUSD
		}
	}
	return s.status.budgets().DecisionSpentTodayUSD
}

// settingsConfirmation says why changing the settings in effect from before to after needs
// ?confirm=true (D77): the message and the enforcing routes it affects, or "" when the change
// needs no confirmation.
func (s *server) settingsConfirmation(before, after runtimecfg.RuntimeSettings) (string, []string) {
	var reasons []string
	if after.DecisionModel.Backend == jev.BackendOff && before.DecisionModel.Backend != jev.BackendOff {
		reasons = append(reasons, "turning the fast check off")
	}
	if after.Bindings.DecisionModel == nil && before.Bindings.DecisionModel != nil {
		reasons = append(reasons, "disconnecting the fast check's key")
	}
	if spent := s.decisionSpentToday(); after.Budgets.DecisionUSDDay > 0 && after.Budgets.DecisionUSDDay < spent &&
		(before.Budgets.DecisionUSDDay == 0 || after.Budgets.DecisionUSDDay < before.Budgets.DecisionUSDDay) {
		reasons = append(reasons, fmt.Sprintf("a fast-check budget below today's spend ($%.2f)", spent))
	}
	if len(reasons) == 0 {
		return "", nil
	}
	routes := s.enforcingRoutes()
	if len(routes) == 0 {
		return "", nil
	}
	return "This change stops the fast check (" + strings.Join(reasons, ", ") + "); these routes block on failure, " +
		"so every answer on them would be blocked. Send it again with ?confirm=true to go ahead.", routes
}

// unusableDecisionStates are the decision-model states in which it cannot answer until someone
// changes the configuration, the key or the budget (transient states such as unreachable or an
// open breaker recover on their own).
var unusableDecisionStates = []string{"off", "unauthorized", "no_credits", "misconfigured", "budget_exhausted"}

// failClosedWarning is the fail_closed_unprotected warning (D77) when an enforcing fail-closed
// route has no usable decision model, else nil.
func (s *server) failClosedWarning() *runtimecfg.Warning {
	if s.status == nil {
		return nil
	}
	if !slices.Contains(unusableDecisionStates, s.status.decisionModel().State) {
		return nil
	}
	routes := s.enforcingRoutes()
	if len(routes) == 0 {
		return nil
	}
	return &runtimecfg.Warning{
		Code: runtimecfg.WarnFailClosedUnprotected,
		Message: "No fast check can answer, and these routes block on failure, so every answer on them is blocked: " +
			strings.Join(routes, ", ") + ".",
		Details: map[string]any{"routes": routes},
	}
}
