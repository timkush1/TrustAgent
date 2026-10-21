// Package netguard refuses outbound connections to link-local and cloud-metadata addresses at
// dial time (SPEC §7.5, D78). credentials.CheckURL judges a URL by how its host is written, so a
// DNS name that resolves to 169.254.169.254 passes it; a net.Dialer's Control function sees the
// address the name resolved to, just before the connection is made, and refuses it there.
//
// Every outbound client that may call an address an operator or admin chose uses it: the upstream
// reverse proxy, the decision-model client (and the key check built on it) and the OpenRouter
// key exchange. Loopback and private addresses stay allowed: which hosts may use plain http is
// CheckURL's decision.
//
// Through an HTTP(S) proxy (HTTPS_PROXY and friends) the dialer connects to the proxy, not the
// target, so Proxy judges the target first: a literal address, or a name that resolves to a
// forbidden one, is refused before the request leaves.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrForbiddenTarget is the dial error for a link-local or cloud-metadata address.
var ErrForbiddenTarget = errors.New("refused to connect to a link-local or cloud-metadata address")

// metadataAddrs are the cloud instance-metadata addresses outside the link-local ranges: AWS's
// IPv6 one (its IPv4 one, 169.254.169.254, is link-local, like GCP's and Azure's) and Alibaba
// Cloud's, which sits in the shared address space (100.64.0.0/10) the rest of which stays allowed.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("fd00:ec2::254"),
	netip.MustParseAddr("100.100.100.200"),
}

// Forbidden reports an address that never receives a request: link-local unicast or multicast
// (169.254.0.0/16, fe80::/10, ff02::/16 and the rest) or a cloud metadata address (AWS IPv6,
// Alibaba Cloud), in any spelling (an IPv4-mapped IPv6 address is judged as the IPv4 address it
// carries).
func Forbidden(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, m := range metadataAddrs {
		if ip == m {
			return true
		}
	}
	return false
}

// Control is a net.Dialer Control function: address is the resolved ip:port the dialer is about to
// connect to, and a Forbidden one is refused with ErrForbiddenTarget before any packet is sent.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("netguard: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("netguard: the dial address %q is not an IP address", host)
	}
	if Forbidden(ip) {
		return fmt.Errorf("%w (%s)", ErrForbiddenTarget, ip.Unmap())
	}
	return nil
}

// Dialer is a net.Dialer with Control set.
func Dialer(timeout, keepAlive time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: keepAlive, Control: Control}
}

// Transport is http.DefaultTransport's configuration with a guarded dialer (Dialer with the
// default transport's 30 s connect timeout and keep-alive) and a guarded Proxy.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = Dialer(30*time.Second, 30*time.Second).DialContext
	t.Proxy = Proxy
	return t
}

// lookup resolves a host name, and fromEnvironment picks the proxy (http.ProxyFromEnvironment,
// which reads the environment once per process); variables so a test can answer without DNS or a
// proxy.
var (
	lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	fromEnvironment = http.ProxyFromEnvironment
)

// Proxy is http.ProxyFromEnvironment for a guarded transport. When a proxy is used, the dial guard
// sees only the proxy's address, so the target is judged here: a literal Forbidden address is
// refused, and so is a name that resolves to one. A name this host cannot resolve is left to the
// proxy (a proxy-only network may resolve names this host cannot); a direct connection is judged
// by the dialer as usual.
func Proxy(req *http.Request) (*url.URL, error) {
	proxy, err := fromEnvironment(req)
	if err != nil || proxy == nil {
		return proxy, err
	}
	if err := checkTarget(req.Context(), req.URL.Hostname()); err != nil {
		return nil, err
	}
	return proxy, nil
}

// checkTarget refuses a host that is, or resolves to, a Forbidden address.
func checkTarget(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if Forbidden(ip) {
			return fmt.Errorf("%w (%s)", ErrForbiddenTarget, ip.Unmap())
		}
		return nil
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil
	}
	for _, ip := range addrs {
		if Forbidden(ip) {
			return fmt.Errorf("%w (%s resolves to %s)", ErrForbiddenTarget, host, ip.Unmap())
		}
	}
	return nil
}
