package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/middleware"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/policy/policytest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

const (
	readKey  = "policy-read-key"
	adminKey = "policy-admin-key"

	schemaPolicyDoc = "policy.v1.schema.json"
	schemaSpec      = "evaluator.v1.schema.json"
	schemaPresets   = "presets.v1.schema.json"

	apiPolicyYAML = `version: 1
defaults: {mode: observe}
routes:
  - name: support-bot
    match: {headers: {X-TrustAgent-Route: support-bot}}
    mode: flag
    evaluators: [{ref: groundedness@1}, {ref: contradiction@1}]
`
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// policyRouter is a router with the policy and evaluator APIs behind real key auth: a read key
// and an admin key.
type policyRouter struct {
	r    *gin.Engine
	file string
}

func testGuards() apiGuards {
	auth := &apiAuth{keys: authz.New([]authz.Key{
		{Label: "reader", Secret: readKey, Scopes: []authz.Scope{authz.ScopeRead}},
		{Label: "admin", Secret: adminKey, Scopes: []authz.Scope{authz.ScopeAdmin}},
	}, authz.IngestKeys)}
	pass := func(c *gin.Context) { c.Next() }
	return apiGuards{
		read: auth.require(authz.ScopeRead), admin: auth.require(authz.ScopeAdmin), writable: pass, rateLimit: pass,
		dryRunLimit: countLimit(6), bodyLimit: middleware.BodyLimit(1 << 20),
	}
}

// countLimit admits n requests, then answers 429 rate_limited. It stands in for the server's
// middleware.RateLimit (6/min on the dry-run), whose signature the P2 middleware changes, so the
// tests pin only that the dry-run route is guarded by its own limit.
func countLimit(n int) gin.HandlerFunc {
	var mu sync.Mutex
	seen := 0
	return func(c *gin.Context) {
		mu.Lock()
		seen++
		over := seen > n
		mu.Unlock()
		if over {
			abortAPI(c, http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded.")
			return
		}
		c.Next()
	}
}

func writePolicy(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newPolicyRouter builds the APIs on an engine over st (nil = file-managed) and replay.
func newPolicyRouter(t *testing.T, st policy.VersionStore, replay policy.ReplaySource, mut ...func(*policy.EngineConfig)) *policyRouter {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "policies.yaml")
	writePolicy(t, file, apiPolicyYAML)
	cfg := policy.EngineConfig{File: file, Env: policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg}, Store: st, Logger: quietLog}
	for _, m := range mut {
		m(&cfg)
	}
	engine, err := policy.NewEngine(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	pols, err := newPoliciesAPI(engine, replay, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	api := r.Group("/api")
	g := testGuards()
	pols.register(api, g)
	newEvaluatorsAPI(reg, engine.Current, nil, nil, quietLog).register(api, g)
	return &policyRouter{r: r, file: file}
}

func (p *policyRouter) do(t *testing.T, method, path, key, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if key != "" {
		req.Header.Set(authz.HeaderKey, key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	p.r.ServeHTTP(w, req)
	return w
}

// wantError checks an error response: status, code, and the ApiErrorBody schema.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
	contractstest.ValidateJSON(t, schemaError, w.Body.Bytes())
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error["code"] != code {
		t.Fatalf("error %s, want code %s", w.Body.String(), code)
	}
	return body.Error
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return v
}

// policyState is the part of api.ts PolicyState the tests read.
type policyState struct {
	ID              int64           `json:"id"`
	CreatedAt       time.Time       `json:"created_at"`
	Source          string          `json:"source"`
	Author          string          `json:"author"`
	Message         string          `json:"message"`
	SHA256          string          `json:"sha256"`
	YAML            string          `json:"yaml"`
	Doc             json.RawMessage `json:"doc"`
	ETag            string          `json:"etag"`
	Locked          bool            `json:"locked"`
	Persisted       bool            `json:"persisted"`
	Editable        bool            `json:"editable"`
	EditableReason  *string         `json:"editable_reason"`
	EffectiveRoutes []struct {
		Name           string            `json:"name"`
		Mode           string            `json:"mode"`
		EffectiveMode  string            `json:"effective_mode"`
		Fail           string            `json:"fail"`
		Downgrades     []json.RawMessage `json:"downgrades"`
		BlockReadiness []struct {
			Evaluator string   `json:"evaluator"`
			Ready     bool     `json:"ready"`
			Missing   []string `json:"missing"`
		} `json:"block_readiness"`
		Evaluators []map[string]any `json:"evaluators"`
		Settings   json.RawMessage  `json:"settings"`
		Match      map[string]any   `json:"match"`
	} `json:"effective_routes"`
	File struct {
		Path   string            `json:"path"`
		SHA256 *string           `json:"sha256"`
		Status string            `json:"status"`
		Errors []json.RawMessage `json:"errors"`
	} `json:"file"`
	Warnings []string `json:"warnings"`
}

// checkState validates a PolicyState response against the contract pieces the schemas cover.
func checkState(t *testing.T, w *httptest.ResponseRecorder) policyState {
	t.Helper()
	st := decode[policyState](t, w)
	if w.Header().Get("ETag") != st.ETag || st.ETag == "" {
		t.Errorf("ETag header %q body %q", w.Header().Get("ETag"), st.ETag)
	}
	contractstest.ValidateJSON(t, schemaPolicyDoc, st.Doc)
	for _, r := range st.EffectiveRoutes {
		contractstest.ValidateJSON(t, schemaPolicyDoc+"#/$defs/RouteSettings", r.Settings)
		if r.Downgrades == nil || r.BlockReadiness == nil || r.Evaluators == nil || r.Match == nil {
			t.Errorf("route %s has null arrays", r.Name)
		}
	}
	if st.Warnings == nil || st.File.Errors == nil || st.CreatedAt.IsZero() || st.SHA256 == "" || st.YAML == "" {
		t.Errorf("state %+v", st)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"id", "created_at", "source", "author", "message", "sha256", "yaml", "doc", "etag", "locked", "persisted",
		"editable", "editable_reason", "effective_routes", "file", "warnings"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("PolicyState lacks %s", k)
		}
	}
	return st
}

func TestPoliciesAPI(t *testing.T) {
	mem := policytest.New()
	p := newPolicyRouter(t, mem, nil)

	// Auth: reads need a key, writes need admin.
	wantError(t, p.do(t, "GET", "/api/policies", "", ""), http.StatusUnauthorized, codeUnauthorized)
	st := checkState(t, p.do(t, "GET", "/api/policies", readKey, ""))
	if st.ID != 1 || st.ETag != `"v1"` || st.Source != "file" || !st.Persisted || st.Editable || *st.EditableReason != policy.ReasonAdminRequired ||
		st.File.Status != "ok" || len(st.EffectiveRoutes) != 2 || st.EffectiveRoutes[0].Name != "support-bot" || st.EffectiveRoutes[1].Name != "default" {
		t.Fatalf("state %+v", st)
	}
	if st := checkState(t, p.do(t, "GET", "/api/policies", adminKey, "")); !st.Editable || st.EditableReason != nil {
		t.Errorf("admin state editable %v", st.Editable)
	}
	save := `{"yaml":"version: 1\ndefaults: {mode: flag}\n","message":"flag everything"}`
	wantError(t, p.do(t, "PUT", "/api/policies", readKey, save, "If-Match", `"v1"`), http.StatusForbidden, codeForbidden)
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, save), http.StatusPreconditionRequired, codePreconditionRequired)
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, save, "If-Match", `"v7"`), http.StatusPreconditionFailed, codeStaleVersion)
	e := wantError(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yaml":"version: 1\ndefaults:\n  mode: strict\n"}`, "If-Match", `"v1"`),
		http.StatusUnprocessableEntity, codeValidationFailed)
	fe := e["field_errors"].([]any)[0].(map[string]any)
	if fe["path"] != "defaults.mode" || fe["line"] != float64(3) || fe["col"] != float64(9) {
		t.Errorf("field error %v", fe)
	}
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yaml":"x","doc":{}}`, "If-Match", `"v1"`), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yml":"x"}`, "If-Match", `"v1"`), http.StatusBadRequest, codeInvalidJSON)
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yaml":`, "If-Match", `"v1"`), http.StatusBadRequest, codeInvalidJSON)
	w := p.do(t, "PUT", "/api/policies", adminKey, save, "If-Match", `"v1"`)
	st = checkState(t, w)
	if st.ID != 2 || st.ETag != `"v2"` || st.Source != "dashboard" || st.Author != "admin" || st.Message != "flag everything" || len(st.EffectiveRoutes) != 1 {
		t.Fatalf("saved %+v", st)
	}
	// The doc form.
	doc := `{"doc":{"version":1,"defaults":{"mode":"observe","deadline_ms":800}},"message":"doc"}`
	if st := checkState(t, p.do(t, "PUT", "/api/policies", adminKey, doc, "If-Match", `"v2"`)); st.ID != 3 || !strings.Contains(st.YAML, "deadline_ms: 800") {
		t.Errorf("doc save %+v", st)
	}

	// Versions: pages, one version, export.
	page := decode[struct {
		Items      []policy.VersionMeta `json:"items"`
		NextCursor *string              `json:"next_cursor"`
	}](t, p.do(t, "GET", "/api/policies/versions?limit=2", readKey, ""))
	if len(page.Items) != 2 || page.Items[0].ID != 3 || page.NextCursor == nil {
		t.Fatalf("page %+v", page)
	}
	page2 := decode[struct {
		Items      []policy.VersionMeta `json:"items"`
		NextCursor *string              `json:"next_cursor"`
	}](t, p.do(t, "GET", "/api/policies/versions?limit=2&cursor="+*page.NextCursor, readKey, ""))
	if len(page2.Items) != 1 || page2.Items[0].ID != 1 || page2.NextCursor != nil {
		t.Errorf("page 2 %+v", page2)
	}
	wantError(t, p.do(t, "GET", "/api/policies/versions?cursor=zzz", readKey, ""), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "GET", "/api/policies/versions?limit=500", readKey, ""), http.StatusBadRequest, codeBadRequest)
	v1 := decode[policy.Version](t, p.do(t, "GET", "/api/policies/versions/1", readKey, ""))
	if v1.YAML != apiPolicyYAML || v1.Source != "file" {
		t.Errorf("v1 %+v", v1)
	}
	contractstest.ValidateJSON(t, schemaPolicyDoc, v1.Doc)
	wantError(t, p.do(t, "GET", "/api/policies/versions/99", readKey, ""), http.StatusNotFound, codeNotFound)
	wantError(t, p.do(t, "GET", "/api/policies/versions/x", readKey, ""), http.StatusBadRequest, codeBadRequest)
	w = p.do(t, "GET", "/api/policies/export?version=1", readKey, "")
	if w.Code != http.StatusOK || w.Body.String() != apiPolicyYAML || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "policies.v1.yaml") {
		t.Errorf("export %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := p.do(t, "GET", "/api/policies/export", readKey, ""); !strings.Contains(w.Body.String(), "deadline_ms: 800") {
		t.Errorf("export active %q", w.Body.String())
	}
	wantError(t, p.do(t, "GET", "/api/policies/export?version=0", readKey, ""), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "GET", "/api/policies/export?version=9", readKey, ""), http.StatusNotFound, codeNotFound)

	// Presets are the contract file.
	w = p.do(t, "GET", "/api/policies/presets", readKey, "")
	if w.Code != http.StatusOK {
		t.Fatalf("presets %d", w.Code)
	}
	contractstest.ValidateJSON(t, schemaPresets, w.Body.Bytes())

	// Rollback and adopt-file.
	wantError(t, p.do(t, "POST", "/api/policies/rollback", adminKey, `{}`), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "POST", "/api/policies/rollback", adminKey, `{"version":42}`), http.StatusNotFound, codeNotFound)
	st = checkState(t, p.do(t, "POST", "/api/policies/rollback", adminKey, `{"version":1,"message":"back"}`))
	if st.ID != 4 || st.Source != "rollback" || st.Message != "back" || st.SHA256 != v1.SHA256 {
		t.Errorf("rollback %+v", st)
	}
	if st := checkState(t, p.do(t, "POST", "/api/policies/adopt-file", adminKey, "")); st.ID != 4 {
		t.Errorf("adopting the active file content is a no-op: %+v", st)
	}
	writePolicy(t, p.file, "version: 1\ndefaults: {mode: flag, deadline_ms: 700}\n")
	if st := checkState(t, p.do(t, "POST", "/api/policies/adopt-file", adminKey, `{"message":"adopt"}`)); st.ID != 5 || st.Source != "adopt_file" || st.File.Status != "ok" {
		t.Errorf("adopt %+v", st)
	}
	writePolicy(t, p.file, "version: 1\ndefaults: {mode: nope}\n")
	wantError(t, p.do(t, "POST", "/api/policies/adopt-file", adminKey, ""), http.StatusUnprocessableEntity, codeValidationFailed)
	if err := os.Remove(p.file); err != nil {
		t.Fatal(err)
	}
	wantError(t, p.do(t, "POST", "/api/policies/adopt-file", adminKey, ""), http.StatusConflict, codeConflict)
	wantError(t, p.do(t, "POST", "/api/policies/adopt-file", readKey, ""), http.StatusForbidden, codeForbidden)

	// Validate.
	res := decode[policy.ValidateResult](t, p.do(t, "POST", "/api/policies/validate", readKey,
		`{"yaml":"version: 1\ndefaults: {mode: flag}\nroutes:\n  - {name: pay, match: {}}\n"}`))
	if !res.OK || len(res.BlockReadiness) != 2 || res.Diff == "" || res.Errors == nil || res.Warnings == nil {
		t.Errorf("validate %+v", res)
	}
	w = p.do(t, "POST", "/api/policies/validate", readKey, `{"doc":{"version":1,"defaults":{"mode":"nope"}}}`)
	res = decode[policy.ValidateResult](t, w)
	if res.OK || len(res.Errors) != 1 || res.Errors[0].Path != "defaults.mode" || res.Errors[0].Line != 0 {
		t.Errorf("invalid doc %+v", res)
	}
	var raw struct {
		Errors []json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for _, fe := range raw.Errors {
		contractstest.ValidateJSON(t, "api.v1.schema.json#/$defs/FieldError", fe)
	}
	wantError(t, p.do(t, "POST", "/api/policies/validate", readKey, `{}`), http.StatusBadRequest, codeBadRequest)

	// Dry-run without recorded traffic.
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\n","window":"24h"}`), http.StatusServiceUnavailable, codePersistenceDisabled)
}

