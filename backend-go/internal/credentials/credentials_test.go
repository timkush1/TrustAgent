package credentials

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/authz"
)

// testMaster returns a durable (env) master key whose 32 bytes are all b.
func testMaster(t *testing.T, b byte) *authz.MasterKey {
	t.Helper()
	m, err := authz.LoadMasterKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, authz.MasterKeySize)), "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testCipher(t *testing.T, b byte) *Cipher {
	t.Helper()
	c, err := New(testMaster(t, b))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSealOpenRoundTrip(t *testing.T) {
	c := testCipher(t, 1)
	const secret = "sk-or-v1-test-0123456789abcdef"
	ct, nonce, keyID, err := c.Seal(NameOpenRouter, ProviderOpenRouter, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != NonceSize || keyID != c.KeyID() || len(keyID) != 16 {
		t.Fatalf("nonce %d bytes, key id %q", len(nonce), keyID)
	}
	if bytes.Contains(ct, []byte(secret)) {
		t.Fatal("the ciphertext holds the secret in clear")
	}
	got, err := c.Open(Sealed{Name: NameOpenRouter, Provider: ProviderOpenRouter, Ciphertext: ct, Nonce: nonce, KeyID: keyID}, nil)
	if err != nil || got != secret {
		t.Fatalf("open: %v (secret equal: %v)", err, got == secret)
	}
	// A second cipher over the same master key opens it too (a restart).
	if got, err := testCipher(t, 1).Open(Sealed{Name: NameOpenRouter, Provider: ProviderOpenRouter, Ciphertext: ct, Nonce: nonce, KeyID: keyID}, nil); err != nil || got != secret {
		t.Fatalf("open after restart: %v", err)
	}
	if _, _, _, err := c.Seal("nope", ProviderGeneric, nil, secret); err == nil {
		t.Error("an unknown name is refused")
	}
	if _, _, _, err := c.Seal(NameOpenRouter, ProviderOpenRouter, nil, ""); err == nil {
		t.Error("an empty secret is refused")
	}
}

func TestNoncesAreUnique(t *testing.T) {
	c := testCipher(t, 2)
	seen := make(map[string]bool, 10000)
	for i := 0; i < 10000; i++ {
		_, nonce, _, err := c.Seal(NameTypeSafe, ProviderTypeSafe, nil, "sk-test-nonce-uniqueness")
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(nonce)] {
			t.Fatalf("nonce repeated after %d seals", i)
		}
		seen[string(nonce)] = true
	}
}

func TestSwappedAADIsUndecryptable(t *testing.T) {
	c := testCipher(t, 3)
	const secret = "sk-test-aad-binding-secret"
	origins := []string{"https://gw.example.com"}
	ct, nonce, keyID, err := c.Seal(NameCompatible, ProviderGeneric, origins, secret)
	if err != nil {
		t.Fatal(err)
	}
	row := Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: ct, Nonce: nonce, KeyID: keyID}
	if got, err := c.Open(row, origins); err != nil || got != secret {
		t.Fatalf("the sealed row opens: %v", err)
	}
	// The origin list is a set: order and duplicates do not matter.
	if _, err := c.Open(row, []string{"https://gw.example.com", "https://gw.example.com"}); err != nil {
		t.Errorf("duplicate origins: %v", err)
	}
	cases := map[string]struct {
		row     Sealed
		origins []string
	}{
		"another name":        {Sealed{Name: NameUpstreamDefault, Provider: ProviderGeneric, Ciphertext: ct, Nonce: nonce, KeyID: keyID}, origins},
		"another provider":    {Sealed{Name: NameCompatible, Provider: ProviderOpenRouter, Ciphertext: ct, Nonce: nonce, KeyID: keyID}, origins},
		"a changed origin":    {row, []string{"https://evil.example.com"}},
		"an added origin":     {row, []string{"https://gw.example.com", "https://evil.example.com"}},
		"no origins":          {row, nil},
		"a changed port":      {row, []string{"https://gw.example.com:8443"}},
		"tampered ciphertext": {Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: flip(ct), Nonce: nonce, KeyID: keyID}, origins},
		"another nonce":       {Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: ct, Nonce: flip(nonce), KeyID: keyID}, origins},
		"a short nonce":       {Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: ct, Nonce: nonce[:8], KeyID: keyID}, origins},
	}
	for name, tc := range cases {
		if _, err := c.Open(tc.row, tc.origins); !errors.Is(err, ErrUndecryptable) {
			t.Errorf("%s: %v, want ErrUndecryptable", name, err)
		}
	}
}

