package costs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/truthtable/backend-go/internal/store"
)

// rowSink is a RowStore and LedgerStore in memory: it keeps every inserted batch and fails the
// next failNext inserts (or every insert while failAll is set).
type rowSink struct {
	mu        sync.Mutex
	batches   [][]store.CostEvent
	failNext  int
	failAll   bool
	inserted  chan int
	totals    store.SpendTotals
	totalsAt  []time.Time
	crossedAt *time.Time
}

func newRowSink() *rowSink { return &rowSink{inserted: make(chan int, 100)} }

func (s *rowSink) InsertCostEvents(_ context.Context, rows []store.CostEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAll || s.failNext > 0 {
		if s.failNext > 0 {
			s.failNext--
		}
		return errors.New("database is down")
	}
	s.batches = append(s.batches, append([]store.CostEvent(nil), rows...))
	select {
	case s.inserted <- len(rows):
	default:
	}
	return nil
}

// CapCrossedAt answers crossedAt for every cap (nil: the stored rows never reached it).
func (s *rowSink) CapCrossedAt(context.Context, time.Time, bool, float64, float64, float64) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.crossedAt, nil
}

func (s *rowSink) SpendToday(_ context.Context, dayStart time.Time) (store.SpendTotals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalsAt = append(s.totalsAt, dayStart)
	return s.totals, nil
}

func (s *rowSink) rows() []store.CostEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.CostEvent
	for _, b := range s.batches {
		out = append(out, b...)
	}
	return out
}

func (s *rowSink) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int
	for _, b := range s.batches {
		out = append(out, len(b))
	}
	return out
}

func (s *rowSink) waitRows(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for len(s.rows()) < n {
		select {
		case <-s.inserted:
		case <-deadline:
			t.Fatalf("waited for %d rows, have %d", n, len(s.rows()))
		}
	}
}

func row(trace string) store.CostEvent {
	usd := 0.001
	return store.CostEvent{TraceID: trace, Component: "gate", Kind: store.CostKindDecision, CostUSD: &usd, CostSource: "computed"}
}

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// dropped reads cost_write_dropped_total.
func dropped() float64 {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return -1
	}
	for _, f := range families {
		if f.GetName() == "trustagent_gateway_cost_write_dropped_total" {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	return 0
}

// FlushRows waiting rows are written at once, without waiting for the tick; a lone row waits for
// the tick.
func TestCostWriterFlushesOnRowsAndOnTick(t *testing.T) {
	sink := newRowSink()
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 3, Logger: quietLog()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	for _, id := range []string{"t1", "t2", "t3"} {
		if !w.Add(row(id)) {
			t.Fatal("a row was dropped from an empty buffer")
		}
	}
	sink.waitRows(t, 3)
	cancel()
	<-done

	sink = newRowSink()
	w = NewWriter(sink, WriterConfig{FlushEvery: 10 * time.Millisecond, FlushRows: 100, Logger: quietLog()})
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	w.Add(row("lone"))
	sink.waitRows(t, 1)
	cancel()
	<-done
	if got := sink.rows(); len(got) != 1 || got[0].TraceID != "lone" {
		t.Errorf("rows = %+v", got)
	}
}

// Flush drains the buffer in inserts of at most FlushRows rows, oldest first; stopping writes
// what is left, and later rows are dropped and counted.
func TestCostWriterDrainsAndStops(t *testing.T) {
	sink := newRowSink()
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 2, MaxBuffered: 100, Logger: quietLog()})
	for i := range 5 {
		w.Add(row(string(rune('a' + i))))
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sink.batchSizes(); len(got) != 3 || got[0] != 2 || got[2] != 1 {
		t.Errorf("batches = %v, want [2 2 1]", got)
	}
	if rows := sink.rows(); rows[0].TraceID != "a" || rows[4].TraceID != "e" || w.Pending() != 0 {
		t.Errorf("order = %v, pending %d", rows, w.Pending())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	w.Add(row("last"))
	cancel()
	<-done
	if rows := sink.rows(); rows[len(rows)-1].TraceID != "last" {
		t.Errorf("the final flush did not write the last row: %v", rows)
	}
	before := dropped()
	if w.Add(row("after-stop")) {
		t.Error("a stopped writer accepted a row")
	}
	if dropped() != before+1 {
		t.Errorf("cost_write_dropped_total +%v after a stopped Add", dropped()-before)
	}
}

// A full buffer drops the row and counts it (D51).
func TestCostWriterDropsWhenFull(t *testing.T) {
	sink := newRowSink()
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 10, MaxBuffered: 2, Logger: quietLog()})
	before := dropped()
	if !w.Add(row("1")) || !w.Add(row("2")) {
		t.Fatal("rows below the limit were dropped")
	}
	if w.Add(row("3")) {
		t.Error("a row past MaxBuffered was kept")
	}
	if dropped() != before+1 || w.Pending() != 2 {
		t.Errorf("dropped +%v, pending %d", dropped()-before, w.Pending())
	}
}