func TestPoliciesAPIDryRun(t *testing.T) {
	mem := policytest.New()
	p := newPolicyRouter(t, mem, mem)
	// Record a flagged decision on support-bot with the engine's own policy.
	reg, _ := evaluators.Builtins()
	g, c := 0.85, 0.2
	spec := func(ref string) *decision.Spec { s, _ := reg.Get(ref); return s }
	in := &decision.Input{
		Policy:      decision.Policy{ConfiguredMode: "flag", Fail: "open", RequireCalibration: true, OnNoEvidence: "ignore"},
		Answers:     map[string]*decision.RawAnswer{"groundedness__v1": {Type: "noul", Noul: &g}, "contradiction__v1": {Type: "noul", Noul: &c}},
		QuestionMap: map[string]string{"groundedness__v1": "groundedness@1", "contradiction__v1": "contradiction@1"},
		Evaluators: []decision.EvaluatorInput{{Ref: "groundedness@1", Spec: spec("groundedness@1"), Stage: "active", Thresholds: decision.Thresholds{Space: "raw"}},
			{Ref: "contradiction@1", Spec: spec("contradiction@1"), Stage: "active", Thresholds: decision.Thresholds{Space: "raw"}}},
		Meta: decision.Meta{Backend: "mock", Stream: "none", EvidencePresent: true, RequestStatus: map[string]string{"0": "ok"},
			QIDRequest: map[string]int{"groundedness__v1": 0, "contradiction__v1": 0}},
		Detectors: map[string]decision.DetectorState{},
	}
	core, err := decision.Decide(in)
	if err != nil || core.Action != "flag" {
		t.Fatalf("decide %v %+v", err, core)
	}
	doc, _ := json.Marshal(map[string]any{"spec": "decision/v1", "status": core.Status, "skip_reason": nil, "evaluators": core.Evaluators,
		"detectors": []decision.DetectorResult{{ID: decision.DetNumbers, Ran: true, Findings: []decision.DetectorFinding{}}}, "excluded": []any{},
		"evidence_summary": map[string]any{"configured": []string{"request_system"}, "used": []map[string]any{{"source": "request_system", "items": 1, "labels": []string{"d"}, "tokens": 5}},
			"empty": []string{}, "knowledge": "not_requested", "truncated": false, "tokens": 5}})
	v := int64(1)
	mem.Rows = []store.ReplayRow{{TraceID: "t1", CreatedAt: time.Now().UTC(), Route: "support-bot", PolicyVersion: &v, Source: "proxy", Decision: doc}}

	body := `{"yaml":"version: 1\ndefaults: {mode: observe}\nroutes:\n  - name: support-bot\n    match: {}\n    mode: flag\n    evaluators: [{ref: groundedness@1, thresholds: {warn_at: 0.9}}, {ref: contradiction@1}]\n","window":"24h","limit":50}`
	w := p.do(t, "POST", "/api/policies/dry-run", readKey, body)
	res := decode[policy.DryRunResult](t, w)
	if res.Evaluated != 1 || res.NoLongerFlagged != 1 || !res.ReplayOnly || res.Window != "24h" || len(res.Examples) != 1 {
		t.Fatalf("dry-run %+v", res)
	}
	var wire map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &wire)
	for _, k := range []string{"window", "evaluated", "replay_only", "by_action_current", "by_action_proposed", "changed", "newly_blocked",
		"newly_flagged", "no_longer_flagged", "by_route", "deep_audit_agreement", "examples", "skipped", "caveats"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("PolicyDryRunResult lacks %s", k)
		}
	}
	// A stored version as the proposal.
	if res := decode[policy.DryRunResult](t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"version":1,"window":"1h"}`)); res.Changed != 0 || res.Evaluated != 1 {
		t.Errorf("version proposal %+v", res)
	}
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"version":9,"window":"1h"}`), http.StatusNotFound, codeNotFound)
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\ndefaults: {mode: x}\n","window":"1h"}`),
		http.StatusUnprocessableEntity, codeValidationFailed)
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\n","window":"1y"}`), http.StatusBadRequest, codeBadRequest)
	// The dry-run's own limit (6 per window; the five requests above count).
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\n","window":"1h","limit":0}`), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\n","window":"1h"}`), http.StatusTooManyRequests, "rate_limited")
	// Other policy routes do not share it.
	checkState(t, p.do(t, "GET", "/api/policies", readKey, ""))
}

func TestPoliciesAPIFileManagedAndLocked(t *testing.T) {
	p := newPolicyRouter(t, nil, nil)
	st := checkState(t, p.do(t, "GET", "/api/policies", adminKey, ""))
	if st.Persisted || st.Editable || *st.EditableReason != policy.ReasonFileManaged {
		t.Errorf("file-managed %+v", st)
	}
	wantError(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yaml":"version: 1\n"}`, "If-Match", `"v1"`), http.StatusConflict, codeFileManaged)
	wantError(t, p.do(t, "POST", "/api/policies/rollback", adminKey, `{"version":1}`), http.StatusConflict, codeFileManaged)
	wantError(t, p.do(t, "POST", "/api/policies/adopt-file", adminKey, ""), http.StatusConflict, codeFileManaged)

	locked := newPolicyRouter(t, policytest.New(), nil, func(c *policy.EngineConfig) { c.Lock = true })
	st = checkState(t, locked.do(t, "GET", "/api/policies", adminKey, ""))
	if !st.Locked || *st.EditableReason != policy.ReasonLocked {
		t.Errorf("locked %+v", st)
	}
	wantError(t, locked.do(t, "PUT", "/api/policies", adminKey, `{"yaml":"version: 1\n"}`, "If-Match", `"v1"`), http.StatusLocked, codeLocked)
}