func TestVendorRowsIgnoreTheStoredOrigins(t *testing.T) {
	c := testCipher(t, 4)
	ct, nonce, keyID, err := c.Seal(NameOpenRouter, ProviderOpenRouter, []string{"https://openrouter.ai"}, "sk-or-v1-test-vendor")
	if err != nil {
		t.Fatal(err)
	}
	row := Sealed{Name: NameOpenRouter, Provider: ProviderOpenRouter, Ciphertext: ct, Nonce: nonce, KeyID: keyID}
	for _, origins := range [][]string{nil, {"https://openrouter.ai"}, {"https://evil.example"}} {
		if _, err := c.Open(row, origins); err != nil {
			t.Errorf("a vendor row binds an empty origin list (D78), opened with %v: %v", origins, err)
		}
	}
}

func TestKeyIDMismatchIsUndecryptable(t *testing.T) {
	a, b := testCipher(t, 5), testCipher(t, 6)
	if a.KeyID() == b.KeyID() {
		t.Fatal("two master keys share a key id")
	}
	ct, nonce, keyID, err := a.Seal(NameTypeSafe, ProviderTypeSafe, nil, "sk-test-other-master")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(Sealed{Name: NameTypeSafe, Provider: ProviderTypeSafe, Ciphertext: ct, Nonce: nonce, KeyID: keyID}, nil); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("another master key: %v", err)
	}
	// A forged key_id does not help: the GCM tag still fails under the other key.
	if _, err := b.Open(Sealed{Name: NameTypeSafe, Provider: ProviderTypeSafe, Ciphertext: ct, Nonce: nonce, KeyID: b.KeyID()}, nil); !errors.Is(err, ErrUndecryptable) {
		t.Errorf("forged key id: %v", err)
	}
}

