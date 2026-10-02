package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

const schemaDecisionRequest = "api.v1.schema.json#/$defs/DecisionRequestView"

// recordedHashes are the request hashes and cpt the gate recorded for a trace's attempt.
func recordedHashes(t *testing.T, pg *store.PostgresStore, traceID string, attempt int) ([]string, float64) {
	t.Helper()
	docs, err := pg.TraceDecisions(context.Background(), traceID)
	if err != nil || len(docs) <= attempt {
		t.Fatalf("decisions of %s: %d, err %v", traceID, len(docs), err)
	}
	var d struct {
		Attempt int `json:"attempt"`
		Compile struct {
			CPT           float64  `json:"cpt"`
			RequestSHA256 []string `json:"request_sha256"`
		} `json:"compile"`
	}
	if err := json.Unmarshal(docs[attempt], &d); err != nil || d.Attempt != attempt {
		t.Fatalf("decision %d: %v", attempt, err)
	}
	return d.Compile.RequestSHA256, d.Compile.CPT
}

func getDecisionRequest(t *testing.T, env *testEnv, traceID, query, key string) (int, decisionRequestView, []byte) {
	t.Helper()
	code, body := env.get("/api/traces/"+traceID+"/decision-request"+query, key)
	var v decisionRequestView
	if code == http.StatusOK {
		contractstest.ValidateJSON(t, schemaDecisionRequest, body)
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
	}
	return code, v, body
}

func TestDecisionRequestRoundTripMatchesTheGate(t *testing.T) {
	env, pg := pgEnv(t, envOpts{})
	scenarios := loadDemoScenarios(t)
	for _, id := range []string{"grounded-pass", "refund-contradiction-flag", "tool-results-grounded", "line-header-context-grounded"} {
		traceID := env.send(scenarios[id]).Header.Get("X-TrustAgent-Trace-Id")
		env.settledTrace(traceID)
		want, cpt := recordedHashes(t, pg, traceID, 0)
		code, v, body := getDecisionRequest(t, env, traceID, "", testKey)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", id, code, body)
		}
		if !v.MatchesRecorded || v.MismatchReason != nil || !slices.Equal(v.RequestSHA256, want) || len(want) == 0 || v.CPT != cpt {
			t.Errorf("%s: view %s, recorded %v cpt %v", id, body, want, cpt)
		}
		if len(v.Requests) != len(want) || v.Requests[0].Model == "" || len(v.Requests[0].Questions) == 0 {
			t.Errorf("%s: requests %+v", id, v.Requests)
		}
		if strings.Contains(string(body), "session_id") {
			t.Errorf("%s: the view carries a session_id", id)
		}
	}
}

func TestDecisionRequestFallbackAttempt(t *testing.T) {
	env, pg := pgEnv(t, envOpts{mutate: func(cfg *config.Config) { cfg.CalibrationDir = config.RepoPath("config/calibration") }})
	traceID := env.send(loadDemoScenarios(t)["payments-fallback"]).Header.Get("X-TrustAgent-Trace-Id")
	env.settledTrace(traceID)
	want, _ := recordedHashes(t, pg, traceID, 1)
	code, v, body := getDecisionRequest(t, env, traceID, "?attempt=1", testKey)
	if code != http.StatusOK || !v.MatchesRecorded || !slices.Equal(v.RequestSHA256, want) {
		t.Fatalf("attempt 1: %d %s (recorded %v)", code, body, want)
	}
	_, first, _ := getDecisionRequest(t, env, traceID, "", testKey)
	a0, _ := first.Requests[0].State["assistant_response"].(string)
	a1, _ := v.Requests[0].State["assistant_response"].(string)
	if a0 == "" || a1 == "" || a0 == a1 {
		t.Errorf("attempt 1 must compile the fallback answer: %q vs %q", a0, a1)
	}
	if code, _, _ := getDecisionRequest(t, env, traceID, "?attempt=2", testKey); code != http.StatusBadRequest {
		t.Errorf("attempt 2: %d", code)
	}
}

func TestDecisionRequestTruncatedBody(t *testing.T) {
	env, _ := pgEnv(t, envOpts{mutate: func(cfg *config.Config) { cfg.BodyMaxBytes = 80 }})
	traceID := env.send(loadDemoScenarios(t)["grounded-pass"]).Header.Get("X-TrustAgent-Trace-Id")
	env.settledTrace(traceID)
	code, v, body := getDecisionRequest(t, env, traceID, "", testKey)
	if code != http.StatusOK || v.MatchesRecorded || v.MismatchReason == nil || *v.MismatchReason != mismatchBodyTruncated {
		t.Errorf("truncated: %d %s", code, body)
	}
}

