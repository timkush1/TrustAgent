package authz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// DefaultSetupTTL is the setup token's lifetime (TRUTHTABLE_SETUP_TOKEN_TTL).
const DefaultSetupTTL = time.Hour

// SetupAdminLabel is the label of the admin key a setup claim creates.
const SetupAdminLabel = "setup-admin"

// SetupLinePrefix starts the line that prints the setup URL (`docker compose logs proxy | grep -m1
// SETUP`).
const SetupLinePrefix = "SETUP: open "

// Setup errors.
var (
	// ErrSetupNeedsStore: the setup token is stored in the database; without one, sign in with
	// TRUTHTABLE_ADMIN_KEYS.
	ErrSetupNeedsStore = errors.New("authz: the setup token needs a database (TRUTHTABLE_DATABASE_URL); set TRUTHTABLE_ADMIN_KEYS instead")
	// ErrSetupNotRequired: an admin credential exists; mint a recovery token with -force.
	ErrSetupNotRequired = errors.New("authz: an admin credential already exists; use -new-setup-token -force to recover access")
	// ErrSetupTokenInvalid, ErrSetupTokenUsed, ErrSetupTokenExpired, ErrSetupComplete: claim
	// failures (see store).
	ErrSetupTokenInvalid = store.ErrSetupTokenInvalid
	ErrSetupTokenUsed    = store.ErrSetupTokenUsed
	ErrSetupTokenExpired = store.ErrSetupTokenExpired
	ErrSetupComplete     = store.ErrSetupComplete
)

// Setup is the one-time setup token (SPEC §7.5): it exists only while no admin credential exists,
// is 32 random bytes stored as its SHA-256 in settings.setup with a 1 h TTL, travels in the URL
// fragment (never sent to a server log), and its claim atomically creates the setup-admin key.
type Setup struct {
	a         *Authenticator
	ttl       time.Duration
	publicURL string
}

// TTL is the token lifetime.
func (s *Setup) TTL() time.Duration { return s.ttl }

// Required reports whether no admin credential exists: no env admin key and no active api_keys row
// with the admin scope. Without a database only env admin keys count.
func (s *Setup) Required(ctx context.Context) (bool, error) {
	if s.a.HasEnvAdmin() {
		return false, nil
	}
	if s.a.store == nil {
		return true, nil
	}
	exists, err := s.a.store.AdminKeyExists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// Mint stores a new setup token and returns it with its expiry. seed ("" for a random token) is
// TRUTHTABLE_SETUP_TOKEN. Unless force is set, it fails with ErrSetupNotRequired while an admin
// credential exists; a forced (recovery) token can be claimed anyway.
func (s *Setup) Mint(ctx context.Context, seed string, force bool, actor string) (string, time.Time, error) {
	if s.a.store == nil {
		return "", time.Time{}, ErrSetupNeedsStore
	}
	if !force {
		required, err := s.Required(ctx)
		if err != nil {
			return "", time.Time{}, err
		}
		if !required {
			return "", time.Time{}, ErrSetupNotRequired
		}
	}
	token := seed
	if token == "" {
		token = randomToken()
	}
	now := s.a.now()
	exp := now.Add(s.ttl)
	rec := store.SetupTokenRecord{SHA256: hashHex(token), CreatedAt: now, ExpiresAt: exp, Forced: force}
	if err := s.a.store.PutSetupToken(ctx, rec, actor); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// URL is the dashboard setup URL; the token is in the fragment, which browsers never send.
func (s *Setup) URL(token string) string {
	return strings.TrimRight(s.publicURL, "/") + "/app/setup#token=" + token
}

// Line is the printed setup line: "SETUP: open <url>".
func (s *Setup) Line(token string) string { return SetupLinePrefix + s.URL(token) }

// Announce mints a token when setup is required and prints its line to w (the proxy's stdout, not
// the structured log, so log redaction does not mask it). It reports whether it printed one.
func (s *Setup) Announce(ctx context.Context, w io.Writer, seed string) (bool, time.Time, error) {
	required, err := s.Required(ctx)
	if err != nil || !required {
		return false, time.Time{}, err
	}
	token, exp, err := s.Mint(ctx, seed, false, "startup")
	if err != nil {
		return false, time.Time{}, err
	}
	if _, err := fmt.Fprintln(w, s.Line(token)); err != nil {
		return false, time.Time{}, err
	}
	return true, exp, nil
}

// Claim redeems a setup token: it creates the setup-admin key (returned once, as secret) and
// returns that key's identity, ready for IssueSession.
func (s *Setup) Claim(ctx context.Context, token string) (*Identity, string, error) {
	if s.a.store == nil {
		return nil, "", ErrSetupNeedsStore
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", ErrSetupTokenInvalid
	}
	secret, sha, prefix := MintKey()
	rec := &store.APIKeyRecord{
		ID: store.NewID(), Label: SetupAdminLabel, Scopes: []string{string(ScopeAdmin)},
		SHA256: sha, Prefix: prefix, CreatedBy: "setup",
	}
	if err := s.a.store.ClaimSetupToken(ctx, hashHex(token), s.a.now(), s.a.HasEnvAdmin(), rec); err != nil {
		return nil, "", err
	}
	return keyIdentity(rec), secret, nil
}
