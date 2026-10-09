package main

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/upstream"
)

// upstreamSource serves the upstream set in effect; the /v1 handler reads Current per request
// (proxy.Options.Upstreams). The set is built from the runtime upstream settings and rebuilt
// atomically when they change (D41): a new URL, auth header or fallback format, or a credential
// bound to or unbound from an upstream. A bound dashboard credential is read per request through
// the runtime configuration's KeyFunc, so replacing its secret needs no rebuild. Each rebuild
// tells the authenticator whether any upstream injects a key (open ingest is refused then).
type upstreamSource struct {
	cur  atomic.Pointer[upstream.Set]
	cfg  *config.Config
	auth *authz.Authenticator
	log  *slog.Logger
	// keyFunc yields a bound credential's KeyFunc (the runtime Manager's); nil without one.
	keyFunc func(name string) jev.KeyFunc

	mu    sync.Mutex // serializes apply
	built upstreamShape
}

// upstreamShape is what an upstream set is built from.
type upstreamShape struct {
	defaultURL, defaultHeader, defaultCred       string
	anthropicURL, anthropicHeader, anthropicCred string
	fallbackURL, fallbackFormat, fallbackCred    string
}

// Current is the upstream set in effect.
func (u *upstreamSource) Current() *upstream.Set { return u.cur.Load() }

// newUpstreams builds the upstream source from the runtime configuration rt (nil: the env alone)
// and follows its changes.
func newUpstreams(cfg *config.Config, auth *authz.Authenticator, rt *runtimecfg.Manager, log *slog.Logger) (*upstreamSource, error) {
	src := &upstreamSource{cfg: cfg, auth: auth, log: log}
	var snap *runtimecfg.Snapshot
	if rt != nil {
		src.keyFunc, snap = rt.KeyFunc, rt.Current()
	}
	shape := src.shape(snap)
	set, err := src.build(shape)
	if err != nil {
		return nil, err
	}
	src.install(set, shape)
	if rt != nil {
		rt.Subscribe(src.apply)
	}
	return src, nil
}

// apply follows a new runtime snapshot: a changed shape builds a new set and swaps it in. A set
// that cannot be built (a URL the settings check should have refused) keeps the one in effect.
func (u *upstreamSource) apply(snap *runtimecfg.Snapshot) {
	shape := u.shape(snap)
	u.mu.Lock()
	defer u.mu.Unlock()
	if shape == u.built {
		return
	}
	set, err := u.build(shape)
	if err != nil {
		u.log.Error("upstream rebuild failed: the previous upstreams stay in effect", "err", err)
		return
	}
	u.install(set, shape)
	u.log.Info("upstreams changed", "inject", setInjecting(set))
}

// install puts set in effect and tells the authenticator about inject mode: an upstream that
// injects a key, or any env upstream key (config.Injecting, as at boot).
func (u *upstreamSource) install(set *upstream.Set, shape upstreamShape) {
	u.cur.Store(set)
	u.built = shape
	if u.auth != nil {
		u.auth.SetInjecting(setInjecting(set) || u.cfg.Injecting())
	}
}

// setInjecting reports whether any upstream of set injects a key of its own (D15).
func setInjecting(set *upstream.Set) bool {
	for _, c := range []*upstream.Client{set.OpenAI, set.Anthropic, set.Fallback} {
		if c != nil && c.Injecting() {
			return true
		}
	}
	return false
}

// shape is what snap (nil: the env) builds. The default upstream's credential is
// upstream.default.credential, or bindings.upstream_default (one field, D41b).
func (u *upstreamSource) shape(snap *runtimecfg.Snapshot) upstreamShape {
	cfg := u.cfg
	s := upstreamShape{
		defaultURL: cfg.UpstreamURL, defaultHeader: cfg.UpstreamAuthHeader,
		anthropicURL: cfg.AnthropicUpstreamURL, anthropicHeader: "x-api-key",
		fallbackURL: cfg.FallbackUpstreamURL, fallbackFormat: cfg.FallbackUpstreamFormat,
	}
	if snap == nil {
		return s
	}
	up := snap.Settings.Upstream
	s.defaultURL, s.defaultHeader = strval(up.Default.URL), up.Default.AuthHeader
	s.anthropicURL = strval(up.Anthropic.URL)
	if up.Anthropic.AuthHeader != "" {
		s.anthropicHeader = up.Anthropic.AuthHeader
	}
	s.fallbackURL, s.fallbackFormat = strval(up.Fallback.URL), up.Fallback.Format
	s.defaultCred = strval(up.Default.Credential)
	if s.defaultCred == "" {
		s.defaultCred = strval(snap.Settings.Bindings.UpstreamDefault)
	}
	s.anthropicCred, s.fallbackCred = strval(up.Anthropic.Credential), strval(up.Fallback.Credential)
	return s
}

