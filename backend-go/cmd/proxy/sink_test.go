package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
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

// replacedResult is a fallback-mode result whose fallback answer went out (Result.Final).
func replacedResult() *gate.Result {
	r := flaggedResult()
	r.Trace.Mode, r.Trace.Route = decision.ModeFallback, "payments-faq"
	d := r.Decision
	d.Mode, d.EffectiveMode, d.Action, d.Route = decision.ModeFallback, decision.ModeFallback, decision.ActionFallback, "payments-faq"
	ttft := int64(120)
	r.Upstream = gate.Upstream{Status: 200, TTFTMs: &ttft}
	r.Final = &gate.Final{Action: decision.ActionFallback, Delivered: gate.DeliveredFallback,
		Text: "Annual plans can be refunded within 30 days.", Flags: []string{gate.FlagFallbackUsed}}
	return r
}

// decidedEvent decodes the last gate_decision event: the trace summary and the decision/v1 document.
func decidedEvent(t *testing.T, pub *events) (store.TraceSummary, gate.Decision) {
	t.Helper()
	pub.mu.Lock()
	defer pub.mu.Unlock()
	var p struct {
		Trace    store.TraceSummary `json:"trace"`
		Decision gate.Decision      `json:"decision"`
	}
	for i := len(pub.evs) - 1; i >= 0; i-- {
		if pub.evs[i].Type != "gate_decision" {
			continue
		}
		if err := json.Unmarshal(pub.evs[i].Data, &p); err != nil {
			t.Fatal(err)
		}
		return p.Trace, p.Decision
	}
	t.Fatal("no gate_decision event")
	return p.Trace, p.Decision
}

