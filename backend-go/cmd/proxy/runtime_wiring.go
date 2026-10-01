package main

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// runtimeParts are the runtime configuration's components (P4, GSEC): the Manager every consumer
// subscribes to, and later the credential store and the settings APIs.
type runtimeParts struct {
	manager *runtimecfg.Manager
}

// initRuntime builds the runtime configuration. Step-0 stub: a static Manager over the env seed
// (no settings store, no credentials).
func (s *server) initRuntime(d serverDeps) {
	s.rt = &runtimeParts{manager: runtimecfg.NewStatic(d.cfg)}
}

// mountSecurityAPI mounts the settings endpoints (SPEC §2.4 rows 52 and 53). Step-0 stub: it
// registers nothing yet.
func (s *server) mountSecurityAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}

// startRuntimeLoops runs the runtime configuration's reload loop (NOTIFY through the hub, the
// poll) until ctx ends. Step-0 stub: nothing to run.
func (s *server) startRuntimeLoops(ctx context.Context, wg *sync.WaitGroup) {
	_, _ = ctx, wg
}
