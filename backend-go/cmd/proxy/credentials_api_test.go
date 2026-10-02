package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
	"github.com/truthtable/backend-go/internal/store"
)

const (
	schemaCredentialList        = "api.v1.schema.json#/$defs/CredentialList"
	schemaCredentialWriteResult = "api.v1.schema.json#/$defs/CredentialWriteResult"
	schemaWsEventCred           = "api.v1.schema.json#/$defs/WsEvent"
	credAdminKey                = "cred-test-admin-key-0001"

	// Fake keys (P4-PLAN §0 rule 8). mock-upstream's key policy: a key containing "forbidden" is
	// refused (403), "nocredits" has no credits (402); any other key works.
	credGoodKey      = "sk-or-v1-e2e-good-6b1f0c9a2d4e7f3a"
	credOtherGoodKey = "sk-or-v1-e2e-good-91d2c4e6f8a0b3c5"
	credForbiddenKey = "sk-or-v1-e2e-forbidden-3c5e7a9b1d2f"
	credNoCreditsKey = "sk-or-v1-e2e-nocredits-8f6d4b2a0c1e"
)

// credRig is a gateway on a Postgres schema of its own, with a durable master key (so the
// credential store is ready), an admin key, and mock-upstream standing in for OpenRouter: nothing
// reaches a vendor.
type credRig struct {
	t    *testing.T
	env  *testEnv
	pg   *store.PostgresStore
	mu   *mockupstream.Server
	base string
	logs *bytes.Buffer
	// bodies collects every response body the rig received (leak checks).
	bodies [][]byte
	// decisionAuth holds the Authorization header of every decision call mock-upstream received.
	decisionAuth *authLog
}

type credRigOpts struct {
	engine *enginetest.Engine
	mutate func(*config.Config)
	// ephemeral leaves the master key ephemeral (store unavailable, D32).
	ephemeral bool
	// pg, when set, is the rig's Postgres store (default: a fresh schema of its own).
	pg *store.PostgresStore
}

func newCredRig(t *testing.T, o credRigOpts) *credRig {
	t.Helper()
	pg := o.pg
	if pg == nil {
		pg = runtimePG(t)
	}
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r := &credRig{t: t, mu: mu, decisionAuth: &authLog{}}
	// mock-upstream redacts the keys it records; the rig notes which key each decision call
	// carried (kept in memory, compared, never printed).
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/alpha/decisions" {
			r.decisionAuth.add(req.Header.Get("Authorization"))
		}
		mu.ServeHTTP(w, req)
	}))
	t.Cleanup(hs.Close)
	var logs bytes.Buffer
	opts := envOpts{pg: pg, adminKey: credAdminKey, logs: &logs, engine: o.engine, mutate: func(c *config.Config) {
		c.Jev.OpenRouterBaseURL, c.Jev.KeepAlive = hs.URL, 0
		// No env decision key unless the test sets one: the dashboard credential decides.
		c.Jev.OpenRouterKey, c.Jev.TypeSafeKey, c.Jev.APIKey = "", "", ""
		c.AllowedOrigins = append(slices.Clone(c.AllowedOrigins), "http://127.0.0.1:3000")
		if o.mutate != nil {
			o.mutate(c)
		}
	}}
	if !o.ephemeral {
		opts.master = durableMaster(t, 37)
	}
	r.env, r.pg, r.base, r.logs = newTestEnv(t, opts), pg, hs.URL, &logs
	return r
}

// authLog records Authorization headers in arrival order.
type authLog struct {
	mu   sync.Mutex
	seen []string
}

func (a *authLog) add(h string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, h)
}

func (a *authLog) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.seen)
}

// call sends method path with the admin key (key "" sends none) and an optional JSON body and
// extra headers (name, value pairs); it returns the status and the body.
func (r *credRig) call(method, path, key string, body any, headers ...string) (int, []byte) {
	r.t.Helper()
	resp, b := r.do(method, path, key, body, headers...)
	return resp.StatusCode, b
}

