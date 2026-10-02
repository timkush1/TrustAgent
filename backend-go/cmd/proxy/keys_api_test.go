package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// auditRow is one admin_audit_log row.
type auditRow struct {
	Actor, Action, Target string
	Diff                  map[string]any
}

// readAudit lists the admin_audit_log rows with action, oldest first.
func readAudit(t *testing.T, schema *storetest.Schema, action string) []auditRow {
	t.Helper()
	rows, err := schema.Pool(t).Query(context.Background(),
		`SELECT actor, action, coalesce(target, ''), diff FROM admin_audit_log WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Actor, &r.Action, &r.Target, &r.Diff); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// pgSchemaEnv is a test server on a Postgres schema of its own, with the schema for SQL checks.
func pgSchemaEnv(t *testing.T, o envOpts) (*testEnv, *storetest.Schema) {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	o.store = pg
	return newTestEnv(t, o), schema
}

// TestKeysAPILastAdminKey (D81): without an env admin key, the last active admin api key cannot be
// revoked (409 conflict, details.reason last_admin_key); with another admin key it can, and an
// env admin key lifts the guard.
func TestKeysAPILastAdminKey(t *testing.T) {
	env, _ := pgSchemaEnv(t, envOpts{mutate: func(c *config.Config) { c.Auth.AdminKeys = nil }})
	ctx := context.Background()
	first, err := env.srv.gateway.auth.CreateKey(ctx, "ops", []authz.Scope{authz.ScopeAdmin}, "test")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := env.srv.gateway.auth.CreateKey(ctx, "viewer", []authz.Scope{authz.ScopeRead}, "test")
	if err != nil {
		t.Fatal(err)
	}
	admin := first.Secret
	// A key without the admin scope is never the last admin key.
	if resp, body := env.request(http.MethodDelete, "/api/keys/"+reader.Record.ID, admin, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke the reader key: %d %s", resp.StatusCode, body)
	}
	resp, body := env.request(http.MethodDelete, "/api/keys/"+first.Record.ID, admin, nil)
	e := apiErr(t, body)
	if resp.StatusCode != http.StatusConflict || e.Code != codeConflict || e.Details["reason"] != "last_admin_key" {
		t.Fatalf("the last admin key: %d %s", resp.StatusCode, body)
	}
	second, err := env.srv.gateway.auth.CreateKey(ctx, "ops-2", []authz.Scope{authz.ScopeAdmin}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := env.request(http.MethodDelete, "/api/keys/"+first.Record.ID, second.Secret, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("with another admin key: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.request(http.MethodDelete, "/api/keys/"+second.Record.ID, second.Secret, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("now ops-2 is the last: %d %s", resp.StatusCode, body)
	}

	// An env admin key keeps Settings reachable, so the last api_keys admin may go.
	withEnv, _ := pgSchemaEnv(t, envOpts{adminKey: settingsAdminKey})
	only, err := withEnv.srv.gateway.auth.CreateKey(ctx, "ops", []authz.Scope{authz.ScopeAdmin}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := withEnv.request(http.MethodDelete, "/api/keys/"+only.Record.ID, settingsAdminKey, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("with an env admin key: %d %s", resp.StatusCode, body)
	}
}

// TestSessionSignInIsAudited (D44): an interactive sign-in with a key writes session.sign_in with
// the caller and its scopes, never the key.
func TestSessionSignInIsAudited(t *testing.T) {
	env, schema := pgSchemaEnv(t, envOpts{adminKey: settingsAdminKey, master: durableMaster(t, 3),
		mutate: func(c *config.Config) { c.PublicURL = dashOrigin }})
	body, _ := json.Marshal(map[string]string{"key": settingsAdminKey})
	if resp, b := env.request(http.MethodPost, "/api/session", "", body, csrf...); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %d %s", resp.StatusCode, b)
	}
	rows := readAudit(t, schema, "session.sign_in")
	if len(rows) != 1 || rows[0].Actor != "admin" || rows[0].Target != "admin" {
		t.Fatalf("session.sign_in rows %+v", rows)
	}
	if scopes, _ := rows[0].Diff["scopes"].([]any); len(scopes) != 4 {
		t.Errorf("scopes %v", rows[0].Diff)
	}
	if raw, _ := json.Marshal(rows); strings.Contains(string(raw), settingsAdminKey) {
		t.Error("the audit row carries the key")
	}
	// A failed sign-in is not a sign-in.
	bad, _ := json.Marshal(map[string]string{"key": "not-a-key"})
	if resp, _ := env.request(http.MethodPost, "/api/session", "", bad, csrf...); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad key: %d", resp.StatusCode)
	}
	if rows := readAudit(t, schema, "session.sign_in"); len(rows) != 1 {
		t.Errorf("rows after a failed sign-in: %d", len(rows))
	}
}

// SessionInfo.key_id names the api_keys row the caller signed in with, so Access marks exactly
// that key "This browser" even when labels repeat; an env key has no row and no key_id.
func TestSessionNamesTheKeyItWasOpenedWith(t *testing.T) {
	env, _ := pgSchemaEnv(t, envOpts{adminKey: settingsAdminKey, master: durableMaster(t, 3),
		mutate: func(c *config.Config) { c.PublicURL = dashOrigin }})
	created, err := env.srv.gateway.auth.CreateKey(context.Background(), "setup-admin", []authz.Scope{authz.ScopeAdmin, authz.ScopeRead}, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, key, wantID string
	}{{"a dashboard key", created.Secret, created.Record.ID}, {"an env key", settingsAdminKey, ""}} {
		body, _ := json.Marshal(map[string]string{"key": c.key})
		resp, b := env.request(http.MethodPost, "/api/session", "", body, csrf...)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: sign in: %d %s", c.name, resp.StatusCode, b)
		}
		var info sessionInfo
		if err := json.Unmarshal(b, &info); err != nil {
			t.Fatal(err)
		}
		if info.KeyID != c.wantID {
			t.Errorf("%s: key_id %q, want %q", c.name, info.KeyID, c.wantID)
		}
	}
}
