package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// CredentialRecord is one credentials row (migration 0004): a dashboard credential sealed with
// AES-256-GCM (internal/credentials). The secret is never stored in clear; Masked and Fingerprint
// are what views show. AllowedHosts holds canonical origins (D78; the column keeps its name).
type CredentialRecord struct {
	Name         string
	Provider     string
	Ciphertext   []byte
	Nonce        []byte
	KeyID        string
	Masked       string
	Fingerprint  string
	AllowedHosts []string
	CreatedAt    time.Time
	RotatedAt    *time.Time
	// LastVerifiedAt and LastVerifyStatus are the last check with the provider (nil when never
	// checked); LastUsedAt the last outbound call that used the secret.
	LastVerifiedAt   *time.Time
	LastVerifyStatus *string
	LastUsedAt       *time.Time
}

// Verify statuses a credentials row may record (api.ts CredentialVerifyStatus).
var credentialVerifyStatuses = []string{"ok", "unauthorized", "no_credits", "unreachable", "unknown"}

const credentialColumns = `name, provider, ciphertext, nonce, key_id, masked, fingerprint, allowed_hosts, created_at,
	rotated_at, last_verified_at, last_verify_status, last_used_at` // #nosec G101 -- column names, not a credential

func scanCredential(row pgx.Row, extra ...any) (*CredentialRecord, error) {
	var c CredentialRecord
	dest := append([]any{&c.Name, &c.Provider, &c.Ciphertext, &c.Nonce, &c.KeyID, &c.Masked, &c.Fingerprint, &c.AllowedHosts,
		&c.CreatedAt, &c.RotatedAt, &c.LastVerifiedAt, &c.LastVerifyStatus, &c.LastUsedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	for _, p := range []**time.Time{&c.RotatedAt, &c.LastVerifiedAt, &c.LastUsedAt} {
		if *p != nil {
			t := (*p).UTC()
			*p = &t
		}
	}
	if c.AllowedHosts == nil {
		c.AllowedHosts = []string{}
	}
	return &c, nil
}

// ListCredentials returns every credentials row, by name.
func (s *PostgresStore) ListCredentials(ctx context.Context) ([]CredentialRecord, error) {
	return listCredentials(ctx, s.pool)
}

func listCredentials(ctx context.Context, q querier) ([]CredentialRecord, error) {
	rows, err := q.Query(ctx, `SELECT `+credentialColumns+` FROM credentials ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()
	out := []CredentialRecord{}
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list credentials: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// GetCredential returns the credential called name, or ErrNotFound.
func (s *PostgresStore) GetCredential(ctx context.Context, name string) (*CredentialRecord, error) {
	return scanCredential(s.pool.QueryRow(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE name = $1`, name))
}

// CredentialAudit describes the admin_audit_log rows of a credential write (D44).
type CredentialAudit struct {
	// Action of the credential's own row: "" means credential.put for a new name and
	// credential.rotate when a row is replaced; the OpenRouter connect flow passes
	// credential.connect_openrouter.
	Action string
	// Diff adds fields to the credential row's diff (the verification outcome, the bindings
	// applied). The store always records provider, masked, fingerprint and allowed_hosts.
	Diff map[string]any
	// Settings is the diff of the settings.update row written when the write also patches
	// settings rows; {keys} is added when absent.
	Settings map[string]any
}

// CredentialPutResult is the outcome of PutCredential.
type CredentialPutResult struct {
	// Version is the runtime-config version after the write (D31).
	Version int64
	// Rotated reports that the write replaced an existing row (rotated_at is set).
	Rotated bool
	Record  *CredentialRecord
}

// PutCredential stores rec in one transaction (D31, D75): it upserts the row (replacing one sets
// rotated_at and clears last_used_at), applies settingsPatch (the bindings a verified write
// applies and D76's leave-simulated flip; only RuntimeSettingKeys), bumps the runtime-config
// version, appends the credential's audit row and, when settings were patched, a settings.update
// row, and notifies "credentials:<name>" and "settings:<key>" on channel (ConfigChannel when
// empty). Each patch value that is a JSON object is merged into its stored row: its top-level
// fields replace the stored ones and the other stored fields stay, so a patch holds only the
// fields it changes (runtimecfg.ChangedFields) and a concurrent write to a sibling field is not
// lost. rec.LastVerifiedAt and LastVerifyStatus are stored as given (nil: not checked).
func (s *PostgresStore) PutCredential(ctx context.Context, channel string, rec *CredentialRecord, settingsPatch map[string]json.RawMessage,
	actor string, audit CredentialAudit) (*CredentialPutResult, error) {
	if err := checkCredentialRecord(rec); err != nil {
		return nil, err
	}
	if err := checkSettingsRows(settingsPatch); err != nil {
		return nil, err
	}
	if channel == "" {
		channel = ConfigChannel
	}
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return nil, err
	}
	out := &CredentialPutResult{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The version row is locked first, as in every versioned write, so concurrent writes
		// cannot deadlock on the credential row and the version row in opposite orders.
		v, err := bumpRuntimeVersion(ctx, tx, actor)
		if err != nil {
			return err
		}
		out.Version = v
		var inserted bool
		out.Record, err = scanCredential(tx.QueryRow(ctx, `
			INSERT INTO credentials (name, provider, ciphertext, nonce, key_id, masked, fingerprint, allowed_hosts,
				last_verified_at, last_verify_status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (name) DO UPDATE SET provider = EXCLUDED.provider, ciphertext = EXCLUDED.ciphertext,
				nonce = EXCLUDED.nonce, key_id = EXCLUDED.key_id, masked = EXCLUDED.masked,
				fingerprint = EXCLUDED.fingerprint, allowed_hosts = EXCLUDED.allowed_hosts, rotated_at = now(),
				last_verified_at = EXCLUDED.last_verified_at, last_verify_status = EXCLUDED.last_verify_status,
				last_used_at = NULL
			RETURNING `+credentialColumns+`, (xmax = 0)`,
			rec.Name, rec.Provider, rec.Ciphertext, rec.Nonce, rec.KeyID, rec.Masked, rec.Fingerprint, nonNilStrings(rec.AllowedHosts),
			rec.LastVerifiedAt, rec.LastVerifyStatus), &inserted)
		if err != nil {
			return fmt.Errorf("store: put credential %s: %w", rec.Name, err)
		}
		out.Rotated = !inserted
		var keys []string
		if len(settingsPatch) > 0 {
			if keys, err = writeSettingsRows(ctx, tx, channel, settingsPatch, actor, true); err != nil {
				return err
			}
		}
		action := audit.Action
		if action == "" {
			action = "credential.put"
			if out.Rotated {
				action = "credential.rotate"
			}
		}
		diff := map[string]any{"provider": rec.Provider, "masked": rec.Masked, "fingerprint": rec.Fingerprint,
			"allowed_hosts": nonNilStrings(rec.AllowedHosts)}
		maps.Copy(diff, audit.Diff)
		if err := insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: action, Target: rec.Name, Diff: diff}); err != nil {
			return err
		}
		if len(keys) > 0 {
			sd := maps.Clone(audit.Settings)
			if sd == nil {
				sd = map[string]any{}
			}
			if _, ok := sd["keys"]; !ok {
				sd["keys"] = keys
			}
			if err := insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: "settings.update", Target: rec.Name, Diff: sd}); err != nil {
				return err
			}
		}
		return notifyCredential(ctx, tx, channel, rec.Name)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CredentialDeleteResult is the outcome of DeleteCredential.
