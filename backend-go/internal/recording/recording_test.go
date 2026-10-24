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

// vendorHosts are the hosts of a real recording: the fast check and the generated answers through
// OpenRouter, the fixtures' anthropic judge at Anthropic.
var vendorHosts = recording.Hosts{Decision: "openrouter.ai", Judge: "api.anthropic.com", Generated: "openrouter.ai"}

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
	rec, _ := build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
	want := "Recorded 2026-10-05 · typesafe/jev-1.13-20260917 + anthropic claude-haiku-4-5 · scripted and generated answers · replayed, not live"
	if rec.Kind != recording.KindRecorded || rec.Label != want {
		t.Errorf("kind %s, label %q", rec.Kind, rec.Label)
	}
	if rec.Traces[0].TraceID != "01a0e780-fe6b-74b8-a21f-000000000001" || rec.AnswerSources != (recording.AnswerSources{Scripted: 2, Generated: 1, GeneratedHost: "openrouter.ai"}) {
		t.Errorf("traces not oldest first or counts wrong: %s, %+v", rec.Traces[0].TraceID, rec.AnswerSources)
	}
	if rec.Totals.Traces != 3 || rec.Totals.LLMUSD != 0.0021 || rec.Totals.DecisionUSD < 5e-5 || rec.ROI.Window != "15m" || rec.ROI.Simulated ||
		rec.ROI.GatedResponses != 3 || rec.Judge != (recording.Judge{Provider: "anthropic", Model: "claude-haiku-4-5", Host: "api.anthropic.com"}) || rec.CalibrationState != "none" ||
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

// TestRecordedLabelNamesTheAnswerSources: the recorded label names the answer sources its traces
// carry, so a recording run whose answers came from the upstream (stamped generated in test mode,
// D121) says "generated answers", never "live".
func TestRecordedLabelNamesTheAnswerSources(t *testing.T) {
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	refund := recordingtest.Fixture(t, "refund-contradiction-flag")
	for _, c := range []struct {
		sources []string
		want    string
		counts  recording.AnswerSources
	}{
		{[]string{"generated", "generated"}, "generated answers", recording.AnswerSources{Generated: 2, GeneratedHost: "openrouter.ai"}},
		{[]string{"scripted"}, "scripted answers", recording.AnswerSources{Scripted: 1}},
		{[]string{"generated", "scripted", "generated"}, "scripted and generated answers", recording.AnswerSources{Scripted: 1, Generated: 2, GeneratedHost: "openrouter.ai"}},
		{[]string{"live", "generated"}, "generated and live answers", recording.AnswerSources{Generated: 1, GeneratedHost: "openrouter.ai"}},
	} {
		var traces []recording.Trace
		for i, source := range c.sources {
			id := "01a0e780-fe6b-74b8-a21f-00000000010" + string(rune('0'+i))
			traces = append(traces, recordingtest.Variant(t, refund, id, base.Add(time.Duration(i)*time.Minute), source))
		}
		rec, _ := build(t, traces, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
		want := "Recorded 2026-10-05 · typesafe/jev-1.13-20260917 + anthropic claude-haiku-4-5 · " + c.want + " · replayed, not live"
		if rec.Kind != recording.KindRecorded || rec.Label != want || rec.AnswerSources != c.counts {
			t.Errorf("%v: kind %s, label %q, counts %+v; want %q, %+v", c.sources, rec.Kind, rec.Label, rec.AnswerSources, want, c.counts)
		}
	}
}

func TestBuildMock(t *testing.T) {
	var traces []recording.Trace
	for _, tr := range recordingtest.Set(t) {
		traces = append(traces, recordingtest.Mock(tr))
	}
	stand := recording.Hosts{Decision: "mock-upstream", Generated: "mock-upstream"}
	rec, m := build(t, traces, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: stand})
	if rec.Kind != recording.KindMock || rec.Label != recording.MockLabel || !rec.ROI.Simulated || rec.DecisionModel.Backend != "mock" || m["kind"] != "mock" {
		t.Errorf("recording = %+v", rec)
	}
	// D203: the mock fast check and the mock judge call no vendor, so a simulated tape never names
	// a vendor host for them (an export told otherwise is refused, not relabelled).
	for _, c := range []struct {
		hosts recording.Hosts
		want  string
	}{
		{recording.Hosts{Decision: "openrouter.ai"}, "drop --decision-host openrouter.ai"},
		{recording.Hosts{Judge: "api.anthropic.com"}, "drop --judge-host api.anthropic.com"},
		{vendorHosts, "the fast check is the mock"},
	} {
		_, err := recording.Build(traces, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: c.hosts})
		if !errors.Is(err, recording.ErrNotRecordable) || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("hosts %+v: err = %v, want %q", c.hosts, err, c.want)
		}
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
	rec, _ := build(t, unaudited, recording.Options{GitSHA: "abc1234", Version: "1.0.0", DefaultJudge: def, Hosts: vendorHosts})
	if rec.Judge.Provider != def.Provider || rec.Judge.Model != def.Model || !strings.Contains(rec.Label, "anthropic/claude-haiku-4.5") {
		t.Errorf("unaudited: judge %+v label %q", rec.Judge, rec.Label)
	}
	// Audits name their judge: the default is ignored, not a clash.
	rec, _ = build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", DefaultJudge: def, Hosts: vendorHosts})
	if rec.Judge.Provider != "anthropic" {
		t.Errorf("audited: judge %+v", rec.Judge)
	}
	// A flag wins over the default.
	flag := &recording.Judge{Provider: "anthropic", Model: "claude-haiku-4-5"}
	rec, _ = build(t, unaudited, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Judge: flag, DefaultJudge: def, Hosts: vendorHosts})
	if rec.Judge.Provider != "anthropic" {
		t.Errorf("flag and default: judge %+v", rec.Judge)
	}
}

