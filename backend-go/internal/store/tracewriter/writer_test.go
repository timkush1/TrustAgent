package tracewriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/truthtable/backend-go/internal/store"
)

// newWriter starts a writer over inner with cfg (a quiet logger and a drop counter added) and
// shuts it down at the end of the test.
func newWriter(t *testing.T, inner store.Store, cfg Config) (*Writer, *dropCounter) {
	t.Helper()
	drops := &dropCounter{}
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.Dropped = drops.add
	w := New(inner, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = w.Shutdown(ctx)
	})
	return w, drops
}

func strp(s string) *string { return &s }

// startRec is a pending trace as the gate sink records it.
func startRec(id string) *store.TraceRecord {
	return &store.TraceRecord{TraceID: id, Source: "proxy", APIFormat: "openai_chat", Path: "/v1/chat/completions",
		Route: "support-bot", Mode: "flag", KeyLabel: strp("app"), QuestionPreview: strp("How long is the refund window?")}
}

// decidedRec is the trace decided (flag, audit queued) with its attempt-0 decision.
func decidedRec(id string) (*store.TraceRecord, *store.DecisionRecord) {
	rec := startRec(id)
	action, verdict, risk := "flag", "unsupported", 0.7
	auditID, lane := "audit-"+id, "high"
	rec.Action, rec.WouldAction, rec.GateStatus, rec.Verdict, rec.MaxRisk = &action, &action, "ok", &verdict, &risk
	rec.Reasons, rec.AuditID, rec.AuditLane, rec.AuditStatus = []string{"groundedness:warn"}, &auditID, &lane, "queued"
	rec.ResponsePreview = strp("Within 60 days.")
	doc := json.RawMessage(`{"spec":"decision/v1","trace_id":"` + id + `","attempt":0,"action":"flag"}`)
	return rec, &store.DecisionRecord{TraceID: id, Route: "support-bot", Mode: "flag", EffectiveMode: "flag",
		Status: "ok", Action: "flag", Calibration: "none", Decision: doc}
}

func lateRec(id string) *store.DecisionRecord {
	return &store.DecisionRecord{TraceID: id, Route: "support-bot", Mode: "flag", Status: "ok", Action: "block",
		Late: true, Decision: json.RawMessage(`{"spec":"decision/v1","trace_id":"` + id + `","attempt":0,"late":true}`)}
}

