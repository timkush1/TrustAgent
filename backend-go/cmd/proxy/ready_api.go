package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/policy"
)

// Overall readiness (api.ts ReadyStatus.status).
const (
	readyReady    = "ready"
	readyDegraded = "degraded"
	readyNotReady = "not_ready"
)

// readyTimeout bounds the database ping of one readiness check.
const readyTimeout = 2 * time.Second

// readyStatus is api.ts ReadyStatus (GET /health/ready, SPEC §2.4 endpoint 2): the overall state
// and every component's state as a ComponentStateName.
type readyStatus struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

// handleReady serves GET /health/ready (public). It reads the same cached component state as
// GET /api/status (statusReporter), plus one database ping, and never runs the status aggregates:
// the endpoint is unauthenticated. strict=1 answers 503 unless every component is ready.
func (s *server) handleReady(c *gin.Context) {
	strict := false
	switch c.Query("strict") {
	case "", "0", "false":
	case "1", "true":
		strict = true
	default:
		badParam(c, "strict", "must be 1 or 0")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), readyTimeout)
	defer cancel()
	r := s.readiness(ctx)
	code := http.StatusOK
	if strict && r.Status != readyReady {
		code = http.StatusServiceUnavailable
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(code, r)
}

// readiness maps the status reporter's view of each dependency onto ReadyStatus.
func (s *server) readiness(ctx context.Context) readyStatus {
	st := s.status
	eng := st.engineHealth()
	dm := st.decisionModel().State
	components := map[string]string{
		"proxy":          stateUp,
		"policy":         policyComponent(st.policyStatus()),
		"database":       st.persistence(ctx).State,
		"engine":         eng.State,
		"judge":          judgeState(eng),
		"decision_model": decisionModelComponent(dm),
		"knowledge":      knowledgeStatus(eng).State,
		"redis":          st.redisState,
	}
	return readyStatus{Status: overallReadiness(components, dm), Components: components}
}

// policyComponent is up while the policy file agrees with the policy in effect (or was
// deliberately superseded from the dashboard), degraded when the file is missing or invalid: the
// gateway then runs on the last good version or the built-in policy.
func policyComponent(p statusPolicy) string {
	switch p.FileStatus {
	case policy.FileOK, policy.FileDiverged:
		return stateUp
	default:
		return stateDegraded
	}
}

// decisionModelComponent maps a DecisionModelStatus state onto a ComponentStateName.
func decisionModelComponent(state string) string {
	switch state {
	case "ready", "mock":
		return stateUp
	case "off":
		return stateDisabled
	case "degraded":
		return stateDegraded
	case "checking", "":
		return stateUnknown
	default: // breaker_open, unauthorized, no_credits, budget_exhausted, misconfigured, unreachable
		return stateDown
	}
}

// decisionModelBroken are the decision-model states that do not heal without an operator: a bad
// configuration or credential.
var decisionModelBroken = map[string]bool{"misconfigured": true, "unauthorized": true, "no_credits": true}

// overallReadiness is not_ready when the gateway cannot do what it is configured to do (a configured
// database is down, or the decision model's configuration or credential is refused), degraded
// when any other component is not up (down, degraded or not checked yet), and ready when every
// component is up or deliberately disabled.
func overallReadiness(components map[string]string, decisionModelState string) string {
	if components["database"] == stateDown || decisionModelBroken[decisionModelState] {
		return readyNotReady
	}
	for _, state := range components {
		if state != stateUp && state != stateDisabled {
			return readyDegraded
		}
	}
	return readyReady
}
