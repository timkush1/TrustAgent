package runtimecfg

import (
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/jev"
)

// Env precedence (SPEC §2.7, P4-PLAN D33): built-in < env seed < settings row, and a non-empty env
// var always wins (an empty one counts as unset). A path a non-empty env var pins is env-managed:
// it shows with the env value, is read-only in the UI, and a write to it answers 409
// env_managed.

// Engine registry keys whose presence in the engine's env (EngineInfo.env_managed) pins the judge.
const (
	keyLLMProvider = "LLM_PROVIDER"
	keyLLMModel    = "LLM_MODEL"
)

// judgeEnvKeys are the engine env vars that make the engine resolve its judge from the env (PY-1:
// an env key or LLM_PROVIDER wins), so bindings.judge is env-managed (D33).
var judgeEnvKeys = []string{keyLLMProvider, "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"}

// envPaths are the D33 paths, in display order. key is the proxy registry key that pins the path
// (empty for paths derived below); set copies the env value from env into dst.
var envPaths = []struct {
	path, key string
	set       func(dst, env *RuntimeSettings)
}{
	{"decision_model.backend", "TRUTHTABLE_JEV_BACKEND", func(d, e *RuntimeSettings) { d.DecisionModel.Backend = e.DecisionModel.Backend }},
	{"decision_model.model", "TRUTHTABLE_JEV_MODEL", func(d, e *RuntimeSettings) { d.DecisionModel.Model = e.DecisionModel.Model }},
	{"decision_model.base_url", "TRUTHTABLE_JEV_BASE_URL", func(d, e *RuntimeSettings) { d.DecisionModel.BaseURL = e.DecisionModel.BaseURL }},
	{"decision_model.path", "TRUTHTABLE_JEV_PATH", func(d, e *RuntimeSettings) { d.DecisionModel.Path = e.DecisionModel.Path }},
	{"judge.provider", "", nil},
	{"judge.model", "", nil},
	{"upstream.default.url", "TRUTHTABLE_UPSTREAM_URL", func(d, e *RuntimeSettings) { d.Upstream.Default.URL = e.Upstream.Default.URL }},
	{"upstream.default.credential", "", nil},
	{"upstream.default.auth_header", "TRUTHTABLE_UPSTREAM_AUTH_HEADER", func(d, e *RuntimeSettings) { d.Upstream.Default.AuthHeader = e.Upstream.Default.AuthHeader }},
	{"upstream.anthropic.url", "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL", func(d, e *RuntimeSettings) { d.Upstream.Anthropic.URL = e.Upstream.Anthropic.URL }},
	{"upstream.anthropic.credential", "", nil},
	{"upstream.fallback.url", "TRUTHTABLE_FALLBACK_UPSTREAM_URL", func(d, e *RuntimeSettings) { d.Upstream.Fallback.URL = e.Upstream.Fallback.URL }},
	{"upstream.fallback.credential", "", nil},
	{"upstream.fallback.format", "TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT", func(d, e *RuntimeSettings) { d.Upstream.Fallback.Format = e.Upstream.Fallback.Format }},
	{"bindings.decision_model", "", nil},
	{"bindings.judge", "", nil},
	{"bindings.upstream_default", "", nil},
	{"budgets.decision_usd_day", "TRUTHTABLE_SPEND_CAP_DECISION_USD_DAY", func(d, e *RuntimeSettings) { d.Budgets.DecisionUSDDay = e.Budgets.DecisionUSDDay }},
	{"budgets.llm_usd_day", "TRUTHTABLE_SPEND_CAP_LLM_USD_DAY", func(d, e *RuntimeSettings) { d.Budgets.LLMUSDDay = e.Budgets.LLMUSDDay }},
}

// pathOrder is the display position of each D33 path.
var pathOrder = func() map[string]int {
	m := map[string]int{}
	for i, p := range envPaths {
		m[p.path] = i
	}
	return m
}()

// envKeysSet returns the proxy registry keys set non-empty by the process env or the .env file.
func envKeysSet(cfg *config.Config) map[string]bool {
	out := map[string]bool{}
	if cfg == nil {
		return out
	}
	for _, e := range cfg.Entries() {
		if (e.Source == config.SourceEnv || e.Source == config.SourceDotenv) && strings.TrimSpace(e.Value) != "" {
			out[e.Key.Name] = true
		}
	}
	return out
}

// defaultsConfig loads the registry defaults alone: no process env and no .env file.
func defaultsConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	off := false
	return config.LoadWith(config.LoadOptions{Environ: []string{}, AutoDotenv: &off, Registry: cfg.Registry()})
}

