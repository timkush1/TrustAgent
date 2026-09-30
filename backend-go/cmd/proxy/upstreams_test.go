package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/upstream"
)

// upstreamsProbe records the credential headers each upstream path receives.
type upstreamsProbe struct {
	*httptest.Server
	mu   sync.Mutex
	seen map[string]http.Header // by base path prefix
}

func newUpstreamsProbe(t *testing.T) *upstreamsProbe {
	t.Helper()
	p := &upstreamsProbe{seen: map[string]http.Header{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		p.mu.Lock()
		p.seen[strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]] = r.Header.Clone()
		p.mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *upstreamsProbe) call(t *testing.T, c *upstream.Client, hdr http.Header) http.Header {
	t.Helper()
	resp, cancel, err := c.Do(context.Background(), http.MethodPost, "/v1/chat/completions", "", hdr, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	base := strings.SplitN(strings.TrimPrefix(c.Base().Path, "/"), "/", 2)[0]
	return p.seen[base]
}

// TestNewUpstreamsBuildsTheNamedUpstreams (GS-3, D15): the default, Anthropic and fallback
// upstreams each go to their own base and inject only their own key, on their family's header;
// the log names each with a redacted URL and its credential mode, and no credential.
func TestNewUpstreamsBuildsTheNamedUpstreams(t *testing.T) {
	probe := newUpstreamsProbe(t)
	cfg := config.LoadWith(config.LoadOptions{Environ: []string{
		"TRUTHTABLE_UPSTREAM_URL=" + probe.URL + "/openai?api-version=1",
		"TRUTHTABLE_UPSTREAM_API_KEY=sk-openai-injected-0001",
		"TRUTHTABLE_UPSTREAM_AUTH_HEADER=api-key",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_URL=" + probe.URL + "/anthropic",
		"TRUTHTABLE_FALLBACK_UPSTREAM_URL=" + strings.Replace(probe.URL, "http://", "http://user:pw@", 1) + "/fallback",
		"TRUTHTABLE_FALLBACK_UPSTREAM_API_KEY=sk-fallback-injected-0003",
		"TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT=anthropic",
		"TRUTHTABLE_API_KEYS=app=ta-upstreams-proxy-key",
	}})
	if len(cfg.Warnings) != 0 {
		t.Fatalf("warnings %v", cfg.Warnings)
	}
	auth := authz.New([]authz.Key{{Label: "app", Secret: "ta-upstreams-proxy-key", Scopes: authz.APIKeyScopes}}, "keys")
	var logs bytes.Buffer
	set, err := newUpstreams(cfg, auth, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if set.OpenAI == nil || set.Anthropic == nil || set.Fallback == nil || set.FallbackFormat != "anthropic" {
		t.Fatalf("set %+v", set)
	}
	if !set.OpenAI.Injecting() || set.Anthropic.Injecting() || !set.Fallback.Injecting() {
		t.Errorf("inject per upstream: openai %v anthropic %v fallback %v", set.OpenAI.Injecting(), set.Anthropic.Injecting(), set.Fallback.Injecting())
	}
	client := http.Header{"Authorization": {"Bearer ta-upstreams-proxy-key"}, "X-Api-Key": {"sk-ant-client-own"}}
	if h := probe.call(t, set.OpenAI, client); h.Get("Api-Key") != "sk-openai-injected-0001" || h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" {
		t.Errorf("openai upstream got %v", h)
	}
	if h := probe.call(t, set.Anthropic, client); h.Get("X-Api-Key") != "sk-ant-client-own" || h.Get("Authorization") != "" {
		t.Errorf("anthropic upstream (passthrough) got %v", h)
	}
	if h := probe.call(t, set.Fallback, client); h.Get("X-Api-Key") != "sk-fallback-injected-0003" || h.Get("Authorization") != "" {
		t.Errorf("fallback upstream got %v", h)
	}

	out := logs.String()
	for _, want := range []string{
		"name=openai", "name=anthropic", `name="fallback (anthropic)"`, "credentials=inject", "credentials=passthrough",
		"/openai", "user:xxxxx@",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"sk-openai-injected", "sk-fallback-injected", "api-version", ":pw@"} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q:\n%s", secret, out)
		}
	}
}

// TestNewUpstreamsWithoutFallbackAndBadURLs: without a fallback URL there is no fallback
// upstream; an upstream URL the client refuses fails the boot, naming its key.
func TestNewUpstreamsWithoutFallbackAndBadURLs(t *testing.T) {
	auth := authz.New(nil, "keys")
	quiet := slog.New(slog.DiscardHandler)
	set, err := newUpstreams(config.LoadWith(config.LoadOptions{Environ: []string{}}), auth, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if set.Fallback != nil || set.FallbackFormat != "openai" || set.OpenAI.Base().String() != config.DefaultUpstreamURL ||
		set.Anthropic.Base().String() != "https://api.anthropic.com" || set.OpenAI.Injecting() || set.Anthropic.Injecting() {
		t.Errorf("defaults: %+v openai %s anthropic %s", set, set.OpenAI.Base(), set.Anthropic.Base())
	}
	for key, value := range map[string]string{
		"TRUTHTABLE_UPSTREAM_URL":           "ftp://files.example",
		"TRUTHTABLE_ANTHROPIC_UPSTREAM_URL": "postgres://db.example/x",
		"TRUTHTABLE_FALLBACK_UPSTREAM_URL":  "redis://cache.example",
	} {
		cfg := config.LoadWith(config.LoadOptions{Environ: []string{key + "=" + value}})
		if _, err := newUpstreams(cfg, auth, quiet); err == nil || !strings.HasPrefix(err.Error(), key+": ") {
			t.Errorf("%s=%s: %v", key, value, err)
		}
	}
}