// TestBuildHosts: a tape is recorded only when the fast check, the judge and (with generated
// answers) the upstream called vendor hosts (D203). mock-upstream, an unknown host or a gateway
// in between keeps it from the recorded label, with the reason non_vendor_host; the hosts survive
// the sanitizer as bare names.
func TestBuildHosts(t *testing.T) {
	rec, m := build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
	if rec.Kind != recording.KindRecorded || rec.DecisionModel.Host != "openrouter.ai" || rec.Judge.Host != "api.anthropic.com" ||
		rec.AnswerSources.GeneratedHost != "openrouter.ai" {
		t.Fatalf("vendor hosts: %+v %+v %+v", rec.DecisionModel, rec.Judge, rec.AnswerSources)
	}
	dm, _ := m["decision_model"].(map[string]any)
	judge, _ := m["judge"].(map[string]any)
	sources, _ := m["answer_sources"].(map[string]any)
	if dm["host"] != "openrouter.ai" || judge["host"] != "api.anthropic.com" || sources["generated_host"] != "openrouter.ai" {
		t.Errorf("the sanitized manifest lost its hosts: %v %v %v", dm, judge, sources)
	}

	for name, c := range map[string]struct {
		hosts recording.Hosts
		want  string
	}{
		"mock-upstream decision": {recording.Hosts{Decision: "mock-upstream", Judge: "api.anthropic.com", Generated: "openrouter.ai"}, "the fast check called mock-upstream"},
		"mock-upstream judge":    {recording.Hosts{Decision: "openrouter.ai", Judge: "mock-upstream", Generated: "openrouter.ai"}, "the judge called mock-upstream"},
		"mock-upstream answers":  {recording.Hosts{Decision: "openrouter.ai", Judge: "api.anthropic.com", Generated: "mock-upstream"}, "generated answers called mock-upstream"},
		"a gateway":              {recording.Hosts{Decision: "ai-gateway.vercel.sh", Judge: "api.anthropic.com", Generated: "openrouter.ai"}, "not a vendor host"},
		"unknown decision host":  {recording.Hosts{Judge: "api.anthropic.com", Generated: "openrouter.ai"}, "fast check host is unknown"},
		"unknown judge host":     {recording.Hosts{Decision: "openrouter.ai", Generated: "openrouter.ai"}, "judge host is unknown"},
		"unknown upstream host":  {recording.Hosts{Decision: "openrouter.ai", Judge: "api.anthropic.com"}, "generated answers host is unknown"},
	} {
		_, err := recording.Build(recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: c.hosts})
		if !errors.Is(err, recording.ErrNotRecordable) || !strings.Contains(err.Error(), "non_vendor_host") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want non_vendor_host and %q", name, err, c.want)
		}
	}

	// Scripted answers only: no upstream was called, so none is named or needed.
	refund := recordingtest.Fixture(t, "refund-contradiction-flag")
	scripted := []recording.Trace{recordingtest.Variant(t, refund, "01a0e780-fe6b-74b8-a21f-000000000201", time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), "scripted")}
	rec, m = build(t, scripted, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: recording.Hosts{Decision: "openrouter.ai", Judge: "api.anthropic.com", Generated: "mock-upstream"}})
	if sources, _ := m["answer_sources"].(map[string]any); rec.Kind != recording.KindRecorded || rec.AnswerSources.GeneratedHost != "" || sources["generated_host"] != nil {
		t.Errorf("scripted only: kind %s, answer sources %+v", rec.Kind, rec.AnswerSources)
	}

	// A mock tape may name any host (it is labelled simulated whatever it called).
	var mock []recording.Trace
	for _, tr := range recordingtest.Set(t) {
		mock = append(mock, recordingtest.Mock(tr))
	}
	rec, _ = build(t, mock, recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: recording.Hosts{Decision: "mock-upstream", Judge: "mock-upstream", Generated: "mock-upstream"}})
	if rec.Kind != recording.KindMock || rec.DecisionModel.Host != "mock-upstream" {
		t.Errorf("mock tape: %s %+v", rec.Kind, rec.DecisionModel)
	}

	// A URL or a host with a port is a caller's mistake, not a label question.
	for _, bad := range []string{"https://openrouter.ai", "openrouter.ai:443", "OpenRouter.ai", "openrouter.ai/api"} {
		_, err := recording.Build(recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: recording.Hosts{Decision: bad, Judge: "api.anthropic.com", Generated: "openrouter.ai"}})
		if err == nil || errors.Is(err, recording.ErrNotRecordable) || !strings.Contains(err.Error(), "bare") {
			t.Errorf("host %q: err = %v", bad, err)
		}
	}
}

