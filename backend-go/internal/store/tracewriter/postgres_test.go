package tracewriter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func newPostgres(t *testing.T) *store.PostgresStore {
	t.Helper()
	schema := storetest.New(t) // skips without TEST_DATABASE_URL
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg
}

// pgTrace gives the test traces UUIDv7 ids (any text works; these sort like the gateway's).
func pgTrace(i int) string { return fmt.Sprintf("%s-%03d", store.NewID(), i) }

// TestWriterAgainstPostgres: concurrent traces through the batch path land whole and in order in
// Postgres (late flag kept, audit status completed and joined), an audit saved before the first
// flush is not overwritten by the decision queued before it (D83), and a write Postgres refuses is
// dropped while the rest of its batch lands.
func TestWriterAgainstPostgres(t *testing.T) {
	pg := newPostgres(t)
	w, drops := newWriter(t, pg, Config{BatchRows: 16, Interval: 10 * time.Millisecond})
	ctx := context.Background()

	ids := make([]string, 40)
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for i := range ids {
		ids[i] = pgTrace(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- writeTrace(ctx, w, ids[i])
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		if err := w.Barrier(ctx, id); err != nil {
			t.Fatal(err)
		}
		rec, err := pg.GetTrace(ctx, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		sum := rec.Summary()
		if sum.GateStatus != "ok" || sum.Audit.Status != "completed" || sum.Audit.Verdict == nil ||
			*sum.Audit.Verdict != "HALLUCINATED" || !slices.Contains(sum.Flags, store.FlagLateDecision) ||
			*sum.QuestionPreview != "How long is the refund window?" {
			t.Errorf("%s stored as %+v", id, sum)
		}
		docs, err := pg.TraceDecisions(ctx, id)
		if err != nil || len(docs) != 1 {
			t.Fatalf("%s decisions: %s, %v", id, docs, err)
		}
		var doc struct {
			Late bool `json:"late"`
		}
		if err := json.Unmarshal(docs[0], &doc); err != nil || !doc.Late {
			t.Errorf("%s: the late decision is the attempt's word: %s", id, docs[0])
		}
	}

	// D83: the audit completes while the decision still waits for the interval.
	slow, _ := newWriter(t, pg, Config{Interval: time.Hour})
	id := pgTrace(99)
	rec, d := decidedRec(id)
	if err := slow.StartTrace(ctx, startRec(id)); err != nil {
		t.Fatal(err)
	}
	if err := slow.RecordDecision(ctx, rec, d, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetTrace(ctx, id); err != store.ErrNotFound {
		t.Fatalf("the decision was written before the interval: %v", err)
	}
	if err := slow.SaveAuditResult(ctx, auditRec(id), "completed"); err != nil {
		t.Fatal(err)
	}
	if err := slow.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetTrace(ctx, id)
	if err != nil || got.AuditStatus != "completed" || got.Audit.Verdict == nil {
		t.Fatalf("audit status after the drain: %+v, %v", got, err)
	}

	// A refused write (its decision document is not JSON) is dropped; its batch neighbours land.
	good, bad := pgTrace(100), pgTrace(101)
	brec, bd := decidedRec(bad)
	bd.Decision = json.RawMessage(`{not json`)
	if err := w.StartTrace(ctx, startRec(good)); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordDecision(ctx, brec, bd, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Barrier(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetTrace(ctx, good); err != nil {
		t.Errorf("the good neighbour was not written: %v", err)
	}
	if _, err := pg.GetTrace(ctx, bad); err != store.ErrNotFound {
		t.Errorf("the refused write: %v", err)
	}
	if drops.get() != 1 {
		t.Errorf("dropped = %d, want 1", drops.get())
	}
	if got, err := w.GetTrace(ctx, bad); err != nil || got.GateStatus != "ok" {
		t.Errorf("the refused trace reads from memory: %+v, %v", got, err)
	}
}
