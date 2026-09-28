package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

// fakeQueue is an audit queue that answers Submit with lane and err.
type fakeQueue struct {
	engine bool
	lane   string
	err    error
	jobs   []*worker.Job
}

func (q *fakeQueue) Submit(j *worker.Job) (string, error) {
	q.jobs = append(q.jobs, j)
	return q.lane, q.err
}

func (q *fakeQueue) HasEngine() bool { return q.engine }

type events struct {
	mu  sync.Mutex
	evs []websocket.Event
}

func (e *events) Publish(ev websocket.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

func (e *events) types() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.evs {
		out = append(out, ev.Type)
	}
	return out
}

// flaggedResult is a flag-mode result whose deep audit the gate selected for the high lane.
func flaggedResult() *gate.Result {
	risk, verdict := 0.98, "contradicted"
	pv, gateMs := int64(1), int64(4)
	trace := &gate.TraceStart{
		TraceID: store.NewID(), CreatedAt: time.Now().UTC(), Source: gate.SourceTrafficgen, APIFormat: "openai_chat",
		Path: "/v1/chat/completions", Route: "support-bot", PolicyVersion: 1, Mode: "flag", Model: "gpt-4o-mini",
		Scenario: "refund-contradiction-flag", AnswerSource: gate.AnswerScripted, Flags: []string{gate.FlagTestMode},
		UserRequest: "Can I get a refund?",
	}
	return &gate.Result{
		Trace: trace,
		Decision: &gate.Decision{
			Spec: gate.DecisionSpec, TraceID: trace.TraceID, CreatedAt: trace.CreatedAt, Route: "support-bot", PolicyVersion: &pv,
			Mode: "flag", Fail: "open", GateMs: &gateMs,
			Core: decision.Core{
				Status: decision.StatusOK, EffectiveMode: "flag", Action: decision.ActionFlag, WouldAction: decision.ActionBlock,
				Enforced: true, Verdict: &verdict, MaxRisk: &risk, Calibration: decision.CalibrationNone,
				DeepAudit: decision.DeepAuditHigh, Reasons: []string{"contradiction:critical"}, Evaluators: decision.EvaluatorResults{},
			},
			Detectors: []decision.DetectorResult{}, Excluded: []decision.Exclusion{},
		},
		Status: "ok", RouteLabel: "support-bot@v1", CheckedAgainst: "request_system:1",
		Evidence: []evidence.Item{
			{ID: "refund-policy.md", Source: "request_system", Label: "refund-policy.md", Text: "Annual plans: 30 days."},
			{ID: "tool-1", Source: "tool_results", Label: "tool-1", Text: "untrusted", Untrusted: true},
		},
		UserRequest: "Can I get a refund?", ResponseText: "Within 60 days.",
		Messages: []gate.ChatMessage{{Role: "user", Content: "Can I get a refund?"}},
		Audit:    gate.AuditPlan{Selection: gate.SelectionHigh, Lane: gate.LaneHigh, InclusionProb: 1, MockScenario: "refund-contradiction-flag"},
		Body:     gate.BodyPlan{Store: true, InclusionProb: 1},
		Flags:    []string{gate.FlagTestMode},
	}
}

func newSink(st traceStore, q auditQueue, pub *events) *traceSink {
	s := &traceSink{pub: pub, bodyRetention: time.Hour, bodyMaxBytes: 1 << 20, now: time.Now, log: slog.New(slog.DiscardHandler)}
	if st != nil {
		s.store = st
	}
	if q != nil {
		s.audits = q
	}
	return s
}

func TestSinkQueuesTheSelectedAudit(t *testing.T) {
	st, pub, q := newMemStore(), &events{}, &fakeQueue{engine: true, lane: gate.LaneHigh}
	s := newSink(st, q, pub)
	r := flaggedResult()
	s.TraceStarted(context.Background(), r.Trace)
	if got := s.GateDecided(context.Background(), r); got != gate.AuditQueued {
		t.Fatalf("audit %q", got)
	}
	if strings.Join(pub.types(), ",") != "trace_started,gate_decision" {
		t.Errorf("events %v", pub.types())
	}
	tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID)
	if tr.AuditStatus != gate.AuditQueued || tr.AuditID == nil || *tr.AuditLane != gate.LaneHigh || tr.GateStatus != "ok" {
		t.Errorf("trace %+v", tr)
	}
	req := q.jobs[0].Request
	if req.GetAuditId() != *tr.AuditID || req.GetTraceId() != r.Trace.TraceID || req.GetQuery() != "Can I get a refund?" ||
		req.GetOptions().GetMockScenario() != "refund-contradiction-flag" || req.GetPriority() != "high" {
		t.Errorf("audit request %v", req)
	}
	if len(req.GetContext()) != 1 || req.GetContext()[0].GetId() != "refund-policy.md" {
		t.Errorf("untrusted evidence is not audit context: %v", req.GetContext())
	}
	var doc map[string]any
	if err := json.Unmarshal(req.GetGate().GetDecisionJson(), &doc); err != nil || doc["spec"] != "decision/v1" {
		t.Errorf("decision_json %s (%v)", req.GetGate().GetDecisionJson(), err)
	}
	if b, err := st.GetTraceBody(context.Background(), r.Trace.TraceID); err != nil || *b.ResponseText != "Within 60 days." {
		t.Errorf("body %+v %v", b, err)
	}
}

