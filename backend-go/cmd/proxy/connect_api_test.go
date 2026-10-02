package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/credentials"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

const (
	schemaConnectStart    = "api.v1.schema.json#/$defs/ConnectStartResponse"
	schemaConnectComplete = "api.v1.schema.json#/$defs/ConnectCompleteResponse"
	// connectOrigin is a dashboard origin other than TRUTHTABLE_PUBLIC_URL that the rig allows.
	connectOrigin = "http://127.0.0.1:3000"
	// credSecondAdminKey is another admin (another actor, D38).
	credSecondAdminKey = "cred-test-admin-key-0002"
)

// connectStartResult is ConnectStartResponse.
type connectStartResult struct {
	AuthURL   string `json:"auth_url"`
	Nonce     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
}

// start runs #58 with key from origin ("" sends no Origin) and returns the response.
func (r *credRig) connectStart(key, origin string, body any) connectStartResult {
	r.t.Helper()
	var headers []string
	if origin != "" {
		headers = []string{"Origin", origin}
	}
	code, b := r.call(http.MethodPost, "/api/connect/openrouter/start", key, body, headers...)
	if code != http.StatusOK {
		r.t.Fatalf("connect start: %d %s", code, b)
	}
	contractstest.ValidateJSON(r.t, schemaConnectStart, b)
	var s connectStartResult
	if err := json.Unmarshal(b, &s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// authorize plays the browser at OpenRouter's /auth (mock-upstream): it follows auth_url, with
// extra query parameters (e.g. mock_key=nocredits), up to the redirect to the dashboard callback,
// and returns that callback URL.
func (r *credRig) authorize(authURL string, extra url.Values) *url.URL {
	r.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		r.t.Fatal(err)
	}
	q := u.Query()
	for k, v := range extra {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(u.String())
	if err != nil {
		r.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		r.t.Fatalf("/auth answered %d, want a redirect to the callback", resp.StatusCode)
	}
	cb, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		r.t.Fatal(err)
	}
	return cb
}

// connectComplete runs #59 and returns the status and the body.
func (r *credRig) connectComplete(key, origin string, body any) (int, []byte) {
	r.t.Helper()
	var headers []string
	if origin != "" {
		headers = []string{"Origin", origin}
	}
	return r.call(http.MethodPost, "/api/connect/openrouter/complete", key, body, headers...)
}

// TestConnectOpenRouterEndToEnd (SPEC §7.5, D38): the whole self-hosted sign-in in process, with
// mock-upstream as OpenRouter. Start from http://127.0.0.1:3000 (an allowed origin other than
// the public URL), sign in at /auth, complete with the code: the key is exchanged on the server,
// checked, stored and bound to the fast check and the judge; the next gated request decides
// through openrouter-decisions with that key, the engine is pushed the judge, and the key never
// reaches a response, a WS frame or the log. The nonce works once.
func TestConnectOpenRouterEndToEnd(t *testing.T) {
	eng := newReportingEngine(t)
	r := newCredRig(t, credRigOpts{engine: eng.eng})
	boot := len(eng.waitPushes(t, 1))

	st := r.connectStart(credAdminKey, connectOrigin, map[string]any{"return_to": "/app/settings/connections"})
	if st.ExpiresIn != 600 || st.Nonce == "" {
		t.Errorf("start: %+v", st)
	}
	au, err := url.Parse(st.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	q := au.Query()
	if au.Scheme+"://"+au.Host != originOfURLForTest(t, r.base) || au.Path != "/auth" ||
		q.Get("callback_url") != connectOrigin+"/connect/callback" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("auth_url = %s", st.AuthURL)
	}

	cb := r.authorize(st.AuthURL, nil)
	if cb.Scheme+"://"+cb.Host+cb.Path != connectOrigin+"/connect/callback" || cb.Query().Get("code") == "" {
		t.Fatalf("OpenRouter returned to %s", cb.Redacted())
	}
	code, b := r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": cb.Query().Get("code"), "nonce": st.Nonce})
	if code != http.StatusOK {
		t.Fatalf("complete: %d %s", code, b)
	}
	contractstest.ValidateJSON(t, schemaConnectComplete, b)
	res := decodeConnectComplete(t, b)
	if !res.Stored || res.Verification == nil || !res.Verification.OK || res.ReturnTo != "/app/settings/connections" ||
		!slices.Equal(res.Bindings.Applied, []string{"decision_model", "judge"}) || len(res.Bindings.Skipped) != 0 {
		t.Fatalf("complete result: %s", b)
	}
	if v := res.Verification; v.CostUSD == nil || v.CostSource == nil || *v.CostSource != "reported" || v.LatencyMs == nil || v.KeyLabel == nil {
		t.Errorf("the probe's cost, latency and key label: %s", b)
	}
	fixture, err := os.ReadFile(config.RepoPath("contracts/fixtures/api/ConnectCompleteResponse.ok.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonShape(t, b), jsonShape(t, fixture); !slices.Equal(got, want) {
		t.Errorf("response fields %v, fixture fields %v", got, want)
	}
	if res.Credential.Name != "openrouter" || res.Credential.Source != "dashboard" || !strings.HasPrefix(res.Credential.Masked, "sk-") ||
		!slices.Equal(res.Credential.UsedBy, []string{"decision_model", "judge"}) {
		t.Errorf("credential: %+v", res.Credential)
	}

	// Ready: the next gated request decides through openrouter-decisions with the exchanged key.
	before := len(r.decisionCalls())
	if resp := r.gated(loadDemoScenarios(t)["grounded-pass"]); resp.StatusCode != http.StatusOK {
		t.Fatalf("gated request: HTTP %d", resp.StatusCode)
	}
	calls := r.decisionCalls()
	if len(calls) <= before || !strings.HasPrefix(calls[len(calls)-1], "Bearer sk-or-v1-mock-") {
		t.Fatal("the gated request did not decide through the exchanged key")
	}
	key := strings.TrimPrefix(calls[len(calls)-1], "Bearer ")
	var status struct {
		DecisionModel struct {
			Backend string `json:"backend"`
			State   string `json:"state"`
		} `json:"decision_model"`
	}
	if code, b := r.call(http.MethodGet, "/api/status", credAdminKey, nil); code != http.StatusOK || json.Unmarshal(b, &status) != nil {
		t.Fatalf("/api/status: %d", code)
	}
	if status.DecisionModel.Backend != jev.BackendOpenRouterDecisions || status.DecisionModel.State != "ready" {
		t.Errorf("decision model after connect: %+v, want openrouter-decisions ready", status.DecisionModel)
	}

	// The engine got the judge and only its credential (D34).
	pushes := eng.waitPushes(t, boot+1)
	p := pushes[len(pushes)-1]
	if p.GetJudge().GetProvider() != "openrouter" || len(p.GetCredentials()) != 1 || p.GetCredentials()["openrouter"].GetSecret() != key {
		t.Errorf("push after connect: judge %v, %d credentials", p.GetJudge(), len(p.GetCredentials()))
	}
	actions, _ := r.auditActions()
	if len(actions) < 2 || actions[0] != "settings.update" || actions[1] != "credential.connect_openrouter" {
		t.Errorf("audit log = %v", actions)
	}
	changed := r.wsEvents("credentials_changed")
	if len(changed) != 1 || changed[0]["name"] != "openrouter" || changed[0]["action"] != "put" {
		t.Errorf("credentials_changed = %v", changed)
	}

	// The nonce was used up.
	code, b = r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": cb.Query().Get("code"), "nonce": st.Nonce})
	if code != http.StatusBadRequest || errorCodeOf(t, b) != codeInvalidNonce {
		t.Errorf("second complete: %d %s", code, b)
	}
	r.assertNoLeak(key)
}

// TestConnectOpenRouterNoCredits (D38, D75): a sign-in whose account has no credits stores the key
// unbound with the no_credits hint; simulated checks stay on.
func TestConnectOpenRouterNoCredits(t *testing.T) {
	r := newCredRig(t, credRigOpts{})
	st := r.connectStart(credAdminKey, connectOrigin, nil)
	cb := r.authorize(st.AuthURL, url.Values{"mock_key": {"nocredits"}})
	code, b := r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": cb.Query().Get("code"), "nonce": st.Nonce})
	if code != http.StatusOK {
		t.Fatalf("complete: %d %s", code, b)
	}
	contractstest.ValidateJSON(t, schemaConnectComplete, b)
	res := decodeConnectComplete(t, b)
	v := res.Verification
	if !res.Stored || v == nil || v.OK || v.Status != "no_credits" || v.Hint == nil || *v.Hint != credentials.HintNoCredits || v.KeyLabel == nil {
		t.Fatalf("no-credits connect: %s", b)
	}
	want := []CredentialBindSkip{{Target: "decision_model", Reason: runtimecfg.SkipVerificationFailed}, {Target: "judge", Reason: runtimecfg.SkipVerificationFailed}}
	if len(res.Bindings.Applied) != 0 || !slices.Equal(res.Bindings.Skipped, want) || res.ReturnTo != credentials.DefaultConnectReturnTo {
		t.Errorf("bindings %+v, return_to %q", res.Bindings, res.ReturnTo)
	}
	// The same fields as the contract fixture the web is built against.
	fixture, err := os.ReadFile(config.RepoPath("contracts/fixtures/api/ConnectCompleteResponse.no-credits.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := jsonShape(t, b), jsonShape(t, fixture); !slices.Equal(got, want) {
		t.Errorf("response fields %v, fixture fields %v", got, want)
	}
	snap := r.env.srv.rt.manager.Current()
	if snap.Settings.Bindings.DecisionModel != nil || snap.Settings.Bindings.Judge != nil || snap.Settings.DecisionModel.Backend != jev.BackendMock {
		t.Errorf("nothing may be bound: %+v, backend %q", snap.Settings.Bindings, snap.Settings.DecisionModel.Backend)
	}
}

// TestConnectStartRefusals (D38): the callback origin, return_to and store rules of #58.
func TestConnectStartRefusals(t *testing.T) {
	r := newCredRig(t, credRigOpts{})

	// Without an Origin the callback is TRUTHTABLE_PUBLIC_URL's origin.
	st := r.connectStart(credAdminKey, "", nil)
	au, _ := url.Parse(st.AuthURL)
	pub, _ := credentials.Canonical(r.env.srv.cfg.PublicURL)
	if au.Query().Get("callback_url") != pub+"/connect/callback" {
		t.Errorf("callback without an Origin = %q, want the public URL's", au.Query().Get("callback_url"))
	}

	code, b := r.call(http.MethodPost, "/api/connect/openrouter/start", credAdminKey, nil, "Origin", "https://evil.example")
	if code != http.StatusConflict || errorCodeOf(t, b) != codeConflict || !strings.Contains(string(b), `"public_url":"`+r.env.srv.cfg.PublicURL+`"`) {
		t.Errorf("an Origin the gateway does not allow: %d %s", code, b)
	}

	for _, bad := range []string{
		"https://evil.example/app/", "//evil.example/app/", "/app/../admin", "/app//evil", `/app/\evil`, "/app/%2fevil",
		"/app/%5cevil", "/app/%2e%2e/x", "/settings", "/app/x\u0000y", "app/settings", "/app/‮evil",
	} {
		code, b := r.call(http.MethodPost, "/api/connect/openrouter/start", credAdminKey, map[string]any{"return_to": bad}, "Origin", connectOrigin)
		if code != http.StatusUnprocessableEntity || errorCodeOf(t, b) != codeValidationFailed {
			t.Errorf("return_to %q: %d %s, want 422", bad, code, b)
		}
	}
	if code, _ := r.call(http.MethodPost, "/api/connect/openrouter/start", testKey, nil); code != http.StatusForbidden {
		t.Errorf("a non-admin key starts a sign-in: %d", code)
	}

	envKey := newCredRig(t, credRigOpts{mutate: func(c *config.Config) { c.Jev.OpenRouterKey = "sk-or-v1-e2e-envkey-0a1b2c3d4e" }})
	if code, b := envKey.call(http.MethodPost, "/api/connect/openrouter/start", credAdminKey, nil); code != http.StatusConflict || errorCodeOf(t, b) != codeEnvManaged {
		t.Errorf("OPENROUTER_API_KEY set: %d %s", code, b)
	}
	ephemeral := newCredRig(t, credRigOpts{ephemeral: true})
	if code, b := ephemeral.call(http.MethodPost, "/api/connect/openrouter/start", credAdminKey, nil); code != http.StatusServiceUnavailable || errorCodeOf(t, b) != codeCredentialsUnavailable {
		t.Errorf("store not ready: %d %s", code, b)
	}
}

// TestConnectCompleteRefusals (D38): an unknown nonce, another origin, another admin, a bind
// outside {decision_model, judge} and a refused code exchange; each known nonce is used up by its
// first call, whatever the outcome.
func TestConnectCompleteRefusals(t *testing.T) {
	r := newCredRig(t, credRigOpts{mutate: func(c *config.Config) {
		c.Auth.AdminKeys = append(slices.Clone(c.Auth.AdminKeys), config.APIKey{Label: "admin-two", Key: credSecondAdminKey})
	}})
	invalid := func(what string, code int, b []byte) {
		t.Helper()
		if code != http.StatusBadRequest || errorCodeOf(t, b) != codeInvalidNonce {
			t.Errorf("%s: %d %s, want 400 invalid_nonce", what, code, b)
		}
	}
	code, b := r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": "mockcode_x", "nonce": "never-issued"})
	invalid("unknown nonce", code, b)

	// Another origin than the sign-in returned to burns the nonce.
	st := r.connectStart(credAdminKey, connectOrigin, nil)
	cb := r.authorize(st.AuthURL, nil)
	codeParam := cb.Query().Get("code")
	code, b = r.connectComplete(credAdminKey, "http://localhost:3000", map[string]any{"code": codeParam, "nonce": st.Nonce})
	invalid("another origin", code, b)
	code, b = r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": codeParam, "nonce": st.Nonce})
	invalid("the same nonce after a refusal", code, b)

	// Another admin cannot complete it.
	st = r.connectStart(credAdminKey, connectOrigin, nil)
	code, b = r.connectComplete(credSecondAdminKey, connectOrigin, map[string]any{"code": "mockcode_y", "nonce": st.Nonce})
	invalid("another admin", code, b)

	// A bind outside the allowed set is 422, and the nonce is gone.
	st = r.connectStart(credAdminKey, connectOrigin, nil)
	code, b = r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": "mockcode_z", "nonce": st.Nonce, "bind": []string{"upstream_default"}})
	if code != http.StatusUnprocessableEntity || errorCodeOf(t, b) != codeValidationFailed {
		t.Errorf("bind outside the set: %d %s", code, b)
	}
	code, b = r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": "mockcode_z", "nonce": st.Nonce})
	invalid("the nonce after a 422", code, b)

	// OpenRouter refusing the code is 502 upstream_error with its status.
	st = r.connectStart(credAdminKey, connectOrigin, nil)
	code, b = r.connectComplete(credAdminKey, connectOrigin, map[string]any{"code": "mockcode_not-issued", "nonce": st.Nonce})
	if code != http.StatusBadGateway || errorCodeOf(t, b) != "upstream_error" || !strings.Contains(string(b), `"status":400`) {
		t.Errorf("a refused exchange: %d %s", code, b)
	}
	if rows, err := r.pg.ListCredentials(t.Context()); err != nil || len(rows) != 0 {
		t.Errorf("refused sign-ins stored %d keys (%v)", len(rows), err)
	}
}

// decodeConnectComplete decodes a ConnectCompleteResponse.
func decodeConnectComplete(t *testing.T, b []byte) credWriteWire {
	t.Helper()
	var w credWriteWire
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w
}

// jsonShape lists the dotted field paths of a JSON object (arrays by their first element).
func jsonShape(t *testing.T, b []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				out = append(out, prefix+k)
				walk(prefix+k+".", c)
			}
		case []any:
			if len(x) > 0 {
				walk(prefix+"[].", x[0])
			}
		}
	}
	walk("", v)
	slices.Sort(out)
	return out
}
