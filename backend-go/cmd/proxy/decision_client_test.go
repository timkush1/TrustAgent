package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// decisionRig is a decision source over a mock OpenRouter, with the snapshot its KeyFunc reads.
type decisionRig struct {
	t      *testing.T
	cfg    *config.Config
	mock   *httptest.Server
	origin string
	cur    atomic.Pointer[runtimecfg.Snapshot]
	d      *decisionSource

	mu      sync.Mutex
	changes []jev.SnapshotChange
}

func newDecisionRig(t *testing.T, mutate func(*config.Config)) *decisionRig {
	t.Helper()
	m, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	cfg := testConfig(t, envOpts{})
	cfg.Jev.Backend, cfg.Jev.Model, cfg.Jev.BaseURL, cfg.Jev.Path = "auto", "", "", ""
	cfg.Jev.OpenRouterKey, cfg.Jev.TypeSafeKey, cfg.Jev.APIKey = "", "", ""
	cfg.Jev.OpenRouterBaseURL, cfg.Jev.KeepAlive, cfg.Jev.MaxRetries = srv.URL, 0, 0
	if mutate != nil {
		mutate(cfg)
	}
	u, _ := url.Parse(srv.URL)
	r := &decisionRig{t: t, cfg: cfg, mock: srv, origin: jev.CanonicalOrigin(u)}
	hooks := gatewayHooks{snapshotChanged: func(c jev.SnapshotChange) {
		r.mu.Lock()
		r.changes = append(r.changes, c)
		r.mu.Unlock()
	}}
	r.d = newDecisionSource(cfg, hooks, slog.New(slog.DiscardHandler))
	// The Manager's KeyFunc contract (D78): the credential of the snapshot in effect, only for its
	// allowed origins.
	r.d.keyFunc = func(name string) jev.KeyFunc {
		return func(_ context.Context, origin string) (string, error) {
			c, ok := r.cur.Load().Credentials[name]
			if !ok || c.Secret == "" {
				return "", runtimecfg.ErrCredentialNotFound
			}
			if !slices.Contains(c.AllowedOrigins, origin) {
				return "", runtimecfg.ErrHostNotAllowed
			}
			return c.Secret, nil
		}
	}
	boot := runtimecfg.NewStatic(cfg).Current()
	r.cur.Store(boot)
	if err := r.d.start(boot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.d.sw.Close() })
	return r
}

// publish applies a snapshot derived from the one in effect, as Manager.Subscribe would.
func (r *decisionRig) publish(edit func(*runtimecfg.Snapshot)) {
	next := *r.cur.Load()
	next.Version++
	next.Credentials = runtimecfg.CredentialSet{}
	for k, v := range r.cur.Load().Credentials {
		next.Credentials[k] = v
	}
	edit(&next)
	r.cur.Store(&next)
	r.d.apply(&next)
}

// bindOpenRouter binds an openrouter dashboard credential holding secret.
func (r *decisionRig) bindOpenRouter(secret string) func(*runtimecfg.Snapshot) {
	return func(s *runtimecfg.Snapshot) {
		name := "openrouter"
		s.Settings.Bindings.DecisionModel = &name
		s.Credentials[name] = runtimecfg.Credential{
			Name: name, Provider: "openrouter", Source: "dashboard", Secret: secret, AllowedOrigins: []string{r.origin},
		}
	}
}

func (r *decisionRig) probe() (*jev.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return jev.Probe(ctx, r.d.sw)
}

// TestDecisionClientFollowsTheBinding: binding a dashboard credential to the decision model turns
// fast checks on without a restart, and unbinding it turns them off again.
func TestDecisionClientFollowsTheBinding(t *testing.T) {
	r := newDecisionRig(t, nil)
	if got := r.d.sw.Info().Backend; got != jev.BackendOff {
		t.Fatalf("boot backend = %q, want off (no credential anywhere)", got)
	}
	off := r.d.sw.Current()

	r.publish(r.bindOpenRouter("sk-or-v1-e2e-good-0123456789"))
	info := r.d.sw.Info()
	if info.Backend != jev.BackendOpenRouterDecisions || info.CredentialSource != "dashboard" {
		t.Fatalf("after the binding: backend %q, credential %q; want openrouter-decisions from the dashboard", info.Backend, info.CredentialSource)
	}
	if r.d.sw.Current() == off {
		t.Fatal("the binding did not swap in a new client")
	}
	if _, err := r.probe(); err != nil {
		t.Fatalf("probe through the bound credential: %v", err)
	}

	r.publish(func(s *runtimecfg.Snapshot) { s.Settings.Bindings.DecisionModel = nil })
	if got := r.d.sw.Info().Backend; got != jev.BackendOff {
		t.Fatalf("after unbinding: backend %q, want off", got)
	}
}

// TestDecisionClientEnvCredentialWins: an env OpenRouter key keeps precedence over a binding
// (D33); the snapshot change rebuilds nothing visible.
func TestDecisionClientEnvCredentialWins(t *testing.T) {
	r := newDecisionRig(t, func(c *config.Config) { c.Jev.OpenRouterKey = "sk-or-v1-e2e-env-0123456789" })
	r.publish(r.bindOpenRouter("sk-or-v1-invalid-0123456789"))
	if info := r.d.sw.Info(); info.CredentialSource != "env" {
		t.Fatalf("credential source = %q, want env", info.CredentialSource)
	}
	if _, err := r.probe(); err != nil {
		t.Fatalf("probe with the env key: %v (the rejected dashboard key must not be used)", err)
	}
}

