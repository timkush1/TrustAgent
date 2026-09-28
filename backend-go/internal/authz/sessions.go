package authz

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CookieName is the dashboard session cookie (SPEC §7.5).
const CookieName = "ta_session"

// Session lifetimes (SPEC §7.5): TRUTHTABLE_SESSION_TTL (12 h) fixed, or a 30-day sliding window
// when the public URL is localhost.
const (
	DefaultSessionTTL = 12 * time.Hour
	SlidingWindow     = 30 * 24 * time.Hour
	// slideAfter is how old a sliding session gets before its cookie is re-issued; renewing on
	// every request would add a Set-Cookie to each response.
	slideAfter = time.Hour
	// maxClockSkew tolerates an iat slightly in the future (replicas with drifting clocks).
	maxClockSkew = time.Minute
)

// SessionPayload is the signed content of a ta_session cookie: b64url(payload).b64url(HMAC).
type SessionPayload struct {
	SID string  `json:"sid"`
	Sub string  `json:"sub"`
	Scp []Scope `json:"scp"`
	Iat int64   `json:"iat"`
	Exp int64   `json:"exp"`
	Ep  int64   `json:"ep"`
}

// Session errors.
var (
	// ErrSessionInvalid: malformed, tampered with, or signed with another key.
	ErrSessionInvalid = errors.New("authz: the session is not valid")
	// ErrSessionExpired: past its exp.
	ErrSessionExpired = errors.New("authz: the session expired")
	// ErrSessionRevoked: the epoch was bumped, or its subject (key or env label) no longer exists.
	ErrSessionRevoked = errors.New("authz: the session was revoked")
	// ErrSessionsDisabled: no session key is configured.
	ErrSessionsDisabled = errors.New("authz: sessions are not available")
)

// Sessions signs and verifies session cookies.
type Sessions struct {
	key     []byte
	ttl     time.Duration
	sliding bool
	secure  bool
	now     func() time.Time
}

func newSessions(key []byte, ttl time.Duration, sliding, secure bool, now func() time.Time) *Sessions {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &Sessions{key: append([]byte(nil), key...), ttl: ttl, sliding: sliding, secure: secure, now: now}
}

// Sliding reports 30-day sliding sessions.
func (s *Sessions) Sliding() bool { return s.sliding }

// Lifetime is how long a new (or renewed) session lasts.
func (s *Sessions) Lifetime() time.Duration {
	if s.sliding {
		return SlidingWindow
	}
	return s.ttl
}

func (s *Sessions) mac(payload string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// Encode signs a payload into a cookie value.
func (s *Sessions) Encode(p SessionPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(b)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(body)), nil
}

// Decode verifies a cookie value's signature and expiry and returns its payload. It does not check
// the epoch or the subject; the Authenticator does.
func (s *Sessions) Decode(v string) (SessionPayload, error) {
	var p SessionPayload
	body, sig, ok := strings.Cut(v, ".")
	if !ok || body == "" || strings.Contains(sig, ".") {
		return p, ErrSessionInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.mac(body)) {
		return p, ErrSessionInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return p, ErrSessionInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || p.SID == "" || p.Sub == "" || len(p.Scp) == 0 {
		return SessionPayload{}, ErrSessionInvalid
	}
	now := s.now()
	if p.Iat > now.Add(maxClockSkew).Unix() {
		return SessionPayload{}, ErrSessionInvalid
	}
	if p.Exp <= now.Unix() {
		return SessionPayload{}, ErrSessionExpired
	}
	return p, nil
}

// Cookie is the Set-Cookie of a session value expiring at exp: HttpOnly, SameSite=Strict, Path=/,
// Secure when the public URL is https (or TRUTHTABLE_COOKIE_SECURE says so).
func (s *Sessions) Cookie(value string, exp time.Time) *http.Cookie {
	maxAge := int(exp.Sub(s.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	// #nosec G124 -- Secure follows the public URL (SPEC §7.5): a plain-http deployment (LAN, localhost)
	// could not keep a Secure cookie. HttpOnly and SameSite=Strict are always set.
	return &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Expires: exp.UTC(), MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure,
	}
}

// ClearCookie is the Set-Cookie that deletes the session cookie.
func (s *Sessions) ClearCookie() *http.Cookie {
	// #nosec G124 -- a deletion cookie; Secure follows the public URL as for the session cookie.
	return &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0).UTC(),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure,
	}
}

