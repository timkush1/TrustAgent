package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	schemaRecording   = "recording.v1.schema.json"
	schemaTraceDetail = "api.v1.schema.json#/$defs/TraceDetail"
	schemaWsEvent     = "api.v1.schema.json#/$defs/WsEvent"
)

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestSanitizer(t *testing.T) {
	san, err := newSanitizer()
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"key sk-or-v1-" + strings.Repeat("ab", 20) + " leaked":       "key sk-o" + strings.Repeat("*", 45) + " leaked",
		"AKIAABCDEFGHIJKLMNOP":                                       "AKIA****************",
		"mail jane.doe@acme-corp.com now":                            "mail xxxx.xxx@xxxx-xxxx.xxx now",
		"write to help@support.example.com":                          "write to help@support.example.com",
		"see https://api.acme-corp.com:8443/v1?x=1":                  "see https://xxx.xxxx-xxxx.xxx:8443/v1?x=1",
		"see https://docs.example/refunds and http://x.test/a":       "see https://docs.example/refunds and http://x.test/a",
		"https://user:pw@example.com/p":                              "https://xxxx:xx@example.com/p",
		"db at pg-1.db.internal:5432":                                "db at xx-x.xx.xxxxxxxx:5432",
		"from 10.0.12.7 and [2001:db8::1] via http://localhost:8080": "from xx.x.xx.x and [xxxx:xxx::x] via http://localhost:8080",
		"version 999.1.1.1 stays":                                    "version 999.1.1.1 stays",
		"héllo 😀 https://café.acme.io/x":                             "héllo 😀 https://xxxx.xxxx.xx/x",
	} {
		got := san.String(in)
		if got != want {
			t.Errorf("sanitize(%q)\n = %q\nwant %q", in, got, want)
		}
		if utf16Len(got) != utf16Len(in) {
			t.Errorf("sanitize(%q) changed the UTF-16 length", in)
		}
	}
	// Astral characters inside a masked host keep their two code units.
	if got := san.String("http://𝔸.acme.io/"); utf16Len(got) != utf16Len("http://𝔸.acme.io/") || !strings.HasPrefix(got, "http://xx.") {
		t.Errorf("astral host = %q", got)
	}
	b, err := san.JSON(map[string]any{"n": json.Number("0.000025704"), "list": []any{"a@b.co", map[string]any{"k<&>": "x@y.io"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"n": 0.000025704`) || !strings.Contains(string(b), `"k<&>": "x@x.xx"`) || strings.Contains(string(b), "b.co") {
		t.Errorf("JSON = %s", b)
	}
}

// fixture loads a TraceDetail fixture from contracts/fixtures/api.
func fixture(t *testing.T, name string) store.TraceDetail {
	t.Helper()
	b, err := contracts.FS.ReadFile("fixtures/api/TraceDetail." + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var d store.TraceDetail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// variant copies a fixture with a new id, time and answer source.
func variant(t *testing.T, d store.TraceDetail, id string, at time.Time, answerSource string) traceData {
	t.Helper()
	b, _ := json.Marshal(d)
	var c store.TraceDetail
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.TraceID, c.CreatedAt, c.UpdatedAt = id, at, at.Add(2*time.Second)
	c.AnswerSource = &answerSource
	for i, raw := range c.Decisions {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["trace_id"] = id
		c.Decisions[i], _ = json.Marshal(m)
	}
	if c.AuditDetail != nil {
		c.AuditDetail.TraceID, c.AuditDetail.CreatedAt = id, at.Add(time.Second)
	}
	return traceData{Detail: c}
}

// mockVariant turns a trace into one checked by the mock backend and judged by the mock judge.
func mockVariant(t *testing.T, td traceData) traceData {
	t.Helper()
	d := td.Detail
	if d.DecisionModel != nil {
		dm := *d.DecisionModel
		dm.Backend = "mock"
		snap := "mock/jev-heuristic-v1"
		dm.Snapshot = &snap
		d.DecisionModel = &dm
	}
	if d.AuditDetail != nil {
		a := *d.AuditDetail
		a.Judge = json.RawMessage(`{"configured_provider":"mock","effective_provider":"mock","model":"mock-judge","fallback_active":false,"fallback_reason":null,"structured_outputs":false,"healthy":true,"credential_source":"none"}`)
		d.AuditDetail = &a
	}
	return traceData{Detail: d}
}

func traceSet(t *testing.T) []traceData {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	refund, noEv := fixture(t, "refund-contradiction-flag"), fixture(t, "no-evidence-not-assessed")
	cost := 0.0021
	a := variant(t, refund, "01a0e780-fe6b-74b8-a21f-000000000002", base.Add(40*time.Second), "generated")
	a.LLMUSD = &cost
	return []traceData{
		a,
		variant(t, noEv, "01a0e780-fe6b-74b8-a21f-000000000001", base, "scripted"),
		variant(t, refund, "01a0e780-fe6b-74b8-a21f-000000000003", base.Add(90*time.Second), "scripted"),
	}
}

// validateRecording checks the manifest, its events and the trace files against the contracts.
func validateRecording(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, schemaRecording, b)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for i, e := range m["events"].([]any) {
		ev := e.(map[string]any)
		env := map[string]any{"v": 2, "seq": i + 1, "epoch": "replay", "type": ev["type"], "ts": "2026-10-05T10:00:00Z", "trace_id": ev["trace_id"], "data": ev["data"]}
		eb, _ := json.Marshal(env)
		contractstest.ValidateJSON(t, schemaWsEvent, eb)
	}
	for _, tr := range m["traces"].([]any) {
		id := tr.(map[string]any)["trace_id"].(string)
		tb, err := os.ReadFile(filepath.Join(dir, "traces", id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, schemaTraceDetail, tb)
	}
	return m
}

func TestBuildRecorded(t *testing.T) {
	rec, err := buildRecording(traceSet(t), buildOptions{gitSHA: "abc1234", version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Recorded 2026-10-05 · typesafe/jev-1.13-20260917 + anthropic claude-haiku-4-5 · scripted and generated answers · replayed, not live"
	if rec.Kind != kindRecorded || rec.Label != want {
		t.Errorf("kind %s, label %q", rec.Kind, rec.Label)
	}
	if rec.Traces[0].TraceID != "01a0e780-fe6b-74b8-a21f-000000000001" || rec.AnswerSources != (answerSources{Scripted: 2, Generated: 1}) {
		t.Errorf("traces not oldest first or counts wrong: %s, %+v", rec.Traces[0].TraceID, rec.AnswerSources)
	}
	if rec.Totals.Traces != 3 || rec.Totals.LLMUSD != 0.0021 || rec.Totals.DecisionUSD < 5e-5 || rec.ROI.Window != "15m" || rec.ROI.Simulated ||
		rec.ROI.GatedResponses != 3 || rec.Judge != (judgeInfo{Provider: "anthropic", Model: "claude-haiku-4-5"}) || rec.CalibrationState != "none" {
		t.Errorf("recording = %+v", rec)
	}
	types := []string{}
	for _, e := range rec.Events {
		types = append(types, e.Type)
	}
	// trace 1 (0 s, no audit), trace 2 (40 s, audit), trace 3 (90 s, audit).
	wantTypes := "trace_started gate_decision trace_started gate_decision audit_queued audit_completed trace_started gate_decision audit_queued audit_completed"
	if strings.Join(types, " ") != wantTypes {
		t.Errorf("events = %s", strings.Join(types, " "))
	}
	for i := 1; i < len(rec.Events); i++ {
		if rec.Events[i].TMs < rec.Events[i-1].TMs {
			t.Errorf("events out of order at %d", i)
		}
	}
	if started := rec.Events[0].Data.(map[string]any)["trace"].(store.TraceSummary); started.GateStatus != "pending" || started.Action != nil || started.Audit.Status != "none" {
		t.Errorf("trace_started carries the decided summary: %+v", started)
	}

	dir := t.TempDir()
	san, _ := newSanitizer()
	if err := writeRecording(dir, rec, traceSet(t), san, false); err != nil {
		t.Fatal(err)
	}
	validateRecording(t, dir)
	if err := writeRecording(dir, rec, traceSet(t), san, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("overwrite without --force: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "traces", "stale.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeRecording(dir, rec, traceSet(t)[:1], san, true); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "traces")); len(entries) != 1 {
		t.Errorf("--force left %d trace files", len(entries))
	}
}

func TestBuildMock(t *testing.T) {
	var traces []traceData
	for _, td := range traceSet(t) {
		traces = append(traces, mockVariant(t, td))
	}
	rec, err := buildRecording(traces, buildOptions{gitSHA: "abc1234", version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != kindMock || rec.Label != mockLabel || !rec.ROI.Simulated || rec.DecisionModel.Backend != "mock" {
		t.Errorf("recording = %+v", rec)
	}
	dir := t.TempDir()
	san, _ := newSanitizer()
	if err := writeRecording(dir, rec, traces, san, false); err != nil {
		t.Fatal(err)
	}
	if m := validateRecording(t, dir); m["kind"] != "mock" {
		t.Errorf("manifest kind %v", m["kind"])
	}
}

func TestBuildRefusesMislabelling(t *testing.T) {
	set := func(mut func([]traceData)) []traceData {
		ts := traceSet(t)
		mut(ts)
		return ts
	}
	for name, c := range map[string]struct {
		traces []traceData
		opt    buildOptions
		want   string
	}{
		"empty":            {nil, buildOptions{}, "no traces"},
		"no answer source": {set(func(ts []traceData) { ts[1].Detail.AnswerSource = nil }), buildOptions{}, "1 traces have no answer_source"},
		"judge fallback": {set(func(ts []traceData) {
			ts[0].Detail.AuditDetail.Judge = json.RawMessage(`{"effective_provider":"openai","model":"gpt-4o-mini","fallback_active":true}`)
			ts[2].Detail.AuditDetail.Judge = ts[0].Detail.AuditDetail.Judge
		}), buildOptions{}, "fallback provider"},
		"mock judge": {set(func(ts []traceData) {
			ts[0] = mockVariant(t, ts[0])
			ts[0].Detail.DecisionModel = ts[2].Detail.DecisionModel
			ts[2].Detail.AuditDetail = nil
			ts[2].Detail.Audit.AuditID = nil
		}), buildOptions{}, "judge is the mock"},
		"mixed backends": {set(func(ts []traceData) { ts[0] = mockVariant(t, ts[0]) }), buildOptions{}, "mix decision backends"},
		"mixed judges": {set(func(ts []traceData) {
			ts[0].Detail.AuditDetail.Judge = json.RawMessage(`{"effective_provider":"openrouter","model":"anthropic/claude-haiku-4.5"}`)
		}), buildOptions{}, "mix judges"},
		"legacy id":         {set(func(ts []traceData) { ts[1].Detail.TraceID = "legacy-1" }), buildOptions{}, "non-UUID"},
		"no decision model": {[]traceData{variant(t, fixture(t, "no-evidence-not-assessed"), "01a0e780-fe6b-74b8-a21f-000000000009", time.Now(), "scripted")}, buildOptions{judge: &judgeInfo{Provider: "anthropic", Model: "claude-haiku-4-5"}}, "no trace was checked"},
		"no judge":          {[]traceData{variant(t, fixture(t, "no-evidence-not-assessed"), "01a0e780-fe6b-74b8-a21f-000000000009", time.Now(), "scripted")}, buildOptions{}, "--judge-provider"},
		"judge flag clash":  {traceSet(t), buildOptions{judge: &judgeInfo{Provider: "openai", Model: "gpt-4o"}}, "were judged by anthropic"},
	} {
		if _, err := buildRecording(c.traces, c.opt); err == nil || !strings.Contains(err.Error(), c.want) || !errors.Is(err, errNotRecordable) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestWindowAndCalibrationState(t *testing.T) {
	for span, want := range map[time.Duration]string{0: "15m", 15 * time.Minute: "15m", time.Hour: "1h", 2 * time.Hour: "24h", 72 * time.Hour: "7d", 40 * 24 * time.Hour: "30d"} {
		if got := window(span); got != want {
			t.Errorf("window(%v) = %s, want %s", span, got, want)
		}
	}
	dec := func(states ...string) traceData {
		evs := map[string]any{}
		for i, s := range states {
			evs[string(rune('a'+i))+"@1"] = map[string]any{"stage": "active", "internal": false, "status": "assessed", "calibration_state": s}
		}
		evs["shadow@1"] = map[string]any{"stage": "shadow", "status": "assessed", "calibration_state": "none"}
		b, _ := json.Marshal(map[string]any{"evaluators": evs})
		return traceData{Detail: store.TraceDetail{Decisions: []json.RawMessage{b}}}
	}
	if got := calibrationState([]traceData{dec("measured", "provisional"), dec("measured")}); got != "provisional" {
		t.Errorf("state = %s, want provisional", got)
	}
	if got := calibrationState([]traceData{dec("measured")}); got != "measured" {
		t.Errorf("state = %s, want measured (shadow ignored)", got)
	}
	if got := calibrationState([]traceData{dec("mock", "family")}); got != "mock" {
		t.Errorf("state = %s", got)
	}
	if got := calibrationState(nil); got != "none" {
		t.Errorf("state of nothing = %s", got)
	}
}

func TestRunAgainstTheAPI(t *testing.T) {
	traces := traceSet(t)
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
		_ = json.NewEncoder(w).Encode(d)
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(keyFile, []byte("admin-key\n"), 0o600)
	d := deps{getenv: func(string) string { return "" }, now: time.Now, rev: func() string { return "0123abcd" }, client: srv.Client()}

	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"--api", srv.URL, "--api-key-file", keyFile, "--out", dir,
		"--since", "2026-10-05T00:00:00Z", "--source", "proxy", "--route", "support-bot"}, &out, &errb, d)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "since=2026-10-05T00%3A00%3A00Z") || !strings.Contains(queries[0], "source=proxy") ||
		!strings.Contains(queries[0], "route=support-bot") || !strings.Contains(queries[0], "limit=200") || !strings.Contains(queries[1], "cursor=c2") {
		t.Errorf("queries = %q", queries)
	}
	m := validateRecording(t, dir)
	if m["kind"] != "recorded" || m["git_sha"] != "0123abcd" || !strings.Contains(out.String(), "Wrote a recorded recording of 3 traces") {
		t.Errorf("manifest kind %v sha %v; %s", m["kind"], m["git_sha"], out.String())
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
	} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, &out, &errb, d); code != exitUsage {
			t.Errorf("%v: exit %d (%s), want %d", args, code, errb.String(), exitUsage)
		}
	}
}
