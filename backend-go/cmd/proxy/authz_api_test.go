package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/authz/authztest"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
)

// Keys of the authz endpoint tests (testKey, label trafficgen, is the harness's ingest key).
const (
	adminEnvKey  = "ta-admin-env-secret-1"
	readerEnvKey = "ta-reader-env-secret-2"
	dashOrigin   = "http://localhost:3000"
)

// authzStore is a store.Store (memStore) that also implements authz.Store (api_keys, setup token,
// session epoch) in memory.
type authzStore struct {
	*memStore
	*authztest.Store
}

var _ authz.Store = (*authzStore)(nil)

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type authzEnv struct {
	t    *testing.T
	srv  *server
	http *httptest.Server
	st   *authzStore // nil without persistence
	logs *syncBuffer
}

type authzEnvOpts struct {
	noStore bool
	mutate  func(*config.Config)
	// wrap replaces the store the server sees (for example to simulate a hanging database).
	wrap func(*authzStore) store.Store
}

// newAuthzEnv starts a proxy without an audit engine, with env keys (ingest: testKey; read:
// readerEnvKey; admin: adminEnvKey unless mutate removes it) and, unless noStore, an in-memory
// authz store. Logs are captured raw (no redaction), so tests see exactly what the code logs.
func newAuthzEnv(t *testing.T, o authzEnvOpts) *authzEnv {
	t.Helper()
	cfg := testConfig(t, envOpts{})
	cfg.Auth.ReadKeys = []config.APIKey{{Label: "reader", Key: readerEnvKey}}
	cfg.Auth.AdminKeys = []config.APIKey{{Label: "root", Key: adminEnvKey}}
	cfg.PublicURL, cfg.AllowedOrigins = dashOrigin, []string{"http://localhost:5173"}
	if o.mutate != nil {
		o.mutate(cfg)
	}
	env := &authzEnv{t: t, logs: &syncBuffer{}}
	d := serverDeps{cfg: cfg, log: slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if !o.noStore {
		env.st = &authzStore{memStore: newMemStore(), Store: authztest.New()}
		d.store = env.st
		if o.wrap != nil {
			d.store = o.wrap(env.st)
		}
	}
	srv, err := newServer(d)
	if err != nil {
		t.Fatal(err)
	}
	srv.start()
	env.srv, env.http = srv, httptest.NewServer(srv.router)
	t.Cleanup(func() {
		env.http.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.shutdown(ctx)
	})
	return env
}

// call sends a request; headers are name/value pairs; a non-nil body is sent as JSON.
func (e *authzEnv) call(method, path string, body any, cookie *http.Cookie, headers ...string) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		if s, ok := body.(string); ok {
			b = []byte(s)
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// csrf are the headers a same-origin dashboard sends on writes.
var csrf = []string{authz.HeaderCSRF, "1", "Origin", dashOrigin}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == authz.CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", authz.CookieName, resp.Header["Set-Cookie"])
	return nil
}

// strictDecode decodes body into v, refusing unknown fields (the api.ts shape exactly).
func strictDecode(t *testing.T, body []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	contractstest.ValidateJSON(t, schemaError, body)
	var e apiErrorBody
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

// TestSetupAPIClaimsTheTokenOnce: with no admin credential the proxy prints the setup URL, the
// wizard claims it with POST /api/session {setup_token}, receives the setup-admin key once and a
// session cookie; the token then answers 410 setup_token_used.
func TestSetupAPIClaimsTheTokenOnce(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) { c.Auth.AdminKeys = nil }})
	resp, body := env.call(http.MethodGet, "/api/setup/status", nil, nil)
	var st setupStatusBody
	strictDecode(t, body, &st)
	if resp.StatusCode != http.StatusOK || !st.SetupRequired || !st.Persistence || st.IngestAuth != "keys" ||
		st.PublicURL != dashOrigin || !strings.Contains(st.RecoveryCommand, "-new-setup-token -force") {
		t.Fatalf("setup status %d %+v", resp.StatusCode, st)
	}

	var out bytes.Buffer
	env.srv.announceSetup(context.Background(), &out)
	line := strings.TrimSpace(out.String())
	prefix := "SETUP: open " + dashOrigin + "/app/setup#token="
	if !strings.HasPrefix(line, prefix) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("setup line %q", out.String())
	}
	token := strings.TrimPrefix(line, prefix)

	if resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Errorf("claim without the CSRF header: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, authz.HeaderCSRF, "1", "Origin", "https://evil.example"); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Errorf("claim from another origin: %d %s", resp.StatusCode, body)
	}
	resp, body = env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, csrf...)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("claim: %d %s", resp.StatusCode, body)
	}
	var info sessionInfo
	strictDecode(t, body, &info)
	if info.Actor != authz.SetupAdminLabel || strings.Join(info.Scopes, ",") != "ingest,read,content,admin" ||
		!authz.IsMintedKey(info.CreatedAdminKey) || !info.Sliding {
		t.Errorf("session info %+v", info)
	}
	if exp, err := time.Parse(time.RFC3339Nano, info.ExpiresAt); err != nil || time.Until(exp) < 29*24*time.Hour {
		t.Errorf("a localhost session slides for 30 days: %s", info.ExpiresAt)
	}
	cookie := sessionCookie(t, resp)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" {
		t.Errorf("cookie attributes %+v", cookie)
	}

	if resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, csrf...); resp.StatusCode != http.StatusGone || errorCode(t, body) != "setup_token_used" {
		t.Errorf("second claim: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": "not-the-token"}, nil, csrf...); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %d %s", resp.StatusCode, body)
	}

	resp, body = env.call(http.MethodGet, "/api/session", nil, cookie)
	var me sessionInfo
	strictDecode(t, body, &me)
	if resp.StatusCode != http.StatusOK || me.Actor != authz.SetupAdminLabel || me.CreatedAdminKey != "" {
		t.Errorf("GET /api/session %d %+v", resp.StatusCode, me)
	}
	_, body = env.call(http.MethodGet, "/api/setup/status", nil, nil)
	strictDecode(t, body, &st)
	if st.SetupRequired {
		t.Error("setup is complete")
	}
	resp, body = env.call(http.MethodGet, "/api/status", nil, cookie)
	var status systemStatus
	_ = json.Unmarshal(body, &status)
	if resp.StatusCode != http.StatusOK || status.Auth.SetupRequired || strings.Join(status.Auth.CallerScopes, ",") != "ingest,read,content,admin" {
		t.Errorf("status with the setup session: %d %+v", resp.StatusCode, status.Auth)
	}
	contractstest.ValidateJSON(t, schemaStatus, body)
	// The created admin key works as a header key too.
	if resp, _ := env.call(http.MethodGet, "/api/keys", nil, nil, authz.HeaderKey, info.CreatedAdminKey); resp.StatusCode != http.StatusOK {
		t.Errorf("admin key: %d", resp.StatusCode)
	}
	env.assertNoSecretsLogged(token, info.CreatedAdminKey, cookie.Value)
	if !strings.Contains(env.logs.String(), "dashboard setup claimed") {
		t.Error("the claim is logged")
	}
}

