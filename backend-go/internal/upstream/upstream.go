// Package upstream forwards requests to the LLM providers (SPEC §2.12 forwarding, §3.1).
//
// A Client is one named upstream: the default (OpenAI formats), the Anthropic one (/v1/messages)
// or the optional fallback upstream; Set holds them. Every request is built from the upstream's
// base URL joined with the request path, the base query merged with the client's (M2), and runs
// on the client's context (M1). Headers are filtered: every X-TrustAgent-* header, hop-by-hop
// headers, Cookie, Host, Content-Length and Accept-Encoding go (B2: gzip then stays transparent,
// and a stray Content-Encoding is decoded defensively by DecodeBody).
//
// Credentials (B1, D15): an upstream with a key of its own injects it (inject mode): the client's
// Authorization, X-Api-Key and Api-Key headers are dropped and the key is sent on the upstream's
// auth header. The key is the env's (Config.APIKey) or a dashboard credential bound to the
// upstream (Config.Key, D41), fetched per request for the upstream's canonical origin so
// the credentials store can refuse an origin the credential was not created for (D78). Without a
// key the client's credential passes through untouched (passthrough mode). A TrustAgent key is
// never forwarded, wherever the client put it.
//
// Timeouts (M12): the transport bounds the wait for response headers; Do bounds a whole JSON
// answer with the total timeout, and a streamed body (StreamBody) with the idle timeout between
// reads instead. The end of every answer Do fetched is observed as upstream_seconds{format}.
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
)

// DefaultBaseURL is the upstream when TRUTHTABLE_UPSTREAM_URL is empty.
const DefaultBaseURL = "https://api.openai.com"

// DefaultAnthropicBaseURL is the Anthropic upstream when none is configured (the registry
// default of TRUTHTABLE_ANTHROPIC_UPSTREAM_URL).
const DefaultAnthropicBaseURL = "https://api.anthropic.com"

// KeyFunc returns the credential to inject on a call to origin, the upstream's canonical origin
// scheme://host[:port] (jev.CanonicalOrigin). The runtime configuration's KeyFunc refuses an
// origin outside the bound credential's allowed origins (D78).
type KeyFunc func(ctx context.Context, origin string) (string, error)

// ErrCredentialUnavailable is the failure of a call whose injected credential cannot be had (a
// deleted, undecryptable or origin-refused dashboard credential). Nothing is sent upstream: the
// request is never retried with the client's own credential instead.
var ErrCredentialUnavailable = errors.New("upstream: the upstream credential is not available")

// Config configures one upstream.
type Config struct {
	// BaseURL is the upstream base: its path prefixes every request path.
	BaseURL string
	// APIKey, when set, is injected (inject mode): the client's Authorization, X-Api-Key and
	// Api-Key headers are dropped and APIKey is sent on AuthHeader instead.
	APIKey string
	// Key, when set and APIKey is not, supplies the injected credential per request (a dashboard
	// credential bound to this upstream, D41): inject mode as with APIKey.
	Key KeyFunc
	// AuthHeader carries APIKey: authorization (the default) sends "Bearer <key>", any other
	// header (x-api-key, api-key) the key as it is (TRUTHTABLE_UPSTREAM_AUTH_HEADER).
	AuthHeader string
	// HeaderTimeout bounds the wait for response headers (TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT).
	HeaderTimeout time.Duration
	// Timeout bounds a whole non-streaming exchange (TRUTHTABLE_UPSTREAM_TIMEOUT).
	Timeout time.Duration
	// IdleTimeout bounds each read of a streamed body (TRUTHTABLE_STREAM_IDLE_TIMEOUT); 0 means
	// none.
	IdleTimeout time.Duration
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
}

// Client sends requests to one upstream.
type Client struct {
	base          *url.URL
	transport     http.RoundTripper
	timeout, idle time.Duration
	apiKey        string
	key           KeyFunc
	origin        string // the base URL's canonical origin, asserted by key
	authHeader    string // canonical
	isProxyKey    func(string) bool
}

