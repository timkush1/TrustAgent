package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/recording/recordingtest"
	"github.com/truthtable/backend-go/internal/store"
)

const exportAdminKey = "export-admin-secret"

// seedRecordable records a trafficgen trace checked by the mock decision model, with its body,
// and (audited) its deep audit by the mock judge. Its question mentions an email address.
func seedRecordable(t *testing.T, pg *store.PostgresStore, at time.Time, answerSource string, audited bool) string {
	t.Helper()
	ctx := context.Background()
	id, pv := store.NewID(), int64(3)
	s := func(v string) *string { return &v }
	tr := &store.TraceRecord{
		TraceID: id, CreatedAt: at, Source: "trafficgen", APIFormat: "openai_chat", Path: "/v1/chat/completions",
		Route: "support-bot", PolicyVersion: &pv, Mode: "flag", KeyLabel: s("trafficgen"), Model: s("gpt-4o-mini"),
		AnswerSource: s(answerSource), QuestionPreview: s("Mail jane@acme-corp.com: can I still get a refund?"),
		Flags: []string{"test_mode", "mock_decision"},
	}
	if err := pg.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(recordingtest.Fixture(t, "refund-contradiction-flag").Decisions[0], &doc); err != nil {
		t.Fatal(err)
	}
	doc["trace_id"] = id
	raw, _ := json.Marshal(doc)
	action, would, verdict, risk, gateMs, cost := "flag", "block", "contradicted", 0.98, int64(41), 0.0000257
	tr.EffectiveMode, tr.Action, tr.WouldAction, tr.GateStatus = s("flag"), &action, &would, "ok"
	tr.Verdict, tr.MaxRisk, tr.Reasons = &verdict, &risk, []string{"contradiction:critical"}
	tr.DecisionBackend, tr.DecisionSnapshot, tr.DecisionCostUSD, tr.GateMs = s("mock"), s("mock/jev-heuristic-v1"), &cost, &gateMs
	if audited {
		tr.AuditID, tr.AuditStatus, tr.AuditLane, tr.AuditSelection = s(store.NewID()), "queued", s("high"), s("high")
	}
	dec := &store.DecisionRecord{TraceID: id, PolicyVersion: &pv, Route: "support-bot", Mode: "flag", EffectiveMode: "flag",
		Status: "ok", Action: action, WouldAction: &would, Verdict: &verdict, MaxRisk: &risk, Calibration: "none",
		Reasons: tr.Reasons, Decision: raw, DecisionBackend: s("mock"), GateMs: &gateMs}
	body := &store.BodyRecord{TraceID: id, Messages: json.RawMessage(`[{"role":"user","content":"Can I get a refund?"}]`),
		ResponseText: s("Within 60 days."), Rules: map[string]string{}, Bytes: 40, InclusionProb: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if err := pg.RecordDecision(ctx, tr, dec, body); err != nil {
		t.Fatal(err)
	}
	if audited {
		score, grade := 0.0, "F"
		a := &store.AuditRecord{AuditID: *tr.AuditID, TraceID: id, UserQuery: s("Can I get a refund?"), LLMResponse: s("Within 60 days."),
			Model: "gpt-4o-mini", FaithfulnessScore: &score, Grade: &grade, Source: "trafficgen", Status: store.AuditCompleted,
			Verdict: s(store.VerdictHallucinated), ScoringVersion: "v2", CreatedAt: at.Add(time.Second),
			Judge: json.RawMessage(`{"configured_provider":"mock","effective_provider":"mock","model":"mock-judge","fallback_active":false,"fallback_reason":null,"structured_outputs":false,"healthy":true,"credential_source":"none"}`)}
		if err := pg.SaveAuditResult(ctx, a, store.AuditCompleted); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestExportReplayNeedsAdminAndTheDatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: exportAdminKey})
	if code, body := env.get("/api/export/replay", exportAdminKey); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("without a database: %d %s", code, body)
	}
	// The ingest key reads content but is not an admin.
	if code, body := env.get("/api/export/replay", testKey); code != http.StatusForbidden || errorCodeOf(t, body) != codeForbidden {
		t.Errorf("a non-admin key: %d %s", code, body)
	}
	if code, _ := env.get("/api/export/replay", ""); code != http.StatusUnauthorized {
		t.Errorf("without a key: %d", code)
	}
}

