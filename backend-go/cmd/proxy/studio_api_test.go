package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// API schema refs of the Studio responses.
const (
	schemaStudioTest    = "api.v1.schema.json#/$defs/StudioTestResponse"
	schemaStudioSamples = "api.v1.schema.json#/$defs/StudioSamplesResponse"
	schemaStudioDraft   = "api.v1.schema.json#/$defs/StudioDraftResponse"
	schemaQuickCalib    = "api.v1.schema.json#/$defs/QuickCalibration"
	schemaDryRunResult  = "api.v1.schema.json#/$defs/DryRunResult"
	studioAdminKey      = "sk-test-studio-admin"
)

// ------------------------------------------------------------------------------- unit rig

// studioJev is a paid decision model that answers every question with noul and records the call
// options; with delay each call waits that long (or until its context ends).
type studioJev struct {
	backend string
	noul    float64
	cost    float64
	fail    error
	// failFrom > 0 fails only the calls from that one on (1-based), with fail.
	failFrom int64
	delay    time.Duration
	calls    atomic.Int64
	mu       sync.Mutex
	opts     []jev.CallOptions
}

func (f *studioJev) Info() jev.Info {
	b := f.backend
	if b == "" {
		b = jev.BackendOpenRouterDecisions
	}
	return jev.Info{Backend: b, Model: "typesafe/jev-1.13", Reason: "test"}
}

func (f *studioJev) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	n := f.calls.Add(1)
	f.mu.Lock()
	f.opts = append(f.opts, opt)
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, &jev.Error{Kind: jev.KindTimeout, Err: ctx.Err()}
		}
	}
	if f.fail != nil && (f.failFrom == 0 || n >= f.failFrom) {
		return nil, f.fail
	}
	answers := map[string]*decision.RawAnswer{}
	for qid := range req.Questions {
		p := f.noul
		answers[qid] = &decision.RawAnswer{Noul: &p}
	}
	cost := f.cost
	return &jev.Response{Answers: answers, CostUSD: &cost, CostSource: jev.CostReported, Snapshot: "jev-1.13.0", Attempts: 1}, nil
}

func (f *studioJev) options() []jev.CallOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jev.CallOptions(nil), f.opts...)
}

// fakeStudioDB is the Studio's storage in memory.
type fakeStudioDB struct {
	mu         sync.Mutex
	rows       []store.StudioResult
	audits     []store.AdminAuditEntry
	population int64
	candidates []store.DryRunCandidate
	sampleIDs  []string
	bodies     map[string]*store.BodyRecord
	compiles   map[string]*store.RecordedCompile
	scan       []store.ScanBody
	scanFilter store.ScanFilter
	sampleF    store.StudioSampleFilter
}

func (f *fakeStudioDB) InsertStudioResults(_ context.Context, rows []store.StudioResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeStudioDB) StudioSampleTraces(_ context.Context, sf store.StudioSampleFilter) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sampleF = sf
	return f.sampleIDs, nil
}

func (f *fakeStudioDB) GetTraceBody(_ context.Context, id string) (*store.BodyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.bodies[id]; ok {
		return b, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStudioDB) DryRunPopulation(context.Context, store.DryRunFilter) (int64, int64, error) {
	return f.population, int64(len(f.candidates)), nil
}

func (f *fakeStudioDB) DryRunSample(_ context.Context, _ store.DryRunFilter, limit int) ([]store.DryRunCandidate, error) {
	return f.candidates[:min(limit, len(f.candidates))], nil
}

func (f *fakeStudioDB) GetRecordedCompile(_ context.Context, id string, attempt int) (*store.RecordedCompile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rc, ok := f.compiles[fmt.Sprintf("%s/%d", id, attempt)]; ok {
		return rc, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStudioDB) NoEvidenceBodies(_ context.Context, sf store.ScanFilter) ([]store.ScanBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanFilter = sf
	return f.scan, nil
}

func (f *fakeStudioDB) AppendAdminAudit(_ context.Context, e store.AdminAuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, e)
	return nil
}

func (f *fakeStudioDB) auditRows() []store.AdminAuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.AdminAuditEntry(nil), f.audits...)
}

// fakeDrafter answers DraftEvaluator with fn.
type fakeDrafter func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error)

func (f fakeDrafter) DraftEvaluator(ctx context.Context, req *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
	return f(ctx, req)
}

type studioRig struct {
	api *studioAPI
	jev *studioJev
	db  *fakeStudioDB
	url string
}

// newStudioRig serves the Studio endpoints with every guard open, the repository's policy and the
// built-in evaluators, a paid fake decision model and an in-memory store. tune edits the API
// before it is mounted.
func newStudioRig(t *testing.T, tune func(r *studioRig)) *studioRig {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.LoadFile(config.RepoPath("config/policies.yaml"), policy.DefaultSeeds(), reg)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.StudioConfirmAboveUSD, cfg.DryRunMaxSamples = 0.10, 200
	r := &studioRig{jev: &studioJev{noul: 0.9, cost: 0.0001}, db: &fakeStudioDB{bodies: map[string]*store.BodyRecord{}, compiles: map[string]*store.RecordedCompile{}}}
	r.api = &studioAPI{
		cfg: cfg, client: func() jev.Client { return r.jev }, registry: reg,
		policy: func() *policy.Policy { return pol }, policyEnv: func() policy.Env { return policy.Env{Seeds: policy.DefaultSeeds(), Specs: reg} },
		policyVersion: func(context.Context, int64) (*policy.Version, error) { return nil, policy.ErrVersionNotFound },
		db:            r.db, barrier: func(context.Context, string) error { return nil },
		decisionPrice: func(backend, _ string) float64 {
			if backend == jev.BackendMock {
				return 0
			}
			return 10
		},
		budget: decision.DefaultBudget(),
		judge:  func() (string, string) { return "openrouter", "anthropic/claude-haiku-4.5" },
		judgePrice: func(string, string) (decision.PriceEntry, bool) {
			return decision.PriceEntry{InputPerMTok: 1, OutputPerMTok: 5}, true
		},
		referencePrice: func() (decision.PriceEntry, bool) {
			return decision.PriceEntry{InputPerMTok: 1, OutputPerMTok: 5}, true
		},
		now: time.Now, log: slog.New(slog.DiscardHandler),
	}
	if tune != nil {
		tune(r)
	}
	g := gin.New()
	pass := func(c *gin.Context) { c.Next() }
	r.api.register(g.Group("/api"), apiGuards{read: pass, content: pass, admin: pass, writable: pass, rateLimit: pass,
		dryRunLimit: pass, bodyLimit: pass, limitBy: func(int, time.Duration) gin.HandlerFunc { return pass }})
	hs := httptest.NewServer(g)
	t.Cleanup(hs.Close)
	r.url = hs.URL
	return r
}

