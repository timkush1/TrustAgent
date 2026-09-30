package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
	"github.com/truthtable/backend-go/internal/worker"
)

// TestDirectAuditRecordsATrace: POST /api/audit records the trace its audit belongs to (source and
// api_format direct, gate skipped), which the trace endpoints list and show, and the stream carries
// its lifecycle.
func TestDirectAuditRecordsATrace(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	conn, _, err := dialWS(t, env, "protocol=2", http.Header{"X-TrustAgent-Key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	readFrame(t, conn) // hello

	code, body := env.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"How long is the refund window?",
		"response":"60 days.","model":"gpt-4o-mini","context":[{"id":"refund-policy.md","text":"30 days."}]}`))
	if code != http.StatusAccepted {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var acc directAuditAccepted
	_ = json.Unmarshal(body, &acc)

	d := env.settledTrace(acc.TraceID)
	if d.Source != "direct" || d.APIFormat != "direct" || d.Route != "direct" || d.Mode != "observe" || d.GateStatus != "skipped" ||
		d.Action != nil || d.SkipReason != nil || d.Model == nil || *d.Model != "gpt-4o-mini" || d.Audit.AuditID == nil ||
		*d.Audit.AuditID != acc.AuditID || d.Audit.Selection == nil || *d.Audit.Selection != "direct" || d.Audit.Lane == nil ||
		*d.Audit.Lane != "low" || d.QuestionPreview == nil || !strings.Contains(*d.QuestionPreview, "refund window") ||
		d.ResponsePreview == nil || *d.ResponsePreview != "60 days." {
		t.Errorf("direct trace %+v", d.TraceSummary)
	}
	// The fake engine has no fixture without a scenario: the audit fails, and the trace says so.
	if d.Audit.Status != store.AuditFailed || d.AuditDetail == nil || d.AuditDetail.AuditID != acc.AuditID {
		t.Errorf("audit of the trace %+v", d.Audit)
	}
	_, detail := env.get("/api/traces/"+acc.TraceID, testKey)
	contractstest.ValidateJSON(t, schemaTrace, detail)
	code, list := env.get("/api/traces?source=direct", testKey)
	contractstest.ValidateJSON(t, schemaTracePage, list)
	var p page[store.TraceSummary]
	_ = json.Unmarshal(list, &p)
	if code != http.StatusOK || len(p.Items) != 1 || p.Items[0].TraceID != acc.TraceID {
		t.Errorf("GET /api/traces?source=direct: %d %s", code, list)
	}
	if _, list := env.get("/api/traces?outcome=not_checked", testKey); !strings.Contains(string(list), acc.TraceID) {
		t.Errorf("a direct trace is not checked by the gate: %s", list)
	}

	var types []string
	for len(types) < 4 {
		f := readFrame(t, conn)
		if f.Type == "hb" || f.Type == "status" {
			continue
		}
		if f.TraceID != acc.TraceID {
			t.Fatalf("frame of another trace: %s", f.raw)
		}
		types = append(types, f.Type)
		if f.Type == "trace_started" && (!strings.Contains(string(f.Data), `"gate_status":"skipped"`) ||
			!strings.Contains(string(f.Data), `"source":"direct"`) || !strings.Contains(string(f.Data), `"audit_id":"`+acc.AuditID+`"`)) {
			t.Errorf("trace_started %s", f.Data)
		}
	}
	if strings.Join(types, ",") != "trace_started,audit_queued,audit_started,audit_failed" {
		t.Errorf("events %v", types)
	}

	// A route in the request names the trace's route.
	code, body = env.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"q","response":"r","route":"support-bot"}`))
	_ = json.Unmarshal(body, &acc)
	if tr := env.settledTrace(acc.TraceID); code != http.StatusAccepted || tr.Route != "support-bot" {
		t.Errorf("route: %d %s", code, tr.Route)
	}
}

