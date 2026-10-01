package upstream

import (
	"net/url"
	"strings"

	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// Codes of the runtime upstream checks (api.ts FieldError.code and the settings warnings).
const (
	// CodeFallbackKeyRequired refuses a fallback upstream on another origin than the primary
	// upstream of its format with no key of its own (D41, P3 D23): the callers' credentials,
	// meant for the primary upstream, would be sent there.
	CodeFallbackKeyRequired = "fallback_key_required"
	// CodeUpstreamPassthrough warns that an upstream without a key of its own forwards the
	// callers' credentials while another upstream injects one (P3 D15: per upstream).
	CodeUpstreamPassthrough = "upstream_passthrough"
	// CodeFallbackCredentialUnused warns that the fallback upstream's key is never sent: there is
	// no fallback upstream.
	CodeFallbackCredentialUnused = "fallback_credential_unused"
)

// Credential names of the upstream rows (P4-PLAN D29); an env upstream key shows as the row of
// the same name with source env.
const (
	CredentialDefault   = "upstream-default"
	CredentialAnthropic = "upstream-anthropic"
	CredentialFallback  = "upstream-fallback"
)

// FieldError is a runtime upstream setting that is refused (a 422 validation_failed field error),
// e.g. code fallback_key_required with details {host} (P4-PLAN D41).
type FieldError struct {
	Path    string
	Code    string
	Message string
	Details map[string]any
}

// Warning is a runtime upstream risk that does not refuse the write (D15 stays a warning). Path
// is the setting it is about and Hint what to do.
type Warning struct {
	Code    string
	Message string
	Details map[string]any
	Path    string
	Hint    string
}

// RuntimeRisks checks runtime upstream settings against the credentials before a PATCH of
// /api/settings is saved (D41, D23). An upstream has a key of its own when its credential is
// bound (upstream.<name>.credential, or bindings.upstream_default for the default upstream, D41b)
// and present in creds, or when its env key is set (the env row of the same name):
//
//   - D23, refused: a fallback upstream without a key whose origin differs from the primary
//     upstream of its format (fallback_key_required on upstream.fallback.url, details {host});
//   - D15, warned: an upstream without a key forwards callers' credentials while another injects
//     one (upstream_passthrough), and a fallback key with no fallback URL is never sent
//     (fallback_credential_unused).
//
// upstream imports runtimecfg, never the other way round.
func RuntimeRisks(settings runtimecfg.RuntimeSettings, creds runtimecfg.CredentialSet) (fieldErrs []FieldError, warnings []Warning) {
	up := settings.Upstream
	defaultCred := up.Default.Credential
	if defaultCred == nil {
		defaultCred = settings.Bindings.UpstreamDefault
	}
	keyed := map[string]bool{
		"default":   hasKey(defaultCred, CredentialDefault, creds),
		"anthropic": hasKey(up.Anthropic.Credential, CredentialAnthropic, creds),
		"fallback":  hasKey(up.Fallback.Credential, CredentialFallback, creds),
	}
	fallbackURL := value(up.Fallback.URL)

	if fallbackURL != "" && !keyed["fallback"] {
		primary, primaryPath := urlOr(up.Default.URL, DefaultBaseURL), "upstream.default.url"
		if up.Fallback.Format == llmapi.FamilyAnthropic {
			primary, primaryPath = urlOr(up.Anthropic.URL, DefaultAnthropicBaseURL), "upstream.anthropic.url"
		}
		if fb, ok := origin(fallbackURL); !ok || fb != originOrEmpty(primary) {
			host := hostOf(fallbackURL)
			fieldErrs = append(fieldErrs, FieldError{
				Path: "upstream.fallback.url", Code: CodeFallbackKeyRequired,
				Message: "The fallback upstream (" + host + ") has no key of its own and is not at the address of " + primaryPath +
					", so your apps' credentials would be sent to a server they never chose. Add a key for the fallback upstream first.",
				Details: map[string]any{"host": host},
			})
		}
	}
	if fallbackURL == "" && keyed["fallback"] {
		warnings = append(warnings, Warning{
			Code: CodeFallbackCredentialUnused, Path: "upstream.fallback.credential",
			Message: "The fallback upstream's key is never sent: no fallback upstream URL is set.",
			Hint:    "Set upstream.fallback.url, or remove the fallback key.",
		})
	}
	if keyed["default"] || keyed["anthropic"] || keyed["fallback"] {
		inUse := []struct{ name, label string }{{"default", "default"}, {"anthropic", "Anthropic"}}
		if fallbackURL != "" {
			inUse = append(inUse, struct{ name, label string }{"fallback", "fallback"})
		}
		for _, u := range inUse {
			if keyed[u.name] {
				continue
			}
			warnings = append(warnings, Warning{
				Code: CodeUpstreamPassthrough, Path: "upstream." + u.name + ".credential",
				Message: "The " + u.label + " upstream has no key of its own: requests to it carry each caller's own credential, while another upstream uses a key from TrustAgent.",
				Hint:    "Add a key for the " + u.label + " upstream if callers should not need their own.",
			})
		}
	}
	return fieldErrs, warnings
}

// hasKey reports whether an upstream has a key of its own: a bound credential present in creds,
// or its env key (the env row named envRow).
func hasKey(bound *string, envRow string, creds runtimecfg.CredentialSet) bool {
	if c, ok := creds[envRow]; ok && c.Source == "env" && c.Secret != "" {
		return true
	}
	if bound == nil || *bound == "" {
		return false
	}
	c, ok := creds[*bound]
	return ok && c.Secret != ""
}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func urlOr(p *string, def string) string {
	if v := value(p); v != "" {
		return v
	}
	return def
}

// origin is the canonical origin of raw (D78), false when raw is not an absolute URL.
func origin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	return jev.CanonicalOrigin(u), true
}

func originOrEmpty(raw string) string {
	o, _ := origin(raw)
	return o
}

// hostOf is the host of raw for messages (never its userinfo or query).
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	return strings.ToLower(u.Hostname())
}
