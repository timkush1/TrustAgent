package jev

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Transport tuning (SPEC §3.1: MaxIdleConnsPerHost ≥ 64, idle 5 m, HTTP/2).
const (
	transportMaxIdlePerHost = 64
	transportIdleTimeout    = 5 * time.Minute
)

// NewTransport builds the dedicated decision-model transport: HTTP/2 over TLS (ALPN), enough idle
// connections per host for the in-flight cap, 5-minute idle keep-alive so the keep-warm ping keeps
// a connection hot, and no proxy environment surprises beyond the standard ones. It is never
// shared with the upstream reverse proxy.
func NewTransport() *http.Transport {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		MaxIdleConns:          2 * transportMaxIdlePerHost,
		MaxIdleConnsPerHost:   transportMaxIdlePerHost,
		IdleConnTimeout:       transportIdleTimeout,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 0, // bounded by the per-attempt context
	}
}
