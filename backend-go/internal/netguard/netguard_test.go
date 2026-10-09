package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestForbiddenAddresses(t *testing.T) {
	for _, s := range []string{
		"169.254.169.254", "169.254.0.1", "169.254.170.2", "fe80::1", "fe80::a9fe:a9fe", "ff02::1",
		"::ffff:169.254.169.254", "fd00:ec2::254", "100.100.100.200", "::ffff:100.100.100.200",
	} {
		if !Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s is allowed", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.17.0.1", "192.168.65.254", "100.64.0.1", "8.8.8.8",
		"::ffff:127.0.0.1", "fd00:ec2::253", "2606:4700::1111", "100.100.100.100", "100.100.100.201",
	} {
		if Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s is refused", s)
		}
	}
}

func TestControlRefusesBeforeConnecting(t *testing.T) {
	for _, addr := range []string{"169.254.169.254:80", "[fe80::1]:443", "[::ffff:169.254.169.254]:80", "[fd00:ec2::254]:80"} {
		if err := Control("tcp", addr, nil); !errors.Is(err, ErrForbiddenTarget) {
			t.Errorf("Control(%s) = %v, want ErrForbiddenTarget", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:443", "10.1.2.3:11434"} {
		if err := Control("tcp", addr, nil); err != nil {
			t.Errorf("Control(%s) = %v", addr, err)
		}
	}
	if err := Control("tcp", "not-an-ip:80", nil); err == nil {
		t.Error("Control accepted a dial address that is not an IP")
	}
}

// The dialer refuses a metadata address without a network round trip, and still reaches a
// loopback server.
func TestDialerAndTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := Dialer(5*time.Second, 0).DialContext(ctx, "tcp", "169.254.169.254:80"); !errors.Is(err, ErrForbiddenTarget) {
		t.Fatalf("dial metadata = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the refusal took %v: it should not wait for a connection", time.Since(start))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	hc := &http.Client{Transport: Transport(), Timeout: 5 * time.Second}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatalf("a loopback server: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := hc.Get("http://169.254.169.254/latest/meta-data/"); !errors.Is(err, ErrForbiddenTarget) {
		t.Errorf("GET metadata through Transport = %v", err)
	}
	var opErr *net.OpError
	if _, err := hc.Get("http://[fd00:ec2::254]/"); !errors.As(err, &opErr) || !errors.Is(err, ErrForbiddenTarget) {
		t.Errorf("GET the IPv6 metadata address = %v", err)
	}
}

// Control sees the address a host name resolved to, which is how a name that resolves to the
// metadata address is caught although CheckURL accepted its spelling.
func TestControlSeesTheResolvedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	var seen string
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
		seen = address
		return Control(network, address, c)
	}}
	conn, err := d.DialContext(context.Background(), "tcp4", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if host, _, _ := net.SplitHostPort(seen); host != "127.0.0.1" {
		t.Errorf("Control saw %q, want the resolved 127.0.0.1", seen)
	}
}

// Through an HTTP(S) proxy the dialer sees the proxy's address, so Proxy judges the target: a
// literal forbidden address, or a name that resolves to one, is refused before the request leaves;
// a name this host cannot resolve is left to the proxy, and without a proxy the dialer judges.
func TestProxyJudgesTheTarget(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.internal:3128")
	useProxy := true
	prevEnv, prevLookup := fromEnvironment, lookup
	t.Cleanup(func() { fromEnvironment, lookup = prevEnv, prevLookup })
	fromEnvironment = func(*http.Request) (*url.URL, error) {
		if useProxy {
			return proxyURL, nil
		}
		return nil, nil
	}
	lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "metadata.example":
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}, nil
		case "api.example":
			return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
		}
		return nil, errors.New("no such host")
	}
	req := func(raw string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, raw := range []string{"http://169.254.169.254/latest/", "http://[fd00:ec2::254]/", "http://100.100.100.200/", "https://metadata.example/v1"} {
		if p, err := Proxy(req(raw)); !errors.Is(err, ErrForbiddenTarget) || p != nil {
			t.Errorf("Proxy(%s) = %v, %v; want ErrForbiddenTarget", raw, p, err)
		}
	}
	for _, raw := range []string{"https://api.example/v1", "https://unresolvable.example/", "http://10.0.0.5:11434/"} {
		if p, err := Proxy(req(raw)); err != nil || p != proxyURL {
			t.Errorf("Proxy(%s) = %v, %v; want the proxy", raw, p, err)
		}
	}
	useProxy = false
	if p, err := Proxy(req("http://169.254.169.254/")); p != nil || err != nil {
		t.Errorf("without a proxy, Proxy = %v, %v; want a direct connection (the dialer judges it)", p, err)
	}
	if Transport().Proxy == nil {
		t.Error("Transport has no Proxy")
	}
}
