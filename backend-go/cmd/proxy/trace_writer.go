package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/store/tracewriter"
)

// traceFlushSync is TRUTHTABLE_TRACE_FLUSH=sync: every trace write runs inline (the test stacks).
const traceFlushSync = "sync"

// wrapTraceWriter puts the batched trace writer (internal/store/tracewriter, D66) in front
// of st unless TRUTHTABLE_TRACE_FLUSH is sync, and returns the store to use and the flush to call at
// shutdown, after the HTTP drain: it writes what is still queued within its context. With sync, or
// without a database, st is returned as it is and the flush has nothing to do.
func wrapTraceWriter(cfg *config.Config, st store.Store, log *slog.Logger) (store.Store, func(context.Context) error) {
	if st == nil || cfg.TraceFlush == traceFlushSync {
		return st, func(context.Context) error { return nil }
	}
	if log == nil {
		log = slog.Default()
	}
	w := tracewriter.New(st, tracewriter.Config{Logger: log})
	log.Info("trace writes are batched (TRUTHTABLE_TRACE_FLUSH=async)",
		"queue", tracewriter.DefaultCapacity, "batch", tracewriter.DefaultBatchRows, "every", tracewriter.DefaultInterval.String())
	if pg, ok := st.(*store.PostgresStore); ok {
		return &pgTraceWriter{PostgresStore: pg, w: w}, w.Shutdown
	}
	return w, w.Shutdown
}

// pgTraceWriter is the Postgres store with the batched writer in front of its trace methods: every
// other method (API keys, settings, credentials and the rest that consumers find by interface, such
// as authz.Store) is the Postgres store's own.
type pgTraceWriter struct {
	*store.PostgresStore
	w *tracewriter.Writer
}

var _ store.Store = (*pgTraceWriter)(nil)

func (p *pgTraceWriter) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	return p.w.StartTrace(ctx, t)
}

func (p *pgTraceWriter) RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error {
	return p.w.RecordDecision(ctx, t, d, b)
}

func (p *pgTraceWriter) RecordAttempt(ctx context.Context, d *store.DecisionRecord) error {
	return p.w.RecordAttempt(ctx, d)
}

func (p *pgTraceWriter) RecordLateDecision(ctx context.Context, d *store.DecisionRecord) error {
	return p.w.RecordLateDecision(ctx, d)
}

func (p *pgTraceWriter) SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error {
	return p.w.SetTraceAudit(ctx, traceID, u)
}

func (p *pgTraceWriter) SaveAudit(ctx context.Context, r *store.AuditRecord) error {
	return p.w.SaveAudit(ctx, r)
}

func (p *pgTraceWriter) SaveAuditResult(ctx context.Context, r *store.AuditRecord, status string) error {
	return p.w.SaveAuditResult(ctx, r, status)
}

// SaveAuditResultWithCosts and DropTraceAudit go through the writer too (the worker and the sink
// find them by interface): the embedded Postgres store's own would skip the D83 barrier, so a
// queued decision write could overwrite the audit status, and the memory copy would miss them.
func (p *pgTraceWriter) SaveAuditResultWithCosts(ctx context.Context, r *store.AuditRecord, status string, costs []store.CostEvent) error {
	return p.w.SaveAuditResultWithCosts(ctx, r, status, costs)
}

func (p *pgTraceWriter) DropTraceAudit(ctx context.Context, traceID, flag string) error {
	return p.w.DropTraceAudit(ctx, traceID, flag)
}

func (p *pgTraceWriter) GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error) {
	return p.w.GetTrace(ctx, traceID)
}

func (p *pgTraceWriter) GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error) {
	return p.w.GetTraceBody(ctx, traceID)
}

func (p *pgTraceWriter) TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error) {
	return p.w.TraceDecisions(ctx, traceID)
}

// Close drains the writer and closes the pool.
func (p *pgTraceWriter) Close() { p.w.Close() }

// Barrier is the writer's D83 barrier.
func (p *pgTraceWriter) Barrier(ctx context.Context, traceID string) error {
	return p.w.Barrier(ctx, traceID)
}

// traceBarrierer is a store with the D83 barrier: the batched writer.
type traceBarrierer interface {
	Barrier(ctx context.Context, traceID string) error
}

// traceBarrierOf is the D83 barrier of the store the server was given (server.traceBarrier): a
// trace's queued writes are flushed before a later read or write through the Postgres store
// itself. Nil (no barrier needed) when trace writes run inline.
func traceBarrierOf(st any) func(ctx context.Context, traceID string) error {
	if b, ok := st.(traceBarrierer); ok {
		return b.Barrier
	}
	return nil
}
