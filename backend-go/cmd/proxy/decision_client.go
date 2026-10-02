package main

import (
	"crypto/sha256"
	"log/slog"
	"strings"
	"sync"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// newDecisionClient builds the decision-model client the gateway gates with: a jev.Swappable over
// the client the runtime configuration calls for (P4-PLAN CN-1). When the configuration changes
// (the decision_model settings, or the credential bound to the decision model), a new client is
// built and swapped in without a restart; the replaced one finishes its running calls and is
// closed after jev.SwapGrace. Every rebuild reuses the cost ledger's sink, budget and prices
// (hooks.costs), so the day's spend carries on, and the last snapshot seen, so a changed snapshot
// is still announced. A replaced key alone keeps the client and closes its breaker.
//
// Auto resolution (SPEC §3.1): demo → mock; base URL + TRUTHTABLE_JEV_API_KEY → compatible; an
// OpenRouter key → openrouter-decisions; a TypeSafe key → typesafe; a dashboard credential bound
// to the decision model; otherwise off, which every gated answer reports honestly as
// skipped:decision_model_off. An env credential always wins over a binding (D33).
func newDecisionClient(cfg *config.Config, hooks gatewayHooks, log *slog.Logger) (jev.Client, error) {
	d := newDecisionSource(cfg, hooks, log)
	var snap *runtimecfg.Snapshot
	if hooks.runtime != nil {
		snap = hooks.runtime.Current()
	}
	if err := d.start(snap); err != nil {
		return nil, err
	}
	if hooks.runtime != nil {
		hooks.runtime.Subscribe(d.apply)
	}
	return d.sw, nil
}

// decisionSource builds decision-model clients from runtime snapshots and keeps the one in effect
// in sw.
type decisionSource struct {
	cfg   *config.Config
	hooks gatewayHooks
	log   *slog.Logger
	// http is the HTTP backends' configuration, built once; budget is the daily decision budget
	// every client shares (the ledger's when there is one, else one in-memory budget).
	http   *jev.HTTPConfig
	budget jev.Budget
	// keyFunc yields the KeyFunc of a bound credential (the runtime Manager's).
	keyFunc func(name string) jev.KeyFunc
	sw      *jev.Swappable

	mu sync.Mutex // serializes apply
	// built is what the client in effect was built from; secret is the digest of its bound
	// credential's secret (zero without one).
	built  decisionShape
	secret [32]byte
}

// decisionShape is what a decision-model client is built from: a change rebuilds the client.
type decisionShape struct {
	backend, model, baseURL, path string
	// binding names the bound credential, bindingBackend the backend it serves and origins its
	// allowed origins.
	binding, bindingBackend, origins string
}

func newDecisionSource(cfg *config.Config, hooks gatewayHooks, log *slog.Logger) *decisionSource {
	h := jevHTTPConfig(cfg, log)
	budget := hooks.costs.budget
	if budget == nil && (h.SpendCapUSDDay > 0 || h.PerKeyCapUSDDay > 0) {
		// Without the ledger, one in-memory budget outlives every rebuild (a client would
		// otherwise start the day's spend over).
		budget = jev.NewMemoryBudget(jev.BudgetConfig{
			CapUSD: h.SpendCapUSDDay, EnforcingReserve: h.EnforcingReserve, PerKeyCapUSD: h.PerKeyCapUSDDay, Now: h.Now,
		})
	}
	d := &decisionSource{cfg: cfg, hooks: hooks, log: log, http: h, budget: budget}
	if hooks.runtime != nil {
		d.keyFunc = hooks.runtime.KeyFunc
	}
	return d
}

// start builds the first client from snap (nil: the env alone).
func (d *decisionSource) start(snap *runtimecfg.Snapshot) error {
	c, err := d.build(snap)
	if err != nil {
		return err
	}
	d.sw = jev.NewSwappable(c)
	d.built, d.secret = d.shape(snap)
	return nil
}

// apply follows a new runtime snapshot (Manager.Subscribe): a changed shape builds and swaps in a
// new client; a replaced secret alone keeps the client (its KeyFunc reads the new key on the next
// call) and closes the breaker at once, so a key the vendor rejected stops blocking checks.
func (d *decisionSource) apply(snap *runtimecfg.Snapshot) {
	shape, secret := d.shape(snap)
	d.mu.Lock()
	defer d.mu.Unlock()
	if shape == d.built {
		if secret != d.secret {
			d.secret = secret
			d.sw.ResetBreaker()
			d.log.Info("decision model credential replaced", "credential", shape.binding)
		}
		return
	}
	c, err := d.build(snap)
	if err != nil {
		// The client in effect stays; the next snapshot tries again.
		d.log.Error("decision model rebuild failed: the previous decision model stays in effect", "err", err)
		return
	}
	d.sw.Swap(c)
	d.built, d.secret = shape, secret
	logDecisionModel(d.log, "decision model changed", c.Info())
}

// build resolves and builds a client for snap.
func (d *decisionSource) build(snap *runtimecfg.Snapshot) (jev.Client, error) {
	cfg := d.cfg
	jc := jev.Config{
		Backend: cfg.Jev.Backend, Model: cfg.Jev.Model, DemoMode: cfg.DemoMode,
		OpenRouterKey: cfg.Jev.OpenRouterKey, TypeSafeKey: cfg.Jev.TypeSafeKey, JevAPIKey: cfg.Jev.APIKey,
		BaseURL: cfg.Jev.BaseURL, Path: cfg.Jev.Path, OpenRouterBaseURL: cfg.Jev.OpenRouterBaseURL,
		Mock: jev.MockConfig{
			Mode: cfg.Jev.MockMode, Strict: cfg.Jev.MockStrict, FixturesPath: cfg.Jev.MockFixtures,
			Seed: cfg.Jev.MockSeed, Latency: cfg.Jev.MockLatency,
		},
		HTTP:             d.http,
		OnSnapshotChange: d.snapshotChanged,
		Costs:            d.hooks.costs.decision, Budget: d.budget, Prices: d.hooks.costs.prices,
		KnownSnapshot: d.hooks.costs.knownSnapshot,
		Logger:        d.log,
	}
	if d.sw != nil {
		// The last snapshot seen carries over: a rebuild that lands on another snapshot is
		// announced (decision_model_changed), one on the same snapshot is not.
		if st := d.sw.Status(); st.Snapshot != "" {
			jc.KnownSnapshot = st.Snapshot
		}
	}
	if snap != nil {
		dm := snap.Settings.DecisionModel
		jc.Backend, jc.Model, jc.BaseURL, jc.Path = dm.Backend, strval(dm.Model), strval(dm.BaseURL), strval(dm.Path)
		jc.Binding = d.binding(snap, jc.Backend)
	}
	return jev.New(jc)
}

// snapshotChanged logs every snapshot sighting and announces a real change (one with a previous
// snapshot) through hooks.snapshotChanged.
func (d *decisionSource) snapshotChanged(c jev.SnapshotChange) {
	d.log.Info("decision model snapshot", "backend", c.Backend, "model", c.Model, "previous", c.Previous, "snapshot", c.Snapshot)
	// The first sighting has no previous snapshot: nothing changed for users.
	if c.Previous != "" && d.hooks.snapshotChanged != nil {
		d.hooks.snapshotChanged(c)
	}
}

// binding is the dashboard credential bound to the decision model, when the snapshot holds it
// with a usable secret and it can serve a decision backend; nil otherwise (auto then resolves to
// an env credential, or off with the reason why).
func (d *decisionSource) binding(snap *runtimecfg.Snapshot, backend string) *jev.Binding {
	name := strval(snap.Settings.Bindings.DecisionModel)
	if name == "" || d.keyFunc == nil {
		return nil
	}
	cred, ok := snap.Credentials[name]
	if !ok || cred.Secret == "" {
		return nil
	}
	b := &jev.Binding{Backend: bindingBackend(name, backend), Name: name, Key: d.keyFunc(name)}
	if b.Backend == "" {
		return nil
	}
	if b.Backend == jev.BackendCompatible && len(cred.AllowedOrigins) > 0 {
		b.BaseURL = cred.AllowedOrigins[0]
	}
	return b
}

// bindingBackend is the decision backend a bound credential serves (D29 names): OpenRouter's
// Decisions endpoint (or SystemOne when that backend is chosen), TypeSafe, or the compatible
// gateway; "" for a credential that cannot serve the decision model.
func bindingBackend(name, backend string) string {
	switch name {
	case "openrouter":
		if backend == jev.BackendOpenRouterSystemOne {
			return backend
		}
		return jev.BackendOpenRouterDecisions
	case "typesafe":
		return jev.BackendTypeSafe
	case "compatible":
		return jev.BackendCompatible
	}
	return ""
}

// shape is what snap builds, and the digest of the bound credential's secret.
func (d *decisionSource) shape(snap *runtimecfg.Snapshot) (decisionShape, [32]byte) {
	cfg := d.cfg
	s := decisionShape{backend: cfg.Jev.Backend, model: cfg.Jev.Model, baseURL: cfg.Jev.BaseURL, path: cfg.Jev.Path}
	if snap == nil {
		return s, [32]byte{}
	}
	dm := snap.Settings.DecisionModel
	s.backend, s.model, s.baseURL, s.path = dm.Backend, strval(dm.Model), strval(dm.BaseURL), strval(dm.Path)
	b := d.binding(snap, s.backend)
	if b == nil {
		return s, [32]byte{}
	}
	cred := snap.Credentials[b.Name]
	s.binding, s.bindingBackend, s.origins = b.Name, b.Backend, strings.Join(cred.AllowedOrigins, ",")
	return s, sha256.Sum256([]byte(cred.Secret))
}

// logDecisionModel logs the resolved decision model (never a key).
func logDecisionModel(log *slog.Logger, msg string, info jev.Info) {
	switch {
	case info.Misconfigured:
		log.Error(msg+": misconfigured, fast checks are off", "backend", info.Backend, "reason", info.Reason)
	case info.Backend == jev.BackendOff:
		log.Warn(msg+": off, answers pass unchecked (skipped:decision_model_off)", "reason", info.Reason)
	default:
		log.Info(msg, "backend", info.Backend, "model", info.Model, "endpoint", info.Endpoint,
			"credential", info.CredentialSource, "reason", info.Reason)
	}
}

// strval is *p, or "" for nil.
func strval(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
