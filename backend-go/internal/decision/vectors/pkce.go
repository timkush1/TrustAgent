package vectors

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// pkceKind is the Go reference of the PKCE vectors (D103): the browser's static
// /connect/callback/ flow and the self-hosted Connect OpenRouter both derive a verifier, its S256
// challenge and the openrouter.ai/auth URL from these rules. The reference uses the standard
// library only (RFC 7636); internal/credentials checks its own functions against the same vectors
// (TestPKCEVectors). The input's op names the operation:
//
//	challenge:           {op, verifier}                         -> {challenge}
//	verifier_from_bytes: {op, bytes_hex}                        -> {verifier}
//	auth_url:            {op, callback_url, challenge, key_label?} -> {url}
var pkceKind = Kind{Mechanical: true, run: runPKCE}

// pkceAuthBase is OpenRouter's sign-in page.
const pkceAuthBase = "https://openrouter.ai/auth"

// pkceVerifierBytes is how many random bytes a verifier encodes (43 base64url characters).
const pkceVerifierBytes = 32

func runPKCE(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Op          string  `json:"op"`
		Verifier    string  `json:"verifier"`
		BytesHex    string  `json:"bytes_hex"`
		CallbackURL string  `json:"callback_url"`
		Challenge   string  `json:"challenge"`
		KeyLabel    *string `json:"key_label"`
	}
	if err := strictUnmarshal(in, &x); err != nil {
		return nil, err
	}
	switch x.Op {
	case "challenge":
		if x.Verifier == "" {
			return nil, fmt.Errorf("pkce challenge: verifier is required")
		}
		sum := sha256.Sum256([]byte(x.Verifier))
		return map[string]string{"challenge": base64.RawURLEncoding.EncodeToString(sum[:])}, nil
	case "verifier_from_bytes":
		b, err := hex.DecodeString(x.BytesHex)
		if err != nil || len(b) != pkceVerifierBytes {
			return nil, fmt.Errorf("pkce verifier_from_bytes: bytes_hex must be %d bytes of hex", pkceVerifierBytes)
		}
		return map[string]string{"verifier": base64.RawURLEncoding.EncodeToString(b)}, nil
	case "auth_url":
		if x.CallbackURL == "" || x.Challenge == "" {
			return nil, fmt.Errorf("pkce auth_url: callback_url and challenge are required")
		}
		params := [][2]string{{"callback_url", x.CallbackURL}, {"code_challenge", x.Challenge}, {"code_challenge_method", "S256"}}
		if x.KeyLabel != nil {
			params = append(params, [2]string{"key_label", *x.KeyLabel})
		}
		parts := make([]string, len(params))
		for i, p := range params {
			parts[i] = p[0] + "=" + uriComponent(p[1])
		}
		return map[string]string{"url": pkceAuthBase + "?" + strings.Join(parts, "&")}, nil
	}
	return nil, fmt.Errorf("pkce vector op %q is not one of challenge, verifier_from_bytes, auth_url", x.Op)
}

// uriComponent percent-encodes every byte of s except the RFC 3986 unreserved characters
// (A-Z a-z 0-9 - . _ ~), in upper-case hex. JavaScript's encodeURIComponent agrees on every value
// without ! ' ( ) *, which it leaves as they are; the vectors use none of them.
func uriComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}