type CredentialDeleteResult struct {
	// Version is the runtime-config version after the delete.
	Version int64
	// Record is the deleted row.
	Record *CredentialRecord
	// Unbound lists the settings paths that named the credential and were set to null (D36), in
	// a stable order: bindings.decision_model, bindings.judge, bindings.upstream_default,
	// upstream.default.credential, upstream.anthropic.credential, upstream.fallback.credential.
	Unbound []string
}

// DeleteCredential removes the credential called name in one transaction (D36): it unbinds it
// (every bindings.* and upstream.*.credential naming it becomes null), bumps the runtime-config
// version, appends credential.delete and, when something was unbound, settings.update, and
// notifies "credentials:<name>" and "settings:<key>" on channel (ConfigChannel when empty). An
// unknown name is ErrNotFound and changes nothing.
func (s *PostgresStore) DeleteCredential(ctx context.Context, channel, name, actor string) (*CredentialDeleteResult, error) {
	if channel == "" {
		channel = ConfigChannel
	}
	if err := ensureEpoch(ctx, s.pool); err != nil {
		return nil, err
	}
	out := &CredentialDeleteResult{Unbound: []string{}}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := bumpRuntimeVersion(ctx, tx, actor)
		if err != nil {
			return err
		}
		out.Version = v
		if out.Record, err = scanCredential(tx.QueryRow(ctx, `DELETE FROM credentials WHERE name = $1 RETURNING `+credentialColumns, name)); err != nil {
			return err // ErrNotFound rolls the version bump back
		}
		patch, unbound, err := unbindCredential(ctx, tx, name)
		if err != nil {
			return err
		}
		out.Unbound = unbound
		var keys []string
		if len(patch) > 0 {
			if keys, err = writeSettingsRows(ctx, tx, channel, patch, actor, false); err != nil {
				return err
			}
		}
		if err := insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: "credential.delete", Target: name,
			Diff: map[string]any{"provider": out.Record.Provider, "fingerprint": out.Record.Fingerprint, "unbound": unbound}}); err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: "settings.update", Target: name,
				Diff: map[string]any{"keys": keys, "unbound": unbound, "reason": "credential.delete"}}); err != nil {
				return err
			}
		}
		return notifyCredential(ctx, tx, channel, name)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// bindingPaths are the settings fields that name a credential, by row (D36).