func (r *credRig) do(method, path, key string, body any, headers ...string) (*http.Response, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			r.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, r.env.http.URL+path, rd)
	if key != "" {
		req.Header.Set("X-TrustAgent-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r.bodies = append(r.bodies, b)
	return resp, b
}

// put is PUT /api/credentials/:name with body, answered 200 with a valid CredentialWriteResult.
func (r *credRig) put(name string, body map[string]any) credWriteWire {
	r.t.Helper()
	code, b := r.call(http.MethodPut, "/api/credentials/"+name, credAdminKey, body)
	if code != http.StatusOK {
		r.t.Fatalf("PUT %s: %d %s", name, code, b)
	}
	return decodeWriteResult(r.t, b)
}

// credWriteWire decodes a CredentialWriteResult (or ConnectCompleteResponse) as the web reads it.
type credWriteWire struct {
	Credential credentialView `json:"credential"`
	Stored     bool           `json:"stored"`
	Bindings   struct {
		Applied []string             `json:"applied"`
		Skipped []CredentialBindSkip `json:"skipped"`
	} `json:"bindings"`
	Verification *struct {
		OK         bool     `json:"ok"`
		Status     string   `json:"status"`
		Hint       *string  `json:"hint"`
		LatencyMs  *int64   `json:"latency_ms"`
		CostUSD    *float64 `json:"cost_usd"`
		CostSource *string  `json:"cost_source"`
		KeyLabel   *string  `json:"key_label"`
	} `json:"verification"`
	ReturnTo string `json:"return_to"`
	raw      map[string]json.RawMessage
	body     []byte
}

func decodeWriteResult(t *testing.T, b []byte) credWriteWire {
	t.Helper()
	contractstest.ValidateJSON(t, schemaCredentialWriteResult, b)
	var w credWriteWire
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(b, &w.raw)
	w.body = b
	return w
}

// list is GET /api/credentials, valid against CredentialList, by name.
func (r *credRig) list() map[string]credentialView {
	r.t.Helper()
	code, b := r.call(http.MethodGet, "/api/credentials", credAdminKey, nil)
	if code != http.StatusOK {
		r.t.Fatalf("GET /api/credentials: %d %s", code, b)
	}
	contractstest.ValidateJSON(r.t, schemaCredentialList, b)
	var l struct {
		Items []credentialView `json:"items"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		r.t.Fatal(err)
	}
	out := map[string]credentialView{}
	for _, it := range l.Items {
		out[it.Name] = it
	}
	return out
}

// gated sends a demo scenario through /v1 and returns the response (any status).
func (r *credRig) gated(s *demoScenario) *http.Response {
	r.t.Helper()
	var req map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &req); err != nil {
		r.t.Fatal(err)
	}
	req["test_response"], _ = json.Marshal(s.Response)
	headers := []string{"X-TrustAgent-Scenario", s.ID}
	for k, v := range s.Headers {
		headers = append(headers, k, v)
	}
	resp, _ := r.do(http.MethodPost, "/v1/chat/completions", testKey, req, headers...)
	return resp
}

// decisionCalls returns the Authorization headers of the decision calls mock-upstream received.
func (r *credRig) decisionCalls() []string { return r.decisionAuth.all() }

// auditActions lists the admin audit log's actions, newest first, and the raw page.
func (r *credRig) auditActions() ([]string, []byte) {
	r.t.Helper()
	code, b := r.call(http.MethodGet, "/api/admin/audit-log?limit=50", credAdminKey, nil)
	if code != http.StatusOK {
		r.t.Fatalf("audit log: %d %s", code, b)
	}
	var p adminAuditPage
	if err := json.Unmarshal(b, &p); err != nil {
		r.t.Fatal(err)
	}
	out := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.Action)
	}
	return out, b
}

// wsEvents returns the data of the WS frames of type typ the hub published, each frame checked
// against the WsEvent schema.
func (r *credRig) wsEvents(typ string) []map[string]any {
	r.t.Helper()
	var out []map[string]any
	for _, f := range r.env.wsFrames(r.t, 200) {
		r.bodies = append(r.bodies, f)
		var ev struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(f, &ev) != nil || ev.Type != typ {
			continue
		}
		contractstest.ValidateJSON(r.t, schemaWsEventCred, f)
		out = append(out, ev.Data)
	}
	return out
}

// assertNoLeak fails when secret appears in a response body, a WS frame or the log.
func (r *credRig) assertNoLeak(secret string) {
	r.t.Helper()
	for i, b := range r.bodies {
		if bytes.Contains(b, []byte(secret)) {
			r.t.Errorf("a secret reached response or frame #%d", i)
		}
	}
	if strings.Contains(r.env.logText(), secret) {
		r.t.Error("a secret reached the log")
	}
}

// waitEngineEnv waits until the runtime configuration knows the engine's env-managed keys.
func waitEngineEnv(t *testing.T, m *runtimecfg.Manager, names ...string) {
	t.Helper()
	waitRuntime(t, m, "engine env", func(s *runtimecfg.Snapshot) bool {
		for _, n := range names {
			if !slices.Contains(s.EngineEnv, n) {
				return false
			}
		}
		return true
	})
}

// TestCredentialPutBindsTheFastCheckAndSkipsAnEnvManagedJudge (D75, D76): a pasted OpenRouter key
// is checked with OpenRouter, stored sealed and bound to the fast check in one write; the judge
// is skipped because the engine's env sets it (LLM_PROVIDER); simulated checks turn off; the next
// gated request decides through the new key; the audit log, the WS events and the list say so,
// and the key itself appears nowhere.
func TestCredentialPutBindsTheFastCheckAndSkipsAnEnvManagedJudge(t *testing.T) {
	eng := newReportingEngine(t, "LLM_PROVIDER")
	r := newCredRig(t, credRigOpts{engine: eng.eng})
	m := r.env.srv.rt.manager
	waitEngineEnv(t, m, "LLM_PROVIDER")
	if b := m.Current().Settings.DecisionModel.Backend; b != jev.BackendMock {
		t.Fatalf("the test gateway starts with simulated checks, got backend %q", b)
	}

	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model", "judge"}})
	if !res.Stored || res.Verification == nil || !res.Verification.OK || res.Verification.Status != "ok" {
		t.Fatalf("write result: stored %v verification %+v", res.Stored, res.Verification)
	}
	if !slices.Equal(res.Bindings.Applied, []string{"decision_model"}) ||
		!slices.Equal(res.Bindings.Skipped, []CredentialBindSkip{{Target: "judge", Reason: runtimecfg.SkipEnvManaged}}) {
		t.Errorf("bindings = %+v, want decision_model applied and judge skipped as env_managed", res.Bindings)
	}
	if res.Verification.LatencyMs == nil || res.Verification.KeyLabel == nil || res.Verification.Hint == nil || *res.Verification.Hint != credentials.HintNoLimit {
		t.Errorf("verification details: %+v", res.Verification)
	}
	if _, ok := res.raw["verification"]; !ok || !bytes.Contains(res.raw["verification"], []byte(`"limit_usd":null`)) {
		t.Errorf("an OpenRouter check reports its (missing) spending limit as null: %s", res.raw["verification"])
	}
	// The same fields as the contract fixture of this very case, which the web is built against.
	fixture, err := os.ReadFile(config.RepoPath("contracts/fixtures/api/CredentialWriteResult.judge-env-skipped.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonShape(t, res.body), jsonShape(t, fixture); !slices.Equal(got, want) {
		t.Errorf("response fields %v, fixture fields %v", got, want)
	}
	c := res.Credential
	if c.Source != "dashboard" || c.Masked != config.MaskSecret(credGoodKey) || !strings.HasPrefix(c.Fingerprint, "fp_") ||
		!slices.Equal(c.UsedBy, []string{"decision_model"}) || c.LastVerifyStatus == nil || *c.LastVerifyStatus != "ok" ||
		!slices.Equal(c.AllowedHosts, credentials.VendorOrigins("openrouter", r.base)) || len(c.AllowedHosts) != 2 {
		t.Errorf("credential view: %+v", c)
	}

	snap := m.Current()
	if strval(snap.Settings.Bindings.DecisionModel) != "openrouter" || snap.Settings.Bindings.Judge != nil {
		t.Errorf("bindings in effect: %+v", snap.Settings.Bindings)
	}
	if snap.Settings.DecisionModel.Backend != jev.BackendAuto {
		t.Errorf("D76: binding a key leaves simulated mode, backend = %q", snap.Settings.DecisionModel.Backend)
	}

	// The gateway now decides through OpenRouter (mock-upstream) with the stored key.
	before := len(r.decisionCalls())
	if resp := r.gated(loadDemoScenarios(t)["grounded-pass"]); resp.StatusCode != http.StatusOK {
		t.Fatalf("gated request: HTTP %d", resp.StatusCode)
	}
	calls := r.decisionCalls()
	if len(calls) <= before || calls[len(calls)-1] != "Bearer "+credGoodKey {
		t.Fatalf("the gated request did not decide through the stored key (%d calls)", len(calls)-before)
	}

	if l := r.list(); l["openrouter"].Fingerprint != c.Fingerprint || !slices.Equal(l["openrouter"].UsedBy, []string{"decision_model"}) {
		t.Errorf("list: %+v", l["openrouter"])
	}
	actions, _ := r.auditActions()
	if len(actions) < 2 || actions[0] != "settings.update" || actions[1] != "credential.put" {
		t.Errorf("audit log (newest first) = %v, want settings.update then credential.put", actions)
	}
	changed := r.wsEvents("credentials_changed")
	if len(changed) != 1 || changed[0]["name"] != "openrouter" || changed[0]["action"] != "put" || changed[0]["fingerprint"] != c.Fingerprint {
		t.Errorf("credentials_changed = %v", changed)
	}
	updated := r.wsEvents("settings_updated")
	if len(updated) != 1 || !slices.Equal(anyStrings(updated[0]["keys"]), []string{"bindings.decision_model", "decision_model.backend"}) {
		t.Errorf("settings_updated = %v", updated)
	}
	r.assertNoLeak(credGoodKey)
}

// TestCredentialFailedCheckKeepsTheBoundKey (D75): replacing the key the fast check uses with one
// the provider refuses stores nothing; the old key stays and keeps deciding.
func TestCredentialFailedCheckKeepsTheBoundKey(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	first := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model"}})
	if !first.Stored || !slices.Equal(first.Bindings.Applied, []string{"decision_model"}) {
		t.Fatalf("first write: %+v", first)
	}

	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credForbiddenKey, "bind": []string{"decision_model"}})
	if res.Stored || res.Verification == nil || res.Verification.OK || res.Verification.Status != "unauthorized" {
		t.Fatalf("a refused key on a bound name: stored %v verification %+v", res.Stored, res.Verification)
	}
	if res.Credential.Fingerprint != first.Credential.Fingerprint ||
		!slices.Equal(res.Bindings.Skipped, []CredentialBindSkip{{Target: "decision_model", Reason: runtimecfg.SkipVerificationFailed}}) {
		t.Errorf("the result shows the key in use and the skipped binding: %+v", res)
	}
	if got := r.list()["openrouter"]; got.Fingerprint != first.Credential.Fingerprint || got.Masked != config.MaskSecret(credGoodKey) {
		t.Errorf("the stored key changed: %+v", got)
	}
	before := len(r.decisionCalls())
	if resp := r.gated(loadDemoScenarios(t)["grounded-pass"]); resp.StatusCode != http.StatusOK {
		t.Fatalf("gated request: HTTP %d", resp.StatusCode)
	}
	calls := r.decisionCalls()
	if len(calls) <= before || calls[len(calls)-1] != "Bearer "+credGoodKey {
		t.Error("the old key no longer decides")
	}
	if len(r.wsEvents("credentials_changed")) != 1 {
		t.Error("a write that stored nothing announced a change")
	}
	r.assertNoLeak(credForbiddenKey)
	r.assertNoLeak(credGoodKey)
}

// TestCredentialRotationSwapsTheKeyInUse (D75, CN-1): pasting a new key under the name the fast
// check uses replaces it at once. The write is a rotation (audit credential.rotate, WS rotated),
// the binding stays without a settings change, and the next gated request decides with the new
// key. The spaces and the newline a copy from a terminal adds are not part of the key.
func TestCredentialRotationSwapsTheKeyInUse(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	first := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model"}})
	if !slices.Equal(first.Bindings.Applied, []string{"decision_model"}) || first.Credential.RotatedAt != nil {
		t.Fatalf("first write: %+v", first)
	}
	if resp := r.gated(loadDemoScenarios(t)["grounded-pass"]); resp.StatusCode != http.StatusOK {
		t.Fatalf("gated request: HTTP %d", resp.StatusCode)
	}

	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": "  " + credOtherGoodKey + "\n"})
	if !res.Stored || res.Verification == nil || !res.Verification.OK {
		t.Fatalf("rotation: stored %v verification %+v", res.Stored, res.Verification)
	}
	c := res.Credential
	if c.Masked != config.MaskSecret(credOtherGoodKey) || c.Fingerprint == first.Credential.Fingerprint || c.RotatedAt == nil ||
		!slices.Equal(c.UsedBy, []string{"decision_model"}) {
		t.Errorf("rotated credential: %+v", c)
	}
	before := len(r.decisionCalls())
	if resp := r.gated(loadDemoScenarios(t)["grounded-pass"]); resp.StatusCode != http.StatusOK {
		t.Fatalf("gated request after the rotation: HTTP %d", resp.StatusCode)
	}
	calls := r.decisionCalls()
	if len(calls) <= before || calls[len(calls)-1] != "Bearer "+credOtherGoodKey {
		t.Fatal("the gated request after the rotation did not decide with the new key")
	}
	changed := r.wsEvents("credentials_changed")
	if len(changed) != 2 || changed[0]["action"] != "put" || changed[1]["action"] != "rotated" || changed[1]["fingerprint"] != c.Fingerprint {
		t.Errorf("credentials_changed = %v", changed)
	}
	if updated := r.wsEvents("settings_updated"); len(updated) != 1 {
		t.Errorf("a rotation that kept the binding announced a settings change: %v", updated)
	}
	if actions, _ := r.auditActions(); len(actions) == 0 || actions[0] != "credential.rotate" {
		t.Errorf("audit log (newest first) = %v, want credential.rotate first", actions)
	}
	r.assertNoLeak(credGoodKey)
	r.assertNoLeak(credOtherGoodKey)
}

// TestCredentialNoCreditsIsStoredUnbound (D75): a key without credits on a name nothing uses is
// stored, unbound, with the no_credits hint, so "I added credits — check again" can bind it.
func TestCredentialNoCreditsIsStoredUnbound(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credNoCreditsKey, "bind": []string{"decision_model", "judge"}})
	v := res.Verification
	if !res.Stored || v == nil || v.OK || v.Status != "no_credits" || v.Hint == nil || *v.Hint != credentials.HintNoCredits || v.KeyLabel == nil {
		t.Fatalf("no-credits write: stored %v verification %+v", res.Stored, v)
	}
	want := []CredentialBindSkip{{Target: "decision_model", Reason: runtimecfg.SkipVerificationFailed}, {Target: "judge", Reason: runtimecfg.SkipVerificationFailed}}
	if len(res.Bindings.Applied) != 0 || !slices.Equal(res.Bindings.Skipped, want) {
		t.Errorf("bindings = %+v", res.Bindings)
	}
	if res.Credential.LastVerifyStatus == nil || *res.Credential.LastVerifyStatus != "no_credits" || len(res.Credential.UsedBy) != 0 {
		t.Errorf("credential = %+v", res.Credential)
	}
	snap := r.env.srv.rt.manager.Current()
	if snap.Settings.Bindings.DecisionModel != nil || snap.Settings.DecisionModel.Backend != jev.BackendMock {
		t.Errorf("nothing is bound and simulated checks stay on: %+v %q", snap.Settings.Bindings, snap.Settings.DecisionModel.Backend)
	}
	if _, ok := snap.Credentials["openrouter"]; !ok {
		t.Error("the stored key is not loaded")
	}
}

// TestCredentialVerifyStoredKeyBinds (#57, D75): a key stored without a check is checked again
// and bound when it passes; the outcome is recorded on the row and in the audit log.
func TestCredentialVerifyStoredKeyBinds(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	saved := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "verify": false})
	if !saved.Stored || saved.Verification != nil || saved.Credential.LastVerifyStatus != nil {
		t.Fatalf("verify:false: %+v", saved)
	}
	if v, ok := saved.raw["verification"]; !ok || string(v) != "null" {
		t.Errorf("verification must be null without a check: %s", v)
	}

	code, b := r.call(http.MethodPost, "/api/credentials/openrouter/verify", credAdminKey, map[string]any{"bind": []string{"decision_model"}})
	if code != http.StatusOK {
		t.Fatalf("verify: %d %s", code, b)
	}
	res := decodeWriteResult(t, b)
	if !res.Stored || res.Verification == nil || !res.Verification.OK || !slices.Equal(res.Bindings.Applied, []string{"decision_model"}) {
		t.Fatalf("verify-and-bind: %+v", res)
	}
	if res.Credential.LastVerifyStatus == nil || *res.Credential.LastVerifyStatus != "ok" || !slices.Equal(res.Credential.UsedBy, []string{"decision_model"}) {
		t.Errorf("credential after the check: %+v", res.Credential)
	}
	if got := strval(r.env.srv.rt.manager.Current().Settings.Bindings.DecisionModel); got != "openrouter" {
		t.Errorf("binding in effect = %q", got)
	}
	actions, _ := r.auditActions()
	if len(actions) < 3 || actions[0] != "settings.update" || actions[1] != "credential.verify" || actions[2] != "credential.put" {
		t.Errorf("audit log = %v", actions)
	}
	r.assertNoLeak(credGoodKey)
}

// TestCredentialDeleteOfTheFastCheckKeyNeedsConfirmation (D36, D77): removing the key the fast
// check uses while a route fails closed needs ?confirm=true; afterwards the binding is gone and
// that route blocks.
func TestCredentialDeleteOfTheFastCheckKeyNeedsConfirmation(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	put := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model"}})
	if !slices.Equal(put.Bindings.Applied, []string{"decision_model"}) {
		t.Fatalf("bind: %+v", put.Bindings)
	}

	code, b := r.call(http.MethodDelete, "/api/credentials/openrouter", credAdminKey, nil)
	if code != http.StatusConflict || errorCodeOf(t, b) != codeConfirmationRequired {
		t.Fatalf("delete without confirm: %d %s", code, b)
	}
	var e struct {
		Error struct {
			Details struct {
				Routes []string `json:"routes"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	if !slices.Equal(e.Error.Details.Routes, []string{"payments-faq"}) {
		t.Errorf("details.routes = %v, want the fail-closed payments-faq", e.Error.Details.Routes)
	}
	if _, ok := r.list()["openrouter"]; !ok {
		t.Fatal("a refused delete removed the key")
	}

	if code, b := r.call(http.MethodDelete, "/api/credentials/openrouter?confirm=true", credAdminKey, nil); code != http.StatusNoContent {
		t.Fatalf("delete with confirm: %d %s", code, b)
	}
	snap := r.env.srv.rt.manager.Current()
	if _, still := snap.Credentials["openrouter"]; still || snap.Settings.Bindings.DecisionModel != nil {
		t.Fatalf("after the delete: credential kept %v, binding %v", still, snap.Settings.Bindings.DecisionModel)
	}
	if _, ok := r.list()["openrouter"]; ok {
		t.Error("the list still shows the deleted key")
	}
	if code, b := r.call(http.MethodDelete, "/api/credentials/openrouter?confirm=true", credAdminKey, nil); code != http.StatusNotFound {
		t.Errorf("a second delete: %d %s", code, b)
	}

	resp := r.gated(loadDemoScenarios(t)["payments-fallback"])
	if got := resp.Header.Get("X-TrustAgent-Action"); got != "block" {
		t.Errorf("the fail-closed route without a fast check: action %q (HTTP %d), want block", got, resp.StatusCode)
	}

	changed := r.wsEvents("credentials_changed")
	if len(changed) != 2 || changed[1]["action"] != "delete" || changed[1]["fingerprint"] != put.Credential.Fingerprint {
		t.Errorf("credentials_changed = %v", changed)
	}
	updated := r.wsEvents("settings_updated")
	if len(updated) != 2 || !slices.Equal(anyStrings(updated[1]["keys"]), []string{"bindings.decision_model"}) {
		t.Errorf("settings_updated = %v", updated)
	}
	actions, _ := r.auditActions()
	if len(actions) < 2 || actions[0] != "settings.update" || actions[1] != "credential.delete" {
		t.Errorf("audit log = %v", actions)
	}
}

// TestCredentialEnvRowsAreReadOnly (D29, D80): keys from the gateway's env and the engine's env
// are listed and win over the dashboard; writing them is 409 env_managed. A gateway env key can
// be checked (the outcome is kept in memory); the engine's are checked with make doctor.
func TestCredentialEnvRowsAreReadOnly(t *testing.T) {
	const envKey = "sk-or-v1-e2e-envkey-4a6c8e0b2d1f"
	const typesafeEnvKey = "sk-test-typesafe-env-5b7d9f1a3c"
	eng := newReportingEngine(t, "ANTHROPIC_API_KEY")
	r := newCredRig(t, credRigOpts{engine: eng.eng, mutate: func(c *config.Config) {
		c.Jev.OpenRouterKey, c.Jev.TypeSafeKey = envKey, typesafeEnvKey
	}})
	waitEngineEnv(t, r.env.srv.rt.manager, "ANTHROPIC_API_KEY")

	l := r.list()
	or, an := l["openrouter"], l["anthropic"]
	if or.Source != "env" || or.Masked != config.MaskSecret(envKey) || !strings.HasPrefix(or.Fingerprint, "fp_") || or.CreatedAt != nil {
		t.Errorf("gateway env row: %+v", or)
	}
	if an.Source != "env" || an.Masked != "" || an.Fingerprint != "" || !slices.Equal(an.AllowedHosts, []string{credentials.OriginAnthropic}) {
		t.Errorf("engine env row (D80): %+v", an)
	}
	if _, ok := l["openai"]; ok {
		t.Error("an unset credential is listed")
	}

	refusals := []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey}},
		{http.MethodPut, "/api/credentials/anthropic", map[string]any{"provider": "anthropic", "secret": "sk-ant-e2e-0123456789"}},
		{http.MethodDelete, "/api/credentials/openrouter", nil},
		{http.MethodDelete, "/api/credentials/anthropic", nil},
		{http.MethodPost, "/api/credentials/typesafe/verify", map[string]any{"bind": []string{"decision_model"}}},
		{http.MethodPost, "/api/credentials/anthropic/verify", nil},
	}
	for _, x := range refusals {
		code, b := r.call(x.method, x.path, credAdminKey, x.body)
		if code != http.StatusConflict || errorCodeOf(t, b) != codeEnvManaged {
			t.Errorf("%s %s: %d %s, want 409 env_managed", x.method, x.path, code, b)
			continue
		}
		if !bytes.Contains(b, []byte(`"keys":[`)) {
			t.Errorf("%s %s: the 409 names no env var: %s", x.method, x.path, b)
		}
	}

	// A gateway env key can be checked; the outcome shows in the list (kept in memory, D80).
	code, b := r.call(http.MethodPost, "/api/credentials/openrouter/verify", credAdminKey, nil)
	if code != http.StatusOK {
		t.Fatalf("verify the env key: %d %s", code, b)
	}
	res := decodeWriteResult(t, b)
	// stored is false only when a failed check kept the key in use (api.ts); checking the env key
	// replaces nothing, so the dashboard must not say "nothing was saved".
	if !res.Stored || res.Verification == nil || !res.Verification.OK || res.Credential.Source != "env" ||
		res.Credential.LastVerifyStatus == nil || *res.Credential.LastVerifyStatus != "ok" {
		t.Fatalf("env verify: %+v", res)
	}
	if got := r.list()["openrouter"]; got.LastVerifyStatus == nil || *got.LastVerifyStatus != "ok" || got.LastVerifiedAt == nil {
		t.Errorf("the list forgot the env key's check: %+v", got)
	}
	// One check per key name every 10 s (D40). The window is fixed, so of two quick retries at
	// least one falls in the same window as the check before it.
	limited := false
	for range 2 {
		if code, _ := r.call(http.MethodPost, "/api/credentials/openrouter/verify", credAdminKey, nil); code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("checking the same key again at once was not rate limited")
	}
	rows, err := r.pg.ListCredentials(context.Background())
	if err != nil || len(rows) != 0 {
		t.Errorf("env keys must never be stored: %d rows (%v)", len(rows), err)
	}
	r.assertNoLeak(envKey)
	r.assertNoLeak(typesafeEnvKey)
}

