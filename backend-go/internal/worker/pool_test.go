package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/contracts"
)

const wsEventSchema = "api.v1.schema.json#/$defs/WsEvent"

// fakeStore records what the pool persists.
type fakeStore struct {
	mu      sync.Mutex
	saved   []*store.AuditRecord
	status  map[string]string
	history map[string][]string // every audit status a trace went through, in order
	saveErr error
}

func (s *fakeStore) SaveAuditResult(_ context.Context, r *store.AuditRecord, traceStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, r)
	s.setStatus(r.TraceID, traceStatus)
	return s.saveErr
}

func (s *fakeStore) SetTraceAudit(_ context.Context, traceID string, u store.TraceAuditUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.Status != "" {
		s.setStatus(traceID, u.Status)
	}
	return nil
}

func (s *fakeStore) setStatus(traceID, st string) {
	if s.status == nil {
		s.status, s.history = map[string]string{}, map[string][]string{}
	}
	s.status[traceID] = st
	s.history[traceID] = append(s.history[traceID], st)
}

func (s *fakeStore) statusHistory(traceID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.history[traceID]...)
}

func (s *fakeStore) traceStatus(traceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[traceID]
}

// fakePub collects published events.
type fakePub struct{ events chan websocket.Event }

func newPub() *fakePub { return &fakePub{events: make(chan websocket.Event, 64)} }

func (p *fakePub) Publish(ev websocket.Event) { p.events <- ev }

func (p *fakePub) next(t *testing.T) websocket.Event {
	t.Helper()
	select {
	case ev := <-p.events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event was published")
		return websocket.Event{}
	}
}

// outcome returns the next event that ends an audit (completed, failed or dropped), checking that
// the lifecycle events before it are valid WsEvents.
func (p *fakePub) outcome(t *testing.T) websocket.Event {
	t.Helper()
	for {
		ev := p.next(t)
		if ev.Type != "audit_queued" && ev.Type != "audit_started" {
			return ev
		}
		validateEvent(t, ev)
	}
}

// fixtureData is the data object of the golden fixture contracts/fixtures/api/WsEvent.<typ>.json.
func fixtureData(t *testing.T, typ string) map[string]any {
	t.Helper()
	b, err := contracts.FS.ReadFile("fixtures/api/WsEvent." + typ + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Data
}

// sameShape reports whether data has exactly the fixture's keys, each with a value of the same JSON type.
func sameShape(data json.RawMessage, fixture map[string]any) bool {
	var got map[string]any
	if json.Unmarshal(data, &got) != nil || len(got) != len(fixture) {
		return false
	}
	for k, v := range fixture {
		g, ok := got[k]
		if !ok || fmt.Sprintf("%T", g) != fmt.Sprintf("%T", v) {
			return false
		}
	}
	return true
}

// validateEvent checks every variant of an event against the WsEvent schema.
func validateEvent(t *testing.T, ev websocket.Event) {
	t.Helper()
	for _, data := range []json.RawMessage{ev.Data, ev.Redacted, ev.Ring} {
		if data == nil {
			continue
		}
		frame, _ := json.Marshal(map[string]any{"v": 2, "seq": 1, "epoch": "e", "type": ev.Type, "ts": "2026-09-28T00:00:00Z",
			"trace_id": ev.TraceID, "data": data})
		contractstest.ValidateJSON(t, wsEventSchema, frame)
	}
}

func startPool(t *testing.T, eng *enginetest.Engine, cfg Config) (*Pool, *fakeStore, *fakePub) {
	t.Helper()
	st, pub := &fakeStore{}, newPub()
	if eng != nil {
		c, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		cfg.Engine = c
	}
	cfg.Store, cfg.Publisher, cfg.TextRetention = st, pub, time.Hour
	cfg.Logger = slog.New(slog.DiscardHandler)
	p := New(cfg)
	p.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Drain(ctx)
	})
	return p, st, pub
}