var bindingPaths = []struct{ row, object, field, path string }{
	{"bindings", "", "decision_model", "bindings.decision_model"},
	{"bindings", "", "judge", "bindings.judge"},
	{"bindings", "", "upstream_default", "bindings.upstream_default"},
	{"upstream", "default", "credential", "upstream.default.credential"},
	{"upstream", "anthropic", "credential", "upstream.anthropic.credential"},
	{"upstream", "fallback", "credential", "upstream.fallback.credential"},
}

// unbindCredential reads the bindings and upstream rows FOR UPDATE and returns the rows with every
// field naming name set to null (other fields kept as stored) and the paths it changed.
func unbindCredential(ctx context.Context, tx pgx.Tx, name string) (map[string]json.RawMessage, []string, error) {
	rows, err := tx.Query(ctx, `SELECT key, value FROM settings WHERE key IN ('bindings', 'upstream') FOR UPDATE`)
	if err != nil {
		return nil, nil, fmt.Errorf("store: read bindings: %w", err)
	}
	stored := map[string]map[string]json.RawMessage{}
	for rows.Next() {
		var (
			k string
			v []byte
		)
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("store: read bindings: %w", err)
		}
		m := map[string]json.RawMessage{}
		if err := json.Unmarshal(v, &m); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("store: settings row %s is not an object: %w", k, err)
		}
		stored[k] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	want, err := json.Marshal(name)
	if err != nil {
		return nil, nil, err
	}
	isName := func(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), want) }
	out := map[string]json.RawMessage{}
	unbound := []string{}
	for _, p := range bindingPaths {
		row, ok := stored[p.row]
		if !ok {
			continue
		}
		if p.object == "" {
			if !isName(row[p.field]) {
				continue
			}
			row[p.field] = json.RawMessage("null")
		} else {
			obj := map[string]json.RawMessage{}
			if raw, ok := row[p.object]; !ok || json.Unmarshal(raw, &obj) != nil || !isName(obj[p.field]) {
				continue
			}
			obj[p.field] = json.RawMessage("null")
			b, err := json.Marshal(obj)
			if err != nil {
				return nil, nil, err
			}
			row[p.object] = b
		}
		b, err := json.Marshal(row)
		if err != nil {
			return nil, nil, err
		}
		out[p.row] = b
		unbound = append(unbound, p.path)
	}
	return out, unbound, nil
}

// MarkCredentialVerified records the outcome of checking the credential with its provider and
// appends credential.verify, in one transaction. It is not a runtime-config change: the version
// does not move and nothing is notified. An unknown name is ErrNotFound.
func (s *PostgresStore) MarkCredentialVerified(ctx context.Context, name, status string, at time.Time, actor string, diff map[string]any) error {
	if !slices.Contains(credentialVerifyStatuses, status) {
		return fmt.Errorf("store: %q is not a verify status", status)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE credentials SET last_verified_at = $2, last_verify_status = $3 WHERE name = $1`, name, at, status)
		if err != nil {
			return fmt.Errorf("store: mark credential %s verified: %w", name, err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		d := maps.Clone(diff)
		if d == nil {
			d = map[string]any{}
		}
		d["status"] = status
		return insertAdminAudit(ctx, tx, AdminAuditEntry{Actor: actor, Action: "credential.verify", Target: name, Diff: d})
	})
}

// TouchCredential records that the credential was used at at (last_used_at only moves forward).
func (s *PostgresStore) TouchCredential(ctx context.Context, name string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE credentials SET last_used_at = $2 WHERE name = $1 AND (last_used_at IS NULL OR last_used_at < $2)`, name, at)
	return err
}

func checkCredentialRecord(rec *CredentialRecord) error {
	switch {
	case rec == nil || rec.Name == "" || rec.Provider == "":
		return errors.New("store: a credential needs a name and a provider")
	case len(rec.Ciphertext) == 0 || len(rec.Nonce) == 0 || rec.KeyID == "":
		return errors.New("store: a credential is stored sealed: ciphertext, nonce and key id are required")
	case rec.LastVerifyStatus != nil && !slices.Contains(credentialVerifyStatuses, *rec.LastVerifyStatus):
		return fmt.Errorf("store: %q is not a verify status", *rec.LastVerifyStatus)
	}
	return nil
}

func notifyCredential(ctx context.Context, tx pgx.Tx, channel, name string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, notifyCredentialsPrefix+name); err != nil {
		return fmt.Errorf("store: notify credential %s: %w", name, err)
	}
	return nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
