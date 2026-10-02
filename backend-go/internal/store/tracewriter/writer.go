// Package tracewriter is the batched, ordered trace writer (SPEC §2.3 writers, P4-PLAN D66 and
// D83). It fronts a store.Store when TRUTHTABLE_TRACE_FLUSH=async:
//
//   - The trace-scoped writes (trace start, decision with its body, later attempt, late decision)
//     go into one ordered queue of 10,000 writes and return at once. One goroutine sends them in
//     order, every 250 ms or as soon as 200 are waiting, as one batch (PostgresStore.WriteTraceBatch:
//     one transaction, one round trip). A batch that fails is retried write by write; a write that
//     fails while the database answers its ping is dropped, and while it does not (an outage) the
//     queue is held and retried with a growing pause.
//   - A full queue drops the whole trace group: the write that did not fit, the trace's writes
//     still queued, and every later write of that trace, so the database never keeps half a trace.
//     trustagent_gateway_trace_write_dropped_total counts every dropped write.
//   - The audit writes (SaveAudit, SaveAuditResult, SetTraceAudit) first wait for the trace's
//     queued writes (Barrier, D83), then run inline: a decision flushed late can never overwrite an
//     audit status written after it, and an audit is stored before audit_completed is broadcast.
//   - A memory copy of the last 2,000 traces (what the gateway wrote, kept as the database would
//     store it) answers GetTrace and TraceDecisions while a trace still has queued writes or lost
//     one, and whenever the database cannot answer.
//
// Shutdown drains the queue within its context; writes that arrive after it run inline.
package tracewriter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/store"
)

// Defaults (SPEC §2.3).
const (
	DefaultCapacity     = 10_000
	DefaultBatchRows    = 200
	DefaultInterval     = 250 * time.Millisecond
	DefaultCacheTraces  = 2_000
	DefaultWriteTimeout = 10 * time.Second
	// maxRetryPause caps the pause between retries while the database is unreachable.
	maxRetryPause = 5 * time.Second
	// minDroppedIDs is the least number of dropped trace groups remembered (their later writes are
	// dropped too).
	minDroppedIDs = 4096
	// shutdownGrace is how long Shutdown waits past its deadline for the drain to count what it
	// dropped.
	shutdownGrace = 250 * time.Millisecond
	// dropLogEvery spaces the warnings about dropped writes.
	dropLogEvery = 10 * time.Second
)

// Config tunes a Writer; zero fields take the defaults.
type Config struct {
	// Capacity is how many writes may wait in the queue.
	Capacity int
	// BatchRows sends a batch as soon as this many writes wait.
	BatchRows int
	// Interval sends what waits at least this often.
	Interval time.Duration
	// CacheTraces is how many traces the memory copy keeps.
	CacheTraces int
	// WriteTimeout bounds one batch or one write.
	WriteTimeout time.Duration
	Logger       *slog.Logger
	// Dropped counts dropped writes (default: trustagent_gateway_trace_write_dropped_total).
	Dropped func(n int)
	// Now is the clock of the memory copy (default time.Now).
	Now func() time.Time
}

// batcher is the inner store's batch path (PostgresStore.WriteTraceBatch); without it batches go
// write by write.
type batcher interface {
	WriteTraceBatch(ctx context.Context, writes []store.TraceWrite) error
}

// op is one queued write; seq orders it.
type op struct {
	seq uint64
	w   store.TraceWrite
}

// Writer is the batched trace writer; it implements store.Store over its inner store.
type Writer struct {
	inner store.Store
	batch batcher
	cfg   Config
	log   *slog.Logger

	queue chan *op
	kick  chan struct{}
	stop  chan context.Context
	done  chan struct{}

	mu sync.Mutex
	// seq is the last queued write; settled is the last write that was written or dropped (the
	// queue is in seq order, so every write up to it is settled). progress is closed and replaced
	// whenever settled moves.
	seq, settled uint64
	progress     chan struct{}
	// pending maps a trace to its last queued write while that write is unsettled.
	pending map[string]uint64
	// dropped are the traces whose group was dropped.
	dropped *idSet
	cache   *cache
	closed  bool
	// unwritten counts the writes Shutdown gave up on.
	unwritten int
	// dropsToLog and lastDropLog space the drop warnings.
	dropsToLog  int
	lastDropLog time.Time
}

