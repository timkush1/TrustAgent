package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

// noPatternSecret matches no redaction pattern.
const noPatternSecret = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"

// runtimePG opens a Postgres store on a schema of its own (TEST_DATABASE_URL).
func runtimePG(t *testing.T) *store.PostgresStore {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg
}

// durableMaster is an env-sourced master key, so the credential store is ready (D32).
func durableMaster(t *testing.T, b byte) *authz.MasterKey {
	t.Helper()
	m, err := authz.LoadMasterKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, authz.MasterKeySize)), "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// putSealed stores secret as the credential name, sealed under mk, with settings rows.
func putSealed(t *testing.T, pg *store.PostgresStore, mk *authz.MasterKey, name, secret string, rows map[string]json.RawMessage, origins ...string) {
	t.Helper()
	c, err := credentials.New(mk)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := credentials.Lookup(name)
	ct, nonce, keyID, err := c.Seal(name, spec.Provider, origins, secret)
	if err != nil {
		t.Fatal(err)
	}
	rec := &store.CredentialRecord{Name: name, Provider: spec.Provider, Ciphertext: ct, Nonce: nonce, KeyID: keyID,
		Masked: credentials.Masked(secret), Fingerprint: c.Fingerprint(secret), AllowedHosts: origins}
	if _, err := pg.PutCredential(context.Background(), "", rec, rows, "admin:test", store.CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
}

// waitRuntime polls the manager until ok accepts the snapshot.
func waitRuntime(t *testing.T, m *runtimecfg.Manager, what string, ok func(*runtimecfg.Snapshot) bool) *runtimecfg.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := m.Current()
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: snapshot v%d never matched", what, s.Version)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRuntimeSettingsLoadAtBootAndReloadOnNotify: the gateway starts with the stored settings and
// credentials, and another replica's write reaches it through NOTIFY and the hub (D84).
func TestRuntimeSettingsLoadAtBootAndReloadOnNotify(t *testing.T) {
	pg := runtimePG(t)
	mk := durableMaster(t, 21)
	putSealed(t, pg, mk, credentials.NameCompatible, noPatternSecret,
		map[string]json.RawMessage{"judge": json.RawMessage(`{"provider":"mock"}`)}, "https://gw.example.com")

	var logs bytes.Buffer
	env := newTestEnv(t, envOpts{pg: pg, master: mk, logs: &logs, mutate: func(c *config.Config) {
		c.PolicyReloadInterval = 0 // the notification alone must deliver the change
	}})
	m := env.srv.rt.manager
	s := m.Current()
	if s.StoreState != credentials.StateReady || s.Settings.Judge.Provider != "mock" || s.Version < 2 {
		t.Fatalf("boot snapshot: state %s judge %s v%d", s.StoreState, s.Settings.Judge.Provider, s.Version)
	}
	c, ok := s.Credentials[credentials.NameCompatible]
	if !ok || c.Source != credentials.SourceDashboard || c.Secret != noPatternSecret {
		t.Fatalf("the stored credential is loaded: %v", ok)
	}
	// The decrypted secret joined the process redactor (D42).
	env.srv.log.Info("echo", "value", "prefix-"+noPatternSecret)
	if strings.Contains(env.logText(), noPatternSecret) {
		t.Error("the decrypted secret reached the log")
	}

	// A write from elsewhere (the store directly, as another replica would) is heard.
	before := s.Version
	if _, err := pg.PatchSettings(context.Background(), "", before, map[string]json.RawMessage{
		"budgets": json.RawMessage(`{"decision_usd_day":3.5}`),
	}, "admin:other-replica", nil); err != nil {
		t.Fatal(err)
	}
	s = waitRuntime(t, m, "after NOTIFY", func(s *runtimecfg.Snapshot) bool { return s.Version > before })
	if s.Settings.Budgets.DecisionUSDDay != 3.5 {
		t.Errorf("budgets %+v", s.Settings.Budgets)
	}
	// A credential delete unbinds and reloads the same way.
	if _, err := pg.DeleteCredential(context.Background(), "", credentials.NameCompatible, "admin:other-replica"); err != nil {
		t.Fatal(err)
	}
	waitRuntime(t, m, "after the delete", func(s *runtimecfg.Snapshot) bool {
		_, still := s.Credentials[credentials.NameCompatible]
		return !still
	})
}

// TestRuntimeSettingsPollAndUnavailableStore: the poll alone reloads; an ephemeral master leaves
// stored credentials unusable and says so (D32).
func TestRuntimeSettingsPollAndUnavailableStore(t *testing.T) {
	pg := runtimePG(t)
	putSealed(t, pg, durableMaster(t, 22), credentials.NameOpenRouter, "sk-or-v1-e2e-0123456789", nil)
	env := newTestEnv(t, envOpts{pg: pg, mutate: func(c *config.Config) { c.PolicyReloadInterval = 50 * time.Millisecond }})
	m := env.srv.rt.manager
	s := m.Current()
	if s.StoreState != credentials.StateUnavailable || len(s.Undecryptable) != 1 || len(s.Warnings) != 1 ||
		s.Warnings[0].Code != runtimecfg.WarnStoreUnavailable {
		t.Fatalf("ephemeral master: state %s undecryptable %v warnings %+v", s.StoreState, s.Undecryptable, s.Warnings)
	}
	if _, ok := s.Credentials[credentials.NameOpenRouter]; ok {
		t.Error("an unopenable row is never used")
	}
	before := s.Version
	if _, err := pg.PatchSettings(context.Background(), "", before, map[string]json.RawMessage{
		"decision_model": json.RawMessage(`{"backend":"mock"}`),
	}, "admin:test", nil); err != nil {
		t.Fatal(err)
	}
	s = waitRuntime(t, m, "after the poll", func(s *runtimecfg.Snapshot) bool { return s.Version > before })
	if s.Settings.DecisionModel.Backend != "mock" {
		t.Errorf("decision model %+v", s.Settings.DecisionModel)
	}
}

// TestRuntimeSettingsWithoutDatabase: no store, env only, version 0 (D31).
func TestRuntimeSettingsWithoutDatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{noStore: true})
	s := env.srv.rt.manager.Current()
	if s.Version != 0 || s.StoreState != credentials.StateNoDatabase || len(s.Epoch) != 36 || env.srv.rt.store != nil {
		t.Errorf("no database: v%d %s %q", s.Version, s.StoreState, s.Epoch)
	}
}
