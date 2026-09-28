package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func policyVersion(source, sha string, fileSHA *string) *PolicyVersionRecord {
	return &PolicyVersionRecord{
		Source: source, Author: "tester", Message: source + " " + sha, Doc: json.RawMessage(`{"version":1}`),
		YAML: "version: 1\n", SHA256: sha, FileSHA256: fileSHA,
	}
}

func TestPolicyVersionsLifecycle(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if _, err := s.LatestPolicyVersion(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty table: %v", err)
	}
	if id, err := s.PolicyHeadID(ctx); err != nil || id != 0 {
		t.Fatalf("head id %d %v", id, err)
	}
	var ids []int64
	for i := range 5 {
		source := PolicySourceDashboard
		var fileSHA *string
		if i%2 == 0 {
			source, fileSHA = PolicySourceFile, strp("f"+strconv.Itoa(i))
		}
		err := s.WithPolicyLock(ctx, "", func(tx PolicyTx) error {
			head, err := tx.Head(ctx)
			if err != nil {
				return err
			}
			v := policyVersion(source, "s"+strconv.Itoa(i), fileSHA)
			if head != nil {
				v.ParentID = &head.ID
			}
			if err := tx.Insert(ctx, v); err != nil {
				return err
			}
			if v.ID == 0 || v.CreatedAt.IsZero() || v.CreatedAt.Location() != time.UTC {
				t.Errorf("insert did not fill id/created_at: %+v", v)
			}
			ids = append(ids, v.ID)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	head, err := s.LatestPolicyVersion(ctx)
	if err != nil || head.ID != ids[4] || head.ParentID == nil || *head.ParentID != ids[3] || *head.FileSHA256 != "f4" {
		t.Fatalf("head %+v %v", head, err)
	}
	if id, _ := s.PolicyHeadID(ctx); id != ids[4] {
		t.Errorf("head id %d", id)
	}
	err = s.WithPolicyLock(ctx, "", func(tx PolicyTx) error {
		lf, err := tx.LastFileVersion(ctx)
		if err != nil || lf.ID != ids[4] {
			t.Errorf("last file %+v %v", lf, err)
		}
		ld, err := tx.LastDashboardVersion(ctx)
		if err != nil || ld.ID != ids[3] || ld.Source != PolicySourceDashboard {
			t.Errorf("last dashboard %+v %v", ld, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Keyset pages, newest first.
	page1, next, err := s.ListPolicyVersions(ctx, 2, "")
	if err != nil || len(page1) != 2 || page1[0].ID != ids[4] || next == "" {
		t.Fatalf("page 1 %+v %q %v", page1, next, err)
	}
	page2, next, err := s.ListPolicyVersions(ctx, 2, next)
	if err != nil || len(page2) != 2 || page2[0].ID != ids[2] {
		t.Fatalf("page 2 %+v %v", page2, err)
	}
	page3, next, err := s.ListPolicyVersions(ctx, 2, next)
	if err != nil || len(page3) != 1 || page3[0].ID != ids[0] || next != "" {
		t.Fatalf("page 3 %+v %q %v", page3, next, err)
	}
	if _, _, err := s.ListPolicyVersions(ctx, 2, "bogus!"); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}
	got, err := s.GetPolicyVersion(ctx, ids[1])
	if err != nil || got.Source != PolicySourceDashboard || got.SHA256 != "s1" || string(got.Doc) != `{"version": 1}` && string(got.Doc) != `{"version":1}` {
		t.Errorf("get %+v %v", got, err)
	}
	if _, err := s.GetPolicyVersion(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing version: %v", err)
	}

	// An error inside the lock rolls back.
	boom := errors.New("boom")
	err = s.WithPolicyLock(ctx, "", func(tx PolicyTx) error {
		if err := tx.Insert(ctx, policyVersion(PolicySourceDashboard, "rolled-back", nil)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if id, _ := s.PolicyHeadID(ctx); id != ids[4] {
		t.Errorf("rolled-back insert is visible: head %d", id)
	}
	// The table's CHECK refuses unknown sources.
	err = s.WithPolicyLock(ctx, "", func(tx PolicyTx) error {
		return tx.Insert(ctx, policyVersion("builtin", "x", nil))
	})
	if err == nil {
		t.Error("source builtin must be refused")
	}
}

// TestPolicyLockSerializesWriters runs concurrent read-then-insert writers: under the lock each
// sees the previous writer's row, so the parents form a chain.
func TestPolicyLockSerializesWriters(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const writers = 6
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.WithPolicyLock(ctx, "", func(tx PolicyTx) error {
				head, err := tx.Head(ctx)
				if err != nil {
					return err
				}
				v := policyVersion(PolicySourceDashboard, "w"+strconv.Itoa(i), nil)
				if head != nil {
					v.ParentID = &head.ID
				}
				time.Sleep(5 * time.Millisecond) // widen the race window
				return tx.Insert(ctx, v)
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	all, _, err := s.ListPolicyVersions(ctx, 50, "")
	if err != nil || len(all) != writers {
		t.Fatalf("%d versions %v", len(all), err)
	}
	for i, v := range all[:len(all)-1] {
		if v.ParentID == nil || *v.ParentID != all[i+1].ID {
			t.Errorf("version %d has parent %v, want %d", v.ID, v.ParentID, all[i+1].ID)
		}
	}
}

func TestListenNotify(t *testing.T) {
	s, schema := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := ScopedConfigChannel(schema.Name)
	if len(channel) > 63 || !strings.HasPrefix(channel, ConfigChannel+"_") {
		t.Fatalf("channel %q", channel)
	}
	ready := make(chan struct{})
	got := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		done <- s.Listen(ctx, channel, func() { close(ready) }, func(p string) { got <- p })
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("listener not ready")
	}
	if err := s.Notify(ctx, channel, "settings:x"); err != nil {
		t.Fatal(err)
	}
	var id int64
	err := s.WithPolicyLock(ctx, channel, func(tx PolicyTx) error {
		v := policyVersion(PolicySourceDashboard, "n", nil)
		err := tx.Insert(ctx, v)
		id = v.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A notification on another channel is not delivered.
	if err := s.Notify(ctx, ConfigChannel+"_elsewhere", "nope"); err != nil {
		t.Fatal(err)
	}
	want := []string{"settings:x", "policy_versions:" + strconv.FormatInt(id, 10)}
	for _, w := range want {
		select {
		case p := <-got:
			if p != w {
				t.Errorf("payload %q, want %q", p, w)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no notification %q", w)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("listen returned %v", err)
	}
	select {
	case p := <-got:
		t.Errorf("unexpected payload %q", p)
	default:
	}
}

func TestEvaluatorVersionsUpsertIsIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	item := func(id string, v int, sha, name string) EvaluatorUpsert {
		return EvaluatorUpsert{
			Evaluator: EvaluatorRecord{ID: id, Name: name, Category: "quality", Origin: EvaluatorOriginBuiltin},
			Version: EvaluatorVersionRecord{EvaluatorID: id, Version: v, Spec: json.RawMessage(`{"id":"` + id + `"}`),
				SpecSHA256: sha, QuestionSHA256: "q" + sha, CalibrationKey: "c" + sha, CreatedBy: "system", Changelog: "c"},
		}
	}
	first := []EvaluatorUpsert{item("tone", 1, "a1", "Tone"), item("tone", 2, "a2", "Tone v2"), item("refusal", 1, "r1", "Refusal")}
	res, err := s.UpsertEvaluatorVersions(ctx, "", first)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Inserted {
			t.Errorf("%s not inserted", r.Ref)
		}
	}
	evs, err := s.ListEvaluators(ctx)
	if err != nil || len(evs) != 2 || evs[1].ID != "tone" || evs[1].LatestVersion != 2 || evs[1].Name != "Tone v2" {
		t.Fatalf("evaluators %+v %v", evs, err)
	}
	fp1, err := s.EvaluatorsFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated := evs[1].UpdatedAt

	// Again, plus a changed spec for tone@1 and a backfilled old version: nothing existing changes.
	again := append(first, item("tone", 1, "CHANGED", "Tone"))
	res, err = s.UpsertEvaluatorVersions(ctx, "", again)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Inserted {
			t.Errorf("%s inserted twice", r.Ref)
		}
		if i == 3 && r.StoredSHA256 != "a1" {
			t.Errorf("conflict reports stored sha %q, want a1", r.StoredSHA256)
		}
	}
	if fp2, _ := s.EvaluatorsFingerprint(ctx); fp2 != fp1 {
		t.Errorf("fingerprint changed on a no-op upsert: %s -> %s", fp1, fp2)
	}
	evs, _ = s.ListEvaluators(ctx)
	if !evs[1].UpdatedAt.Equal(updated) || evs[1].LatestVersion != 2 {
		t.Errorf("no-op upsert touched the evaluator: %+v", evs[1])
	}
	vers, err := s.ListEvaluatorVersions(ctx)
	if err != nil || len(vers) != 3 || vers[1].Ref() != "tone@1" || vers[1].SpecSHA256 != "a1" {
		t.Fatalf("versions %+v %v", vers, err)
	}

	// Registering an older version later keeps latest_version and the newest name.
	if _, err := s.UpsertEvaluatorVersions(ctx, "", []EvaluatorUpsert{item("refusal", 3, "r3", "Refusal 3"), item("refusal", 2, "r2", "Refusal 2")}); err != nil {
		t.Fatal(err)
	}
	evs, _ = s.ListEvaluators(ctx)
	if evs[0].LatestVersion != 3 || evs[0].Name != "Refusal 3" {
		t.Errorf("refusal %+v", evs[0])
	}
	if fp3, _ := s.EvaluatorsFingerprint(ctx); fp3 == fp1 {
		t.Error("fingerprint did not change after new versions")
	}

	// Versions are immutable (migration 0003 trigger).
	_, err = s.pool.Exec(ctx, `UPDATE evaluator_versions SET changelog = 'x' WHERE evaluator_id = 'tone'`)
	if err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("update of a version: %v", err)
	}
	if _, err := s.UpsertEvaluatorVersions(ctx, "", []EvaluatorUpsert{{Evaluator: EvaluatorRecord{ID: "a"}, Version: EvaluatorVersionRecord{EvaluatorID: "b", Version: 1}}}); err == nil {
		t.Error("mismatched ids must be refused")
	}
}

func TestHumanLabelCounts(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for i, label := range []string{"should_fire", "should_fire", "should_not_fire", "hallucinated"} {
		target := "groundedness@1"
		if label == "hallucinated" {
			target = "response"
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO labels (trace_id, target, label, labeled_by) VALUES ($1, $2, $3, 'me')`,
			"t"+strconv.Itoa(i), target, label); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.HumanLabelCounts(ctx, "groundedness@1")
	if err != nil || c != (LabelCounts{N: 3, Pos: 2, Neg: 1}) {
		t.Errorf("counts %+v %v", c, err)
	}
	if c, _ := s.HumanLabelCounts(ctx, "contradiction@1"); c != (LabelCounts{}) {
		t.Errorf("empty target %+v", c)
	}
}

func TestForEachReplayDecision(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var traceIDs []string
	for i := range 4 {
		start := pendingTrace("support-bot", now.Add(-time.Duration(i)*time.Minute))
		if i == 3 {
			start.Route = "other"
		}
		start.Stream = i == 1
		if err := s.StartTrace(ctx, start); err != nil {
			t.Fatal(err)
		}
		tr, d := decided(start, "flag", "flag", "contradicted", 0.9, []string{"contradiction:critical"})
		d.Route = start.Route
		d.DecisionSnapshot = strp("mock/jev-heuristic-v1")
		var b *BodyRecord
		if i == 0 {
			b = body(start.TraceID, now.Add(time.Hour))
		}
		if err := s.RecordDecision(ctx, tr, d, b); err != nil {
			t.Fatal(err)
		}
		// The window and the order use the decision time; pin it to the trace time.
		if _, err := s.pool.Exec(ctx, `UPDATE gate_decisions SET created_at = $2 WHERE trace_id = $1`, start.TraceID, start.CreatedAt); err != nil {
			t.Fatal(err)
		}
		traceIDs = append(traceIDs, start.TraceID)
	}
	// A fallback re-gate (attempt 1) is not replayed.
	if _, err := s.pool.Exec(ctx, `INSERT INTO gate_decisions (trace_id, attempt, route, mode, effective_mode, status, action, decision)
		VALUES ($1, 1, 'support-bot', 'flag', 'flag', 'ok', 'pass', '{}')`, traceIDs[0]); err != nil {
		t.Fatal(err)
	}
	audit := sampleAudit(NewID(), 0.2, true)
	audit.TraceID, audit.Verdict = traceIDs[1], strp("HALLUCINATED")
	if err := s.SaveAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}

	var rows []*ReplayRow
	collect := func(f ReplayFilter) {
		rows = nil
		if err := s.ForEachReplayDecision(ctx, f, func(r *ReplayRow) error { rows = append(rows, r); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	collect(ReplayFilter{Since: now.Add(-time.Hour), WithBodies: true})
	if len(rows) != 4 || rows[0].TraceID != traceIDs[0] || rows[3].TraceID != traceIDs[3] {
		t.Fatalf("rows %d", len(rows))
	}
	if rows[0].Body == nil || rows[0].Body.ResponseText == nil || *rows[0].Body.ResponseText != "Within 60 days." || rows[1].Body != nil {
		t.Errorf("bodies %+v %+v", rows[0].Body, rows[1].Body)
	}
	if !rows[1].Stream || rows[0].Stream || rows[1].AuditVerdict == nil || *rows[1].AuditVerdict != "HALLUCINATED" || rows[0].AuditVerdict != nil {
		t.Errorf("stream/audit %+v %+v", rows[0], rows[1])
	}
	if rows[0].PolicyVersion == nil || *rows[0].PolicyVersion != 3 || rows[0].Source != "trafficgen" || rows[0].DecisionSnapshot == nil {
		t.Errorf("row 0 %+v", rows[0])
	}
	var doc map[string]any
	if err := json.Unmarshal(rows[0].Decision, &doc); err != nil || doc["spec"] != "decision/v1" {
		t.Errorf("decision %s", rows[0].Decision)
	}

	collect(ReplayFilter{Since: now.Add(-90 * time.Second), Routes: []string{"support-bot"}})
	if len(rows) != 2 || rows[0].Body != nil {
		t.Errorf("window+route: %d rows", len(rows))
	}
	collect(ReplayFilter{Since: now.Add(-time.Hour), Limit: 1})
	if len(rows) != 1 {
		t.Errorf("limit: %d rows", len(rows))
	}
	stop := errors.New("stop")
	if err := s.ForEachReplayDecision(ctx, ReplayFilter{Since: now.Add(-time.Hour)}, func(*ReplayRow) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("callback error: %v", err)
	}
}
