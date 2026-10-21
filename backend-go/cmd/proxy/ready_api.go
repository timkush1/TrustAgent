package main

import (
	"context"
	"net/http"
	"sync"
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

// readyProbeTTL is how long one database probe answers readiness checks. The endpoint is public
// and unauthenticated, so however often it is called it costs the database at most one probe per
// interval, and concurrent checks share the probe in flight.
const readyProbeTTL = time.Second

// readyStatus is api.ts ReadyStatus (GET /health/ready, SPEC §2.4 endpoint 2): the overall state
// and every component's state as a ComponentStateName.
type readyStatus struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

// readyAPI serves GET /health/ready (public, SPEC §2.4 endpoint 2). It reads the same cached
// component state as GET /api/status (statusReporter), plus the database probe it shares across
// checks for readyProbeTTL, and never runs the status aggregates: the endpoint is unauthenticated.
type readyAPI struct {
	s   *server
	now func() time.Time

	// mu serializes the database probe; probedAt and database are the last probe's time and state.
	mu       sync.Mutex
	probedAt time.Time
	database string
}

// handle serves GET /health/ready. strict=1 answers 503 unless every component is ready.
func (a *readyAPI) handle(c *gin.Context) {
	strict := false
	switch c.Query("strict") {
	case "", "0", "false":
	case "1", "true":
		strict = true
	default:
		badParam(c, "strict", "must be 1 or 0")
		return
	}
	r := a.readiness()
	code := http.StatusOK
	if strict && r.Status != readyReady {
		code = http.StatusServiceUnavailable
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(code, r)
}

// readiness maps the status reporter's view of each dependency onto ReadyStatus.
func (a *readyAPI) readiness() readyStatus {
	st := a.s.status
	eng := st.engineHealth()
	dm := st.decisionModel().State
	components := map[string]string{
		"proxy":          stateUp,
		"policy":         policyComponent(st.policyStatus()),
		"database":       a.databaseState(),
		"engine":         eng.State,
		"judge":          judgeState(eng),
		"decision_model": decisionModelComponent(dm),
		"knowledge":      knowledgeStatus(eng).State,
		"redis":          st.redisState,
	}
	return readyStatus{Status: overallReadiness(components, dm), Components: components}
}

// databaseState is the persistence state GET /api/status reports, probed at most once per
// readyProbeTTL. The probe runs on its own deadline, so a caller that hangs up cannot leave a
// cancelled ping cached as a database that is down.
func (a *readyAPI) databaseState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.database != "" && a.now().Sub(a.probedAt) < readyProbeTTL {
		return a.database
	}
	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
	defer cancel()
	a.database, a.probedAt = a.s.status.persistence(ctx).State, a.now()
	return a.database
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