// storedDecisions returns a trace's stored decision/v1 documents, by attempt.
func storedDecisions(t *testing.T, st *memStore, traceID string) []gate.Decision {
	t.Helper()
	docs, err := st.TraceDecisions(context.Background(), traceID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]gate.Decision, len(docs))
	for i, doc := range docs {
		if err := json.Unmarshal(doc, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestSinkRecordsTheDeliveredOutcome(t *testing.T) {
	st, pub, q := newMemStore(), &events{}, &fakeQueue{engine: true, lane: gate.LaneHigh}
	s := newSink(st, q, pub)
	r := replacedResult()
	s.TraceStarted(context.Background(), r.Trace)
	s.GateDecided(context.Background(), r)
	tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID)
	if *tr.Action != decision.ActionFallback || !slices.Equal(tr.Flags, []string{gate.FlagTestMode, gate.FlagFallbackUsed}) ||
		tr.TTFTMs == nil || *tr.TTFTMs != 120 {
		t.Errorf("trace action %v flags %v ttft %v", tr.Action, tr.Flags, tr.TTFTMs)
	}
	b, _ := st.GetTraceBody(context.Background(), r.Trace.TraceID)
	if *b.ResponseText != "Within 60 days." || *b.FinalResponseText != "Annual plans can be refunded within 30 days." {
		t.Errorf("body texts %q / %q", *b.ResponseText, *b.FinalResponseText)
	}
	// The audit checks the original answer and knows what went out instead.
	job := q.jobs[0]
	if job.Request.GetResponse() != "Within 60 days." || job.Gate.Delivered != gate.DeliveredFallback ||
		job.Request.GetGate().GetDelivered() != gate.DeliveredFallback || job.Gate.Action != decision.ActionFallback {
		t.Errorf("audit gate %+v, request gate delivered %q", job.Gate, job.Request.GetGate().GetDelivered())
	}
	// The live stream shows the dashboard what the client got: the event's trace is the stored one.
	if sum, d := decidedEvent(t, pub); *sum.Action != decision.ActionFallback || !slices.Contains(sum.Flags, gate.FlagFallbackUsed) ||
		sum.Timings.TTFTMs == nil || *sum.Timings.TTFTMs != 120 || d.Action != decision.ActionFallback {
		t.Errorf("gate_decision event: trace action %v flags %v ttft %v, decision action %s", sum.Action, sum.Flags, sum.Timings.TTFTMs, d.Action)
	}

	// The fallback failed and the route blocked: the trace says block and why; decision/v1 does not
	// change (its action stays fallback, and fallback_failed is never a decision reason).
	st, q, pub = newMemStore(), &fakeQueue{engine: true, lane: gate.LaneHigh}, &events{}
	s = newSink(st, q, pub)
	r = replacedResult()
	r.Final = &gate.Final{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked,
		Text: "I can't confirm that from our policy documents.", Reasons: []string{"fallback_failed"}}
	s.GateDecided(context.Background(), r)
	tr, _ = st.GetTrace(context.Background(), r.Trace.TraceID)
	if *tr.Action != decision.ActionBlock || !slices.Equal(tr.Reasons, []string{"contradiction:critical", "fallback_failed"}) ||
		slices.Contains(tr.Flags, gate.FlagFallbackUsed) {
		t.Errorf("failed fallback: action %v reasons %v flags %v", tr.Action, tr.Reasons, tr.Flags)
	}
	if ds := storedDecisions(t, st, r.Trace.TraceID); len(ds) != 1 || ds[0].Action != decision.ActionFallback ||
		slices.Contains(ds[0].Reasons, "fallback_failed") {
		t.Errorf("decision/v1 %+v", ds)
	}
	if b, _ := st.GetTraceBody(context.Background(), r.Trace.TraceID); *b.FinalResponseText != "I can't confirm that from our policy documents." {
		t.Errorf("blocked text %q", *b.FinalResponseText)
	}
	if q.jobs[0].Gate.Delivered != gate.DeliveredBlocked {
		t.Errorf("audit gate %+v", q.jobs[0].Gate)
	}
	if sum, d := decidedEvent(t, pub); *sum.Action != decision.ActionBlock || !slices.Contains(sum.Reasons, "fallback_failed") ||
		d.Action != decision.ActionFallback || slices.Contains(d.Reasons, "fallback_failed") {
		t.Errorf("gate_decision event: trace action %v reasons %v, decision action %s reasons %v", sum.Action, sum.Reasons, d.Action, d.Reasons)
	}

	// An error-style block sends no answer text; an original delivery keeps the original text.
	for _, tc := range []struct {
		final *gate.Final
		want  *string
	}{
		{&gate.Final{Action: decision.ActionBlock, Delivered: gate.DeliveredBlocked}, nil},
		{&gate.Final{Action: decision.ActionFlag, Delivered: gate.DeliveredOriginal, Reasons: []string{"fallback_failed"}}, nonEmpty("Within 60 days.")},
		{nil, nonEmpty("Within 60 days.")},
	} {
		r := replacedResult()
		r.Final = tc.final
		b := bodyRecord(r, time.Now(), 1<<20)
		if (b.FinalResponseText == nil) != (tc.want == nil) || (tc.want != nil && *b.FinalResponseText != *tc.want) {
			t.Errorf("final %+v: final_response_text %v", tc.final, b.FinalResponseText)
		}
	}
	// Without Final the decision's action and the original answer went out.
	r = flaggedResult()
	if rec := decidedRecord(r, time.Now()); *rec.Action != decision.ActionFlag || rec.TTFTMs != nil || gateDecision(r).GetDelivered() != gate.DeliveredOriginal {
		t.Errorf("no final: action %v ttft %v", rec.Action, rec.TTFTMs)
	}
	// response_chars (0005) is the rune count of the gated text, NULL without one.
	r.ResponseText = "Größe: 30 Tage ✓"
	if rec := decidedRecord(r, time.Now()); rec.ResponseChars == nil || *rec.ResponseChars != 16 {
		t.Errorf("response_chars %v, want 16 runes", rec.ResponseChars)
	}
	r.ResponseText = ""
	if rec := decidedRecord(r, time.Now()); rec.ResponseChars != nil {
		t.Errorf("response_chars %v without an answer, want NULL", *rec.ResponseChars)
	}
}

func TestSinkAttemptDecidedWritesOnlyItsRow(t *testing.T) {
	st, pub, q := newMemStore(), &events{}, &fakeQueue{engine: true, lane: gate.LaneHigh}
	s := newSink(st, q, pub)
	r0 := replacedResult()
	// The re-gate of the fallback answer: attempt 1, no audit planned.
	r1 := *r0
	d1 := *r0.Decision
	d1.Attempt, d1.Action, d1.WouldAction, d1.Reasons, d1.DeepAudit = 1, decision.ActionPass, decision.ActionPass, []string{}, decision.DeepAuditNone
	r1.Decision, r1.Audit, r1.Final, r1.ResponseText = &d1, gate.AuditPlan{}, nil, r0.Final.Text

	// The handler records attempt 1 before it records the trace's outcome (attempt 0).
	s.TraceStarted(context.Background(), r0.Trace)
	s.AttemptDecided(context.Background(), &r1)
	tr, _ := st.GetTrace(context.Background(), r0.Trace.TraceID)
	if tr.GateStatus != store.GateStatusPending || tr.Action != nil || tr.AuditStatus != store.TraceAuditNone {
		t.Errorf("attempt 1 touched the trace: %+v", tr)
	}
	if types := pub.types(); strings.Join(types, ",") != "trace_started" || len(q.jobs) != 0 {
		t.Errorf("attempt 1: events %v, %d audits", types, len(q.jobs))
	}
	if _, err := st.GetTraceBody(context.Background(), r0.Trace.TraceID); err == nil {
		t.Error("attempt 1 wrote a body")
	}

	s.GateDecided(context.Background(), r0)
	ds := storedDecisions(t, st, r0.Trace.TraceID)
	if len(ds) != 2 || ds[0].Attempt != 0 || ds[0].Action != decision.ActionFallback || ds[1].Attempt != 1 || ds[1].Action != decision.ActionPass {
		t.Errorf("decisions by attempt: %+v", ds)
	}
	tr, _ = st.GetTrace(context.Background(), r0.Trace.TraceID)
	if *tr.Action != decision.ActionFallback || tr.AuditStatus != gate.AuditQueued || len(q.jobs) != 1 {
		t.Errorf("trace %+v, %d audits", tr, len(q.jobs))
	}
	if types := pub.types(); strings.Join(types, ",") != "trace_started,gate_decision" {
		t.Errorf("events %v", types)
	}

	// Writing the attempt again replaces its row and leaves the trace alone.
	d1.Action = decision.ActionFlag
	s.AttemptDecided(context.Background(), &r1)
	if ds := storedDecisions(t, st, r0.Trace.TraceID); len(ds) != 2 || ds[1].Action != decision.ActionFlag {
		t.Errorf("replaced attempt: %+v", ds)
	}
	if tr, _ := st.GetTrace(context.Background(), r0.Trace.TraceID); *tr.Action != decision.ActionFallback {
		t.Errorf("trace action %v", tr.Action)
	}
	// Without a database there is nothing to write.
	newSink(nil, nil, &events{}).AttemptDecided(context.Background(), &r1)
}

// lateOf is the late decision of r's attempt: the decision model answered after the deadline and
// would have passed the answer.
func lateOf(r *gate.Result) *gate.Result {
	d := *r.Decision
	snap, gateMs := "jev-1.2", int64(3400)
	d.Action, d.WouldAction, d.Reasons, d.Enforced, d.Late, d.GateMs = decision.ActionPass, decision.ActionPass, []string{}, false, true, &gateMs
	d.DecisionModel = &gate.DecisionModelUsage{Backend: "openrouter-decisions", Snapshot: &snap, Calls: 1, Attempts: 1, CostSource: "reported"}
	return &gate.Result{Trace: r.Trace, Decision: &d, Status: "ok", RouteLabel: r.RouteLabel, Headers: r.Headers,
		Flags: []string{gate.FlagTestMode, gate.FlagLateDecision}}
}

func TestSinkLateDecidedRecordsTheLateRow(t *testing.T) {
	st, pub := newMemStore(), &events{}
	s := newSink(st, &fakeQueue{engine: true, lane: gate.LaneHigh}, pub)
	r := flaggedResult()
	r.LateExpected = true
	s.TraceStarted(context.Background(), r.Trace)
	s.GateDecided(context.Background(), r)

	s.LateDecided(context.Background(), lateOf(r))
	ds := storedDecisions(t, st, r.Trace.TraceID)
	if len(ds) != 1 || !ds[0].Late || ds[0].Action != decision.ActionPass || ds[0].Enforced || ds[0].Attempt != 0 {
		t.Fatalf("the late decision replaces attempt 0's row: %+v", ds)
	}
	tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID)
	if *tr.Action != decision.ActionFlag || tr.GateStatus != "ok" || !slices.Contains(tr.Flags, gate.FlagLateDecision) {
		t.Errorf("the trace keeps what was enforced and gets the flag: action %v flags %v", *tr.Action, tr.Flags)
	}
	if types := pub.types(); strings.Join(types, ",") != "trace_started,gate_decision,gate_decision" {
		t.Fatalf("events %v", types)
	}
	sum, d := decidedEvent(t, pub)
	if !d.Late || d.Action != decision.ActionPass || *sum.Action != decision.ActionFlag || !slices.Contains(sum.Flags, gate.FlagLateDecision) {
		t.Errorf("late event: decision late %v action %s, trace action %v flags %v", d.Late, d.Action, *sum.Action, sum.Flags)
	}
	if sum.Audit.Status != gate.AuditQueued {
		t.Errorf("the event carries the stored trace: audit %+v", sum.Audit)
	}
}

