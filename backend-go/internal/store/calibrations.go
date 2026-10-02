package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Calibration records (SPEC §2.3 calibrations, §2.11; §2.4 rows 42-46b; D55, D62). A row is
// one calibrated target of one decision-model snapshot: the calibration.v1 entry, where it came
// from (origin file, import or fit) and whether it is the one that applies (active). The partial
// unique index one_active_calibration keeps at most one active row per (snapshot, target_kind,
// target_key).
//
//   - Files are mirrored (origin file, created_by file), one row per (file_sha256, target_key)
//     (index calibrations_file_entry); a mirrored row is never rewritten. A new file row is inserted
//     inactive while an import or fit row is active for its key; otherwise it replaces the active
//     file row, in the same transaction.
//   - Activating a row deactivates the active row of its key in the same transaction; deactivating
//     an import or fit row re-activates the newest file row of its key.
//
// Every write takes one transaction-scoped advisory lock, so the swaps of concurrent writers (two
// replicas mirroring the same file, an activation racing a deactivation) never meet the unique
// index halfway, and notifies "calibrations:<snapshot>" on the config channel (D84).

// Calibration origins (calibrations.origin).
const (
	CalibrationOriginFile   = "file"
	CalibrationOriginImport = "import"
	CalibrationOriginFit    = "fit"
)

// calibrationLockKey is the pg_advisory_xact_lock key of calibration writes.
const calibrationLockKey = 7212031

// maxCalibrationList bounds GET /api/calibration (newest first).
const maxCalibrationList = 500

// CalibrationRecord is one calibrations row. Entry and LabelSource are nil in list results.
type CalibrationRecord struct {
	ID          int64
	CreatedAt   time.Time
	CreatedBy   string
	Origin      string
	Backend     string
	Snapshot    string
	Family      string
	TargetKind  string
	TargetKey   string
	Quality     string
	LabelSource json.RawMessage
	Entry       json.RawMessage
	FileSHA256  *string
	Active      bool
}

// CalibrationFilter selects calibration rows: one snapshot ("" = every snapshot) and, when Active
// is set, only active or only inactive rows.
type CalibrationFilter struct {
	Snapshot string
	Active   *bool
}

const calibrationMetaColumns = `id, created_at, created_by, origin, backend, snapshot, family, target_kind, target_key, quality, file_sha256, active`

func scanCalibration(row pgx.Row, full bool) (*CalibrationRecord, error) {
	var r CalibrationRecord
	dest := []any{&r.ID, &r.CreatedAt, &r.CreatedBy, &r.Origin, &r.Backend, &r.Snapshot, &r.Family, &r.TargetKind, &r.TargetKey,
		&r.Quality, &r.FileSHA256, &r.Active}
	if full {
		dest = append(dest, &r.LabelSource, &r.Entry)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

// calibrationTx runs fn in a transaction holding the calibration write lock.
func (s *PostgresStore) calibrationTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("calibrations: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(calibrationLockKey)); err != nil {
		return fmt.Errorf("calibrations: lock: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("calibrations: commit: %w", err)
	}
	return nil
}

func notifyCalibrations(ctx context.Context, tx pgx.Tx, channel string, snapshots ...string) error {
	seen := map[string]bool{}
	for _, snap := range snapshots {
		if seen[snap] {
			continue
		}
		seen[snap] = true
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, configChannel(channel), "calibrations:"+snap); err != nil {
			return fmt.Errorf("notify calibrations: %w", err)
		}
	}
	return nil
}

// deactivateKey turns off the active row of a key, except keep; it returns the row it turned off
// (0 when none).
func deactivateKey(ctx context.Context, tx pgx.Tx, snapshot, kind, key string, keep int64) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `UPDATE calibrations SET active = false
		WHERE snapshot = $1 AND target_kind = $2 AND target_key = $3 AND active AND id <> $4 RETURNING id`,
		snapshot, kind, key, keep).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func insertCalibration(ctx context.Context, tx pgx.Tx, r *CalibrationRecord) error {
	err := tx.QueryRow(ctx, `
		INSERT INTO calibrations (created_by, origin, backend, snapshot, family, target_kind, target_key, quality, label_source, entry, file_sha256, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, false) RETURNING id, created_at`,
		r.CreatedBy, r.Origin, r.Backend, r.Snapshot, r.Family, r.TargetKind, r.TargetKey, r.Quality,
		jsonOr(r.LabelSource, "{}"), r.Entry, r.FileSHA256).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert calibration %s: %w", r.TargetKey, err)
	}
	r.CreatedAt, r.Active = r.CreatedAt.UTC(), false
	return nil
}