var _ store.Store = (*Writer)(nil)

// New starts a writer in front of inner. Shutdown stops it.
func New(inner store.Store, cfg Config) *Writer {
	if cfg.Capacity <= 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.BatchRows <= 0 {
		cfg.BatchRows = DefaultBatchRows
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.CacheTraces <= 0 {
		cfg.CacheTraces = DefaultCacheTraces
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Dropped == nil {
		cfg.Dropped = func(n int) { metrics.TraceWriteDropped.Add(float64(n)) }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	w := &Writer{
		inner: inner, cfg: cfg, log: cfg.Logger,
		queue: make(chan *op, cfg.Capacity), kick: make(chan struct{}, 1), stop: make(chan context.Context, 1),
		done: make(chan struct{}), progress: make(chan struct{}),
		pending: map[string]uint64{}, dropped: newIDSet(max(cfg.Capacity, minDroppedIDs)), cache: newCache(cfg.CacheTraces),
	}
	w.batch, _ = inner.(batcher)
	go w.run()
	return w
}

// enqueue queues a checked write and updates the memory copy. A write that does not fit drops
// its trace group (counted, never an error: the caller cannot do better). After Shutdown the
// write runs inline and its error is returned.
func (w *Writer) enqueue(ctx context.Context, tw store.TraceWrite) error {
	if err := tw.Check(); err != nil {
		return err
	}
	tw = detach(tw)
	id := tw.TraceID()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return w.writeOne(ctx, tw)
	}
	w.cache.apply(&tw, w.cfg.Now())
	if w.dropped.has(id) {
		w.cache.lose(id)
		w.mu.Unlock()
		w.drop(1)
		return nil
	}
	o := &op{seq: w.seq + 1, w: tw}
	select {
	case w.queue <- o:
		w.seq = o.seq
		w.pending[id] = o.seq
		w.mu.Unlock()
		return nil
	default:
		// The queue is full: the trace's group goes, queued writes included (run skips them).
		w.dropped.add(id)
		w.cache.lose(id)
		w.mu.Unlock()
		w.drop(1)
		return nil
	}
}

// Barrier waits until every write of traceID queued before it is written or dropped (D83),
// asking for an early flush; it returns ctx's error if that takes too long.
func (w *Writer) Barrier(ctx context.Context, traceID string) error {
	w.mu.Lock()
	target, ok := w.pending[traceID]
	for ok && w.settled < target {
		progress := w.progress
		w.mu.Unlock()
		select {
		case w.kick <- struct{}{}:
		default:
		}
		select {
		case <-progress:
		case <-ctx.Done():
			return fmt.Errorf("tracewriter: trace %s still has queued writes: %w", traceID, ctx.Err())
		}
		w.mu.Lock()
	}
	w.mu.Unlock()
	return nil
}

// Pending reports whether traceID has queued writes that are not settled yet.
func (w *Writer) Pending(traceID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.pending[traceID]
	return ok
}

// Shutdown stops taking writes into the queue, writes what waits within ctx and drops the rest
// (counted). Later writes run inline. It returns an error naming how many writes were dropped, and
// returns at ctx's end even when a write under way still waits for the database.
func (w *Writer) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	w.stop <- ctx
	select {
	case <-w.done:
	case <-ctx.Done():
		// The drain gives up at ctx's end and counts what it dropped; a write already under way
		// may still wait for the database, and the process need not wait for it.
		select {
		case <-w.done:
		case <-time.After(shutdownGrace):
			return fmt.Errorf("tracewriter: trace writes were still being sent at the shutdown deadline: %w", ctx.Err())
		}
	}
	w.mu.Lock()
	n := w.unwritten
	w.mu.Unlock()
	if n > 0 {
		return fmt.Errorf("tracewriter: %d queued trace writes were not written before the shutdown deadline", n)
	}
	return nil
}

// run is the writer goroutine: it collects queued writes and sends them in order.
func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	var (
		buf      []*op
		retryAt  time.Time
		pause    time.Duration
		outage   bool
		flushNow bool
	)
	for {
		// While the database is unreachable or a batch is full, nothing more is taken from the
		// queue: it fills, and a full queue drops whole trace groups.
		in := w.queue
		if outage || len(buf) >= w.cfg.BatchRows {
			in = nil
		}
		flushNow = false
		select {
		case o := <-in:
			buf = append(buf, o)
			flushNow = len(buf) >= w.cfg.BatchRows
		case <-ticker.C:
			w.logDrops()
			flushNow = !outage || !time.Now().Before(retryAt)
		case <-w.kick:
			if outage && time.Now().Before(retryAt) {
				continue
			}
			buf, flushNow = w.drainInto(buf), true
		case ctx := <-w.stop:
			w.finish(ctx, w.drainInto(buf))
			return
		}
		if !flushNow || len(buf) == 0 {
			continue
		}
		var held bool
		buf, held = w.flush(context.Background(), buf)
		switch {
		case held:
			if !outage {
				w.log.Warn("the database does not answer: trace writes wait in the queue and are retried",
					"waiting", len(buf)+len(w.queue))
			}
			pause = min(max(2*pause, w.cfg.Interval), maxRetryPause)
			outage, retryAt = true, time.Now().Add(pause)
		case outage:
			w.log.Info("trace writes reach the database again")
			outage, pause = false, 0
		}
	}
}

