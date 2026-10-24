package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/recording/recordingtest"
	"github.com/truthtable/backend-go/internal/store"
)

// vendorHosts are a real recording's hosts (the fixtures' judge is anthropic).
var vendorHosts = recording.Hosts{Decision: "openrouter.ai", Judge: "api.anthropic.com", Generated: "openrouter.ai"}

// hostArgs are the flags that name them.
var hostArgs = []string{"--decision-host", "openrouter.ai", "--judge-host", "api.anthropic.com", "--generated-host", "openrouter.ai"}

func TestWriteRecording(t *testing.T) {
	traces := recordingtest.Set(t)
	rec, err := recording.Build(traces, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
	if err != nil {
		t.Fatal(err)
	}
	san, err := recording.NewSanitizer()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeRecording(dir, rec, traces, san, false); err != nil {
		t.Fatal(err)
	}
	recordingtest.ValidateDir(t, dir)
	if err := writeRecording(dir, rec, traces, san, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("overwrite without --force: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "traces", "stale.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	one, err := recording.Build(traces[:1], recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecording(dir, one, traces[:1], san, true); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "traces")); len(entries) != 1 {
		t.Errorf("--force left %d trace files", len(entries))
	}
	recordingtest.ValidateDir(t, dir)

	// Trace files an interrupted export left behind (no manifest) are not mixed into a new one.
	partial := t.TempDir()
	if err := os.MkdirAll(filepath.Join(partial, "traces"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "traces", "stale.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeRecording(partial, one, traces[:1], san, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("stale trace files without --force: %v", err)
	}
	if err := writeRecording(partial, one, traces[:1], san, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(partial, "traces", "stale.json")); !os.IsNotExist(err) {
		t.Errorf("--force kept a stale trace file: %v", err)
	}
}

func TestRunAgainstTheAPI(t *testing.T) {
	traces := recordingtest.Set(t)
	byID := map[string]store.TraceDetail{}
	var summaries []store.TraceSummary
	for i := len(traces) - 1; i >= 0; i-- { // the API lists newest first
		byID[traces[i].Detail.TraceID] = traces[i].Detail
		summaries = append(summaries, traces[i].Detail.TraceSummary)
	}
	var queries []string
	var scopeless bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"A key is required."}}`))
			return
		}
		if r.URL.Path == "/api/traces" {
			queries = append(queries, r.URL.RawQuery)
			items, next := summaries[:2], "c2"
			if r.URL.Query().Get("cursor") == "c2" {
				items, next = summaries[2:], ""
			}
			var nc any
			if next != "" {
				nc = next
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": nc})
			return
		}
		d, ok := byID[strings.TrimPrefix(r.URL.Path, "/api/traces/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if scopeless {
			d.Body = store.UnavailableBody(store.BodyScope)
		}
		// The API serves human labels; a published recording must not.
		d.Labels = []store.LabelView{{Target: "groundedness@1", Label: "should_fire", LabeledBy: "jane.doe", CreatedAt: d.CreatedAt}}
		_ = json.NewEncoder(w).Encode(d)
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(keyFile, []byte("admin-key\n"), 0o600)
	d := deps{getenv: func(string) string { return "" }, now: time.Now, rev: func() string { return "0123abcd" }, client: srv.Client()}

	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run(context.Background(), append([]string{"--api", srv.URL, "--api-key-file", keyFile, "--out", dir,
		"--since", "2026-10-05T00:00:00Z", "--source", "proxy", "--route", "support-bot",
		"--calibration-quality", "measured", "--calibration-snapshot", "typesafe/jev-1.13-20260917"}, hostArgs...), &out, &errb, d)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "since=2026-10-05T00%3A00%3A00Z") || !strings.Contains(queries[0], "source=proxy") ||
		!strings.Contains(queries[0], "route=support-bot") || !strings.Contains(queries[0], "limit=200") || !strings.Contains(queries[1], "cursor=c2") {
		t.Errorf("queries = %q", queries)
	}
	m := recordingtest.ValidateDir(t, dir)
	for _, tr := range traces {
		if b, err := os.ReadFile(filepath.Join(dir, "traces", tr.Detail.TraceID+".json")); err != nil || strings.Contains(string(b), "jane.doe") {
			t.Errorf("trace file %s keeps its labels (%v)", tr.Detail.TraceID, err)
		}
	}
	if m["kind"] != "recorded" || m["git_sha"] != "0123abcd" || !strings.Contains(out.String(), "Wrote a recorded recording of 3 traces") {
		t.Errorf("manifest kind %v sha %v; %s", m["kind"], m["git_sha"], out.String())
	}
	dm, _ := m["decision_model"].(map[string]any)
	judge, _ := m["judge"].(map[string]any)
	sources, _ := m["answer_sources"].(map[string]any)
	feat, _ := m["featured"].(map[string]any)
	calib, _ := m["calibration"].(map[string]any)
	if dm["host"] != "openrouter.ai" || judge["host"] != "api.anthropic.com" || sources["generated_host"] != "openrouter.ai" ||
		feat["rule"] != "contradicted_with_span" || calib["quality"] != "measured" || calib["snapshot"] != "typesafe/jev-1.13-20260917" ||
		!strings.Contains(out.String(), "Featured: "+feat["trace_id"].(string)) {
		t.Errorf("hosts, featured or calibration missing: %v %v %v %v %v\n%s", dm, judge, sources, feat, calib, out.String())
	}

	// D203: without the hosts, or with mock-upstream's, the traces are not labelled recorded.
	for name, hosts := range map[string][]string{
		"no hosts":      nil,
		"mock-upstream": {"--decision-host", "mock-upstream", "--judge-host", "api.anthropic.com", "--generated-host", "openrouter.ai"},
	} {
		errb.Reset()
		args := append([]string{"--api", srv.URL, "--api-key-file", keyFile, "--out", t.TempDir()}, hosts...)
		if code := run(context.Background(), args, &out, &errb, d); code != exitFailed || !strings.Contains(errb.String(), "non_vendor_host") {
			t.Errorf("%s: exit %d %s", name, code, errb.String())
		}
	}
	// The owner's featured pick (no trace meets the rule is simulated by naming one).
	errb.Reset()
	picked := t.TempDir()
	if code := run(context.Background(), append([]string{"--api", srv.URL, "--api-key-file", keyFile, "--out", picked,
		"--featured", traces[2].Detail.TraceID}, hostArgs...), &out, &errb, d); code != exitOK {
		t.Fatalf("--featured: exit %d %s", code, errb.String())
	}
	if f, _ := recordingtest.ValidateDir(t, picked)["featured"].(map[string]any); f["rule"] != "best_flagged" || f["trace_id"] != traces[2].Detail.TraceID {
		t.Errorf("--featured: %v", f)
	}

	scopeless = true
	errb.Reset()
	if code := run(context.Background(), []string{"--api", srv.URL, "--api-key-file", keyFile, "--out", t.TempDir()}, &out, &errb, d); code != exitFailed ||
		!strings.Contains(errb.String(), "content scope") {
		t.Errorf("scopeless key: exit %d %s", code, errb.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"--api", srv.URL, "--out", t.TempDir()}, &out, &errb, d); code != exitFailed ||
		!strings.Contains(errb.String(), "401 unauthorized: A key is required.") {
		t.Errorf("no key: exit %d %s", code, errb.String())
	}
}

func TestUsage(t *testing.T) {
	d := deps{getenv: func(string) string { return "" }, now: time.Now, rev: func() string { return "" }}
	for _, args := range [][]string{
		{},
		{"--out", "x"}, // no database, no api; the git revision is unknown
		{"--out", "x", "--git-sha", "abc1234"},
		{"--out", "x", "--git-sha", "abc1234", "--limit", "201", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--source", "email", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--since", "yesterday", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--judge-provider", "anthropic", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--api", "ftp://host"},
		{"--out", "x", "--git-sha", "abc1234", "--decision-host", "https://openrouter.ai", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--judge-host", "api.anthropic.com:443", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--generated-host", "OpenRouter.ai", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--calibration-quality", "mock", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--calibration-quality", "measured", "--database-url", "postgres://x"},
		{"--out", "x", "--git-sha", "abc1234", "--calibration-snapshot", "s", "--database-url", "postgres://x"},
	} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, &out, &errb, d); code != exitUsage {
			t.Errorf("%v: exit %d (%s), want %d", args, code, errb.String(), exitUsage)
		}
	}
}