func TestDecisionRequestExpiredBodyAndScope(t *testing.T) {
	env, schema := pgSchemaEnv(t, envOpts{adminKey: studioAdminKey})
	traceID := env.send(loadDemoScenarios(t)["grounded-pass"]).Header.Get("X-TrustAgent-Trace-Id")
	env.settledTrace(traceID)
	if code, _, body := getDecisionRequest(t, env, traceID, "", testKey); code != http.StatusOK {
		t.Fatalf("live body: %d %s", code, body)
	}
	if code, body := env.get("/api/traces/"+traceID+"/decision-request", ""); code != http.StatusUnauthorized {
		t.Errorf("no key: %d %s", code, body)
	}
	if code, body := env.get("/api/traces/no-such-trace/decision-request", testKey); code != http.StatusNotFound || errorCodeOf(t, body) != codeNotFound {
		t.Errorf("unknown trace: %d %s", code, body)
	}
	if _, err := schema.Pool(t).Exec(context.Background(), `UPDATE trace_bodies SET expires_at = now() - interval '1 minute' WHERE trace_id = $1`, traceID); err != nil {
		t.Fatal(err)
	}
	if code, body := env.get("/api/traces/"+traceID+"/decision-request", testKey); code != http.StatusConflict || errorCodeOf(t, body) != codeBodyExpired {
		t.Errorf("expired body: %d %s", code, body)
	}

	// A key with read but not content sees the trace list, never the request bodies.
	code, created := env.do(http.MethodPost, "/api/keys", studioAdminKey, []byte(`{"label":"dashboard-reader","scopes":["read"]}`))
	var key struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created, &key); code != http.StatusCreated || err != nil || key.Secret == "" {
		t.Fatalf("read key: %d", code)
	}
	if code, _ := env.get("/api/traces/"+traceID, key.Secret); code != http.StatusOK {
		t.Errorf("the read key cannot read the trace: %d", code)
	}
	if code, body := env.get("/api/traces/"+traceID+"/decision-request", key.Secret); code != http.StatusForbidden || errorCodeOf(t, body) != codeForbidden {
		t.Errorf("read scope: %d %s", code, body)
	}
	if code, body := newTestEnv(t, envOpts{}).get("/api/traces/"+traceID+"/decision-request", testKey); code != http.StatusServiceUnavailable || errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("memory store: %d %s", code, body)
	}
}

// decisionRig serves the decision request over the fake store with a swappable active policy and
// stored policy version.
type decisionReqRig struct {
	*studioRig
	pol     *policy.Policy
	active  atomic.Pointer[policy.Policy]
	stored  atomic.Pointer[policy.Version]
	barrier atomic.Int64
	// onBarrier, when set, runs inside the barrier (a func(string) error).
	onBarrier atomic.Value
}

func newDecisionReqRig(t *testing.T, resolver policy.Resolver) *decisionReqRig {
	t.Helper()
	d := &decisionReqRig{}
	d.studioRig = newStudioRig(t, func(r *studioRig) {
		d.pol = r.api.policy()
		d.active.Store(d.pol)
		r.api.policy = func() *policy.Policy { return d.active.Load() }
		r.api.policyVersion = func(_ context.Context, id int64) (*policy.Version, error) {
			if v := d.stored.Load(); v != nil && v.ID == id {
				return v, nil
			}
			return nil, policy.ErrVersionNotFound
		}
		if resolver != nil {
			r.api.policyEnv = func() policy.Env { return policy.Env{Seeds: policy.DefaultSeeds(), Specs: resolver} }
		}
		r.api.barrier = func(_ context.Context, id string) error {
			d.barrier.Add(1)
			if fn, ok := d.onBarrier.Load().(func(string) error); ok {
				return fn(id)
			}
			return nil
		}
		answer, req := "Annual plans can be refunded within 30 days.", "How long is the refund window?"
		r.db.bodies["t1"] = &store.BodyRecord{TraceID: "t1", ResponseText: &answer, UserRequest: &req, Rules: map[string]string{},
			Evidence:  json.RawMessage(`[{"id":"refund.md","source":"request_system","label":"refund.md","text":"` + studioDoc + `"}]`),
			ExpiresAt: time.Now().Add(time.Hour)}
	})
	return d
}

func (d *decisionReqRig) record(rc *store.RecordedCompile) {
	d.db.mu.Lock()
	defer d.db.mu.Unlock()
	d.db.compiles["t1/0"] = rc
}

func (d *decisionReqRig) view(t *testing.T) decisionRequestView {
	t.Helper()
	code, body := studioDo(t, d.url, http.MethodGet, "/api/traces/t1/decision-request", "", nil)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaDecisionRequest, body)
	var v decisionRequestView
	_ = json.Unmarshal(body, &v)
	return v
}

