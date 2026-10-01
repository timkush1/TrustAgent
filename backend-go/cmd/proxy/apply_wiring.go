package main

import (
	"context"
	"slices"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// applyParts apply runtime configuration changes (P4, GCONN): the decision-model swap and the
// upstream rebuild follow runtimecfg.Manager.Subscribe from the gateway (decision_client.go,
// upstreams.go); the engine push lives here.
type applyParts struct {
	// pusher pushes the runtime configuration to the audit engine; nil without an engine.
	pusher *runtimecfg.Pusher

	mu sync.Mutex
	// engineEnv is the last EngineInfo.env_managed handed to the Manager (engineEnvSeen once
	// there was one).
	engineEnv     []string
	engineEnvSeen bool
}

// initApply wires the hot apply after the gateway is built: the engine push follows every
// runtime snapshot and every engine health answer, and its state shows in /api/status.
func (s *server) initApply(d serverDeps) {
	_ = d
	s.apply = &applyParts{}
	if s.engine == nil || s.rt == nil || s.rt.manager == nil {
		return
	}
	p := runtimecfg.NewPusher(runtimecfg.PusherConfig{
		Engine: s.engine, Prices: s.costs.priceJSON, Redact: s.redact, Poll: s.cfg.PolicyReloadInterval, Logger: s.log,
	})
	s.apply.pusher = p
	s.rt.manager.Subscribe(p.Want)
	if s.pinger != nil {
		s.pinger.OnHealth(s.engineReported)
	}
	s.status.push = func() pushState {
		st := p.State()
		return pushState{Version: st.Version, Epoch: st.Epoch, Warning: st.Warning}
	}
}

// engineReported follows an engine health answer: a runtime configuration other than the one in
// effect is pushed again (an engine restart reports version 0), and the engine's env-managed keys
// (EngineInfo.env_managed, names only) reach the Manager when they change (D34, D80).
func (s *server) engineReported(h grpc.EngineHealth) {
	if r := h.Response; r != nil {
		s.apply.pusher.EngineReported(r.GetRuntimeConfigVersion(), r.GetRuntimeConfigEpoch())
	}
	if h.Info == nil {
		return
	}
	names := slices.Sorted(slices.Values(h.Info.GetEnvManaged()))
	s.apply.mu.Lock()
	changed := !s.apply.engineEnvSeen || !slices.Equal(names, s.apply.engineEnv)
	if changed {
		s.apply.engineEnv, s.apply.engineEnvSeen = names, true
	}
	s.apply.mu.Unlock()
	if changed {
		s.rt.manager.SetEngineEnvManaged(names)
	}
}

// mountConnectAPI mounts the credential, Connect OpenRouter, decision-model test and admin audit
// log endpoints (SPEC §2.4 rows 54-60 and 70). Step-0 stub: it registers nothing yet.
func (s *server) mountConnectAPI(api *gin.RouterGroup, g apiGuards) {
	_, _ = api, g
}

// startApplyLoops runs the engine push until ctx ends, starting with the snapshot in effect.
func (s *server) startApplyLoops(ctx context.Context, wg *sync.WaitGroup) {
	p := s.apply.pusher
	if p == nil {
		return
	}
	p.Want(s.rt.manager.Current())
	wg.Add(1)
	go func() { defer wg.Done(); p.Run(ctx) }()
}