// call sends method path with a JSON body (a string is sent as it is) and returns the response.
func studioCall(t *testing.T, ctx context.Context, base, method, path, key string, body any) *http.Response {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("X-TrustAgent-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// studioDo sends a request and reads the whole answer.
func studioDo(t *testing.T, base, method, path, key string, body any) (int, []byte) {
	t.Helper()
	resp := studioCall(t, context.Background(), base, method, path, key, body)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// forkedSpecDoc is the groundedness template as an unsaved spec named id.
func forkedSpecDoc(t *testing.T, id string) json.RawMessage {
	t.Helper()
	b, err := contracts.FS.ReadFile("evaluators/groundedness.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`"id": "groundedness"`), []byte(`"id": "`+id+`"`), 1)
	return bytes.Replace(b, []byte(`"builtin": true`), []byte(`"builtin": false`), 1)
}

const studioDoc = "Refund policy: annual plans can be refunded within 30 days of purchase. Monthly plans are not refundable."

// benchSamples are n test-bench samples about the refund document; even ones are labelled fire.
func benchSamples(n int) []map[string]any {
	out := make([]map[string]any, n)
	for i := range n {
		expected := "no_fire"
		if i%2 == 0 {
			expected = "fire"
		}
		out[i] = map[string]any{
			"id": fmt.Sprintf("s%d", i+1), "user_request": "How long is the refund window?",
			"assistant_response": "Annual plans can be refunded within 30 days.", "expected": expected,
			"evidence": []map[string]any{{"text": studioDoc, "source": "request_context"}},
		}
	}
	return out
}

// draftRefPattern is an unsaved spec's studio_results ref (D58).
var draftRefPattern = regexp.MustCompile(`^[a-z][a-z0-9_]+@draft:[0-9a-f]{12}$`)

// ------------------------------------------------------------------------------- #35 test

func TestStudioTestRunsAnUnsavedSpecAndKeepsTheRows(t *testing.T) {
	env, pg := pgEnv(t, envOpts{adminKey: studioAdminKey})
	spec := forkedSpecDoc(t, "my_grounding")
	code, body := studioDo(t, env.http.URL, http.MethodPost, "/api/studio/test", studioAdminKey,
		map[string]any{"spec": spec, "samples": benchSamples(3), "thresholds": map[string]any{"warn_at": 0.5}})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioTest, body)
	var resp studioTestResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.RunID, testRunPrefix) || len(resp.Results) != 3 || resp.Summary.N != 3 || resp.Summary.Backend != jev.BackendMock {
		t.Fatalf("response = %s", body)
	}
	compared, agreed := 0, 0
	for _, r := range resp.Results {
		if r.Status != decision.EvalAssessed || r.Fires == nil || r.RiskRaw == nil || r.Level == nil {
			t.Fatalf("result = %+v", r)
		}
		if fires := *r.Level != decision.LevelOK; fires != *r.Fires {
			t.Errorf("%s: level %s but fires %v", r.SampleID, *r.Level, *r.Fires)
		}
		if *r.Fires != (*r.RiskRaw >= 0.5) {
			t.Errorf("%s: risk %v against warn_at 0.5 but fires %v", r.SampleID, *r.RiskRaw, *r.Fires)
		}
		if r.Agrees == nil || *r.Agrees != ((*r.Expected == "fire") == *r.Fires) {
			t.Errorf("%s: agrees = %v", r.SampleID, r.Agrees)
		}
		compared++
		if *r.Agrees {
			agreed++
		}
	}
	if resp.Summary.Assessed != 3 || resp.Summary.Agreement == nil || *resp.Summary.Agreement != float64(agreed)/float64(compared) {
		t.Errorf("summary = %+v", resp.Summary)
	}
	// The rows are kept under the draft ref; nothing is saved as an evaluator version.
	rows, err := pg.StudioRun(context.Background(), resp.RunID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows: %d, err %v", len(rows), err)
	}
	for _, r := range rows {
		if !draftRefPattern.MatchString(r.EvaluatorRef) || !strings.HasPrefix(r.EvaluatorRef, "my_grounding@draft:") ||
			r.Kind != store.StudioKindTest || r.Status == nil || *r.Status != decision.EvalAssessed || r.RiskRaw == nil {
			t.Errorf("row = %+v", r)
		}
	}
	if _, ok := env.srv.policies.Registry.Evaluator("my_grounding"); ok {
		t.Error("a test saved the evaluator")
	}
}

func TestStudioTestValidation(t *testing.T) {
	r := newStudioRig(t, nil)
	spec := forkedSpecDoc(t, "my_grounding")
	invalid := json.RawMessage(`{"spec_version":"evaluator/v1","id":"my_grounding","version":1}`)
	dup := benchSamples(2)
	dup[1]["id"] = "s1"
	badSource := benchSamples(1)
	badSource[0]["evidence"] = []map[string]any{{"text": "x", "source": "rumour"}}
	cases := []struct {
		name string
		body any
		path string
	}{
		{"51 samples", map[string]any{"spec": spec, "samples": benchSamples(51)}, "samples"},
		{"no samples", map[string]any{"spec": spec, "samples": []any{}}, "samples"},
		{"invalid spec", map[string]any{"spec": invalid, "samples": benchSamples(1)}, ""},
		{"spec and ref", map[string]any{"spec": spec, "ref": "groundedness@1", "samples": benchSamples(1)}, "ref"},
		{"neither", map[string]any{"samples": benchSamples(1)}, "spec"},
		{"unknown ref", map[string]any{"ref": "nope@9", "samples": benchSamples(1)}, "ref"},
		{"warn_at out of range", map[string]any{"ref": "groundedness@1", "samples": benchSamples(1), "thresholds": map[string]any{"warn_at": 1.5}}, "thresholds.warn_at"},
		{"repeated sample id", map[string]any{"ref": "groundedness@1", "samples": dup}, "samples[1].id"},
		{"unknown evidence source", map[string]any{"ref": "groundedness@1", "samples": badSource}, "samples[0].evidence[0].source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", c.body)
			if code != http.StatusUnprocessableEntity || errorCodeOf(t, body) != codeValidationFailed {
				t.Fatalf("HTTP %d %s", code, body)
			}
			var e apiErrorBody
			_ = json.Unmarshal(body, &e)
			if c.path != "" && (len(e.Error.FieldErrors) == 0 || e.Error.FieldErrors[0].Path != c.path) {
				t.Errorf("field errors = %+v", e.Error.FieldErrors)
			}
		})
	}
	if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", `{"samples":[], "extra": 1}`); code != http.StatusBadRequest {
		t.Errorf("unknown member: %d %s", code, body)
	}
	if n := r.jev.calls.Load(); n != 0 {
		t.Errorf("%d decision-model calls for refused tests", n)
	}
}