func job(lane, selection string) *Job {
	trace := store.NewID()
	return &Job{
		Request: &pb.AuditRequest{
			RequestId: trace, TraceId: trace, AuditId: store.NewID(), Query: "Can I get a refund?",
			Response: "You can get a full refund within 60 days of purchase.", Model: "gpt-4o-mini",
			Provider: "openai_chat", Route: "support-bot", Source: "trafficgen", Priority: lane,
		},
		Lane: lane, Selection: selection, InclusionProb: 1,
		Gate:            &store.AuditGate{Action: "flag", Delivered: "original"},
		EvidenceSummary: []byte(`{"configured":["request_system"],"used":[],"empty":["request_system"],"knowledge":"not_requested","truncated":false,"tokens":0}`),
		ContextUsed:     []byte(`[{"id":"refund-policy.md","source":"request_system","label":"refund-policy.md","text":"30 days","truncated":false}]`),
	}
}

func hallucinated(_ context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
	score := float32(0.2)
	return &pb.AuditResult{
		AuditId: req.GetAuditId(), TraceId: req.GetTraceId(), Status: pb.AuditStatus_AUDIT_STATUS_COMPLETED,
		Verdict: pb.AuditVerdict_AUDIT_VERDICT_HALLUCINATED, Grade: pb.TrustGrade_TRUST_GRADE_F, HallucinationDetected: true,
		Scores:         &pb.AuditScores{TrustScore: &score, ClaimsTotal: 1, ClaimsDecided: 1},
		StepTimings:    map[string]int64{"total_ms": 12},
		ScoringVersion: "v2",
		Claims: []*pb.ClaimVerification{{
			Index: 0, Claim: "A full refund is available within 60 days of purchase.",
			Status: pb.VerificationStatus_VERIFICATION_STATUS_CONTRADICTED, Confidence: 0.96,
			Evidence: []string{"Annual plans can be refunded within 30 days of purchase."}, Quote: "60 days",
			DecidedBy: "llm", SpanStart: -1, SpanEnd: -1,
		}},
		Costs: []*pb.CostRecord{
			{Component: "deep_audit_decompose", Kind: "llm", Provider: "mock", Model: "mock", InputTokens: 180, OutputTokens: 40,
				CostSource: "simulated", LatencyMs: 3, Status: "ok"},
			{Component: "deep_audit_verify", Kind: "llm", Provider: "anthropic", Model: "claude-haiku-4-5", InputTokens: 400,
				OutputTokens: 60, CostUsd: 0.0007, CostSource: "computed", LatencyMs: 900, Status: "ok"},
		},
	}, nil
}

