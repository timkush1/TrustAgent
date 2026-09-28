package authz

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// Store is the persistence the authz core needs; *store.PostgresStore implements it.
type Store interface {
	CreateAPIKey(ctx context.Context, k *store.APIKeyRecord, actor string) error
	APIKeyBySHA256(ctx context.Context, sha string) (*store.APIKeyRecord, error)
	APIKeyByID(ctx context.Context, id string) (*store.APIKeyRecord, error)
	ListAPIKeys(ctx context.Context) ([]store.APIKeyRecord, error)
	RevokeAPIKey(ctx context.Context, id, actor string) (*store.APIKeyRecord, bool, error)
	TouchAPIKey(ctx context.Context, id string, at time.Time) error
	AdminKeyExists(ctx context.Context) (bool, error)
	PutSetupToken(ctx context.Context, rec store.SetupTokenRecord, actor string) error
	ClaimSetupToken(ctx context.Context, sha string, now time.Time, envAdmin bool, admin *store.APIKeyRecord) error
	SessionEpoch(ctx context.Context) (int64, error)
	BumpSessionEpoch(ctx context.Context, actor string) (int64, error)
}

var _ Store = (*store.PostgresStore)(nil)

// MintedKeyPrefix starts every key TrustAgent mints (api_keys rows, the setup admin key).
const MintedKeyPrefix = "ta_"

// mintedKeyPattern is the shape of a minted key: the prefix and 32 random bytes in unpadded
// base64url. No upstream credential format (sk-…, sk-ant-…, sk-or-…) has this shape.
var mintedKeyPattern = regexp.MustCompile(`^ta_[A-Za-z0-9_-]{43}$`)

// IsMintedKey reports whether s has the shape of a key TrustAgent minted.
func IsMintedKey(s string) bool { return mintedKeyPattern.MatchString(s) }

// displayPrefixLen is how much of a minted key api_keys.prefix keeps for display.
const displayPrefixLen = len(MintedKeyPrefix) + 6

// MintKey returns a new random key, its SHA-256 (hex) and its display prefix.
func MintKey() (secret, sha, prefix string) {
	secret = MintedKeyPrefix + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	return secret, hashHex(secret), secret[:displayPrefixLen]
}

// randomBytes returns n bytes from crypto/rand. crypto/rand.Read never fails on supported
// platforms (Go 1.24+ panics instead of returning an error).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randomUint32() uint32 { return binary.BigEndian.Uint32(randomBytes(4)) }

// randomToken is 32 random bytes in unpadded base64url (43 characters).
func randomToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes(32)) }

// envSubject is the session subject of an env key: its label and a keyed fingerprint of the
// secret, so replacing a leaked key under the same label ends the sessions opened with it, while
// the cookie reveals nothing about the secret.
func envSubject(fpKey []byte, label, secret string) string {
	m := hmac.New(sha256.New, fpKey)
	m.Write([]byte("trustagent/session-subject/v1|" + secret))
	return "env:" + label + "#" + hex.EncodeToString(m.Sum(nil))[:16]
}

// keySubject is the session subject of an api_keys row.
func keySubject(id string) string { return "key:" + id }

// keyIdentity is the identity of an active api_keys row.
func keyIdentity(rec *store.APIKeyRecord) *Identity {
	scopes := make([]Scope, 0, len(rec.Scopes))
	for _, s := range rec.Scopes {
		if sc, ok := ParseScope(s); ok {
			scopes = append(scopes, sc)
		}
	}
	return &Identity{Label: rec.Label, Scopes: Expand(scopes), Kind: KindAPIKey, Subject: keySubject(rec.ID), KeyID: rec.ID}
}

// RevalidateEvery is how long an api_keys lookup and the session epoch are cached: a key revoked
// or an epoch bumped on another replica takes effect within this interval (SPEC §7.5 per-request
// revalidation with a 30 s cache). Changes made through this Authenticator take effect at once.
const RevalidateEvery = 30 * time.Second

// negativeTTL caches "no such key" briefly, so a flood of wrong keys does not become a flood of
// queries, while a key created on another replica works within seconds.
const negativeTTL = 5 * time.Second

// maxCachedKeys bounds each cache map; a full map is cleared.
const maxCachedKeys = 4096

type cachedKey struct {
	rec *store.APIKeyRecord // nil: no such key
	at  time.Time
}

// keyCache caches api_keys lookups by hash and by id.
type keyCache struct {
	mu   sync.Mutex
	now  func() time.Time
	bySh map[string]cachedKey
	byID map[string]cachedKey
}

func newKeyCache(now func() time.Time) *keyCache {
	return &keyCache{now: now, bySh: map[string]cachedKey{}, byID: map[string]cachedKey{}}
}

