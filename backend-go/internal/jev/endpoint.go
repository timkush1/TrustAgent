package jev

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// endpoint is where and how an HTTP backend sends decision requests.
type endpoint struct {
	backend string
	// url is the decision endpoint; host its host; origin its canonical origin
	// scheme://host[:port] (default port elided, host lowercased), which KeyFunc binds the
	// credential to (P4-PLAN D78).
	url, host, origin string
	// openRouter selects the OpenRouter body extras (session_id, provider preferences) and the
	// attribution headers.
	openRouter bool
	// keyURL is OpenRouter's GET /api/v1/key (key check and keep-warm); "" elsewhere.
	keyURL string
}

// resolveEndpoint builds the endpoint of an HTTP backend. The TRUTHTABLE_JEV_BASE_URL and _PATH
// overrides apply to compatible and to explicitly chosen vendor backends only.
func resolveEndpoint(backend string, explicit bool, cfg Config, b *Binding) (endpoint, []string, error) {
	orBase := strings.TrimRight(cfg.OpenRouterBaseURL, "/")
	if orBase == "" {
		orBase = DefaultOpenRouterBaseURL
	}
	base, path := "", cfg.Path
	if explicit {
		base = cfg.BaseURL
	}
	if !explicit {
		path = ""
	}
	var warnings []string
	ep := endpoint{backend: backend}
	switch backend {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
		ep.openRouter = true
		if base == "" {
			base = orBase + "/api"
		}
		if path == "" {
			path = PathDecisions
			if backend == BackendOpenRouterSystemOne {
				path = PathSystemOne
			}
		}
		if u, err := url.Parse(base); err == nil && strings.EqualFold(u.Hostname(), "openrouter.ai") && !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api") {
			warnings = append(warnings, "OpenRouter URLs end in /api: "+base+" will likely return a web page (bad_content_type); fix TRUTHTABLE_JEV_BASE_URL or leave it empty")
		}
		// The key check (and keep-warm) sends the decision credential, so it stays on the
		// decision endpoint's origin: {base}/v1/key for an .../api base, the OpenRouter key
		// endpoint when base is on the OpenRouter origin, and nothing for any other gateway.
		switch {
		case strings.HasSuffix(strings.TrimRight(base, "/"), "/api"):
			ep.keyURL = strings.TrimRight(base, "/") + PathKey
		case sameOrigin(base, orBase):
			ep.keyURL = orBase + "/api" + PathKey
		}
	case BackendTypeSafe:
		if base == "" {
			base = DefaultTypeSafeBaseURL
		}
		if path == "" {
			path = PathSystemOne
		}
	case BackendCompatible:
		base, path = cfg.BaseURL, cfg.Path
		if b != nil && b.BaseURL != "" && cfg.BaseURL == "" {
			base = b.BaseURL
		}
		if base == "" {
			return endpoint{}, nil, fmt.Errorf("the compatible backend needs TRUTHTABLE_JEV_BASE_URL (for Vercel AI Gateway: https://ai-gateway.vercel.sh/typesafe)")
		}
		if path == "" {
			path = PathSystemOne
		}
	default:
		return endpoint{}, nil, fmt.Errorf("%q is not an HTTP backend", backend)
	}
	full := joinURL(base, path)
	u, err := checkURL(full)
	if err != nil {
		return endpoint{}, nil, err
	}
	ep.url, ep.host, ep.origin = u.String(), u.Hostname(), CanonicalOrigin(u)
	if ep.keyURL != "" {
		// Defense in depth: the credential that KeyFunc releases for ep.origin never goes elsewhere.
		if ku, err := checkURL(ep.keyURL); err != nil || !sameOrigin(ku.String(), ep.url) {
			ep.keyURL = ""
		}
	}
	return ep, warnings, nil
}

// CanonicalOrigin is the origin of u as credential binding compares it (P4-PLAN D78):
// scheme://host[:port] with the scheme and host lowercased and the scheme's default port elided.
func CanonicalOrigin(u *url.URL) string {
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

// sameOrigin reports whether two absolute URLs share scheme, host and port.
func sameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && ua.Host != "" && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// joinURL joins a base URL and a path with exactly one slash between them.
func joinURL(base, path string) string {
	if path == "" {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// checkURL accepts https URLs, and http for local hosts (SPEC §2.7: https only, http for
// localhost). Local means loopback, localhost and single-label hosts such as a compose service
// name (mock-upstream). URLs carrying credentials are refused.
func checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("decision endpoint %q is not an absolute URL", redactURL(raw))
	}
	if u.User != nil {
		return nil, fmt.Errorf("decision endpoint %q must not carry credentials; use the key settings", redactURL(raw))
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !localHost(u.Hostname()) {
			return nil, fmt.Errorf("decision endpoint %q must use https (http is allowed for localhost only)", redactURL(raw))
		}
	default:
		return nil, fmt.Errorf("decision endpoint %q must use https", redactURL(raw))
	}
	return u, nil
}

func localHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return h != "" && !strings.Contains(h, ".")
}

// redactURL drops userinfo and the query from a URL for messages.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid URL>"
	}
	u.User, u.RawQuery = nil, ""
	return u.String()
}
