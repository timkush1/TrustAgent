package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// TestTracesReasonFilter: GET /api/traces?reason= takes a whole reason code or a prefix that ends
// in a colon, needs since (D68), and refuses anything that is not a reason code.
func TestTracesReasonFilter(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	ctx := context.Background()
	now := time.Now().UTC()
	add := func(id string, reasons ...string) {
		t.Helper()
		rec := &store.TraceRecord{TraceID: id, CreatedAt: now.Add(-time.Minute), Source: "proxy", APIFormat: "openai_chat",
			Route: "support-bot", Mode: "flag", GateStatus: "ok", Reasons: reasons}
		action := "flag"
		rec.Action, rec.WouldAction = &action, &action
		if err := env.store.RecordDecision(ctx, rec, &store.DecisionRecord{TraceID: id, Route: "support-bot",
			Mode: "flag", Status: "ok", Action: "flag", Decision: json.RawMessage(`{}`)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	add("t-numbers", "groundedness:warn", "det:numbers_not_in_evidence")
	add("t-urls", "det:urls_not_in_evidence")
	add("t-second", "needs_second_signal")

	since := "&since=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339))
	ids := func(path string) string {
		t.Helper()
		code, body := env.get(path, testKey)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", path, code, body)
		}
		var p struct {
			Items []struct {
				TraceID string `json:"trace_id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range p.Items {
			out = append(out, it.TraceID)
		}
		return strings.Join(out, ",")
	}
	for reason, want := range map[string]string{
		"det:numbers_not_in_evidence": "t-numbers",
		"det:":                        "t-urls,t-numbers",
		"needs_second_signal":         "t-second",
		"det":                         "",
		"groundedness:":               "t-numbers",
	} {
		if got := ids("/api/traces?reason=" + url.QueryEscape(reason) + since); got != want {
			t.Errorf("reason=%s: got %q, want %q", reason, got, want)
		}
	}

	for _, bad := range []string{
		"/api/traces?reason=det:",                               // no since
		"/api/traces?reason=" + url.QueryEscape("det%") + since, // not a reason code
		"/api/traces?reason=" + url.QueryEscape("a b") + since,
		"/api/traces?reason=" + url.QueryEscape("::") + since,
		"/api/traces?reason=" + strings.Repeat("a", 129) + since,
	} {
		code, body := env.get(bad, testKey)
		if code != http.StatusBadRequest || !strings.Contains(string(body), `"reason"`) {
			t.Errorf("%s: HTTP %d, want 400 naming reason: %s", bad, code, body)
		}
	}
}