func TestExportReplay(t *testing.T) {
	env, pg := pgEnv(t, envOpts{adminKey: exportAdminKey})
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	seedRecordable(t, pg, base.Add(-48*time.Hour), "scripted", true) // outside since
	first := seedRecordable(t, pg, base, "scripted", true)
	second := seedRecordable(t, pg, base.Add(5*time.Second), "generated", false)

	since := "?since=" + url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339Nano))
	gitArg := "&git_sha=0123abcd4567"
	if gitRevision.MatchString(buildSHA()) {
		gitArg = "" // a build that knows its revision names it itself
	} else {
		code, body := env.get("/api/export/replay"+since, exportAdminKey)
		if code != http.StatusConflict || errorCodeOf(t, body) != codeConflict || !strings.Contains(string(body), "git_sha") {
			t.Errorf("a build without its revision and no git_sha: %d %s", code, body)
		}
		if code, body := env.get("/api/export/replay"+since+"&git_sha=HEAD", exportAdminKey); code != http.StatusBadRequest ||
			errorCodeOf(t, body) != codeBadRequest {
			t.Errorf("git_sha=HEAD: %d %s", code, body)
		}
	}

	code, body := env.get("/api/export/replay"+since+"&source=trafficgen"+gitArg, exportAdminKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	m := recordingtest.ValidateManifest(t, body)
	if m["kind"] != recording.KindMock || m["label"] != recording.MockLabel || m["schema"] != recording.Schema {
		t.Errorf("manifest kind %v label %v", m["kind"], m["label"])
	}
	if gitArg != "" && m["git_sha"] != "0123abcd4567" {
		t.Errorf("git_sha %v", m["git_sha"])
	}
	traces, _ := m["traces"].([]any)
	if len(traces) != 2 || traces[0].(map[string]any)["trace_id"] != first || traces[1].(map[string]any)["trace_id"] != second {
		t.Errorf("traces %v", traces)
	}
	if judge, _ := m["judge"].(map[string]any); judge["provider"] != "mock" {
		t.Errorf("judge %v", m["judge"])
	}
	// Sanitized: the email in the question preview is masked.
	if strings.Contains(string(body), "jane@acme-corp.com") || strings.Contains(string(body), "acme-corp") {
		t.Error("the manifest leaks an email address")
	}

	// limit keeps the newest traces: here only the unaudited one, and the test engine names no judge.
	if code, body := env.get("/api/export/replay"+since+"&limit=1"+gitArg, exportAdminKey); code != http.StatusConflict ||
		errorCodeOf(t, body) != codeConflict || !strings.Contains(string(body), "names its judge") || strings.Contains(string(body), "--judge") {
		t.Errorf("limit=1: %d %s", code, body)
	}
	until := "&until=" + url.QueryEscape(base.Add(time.Second).Format(time.RFC3339Nano))
	if code, body := env.get("/api/export/replay"+since+until+"&limit=1"+gitArg, exportAdminKey); code != http.StatusOK {
		t.Errorf("until: %d %s", code, body)
	} else if m := recordingtest.ValidateManifest(t, body); len(m["traces"].([]any)) != 1 ||
		m["traces"].([]any)[0].(map[string]any)["trace_id"] != first {
		t.Errorf("until: %v", m["traces"])
	}
	// Nothing to record, or nothing recordable: 409 with the reason.
	if code, body := env.get("/api/export/replay"+since+"&source=proxy"+gitArg, exportAdminKey); code != http.StatusConflict ||
		errorCodeOf(t, body) != codeConflict || !strings.Contains(string(body), "no traces match") {
		t.Errorf("no traces: %d %s", code, body)
	}
	for _, bad := range []string{"?limit=0", "?limit=201", "?limit=many", "?since=yesterday", "?until=never", "?source=cron",
		"?route=Bad%20Route"} {
		code, body := env.get("/api/export/replay"+bad+gitArg, exportAdminKey)
		if code != http.StatusBadRequest || errorCodeOf(t, body) != codeBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
}

// fakeEngineHealth is a cached engine health for engineJudge.
type fakeEngineHealth struct{ h grpc.EngineHealth }

func (f fakeEngineHealth) Health() grpc.EngineHealth { return f.h }

func TestEngineJudge(t *testing.T) {
	judge := func(r *pb.HealthResponse) *recording.Judge {
		s := &server{status: &statusReporter{engine: fakeEngineHealth{grpc.EngineHealth{State: "up", Response: r}}}}
		return s.engineJudge()
	}
	if j := judge(&pb.HealthResponse{JudgeProvider: "anthropic", JudgeModel: "claude-haiku-4-5", JudgeFallbackActive: true}); j == nil ||
		j.Provider != "anthropic" || j.Model != "claude-haiku-4-5" || !j.FallbackActive {
		t.Errorf("anthropic judge: %+v", j)
	}
	for name, r := range map[string]*pb.HealthResponse{
		"no answer": nil, "no judge": {JudgeProvider: "none"}, "unnamed": {}, "no model": {JudgeProvider: "anthropic"},
	} {
		if j := judge(r); j != nil {
			t.Errorf("%s: %+v", name, j)
		}
	}
	if j := (&server{status: &statusReporter{}}).engineJudge(); j != nil {
		t.Errorf("no engine: %+v", j)
	}
}