// builtinSettings are the runtime settings before any env var or settings row: the env seed with
// every path that a non-empty env var sets put back to its registry default. A non-empty env var
// always wins over a row (D33), so the env seed never shows under a row; leaving it out of the
// write base (Snapshot.Persisted) keeps an env value from being copied into a row by a write to a
// sibling field. Each call returns fresh pointers, so overlaying rows never writes through to a
// shared value.
func (m *Manager) builtinSettings() RuntimeSettings {
	cfg := m.opts.Config
	s := seedSettings(cfg)
	if cfg == nil || m.defaults == nil {
		return s
	}
	def := seedSettings(m.defaults)
	set := envKeysSet(cfg)
	for _, p := range envPaths {
		if p.key != "" && set[p.key] {
			p.set(&s, &def)
		}
	}
	return s
}

// applyEnv lays every env-managed value over persisted and returns the settings in effect, the
// env-managed paths in display order, and the env vars behind each.
func applyEnv(cfg *config.Config, persisted RuntimeSettings, engineEnv []string) (RuntimeSettings, []string, map[string][]string) {
	eff := persisted
	sources := map[string][]string{}
	mark := func(path string, keys ...string) {
		for _, k := range keys {
			if !slices.Contains(sources[path], k) {
				sources[path] = append(sources[path], k)
			}
		}
	}
	set := envKeysSet(cfg)
	if cfg != nil {
		env := seedSettings(cfg)
		for _, p := range envPaths {
			if p.key != "" && set[p.key] {
				p.set(&eff, &env)
				mark(p.path, p.key)
			}
		}
		// Upstream credentials: an env key binds the matching upstream to the env credential of
		// that name (D33).
		for _, u := range []struct {
			secret, key, name string
			cred              **string
			paths             []string
		}{
			{cfg.UpstreamAPIKey, "TRUTHTABLE_UPSTREAM_API_KEY", credentials.NameUpstreamDefault, &eff.Upstream.Default.Credential,
				[]string{"upstream.default.credential", "bindings.upstream_default"}},
			{cfg.AnthropicUpstreamAPIKey, "TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY", credentials.NameUpstreamAnthropic, &eff.Upstream.Anthropic.Credential,
				[]string{"upstream.anthropic.credential"}},
			{cfg.FallbackUpstreamAPIKey, "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY", credentials.NameUpstreamFallback, &eff.Upstream.Fallback.Credential,
				[]string{"upstream.fallback.credential"}},
		} {
			if u.secret == "" {
				continue
			}
			name := u.name
			*u.cred = &name
			if u.name == credentials.NameUpstreamDefault {
				eff.Bindings.UpstreamDefault = &name
			}
			for _, p := range u.paths {
				mark(p, u.key)
			}
		}
		// The decision model's credential (D33): an env key that the backend in effect would use
		// pins the binding to that env credential; a backend pinned to mock or off pins it too.
		if name, key := envDecisionCredential(cfg, eff); name != "" {
			eff.Bindings.DecisionModel = &name
			mark("bindings.decision_model", key)
		} else if set["TRUTHTABLE_JEV_BACKEND"] && (eff.DecisionModel.Backend == jev.BackendMock || eff.DecisionModel.Backend == jev.BackendOff) {
			mark("bindings.decision_model", "TRUTHTABLE_JEV_BACKEND")
		}
	}
	// The judge lives in the engine: what its env sets arrives through EngineInfo.env_managed.
	if slices.Contains(engineEnv, keyLLMProvider) {
		mark("judge.provider", keyLLMProvider)
	}
	if slices.Contains(engineEnv, keyLLMModel) {
		mark("judge.model", keyLLMModel)
	}
	for _, k := range judgeEnvKeys {
		if slices.Contains(engineEnv, k) {
			mark("bindings.judge", k)
		}
	}
	managed := make([]string, 0, len(sources))
	for p := range sources {
		managed = append(managed, p)
	}
	slices.SortFunc(managed, func(a, b string) int { return pathOrder[a] - pathOrder[b] })
	return eff, managed, sources
}