// TestSetupClaimWaitsForTheDatabase: when a session could not be issued, the setup token is not
// spent (the admin key it creates would be lost); it still works once the database is back.
func TestSetupClaimWaitsForTheDatabase(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) { c.Auth.AdminKeys = nil }})
	var out bytes.Buffer
	env.srv.announceSetup(context.Background(), &out)
	token := strings.TrimSpace(out.String()[strings.Index(out.String(), "#token=")+len("#token="):])
	env.st.SetErr(errors.New("database down"))
	resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, csrf...)
	if resp.StatusCode != http.StatusServiceUnavailable || errorCode(t, body) != codePersistenceDisabled {
		t.Fatalf("claim during an outage: %d %s", resp.StatusCode, body)
	}
	env.st.SetErr(nil)
	resp, body = env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, csrf...)
	var info sessionInfo
	strictDecode(t, body, &info)
	if resp.StatusCode != http.StatusOK || info.CreatedAdminKey == "" {
		t.Errorf("claim after the outage: %d %s", resp.StatusCode, body)
	}
}

func TestSetupAPIWithoutPersistence(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{noStore: true, mutate: func(c *config.Config) { c.Auth.AdminKeys = nil }})
	_, body := env.call(http.MethodGet, "/api/setup/status", nil, nil)
	var st setupStatusBody
	strictDecode(t, body, &st)
	if !st.SetupRequired || st.Persistence {
		t.Errorf("status %+v", st)
	}
	var out bytes.Buffer
	env.srv.announceSetup(context.Background(), &out)
	if out.Len() != 0 || !strings.Contains(env.logs.String(), "set TRUTHTABLE_ADMIN_KEYS") {
		t.Errorf("no token without a database; the log says what to do: %q", out.String())
	}
	resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": "x"}, nil, csrf...)
	if resp.StatusCode != http.StatusServiceUnavailable || errorCode(t, body) != codePersistenceDisabled {
		t.Errorf("claim without a database: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodGet, "/api/keys", nil, nil, authz.HeaderKey, testKey); resp.StatusCode != http.StatusForbidden {
		t.Errorf("keys need admin: %d %s", resp.StatusCode, body)
	}
}

