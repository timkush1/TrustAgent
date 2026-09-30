package main

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/proxy"
)

// decisionWaiters wakes the /v1 decision long-polls when a trace's decision is recorded
// (GateDecided, LateDecided). Empty for now: the long-poll lands in GD-8.
type decisionWaiters struct{}

func newDecisionWaiters() *decisionWaiters { return &decisionWaiters{} }

// handleDecisionLongPoll serves GET /v1/trustagent/decisions/:trace_id (SPEC §2.4 row 74). Until
// it lands (GD-8) it answers as an unknown /v1 path does.
func (s *server) handleDecisionLongPoll(c *gin.Context) {
	proxy.Deny(c, http.StatusNotFound, codeNotFound, "TrustAgent does not serve "+c.Request.Method+" "+c.Request.URL.Path+".")
	c.Abort()
}