func TestSinkRecordsDroppedAndDisabledAudits(t *testing.T) {
	// A full queue: the trace records dropped and the stream gets audit_dropped.
	st, pub := newMemStore(), &events{}
	s := newSink(st, &fakeQueue{engine: true, err: worker.ErrQueueFull}, pub)
	r := flaggedResult()
	s.TraceStarted(context.Background(), r.Trace)
	if got := s.GateDecided(context.Background(), r); got != gate.AuditDropped {
		t.Fatalf("audit %q", got)
	}
	tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID)
	if tr.AuditStatus != store.TraceAuditDropped {
		t.Errorf("trace audit status %q", tr.AuditStatus)
	}
	if types := pub.types(); types[len(types)-1] != "audit_dropped" {
		t.Errorf("events %v", types)
	}

	// The high lane was at its rate: the audit waits in the low lane.
	st = newMemStore()
	s = newSink(st, &fakeQueue{engine: true, lane: gate.LaneLow}, &events{})
	r = flaggedResult()
	s.GateDecided(context.Background(), r)
	if tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID); *tr.AuditLane != gate.LaneLow {
		t.Errorf("lane %q", *tr.AuditLane)
	}

	// No engine: disabled, nothing submitted. Not selected: the plan's status.
	for _, q := range []*fakeQueue{{engine: false}, nil} {
		s = newSink(newMemStore(), nil, &events{})
		if q != nil {
			s.audits = q
		}
		if got := s.GateDecided(context.Background(), flaggedResult()); got != gate.AuditDisabled {
			t.Errorf("without an engine: %q", got)
		}
	}
	r = flaggedResult()
	r.Audit = gate.AuditPlan{Status: gate.AuditNotAssessed}
	q := &fakeQueue{engine: true}
	s = newSink(nil, q, &events{})
	if got := s.GateDecided(context.Background(), r); got != gate.AuditNotAssessed || len(q.jobs) != 0 {
		t.Errorf("not selected: %q, %d jobs", got, len(q.jobs))
	}
}

func TestBodyRecordCapsTheStoredTexts(t *testing.T) {
	r := flaggedResult()
	r.ResponseText = strings.Repeat("é", 400)
	b := bodyRecord(r, time.Now(), 200)
	if !b.Truncated || b.Bytes > 200+8 || !utf8.ValidString(*b.ResponseText) {
		t.Errorf("capped body: truncated=%v bytes=%d", b.Truncated, b.Bytes)
	}
	var items []evidence.Item
	_ = json.Unmarshal(b.Evidence, &items)
	if len(items) != 2 {
		t.Errorf("evidence %s", b.Evidence)
	}
	if full := bodyRecord(flaggedResult(), time.Now(), 1<<20); full.Truncated || *full.ResponseText != "Within 60 days." {
		t.Errorf("uncapped body %+v", full)
	}
}

func TestAuditContextLimits(t *testing.T) {
	var items []evidence.Item
	for i := range maxAuditDocs + 3 {
		items = append(items, evidence.Item{ID: "d" + string(rune('a'+i)), Source: "request_context", Text: "x"})
	}
	items[0].Text = strings.Repeat("ü", maxAuditDocRunes+5)
	docs, used := auditContext(items)
	if len(docs) != maxAuditDocs || len(used) != maxAuditDocs {
		t.Fatalf("%d docs", len(docs))
	}
	if utf8.RuneCountInString(docs[0].GetContent()) != maxAuditDocRunes || !docs[0].GetTruncated() || !used[0].Truncated {
		t.Errorf("first doc not cut at %d runes", maxAuditDocRunes)
	}

	r := flaggedResult()
	r.UserRequest = ""
	r.Messages = []gate.ChatMessage{{Role: "user", Content: "first"}, {Role: "assistant", Content: "a"}, {Role: "user", Content: "last"}}
	if q := lastUserTurn(r); q != "last" {
		t.Errorf("query %q", q)
	}
}
