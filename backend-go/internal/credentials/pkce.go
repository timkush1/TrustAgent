package credentials

// pkce.go is the core of Connect OpenRouter for a self-hosted gateway (SPEC §7.5, D38):
// the RFC 7636 verifier and S256 challenge, the in-memory store of pending sign-ins, the
// return_to and callback-origin rules, the OpenRouter /auth URL and the code exchange. The key
// never reaches browser JavaScript: the verifier stays on the server, keyed by a nonce the
// browser keeps in sessionStorage. It belongs to GCONN; the endpoints are cmd/proxy/connect_api.go.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/netguard"
)

// Connect OpenRouter limits (D38).
const (
	// ConnectTTL is how long a started sign-in may be completed.
	ConnectTTL = 10 * time.Minute
	// ConnectPerActor is how many sign-ins one admin may have pending; a new one evicts the
	// oldest.
	ConnectPerActor = 5
	// connectVerifierBytes and connectNonceBytes are the random bytes of the code verifier
	// (43 base64url characters, RFC 7636 §4.1) and of the nonce.
	connectVerifierBytes = 32
	connectNonceBytes    = 16
	// maxReturnTo bounds a return_to path.
	maxReturnTo = 512
	// maxExchangedKey bounds the key the exchange returns.
	maxExchangedKey = 512
)

// DefaultConnectReturnTo is where the dashboard returns after a sign-in started without a
// return_to: the setup wizard's connect step.
const DefaultConnectReturnTo = "/app/setup?step=connect"

// Connect OpenRouter errors.
var (
	// ErrInvalidNonce: the nonce is unknown, expired, already used, another admin's, or
	// presented from another origin (400 invalid_nonce).
	ErrInvalidNonce = errors.New("the sign-in is unknown, expired, already used, or was started elsewhere")
	// ErrReturnTo: return_to is not a dashboard path (422).
	ErrReturnTo = errors.New("return_to must be a dashboard path under /app/")
	// ErrCallbackOrigin: the request came from an origin the gateway does not allow, so the
	// sign-in could not come back to it (409 conflict with details.public_url).
	ErrCallbackOrigin = errors.New("the dashboard's address is not an allowed origin")
)

// NewCodeVerifier returns a fresh PKCE code verifier: 32 random bytes, base64url without padding
// (RFC 7636 §4.1).
func NewCodeVerifier() (string, error) {
	return randomToken(connectVerifierBytes)
}

// CodeChallenge is the S256 challenge of a verifier: base64url(SHA-256(verifier)) without padding
// (RFC 7636 §4.2).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("credentials: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PendingConnect is one started sign-in, as the store keeps it until it is completed.
type PendingConnect struct {
	// Verifier is the PKCE code verifier; it never leaves the server except to OpenRouter.
	Verifier string
	// Actor is the admin who started it; CallbackOrigin the dashboard origin OpenRouter returns
	// to (and the only origin that may complete it); ReturnTo the dashboard path to go back to.
	Actor, CallbackOrigin, ReturnTo string
	Expires                         time.Time
}

// ConnectStore holds pending sign-ins in memory, by nonce (D38). Several replicas need sticky
// sessions for the 10-minute window (SECURITY.md). It is safe for concurrent use.
type ConnectStore struct {
	ttl      time.Duration
	perActor int
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]*pendingEntry
	seq     uint64
}

type pendingEntry struct {
	PendingConnect
	seq uint64 // start order, for evicting an actor's oldest sign-in
}

// NewConnectStore returns a store whose sign-ins live ttl (ConnectTTL when ≤ 0), at most perActor
// pending per admin (ConnectPerActor when ≤ 0). now is the clock (nil: time.Now).
func NewConnectStore(ttl time.Duration, perActor int, now func() time.Time) *ConnectStore {
	if ttl <= 0 {
		ttl = ConnectTTL
	}
	if perActor <= 0 {
		perActor = ConnectPerActor
	}
	if now == nil {
		now = time.Now
	}
	return &ConnectStore{ttl: ttl, perActor: perActor, now: now, pending: map[string]*pendingEntry{}}
}

