package main

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
)

// REST error codes (SPEC §2.4).
const (
	codeBadRequest          = "bad_request"
	codeInvalidJSON         = "invalid_json"
	codeUnauthorized        = "unauthorized"
	codeForbidden           = "forbidden"
	codeNotFound            = "not_found"
	codeInternal            = "internal"
	codeValidationFailed    = "validation_failed"
	codeTooLarge            = "too_large"
	codePersistenceDisabled = "persistence_disabled"
	codeEngineUnavailable   = "engine_unavailable"
	codeQueueFull           = "queue_full"
)

// fieldError is api.ts FieldError.
type fieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// apiErrorBody is api.ts ApiErrorBody: {"error":{code,message,field_errors?}}.
type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code        string       `json:"code"`
	Message     string       `json:"message"`
	FieldErrors []fieldError `json:"field_errors,omitempty"`
}

// abortAPI writes a REST error and stops the handler chain.
func abortAPI(c *gin.Context, status int, code, message string, fields ...fieldError) {
	c.AbortWithStatusJSON(status, apiErrorBody{Error: apiError{Code: code, Message: message, FieldErrors: fields}})
}

// badParam answers 400 bad_request for one invalid query parameter.
func badParam(c *gin.Context, name, message string) {
	abortAPI(c, http.StatusBadRequest, codeBadRequest, "Invalid query parameter "+name+": "+message,
		fieldError{Path: name, Message: message})
}

// apiAuth authenticates /api and /ws callers (SPEC §2.4 auth sources and scopes) until the P2
// authz core adds sessions, cookies, tickets and api_keys rows:
//
//   - a key in X-TrustAgent-Key, or on /api also Authorization: Bearer or X-API-Key, grants the
//     scopes of that key (TRUTHTABLE_API_KEYS keys hold ingest, read and content); a key that is
//     presented but unknown is refused;
//   - without a key, reads are allowed when TRUTHTABLE_PUBLIC_READ is set (scope read; in demo
//     mode, whose data is scripted, also content), and ingest when TRUTHTABLE_INGEST_AUTH=open.
type apiAuth struct {
	keys       *authz.Authenticator
	publicRead bool
	demo       bool
}

// publicLabel is the identity label of an unauthenticated caller.
const publicLabel = "public"

// apiIdentityKey is the gin context key of the authenticated /api identity.
const apiIdentityKey = "trustagent.api_identity"

// presentedKey returns the key a request presents, and whether one was presented. bearer allows
// the /api-only sources (Authorization: Bearer, X-API-Key).
func presentedKey(r *http.Request, bearer bool) (string, bool) {
	if k := r.Header.Get(authz.HeaderKey); k != "" {
		return k, true
	}
	if !bearer {
		return "", false
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer "), true
	}
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k, true
	}
	return "", false
}

// identify returns the caller's identity for scope, or an error code and message.
func (a *apiAuth) identify(r *http.Request, scope authz.Scope, bearer bool) (*authz.Identity, int, string, string) {
	if secret, ok := presentedKey(r, bearer); ok {
		id, ok := a.keys.Authenticate(secret)
		if !ok {
			return nil, http.StatusUnauthorized, codeUnauthorized, "The presented key is not valid."
		}
		if !id.Has(scope) {
			return nil, http.StatusForbidden, codeForbidden, "This key does not have the " + string(scope) + " scope."
		}
		return id, 0, "", ""
	}
	switch {
	case scope == authz.ScopeRead && a.publicRead:
		scopes := []authz.Scope{authz.ScopeRead}
		if a.demo {
			scopes = append(scopes, authz.ScopeContent)
		}
		return &authz.Identity{Label: publicLabel, Scopes: scopes}, 0, "", ""
	case scope == authz.ScopeIngest && a.keys.IngestMode() == authz.IngestOpen:
		return &authz.Identity{Label: publicLabel, Scopes: []authz.Scope{authz.ScopeIngest}}, 0, "", ""
	}
	return nil, http.StatusUnauthorized, codeUnauthorized,
		"Authentication required: send a key in " + authz.HeaderKey + " (or set TRUTHTABLE_PUBLIC_READ=true for keyless reads)."
}

// require returns middleware that admits callers holding scope and stores their identity.
func (a *apiAuth) require(scope authz.Scope) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, status, code, msg := a.identify(c.Request, scope, true)
		if id == nil {
			abortAPI(c, status, code, msg)
			return
		}
		c.Set(apiIdentityKey, id)
		c.Next()
	}
}

// callerIdentity returns the identity require stored.
func callerIdentity(c *gin.Context) *authz.Identity {
	v, _ := c.Get(apiIdentityKey)
	id, _ := v.(*authz.Identity)
	return id
}

// canSeeContent reports whether the caller may see prompts, answers and claim text.
func canSeeContent(c *gin.Context) bool {
	return callerIdentity(c).Has(authz.ScopeContent)
}

// callerScopes lists the caller's scopes with the implied ones (admin > content > read).
func callerScopes(id *authz.Identity) []string {
	out := []string{}
	for _, s := range []authz.Scope{authz.ScopeIngest, authz.ScopeRead, authz.ScopeContent, authz.ScopeAdmin} {
		if id.Has(s) {
			out = append(out, string(s))
		}
	}
	return out
}

// queryLimit parses limit (1-200, default 50).
func queryLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return 50, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		badParam(c, "limit", "must be an integer from 1 to 200")
		return 0, false
	}
	return n, true
}

// queryTime parses an RFC3339 timestamp parameter; nil when absent.
func queryTime(c *gin.Context, name string) (*time.Time, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		badParam(c, name, "must be an RFC 3339 timestamp such as 2026-09-28T12:00:00Z")
		return nil, false
	}
	t = t.UTC()
	return &t, true
}

// queryEnum reads a parameter that must be one of allowed; "" when absent.
func queryEnum(c *gin.Context, name string, allowed ...string) (string, bool) {
	raw := c.Query(name)
	if raw == "" || slices.Contains(allowed, raw) {
		return raw, true
	}
	badParam(c, name, "must be one of "+strings.Join(allowed, ", "))
	return "", false
}
