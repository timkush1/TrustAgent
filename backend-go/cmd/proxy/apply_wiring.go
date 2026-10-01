package main

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
)

// applyParts apply runtime configuration changes (P4, GCONN): the decision-model swap, the
// upstream rebuild and the engine push, all driven by runtimecfg.Manager.Subscribe.
type applyParts struct{}

// initApply wires the hot apply after the gateway is built. Step-0 stub: nothing changes at run
// time.
func (s *server) initApply(d serverDeps) {
	_ = d
	s.apply = &applyParts{}
}

// mountConnectAPI mounts the credential, Connect OpenRouter, decision-model test and admin audit
// log endpoints (SPEC §2.4 rows 54-60 and 70). Step-0 stub: it registers nothing yet.
func (s *server) mountConnectAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}

// startApplyLoops runs the engine push loop until ctx ends. Step-0 stub: nothing to run.
func (s *server) startApplyLoops(ctx context.Context, wg *sync.WaitGroup) {
	_, _ = ctx, wg
}
