package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	schemaLabelQueue  = "api.v1.schema.json#/$defs/LabelQueue"
	schemaReliability = "api.v1.schema.json#/$defs/ReliabilityResponse"
)

// labelQueue reads GET /api/labels/queue as the admin.
func labelQueue(t *testing.T, env *testEnv, query string) []labelQueueItem {
	t.Helper()
	resp, body := env.call(http.MethodGet, "/api/labels/queue"+query, evalAdminKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue%s: %d %s", query, resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaLabelQueue, body)
	var q struct {
		Items []labelQueueItem `json:"items"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatal(err)
	}
	return q.Items
}

func hasItem(items []labelQueueItem, traceID, ref string) bool {
	for _, it := range items {
		if it.TraceID == traceID && it.EvaluatorRef == ref {
			return true
		}
	}
	return false
}

func TestLabelsAPI(t *testing.T) {
	env, db := evalEnv(t, nil)
	scenarios := loadDemoScenarios(t)
	flagged := env.send(scenarios["refund-contradiction-flag"]).Header.Get("X-TrustAgent-Trace-Id")
	grounded := env.send(scenarios["grounded-pass"]).Header.Get("X-TrustAgent-Trace-Id")
	env.settledTrace(flagged)
	env.settledTrace(grounded)
	put := func(body map[string]any) (*http.Response, []byte) {
		return env.call(http.MethodPut, "/api/labels", evalAdminKey, body)
	}

	// #39: both traces queue for both evaluators (support-bot attaches groundedness@1 and
	// contradiction@1), nearest the flag line first.
	items := labelQueue(t, env, "")
	if len(items) != 4 || !hasItem(items, flagged, "groundedness@1") || !hasItem(items, grounded, "contradiction@1") {
		t.Fatalf("queue %+v", items)
	}
	reg := env.srv.policies.Registry
	dist := func(it labelQueueItem) float64 {
		spec, _ := reg.Get(it.EvaluatorRef)
		return math.Abs(*it.RiskRaw - spec.Outcomes.WarnAt)
	}
	for i := 1; i < len(items); i++ {
		if dist(items[i]) < dist(items[i-1]) {
			t.Errorf("item %d is nearer the flag line than item %d: %+v", i, i-1, items)
		}
	}
	for _, it := range items {
		if it.Route != "support-bot" || it.UserRequest == nil || it.ResponseText == nil || len(it.EvidenceLabels) == 0 || it.Level == nil {
			t.Errorf("item %+v", it)
		}
	}
	if items := labelQueue(t, env, "?evaluator=groundedness&limit=1"); len(items) != 1 || items[0].EvaluatorRef != "groundedness@1" {
		t.Errorf("one evaluator by id, limit 1: %+v", items)
	}
	for _, q := range []string{"?limit=51", "?limit=0", "?window=2d", "?evaluator=Bad!", "?evaluator=no_such_check"} {
		if resp, body := env.call(http.MethodGet, "/api/labels/queue"+q, evalAdminKey, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("queue%s: %d %s", q, resp.StatusCode, body)
		}
	}
	if resp, _ := env.call(http.MethodGet, "/api/labels/queue", testKey, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a content key reads the admin queue: %d", resp.StatusCode)
	}

	// #17 before any label: no human samples.
	reliability := func() reliabilityResponse {
		t.Helper()
		code, body := env.get("/api/gate/reliability?evaluator=groundedness@1&labels=human&snapshot=", testKey)
		if code != http.StatusOK {
			t.Fatalf("reliability: %d %s", code, body)
		}
		contractstest.ValidateJSON(t, schemaReliability, body)
		var r reliabilityResponse
		_ = json.Unmarshal(body, &r)
		return r
	}
	if r := reliability(); r.N != 0 {
		t.Fatalf("human samples before labelling: %+v", r)
	}

	// #40: label the flagged answer's groundedness result.
	label := map[string]any{"trace_id": flagged, "target": "groundedness@1", "label": "should_fire", "note": " says 60 days; the policy says 30 "}
	if resp, body := put(label); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	if resp, body := put(label); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put again: %d %s", resp.StatusCode, body)
	}
	var risk float64
	var snapshot string
	var sample store.LabelSample
	if err := db.QueryRow(t.Context(), `SELECT risk_raw, snapshot, sample FROM labels WHERE trace_id = $1 AND target = 'groundedness@1'`, flagged).
		Scan(&risk, &snapshot, &sample); err != nil {
		t.Fatal(err)
	}
	if snapshot == "" || sample.ResponseText == nil || sample.UserRequest == nil || len(sample.Evidence) < 3 {
		t.Errorf("frozen: risk %v snapshot %q sample %+v", risk, snapshot, sample)
	}
	code, body := env.get("/api/traces/"+flagged, testKey)
	var detail store.TraceDetail
	_ = json.Unmarshal(body, &detail)
	if code != http.StatusOK || len(detail.Labels) != 1 || detail.Labels[0].LabeledBy != "admin" || detail.Labels[0].Label != "should_fire" ||
		detail.Labels[0].Note == nil || *detail.Labels[0].Note != "says 60 days; the policy says 30" {
		t.Errorf("TraceDetail.labels: %d %+v", code, detail.Labels)
	}
	contractstest.ValidateJSON(t, schemaTrace, body)
	// mine: the admin who labelled sees the label as theirs; another caller does not (#41 removes
	// only the caller's own).
	if len(detail.Labels) == 1 && detail.Labels[0].Mine {
		t.Error("a label another key gave reads as the caller's")
	}
	_, adminBody := env.get("/api/traces/"+flagged, evalAdminKey)
	var adminView store.TraceDetail
	_ = json.Unmarshal(adminBody, &adminView)
	if len(adminView.Labels) != 1 || !adminView.Labels[0].Mine {
		t.Errorf("the labeller's own label: %+v", adminView.Labels)
	}
	// The labelled result leaves the caller's queue; #17 human now has the sample, at the label's risk.
	if hasItem(labelQueue(t, env, ""), flagged, "groundedness@1") {
		t.Error("a labelled result is still queued")
	}
	if r := reliability(); r.N != 1 || r.NBySource["human"] != 1 {
		t.Errorf("human samples after labelling: %+v", r)
	}
	if code, body := env.get("/api/evaluators/groundedness", testKey); code != http.StatusOK || !strings.Contains(string(body), `"human_labels":{"n":1,"pos":1,"neg":0}`) {
		t.Errorf("calibration progress after labelling: %s", body)
	}

	// Pair validation (D59) and the trace.
	for _, tc := range []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"claim verdict on an evaluator", map[string]any{"trace_id": flagged, "target": "groundedness@1", "label": "CONTRADICTED"}, http.StatusUnprocessableEntity},
		{"evaluator not asked", map[string]any{"trace_id": flagged, "target": "refusal@1", "label": "should_fire"}, http.StatusUnprocessableEntity},
		{"unknown target", map[string]any{"trace_id": flagged, "target": "everything", "label": "should_fire"}, http.StatusUnprocessableEntity},
		{"missing claim", map[string]any{"trace_id": flagged, "target": "claim:99", "label": "SUPPORTED"}, http.StatusUnprocessableEntity},
		{"machine verdict on a claim", map[string]any{"trace_id": flagged, "target": "claim:0", "label": "UNKNOWN"}, http.StatusUnprocessableEntity},
		{"response value", map[string]any{"trace_id": flagged, "target": "response", "label": "should_fire"}, http.StatusUnprocessableEntity},
		{"unknown trace", map[string]any{"trace_id": "no-such-trace", "target": "response", "label": "grounded"}, http.StatusNotFound},
		{"no trace id", map[string]any{"target": "response", "label": "grounded"}, http.StatusBadRequest},
		{"response", map[string]any{"trace_id": flagged, "target": "response", "label": "hallucinated"}, http.StatusNoContent},
		{"claim", map[string]any{"trace_id": flagged, "target": "claim:0", "label": "CONTRADICTED"}, http.StatusNoContent},
	} {
		if resp, body := put(tc.body); resp.StatusCode != tc.status {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		} else if tc.status >= 400 {
			decodeErr(t, body)
		}
	}
	if resp, _ := env.call(http.MethodPut, "/api/labels", testKey, label); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a content key labels: %d", resp.StatusCode)
	}

	// 409 body_expired once the body is gone.
	if _, err := db.Exec(t.Context(), `UPDATE trace_bodies SET expires_at = now() - interval '1 second' WHERE trace_id = $1`, grounded); err != nil {
		t.Fatal(err)
	}
	resp, body := put(map[string]any{"trace_id": grounded, "target": "groundedness@1", "label": "should_not_fire"})
	if resp.StatusCode != http.StatusConflict || decodeErr(t, body).Error.Code != codeBodyExpired {
		t.Errorf("expired body: %d %s", resp.StatusCode, body)
	}
	if hasItem(labelQueue(t, env, ""), grounded, "groundedness@1") {
		t.Error("a trace whose body expired is still queued")
	}

	// #41: Undo removes the caller's label only.
	if resp, body := env.call(http.MethodDelete, "/api/labels", evalAdminKey, map[string]any{"trace_id": flagged, "target": "groundedness@1"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodDelete, "/api/labels", evalAdminKey, map[string]any{"trace_id": flagged, "target": "groundedness@1"}); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete again: %d %s", resp.StatusCode, body)
	}
	if resp, body := env.call(http.MethodDelete, "/api/labels", evalAdminKey, map[string]any{"trace_id": flagged}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("delete without a target: %d %s", resp.StatusCode, body)
	}
	_, body = env.get("/api/traces/"+flagged, testKey)
	detail = store.TraceDetail{}
	_ = json.Unmarshal(body, &detail)
	if len(detail.Labels) != 2 || detail.Labels[0].Target != "claim:0" || detail.Labels[1].Target != "response" {
		t.Errorf("labels after undo: %+v", detail.Labels)
	}
	if !hasItem(labelQueue(t, env, ""), flagged, "groundedness@1") {
		t.Error("an undone label does not queue the result again")
	}
}

func TestLabelsAPINeedsADatabaseAndWritableMode(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: evalAdminKey})
	for _, req := range []struct{ method, path string }{{http.MethodGet, "/api/labels/queue"}, {http.MethodPut, "/api/labels"}, {http.MethodDelete, "/api/labels"}} {
		resp, body := env.call(req.method, req.path, evalAdminKey, map[string]any{"trace_id": "t", "target": "response", "label": "grounded"})
		if req.method == http.MethodGet {
			resp, body = env.call(req.method, req.path, evalAdminKey, nil)
		}
		if resp.StatusCode != http.StatusServiceUnavailable || decodeErr(t, body).Error.Code != codePersistenceDisabled {
			t.Errorf("%s %s without a database: %d %s", req.method, req.path, resp.StatusCode, body)
		}
	}
	if code, body := env.get("/api/traces/no-such", testKey); code != http.StatusNotFound {
		t.Errorf("trace: %d %s", code, body)
	}
	ro, _ := evalEnv(t, func(o *envOpts) { o.readonly = true })
	resp, body := ro.call(http.MethodPut, "/api/labels", evalAdminKey, map[string]any{"trace_id": "t", "target": "response", "label": "grounded"})
	if resp.StatusCode != http.StatusForbidden || decodeErr(t, body).Error.Code != "readonly" {
		t.Errorf("read-only: %d %s", resp.StatusCode, body)
	}
	if resp, body := ro.call(http.MethodGet, "/api/labels/queue", evalAdminKey, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("the queue is a read: %d %s", resp.StatusCode, body)
	}
}
