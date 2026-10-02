package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"syscall"
	"testing"
	"time"
)

func TestForbiddenAddresses(t *testing.T) {
	for _, s := range []string{
		"169.254.169.254", "169.254.0.1", "169.254.170.2", "fe80::1", "fe80::a9fe:a9fe", "ff02::1",
		"::ffff:169.254.169.254", "fd00:ec2::254",
	} {
		if !Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s is allowed", s)
		}
	}
	for _, s := range []string{
		"127.0.0.1", "::1", "10.0.0.1", "172.17.0.1", "192.168.65.254", "100.64.0.1", "8.8.8.8",
		"::ffff:127.0.0.1", "fd00:ec2::253", "2606:4700::1111",
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
