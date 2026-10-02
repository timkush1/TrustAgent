package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Evaluators and their immutable versions (SPEC §2.3 evaluators, evaluator_versions; §7.2).
// Built-in templates and config/evaluators files are registered at startup with
// UpsertEvaluatorVersions, which only ever adds rows: a version that is already stored keeps its
// stored spec (the migration's trigger refuses updates), and the caller is told when the spec it
// offered differs.

// Evaluator origins (evaluators.origin).
const (
	EvaluatorOriginBuiltin   = "builtin"
	EvaluatorOriginFile      = "file"
	EvaluatorOriginDashboard = "dashboard"
)

// EvaluatorRecord is one evaluators row.
type EvaluatorRecord struct {
	ID            string
	Name          string
	Category      string
	Origin        string
	LatestVersion int
	Archived      bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// EvaluatorVersionRecord is one evaluator_versions row.
type EvaluatorVersionRecord struct {
	EvaluatorID    string
	Version        int
	Spec           json.RawMessage
	SpecSHA256     string
	QuestionSHA256 string
	CalibrationKey string
	CreatedBy      string
	CreatedAt      time.Time
	Changelog      string
}

// Ref returns "<id>@<version>".
func (v *EvaluatorVersionRecord) Ref() string { return v.EvaluatorID + "@" + strconv.Itoa(v.Version) }

// EvaluatorUpsert is an evaluator version to register if it is not stored yet. Evaluator gives the
// name, category and origin used when the evaluator row does not exist.
type EvaluatorUpsert struct {
	Evaluator EvaluatorRecord
	Version   EvaluatorVersionRecord
}

// EvaluatorUpsertResult reports what UpsertEvaluatorVersions did with one version.
type EvaluatorUpsertResult struct {
	Ref string
	// Inserted is true when the version row was added.
	Inserted bool
	// StoredSHA256 is the spec_sha256 now stored: the offered one when Inserted, otherwise the
	// existing row's (which differs from the offered spec when the same version was changed).
	StoredSHA256 string
}

// UpsertEvaluatorVersions registers each version that is not stored yet, in one transaction:
// the evaluator row is created when missing, the version row is added, and latest_version (with
// name and category, which follow the newest version) and updated_at move forward. Existing rows
// are never changed, so calling it again with the same input writes nothing. Each added version
// notifies "evaluator_versions:<id>@<version>" on channel (ConfigChannel when empty).
func (s *PostgresStore) UpsertEvaluatorVersions(ctx context.Context, channel string, items []EvaluatorUpsert) ([]EvaluatorUpsertResult, error) {
	if channel == "" {
		channel = ConfigChannel
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("upsert evaluators: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	out := make([]EvaluatorUpsertResult, 0, len(items))
	for _, it := range items {
		e, v := it.Evaluator, it.Version
		if e.ID == "" || e.ID != v.EvaluatorID || v.Version < 1 || len(v.Spec) == 0 {
			return nil, fmt.Errorf("upsert evaluators: invalid item %q@%d", v.EvaluatorID, v.Version)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO evaluators (id, name, category, origin, latest_version)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
			e.ID, e.Name, e.Category, e.Origin, v.Version); err != nil {
			return nil, fmt.Errorf("upsert evaluator %s: %w", e.ID, err)
		}
		res := EvaluatorUpsertResult{Ref: v.Ref()}
		var inserted int
		err := tx.QueryRow(ctx, `
			INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256, calibration_key, created_by, changelog)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (evaluator_id, version) DO NOTHING
			RETURNING version`,
			v.EvaluatorID, v.Version, v.Spec, v.SpecSHA256, v.QuestionSHA256, v.CalibrationKey, v.CreatedBy, v.Changelog,
		).Scan(&inserted)
		switch {
		case err == nil:
			res.Inserted, res.StoredSHA256 = true, v.SpecSHA256
			if _, err := tx.Exec(ctx, `
				UPDATE evaluators SET
				  name = CASE WHEN $2 >= latest_version THEN $3 ELSE name END,
				  category = CASE WHEN $2 >= latest_version THEN $4 ELSE category END,
				  latest_version = GREATEST(latest_version, $2), updated_at = now()
				WHERE id = $1`, e.ID, v.Version, e.Name, e.Category); err != nil {
				return nil, fmt.Errorf("upsert evaluator %s: %w", e.ID, err)
			}
			if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, "evaluator_versions:"+v.Ref()); err != nil {
				return nil, fmt.Errorf("notify evaluator version: %w", err)
			}
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.QueryRow(ctx, `SELECT spec_sha256 FROM evaluator_versions WHERE evaluator_id = $1 AND version = $2`,
				v.EvaluatorID, v.Version).Scan(&res.StoredSHA256); err != nil {
				return nil, fmt.Errorf("read evaluator version %s: %w", v.Ref(), err)
			}
		default:
			return nil, fmt.Errorf("insert evaluator version %s: %w", v.Ref(), err)
		}
		out = append(out, res)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("upsert evaluators: commit: %w", err)
	}
	return out, nil
}

// ListEvaluators returns every evaluator row, by id.
func (s *PostgresStore) ListEvaluators(ctx context.Context) ([]EvaluatorRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, category, origin, latest_version, archived, created_at, updated_at
		FROM evaluators ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EvaluatorRecord, error) {
		var e EvaluatorRecord
		err := row.Scan(&e.ID, &e.Name, &e.Category, &e.Origin, &e.LatestVersion, &e.Archived, &e.CreatedAt, &e.UpdatedAt)
		e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
		return e, err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []EvaluatorRecord{}
	}
	return out, nil
}

// ListEvaluatorVersions returns every evaluator version with its spec, by id and version. The
// table holds one row per saved version, so the registry reads it whole.
func (s *PostgresStore) ListEvaluatorVersions(ctx context.Context) ([]EvaluatorVersionRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT evaluator_id, version, spec, spec_sha256, question_sha256, calibration_key,
		created_by, created_at, changelog FROM evaluator_versions ORDER BY evaluator_id, version`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EvaluatorVersionRecord, error) {
		var v EvaluatorVersionRecord
		err := row.Scan(&v.EvaluatorID, &v.Version, &v.Spec, &v.SpecSHA256, &v.QuestionSHA256, &v.CalibrationKey,
			&v.CreatedBy, &v.CreatedAt, &v.Changelog)
		v.CreatedAt = v.CreatedAt.UTC()
		return v, err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []EvaluatorVersionRecord{}
	}
	return out, nil
}

// EvaluatorsFingerprint summarizes the evaluator tables (row counts and newest change times) so
// the reload poll can tell cheaply whether anything changed.
func (s *PostgresStore) EvaluatorsFingerprint(ctx context.Context) (string, error) {
	var nEval, nVer int64
	var evalAt, verAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM evaluators), (SELECT max(updated_at) FROM evaluators),
		(SELECT count(*) FROM evaluator_versions), (SELECT max(created_at) FROM evaluator_versions)`).
		Scan(&nEval, &evalAt, &nVer, &verAt)
	if err != nil {
		return "", err
	}
	at := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return strconv.FormatInt(t.UnixMicro(), 10)
	}
	return fmt.Sprintf("%d|%s|%d|%s", nEval, at(evalAt), nVer, at(verAt)), nil
}

