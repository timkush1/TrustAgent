package main

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// applyParts apply runtime configuration changes (GCONN): the decision-model swap and the
// upstream rebuild follow runtimecfg.Manager.Subscribe from the gateway (decision_client.go,
// upstreams.go); the engine push lives here.
type applyParts struct {
	// pusher pushes the runtime configuration to the audit engine; nil without an engine.
	pusher *runtimecfg.Pusher
	// creds is the state of the credential and Connect OpenRouter endpoints.
	creds *credentialService

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
	s.apply = &applyParts{creds: newCredentialService(s)}
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
// log endpoints (SPEC §2.4 rows 54-60 and 70).
func (s *server) mountConnectAPI(api *gin.RouterGroup, g apiGuards) {
	// #54-#57 (credentials_api.go): the verify has its own limit, one check per key name every
	// 10 s, since each may cost a probe (D40).
	api.GET("/credentials", g.admin, g.rateLimit, s.handleListCredentials)
	api.PUT("/credentials/:name", g.writable, g.admin, g.rateLimit, g.bodyLimit, s.handlePutCredential)
	api.DELETE("/credentials/:name", g.writable, g.admin, g.rateLimit, s.handleDeleteCredential)
	api.POST("/credentials/:name/verify", g.writable, g.admin, s.apply.creds.verifyLimit, g.bodyLimit, s.handleVerifyCredential)
	// #58, #59 (connect_api.go)
	api.POST("/connect/openrouter/start", g.writable, g.admin, g.rateLimit, g.bodyLimit, s.handleConnectStart)
	api.POST("/connect/openrouter/complete", g.writable, g.admin, g.rateLimit, g.bodyLimit, s.handleConnectComplete)
	// #60: one probe per caller every 10 s (it costs ≈ $0.00001 each).
	api.POST("/decision-model/test", g.writable, g.admin, g.limitBy(1, decisionModelTestEvery), s.handleDecisionModelTest)
	// #70
	api.GET("/admin/audit-log", g.admin, g.rateLimit, s.handleAdminAuditLog)
}

// decisionModelTestEvery is the per-caller interval of POST /api/decision-model/test.
const decisionModelTestEvery = 10 * time.Second

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
