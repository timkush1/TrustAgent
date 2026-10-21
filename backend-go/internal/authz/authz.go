// Package authz authenticates TrustAgent callers and decides what they may do (SPEC §2.4, §7.5).
//
// Credentials:
//   - env keys: TRUTHTABLE_API_KEYS (ingest, read, content), TRUTHTABLE_READ_KEYS (read, content)
//     and TRUTHTABLE_ADMIN_KEYS (every scope), compared in constant time over SHA-256 digests;
//   - api_keys rows (migration 0004), looked up by the key's SHA-256 with a short cache, so a key
//     revoked on another replica stops working within RevalidateEvery;
//   - ta_session cookies: HMAC-signed {sid, sub, scp, iat, exp, ep}, re-validated on every request
//     (the subject must still exist and the session epoch must be current; see Sessions);
//   - single-use WebSocket tickets (see Tickets);
//   - the one-time setup token, which creates the first admin key (see Setup).
//
// Auth sources, first match wins (SPEC §2.4): X-TrustAgent-Key; on /api also Authorization: Bearer
// or X-API-Key; on /v1 in inject mode Bearer or x-api-key when the value equals a proxy key; the
// ta_session cookie; on /ws the ?ticket= parameter. Cookie-authenticated writes need the
// X-TrustAgent-CSRF: 1 header and an allow-listed Origin.
//
// TRUTHTABLE_INGEST_AUTH is keys (the default) or open; open is never in effect while an upstream
// credential is injected. A proxy key is never forwarded upstream (B1): IsProxyKey recognizes env
// keys and every minted key by its shape, without a database round trip.
package authz

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// HeaderKey is the request header that carries a TrustAgent key.
const HeaderKey = "X-TrustAgent-Key"

// Scope is an access scope.
type Scope string

// Scopes (SPEC §2.4). Admin grants everything; content implies read.
const (
	ScopeIngest  Scope = "ingest"
	ScopeRead    Scope = "read"
	ScopeContent Scope = "content"
	ScopeAdmin   Scope = "admin"
)

// AllScopes lists the scopes in their canonical order.
var AllScopes = []Scope{ScopeIngest, ScopeRead, ScopeContent, ScopeAdmin}

// Scopes of the env key lists (SPEC §2.4).
var (
	// APIKeyScopes are the scopes of a TRUTHTABLE_API_KEYS entry.
	APIKeyScopes = []Scope{ScopeIngest, ScopeRead, ScopeContent}
	// ReadKeyScopes are the scopes of a TRUTHTABLE_READ_KEYS entry.
	ReadKeyScopes = []Scope{ScopeRead, ScopeContent}
	// AdminKeyScopes are the scopes of a TRUTHTABLE_ADMIN_KEYS entry.
	AdminKeyScopes = []Scope{ScopeAdmin}
)

// ParseScope returns the scope s names.
func ParseScope(s string) (Scope, bool) {
	for _, sc := range AllScopes {
		if string(sc) == s {
			return sc, true
		}
	}
	return "", false
}

// grants reports whether holding have grants want: admin grants everything, content implies read.
func grants(have, want Scope) bool {
	return have == want || have == ScopeAdmin || (have == ScopeContent && want == ScopeRead)
}

// Expand lists the scopes a scope set grants, implied ones included, in canonical order.
func Expand(scopes []Scope) []Scope {
	out := []Scope{}
	for _, want := range AllScopes {
		for _, have := range scopes {
			if grants(have, want) {
				out = append(out, want)
				break
			}
		}
	}
	return out
}

// Intersect returns the scopes (expanded) that both a and b grant.
func Intersect(a, b []Scope) []Scope {
	out := []Scope{}
	ea, eb := Expand(a), Expand(b)
	for _, s := range ea {
		if slices.Contains(eb, s) {
			out = append(out, s)
		}
	}
	return out
}

// Ingest auth modes (TRUTHTABLE_INGEST_AUTH).
const (
	IngestKeys = "keys"
	IngestOpen = "open"
)

