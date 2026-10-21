package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// gatewayParts are the /v1 components main wires into the router.
type gatewayParts struct {
	handler *proxy.Handler
	auth    *authz.Authenticator
	// client is the decision-model client; jev is its resolved backend, as built.
	client jev.Client
	jev    jev.Info
}

// close stops the decision-model client's background work (keep-warm, idle connections).
func (g *gatewayParts) close() {
	if c, ok := g.client.(io.Closer); ok {
		_ = c.Close()
	}
}

// probe sends the decision model's connectivity probe once (TRUTHTABLE_JEV_STARTUP_PROBE), so a
// rejected key, a missing model or an unreachable endpoint shows in /api/status before the first
// gated answer. The mock and off backends make no vendor call and are not probed.
func (g *gatewayParts) probe(ctx context.Context, timeout time.Duration, log *slog.Logger) {
	switch g.jev.Backend {
	case jev.BackendMock, jev.BackendOff:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := jev.Probe(ctx, g.client)
	if err != nil {
		var e *jev.Error
		hint := ""
		if errors.As(err, &e) {
			hint = e.Hint
		}
		log.Warn("decision model startup probe failed", "backend", g.jev.Backend, "kind", jev.ErrorKind(err), "hint", hint, "err", err)
		return
	}
	log.Info("decision model startup probe ok", "backend", g.jev.Backend, "snapshot", resp.Snapshot, "latency_ms", resp.LatencyMs)
}

// gatewayHooks are the server's callbacks into the gateway's decision-model client, and the
// calibration lookup the gate reads.
type gatewayHooks struct {
	// snapshotChanged receives a new Jev snapshot for a model seen before (decision_model_changed).
	snapshotChanged func(jev.SnapshotChange)
	// calibrations looks up evaluator calibrations per snapshot; nil means none.
	calibrations policy.CalibrationLookup
	// runtime is the runtime configuration; nil in tests that build a gateway directly.
	runtime *runtimecfg.Manager
	// costs are the cost ledger's hooks (zero: no ledger, today's behaviour).
	costs costHooks
	// retriever fetches knowledge-base context for the gate (D65); nil means none.
	retriever proxy.Retriever
	// redact masks secrets in relayed upstream error bodies (D42); nil is the identity.
	redact func(string) string
}

// jevHTTPConfig maps the decision-model settings onto the HTTP backends' configuration, starting
// from jev.DefaultHTTPConfig so that an unset field keeps its SPEC default rather than zero.
func jevHTTPConfig(cfg *config.Config, log *slog.Logger) *jev.HTTPConfig {
	h := jev.DefaultHTTPConfig()
	j := cfg.Jev
	h.AttemptTimeout, h.MaxRetries, h.MaxRetriesAsync, h.RetryAfterCap = j.AttemptTimeout, j.MaxRetries, j.MaxRetriesAsync, j.RetryAfterCap
	h.RPM, h.SyncReserve, h.MaxInFlight = j.RPM, j.SyncReserve, j.MaxInFlight
	h.BreakerFailures, h.BreakerCooldown, h.AuthCooldown = j.BreakerFailures, j.BreakerCooldown, j.AuthCooldown
	h.PricePerMTok, h.CharsPerToken = j.PricePerMTok, j.CharsPerToken
	h.AppURL, h.AppTitle, h.PublicURL = j.AppURL, j.AppTitle, cfg.PublicURL
	h.ZDR, h.SendSessionID, h.KeepAlive, h.LogPayloads = j.ZDR, j.SendSessionID, j.KeepAlive, j.LogPayloads
	h.SpendCapUSDDay, h.EnforcingReserve, h.PerKeyCapUSDDay = cfg.SpendCapDecisionUSDDay, j.SpendEnforcingReserve, j.KeyUSDDay
	// X-TrustAgent-Scenario reaches the vendor (mock-upstream) only in test mode.
	h.ForwardScenario = cfg.TestMode
	if j.LogPayloads {
		log.Warn("TRUTHTABLE_JEV_LOG_PAYLOADS: decision-model requests and responses are logged at debug level (never keys)")
	}
	return &h
}

// hasSyncRoute reports whether any route gates answers in flight (flag, block or fallback): only
// then is keeping the vendor connection warm worth a request a minute.
func hasSyncRoute(p *policy.Policy) bool {
	routes := append([]*policy.Route{p.Default}, p.Routes...)
	return slices.ContainsFunc(routes, func(r *policy.Route) bool { return r != nil && r.Mode != decision.ModeObserve })
}

// newGateway builds the gate hot path: the decision-model client, the gate and the /v1 handler,
// behind the ingest auth of auth (newAuthenticator). The policy and the evaluator registry come
// from ps, the policy services: the handler reads ps.Engine.Current() per request, so a saved or
// hot-reloaded policy applies without a restart. The sink persists and broadcasts traces and
// decisions and submits deep audits.
func newGateway(cfg *config.Config, sink gate.Sink, auth *authz.Authenticator, ps *policyServices, hooks gatewayHooks, log *slog.Logger) (*gatewayParts, error) {
	for _, w := range cfg.Warnings {
		log.Warn("config: " + w)
	}

	registry, pol := ps.Registry, ps.Engine.Current()
	if pol.Source == policy.SourceBuiltin {
		log.Warn("policy file not found: gating every request on the built-in defaults", "path", cfg.PolicyFile, "mode", pol.Default.Mode)
	} else {
		log.Info("policy loaded", "path", pol.Path, "version", pol.Version, "source", pol.Source, "routes", len(pol.Routes), "default_mode", pol.Default.Mode)
	}

	jevClient, err := newDecisionClient(cfg, hooks, log)
	if err != nil {
		return nil, err
	}
	info := jevClient.Info()
	switch {
	case info.Misconfigured:
		log.Error("decision model misconfigured: fast checks are off", "backend", info.Backend, "reason", info.Reason)
	case info.Backend == jev.BackendOff:
		log.Warn("decision model off: answers pass unchecked (skipped:decision_model_off)", "reason", info.Reason)
	default:
		log.Info("decision model", "backend", info.Backend, "model", info.Model, "endpoint", info.Endpoint,
			"credential", info.CredentialSource, "reason", info.Reason)
	}
	for _, w := range info.Warnings {
		log.Warn("decision model: " + w)
	}
	if kw, ok := jevClient.(jev.KeepWarmer); ok {
		kw.SetKeepWarm(hasSyncRoute(pol))
	}

	// Late decisions are recorded through the sink (never enforced).
	var onLate func(context.Context, *gate.Result)
	if sink != nil {
		onLate = sink.LateDecided
	}
	g, err := gate.New(gate.Config{
		Jev: jevClient, Evaluators: registry, Calibrations: hooks.calibrations, OnLate: onLate,
		Budget: decision.Budget{
			Tokens: cfg.Jev.TokenBudget, CPT: cfg.Jev.CharsPerToken, MaxQuestions: cfg.Jev.MaxQuestionsPerCall,
			Overhead: decision.Overhead, MaxResponseTokens: cfg.Jev.MaxResponseTokens, EvidenceMaxTokens: 8000,
		},
		AttemptTimeout: cfg.Jev.AttemptTimeout, AsyncDeadline: cfg.Jev.AsyncDeadline,
		UniformAuditsPerDay: cfg.UniformAuditsPerDay, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	// The uniform sample's daily count survives restarts (the cost ledger seeds it; 0 without).
	g.SeedUniform(hooks.costs.uniformSeed)

	ups, err := newUpstreams(cfg, auth, hooks.runtime, log)
	if err != nil {
		return nil, err
	}
	if cfg.TestMode {
		log.Warn("TRUTHTABLE_TEST_MODE: test_response and X-TrustAgent-Scenario are honoured")
	}

	h := proxy.New(proxy.Options{
		Upstreams: ups.Current, Policy: ps.Engine.Current, Gate: g, Sink: sink, TestMode: cfg.TestMode,
		MaxUpstreamResponseBytes: cfg.MaxUpstreamResponseBytes, GateMaxConcurrency: cfg.GateMaxConcurrency, Logger: log,
		Knowledge: hooks.retriever, Redact: hooks.redact,
	})
	return &gatewayParts{handler: h, auth: auth, client: jevClient, jev: info}, nil
}