// hangingAdminStore is a database whose admin-key check hangs until the caller gives up.
type hangingAdminStore struct{ *authzStore }

func (hangingAdminStore) AdminKeyExists(ctx context.Context) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

// TestAnnounceSetupIsBounded: a database that hangs at boot does not keep the proxy from starting.
func TestAnnounceSetupIsBounded(t *testing.T) {
	prev := setupAnnounceTimeout
	setupAnnounceTimeout = 50 * time.Millisecond
	t.Cleanup(func() { setupAnnounceTimeout = prev })
	env := newAuthzEnv(t, authzEnvOpts{
		mutate: func(c *config.Config) { c.Auth.AdminKeys = nil },
		wrap:   func(s *authzStore) store.Store { return hangingAdminStore{s} },
	})
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.srv.announceSetup(context.Background(), &out)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("announceSetup blocked on a hanging database")
	}
	if out.Len() != 0 || !strings.Contains(env.logs.String(), "the setup token could not be created") {
		t.Errorf("no SETUP line; the failure is logged: %q\n%s", out.String(), env.logs.String())
	}
}

// TestCrossOriginDashboardClaim: a cross-origin dashboard (DASHBOARD_API_BASE) sends no CSRF header
// and no cookies; its allow-listed Origin admits the setup claim, which returns the admin key it
// then sends as a key header. A foreign Origin is refused.
func TestCrossOriginDashboardClaim(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) { c.Auth.AdminKeys = nil }})
	var out bytes.Buffer
	env.srv.announceSetup(context.Background(), &out)
	token := strings.TrimSpace(out.String()[strings.Index(out.String(), "#token=")+len("#token="):])
	const dashboard = "http://localhost:5173" // TRUTHTABLE_ALLOWED_ORIGINS in newAuthzEnv
	if resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, "Origin", "http://localhost:8888"); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Fatalf("a foreign Origin: %d %s", resp.StatusCode, body)
	}
	resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"setup_token": token}, nil, "Origin", dashboard)
	var info sessionInfo
	strictDecode(t, body, &info)
	if resp.StatusCode != http.StatusOK || info.CreatedAdminKey == "" {
		t.Fatalf("cross-origin claim: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodGet, "/api/keys", nil, nil, authz.HeaderKey, info.CreatedAdminKey, "Origin", dashboard); resp.StatusCode != http.StatusOK {
		t.Errorf("the created key works as a key header: %d %s", resp.StatusCode, body)
	}
}

