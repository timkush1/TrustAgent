package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// settings rows used by the authz core (migration 0004 table settings).
const (
	settingSetup        = "setup"
	settingSessionEpoch = "session_epoch"
)

// Setup-token claim failures (SPEC §7.5).
var (
	// ErrSetupTokenInvalid: no setup token is stored, or it is not the presented one.
	ErrSetupTokenInvalid = errors.New("store: the setup token is not valid")
	// ErrSetupTokenUsed: the token was claimed already.
	ErrSetupTokenUsed = errors.New("store: the setup token was already used")
	// ErrSetupTokenExpired: the token's TTL passed.
	ErrSetupTokenExpired = errors.New("store: the setup token expired")
	// ErrSetupComplete: an admin credential exists and the token was not minted with -force.
	ErrSetupComplete = errors.New("store: an admin credential already exists")
)

// SetupTokenRecord is one pending setup token. Only the token's SHA-256 is stored.
type SetupTokenRecord struct {
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Forced marks a recovery token (proxy -new-setup-token -force): it can be claimed while an
	// admin credential exists.
	Forced bool `json:"forced"`
}

// SetupState is the settings row "setup": the pending setup tokens (every proxy start and every
// -new-setup-token adds one, so a URL printed earlier keeps working until it expires) and, once
// one is claimed, the claim, which ends them all.
type SetupState struct {
	Tokens       []SetupTokenRecord `json:"tokens"`
	ClaimedAt    *time.Time         `json:"claimed_at,omitempty"`
	ClaimedKeyID string             `json:"claimed_key_id,omitempty"`
}

// MaxPendingSetupTokens bounds the pending tokens; adding one drops expired and the oldest ones.
const MaxPendingSetupTokens = 8

// dbTime is a timestamp as Postgres stores it (microseconds, UTC), so the JSON value and the SQL
// comparisons agree.
func dbTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// PutSetupToken adds a pending setup token and logs setup_token.mint (D44) in the same
// transaction. Tokens that expired by rec.CreatedAt are dropped, at most MaxPendingSetupTokens stay
// (the newest), and a claimed state starts over with rec alone. The merge is one INSERT … ON
// CONFLICT statement, so concurrent starts do not lose a token. The audit row records when the
// token expires and whether it is a forced recovery token, never its hash.
func (s *PostgresStore) PutSetupToken(ctx context.Context, rec SetupTokenRecord, actor string) error {
	if rec.SHA256 == "" {
		return errors.New("store: a setup token needs its sha256")
	}
	rec.CreatedAt, rec.ExpiresAt = dbTime(rec.CreatedAt), dbTime(rec.ExpiresAt)
	token, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := putSetupToken(ctx, tx, rec, token, actor); err != nil {
			return err
		}
		return insertAdminAudit(ctx, tx, AdminAuditEntry{
			Actor: actor, Action: "setup_token.mint",
			Diff: map[string]any{"expires_at": rec.ExpiresAt, "forced": rec.Forced},
		})
	})
}

// putSetupToken merges one token into the setup row.
func putSetupToken(ctx context.Context, tx pgx.Tx, rec SetupTokenRecord, token []byte, actor string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO settings (key, value, updated_by)
		VALUES ($1, jsonb_build_object('tokens', jsonb_build_array($2::jsonb)), $3)
		ON CONFLICT (key) DO UPDATE SET
			value = CASE
				WHEN settings.value ? 'claimed_at' OR jsonb_typeof(settings.value->'tokens') IS DISTINCT FROM 'array'
					THEN EXCLUDED.value
				ELSE jsonb_build_object('tokens', (
					SELECT COALESCE(jsonb_agg(keep.t ORDER BY (keep.t->>'created_at')::timestamptz), '[]'::jsonb)
					  FROM (SELECT t FROM jsonb_array_elements(settings.value->'tokens') AS t
					         WHERE (t->>'expires_at')::timestamptz > $4
					         ORDER BY (t->>'created_at')::timestamptz DESC
					         LIMIT $5) AS keep) || jsonb_build_array($2::jsonb))
			END,
			version = settings.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingSetup, token, actor, rec.CreatedAt, MaxPendingSetupTokens-1)
	if err != nil {
		return fmt.Errorf("store: put setup token: %w", err)
	}
	return nil
}

// SetupToken returns the setup state, or ErrNotFound when no token was ever stored.
func (s *PostgresStore) SetupToken(ctx context.Context) (*SetupState, error) {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, settingSetup).Scan(&raw); err != nil {
		return nil, err
	}
	var st SetupState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("store: setup token row: %w", err)
	}
	return &st, nil
}

