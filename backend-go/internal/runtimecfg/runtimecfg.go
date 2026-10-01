// Package runtimecfg is the gateway's runtime configuration (SPEC §2.7, P4-PLAN D31-D33): the
// settings an admin edits on the dashboard over the env seed, the credentials from the encrypted
// store and the env, and the snapshot every consumer reads. A Manager publishes snapshots;
// consumers (the decision-model swap, the upstream rebuild, the engine push, the cost caps)
// subscribe to them.
//
// At P4 Step 0 this file holds the fixed seam: the types, NewStatic (the env seed, no database)
// and the Manager methods other streams call. GSEC fills in the database-backed Manager;
// push*.go belongs to GCONN. runtimecfg imports decision and jev; it never imports upstream or
// costs (D90).
package runtimecfg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// Binding targets a credential write may bind (api.ts BindingTarget, D75).
const (
	TargetDecisionModel = "decision_model"
	TargetJudge         = "judge"
)

// Reasons a binding target is skipped (api.ts BindingSkipReason).
const (
	SkipEnvManaged         = "env_managed"
	SkipBackendPinned      = "backend_pinned"
	SkipVerificationFailed = "verification_failed"
)

// RuntimeSettings is api.ts RuntimeSettings, with the same JSON names: the settings rows over the
// env seed (D33).
type RuntimeSettings struct {
	DecisionModel    DecisionModelSettings `json:"decision_model"`
	Judge            JudgeSettings         `json:"judge"`
	Upstream         UpstreamSettings      `json:"upstream"`
	Bindings         Bindings              `json:"bindings"`
	Budgets          Budgets               `json:"budgets"`
	PricingOverrides []decision.PriceEntry `json:"pricing_overrides"`
}

// DecisionModelSettings is RuntimeSettings.decision_model. Backend is a decision backend id or
// auto.
type DecisionModelSettings struct {
	Backend string  `json:"backend"`
	Model   *string `json:"model"`
	BaseURL *string `json:"base_url"`
	Path    *string `json:"path"`
}

// JudgeSettings is RuntimeSettings.judge: provider auto, anthropic, openrouter, openai, ollama,
// mock or none.
type JudgeSettings struct {
	Provider string  `json:"provider"`
	Model    *string `json:"model"`
}

// UpstreamSetting is api.ts UpstreamSetting.
type UpstreamSetting struct {
	URL        *string `json:"url"`
	Credential *string `json:"credential"`
	AuthHeader string  `json:"auth_header"`
}

// FallbackUpstreamSetting is the fallback upstream: an UpstreamSetting with its API format.
type FallbackUpstreamSetting struct {
	UpstreamSetting
	Format string `json:"format"`
}

// UpstreamSettings is RuntimeSettings.upstream.
type UpstreamSettings struct {
	Default   UpstreamSetting         `json:"default"`
	Anthropic UpstreamSetting         `json:"anthropic"`
	Fallback  FallbackUpstreamSetting `json:"fallback"`
}

// Bindings is api.ts Bindings: which credential each consumer uses (nil = none bound).
type Bindings struct {
	DecisionModel   *string `json:"decision_model"`
	Judge           *string `json:"judge"`
	UpstreamDefault *string `json:"upstream_default"`
}

// Budgets is RuntimeSettings.budgets, USD per UTC day.
type Budgets struct {
	DecisionUSDDay float64 `json:"decision_usd_day"`
	LLMUSDDay      float64 `json:"llm_usd_day"`
}

// Credential is one usable credential of a snapshot: decrypted from the store or read from the
// env. Secret never leaves the process except to the origins in AllowedOrigins.
type Credential struct {
	Name     string
	Provider string
	// Source is env or dashboard.
	Source      string
	Secret      string `json:"-"`
	Masked      string
	Fingerprint string
	// AllowedOrigins are canonical origins scheme://host[:port] (D78).
	AllowedOrigins []string
}

// CredentialSet is a snapshot's usable credentials by name (credentials.name).
type CredentialSet map[string]Credential

// Warning is a snapshot warning shown in /api/status and /api/settings (D32, D77), e.g.
// master_key_changed with details {names} or fail_closed_unprotected with details {routes}.
type Warning struct {
	Code    string
	Message string
	Details map[string]any
}

// Snapshot is the runtime configuration in effect. Consumers treat it as immutable.
type Snapshot struct {
	// Version is the global runtime-config version (the settings row epoch's version column,
	// D31); 0 without a database.
	Version int64
	// Epoch identifies the settings store (D31): a random one per process without a database.
	Epoch    string
	Settings RuntimeSettings
	// EnvManaged lists the dotted RuntimeSettings paths a non-empty env var pins (D33).
	EnvManaged []string
	// Credentials are the usable credentials; Undecryptable names the stored rows that cannot be
	// opened (D32).
	Credentials   CredentialSet
	Undecryptable []string
	Warnings      []Warning
}

// BindingSkip is a binding target that was not applied, with its reason (api.ts
// CredentialWriteResult.bindings.skipped).
type BindingSkip struct {
	Target string
	Reason string
}

// ErrCredentialNotFound is returned by a KeyFunc whose credential the snapshot does not hold.
var ErrCredentialNotFound = errors.New("runtimecfg: credential not available")

// ErrHostNotAllowed is returned by a KeyFunc asked for an origin outside the credential's allowed
// origins (D78).
var ErrHostNotAllowed = errors.New("runtimecfg: origin not allowed for this credential")