// Start records a new sign-in for actor and returns its nonce and the pending record (with a new
// verifier). callbackOrigin and returnTo must already be validated (ConnectCallbackOrigin,
// ValidateReturnTo). Expired sign-ins are dropped, and an actor at the cap loses the oldest.
func (s *ConnectStore) Start(actor, callbackOrigin, returnTo string) (string, PendingConnect, error) {
	origin := connectOrigin(callbackOrigin)
	if origin == "" {
		return "", PendingConnect{}, ErrCallbackOrigin
	}
	verifier, err := NewCodeVerifier()
	if err != nil {
		return "", PendingConnect{}, err
	}
	nonce, err := randomToken(connectNonceBytes)
	if err != nil {
		return "", PendingConnect{}, err
	}
	now := s.now()
	p := PendingConnect{Verifier: verifier, Actor: actor, CallbackOrigin: origin, ReturnTo: returnTo, Expires: now.Add(s.ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mine []*pendingEntry
	var mineNonces []string
	for n, e := range s.pending {
		switch {
		case !now.Before(e.Expires):
			delete(s.pending, n)
		case e.Actor == actor:
			mine = append(mine, e)
			mineNonces = append(mineNonces, n)
		}
	}
	for len(mine) >= s.perActor {
		oldest := 0
		for i := range mine {
			if mine[i].seq < mine[oldest].seq {
				oldest = i
			}
		}
		delete(s.pending, mineNonces[oldest])
		mine = slices.Delete(mine, oldest, oldest+1)
		mineNonces = slices.Delete(mineNonces, oldest, oldest+1)
	}
	s.seq++
	s.pending[nonce] = &pendingEntry{PendingConnect: p, seq: s.seq}
	return nonce, p, nil
}

// Complete consumes nonce for actor calling from origin. The nonce is gone after the first call,
// whatever the outcome: an unknown or expired nonce, another admin's, or one presented from
// another origin than its callback origin all answer ErrInvalidNonce and burn it.
func (s *ConnectStore) Complete(nonce, actor, origin string) (PendingConnect, error) {
	s.mu.Lock()
	e, ok := s.pending[nonce]
	delete(s.pending, nonce)
	s.mu.Unlock()
	switch {
	case !ok, !s.now().Before(e.Expires), e.Actor != actor, connectOrigin(origin) != e.CallbackOrigin:
		return PendingConnect{}, ErrInvalidNonce
	}
	return e.PendingConnect, nil
}

// Pending is the number of sign-ins waiting (expired ones included until the next Start).
func (s *ConnectStore) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// ConnectCallbackOrigin picks the dashboard origin OpenRouter returns to (D38): the request's
// Origin when allows accepts it (authz.AllowsOrigin), else the origin of publicURL
// (TRUTHTABLE_PUBLIC_URL) for a request without one. An Origin that is present but not allowed
// is ErrCallbackOrigin: the sign-in would come back to an address that cannot complete it.
func ConnectCallbackOrigin(requestOrigin string, allows func(string) bool, publicURL string) (string, error) {
	if strings.TrimSpace(requestOrigin) != "" {
		o := connectOrigin(requestOrigin)
		if o == "" || allows == nil || !allows(requestOrigin) {
			return "", ErrCallbackOrigin
		}
		return o, nil
	}
	o := connectOrigin(publicURL)
	if o == "" {
		return "", fmt.Errorf("%w: TRUTHTABLE_PUBLIC_URL is not an http(s) URL", ErrCallbackOrigin)
	}
	return o, nil
}

// connectOrigin is the canonical http(s) origin of raw, or "".
func connectOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return ""
	}
	return jev.CanonicalOrigin(u)
}

