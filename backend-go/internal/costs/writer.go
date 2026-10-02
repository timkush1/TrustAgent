package costs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/store"
)

// RowStore persists ledger rows (store.PostgresStore).
type RowStore interface {
	InsertCostEvents(ctx context.Context, rows []store.CostEvent) error
}

// Writer defaults (P4-PLAN COST-4).
const (
	DefaultFlushEvery  = 250 * time.Millisecond
	DefaultFlushRows   = 200
	DefaultMaxBuffered = 10_000
	// defaultMaxTries is how many flushes a row may fail before it is dropped.
	defaultMaxTries = 3
	// finalFlushTimeout bounds the flush when the writer stops.
	finalFlushTimeout = 5 * time.Second
	// failureLogEvery limits the warnings of a failing database to one a minute.
	failureLogEvery = time.Minute
)

// WriterConfig configures a Writer; zero values take the defaults.
type WriterConfig struct {
	// FlushEvery is how often buffered rows are written; FlushRows how many rows trigger a write
	// sooner (and the most one insert carries).
	FlushEvery time.Duration
	FlushRows  int
	// MaxBuffered is how many rows wait at most; one more is dropped (D51).
	MaxBuffered int
	Logger      *slog.Logger
}

// Writer buffers cost_events rows and writes them in batches, so a call never waits for the
// database: every FlushEvery, or as soon as FlushRows are waiting. A row that does not fit in the
// buffer, or that failed to write three times, is dropped and counted in
// cost_write_dropped_total (P4-PLAN D51); the spend it carries was booked in memory already, so
// the caps never miss it. Stop drains it.
type Writer struct {
	st  RowStore
	cfg WriterConfig

	mu       sync.Mutex
	buf      []pendingRow
	stopped  bool
	lastWarn time.Time

	kick chan struct{}
	// flushMu serializes flushes (the loop's and Flush's).
	flushMu sync.Mutex
}

type pendingRow struct {
	row   store.CostEvent
	tries int
}

// NewWriter builds a writer over st.
func NewWriter(st RowStore, cfg WriterConfig) *Writer {
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = DefaultFlushEvery
	}
	if cfg.FlushRows <= 0 {
		cfg.FlushRows = DefaultFlushRows
	}
	if cfg.MaxBuffered <= 0 {
		cfg.MaxBuffered = DefaultMaxBuffered
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Writer{st: st, cfg: cfg, kick: make(chan struct{}, 1)}
}

// Add queues a row without blocking. It reports false when the row was dropped: the buffer is
// full or the writer has stopped.
func (w *Writer) Add(row store.CostEvent) bool {
	w.mu.Lock()
	if w.stopped || len(w.buf) >= w.cfg.MaxBuffered {
		w.mu.Unlock()
		metrics.CostWriteDropped()
		return false
	}
	w.buf = append(w.buf, pendingRow{row: row})
	full := len(w.buf) >= w.cfg.FlushRows
	w.mu.Unlock()
	if full {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
	return true
}

// Pending is how many rows wait to be written.
func (w *Writer) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buf)
}

// Run writes the buffered rows every FlushEvery, and sooner once FlushRows wait, until ctx ends;
// it then writes what is left (bounded by five seconds), drops what still fails, and stops: later
// rows are dropped.
func (w *Writer) Run(ctx context.Context) {
	t := time.NewTicker(w.cfg.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.stop()
			return
		case <-t.C:
		case <-w.kick:
		}
		_ = w.flush(ctx, false)
	}
}

// Flush writes every buffered row now (the drain at shutdown, while Run still runs). Rows that
// fail stay buffered for a later flush, up to their third failure (a flush ctx cut short costs
// them no try); the error is the first write error.
func (w *Writer) Flush(ctx context.Context) error { return w.flush(ctx, false) }

// stop marks the writer stopped and writes what is left; rows that fail now are dropped.
func (w *Writer) stop() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
	defer cancel()
	_ = w.flush(ctx, true)
}

// flush writes the buffer in inserts of at most FlushRows rows, oldest first, and returns the
// first write error. A failed insert puts its rows back at the front with one more try, or drops
// them once they failed defaultMaxTries times, and ends this flush. The final flush (the writer
// has stopped, so nothing would write them later) drops a failed insert's rows at once and goes
// on with the rest, so every row is either written or counted as dropped.
func (w *Writer) flush(ctx context.Context, final bool) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	var first error
	for {
		w.mu.Lock()
		n := min(len(w.buf), w.cfg.FlushRows)
		if n == 0 {
			w.mu.Unlock()
			return first
		}
		chunk := make([]pendingRow, n)
		copy(chunk, w.buf[:n])
		w.buf = w.buf[n:]
		w.mu.Unlock()

		rows := make([]store.CostEvent, n)
		for i, p := range chunk {
			rows[i] = p.row
		}
		err := w.st.InsertCostEvents(ctx, rows)
		if err == nil {
			continue
		}
		// A flush cut short by its own context (shutdown) is not a failure of the rows.
		w.failed(chunk, err, final, ctx.Err() != nil)
		if first == nil {
			first = err
		}
		if !final {
			return first
		}
	}
}

// failed requeues the rows of a failed insert at the front of the buffer, dropping those that
// used their tries (all of them when final) and those that no longer fit. An insert its context
// cancelled (canceled) costs the rows no try: the final flush still writes them.
func (w *Writer) failed(chunk []pendingRow, err error, final, canceled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var keep []pendingRow
	dropped := 0
	for _, p := range chunk {
		if !canceled {
			p.tries++
		}
		if final || p.tries >= defaultMaxTries {
			dropped++
			continue
		}
		keep = append(keep, p)
	}
	if room := w.cfg.MaxBuffered - len(w.buf); len(keep) > room {
		dropped += len(keep) - max(room, 0)
		keep = keep[:max(room, 0)]
	}
	w.buf = append(keep, w.buf...)
	for range dropped {
		metrics.CostWriteDropped()
	}
	if now := time.Now(); now.Sub(w.lastWarn) >= failureLogEvery {
		w.lastWarn = now
		w.cfg.Logger.Warn("cost ledger: writing cost rows failed; they are retried, then dropped",
			"rows", len(chunk), "dropped", dropped, "err", err)
	}
}
