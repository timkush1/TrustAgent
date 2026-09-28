// Package recordingtest builds recording inputs from the contract fixtures and validates written
// recordings against the contract schemas. It is imported by tests only.
package recordingtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/store"
)

// Schemas a recording is validated against.
const (
	SchemaRecording   = "recording.v1.schema.json"
	SchemaTraceDetail = "api.v1.schema.json#/$defs/TraceDetail"
	SchemaWsEvent     = "api.v1.schema.json#/$defs/WsEvent"
)

// Fixture loads contracts/fixtures/api/TraceDetail.<name>.json.
func Fixture(t testing.TB, name string) store.TraceDetail {
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

// Variant copies a trace detail with a new id, creation time and answer source; its decisions and
// deep audit follow the new id and time.
func Variant(t testing.TB, d store.TraceDetail, id string, at time.Time, answerSource string) recording.Trace {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var c store.TraceDetail
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	c.TraceID, c.CreatedAt, c.UpdatedAt = id, at, at.Add(2*time.Second)
	c.AnswerSource = &answerSource
	for i, raw := range c.Decisions {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		m["trace_id"] = id
		if c.Decisions[i], err = json.Marshal(m); err != nil {
			t.Fatal(err)
		}
	}
	if c.AuditDetail != nil {
		c.AuditDetail.TraceID, c.AuditDetail.CreatedAt = id, at.Add(time.Second)
	}
	return recording.Trace{Detail: c}
}

// Mock turns a trace into one checked by the mock decision backend and judged by the mock judge.
func Mock(tr recording.Trace) recording.Trace {
	d := tr.Detail
	if d.DecisionModel != nil {
		dm := *d.DecisionModel
		snap := "mock/jev-heuristic-v1"
		dm.Backend, dm.Snapshot = "mock", &snap
		d.DecisionModel = &dm
	}
	if d.AuditDetail != nil {
		a := *d.AuditDetail
		a.Judge = json.RawMessage(`{"configured_provider":"mock","effective_provider":"mock","model":"mock-judge","fallback_active":false,"fallback_reason":null,"structured_outputs":false,"healthy":true,"credential_source":"none"}`)
		d.AuditDetail = &a
	}
	return recording.Trace{Detail: d, LLMUSD: tr.LLMUSD}
}

// Set is three traces on 2026-10-05, deliberately out of order: a generated answer flagged with a
// deep audit at 10:00:40 (LLM cost $0.0021), a scripted no-evidence answer at 10:00:00 and a
// scripted flagged answer with an audit at 10:01:30, all on openrouter-decisions and judged by
// anthropic claude-haiku-4-5.
func Set(t testing.TB) []recording.Trace {
	t.Helper()
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	refund, noEv := Fixture(t, "refund-contradiction-flag"), Fixture(t, "no-evidence-not-assessed")
	cost := 0.0021
	a := Variant(t, refund, "01a0e780-fe6b-74b8-a21f-000000000002", base.Add(40*time.Second), "generated")
	a.LLMUSD = &cost
	return []recording.Trace{
		a,
		Variant(t, noEv, "01a0e780-fe6b-74b8-a21f-000000000001", base, "scripted"),
		Variant(t, refund, "01a0e780-fe6b-74b8-a21f-000000000003", base.Add(90*time.Second), "scripted"),
	}
}

// ValidateManifest validates a manifest against recording.v1 and each of its events, wrapped in a
// WebSocket v2 envelope, against WsEvent. It returns the decoded manifest.
func ValidateManifest(t testing.TB, b []byte) map[string]any {
	t.Helper()
	contractstest.ValidateJSON(t, SchemaRecording, b)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	events, _ := m["events"].([]any)
	for i, e := range events {
		ev := e.(map[string]any)
		env := map[string]any{"v": 2, "seq": i + 1, "epoch": "replay", "type": ev["type"], "ts": "2026-10-05T10:00:00Z", "data": ev["data"]}
		if id, ok := ev["trace_id"]; ok {
			env["trace_id"] = id
		}
		eb, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, SchemaWsEvent, eb)
	}
	return m
}

// ValidateDir validates a written recording: manifest.json and traces/<id>.json for every trace
// the manifest lists. It returns the decoded manifest.
func ValidateDir(t testing.TB, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := ValidateManifest(t, b)
	for _, tr := range m["traces"].([]any) {
		id := tr.(map[string]any)["trace_id"].(string)
		tb, err := os.ReadFile(filepath.Join(dir, "traces", id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, SchemaTraceDetail, tb)
	}
	return m
}