// TestSessionAPI: sign in with a key, use the cookie, CSRF on writes, sign out, and end every
// session with ?all=true.
func TestSessionAPI(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{})
	for name, c := range map[string]struct {
		body   any
		status int
		code   string
	}{
		"invalid json":     {"{nope", http.StatusBadRequest, "invalid_json"},
		"trailing data":    {`{"key":"a"} {}`, http.StatusBadRequest, "invalid_json"},
		"unknown field":    {map[string]string{"password": "x"}, http.StatusBadRequest, "invalid_json"},
		"neither":          {map[string]string{}, http.StatusBadRequest, "bad_request"},
		"both":             {map[string]string{"key": testKey, "setup_token": "t"}, http.StatusBadRequest, "bad_request"},
		"wrong key":        {map[string]string{"key": "wrong"}, http.StatusUnauthorized, "unauthorized"},
		"ingest-only key":  {map[string]string{"key": env.mintKey("tg", "ingest")}, http.StatusForbidden, "forbidden"},
		"setup, not setup": {map[string]string{"setup_token": "stale"}, http.StatusUnauthorized, "unauthorized"},
	} {
		resp, body := env.call(http.MethodPost, "/api/session", c.body, nil, csrf...)
		if resp.StatusCode != c.status || errorCode(t, body) != c.code {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}

	resp, body := env.call(http.MethodPost, "/api/session", map[string]string{"key": readerEnvKey}, nil, csrf...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %d %s", resp.StatusCode, body)
	}
	reader := sessionCookie(t, resp)
	var info sessionInfo
	strictDecode(t, body, &info)
	if info.Actor != "reader" || strings.Join(info.Scopes, ",") != "read,content" || info.CreatedAdminKey != "" {
		t.Errorf("reader session %+v", info)
	}
	if resp, _ := env.call(http.MethodGet, "/api/traces", nil, reader); resp.StatusCode != http.StatusOK {
		t.Errorf("the cookie reads: %d", resp.StatusCode)
	}
	if resp, body := env.call(http.MethodGet, "/api/keys", nil, reader); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session does not have the admin scope") {
		t.Errorf("a reader session is not admin: %d %s", resp.StatusCode, body)
	}

	resp, _ = env.call(http.MethodPost, "/api/session", map[string]string{"key": adminEnvKey}, nil, csrf...)
	admin := sessionCookie(t, resp)
	if resp, body := env.call(http.MethodPost, "/api/keys", map[string]any{"label": "ci", "scopes": []string{"ingest"}}, admin); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Errorf("cookie write without CSRF: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodPost, "/api/keys", map[string]any{"label": "ci", "scopes": []string{"ingest"}}, admin, authz.HeaderCSRF, "1", "Origin", "https://evil.example"); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Errorf("cookie write from a foreign Origin: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodPost, "/api/keys", map[string]any{"label": "ci", "scopes": []string{"ingest"}}, admin, csrf...); resp.StatusCode != http.StatusCreated {
		t.Errorf("cookie write with CSRF: %d %s", resp.StatusCode, body)
	}

	// GET /api/session with a key header: no session, scopes of the key.
	resp, body = env.call(http.MethodGet, "/api/session", nil, nil, authz.HeaderKey, testKey)
	strictDecode(t, body, &info)
	if resp.StatusCode != http.StatusOK || info.Actor != "trafficgen" || info.Sliding {
		t.Errorf("key caller %d %+v", resp.StatusCode, info)
	}
	if resp, _ := env.call(http.MethodGet, "/api/session", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: %d", resp.StatusCode)
	}

	// Sign out clears the cookie; ?all=true needs admin and ends every session.
	resp, _ = env.call(http.MethodDelete, "/api/session", nil, reader, csrf...)
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(strings.Join(resp.Header["Set-Cookie"], ";"), "Max-Age=0") {
		t.Errorf("sign out: %d %v", resp.StatusCode, resp.Header["Set-Cookie"])
	}
	if resp, body := env.call(http.MethodDelete, "/api/session?all=true", nil, reader, csrf...); resp.StatusCode != http.StatusForbidden {
		t.Errorf("all=true by a reader: %d %s", resp.StatusCode, body)
	}
	if resp, _ := env.call(http.MethodDelete, "/api/session?all=true", nil, admin); resp.StatusCode != http.StatusForbidden {
		t.Errorf("all=true through a cookie without CSRF: %d", resp.StatusCode)
	}
	if resp, body := env.call(http.MethodDelete, "/api/session?all=true", nil, admin, csrf...); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("all=true: %d %s", resp.StatusCode, body)
	}
	for name, c := range map[string]*http.Cookie{"reader": reader, "admin": admin} {
		resp, _ := env.call(http.MethodGet, "/api/traces", nil, c)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(strings.Join(resp.Header["Set-Cookie"], ";"), authz.CookieName+"=;") {
			t.Errorf("%s session after all=true: %d %v", name, resp.StatusCode, resp.Header["Set-Cookie"])
		}
	}
	if got := env.st.AuditActions(); got[len(got)-1] != "session.revoke_all" {
		t.Errorf("audit %v", got)
	}
	if resp, _ := env.call(http.MethodDelete, "/api/session", nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("sign out without a session: %d", resp.StatusCode)
	}
	env.assertNoSecretsLogged(readerEnvKey, adminEnvKey, reader.Value, admin.Value)
}

