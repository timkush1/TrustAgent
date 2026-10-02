package credentials

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/truthtable/backend-go/internal/authz"
)

// InfoFingerprint is the HKDF info string of the fingerprint subkey (D30).
const InfoFingerprint = "trustagent/credentials/v1/fingerprint"

// FingerprintPrefix starts every fingerprint.
const FingerprintPrefix = "fp_"

// Fingerprinter computes credential fingerprints under one master key (D30): "fp_" plus the first
// 12 hex characters of HMAC-SHA256(k_fp, secret), where k_fp is the master key's
// InfoFingerprint subkey. A fingerprint identifies a key across views and events
// (credentials_changed carries only it) without revealing anything usable: it is keyed, so it
// cannot be checked against a guessed key without the master key, and it differs per instance.
// Env credentials on the Go side use the same function; the engine never computes one.
type Fingerprinter struct{ key []byte }

// NewFingerprinter derives the fingerprint subkey of m. A nil master gives a Fingerprinter whose
// fingerprints are all "".
func NewFingerprinter(m *authz.MasterKey) Fingerprinter {
	if m == nil {
		return Fingerprinter{}
	}
	return Fingerprinter{key: m.Derive(InfoFingerprint)}
}

// Of returns the fingerprint of secret ("" for an empty secret or a Fingerprinter without a key).
func (f Fingerprinter) Of(secret string) string {
	if secret == "" || len(f.key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, f.key)
	mac.Write([]byte(secret))
	return FingerprintPrefix + hex.EncodeToString(mac.Sum(nil))[:12]
}
