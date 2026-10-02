package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	schemaCalibrationList   = "api.v1.schema.json#/$defs/CalibrationList"
	schemaCalibrationRecord = "api.v1.schema.json#/$defs/CalibrationRecord"
	schemaCalibrationMeta   = "api.v1.schema.json#/$defs/CalibrationRecordMeta"
	schemaCalibrationFit    = "api.v1.schema.json#/$defs/CalibrationFitResponse"
	schemaCalibrationImport = "api.v1.schema.json#/$defs/CalibrationImportResult"

	mockSnapshot = "mock/jev-heuristic-v1"
	liveSnapshot = "typesafe/jev-1.13-20260917"
)

// seedLabels adds n labels of target on snapshot from source, half should_fire (risk 0.5-0.99)
// and half should_not_fire (risk 0-0.49), so a fit separates them.
func seedLabels(t *testing.T, db *pgxpool.Pool, target, snapshot, source string, n int) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `
		INSERT INTO labels (trace_id, target, label, labeled_by, label_source, risk_raw, snapshot)
		SELECT 'seed-' || $1 || '-' || $2 || '-' || g, $1,
			CASE WHEN g % 2 = 0 THEN 'should_fire' ELSE 'should_not_fire' END, 'seed', $3,
			CASE WHEN g % 2 = 0 THEN 0.5 + (g % 50) / 100.0 ELSE (g % 50) / 100.0 END, $2
		FROM generate_series(1, $4) g`, target, snapshot, source, n); err != nil {
		t.Fatal(err)
	}
}