// TestCredentialWriteValidation: what a write refuses before anything is checked or stored.
func TestCredentialWriteValidation(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	cases := []struct {
		name, path string
		body       any
		code       int
		errCode    string
		field      string
	}{
		{"unknown name", "/api/credentials/github", map[string]any{"provider": "generic", "secret": "x"}, 404, codeNotFound, ""},
		{"provider mismatch", "/api/credentials/openrouter", map[string]any{"provider": "typesafe", "secret": credGoodKey}, 422, codeValidationFailed, "provider"},
		{"no secret", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": ""}, 422, codeValidationFailed, "secret"},
		{"only whitespace", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": " \n\t "}, 422, codeValidationFailed, "secret"},
		{"a space inside the key", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": "sk-or-v1-e2e good-0123456789"}, 422, codeValidationFailed, "secret"},
		{"a control character inside the key", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": "sk-or-v1-e2e-good\r\nX-Evil: 1"}, 422, codeValidationFailed, "secret"},
		{"vendor with base_url", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "base_url": "https://or.example.com"}, 422, codeValidationFailed, "base_url"},
		{"compatible without base_url", "/api/credentials/compatible", map[string]any{"provider": "generic", "secret": "sk-test-compat-0123456789"}, 422, codeValidationFailed, "base_url"},
		{"plain http to a remote host", "/api/credentials/compatible", map[string]any{"provider": "generic", "secret": "sk-test-compat-0123456789", "base_url": "http://gw.example.com"}, 422, codeValidationFailed, "base_url"},
		{"metadata target", "/api/credentials/upstream-default", map[string]any{"provider": "generic", "secret": "sk-test-up-0123456789", "base_url": "https://169.254.169.254"}, 422, codeValidationFailed, "base_url"},
		{"bind without a check", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "verify": false, "bind": []string{"decision_model"}}, 422, codeValidationFailed, "bind"},
		{"judge on a compatible key", "/api/credentials/compatible", map[string]any{"provider": "generic", "secret": "sk-test-compat-0123456789", "base_url": r.base, "bind": []string{"judge"}}, 422, codeValidationFailed, "bind"},
		{"upstream target", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"upstream_default"}}, 422, codeValidationFailed, "bind"},
		{"unknown field", "/api/credentials/openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "label": "x"}, 400, codeInvalidJSON, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, b := r.call(http.MethodPut, tc.path, credAdminKey, tc.body)
			if code != tc.code || errorCodeOf(t, b) != tc.errCode {
				t.Fatalf("%d %s, want %d %s", code, b, tc.code, tc.errCode)
			}
			if tc.field != "" && !bytes.Contains(b, []byte(`"path":"`+tc.field+`"`)) {
				t.Errorf("no field error on %s: %s", tc.field, b)
			}
		})
	}
	if code, b := r.call(http.MethodPost, "/api/credentials/typesafe/verify", credAdminKey, nil); code != http.StatusNotFound {
		t.Errorf("verify of a key nobody stored: %d %s", code, b)
	}
	if code, b := r.call(http.MethodDelete, "/api/credentials/openai", credAdminKey, nil); code != http.StatusNotFound {
		t.Errorf("delete of a key nobody stored: %d %s", code, b)
	}
	if code, _ := r.call(http.MethodGet, "/api/credentials", testKey, nil); code != http.StatusForbidden {
		t.Errorf("a non-admin key lists credentials: %d", code)
	}
	if code, _ := r.call(http.MethodPut, "/api/credentials/openrouter", testKey, map[string]any{"provider": "openrouter", "secret": credGoodKey}); code != http.StatusForbidden {
		t.Errorf("a non-admin key writes a credential: %d", code)
	}
	if rows, err := r.pg.ListCredentials(context.Background()); err != nil || len(rows) != 0 {
		t.Errorf("refused writes stored %d rows (%v)", len(rows), err)
	}
	if len(r.mu.Requests()) != 0 {
		t.Errorf("refused writes reached the vendor %d times", len(r.mu.Requests()))
	}
}

