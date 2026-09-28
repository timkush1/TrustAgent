// Package upstream forwards requests to the LLM provider (SPEC §2.12 forwarding, §3.1).
//
// S0 covers the default upstream in passthrough mode: URL join with query merge (M2), the request
// on the client's context (M1), header filtering that drops every X-TrustAgent-* header,
// hop-by-hop headers, Cookie, Host, Content-Length and Accept-Encoding (B2; gzip then stays
// transparent and a stray Content-Encoding is decoded defensively), the client's own credential
// passed through while a TrustAgent key is never forwarded (B1), and a ReverseProxy for ungated
// endpoints. Named upstreams, inject mode, deadline middleware, 413 handling and the M3/M12
// timeouts land in P2.
package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the upstream when TRUTHTABLE_UPSTREAM_URL is empty.
const DefaultBaseURL = "https://api.openai.com"

// Config configures the default upstream.
type Config struct {
	// BaseURL is the upstream base: its path prefixes every request path.
	BaseURL string
	// HeaderTimeout bounds the wait for response headers (TRUTHTABLE_UPSTREAM_HEADER_TIMEOUT).
	HeaderTimeout time.Duration
	// Timeout bounds a whole non-streaming exchange (TRUTHTABLE_UPSTREAM_TIMEOUT).
	Timeout time.Duration
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
}

// Client sends requests to one upstream.
type Client struct {
	base       *url.URL
	transport  http.RoundTripper
	timeout    time.Duration
	isProxyKey func(string) bool
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
		return nil, fmt.Errorf("upstream: invalid base URL %q (want http(s)://host[/path])", raw)
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
	return &Client{base: u, transport: t, timeout: cfg.Timeout, isProxyKey: isProxyKey}, nil
}

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

// OutboundHeaders filters the client's request headers for the upstream.
func (c *Client) OutboundHeaders(in http.Header) http.Header {
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
	// Passthrough: the client's credential goes upstream untouched, unless it is a TrustAgent key.
	if v := out.Get("Authorization"); v != "" {
		if token, ok := cutBearer(v); ok && c.isProxyKey(token) {
			out.Del("Authorization")
		}
	}
	for _, h := range []string{"X-Api-Key", "Api-Key"} {
		if v := out.Get(h); v != "" && c.isProxyKey(v) {
			out.Del(h)
		}
	}
	return out
}

func cutBearer(v string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(v), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func isTrustAgentHeader(name string) bool {
	return len(name) >= len("X-Trustagent-") && strings.EqualFold(name[:len("X-Trustagent-")], "X-Trustagent-")
}

// Do sends a non-streaming request upstream. The request runs on ctx, the client's context, so a
// client that goes away cancels it (M1), bounded by the configured timeout. The caller closes the
// response body; the returned cancel releases the timeout once the body is consumed.
func (c *Client) Do(ctx context.Context, method, path, rawQuery string, header http.Header, body []byte) (*http.Response, context.CancelFunc, error) {
	cancel := context.CancelFunc(func() {})
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL(path, rawQuery).String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header = c.OutboundHeaders(header)
	req.ContentLength = int64(len(body))
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

// IsTimeout reports whether err is an upstream timeout (as opposed to a refused or reset
// connection), for choosing 504 over 502.
func IsTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// ReadBody reads at most limit bytes of a response body. It reports whether the body is larger;
// the caller then streams the rest from resp.Body after what was read. A gzip body the transport did
// not decode itself (a stray Content-Encoding) is decoded here, and its encoding headers removed.
func ReadBody(resp *http.Response, limit int64) ([]byte, bool, error) {
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") && !resp.Uncompressed {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, false, fmt.Errorf("upstream: undecodable gzip body: %w", err)
		}
		resp.Body = struct {
			io.Reader
			io.Closer
		}{zr, resp.Body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b, true, nil
	}
	return b, false, nil
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

// ReverseProxy forwards an ungated request as is (headers filtered, credential passed through),
// streaming the response back. onError writes a TrustAgent error for a failed upstream call.
func (c *Client) ReverseProxy(onError func(w http.ResponseWriter, r *http.Request, err error)) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = c.URL(pr.In.URL.Path, pr.In.URL.RawQuery)
			pr.Out.Host = pr.Out.URL.Host
			pr.Out.Header = c.OutboundHeaders(pr.In.Header)
		},
		Transport: c.transport,
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
