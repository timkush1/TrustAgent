package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// keyServer records the credential headers it receives.
type keyServer struct {
	*httptest.Server
	hits atomic.Int64
	mu   sync.Mutex
	last http.Header
}

func newKeyServer(t *testing.T) *keyServer {
	t.Helper()
	k := &keyServer{}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.hits.Add(1)
		k.mu.Lock()
		k.last = r.Header.Clone()
		k.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(k.Close)
	return k
}

func (k *keyServer) header() http.Header {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.last
}

// boundKey is the runtime Manager's KeyFunc contract (D78): the secret, only for the
// credential's allowed origins; it records the origins it was asked for.
type boundKey struct {
	secret  string
	allowed []string
	mu      sync.Mutex
	asked   []string
}

func (b *boundKey) fn(_ context.Context, origin string) (string, error) {
	b.mu.Lock()
	b.asked = append(b.asked, origin)
	b.mu.Unlock()
	if !slices.Contains(b.allowed, origin) {
		return "", runtimecfg.ErrHostNotAllowed
	}
	return b.secret, nil
}

func originOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return jev.CanonicalOrigin(u)
}

func doCall(t *testing.T, c *Client, hdr http.Header) error {
	t.Helper()
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", hdr, []byte(`{}`))
	if err != nil {
		return err
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	cancel()
	return nil
}

// TestInjectsTheDashboardCredential (D41): an upstream with a bound dashboard credential injects
// it on its auth header, asked for the upstream's canonical origin; every client credential,
// the proxy key included, is dropped.
func TestInjectsTheDashboardCredential(t *testing.T) {
	srv := newKeyServer(t)
	key := &boundKey{secret: "sk-test-dashboard-upstream-0001", allowed: []string{originOf(t, srv.URL)}}
	for _, tc := range []struct{ header, want string }{
		{"", "Bearer sk-test-dashboard-upstream-0001"},
		{"x-api-key", "sk-test-dashboard-upstream-0001"},
	} {
		c, err := New(Config{BaseURL: srv.URL + "/base", Key: key.fn, AuthHeader: tc.header, Timeout: 5 * time.Second},
			func(s string) bool { return s == "ta-proxy-key" })
		if err != nil {
			t.Fatal(err)
		}
		if !c.Injecting() {
			t.Fatal("a client with a bound credential is not in inject mode")
		}
		client := http.Header{"Authorization": {"Bearer ta-proxy-key"}, "X-Api-Key": {"sk-ant-client-own"}, "Api-Key": {"client"}}
		if err := doCall(t, c, client); err != nil {
			t.Fatal(err)
		}
		got := srv.header()
		name := "Authorization"
		if tc.header != "" {
			name = tc.header
		}
		if got.Get(name) != tc.want {
			t.Errorf("auth header %s: %q missing the dashboard key", name, got.Get(name))
		}
		for _, h := range []string{"Authorization", "X-Api-Key", "Api-Key"} {
			if http.CanonicalHeaderKey(h) == http.CanonicalHeaderKey(name) {
				continue
			}
			if v := got.Get(h); v != "" {
				t.Errorf("client credential %s forwarded in inject mode", h)
			}
		}
	}
	key.mu.Lock()
	defer key.mu.Unlock()
	for _, o := range key.asked {
		if o != originOf(t, srv.URL) {
			t.Fatalf("KeyFunc asked for %q, want the upstream's canonical origin", o)
		}
	}
}

// TestEnvKeyWinsOverTheBinding (D33): with both, the env key is injected and the KeyFunc is never
// asked.
func TestEnvKeyWinsOverTheBinding(t *testing.T) {
	srv := newKeyServer(t)
	key := &boundKey{secret: "sk-test-dashboard-0002", allowed: []string{originOf(t, srv.URL)}}
	c, err := New(Config{BaseURL: srv.URL, APIKey: "sk-test-env-0002", Key: key.fn, Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := doCall(t, c, nil); err != nil {
		t.Fatal(err)
	}
	if got := srv.header().Get("Authorization"); got != "Bearer sk-test-env-0002" {
		t.Fatalf("Authorization = %q, want the env key", got)
	}
	if len(key.asked) != 0 {
		t.Fatal("the KeyFunc was asked although the env key wins")
	}
}

// TestCredentialRefusedSendsNothing (D78): an upstream URL whose origin the bound credential was
// not created for (an origin change without a new secret), or a credential the runtime
// configuration does not hold, fails the call before anything is sent; the client's own
// credential is never sent instead. Both Do and the passthrough reverse proxy refuse.
func TestCredentialRefusedSendsNothing(t *testing.T) {
	srv := newKeyServer(t)
	moved := &boundKey{secret: "sk-test-dashboard-0003", allowed: []string{"https://old-upstream.example.com"}}
	missing := runtimecfg.NewStatic(nil).KeyFunc(CredentialDefault)
	for name, fn := range map[string]KeyFunc{"origin changed": moved.fn, "credential missing": KeyFunc(missing)} {
		t.Run(name, func(t *testing.T) {
			c, err := New(Config{BaseURL: srv.URL, Key: fn, Timeout: 5 * time.Second}, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := srv.hits.Load()
			err = doCall(t, c, http.Header{"Authorization": {"Bearer sk-client-own-0003"}})
			if !errors.Is(err, ErrCredentialUnavailable) {
				t.Fatalf("Do: %v, want ErrCredentialUnavailable", err)
			}
			h, err := c.Outbound(context.Background(), http.Header{"Authorization": {"Bearer sk-client-own-0003"}})
			if !errors.Is(err, ErrCredentialUnavailable) || h.Get("Authorization") != "" {
				t.Fatalf("Outbound: %v with Authorization %q, want an error and no credential", err, h.Get("Authorization"))
			}
			if out := c.OutboundHeaders(http.Header{"Authorization": {"Bearer sk-client-own-0003"}}); out.Get("Authorization") != "" {
				t.Fatal("OutboundHeaders kept the client's credential although the injected one is unavailable")
			}

			var proxyErr error
			rp := c.ReverseProxy(func(w http.ResponseWriter, _ *http.Request, err error) {
				proxyErr = err
				w.WriteHeader(http.StatusBadGateway)
			})
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer sk-client-own-0003")
			rec := httptest.NewRecorder()
			rp.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadGateway || !errors.Is(proxyErr, ErrCredentialUnavailable) {
				t.Fatalf("reverse proxy: %d, %v; want 502 with ErrCredentialUnavailable", rec.Code, proxyErr)
			}
			if srv.hits.Load() != before {
				t.Fatal("a request reached the upstream without its credential")
			}
		})
	}
}

// TestReverseProxyInjectsTheDashboardCredential: the passthrough endpoints (/v1/models) inject the
// bound credential too.
func TestReverseProxyInjectsTheDashboardCredential(t *testing.T) {
	srv := newKeyServer(t)
	key := &boundKey{secret: "sk-test-dashboard-0004", allowed: []string{originOf(t, srv.URL)}}
	c, err := New(Config{BaseURL: srv.URL, Key: key.fn, Timeout: 5 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rp := c.ReverseProxy(func(w http.ResponseWriter, _ *http.Request, err error) { t.Errorf("proxy error: %v", err) })
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-client-own-0004")
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || srv.header().Get("Authorization") != "Bearer sk-test-dashboard-0004" {
		t.Fatalf("reverse proxy: %d, Authorization %q", rec.Code, srv.header().Get("Authorization"))
	}
}

func strp(s string) *string { return &s }

// TestRuntimeRisks: the D23 condition refuses the write (fallback_key_required on
// upstream.fallback.url, details.host); the D15 conditions only warn.
func TestRuntimeRisks(t *testing.T) {
	base := func() runtimecfg.RuntimeSettings {
		s := runtimecfg.RuntimeSettings{}
		s.Upstream.Default = runtimecfg.UpstreamSetting{URL: strp("https://api.openai.com"), AuthHeader: "authorization"}
		s.Upstream.Anthropic = runtimecfg.UpstreamSetting{URL: strp("https://api.anthropic.com"), AuthHeader: "x-api-key"}
		s.Upstream.Fallback = runtimecfg.FallbackUpstreamSetting{Format: "openai"}
		return s
	}
	dash := func(name string) runtimecfg.Credential {
		return runtimecfg.Credential{Name: name, Provider: "generic", Source: "dashboard", Secret: "sk-test-" + name}
	}
	refused := []FieldError{{
		Path: "upstream.fallback.url", Code: CodeFallbackKeyRequired, Details: map[string]any{"host": "fallback.example.com"},
	}}
	for _, tc := range []struct {
		name     string
		edit     func(*runtimecfg.RuntimeSettings)
		creds    runtimecfg.CredentialSet
		errs     []FieldError
		warnings []string // codes and paths
	}{
		{name: "defaults", edit: func(*runtimecfg.RuntimeSettings) {}},
		{
			name: "fallback on another host without a key",
			edit: func(s *runtimecfg.RuntimeSettings) { s.Upstream.Fallback.URL = strp("https://Fallback.example.com/v1") },
			errs: refused,
		},
		{
			name: "fallback on the primary's origin needs no key",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Upstream.Fallback.URL = strp("https://api.openai.com:443/other")
			},
		},
		{
			name: "fallback on the primary's host but another port",
			edit: func(s *runtimecfg.RuntimeSettings) { s.Upstream.Fallback.URL = strp("https://api.openai.com:8443") },
			errs: []FieldError{{Path: "upstream.fallback.url", Code: CodeFallbackKeyRequired, Details: map[string]any{"host": "api.openai.com"}}},
		},
		{
			name: "anthropic fallback compares with the anthropic upstream",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Upstream.Fallback.Format, s.Upstream.Fallback.URL = "anthropic", strp("https://api.anthropic.com/fallback")
			},
		},
		{
			name: "default upstream unset means api.openai.com",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Upstream.Default.URL, s.Upstream.Fallback.URL = nil, strp("https://api.openai.com/v2")
			},
		},
		{
			name: "fallback with its own bound key",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Upstream.Fallback.URL, s.Upstream.Fallback.Credential = strp("https://fallback.example.com"), strp(CredentialFallback)
			},
			creds: runtimecfg.CredentialSet{CredentialFallback: dash(CredentialFallback)},
			warnings: []string{
				CodeUpstreamPassthrough + " upstream.default.credential", CodeUpstreamPassthrough + " upstream.anthropic.credential",
			},
		},
		{
			name:  "fallback with an env key",
			edit:  func(s *runtimecfg.RuntimeSettings) { s.Upstream.Fallback.URL = strp("https://fallback.example.com") },
			creds: runtimecfg.CredentialSet{CredentialFallback: {Name: CredentialFallback, Source: "env", Secret: "sk-test-env"}},
			warnings: []string{
				CodeUpstreamPassthrough + " upstream.default.credential", CodeUpstreamPassthrough + " upstream.anthropic.credential",
			},
		},
		{
			name: "a bound fallback key that cannot be read does not count",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Upstream.Fallback.URL, s.Upstream.Fallback.Credential = strp("https://fallback.example.com"), strp(CredentialFallback)
			},
			errs: refused,
		},
		{
			name: "every upstream keyed",
			edit: func(s *runtimecfg.RuntimeSettings) {
				s.Bindings.UpstreamDefault = strp(CredentialDefault) // D41b: the same field
				s.Upstream.Anthropic.Credential = strp(CredentialAnthropic)
			},
			creds: runtimecfg.CredentialSet{CredentialDefault: dash(CredentialDefault), CredentialAnthropic: dash(CredentialAnthropic)},
		},
		{
			name:     "fallback key without a fallback URL",
			edit:     func(s *runtimecfg.RuntimeSettings) { s.Upstream.Fallback.Credential = strp(CredentialFallback) },
			creds:    runtimecfg.CredentialSet{CredentialFallback: dash(CredentialFallback)},
			warnings: []string{CodeFallbackCredentialUnused + " upstream.fallback.credential", CodeUpstreamPassthrough + " upstream.default.credential", CodeUpstreamPassthrough + " upstream.anthropic.credential"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.edit(&s)
			errs, warns := RuntimeRisks(s, tc.creds)
			for i := range errs {
				if errs[i].Message == "" {
					t.Errorf("field error %s has no message", errs[i].Code)
				}
				errs[i].Message = ""
			}
			if !reflect.DeepEqual(errs, tc.errs) {
				t.Errorf("field errors = %+v, want %+v", errs, tc.errs)
			}
			var got []string
			for _, w := range warns {
				if w.Message == "" || w.Hint == "" {
					t.Errorf("warning %s lacks words", w.Code)
				}
				got = append(got, w.Code+" "+w.Path)
			}
			if !reflect.DeepEqual(got, tc.warnings) {
				t.Errorf("warnings = %v, want %v", got, tc.warnings)
			}
		})
	}
}
