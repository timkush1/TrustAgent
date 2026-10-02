// Package runtimecfg is the gateway's runtime configuration (SPEC §2.7, P4-PLAN D31-D33): the
// settings an admin edits on the dashboard over the env seed, the credentials from the encrypted
// store and the env, and the snapshot every consumer reads. A Manager publishes snapshots;
// consumers (the decision-model swap, the upstream rebuild, the engine push, the cost caps)
// subscribe to them.
//
// This file holds the seam other streams call (frozen at CP-SEC1): the types, NewStatic (no
// database), Current, Subscribe, KeyFunc, SetEngineEnvManaged and ApplyBindings. manager.go is the
// database-backed Manager (Reload, the reload loop), envmanaged.go the D33 env precedence, and
// push*.go belongs to GCONN. runtimecfg imports decision, jev, credentials and store; it never
// imports upstream or costs (D90).
package runtimecfg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
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
	// Source is env (credentials.SourceEnv) or dashboard (credentials.SourceDashboard).
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

// Warning codes a snapshot carries. fail_closed_unprotected (D77) depends on the policy in effect
// and the decision model's live state, so the status and settings handlers add it when they
// answer; WarnFailClosedUnprotected is its code.
const (
	// WarnMasterKeyChanged: stored credentials cannot be opened with the current master key
	// (D32); details.names lists them.
	WarnMasterKeyChanged = "master_key_changed"
	// WarnStoreUnavailable: a database but no durable master key, so dashboard credentials can be
	// neither stored nor read (D32); details.names lists the stored rows that are unusable.
	WarnStoreUnavailable = "store_unavailable"
	// WarnSettingsUnreadable: the last reload could not read the settings store; the previous
	// snapshot (or the env seed at boot) stays in effect until a reload succeeds.
	WarnSettingsUnreadable = "settings_unreadable"
	// WarnFailClosedUnprotected: an enforcing fail-closed route has no usable decision model
	// (D77); details.routes lists the routes.
	WarnFailClosedUnprotected = "fail_closed_unprotected"
)

// Snapshot is the runtime configuration in effect. Consumers treat it as immutable.
type Snapshot struct {
	// Version is the global runtime-config version (the settings row epoch's version column,
	// D31); 0 without a database.
	Version int64
	// Epoch identifies the settings store (D31): a random one per process without a database.
	Epoch string
	// Settings are the settings in effect: the env seed, the stored rows over it, and every
	// env-managed path over those (D33).
	Settings RuntimeSettings
	// Persisted is the built-in defaults with the stored rows over it, without any env value: the
	// base a write starts from, so that an env value is never copied into a row (ChangedRows,
	// ChangedFields). Settings is Persisted with the env-managed paths laid over it.
	Persisted RuntimeSettings
	// EnvManaged lists the dotted RuntimeSettings paths a non-empty env var pins (D33).
	EnvManaged []string
	// Credentials are the usable credentials; Undecryptable names the stored rows that cannot be
	// opened (D32).
	Credentials   CredentialSet
	Undecryptable []string
	Warnings      []Warning
	// StoreState is the credential store's state: ready, unavailable or no_database (D32).
	StoreState string
	// EnvSources maps each EnvManaged path to the env var names that pin it (a 409 env_managed
	// carries them as details.keys, D33).
	EnvSources map[string][]string
	// EngineEnv names the engine registry keys set non-empty in the engine's env, as the engine
	// last reported them (EngineInfo.env_managed, D34/D80): anthropic and openai keys there show
	// as env credentials.
	EngineEnv []string
}

// IsEnvManaged reports whether a non-empty env var pins the dotted RuntimeSettings path (D33).
func (s *Snapshot) IsEnvManaged(path string) bool { return slices.Contains(s.EnvManaged, path) }

// BackendPinned reports whether binding the credential called name to the decision model would be
// skipped as backend_pinned (D33, D75): decision_model.backend is pinned in the env to mock, off,
// or a backend that does not use that credential.
func (s *Snapshot) BackendPinned(name string) bool {
	b := s.Settings.DecisionModel.Backend
	if !s.IsEnvManaged("decision_model.backend") || b == jev.BackendAuto {
		return false
	}
	return !slices.Contains(DecisionBackends(name), b)
}