func activateCalibration(ctx context.Context, tx pgx.Tx, r *CalibrationRecord) error {
	if _, err := deactivateKey(ctx, tx, r.Snapshot, r.TargetKind, r.TargetKey, r.ID); err != nil {
		return fmt.Errorf("activate calibration %d: %w", r.ID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE calibrations SET active = true WHERE id = $1`, r.ID); err != nil {
		return fmt.Errorf("activate calibration %d: %w", r.ID, err)
	}
	r.Active = true
	return nil
}

// InsertCalibrations stores imported or fitted calibrations (origin import or fit) with one admin
// audit row, in one transaction. With activate each one becomes the active row of its key (the
// row it replaces is deactivated); otherwise they are stored inactive, to be previewed and
// activated later (D62). ID, CreatedAt and Active are filled in.
func (s *PostgresStore) InsertCalibrations(ctx context.Context, channel string, recs []*CalibrationRecord, activateAll bool, audit AdminAuditEntry) error {
	for _, r := range recs {
		if r.Origin != CalibrationOriginImport && r.Origin != CalibrationOriginFit {
			return fmt.Errorf("insert calibrations: origin %q (file rows are mirrored, not inserted)", r.Origin)
		}
		if len(r.Entry) == 0 || r.Snapshot == "" || r.TargetKey == "" {
			return errors.New("insert calibrations: a record needs an entry, a snapshot and a target key")
		}
	}
	return s.calibrationTx(ctx, func(tx pgx.Tx) error {
		snaps := make([]string, 0, len(recs))
		for _, r := range recs {
			if err := insertCalibration(ctx, tx, r); err != nil {
				return err
			}
			if activateAll {
				if err := activateCalibration(ctx, tx, r); err != nil {
					return err
				}
			}
			snaps = append(snaps, r.Snapshot)
		}
		if err := InsertAdminAudit(ctx, tx, audit); err != nil {
			return fmt.Errorf("insert calibrations: audit: %w", err)
		}
		return notifyCalibrations(ctx, tx, channel, snaps...)
	})
}

// SetCalibrationActive activates or deactivates a calibration with its admin audit row, in one
// transaction, and reports whether anything changed (setting the state a row already has writes
// nothing). Deactivating an import or fit row re-activates the newest file row of its key. An
// unknown id is ErrNotFound.
func (s *PostgresStore) SetCalibrationActive(ctx context.Context, channel string, id int64, active bool, audit AdminAuditEntry) (*CalibrationRecord, bool, error) {
	var out *CalibrationRecord
	changed := false
	err := s.calibrationTx(ctx, func(tx pgx.Tx) error {
		r, err := scanCalibration(tx.QueryRow(ctx, `SELECT `+calibrationMetaColumns+` FROM calibrations WHERE id = $1`, id), false)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = r
		if r.Active == active {
			return nil
		}
		changed = true
		if active {
			if err := activateCalibration(ctx, tx, r); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE calibrations SET active = false WHERE id = $1`, id); err != nil {
				return fmt.Errorf("deactivate calibration %d: %w", id, err)
			}
			r.Active = false
			if r.Origin != CalibrationOriginFile {
				if _, err := tx.Exec(ctx, `UPDATE calibrations SET active = true WHERE id = (
					SELECT id FROM calibrations WHERE snapshot = $1 AND target_kind = $2 AND target_key = $3 AND origin = 'file'
					ORDER BY created_at DESC, id DESC LIMIT 1)`, r.Snapshot, r.TargetKind, r.TargetKey); err != nil {
					return fmt.Errorf("re-activate the file calibration of %s: %w", r.TargetKey, err)
				}
			}
		}
		if err := InsertAdminAudit(ctx, tx, audit); err != nil {
			return fmt.Errorf("calibration activation: audit: %w", err)
		}
		return notifyCalibrations(ctx, tx, channel, r.Snapshot)
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

// MirrorCalibrationFile mirrors the entries of one calibration file (same file_sha256, backend,
// snapshot and family) into calibrations with origin file (D55) and returns how many rows it
// added. An entry already mirrored from this file content is left as it is. A new row is active
// unless an import or fit row is active for its key; it then replaces the active file row.
func (s *PostgresStore) MirrorCalibrationFile(ctx context.Context, channel string, recs []*CalibrationRecord) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	added := 0
	err := s.calibrationTx(ctx, func(tx pgx.Tx) error {
		added = 0
		for _, r := range recs {
			if r.FileSHA256 == nil || *r.FileSHA256 == "" || len(r.Entry) == 0 {
				return errors.New("mirror calibration file: a record needs the file's sha256 and its entry")
			}
			r.Origin, r.CreatedBy = CalibrationOriginFile, CalibrationOriginFile
			err := tx.QueryRow(ctx, `
				INSERT INTO calibrations (created_by, origin, backend, snapshot, family, target_kind, target_key, quality, label_source, entry, file_sha256, active)
				VALUES ('file', 'file', $1, $2, $3, $4, $5, $6, $7, $8, $9, false)
				ON CONFLICT (file_sha256, target_key) WHERE origin = 'file' DO NOTHING
				RETURNING id, created_at`,
				r.Backend, r.Snapshot, r.Family, r.TargetKind, r.TargetKey, r.Quality, jsonOr(r.LabelSource, "{}"), r.Entry, r.FileSHA256,
			).Scan(&r.ID, &r.CreatedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // mirrored before
			}
			if err != nil {
				return fmt.Errorf("mirror calibration %s: %w", r.TargetKey, err)
			}
			added++
			var origin string
			err = tx.QueryRow(ctx, `SELECT origin FROM calibrations WHERE snapshot = $1 AND target_kind = $2 AND target_key = $3 AND active`,
				r.Snapshot, r.TargetKind, r.TargetKey).Scan(&origin)
			switch {
			case errors.Is(err, pgx.ErrNoRows), err == nil && origin == CalibrationOriginFile:
				if err := activateCalibration(ctx, tx, r); err != nil {
					return err
				}
			case err != nil:
				return fmt.Errorf("mirror calibration %s: %w", r.TargetKey, err)
			}
		}
		if added == 0 {
			return nil
		}
		return notifyCalibrations(ctx, tx, channel, recs[0].Snapshot)
	})
	return added, err
}

