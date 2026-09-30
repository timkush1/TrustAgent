package recording_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/recording"
	"github.com/truthtable/backend-go/internal/store"
)

// fakeTraces is an in-memory TraceStore of n traces, newest first; odd traces have a deep audit
// (trace 1's audit row is missing: still queued) and even traces a body.
type fakeTraces struct {
	recs      []store.TraceRecord
	pageSizes []int
	bodyReads int
	failAudit bool
}

func newFakeTraces(n int) *fakeTraces {
	f := &fakeTraces{}
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	for i := range n {
		r := store.TraceRecord{TraceID: "t" + strconv.Itoa(i), CreatedAt: base.Add(-time.Duration(i) * time.Minute),
			Source: "trafficgen", Route: "support-bot", GateStatus: "ok"}
		if i%2 == 1 {
			id := "a" + strconv.Itoa(i)
			r.AuditID = &id
		}
		f.recs = append(f.recs, r)
	}
	return f
}

func (f *fakeTraces) ListTraces(_ context.Context, tf store.TraceFilter) ([]store.TraceRecord, string, error) {
	start := 0
	if tf.Cursor != "" {
		start, _ = strconv.Atoi(tf.Cursor)
	}
	f.pageSizes = append(f.pageSizes, tf.Limit)
	end := min(start+min(tf.Limit, 2), len(f.recs)) // the store pages by two here
	next := ""
	if end < len(f.recs) {
		next = strconv.Itoa(end)
	}
	return f.recs[start:end], next, nil
}

func (f *fakeTraces) GetTraceBody(_ context.Context, id string) (*store.BodyRecord, error) {
	f.bodyReads++
	if n, _ := strconv.Atoi(id[1:]); n%2 == 1 {
		return nil, store.ErrNotFound
	}
	text := "body of " + id
	return &store.BodyRecord{TraceID: id, ResponseText: &text, ExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeTraces) TraceDecisions(_ context.Context, id string) ([]json.RawMessage, error) {
	return []json.RawMessage{json.RawMessage(`{"spec":"decision/v1","trace_id":"` + id + `"}`)}, nil
}

func (f *fakeTraces) GetAudit(_ context.Context, id string) (*store.AuditRecord, error) {
	switch {
	case f.failAudit:
		return nil, errors.New("database gone")
	case id == "a1":
		return nil, store.ErrNotFound
	}
	cost := 0.001
	return &store.AuditRecord{AuditID: id, Status: store.AuditCompleted, ScoringVersion: "v2", CostUSD: &cost}, nil
}

func TestDBSource(t *testing.T) {
	st := newFakeTraces(5)
	src := &recording.DBSource{Store: st, Now: func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) }, Bodies: true}
	traces, err := src.Traces(context.Background(), recording.Filter{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 4 || traces[0].Detail.TraceID != "t0" || traces[3].Detail.TraceID != "t3" {
		t.Fatalf("traces %v", traces)
	}
	// Pages follow the cursor and ask only for what is still missing.
	if len(st.pageSizes) != 2 || st.pageSizes[0] != 4 || st.pageSizes[1] != 2 {
		t.Errorf("page sizes %v", st.pageSizes)
	}
	t0, t1, t3 := traces[0].Detail, traces[1].Detail, traces[3].Detail
	if !t0.Body.Available || t0.Body.ResponseText == nil || *t0.Body.ResponseText != "body of t0" || len(t0.Decisions) != 1 {
		t.Errorf("t0 %+v", t0)
	}
	if t1.Body.Available || t1.Body.UnavailableReason == nil || *t1.Body.UnavailableReason != store.BodyNotStored ||
		t1.AuditDetail != nil || traces[1].LLMUSD != nil {
		t.Errorf("t1 (no body, audit still queued) %+v", t1)
	}
	if t3.AuditDetail == nil || t3.AuditDetail.AuditID != "a3" || traces[3].LLMUSD == nil || *traces[3].LLMUSD != 0.001 {
		t.Errorf("t3 audit %+v", t3.AuditDetail)
	}

	// Without bodies (the manifest alone): no body reads.
	st = newFakeTraces(3)
	traces, err = (&recording.DBSource{Store: st}).Traces(context.Background(), recording.Filter{})
	if err != nil || len(traces) != 3 || st.bodyReads != 0 || traces[0].Detail.Body.Available {
		t.Errorf("without bodies: %d traces, %d body reads, %v", len(traces), st.bodyReads, err)
	}
	// A limit above MaxTraces is capped.
	st = newFakeTraces(1)
	if _, err := (&recording.DBSource{Store: st}).Traces(context.Background(), recording.Filter{Limit: 5000}); err != nil ||
		st.pageSizes[0] != recording.MaxTraces {
		t.Errorf("cap: %v %v", st.pageSizes, err)
	}
	st = newFakeTraces(2)
	st.failAudit = true
	if _, err := (&recording.DBSource{Store: st}).Traces(context.Background(), recording.Filter{}); err == nil {
		t.Error("a failing audit read is an error")
	}
}