// Key is a configured env key.
type Key struct {
	Label  string
	Secret string
	Scopes []Scope
}

// IdentityKind says how a caller authenticated.
type IdentityKind string

// Identity kinds.
const (
	KindEnvKey  IdentityKind = "env_key"
	KindAPIKey  IdentityKind = "api_key"
	KindSession IdentityKind = "session"
	KindTicket  IdentityKind = "ticket"
	KindPublic  IdentityKind = "public"
)

// Identity is an authenticated caller.
type Identity struct {
	Label  string
	Scopes []Scope

	// Kind is how the caller authenticated ("" in identities built by S0 callers).
	Kind IdentityKind
	// Subject is the stable principal a session is bound to: "env:<label>#<fingerprint>" for an
	// env key, "key:<id>" for an api_keys row.
	Subject string
	// KeyID is the api_keys id for KindAPIKey identities and sessions opened with such a key.
	KeyID string
	// SessionID is the sid of a session identity.
	SessionID string
	// ExpiresAt is when a session identity ends.
	ExpiresAt time.Time
}

// Has reports whether the identity holds scope s. Admin grants everything; content implies read.
func (id *Identity) Has(s Scope) bool {
	if id == nil {
		return false
	}
	for _, have := range id.Scopes {
		if grants(have, s) {
			return true
		}
	}
	return false
}

// RateKey is the rate-limit bucket of the identity (SPEC §2.4: per key id, per session id for
// cookie auth); "" for callers limited by IP.
func (id *Identity) RateKey() string {
	switch {
	case id == nil:
		return ""
	case id.SessionID != "":
		return "session:" + id.SessionID
	case id.Subject != "":
		return id.Subject
	}
	return ""
}

type entry struct {
	id     Identity
	digest [sha256.Size]byte
}

// Authenticator checks credentials. The zero value is not usable; build one with New or
// NewAuthenticator.
type Authenticator struct {
	keys      []entry
	subjects  map[string]subjectInfo // env subjects → label and union scopes
	ingest    string
	injecting atomic.Bool
	origins   map[string]bool
	now       func() time.Time
	log       *slog.Logger

	store    Store
	cache    *keyCache
	sessions *Sessions
	tickets  *Tickets
	epoch    *epochCache
	setup    *Setup

	touchMu sync.Mutex
	touched map[string]time.Time
	touchWG sync.WaitGroup
}

type subjectInfo struct {
	label  string
	scopes []Scope
}

// Options configure NewAuthenticator.
type Options struct {
	// Keys are the env keys with their scopes (APIKeyScopes, ReadKeyScopes, AdminKeyScopes).
	Keys []Key
	// IngestAuth is TRUTHTABLE_INGEST_AUTH; anything but open means keys.
	IngestAuth string
	// Injecting is set while an upstream credential is injected: open ingest is then refused, and
	// on /v1 a Bearer or x-api-key equal to a proxy key authenticates.
	Injecting bool
	// Store backs api_keys, the setup token and the session epoch; nil means env keys only, no
	// setup token and an in-memory epoch.
	Store Store
	// SessionKey is the HMAC key of ta_session cookies (MasterKey.Derive(InfoSession)); nil
	// disables cookie sessions.
	SessionKey []byte
	// SessionTTL is TRUTHTABLE_SESSION_TTL (default 12h).
	SessionTTL time.Duration
	// Sliding selects 30-day sliding sessions (TRUTHTABLE_PUBLIC_URL on localhost).
	Sliding bool
	// SecureCookies sets the Secure attribute on the session cookie.
	SecureCookies bool
	// AllowedOrigins are the Origins cookie-authenticated writes may come from.
	AllowedOrigins []string
	// SetupTTL is TRUTHTABLE_SETUP_TOKEN_TTL (default 1h).
	SetupTTL time.Duration
	// PublicURL is TRUTHTABLE_PUBLIC_URL, the base of the setup URL.
	PublicURL string
	// Now is the clock (tests); default time.Now.
	Now func() time.Time
	// Logger receives operational messages (never secrets); default slog.Default().
	Logger *slog.Logger
}