func TestStudioTestCallsAreNotEnforcing(t *testing.T) {
	r := newStudioRig(t, nil)
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": benchSamples(4)})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	opts := r.jev.options()
	if len(opts) != 4 {
		t.Fatalf("calls = %d", len(opts))
	}
	for _, o := range opts {
		if o.Enforcing || o.Component != jev.ComponentStudioTest || o.Class != jev.ClassAsync {
			t.Errorf("options = %+v", o)
		}
	}
	var resp studioTestResponse
	_ = json.Unmarshal(body, &resp)
	// Four paid calls at $0.0001 each, rows under the saved ref.
	if math.Abs(resp.Summary.CostUSD-0.0004) > 1e-12 || resp.Summary.Snapshot == nil || *resp.Summary.Snapshot != "jev-1.13.0" {
		t.Errorf("summary = %+v", resp.Summary)
	}
	if len(r.db.rows) != 4 || r.db.rows[0].EvaluatorRef != "groundedness@1" {
		t.Errorf("rows = %+v", r.db.rows)
	}
}

func TestStudioTestBudgetAndBackend(t *testing.T) {
	// The estimate is over what is left before the observe line.
	var asked []bool
	r := newStudioRig(t, func(r *studioRig) {
		r.api.costs.decisionHeadroom = func(enforcing bool) float64 { asked = append(asked, enforcing); return 1e-9 }
	})
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": benchSamples(2)})
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeBudgetExhausted || r.jev.calls.Load() != 0 {
		t.Fatalf("over the headroom: %d %s (calls %d)", code, body, r.jev.calls.Load())
	}
	if len(asked) == 0 || asked[0] {
		t.Errorf("headroom asked with enforcing %v, want the observe line (false)", asked)
	}
	var e apiErrorBody
	_ = json.Unmarshal(body, &e)
	if e.Error.Details["estimate_usd"] == nil || e.Error.Details["decision_budget_left_usd"] == nil {
		t.Errorf("details = %v", e.Error.Details)
	}

	// The decision model refuses for budget mid-test.
	r = newStudioRig(t, func(r *studioRig) { r.jev.fail = &jev.Error{Kind: jev.KindBudget} })
	code, body = studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": benchSamples(2)})
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeBudgetExhausted || len(r.db.rows) != 0 {
		t.Errorf("budget refusal: %d %s", code, body)
	}
	// The budget runs out after the first call: the sample that ran was paid for and keeps its row.
	r = newStudioRig(t, func(r *studioRig) { r.jev.fail, r.jev.failFrom = &jev.Error{Kind: jev.KindBudget}, 2 })
	code, body = studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": benchSamples(2)})
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeBudgetExhausted || len(r.db.rows) != 1 || r.db.rows[0].CostUSD == nil {
		t.Errorf("budget ran out mid-test: %d %s, rows %d", code, body, len(r.db.rows))
	}

	// The decision model is off.
	r = newStudioRig(t, func(r *studioRig) { r.jev.backend = jev.BackendOff })
	code, body = studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": benchSamples(1)})
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeDecisionModelUnavailable || r.jev.calls.Load() != 0 {
		t.Errorf("off: %d %s", code, body)
	}
}

func TestStudioTestErrorsAndNotAssessed(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) { r.jev.fail = &jev.Error{Kind: jev.KindCredits, Status: 402} })
	samples := benchSamples(2)
	delete(samples[1], "evidence") // groundedness needs evidence: not assessed, never asked
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/test", "", map[string]any{"ref": "groundedness@1", "samples": samples})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioTest, body)
	var resp studioTestResponse
	_ = json.Unmarshal(body, &resp)
	a, b := resp.Results[0], resp.Results[1]
	if a.Status != decision.EvalError || a.Reason == nil || *a.Reason != "error:credits" || a.Fires != nil || a.Agrees != nil {
		t.Errorf("failed call: %+v", a)
	}
	if b.Status != decision.EvalNotAssessed || b.Reason == nil || *b.Reason != "no_evidence" || b.CostUSD != 0 {
		t.Errorf("no evidence: %+v", b)
	}
	if resp.Summary.Assessed != 0 || resp.Summary.Agreement != nil || r.jev.calls.Load() != 1 {
		t.Errorf("summary %+v, calls %d", resp.Summary, r.jev.calls.Load())
	}
}

func TestStudioEndpointsAuthAndLimits(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: studioAdminKey})
	// testKey holds ingest, read and content: Studio writes and spend need admin.
	for _, p := range []string{"/api/studio/test", "/api/studio/draft", "/api/studio/samples", "/api/studio/dry-run"} {
		if code, body := env.do(http.MethodPost, p, testKey, []byte(`{}`)); code != http.StatusForbidden {
			t.Errorf("%s with a content key: %d %s", p, code, body)
		}
		if code, _ := env.do(http.MethodPost, p, "", []byte(`{}`)); code != http.StatusUnauthorized {
			t.Errorf("%s without a key: %d", p, code)
		}
	}
	if code, _ := env.get("/api/evidence/suggestions", testKey); code != http.StatusForbidden {
		t.Errorf("suggestions with a content key: %d", code)
	}
	// The quick calibration is pure: read is enough.
	if code, body := env.do(http.MethodPost, "/api/studio/calibrate", testKey, []byte(`{"labeled":[]}`)); code != http.StatusOK {
		t.Errorf("calibrate with a read key: %d %s", code, body)
	}
	// 20 tests a minute per caller.
	body, _ := json.Marshal(map[string]any{"ref": "groundedness@1", "samples": benchSamples(1)})
	for i := range studioTestLimit {
		if code, b := env.do(http.MethodPost, "/api/studio/test", studioAdminKey, body); code != http.StatusOK {
			t.Fatalf("test %d: %d %s", i+1, code, b)
		}
	}
	if code, b := env.do(http.MethodPost, "/api/studio/test", studioAdminKey, body); code != http.StatusTooManyRequests || errorCodeOf(t, b) != "rate_limited" {
		t.Errorf("test %d: %d %s", studioTestLimit+1, code, b)
	}
}

func TestStudioReadonlyRefusesWrites(t *testing.T) {
	env := newTestEnv(t, envOpts{adminKey: studioAdminKey, readonly: true})
	if code, body := env.do(http.MethodPost, "/api/studio/test", studioAdminKey, []byte(`{}`)); code != http.StatusForbidden || errorCodeOf(t, body) != "readonly" {
		t.Errorf("readonly: %d %s", code, body)
	}
}

// ------------------------------------------------------------------------------- #36 samples

