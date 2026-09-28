package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Policy versions (SPEC §2.3 policy_versions, §7.2). Every write takes the policy write lock, a
// transaction-scoped advisory lock, so dashboard saves, rollbacks, file adoption and the file
// sync of every proxy replica are serialized: a file version is inserted at most once per file
// hash, and an If-Match save compares against the head it will actually be inserted after.

// Policy version sources (policy_versions.source).
const (
	PolicySourceBootstrap = "bootstrap"
	PolicySourceFile      = "file"
	PolicySourceDashboard = "dashboard"
	PolicySourceRollback  = "rollback"
	PolicySourceAdoptFile = "adopt_file"
)

// policyLockClass is the first key of the policy write lock, pg_advisory_xact_lock(class,
// hashtext(current_schema())). The second key scopes the lock to the schema, so isolated test
// schemas (storetest) never contend. Migrations use 7212026.
const policyLockClass int32 = 7212027

// ConfigChannel is the default NOTIFY channel of configuration writes (SPEC §2.3): the payload is
// "<table>:<id>". Tests use a per-schema channel.
const ConfigChannel = "trustagent_config"

// PolicyVersionRecord is one policy_versions row.
type PolicyVersionRecord struct {
	ID        int64
	CreatedAt time.Time
	// Source is bootstrap, file, dashboard, rollback or adopt_file.
	Source  string
	Author  string
	Message string
	// Doc is the policy document as written (PolicyDoc JSON); SHA256 is sha256(JCS(doc)).
	Doc    json.RawMessage
	YAML   string
	SHA256 string
	// FileSHA256 is the sha256 of the policies.yaml bytes a file or adopt_file version came from.
	FileSHA256 *string
	ParentID   *int64
}

// PolicyTx reads and writes policy versions inside the policy write lock.
type PolicyTx interface {
	// Head returns the newest version, or nil when there is none.
	Head(ctx context.Context) (*PolicyVersionRecord, error)
	// LastFileVersion returns the newest version that came from the policy file (file or
	// adopt_file, i.e. with a file hash), or nil.
	LastFileVersion(ctx context.Context) (*PolicyVersionRecord, error)
	// LastDashboardVersion returns the newest dashboard or rollback version, or nil.
	LastDashboardVersion(ctx context.Context) (*PolicyVersionRecord, error)
	// Insert adds v, filling ID and CreatedAt, and queues the "policy_versions:<id>" notification,
	// which is delivered when the transaction commits.
	Insert(ctx context.Context, v *PolicyVersionRecord) error
}

const policyVersionColumns = `id, created_at, source, author, message, doc, yaml, sha256, file_sha256, parent_id`

