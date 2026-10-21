package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// The P6 additions to gate-eval (D203 endpoint host, D204 built-in policy, D205 warm connection and
// one-at-a-time evaluation), against an in-process mock-upstream: no vendor, no key, no spend.

// countingVendor serves mock-upstream over TLS (HTTP/2, like the vendor) and counts the TCP
// connections it accepts and the requests it answers.
type countingVendor struct {
	srv      *httptest.Server
	conns    atomic.Int64
	mu       sync.Mutex
	requests []string
}

func newCountingVendor(t *testing.T, keyDelay time.Duration) *countingVendor {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	v := &countingVendor{}
	v.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.requests = append(v.requests, r.Method+" "+r.URL.Path)
		v.mu.Unlock()
		if r.URL.Path == "/api/v1/key" && keyDelay > 0 {
			time.Sleep(keyDelay)
		}
		mu.ServeHTTP(w, r)
	}))
	v.srv.EnableHTTP2 = true
	v.srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			v.conns.Add(1)
		}
	}
	v.srv.StartTLS()
	t.Cleanup(v.srv.Close)
	return v
}

func (v *countingVendor) seen() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.requests...)
}

// trustVendor makes gate-eval's decision client trust the test server's certificate (the
// dedicated transport is replaced, as jev's own tests do), for the duration of the test.
func trustVendor(t *testing.T, v *countingVendor) {
	t.Helper()
	orig := newJevClient
	t.Cleanup(func() { newJevClient = orig })
	newJevClient = func(cfg jev.Config) (jev.Client, error) {
		h := jev.DefaultHTTPConfig()
		tr := v.srv.Client().Transport.(*http.Transport).Clone()
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.MinVersion = tls.VersionTLS12
		h.Transport = tr
		cfg.HTTP = &h
		return orig(cfg)
	}
}

// longExample has evidence long enough for both evaluators to be assessed.
const longExample = `{"id":"p6-1","query":"What is the capital of France?","response":"Lyon is the capital of France and its seat of government.","evidence":[{"id":"d1","source":"request_context","text":"Paris is the capital and most populous city of France. It has been the seat of the French government for centuries and hosts the national parliament, the presidency and most ministries. Lyon is the third largest city."}]}`

func TestGateEvalWarmUsesOneConnection(t *testing.T) {
	const keyDelay = 250 * time.Millisecond
	v := newCountingVendor(t, keyDelay)
	trustVendor(t, v)
	getenv := env("OPENROUTER_API_KEY", "sk-or-v1-test", "TRUTHTABLE_OPENROUTER_BASE_URL", v.srv.URL)

	code, lines, stderr := runCLI(t, longExample+"\n", getenv,
		"--warm", "--policy", "builtin", "--concurrency", "1", "--max-cost-usd", "0.01", "--evaluators", "groundedness@1,contradiction@1")
	if code != exitOK || len(lines) != 1 {
		t.Fatalf("exit %d, %d lines: %s", code, len(lines), stderr)
	}
	line := lines[0]
	if line["error"] != nil || dig(line, "decision", "status") != "ok" {
		t.Fatalf("not evaluated: error %v, decision %v", line["error"], line["decision"])
	}
	if line["connection"] != connectionWarm || line["endpoint_host"] != "127.0.0.1" {
		t.Errorf("connection %v, endpoint_host %v; want warm on 127.0.0.1", line["connection"], line["endpoint_host"])
	}
	// The key check and the decision call share one TLS connection.
	if got := v.seen(); len(got) != 2 || got[0] != "GET /api/v1/key" || got[1] != "POST /api/alpha/decisions" {
		t.Errorf("requests %v, want the key check, then the decision call", got)
	}
	if n := v.conns.Load(); n != 1 {
		t.Errorf("%d connections, want 1: the decision call did not reuse the warm connection", n)
	}
	// The example's latency leaves out the warm-up (the key check alone took keyDelay).
	if ms, _ := line["latency_ms"].(float64); ms >= float64(keyDelay.Milliseconds()) {
		t.Errorf("latency_ms %v includes the warm-up (%v)", ms, keyDelay)
	}
	if !strings.Contains(stderr, "at 127.0.0.1, warm connection") || !strings.Contains(stderr, "built-in policy") {
		t.Errorf("summary does not name the host, the connection and the policy:\n%s", stderr)
	}

	// Without --warm the line says cold, and nothing but the decision call is made.
	v2 := newCountingVendor(t, 0)
	trustVendor(t, v2)
	code, lines, stderr = runCLI(t, longExample+"\n", env("OPENROUTER_API_KEY", "sk-or-v1-test", "TRUTHTABLE_OPENROUTER_BASE_URL", v2.srv.URL),
		"--policy", "builtin", "--max-cost-usd", "0.01")
	if code != exitOK || len(lines) != 1 || lines[0]["connection"] != connectionCold {
		t.Fatalf("exit %d, lines %v: %s", code, lines, stderr)
	}
	if got := v2.seen(); len(got) != 1 || got[0] != "POST /api/alpha/decisions" {
		t.Errorf("requests %v without --warm", got)
	}
}