// seedUniformAudits adds n gated traces whose attempt-0 decision gave ref a raw risk and whose
// uniform-sample deep audit has a verdict (a proxy label), on snapshot, as live (non-mock) traffic.
func seedUniformAudits(t *testing.T, db *pgxpool.Pool, ref, snapshot string, n int) {
	t.Helper()
	for i := range n {
		traceID, auditID := "uniform-"+strconv.Itoa(i), "audit-uniform-"+strconv.Itoa(i)
		hallucinated := i%2 == 0
		verdict, risk := "GROUNDED", 0.1+float64(i%5)/20
		if hallucinated {
			verdict, risk = "HALLUCINATED", 0.6+float64(i%5)/20
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO traces (trace_id, source, api_format, path, route, mode, audit_id, decision_backend, decision_snapshot)
				VALUES ($1, 'proxy', 'openai_chat', '/v1/chat/completions', 'support-bot', 'flag', $2, 'openrouter-decisions', $3)`,
				[]any{traceID, auditID, snapshot}},
			{`INSERT INTO gate_decisions (trace_id, route, mode, effective_mode, status, action, decision, decision_snapshot)
				VALUES ($1, 'support-bot', 'flag', 'flag', 'ok', 'pass',
					jsonb_build_object('evaluators', jsonb_build_object($2::text, jsonb_build_object('risk_raw', $3::float8))), $4)`,
				[]any{traceID, ref, risk, snapshot}},
			{`INSERT INTO audits (audit_id, request_id, user_query, llm_response, faithfulness_score, grade, hallucination_detected, selection, verdict)
				VALUES ($1, $1, 'q', 'a', 0.5, 'C', $2, 'uniform', $3)`, []any{auditID, hallucinated, verdict}},
		} {
			if _, err := db.Exec(t.Context(), q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func calibrationList(t *testing.T, env *testEnv, query string) []calibrationMeta {
	t.Helper()
	code, body := env.get("/api/calibration"+query, testKey)
	if code != http.StatusOK {
		t.Fatalf("list%s: %d %s", query, code, body)
	}
	contractstest.ValidateJSON(t, schemaCalibrationList, body)
	var l struct {
		Items []calibrationMeta `json:"items"`
	}
	_ = json.Unmarshal(body, &l)
	return l.Items
}

func fitCalibration(t *testing.T, env *testEnv, req map[string]any) fitResponse {
	t.Helper()
	resp, body := env.call(http.MethodPost, "/api/calibration/fit", evalAdminKey, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fit %v: %d %s", req, resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaCalibrationFit, body)
	var f fitResponse
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func activeFor(items []calibrationMeta, snapshot, prefix string) []calibrationMeta {
	var out []calibrationMeta
	for _, it := range items {
		if it.Active && it.Snapshot == snapshot && strings.HasPrefix(it.TargetKey, prefix) {
			out = append(out, it)
		}
	}
	return out
}

func TestCalibrationAPI(t *testing.T) {
	env, db := evalEnv(t, nil)

	// #42: the committed mock calibration file is mirrored into the database, active.
	items := calibrationList(t, env, "")
	files := activeFor(items, mockSnapshot, "groundedness@1#")
	if len(files) != 1 || files[0].Origin != "file" || files[0].Quality != "mock" || len(activeFor(items, mockSnapshot, "contradiction@1#")) != 1 {
		t.Fatalf("mirrored file rows %+v", items)
	}
	fileRow := files[0]
	code, body := env.get("/api/calibration/"+strconv.FormatInt(fileRow.ID, 10), testKey)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaCalibrationRecord, body)

	// #45 on the snapshot in effect (the mock model's): stored inactive for Preview impact.
	seedLabels(t, db, "groundedness@1", mockSnapshot, "human", 12)
	fit := fitCalibration(t, env, map[string]any{"target": "groundedness@1", "sources": []string{"labels"}, "method": "auto", "activate": false})
	r := fit.Record
	if r.Origin != "fit" || r.Active || r.Snapshot != mockSnapshot || r.Quality != "mock" || r.Backend != "mock" ||
		!strings.HasPrefix(r.TargetKey, "groundedness@1#") || fit.Reliability.N != 12 || fit.Reliability.NBySource["human"] != 12 ||
		fit.Reliability.Window != "30d" || fit.Metrics.AUROC == nil {
		t.Fatalf("fit %+v reliability %+v", r.calibrationMeta, fit.Reliability)
	}
	var entry decision.CalibrationFileEntry
	if err := json.Unmarshal(r.Entry, &entry); err != nil || entry.Target.Key() != r.TargetKey || entry.N != 12 || len(entry.Table) != 101 {
		t.Fatalf("fit entry %+v %v", entry, err)
	}
	if len(activeFor(calibrationList(t, env, ""), mockSnapshot, "groundedness@1#")) != 1 {
		t.Fatal("an inactive fit changed the active rows")
	}

	// #26: Preview impact with the fitted record in place of the active one.
	dry := func(ids any) (*http.Response, []byte) {
		return env.call(http.MethodPost, "/api/policies/dry-run", testKey, map[string]any{"version": 1, "window": "24h", "calibration_ids": ids})
	}
	if resp, body := dry([]int64{r.ID}); resp.StatusCode != http.StatusOK {
		t.Errorf("dry-run with calibration_ids: %d %s", resp.StatusCode, body)
	}
	if resp, body := dry([]int64{r.ID, 999999}); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "999999") {
		t.Errorf("dry-run with an unknown calibration id: %d %s", resp.StatusCode, body)
	}
	if resp, _ := dry([]int64{0}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("dry-run with id 0: %d", resp.StatusCode)
	}

	// #46b swaps the fit in; #46 brings the file row back.
	post := func(path string) (*http.Response, []byte) { return env.call(http.MethodPost, path, evalAdminKey, nil) }
	resp, body := post("/api/calibration/" + strconv.FormatInt(r.ID, 10) + "/activate")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"active":true`) {
		t.Fatalf("activate: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaCalibrationMeta, body)
	if act := activeFor(calibrationList(t, env, "?active=true"), mockSnapshot, "groundedness@1#"); len(act) != 1 || act[0].ID != r.ID {
		t.Fatalf("active after activation %+v", act)
	}
	if st := env.srv.calib.Lookup("groundedness@1", nil, mockSnapshot); st.Table == nil || st.Table[100] != entry.Table[100] {
		t.Errorf("the lookup did not reload before the reply: %+v", st)
	}
	resp, body = post("/api/calibration/" + strconv.FormatInt(r.ID, 10) + "/deactivate")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"active":false`) {
		t.Fatalf("deactivate: %d %s", resp.StatusCode, body)
	}
	if act := activeFor(calibrationList(t, env, "?active=true"), mockSnapshot, "groundedness@1#"); len(act) != 1 || act[0].ID != fileRow.ID {
		t.Fatalf("the file row did not come back: %+v", act)
	}
	if resp, _ := post("/api/calibration/999999/activate"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("activate unknown: %d", resp.StatusCode)
	}
	if resp, _ := post("/api/calibration/abc/activate"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("activate bad id: %d", resp.StatusCode)
	}

	// Quality follows the labels on a live snapshot: provisional, measured, proxy.
	seedLabels(t, db, "contradiction@1", liveSnapshot, "human", 20)
	seedLabels(t, db, "groundedness@1", liveSnapshot, "public_dataset", 240)
	seedLabels(t, db, "refusal@1", liveSnapshot, "human", 10)
	seedUniformAudits(t, db, "refusal@1", liveSnapshot, 6)
	for _, tc := range []struct {
		target  string
		sources []string
		quality string
		n       int
	}{
		{"contradiction@1", []string{"labels"}, "provisional", 20},
		{"groundedness@1", []string{"labels"}, "measured", 240},
		{"refusal@1", []string{"labels"}, "provisional", 10},
		{"refusal@1", []string{"uniform_audit", "labels"}, "proxy", 16},
	} {
		f := fitCalibration(t, env, map[string]any{"target": tc.target, "snapshot": liveSnapshot, "sources": tc.sources, "activate": true})
		if f.Record.Quality != tc.quality || f.Record.Backend == "" || !f.Record.Active || f.Reliability.N != tc.n {
			t.Errorf("fit %s %v: quality %s active %v n %d, want %s n %d", tc.target, tc.sources, f.Record.Quality, f.Record.Active,
				f.Reliability.N, tc.quality, tc.n)
		}
	}
	if st := env.srv.calib.Lookup("groundedness@1", nil, liveSnapshot); st.State != "measured" {
		t.Errorf("the measured fit does not apply: %+v", st)
	}

	// 422 below 8 labels or 3 of each class, and for a bad request.
	seedLabels(t, db, "pii_secret_leak@1", liveSnapshot, "human", 5)
	for _, req := range []map[string]any{
		{"target": "pii_secret_leak@1", "snapshot": liveSnapshot, "sources": []string{"labels"}, "activate": false},
		{"target": "nope@1", "sources": []string{"labels"}, "activate": false},
		{"target": "groundedness@1", "sources": []string{"uniform_audit"}, "activate": false},
		{"target": "groundedness@1", "sources": []string{"labels"}, "method": "spline", "activate": false},
		{"target": "groundedness@1", "route": "no-such-route", "sources": []string{"labels"}, "activate": false},
		{"target": "groundedness", "sources": []string{"labels"}, "activate": false},
	} {
		resp, body := env.call(http.MethodPost, "/api/calibration/fit", evalAdminKey, req)
		if resp.StatusCode != http.StatusUnprocessableEntity || decodeErr(t, body).Error.Code != codeValidationFailed {
			t.Errorf("fit %v: %d %s", req, resp.StatusCode, body)
		}
	}
	if resp, _ := env.call(http.MethodPost, "/api/calibration/fit", testKey, map[string]any{"target": "groundedness@1"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a content key fits: %d", resp.StatusCode)
	}

	// Each write left its audit row, and broadcast calibration_updated.
	var actions []string
	rows, err := db.Query(t.Context(), `SELECT action FROM admin_audit_log WHERE action LIKE 'calibration.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	want := "calibration.fit,calibration.activate,calibration.deactivate,calibration.fit,calibration.fit,calibration.fit,calibration.fit"
	if strings.Join(actions, ",") != want {
		t.Errorf("audit rows %v", actions)
	}
	updates := 0
	for _, f := range env.wsFrames(t, 300) {
		if strings.Contains(string(f), `"type":"calibration_updated"`) {
			contractstest.ValidateJSON(t, schemaWsEvent, f)
			updates++
		}
	}
	if updates < 7 {
		t.Errorf("%d calibration_updated events, want one per write (7)", updates)
	}
}

// calibrationFile is a calibration.v1 document for snapshot whose entries are those of the
// committed mock file, retargeted.
func calibrationFile(t *testing.T, env *testEnv, snapshot string, entries ...decision.CalibrationFileEntry) map[string]any {
	t.Helper()
	f := decision.NewCalibrationFile("openrouter-decisions", snapshot, "cmd/calibrate@test", time.Now().UTC())
	if strings.HasPrefix(snapshot, "mock/") {
		f.DecisionModel.Backend = "mock"
	}
	f.Entries = append(f.Entries, entries...)
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// importEntry is a valid entry for ref with labels from src.
func importEntry(t *testing.T, env *testEnv, ref string, src decision.LabelSourceCounts, pos, neg int) decision.CalibrationFileEntry {
	t.Helper()
	spec, ok := env.srv.policies.Registry.Get(ref)
	if !ok {
		t.Fatalf("no %s", ref)
	}
	target, err := decision.EvaluatorTarget(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := decision.SoloQuestionSetSHA256(spec)
	table := make([]float64, 101)
	for i := range table {
		table[i] = float64(i) / 100
	}
	return decision.CalibrationFileEntry{
		Target: target, FitContext: decision.FitContext{StateFields: spec.StateFields, Partition: "single", QuestionSetSHA256: qs},
		Method: "isotonic", Table: table, Quality: "measured", N: pos + neg, NPos: pos, NNeg: neg, LabelSource: src,
		Datasets: []string{}, Reliability: []decision.ReliabilityBin{}, Recommended: decision.Recommended{Space: "calibrated"},
	}
}

func TestCalibrationImportAPI(t *testing.T) {
	env, db := evalEnv(t, nil)
	good := importEntry(t, env, "groundedness@1", decision.LabelSourceCounts{Human: 20}, 10, 10) // claims measured, has 20 labels
	unknown := importEntry(t, env, "contradiction@1", decision.LabelSourceCounts{Human: 20}, 10, 10)
	unknown.Target.EvaluatorVersion = 9
	mismatch := importEntry(t, env, "contradiction@1", decision.LabelSourceCounts{PublicDataset: 300}, 100, 200)
	mismatch.Target.CalibrationKey = good.Target.CalibrationKey
	broken := importEntry(t, env, "refusal@1", decision.LabelSourceCounts{Human: 20}, 10, 10)
	broken.Table = broken.Table[:50]
	file := calibrationFile(t, env, liveSnapshot, good, unknown, mismatch, broken)

	resp, body := env.call(http.MethodPost, "/api/calibration", evalAdminKey, map[string]any{"file": file, "activate": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: %d %s", resp.StatusCode, body)
	}
	contractstest.ValidateJSON(t, schemaCalibrationImport, body)
	var res struct {
		Imported []calibrationMeta `json:"imported"`
		Rejected []importRejection `json:"rejected"`
	}
	_ = json.Unmarshal(body, &res)
	if len(res.Imported) != 1 || res.Imported[0].Quality != "provisional" || res.Imported[0].Origin != "import" || !res.Imported[0].Active {
		t.Errorf("imported %+v (quality re-derived from 20 human labels: provisional)", res.Imported)
	}
	reasons := []string{}
	for _, r := range res.Rejected {
		reasons = append(reasons, r.Reason)
		if r.TargetKey == nil || r.Message == "" {
			t.Errorf("rejection %+v", r)
		}
	}
	if strings.Join(reasons, ",") != "unknown_version,calibration_key_mismatch,invalid" {
		t.Errorf("rejections %v", reasons)
	}
	if st := env.srv.calib.Lookup("groundedness@1", nil, liveSnapshot); st.State != "provisional" {
		t.Errorf("the import does not apply: %+v", st)
	}
	var stored string
	if err := db.QueryRow(t.Context(), `SELECT entry->>'quality' FROM calibrations WHERE id = $1`, res.Imported[0].ID).Scan(&stored); err != nil || stored != "provisional" {
		t.Errorf("stored entry quality %q %v", stored, err)
	}

	// Mock snapshots are refused where the mock model does not run.
	env.srv.eval.calib.allowMock = false
	resp, body = env.call(http.MethodPost, "/api/calibration", evalAdminKey,
		map[string]any{"file": calibrationFile(t, env, mockSnapshot, good), "activate": false})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"reason":"mock_snapshot"`) || !strings.Contains(string(body), `"imported":[]`) {
		t.Errorf("mock import: %d %s", resp.StatusCode, body)
	}
	env.srv.eval.calib.allowMock = true

	// A document that is not a calibration file is refused whole.
	bad := calibrationFile(t, env, liveSnapshot, good)
	bad["schema"] = "trustagent.calibration/v0"
	for _, b := range []map[string]any{{"file": bad, "activate": false}, {"file": map[string]any{"x": 1}, "activate": false}} {
		if resp, body := env.call(http.MethodPost, "/api/calibration", evalAdminKey, b); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("invalid file: %d %s", resp.StatusCode, body)
		}
	}
	if resp, _ := env.call(http.MethodPost, "/api/calibration", evalAdminKey, map[string]any{"activate": true}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no file: %d", resp.StatusCode)
	}
	if resp, _ := env.call(http.MethodPost, "/api/calibration", testKey, map[string]any{"file": file, "activate": true}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a content key imports: %d", resp.StatusCode)
	}
	var n int
	_ = db.QueryRow(t.Context(), `SELECT count(*) FROM admin_audit_log WHERE action = 'calibration.import'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d import audit rows, want 1 (refused imports write none)", n)
	}
	found := false
	for _, f := range env.wsFrames(t, 300) {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Snapshot string `json:"snapshot"`
				Imported int    `json:"imported"`
			} `json:"data"`
		}
		if json.Unmarshal(f, &ev) == nil && ev.Type == "calibration_updated" && ev.Data.Snapshot == liveSnapshot && ev.Data.Imported == 1 {
			found = true
		}
	}
	if !found {
		t.Error("the import did not broadcast calibration_updated {snapshot, imported: 1}")
	}
}

// TestCalibrationNotificationReachesTheLookup: a calibration written by another replica reaches
// this one's lookup through the config notification and the hub (D84), long before the poll.
func TestCalibrationNotificationReachesTheLookup(t *testing.T) {
	every := calibrationReload
	calibrationReload = time.Hour
	t.Cleanup(func() { calibrationReload = every })
	env, _ := evalEnv(t, nil)
	entry := importEntry(t, env, "contradiction@1", decision.LabelSourceCounts{PublicDataset: 300}, 100, 200)
	doc, _ := json.Marshal(entry)
	rec := &store.CalibrationRecord{CreatedBy: "replica-2", Origin: store.CalibrationOriginImport, Backend: "openrouter-decisions",
		Snapshot: liveSnapshot, Family: decision.SnapshotFamily(liveSnapshot), TargetKind: "evaluator", TargetKey: entry.Target.Key(),
		Quality: "measured", LabelSource: json.RawMessage(`{"human":0,"public_dataset":300,"uniform_audit":0}`), Entry: doc}
	// The server's lookup does not see this write: only the notification tells it.
	if err := env.srv.pg.InsertCalibrations(t.Context(), "", []*store.CalibrationRecord{rec}, true,
		store.AdminAuditEntry{Actor: "replica-2", Action: "calibration.import"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for env.srv.calib.Lookup("contradiction@1", nil, liveSnapshot).State != "measured" {
		if time.Now().After(deadline) {
			t.Fatal("the notification did not reload the lookup")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCalibrationAPINeedsADatabase(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: evalAdminKey})
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/calibration"}, {http.MethodGet, "/api/calibration/1"},
		{http.MethodPost, "/api/calibration"}, {http.MethodPost, "/api/calibration/fit"},
		{http.MethodPost, "/api/calibration/1/activate"}, {http.MethodPost, "/api/calibration/1/deactivate"},
	} {
		var body any
		switch req.path {
		case "/api/calibration":
			if req.method == http.MethodPost {
				body = map[string]any{"file": map[string]any{}, "activate": false}
			}
		case "/api/calibration/fit":
			body = map[string]any{"target": "groundedness@1", "sources": []string{"labels"}, "activate": false}
		}
		resp, b := env.call(req.method, req.path, evalAdminKey, body)
		if resp.StatusCode != http.StatusServiceUnavailable || decodeErr(t, b).Error.Code != codePersistenceDisabled {
			t.Errorf("%s %s without a database: %d %s", req.method, req.path, resp.StatusCode, b)
		}
	}
	// The lookup still reads the files (D14).
	if st := env.srv.calib.Lookup("groundedness@1", nil, mockSnapshot); st.State != "mock" {
		t.Errorf("file lookup without a database: %+v", st)
	}
	ro, _ := evalEnv(t, func(o *envOpts) { o.readonly = true })
	if resp, body := ro.call(http.MethodPost, "/api/calibration/1/activate", evalAdminKey, nil); resp.StatusCode != http.StatusForbidden || decodeErr(t, body).Error.Code != "readonly" {
		t.Errorf("read-only: %d %s", resp.StatusCode, body)
	}
	if code, _ := ro.get("/api/calibration", testKey); code != http.StatusOK {
		t.Errorf("read-only list: %d", code)
	}
}
