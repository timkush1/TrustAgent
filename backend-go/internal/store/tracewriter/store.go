package tracewriter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// closeTimeout bounds the drain of Close (the store.Store close, used when nothing called
// Shutdown first).
const closeTimeout = 10 * time.Second

// bodyBarrierTimeout bounds how long a body read waits for its trace's queued writes.
const bodyBarrierTimeout = time.Second

// StartTrace queues a pending trace.
func (w *Writer) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	return w.enqueue(ctx, store.TraceWrite{Kind: store.WriteStart, Trace: t})
}

// RecordDecision queues a decided trace with its decision and body.
func (w *Writer) RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error {
	return w.enqueue(ctx, store.TraceWrite{Kind: store.WriteDecision, Trace: t, Decision: d, Body: b})
}

// RecordAttempt queues a later attempt's decision.
func (w *Writer) RecordAttempt(ctx context.Context, d *store.DecisionRecord) error {
	return w.enqueue(ctx, store.TraceWrite{Kind: store.WriteAttempt, Decision: d})
}

// RecordLateDecision queues a late decision.
func (w *Writer) RecordLateDecision(ctx context.Context, d *store.DecisionRecord) error {
	return w.enqueue(ctx, store.TraceWrite{Kind: store.WriteLateDecision, Decision: d})
}

// SetTraceAudit waits for the trace's queued writes (D83, auditBarrier), then updates its audit
// fields inline. When the wait fails (it timed out, or the database does not answer) nothing is
// written (a queued decision would overwrite it) and the error is returned.
func (w *Writer) SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error {
	err := w.auditBarrier(ctx, traceID)
	if err == nil {
		err = w.inner.SetTraceAudit(ctx, traceID, u)
	}
	// Memory takes the update either way; when the database did not, memory is ahead of it.
	w.mu.Lock()
	if e := w.cache.peek(traceID); e != nil {
		e.auditUpdate(u, w.cfg.Now())
		if err != nil {
			e.lost = true
		}
	}
	w.mu.Unlock()
	return err
}

// SaveAudit waits for the trace's queued writes (D83, auditBarrier), then stores the audit inline.
func (w *Writer) SaveAudit(ctx context.Context, r *store.AuditRecord) error {
	if err := w.auditBarrier(ctx, auditTraceID(r)); err != nil {
		return err
	}
	return w.inner.SaveAudit(ctx, r)
}

// SaveAuditResult waits for the trace's queued writes (D83, auditBarrier), then stores the audit
// and the trace's audit status inline: the audit is in the database before audit_completed is
// broadcast, and no queued decision can overwrite the status afterwards.
func (w *Writer) SaveAuditResult(ctx context.Context, r *store.AuditRecord, status string) error {
	traceID := auditTraceID(r)
	err := w.auditBarrier(ctx, traceID)
	if err == nil {
		err = w.inner.SaveAuditResult(ctx, r, status)
	}
	if errors.Is(err, store.ErrDuplicate) {
		return err
	}
	w.mu.Lock()
	if e := w.cache.peek(traceID); e != nil {
		e.auditSaved(r, status, w.cfg.Now())
		if err != nil {
			e.lost = true
		}
	}
	w.mu.Unlock()
	return err
}

// linkedCostStore is a store that writes an audit's cost rows in the audit's transaction
// (store.PostgresStore, worker.LinkedCostStore).
type linkedCostStore interface {
	SaveAuditResultWithCosts(ctx context.Context, r *store.AuditRecord, status string, costs []store.CostEvent) error
}

// auditDropper is a store that records a dropped audit with a trace flag (store.PostgresStore).
type auditDropper interface {
	DropTraceAudit(ctx context.Context, traceID, flag string) error
}

// SaveAuditResultWithCosts is SaveAuditResult with the audit's linked cost rows (P4-PLAN D45),
// behind the same barrier and memory update (D83). An inner store that cannot link the rows
// stores the audit through SaveAuditResult, which derives them from the record.
func (w *Writer) SaveAuditResultWithCosts(ctx context.Context, r *store.AuditRecord, status string, costs []store.CostEvent) error {
	ls, ok := w.inner.(linkedCostStore)
	if !ok {
		return w.SaveAuditResult(ctx, r, status)
	}
	traceID := auditTraceID(r)
	err := w.auditBarrier(ctx, traceID)
	if err == nil {
		err = ls.SaveAuditResultWithCosts(ctx, r, status, costs)
	}
	if errors.Is(err, store.ErrDuplicate) {
		return err
	}
	w.mu.Lock()
	if e := w.cache.peek(traceID); e != nil {
		e.auditSaved(r, status, w.cfg.Now())
		if err != nil {
			e.lost = true
		}
	}
	w.mu.Unlock()
	return err
}