// TestBuildFeatured: the featured trace is picked by rule and never by editing the tape (D209).
func TestBuildFeatured(t *testing.T) {
	opt := recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts}
	// Set: ...01 no evidence (passed), ...02 a flagged refund contradiction (support-bot), ...03
	// the same scripted. Without scenario ids the first support-bot candidate wins.
	rec, m := build(t, recordingtest.Set(t), opt)
	if rec.Featured == nil || *rec.Featured != (recording.Featured{TraceID: "01a0e780-fe6b-74b8-a21f-000000000002", Rule: recording.RuleContradictedWithSpan}) {
		t.Errorf("featured = %+v", rec.Featured)
	}
	if f, _ := m["featured"].(map[string]any); f["rule"] != "contradicted_with_span" {
		t.Errorf("manifest featured = %v", m["featured"])
	}

	// The refund contradiction scenario (trafficgen's X-Request-ID) comes first, wherever it is.
	ts := recordingtest.Set(t)
	id := "trafficgen:refund-contradiction-flag:7"
	ts[2].Detail.ClientRequestID = &id
	rec, _ = build(t, ts, opt)
	if rec.Featured == nil || rec.Featured.TraceID != "01a0e780-fe6b-74b8-a21f-000000000003" {
		t.Errorf("scenario preference: featured = %+v", rec.Featured)
	}
	if got := recording.ScenarioOf(ts[2].Detail.TraceSummary); got != "refund-contradiction-flag" {
		t.Errorf("ScenarioOf = %q", got)
	}
	for _, other := range []string{"req_9Kd4sT2w", "trafficgen:x", "other:refund-contradiction-flag:1"} {
		s := store.TraceSummary{ClientRequestID: &other}
		if got := recording.ScenarioOf(s); got != "" {
			t.Errorf("ScenarioOf(%q) = %q", other, got)
		}
	}

	// Another route's candidate counts only when no support-bot trace does.
	ts = recordingtest.Set(t)
	ts[0].Detail.Route = "payments-faq"
	rec, _ = build(t, ts, opt)
	if rec.Featured == nil || rec.Featured.TraceID != "01a0e780-fe6b-74b8-a21f-000000000003" {
		t.Errorf("route preference: featured = %+v", rec.Featured)
	}
	ts[2].Detail.Route = "payments-faq"
	rec, _ = build(t, ts, opt)
	if rec.Featured == nil || rec.Featured.TraceID != "01a0e780-fe6b-74b8-a21f-000000000002" {
		t.Errorf("first candidate: featured = %+v", rec.Featured)
	}

	// No trace meets the rule (a pass, or a contradiction without a span): featured is left out
	// and the tape is still recorded; the owner reviews it.
	ts = recordingtest.Set(t)
	pass := "pass"
	ts[0].Detail.Action = &pass
	for i := range ts[2].Detail.AuditDetail.Claims {
		ts[2].Detail.AuditDetail.Claims[i].Span = nil
	}
	rec, m = build(t, ts, opt)
	if rec.Kind != recording.KindRecorded || rec.Featured != nil || m["featured"] != nil {
		t.Errorf("no candidate: kind %s, featured %+v", rec.Kind, rec.Featured)
	}
	// The owner's pick (best_flagged) must be a flagged trace of the tape.
	named := opt
	named.Featured = "01a0e780-fe6b-74b8-a21f-000000000003"
	rec, _ = build(t, ts, named)
	if rec.Featured == nil || *rec.Featured != (recording.Featured{TraceID: named.Featured, Rule: recording.RuleBestFlagged}) {
		t.Errorf("owner's pick: featured = %+v", rec.Featured)
	}
	for pick, want := range map[string]string{"01a0e780-fe6b-74b8-a21f-000000000001": "not flagged", "01a0e780-fe6b-74b8-a21f-0000000000ff": "not in the recording"} {
		named.Featured = pick
		if _, err := recording.Build(ts, named); !errors.Is(err, recording.ErrNotRecordable) || !strings.Contains(err.Error(), want) {
			t.Errorf("pick %s: err = %v, want %q", pick, err, want)
		}
	}

	// A mock tape's pick is scripted.
	var mock []recording.Trace
	for _, tr := range recordingtest.Set(t) {
		mock = append(mock, recordingtest.Mock(tr))
	}
	rec, _ = build(t, mock, recording.Options{GitSHA: "abc1234", Version: "1.0.0"})
	if rec.Featured == nil || rec.Featured.Rule != recording.RuleScripted {
		t.Errorf("mock featured = %+v", rec.Featured)
	}
}

