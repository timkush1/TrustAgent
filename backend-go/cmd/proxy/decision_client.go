package main

import (
	"log/slog"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
)

// newDecisionClient builds the decision-model client from the env (moved verbatim from
// newGateway at P4 Step 0), with the cost ledger's sink, budget, prices and known snapshot from
// hooks.costs: nil hooks keep the in-memory budget, the embedded price table and no cost events.
// GCONN extends it with the dashboard binding and the hot swap (D78).
//
// Auto resolution (SPEC §3.1): demo → mock; base URL + TRUTHTABLE_JEV_API_KEY → compatible; an
// OpenRouter key → openrouter-decisions; a TypeSafe key → typesafe; otherwise off, which every
// gated answer reports honestly as skipped:decision_model_off.
func newDecisionClient(cfg *config.Config, hooks gatewayHooks, log *slog.Logger) (jev.Client, error) {
	return jev.New(jev.Config{
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
		Costs: hooks.costs.decision, Budget: hooks.costs.budget, Prices: hooks.costs.prices,
		KnownSnapshot: hooks.costs.knownSnapshot,
		Logger:        log,
	})
}
