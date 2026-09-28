package authz

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/authz/authztest"
)

var _ Store = (*authztest.Store)(nil)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Env keys of the test authenticators.
const (
	appKey    = "ta-app-secret-0001"
	viewerKey = "ta-viewer-secret-02"
	rootKey   = "ta-root-secret-0003"
)

var sessionKey = bytes.Repeat([]byte{7}, 32)

type fixture struct {
	a     *Authenticator
	st    *authztest.Store
	clock *clock
	logs  *bytes.Buffer
}

// newFixture builds an Authenticator with the three env key lists, an in-memory store, sessions
// and a test clock; mutate adjusts the options.
func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := &fixture{st: authztest.New(), clock: newClock(), logs: &bytes.Buffer{}}
	o := Options{
		Keys: []Key{
			{Label: "app", Secret: appKey, Scopes: APIKeyScopes},
			{Label: "viewer", Secret: viewerKey, Scopes: ReadKeyScopes},
			{Label: "root", Secret: rootKey, Scopes: AdminKeyScopes},
		},
		IngestAuth: IngestKeys, Store: f.st, SessionKey: sessionKey, SessionTTL: 12 * time.Hour,
		AllowedOrigins: []string{"http://localhost:5173"}, PublicURL: "http://localhost:3000/",
		Now: f.clock.Now, Logger: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if mutate != nil {
		mutate(&o)
	}
	if o.Store == nil {
		f.st = nil
	}
	f.a = NewAuthenticator(o)
	return f
}

// request builds a request to path with headers (pairs) and an optional session cookie.
func request(method, path, cookie string, headers ...string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	return r
}

// login opens a session with key and returns the cookie value.
func (f *fixture) login(t *testing.T, key string) (string, *Identity) {
	t.Helper()
	id, err := f.a.AuthenticateContext(context.Background(), key)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	c, sess, err := f.a.IssueSession(context.Background(), id)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return c.Value, sess
}

// mint creates an api key with scopes and returns its secret and id.
func (f *fixture) mint(t *testing.T, label string, scopes ...Scope) (string, string) {
	t.Helper()
	k, err := f.a.CreateKey(context.Background(), label, scopes, "test")
	if err != nil {
		t.Fatal(err)
	}
	return k.Secret, k.Record.ID
}