// TestCredentialUnmatchedSecretNeverLeaks (D42, §0 rule 8): a key that matches no redaction
// pattern is masked everywhere once received: responses, the list, the audit log, WS and the
// log; the database holds only its ciphertext. An upstream key is stored without a call.
func TestCredentialUnmatchedSecretNeverLeaks(t *testing.T) {
	if config.NewRedactor().Redact("key "+noPatternSecret) != "key "+noPatternSecret {
		t.Fatal("the unmatched secret matches a redaction pattern; pick another")
	}
	r := newCredRig(t, credRigOpts{})
	res := r.put("compatible", map[string]any{"provider": "generic", "secret": noPatternSecret, "base_url": r.base + "/v1", "verify": false})
	if !res.Stored || res.Verification != nil || res.Credential.Masked != config.MaskSecret(noPatternSecret) ||
		!slices.Equal(res.Credential.AllowedHosts, []string{originOfURLForTest(t, r.base)}) {
		t.Fatalf("compatible write: %+v", res)
	}
	up := r.put("upstream-default", map[string]any{"provider": "generic", "secret": "sk-test-upstream-" + noPatternSecret, "base_url": "https://llm.example.com/v1"})
	if !up.Stored || up.Verification == nil || up.Verification.Status != "unknown" || up.Verification.Hint == nil ||
		*up.Verification.Hint != credentials.HintNotChecked || up.Verification.LatencyMs != nil || up.Credential.LastVerifyStatus != nil {
		t.Errorf("an upstream key is stored without a check: %+v", up)
	}
	r.env.srv.log.Info("echo", "value", "prefix-"+noPatternSecret)
	_ = r.list()
	_, _ = r.auditActions()
	_ = r.wsEvents("credentials_changed")
	rec, err := r.pg.GetCredential(context.Background(), "compatible")
	if err != nil || bytes.Contains(rec.Ciphertext, []byte(noPatternSecret)) {
		t.Errorf("stored row (%v): the secret is not sealed", err)
	}
	if len(r.mu.Requests()) != 0 {
		t.Errorf("verify:false and upstream keys must not call out: %d requests", len(r.mu.Requests()))
	}
	r.assertNoLeak(noPatternSecret)
}

