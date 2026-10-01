package recording_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/recording/recordingtest"
	"github.com/truthtable/backend-go/internal/store"
)

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestSanitizer(t *testing.T) {
	san, err := recording.NewSanitizer()
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		"key sk-or-v1-" + strings.Repeat("ab", 20) + " leaked":                   "key sk-o" + strings.Repeat("*", 45) + " leaked",
		"AKIAABCDEFGHIJKLMNOP":                                                   "AKIA****************",
		"mail jane.doe@acme-corp.com now":                                        "mail xxxx.xxx@xxxx-xxxx.xxx now",
		"write to help@support.example.com":                                      "write to help@support.example.com",
		"see https://api.acme-corp.com:8443/v1?x=1":                              "see https://xxx.xxxx-xxxx.xxx:8443/v1?x=1",
		"see https://docs.example/refunds and http://x.test/a":                   "see https://docs.example/refunds and http://x.test/a",
		"https://user:pw@example.com/p":                                          "https://xxxx:xx@example.com/p",
		"db at pg-1.db.internal:5432":                                            "db at xx-x.xx.xxxxxxxx:5432",
		"from 10.0.12.7 and [2001:db8::1] via http://localhost:8080":             "from xx.x.xx.x and [xxxx:xxx::x] via http://localhost:8080",
		"version 999.1.1.1 stays":                                                "version 999.1.1.1 stays",
		"héllo 😀 https://café.acme.io/x":                                         "héllo 😀 https://xxxx.xxxx.xx/x",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.SflKxwRJSMeKKF2QT4fw done": "jwt eyJh" + strings.Repeat("*", 57) + " done",
		"Authorization: Bearer abcdefghijklmnop1234":                             "Authorization: Bearer abcd" + strings.Repeat("*", 16),
		"google AIza" + strings.Repeat("B", 35):                                  "google AIza" + strings.Repeat("*", 35),
		"stripe sk_live_" + strings.Repeat("9", 24) + ".":                        "stripe sk_l" + strings.Repeat("*", 28) + ".",
	} {
		got := san.String(in)
		if got != want {
			t.Errorf("sanitize(%q)\n = %q\nwant %q", in, got, want)
		}
		if utf16Len(got) != utf16Len(in) {
			t.Errorf("sanitize(%q) changed the UTF-16 length", in)
		}
	}
	// A PEM private key is masked whole, not just its BEGIN line (the detector's pattern); line
	// breaks stay. A block cut off before its END line is masked to the end.
	for _, in := range []string{
		"key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nq1w2e3r4==\n-----END RSA PRIVATE KEY-----\nthanks",
		"-----BEGIN PRIVATE KEY-----\r\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
	} {
		got := san.String(in)
		if strings.Contains(got, "MIIE") || strings.Contains(got, "q1w2") || strings.Contains(got, "PRIVATE") ||
			strings.Count(got, "\n") != strings.Count(in, "\n") || utf16Len(got) != utf16Len(in) {
			t.Errorf("sanitize(%q) = %q", in, got)
		}
		if strings.HasPrefix(in, "key:") && (!strings.HasPrefix(got, "key:\n----*") || !strings.HasSuffix(got, "\nthanks")) {
			t.Errorf("sanitize(%q) = %q, want the text around the block kept", in, got)
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

func TestDetailLeavesOutLabels(t *testing.T) {
	san, err := recording.NewSanitizer()
	if err != nil {
		t.Fatal(err)
	}
	d := recordingtest.Set(t)[0].Detail
	note := "called the customer at 555-0100"
	d.Labels = []store.LabelView{{Target: "groundedness@1", Label: "should_fire", LabeledBy: "jane.doe", CreatedAt: d.CreatedAt, Note: &note}}
	b, err := san.Detail(d)
	if err != nil {
		t.Fatal(err)
	}
	contractstest.ValidateJSON(t, recordingtest.SchemaTraceDetail, b)
	if strings.Contains(string(b), "jane.doe") || strings.Contains(string(b), "555-0100") || !strings.Contains(string(b), `"labels": []`) {
		t.Errorf("the published detail keeps its labels:\n%s", b)
	}
	if len(d.Labels) != 1 {
		t.Error("Detail changed the caller's labels")
	}
}

func build(t *testing.T, traces []recording.Trace, opt recording.Options) (*recording.Recording, map[string]any) {
	t.Helper()
	rec, err := recording.Build(traces, opt)
	if err != nil {
		t.Fatal(err)
	}
	san, err := recording.NewSanitizer()
	if err != nil {
		t.Fatal(err)
	}
	b, err := san.JSON(rec)
	if err != nil {
		t.Fatal(err)
	}
	return rec, recordingtest.ValidateManifest(t, b)
}

func TestBuildRecorded(t *testing.T) {
	rec, _ := build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0"})
	want := "Recorded 2026-10-05 · typesafe/jev-1.13-20260917 + anthropic claude-haiku-4-5 · scripted and generated answers · replayed, not live"
	if rec.Kind != recording.KindRecorded || rec.Label != want {
		t.Errorf("kind %s, label %q", rec.Kind, rec.Label)
	}
	if rec.Traces[0].TraceID != "01a0e780-fe6b-74b8-a21f-000000000001" || rec.AnswerSources != (recording.AnswerSources{Scripted: 2, Generated: 1}) {
		t.Errorf("traces not oldest first or counts wrong: %s, %+v", rec.Traces[0].TraceID, rec.AnswerSources)
	}
	if rec.Totals.Traces != 3 || rec.Totals.LLMUSD != 0.0021 || rec.Totals.DecisionUSD < 5e-5 || rec.ROI.Window != "15m" || rec.ROI.Simulated ||
		rec.ROI.GatedResponses != 3 || rec.Judge != (recording.Judge{Provider: "anthropic", Model: "claude-haiku-4-5"}) || rec.CalibrationState != "none" ||
		!rec.RecordedAt.Equal(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
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
	if decided := rec.Events[3].Data.(map[string]any)["trace"].(store.TraceSummary); rec.Events[3].Type != "gate_decision" ||
		decided.Audit.Status != "queued" || decided.Audit.Verdict != nil || decided.Action == nil {
		t.Errorf("gate_decision shows the audit outcome early: %+v", decided)
	}
	if final := rec.Traces[1]; final.Audit.Status != "completed" || final.Audit.Verdict == nil {
		t.Errorf("the manifest trace list lost the audit outcome: %+v", final.Audit)
	}
}

func TestBuildMock(t *testing.T) {
	var traces []recording.Trace
	for _, tr := range recordingtest.Set(t) {
		traces = append(traces, recordingtest.Mock(tr))
	}
	rec, m := build(t, traces, recording.Options{GitSHA: "abc1234", Version: "1.0.0"})
	if rec.Kind != recording.KindMock || rec.Label != recording.MockLabel || !rec.ROI.Simulated || rec.DecisionModel.Backend != "mock" || m["kind"] != "mock" {
		t.Errorf("recording = %+v", rec)
	}
}

func TestBuildRefusesMislabelling(t *testing.T) {
	set := func(mut func([]recording.Trace)) []recording.Trace {
		ts := recordingtest.Set(t)
		mut(ts)
		return ts
	}
	noEvidence := func() []recording.Trace {
		return []recording.Trace{recordingtest.Variant(t, recordingtest.Fixture(t, "no-evidence-not-assessed"), "01a0e780-fe6b-74b8-a21f-000000000009", time.Now(), "scripted")}
	}
	for name, c := range map[string]struct {
		traces []recording.Trace
		opt    recording.Options
		want   string
	}{
		"empty":            {nil, recording.Options{}, "no traces"},
		"no answer source": {set(func(ts []recording.Trace) { ts[1].Detail.AnswerSource = nil }), recording.Options{}, "1 traces have no answer_source"},
		"judge fallback": {set(func(ts []recording.Trace) {
			ts[0].Detail.AuditDetail.Judge = json.RawMessage(`{"effective_provider":"openai","model":"gpt-4o-mini","fallback_active":true}`)
			ts[2].Detail.AuditDetail.Judge = ts[0].Detail.AuditDetail.Judge
		}), recording.Options{}, "fallback provider"},
		"mock judge": {set(func(ts []recording.Trace) {
			ts[0] = recordingtest.Mock(ts[0])
			ts[0].Detail.DecisionModel = ts[2].Detail.DecisionModel
			ts[2].Detail.AuditDetail, ts[2].Detail.Audit.AuditID = nil, nil
		}), recording.Options{}, "judge is the mock"},
		"mixed backends": {set(func(ts []recording.Trace) { ts[0] = recordingtest.Mock(ts[0]) }), recording.Options{}, "mix decision backends"},
		"mixed judges": {set(func(ts []recording.Trace) {
			ts[0].Detail.AuditDetail.Judge = json.RawMessage(`{"effective_provider":"openrouter","model":"anthropic/claude-haiku-4.5"}`)
		}), recording.Options{}, "mix judges"},
		"legacy id": {set(func(ts []recording.Trace) { ts[1].Detail.TraceID = "legacy-1" }), recording.Options{}, "canonical UUID"},
		"urn id": {set(func(ts []recording.Trace) {
			ts[1].Detail.TraceID = "urn:uuid:" + ts[1].Detail.TraceID
		}), recording.Options{}, "canonical UUID"},
		"no decision model": {noEvidence(), recording.Options{Judge: &recording.Judge{Provider: "anthropic", Model: "claude-haiku-4-5"}}, "no trace was checked"},
		"no judge":          {noEvidence(), recording.Options{}, "--judge-provider"},
		"judge flag clash":  {recordingtest.Set(t), recording.Options{Judge: &recording.Judge{Provider: "openai", Model: "gpt-4o"}}, "were judged by anthropic"},
	} {
		_, err := recording.Build(c.traces, c.opt)
		if err == nil || !strings.Contains(err.Error(), c.want) || !errors.Is(err, recording.ErrNotRecordable) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if errors.Is(err, recording.ErrNoJudge) != (name == "no judge") {
			t.Errorf("%s: ErrNoJudge = %t", name, errors.Is(err, recording.ErrNoJudge))
		}
	}
}

// TestBuildDefaultJudge: the default judge (GET /api/export/replay: the engine's judge) names the
// judge only when no audit and no flag does, and is never checked against the audits.
func TestBuildDefaultJudge(t *testing.T) {
	def := &recording.Judge{Provider: "openrouter", Model: "anthropic/claude-haiku-4.5"}
	unaudited := recordingtest.Set(t)
	for i := range unaudited {
		unaudited[i].Detail.AuditDetail, unaudited[i].Detail.Audit.AuditID, unaudited[i].LLMUSD = nil, nil, nil
	}
	rec, _ := build(t, unaudited, recording.Options{GitSHA: "abc1234", Version: "1.0.0", DefaultJudge: def})
	if rec.Judge.Provider != def.Provider || rec.Judge.Model != def.Model || !strings.Contains(rec.Label, "anthropic/claude-haiku-4.5") {
		t.Errorf("unaudited: judge %+v label %q", rec.Judge, rec.Label)
	}
	// Audits name their judge: the default is ignored, not a clash.
	rec, _ = build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", DefaultJudge: def})
	if rec.Judge.Provider != "anthropic" {
		t.Errorf("audited: judge %+v", rec.Judge)
	}
	// A flag wins over the default.
	flag := &recording.Judge{Provider: "anthropic", Model: "claude-haiku-4-5"}
	rec, _ = build(t, unaudited, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Judge: flag, DefaultJudge: def})
	if rec.Judge.Provider != "anthropic" {
		t.Errorf("flag and default: judge %+v", rec.Judge)
	}
}
