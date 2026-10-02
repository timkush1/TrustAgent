package credentials

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/jev"
)

// Vendor origins (D78; SPEC §7.5 host binding).
const (
	OriginOpenRouter = "https://openrouter.ai"
	OriginTypeSafe   = "https://api.typesafe.ai"
	OriginAnthropic  = "https://api.anthropic.com"
	OriginOpenAI     = "https://api.openai.com"
)

// URL error codes (FieldError.code of a refused URL).
const (
	CodeInvalidURL      = "invalid_url"
	CodeURLCredentials  = "url_credentials"
	CodeInsecureURL     = "insecure_url"
	CodeForbiddenTarget = "forbidden_target"
)

// URLError is a URL that CheckURL refused. Its message names the URL without userinfo or query,
// so it is safe to log and to return to a client.
type URLError struct {
	// Code is one of the Code* constants.
	Code string
	// URL is the refused URL, redacted.
	URL    string
	Reason string
}

func (e *URLError) Error() string { return fmt.Sprintf("%q %s", e.URL, e.Reason) }

// ErrProviderMismatch: the provider is not the one of the credential's name (D29).
var ErrProviderMismatch = errors.New("credentials: the provider does not match the credential name")

// ErrBaseURL: a base URL was given for a vendor credential (whose origins are fixed), or none for
// compatible or upstream-* (whose origin is fixed at creation from it) (D29).
var ErrBaseURL = errors.New("credentials: base_url")

// ErrUnknownName: not one of the credential names (D29).
var ErrUnknownName = errors.New("credentials: unknown credential name")

// Canonical returns the canonical origin scheme://host[:port] of an absolute URL (D78): scheme
// and host lowercased, the scheme's default port elided.
func Canonical(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &URLError{Code: CodeInvalidURL, URL: redactURL(raw), Reason: "is not an absolute URL"}
	}
	return jev.CanonicalOrigin(u), nil
}

// AllowedOrigins returns the sorted canonical origins the credential name may be sent to (D78):
//   - openrouter: https://openrouter.ai, plus the origin of openRouterBase
//     (TRUTHTABLE_OPENROUTER_BASE_URL) when it is set and passes CheckURL;
//   - typesafe, anthropic, openai: the vendor's origin;
//   - compatible and upstream-*: the origin of baseURL, which must pass CheckURL.
//
// provider must be the name's provider (ErrProviderMismatch). A vendor credential refuses a
// baseURL and a custom-origin one requires it (ErrBaseURL); a refused URL is a *URLError.
func AllowedOrigins(name, provider, baseURL, openRouterBase string) ([]string, error) {
	spec, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownName, name)
	}
	if provider != spec.Provider {
		return nil, fmt.Errorf("%w: %s takes provider %s", ErrProviderMismatch, name, spec.Provider)
	}
	baseURL = strings.TrimSpace(baseURL)
	if !spec.CustomOrigin {
		if baseURL != "" {
			return nil, fmt.Errorf("%w is accepted only for compatible and upstream credentials; %s always goes to its own API", ErrBaseURL, name)
		}
		return VendorOrigins(name, openRouterBase), nil
	}
	if baseURL == "" {
		return nil, fmt.Errorf("%w is required for %s: the key is sent only to that address", ErrBaseURL, name)
	}
	u, err := CheckURL(baseURL)
	if err != nil {
		return nil, err
	}
	return []string{jev.CanonicalOrigin(u)}, nil
}

// VendorOrigins is the fixed origin table of a vendor credential, recomputed at every load (D78);
// nil for compatible and upstream-*, whose origins are stored.
func VendorOrigins(name, openRouterBase string) []string {
	switch name {
	case NameOpenRouter:
		out := []string{OriginOpenRouter}
		if strings.TrimSpace(openRouterBase) != "" {
			if u, err := CheckURL(openRouterBase); err == nil {
				out = append(out, jev.CanonicalOrigin(u))
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	case NameTypeSafe:
		return []string{OriginTypeSafe}
	case NameAnthropic:
		return []string{OriginAnthropic}
	case NameOpenAI:
		return []string{OriginOpenAI}
	}
	return nil
}

// CheckURL accepts a URL a credential may be sent to (D78):
//   - https, or http for a local host only (loopback, localhost, *.localhost, or a single-label
//     host such as a compose service name);
//   - no userinfo (credentials belong in the key settings, never in a URL);
//   - no link-local or cloud-metadata target: 169.254.0.0/16, fe80::/10, fd00:ec2::254,
//     metadata.google.internal, nor a numeric host in a non-canonical IPv4 spelling (decimal,
//     hex or octal) that some resolvers map onto those addresses.
//
// A refusal is a *URLError whose message never carries userinfo or the query.
func CheckURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, &URLError{Code: CodeInvalidURL, URL: redactURL(raw), Reason: "is not an absolute URL"}
	}
	red := redactURL(raw)
	if u.User != nil {
		return nil, &URLError{Code: CodeURLCredentials, URL: red, Reason: "must not carry credentials; enter the key separately"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if forbiddenHost(host) {
		return nil, &URLError{Code: CodeForbiddenTarget, URL: red, Reason: "points at a link-local or cloud-metadata address, which never receives a credential"}
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !localHost(host) {
			return nil, &URLError{Code: CodeInsecureURL, URL: red, Reason: "must use https (http is allowed for localhost only)"}
		}
	default:
		return nil, &URLError{Code: CodeInsecureURL, URL: red, Reason: "must use https"}
	}
	return u, nil
}

// metadataV6 is the AWS instance-metadata IPv6 address (D78).
var metadataV6 = netip.MustParseAddr("fd00:ec2::254")

// forbiddenHost reports a link-local or metadata target (host lowercased, no trailing dot).
func forbiddenHost(host string) bool {
	if host == "metadata.google.internal" || host == "metadata" {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == metadataV6
	}
	return numericIPv4Spelling(host)
}

// numericIPv4Spelling reports a host that is not a canonical IP but that inet_aton-style
// resolvers read as an IPv4 address: every dot-separated label numeric, in decimal, octal or 0x
// hex ("2852039166", "0xa9fea9fe", "0251.0376.0251.0376"). Real host names always have a
// non-numeric label, so refusing these costs nothing.
func numericIPv4Spelling(host string) bool {
	labels := strings.Split(host, ".")
	if len(labels) > 4 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
		digits := l
		if strings.HasPrefix(l, "0x") {
			digits = l[2:]
			if digits == "" || strings.Trim(digits, "0123456789abcdef") != "" {
				return false
			}
			continue
		}
		if strings.Trim(digits, "0123456789") != "" {
			return false
		}
	}
	return true
}

// localHost reports loopback, localhost, *.localhost and single-label hosts (jev/endpoint.go).
func localHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return h != "" && !strings.Contains(h, ".") && !strings.Contains(h, ":")
}

// redactURL drops userinfo and the query from a URL for messages.
func redactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "<invalid URL>"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}
