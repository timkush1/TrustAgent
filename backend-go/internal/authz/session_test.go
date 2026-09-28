package authz

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func resolveCookie(f *fixture, cookie string) Result {
	return f.a.Resolve(request(http.MethodGet, "/api/traces", cookie), SurfaceAPI)
}

func TestSessionIssueAndCookieAttributes(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.SecureCookies = true })
	id, _ := f.a.AuthenticateContext(context.Background(), viewerKey)
	c, sess, err := f.a.IssueSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != CookieName || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || !c.Secure || c.Path != "/" || c.MaxAge != int((12*time.Hour).Seconds()) {
		t.Errorf("cookie %+v", c)
	}
	if sess.Kind != KindSession || sess.SessionID == "" || !sess.ExpiresAt.Equal(f.clock.Now().Add(12*time.Hour)) || !reflect.DeepEqual(sess.Scopes, []Scope{ScopeRead, ScopeContent}) {
		t.Errorf("session identity %+v", sess)
	}
	body, _, _ := strings.Cut(c.Value, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range p {
		keys = append(keys, k)
	}
	if len(keys) != 6 || p["sid"] == nil || p["sub"] == nil || p["scp"] == nil || p["iat"] == nil || p["exp"] == nil || p["ep"] == nil {
		t.Errorf("payload %v: want {sid, sub, scp, iat, exp, ep}", p)
	}
	if strings.Contains(string(raw), viewerKey) {
		t.Error("the session payload contains the key")
	}
	res := resolveCookie(f, c.Value)
	if res.Identity == nil || res.Identity.Label != "viewer" || res.Source != SourceCookie || res.Refresh != nil {
		t.Errorf("resolve %+v", res)
	}
	if _, _, err := f.a.IssueSession(context.Background(), &Identity{Label: "public", Scopes: []Scope{ScopeRead}, Kind: KindPublic}); err == nil {
		t.Error("a public identity cannot open a session")
	}
	if _, _, err := New(nil, "").IssueSession(context.Background(), id); !errors.Is(err, ErrSessionsDisabled) {
		t.Errorf("no session key: %v", err)
	}
	if clear := f.a.ClearCookie(); clear.MaxAge != -1 || clear.Name != CookieName || !clear.Secure {
		t.Errorf("clear cookie %+v", clear)
	}
}

// TestSessionHMACTamperAndExpiry: a changed payload, a foreign signature or a malformed value is
// refused; so is a session past its exp.
func TestSessionHMACTamperAndExpiry(t *testing.T) {
	f := newFixture(t, nil)
	cookie, _ := f.login(t, viewerKey)
	body, sig, _ := strings.Cut(cookie, ".")

	raw, _ := base64.RawURLEncoding.DecodeString(body)
	escalated := bytes.Replace(raw, []byte(`"scp":["read","content"]`), []byte(`"scp":["admin"]`), 1)
	if bytes.Equal(escalated, raw) {
		t.Fatalf("payload %s", raw)
	}
	forged := base64.RawURLEncoding.EncodeToString(escalated) + "." + sig
	other := newSessions(bytes.Repeat([]byte{9}, 32), time.Hour, false, false, f.clock.Now)
	var p SessionPayload
	_ = json.Unmarshal(raw, &p)
	foreign, _ := other.Encode(p)
	for name, v := range map[string]string{
		"escalated scopes": forged, "foreign key": foreign, "no signature": body, "empty signature": body + ".",
		"extra part": cookie + ".x", "garbage": "!!!", "bad base64": body + ".***",
	} {
		if res := resolveCookie(f, v); res.Identity != nil || !res.StaleCookie || res.Err != nil {
			t.Errorf("%s: %+v", name, res)
		}
		if _, err := f.a.sessions.Decode(v); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.clock.Advance(12*time.Hour - time.Second)
	if res := resolveCookie(f, cookie); res.Identity == nil {
		t.Error("a session is valid until its exp")
	}
	f.clock.Advance(time.Second)
	if res := resolveCookie(f, cookie); res.Identity != nil || !res.StaleCookie {
		t.Errorf("an expired session: %+v", res)
	}
	if _, err := f.a.sessions.Decode(cookie); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("decode expired: %v", err)
	}
	// An iat from the future (beyond the skew allowance) is refused.
	p.Iat = f.clock.Now().Add(time.Hour).Unix()
	p.Exp = f.clock.Now().Add(2 * time.Hour).Unix()
	future, _ := f.a.sessions.Encode(p)
	if _, err := f.a.sessions.Decode(future); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("future iat: %v", err)
	}
}