// TestPoliciesAPIReadonly: read-only mode refuses every non-GET policy request, reads still work.
func TestPoliciesAPIReadonly(t *testing.T) {
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "policies.yaml")
	writePolicy(t, file, apiPolicyYAML)
	engine, err := policy.NewEngine(context.Background(), policy.EngineConfig{File: file,
		Env: policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg}, Store: policytest.New(), Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	pols, err := newPoliciesAPI(engine, policytest.New(), quietLog)
	if err != nil {
		t.Fatal(err)
	}
	g := testGuards()
	g.writable = (&server{cfg: &config.Config{Readonly: true}}).writable()
	r := gin.New()
	pols.register(r.Group("/api"), g)
	p := &policyRouter{r: r, file: file}
	checkState(t, p.do(t, "GET", "/api/policies", readKey, ""))
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/policies/validate", `{"yaml":"version: 1\n"}`},
		{"POST", "/api/policies/dry-run", `{"yaml":"version: 1\n","window":"1h"}`},
		{"PUT", "/api/policies", `{"yaml":"version: 1\n"}`},
		{"POST", "/api/policies/rollback", `{"version":1}`},
		{"POST", "/api/policies/adopt-file", ""},
	} {
		wantError(t, p.do(t, req.method, req.path, adminKey, req.body, "If-Match", `"v1"`), http.StatusForbidden, "readonly")
	}
}

