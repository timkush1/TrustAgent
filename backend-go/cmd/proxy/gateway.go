package main

import (
	"fmt"
	"log/slog"

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
	jev     jev.Info
}

// newGateway builds the gate hot path: the policy, the decision-model client, the gate and the /v1
// handler, behind the ingest auth of auth (newAuthenticator). The sink persists and broadcasts
// traces and decisions and submits deep audits.
func newGateway(cfg *config.Config, sink gate.Sink, auth *authz.Authenticator, log *slog.Logger) (*gatewayParts, error) {
	for _, w := range cfg.Warnings {
		log.Warn("config: " + w)
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

	jevClient, err := jev.New(jev.Config{
		Backend: cfg.Jev.Backend, Model: cfg.Jev.Model, DemoMode: cfg.DemoMode,
		OpenRouterKey: cfg.Jev.OpenRouterKey, TypeSafeKey: cfg.Jev.TypeSafeKey, JevAPIKey: cfg.Jev.APIKey, BaseURL: cfg.Jev.BaseURL,
		Mock: jev.MockConfig{
			Mode: cfg.Jev.MockMode, Strict: cfg.Jev.MockStrict, FixturesPath: cfg.Jev.MockFixtures,
			Seed: cfg.Jev.MockSeed, Latency: cfg.Jev.MockLatency,
		},
	})
	if err != nil {
		return nil, err
	}
	info := jevClient.Info()
	log.Info("decision model", "backend", info.Backend, "reason", info.Reason)

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
	log.Info("upstream (passthrough credentials)", "url", up.Base())
	if cfg.TestMode {
		log.Warn("TRUTHTABLE_TEST_MODE: test_response and X-TrustAgent-Scenario are honoured")
	}

	h := proxy.New(proxy.Options{
		Upstream: up, Policy: pol, Gate: g, Sink: sink, TestMode: cfg.TestMode,
		MaxUpstreamResponseBytes: cfg.MaxUpstreamResponseBytes, GateMaxConcurrency: cfg.GateMaxConcurrency, Logger: log,
	})
	return &gatewayParts{handler: h, auth: auth, policy: pol, jev: info}, nil
}