// TestWSTicketAPI: POST /api/ws-ticket (read) returns a single-use ticket that opens /ws once.
func TestWSTicketAPI(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{})
	resp, _ := env.call(http.MethodPost, "/api/session", map[string]string{"key": readerEnvKey}, nil, csrf...)
	cookie := sessionCookie(t, resp)
	if resp, body := env.call(http.MethodPost, "/api/ws-ticket", nil, cookie); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "csrf_failed" {
		t.Errorf("ticket through a cookie without CSRF: %d %s", resp.StatusCode, body)
	}
	resp, body := env.call(http.MethodPost, "/api/ws-ticket", nil, cookie, csrf...)
	var tk struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
	}
	strictDecode(t, body, &tk)
	contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/WsTicket", body)
	if resp.StatusCode != http.StatusOK || tk.ExpiresIn != 30 || tk.Ticket == "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("ticket %d %s", resp.StatusCode, body)
	}
	url := "ws" + strings.TrimPrefix(env.http.URL, "http") + "/ws?protocol=2&ticket=" + tk.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial with the ticket: %v", err)
	}
	f := readFrame(t, conn)
	_ = conn.Close()
	if f.Type != "hello" {
		t.Errorf("first frame %s", f.Type)
	}
	_, resp2, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil || resp2 == nil || resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("a ticket works once: %v %v", err, resp2)
	}
	if resp, _ := env.call(http.MethodPost, "/api/ws-ticket", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a ticket needs read: %d", resp.StatusCode)
	}
	if resp, _ := env.call(http.MethodPost, "/api/ws-ticket", nil, nil, authz.HeaderKey, testKey); resp.StatusCode != http.StatusOK {
		t.Errorf("a key header gets a ticket without CSRF: %d", resp.StatusCode)
	}
	env.assertNoSecretsLogged(tk.Ticket, cookie.Value, readerEnvKey)
}

// mintKey creates an api key through the admin API and returns the secret.
func (e *authzEnv) mintKey(label string, scopes ...string) string {
	e.t.Helper()
	resp, body := e.call(http.MethodPost, "/api/keys", map[string]any{"label": label, "scopes": scopes}, nil, authz.HeaderKey, adminEnvKey)
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("mint %s: %d %s", label, resp.StatusCode, body)
	}
	var k createdAPIKey
	strictDecode(e.t, body, &k)
	return k.Secret
}

