package main

import (
	"fmt"
	"log/slog"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/upstream"
)

// newUpstreams builds the gateway's named upstreams (SPEC §2.12 forwarding):
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
func newUpstreams(cfg *config.Config, auth *authz.Authenticator, log *slog.Logger) (*upstream.Set, error) {
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