// TestSessionEpochBump: DELETE /api/session?all=true bumps the epoch, which ends every session at
// once on this replica and within RevalidateEvery on the others.
func TestSessionEpochBump(t *testing.T) {
	f := newFixture(t, nil)
	c1, _ := f.login(t, viewerKey)
	c2, _ := f.login(t, rootKey)
	ep, err := f.a.RevokeAllSessions(context.Background(), "root")
	if err != nil || ep != 1 {
		t.Fatalf("bump: %d %v", ep, err)
	}
	for _, c := range []string{c1, c2} {
		if res := resolveCookie(f, c); res.Identity != nil || !res.StaleCookie {
			t.Errorf("a session of the old epoch: %+v", res)
		}
	}
	c3, _ := f.login(t, viewerKey)
	if res := resolveCookie(f, c3); res.Identity == nil {
		t.Error("a new session works")
	}
	if got := f.st.AuditActions(); got[len(got)-1] != "session.revoke_all" {
		t.Errorf("audit %v", got)
	}

	// Another replica sharing the store sees the bump within RevalidateEvery.
	g := newFixture(t, func(o *Options) { o.Store = f.st; o.Now = f.clock.Now })
	if res := resolveCookie(g, c3); res.Identity == nil {
		t.Fatal("replica: the session is valid")
	}
	f.st.BumpDirect()
	if res := resolveCookie(g, c3); res.Identity == nil {
		t.Error("replica: the cached epoch keeps the session for up to RevalidateEvery")
	}
	f.clock.Advance(RevalidateEvery)
	if res := resolveCookie(g, c3); res.Identity != nil {
		t.Error("replica: the bump takes effect within RevalidateEvery")
	}

	// Without a store the epoch lives in memory.
	m := newFixture(t, func(o *Options) { o.Store = nil })
	mc, _ := m.login(t, viewerKey)
	if _, err := m.a.RevokeAllSessions(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if res := resolveCookie(m, mc); res.Identity != nil {
		t.Error("in-memory epoch bump")
	}
}

// TestSessionSubRevocation: the subject is re-validated on every request (cached for 30 s): a
// revoked api key ends its sessions within RevalidateEvery (at once when revoked through this
// Authenticator), and an env key that is removed or replaced ends its sessions at restart.
func TestSessionSubRevocation(t *testing.T) {
	f := newFixture(t, nil)
	secret, id := f.mint(t, "dash", ScopeRead, ScopeContent)
	cookie, sess := f.login(t, secret)
	if sess.KeyID != id || sess.Subject != "key:"+id {
		t.Fatalf("session %+v", sess)
	}
	if res := resolveCookie(f, cookie); res.Identity == nil || res.Identity.KeyID != id {
		t.Fatal("session works")
	}
	f.st.RevokeDirect(id) // another replica revoked it
	f.clock.Advance(RevalidateEvery - time.Second)
	if res := resolveCookie(f, cookie); res.Identity == nil {
		t.Error("the cached subject is trusted for up to RevalidateEvery")
	}
	f.clock.Advance(time.Second)
	if res := resolveCookie(f, cookie); res.Identity != nil || !res.StaleCookie {
		t.Errorf("revoked key within 30 s: %+v", res)
	}

	secret2, id2 := f.mint(t, "dash2", ScopeRead)
	cookie2, _ := f.login(t, secret2)
	if _, err := f.a.RevokeKey(context.Background(), id2, "root"); err != nil {
		t.Fatal(err)
	}
	if res := resolveCookie(f, cookie2); res.Identity != nil {
		t.Error("a key revoked through this Authenticator ends its sessions at once")
	}

	// Env keys: the same session key, a restart without the label (or with a new secret under the
	// same label) ends the session; an unrelated change does not.
	envCookie, _ := f.login(t, viewerKey)
	restart := func(keys []Key) *fixture {
		return newFixture(t, func(o *Options) { o.Keys = keys; o.Store = f.st; o.Now = f.clock.Now })
	}
	same := restart([]Key{{Label: "viewer", Secret: viewerKey, Scopes: ReadKeyScopes}})
	if res := resolveCookie(same, envCookie); res.Identity == nil || res.Identity.Label != "viewer" {
		t.Errorf("restart with the same key: %+v", res)
	}
	removed := restart([]Key{{Label: "app", Secret: appKey, Scopes: APIKeyScopes}})
	if res := resolveCookie(removed, envCookie); res.Identity != nil {
		t.Error("a removed env label ends its sessions")
	}
	rotated := restart([]Key{{Label: "viewer", Secret: "ta-viewer-rotated-9", Scopes: ReadKeyScopes}})
	if res := resolveCookie(rotated, envCookie); res.Identity != nil {
		t.Error("a replaced env key ends the sessions opened with the old one")
	}
	narrowed := restart([]Key{{Label: "viewer", Secret: viewerKey, Scopes: []Scope{ScopeRead}}})
	if res := resolveCookie(narrowed, envCookie); res.Identity == nil || res.Identity.Has(ScopeContent) {
		t.Errorf("scopes are what the subject still holds: %+v", res.Identity)
	}
	// A database outage while re-validating a key subject is a 503, not a sign-out.
	cookie3, _ := f.login(t, mustMint(t, f, "dash3"))
	f.clock.Advance(RevalidateEvery)
	f.st.SetErr(errors.New("db down"))
	if res := resolveCookie(f, cookie3); res.Err == nil || res.Err.Status != http.StatusServiceUnavailable {
		t.Errorf("outage: %+v", res)
	}
}

func mustMint(t *testing.T, f *fixture, label string) string {
	t.Helper()
	s, _ := f.mint(t, label, ScopeRead)
	return s
}

// TestSessionSliding: on localhost sessions last 30 days and slide: a request more than an hour
// after the cookie was issued renews it.
func TestSessionSliding(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Sliding = true })
	cookie, sess := f.login(t, viewerKey)
	if !sess.ExpiresAt.Equal(f.clock.Now().Add(SlidingWindow)) || !f.a.Sessions().Sliding() {
		t.Fatalf("sliding session expires %v", sess.ExpiresAt)
	}
	f.clock.Advance(30 * time.Minute)
	if res := resolveCookie(f, cookie); res.Identity == nil || res.Refresh != nil {
		t.Errorf("no renewal within the first hour: %+v", res)
	}
	f.clock.Advance(29 * 24 * time.Hour)
	res := resolveCookie(f, cookie)
	if res.Identity == nil || res.Refresh == nil || !res.Identity.ExpiresAt.Equal(f.clock.Now().Add(SlidingWindow)) {
		t.Fatalf("renewal: %+v", res)
	}
	renewed := res.Refresh.Value
	f.clock.Advance(20 * 24 * time.Hour) // past the first cookie's exp, within the renewed one
	if res := resolveCookie(f, cookie); res.Identity != nil {
		t.Error("the original cookie expired")
	}
	if res := resolveCookie(f, renewed); res.Identity == nil || res.Identity.SessionID != sess.SessionID {
		t.Errorf("the renewed cookie keeps the session: %+v", res)
	}

	fixed := newFixture(t, nil)
	fc, _ := fixed.login(t, viewerKey)
	fixed.clock.Advance(2 * time.Hour)
	if res := resolveCookie(fixed, fc); res.Identity == nil || res.Refresh != nil || fixed.a.Sessions().Sliding() {
		t.Errorf("a 12 h session never slides: %+v", res)
	}
}
