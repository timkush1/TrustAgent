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

	// Shared by several endpoint families (moved here from the per-family files).
	codeNoJudge              = "no_judge"
	codeConflict             = "conflict"
	codeFileManaged          = "file_managed"
	codeStaleVersion         = "stale_version"
	codePreconditionRequired = "precondition_required"

	// Settings, credentials, costs and Studio (SPEC §2.4 error codes).
	codeEnvManaged               = "env_managed"
	codeIngestKeyRequired        = "ingest_key_required"
	codeConfirmationRequired     = "confirmation_required"
	codeBodyExpired              = "body_expired"
	codeInvalidNonce             = "invalid_nonce"
	codeBudgetExhausted          = "budget_exhausted"
	codeCredentialsUnavailable   = "credentials_unavailable"
	codeDecisionModelUnavailable = "decision_model_unavailable"
)

// apiRedact masks secrets in every REST error message (D42). It is the identity until
// the credential store's redactor is wired (GSEC sets it to the process Redactor).
var apiRedact = func(s string) string { return s }

// fieldError is api.ts FieldError. Code and Details let the web render words by code, e.g.
// reenter_secret with {names} (D37) or fallback_key_required with {host} (D41).
type fieldError struct {
	Path    string         `json:"path"`
	Message string         `json:"message"`
	Code    string         `json:"code,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// apiErrorBody is api.ts ApiErrorBody: {"error":{code,message,field_errors?,details?}}.
type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	FieldErrors []fieldError   `json:"field_errors,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

// abortAPI writes a REST error and stops the handler chain. The message passes through
// apiRedact.
func abortAPI(c *gin.Context, status int, code, message string, fields ...fieldError) {
	abortAPIDetails(c, status, code, message, nil, fields...)
}

// abortAPIDetails is abortAPI with ApiErrorBody.error.details, e.g. {routes} on 409
// confirmation_required (D77) or {fields, keys} on 409 env_managed (D33). Details carry names
// and codes only, never secrets.
func abortAPIDetails(c *gin.Context, status int, code, message string, details map[string]any, fields ...fieldError) {
	var redacted []fieldError
	if len(fields) > 0 {
		redacted = make([]fieldError, len(fields))
		for i, f := range fields {
			f.Message = apiRedact(f.Message)
			redacted[i] = f
		}
	}
	c.AbortWithStatusJSON(status, apiErrorBody{Error: apiError{
		Code: code, Message: apiRedact(message), FieldErrors: redacted, Details: details,
	}})
}

// badParam answers 400 bad_request for one invalid query parameter.
func badParam(c *gin.Context, name, message string) {
	abortAPI(c, http.StatusBadRequest, codeBadRequest, "Invalid query parameter "+name+": "+message,
		fieldError{Path: name, Message: message})
}

// apiAuth authenticates /api and /ws callers through the authz core (SPEC §2.4 auth sources and
// scopes; see authz.Resolve):
//
//   - a credential (X-TrustAgent-Key; on /api also Authorization: Bearer or X-API-Key; the
//     ta_session cookie, whose writes need the CSRF header; on /ws a ?ticket=) grants its scopes; a
//     credential that is presented but refused is refused, never downgraded to public access;
//   - without one, reads are allowed when TRUTHTABLE_PUBLIC_READ is set (scope read; in demo mode,
//     whose data is scripted, also content), and ingest when TRUTHTABLE_INGEST_AUTH=open is in
//     effect.
type apiAuth struct {
	keys       *authz.Authenticator
	publicRead bool
	demo       bool
}

// publicLabel is the identity label of an unauthenticated caller.
const publicLabel = "public"

// apiIdentityKey is the gin context key of the authenticated /api identity.
const apiIdentityKey = "trustagent.api_identity"

// identify returns the caller's identity for scope, or an error code and message. bearer selects
// the /api sources (Bearer, X-API-Key); otherwise the request path decides (/ws accepts tickets).
func (a *apiAuth) identify(r *http.Request, scope authz.Scope, bearer bool) (*authz.Identity, int, string, string) {
	return a.decide(a.resolve(r, bearer), scope)
}

// decide applies scope to a resolved request.
func (a *apiAuth) decide(res authz.Result, scope authz.Scope) (*authz.Identity, int, string, string) {
	if res.Err != nil {
		return nil, res.Err.Status, res.Err.Code, res.Err.Message
	}
	if id := res.Identity; id != nil {
		if !id.Has(scope) {
			who := "key"
			if id.Kind == authz.KindSession || id.Kind == authz.KindTicket {
				who = "session"
			}
			return nil, http.StatusForbidden, codeForbidden, "This " + who + " does not have the " + string(scope) + " scope."
		}
		return id, 0, "", ""
	}
	switch {
	case scope == authz.ScopeRead && a.publicRead:
		scopes := []authz.Scope{authz.ScopeRead}
		if a.demo {
			scopes = append(scopes, authz.ScopeContent)
		}
		return &authz.Identity{Label: publicLabel, Scopes: scopes, Kind: authz.KindPublic}, 0, "", ""
	case scope == authz.ScopeIngest && a.keys.IngestMode() == authz.IngestOpen:
		return &authz.Identity{Label: publicLabel, Scopes: []authz.Scope{authz.ScopeIngest}, Kind: authz.KindPublic}, 0, "", ""
	}
	return nil, http.StatusUnauthorized, codeUnauthorized,
		"Authentication required: send a key in " + authz.HeaderKey + " or sign in (or set TRUTHTABLE_PUBLIC_READ=true for keyless reads)."
}

// require returns middleware that admits callers holding scope and stores their identity. It
// renews a sliding session cookie and clears a stale one it refuses.
func (a *apiAuth) require(scope authz.Scope) gin.HandlerFunc {
	return func(c *gin.Context) {
		res := a.resolve(c.Request, true)
		if res.Refresh != nil {
			http.SetCookie(c.Writer, res.Refresh)
		}
		id, status, code, msg := a.decide(res, scope)
		if id == nil {
			if res.StaleCookie {
				http.SetCookie(c.Writer, a.keys.ClearCookie())
			}
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