// TestDecisionClientSecretChangeResetsTheBreaker: a replaced secret keeps the client (no
// rebuild), and its breaker, opened by the rejected key, closes at once.
func TestDecisionClientSecretChangeResetsTheBreaker(t *testing.T) {
	r := newDecisionRig(t, nil)
	r.publish(r.bindOpenRouter("sk-or-v1-invalid-0123456789"))
	var e *jev.Error
	if _, err := r.probe(); !errors.As(err, &e) || e.Kind != jev.KindAuth {
		t.Fatalf("probe with the rejected key: %v, want an auth error", err)
	}
	if st := r.d.sw.Status(); st.Breaker != jev.BreakerOpen {
		t.Fatalf("breaker = %q, want open after a 401", st.Breaker)
	}
	client := r.d.sw.Current()

	r.publish(r.bindOpenRouter("sk-or-v1-e2e-good-0123456789"))
	if r.d.sw.Current() != client {
		t.Fatal("a secret-only change rebuilt the client")
	}
	if st := r.d.sw.Status(); st.Breaker != jev.BreakerClosed {
		t.Fatalf("breaker after the new secret = %q, want closed at once", st.Breaker)
	}
	if _, err := r.probe(); err != nil {
		t.Fatalf("probe with the new secret: %v", err)
	}
}

// TestDecisionClientRebuildKeepsSpendKeepWarmAndSnapshot: a model change rebuilds the client; the
// day's spend, the keep-warm wish and the last snapshot carry over, so the new model's snapshot
// is announced as a change.
func TestDecisionClientRebuildKeepsSpendKeepWarmAndSnapshot(t *testing.T) {
	r := newDecisionRig(t, func(c *config.Config) {
		c.SpendCapDecisionUSDDay = 5
		c.Jev.KeepAlive = time.Hour
	})
	r.publish(r.bindOpenRouter("sk-or-v1-e2e-good-0123456789"))
	r.d.sw.SetKeepWarm(true)
	first, err := r.probe()
	if err != nil {
		t.Fatal(err)
	}
	before := r.d.sw.Status()
	if before.Spend == nil || before.Spend.SpentUSD <= 0 {
		t.Fatalf("spend after a probe = %+v, want a positive spend", before.Spend)
	}
	old := r.d.sw.Current()

	r.publish(func(s *runtimecfg.Snapshot) {
		m := "typesafe/jev-1.12"
		s.Settings.DecisionModel.Model = &m
	})
	if r.d.sw.Current() == old {
		t.Fatal("a model change did not rebuild the client")
	}
	after := r.d.sw.Status()
	if after.Model != "typesafe/jev-1.12" {
		t.Fatalf("model in effect = %q", after.Model)
	}
	if after.Spend == nil || after.Spend.SpentUSD != before.Spend.SpentUSD {
		t.Fatalf("spend across the rebuild: %+v, want %v carried over", after.Spend, before.Spend.SpentUSD)
	}
	if !after.KeepWarm {
		t.Fatal("keep-warm did not carry over to the rebuilt client")
	}
	if st := old.(jev.StatusReporter).Status(); st.KeepWarm {
		t.Fatal("the replaced client still keeps warm")
	}
	second, err := r.probe()
	if err != nil {
		t.Fatal(err)
	}
	if second.Snapshot == first.Snapshot {
		t.Fatalf("the mock answered both models with snapshot %q", first.Snapshot)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.changes) != 1 || r.changes[0].Previous != first.Snapshot || r.changes[0].Snapshot != second.Snapshot {
		t.Fatalf("snapshot changes = %+v, want one from %q to %q", r.changes, first.Snapshot, second.Snapshot)
	}
}

// TestDecisionClientUnchangedSnapshotKeepsTheClient: a snapshot that changes nothing the decision
// model is built from (a budget, an upstream) leaves the client alone.
func TestDecisionClientUnchangedSnapshotKeepsTheClient(t *testing.T) {
	r := newDecisionRig(t, nil)
	r.publish(r.bindOpenRouter("sk-or-v1-e2e-good-0123456789"))
	client := r.d.sw.Current()
	r.publish(func(s *runtimecfg.Snapshot) {
		s.Settings.Budgets.DecisionUSDDay = 9
		u := "https://api.example.com"
		s.Settings.Upstream.Default.URL = &u
	})
	if r.d.sw.Current() != client {
		t.Fatal("an unrelated change rebuilt the decision-model client")
	}
}

// TestDecisionClientUnusableBindingIsOff: a binding whose credential is missing (deleted,
// undecryptable) or cannot serve the decision model leaves fast checks off, with the reason.
func TestDecisionClientUnusableBindingIsOff(t *testing.T) {
	r := newDecisionRig(t, nil)
	r.publish(func(s *runtimecfg.Snapshot) {
		name := "openrouter"
		s.Settings.Bindings.DecisionModel = &name // no such credential in the snapshot
	})
	if got := r.d.sw.Info().Backend; got != jev.BackendOff {
		t.Fatalf("binding without its credential: backend %q, want off", got)
	}
	r.publish(func(s *runtimecfg.Snapshot) {
		name := "anthropic"
		s.Settings.Bindings.DecisionModel = &name
		s.Credentials[name] = runtimecfg.Credential{Name: name, Provider: "anthropic", Secret: "sk-ant-e2e-0123456789", AllowedOrigins: []string{"https://api.anthropic.com"}}
	})
	if got := r.d.sw.Info().Backend; got != jev.BackendOff {
		t.Fatalf("an anthropic credential bound to the decision model: backend %q, want off", got)
	}
}