func auditRec(id string) *store.AuditRecord {
	verdict, score := "HALLUCINATED", 0.25
	return &store.AuditRecord{AuditID: "audit-" + id, TraceID: id, Status: "completed", Verdict: &verdict,
		FaithfulnessScore: &score, Claims: []store.ClaimRecord{{Status: "CONTRADICTED"}, {Status: "SUPPORTED"}}}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeTrace writes one trace's life as the gateway does: start, decision, a late decision, the
// audit starting and its result.
func writeTrace(ctx context.Context, w *Writer, id string) error {
	if err := w.StartTrace(ctx, startRec(id)); err != nil {
		return err
	}
	rec, d := decidedRec(id)
	if err := w.RecordDecision(ctx, rec, d, nil); err != nil {
		return err
	}
	if err := w.RecordLateDecision(ctx, lateRec(id)); err != nil {
		return err
	}
	auditID := "audit-" + id
	if err := w.SetTraceAudit(ctx, id, store.TraceAuditUpdate{AuditID: &auditID, Status: "running"}); err != nil {
		return err
	}
	return w.SaveAuditResult(ctx, auditRec(id), "completed")
}

// TestWriterKeepsEachTraceInOrderUnderConcurrency: many traces written at once, with and without
// the batch path, each land in the order they were written, in batches no larger than BatchRows.
func TestWriterKeepsEachTraceInOrderUnderConcurrency(t *testing.T) {
	for _, batched := range []bool{true, false} {
		t.Run(fmt.Sprintf("batched=%v", batched), func(t *testing.T) {
			fake := newFake()
			var inner store.Store = fake
			if batched {
				inner = batchStore{fake}
			}
			w, drops := newWriter(t, inner, Config{BatchRows: 7, Interval: 5 * time.Millisecond})
			const traces = 60
			var wg sync.WaitGroup
			errs := make(chan error, traces)
			for i := range traces {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- writeTrace(context.Background(), w, fmt.Sprintf("t%02d", i))
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			want := []string{"start", "decision", "late", "audit:running", "saved:completed"}
			for i := range traces {
				id := fmt.Sprintf("t%02d", i)
				if got := fake.written(id); !slices.Equal(got, want) {
					t.Errorf("%s: written %v, want %v", id, got, want)
				}
				rec := fake.stored(id)
				if rec == nil || rec.AuditStatus != "completed" || !slices.Contains(rec.Flags, store.FlagLateDecision) {
					t.Errorf("%s: stored %+v", id, rec)
				}
			}
			if drops.get() != 0 {
				t.Errorf("%d writes dropped", drops.get())
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if batched && len(fake.batches) == 0 {
				t.Error("no batch was sent")
			}
			for _, n := range fake.batches {
				if n > 7 {
					t.Errorf("a batch of %d writes, BatchRows is 7", n)
				}
			}
		})
	}
}

// TestWriterFlushesAtBatchRowsOrInterval: a full batch goes at once; fewer writes wait for the
// interval.
func TestWriterFlushesAtBatchRowsOrInterval(t *testing.T) {
	fake := newFake()
	w, _ := newWriter(t, batchStore{fake}, Config{BatchRows: 3, Interval: time.Hour})
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := w.StartTrace(ctx, startRec(id)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if fake.stored("a") != nil || !w.Pending("a") {
		t.Fatal("two writes went out before the batch was full or the interval passed")
	}
	if err := w.StartTrace(ctx, startRec("c")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the full batch", func() bool { return fake.stored("a") != nil && fake.stored("c") != nil })
	eventually(t, "pending cleared", func() bool { return !w.Pending("a") && !w.Pending("c") })

	fast := newFake()
	w2, _ := newWriter(t, batchStore{fast}, Config{BatchRows: 100, Interval: 20 * time.Millisecond})
	start := time.Now()
	if err := w2.StartTrace(ctx, startRec("x")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the interval flush", func() bool { return fast.stored("x") != nil })
	if time.Since(start) > 2*time.Second {
		t.Errorf("the interval flush took %v", time.Since(start))
	}
}

// TestWriterDropsWholeTraceGroups: when the queue is full the write that does not fit drops its
// whole trace (queued writes included, and later ones), counted per write; other traces are
// untouched and the dropped trace still reads from memory.
func TestWriterDropsWholeTraceGroups(t *testing.T) {
	fake := newFake()
	w, drops := newWriter(t, batchStore{fake}, Config{Capacity: 2, BatchRows: 1, Interval: time.Hour})
	ctx := context.Background()
	entered, release := fake.holdNext()
	if err := w.StartTrace(ctx, startRec("a")); err != nil {
		t.Fatal(err)
	}
	<-entered // the writer is inside a's write; the queue holds the next two writes
	recB, decB := decidedRec("b")
	for _, err := range []error{
		w.StartTrace(ctx, startRec("b")),
		w.RecordDecision(ctx, recB, decB, nil),
		w.StartTrace(ctx, startRec("c")),        // full: c is dropped
		w.RecordLateDecision(ctx, lateRec("b")), // full: b's group is dropped, queued writes too
	} {
		if err != nil {
			t.Fatalf("a dropped write is not the caller's error: %v", err)
		}
	}
	release()
	eventually(t, "the queue to settle", func() bool { return !w.Pending("a") && !w.Pending("b") })
	if err := w.StartTrace(ctx, startRec("d")); err != nil {
		t.Fatal(err)
	}
	recC, decC := decidedRec("c")
	if err := w.RecordDecision(ctx, recC, decC, nil); err != nil { // c stays dropped
		t.Fatal(err)
	}
	if err := w.Barrier(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if got := fake.written("a"); !slices.Equal(got, []string{"start"}) {
		t.Errorf("a: %v", got)
	}
	if got := fake.written("d"); !slices.Equal(got, []string{"start"}) {
		t.Errorf("d: %v", got)
	}
	for _, id := range []string{"b", "c"} {
		if got := fake.written(id); len(got) != 0 {
			t.Errorf("%s was dropped, yet the database got %v", id, got)
		}
	}
	// c start + b late + b's two queued writes + c decision.
	if got := drops.get(); got != 5 {
		t.Errorf("dropped writes = %d, want 5", got)
	}
	rec, err := w.GetTrace(ctx, "b")
	if err != nil || rec.GateStatus != "ok" || !slices.Contains(rec.Flags, store.FlagLateDecision) {
		t.Fatalf("a dropped trace reads from memory: %+v, %v", rec, err)
	}
	docs, err := w.TraceDecisions(ctx, "b")
	if err != nil || len(docs) != 1 || !strings.Contains(string(docs[0]), `"late":true`) {
		t.Errorf("decisions of a dropped trace: %s, %v", docs, err)
	}
}

// TestWriterRetriesRowByRow: a batch the database refuses is retried write by write; only the
// refused write is dropped (and counted), and its trace then reads from memory.
func TestWriterRetriesRowByRow(t *testing.T) {
	fake := newFake()
	fake.refuse["bad"] = true
	w, drops := newWriter(t, batchStore{fake}, Config{BatchRows: 3, Interval: time.Hour})
	ctx := context.Background()
	for _, id := range []string{"ok1", "bad", "ok2"} {
		if err := w.StartTrace(ctx, startRec(id)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the batch", func() bool { return !w.Pending("ok2") })
	if fake.stored("ok1") == nil || fake.stored("ok2") == nil || fake.stored("bad") != nil {
		t.Fatalf("stored ok1=%v bad=%v ok2=%v", fake.stored("ok1") != nil, fake.stored("bad") != nil, fake.stored("ok2") != nil)
	}
	fake.mu.Lock()
	batches := len(fake.batches)
	fake.mu.Unlock()
	if batches != 0 {
		t.Errorf("%d batches went through though one write is refused", batches)
	}
	if drops.get() != 1 {
		t.Errorf("dropped = %d, want 1", drops.get())
	}
	if rec, err := w.GetTrace(ctx, "bad"); err != nil || rec.TraceID != "bad" || rec.GateStatus != store.GateStatusPending {
		t.Errorf("the refused trace reads from memory: %+v, %v", rec, err)
	}
}

// TestWriterHoldsWritesThroughAnOutage: while the database is down nothing is dropped: the writes
// wait, reads come from memory, a barrier times out and an audit write behind it is refused
// rather than run out of order; when the database is back everything lands in order.
func TestWriterHoldsWritesThroughAnOutage(t *testing.T) {
	fake := newFake()
	fake.setDown(true)
	w, drops := newWriter(t, batchStore{fake}, Config{BatchRows: 2, Interval: 5 * time.Millisecond})
	ctx := context.Background()
	rec, d := decidedRec("t1")
	if err := w.StartTrace(ctx, startRec("t1")); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordDecision(ctx, rec, d, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if !w.Pending("t1") || drops.get() != 0 {
		t.Fatalf("pending %v, dropped %d during the outage", w.Pending("t1"), drops.get())
	}
	got, err := w.GetTrace(ctx, "t1")
	if err != nil || got.GateStatus != "ok" || got.AuditStatus != "queued" {
		t.Fatalf("GetTrace during the outage: %+v, %v", got, err)
	}

	// An audit write behind queued writes is refused at once, even with no deadline of its own (a
	// caller on a request's path): the outage must not hold it.
	err = w.SetTraceAudit(ctx, "t1", store.TraceAuditUpdate{Status: "running"})
	if !errors.Is(err, ErrDatabaseUnreachable) {
		t.Fatalf("an audit write behind queued writes during an outage: %v", err)
	}
	if err := w.SaveAuditResult(ctx, auditRec("t1"), "completed"); !errors.Is(err, ErrDatabaseUnreachable) {
		t.Fatalf("an audit result behind queued writes during an outage: %v", err)
	}
	if got, _ := w.GetTrace(ctx, "t1"); got.AuditStatus != "completed" {
		t.Errorf("memory took the audit updates: %q", got.AuditStatus)
	}

	fake.setDown(false)
	if err := w.Barrier(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if got := fake.written("t1"); !slices.Equal(got, []string{"start", "decision"}) {
		t.Errorf("after the outage: %v", got)
	}
	if drops.get() != 0 {
		t.Errorf("dropped = %d after the outage", drops.get())
	}
}

// TestWriterHoldsAFailedBatchWhenTheDatabaseIsDown: a batch that fails because the database is
// down is held whole after one ping, not retried write by write first (each write would wait for
// its own timeout in a network outage).
func TestWriterHoldsAFailedBatchWhenTheDatabaseIsDown(t *testing.T) {
	fake := newFake()
	fake.setDown(true)
	w, drops := newWriter(t, batchStore{fake}, Config{BatchRows: 2, Interval: time.Hour})
	ctx := context.Background()
	for _, id := range []string{"t1", "t2"} {
		if err := w.StartTrace(ctx, startRec(id)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the writer holds its queue", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.held
	})
	fake.mu.Lock()
	calls := fake.calls
	fake.mu.Unlock()
	if calls != 2 {
		t.Errorf("%d calls reached the database, want the batch and one ping", calls)
	}
	if !w.Pending("t1") || !w.Pending("t2") || drops.get() != 0 {
		t.Errorf("pending t1 %v, t2 %v, dropped %d: both writes must wait", w.Pending("t1"), w.Pending("t2"), drops.get())
	}
	fake.setDown(false)
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.stored("t1") == nil || fake.stored("t2") == nil {
		t.Error("the held batch was not written once the database answered")
	}
}

// TestWriterAuditWriteEndsWhenAnOutageBegins: an audit write with no deadline of its own waits for
// its trace's queued writes; when the flush it asked for finds the database down, the wait ends at
// once instead of lasting as long as the outage.
func TestWriterAuditWriteEndsWhenAnOutageBegins(t *testing.T) {
	fake := newFake()
	w, _ := newWriter(t, batchStore{fake}, Config{Interval: time.Hour})
	ctx := context.Background()
	if err := w.StartTrace(ctx, startRec("t1")); err != nil {
		t.Fatal(err)
	}
	fake.setDown(true)
	done := make(chan error, 1)
	go func() { done <- w.SetTraceAudit(ctx, "t1", store.TraceAuditUpdate{Status: "dropped"}) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrDatabaseUnreachable) {
			t.Fatalf("SetTraceAudit = %v, want ErrDatabaseUnreachable", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the audit write waited for the outage to end")
	}
	if !w.Pending("t1") {
		t.Error("the trace's start was dropped; it must wait for the database")
	}
	// The database is back: the drain writes the start, then the audit write runs inline.
	fake.setDown(false)
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.SetTraceAudit(ctx, "t1", store.TraceAuditUpdate{Status: "dropped"}); err != nil {
		t.Fatalf("after the outage: %v", err)
	}
	if got := fake.stored("t1"); got == nil || got.AuditStatus != "dropped" {
		t.Errorf("stored %+v, want the start then the audit status", got)
	}
}

// TestWriterAuditBeforeTheFirstFlush (D83): an audit that completes before the trace's decision
// was flushed waits for it, so the queued decision cannot overwrite the audit status.
func TestWriterAuditBeforeTheFirstFlush(t *testing.T) {
	fake := newFake()
	w, _ := newWriter(t, batchStore{fake}, Config{Interval: time.Hour})
	ctx := context.Background()
	rec, d := decidedRec("t1")
	if err := w.StartTrace(ctx, startRec("t1")); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordDecision(ctx, rec, d, nil); err != nil {
		t.Fatal(err)
	}
	if fake.stored("t1") != nil {
		t.Fatal("the decision went out before the interval")
	}
	if err := w.SaveAuditResult(ctx, auditRec("t1"), "completed"); err != nil {
		t.Fatal(err)
	}
	if got := fake.written("t1"); !slices.Equal(got, []string{"start", "decision", "saved:completed"}) {
		t.Fatalf("written %v", got)
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fake.stored("t1"); got.AuditStatus != "completed" {
		t.Errorf("audit status %q after the drain, want completed", got.AuditStatus)
	}
	// A duplicate result is the inner store's answer, as without the writer.
	if err := w.SaveAuditResult(ctx, auditRec("t1"), "completed"); !errors.Is(err, store.ErrDuplicate) {
		t.Errorf("duplicate: %v", err)
	}
}

// TestWriterReadsThroughMemory (#11, #74): a trace with queued writes reads from memory as the
// database would store it, joined audit included; once written it reads from the database; when
// the database fails, memory answers.
func TestWriterReadsThroughMemory(t *testing.T) {
	fake := newFake()
	w, _ := newWriter(t, batchStore{fake}, Config{Interval: time.Hour})
	ctx := context.Background()
	rec, d := decidedRec("t1")
	if err := w.StartTrace(ctx, startRec("t1")); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordDecision(ctx, rec, d, nil); err != nil {
		t.Fatal(err)
	}
	got, err := w.GetTrace(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.GateStatus != "ok" || *got.Action != "flag" || got.KeyLabel == nil || *got.KeyLabel != "app" ||
		got.CreatedAt.IsZero() || got.Summary().Audit.Status != "queued" {
		t.Errorf("memory copy %+v", got)
	}
	docs, err := w.TraceDecisions(ctx, "t1")
	if err != nil || len(docs) != 1 || !strings.Contains(string(docs[0]), `"attempt":0`) {
		t.Errorf("decisions %s, %v", docs, err)
	}
	if fake.reads != 0 {
		t.Errorf("%d database reads for a trace with queued writes", fake.reads)
	}

	// The audit saved (after the barrier) joins the trace; a late decision queued afterwards reads
	// from memory with the join.
	if err := w.SaveAuditResult(ctx, auditRec("t1"), "completed"); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordLateDecision(ctx, lateRec("t1")); err != nil {
		t.Fatal(err)
	}
	got, _ = w.GetTrace(ctx, "t1")
	sum := got.Summary()
	if sum.Audit.Status != "completed" || sum.Audit.Verdict == nil || *sum.Audit.Verdict != "HALLUCINATED" ||
		sum.Audit.VerdictCounts["CONTRADICTED"] != 1 || !slices.Contains(sum.Flags, store.FlagLateDecision) {
		t.Errorf("memory copy after the audit and a late decision: %+v", sum)
	}
	docs, _ = w.TraceDecisions(ctx, "t1")
	if len(docs) != 1 || !strings.Contains(string(docs[0]), `"late":true`) {
		t.Errorf("late decision in memory: %s", docs)
	}

	if err := w.Barrier(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.GetTrace(ctx, "t1"); err != nil || fake.reads != 1 {
		t.Errorf("a written trace reads the database: reads %d, %v", fake.reads, err)
	}
	fake.setDown(true)
	if got, err := w.GetTrace(ctx, "t1"); err != nil || got.TraceID != "t1" {
		t.Errorf("memory answers when the database fails: %+v, %v", got, err)
	}
	if _, err := w.GetTrace(ctx, "unknown"); !errors.Is(err, errDown) {
		t.Errorf("unknown trace with the database down: %v", err)
	}
	fake.setDown(false)
	if _, err := w.GetTrace(ctx, "unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown trace: %v", err)
	}
}

// TestWriterShutdownDrains: Shutdown writes everything queued, then later writes run inline; a
// shutdown that cannot reach the database drops (and counts) what waits and says so.
func TestWriterShutdownDrains(t *testing.T) {
	fake := newFake()
	w, drops := newWriter(t, batchStore{fake}, Config{BatchRows: 50, Interval: time.Hour})
	ctx := context.Background()
	for i := range 120 {
		if err := w.StartTrace(ctx, startRec(fmt.Sprintf("t%03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range 120 {
		if fake.stored(fmt.Sprintf("t%03d", i)) == nil {
			t.Fatalf("t%03d was not written by the drain", i)
		}
	}
	if err := w.StartTrace(ctx, startRec("late")); err != nil || fake.stored("late") == nil {
		t.Errorf("a write after the drain runs inline: %v", err)
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Errorf("a second Shutdown: %v", err)
	}

	down := newFake()
	down.setDown(true)
	w2, drops2 := newWriter(t, batchStore{down}, Config{Interval: time.Hour})
	for _, id := range []string{"x", "y"} {
		if err := w2.StartTrace(ctx, startRec(id)); err != nil {
			t.Fatal(err)
		}
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err := w2.Shutdown(short)
	if err == nil || !strings.Contains(err.Error(), "2 queued trace writes") {
		t.Errorf("a drain that cannot write: %v", err)
	}
	if drops2.get() != 2 || drops.get() != 0 {
		t.Errorf("dropped %d (want 2) and %d (want 0)", drops2.get(), drops.get())
	}
	if w2.Pending("x") {
		t.Error("a dropped write is still pending after the drain")
	}
}

// TestWriterRefusesWhatTheStoreRefuses: an invalid write is the caller's error at once, as with
// the store itself, and nothing is queued.
func TestWriterRefusesWhatTheStoreRefuses(t *testing.T) {
	w, _ := newWriter(t, newFake(), Config{})
	ctx := context.Background()
	for name, err := range map[string]error{
		"no trace id":       w.StartTrace(ctx, &store.TraceRecord{}),
		"no decision":       w.RecordDecision(ctx, startRec("t"), nil, nil),
		"attempt 0":         w.RecordAttempt(ctx, &store.DecisionRecord{TraceID: "t"}),
		"late without late": w.RecordLateDecision(ctx, &store.DecisionRecord{TraceID: "t"}),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if w.Pending("t") {
		t.Error("a refused write was queued")
	}
}

// TestCacheEvictsTheOldest: the memory copy keeps CacheTraces traces.
func TestCacheEvictsTheOldest(t *testing.T) {
	c := newCache(2)
	now := time.Now()
	for _, id := range []string{"a", "b", "c"} {
		c.apply(&store.TraceWrite{Kind: store.WriteStart, Trace: startRec(id)}, now)
	}
	if c.peek("a") != nil || c.peek("b") == nil || c.peek("c") == nil {
		t.Error("the oldest trace was not evicted")
	}
	// A later attempt's decision never replaces a late one; RecordDecision keeps the late flag.
	c.apply(&store.TraceWrite{Kind: store.WriteLateDecision, Decision: lateRec("b")}, now)
	rec, d := decidedRec("b")
	c.apply(&store.TraceWrite{Kind: store.WriteDecision, Trace: rec, Decision: d}, now)
	e := c.peek("b")
	if !strings.Contains(string(e.decisions[0]), `"late":true`) || !slices.Contains(e.rec.Flags, store.FlagLateDecision) {
		t.Errorf("late decision overwritten: %s %v", e.decisions[0], e.rec.Flags)
	}
}

// TestWriterBodyReadWaitsBriefly: a body read waits for the trace's queued writes, so a body
// written a moment ago is found, but not longer than bodyBarrierTimeout while the database is
// down.
func TestWriterBodyReadWaitsBriefly(t *testing.T) {
	fake := newFake()
	w, _ := newWriter(t, batchStore{fake}, Config{Interval: time.Hour})
	ctx := context.Background()
	if err := w.StartTrace(ctx, startRec("t1")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.GetTraceBody(ctx, "t1"); !errors.Is(err, store.ErrNotFound) || w.Pending("t1") {
		t.Fatalf("the body read flushed the trace first: pending %v, %v", w.Pending("t1"), err)
	}
	fake.setDown(true)
	if err := w.StartTrace(ctx, startRec("t2")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = w.GetTraceBody(ctx, "t2")
	if took := time.Since(start); took > bodyBarrierTimeout+time.Second {
		t.Errorf("a body read during an outage took %v", took)
	}
	fake.setDown(false)
}

// stuckStore's trace starts wait for release whatever their context says.
type stuckStore struct {
	*fakeStore
	entered, release chan struct{}
}

func (s stuckStore) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	close(s.entered)
	<-s.release
	return s.fakeStore.StartTrace(ctx, t)
}

// TestWriterShutdownReturnsAtItsDeadline: a write that ignores its context does not hold the
// process past the shutdown deadline.
func TestWriterShutdownReturnsAtItsDeadline(t *testing.T) {
	inner := stuckStore{fakeStore: newFake(), entered: make(chan struct{}), release: make(chan struct{})}
	defer close(inner.release)
	w, _ := newWriter(t, inner, Config{BatchRows: 1, Interval: time.Hour})
	if err := w.StartTrace(context.Background(), startRec("t1")); err != nil {
		t.Fatal(err)
	}
	<-inner.entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := w.Shutdown(ctx)
	if err == nil || !strings.Contains(err.Error(), "still being sent") {
		t.Errorf("Shutdown with a write under way: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Shutdown took %v past a 50 ms deadline", took)
	}
}

// droppedTotal reads trustagent_gateway_trace_write_dropped_total from the default registry.
func droppedTotal(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "trustagent_gateway_trace_write_dropped_total" {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatal("trustagent_gateway_trace_write_dropped_total is not registered")
	return 0
}

// TestWriterCountsDropsInTheGatewayMetric: by default a dropped write counts in
// trustagent_gateway_trace_write_dropped_total.
func TestWriterCountsDropsInTheGatewayMetric(t *testing.T) {
	fake := newFake()
	w := New(batchStore{fake}, Config{Capacity: 1, BatchRows: 1, Interval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx := context.Background()
	entered, release := fake.holdNext()
	if err := w.StartTrace(ctx, startRec("a")); err != nil {
		t.Fatal(err)
	}
	<-entered
	before := droppedTotal(t)
	_ = w.StartTrace(ctx, startRec("b")) // fills the queue
	_ = w.StartTrace(ctx, startRec("c")) // dropped
	if got := droppedTotal(t) - before; got != 1 {
		t.Errorf("trace_write_dropped_total grew by %v, want 1", got)
	}
	release()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