// ListCalibrations returns calibration rows without their entries, newest first (at most 500).
func (s *PostgresStore) ListCalibrations(ctx context.Context, f CalibrationFilter) ([]CalibrationRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+calibrationMetaColumns+` FROM calibrations
		WHERE ($1 = '' OR snapshot = $1) AND ($2::BOOLEAN IS NULL OR active = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, f.Snapshot, f.Active, maxCalibrationList)
	if err != nil {
		return nil, fmt.Errorf("list calibrations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CalibrationRecord, error) {
		r, err := scanCalibration(row, false)
		if err != nil {
			return CalibrationRecord{}, err
		}
		return *r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list calibrations: %w", err)
	}
	if out == nil {
		out = []CalibrationRecord{}
	}
	return out, nil
}

// GetCalibration returns one calibration row with its entry; ErrNotFound when there is none.
func (s *PostgresStore) GetCalibration(ctx context.Context, id int64) (*CalibrationRecord, error) {
	return scanCalibration(s.pool.QueryRow(ctx, `SELECT `+calibrationMetaColumns+`, label_source, entry FROM calibrations WHERE id = $1`, id), true)
}

// CalibrationsByID returns the rows with the given ids, with their entries, in id order. Ids that
// do not exist are left out.
func (s *PostgresStore) CalibrationsByID(ctx context.Context, ids []int64) ([]CalibrationRecord, error) {
	return s.fullCalibrations(ctx, `WHERE id = ANY($1) ORDER BY id`, ids)
}

// ActiveCalibrations returns every active row with its entry: the calibrations that apply.
func (s *PostgresStore) ActiveCalibrations(ctx context.Context) ([]CalibrationRecord, error) {
	return s.fullCalibrations(ctx, `WHERE active ORDER BY snapshot, target_kind, target_key`)
}

func (s *PostgresStore) fullCalibrations(ctx context.Context, where string, args ...any) ([]CalibrationRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+calibrationMetaColumns+`, label_source, entry FROM calibrations `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read calibrations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (CalibrationRecord, error) {
		r, err := scanCalibration(row, true)
		if err != nil {
			return CalibrationRecord{}, err
		}
		return *r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read calibrations: %w", err)
	}
	if out == nil {
		out = []CalibrationRecord{}
	}
	return out, nil
}
