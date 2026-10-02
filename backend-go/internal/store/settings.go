package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The runtime configuration's rows in the settings table (P4-PLAN D31). Writing any of the
// RuntimeSettingKeys, or any credential, bumps the global runtime-config version: the version
// column of the epoch row, whose value is {"epoch":"<uuidv7>"}. The setup and session_epoch rows
// are not runtime configuration and never bump it.
const (
	settingEpoch = "epoch"
	// Settings NOTIFY payloads are "settings:<key>", credential ones "credentials:<name>" (D84).
	notifySettingsPrefix    = "settings:"
	notifyCredentialsPrefix = "credentials:"
)

// RuntimeSettingKeys are the settings rows that hold api.ts RuntimeSettings, one per top-level
// field.
var RuntimeSettingKeys = []string{"decision_model", "judge", "upstream", "bindings", "budgets", "pricing_overrides"}

// ErrStaleVersion: If-Match names a runtime-config version that is no longer current (412
// stale_version).
var ErrStaleVersion = errors.New("store: the settings changed since they were read")

// SettingsRows are the runtime configuration as stored: the global version and epoch (D31) and
// the runtime settings rows by key. A key that was never written is absent.
type SettingsRows struct {
	Version int64
	Epoch   string
	Rows    map[string]json.RawMessage
}

// isRuntimeKey reports one of RuntimeSettingKeys.
func isRuntimeKey(k string) bool { return slices.Contains(RuntimeSettingKeys, k) }

// newEpoch is a UUIDv7 epoch (D31).
func newEpoch() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// ensureEpoch inserts the epoch row once (version 1) when it is absent.
func ensureEpoch(ctx context.Context, q Execer) error {
	_, err := q.Exec(ctx, `
		INSERT INTO settings (key, value, updated_by) VALUES ($1, jsonb_build_object('epoch', $2::text), 'system')
		ON CONFLICT (key) DO NOTHING`, settingEpoch, newEpoch())
	if err != nil {
		return fmt.Errorf("store: settings epoch: %w", err)
	}
	return nil
}

// bumpRuntimeVersion increments the global runtime-config version inside tx and returns it. The
// row lock it takes serializes every versioned write until tx ends.
func bumpRuntimeVersion(ctx context.Context, tx pgx.Tx, actor string) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `
		INSERT INTO settings (key, value, updated_by) VALUES ($1, jsonb_build_object('epoch', $2::text), $3)
		ON CONFLICT (key) DO UPDATE SET version = settings.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING version`, settingEpoch, newEpoch(), actor).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: bump the settings version: %w", err)
	}
	return v, nil
}

// RuntimeEpoch returns the settings epoch and the global runtime-config version, inserting the
// epoch row (version 1) the first time.
func (s *PostgresStore) RuntimeEpoch(ctx context.Context) (epoch string, version int64, err error) {
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return "", 0, err
	}
	err = s.pool.QueryRow(ctx, `SELECT value->>'epoch', version FROM settings WHERE key = $1`, settingEpoch).Scan(&epoch, &version)
	if err != nil {
		return "", 0, fmt.Errorf("store: read the settings epoch: %w", err)
	}
	return epoch, version, nil
}

// Settings returns the stored runtime configuration (D31), inserting the epoch row the first time.
func (s *PostgresStore) Settings(ctx context.Context) (*SettingsRows, error) {
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return nil, err
	}
	return readSettings(ctx, s.pool)
}

// RuntimeState reads the stored runtime configuration and every credential row in one
// repeatable-read snapshot, so the version always matches the rows read with it.
func (s *PostgresStore) RuntimeState(ctx context.Context) (*SettingsRows, []CredentialRecord, error) {
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return nil, nil, err
	}
	var (
		rows  *SettingsRows
		creds []CredentialRecord
	)
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		if rows, err = readSettings(ctx, tx); err != nil {
			return err
		}
		creds, err = listCredentials(ctx, tx)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return rows, creds, nil
}

