package main

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/store"
)

// evalParts are the evaluator-write, label and calibration services (P4, GEVAL).
type evalParts struct{}

// initEval builds them after the policy services. Step-0 stub.
func (s *server) initEval(d serverDeps) {
	_ = d
	s.eval = &evalParts{}
}

// mountEvalAPI mounts evaluator validate and writes, labels and calibration (SPEC §2.4 rows 30-33
// and 39-46b). Step-0 stub: it registers nothing yet.
func (s *server) mountEvalAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}

// traceLabels are the labels of a trace for GET /api/traces/:id (TraceDetail.labels). Step-0
// stub: none.
func (s *server) traceLabels(ctx context.Context, traceID string) ([]store.LabelView, error) {
	_, _ = ctx, traceID
	return []store.LabelView{}, nil
}
