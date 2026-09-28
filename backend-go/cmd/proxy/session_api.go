package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
)

// REST error codes of the authz endpoints (SPEC §2.4).
const (
	codeCSRFFailed     = authz.CodeCSRFFailed
	codeSetupTokenUsed = "setup_token_used"
	codeRateLimited    = "rate_limited"
)

// newAuthenticator builds the authz core (SPEC §2.4, §7.5) from the configuration: env keys with
// their scopes, api_keys rows when the store supports them, the ingest mode (open is refused while
// an upstream credential is injected), sessions signed with the master key's session subkey
// (ephemeral when master is nil), and the CSRF origin allowlist.
func newAuthenticator(cfg *config.Config, st store.Store, master *authz.MasterKey, log *slog.Logger) *authz.Authenticator {
	var keys []authz.Key
	add := func(list []config.APIKey, scopes []authz.Scope) {
		for _, k := range list {
			keys = append(keys, authz.Key{Label: k.Label, Secret: k.Key, Scopes: scopes})
		}
	}
	add(cfg.APIKeys, authz.APIKeyScopes)
	add(cfg.Auth.ReadKeys, authz.ReadKeyScopes)
	add(cfg.Auth.AdminKeys, authz.AdminKeyScopes)

	var as authz.Store
	if s, ok := st.(authz.Store); ok && st != nil {
		as = s
	}
	if master == nil {
		master = authz.EphemeralMasterKey()
	}
	auth := authz.NewAuthenticator(authz.Options{
		Keys: keys, IngestAuth: cfg.IngestAuth, Injecting: cfg.Injecting(), Store: as,
		SessionKey: master.Derive(authz.InfoSession), SessionTTL: cfg.Auth.SessionTTL,
		Sliding: isLocalURL(cfg.PublicURL), SecureCookies: secureCookies(cfg),
		AllowedOrigins: cfg.AllowedOrigins, SetupTTL: cfg.Auth.SetupTokenTTL, PublicURL: cfg.PublicURL,
		Logger: log,
	})
	switch {
	case auth.IngestMode() == authz.IngestOpen:
		log.Warn("TRUTHTABLE_INGEST_AUTH=open: /v1 accepts requests without an ingest key")
	case !auth.HasIngestKeys() && as == nil:
		log.Warn("no ingest keys: /v1 answers 401 until TRUTHTABLE_API_KEYS (label=key) is set, or TRUTHTABLE_INGEST_AUTH=open")
	default:
		log.Info("/v1 ingest auth: TrustAgent keys", "env_keys", len(keys), "dashboard_keys", as != nil, "inject_mode", auth.Injecting())
	}
	return auth
}

// loadMasterKey loads (or generates) the master key for session signing; when that fails,
// sessions use an ephemeral key and end at the next restart.
func loadMasterKey(cfg *config.Config, log *slog.Logger) *authz.MasterKey {
	m, err := authz.LoadMasterKey(cfg.Auth.SecretKey, cfg.Auth.SecretKeyFile)
	if err != nil {
		log.Warn("master key unavailable: dashboard sessions end when the proxy restarts", "err", err)
		return authz.EphemeralMasterKey()
	}
	log.Info("master key loaded", "source", m.Source, "key_id", m.ID)
	return m
}

// isLocalURL reports a public URL on localhost, where sessions slide for 30 days (SPEC §7.5).
func isLocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

// secureCookies applies TRUTHTABLE_COOKIE_SECURE: auto means Secure when the public URL is https.
func secureCookies(cfg *config.Config) bool {
	switch cfg.Auth.CookieSecure {
	case "true":
		return true
	case "false":
		return false
	}
	return strings.HasPrefix(strings.ToLower(cfg.PublicURL), "https://")
}

// resolve authenticates an /api request (Bearer and X-API-Key allowed when bearer is set; the
// surface of its path otherwise).
func (a *apiAuth) resolve(r *http.Request, bearer bool) authz.Result {
	s := authz.SurfaceOf(r.URL.Path)
	if bearer {
		s = authz.SurfaceAPI
	}
	return a.keys.Resolve(r, s)
}

// setupRequired reports whether no admin credential exists (false when that cannot be checked).
func (a *apiAuth) setupRequired(ctx context.Context) bool {
	required, err := a.keys.Setup().Required(ctx)
	return err == nil && required
}

// authzAPI serves the setup, session, WebSocket-ticket and key endpoints (SPEC §2.4 #3-7, #67-69).
type authzAPI struct {
	auth *authz.Authenticator
	api  *apiAuth
	cfg  *config.Config
	log  *slog.Logger
	now  func() time.Time
}

