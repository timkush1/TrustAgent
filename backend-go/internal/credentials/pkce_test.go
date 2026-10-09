package credentials

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/contracts"
)

// TestCodeChallengeRFC7636 is RFC 7636 Appendix B.
func TestCodeChallengeRFC7636(t *testing.T) {
	const verifier, challenge = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := CodeChallenge(verifier); got != challenge {
		t.Fatalf("CodeChallenge = %q, want %q", got, challenge)
	}
}

func TestNewCodeVerifier(t *testing.T) {
	shape := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	seen := map[string]bool{}
	for range 50 {
		v, err := NewCodeVerifier()
		if err != nil {
			t.Fatal(err)
		}
		if !shape.MatchString(v) {
			t.Fatalf("verifier %q is not 43 base64url characters (RFC 7636 §4.1)", v)
		}
		if seen[v] {
			t.Fatal("a verifier repeated")
		}
		seen[v] = true
	}
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const (
	dashOrigin = "http://localhost:3000"
	adminA     = "admin:setup-admin"
	adminB     = "admin:ops"
)

func newTestConnectStore() (*ConnectStore, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	return NewConnectStore(0, 0, clk.now), clk
}

func TestConnectStoreCompleteOnce(t *testing.T) {
	s, _ := newTestConnectStore()
	nonce, p, err := s.Start(adminA, dashOrigin, "/app/setup?step=connect")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`).MatchString(nonce) || p.Verifier == "" {
		t.Fatalf("nonce %q / verifier %q", nonce, p.Verifier)
	}
	// The Origin header compares as a canonical origin.
	got, err := s.Complete(nonce, adminA, "HTTP://LocalHost:3000")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.Verifier != p.Verifier || got.ReturnTo != "/app/setup?step=connect" || got.Actor != adminA {
		t.Errorf("pending = %+v", got)
	}
	if _, err := s.Complete(nonce, adminA, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
		t.Errorf("second complete: %v, want ErrInvalidNonce", err)
	}
	if _, err := s.Complete("never-issued", adminA, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
		t.Errorf("unknown nonce: %v", err)
	}
	if _, _, err := s.Start(adminA, "not an origin", DefaultConnectReturnTo); !errors.Is(err, ErrCallbackOrigin) {
		t.Errorf("start without a callback origin: %v, want ErrCallbackOrigin", err)
	}
}

// TestConnectStoreFailedCompleteBurnsTheNonce: the first call consumes the nonce whatever the
// outcome, so a guess from another admin or origin also ends the sign-in.
func TestConnectStoreFailedCompleteBurnsTheNonce(t *testing.T) {
	for _, tc := range []struct{ name, actor, origin string }{
		{"another admin", adminB, dashOrigin},
		{"another origin", adminA, "http://127.0.0.1:3000"},
		{"no origin", adminA, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestConnectStore()
			nonce, _, _ := s.Start(adminA, dashOrigin, DefaultConnectReturnTo)
			if _, err := s.Complete(nonce, tc.actor, tc.origin); !errors.Is(err, ErrInvalidNonce) {
				t.Fatalf("complete as %s from %q: %v, want ErrInvalidNonce", tc.actor, tc.origin, err)
			}
			if _, err := s.Complete(nonce, adminA, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
				t.Errorf("the rightful complete after a failed one: %v, want ErrInvalidNonce", err)
			}
			if s.Pending() != 0 {
				t.Errorf("%d pending, want 0", s.Pending())
			}
		})
	}
}

func TestConnectStoreExpiry(t *testing.T) {
	s, clk := newTestConnectStore()
	n1, _, _ := s.Start(adminA, dashOrigin, DefaultConnectReturnTo)
	n2, _, _ := s.Start(adminA, dashOrigin, DefaultConnectReturnTo)
	clk.add(ConnectTTL - time.Second)
	if _, err := s.Complete(n1, adminA, dashOrigin); err != nil {
		t.Errorf("complete just inside the window: %v", err)
	}
	clk.add(time.Second)
	if _, err := s.Complete(n2, adminA, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
		t.Errorf("complete after %v: %v, want ErrInvalidNonce", ConnectTTL, err)
	}
	// A later Start drops expired sign-ins.
	n3, _, _ := s.Start(adminB, dashOrigin, DefaultConnectReturnTo)
	_, _, _ = s.Start(adminA, dashOrigin, DefaultConnectReturnTo)
	clk.add(ConnectTTL)
	_, _, _ = s.Start(adminB, dashOrigin, DefaultConnectReturnTo)
	if s.Pending() != 1 {
		t.Errorf("%d pending after expiry, want only the newest", s.Pending())
	}
	if _, err := s.Complete(n3, adminB, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
		t.Errorf("expired nonce: %v", err)
	}
}

func TestConnectStoreCapEvictsTheOldest(t *testing.T) {
	s, clk := newTestConnectStore()
	var nonces []string
	for range ConnectPerActor + 1 {
		n, _, err := s.Start(adminA, dashOrigin, DefaultConnectReturnTo)
		if err != nil {
			t.Fatal(err)
		}
		nonces = append(nonces, n)
		clk.add(time.Second)
	}
	other, _, _ := s.Start(adminB, dashOrigin, DefaultConnectReturnTo)
	if got := s.Pending(); got != ConnectPerActor+1 {
		t.Fatalf("%d pending, want %d for A plus 1 for B", got, ConnectPerActor+1)
	}
	if _, err := s.Complete(nonces[0], adminA, dashOrigin); !errors.Is(err, ErrInvalidNonce) {
		t.Errorf("the oldest sign-in survived the cap: %v", err)
	}
	for _, n := range nonces[1:] {
		if _, err := s.Complete(n, adminA, dashOrigin); err != nil {
			t.Errorf("a newer sign-in was evicted: %v", err)
		}
	}
	if _, err := s.Complete(other, adminB, dashOrigin); err != nil {
		t.Errorf("another admin's sign-in was evicted: %v", err)
	}
}

func TestValidateReturnTo(t *testing.T) {
	for raw, want := range map[string]string{
		"":                        DefaultConnectReturnTo,
		"/app/setup?step=connect": "/app/setup?step=connect",
		"/app/traffic/tr_123":     "/app/traffic/tr_123",
		"/app/./settings/keys":    "/app/settings/keys",
		"/app/settings/keys/":     "/app/settings/keys",
	} {
		got, err := ValidateReturnTo(raw)
		if err != nil || got != want {
			t.Errorf("ValidateReturnTo(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	attacks := []string{
		"//evil.example/app/",
		"https://evil.example/app/x",
		"javascript:alert(1)",
		"/app//evil.example",
		"/app/../admin",
		"/app/x/..",
		"/app/%2e%2e/x",
		`/app\evil`,
		`/app/%5cevil.example`,
		"/app/%2fevil.example",
		"/app/%2Fevil.example",
		"/%2F%2Fevil.example",
		"/app/%252fevil",
		"/app/x%0d%0aSet-Cookie:a=b",
		"/app/x\r\nSet-Cookie:a=b",
		"/app/\tx",
		"/app/x\x00",
		"/app/x ",
		"/app/%zz",
		"/APP/x",
		"/app",
		"/app/",
		"/application/x",
		"app/x",
		" /app/x",
		"/app/" + strings.Repeat("a", maxReturnTo),
	}
	for _, raw := range attacks {
		if got, err := ValidateReturnTo(raw); !errors.Is(err, ErrReturnTo) {
			t.Errorf("ValidateReturnTo(%q) = %q, %v; want ErrReturnTo", raw, got, err)
		}
	}
}

func TestConnectCallbackOrigin(t *testing.T) {
	allows := func(o string) bool {
		return connectOrigin(o) == "http://127.0.0.1:3000" || connectOrigin(o) == "https://ta.example.com"
	}
	got, err := ConnectCallbackOrigin("http://127.0.0.1:3000", allows, "https://ta.example.com")
	if err != nil || got != "http://127.0.0.1:3000" {
		t.Fatalf("allowed Origin: %q, %v", got, err)
	}
	authURL, err := ConnectAuthURL("", got, "challenge")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if cb := u.Query().Get("callback_url"); cb != "http://127.0.0.1:3000/connect/callback" {
		t.Errorf("callback_url = %q, want the request's origin", cb)
	}
	// No Origin (a non-browser client): the public URL's origin.
	if got, err := ConnectCallbackOrigin("", allows, "https://TA.example.com:443/app/"); err != nil || got != "https://ta.example.com" {
		t.Errorf("no Origin: %q, %v", got, err)
	}
	for _, o := range []string{"http://evil.example", "null", "file://x"} {
		if _, err := ConnectCallbackOrigin(o, allows, "https://ta.example.com"); !errors.Is(err, ErrCallbackOrigin) {
			t.Errorf("Origin %q: %v, want ErrCallbackOrigin", o, err)
		}
	}
	if _, err := ConnectCallbackOrigin("", allows, ""); !errors.Is(err, ErrCallbackOrigin) {
		t.Errorf("no Origin and no public URL: %v", err)
	}
}

func TestConnectAuthURL(t *testing.T) {
	// The ConnectStartResponse.default.json fixture's shape.
	got, err := ConnectAuthURL("", "http://localhost:3000", "Vq3kM1yYl0d9Zr2oJcQx8w6a4tH5sFbN7uEpLgCiKjA")
	want := "https://openrouter.ai/auth?callback_url=http%3A%2F%2Flocalhost%3A3000%2Fconnect%2Fcallback&code_challenge=Vq3kM1yYl0d9Zr2oJcQx8w6a4tH5sFbN7uEpLgCiKjA&code_challenge_method=S256"
	if err != nil || got != want {
		t.Errorf("ConnectAuthURL = %q, %v\nwant %q", got, err, want)
	}
	// TRUTHTABLE_OPENROUTER_BASE_URL contributes its origin only.
	got, _ = ConnectAuthURL("http://127.0.0.1:28090/api", "http://localhost:3000", "c")
	if !strings.HasPrefix(got, "http://127.0.0.1:28090/auth?") {
		t.Errorf("custom base: %q", got)
	}
}

func TestExchangeConnectCode(t *testing.T) {
	m, hs := startMockUpstream(t)
	origin := originOf(t, hs.URL)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	authorize := func(verifier, mockKey string) string {
		t.Helper()
		authURL, err := ConnectAuthURL(hs.URL, dashOrigin, CodeChallenge(verifier))
		if err != nil {
			t.Fatal(err)
		}
		if mockKey != "" {
			authURL += "&mock_key=" + mockKey
		}
		resp, err := noRedirect.Get(authURL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if !strings.HasPrefix(loc.String(), dashOrigin+"/connect/callback?") {
			t.Fatalf("redirect to %s", loc)
		}
		return loc.Query().Get("code")
	}
	verifier, _ := NewCodeVerifier()
	code := authorize(verifier, "nocredits")
	key, err := ExchangeConnectCode(context.Background(), nil, hs.URL, []string{origin}, code, verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !strings.HasPrefix(key, "sk-or-v1-mock-nocredits-") {
		t.Errorf("key %q…, want the mock's nocredits key", key[:14])
	}
	// The code is single use.
	_, err = ExchangeConnectCode(context.Background(), nil, hs.URL, []string{origin}, code, verifier)
	var xe *ExchangeError
	if !errors.As(err, &xe) || xe.Status != http.StatusBadRequest {
		t.Errorf("reused code: %v, want HTTP 400", err)
	}
	// A wrong verifier is refused by OpenRouter.
	other, _ := NewCodeVerifier()
	_, err = ExchangeConnectCode(context.Background(), nil, hs.URL, []string{origin}, authorize(verifier, ""), other)
	if !errors.As(err, &xe) || xe.Status != http.StatusForbidden || strings.Contains(xe.Error(), other) {
		t.Errorf("wrong verifier: %v, want HTTP 403 without the verifier", err)
	}
	// The exchange only goes to the openrouter credential's origins.
	before := len(m.Requests())
	_, err = ExchangeConnectCode(context.Background(), nil, hs.URL, []string{"https://openrouter.ai"}, "code", verifier)
	if err == nil || !errors.Is(err, errVerifyOriginRefused) || len(m.Requests()) != before {
		t.Errorf("exchange to an origin outside the allowed list: %v (%d new requests)", err, len(m.Requests())-before)
	}
}

// D96: the code exchange's own client dials through netguard, so even an allowed origin that
// resolves to a link-local or cloud-metadata address is refused before any byte is sent.
func TestExchangeConnectCodeRefusesMetadataAddresses(t *testing.T) {
	verifier, _ := NewCodeVerifier()
	for _, base := range []string{"http://169.254.169.254", "http://[fd00:ec2::254]"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := ExchangeConnectCode(ctx, nil, base, []string{base}, "code", verifier)
		cancel()
		var xe *ExchangeError
		if !errors.As(err, &xe) || xe.Status != 0 || !strings.Contains(xe.Message, "link-local or cloud-metadata") {
			t.Errorf("%s: %v, want the netguard refusal", base, err)
		}
	}
}

func TestPlausibleKey(t *testing.T) {
	for k, want := range map[string]bool{
		"sk-or-v1-abc": true, "": false, "sk or": false, "sk-\n": false, "sk-é": false, strings.Repeat("k", maxExchangedKey+1): false,
	} {
		if got := plausibleKey(k); got != want {
			t.Errorf("plausibleKey(%q) = %v", k, got)
		}
	}
}

// TestPKCEVectors checks this package's PKCE against the shared vectors (contracts/vectors/pkce,
// P5-PLAN D103), which the browser's static /connect/callback/ flow follows too: CodeChallenge for
// the challenge op, the verifier encoding of NewCodeVerifier (randomToken) for verifier_from_bytes,
// and, for auth_url, that the parameters ConnectAuthURL sends for the same challenge are the ones
// the vector's URL carries (the self-hosted flow names its own callback and no key label).
func TestPKCEVectors(t *testing.T) {
	type vector struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Input  struct {
			Op          string `json:"op"`
			Verifier    string `json:"verifier"`
			BytesHex    string `json:"bytes_hex"`
			CallbackURL string `json:"callback_url"`
			Challenge   string `json:"challenge"`
		} `json:"input"`
		Expected map[string]string `json:"expected"`
	}
	files, err := fs.Glob(contracts.FS, "vectors/pkce/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no PKCE vectors (%v)", err)
	}
	ops := map[string]int{}
	for _, f := range files {
		b, err := fs.ReadFile(contracts.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		ops[v.Input.Op]++
		switch v.Input.Op {
		case "challenge":
			if got := CodeChallenge(v.Input.Verifier); got != v.Expected["challenge"] {
				t.Errorf("%s: CodeChallenge = %q, want %q", v.Name, got, v.Expected["challenge"])
			}
		case "verifier_from_bytes":
			raw, err := hex.DecodeString(v.Input.BytesHex)
			if err != nil || len(raw) != connectVerifierBytes {
				t.Fatalf("%s: bytes_hex is not %d bytes", v.Name, connectVerifierBytes)
			}
			if got := base64.RawURLEncoding.EncodeToString(raw); got != v.Expected["verifier"] {
				t.Errorf("%s: verifier = %q, want %q", v.Name, got, v.Expected["verifier"])
			}
		case "auth_url":
			want, err := url.Parse(v.Expected["url"])
			if err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			raw, err := ConnectAuthURL("", "https://dashboard.example", v.Input.Challenge)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := url.Parse(raw)
			if got.Scheme+"://"+got.Host+got.Path != want.Scheme+"://"+want.Host+want.Path {
				t.Errorf("%s: sign-in page %s, the vector's is %s", v.Name, got.Path, want.Path)
			}
			for _, k := range []string{"code_challenge", "code_challenge_method"} {
				if got.Query().Get(k) != want.Query().Get(k) {
					t.Errorf("%s: %s = %q, the vector's is %q", v.Name, k, got.Query().Get(k), want.Query().Get(k))
				}
			}
			if want.Query().Get("callback_url") != v.Input.CallbackURL {
				t.Errorf("%s: the vector's URL carries callback_url %q, not its input", v.Name, want.Query().Get("callback_url"))
			}
		default:
			t.Errorf("%s: unknown op %q", v.Name, v.Input.Op)
		}
	}
	for _, op := range []string{"challenge", "verifier_from_bytes", "auth_url"} {
		if ops[op] == 0 {
			t.Errorf("no PKCE vector for op %s", op)
		}
	}
}
