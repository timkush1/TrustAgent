package main

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
)

// CostLink ties LLM cost records to what they were spent on (P4-PLAN D45): the trace, the audit
// (LLM rows link by audit_id and are stamped at completion), the route, the caller's key label,
// the audit selection and the attempt.
type CostLink struct {
	TraceID   string
	AuditID   string
	Route     string
	KeyLabel  string
	Selection string
	Attempt   int
}

// initCosts builds the cost ledger and fills s.costs (P4, GCOST). Step-0 stub: zero hooks, so
// every cost path keeps today's behaviour.
func (s *server) initCosts(d serverDeps) {
	_ = d
	s.costs = costHooks{}
}

// mountCostAPI mounts the ROI time series (SPEC §2.4 row 48). Step-0 stub: it registers nothing
// yet.
func (s *server) mountCostAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}

// startCostLoops runs the ledger's flush and retention loops until ctx ends. Step-0 stub: nothing
// to run.
func (s *server) startCostLoops(ctx context.Context, wg *sync.WaitGroup) {
	_, _ = ctx, wg
}

// stopCosts flushes the ledger at shutdown, after the audits drained. Step-0 stub: nothing to
// flush.
func (s *server) stopCosts(ctx context.Context) error {
	_ = ctx
	return nil
}