// ClearCookie is Sessions.ClearCookie, or a plain deletion cookie when sessions are disabled.
func (a *Authenticator) ClearCookie() *http.Cookie {
	if a.sessions != nil {
		return a.sessions.ClearCookie()
	}
	// #nosec G124 -- a deletion cookie carries no session.
	return &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

// epochRetry is how long a failed epoch refresh waits before the next attempt, while the last
// known epoch keeps being served.
const epochRetry = 5 * time.Second

// epochCache holds the current session epoch (settings row session_epoch), refreshed every
// RevalidateEvery; without a store it lives in memory. One caller reads the store at a time and
// never under the state lock: once an epoch is known, while a refresh runs (or after one failed,
// for epochRetry) the others get the last known epoch at once, so a slow database does not
// serialize every cookie request. Before any epoch is known, callers wait for the one read only as
// long as their own context allows.
type epochCache struct {
	store  Store
	now    func() time.Time
	mu     sync.Mutex // guards the fields below
	value  int64
	next   time.Time // when the value is due for a refresh
	known  bool
	memory int64
	// gen counts local bumps: a read that overlapped one does not store its (possibly
	// pre-bump) value, so a revoke-all cannot be undone by a refresh that read the old epoch.
	gen uint64
	// refresh holds a token while one caller reads the store.
	refresh chan struct{}
}

func newEpochCache(st Store, now func() time.Time) *epochCache {
	return &epochCache{store: st, now: now, refresh: make(chan struct{}, 1)}
}

// cached returns the epoch, whether it is known and fresh, and the bump generation.
func (e *epochCache) cached() (v int64, known, fresh bool, gen uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store == nil {
		return e.memory, true, true, e.gen
	}
	return e.value, e.known, e.known && e.now().Before(e.next), e.gen
}

func (e *epochCache) current(ctx context.Context) (int64, error) {
	v, known, fresh, _ := e.cached()
	if fresh {
		return v, nil
	}
	if known {
		select {
		case e.refresh <- struct{}{}:
		default:
			return v, nil // another request is refreshing
		}
	} else {
		select {
		case e.refresh <- struct{}{}:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	defer func() { <-e.refresh }()
	v, _, fresh, gen := e.cached()
	if fresh {
		return v, nil
	}
	got, err := e.store.SessionEpoch(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		if e.known {
			e.next = e.now().Add(epochRetry) // keep serving the last known epoch
			return e.value, nil
		}
		return 0, err
	}
	if e.gen != gen {
		return e.value, nil // a local bump overlapped the read: its epoch is newer
	}
	e.value, e.known, e.next = got, true, e.now().Add(RevalidateEvery)
	return got, nil
}

func (e *epochCache) bump(ctx context.Context, actor string) (int64, error) {
	if e.store == nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.memory++
		return e.memory, nil
	}
	v, err := e.store.BumpSessionEpoch(ctx, actor)
	if err != nil {
		return 0, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gen++
	if !e.known || v > e.value {
		e.value, e.known = v, true
	}
	e.next = e.now().Add(RevalidateEvery)
	return v, nil
}

// SessionsReady reports whether a session can be issued now: sessions are enabled and the session
// epoch can be read. Check it before an action that must not be lost when the session fails.
func (a *Authenticator) SessionsReady(ctx context.Context) error {
	if a.sessions == nil {
		return ErrSessionsDisabled
	}
	if _, err := a.epoch.current(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

// IssueSession starts a session for id, which must be a key identity (env key or api key). It
// returns the Set-Cookie and the session identity.
func (a *Authenticator) IssueSession(ctx context.Context, id *Identity) (*http.Cookie, *Identity, error) {
	if a.sessions == nil {
		return nil, nil, ErrSessionsDisabled
	}
	if id == nil || id.Subject == "" || (id.Kind != KindEnvKey && id.Kind != KindAPIKey) {
		return nil, nil, errors.New("authz: only a key can open a session")
	}
	ep, err := a.epoch.current(ctx)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	now := a.now()
	exp := now.Add(a.sessions.Lifetime())
	p := SessionPayload{
		SID: randomToken()[:22], Sub: id.Subject, Scp: Expand(id.Scopes), Iat: now.Unix(), Exp: exp.Unix(), Ep: ep,
	}
	v, err := a.sessions.Encode(p)
	if err != nil {
		return nil, nil, err
	}
	sess := *id
	sess.Scopes = Expand(id.Scopes)
	sess.Kind, sess.SessionID, sess.ExpiresAt = KindSession, p.SID, time.Unix(p.Exp, 0).UTC()
	return a.sessions.Cookie(v, sess.ExpiresAt), &sess, nil
}

// sessionIdentity verifies a cookie value and re-validates it: the epoch must be current and the
// subject must still exist; the scopes are those the session was opened with that the subject
// still holds. A sliding session older than an hour gets a renewed cookie.
func (a *Authenticator) sessionIdentity(ctx context.Context, value string) (*Identity, *http.Cookie, error) {
	p, err := a.sessions.Decode(value)
	if err != nil {
		return nil, nil, err
	}
	ep, err := a.epoch.current(ctx)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	if p.Ep != ep {
		return nil, nil, ErrSessionRevoked
	}
	label, current, keyID, err := a.subject(ctx, p.Sub)
	if err != nil {
		return nil, nil, err
	}
	scopes := Intersect(p.Scp, current)
	if len(scopes) == 0 {
		return nil, nil, ErrSessionRevoked
	}
	id := &Identity{
		Label: label, Scopes: scopes, Kind: KindSession, Subject: p.Sub, KeyID: keyID, SessionID: p.SID,
		ExpiresAt: time.Unix(p.Exp, 0).UTC(),
	}
	var refresh *http.Cookie
	now := a.now()
	if a.sessions.sliding && now.Sub(time.Unix(p.Iat, 0)) >= slideAfter {
		exp := now.Add(SlidingWindow)
		renewed := p
		renewed.Iat, renewed.Exp = now.Unix(), exp.Unix()
		if v, err := a.sessions.Encode(renewed); err == nil {
			refresh = a.sessions.Cookie(v, exp)
			id.ExpiresAt = time.Unix(renewed.Exp, 0).UTC()
		}
	}
	return id, refresh, nil
}

// subject re-validates a session subject: an env key that is still configured, or an api_keys row
// that is still active (cached for RevalidateEvery).
func (a *Authenticator) subject(ctx context.Context, sub string) (label string, scopes []Scope, keyID string, err error) {
	switch {
	case strings.HasPrefix(sub, "env:"):
		info, ok := a.subjects[sub]
		if !ok {
			return "", nil, "", ErrSessionRevoked
		}
		return info.label, info.scopes, "", nil
	case strings.HasPrefix(sub, "key:"):
		if a.store == nil {
			return "", nil, "", ErrSessionRevoked
		}
		id := strings.TrimPrefix(sub, "key:")
		rec, err := a.cache.byKeyID(ctx, a.store, id)
		if err != nil {
			a.log.Warn("authz: session subject lookup failed", "err", err)
			return "", nil, "", ErrUnavailable
		}
		if rec == nil || rec.RevokedAt != nil {
			return "", nil, "", ErrSessionRevoked
		}
		return rec.Label, keyIdentity(rec).Scopes, rec.ID, nil
	}
	return "", nil, "", ErrSessionInvalid
}

// RevokeAllSessions bumps the session epoch, ending every session: at once on this replica and
// within RevalidateEvery on the others.
func (a *Authenticator) RevokeAllSessions(ctx context.Context, actor string) (int64, error) {
	return a.epoch.bump(ctx, actor)
}
