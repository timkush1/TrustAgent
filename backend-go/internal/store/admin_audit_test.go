package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/truthtable/backend-go/internal/storetest"
)

func TestListAdminAuditKeysetPages(t *testing.T) {
	ctx := context.Background()
	schema := storetest.New(t)
	s, err := NewPostgresStore(ctx, schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	empty, next, err := s.ListAdminAudit(ctx, 10, "")
	if err != nil || len(empty) != 0 || next != "" || empty == nil {
		t.Fatalf("empty log: %v %q %v (want a non-nil empty page)", empty, next, err)
	}
	for i, e := range []AdminAuditEntry{
		{Actor: "setup", Action: "setup.claim"},
		{Actor: "a", Action: "credential.put", Target: "openrouter", Diff: map[string]any{"fingerprint": "fp_0123456789ab"}},
		{Actor: "a", Action: "credential.delete", Target: "openrouter"},
		{Actor: "a", Action: "settings.update", Target: "budgets", Diff: map[string]any{"budgets.llm_usd_day": map[string]any{"from": 5, "to": 3}}},
		{Actor: "b", Action: "api_key.revoke", Target: "ci"},
	} {
		if err := s.AppendAdminAudit(ctx, e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	var actions []string
	var ids []int64
	cursor := ""
	pages := 0
	for {
		rows, next, err := s.ListAdminAudit(ctx, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range rows {
			actions = append(actions, r.Action)
			ids = append(ids, r.ID)
			if r.At.IsZero() || r.At.Location().String() != "UTC" {
				t.Errorf("row %d at %v, want a UTC timestamp", r.ID, r.At)
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	want := []string{"api_key.revoke", "settings.update", "credential.delete", "credential.put", "setup.claim"}
	if pages != 3 || len(actions) != len(want) {
		t.Fatalf("%d pages, actions %v; want 3 pages of %v", pages, actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions %v, want newest first %v", actions, want)
		}
		if i > 0 && ids[i] >= ids[i-1] {
			t.Fatalf("ids %v not descending", ids)
		}
	}

	all, next, err := s.ListAdminAudit(ctx, 0, "")
	if err != nil || len(all) != 5 || next != "" {
		t.Fatalf("default limit: %d rows, next %q, %v", len(all), next, err)
	}
	if r := all[4]; r.Target != nil || r.Diff != nil {
		t.Errorf("setup.claim: target %v diff %s, want both nil", r.Target, r.Diff)
	}
	if r := all[2]; r.Target == nil || *r.Target != "openrouter" || r.Diff != nil {
		t.Errorf("credential.delete: target %v diff %s", r.Target, r.Diff)
	}
	var diff map[string]string
	if err := json.Unmarshal(all[3].Diff, &diff); err != nil || diff["fingerprint"] != "fp_0123456789ab" {
		t.Errorf("credential.put diff %s (%v)", all[3].Diff, err)
	}

	for _, bad := range []string{"bogus!", "aWR8MA", encodeCursor(all[0].At, "x")} {
		if _, _, err := s.ListAdminAudit(ctx, 2, bad); !errors.Is(err, ErrBadCursor) {
			t.Errorf("cursor %q: %v, want ErrBadCursor", bad, err)
		}
	}
}