func TestGateEvalWarmWithARefusedKeyStopsBeforeAnyExample(t *testing.T) {
	v := newCountingVendor(t, 0)
	trustVendor(t, v)
	code, lines, stderr := runCLI(t, longExample+"\n",
		env("OPENROUTER_API_KEY", "sk-or-v1-invalid", "TRUTHTABLE_OPENROUTER_BASE_URL", v.srv.URL),
		"--warm", "--policy", "builtin", "--max-cost-usd", "0.01")
	if code != exitFailed || len(lines) != 0 {
		t.Fatalf("exit %d with %d lines, want %d and none", code, len(lines), exitFailed)
	}
	if !strings.Contains(stderr, "the key check was refused (auth)") || strings.Contains(stderr, "sk-or-v1-invalid") {
		t.Errorf("stderr should name the refusal and never the key:\n%s", stderr)
	}
	if got := v.seen(); len(got) != 1 || got[0] != "GET /api/v1/key" {
		t.Errorf("requests %v: no decision call may follow a refused key", got)
	}
}

func TestGateEvalWarmIsColdOnABackendWithoutAKeyEndpoint(t *testing.T) {
	code, lines, stderr := runCLI(t, longExample+"\n", env(), "--backend", "mock", "--warm", "--policy", "builtin")
	if code != exitOK || len(lines) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if lines[0]["connection"] != connectionCold || lines[0]["endpoint_host"] != nil || lines[0]["simulated"] != true {
		t.Errorf("mock line: connection %v, endpoint_host %v, simulated %v", lines[0]["connection"], lines[0]["endpoint_host"], lines[0]["simulated"])
	}
}

func TestGateEvalEndpointHostFromEitherBaseURL(t *testing.T) {
	// TRUTHTABLE_OPENROUTER_BASE_URL moves the OpenRouter backends (auto resolution, as the proxy).
	v := newCountingVendor(t, 0)
	trustVendor(t, v)
	code, lines, stderr := runCLI(t, longExample+"\n",
		env("OPENROUTER_API_KEY", "sk-or-v1-test", "TRUTHTABLE_OPENROUTER_BASE_URL", v.srv.URL),
		"--policy", "builtin", "--max-cost-usd", "0.01")
	if code != exitOK || len(lines) != 1 || lines[0]["endpoint_host"] != "127.0.0.1" {
		t.Fatalf("TRUTHTABLE_OPENROUTER_BASE_URL: exit %d, lines %v: %s", code, lines, stderr)
	}

	// TRUTHTABLE_JEV_BASE_URL moves an explicitly chosen backend: here to localhost.
	plain := httptest.NewServer(mustMockUpstream(t))
	t.Cleanup(plain.Close)
	jevBase := strings.Replace(plain.URL, "127.0.0.1", "localhost", 1) + "/api"
	code, lines, stderr = runCLI(t, longExample+"\n",
		env("OPENROUTER_API_KEY", "sk-or-v1-test", "TRUTHTABLE_JEV_BACKEND", jev.BackendOpenRouterDecisions, "TRUTHTABLE_JEV_BASE_URL", jevBase),
		"--policy", "builtin", "--max-cost-usd", "0.01")
	if code != exitOK || len(lines) != 1 || lines[0]["endpoint_host"] != "localhost" {
		t.Fatalf("TRUTHTABLE_JEV_BASE_URL: exit %d, lines %v: %s", code, lines, stderr)
	}
	if dig(lines[0], "decision", "status") != "ok" {
		t.Errorf("the localhost backend was not called: %v", lines[0]["decision"])
	}

	// With neither, the host is the vendor's (resolved without a call).
	o := options{policyFile: policyBuiltin, route: policy.DefaultRouteName, backend: jev.BackendAuto, maxCostUSD: 1, pricePerMTok: 0.042, timeout: time.Second}
	ev, err := setup(&o, env("OPENROUTER_API_KEY", "sk-or-v1-test"))
	if err != nil {
		t.Fatal(err)
	}
	if ev.endpointHost == nil || *ev.endpointHost != "openrouter.ai" {
		t.Errorf("default endpoint host %v, want openrouter.ai", ev.endpointHost)
	}
	if h := endpointHost(jev.Info{Endpoint: "https://API.TypeSafe.ai/v1/systemone"}); h == nil || *h != "api.typesafe.ai" {
		t.Errorf("endpointHost lowercases the bare hostname: %v", h)
	}
	if h := endpointHost(jev.Info{}); h != nil {
		t.Errorf("no endpoint, host %v", *h)
	}
}