// WithPolicyLock runs fn in a transaction that holds the policy write lock. Notifications go to
// channel (ConfigChannel when empty). fn's error rolls the transaction back.
func (s *PostgresStore) WithPolicyLock(ctx context.Context, channel string, fn func(PolicyTx) error) error {
	if channel == "" {
		channel = ConfigChannel
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("policy lock: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext(current_schema()))`, policyLockClass); err != nil {
		return fmt.Errorf("policy lock: %w", err)
	}
	if err := fn(&policyTx{tx: tx, channel: channel}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("policy lock: commit: %w", err)
	}
	return nil
}

type policyTx struct {
	tx      pgx.Tx
	channel string
}

func (p *policyTx) Head(ctx context.Context) (*PolicyVersionRecord, error) {
	return optionalVersion(scanPolicyVersion(p.tx.QueryRow(ctx,
		`SELECT `+policyVersionColumns+` FROM policy_versions ORDER BY id DESC LIMIT 1`)))
}

func (p *policyTx) LastFileVersion(ctx context.Context) (*PolicyVersionRecord, error) {
	return optionalVersion(scanPolicyVersion(p.tx.QueryRow(ctx,
		`SELECT `+policyVersionColumns+` FROM policy_versions WHERE file_sha256 IS NOT NULL ORDER BY id DESC LIMIT 1`)))
}

func (p *policyTx) LastDashboardVersion(ctx context.Context) (*PolicyVersionRecord, error) {
	return optionalVersion(scanPolicyVersion(p.tx.QueryRow(ctx,
		`SELECT `+policyVersionColumns+` FROM policy_versions WHERE source IN ('dashboard', 'rollback') ORDER BY id DESC LIMIT 1`)))
}

func (p *policyTx) Insert(ctx context.Context, v *PolicyVersionRecord) error {
	if len(v.Doc) == 0 || v.YAML == "" || v.SHA256 == "" || v.Author == "" {
		return errors.New("store: a policy version needs doc, yaml, sha256 and author")
	}
	err := p.tx.QueryRow(ctx, `
		INSERT INTO policy_versions (source, author, message, doc, yaml, sha256, file_sha256, parent_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		v.Source, v.Author, v.Message, v.Doc, v.YAML, v.SHA256, v.FileSHA256, v.ParentID,
	).Scan(&v.ID, &v.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert policy version: %w", err)
	}
	v.CreatedAt = v.CreatedAt.UTC()
	if _, err := p.tx.Exec(ctx, `SELECT pg_notify($1, $2)`, p.channel, "policy_versions:"+strconv.FormatInt(v.ID, 10)); err != nil {
		return fmt.Errorf("notify policy version: %w", err)
	}
	return nil
}

// LatestPolicyVersion returns the newest policy version, or ErrNotFound when there is none.
func (s *PostgresStore) LatestPolicyVersion(ctx context.Context) (*PolicyVersionRecord, error) {
	return scanPolicyVersion(s.pool.QueryRow(ctx,
		`SELECT `+policyVersionColumns+` FROM policy_versions ORDER BY id DESC LIMIT 1`))
}

// PolicyHeadID returns the id of the newest policy version, 0 when there is none. It is the cheap
// probe of the reload poll.
func (s *PostgresStore) PolicyHeadID(ctx context.Context) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM policy_versions`).Scan(&id)
	return id, err
}

// GetPolicyVersion returns one policy version, or ErrNotFound.
func (s *PostgresStore) GetPolicyVersion(ctx context.Context, id int64) (*PolicyVersionRecord, error) {
	return scanPolicyVersion(s.pool.QueryRow(ctx,
		`SELECT `+policyVersionColumns+` FROM policy_versions WHERE id = $1`, id))
}

// ListPolicyVersions returns a keyset page of policy versions, newest first, and the cursor of
// the next page ("" at the end). limit is clamped to [1, 200] (default 50).
func (s *PostgresStore) ListPolicyVersions(ctx context.Context, limit int, cursor string) ([]PolicyVersionRecord, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before := int64(0)
	if cursor != "" {
		var err error
		if before, err = decodeIDCursor(cursor); err != nil {
			return nil, "", err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT `+policyVersionColumns+` FROM policy_versions
		WHERE ($1 = 0 OR id < $1) ORDER BY id DESC LIMIT $2`, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []PolicyVersionRecord{}
	for rows.Next() {
		v, err := scanPolicyVersion(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeIDCursor(out[limit-1].ID)
	}
	return out, next, nil
}

func scanPolicyVersion(row pgx.Row) (*PolicyVersionRecord, error) {
	var v PolicyVersionRecord
	if err := row.Scan(&v.ID, &v.CreatedAt, &v.Source, &v.Author, &v.Message, &v.Doc, &v.YAML, &v.SHA256,
		&v.FileSHA256, &v.ParentID); err != nil {
		return nil, err
	}
	v.CreatedAt = v.CreatedAt.UTC()
	return &v, nil
}

// optionalVersion turns ErrNotFound into (nil, nil).
func optionalVersion(v *PolicyVersionRecord, err error) (*PolicyVersionRecord, error) {
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return v, err
}

// encodeIDCursor encodes a keyset position over a BIGSERIAL id.
func encodeIDCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("id|" + strconv.FormatInt(id, 10)))
}

func decodeIDCursor(c string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, ErrBadCursor
	}
	s, ok := strings.CutPrefix(string(raw), "id|")
	if !ok {
		return 0, ErrBadCursor
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrBadCursor
	}
	return id, nil
}