// build builds the set of a shape (buildUpstreams).
func (u *upstreamSource) build(s upstreamShape) (*upstream.Set, error) {
	return buildUpstreams(u.cfg, s, u.keyFunc, u.auth, u.log)
}

// buildUpstreams builds the gateway's named upstreams (SPEC §2.12 forwarding) from a shape:
//
//   - openai, the default upstream (upstream.default: TRUTHTABLE_UPSTREAM_URL, its key on
//     _AUTH_HEADER), serves the OpenAI formats and the ungated endpoints;
//   - anthropic (upstream.anthropic: TRUTHTABLE_ANTHROPIC_UPSTREAM_URL, its key on x-api-key)
//     serves /v1/messages;
//   - fallback, only when a fallback URL is set, answers the routes with fallback.upstream:
//     fallback in the fallback format, its key on that family's header (Bearer for openai,
//     x-api-key for anthropic).
//
// Each upstream injects its own key when it has one and passes the client's credentials through
// otherwise (D15); a TrustAgent key is never forwarded (B1). Its key is the env's
// (TRUTHTABLE_*UPSTREAM_API_KEY, which always wins, D33) or the dashboard credential bound to it,
// read per request through keyFunc for the upstream's origin (D41, D78). Each is logged with its
// redacted URL and credential mode. Every client shares the header timeout, the total timeout of
// a JSON answer and the idle timeout of a stream.
func buildUpstreams(cfg *config.Config, s upstreamShape, keyFunc func(string) jev.KeyFunc, auth *authz.Authenticator, log *slog.Logger) (*upstream.Set, error) {
	var isProxyKey func(string) bool
	if auth != nil {
		isProxyKey = auth.IsProxyKey
	}
	build := func(name, urlKey, base, envKey, cred, header string) (*upstream.Client, error) {
		uc := upstream.Config{
			BaseURL: base, APIKey: envKey, AuthHeader: header, HeaderTimeout: cfg.UpstreamHeaderTimeout,
			Timeout: cfg.UpstreamTimeout, IdleTimeout: cfg.StreamIdleTimeout,
		}
		mode := "passthrough"
		switch {
		case envKey != "":
			mode = "inject"
		case cred != "" && keyFunc != nil:
			uc.Key, mode = upstream.KeyFunc(keyFunc(cred)), "inject"
		}
		c, err := upstream.New(uc, isProxyKey)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", urlKey, err)
		}
		attrs := []any{"name", name, "url", redactURL(c.Base().String()), "credentials", mode}
		if uc.Key != nil {
			attrs = append(attrs, "credential", cred)
		}
		log.Info("upstream", attrs...)
		return c, nil
	}
	openai, err := build("openai", "TRUTHTABLE_UPSTREAM_URL", s.defaultURL, cfg.UpstreamAPIKey, s.defaultCred, s.defaultHeader)
	if err != nil {
		return nil, err
	}
	anthropicURL := s.anthropicURL
	if anthropicURL == "" {
		anthropicURL = upstream.DefaultAnthropicBaseURL
	}
	anthropic, err := build("anthropic", "TRUTHTABLE_ANTHROPIC_UPSTREAM_URL", anthropicURL, cfg.AnthropicUpstreamAPIKey, s.anthropicCred, s.anthropicHeader)
	if err != nil {
		return nil, err
	}
	set := &upstream.Set{OpenAI: openai, Anthropic: anthropic, FallbackFormat: s.fallbackFormat}
	if set.FallbackFormat == "" {
		set.FallbackFormat = llmapi.FamilyOpenAI
	}
	if s.fallbackURL != "" {
		header := "authorization"
		if set.FallbackFormat == llmapi.FamilyAnthropic {
			header = "x-api-key"
		}
		if set.Fallback, err = build("fallback ("+set.FallbackFormat+")", "TRUTHTABLE_FALLBACK_UPSTREAM_URL",
			s.fallbackURL, cfg.FallbackUpstreamAPIKey, s.fallbackCred, header); err != nil {
			return nil, err
		}
	}
	return set, nil
}