// drainInto appends every write waiting in the queue.
func (w *Writer) drainInto(buf []*op) []*op {
	for {
		select {
		case o := <-w.queue:
			buf = append(buf, o)
		default:
			return buf
		}
	}
}

// flush sends buf in batches of BatchRows. When the database is unreachable it stops and returns
// the writes it still holds, from the first unwritten one, with held true.
func (w *Writer) flush(ctx context.Context, buf []*op) ([]*op, bool) {
	for len(buf) > 0 {
		n := min(len(buf), w.cfg.BatchRows)
		if rest := w.send(ctx, buf[:n]); len(rest) > 0 {
			held := make([]*op, 0, len(rest)+len(buf)-n)
			return append(append(held, rest...), buf[n:]...), true
		}
		buf = buf[n:]
	}
	return buf[:0], false
}

// send writes one batch in order and settles what it wrote or dropped. It returns the writes it
// could not settle because the database stopped answering (none when the whole batch settled).
func (w *Writer) send(ctx context.Context, batch []*op) []*op {
	// The writes of a dropped group are skipped (and counted once they settle).
	live := make([]*op, 0, len(batch))
	w.mu.Lock()
	for _, o := range batch {
		if !w.dropped.has(o.w.TraceID()) {
			live = append(live, o)
		}
	}
	w.mu.Unlock()
	skipped := len(batch) - len(live)

	if w.batch != nil && len(live) > 1 {
		writes := make([]store.TraceWrite, len(live))
		for i, o := range live {
			writes[i] = o.w
		}
		bctx, cancel := context.WithTimeout(ctx, w.cfg.WriteTimeout)
		err := w.batch.WriteTraceBatch(bctx, writes)
		cancel()
		if err == nil {
			w.drop(skipped)
			w.settle(batch)
			return nil
		}
		w.log.Debug("trace batch failed; retrying write by write", "writes", len(live), "err", err)
	}

	// Write by write: a write the database refuses while it answers is dropped; one it cannot take
	// because it is down holds the rest of the batch for a later retry.
	refused := 0
	for i, o := range live {
		err := w.writeOne(ctx, o.w)
		if err == nil {
			continue
		}
		if w.unreachable(ctx) {
			// Settle what came before o: the writes it refused and the skipped writes among them
			// (k - i of them) count as dropped. o and everything after it wait for the retry.
			k := indexOf(batch, o)
			w.drop(refused + k - i)
			w.settle(batch[:k])
			return batch[k:]
		}
		refused++
		w.mu.Lock()
		w.cache.lose(o.w.TraceID())
		w.mu.Unlock()
		w.log.Error("trace write dropped: the database refused it", "trace_id", o.w.TraceID(), "err", err)
	}
	w.drop(refused + skipped)
	w.settle(batch)
	return nil
}