// mountAuthz registers the authz endpoints on /api. Readonly mode leaves session and ws-ticket
// writable (SPEC §2.4); key writes need writable.
func (s *server) mountAuthz(api *gin.RouterGroup, rateLimit, bodyLimit, writable gin.HandlerFunc) {
	h := &authzAPI{auth: s.gateway.auth, api: s.auth, cfg: s.cfg, log: s.log, now: time.Now}
	api.GET("/setup/status", rateLimit, h.setupStatus)
	api.POST("/session", rateLimit, bodyLimit, h.createSession)
	api.GET("/session", rateLimit, h.getSession)
	api.DELETE("/session", rateLimit, h.deleteSession)
	api.POST("/ws-ticket", s.auth.require(authz.ScopeRead), rateLimit, h.wsTicket)
	admin := s.auth.require(authz.ScopeAdmin)
	api.GET("/keys", admin, rateLimit, h.listKeys)
	api.POST("/keys", writable, admin, rateLimit, bodyLimit, h.createKey)
	api.DELETE("/keys/:id", writable, admin, rateLimit, h.revokeKey)
}

// sessionInfo is api.ts SessionInfo.
type sessionInfo struct {
	Actor           string   `json:"actor"`
	Scopes          []string `json:"scopes"`
	ExpiresAt       string   `json:"expires_at"`
	Sliding         bool     `json:"sliding"`
	CreatedAdminKey string   `json:"created_admin_key,omitempty"`
}

// info describes an identity. A key-authenticated caller has no session: its expires_at is the
// response time, so a client treats it as "send the key on every request".
func (h *authzAPI) info(id *authz.Identity, createdAdminKey string) sessionInfo {
	exp := h.now()
	sliding := false
	if id.Kind == authz.KindSession {
		exp = id.ExpiresAt
		sliding = h.auth.Sessions() != nil && h.auth.Sessions().Sliding()
	}
	return sessionInfo{
		Actor: id.Label, Scopes: callerScopes(id), ExpiresAt: exp.UTC().Format(time.RFC3339Nano),
		Sliding: sliding, CreatedAdminKey: createdAdminKey,
	}
}

// decodeAuthzBody decodes a JSON object body, answering 413 or 400 invalid_json itself.
func decodeAuthzBody(c *gin.Context, v any, what string) bool {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, tail := dec.Token(); !errors.Is(tail, io.EOF) {
			err = errors.New("trailing data after the JSON object")
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			abortAPI(c, http.StatusRequestEntityTooLarge, codeTooLarge, "The request body is too large.")
			return false
		}
		abortAPI(c, http.StatusBadRequest, codeInvalidJSON, "The body is not a "+what+" JSON object.")
		return false
	}
	return true
}

// noStore marks a response that carries a credential or a session as not cacheable.
func noStore(c *gin.Context) { c.Header("Cache-Control", "no-store") }

// createSession serves POST /api/session: {setup_token} claims the setup token (creating the
// setup-admin key, returned once as created_admin_key), {key} signs in with a key that has the
// read scope. Either way the ta_session cookie is set.
func (h *authzAPI) createSession(c *gin.Context) {
	if err := h.auth.CheckLoginCSRF(c.Request); err != nil {
		abortAPI(c, http.StatusForbidden, codeCSRFFailed, "Signing in needs the "+authz.HeaderCSRF+": 1 header, from an allowed Origin.")
		return
	}
	var body struct {
		SetupToken *string `json:"setup_token"`
		Key        *string `json:"key"`
	}
	if !decodeAuthzBody(c, &body, "{setup_token} or {key}") {
		return
	}
	hasToken, hasKey := body.SetupToken != nil && *body.SetupToken != "", body.Key != nil && *body.Key != ""
	if hasToken == hasKey {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "Send exactly one of setup_token and key.",
			fieldError{Path: "setup_token", Message: "send either setup_token or key"},
			fieldError{Path: "key", Message: "send either setup_token or key"})
		return
	}
	ctx := c.Request.Context()
	// The setup claim creates the only copy of the admin key: make sure a session can be issued
	// before claiming, so a failure afterwards is unlikely (and still returns the key, below).
	if err := h.auth.SessionsReady(ctx); err != nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "Sessions cannot be started right now: the database is unavailable.")
		return
	}
	var (
		id      *authz.Identity
		created string
	)
	if hasToken {
		var err error
		id, created, err = h.auth.Setup().Claim(ctx, *body.SetupToken)
		if err != nil {
			h.setupClaimFailed(c, err)
			return
		}
		h.log.Info("dashboard setup claimed: the setup-admin key was created", "key_id", id.KeyID)
	} else {
		var err error
		id, err = h.auth.AuthenticateContext(ctx, *body.Key)
		switch {
		case errors.Is(err, authz.ErrUnavailable):
			abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "Keys created in the dashboard cannot be checked right now: the database is unavailable.")
			return
		case err != nil:
			abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "The key is not valid.")
			return
		case !id.Has(authz.ScopeRead):
			abortAPI(c, http.StatusForbidden, codeForbidden, "This key cannot open a dashboard session: it does not have the read scope.")
			return
		}
	}
	cookie, sess, err := h.auth.IssueSession(ctx, id)
	if err != nil {
		h.log.Error("session not issued", "err", err)
		if created != "" {
			// The claim succeeded: never lose the admin key. No cookie is set; the dashboard signs
			// in with the key (POST /api/session {key}) once the database answers again.
			noStore(c)
			c.JSON(http.StatusOK, h.info(id, created))
			return
		}
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "The session cannot be started right now.")
		return
	}
	http.SetCookie(c.Writer, cookie)
	noStore(c)
	c.JSON(http.StatusOK, h.info(sess, created))
}

