package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/truthtable/backend-go/internal/authz"
)

// NonceSize is the AES-GCM nonce length (SPEC §7.5).
const NonceSize = 12

// aadPrefix starts every credential's additional authenticated data (D78).
const aadPrefix = "trustagent/credential/v1"

// ErrUnavailable: the master key is missing or ephemeral, so the store neither seals nor opens
// (D32: the store is unavailable; 503 credentials_unavailable).
var ErrUnavailable = errors.New("credentials: the credential store is unavailable (no durable master key)")

// ErrUndecryptable: a stored row cannot be opened, because it was sealed under another master key
// (key_id differs) or its ciphertext, nonce, name, provider or allowed origins were changed (the
// GCM tag fails). Such a row is listed and never used (D32).
var ErrUndecryptable = errors.New("credentials: the stored credential cannot be decrypted")

// Sealed is a stored credential as the cipher needs it: the columns of a credentials row that
// take part in sealing.
type Sealed struct {
	Name       string
	Provider   string
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// Cipher seals and opens credential secrets with AES-256-GCM under the master key's
// InfoCredentials subkey (SPEC §7.5). It is safe for concurrent use.
type Cipher struct {
	aead  cipher.AEAD
	keyID string
	fp    Fingerprinter
}

// New returns the Cipher of master key m. A nil or ephemeral key gives ErrUnavailable: a secret
// sealed with a key that dies with the process would be lost at the next restart (D32).
func New(m *authz.MasterKey) (*Cipher, error) {
	if !Durable(m) {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(m.Derive(authz.InfoCredentials))
	if err != nil {
		return nil, fmt.Errorf("credentials: cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithNonceSize(block, NonceSize)
	if err != nil {
		return nil, fmt.Errorf("credentials: cipher: %w", err)
	}
	return &Cipher{aead: aead, keyID: m.ID, fp: NewFingerprinter(m)}, nil
}

// KeyID is the master key's id (credentials.key_id): the first 16 hex characters of
// sha256(master).
func (c *Cipher) KeyID() string { return c.keyID }

// Fingerprint returns the D30 fingerprint of secret under this master key.
func (c *Cipher) Fingerprint(secret string) string { return c.fp.Of(secret) }

// AAD is the additional authenticated data of a credential (D78):
// "trustagent/credential/v1|<name>|<provider>|<sorted origins joined by ",">". Only compatible and
// upstream-* bind their origins (fixed at creation); a vendor row binds an empty list, because its
// allowed origins are recomputed at load and the stored column is never trusted for it.
func AAD(name, provider string, origins []string) []byte {
	var bound []string
	if spec, ok := Lookup(name); ok && spec.CustomOrigin {
		bound = slices.Clone(origins)
		slices.Sort(bound)
		bound = slices.Compact(bound)
	}
	return []byte(aadPrefix + "|" + name + "|" + provider + "|" + strings.Join(bound, ","))
}

// Seal encrypts secret for the credential name of provider with allowed origins (used in the AAD
// for compatible and upstream-* only). It returns the ciphertext, a fresh random 12-byte nonce
// and the master key id to store with them. provider must be the name's provider
// (ErrProviderMismatch, D29): the AAD binds it, so a row sealed under another provider would open
// and present itself as that provider.
func (c *Cipher) Seal(name, provider string, origins []string, secret string) (ct, nonce []byte, keyID string, err error) {
	spec, ok := Lookup(name)
	if !ok {
		return nil, nil, "", fmt.Errorf("%w %q", ErrUnknownName, name)
	}
	if provider != spec.Provider {
		return nil, nil, "", fmt.Errorf("%w: %s takes provider %s", ErrProviderMismatch, name, spec.Provider)
	}
	if secret == "" {
		return nil, nil, "", errors.New("credentials: an empty secret cannot be stored")
	}
	nonce = make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, "", fmt.Errorf("credentials: nonce: %w", err)
	}
	ct = c.aead.Seal(nil, nonce, []byte(secret), AAD(name, provider, origins))
	return ct, nonce, c.keyID, nil
}

// Open decrypts a stored row whose allowed origins are origins (the stored column for compatible
// and upstream-*; ignored for vendor rows). Any failure is ErrUndecryptable, wrapped with the
// credential's name: a key_id from another master key, a nonce of the wrong size, or a GCM tag
// that does not verify (another name, provider or origin list, or tampered bytes).
func (c *Cipher) Open(row Sealed, origins []string) (string, error) {
	if row.KeyID != c.keyID {
		return "", fmt.Errorf("%w: %s was sealed under another master key", ErrUndecryptable, row.Name)
	}
	if len(row.Nonce) != NonceSize {
		return "", fmt.Errorf("%w: %s has a malformed nonce", ErrUndecryptable, row.Name)
	}
	pt, err := c.aead.Open(nil, row.Nonce, row.Ciphertext, AAD(row.Name, row.Provider, origins))
	if err != nil {
		return "", fmt.Errorf("%w: %s failed authentication", ErrUndecryptable, row.Name)
	}
	return string(pt), nil
}
