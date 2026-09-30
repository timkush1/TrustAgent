package main

import (
	"context"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func TestRetentionJobRunsWithADatabase(t *testing.T) {
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	// A trace from before RETENTION_DAYS (30 by default).
	old := time.Now().UTC().AddDate(0, 0, -45)
	label := "trafficgen"
	tr := &store.TraceRecord{TraceID: store.NewID(), CreatedAt: old, Source: "proxy", APIFormat: "openai_chat",
		Path: "/v1/chat/completions", Route: "default", Mode: "observe", KeyLabel: &label, Reasons: []string{}, Flags: []string{}}
	if err := pg.StartTrace(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	first, every := retentionFirst, retentionEvery
	retentionFirst, retentionEvery = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { retentionFirst, retentionEvery = first, every })

	newTestEnv(t, envOpts{store: pg})
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := pg.GetTrace(context.Background(), tr.TraceID)
		if err == store.ErrNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the retention job did not delete the old trace: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