func TestCompletedAuditIsPersistedThenBroadcast(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: hallucinated})
	p, st, pub := startPool(t, eng, Config{Workers: 1, HighPerMin: 30})
	j := job(LaneHigh, "high")
	lane, err := p.Submit(j)
	if err != nil || lane != LaneHigh {
		t.Fatalf("submit: %s %v", lane, err)
	}
	// The lifecycle: queued in its lane, started by a worker (persisted as running), completed.
	queued := pub.next(t)
	validateEvent(t, queued)
	var q struct {
		AuditID    string `json:"audit_id"`
		Lane       string `json:"lane"`
		QueueDepth int    `json:"queue_depth"`
	}
	_ = json.Unmarshal(queued.Data, &q)
	if queued.Type != "audit_queued" || queued.TraceID != j.Request.TraceId || q.AuditID != j.Request.AuditId ||
		q.Lane != LaneHigh || q.QueueDepth != 0 || !sameShape(queued.Data, fixtureData(t, "audit_queued")) {
		t.Fatalf("queued %s %s", queued.Type, queued.Data)
	}
	started := pub.next(t)
	validateEvent(t, started)
	var sd struct {
		AuditID     string `json:"audit_id"`
		QueueWaitMs int64  `json:"queue_wait_ms"`
	}
	_ = json.Unmarshal(started.Data, &sd)
	if started.Type != "audit_started" || started.TraceID != j.Request.TraceId || sd.AuditID != j.Request.AuditId ||
		sd.QueueWaitMs < 0 || !sameShape(started.Data, fixtureData(t, "audit_started")) {
		t.Fatalf("started %s %s", started.Type, started.Data)
	}
	ev := pub.next(t)
	if ev.Type != "audit_completed" || ev.TraceID != j.Request.TraceId {
		t.Fatalf("event %+v", ev)
	}
	validateEvent(t, ev)
	if got := st.statusHistory(j.Request.TraceId); strings.Join(got, ",") != "running,completed" {
		t.Errorf("trace audit statuses %v, want running then completed", got)
	}

	reqs := eng.Requests()
	if len(reqs) != 1 || reqs[0].GetAuditId() != j.Request.AuditId || reqs[0].GetQuery() != "Can I get a refund?" {
		t.Fatalf("engine requests %v", reqs)
	}
	st.mu.Lock()
	rec := st.saved[0]
	st.mu.Unlock()
	if rec.Status != store.AuditCompleted || *rec.Verdict != store.VerdictHallucinated || *rec.Grade != "F" ||
		*rec.FaithfulnessScore != 0.2 || *rec.Priority != LaneHigh || *rec.Selection != "high" || rec.ScoringVersion != "v2" {
		t.Errorf("record %+v", rec)
	}
	c := rec.Claims[0]
	if c.Status != "CONTRADICTED" || c.Confidence != 0.96 || c.SpanStart != nil || *c.Quote != "60 days" {
		t.Errorf("claim %+v", c)
	}
	if rec.TextExpiresAt == nil || rec.CompletedAt == nil || rec.QueueWaitMs == nil {
		t.Errorf("timestamps %+v", rec)
	}
	// Every model call of the audit is kept with its cost source, and the audit's cost is their sum.
	if len(rec.Costs) != 2 || rec.Costs[0].Component != "deep_audit_decompose" || rec.Costs[0].CostSource != "simulated" ||
		rec.Costs[1].Model != "claude-haiku-4-5" || rec.Costs[1].InputTokens != 400 || rec.CostUSD == nil || *rec.CostUSD != 0.0007 {
		t.Errorf("costs %+v total %v", rec.Costs, rec.CostUSD)
	}
	if got := st.traceStatus(j.Request.TraceId); got != store.AuditCompleted {
		t.Errorf("trace audit status %q", got)
	}

	var full, red struct {
		Audit store.AuditDetail `json:"audit"`
	}
	_ = json.Unmarshal(ev.Data, &full)
	_ = json.Unmarshal(ev.Redacted, &red)
	if full.Audit.Claims[0].Claim == nil || red.Audit.Claims[0].Claim != nil || !red.Audit.Redacted || red.Audit.Claims[0].Status != "CONTRADICTED" {
		t.Errorf("variants: full %+v redacted %+v", full.Audit.Claims, red.Audit.Claims)
	}
	if !strings.Contains(string(ev.Data), `"context_used":[{"id":"refund-policy.md"`) || !strings.Contains(string(ev.Ring), `"context_used":[]`) {
		t.Errorf("the ring variant drops context_used: %s", ev.Ring)
	}
}

func TestNotAssessedAndDegradedTraceStatuses(t *testing.T) {
	results := map[string]*pb.AuditResult{
		"not_assessed": {Status: pb.AuditStatus_AUDIT_STATUS_COMPLETED, Verdict: pb.AuditVerdict_AUDIT_VERDICT_NOT_ASSESSED, NotAssessedReason: "no_evidence"},
		"degraded":     {Status: pb.AuditStatus_AUDIT_STATUS_DEGRADED, Verdict: pb.AuditVerdict_AUDIT_VERDICT_GROUNDED, DegradedReasons: []string{"unknown_claims"}},
	}
	for want, res := range results {
		eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) { return res, nil }})
		p, st, pub := startPool(t, eng, Config{Workers: 1})
		j := job(LaneLow, "sample")
		if _, err := p.Submit(j); err != nil {
			t.Fatal(err)
		}
		ev := pub.outcome(t)
		validateEvent(t, ev)
		if got := st.traceStatus(j.Request.TraceId); got != want {
			t.Errorf("%s: trace audit status %q", want, got)
		}
	}
}

