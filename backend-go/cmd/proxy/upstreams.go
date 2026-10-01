package main

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/upstream"
)

// upstreamSource serves the upstream set in effect; the /v1 handler reads Current per request
// (proxy.Options.Upstreams). At P4 Step 0 the set is built once from the env; GCONN rebuilds it
// atomically on runtime upstream settings and credentials (D41).
type upstreamSource struct {
	cur atomic.Pointer[upstream.Set]
}

// Current is the upstream set in effect.
func (u *upstreamSource) Current() *upstream.Set { return u.cur.Load() }

// newUpstreams builds the upstream source over the env's upstreams (buildUpstreams). rt is the
// runtime configuration (nil in tests); it is not read yet.
func newUpstreams(cfg *config.Config, auth *authz.Authenticator, rt *runtimecfg.Manager, log *slog.Logger) (*upstreamSource, error) {
	_ = rt
	set, err := buildUpstreams(cfg, auth, log)
	if err != nil {
		return nil, err
	}
	src := &upstreamSource{}
	src.cur.Store(set)
	return src, nil
}

// buildUpstreams builds the gateway's named upstreams (SPEC §2.12 forwarding):
//
//   - openai, the default upstream (TRUTHTABLE_UPSTREAM_URL, _API_KEY on _AUTH_HEADER), serves
//     the OpenAI formats and the ungated endpoints;
//   - anthropic (TRUTHTABLE_ANTHROPIC_UPSTREAM_URL, _API_KEY on x-api-key) serves /v1/messages;
//   - fallback, only when TRUTHTABLE_FALLBACK_UPSTREAM_URL is set, answers the routes with
//     fallback.upstream: fallback in TRUTHTABLE_FALLBACK_UPSTREAM_FORMAT, its key on that
//     family's header (Bearer for openai, x-api-key for anthropic).
//
// Each upstream injects its own key when one is set and passes the client's credentials through
// otherwise (D15); a TrustAgent key is never forwarded (B1). Each is logged with its redacted URL
// and credential mode. Every client shares the header timeout, the total timeout of a JSON answer
// and the idle timeout of a stream.
func buildUpstreams(cfg *config.Config, auth *authz.Authenticator, log *slog.Logger) (*upstream.Set, error) {
	build := func(name, urlKey, base, apiKey, header string) (*upstream.Client, error) {
		c, err := upstream.New(upstream.Config{
			BaseURL: base, APIKey: apiKey, AuthHeader: header, HeaderTimeout: cfg.UpstreamHeaderTimeout,
			Timeout: cfg.UpstreamTimeout, IdleTimeout: cfg.StreamIdleTimeout,
		}, auth.IsProxyKey)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", urlKey, err)
		}
		mode := "passthrough"
		if c.Injecting() {
			mode = "inject"
		}
		log.Info("upstream", "name", name, "url", redactURL(c.Base().String()), "credentials", mode)
		return c, nil
	}
	openai, err := build("openai", "TRUTHTABLE_UPSTREAM_URL", cfg.UpstreamURL, cfg.UpstreamAPIKey, cfg.UpstreamAuthHeader)
	if err != nil {
		return nil, err
	}
	anthropic, err := build("anthropic", "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL", cfg.AnthropicUpstreamURL, cfg.AnthropicUpstreamAPIKey, "x-api-key")
	if err != nil {
		return nil, err
	}
	set := &upstream.Set{OpenAI: openai, Anthropic: anthropic, FallbackFormat: cfg.FallbackUpstreamFormat}
	if set.FallbackFormat == "" {
		set.FallbackFormat = llmapi.FamilyOpenAI
	}
	if cfg.FallbackUpstreamURL != "" {
		header := "authorization"
		if set.FallbackFormat == llmapi.FamilyAnthropic {
			header = "x-api-key"
		}
		if set.Fallback, err = build("fallback ("+set.FallbackFormat+")", "TRUTHTABLE_FALLBACK_UPSTREAM_URL",
			cfg.FallbackUpstreamURL, cfg.FallbackUpstreamAPIKey, header); err != nil {
			return nil, err
		}
	}
	return set, nil
}