func TestSinkLateDecisionWaitsForItsAttempt(t *testing.T) {
	st, pub := newMemStore(), &events{}
	s := newSink(st, &fakeQueue{engine: true, lane: gate.LaneHigh}, pub)
	r := flaggedResult()
	r.LateExpected = true
	s.TraceStarted(context.Background(), r.Trace)

	// The late answer comes while the handler still delivers: nothing is written yet.
	s.LateDecided(context.Background(), lateOf(r))
	if ds := storedDecisions(t, st, r.Trace.TraceID); len(ds) != 0 {
		t.Fatalf("a late decision was written before its attempt: %+v", ds)
	}
	s.GateDecided(context.Background(), r)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ds := storedDecisions(t, st, r.Trace.TraceID)
		if len(ds) == 1 && ds[0].Late {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the late decision was not written after its attempt: %+v", ds)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for time.Now().Before(deadline) && len(pub.types()) < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	if types := pub.types(); strings.Join(types, ",") != "trace_started,gate_decision,gate_decision" {
		t.Errorf("events %v", types)
	}
	if tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID); *tr.Action != decision.ActionFlag {
		t.Errorf("trace action %v", *tr.Action)
	}

	// A later attempt's late decision: its row only, no event.
	r1 := replacedResult()
	r1.Decision.Attempt, r1.LateExpected = 1, true
	s.TraceStarted(context.Background(), r1.Trace)
	s.AttemptDecided(context.Background(), r1)
	n := len(pub.types())
	s.LateDecided(context.Background(), lateOf(r1))
	if ds := storedDecisions(t, st, r1.Trace.TraceID); len(ds) != 1 || !ds[0].Late || ds[0].Attempt != 1 {
		t.Errorf("attempt 1's late row: %+v", ds)
	}
	if len(pub.types()) != n {
		t.Errorf("attempt 1's late decision broadcast: %v", pub.types())
	}
	// Without a database the late decision is still broadcast, from the published trace.
	pub2 := &events{}
	s2 := newSink(nil, nil, pub2)
	r2 := flaggedResult()
	r2.LateExpected = true
	s2.GateDecided(context.Background(), r2)
	s2.LateDecided(context.Background(), lateOf(r2))
	if sum, d := decidedEvent(t, pub2); !d.Late || !slices.Contains(sum.Flags, gate.FlagLateDecision) {
		t.Errorf("no database: late %v flags %v", d.Late, sum.Flags)
	}
}

