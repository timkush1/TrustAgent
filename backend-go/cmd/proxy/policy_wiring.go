package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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

// Wiring of the policy engine, the evaluator registry and their REST APIs. The lead connects it
// to the gateway after the wave-2 merge (see policyServices): the /v1 handler and the gate must
// read ps.Engine.Current() and ps.Registry instead of a policy loaded once at startup.

// policyEnv are the policy keys of contracts/config/keys.v1.json that config.Config does not
// load yet (the registry-driven loader of P2 replaces this).
type policyEnv struct {
	ReloadInterval time.Duration // TRUTHTABLE_POLICY_RELOAD_INTERVAL (5s; 0 disables polling)
	ConfigLock     bool          // TRUTHTABLE_CONFIG_LOCK (false)
	EvaluatorsDir  string        // TRUTHTABLE_EVALUATORS_DIR (config/evaluators)
	Warnings       []string
}

// loadPolicyEnv reads the policy keys with their registry defaults.
func loadPolicyEnv(getenv func(string) string) policyEnv {
	e := policyEnv{ReloadInterval: 5 * time.Second, EvaluatorsDir: "config/evaluators"}
	if raw := strings.TrimSpace(getenv("TRUTHTABLE_POLICY_RELOAD_INTERVAL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			e.Warnings = append(e.Warnings, fmt.Sprintf("TRUTHTABLE_POLICY_RELOAD_INTERVAL=%q is not a duration such as 5s; using 5s", raw))
		} else {
			e.ReloadInterval = d
		}
	}
	if raw := strings.TrimSpace(getenv("TRUTHTABLE_CONFIG_LOCK")); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			e.Warnings = append(e.Warnings, fmt.Sprintf("TRUTHTABLE_CONFIG_LOCK=%q is not true or false; using false", raw))
		}
		e.ConfigLock = b
	}
	if raw := strings.TrimSpace(getenv("TRUTHTABLE_EVALUATORS_DIR")); raw != "" {
		e.EvaluatorsDir = raw
	}
	e.EvaluatorsDir = config.RepoPath(e.EvaluatorsDir)
	return e
}

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
	// channel overrides the NOTIFY channel (tests); getenv overrides os.Getenv (tests).
	channel string
	getenv  func(string) string
	log     *slog.Logger
}

// policyServices are the policy engine, the evaluator registry and their REST APIs.
//
// Wiring for the lead (cmd/proxy), in order:
//
//	ps, err := newPolicyServices(ctx, policyWiring{cfg: cfg, pg: pg, hub: s.hub, runtime: rt, log: log})
//	// newGateway: use ps.Registry instead of evaluators.Builtins(), and give the /v1 handler
//	// ps.Engine.Current (per request) instead of the policy.LoadFile result;
//	// statusReporter.policyStatus: return ps.Engine.Status().
//	ps.mount(api, apiGuards{read: read, admin: s.auth.require(authz.ScopeAdmin), writable: writable,
//	    rateLimit: rateLimit, dryRunLimit: middleware.RateLimit(limiter, 6, time.Minute), bodyLimit: bodyLimit})
//	go ps.run(ctx) // in server.start
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
	log         *slog.Logger
}

// newPolicyServices loads the evaluator registry (built-ins, files, database) and the policy
// engine, and builds their REST APIs.
func newPolicyServices(ctx context.Context, w policyWiring) (*policyServices, error) {
	log := w.log
	if log == nil {
		log = slog.Default()
	}
	getenv := w.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	penv := loadPolicyEnv(getenv)
	for _, warning := range penv.Warnings {
		log.Warn("config: " + warning)
	}
	ps := &policyServices{hub: w.hub, reload: penv.ReloadInterval, evalTrigger: make(chan struct{}, 1), log: log}

	regCfg := evaluators.Config{Dir: penv.EvaluatorsDir, Channel: w.channel, Logger: log}
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

	engine, err := policy.NewEngine(ctx, policy.EngineConfig{
		File: w.cfg.PolicyFile,
		Env: policy.Env{
			Seeds:     policy.Seeds{Mode: w.cfg.GateDefaultMode, Fail: w.cfg.GateFailMode, MaxResponseTokens: w.cfg.Jev.MaxResponseTokens},
			Specs:     reg,
			Enforcing: w.enforcing,
		},
		Store: versions, Lock: penv.ConfigLock, ReloadInterval: penv.ReloadInterval, Channel: w.channel,
		Runtime: w.runtime, OnUpdate: ps.policyUpdated, OnError: ps.policyFailed, OnNotify: ps.notified, Logger: log,
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

// reloadEvaluators re-reads the stored evaluators, announces new versions, and has the engine
// retry a policy version that named an evaluator this replica had not loaded yet.
func (ps *policyServices) reloadEvaluators(ctx context.Context) {
	before := ps.Registry.Refs()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	changed, err := ps.Registry.Reload(rctx)
	if err != nil {
		if ctx.Err() == nil { // not shutting down
			ps.log.Warn("evaluator reload failed", "err", err)
		}
		return
	}
	if !changed {
		return
	}
	if ps.Engine != nil {
		ps.Engine.Revalidate()
	}
	if ps.hub == nil {
		return
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
}

func (ps *policyServices) policyUpdated(u policy.Update) {
	metrics.PolicyVersion.Set(float64(u.Version))
	ps.publish("policy_updated", u)
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