// ValidateReturnTo checks a return_to (D38): after path.Clean it must start with /app/ and,
// as sent and once percent-decoded, contain no "//", "\", "..", encoded slash or backslash, nor
// any control or other non-printable character. "" gives DefaultConnectReturnTo. The cleaned
// path is returned; the dashboard navigates only to that server-echoed value, through its router.
func ValidateReturnTo(raw string) (string, error) {
	if raw == "" {
		return DefaultConnectReturnTo, nil
	}
	if len(raw) > maxReturnTo {
		return "", fmt.Errorf("%w (at most %d characters)", ErrReturnTo, maxReturnTo)
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("%w (bad percent-encoding)", ErrReturnTo)
	}
	for _, s := range []string{raw, decoded} {
		lower := strings.ToLower(s)
		for _, bad := range []string{"//", `\`, "..", "%2f", "%5c"} {
			if strings.Contains(lower, bad) {
				return "", fmt.Errorf("%w (it contains %q)", ErrReturnTo, bad)
			}
		}
		if strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
			return "", fmt.Errorf("%w (it contains a control or invisible character)", ErrReturnTo)
		}
	}
	clean := path.Clean(raw)
	if !strings.HasPrefix(clean, "/app/") {
		return "", ErrReturnTo
	}
	return clean, nil
}

// ConnectAuthURL is OpenRouter's sign-in URL (D38): <origin of openRouterBase>/auth with the
// dashboard's /connect/callback as callback_url and the S256 challenge. openRouterBase is
// TRUTHTABLE_OPENROUTER_BASE_URL ("" = https://openrouter.ai).
func ConnectAuthURL(openRouterBase, callbackOrigin, challenge string) (string, error) {
	base := connectOrigin(defaultIfBlank(openRouterBase, jev.DefaultOpenRouterBaseURL))
	if base == "" {
		return "", errors.New("credentials: the OpenRouter base URL is not an http(s) URL")
	}
	q := url.Values{}
	q.Set("callback_url", callbackOrigin+"/connect/callback")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return base + "/auth?" + q.Encode(), nil
}

// ExchangeError is a failed code exchange: OpenRouter's HTTP status (0 for a network error) and
// its message, masked and cut to MaxVerifyMessage characters.
type ExchangeError struct {
	Status  int
	Message string
}

func (e *ExchangeError) Error() string {
	if e.Status == 0 {
		return "the OpenRouter code exchange failed: " + e.Message
	}
	return fmt.Sprintf("OpenRouter refused the code exchange (HTTP %d): %s", e.Status, e.Message)
}

// ExchangeConnectCode trades a sign-in code for a key: POST <origin of openRouterBase>/api/v1/auth/keys
// {code, code_verifier, code_challenge_method:"S256"} (SPEC §7.5 step 4). The URL's origin must be
// one of allowed (the openrouter credential's allowed origins, D78). hc nil uses a client with a
// 15 s timeout; redirects are never followed. The key is returned as received and must join the
// process redactor before any other use (D42).
func ExchangeConnectCode(ctx context.Context, hc *http.Client, openRouterBase string, allowed []string, code, verifier string) (string, error) {
	origin := connectOrigin(defaultIfBlank(openRouterBase, jev.DefaultOpenRouterBaseURL))
	if origin == "" || !slices.Contains(canonicalOrigins(allowed), origin) {
		return "", fmt.Errorf("%w: %s", errVerifyOriginRefused, origin)
	}
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v1/auth/keys", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := http.Client{Timeout: 15 * time.Second, Transport: netguard.Transport()}
	if hc != nil {
		client = *hc
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", &ExchangeError{Message: cleanText(unwrapURLErr(err).Error(), "")}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", &ExchangeError{Status: resp.StatusCode, Message: cleanText(vendorMessage(raw, resp.StatusCode), "")}
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || !plausibleKey(out.Key) {
		return "", &ExchangeError{Status: resp.StatusCode, Message: "OpenRouter answered without a usable key"}
	}
	return out.Key, nil
}

// plausibleKey accepts a non-empty key of printable ASCII without spaces.
func plausibleKey(k string) bool {
	if k == "" || len(k) > maxExchangedKey {
		return false
	}
	for _, r := range k {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

// canonicalOrigins canonicalizes a list of origins, dropping anything that is not one.
func canonicalOrigins(list []string) []string {
	out := make([]string, 0, len(list))
	for _, a := range list {
		if u, err := url.Parse(strings.TrimSpace(a)); err == nil && u.Scheme != "" && u.Host != "" {
			out = append(out, jev.CanonicalOrigin(u))
		}
	}
	return out
}

// cleanText masks secret and every credential pattern in s, then cuts it to MaxVerifyMessage
// characters.
func cleanText(s, secret string) string {
	r := config.NewRedactor(secret)
	return r.Redact(truncateChars(r.Redact(maskCutSecret(s, secret)), MaxVerifyMessage))
}
