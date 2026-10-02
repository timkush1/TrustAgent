package tracewriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/store"
)

// errDown and errRefused are the fake database's failures: unreachable, or a write it refuses.
var (
	errDown    = errors.New("fake: the database does not answer")
	errRefused = errors.New("fake: the database refused the write")
)

// fakeStore is a store.Store that logs every write it takes, per trace and in order, applies them
// as the Postgres store does (store.Stored and store.MergeDecided), and can be taken down, made to
// refuse a trace's writes, or held inside a write.
type fakeStore struct {
	mu        sync.Mutex
	batch     bool // implements WriteTraceBatch (see batchStore)
	log       map[string][]string
	traces    map[string]*store.TraceRecord
	decisions map[string]map[int]json.RawMessage
	audits    map[string]*store.AuditRecord
	batches   []int // sizes of the batches written
	down      bool
	refuse    map[string]bool
	// hold, when set, makes the next write wait in it until it is closed; entered is closed when
	// that write arrives.
	hold, entered chan struct{}
	reads         int
}

// batchStore is a fakeStore with the batch path.
type batchStore struct{ *fakeStore }

func (b batchStore) WriteTraceBatch(ctx context.Context, writes []store.TraceWrite) error {
	return b.writeBatch(ctx, writes)
}

func newFake() *fakeStore {
	return &fakeStore{
		log: map[string][]string{}, traces: map[string]*store.TraceRecord{},
		decisions: map[string]map[int]json.RawMessage{}, audits: map[string]*store.AuditRecord{},
		refuse: map[string]bool{},
	}
}

var _ store.Store = (*fakeStore)(nil)

func (f *fakeStore) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// holdNext makes the next write wait until the returned release is called; entered is closed
// once that write is waiting.
func (f *fakeStore) holdNext() (entered <-chan struct{}, release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold, f.entered = make(chan struct{}), make(chan struct{})
	hold := f.hold
	return f.entered, func() { close(hold) }
}

// gate runs before every write: it waits in a hold, then fails when the database is down or
// refuses one of ids. The caller does not hold mu.
func (f *fakeStore) gate(ctx context.Context, ids ...string) error {
	f.mu.Lock()
	hold, entered := f.hold, f.entered
	f.hold, f.entered = nil, nil
	f.mu.Unlock()
	if hold != nil {
		close(entered)
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errDown
	}
	for _, id := range ids {
		if f.refuse[id] {
			return errRefused
		}
	}
	return nil
}

func (f *fakeStore) writeBatch(ctx context.Context, writes []store.TraceWrite) error {
	ids := make([]string, len(writes))
	for i := range writes {
		ids[i] = writes[i].TraceID()
	}
	if err := f.gate(ctx, ids...); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range writes {
		f.apply(&writes[i])
	}
	f.batches = append(f.batches, len(writes))
	return nil
}

// apply writes w; the caller holds mu.
func (f *fakeStore) apply(w *store.TraceWrite) {
	id, now := w.TraceID(), time.Now()
	switch w.Kind {
	case store.WriteStart:
		f.log[id] = append(f.log[id], "start")
		if f.traces[id] == nil {
			rec := w.Trace.Stored(now)
			f.traces[id] = &rec
		}
	case store.WriteDecision:
		f.log[id] = append(f.log[id], "decision")
		rec := w.Trace.Stored(now)
		if old := f.traces[id]; old != nil {
			rec = store.MergeDecided(old, &rec)
		}
		f.traces[id] = &rec
		f.putDecision(w.Decision)
	case store.WriteAttempt:
		f.log[id] = append(f.log[id], fmt.Sprintf("attempt%d", w.Decision.Attempt))
		f.putDecision(w.Decision)
	case store.WriteLateDecision:
		f.log[id] = append(f.log[id], "late")
		f.putDecision(w.Decision)
		if rec := f.traces[id]; rec != nil && !slices.Contains(rec.Flags, store.FlagLateDecision) {
			rec.Flags = append(rec.Flags, store.FlagLateDecision)
		}
	}
}