func TestFailedAudits(t *testing.T) {
	cases := []struct {
		name      string
		eval      func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error)
		kind      string
		retryable bool
	}{
		{"engine down", func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return nil, status.Error(codes.Unavailable, "connection refused")
		}, KindEngineUnavailable, true},
		{"engine error", func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return nil, status.Error(codes.Internal, "boom")
		}, KindInternal, false},
		{"failed result", func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return &pb.AuditResult{Status: pb.AuditStatus_AUDIT_STATUS_FAILED, ErrorKind: "mock_fixture_missing", Error: "no fixture"}, nil
		}, KindInternal, false},
		{"judge down", func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return &pb.AuditResult{Status: pb.AuditStatus_AUDIT_STATUS_FAILED, ErrorKind: "judge_unavailable", Error: "timeouts"}, nil
		}, KindJudgeUnavailable, true},
		{"no status", func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return &pb.AuditResult{}, nil
		}, KindInternal, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: tc.eval})
			p, st, pub := startPool(t, eng, Config{Workers: 1})
			j := job(LaneLow, "sample")
			if _, err := p.Submit(j); err != nil {
				t.Fatal(err)
			}
			ev := pub.outcome(t)
			validateEvent(t, ev)
			var data struct {
				AuditID   string `json:"audit_id"`
				Kind      string `json:"kind"`
				Retryable bool   `json:"retryable"`
			}
			_ = json.Unmarshal(ev.Data, &data)
			if ev.Type != "audit_failed" || data.Kind != tc.kind || data.Retryable != tc.retryable || data.AuditID != j.Request.AuditId {
				t.Errorf("event %s %s", ev.Type, ev.Data)
			}
			if got := st.traceStatus(j.Request.TraceId); got != store.AuditFailed {
				t.Errorf("trace audit status %q", got)
			}
			st.mu.Lock()
			rec := st.saved[0]
			st.mu.Unlock()
			if rec.Status != store.AuditFailed || rec.ErrorKind == nil || rec.Detail().Error == nil {
				t.Errorf("record %+v", rec)
			}
		})
	}
}

func TestSubmitErrorsAndLanes(t *testing.T) {
	if _, err := New(Config{}).Submit(job(LaneLow, "sample")); !errors.Is(err, ErrNoEngine) || DropReason(err) != "no_engine" {
		t.Errorf("no engine: %v", err)
	}

	eng := enginetest.Start(t, &enginetest.Engine{})
	c, err := grpc.NewAuditClient(eng.Target, time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Not started: jobs stay queued. One high token, one slot per lane.
	p := New(Config{Engine: c, LowQueue: 1, HighQueue: 1, HighPerMin: 1, Logger: slog.New(slog.DiscardHandler)})
	if lane, err := p.Submit(job(LaneHigh, "high")); lane != LaneHigh || err != nil {
		t.Errorf("first high: %s %v", lane, err)
	}
	if lane, err := p.Submit(job(LaneHigh, "high")); lane != LaneLow || err != nil {
		t.Errorf("an empty bucket sends high to low: %s %v", lane, err)
	}
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrQueueFull) || DropReason(err) != "queue_full" {
		t.Errorf("full: %v", err)
	}
	if p.QueueDepth(LaneHigh) != 1 || p.QueueDepth(LaneLow) != 1 {
		t.Errorf("depths %d %d", p.QueueDepth(LaneHigh), p.QueueDepth(LaneLow))
	}
	anonymous := &Job{Request: &pb.AuditRequest{Query: "q"}, Lane: LaneLow}
	p2 := New(Config{Engine: c, Logger: slog.New(slog.DiscardHandler)})
	if _, err := p2.Submit(anonymous); err != nil {
		t.Fatal(err)
	}
	if id, err := uuid.Parse(anonymous.Request.AuditId); err != nil || id.Version() != 7 || anonymous.Request.TraceId != anonymous.Request.AuditId {
		t.Errorf("B4 ids %+v", anonymous.Request)
	}

	// Drain with an unstarted pool: nothing runs, so the queued jobs are dropped as stopped.
	p.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrStopped) || DropReason(err) != "stopped" {
		t.Errorf("after drain: %v", err)
	}
	_ = p2.Drain(ctx)
}