func TestEphemeralMasterIsUnavailable(t *testing.T) {
	if _, err := New(authz.EphemeralMasterKey()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("ephemeral master: %v", err)
	}
	if _, err := New(nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil master: %v", err)
	}
	durable := testMaster(t, 7)
	for _, tc := range []struct {
		db     bool
		master *authz.MasterKey
		want   string
	}{
		{true, durable, StateReady},
		{true, authz.EphemeralMasterKey(), StateUnavailable},
		{true, nil, StateUnavailable},
		{false, durable, StateNoDatabase},
		{false, authz.EphemeralMasterKey(), StateNoDatabase},
	} {
		if got := StoreState(tc.db, tc.master); got != tc.want {
			t.Errorf("StoreState(db=%v, %v) = %s, want %s", tc.db, tc.master != nil, got, tc.want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a, b := NewFingerprinter(testMaster(t, 8)), NewFingerprinter(testMaster(t, 9))
	const secret = "sk-test-fingerprint-0001"
	fp := a.Of(secret)
	if !strings.HasPrefix(fp, FingerprintPrefix) || len(fp) != len(FingerprintPrefix)+12 || strings.Trim(fp[3:], "0123456789abcdef") != "" {
		t.Fatalf("fingerprint %q is not fp_ + 12 hex", fp)
	}
	if a.Of(secret) != fp || NewFingerprinter(testMaster(t, 8)).Of(secret) != fp {
		t.Error("a fingerprint is stable under one master key")
	}
	if b.Of(secret) == fp {
		t.Error("fingerprints differ across master keys")
	}
	if a.Of("sk-test-fingerprint-0002") == fp {
		t.Error("fingerprints differ across secrets")
	}
	if a.Of("") != "" || NewFingerprinter(nil).Of(secret) != "" {
		t.Error("no secret or no master key gives no fingerprint")
	}
	if testCipher(t, 8).Fingerprint(secret) != fp {
		t.Error("the cipher fingerprints with the same function")
	}
}

func TestAllowedOrigins(t *testing.T) {
	for _, tc := range []struct {
		name, provider, base, orBase string
		want                         []string
	}{
		{NameOpenRouter, ProviderOpenRouter, "", "", []string{"https://openrouter.ai"}},
		{NameOpenRouter, ProviderOpenRouter, "", "https://openrouter.ai/", []string{"https://openrouter.ai"}},
		{NameOpenRouter, ProviderOpenRouter, "", "http://mock-upstream:8090", []string{"http://mock-upstream:8090", "https://openrouter.ai"}},
		{NameOpenRouter, ProviderOpenRouter, "", "https://OR.Example.com:443/api", []string{"https://openrouter.ai", "https://or.example.com"}},
		// An env base that CheckURL refuses never widens the list.
		{NameOpenRouter, ProviderOpenRouter, "", "http://or.example.com", []string{"https://openrouter.ai"}},
		{NameTypeSafe, ProviderTypeSafe, "", "http://mock-upstream:8090", []string{"https://api.typesafe.ai"}},
		{NameAnthropic, ProviderAnthropic, "", "", []string{"https://api.anthropic.com"}},
		{NameOpenAI, ProviderOpenAI, "", "", []string{"https://api.openai.com"}},
		{NameCompatible, ProviderGeneric, "https://ai-gateway.vercel.sh/typesafe", "", []string{"https://ai-gateway.vercel.sh"}},
		{NameCompatible, ProviderGeneric, "https://gw.example.com:8443/v1", "", []string{"https://gw.example.com:8443"}},
		{NameUpstreamDefault, ProviderGeneric, "http://localhost:11434/v1", "", []string{"http://localhost:11434"}},
		{NameUpstreamFallback, ProviderGeneric, "HTTPS://API.Example.COM:443", "", []string{"https://api.example.com"}},
	} {
		got, err := AllowedOrigins(tc.name, tc.provider, tc.base, tc.orBase)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("AllowedOrigins(%s, %q, %q) = %v, %v; want %v", tc.name, tc.base, tc.orBase, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		name, provider, base string
		want                 error
	}{
		{NameOpenRouter, ProviderGeneric, "", ErrProviderMismatch},
		{NameCompatible, ProviderOpenRouter, "https://gw.example.com", ErrProviderMismatch},
		{NameOpenRouter, ProviderOpenRouter, "https://openrouter.ai", ErrBaseURL},
		{NameAnthropic, ProviderAnthropic, "https://proxy.example.com", ErrBaseURL},
		{NameCompatible, ProviderGeneric, "", ErrBaseURL},
		{NameUpstreamAnthropic, ProviderGeneric, "  ", ErrBaseURL},
		{"github", ProviderGeneric, "", ErrUnknownName},
	} {
		if _, err := AllowedOrigins(tc.name, tc.provider, tc.base, ""); !errors.Is(err, tc.want) {
			t.Errorf("AllowedOrigins(%s, %s, %q): %v, want %v", tc.name, tc.provider, tc.base, err, tc.want)
		}
	}
	var ue *URLError
	if _, err := AllowedOrigins(NameCompatible, ProviderGeneric, "http://gw.example.com", ""); !errors.As(err, &ue) || ue.Code != CodeInsecureURL {
		t.Errorf("an insecure base URL: %v", err)
	}
}

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"https://openrouter.ai/api/v1":  "https://openrouter.ai",
		"HTTPS://OpenRouter.AI:443":     "https://openrouter.ai",
		"http://localhost:80/x":         "http://localhost",
		"http://localhost:8080":         "http://localhost:8080",
		"https://gw.example.com:8443/a": "https://gw.example.com:8443",
		"https://[::1]:9000/":           "https://[::1]:9000",
	} {
		if got, err := Canonical(in); err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Canonical("/relative/path"); err == nil {
		t.Error("a relative URL has no origin")
	}
}

func TestCheckURL(t *testing.T) {
	for _, ok := range []string{
		"https://openrouter.ai/api",
		"https://gw.example.com:8443/v1",
		"http://localhost:8090",
		"http://127.0.0.1:8090",
		"http://[::1]:8090",
		"http://mock-upstream:8090",
		"http://dev.localhost:5173",
		"https://10.0.0.5/v1",
	} {
		if _, err := CheckURL(ok); err != nil {
			t.Errorf("CheckURL(%q): %v", ok, err)
		}
	}
	for raw, code := range map[string]string{
		"https://user:pa55word@gw.example.com":     CodeURLCredentials,
		"https://token@gw.example.com":             CodeURLCredentials,
		"http://gw.example.com/v1":                 CodeInsecureURL,
		"http://10.0.0.5/v1":                       CodeInsecureURL,
		"ftp://gw.example.com":                     CodeInsecureURL,
		"https://169.254.169.254/latest/meta-data": CodeForbiddenTarget,
		"http://169.254.169.254":                   CodeForbiddenTarget,
		"https://169.254.10.1":                     CodeForbiddenTarget,
		"https://[fe80::1]/":                       CodeForbiddenTarget,
		"https://[fe80::1%25eth0]/":                CodeForbiddenTarget,
		"https://[fd00:ec2::254]/":                 CodeForbiddenTarget,
		"https://[::ffff:169.254.169.254]/":        CodeForbiddenTarget,
		"https://metadata.google.internal/":        CodeForbiddenTarget,
		"http://METADATA.google.internal./":        CodeForbiddenTarget,
		"https://2852039166/":                      CodeForbiddenTarget,
		"https://0xa9fea9fe/":                      CodeForbiddenTarget,
		"https://0251.0376.0251.0376/":             CodeForbiddenTarget,
		"/just/a/path":                             CodeInvalidURL,
		"https://":                                 CodeInvalidURL,
	} {
		_, err := CheckURL(raw)
		var ue *URLError
		if !errors.As(err, &ue) || ue.Code != code {
			t.Errorf("CheckURL(%q) = %v, want code %s", raw, err, code)
			continue
		}
		if strings.Contains(err.Error(), "pa55word") || strings.Contains(err.Error(), "token@") {
			t.Errorf("CheckURL(%q) error carries the userinfo: %v", raw, err)
		}
	}
}

func TestSpecs(t *testing.T) {
	names := Names()
	if len(names) != 8 || names[0] != NameOpenRouter || names[7] != NameUpstreamFallback {
		t.Fatalf("names %v", names)
	}
	for _, n := range names {
		s, ok := Lookup(n)
		if !ok || s.EnvVar == "" || s.Provider == "" {
			t.Errorf("spec of %s: %+v", n, s)
		}
		custom := n == NameCompatible || strings.HasPrefix(n, "upstream-")
		if s.CustomOrigin != custom {
			t.Errorf("%s CustomOrigin = %v", n, s.CustomOrigin)
		}
		engine := n == NameAnthropic || n == NameOpenAI
		if (s.Side == SideEngine) != engine {
			t.Errorf("%s side %s", n, s.Side)
		}
	}
	if Masked("sk-or-v1-abcdefghijklmnop3f2a") != "sk-…3f2a" {
		t.Errorf("Masked = %q", Masked("sk-or-v1-abcdefghijklmnop3f2a"))
	}
}

func flip(b []byte) []byte {
	out := slices.Clone(b)
	out[len(out)-1] ^= 0x01
	return out
}
