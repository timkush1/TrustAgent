package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"

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
