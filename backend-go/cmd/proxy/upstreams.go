package main

import (
	"log/slog"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/upstream"
)

// newUpstreams builds the gateway's upstreams. For now one client, the configured upstream in
// passthrough mode, serves both API families and there is no fallback upstream (GS-3 builds the
// named upstreams).
func newUpstreams(cfg *config.Config, auth *authz.Authenticator, log *slog.Logger) (*upstream.Set, error) {
	c, err := upstream.New(upstream.Config{
		BaseURL: cfg.UpstreamURL, HeaderTimeout: cfg.UpstreamHeaderTimeout, Timeout: cfg.UpstreamTimeout,
	}, auth.IsProxyKey)
	if err != nil {
		return nil, err
	}
	log.Info("upstream (passthrough credentials)", "url", redactURL(c.Base().String()))
	return &upstream.Set{OpenAI: c, Anthropic: c}, nil
}