func TestDecisionRequestLoadsTheRecordedPolicyVersion(t *testing.T) {
	d := newDecisionReqRig(t, nil)
	model, cpt := "typesafe/jev-1.13", 3.61
	version := d.pol.Version
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version, ModelRequested: &model, CPT: &cpt})
	first := d.view(t)
	if first.MatchesRecorded || first.MismatchReason != nil || len(first.RequestSHA256) != 1 || first.CPT != cpt {
		t.Fatalf("nothing recorded: %+v", first)
	}
	if first.Requests[0].Model != model {
		t.Errorf("model = %s", first.Requests[0].Model)
	}
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version, ModelRequested: &model, CPT: &cpt, RequestSHA256: first.RequestSHA256})
	if v := d.view(t); !v.MatchesRecorded {
		t.Errorf("same inputs: %+v", v)
	}
	// Another model is another request.
	otherModel := "typesafe/jev-2"
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version, ModelRequested: &otherModel, CPT: &cpt, RequestSHA256: first.RequestSHA256})
	if v := d.view(t); v.MatchesRecorded || v.MismatchReason != nil || v.Requests[0].Model != otherModel {
		t.Errorf("model %s: %+v", otherModel, v)
	}
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version, ModelRequested: &model, CPT: &cpt, RequestSHA256: first.RequestSHA256})

	// A newer policy is active now: the recorded version is loaded from storage.
	newer := *d.pol
	newer.Version = version + 5
	d.active.Store(&newer)
	if v := d.view(t); v.MismatchReason == nil || *v.MismatchReason != mismatchPolicyMissing {
		t.Errorf("version gone: %+v", v)
	}
	d.stored.Store(&policy.Version{VersionMeta: policy.VersionMeta{ID: version}, YAML: d.pol.YAML})
	if v := d.view(t); !v.MatchesRecorded || v.MismatchReason != nil {
		t.Errorf("stored version: %+v", v)
	}
	if d.barrier.Load() == 0 {
		t.Error("the barrier never ran")
	}
	// A route the version does not have.
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "gone-route", PolicyVersion: &version, RequestSHA256: first.RequestSHA256})
	if v := d.view(t); v.MismatchReason == nil || *v.MismatchReason != mismatchPolicyMissing || v.MatchesRecorded {
		t.Errorf("route gone: %+v", v)
	}
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", RequestSHA256: first.RequestSHA256})
	if v := d.view(t); v.MismatchReason == nil || *v.MismatchReason != mismatchPolicyMissing {
		t.Errorf("no version recorded: %+v", v)
	}
}

// specsWith resolves the built-ins plus extra.
type specsWith struct {
	reg   *evaluators.Registry
	extra *decision.Spec
}

func (s specsWith) Get(ref string) (*decision.Spec, bool) {
	if ref == s.extra.Ref() {
		return s.extra, true
	}
	return s.reg.Get(ref)
}

func TestDecisionRequestEvaluatorMissing(t *testing.T) {
	gone, err := decision.ParseSpec(forkedSpecDoc(t, "gone_check"))
	if err != nil {
		t.Fatal(err)
	}
	d := newDecisionReqRig(t, specsWith{reg: mustBuiltins(t), extra: gone})
	version := d.pol.Version + 7
	// The version support-bot was gated with ran an evaluator the registry no longer has.
	d.stored.Store(&policy.Version{VersionMeta: policy.VersionMeta{ID: version}, YAML: `version: 1
routes:
  - name: support-bot
    match: {headers: {X-TrustAgent-Route: support-bot}}
    evaluators:
      - ref: gone_check@1
      - ref: contradiction@1
`})
	if p := d.api.recordedPolicy(context.Background(), version); p == nil {
		t.Fatal("the stored version does not load")
	} else if r, _ := p.Route("support-bot"); r == nil || r.Evaluators[0].Ref != "gone_check@1" {
		t.Fatalf("support-bot = %+v", r)
	}
	d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version, RequestSHA256: []string{"x"}})
	if v := d.view(t); v.MismatchReason == nil || *v.MismatchReason != mismatchEvaluatorMissing || v.MatchesRecorded {
		t.Errorf("evaluator gone: %+v", v)
	}
}

func TestDecisionRequestWaitsForThePendingTrace(t *testing.T) {
	// The trace's decision is still queued in the async writer: the barrier writes it, and only
	// then is it read (D83).
	d := newDecisionReqRig(t, nil)
	version := d.pol.Version
	d.onBarrier.Store(func(id string) error {
		if id == "t1" {
			d.record(&store.RecordedCompile{TraceID: "t1", Route: "support-bot", PolicyVersion: &version})
		}
		return nil
	})
	if v := d.view(t); v.MismatchReason != nil || len(v.RequestSHA256) != 1 {
		t.Errorf("pending trace: %+v", v)
	}
	d.onBarrier.Store(func(string) error { return errors.New("database unreachable") })
	if code, body := studioDo(t, d.url, http.MethodGet, "/api/traces/t1/decision-request", "", nil); code != http.StatusInternalServerError || errorCodeOf(t, body) != codeInternal {
		t.Errorf("barrier failed: %d %s", code, body)
	}
}
