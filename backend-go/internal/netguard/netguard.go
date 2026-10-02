// Package netguard refuses outbound connections to link-local and cloud-metadata addresses at
// dial time (SPEC §7.5, D78). credentials.CheckURL judges a URL by how its host is written, so a
// DNS name that resolves to 169.254.169.254 passes it; a net.Dialer's Control function sees the
// address the name resolved to, just before the connection is made, and refuses it there.
//
// Every outbound client that may call an address an operator or admin chose uses it: the upstream
// reverse proxy, the decision-model client (and the key check built on it) and the OpenRouter
// key exchange. Loopback and private addresses stay allowed: which hosts may use plain http is
// CheckURL's decision.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrForbiddenTarget is the dial error for a link-local or cloud-metadata address.
var ErrForbiddenTarget = errors.New("refused to connect to a link-local or cloud-metadata address")

// metadataV6 is the AWS instance-metadata IPv6 address; the IPv4 one (169.254.169.254) is
// link-local.
var metadataV6 = netip.MustParseAddr("fd00:ec2::254")

// Forbidden reports an address that never receives a request: link-local unicast or multicast
// (169.254.0.0/16, fe80::/10, ff02::/16 and the rest) or the AWS IPv6 metadata address, in any
// spelling (an IPv4-mapped IPv6 address is judged as the IPv4 address it carries).
func Forbidden(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == metadataV6
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
// default transport's 30 s connect timeout and keep-alive).
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = Dialer(30*time.Second, 30*time.Second).DialContext
	return t
}