// TestCredentialStoreRefusals (D31, D32): without a database, or without a durable master key,
// writes answer 503; env keys still list.
func TestCredentialStoreRefusals(t *testing.T) {
	put := map[string]any{"provider": "openrouter", "secret": credGoodKey}
	env := newTestEnv(t, envOpts{adminKey: credAdminKey})
	nodb := &credRig{t: t, env: env}
	if code, b := nodb.call(http.MethodPut, "/api/credentials/openrouter", credAdminKey, put); code != http.StatusServiceUnavailable || errorCodeOf(t, b) != codePersistenceDisabled {
		t.Errorf("no database: %d %s", code, b)
	}
	if code, b := nodb.call(http.MethodDelete, "/api/credentials/openrouter", credAdminKey, nil); code != http.StatusServiceUnavailable {
		t.Errorf("no database delete: %d %s", code, b)
	}
	if code, b := nodb.call(http.MethodGet, "/api/credentials", credAdminKey, nil); code != http.StatusOK {
		t.Errorf("no database list: %d %s", code, b)
	}

	r := newCredRig(t, credRigOpts{ephemeral: true})
	if code, b := r.call(http.MethodPut, "/api/credentials/openrouter", credAdminKey, put); code != http.StatusServiceUnavailable || errorCodeOf(t, b) != codeCredentialsUnavailable {
		t.Errorf("ephemeral master key: %d %s", code, b)
	}
	if len(r.mu.Requests()) != 0 {
		t.Error("a refused write called the vendor")
	}
}