func TestStudioSamplesFromTraffic(t *testing.T) {
	env, _ := pgEnv(t, envOpts{adminKey: studioAdminKey})
	scenarios := loadDemoScenarios(t)
	ids := map[string]bool{}
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "tool-results-grounded"} {
		resp := env.send(scenarios[id])
		traceID := resp.Header.Get("X-TrustAgent-Trace-Id")
		env.settledTrace(traceID)
		ids[traceID] = true
	}
	code, body := env.do(http.MethodPost, "/api/studio/samples", studioAdminKey, []byte(`{"window":"24h","filter":"random","limit":50}`))
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioSamples, body)
	var resp struct {
		Samples []studioSampleView `json:"samples"`
	}
	_ = json.Unmarshal(body, &resp)
	if len(resp.Samples) != 3 {
		t.Fatalf("samples = %s", body)
	}
	for _, s := range resp.Samples {
		if !ids[s.TraceID] || s.ID != s.TraceID || s.AssistantResponse == "" || s.UserRequest == nil {
			t.Errorf("sample = %+v", s)
		}
	}
	// The flagged filter keeps the flagged answer only.
	code, body = env.do(http.MethodPost, "/api/studio/samples", studioAdminKey, []byte(`{"window":"24h","filter":"flagged"}`))
	_ = json.Unmarshal(body, &resp)
	if code != http.StatusOK || len(resp.Samples) != 1 || !strings.Contains(resp.Samples[0].AssistantResponse, "60 days") {
		t.Errorf("flagged: %d %s", code, body)
	}
	// A sample is a test bench input as it is: the test endpoint takes it back.
	in, _ := json.Marshal(map[string]any{"ref": "groundedness@1", "samples": resp.Samples})
	if code, b := env.do(http.MethodPost, "/api/studio/test", studioAdminKey, in); code != http.StatusOK {
		t.Errorf("round trip into a test: %d %s", code, b)
	}
}

func TestStudioSamplesValidationBarrierAndNoDatabase(t *testing.T) {
	var barred []string
	r := newStudioRig(t, func(r *studioRig) {
		r.api.barrier = func(_ context.Context, id string) error { barred = append(barred, id); return nil }
		expired := time.Now().Add(-time.Minute)
		live := time.Now().Add(time.Hour)
		answer, req := "Within 30 days.", "Refund window?"
		r.db.sampleIDs = []string{"t1", "t2", "t3"}
		r.db.bodies["t1"] = &store.BodyRecord{TraceID: "t1", ResponseText: &answer, UserRequest: &req, ExpiresAt: live,
			Evidence: json.RawMessage(`[{"id":"a","source":"tool_results","label":"x","text":"trusted"},{"id":"b","source":"tool_results","label":"y","text":"risky","untrusted":true}]`),
			Rules:    map[string]string{"currency": "USD"}}
		r.db.bodies["t2"] = &store.BodyRecord{TraceID: "t2", ResponseText: &answer, ExpiresAt: expired}
	})
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/samples", "", map[string]any{"window": "7d", "filter": "disagreement", "route": "support-bot"})
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioSamples, body)
	var resp struct {
		Samples []studioSampleView `json:"samples"`
	}
	_ = json.Unmarshal(body, &resp)
	if len(resp.Samples) != 1 || resp.Samples[0].TraceID != "t1" || len(resp.Samples[0].Evidence) != 1 ||
		resp.Samples[0].Evidence[0].Text != "trusted" || resp.Samples[0].Rules["currency"] != "USD" {
		t.Errorf("samples = %s", body)
	}
	if strings.Join(barred, ",") != "t1,t2,t3" {
		t.Errorf("barrier ran for %v", barred)
	}
	if f := r.db.sampleF; f.Filter != store.SampleDisagreement || f.Route != "support-bot" || f.Limit != defaultStudioSamples ||
		f.Now.Sub(f.Since) != 7*24*time.Hour {
		t.Errorf("filter = %+v", f)
	}
	for _, bad := range []string{`{"window":"2d","filter":"random"}`, `{"window":"7d","filter":"all"}`, `{"window":"7d","filter":"random","limit":51}`, `{"window":"7d","filter":"random","limit":0}`} {
		if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/samples", "", bad); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", bad, code, body)
		}
	}
	noDB := newStudioRig(t, func(r *studioRig) { r.api.db = nil })
	if code, body := studioDo(t, noDB.url, http.MethodPost, "/api/studio/samples", "", `{"window":"7d","filter":"random"}`); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("no database: %d %s", code, body)
	}
}

// ------------------------------------------------------------------------------- #37 calibrate