// TestDirectAuditWithoutAJudge: an engine without a working judge refuses direct audits with 503
// no_judge, and nothing is recorded.
func TestDirectAuditWithoutAJudge(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	env.srv.pool.SetJudgeCheck(func() error { return worker.ErrNoJudge })
	code, body := env.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"q","response":"r"}`))
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeNoJudge {
		t.Errorf("no judge: %d %s", code, body)
	}
	if _, list := env.get("/api/traces?source=direct", testKey); strings.Contains(string(list), `"trace_id"`) {
		t.Errorf("a refused audit recorded a trace: %s", list)
	}
}

// TestJudgeCheck maps the engine's cached health onto the pool's judge check.
func TestJudgeCheck(t *testing.T) {
	check := func(h grpc.EngineHealth) error {
		return (&server{status: &statusReporter{engine: fakeEngineHealth{h}}}).judgeCheck()
	}
	up := grpc.EngineHealth{State: "up", Response: &pb.HealthResponse{Healthy: true, Dependencies: map[string]bool{"judge": true}}}
	if err := check(up); err != nil {
		t.Errorf("judge up: %v", err)
	}
	for name, h := range map[string]grpc.EngineHealth{
		"no judge configured": {State: "up", Response: &pb.HealthResponse{Healthy: false, JudgeProvider: "none"}},
		"judge down":          {State: "degraded", Response: &pb.HealthResponse{Dependencies: map[string]bool{"judge": false}}},
	} {
		if err := check(h); !errors.Is(err, worker.ErrNoJudge) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, h := range map[string]grpc.EngineHealth{
		"not checked yet": {State: "unknown"},
		"judge fallback":  {State: "up", Response: &pb.HealthResponse{Healthy: true, JudgeFallbackActive: true, Dependencies: map[string]bool{"judge": true}}},
		"judge unnamed":   {State: "up", Response: &pb.HealthResponse{Healthy: true}},
	} {
		if err := check(h); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// directQueue is an audit queue whose Submit fails with err.
type directQueue struct{ err error }

func (q directQueue) Submit(*worker.Job) (string, error) { return "", q.err }
func (q directQueue) HasEngine() bool                    { return true }

// directEvents collects published events.
type directEvents struct {
	mu  sync.Mutex
	evs []websocket.Event
}

func (e *directEvents) Publish(ev websocket.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, ev)
}

// TestDirectAuditNotQueued: an audit the pool does not take answers 503 and leaves its trace
// dropped, with audit_dropped broadcast.
func TestDirectAuditNotQueued(t *testing.T) {
	st, pub := newMemStore(), &directEvents{}
	a := &auditsAPI{store: st, audits: directQueue{worker.ErrQueueFull}, pub: pub, maxTextChars: 1000, log: slog.New(slog.DiscardHandler)}
	r := gin.New()
	r.POST("/api/audit", a.direct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/audit", bytes.NewReader([]byte(`{"query":"q","response":"r"}`))))
	if w.Code != http.StatusServiceUnavailable || errorCodeOf(t, w.Body.Bytes()) != codeQueueFull {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.evs) != 2 || pub.evs[0].Type != "trace_started" || pub.evs[1].Type != "audit_dropped" ||
		!strings.Contains(string(pub.evs[1].Data), `"reason":"queue_full"`) {
		t.Fatalf("events %+v", pub.evs)
	}
	tr, err := st.GetTrace(t.Context(), pub.evs[0].TraceID)
	if err != nil || tr.AuditStatus != store.TraceAuditDropped || tr.Source != "direct" {
		t.Errorf("trace %+v %v", tr, err)
	}
}

// TestDirectAuditTraceAgainstPostgres: the direct trace round-trips through Postgres and settles
// with its audit.
func TestDirectAuditTraceAgainstPostgres(t *testing.T) {
	env, _ := pgEnv(t, envOpts{})
	code, body := env.do(http.MethodPost, "/api/audit", testKey, []byte(`{"query":"How long is the refund window?","response":"60 days.","route":"support-bot"}`))
	if code != http.StatusAccepted {
		t.Fatalf("HTTP %d %s", code, body)
	}
	var acc directAuditAccepted
	_ = json.Unmarshal(body, &acc)
	d := env.settledTrace(acc.TraceID)
	if d.Source != "direct" || d.APIFormat != "direct" || d.GateStatus != "skipped" || d.Route != "support-bot" ||
		d.Audit.Status != store.AuditFailed || d.AuditDetail == nil || d.AuditDetail.AuditID != acc.AuditID {
		t.Errorf("direct trace %+v", d.TraceSummary)
	}
	_, detail := env.get("/api/traces/"+acc.TraceID, testKey)
	contractstest.ValidateJSON(t, schemaTrace, detail)
}

// writeCtxStore records the context each direct-audit trace write ran on.
type writeCtxStore struct {
	*memStore
	mu      sync.Mutex
	ctxs    []context.Context
	traceID string
}

func (w *writeCtxStore) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	w.record(ctx)
	w.mu.Lock()
	w.traceID = t.TraceID
	w.mu.Unlock()
	return w.memStore.StartTrace(ctx, t)
}

func (w *writeCtxStore) SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error {
	w.record(ctx)
	return w.memStore.SetTraceAudit(ctx, traceID, u)
}

func (w *writeCtxStore) record(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ctxs = append(w.ctxs, ctx)
}

// TestDirectAuditWritesOutliveTheCallerButNotForever: a caller that already hung up still gets its
// trace written and settled, and every write carries a deadline, so a stalled database cannot hold
// the handler.
func TestDirectAuditWritesOutliveTheCallerButNotForever(t *testing.T) {
	st := &writeCtxStore{memStore: newMemStore()}
	a := &auditsAPI{store: st, audits: directQueue{worker.ErrQueueFull}, maxTextChars: 1000, log: slog.New(slog.DiscardHandler)}
	r := gin.New()
	r.POST("/api/audit", a.direct)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/audit", bytes.NewReader([]byte(`{"query":"q","response":"r"}`))).WithContext(gone)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.ctxs) != 2 {
		t.Fatalf("%d trace writes, want the start and the drop", len(st.ctxs))
	}
	for i, ctx := range st.ctxs {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > directWriteTimeout {
			t.Errorf("write %d: deadline %v %t, want one within %s", i, deadline, ok, directWriteTimeout)
		}
	}
	tr, err := st.GetTrace(t.Context(), st.traceID)
	if err != nil || tr.AuditStatus != store.TraceAuditDropped {
		t.Errorf("trace %+v %v", tr, err)
	}
}
