package authz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestScopeMatrix: what each credential kind may do (SPEC §2.4): admin implies content implies
// read; env API keys ingest+read+content, read keys read+content, admin keys everything, and
// api_keys rows exactly their scopes.
func TestScopeMatrix(t *testing.T) {
	f := newFixture(t, nil)
	ingestOnly, _ := f.mint(t, "tg", ScopeIngest)
	readOnly, _ := f.mint(t, "ro", ScopeRead)
	content, _ := f.mint(t, "ct", ScopeContent)
	dbAdmin, _ := f.mint(t, "adm", ScopeAdmin)
	matrix := []struct {
		name   string
		secret string
		want   map[Scope]bool
	}{
		{"TRUTHTABLE_API_KEYS", appKey, map[Scope]bool{ScopeIngest: true, ScopeRead: true, ScopeContent: true}},
		{"TRUTHTABLE_READ_KEYS", viewerKey, map[Scope]bool{ScopeRead: true, ScopeContent: true}},
		{"TRUTHTABLE_ADMIN_KEYS", rootKey, map[Scope]bool{ScopeIngest: true, ScopeRead: true, ScopeContent: true, ScopeAdmin: true}},
		{"api_keys ingest", ingestOnly, map[Scope]bool{ScopeIngest: true}},
		{"api_keys read", readOnly, map[Scope]bool{ScopeRead: true}},
		{"api_keys content", content, map[Scope]bool{ScopeRead: true, ScopeContent: true}},
		{"api_keys admin", dbAdmin, map[Scope]bool{ScopeIngest: true, ScopeRead: true, ScopeContent: true, ScopeAdmin: true}},
	}
	for _, m := range matrix {
		id, err := f.a.AuthenticateContext(context.Background(), m.secret)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		for _, s := range AllScopes {
			if got := id.Has(s); got != m.want[s] {
				t.Errorf("%s: Has(%s) = %v, want %v", m.name, s, got, m.want[s])
			}
		}
	}
	if (*Identity)(nil).Has(ScopeRead) {
		t.Error("a nil identity has no scope")
	}
	if got := Expand([]Scope{ScopeAdmin}); !reflect.DeepEqual(got, AllScopes) {
		t.Errorf("Expand(admin) = %v", got)
	}
	if got := Intersect([]Scope{ScopeAdmin}, []Scope{ScopeContent}); !reflect.DeepEqual(got, []Scope{ScopeRead, ScopeContent}) {
		t.Errorf("Intersect = %v", got)
	}
	if _, err := NormalizeScopes([]string{"read", "admin", "read"}); err != nil {
		t.Error(err)
	}
	if s, _ := NormalizeScopes([]string{"admin", "read", "read"}); !reflect.DeepEqual(s, []Scope{ScopeRead, ScopeAdmin}) {
		t.Errorf("normalized %v", s)
	}
	for _, bad := range [][]string{nil, {}, {"write"}, {"READ"}} {
		if _, err := NormalizeScopes(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestKeysAuthenticateAndRevoke(t *testing.T) {
	f := newFixture(t, nil)
	secret, id := f.mint(t, "ci", ScopeIngest, ScopeRead)
	if !IsMintedKey(secret) || !strings.HasPrefix(secret, MintedKeyPrefix) || len(secret) != 46 {
		t.Fatalf("minted key %q", secret)
	}
	got, err := f.a.AuthenticateContext(context.Background(), secret)
	if err != nil || got.Label != "ci" || got.Kind != KindAPIKey || got.KeyID != id || got.Subject != "key:"+id {
		t.Fatalf("identity %+v %v", got, err)
	}
	f.a.Wait()
	keys, _ := f.a.ListKeys(context.Background())
	if len(keys) != 1 || keys[0].SHA256 == secret || keys[0].Prefix != secret[:9] || keys[0].LastUsedAt == nil {
		t.Errorf("stored key %+v", keys)
	}
	for _, bad := range []string{"", secret + "x", strings.ToUpper(secret), "ta_" + strings.Repeat("B", 43)} {
		if _, err := f.a.AuthenticateContext(context.Background(), bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := f.a.RevokeKey(context.Background(), id, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.AuthenticateContext(context.Background(), secret); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("a key revoked here stops working at once: %v", err)
	}
	if _, err := f.a.RevokeKey(context.Background(), id, "admin"); err != nil {
		t.Errorf("revoking twice is not an error: %v", err)
	}
	if _, err := f.a.RevokeKey(context.Background(), "nope", "admin"); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if !reflect.DeepEqual(f.st.AuditActions(), []string{"api_key.create", "api_key.revoke"}) {
		t.Errorf("audit %v", f.st.AuditActions())
	}

	// A database outage is "unavailable", not "invalid".
	other, _ := f.mint(t, "other", ScopeRead)
	f.clock.Advance(RevalidateEvery)
	f.st.SetErr(errors.New("db down"))
	if _, err := f.a.AuthenticateContext(context.Background(), other); !errors.Is(err, ErrUnavailable) {
		t.Errorf("outage: %v", err)
	}
	if _, err := f.a.AuthenticateContext(context.Background(), appKey); err != nil {
		t.Errorf("env keys work without the database: %v", err)
	}

	noStore := newFixture(t, func(o *Options) { o.Store = nil })
	if _, err := noStore.a.CreateKey(context.Background(), "x", []Scope{ScopeRead}, "a"); !errors.Is(err, ErrNoStore) {
		t.Errorf("no store: %v", err)
	}
	if _, err := noStore.a.AuthenticateContext(context.Background(), secret); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("no store, minted key: %v", err)
	}
}

// TestKeyLookupCache: api_keys lookups are cached (misses briefly), so repeated requests with one
// key cost one query per RevalidateEvery.
func TestKeyLookupCache(t *testing.T) {
	f := newFixture(t, nil)
	secret, _ := f.mint(t, "ci", ScopeIngest)
	base := f.st.Lookups
	for i := 0; i < 5; i++ {
		if _, err := f.a.AuthenticateContext(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.st.Lookups - base; n != 1 {
		t.Errorf("%d lookups for 5 authentications", n)
	}
	f.clock.Advance(RevalidateEvery)
	_, _ = f.a.AuthenticateContext(context.Background(), secret)
	if n := f.st.Lookups - base; n != 2 {
		t.Errorf("the cache expires after RevalidateEvery: %d lookups", n)
	}
	unknown := "ta_" + strings.Repeat("C", 43)
	base = f.st.Lookups
	_, _ = f.a.AuthenticateContext(context.Background(), unknown)
	_, _ = f.a.AuthenticateContext(context.Background(), unknown)
	if n := f.st.Lookups - base; n != 1 {
		t.Errorf("misses are cached briefly: %d lookups", n)
	}
	_, _ = f.a.AuthenticateContext(context.Background(), "sk-not-minted-shape")
	if n := f.st.Lookups - base; n != 1 {
		t.Errorf("a value without the minted shape never reaches the database: %d lookups", n)
	}
}

// TestB1ProxyKeyRecognised: every TrustAgent key is recognised (env keys, and minted keys by
// shape, even while the database is down), so the upstream client can strip it wherever a client
// put it (proxykey_upstream_test.go checks the stripping).
func TestB1ProxyKeyRecognised(t *testing.T) {
	f := newFixture(t, nil)
	minted, _ := f.mint(t, "ci", ScopeIngest)
	f.st.SetErr(errors.New("db down"))
	for _, k := range []string{appKey, viewerKey, rootKey, minted, "ta_" + strings.Repeat("Z", 43)} {
		if !f.a.IsProxyKey(k) {
			t.Errorf("%q is not recognised as a proxy key", k)
		}
	}
	for _, k := range []string{"", "sk-upstream-1234567890", "sk-ant-api03-xyz", "sk-or-v1-abc"} {
		if f.a.IsProxyKey(k) {
			t.Errorf("%q is taken for a proxy key", k)
		}
	}
}

func TestIngestAuthModes(t *testing.T) {
	open := newFixture(t, func(o *Options) { o.IngestAuth = IngestOpen })
	if open.a.IngestMode() != IngestOpen {
		t.Error("open without inject mode")
	}
	open.a.SetInjecting(true)
	if open.a.IngestMode() != IngestKeys || !open.a.Injecting() {
		t.Error("inject mode refuses open ingest")
	}
	inject := newFixture(t, func(o *Options) { o.IngestAuth = IngestOpen; o.Injecting = true })
	if inject.a.IngestMode() != IngestKeys {
		t.Error("inject mode at construction refuses open ingest")
	}
	if New(nil, "nonsense").IngestMode() != IngestKeys {
		t.Error("an unknown mode means keys")
	}
}

type denied struct {
	status        int
	code, message string
}

// serveIngest runs one /v1 request through Ingest and reports the denial (nil when admitted) and
// the identity label.
func serveIngest(a *Authenticator, r *http.Request) (*denied, string) {
	var d *denied
	label := "<not reached>"
	e := gin.New()
	e.POST("/v1/chat/completions", a.Ingest(func(c *gin.Context, status int, code, message string) {
		d = &denied{status, code, message}
		c.Status(status)
	}), func(c *gin.Context) {
		label = KeyLabel(c)
		c.Status(http.StatusOK)
	})
	e.ServeHTTP(httptest.NewRecorder(), r)
	return d, label
}

// TestIngestB1InjectMode: in inject mode a Bearer or x-api-key equal to a proxy key authenticates
// /v1 and any other Authorization is the (stripped) client credential; open ingest is refused. In
// passthrough mode Authorization is never a TrustAgent credential on /v1.
func TestIngestB1InjectMode(t *testing.T) {
	inject := newFixture(t, func(o *Options) { o.Injecting = true; o.IngestAuth = IngestOpen })
	minted, _ := inject.mint(t, "sdk", ScopeIngest)
	for name, r := range map[string]*http.Request{
		"bearer env key":  request(http.MethodPost, "/v1/chat/completions", "", "Authorization", "Bearer "+appKey),
		"bearer minted":   request(http.MethodPost, "/v1/chat/completions", "", "Authorization", "bearer "+minted),
		"x-api-key":       request(http.MethodPost, "/v1/chat/completions", "", "x-api-key", appKey),
		"trustagent key":  request(http.MethodPost, "/v1/chat/completions", "", HeaderKey, appKey),
		"key over bearer": request(http.MethodPost, "/v1/chat/completions", "", HeaderKey, appKey, "Authorization", "Bearer sk-client"),
	} {
		if d, label := serveIngest(inject.a, r); d != nil || (label != "app" && label != "sdk") {
			t.Errorf("inject %s: %+v %q", name, d, label)
		}
	}
	d, _ := serveIngest(inject.a, request(http.MethodPost, "/v1/chat/completions", "", "Authorization", "Bearer sk-client-own-key"))
	if d == nil || d.status != http.StatusUnauthorized || !strings.HasPrefix(d.message, "Missing") {
		t.Errorf("inject mode, a client credential that is not a proxy key, open refused: %+v", d)
	}
	d, _ = serveIngest(inject.a, request(http.MethodPost, "/v1/chat/completions", "", "Authorization", "Bearer ta_"+strings.Repeat("Q", 43)))
	if d == nil || d.status != http.StatusUnauthorized || d.message != "The presented key is not valid." {
		t.Errorf("inject mode, an unknown minted key: %+v", d)
	}

	pass := newFixture(t, nil)
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", "", "Authorization", "Bearer "+appKey))
	if d == nil || d.status != http.StatusUnauthorized {
		t.Errorf("passthrough: Authorization is the upstream credential, not an ingest key: %+v", d)
	}
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", "", HeaderKey, viewerKey))
	if d == nil || d.message != "Invalid X-TrustAgent-Key." {
		t.Errorf("a key without ingest: %+v", d)
	}
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", "", HeaderKey, "wrong"))
	if d == nil || d.message != "Invalid X-TrustAgent-Key." {
		t.Errorf("a wrong key: %+v", d)
	}
	// An admin session may ingest; writes through a cookie need the CSRF header.
	cookie, _ := pass.login(t, rootKey)
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", cookie))
	if d == nil || d.code != CodeCSRFFailed {
		t.Errorf("cookie ingest without CSRF: %+v", d)
	}
	d, label := serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", cookie, HeaderCSRF, "1", "Origin", "http://localhost:3000"))
	if d != nil || label != "root" {
		t.Errorf("cookie ingest with CSRF: %+v %q", d, label)
	}
	viewerCookie, _ := pass.login(t, viewerKey)
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", viewerCookie, HeaderCSRF, "1", "Origin", "http://localhost:3000"))
	if d == nil || d.status != http.StatusForbidden {
		t.Errorf("a session without ingest: %+v", d)
	}
	// A database outage while checking a minted key is a 503, not a 401.
	withKey, _ := pass.mint(t, "ci", ScopeIngest)
	pass.clock.Advance(time.Hour)
	pass.st.SetErr(errors.New("db down"))
	d, _ = serveIngest(pass.a, request(http.MethodPost, "/v1/chat/completions", "", HeaderKey, withKey))
	if d == nil || d.status != http.StatusServiceUnavailable || d.code != CodeUnavailable {
		t.Errorf("outage: %+v", d)
	}
}

// TestResolveSources: the SPEC §2.4 source order; a refused credential is never downgraded.
func TestResolveSources(t *testing.T) {
	f := newFixture(t, nil)
	cookie, _ := f.login(t, viewerKey)
	cases := []struct {
		name    string
		r       *http.Request
		s       Surface
		label   string
		source  CredentialSource
		errCode string
	}{
		{"header key", request("GET", "/api/traces", "", HeaderKey, appKey), SurfaceAPI, "app", SourceHeaderKey, ""},
		{"header key beats cookie", request("GET", "/api/traces", cookie, HeaderKey, rootKey), SurfaceAPI, "root", SourceHeaderKey, ""},
		{"wrong header key never falls back to the cookie", request("GET", "/api/traces", cookie, HeaderKey, "wrong"), SurfaceAPI, "", SourceHeaderKey, CodeUnauthorized},
		{"api bearer", request("GET", "/api/traces", "", "Authorization", "Bearer "+rootKey), SurfaceAPI, "root", SourceBearer, ""},
		{"api x-api-key", request("GET", "/api/traces", "", "X-API-Key", appKey), SurfaceAPI, "app", SourceAPIKeyHeader, ""},
		{"api wrong bearer", request("GET", "/api/traces", cookie, "Authorization", "Bearer nope"), SurfaceAPI, "", SourceBearer, CodeUnauthorized},
		{"cookie", request("GET", "/api/traces", cookie), SurfaceAPI, "viewer", SourceCookie, ""},
		{"ws ignores bearer", request("GET", "/ws", "", "Authorization", "Bearer "+rootKey), SurfaceWS, "", SourceNone, ""},
		{"api ignores ticket", request("GET", "/api/traces?ticket=abc", ""), SurfaceAPI, "", SourceNone, ""},
		{"ws bad ticket", request("GET", "/ws?ticket=abc", ""), SurfaceWS, "", SourceTicket, CodeUnauthorized},
	}
	for _, c := range cases {
		res := f.a.Resolve(c.r, c.s)
		label, code := "", ""
		if res.Identity != nil {
			label = res.Identity.Label
		}
		if res.Err != nil {
			code = res.Err.Code
		}
		if label != c.label || res.Source != c.source || code != c.errCode {
			t.Errorf("%s: label %q source %q err %q", c.name, label, res.Source, code)
		}
	}
	for path, want := range map[string]Surface{"/ws": SurfaceWS, "/v1/chat/completions": SurfaceV1, "/v1": SurfaceV1, "/api/x": SurfaceAPI, "/v1x": SurfaceAPI} {
		if got := SurfaceOf(path); got != want {
			t.Errorf("SurfaceOf(%s) = %v", path, got)
		}
	}
}

// TestRevokeKeyKeepsTheLastAdminKey (D81): without an env admin key the last active admin api key
// is not revoked; another admin key, or an env admin key, lets it go.
func TestRevokeKeyKeepsTheLastAdminKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, func(o *Options) { o.Keys = o.Keys[:2] }) // app and viewer: no env admin
	_, readerID := f.mint(t, "dash-reader", ScopeRead)
	_, first := f.mint(t, "ops", ScopeAdmin)
	if _, err := f.a.RevokeKey(ctx, readerID, "test"); err != nil {
		t.Fatalf("a key without the admin scope: %v", err)
	}
	if _, err := f.a.RevokeKey(ctx, first, "test"); !errors.Is(err, ErrLastAdminKey) {
		t.Fatalf("the last admin key: %v", err)
	}
	_, second := f.mint(t, "ops-2", ScopeAdmin)
	if _, err := f.a.RevokeKey(ctx, first, "test"); err != nil {
		t.Fatalf("with another admin key: %v", err)
	}
	if _, err := f.a.RevokeKey(ctx, second, "test"); !errors.Is(err, ErrLastAdminKey) {
		t.Errorf("ops-2 is the last now: %v", err)
	}
	if _, err := f.a.RevokeKey(ctx, first, "test"); err != nil {
		t.Errorf("revoking a revoked key is not an error: %v", err)
	}

	withEnv := newFixture(t, nil) // root is an env admin key
	_, only := withEnv.mint(t, "ops", ScopeAdmin)
	if _, err := withEnv.a.RevokeKey(ctx, only, "test"); err != nil {
		t.Errorf("with an env admin key: %v", err)
	}
}

// TestAuditSignIn: a sign-in is recorded with the caller and its scopes (session.sign_in, D44).
func TestAuditSignIn(t *testing.T) {
	f := newFixture(t, nil)
	id, err := f.a.AuthenticateContext(context.Background(), viewerKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.AuditSignIn(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	got := f.st.Audit[len(f.st.Audit)-1]
	if got.Action != "session.sign_in" || got.Actor != "viewer" || got.Target != "viewer" {
		t.Errorf("audit %+v", got)
	}
	if scopes, _ := got.Diff.(map[string]any)["scopes"].([]string); len(scopes) != 2 {
		t.Errorf("scopes %v", got.Diff)
	}
	if err := newFixture(t, func(o *Options) { o.Store = nil }).a.AuditSignIn(context.Background(), id); err != nil {
		t.Errorf("without a store: %v", err)
	}
}