func TestStudioCalibrateMatchesTheQuickVectors(t *testing.T) {
	r := newStudioRig(t, nil)
	entries, err := contracts.FS.ReadDir("vectors/calibrate_fit")
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "quick-") {
			continue
		}
		raw, err := contracts.FS.ReadFile("vectors/calibrate_fit/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Input    map[string]any `json:"input"`
			Expected any            `json:"expected"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		delete(v.Input, "op")
		code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/calibrate", "", v.Input)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", e.Name(), code, body)
		}
		contractstest.ValidateJSON(t, schemaQuickCalib, body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		// The vectors pin the numbers, not the message's wording.
		if msg, _ := got["message"].(string); msg == "" && got["enough"] == false {
			t.Errorf("%s: no message for too few labels", e.Name())
		}
		delete(got, "message")
		if path, ok := sameJSON(any(got), v.Expected, "$"); !ok {
			t.Errorf("%s: differs at %s\n got %s", e.Name(), path, body)
		}
		ran++
	}
	if ran < 5 {
		t.Fatalf("%d quick vectors ran", ran)
	}
	big := make([]map[string]any, maxQuickLabels+1)
	for i := range big {
		big[i] = map[string]any{"risk_raw": 0.5, "label": i%2 == 0}
	}
	if code, _ := studioDo(t, r.url, http.MethodPost, "/api/studio/calibrate", "", map[string]any{"labeled": big}); code != http.StatusUnprocessableEntity {
		t.Errorf("too many labels: %d", code)
	}
	if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/calibrate", "", `{"labeled":[{"risk_raw":1.5,"label":true}]}`); code != http.StatusUnprocessableEntity {
		t.Errorf("risk out of range: %d %s", code, body)
	}
}

// sameJSON compares decoded JSON, numbers within 1e-9; it returns the first differing path.
func sameJSON(a, b any, path string) (string, bool) {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return path, ok && math.Abs(x-y) <= 1e-9
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return path, false
		}
		for k, v := range x {
			if p, ok := sameJSON(v, y[k], path+"."+k); !ok {
				return p, false
			}
		}
		return path, true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return path, false
		}
		for i := range x {
			if p, ok := sameJSON(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); !ok {
				return p, false
			}
		}
		return path, true
	}
	return path, reflect.DeepEqual(a, b)
}

// ------------------------------------------------------------------------------- #38 dry-run

// studioEvent is one server-sent event.
type studioEvent struct {
	name string
	data json.RawMessage
}

// readStudioEvents reads a dry-run stream; onEvent (optional) sees each event as it arrives.
func readStudioEvents(t *testing.T, resp *http.Response, onEvent func(studioEvent)) []studioEvent {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	var out []studioEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var ev studioEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = json.RawMessage(strings.TrimPrefix(line, "data: "))
		case line == "" && ev.name != "":
			out = append(out, ev)
			if onEvent != nil {
				onEvent(ev)
			}
			ev = studioEvent{}
		}
	}
	return out
}

// dryRunCandidates are n support-bot traces with live bodies; prob is each body's inclusion
// probability.
func dryRunCandidates(n int, prob func(i int) float64) []store.DryRunCandidate {
	out := make([]store.DryRunCandidate, n)
	req := "How long is the refund window?"
	for i := range out {
		id := fmt.Sprintf("t%03d", i)
		answer := "Annual plans can be refunded within 30 days (case " + id + ")."
		warn := decision.LevelWarn
		out[i] = store.DryRunCandidate{TraceID: id, Levels: map[string]*string{"contradiction@1": &warn},
			Body: store.BodyRecord{TraceID: id, ResponseText: &answer, UserRequest: &req, InclusionProb: prob(i),
				Evidence:  json.RawMessage(`[{"id":"refund.md","source":"request_system","label":"refund.md","text":"` + studioDoc + `"}]`),
				ExpiresAt: time.Now().Add(time.Hour)}}
	}
	return out
}

func dryRunBody(extra map[string]any) map[string]any {
	b := map[string]any{"ref": "groundedness@1", "route": "support-bot", "window": "24h", "max_samples": 200}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func TestStudioDryRunStreamsInOrderAndExtrapolates(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		r.jev.noul, r.jev.cost = 0.9, 1e-6 // every answer fires, each call under its estimate
		r.db.population = 1000
		// Half the bodies were kept with probability 0.25 (passing traffic sampled).
		r.db.candidates = dryRunCandidates(24, func(i int) float64 { return map[bool]float64{true: 1, false: 0.25}[i%2 == 0] })
		r.api.cfg.StudioConfirmAboveUSD = 1 // under the confirmation line
	})
	resp := studioCall(t, context.Background(), r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil))
	events := readStudioEvents(t, resp, nil)
	var names []string
	for _, e := range events {
		names = append(names, e.name)
	}
	if got := strings.Join(names, ","); got != "estimate,progress,progress,progress,done" {
		t.Fatalf("events = %s", got)
	}
	var est struct {
		Cost       float64 `json:"est_cost_usd"`
		Sampled    int     `json:"sampled"`
		Population int64   `json:"population"`
	}
	_ = json.Unmarshal(events[0].data, &est)
	if est.Sampled != 24 || est.Population != 1000 || est.Cost <= 0 {
		t.Errorf("estimate = %s", events[0].data)
	}
	lastCost := 0.0
	for i, e := range events[1:4] {
		var p struct {
			Evaluated int     `json:"evaluated"`
			Fired     int     `json:"fired"`
			Cost      float64 `json:"cost_usd"`
		}
		_ = json.Unmarshal(e.data, &p)
		if want := min((i+1)*dryRunBatch, 24); p.Evaluated != want || p.Fired != want || p.Cost <= lastCost {
			t.Errorf("progress %d = %s", i, e.data)
		}
		lastCost = p.Cost
	}
	contractstest.ValidateJSON(t, schemaDryRunResult, events[4].data)
	var done dryRunResult
	_ = json.Unmarshal(events[4].data, &done)
	lo, hi := decision.WilsonInterval(24, 24, decision.WilsonZ95)
	if done.Sampled != 24 || done.Fired != 24 || done.FireRate != 1 || done.FireRateCI95 != [2]float64{lo, hi} ||
		done.Population != 1000 || done.EvaluatorRef != "groundedness@1" || done.Route != "support-bot" || done.Window != "24h" ||
		!strings.HasPrefix(done.RunID, dryRunRunPrefix) || math.Abs(done.CostUSD-lastCost) > 1e-12 {
		t.Errorf("done = %s", events[4].data)
	}
	// Every answer fired, so the weighted rate is 1 too: the extrapolation is the population.
	if done.ExtrapolatedFires != 1000 {
		t.Errorf("extrapolated = %d", done.ExtrapolatedFires)
	}
	if len(done.Overlap) != 1 || done.Overlap[0].EvaluatorRef != "contradiction@1" || done.Overlap[0].BothFired != 24 {
		t.Errorf("overlap = %+v", done.Overlap)
	}
	for _, o := range r.jev.options() {
		if o.Enforcing || o.Component != jev.ComponentDryRun || o.Route != "support-bot" {
			t.Fatalf("options = %+v", o)
		}
	}
	audits := r.db.auditRows()
	if len(audits) != 1 || audits[0].Action != "studio.dry_run" || audits[0].Target != "support-bot" {
		t.Fatalf("audit = %+v", audits)
	}
	diff, _ := audits[0].Diff.(map[string]any)
	if diff["outcome"] != dryRunDone || diff["cost_usd"] != done.CostUSD || diff["run_id"] != done.RunID {
		t.Errorf("audit diff = %v", diff)
	}
	if len(r.db.rows) != 24 || r.db.rows[0].Kind != store.StudioKindDryRun {
		t.Errorf("rows = %d", len(r.db.rows))
	}
}

func TestStudioDryRunWeightsBodiesKeptLessOften(t *testing.T) {
	// The answers kept with probability 0.25 pass; the always-kept ones fire.
	r := newStudioRig(t, func(r *studioRig) {
		r.api.decisionPrice = func(string, string) float64 { return 0 } // free: no confirmation
		r.db.population = 500
		cands := dryRunCandidates(10, func(i int) float64 { return map[bool]float64{true: 1, false: 0.25}[i < 2] })
		r.db.candidates = cands
		r.api.client = func() jev.Client { return firesOn{inner: r.jev, ids: map[string]bool{"t000": true, "t001": true}} }
	})
	events := readStudioEvents(t, studioCall(t, context.Background(), r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil)), nil)
	var done dryRunResult
	_ = json.Unmarshal(events[len(events)-1].data, &done)
	// 2 of 10 sampled fire (20%); weighted, 2 of 2 + 8×4 = 34 → 500 × 2/34 ≈ 29, not 100.
	if done.Fired != 2 || done.FireRate != 0.2 || done.ExtrapolatedFires != 29 {
		t.Errorf("done = %s", events[len(events)-1].data)
	}
	lo, hi := decision.WilsonInterval(2, 10, decision.WilsonZ95)
	if done.FireRateCI95 != [2]float64{lo, hi} {
		t.Errorf("ci = %v, want Wilson %v %v", done.FireRateCI95, lo, hi)
	}
}

// firesOn answers high risk only for the samples whose answer names one of ids (dryRunCandidates
// put the trace id in the answer), else low risk.
type firesOn struct {
	inner *studioJev
	ids   map[string]bool
}

func (f firesOn) Info() jev.Info { return f.inner.Info() }

func (f firesOn) Decide(ctx context.Context, req *jev.Request, opt jev.CallOptions) (*jev.Response, error) {
	resp, err := f.inner.Decide(ctx, req, opt)
	if err != nil {
		return nil, err
	}
	state, _ := json.Marshal(req.State)
	fire := false
	for id := range f.ids {
		fire = fire || strings.Contains(string(state), id)
	}
	for _, a := range resp.Answers {
		p := 0.05
		if fire {
			p = 0.95
		}
		a.Noul = &p
	}
	return resp, nil
}

func TestStudioDryRunConfirmationAndBudget(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		r.db.population = 1840
		r.db.candidates = dryRunCandidates(200, func(int) float64 { return 1 })
		r.api.cfg.StudioConfirmAboveUSD = 1e-9
		r.api.costs.decisionHeadroom = func(bool) float64 { return 1.84 }
	})
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil))
	if code != http.StatusConflict || errorCodeOf(t, body) != codeConfirmationRequired {
		t.Fatalf("unconfirmed: %d %s", code, body)
	}
	var e apiErrorBody
	_ = json.Unmarshal(body, &e)
	d := e.Error.Details
	if d["samples"] != float64(200) || d["population"] != float64(1840) || d["decision_budget_left_usd"] != 1.84 || d["estimate_usd"].(float64) <= 0 {
		t.Errorf("details = %v", d)
	}
	if r.jev.calls.Load() != 0 || len(r.db.auditRows()) != 0 {
		t.Errorf("a refused dry-run called %d times", r.jev.calls.Load())
	}

	// Confirmed but over the headroom.
	r = newStudioRig(t, func(r *studioRig) {
		r.db.population, r.db.candidates = 10, dryRunCandidates(10, func(int) float64 { return 1 })
		r.api.costs.decisionHeadroom = func(bool) float64 { return 0 }
	})
	code, body = studioDo(t, r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(map[string]any{"confirm_cost": true}))
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeBudgetExhausted || r.jev.calls.Load() != 0 {
		t.Errorf("over the headroom: %d %s", code, body)
	}

	// The budget runs out mid-run: an error event, the rows so far, the audit row.
	r = newStudioRig(t, func(r *studioRig) {
		r.db.population, r.db.candidates = 30, dryRunCandidates(30, func(int) float64 { return 1 })
		r.jev.fail = &jev.Error{Kind: jev.KindBudget}
		r.api.cfg.StudioConfirmAboveUSD = 1
	})
	events := readStudioEvents(t, studioCall(t, context.Background(), r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil)), nil)
	if n := len(events); n != 3 || events[1].name != "progress" || events[2].name != "error" {
		t.Fatalf("events = %+v", events)
	}
	if code := errorCodeOf(t, events[2].data); code != codeBudgetExhausted {
		t.Errorf("error event code %s", code)
	}
	if a := r.db.auditRows(); len(a) != 1 || a[0].Diff.(map[string]any)["outcome"] != dryRunNoBudget {
		t.Errorf("audit = %+v", a)
	}
	if r.jev.calls.Load() != dryRunBatch {
		t.Errorf("calls after the budget ran out: %d", r.jev.calls.Load())
	}
}

func TestStudioDryRunClampsAndValidates(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		r.api.decisionPrice = func(string, string) float64 { return 0 }
		r.api.cfg.DryRunMaxSamples = 200
		r.db.population, r.db.candidates = 300, dryRunCandidates(250, func(int) float64 { return 1 })
	})
	events := readStudioEvents(t, studioCall(t, context.Background(), r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(map[string]any{"max_samples": 1000})), nil)
	var est struct {
		Sampled int `json:"sampled"`
	}
	_ = json.Unmarshal(events[0].data, &est)
	if est.Sampled != 200 || r.jev.calls.Load() != 200 {
		t.Errorf("clamp: sampled %d, calls %d", est.Sampled, r.jev.calls.Load())
	}
	for _, c := range []map[string]any{
		{"window": "1h"}, {"route": "nope"}, {"max_samples": 0}, {"ref": "", "spec": nil},
	} {
		if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(c)); code != http.StatusUnprocessableEntity {
			t.Errorf("%v: %d %s", c, code, body)
		}
	}
	noDB := newStudioRig(t, func(r *studioRig) { r.api.db = nil })
	if code, body := studioDo(t, noDB.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil)); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("no database: %d %s", code, body)
	}
}

func TestStudioDryRunAbortStopsCalls(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		r.jev.delay, r.jev.cost = 40*time.Millisecond, 1e-6
		r.db.population, r.db.candidates = 100, dryRunCandidates(100, func(int) float64 { return 1 })
		r.api.cfg.StudioConfirmAboveUSD = 1
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := studioCall(t, ctx, r.url, http.MethodPost, "/api/studio/dry-run", "", dryRunBody(nil))
	events := readStudioEvents(t, resp, func(e studioEvent) {
		if e.name == "progress" {
			cancel() // the user closes the dialog after the first batch
		}
	})
	if len(events) > 2 || events[len(events)-1].name != "progress" {
		t.Errorf("events after the abort: %+v", events)
	}
	var audits []store.AdminAuditEntry
	deadline := time.Now().Add(5 * time.Second)
	for len(audits) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		audits = r.db.auditRows()
	}
	if len(audits) != 1 || audits[0].Diff.(map[string]any)["outcome"] != dryRunCancelled {
		t.Fatalf("audit = %+v", audits)
	}
	if calls := r.jev.calls.Load(); calls >= 100 || calls > 2*dryRunBatch {
		t.Errorf("%d of 100 calls made after an abort following the first batch", calls)
	}
	if cost := audits[0].Diff.(map[string]any)["cost_usd"].(float64); cost <= 0 {
		t.Errorf("the cancelled run's cost was not recorded: %v", cost)
	}
}

func TestStudioDryRunAgainstPostgres(t *testing.T) {
	env, pg := pgEnv(t, envOpts{adminKey: studioAdminKey})
	scenarios := loadDemoScenarios(t)
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "tool-results-grounded"} {
		env.settledTrace(env.send(scenarios[id]).Header.Get("X-TrustAgent-Trace-Id"))
	}
	spec := forkedSpecDoc(t, "my_grounding")
	resp := studioCall(t, context.Background(), env.http.URL, http.MethodPost, "/api/studio/dry-run", studioAdminKey,
		map[string]any{"spec": spec, "route": "support-bot", "window": "24h", "max_samples": 50})
	events := readStudioEvents(t, resp, nil)
	if len(events) != 3 || events[0].name != "estimate" || events[1].name != "progress" || events[2].name != "done" {
		t.Fatalf("events = %+v", events)
	}
	contractstest.ValidateJSON(t, schemaDryRunResult, events[2].data)
	var done dryRunResult
	_ = json.Unmarshal(events[2].data, &done)
	// The mock decision model's cost is simulated, never billed.
	if done.Sampled != 3 || done.Population != 3 || !draftRefPattern.MatchString(done.EvaluatorRef) || done.CostUSD < 0 ||
		done.Snapshot == nil || *done.Snapshot != jev.DefaultMockModel {
		t.Errorf("done = %s", events[2].data)
	}
	rows, err := pg.StudioRun(context.Background(), done.RunID)
	if err != nil || len(rows) != 3 || rows[0].Kind != store.StudioKindDryRun {
		t.Errorf("rows: %d %v", len(rows), err)
	}
	log, _, err := pg.ListAdminAudit(context.Background(), 10, "")
	if err != nil || len(log) == 0 || log[0].Action != "studio.dry_run" || log[0].Actor != "admin" || !bytes.Contains(log[0].Diff, []byte(`"cost_usd"`)) {
		t.Errorf("audit log: %+v %v", log, err)
	}
}

// ------------------------------------------------------------------------------- #34 draft

// draftFixture is the evaluator draft of contracts/fixtures/api/StudioDraftResponse.ok.json.
func draftFixture(t *testing.T) string {
	t.Helper()
	b, err := contracts.FS.ReadFile("fixtures/api/StudioDraftResponse.ok.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Draft json.RawMessage `json:"draft"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return string(f.Draft)
}