// TestKeysAPI: admins list, create (secret shown once) and revoke api keys; a revoked key stops
// working at once; validation, read-only mode and a missing database answer as specified.
func TestKeysAPI(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{})
	admin := []string{authz.HeaderKey, adminEnvKey}
	resp, body := env.call(http.MethodPost, "/api/keys", map[string]any{"label": "ci-bot", "scopes": []string{"read", "ingest", "read"}}, nil, admin...)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create %d %s", resp.StatusCode, body)
	}
	var created createdAPIKey
	strictDecode(t, body, &created)
	contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/CreatedApiKey", body)
	if !authz.IsMintedKey(created.Secret) || created.Label != "ci-bot" || strings.Join(created.Scopes, ",") != "ingest,read" ||
		created.Prefix != created.Secret[:9] || created.RevokedAt != nil || created.LastUsedAt != nil {
		t.Errorf("created %+v", created)
	}
	if resp, _ := env.call(http.MethodGet, "/api/traces", nil, nil, authz.HeaderKey, created.Secret); resp.StatusCode != http.StatusOK {
		t.Errorf("the new key reads: %d", resp.StatusCode)
	}
	env.srv.gateway.auth.Wait() // last_used_at is written in the background
	resp, body = env.call(http.MethodGet, "/api/keys", nil, nil, admin...)
	var list struct {
		Items []apiKeyView `json:"items"`
	}
	strictDecode(t, body, &list)
	contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/ApiKeyList", body)
	if resp.StatusCode != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != created.ID || list.Items[0].LastUsedAt == nil {
		t.Fatalf("list %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), created.Secret) || strings.Contains(string(body), "sha256") {
		t.Error("the list reveals the key")
	}
	for name, c := range map[string]struct {
		body   any
		status int
		fields string
	}{
		"bad label":      {map[string]any{"label": "has space", "scopes": []string{"read"}}, http.StatusUnprocessableEntity, "label"},
		"no scopes":      {map[string]any{"label": "x", "scopes": []string{}}, http.StatusUnprocessableEntity, "scopes"},
		"unknown scope":  {map[string]any{"label": "x", "scopes": []string{"write"}}, http.StatusUnprocessableEntity, "scopes"},
		"both invalid":   {map[string]any{"label": "", "scopes": nil}, http.StatusUnprocessableEntity, "label,scopes"},
		"not json":       {"[", http.StatusBadRequest, ""},
		"unknown member": {map[string]any{"label": "x", "scopes": []string{"read"}, "secret": "mine"}, http.StatusBadRequest, ""},
	} {
		resp, body := env.call(http.MethodPost, "/api/keys", c.body, nil, admin...)
		var e apiErrorBody
		_ = json.Unmarshal(body, &e)
		paths := []string{}
		for _, f := range e.Error.FieldErrors {
			paths = append(paths, f.Path)
		}
		if resp.StatusCode != c.status || strings.Join(paths, ",") != c.fields {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
		contractstest.ValidateJSON(t, schemaError, body)
	}
	for name, key := range map[string]string{"ingest+read+content": testKey, "read": readerEnvKey, "the new non-admin key": created.Secret} {
		if resp, _ := env.call(http.MethodPost, "/api/keys", map[string]any{"label": "x", "scopes": []string{"admin"}}, nil, authz.HeaderKey, key); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s may not create keys: %d", name, resp.StatusCode)
		}
	}

	if resp, _ := env.call(http.MethodDelete, "/api/keys/"+created.ID, nil, nil, admin...); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke %d", resp.StatusCode)
	}
	if resp, _ := env.call(http.MethodGet, "/api/traces", nil, nil, authz.HeaderKey, created.Secret); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked key stops working at once: %d", resp.StatusCode)
	}
	if resp, _ := env.call(http.MethodDelete, "/api/keys/"+created.ID, nil, nil, admin...); resp.StatusCode != http.StatusNoContent {
		t.Errorf("revoking again: %d", resp.StatusCode)
	}
	if resp, body := env.call(http.MethodDelete, "/api/keys/nope", nil, nil, admin...); resp.StatusCode != http.StatusNotFound || errorCode(t, body) != codeNotFound {
		t.Errorf("unknown id: %d %s", resp.StatusCode, body)
	}
	_, body = env.call(http.MethodGet, "/api/keys", nil, nil, admin...)
	strictDecode(t, body, &list)
	contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/ApiKeyList", body)
	if list.Items[0].RevokedAt == nil {
		t.Error("the list shows the revocation")
	}
	if got := strings.Join(env.st.AuditActions(), ","); got != "api_key.create,api_key.revoke" {
		t.Errorf("audit %s", got)
	}
	if !strings.Contains(env.logs.String(), "api key created") || !strings.Contains(env.logs.String(), "api key revoked") {
		t.Error("key changes are logged")
	}
	env.assertNoSecretsLogged(created.Secret, adminEnvKey)

	ro := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) { c.Readonly = true }})
	if resp, body := ro.call(http.MethodPost, "/api/keys", map[string]any{"label": "x", "scopes": []string{"read"}}, nil, admin...); resp.StatusCode != http.StatusForbidden || errorCode(t, body) != "readonly" {
		t.Errorf("readonly create: %d %s", resp.StatusCode, body)
	}
	if resp, _ := ro.call(http.MethodDelete, "/api/keys/x", nil, nil, admin...); resp.StatusCode != http.StatusForbidden {
		t.Errorf("readonly revoke: %d", resp.StatusCode)
	}
	resp, _ = ro.call(http.MethodPost, "/api/session", map[string]string{"key": readerEnvKey}, nil, csrf...)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("readonly still signs in: %d", resp.StatusCode)
	}
	if resp, _ := ro.call(http.MethodPost, "/api/ws-ticket", nil, nil, authz.HeaderKey, readerEnvKey); resp.StatusCode != http.StatusOK {
		t.Errorf("readonly still issues tickets: %d", resp.StatusCode)
	}

	none := newAuthzEnv(t, authzEnvOpts{noStore: true})
	if resp, body := none.call(http.MethodGet, "/api/keys", nil, nil, admin...); resp.StatusCode != http.StatusServiceUnavailable || errorCode(t, body) != codePersistenceDisabled {
		t.Errorf("keys without a database: %d %s", resp.StatusCode, body)
	}
}