func (f *fakeStore) putDecision(d *store.DecisionRecord) {
	if f.decisions[d.TraceID] == nil {
		f.decisions[d.TraceID] = map[int]json.RawMessage{}
	}
	f.decisions[d.TraceID][d.Attempt] = d.Decision
}

func (f *fakeStore) one(ctx context.Context, w store.TraceWrite) error {
	if err := w.Check(); err != nil {
		return err
	}
	if err := f.gate(ctx, w.TraceID()); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apply(&w)
	return nil
}

func (f *fakeStore) StartTrace(ctx context.Context, t *store.TraceRecord) error {
	return f.one(ctx, store.TraceWrite{Kind: store.WriteStart, Trace: t})
}

func (f *fakeStore) RecordDecision(ctx context.Context, t *store.TraceRecord, d *store.DecisionRecord, b *store.BodyRecord) error {
	return f.one(ctx, store.TraceWrite{Kind: store.WriteDecision, Trace: t, Decision: d, Body: b})
}

func (f *fakeStore) RecordAttempt(ctx context.Context, d *store.DecisionRecord) error {
	return f.one(ctx, store.TraceWrite{Kind: store.WriteAttempt, Decision: d})
}

func (f *fakeStore) RecordLateDecision(ctx context.Context, d *store.DecisionRecord) error {
	return f.one(ctx, store.TraceWrite{Kind: store.WriteLateDecision, Decision: d})
}

func (f *fakeStore) SetTraceAudit(ctx context.Context, traceID string, u store.TraceAuditUpdate) error {
	if err := f.gate(ctx, traceID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log[traceID] = append(f.log[traceID], "audit:"+u.Status)
	if rec := f.traces[traceID]; rec != nil && u.Status != "" {
		rec.AuditStatus = u.Status
	}
	return nil
}

func (f *fakeStore) SaveAudit(ctx context.Context, r *store.AuditRecord) error {
	return f.SaveAuditResult(ctx, r, "")
}

func (f *fakeStore) SaveAuditResult(ctx context.Context, r *store.AuditRecord, status string) error {
	if err := f.gate(ctx, r.TraceID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.audits[r.AuditID]; ok {
		return store.ErrDuplicate
	}
	f.audits[r.AuditID] = r
	f.log[r.TraceID] = append(f.log[r.TraceID], "saved:"+status)
	if rec := f.traces[r.TraceID]; rec != nil && status != "" {
		rec.AuditStatus, rec.AuditID = status, &r.AuditID
	}
	return nil
}

func (f *fakeStore) GetTrace(ctx context.Context, id string) (*store.TraceRecord, error) {
	if err := f.gate(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	rec, ok := f.traces[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (f *fakeStore) TraceDecisions(ctx context.Context, id string) ([]json.RawMessage, error) {
	if err := f.gate(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	var out []json.RawMessage
	for _, a := range slices.Sorted(maps.Keys(f.decisions[id])) {
		out = append(out, f.decisions[id][a])
	}
	return out, nil
}

func (f *fakeStore) GetTraceBody(context.Context, string) (*store.BodyRecord, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListTraces(context.Context, store.TraceFilter) ([]store.TraceRecord, string, error) {
	return nil, "", nil
}

func (f *fakeStore) ListAudits(context.Context, store.AuditFilter) ([]store.AuditRecord, string, error) {
	return nil, "", nil
}

func (f *fakeStore) GetAudit(context.Context, string) (*store.AuditRecord, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) LatestAuditForTrace(context.Context, string) (*store.AuditRecord, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) Ping(ctx context.Context) error { return f.gate(ctx) }

func (f *fakeStore) Migration(context.Context) (string, error) { return "0005", nil }

func (f *fakeStore) Close() {}

// written returns the trace's write log.
func (f *fakeStore) written(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.log[id])
}

// stored returns the trace's row, or nil.
func (f *fakeStore) stored(id string) *store.TraceRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec := f.traces[id]; rec != nil {
		cp := *rec
		return &cp
	}
	return nil
}

// dropCounter counts the writer's drops.
type dropCounter struct {
	mu sync.Mutex
	n  int
}

func (d *dropCounter) add(n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n += n
}

func (d *dropCounter) get() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}