func draftEngine(t *testing.T, fn func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error)) *enginetest.Engine {
	t.Helper()
	return enginetest.Start(t, &enginetest.Engine{DraftEvaluatorFunc: fn})
}

func TestStudioDraft(t *testing.T) {
	spec := draftFixture(t)
	var got *pb.DraftEvaluatorRequest
	var mu sync.Mutex
	eng := draftEngine(t, func(_ context.Context, req *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
		mu.Lock()
		got = req
		mu.Unlock()
		return &pb.DraftEvaluatorResponse{SpecJson: spec, Model: "mock/judge", Attempts: 1,
			Cost: &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "mock", Model: "mock/judge", CostUsd: 0.0049, CostSource: "simulated"}}, nil
	})
	env := newTestEnv(t, envOpts{adminKey: studioAdminKey, engine: eng})
	code, body := env.do(http.MethodPost, "/api/studio/draft", studioAdminKey, []byte(`{"description":"Catch answers that blame the customer.","route":"support-bot"}`))
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioDraft, body)
	var resp studioDraftResponse
	_ = json.Unmarshal(body, &resp)
	if string(resp.Draft) == "null" || len(resp.Errors) != 0 || resp.CostSource != "simulated" || resp.CostUSD != 0.0049 ||
		resp.Model != "mock/judge" || resp.Attempts != 1 {
		t.Errorf("response = %s", body)
	}
	mu.Lock()
	if got.GetRouteName() != "support-bot" || got.GetRouteDescription() == "" || !strings.Contains(strings.Join(got.GetExistingIds(), ","), "groundedness") ||
		got.GetDescription() != "Catch answers that blame the customer." {
		t.Errorf("engine request = %+v", got)
	}
	mu.Unlock()
	// Never saved or attached.
	if n := len(env.srv.policies.Registry.Evaluators(true)); n != len(mustBuiltins(t).Evaluators(true)) {
		t.Errorf("the registry grew to %d evaluators", n)
	}
}

