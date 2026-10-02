package main

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/store"
)

// evalParts are the label and calibration services (P4, GEVAL). The evaluator writes live with
// the evaluator reads in the policy services (policy_wiring.go, evaluators_api.go).
type evalParts struct {
	labels *labelsAPI
}

// initEval builds them after the policy services.
func (s *server) initEval(d serverDeps) {
	_ = d
	labels := &labelsAPI{
		barrier: s.barrier, policy: s.policies.Engine.Current, registry: s.policies.Registry,
		retention: s.cfg.LabelSampleRetention, now: time.Now, log: s.log,
	}
	if s.pg != nil {
		labels.store = s.pg
	}
	s.eval = &evalParts{labels: labels}
}

// mountEvalAPI mounts labels and calibration (SPEC §2.4 rows 39-46b).
func (s *server) mountEvalAPI(api *gin.RouterGroup, g apiGuards) {
	s.eval.labels.register(api, g)
}

// traceLabels are the labels of a trace for GET /api/traces/:id (TraceDetail.labels); none
// without a database.
func (s *server) traceLabels(ctx context.Context, traceID string) ([]store.LabelView, error) {
	if s.pg == nil {
		return []store.LabelView{}, nil
	}
	return s.pg.TraceLabels(ctx, traceID)
}