// New builds a client. isProxyKey reports TrustAgent keys, which are stripped wherever a client
// put one (B1); nil means none are configured.
func New(cfg Config, isProxyKey func(string) bool) (*Client, error) {
	raw := cfg.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("upstream: invalid base URL %q (want http(s)://host[/path])", redact(raw))
	}
	header := strings.TrimSpace(cfg.AuthHeader)
	if header == "" {
		header = "Authorization"
	}
	if strings.ContainsAny(header, " \t\r\n:") {
		return nil, fmt.Errorf("upstream: invalid auth header %q", header)
	}
	if isProxyKey == nil {
		isProxyKey = func(string) bool { return false }
	}
	t := cfg.Transport
	if t == nil {
		dt := http.DefaultTransport.(*http.Transport).Clone()
		dt.ResponseHeaderTimeout = cfg.HeaderTimeout
		dt.MaxIdleConnsPerHost = 64
		dt.IdleConnTimeout = 90 * time.Second
		t = dt
	}
	c := &Client{
		base: u, transport: t, timeout: cfg.Timeout, idle: cfg.IdleTimeout,
		apiKey: cfg.APIKey, origin: jev.CanonicalOrigin(u), authHeader: http.CanonicalHeaderKey(header), isProxyKey: isProxyKey,
	}
	if cfg.APIKey == "" {
		c.key = cfg.Key
	}
	return c, nil
}

// redact is a URL safe for an error message: a password becomes xxxxx and the query goes (a base
// URL may carry an api-key parameter).
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable)"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.Redacted()
}

// Injecting reports whether the upstream injects its own credential (inject mode).
func (c *Client) Injecting() bool { return c.apiKey != "" || c.key != nil }

// Origin is the canonical origin of the base URL, the one an injected credential is asserted for.
func (c *Client) Origin() string { return c.origin }

// credential is the key to inject: the static key, or the bound credential for the origin.
func (c *Client) credential(ctx context.Context) (string, error) {
	if c.apiKey != "" {
		return c.apiKey, nil
	}
	key, err := c.key(ctx, c.origin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCredentialUnavailable, err)
	}
	if key == "" {
		return "", ErrCredentialUnavailable
	}
	return key, nil
}

// Timeout is the total timeout of a JSON exchange (0: none).
func (c *Client) Timeout() time.Duration { return c.timeout }

// IdleTimeout is the longest wait for the next read of a stream (0: none).
func (c *Client) IdleTimeout() time.Duration { return c.idle }

// Base returns the upstream base URL.
func (c *Client) Base() *url.URL { u := *c.base; return &u }

// URL joins the base with a request path and query: base path + request path, base query then
// client query (M2).
func (c *Client) URL(path, rawQuery string) *url.URL {
	u := *c.base
	u.Path = strings.TrimSuffix(c.base.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	u.RawPath = ""
	switch {
	case c.base.RawQuery == "":
		u.RawQuery = rawQuery
	case rawQuery != "":
		u.RawQuery = c.base.RawQuery + "&" + rawQuery
	}
	u.Fragment = ""
	return &u
}

// hopByHop are the connection-level headers a proxy never forwards (RFC 9110 §7.6.1).
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// credentialHeaders are the request headers that carry an LLM API credential.
var credentialHeaders = []string{"Authorization", "X-Api-Key", "Api-Key"}

// OutboundHeaders filters the client's request headers for the upstream and sets its credential:
// the configured key in inject mode, else the client's own (never a TrustAgent key). In inject
// mode a credential that cannot be had leaves no credential at all (Outbound reports why).
func (c *Client) OutboundHeaders(in http.Header) http.Header {
	out, _ := c.Outbound(context.Background(), in)
	return out
}

// Outbound is OutboundHeaders for a request running on ctx. In inject mode it fails with
// ErrCredentialUnavailable when the injected credential cannot be had; the headers it returns
// then carry no credential, so the caller must not send them.
func (c *Client) Outbound(ctx context.Context, in http.Header) (http.Header, error) {
	out := in.Clone()
	if out == nil {
		out = http.Header{}
	}
	for _, v := range in.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			out.Del(strings.TrimSpace(name))
		}
	}
	for _, h := range hopByHop {
		out.Del(h)
	}
	for name := range out {
		if isTrustAgentHeader(name) {
			delete(out, name)
		}
	}
	for _, h := range []string{"Cookie", "Host", "Content-Length", "Accept-Encoding"} {
		out.Del(h)
	}
	if c.Injecting() {
		// Inject mode: no client credential goes upstream (a proxy key a client sent as its
		// credential goes with them), the upstream's own does.
		for name := range out {
			if isCredentialHeader(name) {
				delete(out, name)
			}
		}
		key, err := c.credential(ctx)
		if err != nil {
			return out, err
		}
		if c.authHeader == "Authorization" {
			out.Set("Authorization", "Bearer "+key)
		} else {
			out.Set(c.authHeader, key)
		}
		return out, nil
	}
	// Passthrough: the client's credential goes upstream untouched, unless it is a TrustAgent key.
	for name, values := range out {
		if isCredentialHeader(name) && slices.ContainsFunc(values, c.carriesProxyKey) {
			delete(out, name)
		}
	}
	return out, nil
}

