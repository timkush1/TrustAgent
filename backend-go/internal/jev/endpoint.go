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
	// url is the decision endpoint; host its host (for KeyFunc host binding).
	url, host string
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
		// The key check follows the decision base when it is an .../api base, else OpenRouter.
		keyBase := orBase + "/api"
		if strings.HasSuffix(strings.TrimRight(base, "/"), "/api") {
			keyBase = strings.TrimRight(base, "/")
		}
		ep.keyURL = keyBase + PathKey
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
	ep.url, ep.host = u.String(), u.Hostname()
	if ep.keyURL != "" {
		if _, err := checkURL(ep.keyURL); err != nil {
			return endpoint{}, nil, err
		}
	}
	return ep, warnings, nil
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