// TestInjectModeRefusesOpenIngestB1: with an upstream credential injected, TRUTHTABLE_INGEST_AUTH=
// open is not in effect: /v1 without a proxy key is 401, and the status reports ingest "keys".
func TestInjectModeRefusesOpenIngestB1(t *testing.T) {
	env := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) {
		c.IngestAuth, c.UpstreamAPIKey = "open", "sk-injected-upstream-credential"
	}})
	resp, body := env.call(http.MethodPost, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("open ingest in inject mode: %d %s", resp.StatusCode, body)
	}
	_, body = env.call(http.MethodGet, "/api/setup/status", nil, nil)
	var st setupStatusBody
	strictDecode(t, body, &st)
	if st.IngestAuth != "keys" {
		t.Errorf("setup status ingest %s", st.IngestAuth)
	}
	_, body = env.call(http.MethodGet, "/api/status", nil, nil, authz.HeaderKey, testKey)
	var status systemStatus
	_ = json.Unmarshal(body, &status)
	if status.Auth.Ingest != "keys" {
		t.Errorf("status ingest %s", status.Auth.Ingest)
	}
	open := newAuthzEnv(t, authzEnvOpts{mutate: func(c *config.Config) { c.IngestAuth = "open" }})
	_, body = open.call(http.MethodGet, "/api/setup/status", nil, nil)
	strictDecode(t, body, &st)
	if st.IngestAuth != "open" {
		t.Errorf("passthrough keeps open: %s", st.IngestAuth)
	}
	env.assertNoSecretsLogged("sk-injected-upstream-credential")
}

// assertNoSecretsLogged fails when any secret appears in the captured log.
func (e *authzEnv) assertNoSecretsLogged(secrets ...string) {
	e.t.Helper()
	logs := e.logs.String()
	for _, s := range append(secrets, testKey, adminEnvKey, readerEnvKey) {
		if s != "" && strings.Contains(logs, s) {
			e.t.Errorf("a secret (%s…) appears in the logs:\n%s", s[:min(6, len(s))], logs)
		}
	}
}