// isCredentialHeader reports the request headers that carry an LLM API credential, in any
// spelling: Authorization, X-Api-Key and Api-Key.
func isCredentialHeader(name string) bool {
	return slices.ContainsFunc(credentialHeaders, func(h string) bool { return strings.EqualFold(h, name) })
}

// carriesProxyKey reports whether one value of a credential header carries a TrustAgent key: as
// the whole value, or as the credential after a scheme (Bearer or any other), so a proxy key is
// never forwarded, however the client sent it (B1).
func (c *Client) carriesProxyKey(v string) bool {
	v = strings.TrimSpace(v)
	if c.isProxyKey(v) {
		return true
	}
	_, token, ok := strings.Cut(v, " ")
	return ok && c.isProxyKey(strings.TrimSpace(token))
}

func isTrustAgentHeader(name string) bool {
	return len(name) >= len("X-Trustagent-") && strings.EqualFold(name[:len("X-Trustagent-")], "X-Trustagent-")
}

// Do sends a request upstream. The request runs on ctx, the client's context, so a client that
// goes away cancels it (M1), bounded by the total timeout until the answer is read, or, once
// StreamBody takes a streamed body over, by the idle timeout of each read. The caller closes the
// response body; the returned cancel releases the exchange once the body is consumed. A request
// the timeout aborted fails with an error IsTimeout reports.
func (c *Client) Do(ctx context.Context, method, path, rawQuery string, header http.Header, body []byte) (*http.Response, context.CancelFunc, error) {
	start := time.Now()
	ctx, cancelCause := context.WithCancelCause(ctx)
	d := &deadline{cancel: cancelCause}
	d.arm(c.timeout, errTotalTimeout)
	release := func() {
		d.disarm()
		cancelCause(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path, rawQuery).String(), bytes.NewReader(body))
	if err != nil {
		release()
		return nil, nil, err
	}
	if req.Header, err = c.Outbound(ctx, header); err != nil {
		release()
		return nil, nil, err
	}
	req.ContentLength = int64(len(body))
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		if fired := d.err(); fired != nil {
			err = fired
		}
		release()
		return nil, nil, err
	}
	resp.Body = &timedBody{rc: resp.Body, d: d, start: start, format: llmapi.FormatForPath(path)}
	return resp, release, nil
}

// IsTimeout reports whether err is an upstream timeout (as opposed to a refused or reset
// connection), for choosing 504 over 502.
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// CopyResponseHeaders copies an upstream response's headers for the client with multi-value Add,
// minus hop-by-hop headers, Content-Length and Content-Encoding (the gateway writes the body
// itself), and the headers the gateway owns (X-TrustAgent-*, X-Request-ID and CORS).
func CopyResponseHeaders(dst, src http.Header) {
	skip := map[string]bool{"Content-Length": true, "Content-Encoding": true, "X-Request-Id": true}
	for _, h := range hopByHop {
		skip[h] = true
	}
	for _, v := range src.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			skip[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		name = http.CanonicalHeaderKey(name)
		if skip[name] || isTrustAgentHeader(name) || strings.HasPrefix(name, "Access-Control-") {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

// ReverseProxy forwards an ungated request as is (headers filtered, credential passed through or
// injected), streaming the response back. onError writes a TrustAgent error for a failed upstream
// call, including one whose injected credential cannot be had (nothing is sent then).
func (c *Client) ReverseProxy(onError func(w http.ResponseWriter, r *http.Request, err error)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = c.URL(pr.In.URL.Path, pr.In.URL.RawQuery)
			pr.Out.Host = pr.Out.URL.Host
			h, err := c.Outbound(pr.In.Context(), pr.In.Header)
			pr.Out.Header = h
			if err != nil {
				// Rewrite cannot fail: the transport refuses the request instead.
				pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), credentialErrKey{}, err))
			}
		},
		Transport: refuseWithoutCredential{next: c.transport},
		ModifyResponse: func(resp *http.Response) error {
			for name := range resp.Header {
				if isTrustAgentHeader(name) || strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") ||
					http.CanonicalHeaderKey(name) == "X-Request-Id" {
					resp.Header.Del(name)
				}
			}
			return nil
		},
		ErrorHandler: onError,
	}
}

// credentialErrKey carries a ReverseProxy request's credential failure from Rewrite to the
// transport.
type credentialErrKey struct{}

// refuseWithoutCredential fails a request whose injected credential could not be had before
// anything is sent.
type refuseWithoutCredential struct{ next http.RoundTripper }

func (t refuseWithoutCredential) RoundTrip(r *http.Request) (*http.Response, error) {
	if err, ok := r.Context().Value(credentialErrKey{}).(error); ok {
		return nil, err
	}
	return t.next.RoundTrip(r)
}