func TestDrainWaitsForQueuedAudits(t *testing.T) {
	release := make(chan struct{})
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
		<-release
		return hallucinated(ctx, req)
	}})
	p, st, _ := startPool(t, eng, Config{Workers: 1})
	for range 3 {
		if _, err := p.Submit(job(LaneLow, "sample")); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error)
	go func() { done <- p.Drain(context.Background()) }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.saved) != 3 {
		t.Errorf("drain finished %d of 3 audits", len(st.saved))
	}
}

func TestDrainTimeoutDropsTheRest(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(ctx context.Context, _ *pb.AuditRequest) (*pb.AuditResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	p, st, pub := startPool(t, eng, Config{Workers: 1})
	jobs := []*Job{job(LaneLow, "sample"), job(LaneLow, "sample")}
	for _, j := range jobs {
		if _, err := p.Submit(j); err != nil {
			t.Fatal(err)
		}
	}
	for len(eng.Requests()) == 0 { // the first audit is running
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drain: %v", err)
	}
	for _, j := range jobs {
		if got := st.traceStatus(j.Request.TraceId); got != store.TraceAuditDropped {
			t.Errorf("trace %s: %q", j.Request.TraceId, got)
		}
		ev := pub.outcome(t)
		validateEvent(t, ev)
		if ev.Type != "audit_dropped" || !strings.Contains(string(ev.Data), `"reason":"stopped"`) {
			t.Errorf("event %s %s", ev.Type, ev.Data)
		}
	}
}

func TestTokenBucketRefills(t *testing.T) {
	now := time.Unix(0, 0)
	b := newTokenBucket(60, func() time.Time { return now })
	for i := 0; i < 60; i++ {
		if !b.take() {
			t.Fatalf("token %d", i)
		}
	}
	if b.take() {
		t.Fatal("the bucket is empty")
	}
	now = now.Add(time.Second)
	if !b.take() || b.take() {
		t.Fatal("one token a second")
	}
	if z := newTokenBucket(0, time.Now); z.take() {
		t.Fatal("a zero rate allows nothing")
	}
}

// TestQueuedEventsCountTheJobsAhead: audit_queued names the lane a job went to and how many audits
// wait ahead of it there; a job that is not queued announces nothing.
func TestQueuedEventsCountTheJobsAhead(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{})
	c, err := grpc.NewAuditClient(eng.Target, time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pub := newPub()
	// Not started: the jobs stay queued.
	p := New(Config{Engine: c, LowQueue: 3, HighQueue: 1, HighPerMin: 1, Publisher: pub, Logger: slog.New(slog.DiscardHandler)})
	jobs := []*Job{job(LaneLow, "sample"), job(LaneHigh, "high"), job(LaneLow, "sample"), job(LaneHigh, "high")}
	want := []struct {
		lane  string
		depth int
	}{{LaneLow, 0}, {LaneHigh, 0}, {LaneLow, 1}, {LaneLow, 2}} // the second high job finds no token
	for i, j := range jobs {
		if _, err := p.Submit(j); err != nil {
			t.Fatal(err)
		}
		ev := pub.next(t)
		validateEvent(t, ev)
		var q struct {
			AuditID    string `json:"audit_id"`
			Lane       string `json:"lane"`
			QueueDepth int    `json:"queue_depth"`
		}
		_ = json.Unmarshal(ev.Data, &q)
		if ev.Type != "audit_queued" || ev.TraceID != j.Request.TraceId || q.AuditID != j.Request.AuditId ||
			q.Lane != want[i].lane || q.QueueDepth != want[i].depth {
			t.Errorf("job %d: %s %s, want lane %s depth %d", i, ev.Type, ev.Data, want[i].lane, want[i].depth)
		}
	}
	if _, err := p.Submit(job(LaneLow, "sample")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full: %v", err)
	}
	select {
	case ev := <-pub.events:
		t.Errorf("a job that was not queued announced %s", ev.Type)
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Drain(ctx)
}