func TestLateOrderExpires(t *testing.T) {
	var o lateOrder
	now := time.Now()
	r := lateOf(flaggedResult())
	k := lateKeyOf(r)
	if _, ok := o.arrive(k, r, now); ok {
		t.Fatal("nothing was recorded")
	}
	// Past lateHold the parked late decision is gone: the attempt's record finds nothing.
	if got := o.recorded(k, nil, now.Add(lateHold+time.Second)); got != nil {
		t.Error("an expired late decision was handed back")
	}
	if len(o.slots) != 1 || len(o.queue) != 1 {
		t.Errorf("slots %d queue %d", len(o.slots), len(o.queue))
	}
	for i := range lateSlots + 10 {
		o.recorded(lateKey{trace: strconv.Itoa(i)}, nil, now.Add(lateHold+2*time.Second))
	}
	if len(o.slots) > lateSlots || len(o.queue) > lateSlots {
		t.Errorf("slots %d queue %d, want at most %d", len(o.slots), len(o.queue), lateSlots)
	}
}

// slowJev answers every call only after the caller's deadline: the call times out, its attempt
// goes on, and the mock's answer arrives through OnLate.
type slowJev struct {
	jev.Client
	wg sync.WaitGroup
}

func (s *slowJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	resp, err := s.Client.Decide(context.Background(), req, opt)
	if err != nil {
		return nil, err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		resp.Late = true
		opt.OnLate(resp, nil)
	}()
	<-ctx.Done()
	return nil, &jev.Error{Kind: jev.KindTimeout, Attempts: 1, Continued: true, Err: ctx.Err()}
}

