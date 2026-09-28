package authz

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// HeaderCSRF must be "1" on every cookie-authenticated write (SPEC §2.4). A cross-site form cannot
// set it, and a cross-site fetch that sets it needs a CORS preflight the proxy only grants to
// allow-listed origins.
const HeaderCSRF = "X-TrustAgent-CSRF"

// CSRF errors.
var (
	ErrCSRFHeader = errors.New("authz: missing " + HeaderCSRF + ": 1")
	ErrCSRFOrigin = errors.New("authz: the request's Origin is not allowed")
)

// SafeMethod reports methods that never change state and so need no CSRF check.
func SafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// CheckCSRF checks a cookie-authenticated write: the CSRF header and an allow-listed Origin
// (TRUTHTABLE_ALLOWED_ORIGINS and the origin of TRUTHTABLE_PUBLIC_URL). A missing Origin fails:
// browsers send it on every non-GET request.
func (a *Authenticator) CheckCSRF(r *http.Request) error {
	if r.Header.Get(HeaderCSRF) != "1" {
		return ErrCSRFHeader
	}
	if !a.AllowsOrigin(r.Header.Get("Origin")) {
		return ErrCSRFOrigin
	}
	return nil
}

// CheckLoginCSRF checks a request that establishes a session (POST /api/session). A browser always
// sends an Origin on a POST: it must be allow-listed, which is what defeats login CSRF (a cross-site
// page cannot claim an allowed Origin). A request without an Origin (a non-browser client) needs
// the CSRF header instead. A cross-origin dashboard (DASHBOARD_API_BASE), whose fetch client sends
// no CSRF header, is admitted by its allow-listed Origin, so its setup claim works.
func (a *Authenticator) CheckLoginCSRF(r *http.Request) error {
	if o := r.Header.Get("Origin"); o != "" {
		if !a.AllowsOrigin(o) {
			return ErrCSRFOrigin
		}
		return nil
	}
	if r.Header.Get(HeaderCSRF) != "1" {
		return ErrCSRFHeader
	}
	return nil
}

// AllowsOrigin reports whether origin is allow-listed (TRUTHTABLE_ALLOWED_ORIGINS and the origin of
// TRUTHTABLE_PUBLIC_URL).
func (a *Authenticator) AllowsOrigin(origin string) bool {
	n := normalizeOrigin(origin)
	return n != "" && a.origins[n]
}

// normalizeOrigin reduces an origin or URL to scheme://host[:port], lowercase, without a default
// port; "" for anything that is not an http(s) origin (including "null").
func normalizeOrigin(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}