// querier runs a query: the pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readSettings(ctx context.Context, q querier) (*SettingsRows, error) {
	out := &SettingsRows{Rows: map[string]json.RawMessage{}}
	if err := q.QueryRow(ctx, `SELECT value->>'epoch', version FROM settings WHERE key = $1`, settingEpoch).Scan(&out.Epoch, &out.Version); err != nil {
		return nil, fmt.Errorf("store: read the settings epoch: %w", err)
	}
	rows, err := q.Query(ctx, `SELECT key, value FROM settings WHERE key = ANY($1)`, RuntimeSettingKeys)
	if err != nil {
		return nil, fmt.Errorf("store: read settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			k string
			v []byte
		)
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: read settings: %w", err)
		}
		out.Rows[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// PatchSettings writes runtime settings rows (each value replaces its row) if the global version
// is still ifVersion, else ErrStaleVersion. In one transaction it bumps the version, appends the
// settings.update audit row with diff, and notifies "settings:<key>" for each row on channel
// (ConfigChannel when empty). It returns the new version. Only RuntimeSettingKeys may be written,
// and at least one.
func (s *PostgresStore) PatchSettings(ctx context.Context, channel string, ifVersion int64, rows map[string]json.RawMessage, actor string, diff map[string]any) (int64, error) {
	if len(rows) == 0 {
		return 0, errors.New("store: a settings patch needs at least one row")
	}
	if err := checkSettingsRows(rows); err != nil {
		return 0, err
	}
	if channel == "" {
		channel = ConfigChannel
	}
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return 0, err
	}
	var version int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int64
		if err := tx.QueryRow(ctx, `SELECT version FROM settings WHERE key = $1 FOR UPDATE`, settingEpoch).Scan(&cur); err != nil {
			return fmt.Errorf("store: read the settings version: %w", err)
		}
		if cur != ifVersion {
			return ErrStaleVersion
		}
		keys, err := writeSettingsRows(ctx, tx, channel, rows, actor, false)
		if err != nil {
			return err
		}
		if version, err = bumpRuntimeVersion(ctx, tx, actor); err != nil {
			return err
		}
		d := maps.Clone(diff)
		if d == nil {
			d = map[string]any{}
		}
		if _, ok := d["keys"]; !ok {
			d["keys"] = keys
		}
		return insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: "settings.update", Diff: d})
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// checkSettingsRows refuses rows outside RuntimeSettingKeys and values that are not JSON.
func checkSettingsRows(rows map[string]json.RawMessage) error {
	for k, v := range rows {
		if !isRuntimeKey(k) {
			return fmt.Errorf("store: %q is not a runtime settings row", k)
		}
		if !json.Valid(v) {
			return fmt.Errorf("store: settings row %q is not valid JSON", k)
		}
	}
	return nil
}

// writeSettingsRows upserts rows inside tx (bumping each row's own version) and notifies
// "settings:<key>" for each, in key order. It returns the keys written. With merge, a value that is
// a JSON object is merged into a stored object row (its top-level fields replace the stored ones,
// the other stored fields stay); any other value replaces its row.
func writeSettingsRows(ctx context.Context, tx pgx.Tx, channel string, rows map[string]json.RawMessage, actor string, merge bool) ([]string, error) {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := tx.Exec(ctx, `
			INSERT INTO settings (key, value, updated_by) VALUES ($1, $2::jsonb, $3)
			ON CONFLICT (key) DO UPDATE SET
				value = CASE WHEN $4 AND jsonb_typeof(settings.value) = 'object' AND jsonb_typeof(EXCLUDED.value) = 'object'
					THEN settings.value || EXCLUDED.value ELSE EXCLUDED.value END,
				version = settings.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			k, string(rows[k]), actor, merge); err != nil {
			return nil, fmt.Errorf("store: write settings %s: %w", k, err)
		}
		if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, notifySettingsPrefix+k); err != nil {
			return nil, fmt.Errorf("store: notify settings %s: %w", k, err)
		}
	}
	return keys, nil
}