func TestEvaluatorsAPI(t *testing.T) {
	p := newPolicyRouter(t, nil, nil)
	wantError(t, p.do(t, "GET", "/api/evaluators", "", ""), http.StatusUnauthorized, codeUnauthorized)
	list := decode[struct {
		Items []evaluatorMeta `json:"items"`
	}](t, p.do(t, "GET", "/api/evaluators?include_archived=true", readKey, ""))
	if len(list.Items) != 11 || list.Items[0].ID != "commitments" {
		t.Fatalf("list %+v", list.Items)
	}
	var grounded evaluatorMeta
	for _, e := range list.Items {
		if e.ID == "groundedness" {
			grounded = e
		}
		if e.AttachedRoutes == nil || e.UpdatedAt.IsZero() || e.Origin != "builtin" {
			t.Errorf("meta %+v", e)
		}
	}
	if len(grounded.AttachedRoutes) != 2 || grounded.AttachedRoutes[0] != (attachedRoute{Route: "support-bot", Stage: "active"}) {
		t.Errorf("groundedness attached %+v", grounded.AttachedRoutes)
	}

	w := p.do(t, "GET", "/api/evaluators/groundedness", readKey, "")
	type detailWire struct {
		evaluatorMeta
		Versions []map[string]any `json:"versions"`
		Latest   struct {
			Spec json.RawMessage `json:"spec"`
			evaluatorVersionMeta
		} `json:"latest"`
		CalibrationProgress calibrationProgress `json:"calibration_progress"`
	}
	detail := decode[detailWire](t, w)
	contractstest.ValidateJSON(t, schemaSpec, detail.Latest.Spec)
	cp := detail.CalibrationProgress
	if len(detail.Versions) != 1 || detail.Versions[0]["spec"] != nil || detail.Latest.CalibrationKey == "" || detail.Latest.CreatedBy != "system" ||
		cp.EvaluatorRef != "groundedness@1" || cp.Needed != (labelNeeds{N: 200, Pos: 30, Neg: 30}) || cp.State != "none" || cp.CanFitMeasured {
		t.Errorf("detail %+v", detail)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"id", "name", "category", "origin", "latest_version", "archived", "attached_routes", "updated_at", "versions", "latest", "calibration_progress"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("EvaluatorDetail lacks %s", k)
		}
	}
	v := decode[map[string]json.RawMessage](t, p.do(t, "GET", "/api/evaluators/refusal/versions/1", readKey, ""))
	contractstest.ValidateJSON(t, schemaSpec, v["spec"])
	for _, k := range []string{"evaluator_id", "version", "spec", "spec_sha256", "question_sha256", "calibration_key", "created_by", "created_at", "changelog"} {
		if _, ok := v[k]; !ok {
			t.Errorf("EvaluatorVersion lacks %s", k)
		}
	}
	wantError(t, p.do(t, "GET", "/api/evaluators/refusal/versions/2", readKey, ""), http.StatusNotFound, codeNotFound)
	wantError(t, p.do(t, "GET", "/api/evaluators/refusal/versions/x", readKey, ""), http.StatusBadRequest, codeBadRequest)
	wantError(t, p.do(t, "GET", "/api/evaluators/nope", readKey, ""), http.StatusNotFound, codeNotFound)
	d := decode[evaluatorDiff](t, p.do(t, "GET", "/api/evaluators/refusal/diff", readKey, ""))
	if d.From != 1 || d.To != 1 || d.Diff != "" {
		t.Errorf("diff of one version %+v", d)
	}
}

