package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/config"
)

// listenPayloads subscribes to channel and returns a function that waits for the next n payloads.
func listenPayloads(t *testing.T, s *PostgresStore, channel string) func(n int) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 64)
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Listen(ctx, channel, func() { close(ready) }, func(p string) { got <- p })
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("LISTEN did not start")
	}
	return func(n int) []string {
		t.Helper()
		out := []string{}
		for len(out) < n {
			select {
			case p := <-got:
				out = append(out, p)
			case <-time.After(5 * time.Second):
				t.Fatalf("got notifications %v, want %d", out, n)
			}
		}
		select {
		case p := <-got:
			t.Errorf("unexpected extra notification %q after %v", p, out)
		case <-time.After(100 * time.Millisecond):
		}
		return out
	}
}

func sealedRecord(name, provider string, hosts ...string) *CredentialRecord {
	return &CredentialRecord{Name: name, Provider: provider, Ciphertext: []byte("ciphertext-" + name), Nonce: []byte("123456789012"),
		KeyID: "0123456789abcdef", Masked: "sk-…" + name[:4], Fingerprint: "fp_0123456789ab", AllowedHosts: hosts}
}

func settingsVersion(t *testing.T, s *PostgresStore) int64 {
	t.Helper()
	_, v, err := s.RuntimeEpoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func auditDiffs(t *testing.T, s *PostgresStore, action string) []map[string]any {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT coalesce(diff, 'null'::jsonb) FROM admin_audit_log WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestCredentialPutRotateDeleteWithUnbind(t *testing.T) {
	s, schema := newTestStore(t)
	ctx := context.Background()
	channel := ScopedConfigChannel(schema.Name)
	next := listenPayloads(t, s, channel)

	if _, err := s.GetCredential(ctx, "openrouter"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no credential yet: %v", err)
	}
	v0 := settingsVersion(t, s)

	// A first put with bindings (a verified write that binds, D75/D76).
	ok := "ok"
	verified := time.Now().UTC().Truncate(time.Second)
	rec := sealedRecord("openrouter", "openrouter")
	rec.LastVerifiedAt, rec.LastVerifyStatus = &verified, &ok
	patch := map[string]json.RawMessage{
		"bindings":       json.RawMessage(`{"decision_model":"openrouter","judge":"openrouter"}`),
		"decision_model": json.RawMessage(`{"backend":"auto"}`),
	}
	res, err := s.PutCredential(ctx, channel, rec, patch, "admin:root", CredentialAudit{Diff: map[string]any{"bind": []string{"decision_model", "judge"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rotated || res.Version != v0+1 || res.Record.RotatedAt != nil || res.Record.LastVerifyStatus == nil || *res.Record.LastVerifyStatus != "ok" {
		t.Fatalf("first put %+v", res)
	}
	if got := next(3); !slices.Equal(got, []string{"settings:bindings", "settings:decision_model", "credentials:openrouter"}) {
		t.Errorf("notifications %v", got)
	}

	// Rotation: the same name with another secret.
	rec2 := sealedRecord("openrouter", "openrouter")
	rec2.Ciphertext, rec2.Fingerprint = []byte("rotated"), "fp_ba9876543210"
	if err := s.TouchCredential(ctx, "openrouter", time.Now()); err != nil {
		t.Fatal(err)
	}
	res2, err := s.PutCredential(ctx, channel, rec2, nil, "admin:root", CredentialAudit{})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Rotated || res2.Version != v0+2 || res2.Record.RotatedAt == nil || string(res2.Record.Ciphertext) != "rotated" ||
		res2.Record.LastUsedAt != nil || res2.Record.LastVerifyStatus != nil || !res2.Record.CreatedAt.Equal(res.Record.CreatedAt) {
		t.Fatalf("rotation %+v", res2.Record)
	}
	if got := next(1); !slices.Equal(got, []string{"credentials:openrouter"}) {
		t.Errorf("notifications %v", got)
	}

	// An upstream credential bound through settings, plus a partly unrelated upstream row.
	if _, err := s.PutCredential(ctx, channel, sealedRecord("upstream-default", "generic", "https://gw.example.com"), nil, "admin:root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	next(1)
	v := settingsVersion(t, s)
	if _, err := s.PatchSettings(ctx, channel, v, map[string]json.RawMessage{
		"upstream": json.RawMessage(`{"default":{"url":"https://gw.example.com","credential":"upstream-default"},"fallback":{"credential":"openrouter","format":"openai"}}`),
		"bindings": json.RawMessage(`{"decision_model":"openrouter","judge":"openrouter","upstream_default":"upstream-default","extra":1}`),
	}, "admin:root", nil); err != nil {
		t.Fatal(err)
	}
	next(2)

	list, err := s.ListCredentials(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "openrouter" || list[1].Name != "upstream-default" ||
		!slices.Equal(list[1].AllowedHosts, []string{"https://gw.example.com"}) {
		t.Fatalf("list %+v %v", list, err)
	}

	// Delete unbinds every reference to the name (D36), and nothing else.
	vBefore := settingsVersion(t, s)
	del, err := s.DeleteCredential(ctx, channel, "openrouter", "admin:root")
	if err != nil {
		t.Fatal(err)
	}
	wantUnbound := []string{"bindings.decision_model", "bindings.judge", "upstream.fallback.credential"}
	if del.Version != vBefore+1 || del.Record.Name != "openrouter" || !slices.Equal(del.Unbound, wantUnbound) {
		t.Fatalf("delete %+v", del)
	}
	if got := next(3); !slices.Equal(got, []string{"settings:bindings", "settings:upstream", "credentials:openrouter"}) {
		t.Errorf("notifications %v", got)
	}
	st, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bindings map[string]any
	var upstream map[string]map[string]any
	if json.Unmarshal(st.Rows["bindings"], &bindings) != nil || json.Unmarshal(st.Rows["upstream"], &upstream) != nil {
		t.Fatalf("rows %s %s", st.Rows["bindings"], st.Rows["upstream"])
	}
	if bindings["decision_model"] != nil || bindings["judge"] != nil || bindings["upstream_default"] != "upstream-default" || bindings["extra"] != float64(1) {
		t.Errorf("bindings after delete %v", bindings)
	}
	if upstream["fallback"]["credential"] != nil || upstream["fallback"]["format"] != "openai" || upstream["default"]["credential"] != "upstream-default" {
		t.Errorf("upstream after delete %v", upstream)
	}
	if _, err := s.DeleteCredential(ctx, channel, "openrouter", "admin:root"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if got := settingsVersion(t, s); got != del.Version {
		t.Errorf("a failed delete moved the version to %d", got)
	}
	// Deleting an unbound credential unbinds nothing and writes no settings row.
	if _, err := s.PutCredential(ctx, channel, sealedRecord("typesafe", "typesafe"), nil, "admin:root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	next(1)
	del2, err := s.DeleteCredential(ctx, channel, "typesafe", "admin:root")
	if err != nil || len(del2.Unbound) != 0 {
		t.Fatalf("unbound delete %+v %v", del2, err)
	}
	if got := next(1); !slices.Equal(got, []string{"credentials:typesafe"}) {
		t.Errorf("notifications %v", got)
	}

	want := []string{
		"credential.put:openrouter:admin:root", "settings.update:openrouter:admin:root",
		"credential.rotate:openrouter:admin:root",
		"credential.put:upstream-default:admin:root",
		"settings.update::admin:root",
		"credential.delete:openrouter:admin:root", "settings.update:openrouter:admin:root",
		"credential.put:typesafe:admin:root", "credential.delete:typesafe:admin:root",
	}
	if got := adminActions(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("audit\n got %v\nwant %v", got, want)
	}
	put := auditDiffs(t, s, "credential.put")[0]
	if put["fingerprint"] != "fp_0123456789ab" || put["provider"] != "openrouter" || put["masked"] == nil || put["bind"] == nil {
		t.Errorf("credential.put diff %v", put)
	}
	for _, d := range auditDiffs(t, s, "credential.put") {
		for k := range d {
			if strings.Contains(k, "cipher") || strings.Contains(k, "nonce") || strings.Contains(k, "secret") {
				t.Errorf("an audit diff holds %s", k)
			}
		}
	}
}

// TestCredentialPutMergesThePatch: a credential write's settings patch carries only the fields it
// changes; the other stored fields of those rows survive, including ones written after the writer
// read the settings (D75, D76).
func TestCredentialPutMergesThePatch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	v := settingsVersion(t, s)
	if _, err := s.PatchSettings(ctx, "", v, map[string]json.RawMessage{
		"decision_model":    json.RawMessage(`{"backend":"mock","model":"jev-small"}`),
		"bindings":          json.RawMessage(`{"upstream_default":"upstream-default"}`),
		"pricing_overrides": json.RawMessage(`[{"match":"a","provider":"openrouter","input_per_mtok":1,"output_per_mtok":2}]`),
	}, "admin:root", nil); err != nil {
		t.Fatal(err)
	}
	patch := map[string]json.RawMessage{
		"bindings":          json.RawMessage(`{"decision_model":"openrouter","judge":"openrouter"}`),
		"decision_model":    json.RawMessage(`{"backend":"auto"}`),
		"pricing_overrides": json.RawMessage(`[]`),
	}
	if _, err := s.PutCredential(ctx, "", sealedRecord("openrouter", "openrouter"), patch, "admin:root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var dm, bindings map[string]any
	var overrides []any
	if json.Unmarshal(st.Rows["decision_model"], &dm) != nil || json.Unmarshal(st.Rows["bindings"], &bindings) != nil ||
		json.Unmarshal(st.Rows["pricing_overrides"], &overrides) != nil {
		t.Fatalf("rows %s %s %s", st.Rows["decision_model"], st.Rows["bindings"], st.Rows["pricing_overrides"])
	}
	if dm["backend"] != "auto" || dm["model"] != "jev-small" {
		t.Errorf("decision_model %v: the patched field changes and the stored model stays", dm)
	}
	if bindings["decision_model"] != "openrouter" || bindings["judge"] != "openrouter" || bindings["upstream_default"] != "upstream-default" {
		t.Errorf("bindings %v: the new bindings join the stored upstream binding", bindings)
	}
	if len(overrides) != 0 {
		t.Errorf("pricing_overrides %v: an array replaces its row", overrides)
	}
}

func TestCredentialPutIsAtomic(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	v := settingsVersion(t, s)
	// A settings row outside the runtime keys is refused before anything is written.
	if _, err := s.PutCredential(ctx, "", sealedRecord("openrouter", "openrouter"), map[string]json.RawMessage{"setup": json.RawMessage(`{}`)}, "root", CredentialAudit{}); err == nil {
		t.Fatal("a setup row in a credential write is refused")
	}
	// A failure inside the transaction (the name CHECK constraint) rolls the version back.
	if _, err := s.PutCredential(ctx, "", sealedRecord("github", "generic"), map[string]json.RawMessage{"bindings": json.RawMessage(`{"judge":"github"}`)}, "root", CredentialAudit{}); err == nil {
		t.Fatal("an unknown name is refused by the table")
	}
	if got := settingsVersion(t, s); got != v {
		t.Errorf("a failed put moved the version %d → %d", v, got)
	}
	if st, _ := s.Settings(ctx); len(st.Rows) != 0 {
		t.Errorf("a failed put wrote settings %v", st.Rows)
	}
	if got := adminActions(t, s); len(got) != 0 {
		t.Errorf("a failed put logged %v", got)
	}
	for _, bad := range []*CredentialRecord{
		nil, {Name: "openrouter"}, {Name: "openrouter", Provider: "openrouter", Ciphertext: []byte("x")},
		{Name: "openrouter", Provider: "openrouter", Ciphertext: []byte("x"), Nonce: []byte("n"), KeyID: "k", LastVerifyStatus: ptr("great")},
	} {
		if _, err := s.PutCredential(ctx, "", bad, nil, "root", CredentialAudit{}); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestCredentialMarkVerifiedAndTouch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.PutCredential(ctx, "", sealedRecord("openrouter", "openrouter"), nil, "root", CredentialAudit{}); err != nil {
		t.Fatal(err)
	}
	v := settingsVersion(t, s)
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.MarkCredentialVerified(ctx, "openrouter", "no_credits", at, "admin:root", map[string]any{"latency_ms": 120}); err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCredential(ctx, "openrouter")
	if err != nil || c.LastVerifyStatus == nil || *c.LastVerifyStatus != "no_credits" || !c.LastVerifiedAt.Equal(at) {
		t.Fatalf("verified %+v %v", c, err)
	}
	if settingsVersion(t, s) != v {
		t.Error("a verification is not a runtime-config change")
	}
	if d := auditDiffs(t, s, "credential.verify"); len(d) != 1 || d[0]["status"] != "no_credits" || d[0]["latency_ms"] != float64(120) {
		t.Errorf("verify audit %v", d)
	}
	if err := s.MarkCredentialVerified(ctx, "typesafe", "ok", at, "root", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name: %v", err)
	}
	if err := s.MarkCredentialVerified(ctx, "openrouter", "fine", at, "root", nil); err == nil {
		t.Error("an unknown status is refused")
	}
	later := at.Add(time.Minute)
	if err := s.TouchCredential(ctx, "openrouter", later); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchCredential(ctx, "openrouter", at); err != nil { // an older touch never moves it back
		t.Fatal(err)
	}
	if c, _ := s.GetCredential(ctx, "openrouter"); c.LastUsedAt == nil || !c.LastUsedAt.Equal(later) {
		t.Errorf("last_used_at %v, want %v", c.LastUsedAt, later)
	}
}

func TestAdminAuditScrubsSecretsAndURLs(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const noPattern = "cmp0e2eZq7Lw4Rt9Yx2Vb6Nm"
	if config.NewRedactor().Redact(noPattern) != noPattern {
		t.Fatal("the no-pattern secret must not match a redaction pattern")
	}
	t.Cleanup(AddAuditRedactor(config.NewRedactor(noPattern)))
	diff := map[string]any{
		"url":     "https://user:" + noPattern + "@gw.example.com/v1?api_key=" + noPattern,
		"token":   "https://" + noPattern + "@gw.example.com",
		"plain":   "https://gw.example.com/v1",
		"note":    "rotated " + noPattern + " and Bearer sk-or-v1-e2e-0123456789",
		"nested":  []any{map[string]any{"k": "sk-or-v1-e2e-abcdefghij"}},
		"count":   3,
		"enabled": true,
	}
	// Scrubbing never refuses a write (D44).
	if err := s.AppendAdminAudit(ctx, AdminAuditEntry{Actor: "admin:root", Action: "settings.update", Diff: diff}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.pool.QueryRow(ctx, `SELECT diff::text FROM admin_audit_log`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{noPattern, "sk-or-v1-e2e", "api_key"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the stored diff still holds %q", leak)
		}
	}
	got := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "https://user:xxxxx@gw.example.com/v1" || got["token"] != "https://xxxxx@gw.example.com" || got["plain"] != "https://gw.example.com/v1" {
		t.Errorf("URLs %v %v %v", got["url"], got["token"], got["plain"])
	}
	if got["count"] != float64(3) || got["enabled"] != true {
		t.Errorf("non-strings changed: %v", got)
	}
	// InsertAdminAudit, the writers' entry point, scrubs the same way.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertAdminAudit(ctx, tx, AdminAuditEntry{Actor: "a", Action: "kb.upload", Diff: map[string]string{"k": noPattern}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_log WHERE diff::text LIKE '%' || $1 || '%'`, noPattern).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d rows hold the secret (%v)", n, err)
	}
}

func TestIngestKeyExistsAndActiveAdminKeyCount(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	check := func(wantIngest bool, wantAdmins int) {
		t.Helper()
		ok, err := s.IngestKeyExists(ctx)
		if err != nil || ok != wantIngest {
			t.Errorf("IngestKeyExists = %v %v, want %v", ok, err, wantIngest)
		}
		n, err := s.ActiveAdminKeyCount(ctx)
		if err != nil || n != wantAdmins {
			t.Errorf("ActiveAdminKeyCount = %d %v, want %d", n, err, wantAdmins)
		}
	}
	check(false, 0)
	for _, k := range []*APIKeyRecord{
		{ID: "a1", Label: "admin one", Scopes: []string{"admin"}, SHA256: "s1", Prefix: "ta_1", CreatedBy: "setup"},
		{ID: "a2", Label: "admin two", Scopes: []string{"read", "admin"}, SHA256: "s2", Prefix: "ta_2", CreatedBy: "setup"},
		{ID: "r1", Label: "reader", Scopes: []string{"read"}, SHA256: "s3", Prefix: "ta_3", CreatedBy: "setup"},
	} {
		if err := s.CreateAPIKey(ctx, k, "root"); err != nil {
			t.Fatal(err)
		}
	}
	check(false, 2)
	if err := s.CreateAPIKey(ctx, &APIKeyRecord{ID: "i1", Label: "app", Scopes: []string{"ingest"}, SHA256: "s4", Prefix: "ta_4", CreatedBy: "root"}, "root"); err != nil {
		t.Fatal(err)
	}
	check(true, 2)
	for _, id := range []string{"a2", "i1"} {
		if _, _, err := s.RevokeAPIKey(ctx, id, "root"); err != nil {
			t.Fatal(err)
		}
	}
	check(false, 1)
}
