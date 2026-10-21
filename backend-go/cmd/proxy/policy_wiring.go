package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

// Wiring of the policy engine, the evaluator registry and their REST APIs (newServer): the /v1
// handler and the gate read ps.Engine.Current() and ps.Registry per request, so a saved, rolled
// back or hot-reloaded version reaches the gate without a restart.

// policyWiring are the inputs of the policy services.
type policyWiring struct {
	cfg *config.Config
	// pg is the database; nil runs file-managed (no versions, no dry-run).
	pg *store.PostgresStore
	// hub receives policy_updated, policy_error and evaluator_updated events; may be nil.
	hub *websocket.Hub
	// runtime reports the decision-model state for effective routes and readiness; may be nil.
	runtime func() policy.Runtime
	// enforcing accepts block and fallback modes. Keep it false until the /v1 handler enforces
	// them (P3 handler pipeline).
	enforcing bool
	// onPolicy, when set, receives each newly active policy after a save, rollback, adoption or
	// reload (not the one active at startup), for state derived from it such as the decision
	// model's keep-warm. It runs on the goroutine that activated the version.
	onPolicy func(*policy.Policy)
	// channel overrides the NOTIFY channel (tests).
	channel string
	// notify receives every config notification that is not a policy version (D84); the
	// evaluator reload subscribes to it. nil uses a hub of its own.
	notify *notifyHub
	log    *slog.Logger
}

// policyServices are the policy engine, the evaluator registry and their REST APIs.
//
// newServer builds them before the gateway (which reads ps.Registry and ps.Engine.Current), mounts
// their routes on /api, and server.start runs the reload loop until shutdown.
type policyServices struct {
	Registry *evaluators.Registry
	Engine   *policy.Engine
	policies *policiesAPI
	evals    *evaluatorsAPI
	hub      *websocket.Hub
	// reload is the poll interval of the evaluator tables (TRUTHTABLE_POLICY_RELOAD_INTERVAL).
	reload time.Duration
	// evalTrigger asks run to reload the evaluators (an evaluator notification arrived).
	evalTrigger chan struct{}
	// reloadMu serializes evaluator reloads (the poll, notifications and the writes' synchronous
	// reload), so each change is announced exactly once.
	reloadMu sync.Mutex
	onPolicy func(*policy.Policy)
	log      *slog.Logger
}

// newPolicyServices loads the evaluator registry (built-ins, files, database) and the policy
// engine, and builds their REST APIs.
func newPolicyServices(ctx context.Context, w policyWiring) (*policyServices, error) {
	log := w.log
	if log == nil {
		log = slog.Default()
	}
	ps := &policyServices{hub: w.hub, reload: w.cfg.PolicyReloadInterval, evalTrigger: make(chan struct{}, 1), onPolicy: w.onPolicy, log: log}

	regCfg := evaluators.Config{Dir: w.cfg.EvaluatorsDir, Channel: w.channel, Logger: log}
	var versions policy.VersionStore
	var replay policy.ReplaySource
	var labels labelCounter
	if w.pg != nil {
		regCfg.Store, versions, replay, labels = w.pg, w.pg, w.pg, w.pg
	}
	reg, err := evaluators.New(ctx, regCfg)
	if err != nil {
		return nil, err
	}
	ps.Registry = reg

	// One LISTEN connection (the engine's) serves every config notification (D84): the engine
	// hands the payloads that are not policy versions to the hub.
	notify := w.notify
	if notify == nil {
		notify = newNotifyHub()
	}
	notify.Subscribe("evaluator", ps.notified)

	engine, err := policy.NewEngine(ctx, policy.EngineConfig{
		File: w.cfg.PolicyFile,
		Env: policy.Env{
			Seeds:     policy.Seeds{Mode: w.cfg.GateDefaultMode, Fail: w.cfg.GateFailMode, MaxResponseTokens: w.cfg.Jev.MaxResponseTokens},
			Specs:     reg,
			Enforcing: w.enforcing,
			// fallback.upstream: fallback needs an upstream to call.
			FallbackUpstream: w.cfg.FallbackUpstreamURL != "",
			// evidence.sources: knowledge needs the knowledge add-on (CO-9, GO-6).
			Knowledge: w.cfg.KnowledgeEnabled,
		},
		Store: versions, Lock: w.cfg.ConfigLock, ReloadInterval: w.cfg.PolicyReloadInterval, Channel: w.channel,
		Runtime: w.runtime, OnUpdate: ps.policyUpdated, OnError: ps.policyFailed, OnNotify: notify.Dispatch, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	ps.Engine = engine
	metrics.PolicyVersion.Set(float64(engine.State().Version.ID))
	st := engine.State()
	log.Info("policy active", "version", st.Version.ID, "source", st.Version.Source, "routes", len(st.Policy.Routes),
		"file", st.File.Path, "file_status", st.File.Status, "persisted", st.Persisted, "locked", st.Locked)

	if ps.policies, err = newPoliciesAPI(engine, replay, log); err != nil {
		return nil, err
	}
	ps.evals = newEvaluatorsAPI(reg, engine.Current, labels, w.runtime, log)
	ps.evals.includeMock = w.cfg.DemoMode
	if w.cfg.Jev.CharsPerToken > 0 {
		ps.evals.cpt = w.cfg.Jev.CharsPerToken
	}
	if w.pg != nil {
		ps.evals.writes = &evaluatorWrites{store: w.pg, channel: w.channel, reload: ps.reloadNow}
	}
	return ps, nil
}

// mount registers the policy and evaluator routes on the /api group.
func (ps *policyServices) mount(api *gin.RouterGroup, g apiGuards) {
	ps.policies.register(api, g)
	ps.evals.register(api, g)
}

// run hot-reloads the policy and the evaluators until ctx ends: notifications, which carry both,
// and the poll. It returns once nothing it started still uses the store, so the caller may close
// the store after it.
func (ps *policyServices) run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var tick <-chan time.Time
		if ps.reload > 0 {
			t := time.NewTicker(ps.reload)
			defer t.Stop()
			tick = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
			case <-ps.evalTrigger:
			}
			ps.reloadEvaluators(ctx)
		}
	}()
	ps.Engine.Run(ctx)
	wg.Wait()
}

