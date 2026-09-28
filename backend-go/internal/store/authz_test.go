package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func adminActions(t *testing.T, s *PostgresStore) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT action || ':' || coalesce(target, '') || ':' || actor FROM admin_audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestAPIKeysRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	k1 := &APIKeyRecord{ID: "k1", Label: "ci", Scopes: []string{"ingest"}, SHA256: "aa", Prefix: "ta_abc", CreatedBy: "root"}
	k2 := &APIKeyRecord{ID: "k2", Label: "dash", Scopes: []string{"read", "admin"}, SHA256: "bb", Prefix: "ta_def", CreatedBy: "root"}
	for _, k := range []*APIKeyRecord{k1, k2} {
		if err := s.CreateAPIKey(ctx, k, "root"); err != nil {
			t.Fatal(err)
		}
		if k.CreatedAt.IsZero() {
			t.Error("CreatedAt not filled")
		}
	}
	if err := s.CreateAPIKey(ctx, &APIKeyRecord{ID: "k3", Label: "dup", Scopes: []string{"read"}, SHA256: "aa", Prefix: "x", CreatedBy: "r"}, "r"); err == nil {
		t.Error("a duplicate sha256 is refused")
	}
	if err := s.CreateAPIKey(ctx, &APIKeyRecord{ID: "k4", Label: "bad", Scopes: []string{"write"}, SHA256: "cc", Prefix: "x", CreatedBy: "r"}, "r"); err == nil {
		t.Error("an unknown scope is refused by the 0004 CHECK")
	}
	if err := s.CreateAPIKey(ctx, &APIKeyRecord{ID: "k5", Label: "none", SHA256: "dd", CreatedBy: "r"}, "r"); err == nil {
		t.Error("a key without scopes is refused")
	}
	got, err := s.APIKeyBySHA256(ctx, "bb")
	if err != nil || got.ID != "k2" || !reflect.DeepEqual(got.Scopes, []string{"read", "admin"}) || got.LastUsedAt != nil || got.RevokedAt != nil || !got.Active() {
		t.Fatalf("by sha %+v %v", got, err)
	}
	if _, err := s.APIKeyBySHA256(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sha: %v", err)
	}
	if got, err := s.APIKeyByID(ctx, "k1"); err != nil || got.Label != "ci" {
		t.Errorf("by id %+v %v", got, err)
	}
	if _, err := s.APIKeyByID(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	list, err := s.ListAPIKeys(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "k2" {
		t.Errorf("list %+v %v", list, err)
	}
	t1 := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := s.TouchAPIKey(ctx, "k1", t1); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchAPIKey(ctx, "k1", t1.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.APIKeyByID(ctx, "k1"); got.LastUsedAt == nil || !got.LastUsedAt.Equal(t1) {
		t.Errorf("last_used_at only moves forward: %v", got.LastUsedAt)
	}
	if ok, err := s.AdminKeyExists(ctx); err != nil || !ok {
		t.Errorf("admin exists %v %v", ok, err)
	}
	rec, revoked, err := s.RevokeAPIKey(ctx, "k2", "root")
	if err != nil || !revoked || rec.RevokedAt == nil || rec.Active() {
		t.Fatalf("revoke %+v %v %v", rec, revoked, err)
	}
	if _, revoked, err := s.RevokeAPIKey(ctx, "k2", "root"); err != nil || revoked {
		t.Errorf("second revoke: %v %v", revoked, err)
	}
	if _, _, err := s.RevokeAPIKey(ctx, "nope", "root"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke unknown: %v", err)
	}
	if ok, _ := s.AdminKeyExists(ctx); ok {
		t.Error("a revoked admin key does not count")
	}
	if err := s.AppendAdminAudit(ctx, AdminAuditEntry{Actor: "root", Action: "settings.update", Diff: map[string]int{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"api_key.create:k1:root", "api_key.create:k2:root", "api_key.revoke:k2:root", "settings.update::root"}
	if got := adminActions(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("audit log %v, want %v", got, want)
	}
}

func TestSetupTokenClaimOutcomes(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	admin := func(id string) *APIKeyRecord {
		return &APIKeyRecord{ID: id, Label: "setup-admin", Scopes: []string{"admin"}, SHA256: "sha-" + id, Prefix: "ta_x", CreatedBy: "setup"}
	}
	if _, err := s.SetupToken(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("no token yet: %v", err)
	}
	if err := s.ClaimSetupToken(ctx, "h1", now, false, admin("a0")); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Errorf("claim without a token: %v", err)
	}
	put := func(sha string, exp time.Time, forced bool) {
		t.Helper()
		if err := s.PutSetupToken(ctx, SetupTokenRecord{SHA256: sha, CreatedAt: now, ExpiresAt: exp, Forced: forced}, "startup"); err != nil {
			t.Fatal(err)
		}
	}
	put("h1", now.Add(time.Hour), false)
	if err := s.ClaimSetupToken(ctx, "other", now, false, admin("a1")); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Errorf("wrong hash: %v", err)
	}
	if err := s.ClaimSetupToken(ctx, "h1", now, true, admin("a1")); !errors.Is(err, ErrSetupComplete) {
		t.Errorf("env admins complete setup: %v", err)
	}
	if err := s.ClaimSetupToken(ctx, "h1", now.Add(time.Hour), false, admin("a1")); !errors.Is(err, ErrSetupTokenExpired) {
		t.Errorf("at expiry: %v", err)
	}
	if err := s.ClaimSetupToken(ctx, "h1", now, false, admin("a1")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec, err := s.SetupToken(ctx)
	if err != nil || rec.ClaimedAt == nil || rec.ClaimedKeyID != "a1" || rec.SHA256 != "h1" {
		t.Errorf("claimed record %+v %v", rec, err)
	}
	if k, err := s.APIKeyByID(ctx, "a1"); err != nil || k.Label != "setup-admin" {
		t.Errorf("setup admin key %+v %v", k, err)
	}
	if err := s.ClaimSetupToken(ctx, "h1", now, false, admin("a2")); !errors.Is(err, ErrSetupTokenUsed) {
		t.Errorf("second claim: %v", err)
	}
	if _, err := s.APIKeyByID(ctx, "a2"); !errors.Is(err, ErrNotFound) {
		t.Error("a failed claim creates no key")
	}

	put("h2", now.Add(time.Hour), false)
	if err := s.ClaimSetupToken(ctx, "h2", now, false, admin("a3")); !errors.Is(err, ErrSetupComplete) {
		t.Errorf("a plain token while an admin key exists: %v", err)
	}
	put("h3", now.Add(time.Hour), true)
	if rec, _ := s.SetupToken(ctx); rec.ClaimedAt != nil || !rec.Forced {
		t.Errorf("a new token resets the claim: %+v", rec)
	}
	if err := s.ClaimSetupToken(ctx, "h3", now, true, admin("a4")); err != nil {
		t.Errorf("a forced token works despite admins: %v", err)
	}
	want := []string{"setup.claim:a1:setup", "setup.claim:a4:setup"}
	if got := adminActions(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("audit %v", got)
	}
	if err := s.PutSetupToken(ctx, SetupTokenRecord{}, "x"); err == nil {
		t.Error("a token without a hash is refused")
	}
}

func TestSessionEpochBump(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if ep, err := s.SessionEpoch(ctx); err != nil || ep != 0 {
		t.Fatalf("initial epoch %d %v", ep, err)
	}
	for want := int64(1); want <= 3; want++ {
		ep, err := s.BumpSessionEpoch(ctx, "root")
		if err != nil || ep != want {
			t.Fatalf("bump %d %v, want %d", ep, err, want)
		}
		if got, _ := s.SessionEpoch(ctx); got != want {
			t.Errorf("read back %d", got)
		}
	}
	if got := adminActions(t, s); len(got) != 3 || got[0] != "session.revoke_all::root" {
		t.Errorf("audit %v", got)
	}
}