func TestLateDecisionThroughTheGateAndTheSink(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	mock, err := jev.New(jev.Config{Backend: jev.BackendMock, Mock: jev.MockConfig{Mode: jev.MockScenario}})
	if err != nil {
		t.Fatal(err)
	}
	st, pub := newMemStore(), &events{}
	sink := newSink(st, &fakeQueue{engine: true, lane: gate.LaneHigh}, pub)
	cl := &slowJev{Client: mock}
	g, err := gate.New(gate.Config{Jev: cl, Evaluators: reg, OnLate: sink.LateDecided, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse([]byte("version: 1\ndefaults:\n  mode: flag\n  deadline_ms: 100\n"), policy.DefaultSeeds(), reg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := llmapi.ParseChatRequest([]byte(`{"model":"gpt-4o-mini","messages":[
		{"role":"system","content":"<document id=\"refund-policy.md\">Annual plans can be refunded within 30 days of purchase.</document>"},
		{"role":"user","content":"How long do I have to get a refund on an annual plan?"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	route := p.Match(&policy.Request{Path: "/v1/chat/completions", Model: req.Model})
	settings := route.Evidence.Extraction()
	ev, err := evidence.Extract(req, &settings)
	if err != nil {
		t.Fatal(err)
	}
	trace := &gate.TraceStart{TraceID: store.NewID(), CreatedAt: time.Now().UTC(), Source: gate.SourceTest,
		APIFormat: "openai_chat", Path: "/v1/chat/completions", Route: route.Name, PolicyVersion: p.Version, Mode: route.Mode,
		Model: req.Model, Flags: []string{}}
	sink.TraceStarted(context.Background(), trace)
	res := g.Evaluate(context.Background(), &gate.Exchange{Trace: trace, Policy: p, Route: route, Request: req, Evidence: ev,
		Response: "Annual plans can be refunded within 60 days of purchase."})
	if !res.LateExpected || res.Status != decision.StatusTimeout {
		t.Fatalf("status %s late expected %v", res.Status, res.LateExpected)
	}
	sink.GateDecided(context.Background(), res)
	cl.wg.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if ds := storedDecisions(t, st, trace.TraceID); len(ds) == 1 && ds[0].Late {
			if ds[0].Enforced || ds[0].Status == decision.StatusTimeout || ds[0].DecisionModel == nil {
				t.Errorf("late row %+v", ds[0].Core)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no late row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	tr, _ := st.GetTrace(context.Background(), trace.TraceID)
	if *tr.Action != decision.ActionPass || tr.GateStatus != decision.StatusTimeout || !slices.Contains(tr.Flags, gate.FlagLateDecision) {
		t.Errorf("trace action %v status %s flags %v", *tr.Action, tr.GateStatus, tr.Flags)
	}
	if sum, d := decidedEvent(t, pub); !d.Late || *sum.Action != decision.ActionPass {
		t.Errorf("late event: late %v trace action %v", d.Late, *sum.Action)
	}
}

// flaggingStore is a memStore that records the flag of each dropped audit (auditDropper).
type flaggingStore struct {
	*memStore
	mu    sync.Mutex
	flags map[string]string
}

func (f *flaggingStore) DropTraceAudit(ctx context.Context, traceID, flag string) error {
	f.mu.Lock()
	if f.flags == nil {
		f.flags = map[string]string{}
	}
	f.flags[traceID] = flag
	f.mu.Unlock()
	return f.SetTraceAudit(ctx, traceID, store.TraceAuditUpdate{Status: store.TraceAuditDropped})
}

// TestSinkAuditPastTheLLMCap: a gated answer whose audit the day's LLM cap refuses (D48) records
// the audit dropped with the trace flag llm_budget, and audit_dropped says llm_budget. The job
// carried the trace's key label for its cost rows. Other refusals add no flag.
func TestSinkAuditPastTheLLMCap(t *testing.T) {
	st, pub := &flaggingStore{memStore: newMemStore()}, &events{}
	q := &fakeQueue{engine: true, err: worker.ErrLLMBudget}
	s := newSink(st, q, pub)
	r := flaggedResult()
	r.Trace.KeyLabel = "support-app"
	s.TraceStarted(context.Background(), r.Trace)
	if got := s.GateDecided(context.Background(), r); got != gate.AuditDropped {
		t.Fatalf("audit %q", got)
	}
	if len(q.jobs) != 1 || q.jobs[0].KeyLabel != "support-app" {
		t.Fatalf("jobs %+v", q.jobs)
	}
	if tr, _ := st.GetTrace(context.Background(), r.Trace.TraceID); tr.AuditStatus != store.TraceAuditDropped {
		t.Errorf("trace audit status %q", tr.AuditStatus)
	}
	if st.flags[r.Trace.TraceID] != flagLLMBudget {
		t.Errorf("flags %v", st.flags)
	}
	pub.mu.Lock()
	last := pub.evs[len(pub.evs)-1]
	pub.mu.Unlock()
	if last.Type != "audit_dropped" || !strings.Contains(string(last.Data), `"reason":"llm_budget"`) {
		t.Errorf("last event %s %s", last.Type, last.Data)
	}

	st2 := &flaggingStore{memStore: newMemStore()}
	s = newSink(st2, &fakeQueue{engine: true, err: worker.ErrQueueFull}, &events{})
	r = flaggedResult()
	s.GateDecided(context.Background(), r)
	if len(st2.flags) != 0 {
		t.Errorf("a full queue flagged the trace: %v", st2.flags)
	}
}
