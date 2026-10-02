package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSettingsEpochAndVersion(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	epoch, v, err := s.RuntimeEpoch(ctx)
	if err != nil || len(epoch) != 36 || v != 1 {
		t.Fatalf("first epoch %q v%d %v", epoch, v, err)
	}
	if e2, v2, _ := s.RuntimeEpoch(ctx); e2 != epoch || v2 != v {
		t.Errorf("the epoch row is inserted once: %q v%d", e2, v2)
	}
	st, err := s.Settings(ctx)
	if err != nil || st.Epoch != epoch || st.Version != 1 || len(st.Rows) != 0 {
		t.Fatalf("settings %+v %v", st, err)
	}
}

func TestSettingsPatchAndStaleVersion(t *testing.T) {
	s, schema := newTestStore(t)
	ctx := context.Background()
	channel := ScopedConfigChannel(schema.Name)
	next := listenPayloads(t, s, channel)
	_, v, err := s.RuntimeEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]json.RawMessage{
		"budgets":        json.RawMessage(`{"decision_usd_day":2.5}`),
		"decision_model": json.RawMessage(`{"backend":"mock"}`),
	}
	v2, err := s.PatchSettings(ctx, channel, v, rows, "admin:root", map[string]any{"changes": map[string]any{"decision_model.backend": "mock"}})
	if err != nil || v2 != v+1 {
		t.Fatalf("patch v%d %v", v2, err)
	}
	if got := next(2); !slices.Equal(got, []string{"settings:budgets", "settings:decision_model"}) {
		t.Errorf("notifications %v", got)
	}
	// The same If-Match again is stale, and changes nothing.
	if _, err := s.PatchSettings(ctx, channel, v, map[string]json.RawMessage{"budgets": json.RawMessage(`{"decision_usd_day":9}`)}, "admin:root", nil); !errors.Is(err, ErrStaleVersion) {
		t.Errorf("stale patch: %v", err)
	}
	st, err := s.Settings(ctx)
	if err != nil || st.Version != v2 || string(st.Rows["budgets"]) != `{"decision_usd_day": 2.5}` || string(st.Rows["decision_model"]) != `{"backend": "mock"}` {
		t.Fatalf("settings after patch %+v %v (budgets %s)", st, err, st.Rows["budgets"])
	}
	// A row is replaced whole.
	if _, err := s.PatchSettings(ctx, channel, v2, map[string]json.RawMessage{"budgets": json.RawMessage(`{"llm_usd_day":1}`)}, "admin:root", nil); err != nil {
		t.Fatal(err)
	}
	next(1)
	if st, _ := s.Settings(ctx); string(st.Rows["budgets"]) != `{"llm_usd_day": 1}` {
		t.Errorf("budgets row %s", st.Rows["budgets"])
	}
	for name, bad := range map[string]map[string]json.RawMessage{
		"empty":           {},
		"setup row":       {"setup": json.RawMessage(`{}`)},
		"session epoch":   {"session_epoch": json.RawMessage(`9`)},
		"epoch row":       {"epoch": json.RawMessage(`{"epoch":"x"}`)},
		"invalid JSON":    {"budgets": json.RawMessage(`{`)},
		"unknown setting": {"theme": json.RawMessage(`"dark"`)},
	} {
		if _, err := s.PatchSettings(ctx, channel, v2+1, bad, "admin:root", nil); err == nil || errors.Is(err, ErrStaleVersion) {
			t.Errorf("%s: %v", name, err)
		}
	}
	d := auditDiffs(t, s, "settings.update")
	if len(d) != 2 || d[0]["changes"] == nil || !slices.Equal(anyStrings(d[0]["keys"]), []string{"budgets", "decision_model"}) {
		t.Errorf("settings.update diffs %v", d)
	}
}

// TestSettingsVersionBumpScope: only runtime settings and credentials bump the version (D31); the
// setup token and the session epoch do not.
func TestSettingsVersionBumpScope(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	v := settingsVersion(t, s)
	now := time.Now().UTC()
	if err := s.PutSetupToken(ctx, SetupTokenRecord{SHA256: "h1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, "startup"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BumpSessionEpoch(ctx, "admin:root"); err != nil {
		t.Fatal(err)
	}
	if got := settingsVersion(t, s); got != v {
		t.Errorf("setup and session writes moved the version %d → %d", v, got)
	}
	if _, err := s.PatchSettings(ctx, "", v, map[string]json.RawMessage{"judge": json.RawMessage(`{"provider":"mock"}`)}, "admin:root", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCredential(ctx, "", sealedRecord("typesafe", "typesafe"), nil, "admin:root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCredential(ctx, "", "typesafe", "admin:root"); err != nil {
		t.Fatal(err)
	}
	if got := settingsVersion(t, s); got != v+3 {
		t.Errorf("version %d after a patch, a put and a delete from %d", got, v)
	}
}

// TestSettingsRuntimeStateIsOneSnapshot reads the version, the rows and the credentials together.
func TestSettingsRuntimeStateIsOneSnapshot(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.PutCredential(ctx, "", sealedRecord("openrouter", "openrouter"), map[string]json.RawMessage{"bindings": json.RawMessage(`{"judge":"openrouter"}`)}, "root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	rows, creds, err := s.RuntimeState(ctx)
	if err != nil || rows.Version != 2 || len(creds) != 1 || creds[0].Name != "openrouter" || string(rows.Rows["bindings"]) != `{"judge": "openrouter"}` {
		t.Fatalf("runtime state %+v %+v %v", rows, creds, err)
	}
}

func TestSetupTokenMintIsAudited(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.PutSetupToken(ctx, SetupTokenRecord{SHA256: "secret-hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Forced: true}, "cli"); err != nil {
		t.Fatal(err)
	}
	if got := adminActions(t, s); !slices.Equal(got, []string{"setup_token.mint::cli"}) {
		t.Fatalf("audit %v", got)
	}
	d := auditDiffs(t, s, "setup_token.mint")[0]
	if d["forced"] != true || d["expires_at"] == nil || d["sha256"] != nil {
		t.Errorf("mint diff %v", d)
	}
}

func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := []string{}
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
