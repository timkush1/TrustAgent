// Package authz authenticates TrustAgent callers (SPEC §2.4, §7.5).
//
// S0 is the minimal ingest half: env keys from TRUTHTABLE_API_KEYS presented in X-TrustAgent-Key,
// compared in constant time, and TRUTHTABLE_INGEST_AUTH (keys | open) for /v1. The proxy key never
// travels in Authorization, so the client's upstream credential passes through untouched (B1
// passthrough mode). Read and admin keys, api_keys rows, sessions, CSRF, the setup token and WS
// tickets are the P2 authz core.
package authz

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"slices"

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

// APIKeyScopes are the scopes of a TRUTHTABLE_API_KEYS entry.
var APIKeyScopes = []Scope{ScopeIngest, ScopeRead, ScopeContent}

// Ingest auth modes (TRUTHTABLE_INGEST_AUTH).
const (
	IngestKeys = "keys"
	IngestOpen = "open"
)

// Key is a configured key.
type Key struct {
	Label  string
	Secret string
	Scopes []Scope
}

// Identity is an authenticated caller.
type Identity struct {
	Label  string
	Scopes []Scope
}

// Has reports whether the identity holds scope s. Admin grants everything; content implies read.
func (id *Identity) Has(s Scope) bool {
	if id == nil {
		return false
	}
	for _, have := range id.Scopes {
		if have == s || have == ScopeAdmin || (have == ScopeContent && s == ScopeRead) {
			return true
		}
	}
	return false
}

type entry struct {
	id     Identity
	digest [sha256.Size]byte
}

// Authenticator checks presented keys against the configured ones.
type Authenticator struct {
	keys   []entry
	ingest string
}

// New builds an Authenticator. An empty or unknown ingest mode means keys, the secure default.
func New(keys []Key, ingestAuth string) *Authenticator {
	a := &Authenticator{ingest: IngestKeys}
	if ingestAuth == IngestOpen {
		a.ingest = IngestOpen
	}
	for _, k := range keys {
		if k.Secret == "" {
			continue
		}
		a.keys = append(a.keys, entry{
			id:     Identity{Label: k.Label, Scopes: slices.Clone(k.Scopes)},
			digest: sha256.Sum256([]byte(k.Secret)),
		})
	}
	return a
}

// IngestMode returns keys or open.
func (a *Authenticator) IngestMode() string { return a.ingest }

// HasIngestKeys reports whether any configured key can ingest.
func (a *Authenticator) HasIngestKeys() bool {
	return slices.ContainsFunc(a.keys, func(e entry) bool { return e.id.Has(ScopeIngest) })
}

// Authenticate returns the identity of secret. Every configured key is compared, in constant time
// over SHA-256 digests, so neither the position of a match nor the key lengths leak.
func (a *Authenticator) Authenticate(secret string) (*Identity, bool) {
	if secret == "" {
		return nil, false
	}
	d := sha256.Sum256([]byte(secret))
	match := -1
	for i := range a.keys {
		if subtle.ConstantTimeCompare(d[:], a.keys[i].digest[:]) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, false
	}
	id := a.keys[match].id
	return &id, true
}

// IsProxyKey reports whether secret is one of the configured keys; such a value must never be
// forwarded upstream (B1).
func (a *Authenticator) IsProxyKey(secret string) bool {
	_, ok := a.Authenticate(secret)
	return ok
}

// Error codes of ingest authentication failures.
const (
	CodeUnauthorized = "unauthorized"
)

// Deny writes an authentication failure in the route's error format.
type Deny func(c *gin.Context, status int, code, message string)

// identityKey is the gin context key of the authenticated identity.
const identityKey = "trustagent.identity"

// Ingest returns middleware for /v1 routes: a valid X-TrustAgent-Key with the ingest scope, or no
// key at all when ingest auth is open. A key that is presented but wrong is refused in every mode.
func (a *Authenticator) Ingest(deny Deny) gin.HandlerFunc {
	return func(c *gin.Context) {
		secret := c.GetHeader(HeaderKey)
		if secret == "" {
			if a.ingest == IngestOpen {
				c.Next()
				return
			}
			msg := "Missing " + HeaderKey + ": this gateway requires an ingest key."
			if !a.HasIngestKeys() {
				msg = "No ingest keys are configured: set TRUTHTABLE_API_KEYS (label=key), or TRUTHTABLE_INGEST_AUTH=open for local development."
			}
			deny(c, http.StatusUnauthorized, CodeUnauthorized, msg)
			c.Abort()
			return
		}
		id, ok := a.Authenticate(secret)
		if !ok || !id.Has(ScopeIngest) {
			deny(c, http.StatusUnauthorized, CodeUnauthorized, "Invalid "+HeaderKey+".")
			c.Abort()
			return
		}
		c.Set(identityKey, id)
		c.Next()
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