// DropTraceAudit records that a trace's audit was not queued (its audit status becomes dropped and
// flag joins its flags once) behind the barrier (D83), so no queued write of the trace overwrites
// it; memory takes it either way. An inner store without the method records the dropped status
// through SetTraceAudit.
func (w *Writer) DropTraceAudit(ctx context.Context, traceID, flag string) error {
	err := w.auditBarrier(ctx, traceID)
	if err == nil {
		if d, ok := w.inner.(auditDropper); ok {
			err = d.DropTraceAudit(ctx, traceID, flag)
		} else {
			err = w.inner.SetTraceAudit(ctx, traceID, store.TraceAuditUpdate{Status: store.TraceAuditDropped})
		}
	}
	w.mu.Lock()
	if e := w.cache.peek(traceID); e != nil {
		e.auditDropped(flag, w.cfg.Now())
		if err != nil {
			e.lost = true
		}
	}
	w.mu.Unlock()
	return err
}

// auditTraceID is the trace an audit belongs to (SaveAudit's default: the audit id).
func auditTraceID(r *store.AuditRecord) string {
	if r == nil {
		return ""
	}
	if r.TraceID != "" {
		return r.TraceID
	}
	return r.AuditID
}

// fresh returns the memory copy of a trace when it is ahead of the database (queued or lost
// writes), else nil. The caller holds mu.
func (w *Writer) fresh(traceID string) *cached {
	e := w.cache.peek(traceID)
	if e == nil {
		return nil
	}
	if _, queued := w.pending[traceID]; queued || e.lost {
		return e
	}
	return nil
}

// GetTrace reads through the memory copy first (#11, #74): a trace with queued or lost writes
// answers from memory; any other is read from the database, and memory answers when the database
// cannot (an error or a row it lacks).
func (w *Writer) GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error) {
	w.mu.Lock()
	rec := w.fresh(traceID).trace()
	w.mu.Unlock()
	if rec != nil {
		return rec, nil
	}
	got, err := w.inner.GetTrace(ctx, traceID)
	if err == nil {
		return got, nil
	}
	w.mu.Lock()
	rec = w.cache.peek(traceID).trace()
	w.mu.Unlock()
	if rec != nil {
		return rec, nil
	}
	return nil, err
}

// TraceDecisions reads through the memory copy first, as GetTrace does.
func (w *Writer) TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error) {
	w.mu.Lock()
	if e := w.fresh(traceID); e != nil {
		docs := e.decisionDocs()
		w.mu.Unlock()
		return docs, nil
	}
	w.mu.Unlock()
	docs, err := w.inner.TraceDecisions(ctx, traceID)
	if err == nil {
		return docs, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if e := w.cache.peek(traceID); e != nil && len(e.decisions) > 0 {
		return e.decisionDocs(), nil
	}
	return nil, err
}

// GetTraceBody waits for the trace's queued writes (bodies are not kept in memory) for at most
// bodyBarrierTimeout, and not at all while the queue is held for an outage, then reads the
// database: the body may simply not be there yet.
func (w *Writer) GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error) {
	bctx, cancel := context.WithTimeout(ctx, bodyBarrierTimeout)
	_ = w.barrier(bctx, traceID, true)
	cancel()
	return w.inner.GetTraceBody(ctx, traceID)
}

// ListTraces reads the database: a trace whose writes are still queued (at most Interval) shows
// in the list once they land.
func (w *Writer) ListTraces(ctx context.Context, f store.TraceFilter) ([]store.TraceRecord, string, error) {
	return w.inner.ListTraces(ctx, f)
}

// ListAudits reads the database (audits are written inline).
func (w *Writer) ListAudits(ctx context.Context, f store.AuditFilter) ([]store.AuditRecord, string, error) {
	return w.inner.ListAudits(ctx, f)
}

// GetAudit reads the database.
func (w *Writer) GetAudit(ctx context.Context, auditID string) (*store.AuditRecord, error) {
	return w.inner.GetAudit(ctx, auditID)
}

// LatestAuditForTrace reads the database.
func (w *Writer) LatestAuditForTrace(ctx context.Context, traceID string) (*store.AuditRecord, error) {
	return w.inner.LatestAuditForTrace(ctx, traceID)
}

// Ping pings the database.
func (w *Writer) Ping(ctx context.Context) error { return w.inner.Ping(ctx) }

// Migration reads the database's migration.
func (w *Writer) Migration(ctx context.Context) (string, error) { return w.inner.Migration(ctx) }

// Close drains the queue (Shutdown, bounded by closeTimeout) and closes the inner store.
func (w *Writer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		w.log.Warn("trace writes were still queued at close", "err", err)
	}
	w.inner.Close()
}
