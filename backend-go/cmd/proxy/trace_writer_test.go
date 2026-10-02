package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/store/tracewriter"
	"github.com/truthtable/backend-go/internal/storetest"
)

// The Postgres store behind the writer keeps every other interface it is found by: the API keys
// (authz.Store) in particular.
var _ authz.Store = (*pgTraceWriter)(nil)

// TestWrapTraceWriterModes: sync and no database leave the store as it is with a no-op flush;
// async fronts it with the batched writer, whose barrier the server takes (D83).
func TestWrapTraceWriterModes(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()
	mem := newMemStore()

	st, flush := wrapTraceWriter(&config.Config{TraceFlush: "sync"}, mem, log)
	if st != store.Store(mem) || flush(ctx) != nil || traceBarrierOf(st) != nil {
		t.Errorf("sync: %T", st)
	}
	if st, flush := wrapTraceWriter(&config.Config{TraceFlush: "async"}, nil, log); st != nil || flush(ctx) != nil {
		t.Errorf("no database: %T", st)
	}
	st, flush = wrapTraceWriter(&config.Config{TraceFlush: "async"}, mem, log)
	if _, ok := st.(*tracewriter.Writer); !ok || traceBarrierOf(st) == nil {
		t.Fatalf("async: %T", st)
	}
	if err := st.StartTrace(ctx, &store.TraceRecord{TraceID: "t1", Source: "proxy"}); err != nil {
		t.Fatal(err)
	}
	if err := flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.GetTrace(ctx, "t1"); err != nil {
		t.Errorf("the flush at shutdown wrote the queued trace: %v", err)
	}

	env := newTestEnv(t, envOpts{store: func() store.Store {
		st, flush := wrapTraceWriter(&config.Config{TraceFlush: "async"}, newMemStore(), log)
		t.Cleanup(func() { _ = flush(context.Background()) })
		return st
	}()})
	if env.srv.traceBarrier == nil {
		t.Error("the server has no trace barrier with the batched writer")
	}
	if plain := newTestEnv(t, envOpts{}); plain.srv.traceBarrier != nil {
		t.Error("a trace barrier without the batched writer")
	}
}

// TestWrapTraceWriterPostgres: with Postgres the wrapped store is still the Postgres store for
// everything but traces, so the API keys and setup keep working.
func TestWrapTraceWriterPostgres(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	st, flush := wrapTraceWriter(&config.Config{TraceFlush: "async"}, pg, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = flush(context.Background()) })
	as, ok := st.(authz.Store)
	if !ok {
		t.Fatalf("%T is not an authz.Store", st)
	}
	if _, err := as.AdminKeyExists(context.Background()); err != nil {
		t.Errorf("an authz.Store method through the wrapper: %v", err)
	}
	ctx := context.Background()
	id := store.NewID()
	if err := st.StartTrace(ctx, &store.TraceRecord{TraceID: id, Source: "proxy", APIFormat: "openai_chat", Route: "default", Mode: "observe"}); err != nil {
		t.Fatal(err)
	}
	if err := traceBarrierOf(st)(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetTrace(ctx, id); err != nil {
		t.Errorf("the barrier wrote the trace to Postgres: %v", err)
	}
}

// stalledStore is a memStore whose trace writes wait until release is closed: the writer's queue
// holds everything behind the first write.
type stalledStore struct {
	*memStore
	release chan struct{}
	once    sync.Once
}

func (s *stalledStore) wait(ctx context.Context) error {
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *stalledStore) open() { s.once.Do(func() { close(s.release) }) }

func (s *stalledStore) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.memStore.StartTrace(ctx, t)
}

func (s *stalledStore) RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.memStore.RecordDecision(ctx, t, d, b)
}

func (s *stalledStore) RecordLateDecision(ctx context.Context, d *store.DecisionRecord) error {
	if err := s.wait(ctx); err != nil {
		return err
	}
	return s.memStore.RecordLateDecision(ctx, d)
}

// TestTracesAndDecisionPollReadQueuedTraces (#11, #74): while a trace's writes still wait in the
// writer's queue, GET /api/traces/:id and the decision long-poll (from another process's point of
// view: the waiters do not know the trace) answer the decided trace from the writer's memory.
func TestTracesAndDecisionPollReadQueuedTraces(t *testing.T) {
	inner := &stalledStore{memStore: newMemStore(), release: make(chan struct{})}
	st, flush := wrapTraceWriter(&config.Config{TraceFlush: "async"}, inner, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		inner.open()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = flush(ctx)
	})
	env := newTestEnv(t, envOpts{store: st})
	sc := loadDemoScenarios(t)
	id := env.send(sc["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")

	if _, err := inner.memStore.GetTrace(context.Background(), id); err == nil {
		t.Fatal("the trace reached the database: the test needs it queued")
	}
	code, body := env.get("/api/traces/"+id, testKey)
	if code != http.StatusOK {
		t.Fatalf("#11 for a queued trace: HTTP %d %s", code, body)
	}
	var d store.TraceDetail
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.GateStatus != "ok" || d.Action == nil || *d.Action != "flag" || len(d.Decisions) != 1 {
		t.Errorf("#11 from memory: gate %s action %v decisions %d", d.GateStatus, d.Action, len(d.Decisions))
	}

	env.srv.waiters = newDecisionWaiters() // as another gateway sharing the database would see it
	code, p, raw := pollDecision(t, env, id, "")
	if code != http.StatusOK || p.State != decisionDecided {
		t.Fatalf("#74 for a queued trace: HTTP %d %s", code, raw)
	}

	inner.open()
	if err := env.srv.barrier(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.memStore.GetTrace(context.Background(), id); err != nil {
		t.Errorf("the barrier wrote the trace: %v", err)
	}
	if detail := env.settledTrace(id); detail.Audit.Status != store.AuditCompleted {
		t.Errorf("the audit settled as %s", detail.Audit.Status)
	}
}
