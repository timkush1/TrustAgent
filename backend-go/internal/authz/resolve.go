package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Surface is the part of the proxy a request addresses; it decides which auth sources apply.
type Surface int

// Surfaces.
const (
	// SurfaceAPI is /api/*: Bearer and X-API-Key are TrustAgent credentials there.
	SurfaceAPI Surface = iota
	// SurfaceV1 is the LLM API: Authorization belongs to the upstream unless a credential is
	// injected and the value is a proxy key.
	SurfaceV1
	// SurfaceWS is /ws: ?ticket= is accepted.
	SurfaceWS
)

// SurfaceOf returns the surface of a request path.
func SurfaceOf(path string) Surface {
	switch {
	case path == "/ws" || strings.HasPrefix(path, "/ws/"):
		return SurfaceWS
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return SurfaceV1
	}
	return SurfaceAPI
}

// CredentialSource names where a request's credential came from.
type CredentialSource string

// Credential sources (SPEC §2.4, in precedence order).
const (
	SourceNone           CredentialSource = ""
	SourceHeaderKey      CredentialSource = "x-trustagent-key"
	SourceBearer         CredentialSource = "bearer"
	SourceAPIKeyHeader   CredentialSource = "x-api-key"
	SourceInjectedBearer CredentialSource = "inject"
	SourceCookie         CredentialSource = "cookie"
	SourceTicket         CredentialSource = "ticket"
)

// Failure is an authentication failure to answer with.
type Failure struct {
	Status  int
	Code    string
	Message string
}

// Result is the outcome of Resolve.
type Result struct {
	// Identity is the authenticated caller; nil when no credential was presented (or only a stale
	// session cookie).
	Identity *Identity
	Source   CredentialSource
	// Err is set when a credential was presented and refused.
	Err *Failure
	// Refresh is a renewed session cookie to set on the response (sliding sessions).
	Refresh *http.Cookie
	// StaleCookie is set when a session cookie was presented that no longer authenticates
	// (expired, revoked, tampered with); callers clear it when they refuse the request.
	StaleCookie bool
}

var (
	failInvalidKey  = &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: "The presented key is not valid."}
	failUnavailable = &Failure{Status: http.StatusServiceUnavailable, Code: CodeUnavailable,
		Message: "Keys created in the dashboard cannot be checked right now: the database is unavailable."}
	failTicket = &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthorized,
		Message: "The WebSocket ticket is not valid: it expired or was already used. Request a new one with POST /api/ws-ticket."}
)

// Resolve authenticates a request from the first credential it presents (SPEC §2.4):
//
//  1. X-TrustAgent-Key;
//  2. on /api only, Authorization: Bearer or X-API-Key;
//  3. on /v1 in inject mode, Bearer or x-api-key, but only when the value equals a proxy key;
//  4. the ta_session cookie (writes need the CSRF header and an allow-listed Origin);
//  5. on /ws only, ?ticket=.
//
// A presented credential that is refused is an error, never a fall-through to a weaker source; a
// session cookie that no longer authenticates counts as absent (StaleCookie).
func (a *Authenticator) Resolve(r *http.Request, s Surface) Result {
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	defer cancel()

	if k := r.Header.Get(HeaderKey); k != "" {
		return a.keyResult(ctx, k, SourceHeaderKey)
	}
	bearer, hasBearer := bearerToken(r.Header.Get("Authorization"))
	if s == SurfaceAPI {
		if hasBearer {
			return a.keyResult(ctx, bearer, SourceBearer)
		}
		if k := r.Header.Get("X-API-Key"); k != "" {
			return a.keyResult(ctx, k, SourceAPIKeyHeader)
		}
	}
	if s == SurfaceV1 && a.injecting.Load() {
		for _, v := range []string{bearer, r.Header.Get("X-Api-Key")} {
			if v == "" {
				continue
			}
			id, err := a.AuthenticateContext(ctx, v)
			switch {
			case err == nil:
				return Result{Identity: id, Source: SourceInjectedBearer}
			case errors.Is(err, ErrUnavailable):
				return Result{Source: SourceInjectedBearer, Err: failUnavailable}
			}
			// Not a proxy key: the client's own credential, which inject mode strips.
		}
	}
	stale := false
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if a.sessions == nil {
			stale = true
		} else {
			id, refresh, err := a.sessionIdentity(ctx, c.Value)
			switch {
			case err == nil:
				if !SafeMethod(r.Method) {
					if err := a.CheckCSRF(r); err != nil {
						return Result{Source: SourceCookie, Err: &Failure{
							Status: http.StatusForbidden, Code: CodeCSRFFailed,
							Message: "Cookie-authenticated writes need the " + HeaderCSRF + ": 1 header and an allowed Origin.",
						}}
					}
				}
				return Result{Identity: id, Source: SourceCookie, Refresh: refresh}
			case errors.Is(err, ErrUnavailable):
				return Result{Source: SourceCookie, Err: failUnavailable}
			default:
				stale = true
			}
		}
	}
	if s == SurfaceWS {
		if t := r.URL.Query().Get("ticket"); t != "" {
			if id, ok := a.tickets.Consume(t); ok {
				return Result{Identity: id, Source: SourceTicket, StaleCookie: stale}
			}
			return Result{Source: SourceTicket, Err: failTicket, StaleCookie: stale}
		}
	}
	return Result{StaleCookie: stale}
}

// keyResult authenticates a presented key.
func (a *Authenticator) keyResult(ctx context.Context, secret string, src CredentialSource) Result {
	id, err := a.AuthenticateContext(ctx, secret)
	switch {
	case err == nil:
		return Result{Identity: id, Source: src}
	case errors.Is(err, ErrUnavailable):
		return Result{Source: src, Err: failUnavailable}
	}
	return Result{Source: src, Err: failInvalidKey}
}

// bearerToken extracts the token of an Authorization: Bearer header (scheme case-insensitive).
func bearerToken(h string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
