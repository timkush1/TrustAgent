package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/proxy"
	"github.com/truthtable/backend-go/internal/upstream"
)

// gatewayParts are the /v1 components main wires into the router.
type gatewayParts struct {
	handler *proxy.Handler
	auth    *authz.Authenticator
	policy  *policy.Policy
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

// gatewayHooks are the server's callbacks into the gateway's decision-model client.
type gatewayHooks struct {
	// snapshotChanged receives a new Jev snapshot for a model seen before (decision_model_changed).
	snapshotChanged func(jev.SnapshotChange)
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

// newGateway builds the gate hot path: ingest auth, the policy, the decision-model client, the
// gate and the /v1 handler. The sink persists and broadcasts traces and decisions and submits
// deep audits.
func newGateway(cfg *config.Config, sink gate.Sink, hooks gatewayHooks, log *slog.Logger) (*gatewayParts, error) {
	for _, w := range cfg.Warnings {
		log.Warn("config: " + w)
	}

	keys := make([]authz.Key, 0, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		keys = append(keys, authz.Key{Label: k.Label, Secret: k.Key, Scopes: authz.APIKeyScopes})
	}
	auth := authz.New(keys, cfg.IngestAuth)
	switch {
	case auth.IngestMode() == authz.IngestOpen:
		log.Warn("TRUTHTABLE_INGEST_AUTH=open: /v1 accepts requests without an ingest key")
	case !auth.HasIngestKeys():
		log.Warn("no ingest keys: /v1 answers 401 until TRUTHTABLE_API_KEYS (label=key) is set, or TRUTHTABLE_INGEST_AUTH=open")
	default:
		log.Info("/v1 ingest auth: X-TrustAgent-Key", "keys", len(keys))
	}

	registry, err := evaluators.Builtins()
	if err != nil {
		return nil, fmt.Errorf("evaluator templates: %w", err)
	}
	pol, err := policy.LoadFile(cfg.PolicyFile, policy.Seeds{
		Mode: cfg.GateDefaultMode, Fail: cfg.GateFailMode, MaxResponseTokens: cfg.Jev.MaxResponseTokens,
	}, registry)
	if err != nil {
		return nil, err
	}
	if pol.Source == policy.SourceBuiltin {
		log.Warn("policy file not found: gating every request on the built-in defaults", "path", cfg.PolicyFile, "mode", pol.Default.Mode)
	} else {
		log.Info("policy loaded", "path", pol.Path, "routes", len(pol.Routes), "default_mode", pol.Default.Mode)
	}

	// Auto resolution (SPEC §3.1): demo → mock; base URL + TRUTHTABLE_JEV_API_KEY → compatible; an
	// OpenRouter key → openrouter-decisions; a TypeSafe key → typesafe; otherwise off, which every
	// gated answer reports honestly as skipped:decision_model_off.
	jevClient, err := jev.New(jev.Config{
		Backend: cfg.Jev.Backend, Model: cfg.Jev.Model, DemoMode: cfg.DemoMode,
		OpenRouterKey: cfg.Jev.OpenRouterKey, TypeSafeKey: cfg.Jev.TypeSafeKey, JevAPIKey: cfg.Jev.APIKey,
		BaseURL: cfg.Jev.BaseURL, Path: cfg.Jev.Path, OpenRouterBaseURL: cfg.Jev.OpenRouterBaseURL,
		Mock: jev.MockConfig{
			Mode: cfg.Jev.MockMode, Strict: cfg.Jev.MockStrict, FixturesPath: cfg.Jev.MockFixtures,
			Seed: cfg.Jev.MockSeed, Latency: cfg.Jev.MockLatency,
		},
		HTTP: jevHTTPConfig(cfg, log),
		OnSnapshotChange: func(c jev.SnapshotChange) {
			log.Info("decision model snapshot", "backend", c.Backend, "model", c.Model, "previous", c.Previous, "snapshot", c.Snapshot)
			// The first sighting has no previous snapshot: nothing changed for users.
			if c.Previous != "" && hooks.snapshotChanged != nil {
				hooks.snapshotChanged(c)
			}
		},
		Logger: log,
	})
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

	g, err := gate.New(gate.Config{
		Jev: jevClient, Evaluators: registry,
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

	up, err := upstream.New(upstream.Config{
		BaseURL: cfg.UpstreamURL, HeaderTimeout: cfg.UpstreamHeaderTimeout, Timeout: cfg.UpstreamTimeout,
	}, auth.IsProxyKey)
	if err != nil {
		return nil, err
	}
	log.Info("upstream (passthrough credentials)", "url", redactURL(up.Base().String()))
	if cfg.TestMode {
		log.Warn("TRUTHTABLE_TEST_MODE: test_response and X-TrustAgent-Scenario are honoured")
	}

	h := proxy.New(proxy.Options{
		Upstream: up, Policy: pol, Gate: g, Sink: sink, TestMode: cfg.TestMode,
		MaxUpstreamResponseBytes: cfg.MaxUpstreamResponseBytes, GateMaxConcurrency: cfg.GateMaxConcurrency, Logger: log,
	})
	return &gatewayParts{handler: h, auth: auth, policy: pol, client: jevClient, jev: info}, nil
}