// DecisionBackends are the decision backends a credential can serve: an OpenRouter key the two
// OpenRouter endpoints, a TypeSafe key TypeSafe, the compatible key the compatible gateway, and
// any other credential none.
func DecisionBackends(name string) []string {
	switch name {
	case credentials.NameOpenRouter:
		return []string{jev.BackendOpenRouterDecisions, jev.BackendOpenRouterSystemOne}
	case credentials.NameTypeSafe:
		return []string{jev.BackendTypeSafe}
	case credentials.NameCompatible:
		return []string{jev.BackendCompatible}
	}
	return nil
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

// Manager publishes runtime configuration snapshots. New builds it; NewStatic is the one of a
// gateway without a settings store.
type Manager struct {
	opts Options
	fp   credentials.Fingerprinter
	// cipher opens stored credentials; nil while the store is not ready (D32).
	cipher *credentials.Cipher
	// defaults is the configuration of the registry defaults alone (no env, no .env file): the
	// built-in values of the env-managed paths (builtinSettings).
	defaults *config.Config
	// localEpoch is the epoch of a snapshot without a database (D31: random per process).
	localEpoch string

	// reloadMu serializes building and publishing, so subscribers see snapshots in order.
	reloadMu sync.Mutex
	// loaded is the last state read from the store (nil before the first successful read).
	loaded *loadedState
	// loadErr is the error of the last read ("" after a success).
	loadErr string

	mu        sync.RWMutex
	cur       *Snapshot
	subs      map[int]func(*Snapshot)
	nextSub   int
	engineEnv []string

	// kick asks the reload loop for a reload (hub notifications).
	kick chan struct{}

	touchMu sync.Mutex
	touched map[string]time.Time
	touchWG sync.WaitGroup
}

// NewStatic is the Manager of a gateway without a settings store: one snapshot from the built-in
// defaults and the env (version 0, a random epoch, env credentials only). Only
// SetEngineEnvManaged changes it.
func NewStatic(cfg *config.Config) *Manager { return New(Options{Config: cfg}) }

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
// runs on the publishing goroutine, in publication order, and must not block: hand slow work
// (a client rebuild, an engine push) to a goroutine of its own. fn must not call Reload,
// SetEngineEnvManaged or Stored, which take the same lock; the snapshot it gets is current.
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
// an origin outside its allowed origins (D78). origin is a canonical origin
// (jev.CanonicalOrigin). A dashboard credential's use is recorded in last_used_at, at most once a
// minute per name and in the background. Errors name the credential and the origin, never the
// secret.
func (m *Manager) KeyFunc(name string) jev.KeyFunc {
	return func(_ context.Context, origin string) (string, error) {
		c, ok := m.Current().Credentials[name]
		if !ok || c.Secret == "" {
			return "", fmt.Errorf("%w: %s", ErrCredentialNotFound, name)
		}
		if !slices.Contains(c.AllowedOrigins, origin) {
			return "", fmt.Errorf("%w: %s may not be sent to %s", ErrHostNotAllowed, name, origin)
		}
		if c.Source == credentials.SourceDashboard {
			m.touch(name)
		}
		return c.Secret, nil
	}
}

// SetEngineEnvManaged records the engine registry keys set in the engine's env
// (EngineInfo.env_managed, D34/D80). They join the snapshot's env-managed paths (judge.*,
// bindings.judge) and its engine env credentials; a change publishes a new snapshot at once,
// from the last state read (no database round trip).
func (m *Manager) SetEngineEnvManaged(names []string) {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	same := slices.Equal(m.engineEnv, names)
	m.engineEnv = names
	m.mu.Unlock()
	if !same {
		m.publishLocked(m.build())
	}
}

// ApplyBindings binds the credential called name to targets (D75) and returns the new settings,
// the targets applied and those skipped. decision_model is skipped when the backend is env-pinned
// to one that does not use the credential (backend_pinned: pass Snapshot.BackendPinned(name));
// otherwise a target whose bindings.<target> path is env-managed is skipped (env_managed). When
// any target applies, a persisted mock backend or mock judge becomes auto (D76), unless that path
// is env-managed. Pass the persisted settings to get the rows to write. It is pure.
func ApplyBindings(s RuntimeSettings, name string, targets, envManaged []string, backendPinned bool) (RuntimeSettings, []string, []BindingSkip) {
	applied, skipped := []string{}, []BindingSkip{}
	for _, t := range []string{TargetDecisionModel, TargetJudge} {
		if !slices.Contains(targets, t) {
			continue
		}
		switch {
		// A pinned backend is the more specific reason: D33 lists bindings.decision_model as
		// env-managed when the backend is pinned to mock or off, and the write reports why.
		case t == TargetDecisionModel && backendPinned:
			skipped = append(skipped, BindingSkip{Target: t, Reason: SkipBackendPinned})
		case slices.Contains(envManaged, "bindings."+t):
			skipped = append(skipped, BindingSkip{Target: t, Reason: SkipEnvManaged})
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