// envDecisionCredential returns the env credential the decision model would use with the
// settings s in effect, mirroring jev's resolution (jev.go autoPick and explicitKey), and the env
// var that holds it; "" when the backend would use a dashboard binding or none.
func envDecisionCredential(cfg *config.Config, s RuntimeSettings) (name, key string) {
	jevKey := jevAPIKeyVar(cfg)
	base := ""
	if s.DecisionModel.BaseURL != nil {
		base = *s.DecisionModel.BaseURL
	}
	switch s.DecisionModel.Backend {
	case jev.BackendAuto, "":
		switch {
		case base != "" && cfg.Jev.APIKey != "":
			return credentials.NameCompatible, jevKey
		case cfg.Jev.OpenRouterKey != "":
			return credentials.NameOpenRouter, "OPENROUTER_API_KEY"
		case cfg.Jev.TypeSafeKey != "":
			return credentials.NameTypeSafe, "TYPESAFE_API_KEY"
		}
	case jev.BackendOpenRouterDecisions, jev.BackendOpenRouterSystemOne:
		switch {
		case cfg.Jev.APIKey != "":
			return credentials.NameCompatible, jevKey
		case cfg.Jev.OpenRouterKey != "":
			return credentials.NameOpenRouter, "OPENROUTER_API_KEY"
		}
	case jev.BackendTypeSafe:
		switch {
		case cfg.Jev.APIKey != "":
			return credentials.NameCompatible, jevKey
		case cfg.Jev.TypeSafeKey != "":
			return credentials.NameTypeSafe, "TYPESAFE_API_KEY"
		}
	case jev.BackendCompatible:
		if cfg.Jev.APIKey != "" {
			return credentials.NameCompatible, jevKey
		}
	}
	return "", ""
}

// jevAPIKeyVar names the variable behind cfg.Jev.APIKey: the key itself, or the file it was read
// from.
func jevAPIKeyVar(cfg *config.Config) string {
	if envKeysSet(cfg)["TRUTHTABLE_JEV_API_KEY"] {
		return "TRUTHTABLE_JEV_API_KEY"
	}
	return "TRUTHTABLE_JEV_API_KEY_FILE"
}

// envCredentials are the Go-side env credentials (D29), which win over dashboard rows of the same
// name. Vendor keys go to their vendor origins; the compatible key to the env decision endpoint,
// or to the vendor endpoint of the decision backend in effect (backend); an upstream key to the
// origin of its env (or default) upstream URL. Those URLs come from the operator's env, so they
// are trusted as given; a dashboard URL never widens an env key's origins.
func (m *Manager) envCredentials(backend string) CredentialSet {
	cfg := m.opts.Config
	out := CredentialSet{}
	if cfg == nil {
		return out
	}
	add := func(name, secret string, origins []string) {
		if secret == "" {
			return
		}
		spec, _ := credentials.Lookup(name)
		out[name] = Credential{Name: name, Provider: spec.Provider, Source: credentials.SourceEnv, Secret: secret,
			Masked: credentials.Masked(secret), Fingerprint: m.fp.Of(secret), AllowedOrigins: origins}
	}
	add(credentials.NameOpenRouter, cfg.Jev.OpenRouterKey, credentials.VendorOrigins(credentials.NameOpenRouter, cfg.Jev.OpenRouterBaseURL))
	add(credentials.NameTypeSafe, cfg.Jev.TypeSafeKey, credentials.VendorOrigins(credentials.NameTypeSafe, ""))
	add(credentials.NameCompatible, cfg.Jev.APIKey, compatibleEnvOrigins(cfg, backend))
	upstreamURL := cfg.UpstreamURL
	if upstreamURL == "" {
		upstreamURL = config.DefaultUpstreamURL
	}
	add(credentials.NameUpstreamDefault, cfg.UpstreamAPIKey, originOf(upstreamURL))
	add(credentials.NameUpstreamAnthropic, cfg.AnthropicUpstreamAPIKey, originOf(cfg.AnthropicUpstreamURL))
	add(credentials.NameUpstreamFallback, cfg.FallbackUpstreamAPIKey, originOf(cfg.FallbackUpstreamURL))
	return out
}

// compatibleEnvOrigins are where TRUTHTABLE_JEV_API_KEY may go (SPEC §7.5: only for an explicit
// backend or compatible): the env decision base URL when one is set, otherwise the vendor
// endpoint of the vendor backend in effect, whether the env or a settings row chose it. A vendor
// origin is a fixed table, so a dashboard choice of backend cannot send the key anywhere else; a
// dashboard base URL never counts.
func compatibleEnvOrigins(cfg *config.Config, backend string) []string {
	if cfg.Jev.BaseURL != "" {
		return originOf(cfg.Jev.BaseURL)
	}
	switch backend {
	case jev.BackendOpenRouterDecisions, jev.BackendOpenRouterSystemOne:
		return credentials.VendorOrigins(credentials.NameOpenRouter, cfg.Jev.OpenRouterBaseURL)
	case jev.BackendTypeSafe:
		return credentials.VendorOrigins(credentials.NameTypeSafe, "")
	}
	return []string{}
}

// originOf is the one-element origin list of raw, or empty when raw has no origin.
func originOf(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	o, err := credentials.Canonical(raw)
	if err != nil {
		return []string{}
	}
	return []string{o}
}