// setupClaimFailed answers a failed setup claim.
func (h *authzAPI) setupClaimFailed(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authz.ErrSetupTokenUsed):
		abortAPI(c, http.StatusGone, codeSetupTokenUsed, "This setup token was already used. Sign in with the admin key it created.")
	case errors.Is(err, authz.ErrSetupTokenExpired):
		abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "The setup token expired. Print a new setup URL with `make setup-url`.")
	case errors.Is(err, authz.ErrSetupComplete):
		abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "Setup is complete: sign in with an admin key. Lost it? Run: "+recoveryCommand)
	case errors.Is(err, authz.ErrSetupTokenInvalid):
		abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "The setup token is not valid.")
	case errors.Is(err, authz.ErrSetupNeedsStore):
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"The setup token needs a database (TRUTHTABLE_DATABASE_URL). Without one, set TRUTHTABLE_ADMIN_KEYS and sign in with that key.")
	default:
		h.log.Error("setup claim failed", "err", err)
		abortAPI(c, http.StatusInternalServerError, codeInternal, "The setup token could not be claimed.")
	}
}

// getSession serves GET /api/session: the caller's identity and session.
func (h *authzAPI) getSession(c *gin.Context) {
	res := h.api.resolve(c.Request, true)
	if res.Refresh != nil {
		http.SetCookie(c.Writer, res.Refresh)
	}
	if res.Err != nil {
		abortAPI(c, res.Err.Status, res.Err.Code, res.Err.Message)
		return
	}
	if res.Identity == nil {
		if res.StaleCookie {
			http.SetCookie(c.Writer, h.auth.ClearCookie())
		}
		abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "Not signed in.")
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, h.info(res.Identity, ""))
}

// deleteSession serves DELETE /api/session: sign out (the cookie is cleared). ?all=true (admin)
// bumps the session epoch, which ends every session on every replica.
func (h *authzAPI) deleteSession(c *gin.Context) {
	res := h.api.resolve(c.Request, true)
	if res.Err != nil {
		abortAPI(c, res.Err.Status, res.Err.Code, res.Err.Message)
		return
	}
	if all := c.Query("all"); all == "true" || all == "1" {
		id := res.Identity
		switch {
		case id == nil:
			abortAPI(c, http.StatusUnauthorized, codeUnauthorized, "Sign in as an admin to end every session.")
			return
		case !id.Has(authz.ScopeAdmin):
			abortAPI(c, http.StatusForbidden, codeForbidden, "Ending every session needs the admin scope.")
			return
		}
		if _, err := h.auth.RevokeAllSessions(c.Request.Context(), id.Label); err != nil {
			h.log.Error("sessions not revoked", "err", err)
			abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled, "Sessions cannot be ended right now: the database is unavailable.")
			return
		}
		h.log.Info("every dashboard session was ended", "actor", id.Label)
	}
	http.SetCookie(c.Writer, h.auth.ClearCookie())
	c.Status(http.StatusNoContent)
}

// wsTicket serves POST /api/ws-ticket: a single-use ticket for /ws?ticket=, valid 30 s.
func (h *authzAPI) wsTicket(c *gin.Context) {
	ticket, err := h.auth.Tickets().Issue(callerIdentity(c))
	if err != nil {
		c.Header("Retry-After", "30")
		abortAPI(c, http.StatusTooManyRequests, codeRateLimited, "Too many WebSocket tickets are outstanding; retry shortly.")
		return
	}
	noStore(c)
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_in": int(h.auth.Tickets().TTL().Seconds())})
}