// TestBuildCalibration: the calibration the tape was recorded with is written as given (D207,
// D208); quality none is what ALLOW_UNCALIBRATED records.
func TestBuildCalibration(t *testing.T) {
	snap := "typesafe/jev-1.13-20260917"
	for _, c := range []recording.Calibration{{Snapshot: &snap, Quality: "measured"}, {Snapshot: &snap, Quality: "provisional"}, {Snapshot: nil, Quality: "none"}} {
		rec, m := build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts, Calibration: &c})
		got, _ := m["calibration"].(map[string]any)
		if rec.Calibration == nil || rec.Calibration.Quality != c.Quality || got["quality"] != c.Quality {
			t.Errorf("calibration %+v: written %v", c, m["calibration"])
		}
		if (c.Snapshot == nil) != (got["snapshot"] == nil) {
			t.Errorf("calibration %+v: snapshot written as %v", c, got["snapshot"])
		}
	}
	rec, m := build(t, recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts})
	if rec.Calibration != nil || m["calibration"] != nil {
		t.Errorf("no calibration given, but one written: %v", m["calibration"])
	}
	_, err := recording.Build(recordingtest.Set(t), recording.Options{GitSHA: "abc1234", Version: "1.0.0", Hosts: vendorHosts, Calibration: &recording.Calibration{Quality: "mock"}})
	if err == nil || !strings.Contains(err.Error(), "calibration quality") {
		t.Errorf("quality mock: err = %v", err)
	}
}
