package credentials

import (
	"errors"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
)

// noPatternSecret matches none of the redaction patterns: only the loaded
// secrets list can mask it, so a leak of it is never hidden by a pattern.
const noPatternSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// TestErrorsNeverCarryTheSecret drives every failure of the cipher and the origin checks with a
// secret in reach and asserts that no error text contains it.
func TestErrorsNeverCarryTheSecret(t *testing.T) {
	if config.NewRedactor().Redact(noPatternSecret) != noPatternSecret {
		t.Fatal("the no-pattern secret must not match a redaction pattern")
	}
	secrets := []string{noPatternSecret, "sk-or-v1-e2e-0123456789abcdef"}
	a, b := testCipher(t, 11), testCipher(t, 12)
	var errs []error
	for _, secret := range secrets {
		ct, nonce, keyID, err := a.Seal(NameCompatible, ProviderGeneric, []string{"https://gw.example.com"}, secret)
		if err != nil {
			t.Fatal(err)
		}
		row := Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: ct, Nonce: nonce, KeyID: keyID}
		_, e1 := b.Open(row, []string{"https://gw.example.com"})   // another master key
		_, e2 := a.Open(row, []string{"https://evil.example.com"}) // another origin
		_, e3 := a.Open(Sealed{Name: NameCompatible, Provider: ProviderGeneric, Ciphertext: flip(ct), Nonce: nonce, KeyID: keyID}, []string{"https://gw.example.com"})
		_, _, _, e4 := a.Seal("unknown-name", ProviderGeneric, nil, secret)
		_, e5 := CheckURL("https://user:" + secret + "@gw.example.com/v1")
		_, e6 := CheckURL("http://gw.example.com/v1?key=" + secret)
		_, e7 := AllowedOrigins(NameCompatible, ProviderGeneric, "https://"+secret+":x@gw.example.com", "")
		_, e8 := Canonical("::" + secret)
		errs = append(errs, e1, e2, e3, e4, e5, e6, e7, e8)
		for i, err := range errs {
			if err == nil {
				t.Fatalf("case %d did not fail", i)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("case %d: the error carries the secret", i)
			}
		}
		errs = errs[:0]
		if !errors.Is(e1, ErrUndecryptable) || !errors.Is(e2, ErrUndecryptable) || !errors.Is(e3, ErrUndecryptable) {
			t.Error("open failures are ErrUndecryptable")
		}
	}
}
