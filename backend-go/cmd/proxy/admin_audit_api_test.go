package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	schemaAdminAuditPage = "api.v1.schema.json#/$defs/Page%3CAdminAuditEntry%3E"
	auditAdminKey        = "audit-log-admin-key-0001"
)

func TestAdminAuditLogNeedsTheDatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: auditAdminKey})
	code, body := env.get("/api/admin/audit-log", auditAdminKey)
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("without a database: %d %s, want 503 persistence_disabled", code, body)
	}
}

func TestAdminAuditLogPagingAgainstPostgres(t *testing.T) {
	env, pg := pgEnv(t, envOpts{adminKey: auditAdminKey})
	ctx := context.Background()
	// The rows the page must show, oldest first; whatever the server wrote at startup comes
	// before them.
	writes := []store.AdminAuditEntry{
		{Actor: "setup", Action: "setup.claim"},
		{Actor: "setup-admin", Action: "credential.put", Target: "openrouter", Diff: map[string]any{"fingerprint": "fp_5d0c41e9a7b2", "bind": []string{"decision_model", "judge"}}},
		{Actor: "setup-admin", Action: "settings.update", Target: "budgets", Diff: map[string]any{"budgets.llm_usd_day": map[string]any{"from": 5.0, "to": 3.0}}},
	}
	for _, w := range writes {
		if err := pg.AppendAdminAudit(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	page := func(query string) (adminAuditPage, []byte) {
		t.Helper()
		code, body := env.get("/api/admin/audit-log"+query, auditAdminKey)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, code, body)
		}
		contractstest.ValidateJSON(t, schemaAdminAuditPage, body)
		var p adminAuditPage
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p, body
	}
	first, _ := page("?limit=2")
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page: %d items, next %v", len(first.Items), first.NextCursor)
	}
	newest, second := first.Items[0], first.Items[1]
	if newest.Action != "settings.update" || newest.Target == nil || *newest.Target != "budgets" || second.Action != "credential.put" {
		t.Errorf("newest first: got %s, %s", newest.Action, second.Action)
	}
	if newest.ID <= second.ID || newest.At.IsZero() {
		t.Errorf("ids %d, %d not descending / no timestamp", newest.ID, second.ID)
	}
	var diff map[string]map[string]float64
	if err := json.Unmarshal(newest.Diff, &diff); err != nil || diff["budgets.llm_usd_day"]["to"] != 3 {
		t.Errorf("diff = %s (%v)", newest.Diff, err)
	}

	// Follow the cursor to the end: every row once, ids strictly descending.
	seen := []int64{newest.ID, second.ID}
	cursor := *first.NextCursor
	for {
		p, _ := page("?limit=2&cursor=" + cursor)
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("ids not strictly descending across pages: %v", seen)
		}
	}
	all, body := page("")
	if len(all.Items) != len(seen) || all.NextCursor != nil {
		t.Errorf("default page has %d rows (next %v), the paged walk %d", len(all.Items), all.NextCursor, len(seen))
	}
	// The third newest row is the setup.claim written first: no target, no diff, both null.
	if claim := all.Items[2]; claim.Action != "setup.claim" || claim.Target != nil {
		t.Errorf("setup.claim row = %+v, want a null target", claim)
	}
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(body, &raw)
	if string(raw.Items[2]["target"]) != "null" || string(raw.Items[2]["diff"]) != "null" {
		t.Errorf("wire target/diff = %s / %s, want null", raw.Items[2]["target"], raw.Items[2]["diff"])
	}

	for _, q := range []string{"?cursor=not-a-cursor", "?limit=0", "?limit=201", "?cursor=aWR8LTE" /* id|-1 */} {
		code, body := env.get("/api/admin/audit-log"+q, auditAdminKey)
		if code != http.StatusBadRequest || errorCodeOf(t, body) != codeBadRequest {
			t.Errorf("GET %s: %d %s, want 400 bad_request", q, code, body)
		}
	}
	if code, _ := env.get("/api/admin/audit-log", testKey); code != http.StatusForbidden {
		t.Errorf("non-admin key: %d, want 403", code)
	}
	if code, _ := env.get("/api/admin/audit-log", ""); code != http.StatusUnauthorized {
		t.Errorf("no key: %d, want 401", code)
	}
}

// TestCORSOnConnectRoutes: a cross-origin dashboard's preflight on a settings or Connect route gets exactly the
// SPEC §2.4 headers (D43).
func TestCORSOnConnectRoutes(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: auditAdminKey, mutate: func(cfg *config.Config) {
		cfg.AllowedOrigins = []string{"http://localhost:5173"}
	}})
	for _, path := range []string{"/api/decision-model/test", "/api/admin/audit-log"} {
		r, _ := http.NewRequest(http.MethodOptions, env.http.URL+path, nil)
		r.Header.Set("Origin", "http://localhost:5173")
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "x-trustagent-key")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("%s preflight: %d", path, resp.StatusCode)
		}
		if got, want := resp.Header.Get("Access-Control-Allow-Headers"),
			"Content-Type, If-Match, X-TrustAgent-Key, X-TrustAgent-CSRF, X-TrustAgent-Route, X-TrustAgent-Mode, X-Request-ID"; got != want {
			t.Errorf("%s Access-Control-Allow-Headers = %q, want %q", path, got, want)
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, PATCH, DELETE, OPTIONS" {
			t.Errorf("%s Access-Control-Allow-Methods = %q", path, got)
		}
	}
	// An origin not on the list gets no CORS headers at all.
	r, _ := http.NewRequest(http.MethodOptions, env.http.URL+"/api/admin/audit-log", nil)
	r.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Access-Control-Allow-Headers") != "" {
		t.Error("an unlisted origin got CORS headers")
	}
}
