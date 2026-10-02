// Package credentials is the encrypted credential store's core (SPEC §7.5, P4-PLAN D29-D32,
// D78): the credential names and providers, AES-256-GCM sealing under the master key's
// credentials subkey, fingerprints, the origins a secret may be sent to, and the store's state.
//
// It holds no storage and no network code. The rows live in internal/store (credentials.go); the
// runtime configuration (internal/runtimecfg) opens them into a snapshot; verify*.go and pkce*.go
// (GCONN) check a key with its provider and run the OpenRouter PKCE exchange.
//
// A secret never appears in an error, a log line or a String method of this package: errors name
// the credential and the failure only.
package credentials

import (
	"slices"

	"github.com/truthtable/backend-go/internal/config"
)

// Credential names (credentials.name, D29). The database CHECK constraint holds the same list.
const (
	NameOpenRouter        = "openrouter"
	NameTypeSafe          = "typesafe"
	NameAnthropic         = "anthropic"
	NameOpenAI            = "openai"
	NameCompatible        = "compatible"
	NameUpstreamDefault   = "upstream-default"
	NameUpstreamAnthropic = "upstream-anthropic"
	NameUpstreamFallback  = "upstream-fallback"
)

// Providers (api.ts CredentialView.provider).
const (
	ProviderOpenRouter = "openrouter"
	ProviderTypeSafe   = "typesafe"
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGeneric    = "generic"
)

// Sources of a credential (api.ts CredentialView.source).
const (
	SourceEnv       = "env"
	SourceDashboard = "dashboard"
)

// Side says which process reads a credential's env var (D29, D80).
type Side string

// Sides of an env credential.
const (
	SideProxy  Side = "proxy"
	SideEngine Side = "engine"
)

// Spec describes one credential name: its provider, the env var that overrides it and the
// process that reads that var (D29).
type Spec struct {
	Name     string
	Provider string
	// EnvVar is the variable whose non-empty value wins over a dashboard row (D33).
	EnvVar string
	Side   Side
	// CustomOrigin marks compatible and upstream-*: their allowed origins are fixed at creation
	// from the base URL given with the secret, and that origin list is part of the AAD (D78).
	// The four vendors take a fixed origin table and reject a base URL.
	CustomOrigin bool
}

// specs is the D29 map, in display order.
var specs = []Spec{
	{Name: NameOpenRouter, Provider: ProviderOpenRouter, EnvVar: "OPENROUTER_API_KEY", Side: SideProxy},
	{Name: NameTypeSafe, Provider: ProviderTypeSafe, EnvVar: "TYPESAFE_API_KEY", Side: SideProxy},
	{Name: NameAnthropic, Provider: ProviderAnthropic, EnvVar: "ANTHROPIC_API_KEY", Side: SideEngine},
	{Name: NameOpenAI, Provider: ProviderOpenAI, EnvVar: "OPENAI_API_KEY", Side: SideEngine},
	{Name: NameCompatible, Provider: ProviderGeneric, EnvVar: "TRUTHTABLE_JEV_API_KEY", Side: SideProxy, CustomOrigin: true},
	{Name: NameUpstreamDefault, Provider: ProviderGeneric, EnvVar: "TRUTHTABLE_UPSTREAM_API_KEY", Side: SideProxy, CustomOrigin: true},
	{Name: NameUpstreamAnthropic, Provider: ProviderGeneric, EnvVar: "TRUTHTABLE_ANTHROPIC_UPSTREAM_API_KEY", Side: SideProxy, CustomOrigin: true},
	{Name: NameUpstreamFallback, Provider: ProviderGeneric, EnvVar: "TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY", Side: SideProxy, CustomOrigin: true},
}

// Specs returns every credential name's spec, in display order.
func Specs() []Spec { return slices.Clone(specs) }

// Lookup returns the spec of name.
func Lookup(name string) (Spec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return Spec{}, false
}

// Names returns every credential name, in display order.
func Names() []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// Masked renders a secret for display (CredentialView.masked, D79): config.MaskSecret, "sk-…3f2a".
func Masked(secret string) string { return config.MaskSecret(secret) }