// TestEvaluatorVersionDiff uses a registry with a second, file-defined version.
func TestEvaluatorVersionDiff(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "evaluators", "refusal.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	v2 := bytes.Replace(bytes.Replace(b, []byte(`"version": 1`), []byte(`"version": 2`), 1), []byte(`"warn_at": 0.7`), []byte(`"warn_at": 0.75`), 1)
	writePolicy(t, filepath.Join(dir, "refusal.v2.json"), string(v2))
	reg, err := evaluators.New(context.Background(), evaluators.Config{Dir: dir, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	newEvaluatorsAPI(reg, nil, nil, nil, quietLog).register(r.Group("/api"), testGuards())
	p := &policyRouter{r: r}
	d := decode[evaluatorDiff](t, p.do(t, "GET", "/api/evaluators/refusal/diff", readKey, ""))
	if d.From != 1 || d.To != 2 || !strings.Contains(d.Diff, `-  "version": 1`) || !strings.Contains(d.Diff, `+    "warn_at": 0.75`) {
		t.Errorf("diff %+v", d)
	}
	if d := decode[evaluatorDiff](t, p.do(t, "GET", "/api/evaluators/refusal/diff?from=2&to=1", readKey, "")); !strings.Contains(d.Diff, `+  "version": 1`) {
		t.Errorf("reverse diff %s", d.Diff)
	}
	wantError(t, p.do(t, "GET", "/api/evaluators/refusal/diff?from=3", readKey, ""), http.StatusNotFound, codeNotFound)
	if e := decode[struct {
		Items []evaluatorMeta `json:"items"`
	}](t, p.do(t, "GET", "/api/evaluators", readKey, "")); len(e.Items) != 11 {
		t.Errorf("list %d", len(e.Items))
	}
}

func TestLoadPolicyEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	e := loadPolicyEnv(env(nil))
	if e.ReloadInterval != 5*time.Second || e.ConfigLock || !strings.HasSuffix(e.EvaluatorsDir, "evaluators") || len(e.Warnings) != 0 {
		t.Errorf("defaults %+v", e)
	}
	e = loadPolicyEnv(env(map[string]string{"TRUTHTABLE_POLICY_RELOAD_INTERVAL": "0", "TRUTHTABLE_CONFIG_LOCK": "true", "TRUTHTABLE_EVALUATORS_DIR": "/etc/x"}))
	if e.ReloadInterval != 0 || !e.ConfigLock || e.EvaluatorsDir != "/etc/x" {
		t.Errorf("set %+v", e)
	}
	e = loadPolicyEnv(env(map[string]string{"TRUTHTABLE_POLICY_RELOAD_INTERVAL": "soon", "TRUTHTABLE_CONFIG_LOCK": "maybe"}))
	if e.ReloadInterval != 5*time.Second || e.ConfigLock || len(e.Warnings) != 2 {
		t.Errorf("invalid %+v", e)
	}
}

// TestPolicyServicesPostgres wires the services on Postgres: a save on one replica reaches the
// other through NOTIFY, an evaluator version stored by another writer joins the registry, and
// the dry-run reads recorded decisions.
func TestPolicyServicesPostgres(t *testing.T) {
	schema := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Close after the deferred cancel stops the listener: a pool waits for its acquired
	// connections.
	t.Cleanup(pg.Close)
	file := filepath.Join(t.TempDir(), "policies.yaml")
	writePolicy(t, file, apiPolicyYAML)
	cfg := &config.Config{PolicyFile: file, GateDefaultMode: "observe", GateFailMode: "open", Jev: config.JevConfig{MaxResponseTokens: 6000}}
	wiring := policyWiring{cfg: cfg, pg: pg, channel: store.ScopedConfigChannel(schema.Name), log: quietLog,
		getenv: func(k string) string {
			if k == "TRUTHTABLE_POLICY_RELOAD_INTERVAL" {
				return "0" // notifications only
			}
			return ""
		}}
	a, err := newPolicyServices(ctx, wiring)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newPolicyServices(ctx, wiring)
	if err != nil {
		t.Fatal(err)
	}
	go b.run(ctx)
	r := gin.New()
	a.mount(r.Group("/api"), testGuards())
	p := &policyRouter{r: r, file: file}
	time.Sleep(300 * time.Millisecond) // let b subscribe

	st := checkState(t, p.do(t, "PUT", "/api/policies", adminKey, `{"yaml":"version: 1\ndefaults: {mode: flag}\n"}`, "If-Match", `"v1"`))
	waitFor(t, "policy on the replica", func() bool { return b.Engine.State().Version.ID == st.ID })

	doc := []byte(strings.Replace(strings.Replace(toneSpecJSON, "%V", "1", 1), "%W", "it", 1))
	spec, err := decision.ParseSpec(doc)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := spec.Hashes()
	if _, err := pg.UpsertEvaluatorVersions(ctx, wiring.channel, []store.EvaluatorUpsert{{
		Evaluator: store.EvaluatorRecord{ID: "tone", Name: "Tone", Category: "quality", Origin: "dashboard"},
		Version: store.EvaluatorVersionRecord{EvaluatorID: "tone", Version: 1, Spec: doc, SpecSHA256: h.SpecSHA256,
			QuestionSHA256: h.QuestionSHA256, CalibrationKey: h.CalibrationKey, CreatedBy: "admin"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "evaluator on the replica", func() bool { _, ok := b.Registry.Get("tone@1"); return ok })

	// A policy version naming an evaluator the replica has not loaded yet is retried once the
	// evaluator arrives.
	early := &store.PolicyVersionRecord{Source: store.PolicySourceDashboard, Author: "other", Doc: json.RawMessage(`{"version":1}`),
		YAML: "version: 1\ndefaults:\n  evaluators: [{ref: tone@2}]\n", SHA256: "early"}
	if err := pg.WithPolicyLock(ctx, wiring.channel, func(tx store.PolicyTx) error { return tx.Insert(ctx, early) }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the replica to see the version", func() bool { return b.Engine.State().HeadID == early.ID })
	if b.Engine.State().Version.ID == early.ID {
		t.Fatal("a version naming an unknown evaluator became active")
	}
	doc2 := []byte(strings.Replace(strings.Replace(toneSpecJSON, "%V", "2", 1), "%W", "it", 1))
	spec2, err := decision.ParseSpec(doc2)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := spec2.Hashes()
	if _, err := pg.UpsertEvaluatorVersions(ctx, wiring.channel, []store.EvaluatorUpsert{{
		Evaluator: store.EvaluatorRecord{ID: "tone", Name: "Tone", Category: "quality", Origin: "dashboard"},
		Version: store.EvaluatorVersionRecord{EvaluatorID: "tone", Version: 2, Spec: doc2, SpecSHA256: h2.SpecSHA256,
			QuestionSHA256: h2.QuestionSHA256, CalibrationKey: h2.CalibrationKey, CreatedBy: "admin"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retried version on the replica", func() bool { return b.Engine.State().Version.ID == early.ID })

	w := p.do(t, "POST", "/api/policies/dry-run", readKey, `{"yaml":"version: 1\n","window":"24h"}`)
	if res := decode[policy.DryRunResult](t, w); res.Evaluated != 0 || len(res.Caveats) == 0 {
		t.Errorf("empty dry-run %+v", res)
	}
	w = p.do(t, "GET", "/api/evaluators/refusal", readKey, "")
	if d := decode[map[string]json.RawMessage](t, w); !strings.Contains(string(d["calibration_progress"]), `"human_labels":{"n":0`) {
		t.Errorf("progress %s", d["calibration_progress"])
	}
	// File-managed services without a database.
	fm, err := newPolicyServices(ctx, policyWiring{cfg: cfg, log: quietLog, getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if st := fm.Engine.State(); st.Persisted || st.Policy.Default.Mode != "observe" {
		t.Errorf("file-managed %+v", st.Version)
	}
}

const toneSpecJSON = `{"spec_version":"evaluator/v1","id":"tone","version":%V,"name":"Tone","category":"quality",
  "state_fields":["assistant_response"],"question":{"type":"noul","instructions":"Is %W rude?","criteria":{"true":"Rude","false":"Polite"}},
  "evidence":{"requires":"none"},"signal":{"invert":false},"outcomes":{"warn_at":0.7}}`

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
