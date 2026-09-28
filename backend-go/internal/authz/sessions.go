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
	return &http.Cookie{
		Name: CookieName, Value: value, Path: "/", Expires: exp.UTC(), MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secure,
	}
}

// ClearCookie is the Set-Cookie that deletes the session cookie.
func (s *Sessions) ClearCookie() *http.Cookie {
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
	return &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

// epochCache holds the current session epoch (settings row session_epoch), refreshed every
// RevalidateEvery; without a store it lives in memory.
type epochCache struct {
	store  Store
	now    func() time.Time
	mu     sync.Mutex
	value  int64
	at     time.Time
	known  bool
	memory int64
}

func (e *epochCache) current(ctx context.Context) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store == nil {
		return e.memory, nil
	}
	if e.known && e.now().Sub(e.at) < RevalidateEvery {
		return e.value, nil
	}
	v, err := e.store.SessionEpoch(ctx)
	if err != nil {
		if e.known {
			return e.value, nil // keep serving the last known epoch while the database is away
		}
		return 0, err
	}
	e.value, e.at, e.known = v, e.now(), true
	return v, nil
}

func (e *epochCache) bump(ctx context.Context, actor string) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store == nil {
		e.memory++
		return e.memory, nil
	}
	v, err := e.store.BumpSessionEpoch(ctx, actor)
	if err != nil {
		return 0, err
	}
	e.value, e.at, e.known = v, e.now(), true
	return v, nil
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
