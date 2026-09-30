package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
	"github.com/truthtable/contracts"
)

const schemaDecisionPage = "api.v1.schema.json#/$defs/Page%3CGateDecisionRow%3E"

// errorCodeOf is the code of an ApiErrorBody, validated against the schema.
func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	contractstest.ValidateJSON(t, schemaError, body)
	var e apiErrorBody
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

// pgEnv is a test server on a fresh Postgres schema (skipped without TEST_DATABASE_URL).
func pgEnv(t *testing.T, o envOpts) (*testEnv, *store.PostgresStore) {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	o.store = pg
	return newTestEnv(t, o), pg
}

func TestGateDecisionsNeedTheDatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{})
	code, body := env.get("/api/gate/decisions", testKey)
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("without a database: %d %s", code, body)
	}
	if code, _ := env.get("/api/gate/decisions", ""); code != http.StatusUnauthorized {
		t.Errorf("without a key: %d", code)
	}
}

func TestGateDecisionsAgainstPostgres(t *testing.T) {
	env, _ := pgEnv(t, envOpts{})
	scenarios := loadDemoScenarios(t)
	sent := []string{"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed"}
	trace := map[string]string{}
	for _, id := range sent {
		traceID := env.send(scenarios[id]).Header.Get("X-TrustAgent-Trace-Id")
		env.settledTrace(traceID)
		trace[id] = traceID
	}

	read := func(query string) page[store.DecisionRow] {
		t.Helper()
		code, body := env.get("/api/gate/decisions"+query, testKey)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", query, code, body)
		}
		contractstest.ValidateJSON(t, schemaDecisionPage, body)
		var p page[store.DecisionRow]
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	all := read("")
	if len(all.Items) != 3 || all.NextCursor != nil {
		t.Fatalf("%d rows, next %v", len(all.Items), all.NextCursor)
	}
	// Newest first: the reverse of the order they were sent in.
	for i, id := range sent {
		if got := all.Items[len(sent)-1-i].TraceID; got != trace[id] {
			t.Errorf("row %d is %s, want %s (%s)", len(sent)-1-i, got, trace[id], id)
		}
	}
	// The rows have the golden fixture's fields.
	fixture := fixtureKeys(t, "fixtures/api/Page-GateDecisionRow.recent.json")
	for _, it := range all.Items {
		b, _ := json.Marshal(it)
		if got := keysOf(t, b); got != fixture {
			t.Errorf("row keys %s, fixture %s", got, fixture)
		}
	}

	flagged := all.Items[1]
	if flagged.Action != "flag" || flagged.Route != "support-bot" || flagged.Status != "ok" || flagged.Late ||
		flagged.AuditVerdict == nil || *flagged.AuditVerdict != "HALLUCINATED" || len(flagged.EvaluatorLevels) == 0 {
		t.Errorf("flagged row %+v", flagged)
	}
	var critical string
	for ref, level := range flagged.EvaluatorLevels {
		if level != nil && *level == "critical" {
			critical = ref
		}
	}
	if critical == "" {
		t.Fatalf("no critical evaluator in %v", flagged.EvaluatorLevels)
	}
	if na := all.Items[0]; na.Status != "not_assessed" || na.AuditVerdict == nil || *na.AuditVerdict != "NOT_ASSESSED" {
		t.Errorf("not-assessed row %+v", na)
	}

	// Keyset paging.
	first := read("?limit=2")
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("page 1: %d rows, next %v", len(first.Items), first.NextCursor)
	}
	if second := read("?limit=2&cursor=" + *first.NextCursor); len(second.Items) != 1 || second.NextCursor != nil ||
		second.Items[0].TraceID != trace["grounded-pass"] {
		t.Errorf("page 2: %+v", second)
	}

	// Filters.
	for query, want := range map[string]string{
		"?action=flag":         trace["refund-contradiction-flag"],
		"?status=not_assessed": trace["no-evidence-not-assessed"],
		"?evaluator=" + critical + "&level=critical": trace["refund-contradiction-flag"],
		"?level=critical&route=support-bot":          trace["refund-contradiction-flag"],
		"?late=true":                                 "",
		"?route=faq-bot":                             "",
	} {
		p := read(query)
		got := ""
		for _, it := range p.Items {
			got += it.TraceID
		}
		if got != want {
			t.Errorf("%s: %q, want %q", query, got, want)
		}
	}
	if p := read("?disagreement=false&late=false"); len(p.Items) != 3 {
		t.Errorf("agreement: %d rows", len(p.Items))
	}

	for _, bad := range []string{"?limit=0", "?cursor=nope", "?status=pending", "?level=high", "?evaluator=Groundedness",
		"?late=maybe", "?disagreement=2", "?since=yesterday", "?action=allow"} {
		code, body := env.get("/api/gate/decisions"+bad, testKey)
		if code != http.StatusBadRequest || errorCodeOf(t, body) != codeBadRequest {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
}

// fixtureKeys lists the keys of the first item of a Page fixture.
func fixtureKeys(t *testing.T, path string) string {
	t.Helper()
	b, err := contracts.FS.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(b, &p); err != nil || len(p.Items) == 0 {
		t.Fatalf("%s: %v", path, err)
	}
	return keysOf(t, p.Items[0])
}

func keysOf(t *testing.T, b []byte) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out, _ := json.Marshal(keys)
	return string(out)
}