// Manager publishes runtime configuration snapshots.
type Manager struct {
	mu        sync.RWMutex
	cur       *Snapshot
	subs      map[int]func(*Snapshot)
	nextSub   int
	engineEnv []string
}

// NewStatic is the Manager of a gateway without a settings store: one snapshot from the env seed
// (version 0, a random epoch, no stored credentials). It never changes on its own.
func NewStatic(cfg *config.Config) *Manager {
	return &Manager{cur: &Snapshot{Epoch: newEpoch(), Settings: seedSettings(cfg), Credentials: CredentialSet{}},
		subs: map[int]func(*Snapshot){}}
}

// newEpoch is a UUIDv7 epoch (D31).
func newEpoch() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// seedSettings are the built-in defaults and the env seed of the runtime settings (D33).
func seedSettings(cfg *config.Config) RuntimeSettings {
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	s := RuntimeSettings{
		DecisionModel: DecisionModelSettings{Backend: "auto"},
		Judge:         JudgeSettings{Provider: "auto"},
		Upstream: UpstreamSettings{
			Default:   UpstreamSetting{AuthHeader: "authorization"},
			Anthropic: UpstreamSetting{AuthHeader: "x-api-key"},
			Fallback:  FallbackUpstreamSetting{UpstreamSetting: UpstreamSetting{AuthHeader: "authorization"}, Format: "openai"},
		},
		PricingOverrides: []decision.PriceEntry{},
	}
	if cfg == nil {
		return s
	}
	if cfg.Jev.Backend != "" {
		s.DecisionModel.Backend = cfg.Jev.Backend
	}
	s.DecisionModel.Model, s.DecisionModel.BaseURL, s.DecisionModel.Path = opt(cfg.Jev.Model), opt(cfg.Jev.BaseURL), opt(cfg.Jev.Path)
	s.Upstream.Default.URL = opt(cfg.UpstreamURL)
	if cfg.UpstreamAuthHeader != "" {
		s.Upstream.Default.AuthHeader = cfg.UpstreamAuthHeader
	}
	s.Upstream.Anthropic.URL = opt(cfg.AnthropicUpstreamURL)
	s.Upstream.Fallback.URL = opt(cfg.FallbackUpstreamURL)
	if cfg.FallbackUpstreamFormat != "" {
		s.Upstream.Fallback.Format = cfg.FallbackUpstreamFormat
	}
	s.Budgets = Budgets{DecisionUSDDay: cfg.SpendCapDecisionUSDDay, LLMUSDDay: cfg.SpendCapLLMUSDDay}
	return s
}

// Current returns the snapshot in effect.
func (m *Manager) Current() *Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur
}

// Subscribe registers fn for every new snapshot and returns the function that removes it. fn
// runs on the publishing goroutine and must not block.
func (m *Manager) Subscribe(fn func(*Snapshot)) (cancel func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, id)
	}
}

// KeyFunc returns the credential called name from the snapshot in effect at each call, refusing
// an origin outside its allowed origins (D78).
func (m *Manager) KeyFunc(name string) jev.KeyFunc {
	return func(_ context.Context, origin string) (string, error) {
		c, ok := m.Current().Credentials[name]
		if !ok || c.Secret == "" {
			return "", fmt.Errorf("%w: %s", ErrCredentialNotFound, name)
		}
		if !slices.Contains(c.AllowedOrigins, origin) {
			return "", fmt.Errorf("%w: %s", ErrHostNotAllowed, name)
		}
		return c.Secret, nil
	}
}

// SetEngineEnvManaged records the engine registry keys set in the engine's env
// (EngineInfo.env_managed, D34/D80); they join the snapshot's env-managed paths.
func (m *Manager) SetEngineEnvManaged(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.engineEnv = append([]string(nil), names...)
}

// ApplyBindings binds the credential called name to targets (D75) and returns the new settings,
// the targets applied and those skipped. A target whose bindings.<target> path is env-managed is
// skipped (env_managed); decision_model is skipped when the backend is env-pinned to one that
// does not use the credential (backend_pinned). When any target applies, a persisted mock
// backend or mock judge becomes auto (D76). It is pure.
func ApplyBindings(s RuntimeSettings, name string, targets, envManaged []string, backendPinned bool) (RuntimeSettings, []string, []BindingSkip) {
	applied, skipped := []string{}, []BindingSkip{}
	for _, t := range []string{TargetDecisionModel, TargetJudge} {
		if !slices.Contains(targets, t) {
			continue
		}
		switch {
		case slices.Contains(envManaged, "bindings."+t):
			skipped = append(skipped, BindingSkip{Target: t, Reason: SkipEnvManaged})
		case t == TargetDecisionModel && backendPinned:
			skipped = append(skipped, BindingSkip{Target: t, Reason: SkipBackendPinned})
		default:
			n := name
			if t == TargetDecisionModel {
				s.Bindings.DecisionModel = &n
			} else {
				s.Bindings.Judge = &n
			}
			applied = append(applied, t)
		}
	}
	if len(applied) > 0 {
		if s.DecisionModel.Backend == jev.BackendMock && !slices.Contains(envManaged, "decision_model.backend") {
			s.DecisionModel.Backend = "auto"
		}
		if s.Judge.Provider == "mock" && !slices.Contains(envManaged, "judge.provider") {
			s.Judge.Provider = "auto"
		}
	}
	return s, applied, skipped
}