// writeOne runs one write through the inner store's own method.
func (w *Writer) writeOne(ctx context.Context, tw store.TraceWrite) error {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.WriteTimeout)
	defer cancel()
	switch tw.Kind {
	case store.WriteStart:
		return w.inner.StartTrace(ctx, tw.Trace)
	case store.WriteDecision:
		return w.inner.RecordDecision(ctx, tw.Trace, tw.Decision, tw.Body)
	case store.WriteAttempt:
		return w.inner.RecordAttempt(ctx, tw.Decision)
	case store.WriteLateDecision:
		return w.inner.RecordLateDecision(ctx, tw.Decision)
	}
	return tw.Check()
}

// unreachable reports whether the database does not answer a ping (an outage, not a bad write).
func (w *Writer) unreachable(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	pctx, cancel := context.WithTimeout(ctx, w.cfg.WriteTimeout)
	defer cancel()
	return w.inner.Ping(pctx) != nil
}

// settle marks the writes of ops (a prefix of what is unsettled, in order) settled: a trace leaves
// pending once its last queued write is among them, and barriers wake.
func (w *Writer) settle(ops []*op) {
	if len(ops) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq := ops[len(ops)-1].seq; seq > w.settled {
		w.settled = seq
	}
	for _, o := range ops {
		if id := o.w.TraceID(); w.pending[id] == o.seq {
			delete(w.pending, id)
		}
	}
	close(w.progress)
	w.progress = make(chan struct{})
}

// finish drains the queue at shutdown: it writes what it can within ctx, retrying while the
// database is unreachable, and drops the rest.
func (w *Writer) finish(ctx context.Context, buf []*op) {
	pause := min(w.cfg.Interval, DefaultInterval)
	for len(buf) > 0 && ctx.Err() == nil {
		var held bool
		buf, held = w.flush(ctx, buf)
		if !held {
			break
		}
		select {
		case <-time.After(pause):
			pause = min(2*pause, maxRetryPause)
		case <-ctx.Done():
		}
	}
	if len(buf) > 0 {
		w.mu.Lock()
		w.unwritten += len(buf)
		for _, o := range buf {
			w.cache.lose(o.w.TraceID())
		}
		w.mu.Unlock()
		w.drop(len(buf))
		w.settle(buf)
	}
	w.logDrops()
}

// drop counts n dropped writes and schedules a warning.
func (w *Writer) drop(n int) {
	if n <= 0 {
		return
	}
	w.cfg.Dropped(n)
	w.mu.Lock()
	w.dropsToLog += n
	w.mu.Unlock()
}

// logDrops warns about the writes dropped since the last warning, at most every dropLogEvery.
func (w *Writer) logDrops() {
	w.mu.Lock()
	n, now := w.dropsToLog, time.Now()
	if n == 0 || now.Sub(w.lastDropLog) < dropLogEvery && !w.closed {
		w.mu.Unlock()
		return
	}
	w.dropsToLog, w.lastDropLog = 0, now
	w.mu.Unlock()
	w.log.Warn("trace writes dropped (trustagent_gateway_trace_write_dropped_total)", "writes", n)
}

// detach copies a write's records, so a caller that changes its record after the call cannot
// change the queued write (the records' slices and documents are never changed in place).
func detach(tw store.TraceWrite) store.TraceWrite {
	if tw.Trace != nil {
		t := *tw.Trace
		tw.Trace = &t
	}
	if tw.Decision != nil {
		d := *tw.Decision
		tw.Decision = &d
	}
	if tw.Body != nil {
		b := *tw.Body
		tw.Body = &b
	}
	return tw
}

func indexOf(ops []*op, o *op) int {
	for i, x := range ops {
		if x == o {
			return i
		}
	}
	return len(ops)
}

// idSet is a set of trace ids that forgets the oldest past its capacity.
type idSet struct {
	max   int
	ids   map[string]struct{}
	order []string
}

func newIDSet(max int) *idSet { return &idSet{max: max, ids: map[string]struct{}{}} }

func (s *idSet) has(id string) bool {
	_, ok := s.ids[id]
	return ok
}

func (s *idSet) add(id string) {
	if s.has(id) {
		return
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.max {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
}