// notified handles config notifications other than policy versions. It runs on the listener
// goroutine, so it only hands the reload to run.
func (ps *policyServices) notified(payload string) {
	if strings.HasPrefix(payload, "evaluator") {
		select {
		case ps.evalTrigger <- struct{}{}:
		default:
		}
	}
}

// reloadEvaluators re-reads the stored evaluators in the background (the poll and notifications).
func (ps *policyServices) reloadEvaluators(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ps.reloadNow(rctx); err != nil && ctx.Err() == nil { // not shutting down
		ps.log.Warn("evaluator reload failed", "err", err)
	}
}

// reloadNow re-reads the stored evaluators, announces new versions and archive changes
// (evaluator_updated, D61), and has the engine retry a policy version that named an evaluator
// this replica had not loaded yet. The evaluator writes call it before they reply.
func (ps *policyServices) reloadNow(ctx context.Context) error {
	ps.reloadMu.Lock()
	defer ps.reloadMu.Unlock()
	before := ps.Registry.Refs()
	archived := map[string]bool{}
	for _, e := range ps.Registry.Evaluators(true) {
		archived[e.ID] = e.Archived
	}
	changed, err := ps.Registry.Reload(ctx)
	if err != nil || !changed {
		return err
	}
	if ps.Engine != nil {
		ps.Engine.Revalidate()
	}
	if ps.hub == nil {
		return nil
	}
	known := map[string]bool{}
	for _, ref := range before {
		known[ref] = true
	}
	for _, ref := range ps.Registry.Refs() {
		if known[ref] {
			continue
		}
		id, v, err := decision.ParseRef(ref)
		if err != nil {
			continue
		}
		origin := ""
		if e, ok := ps.Registry.Evaluator(id); ok {
			origin = e.Origin
		}
		ps.publish("evaluator_updated", map[string]any{"id": id, "version": v, "origin": origin})
	}
	for _, e := range ps.Registry.Evaluators(true) {
		if was, ok := archived[e.ID]; ok && was != e.Archived {
			ps.publish("evaluator_updated", map[string]any{"id": e.ID, "version": e.LatestVersion, "origin": e.Origin})
		}
	}
	return nil
}

func (ps *policyServices) policyUpdated(u policy.Update) {
	metrics.PolicyVersion.Set(float64(u.Version))
	ps.publish("policy_updated", u)
	if ps.onPolicy != nil && ps.Engine != nil {
		ps.onPolicy(ps.Engine.Current())
	}
}

func (ps *policyServices) policyFailed(e policy.PolicyError) {
	metrics.PolicyReloadErrors.WithLabelValues(e.Source).Inc()
	ps.publish("policy_error", e)
}

func (ps *policyServices) publish(typ string, data any) {
	if ps.hub == nil {
		return
	}
	ev, err := websocket.NewEvent(typ, "", data, nil)
	if err != nil {
		ps.log.Warn("ws event", "type", typ, "err", err)
		return
	}
	ps.hub.Publish(ev)
}