func (c *keyCache) fresh(e cachedKey, ok bool) bool {
	if !ok {
		return false
	}
	ttl := RevalidateEvery
	if e.rec == nil {
		ttl = negativeTTL
	}
	return c.now().Sub(e.at) < ttl
}

func (c *keyCache) put(m map[string]cachedKey, k string, rec *store.APIKeyRecord) {
	if len(m) >= maxCachedKeys {
		clear(m)
	}
	m[k] = cachedKey{rec: rec, at: c.now()}
}

// bySHA returns the row whose hash is sha, or nil when there is none.
func (c *keyCache) bySHA(ctx context.Context, st Store, sha string) (*store.APIKeyRecord, error) {
	c.mu.Lock()
	e, ok := c.bySh[sha]
	c.mu.Unlock()
	if c.fresh(e, ok) {
		return e.rec, nil
	}
	rec, err := st.APIKeyBySHA256(ctx, sha)
	if errors.Is(err, store.ErrNotFound) {
		rec, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.put(c.bySh, sha, rec)
	if rec != nil {
		c.put(c.byID, rec.ID, rec)
	}
	c.mu.Unlock()
	return rec, nil
}

// byKeyID returns the row with id, or nil when there is none.
func (c *keyCache) byKeyID(ctx context.Context, st Store, id string) (*store.APIKeyRecord, error) {
	c.mu.Lock()
	e, ok := c.byID[id]
	c.mu.Unlock()
	if c.fresh(e, ok) {
		return e.rec, nil
	}
	rec, err := st.APIKeyByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		rec, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.put(c.byID, id, rec)
	if rec != nil {
		c.put(c.bySh, rec.SHA256, rec)
	}
	c.mu.Unlock()
	return rec, nil
}

// forget drops a key from the cache (after a local revocation).
func (c *keyCache) forget(rec *store.APIKeyRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byID, rec.ID)
	delete(c.bySh, rec.SHA256)
}

// Key management errors.
var (
	// ErrNoStore: api_keys need a database.
	ErrNoStore = errors.New("authz: API keys need a database (TRUTHTABLE_DATABASE_URL)")
	// ErrKeyNotFound: no api_keys row has that id.
	ErrKeyNotFound = errors.New("authz: no such key")
)

// labelPattern is the syntax of a key label.
var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,63}$`)

// ValidLabel reports whether s is a valid key label (1-64 of A-Z a-z 0-9 . _ : @ -, starting with
// a letter or digit).
func ValidLabel(s string) bool { return labelPattern.MatchString(s) }

// NormalizeScopes validates scope names and returns them deduplicated in canonical order.
func NormalizeScopes(names []string) ([]Scope, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one scope is required")
	}
	set := map[Scope]bool{}
	for _, n := range names {
		sc, ok := ParseScope(n)
		if !ok {
			return nil, fmt.Errorf("unknown scope %q (want ingest, read, content or admin)", n)
		}
		set[sc] = true
	}
	out := []Scope{}
	for _, sc := range AllScopes {
		if set[sc] {
			out = append(out, sc)
		}
	}
	return out, nil
}

// CreatedKey is a newly minted api key; Secret is shown once and never stored.
type CreatedKey struct {
	Record store.APIKeyRecord
	Secret string
}

// CreateKey mints an api key with label and scopes, stores its hash and logs the creation as actor.
func (a *Authenticator) CreateKey(ctx context.Context, label string, scopes []Scope, actor string) (*CreatedKey, error) {
	if a.store == nil {
		return nil, ErrNoStore
	}
	secret, sha, prefix := MintKey()
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, string(s))
	}
	rec := store.APIKeyRecord{ID: store.NewID(), Label: label, Scopes: names, SHA256: sha, Prefix: prefix, CreatedBy: actor}
	if err := a.store.CreateAPIKey(ctx, &rec, actor); err != nil {
		return nil, err
	}
	a.cache.mu.Lock()
	delete(a.cache.bySh, sha) // a cached "no such key" must not hide the new key
	a.cache.mu.Unlock()
	return &CreatedKey{Record: rec, Secret: secret}, nil
}

// ListKeys returns every api key, newest first.
func (a *Authenticator) ListKeys(ctx context.Context) ([]store.APIKeyRecord, error) {
	if a.store == nil {
		return nil, ErrNoStore
	}
	return a.store.ListAPIKeys(ctx)
}

// RevokeKey revokes an api key. It takes effect at once on this replica and within
// RevalidateEvery elsewhere; sessions opened with the key end with it. Revoking a revoked key is
// not an error.
func (a *Authenticator) RevokeKey(ctx context.Context, id, actor string) (*store.APIKeyRecord, error) {
	if a.store == nil {
		return nil, ErrNoStore
	}
	rec, _, err := a.store.RevokeAPIKey(ctx, id, actor)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	a.cache.forget(rec)
	return rec, nil
}