// A failed insert is retried by later flushes; a row that fails three times is dropped and
// counted.
func TestCostWriterRetriesThenDrops(t *testing.T) {
	sink := newRowSink()
	sink.failNext = 2
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 10, Logger: quietLog()})
	w.Add(row("retry"))
	before := dropped()
	for i := range 2 {
		if err := w.Flush(context.Background()); err == nil {
			t.Fatalf("flush %d did not report the failure", i)
		}
		if w.Pending() != 1 {
			t.Fatalf("the failed row left the buffer after flush %d", i)
		}
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.rows()) != 1 || dropped() != before {
		t.Errorf("rows %d, dropped +%v", len(sink.rows()), dropped()-before)
	}

	sink.failAll = true
	w.Add(row("doomed"))
	for range defaultMaxTries {
		_ = w.Flush(context.Background())
	}
	if w.Pending() != 0 || dropped() != before+1 {
		t.Errorf("after %d failures: pending %d, dropped +%v", defaultMaxTries, w.Pending(), dropped()-before)
	}
}

// The final flush (the writer stopped) goes on past a failed insert: the rows of the failed
// insert are dropped and counted, and the rows after it are still written, so no row is lost
// without a count (D51).
func TestCostWriterFinalFlushWritesOrCountsEveryRow(t *testing.T) {
	sink := newRowSink()
	sink.failNext = 1
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 2, Logger: quietLog()})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		w.Add(row(id))
	}
	before := dropped()
	w.stop()
	if got := sink.rows(); len(got) != 3 || got[0].TraceID != "c" || got[2].TraceID != "e" {
		t.Errorf("rows written after the failed insert = %+v", got)
	}
	if w.Pending() != 0 || dropped() != before+2 {
		t.Errorf("pending %d, dropped +%v, want 0 and +2", w.Pending(), dropped()-before)
	}

	// With the database down, every row left is dropped and counted, not only the first insert's.
	down := newRowSink()
	down.failAll = true
	w = NewWriter(down, WriterConfig{FlushEvery: time.Hour, FlushRows: 2, Logger: quietLog()})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		w.Add(row(id))
	}
	before = dropped()
	w.stop()
	if w.Pending() != 0 || dropped() != before+5 {
		t.Errorf("database down at stop: pending %d, dropped +%v, want 0 and +5", w.Pending(), dropped()-before)
	}
}

// A flush its own context cut short (shutdown) costs the rows no try: they stay buffered for the
// final flush however often that happens.
func TestCostWriterCanceledFlushKeepsTheRows(t *testing.T) {
	sink := newRowSink()
	sink.failAll = true
	w := NewWriter(sink, WriterConfig{FlushEvery: time.Hour, FlushRows: 10, Logger: quietLog()})
	w.Add(row("kept"))
	before := dropped()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range defaultMaxTries + 1 {
		if err := w.Flush(ctx); err == nil {
			t.Fatal("a failed flush reported no error")
		}
	}
	if w.Pending() != 1 || dropped() != before {
		t.Fatalf("after canceled flushes: pending %d, dropped +%v", w.Pending(), dropped()-before)
	}
	sink.mu.Lock()
	sink.failAll = false
	sink.mu.Unlock()
	w.stop()
	if got := sink.rows(); len(got) != 1 || got[0].TraceID != "kept" || dropped() != before {
		t.Errorf("the final flush = %+v, dropped +%v", got, dropped()-before)
	}
}