// ClaimSetupToken claims the pending setup token whose SHA-256 is sha and creates admin, the
// setup-admin api key, in one transaction. The claim is a single atomic UPDATE … RETURNING on the
// settings row, so of concurrent claims exactly one succeeds; the others get ErrSetupTokenUsed. A
// token that is unknown (ErrSetupTokenInvalid), expired at now (ErrSetupTokenExpired), or not
// forced while an admin credential exists (ErrSetupComplete) creates nothing. envAdmin reports
// admin keys configured in the environment, which count as an admin credential.
func (s *PostgresStore) ClaimSetupToken(ctx context.Context, sha string, now time.Time, envAdmin bool, admin *APIKeyRecord) error {
	now = dbTime(now)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var key string
		err := tx.QueryRow(ctx, `
			UPDATE settings
			   SET value = value || jsonb_build_object('claimed_at', $2::text, 'claimed_key_id', $3::text),
			       version = version + 1, updated_by = 'setup', updated_at = now()
			 WHERE key = $1
			   AND value->>'claimed_at' IS NULL
			   AND jsonb_typeof(value->'tokens') = 'array'
			   AND EXISTS (
			       SELECT 1 FROM jsonb_array_elements(value->'tokens') AS t
			        WHERE t->>'sha256' = $4
			          AND (t->>'expires_at')::timestamptz > $5
			          AND (COALESCE((t->>'forced')::boolean, false)
			               OR (NOT $6 AND NOT EXISTS (SELECT 1 FROM api_keys WHERE 'admin' = ANY (scopes) AND revoked_at IS NULL))))
			RETURNING key`,
			settingSetup, now.Format(time.RFC3339Nano), admin.ID, sha, now, envAdmin).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.setupClaimFailure(ctx, tx, sha, now)
		}
		if err != nil {
			return fmt.Errorf("store: claim setup token: %w", err)
		}
		if err := insertAPIKey(ctx, tx, admin); err != nil {
			return fmt.Errorf("store: create the setup admin key: %w", err)
		}
		return insertAdminAudit(ctx, tx, AdminAuditEntry{
			Actor: "setup", Action: "setup.claim", Target: admin.ID,
			Diff: map[string]any{"label": admin.Label, "scopes": admin.Scopes, "prefix": admin.Prefix},
		})
	})
}

// setupClaimFailure explains why the claim UPDATE matched no row.
func (s *PostgresStore) setupClaimFailure(ctx context.Context, tx pgx.Tx, sha string, now time.Time) error {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, settingSetup).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSetupTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("store: read setup token: %w", err)
	}
	var st SetupState
	if err := json.Unmarshal(raw, &st); err != nil {
		return ErrSetupTokenInvalid
	}
	return SetupClaimOutcome(&st, sha, now)
}

// SetupClaimOutcome explains a failed claim of the token hashed sha against st at now: unknown,
// used, expired, or else (the token itself was claimable) an admin credential exists.
func SetupClaimOutcome(st *SetupState, sha string, now time.Time) error {
	var tok *SetupTokenRecord
	for i := range st.Tokens {
		if st.Tokens[i].SHA256 == sha {
			tok = &st.Tokens[i]
		}
	}
	switch {
	case tok == nil:
		return ErrSetupTokenInvalid
	case st.ClaimedAt != nil:
		return ErrSetupTokenUsed
	case !tok.ExpiresAt.After(now):
		return ErrSetupTokenExpired
	}
	return ErrSetupComplete
}

// SessionEpoch returns the settings row session_epoch (0 when it was never bumped). Sessions carry
// the epoch they were issued in; bumping it ends every session (SPEC §7.5).
func (s *PostgresStore) SessionEpoch(ctx context.Context) (int64, error) {
	var ep int64
	err := s.pool.QueryRow(ctx, `SELECT (value #>> '{}')::bigint FROM settings WHERE key = $1`, settingSessionEpoch).Scan(&ep)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: read session epoch: %w", err)
	}
	return ep, nil
}

// BumpSessionEpoch increments session_epoch, logs session.revoke_all and returns the new epoch.
func (s *PostgresStore) BumpSessionEpoch(ctx context.Context, actor string) (int64, error) {
	var ep int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO settings (key, value, updated_by) VALUES ($1, '1'::jsonb, $2)
			ON CONFLICT (key) DO UPDATE SET value = to_jsonb((settings.value #>> '{}')::bigint + 1),
				version = settings.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
			RETURNING (value #>> '{}')::bigint`, settingSessionEpoch, actor).Scan(&ep); err != nil {
			return fmt.Errorf("store: bump session epoch: %w", err)
		}
		return insertAdminAudit(ctx, tx, AdminAuditEntry{
			Actor: actor, Action: "session.revoke_all", Diff: map[string]any{"session_epoch": ep},
		})
	})
	return ep, err
}