// Errors of the evaluator writes (SPEC §2.4 rows 31-33, D57).
var (
	// ErrEvaluatorExists: POST /api/evaluators named an id that is already registered.
	ErrEvaluatorExists = errors.New("store: an evaluator with this id exists")
	// ErrEvaluatorBuiltin: built-in evaluators take no dashboard versions ("Duplicate & edit"
	// forks them instead).
	ErrEvaluatorBuiltin = errors.New("store: built-in evaluators are read-only")
	// ErrEvaluatorFileManaged: an evaluator from config/evaluators changes through its file.
	ErrEvaluatorFileManaged = errors.New("store: the evaluator is file-managed")
)

// StaleEvaluatorError is returned when a new version's base is not the latest version any more
// (another writer saved one first).
type StaleEvaluatorError struct {
	ID     string
	Base   int
	Latest int
}

func (e *StaleEvaluatorError) Error() string {
	return fmt.Sprintf("store: evaluator %s is at version %d, not %d", e.ID, e.Latest, e.Base)
}

// evaluatorColumns are the evaluators columns scanEvaluator reads.
const evaluatorColumns = `id, name, category, origin, latest_version, archived, created_at, updated_at`

func scanEvaluator(row pgx.Row) (*EvaluatorRecord, error) {
	var e EvaluatorRecord
	if err := row.Scan(&e.ID, &e.Name, &e.Category, &e.Origin, &e.LatestVersion, &e.Archived, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return &e, nil
}

// insertEvaluatorVersion adds one version row in tx, fills v.CreatedAt and notifies
// "evaluator_versions:<ref>" on channel at commit.
func insertEvaluatorVersion(ctx context.Context, tx pgx.Tx, channel string, v *EvaluatorVersionRecord) error {
	if err := tx.QueryRow(ctx, `
		INSERT INTO evaluator_versions (evaluator_id, version, spec, spec_sha256, question_sha256, calibration_key, created_by, changelog)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
		v.EvaluatorID, v.Version, v.Spec, v.SpecSHA256, v.QuestionSHA256, v.CalibrationKey, v.CreatedBy, v.Changelog,
	).Scan(&v.CreatedAt); err != nil {
		return fmt.Errorf("insert evaluator version %s: %w", v.Ref(), err)
	}
	v.CreatedAt = v.CreatedAt.UTC()
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, configChannel(channel), "evaluator_versions:"+v.Ref()); err != nil {
		return fmt.Errorf("notify evaluator version: %w", err)
	}
	return nil
}

func configChannel(channel string) string {
	if channel == "" {
		return ConfigChannel
	}
	return channel
}

// CreateEvaluator stores a new dashboard evaluator with its first version and the admin audit row,
// in one transaction (POST /api/evaluators). An id that is already stored, archived or not, is
// ErrEvaluatorExists. v.CreatedAt is filled in.
func (s *PostgresStore) CreateEvaluator(ctx context.Context, channel string, e EvaluatorRecord, v *EvaluatorVersionRecord, audit AdminAuditEntry) error {
	if e.ID == "" || e.ID != v.EvaluatorID || v.Version < 1 || len(v.Spec) == 0 {
		return fmt.Errorf("create evaluator: invalid version %q@%d", v.EvaluatorID, v.Version)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create evaluator: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	tag, err := tx.Exec(ctx, `
		INSERT INTO evaluators (id, name, category, origin, latest_version)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Name, e.Category, EvaluatorOriginDashboard, v.Version)
	if err != nil {
		return fmt.Errorf("create evaluator %s: %w", e.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEvaluatorExists
	}
	if err := insertEvaluatorVersion(ctx, tx, channel, v); err != nil {
		return err
	}
	if err := InsertAdminAudit(ctx, tx, audit); err != nil {
		return fmt.Errorf("create evaluator: audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create evaluator: commit: %w", err)
	}
	return nil
}

// CreateEvaluatorVersion stores the next version of a dashboard evaluator with the admin audit row,
// in one transaction (POST /api/evaluators/:id/versions). The evaluator row is locked, so of two
// writers that start from the same base the second gets a *StaleEvaluatorError (compare and swap
// on latest_version); v.Version must be base+1. It refuses built-in evaluators
// (ErrEvaluatorBuiltin) and file ones (ErrEvaluatorFileManaged); an unknown id is ErrNotFound.
// v.CreatedAt is filled in.
func (s *PostgresStore) CreateEvaluatorVersion(ctx context.Context, channel string, base int, name, category string, v *EvaluatorVersionRecord, audit AdminAuditEntry) error {
	if v.EvaluatorID == "" || v.Version != base+1 || base < 1 || len(v.Spec) == 0 {
		return fmt.Errorf("create evaluator version: invalid version %q@%d on base %d", v.EvaluatorID, v.Version, base)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create evaluator version: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var origin string
	var latest int
	err = tx.QueryRow(ctx, `SELECT origin, latest_version FROM evaluators WHERE id = $1 FOR UPDATE`, v.EvaluatorID).Scan(&origin, &latest)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("create evaluator version: %w", err)
	case origin == EvaluatorOriginBuiltin:
		return ErrEvaluatorBuiltin
	case origin == EvaluatorOriginFile:
		return ErrEvaluatorFileManaged
	case latest != base:
		return &StaleEvaluatorError{ID: v.EvaluatorID, Base: base, Latest: latest}
	}
	if err := insertEvaluatorVersion(ctx, tx, channel, v); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE evaluators SET name = $2, category = $3, latest_version = $4, updated_at = now() WHERE id = $1`,
		v.EvaluatorID, name, category, v.Version); err != nil {
		return fmt.Errorf("create evaluator version: %w", err)
	}
	if err := InsertAdminAudit(ctx, tx, audit); err != nil {
		return fmt.Errorf("create evaluator version: audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create evaluator version: commit: %w", err)
	}
	return nil
}

// SetEvaluatorArchived archives or restores an evaluator with the admin audit row, in one
// transaction (POST /api/evaluators/:id/archive), and notifies "evaluators:<id>". Setting the flag
// it already has changes nothing and writes no audit row. An unknown id is ErrNotFound.
func (s *PostgresStore) SetEvaluatorArchived(ctx context.Context, channel, id string, archived bool, audit AdminAuditEntry) (*EvaluatorRecord, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("archive evaluator: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	cur, err := scanEvaluator(tx.QueryRow(ctx, `SELECT `+evaluatorColumns+` FROM evaluators WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("archive evaluator: %w", err)
	}
	if cur.Archived == archived {
		return cur, nil
	}
	rec, err := scanEvaluator(tx.QueryRow(ctx, `UPDATE evaluators SET archived = $2, updated_at = now() WHERE id = $1
		RETURNING `+evaluatorColumns, id, archived))
	if err != nil {
		return nil, fmt.Errorf("archive evaluator: %w", err)
	}
	if err := InsertAdminAudit(ctx, tx, audit); err != nil {
		return nil, fmt.Errorf("archive evaluator: audit: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, configChannel(channel), "evaluators:"+id); err != nil {
		return nil, fmt.Errorf("notify evaluator: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("archive evaluator: commit: %w", err)
	}
	return rec, nil
}