// seedReliability records, on schema url, two human labels and two uniform-sample audits of real
// (non-simulated) traffic for groundedness@1 under snapshot, plus a label under another snapshot.
func seedReliability(t *testing.T, pg *store.PostgresStore, url, snapshot string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, l := range []struct {
		label, snapshot string
		risk            float64
	}{{"should_fire", snapshot, 0.92}, {"should_not_fire", snapshot, 0.07}, {"should_fire", "typesafe/jev-1.12-20260801", 0.5}} {
		if _, err := conn.Exec(ctx, `INSERT INTO labels (trace_id, target, label, labeled_by, label_source, risk_raw, snapshot)
			VALUES ($1, 'groundedness@1', $2, 'reviewer', 'human', $3, $4)`, store.NewID(), l.label, l.risk, l.snapshot); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []struct {
		verdict string
		risk    float64
	}{{"HALLUCINATED", 0.81}, {"GROUNDED", 0.15}} {
		id, auditID, pv := store.NewID(), store.NewID(), int64(1)
		action, selection, backend := "flag", "uniform", "openrouter-decisions"
		tr := &store.TraceRecord{TraceID: id, Source: "proxy", APIFormat: "openai_chat", Path: "/v1/chat/completions",
			Route: "support-bot", PolicyVersion: &pv, Mode: "flag", EffectiveMode: &action, Action: &action, GateStatus: "ok",
			DecisionBackend: &backend, DecisionSnapshot: &snapshot, AuditID: &auditID, AuditStatus: "queued",
			AuditSelection: &selection, Flags: []string{"uniform_sample"}}
		doc, _ := json.Marshal(map[string]any{"spec": "decision/v1", "trace_id": id,
			"evaluators": map[string]any{"groundedness@1": map[string]any{"ref": "groundedness@1", "risk_raw": u.risk, "level": "warn"}}})
		dec := &store.DecisionRecord{TraceID: id, PolicyVersion: &pv, Route: "support-bot", Mode: "flag", EffectiveMode: "flag",
			Status: "ok", Action: action, Calibration: "none", Decision: doc, DecisionSnapshot: &snapshot}
		if err := pg.RecordDecision(ctx, tr, dec, nil); err != nil {
			t.Fatal(err)
		}
		verdict := u.verdict
		a := &store.AuditRecord{AuditID: auditID, TraceID: id, Model: "gpt-4o-mini", Source: "proxy", Selection: &selection,
			Status: store.AuditCompleted, Verdict: &verdict, ScoringVersion: "v2", CreatedAt: time.Now().UTC()}
		if err := pg.SaveAuditResult(ctx, a, store.AuditCompleted); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGateReliability(t *testing.T) {
	if code, body := newTestEnv(t, envOpts{}).get("/api/gate/reliability?evaluator=groundedness@1", testKey); code != http.StatusServiceUnavailable ||
		errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("without a database: %d %s", code, body)
	}

	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	env := newTestEnv(t, envOpts{store: pg})
	live := env.srv.status.jevStatus().Snapshot // the mock decision model's snapshot
	if live == "" {
		t.Fatal("the mock decision model reports no snapshot")
	}
	seedReliability(t, pg, schema.URL, live)

	get := func(query string) reliabilityResponse {
		t.Helper()
		code, body := env.get("/api/gate/reliability"+query, testKey)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", query, code, body)
		}
		// api.ts ReliabilityResponse, field for field (api.v1.schema.json does not define it yet).
		if got := keysOf(t, body); got != `["bins","ece","evaluator","n","n_by_source","snapshot","window"]` {
			t.Errorf("%s: keys %s", query, got)
		}
		var r reliabilityResponse
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("%s: %v in %s", query, err, body)
		}
		return r
	}

	all := get("?evaluator=groundedness@1")
	if all.Evaluator != "groundedness@1" || all.Snapshot == nil || *all.Snapshot != live || all.Window != "7d" || all.N != 4 ||
		all.NBySource["human"] != 2 || all.NBySource["uniform_audit"] != 2 || all.ECE == nil || len(all.Bins) != 4 {
		t.Fatalf("all sources: %+v", all)
	}
	for i, b := range all.Bins {
		if b.N != 1 || b.Hi-b.Lo < 0.099 || (i > 0 && b.Lo < all.Bins[i-1].Hi) {
			t.Errorf("bin %d %+v", i, b)
		}
	}
	// Two confident, right answers per source: every bin agrees with its label, give or take its mean.
	if *all.ECE > 0.2 {
		t.Errorf("ece %v", *all.ECE)
	}
	if h := get("?evaluator=groundedness&labels=human&bins=10&window=30d"); h.N != 2 || h.NBySource["uniform_audit"] != 0 ||
		h.NBySource["human"] != 2 || h.Evaluator != "groundedness" || h.Window != "30d" {
		t.Errorf("human: %+v", h)
	}
	if u := get("?evaluator=groundedness@1&labels=uniform_audit"); u.N != 2 || u.NBySource["human"] != 0 {
		t.Errorf("uniform audits: %+v", u)
	}
	if other := get("?evaluator=groundedness@1&snapshot=typesafe/jev-1.13-20260917"); other.N != 0 || len(other.Bins) != 0 ||
		other.ECE != nil || other.Snapshot == nil {
		t.Errorf("another snapshot: %+v", other)
	}
	if every := get("?evaluator=groundedness@1&snapshot="); every.N != 5 || every.Snapshot != nil {
		t.Errorf("every snapshot: %+v", every)
	}
	if none := get("?evaluator=contradiction@1"); none.N != 0 || none.Bins == nil || none.ECE != nil {
		t.Errorf("an evaluator without labels: %+v", none)
	}

	for _, bad := range []string{"", "?evaluator=", "?evaluator=Groundedness", "?evaluator=groundedness@1&bins=5",
		"?evaluator=groundedness@1&labels=judge", "?evaluator=groundedness@1&window=2d", "?evaluator=groundedness@1&snapshot=a%20b"} {
		code, body := env.get("/api/gate/reliability"+bad, testKey)
		if code != http.StatusBadRequest || errorCodeOf(t, body) != codeBadRequest {
			t.Errorf("%q: %d %s", bad, code, body)
		}
	}
}