// TestCredentialBindJudgePushesToEngine (D34, D75): binding the judge pushes the engine a runtime
// configuration with that judge and only its credential.
func TestCredentialBindJudgePushesToEngine(t *testing.T) {
	eng := newReportingEngine(t)
	r := newCredRig(t, credRigOpts{engine: eng.eng})
	boot := len(eng.waitPushes(t, 1))
	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"judge"}})
	if !slices.Equal(res.Bindings.Applied, []string{"judge"}) {
		t.Fatalf("bind judge: %+v", res.Bindings)
	}
	pushes := eng.waitPushes(t, boot+1)
	p := pushes[len(pushes)-1]
	cred, ok := p.GetCredentials()["openrouter"]
	if p.GetJudge().GetProvider() != "openrouter" || p.GetJudge().GetCredential() != "openrouter" || len(p.GetCredentials()) != 1 || !ok ||
		cred.GetSecret() != credGoodKey || p.GetVersion() != r.env.srv.rt.manager.Current().Version {
		t.Errorf("push after binding the judge: judge %v, %d credentials, version %d", p.GetJudge(), len(p.GetCredentials()), p.GetVersion())
	}
}

// anyStrings converts a decoded JSON array of strings.
func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// originOfURLForTest is the canonical origin of raw.
func originOfURLForTest(t *testing.T, raw string) string {
	t.Helper()
	o, err := credentials.Canonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