func mustMockUpstream(t *testing.T) http.Handler {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return mu
}

func TestGateEvalConcurrencyOneEvaluatesOneAtATime(t *testing.T) {
	var inFlight, peak atomic.Int64
	cost := 0.00002
	client := &fakeClient{costUSD: &cost, noul: 0.2, delay: func() time.Duration {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return 0
	}}
	orig := newJevClient
	t.Cleanup(func() { newJevClient = orig })
	newJevClient = func(jev.Config) (jev.Client, error) { return client, nil }

	code, lines, stderr := runCLI(t, examples(6), env(), "--policy", "builtin", "--concurrency", "1", "--max-cost-usd", "1", "--price-per-mtok", "10")
	if code != exitOK || len(lines) != 6 {
		t.Fatalf("exit %d, %d lines: %s", code, len(lines), stderr)
	}
	if p := peak.Load(); p != 1 {
		t.Errorf("%d calls ran at once with --concurrency 1", p)
	}
	for i, l := range lines {
		if l["id"] != "ex-"+string(rune('0'+i)) {
			t.Errorf("line %d is %v: the input order is kept", i, l["id"])
		}
	}

	peak.Store(0)
	code, _, stderr = runCLI(t, examples(6), env(), "--policy", "builtin", "--concurrency", "3", "--max-cost-usd", "1", "--price-per-mtok", "10")
	if code != exitOK || peak.Load() < 2 {
		t.Errorf("--concurrency 3: exit %d, peak %d (want calls at once): %s", code, peak.Load(), stderr)
	}
}

func TestGateEvalPolicyBuiltinIgnoresThePolicyFile(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "policies.yaml")
	if err := os.WriteFile(broken, []byte("version: 1\nroutes: [this is not a route]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := env("TRUTHTABLE_POLICY_FILE", broken)

	// The environment's policy file is read without --policy ...
	if code, _, stderr := runCLI(t, longExample+"\n", getenv, "--backend", "mock"); code == exitOK {
		t.Fatalf("a broken TRUTHTABLE_POLICY_FILE was not read: %s", stderr)
	}
	// ... and ignored with --policy builtin, which gates on the defaults route.
	code, lines, stderr := runCLI(t, longExample+"\n", getenv, "--backend", "mock", "--policy", "builtin")
	if code != exitOK || len(lines) != 1 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if lines[0]["route"] != policy.DefaultRouteName {
		t.Errorf("route %v, want the defaults route", lines[0]["route"])
	}
	if !strings.Contains(stderr, "built-in policy") {
		t.Errorf("the summary does not say the policy is built in:\n%s", stderr)
	}
	var b strings.Builder
	_ = json.NewEncoder(&b).Encode(lines[0]["decision"])
	if !strings.Contains(b.String(), `"groundedness@1"`) {
		t.Errorf("the defaults route does not assess groundedness: %s", b.String())
	}
}