func mustBuiltins(t *testing.T) *evaluators.Registry {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestStudioDraftStillInvalidAfterTheRepair(t *testing.T) {
	r := newStudioRig(t, func(r *studioRig) {
		r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
			return &pb.DraftEvaluatorResponse{SpecJson: `{"name":"Blames","question":{"type":"noul"}}`, Model: "anthropic/claude-haiku-4.5",
				Attempts: 2, SchemaErrors: []string{"outcomes: required"},
				Cost: &pb.CostRecord{Component: "studio_draft", Kind: "llm", CostUsd: 0.006, CostSource: "reported"}}, nil
		})
	})
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaStudioDraft, body)
	var resp studioDraftResponse
	_ = json.Unmarshal(body, &resp)
	if string(resp.Draft) != "null" || len(resp.Errors) == 0 || resp.Attempts != 2 || resp.CostSource != "reported" {
		t.Errorf("response = %s", body)
	}
}

func TestStudioDraftRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"no judge", status.Error(codes.FailedPrecondition, "no_judge"), codeNoJudge},
		{"unreachable", status.Error(codes.Unavailable, "connection refused"), codeEngineUnavailable},
		{"too slow", status.Error(codes.DeadlineExceeded, "deadline"), codeEngineUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var settled []float64
			r := newStudioRig(t, func(r *studioRig) {
				r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
					return nil, c.err
				})
				r.api.costs.reserveLLM = func(_ context.Context, _ float64, component string) (func(float64), error) {
					if component != componentStudioDraft {
						t.Errorf("component %s", component)
					}
					return func(v float64) { settled = append(settled, v) }, nil
				}
			})
			code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`)
			if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != c.code {
				t.Errorf("HTTP %d %s", code, body)
			}
			if len(settled) != 1 || (c.name == "too slow") != (settled[0] > 0) {
				t.Errorf("settled %v", settled)
			}
		})
	}
	// The LLM budget is used up: nothing is sent.
	called := false
	r := newStudioRig(t, func(r *studioRig) {
		r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
			called = true
			return nil, errors.New("unreachable")
		})
		r.api.costs.reserveLLM = func(context.Context, float64, string) (func(float64), error) { return nil, errors.New("cap") }
	})
	if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeBudgetExhausted || called {
		t.Errorf("budget: %d %s (called %v)", code, body, called)
	}
	// Validation and no engine.
	for _, b := range []string{`{"description":"   "}`, `{"description":"` + strings.Repeat("x", 2001) + `"}`, `{"description":"x","route":"nope"}`} {
		if code, _ := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", b); code != http.StatusUnprocessableEntity {
			t.Errorf("%.40s: %d", b, code)
		}
	}
	noEngine := newStudioRig(t, func(r *studioRig) { r.api.drafter = nil })
	if code, body := studioDo(t, noEngine.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeEngineUnavailable {
		t.Errorf("no engine: %d %s", code, body)
	}
}

// An engine error can quote the judge provider's answer, which may echo the key it was sent: the
// draft's 503 masks every secret the gateway knows (by value and by shape) before it cuts the text.
func TestStudioDraftMasksSecretsInEngineErrors(t *testing.T) {
	const shaped = "sk-or-v1-e2e-0123456789abcdef0123"
	if config.NewRedactor().Redact(dmUnmatchedSecret) != dmUnmatchedSecret {
		t.Fatal("the unmatched secret matches a redaction pattern")
	}
	redactor := config.NewRedactor(dmUnmatchedSecret)
	// The unmatched secret straddles the 300-character cut: masked first, no prefix of it is left.
	msg := "judge refused: " + strings.Repeat("x", maxEngineText-30) + " key " + dmUnmatchedSecret + " and " + shaped
	r := newStudioRig(t, func(r *studioRig) {
		r.api.redact = redactor.Redact
		r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
			return nil, status.Error(codes.Internal, msg)
		})
	})
	code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`)
	if code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codeEngineUnavailable {
		t.Errorf("HTTP %d", code)
	}
	for _, leak := range []string{dmUnmatchedSecret, dmUnmatchedSecret[:8], shaped, "sk-or-v1-e2e"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the answer carries a secret or its prefix (%d characters)", len(leak))
		}
	}
	var e apiErrorBody
	if err := json.Unmarshal(body, &e); err != nil || !strings.Contains(e.Error.Message, "judge refused") ||
		utf8.RuneCountInString(e.Error.Message) > len("The audit engine could not draft: ")+maxEngineText+1 {
		t.Errorf("message of %d characters (err %v)", utf8.RuneCountInString(e.Error.Message), err)
	}
}