// New builds an Authenticator from env keys alone (the S0 constructor). An empty or unknown ingest
// mode means keys, the secure default.
func New(keys []Key, ingestAuth string) *Authenticator {
	return NewAuthenticator(Options{Keys: keys, IngestAuth: ingestAuth})
}

// NewAuthenticator builds an Authenticator.
func NewAuthenticator(o Options) *Authenticator {
	a := &Authenticator{
		ingest: IngestKeys, now: o.Now, log: o.Logger, store: o.Store,
		subjects: map[string]subjectInfo{}, origins: map[string]bool{}, touched: map[string]time.Time{},
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if o.IngestAuth == IngestOpen {
		a.ingest = IngestOpen
	}
	a.injecting.Store(o.Injecting)
	for _, origin := range o.AllowedOrigins {
		if n := normalizeOrigin(origin); n != "" {
			a.origins[n] = true
		}
	}
	if n := normalizeOrigin(o.PublicURL); n != "" {
		a.origins[n] = true
	}

	subjectKey := o.SessionKey
	if len(subjectKey) == 0 {
		subjectKey = randomBytes(32)
	}
	// A secret listed twice (for example in _API_KEYS and _ADMIN_KEYS) is one subject with the union
	// of the scopes, named after its first label, as envIdentity reports it.
	subOf := map[[sha256.Size]byte]string{}
	for _, k := range o.Keys {
		if k.Secret == "" {
			continue
		}
		digest := sha256.Sum256([]byte(k.Secret))
		sub, seen := subOf[digest]
		if !seen {
			sub = envSubject(subjectKey, k.Label, k.Secret)
			subOf[digest] = sub
		}
		a.keys = append(a.keys, entry{
			id:     Identity{Label: k.Label, Scopes: slices.Clone(k.Scopes), Kind: KindEnvKey, Subject: sub},
			digest: digest,
		})
		if seen {
			info := a.subjects[sub]
			info.scopes = Expand(append(slices.Clone(info.scopes), k.Scopes...))
			a.subjects[sub] = info
			continue
		}
		a.subjects[sub] = subjectInfo{label: k.Label, scopes: Expand(k.Scopes)}
	}
	a.cache = newKeyCache(a.now)
	a.epoch = newEpochCache(o.Store, a.now)
	if o.Store == nil {
		// Without a database the epoch lives in memory; a random start value ends the sessions of
		// a previous process, whose revocations were not persisted either.
		a.epoch.memory = int64(randomUint32())
	}
	if len(o.SessionKey) > 0 {
		a.sessions = newSessions(o.SessionKey, o.SessionTTL, o.Sliding, o.SecureCookies, a.now)
	}
	a.tickets = NewTickets(TicketTTL, a.now)
	a.setup = &Setup{a: a, ttl: o.SetupTTL, publicURL: o.PublicURL}
	if a.setup.ttl <= 0 {
		a.setup.ttl = DefaultSetupTTL
	}
	return a
}

// IngestMode returns the effective ingest mode: keys or open. Open is never in effect while an
// upstream credential is injected.
func (a *Authenticator) IngestMode() string {
	if a.ingest == IngestOpen && !a.injecting.Load() {
		return IngestOpen
	}
	return IngestKeys
}

// SetInjecting records whether an upstream credential is injected (runtime credential bindings
// change it without a restart).
func (a *Authenticator) SetInjecting(on bool) { a.injecting.Store(on) }

// Injecting reports inject mode.
func (a *Authenticator) Injecting() bool { return a.injecting.Load() }

// HasStore reports whether api_keys, the setup token and the persisted epoch are available.
func (a *Authenticator) HasStore() bool { return a.store != nil }

// SessionsEnabled reports whether cookie sessions can be issued.
func (a *Authenticator) SessionsEnabled() bool { return a.sessions != nil }

// Sessions returns the session codec, or nil when sessions are disabled.
func (a *Authenticator) Sessions() *Sessions { return a.sessions }

// Tickets returns the WebSocket ticket store.
func (a *Authenticator) Tickets() *Tickets { return a.tickets }

// Setup returns the setup-token service.
func (a *Authenticator) Setup() *Setup { return a.setup }

// HasIngestKeys reports whether any configured env key can ingest. Keys created in the dashboard
// are not counted: this is the boot-time hint for an empty ingest configuration.
func (a *Authenticator) HasIngestKeys() bool {
	return slices.ContainsFunc(a.keys, func(e entry) bool { return e.id.Has(ScopeIngest) })
}

// HasEnvIngestScope reports whether an env key holds the ingest scope itself (an app key such
// as TRUTHTABLE_API_KEYS), not through admin: the D41 check for an upstream key bound for every
// caller, which counts dashboard keys the same way (store.IngestKeyExists, D95).
func (a *Authenticator) HasEnvIngestScope() bool {
	return slices.ContainsFunc(a.keys, func(e entry) bool { return slices.Contains(e.id.Scopes, ScopeIngest) })
}

// HasEnvAdmin reports whether TRUTHTABLE_ADMIN_KEYS (or any env key with admin) is configured.
func (a *Authenticator) HasEnvAdmin() bool {
	return slices.ContainsFunc(a.keys, func(e entry) bool { return e.id.Has(ScopeAdmin) })
}

// Errors of AuthenticateContext.
var (
	// ErrInvalidKey: the key is unknown or revoked.
	ErrInvalidKey = errors.New("authz: the key is not valid")
	// ErrUnavailable: the database needed to check the key cannot be reached.
	ErrUnavailable = errors.New("authz: keys cannot be checked right now")
)

// authTimeout bounds the database lookups of one authentication.
const authTimeout = 2 * time.Second

// Authenticate returns the identity of secret (the S0 API). Every env key is compared, in constant
// time over SHA-256 digests, so neither the position of a match nor the key lengths leak; then
// api_keys rows are looked up.
func (a *Authenticator) Authenticate(secret string) (*Identity, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	id, err := a.AuthenticateContext(ctx, secret)
	return id, err == nil
}

// AuthenticateContext returns the identity of secret: an env key (scopes of every list that holds
// it) or an active api_keys row. It returns ErrInvalidKey or ErrUnavailable.
func (a *Authenticator) AuthenticateContext(ctx context.Context, secret string) (*Identity, error) {
	if secret == "" {
		return nil, ErrInvalidKey
	}
	if id := a.envIdentity(secret); id != nil {
		return id, nil
	}
	if a.store == nil || !IsMintedKey(secret) {
		return nil, ErrInvalidKey
	}
	rec, err := a.cache.bySHA(ctx, a.store, hashHex(secret))
	if err != nil {
		a.log.Warn("authz: api key lookup failed", "err", err)
		return nil, ErrUnavailable
	}
	if rec == nil || rec.RevokedAt != nil {
		return nil, ErrInvalidKey
	}
	a.touch(rec.ID)
	return keyIdentity(rec), nil
}

// envIdentity compares secret with every env key in constant time.
func (a *Authenticator) envIdentity(secret string) *Identity {
	d := sha256.Sum256([]byte(secret))
	match := -1
	var scopes []Scope
	for i := range a.keys {
		if subtle.ConstantTimeCompare(d[:], a.keys[i].digest[:]) == 1 {
			if match < 0 {
				match = i
			}
			scopes = append(scopes, a.keys[i].id.Scopes...)
		}
	}
	if match < 0 {
		return nil
	}
	id := a.keys[match].id
	id.Scopes = Expand(scopes)
	return &id
}

// IsProxyKey reports whether secret is a TrustAgent key; such a value must never be forwarded
// upstream (B1). It matches env keys and anything shaped like a minted key, without a database
// round trip, so a proxy key is stripped even while the database is down.
func (a *Authenticator) IsProxyKey(secret string) bool {
	return secret != "" && (a.envIdentity(secret) != nil || IsMintedKey(secret))
}

// touchEvery throttles last_used_at updates per key.
const touchEvery = time.Minute

// touch records a key's use (api_keys.last_used_at) at most once per touchEvery, in the
// background: the bookkeeping write never delays the request.
func (a *Authenticator) touch(id string) {
	now := a.now()
	a.touchMu.Lock()
	last, ok := a.touched[id]
	if ok && now.Sub(last) < touchEvery {
		a.touchMu.Unlock()
		return
	}
	if len(a.touched) > 10000 {
		a.touched = map[string]time.Time{}
	}
	a.touched[id] = now
	a.touchMu.Unlock()
	a.touchWG.Add(1)
	go func() {
		defer a.touchWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.store.TouchAPIKey(ctx, id, now); err != nil {
			a.log.Debug("authz: last_used_at not updated", "key_id", id, "err", err)
		}
	}()
}

// Wait blocks until the background last-used updates finish (shutdown and tests).
func (a *Authenticator) Wait() { a.touchWG.Wait() }

func hashHex(secret string) string {
	d := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(d[:])
}

// Error codes of authentication failures.
const (
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeCSRFFailed   = "csrf_failed"
	// CodeUnavailable is the 503 of a key that cannot be checked because the database is down.
	CodeUnavailable = "persistence_disabled"
)

// Deny writes an authentication failure in the route's error format.
type Deny func(c *gin.Context, status int, code, message string)

// identityKey is the gin context key of the authenticated identity.
const identityKey = "trustagent.identity"

// Ingest returns middleware for /v1 routes: a credential with the ingest scope (SPEC §2.4 auth
// sources for /v1), or none at all when ingest auth is open. A credential that is presented but
// wrong is refused in every mode.
func (a *Authenticator) Ingest(deny Deny) gin.HandlerFunc {
	return func(c *gin.Context) {
		res := a.Resolve(c.Request, SurfaceV1)
		if res.Refresh != nil {
			http.SetCookie(c.Writer, res.Refresh)
		}
		if res.Err != nil {
			msg := res.Err.Message
			if res.Source == SourceHeaderKey && res.Err.Status == http.StatusUnauthorized {
				msg = "Invalid " + HeaderKey + "."
			}
			deny(c, res.Err.Status, res.Err.Code, msg)
			c.Abort()
			return
		}
		if id := res.Identity; id != nil {
			if !id.Has(ScopeIngest) {
				if res.Source == SourceHeaderKey || res.Source == SourceInjectedBearer {
					deny(c, http.StatusUnauthorized, CodeUnauthorized, "Invalid "+HeaderKey+".")
				} else {
					deny(c, http.StatusForbidden, CodeForbidden, "This session does not have the ingest scope.")
				}
				c.Abort()
				return
			}
			c.Set(identityKey, id)
			c.Next()
			return
		}
		if a.IngestMode() == IngestOpen {
			c.Next()
			return
		}
		msg := "Missing " + HeaderKey + ": this gateway requires an ingest key."
		if !a.HasIngestKeys() && a.store == nil {
			msg = "No ingest keys are configured: set TRUTHTABLE_API_KEYS (label=key), or TRUTHTABLE_INGEST_AUTH=open for local development."
		}
		deny(c, http.StatusUnauthorized, CodeUnauthorized, msg)
		c.Abort()
	}
}

// FromContext returns the identity Ingest stored, or nil for an unauthenticated (open) request.
func FromContext(c *gin.Context) *Identity {
	v, ok := c.Get(identityKey)
	if !ok {
		return nil
	}
	id, _ := v.(*Identity)
	return id
}

// KeyLabel returns the label of the key that authenticated the request, or "".
func KeyLabel(c *gin.Context) string {
	if id := FromContext(c); id != nil {
		return id.Label
	}
	return ""
}