func TestStudioDraftReservesAndSettlesTheRealCost(t *testing.T) {
	var reserved float64
	var settled []float64
	var recorded []*pb.CostRecord
	r := newStudioRig(t, func(r *studioRig) {
		r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
			return &pb.DraftEvaluatorResponse{SpecJson: draftFixture(t), Model: "anthropic/claude-haiku-4.5", Attempts: 1,
				Cost: &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "openrouter", CostUsd: 0.0042, CostSource: "reported"}}, nil
		})
		r.api.costs.reserveLLM = func(_ context.Context, usd float64, _ string) (func(float64), error) {
			reserved = usd
			return func(v float64) { settled = append(settled, v) }, nil
		}
		r.api.costs.recordLLM = func(_ context.Context, recs []*pb.CostRecord, link CostLink) { recorded = recs }
	})
	if code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`); code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	// The reservation is the prompt at $1/M in and 1,600 tokens at $5/M out: just over $0.008.
	if reserved < 0.008 || reserved > 0.0115 {
		t.Errorf("reserved %v", reserved)
	}
	if len(settled) != 1 || settled[0] != 0.0042 || len(recorded) != 1 || recorded[0].GetCostUsd() != 0.0042 {
		t.Errorf("settled %v recorded %v", settled, recorded)
	}
}

// A draft cost no price covers counts at the reference judge's price for the cap (D91); one the
// engine did not report keeps the reservation; a simulated one counts $0.
func TestStudioDraftSettlesTheCapCost(t *testing.T) {
	cases := []struct {
		name    string
		cost    *pb.CostRecord
		noRef   bool
		settled func(reserved float64) float64
	}{
		{name: "unknown at the reference price",
			cost:    &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "acme", InputTokens: 2000, OutputTokens: 400, CostSource: "unknown"},
			settled: func(float64) float64 { return (2000*1 + 400*5) / 1e6 }},
		{name: "unknown without a reference price keeps the reservation", noRef: true,
			cost:    &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "acme", InputTokens: 2000, OutputTokens: 400, CostSource: "unknown"},
			settled: func(reserved float64) float64 { return reserved }},
		{name: "no record keeps the reservation", settled: func(reserved float64) float64 { return reserved }},
		{name: "simulated counts nothing",
			cost:    &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "mock", InputTokens: 2000, OutputTokens: 400, CostUsd: 0.003, CostSource: "simulated"},
			settled: func(float64) float64 { return 0 }},
		{name: "a negative reported cost counts nothing",
			cost:    &pb.CostRecord{Component: "studio_draft", Kind: "llm", Provider: "openrouter", CostUsd: -1, CostSource: "reported"},
			settled: func(float64) float64 { return 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reserved float64
			var settled []float64
			r := newStudioRig(t, func(r *studioRig) {
				r.api.drafter = fakeDrafter(func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
					return &pb.DraftEvaluatorResponse{SpecJson: draftFixture(t), Model: "acme/judge-1", Attempts: 1, Cost: tc.cost}, nil
				})
				r.api.costs.reserveLLM = func(_ context.Context, usd float64, _ string) (func(float64), error) {
					reserved = usd
					return func(v float64) { settled = append(settled, v) }, nil
				}
				if tc.noRef {
					r.api.referencePrice = func() (decision.PriceEntry, bool) { return decision.PriceEntry{}, false }
				}
			})
			code, body := studioDo(t, r.url, http.MethodPost, "/api/studio/draft", "", `{"description":"Catch blame."}`)
			if code != http.StatusOK {
				t.Fatalf("HTTP %d %s", code, body)
			}
			want := tc.settled(reserved)
			if reserved <= 0 || len(settled) != 1 || math.Abs(settled[0]-want) > 1e-12 {
				t.Errorf("reserved %v settled %v, want %v", reserved, settled, want)
			}
			var resp studioDraftResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatal(err)
			}
			if wantSource := tc.cost.GetCostSource(); (wantSource == "" && resp.CostSource != decision.CostUnknown) || (wantSource != "" && resp.CostSource != wantSource) || resp.CostUSD < 0 {
				t.Errorf("response cost %v %q", resp.CostUSD, resp.CostSource)
			}
		})
	}
}

func TestStudioDraftLimit(t *testing.T) {
	eng := draftEngine(t, func(context.Context, *pb.DraftEvaluatorRequest) (*pb.DraftEvaluatorResponse, error) {
		return nil, status.Error(codes.FailedPrecondition, "no_judge")
	})
	env := newTestEnv(t, envOpts{adminKey: studioAdminKey, engine: eng})
	for i := range studioDraftLimit {
		if code, b := env.do(http.MethodPost, "/api/studio/draft", studioAdminKey, []byte(`{"description":"Catch blame."}`)); code != http.StatusServiceUnavailable {
			t.Fatalf("draft %d: %d %s", i+1, code, b)
		}
	}
	if code, b := env.do(http.MethodPost, "/api/studio/draft", studioAdminKey, []byte(`{"description":"Catch blame."}`)); code != http.StatusTooManyRequests {
		t.Errorf("draft %d: %d %s", studioDraftLimit+1, code, b)
	}
}
